# Shell Command Policy Reference

`shell_execute` can run in **jailed mode**: commands run in a pure-Go shell interpreter in a child process, where every program launch, file the shell opens and directory a glob lists is checked against a policy on its real, expanded arguments. The `command-policy` admission hook decides, before a command runs, whether it may run without a person's approval. It parses the command, finds every program, builtin, redirect and path it names, and judges each against a named policy:

- **Allow:** every part is within the policy.
- **Ask:** some literal part needs approval. The approval card shows the reason, and approving runs the call with a grant naming exactly what was flagged.
- **Deny:** the command is refused outright.

The hook decides what starts. It does not contain what a started program does.

| Status | Piece |
|---|---|
| ✅ | `pkg/shellpolicy`: the policy model, the built-in `readonly` policy, the static analyzer (`Policy.Analyze`), the per-launch checks (`CheckLaunch`, `CheckBuiltin`, `CheckOpen`, `CheckListDir`) and the hook factory (`shellpolicy.Factory`). |
| ✅ | The `command-policy` hook kind in `pkg/shuttle` (`HookBinding.Policy`, `HookBinding.Enforcement`, `ChainDeps.CommandPolicy`). |
| ✅ | `pkg/shelljail`: the jailed runner, and `tools.shell_execute.mode: jailed` in `looms serve` (see [Jailed mode](#jailed-mode)). |
| ✅ | Linux and macOS. On Windows, jailed mode refuses to start. |
| ✅ | Lua scripts calling `shell_execute`: only when it runs jailed under a `command-policy` binding, and never from shared scripts (see [lua-script-api.md](lua-script-api.md)). |

Design: Teradata-PE/avmo-tera-cloud `docs/design/lua-script-tool/06-shell-command-policy.md`.

## Binding

```yaml
tools:
  hooks:
    - kind: command-policy
      scope: shell_execute        # any tool whose "command" param is a shell string
      policy: readonly            # required: a policy the host registered
      enforcement: runtime        # runtime (default) | static
```

| Field | Required | Meaning |
|---|---|---|
| `kind` | yes | `command-policy` |
| `scope` | yes | Tool selector, with the same semantics as every other binding |
| `matcher` | no | Narrows the calls it governs, with the same semantics as every other binding |
| `policy` | yes | Name of a policy passed to `shellpolicy.Factory` |
| `enforcement` | no | `runtime`: a runner re-checks every launch, so a program named by a run-time expansion is left to the runner. `static`: nothing re-checks, so everything the analyzer cannot see is denied (details below). |

Validation, at save time and at build:

| Condition | Result |
|---|---|
| `policy` missing | error |
| `enforcement` not `runtime` or `static` | error |
| `policy` or `enforcement` set on any other kind | error |
| a `command-policy` binding but no `ChainDeps.CommandPolicy` | build error |
| the binding names a policy the factory does not know | build error, listing the known names |

## How a command is judged

The command is parsed as bash with `mvdan.cc/sh/v3/syntax` (BSD-3-Clause). Every simple command is judged, including those inside `$(…)`, backticks, `<(…)`, functions, loops, conditionals, here-documents, arithmetic and assignments. The literal argument of `eval` and the action of `trap` are parsed and judged too, up to 4 levels deep. Before judging a command name, the analyzer unwraps the prefixes `command`, `exec` and `builtin`.

A word is **literal** when it has no parameter, command, arithmetic or process expansion, no glob, brace or tilde expansion, and no `$'…'` or `$"…"` quoting. Quotes and backslashes are removed first, so `\rm`, `r''m` and `"rm"` are all `rm`.

| The command name is… | Verdict |
|---|---|
| a function declared in the same command | no verdict (its body is judged) |
| a builtin on the policy's builtin list | Allow, with special handling: the literal argument of `eval` and the action of `trap` are parsed and judged, and a `cd` or `pushd` target is path-checked |
| a builtin not on the list (`alias`, `unalias`, `shopt`, `hash`, `source`, `.`, `help`) | Ask, granting that builtin. **Deny** for `source`, `.`, `alias` and `shopt` under `static` |
| a builtin the interpreter does not implement (`kill`, `umask`, `ulimit`, `fc`, `jobs`, `bg`, `fg`, `disown`, `enable`, `history`, …) | Deny |
| on the policy's `Never` list (by base name) | Deny |
| a literal path (`./x`, `/usr/bin/rm`) | Ask, granting that exact path |
| an allowlisted program, all literal arguments within its profile | Allow |
| an allowlisted program, a literal argument outside its profile | Ask, granting the program (any arguments, for this call) |
| any other literal name | Ask, granting the name |
| computed at run time | `runtime`: no verdict (the runner checks it, and it can never be approved). `static`: Deny |

| Paths and redirects | Verdict |
|---|---|
| an existing literal path argument outside the read roots | Ask, granting the resolved path |
| the literal directory part of a glob outside the read roots (`/etc` in `/etc/pass*`) | Ask, granting the resolved path |
| a literal `cd` target outside the read roots | Ask, granting the resolved path |
| a write redirect (`>`, `>>`, `>\|`, `&>`, `&>>`, `<>`, `>& file`) to a literal path outside the write roots | Deny |
| a write redirect to a computed path | `static` only: Deny |
| a read redirect (`<`, `<& file`) from outside the read roots | Ask, granting the resolved path |
| `/dev/null`, `/dev/stdin`, `/dev/stdout`, `/dev/stderr` | always allowed |
| redirects on file descriptors above 2, `coproc`, bats `@test` | Deny (the interpreter does not support them) |
| a command that does not parse as bash | Deny, with the parser's message |

Relative paths resolve against the call's working directory. Paths are compared after symlinks are resolved, by whole path components.

The hook additionally turns an **Ask into a Deny** when the call's JSON-encoded params exceed `shuttle.ApprovalParamsMaxBytes` (8,192 bytes). An approval card cuts params past that size, and a person is never asked to approve text they cannot see. It also denies a call with no `command` string.

## Reasons

```
needs approval under shell policy readonly: rm (1:1): not on the shell allowlist; npm (1:10):
not on the shell allowlist; /etc/hosts (2:1): reads outside the session. Everything else in
the command is allowed.

shell policy readonly refuses this command: sudo (1:1): never allowed by the shell policy
```

A Deny's reason lists only what is refused. A finding inside a literal `eval` or `trap` argument is reported at the `eval` or `trap`.

## The grant

On Ask, `Decision.Grant` is a `*shellpolicy.Grant`:

```go
type Grant struct {
    Programs  []string // names or exact paths that may run, with any arguments, for this call
    Builtins  []string
    ReadPaths []string // resolved paths that may be read, with everything beneath them
}
```

When the Ask is approved, the tool body reads the grants with `shellpolicy.MergeGrants(shuttle.AdmissionGrantsFromContext(ctx))`. A call allowed outright carries none.

## The `readonly` policy

`shellpolicy.Readonly()`. A program qualifies when it cannot start another program, cannot write a file except through an argument its profile refuses, and is run often.

**Builtins:** `echo printf test [ cd pwd read true false : set shift exit return break continue wait type getopts pushd popd dirs mapfile readarray eval trap unset`. Assignments (`export`, `local`, `declare`, `readonly`, `typeset`, `let`) are not calls and need no entry.

| Program | Argument rules |
|---|---|
| `cat head tail wc cut comm cmp diff od ls stat du realpath md5sum sha1sum sha256sum shasum md5 grep egrep fgrep jq` | path confinement |
| `tr basename dirname seq sleep uname whoami id which` | none (arguments are not files) |
| `sort` | refuses `-o`, `-T`, `--output`, `--compress-program`, `--temporary-directory` |
| `uniq` | at most one positional argument (`-f`, `-s`, `-w` take a value) |
| `find` | refuses `-exec`, `-execdir`, `-ok`, `-okdir`, `-delete`, `-fprint`, `-fprint0`, `-fprintf`, `-fls` |
| `file` | refuses `-C`, `--compile` |
| `base64` | refuses `-o`, `--output` |
| `date` | refuses `-s`, `--set`, and a positional time such as `202601010000` |
| `git` | the git profile below |

**Never:** `sudo su doas pkexec`.

**Option matching.** Short options are matched inside groups, so `-ro` contains `-o`. Long options are matched with or without `=value`, and by abbreviation, so `--out` matches `--output`. Arguments after `--` are positional.

**Path confinement.** Every positional argument, and every `--option=value` value, that names an existing path must lie within the read roots or a granted path.

**git profile.**
- **Subcommands:** `status diff log show rev-parse ls-files blame grep describe shortlog`, and `branch` with only `--list -a --all -r --remotes -v -vv --verbose --show-current`.
- **Global options:** only `--no-pager`, `-P`, `--no-optional-locks` and `-C <dir>`. This is what refuses `-c`.
- **Refused anywhere:** `--config-env --exec-path --attr-source --git-dir --work-tree --namespace --super-prefix --output --ext-diff --textconv -O --open-files-in-pager --paginate`.
- **Hardening.** `Profile.Harden` prepends the following at run time, and adds `--no-ext-diff --no-textconv` to `diff`, `log` and `show`:

  ```
  -c core.fsmonitor=false -c core.pager=cat -c core.hooksPath=/dev/null -c diff.external= --no-pager --attr-source=4b825dc642cb6eb9a060e54bf8d69288fbee4904
  ```

- **Environment.** `Profile.Env` sets `GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ATTR_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0`.

With the hardening applied, git 2.50.1 ran no program from the repository's own config (fsmonitor, pager, external diff, clean filter, textconv). The measurement is design Probe 9.

`Policy.Extend(name, allow, never)` derives a policy. Added programs get path confinement only. A program on both lists is never allowed. `Policy.Validate` rejects a policy with no name, an allowlisted name containing `/`, or a name that is both allowed and never allowed.

## Jailed mode

```yaml
tools:
  shell_execute:
    mode: jailed              # bash (default) | jailed
    policy: readonly          # the policy every jailed command runs under (default readonly)
    policies:                 # optional named policies
      dev:
        extends: readonly     # default readonly
        allow: [make, go]     # path confinement only
        never: [curl]         # never wins over allow, at any level
    search_path: [/usr/local/bin, /usr/bin, /bin]   # default
    memory_bytes: 536870912   # default 512 MiB
    file_bytes: 268435456     # default 256 MiB
    strict: true              # set -u (default true)
    read_roots: []            # added to LOOM_DATA_DIR and /tmp
    write_roots: []           # added to the session's scratchpad and artifact directories
  hooks:
    - kind: command-policy    # optional; without it, off-list programs are blocked and never approvable
      scope: shell_execute
      policy: readonly        # must equal tools.shell_execute.policy
```

**Startup** (`serve`). The following abort startup:

- an unknown `mode`;
- a `policies` entry that is undefined, loops, redefines `readonly`, or fails `Validate`;
- a `command-policy` binding on `shell_execute` while `mode` is `bash`, because nothing would enforce what the hook approves;
- a binding naming a different policy than `tools.shell_execute.policy`;
- a failed self-test.

In jailed mode, `serve` also:

1. **Pins programs.** It resolves every allowlisted program once on `search_path` and logs each one it cannot find. A missing program fails with exit 127.
2. **Checks git.** It drops git when git does not accept `--attr-source`, so that git calls need approval rather than running without the hardening.
3. **Runs a self-test.** It runs `echo` through the jail. A binary that does not call `shelljail.Main()` first in `main()` fails this check.

**A call**:

| | Jailed mode |
|---|---|
| Interpreter | `mvdan.cc/sh/v3/interp`, bash dialect, in a child process (the host binary re-executed with `LOOM_SHELLJAIL_CHILD=1`). The command spec goes on stdin, never argv. |
| Session | Required. Without one, the result is `JAIL_UNAVAILABLE`. |
| Working directory | The session scratchpad. A relative `working_dir` resolves against it. |
| `shell` param | Only `default`, `bash` or `sh`. Anything else is `INVALID_PARAMS`. |
| Program launch | `CheckLaunch` on the expanded argv and current directory. A bare name runs only from its pinned path, and a name containing `/` only when granted. A refusal fails that one command with exit 126; the shell continues, and `set -e` stops it. |
| Builtins, redirects, globs | `CheckBuiltin`, `CheckOpen` and `CheckListDir`. |
| What a person approved | The call's admission grants (`shellpolicy.MergeGrants(shuttle.AdmissionGrantsFromContext(ctx))`). |
| Environment | Built from scratch. No server variable reaches a command. The interpreter starts with `PATH` (`search_path`), `HOME` (scratchpad), `TERM=dumb`, `NO_COLOR=1`, `LANG`/`LC_ALL`/`TZ` from the server, `LOOM_DATA_DIR`, `SESSION_ID`, `SESSION_ARTIFACT_DIR`, `SESSION_SCRATCHPAD_DIR`, and the call's `env`. Each program gets the shell's exported variables with `PATH` forced to `search_path`. Credential names (as in bash mode) and names that change what a program executes (`LD_*`, `DYLD_*`, `GIT_*`, `PYTHON*`, `BASH_FUNC_*`, `PAGER`, `EDITOR`, `VISUAL`, `BASH_ENV`, `ENV`, `LESSOPEN`, `NODE_OPTIONS`, `PERL5OPT`, `RUBYOPT`, …) are dropped and reported. |
| Limits | Linux: `RLIMIT_DATA` (`memory_bytes`), `RLIMIT_FSIZE` (`file_bytes`), `RLIMIT_CPU` (timeout + 5 s) and `RLIMIT_CORE` = 0, all inherited by every program, plus `oom_score_adj` = 1000. Every OS: a heap watchdog that stops the interpreter at a third of `memory_bytes`, with exit 137 and `limit: memory`. macOS rejects `RLIMIT_DATA`, so there the watchdog is the only memory bound. |
| End of call | The child's whole process group is killed, so a jailed command leaves nothing running, background jobs included. |
| Unsupported | `coproc`, redirects on descriptors above 2, `kill`, `umask`, `ulimit` and other builtins the interpreter does not implement. `printf -v` is not supported: it prints `-v` and sets nothing. `$!` names an interpreter job (`g1`), not a process ID. |

**Result.** `Data` gains a `jail` block:

```json
"jail": {
  "blocked": [{"name": "rm", "reason": "not on the shell allowlist and not approved for this call", "pos": "1:7"}],
  "env_dropped": ["GH_TOKEN", "LD_PRELOAD"],
  "limit": ""
}
```

`limit` is `memory`, `timeout`, or empty. When anything was blocked, the error message lists it, and the suggestion tells the model to write the program out literally so it can be approved. A program computed at run time can never be approved.

## Run-time checks

These are for a runner that executes the command and sees each launch with its expanded arguments.

| Call | Judges |
|---|---|
| `CheckLaunch(argv, dir, roots, grant)` | one program launch: the never list, the current directory, the grant, the allowlist, the profile, and path confinement |
| `CheckBuiltin(name, grant)` | a builtin |
| `CheckOpen(path, dir, write, roots, grant)` | a file the shell opens (a redirect or `source`). A read grant never covers a write. |
| `CheckListDir(dir, roots, grant)` | a directory listed to expand a glob |

Each returns a `*shellpolicy.LaunchError{Name, Reason}`, or nil.

## Tests

- `pkg/shelljail` (87.8% coverage):
  - the corpus, through a real child and in-process under `-race`;
  - git runs nothing from repository config;
  - a timeout kills background programs;
  - the memory bomb is stopped (on Linux, by `RLIMIT_DATA`, in 0.05 s);
  - policy and jail agree on fully literal commands;
  - the self-test catches a binary that is not a jail.
- `pkg/shuttle/builtin`: the full hook → approval → jail path through the executor.
- `cmd/looms`: config decoding, validation and `serve` setup.

- `pkg/shellpolicy`: 94.7% statement coverage.
- **Corpus:** the design's bypass corpus, about 80 commands, each run under both enforcement modes.
- **Run-time checks:** a 22-case table for `CheckLaunch`.
- **Interpreter drift:** `TestImplementedBuiltinsMatchInterpreter` fails when an interpreter upgrade implements or drops a builtin.
- **Executor:** an end-to-end test through `BuildChainFromConfig` and the executor.
- **Fuzzing:** `FuzzAnalyze`, which also runs in the CI fuzz job.
