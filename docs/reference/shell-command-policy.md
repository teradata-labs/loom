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
| ✅ | Lua scripts calling `shell_execute`: only when it runs jailed under a `command-policy` binding with no `matcher`, and never from shared scripts (see [lua-script-api.md](lua-script-api.md)). |

Design: Teradata-PE/avmo-tera-cloud `docs/design/lua-script-tool/06-shell-command-policy.md`.

## Platform support

The analyzer judges paths as POSIX paths. On Windows, Go's `path/filepath` does not treat `/etc/x` as absolute, so a command such as `echo x > /etc/x` run with `shell: bash` would be allowed there, where Linux and macOS ask. On Windows the `command-policy` hook therefore refuses (Deny) every call it governs, whatever the `shell` parameter and the enforcement, with the reason `command-policy: the command policy judges POSIX paths and cannot judge commands on Windows`. The refusal happens per call: a `command-policy` binding still builds on Windows, so the server starts.

## Binding

```yaml
tools:
  hooks:
    - kind: command-policy
      scope: shell_execute        # any tool whose "command" param is a shell string
      policy: readonly            # required: a policy the host registered
      enforcement: static         # static (default) | runtime
```

| Field | Required | Meaning |
|---|---|---|
| `kind` | yes | `command-policy` |
| `scope` | yes | Tool selector, with the same semantics as every other binding |
| `matcher` | no | Narrows the calls it governs, with the same semantics as every other binding. A binding with a matcher does not make `shell_execute` available to Lua scripts. |
| `policy` | yes | Name of a policy passed to `shellpolicy.Factory` |
| `enforcement` | no | `static` (the default when empty): nothing re-checks, so everything the analyzer cannot see is denied (details below). `runtime`: a runner re-checks every launch, so a program named by a run-time expansion is left to the runner. The jailed `shell_execute` is that runner (see [Jailed mode](#jailed-mode)); bind it with `runtime`. On any tool that does not re-check, `runtime` leaves run-time expansions unchecked. |

Validation, at save time and at build:

| Condition | Result |
|---|---|
| `policy` missing | error |
| `enforcement` not `runtime` or `static` | error |
| `policy` or `enforcement` set on any other kind | error |
| a `command-policy` binding but no `ChainDeps.CommandPolicy` | build error |
| the binding names a policy the factory does not know | build error, listing the known names |

## How a command is judged

The command is parsed as bash with `mvdan.cc/sh/v3/syntax` (BSD-3-Clause). Every simple command is judged, including those inside `$(…)`, backticks, `<(…)`, functions, loops, conditionals, here-documents, arithmetic and assignments. The literal argument of `eval` and the action of `trap` are parsed and judged too, up to 4 levels deep. Before judging a command name, the analyzer unwraps the prefixes `command`, `exec` and `builtin`. A name after one of them is never a function call, since those prefixes skip functions.

A word is **literal** when it has no parameter, command, arithmetic or process expansion, no glob, brace or tilde expansion, and no `$'…'` or `$"…"` quoting. Quotes and backslashes are removed first, so `\rm`, `r''m` and `"rm"` are all `rm`. A `[` with no `]` after it is not a glob, so the `[` command is literal.

| The command name is… | Verdict |
|---|---|
| a function declared by a top-level statement before the call, when no `unset` in the command may remove it | no verdict (its body is judged where it is declared) |
| a builtin on the policy's builtin list | Allow, with special handling: the literal argument of `eval` and the action of `trap` are parsed and judged (after a leading `--`; `trap -p`, `-l`, `-P` and `-` run no code), and `cd`, `pushd` and `popd` are followed (see "Changing directory") |
| a builtin not on the list (`alias`, `unalias`, `shopt`, `hash`, `source`, `.`, `help`) | Ask, granting that builtin. **Deny** for `source`, `.`, `alias` and `shopt` under `static` |
| a builtin the interpreter does not implement (`kill`, `umask`, `ulimit`, `fc`, `jobs`, `bg`, `fg`, `disown`, `enable`, `history`, …) | Deny |
| `mapfile -C` or `readarray -C` (a callback run as code), `set -k` or `set -o keyword` (every `NAME=value` argument becomes an environment assignment) | Deny (the interpreter does not support them). Under `static`, a `set` option computed at run time is a Deny too |
| on the policy's `Never` list (by base name) | Deny |
| a literal path (`./x`, `/usr/bin/rm`) | Ask, granting that exact path |
| an allowlisted program whose profile needs run-time hardening (`Profile.Harden` or `Profile.Env`, such as `git`) | `static`: Deny, since no runner applies the hardening. `runtime`: judged like any allowlisted program |
| an allowlisted program, all literal arguments within its profile | Allow |
| an allowlisted program, a literal argument outside its profile | Ask, granting the program (any arguments, for this call) |
| any other literal name | Ask, granting the name |
| computed at run time | `runtime`: no verdict (the runner checks it, and it can never be approved). `static`: Deny |

| Paths and redirects | Verdict |
|---|---|
| an existing literal path argument outside the read roots | Ask, granting the resolved path |
| the literal directory part of a glob outside the read roots (`/etc` in `/etc/pass*`) | Ask, granting the resolved path |
| a glob argument, under `static`: any name it matches outside the read roots (a symlink it matches is followed) | Ask, granting each resolved path |
| a glob argument with a `..` component after a glob component (`*/../x`) | `static` only: Deny |
| a glob argument, under `static`, that matches more than 4,096 names or does not parse | Deny |
| a path argument built from `~`, a parameter (`$HOME`, `${X}`) or a command or process substitution, for a program whose arguments are paths | `runtime`: no verdict (the runner checks the expanded path). `static`: Deny |
| a literal `cd` or `pushd` target outside the read roots | Ask, granting the resolved path |
| a write redirect (`>`, `>>`, `>\|`, `&>`, `&>>`, `<>`, `>& file`) to a literal path outside the write roots | Deny |
| a write redirect to a literal path inside the write roots that lies in a git directory: a component named `.git`, or an ancestor that git takes for a repository (a `HEAD` file and `objects` and `refs` directories, such as a bare repository) | Ask in both modes, granting that exact resolved path (`Grant.WritePaths`). A file written there can define a filter in the config and name it in `info/attributes` or `core.attributesFile`, and git runs it (see the git profile) |
| a write redirect, a read redirect or a `<&`/`>&` target that is computed | `static` only: Deny |
| a read redirect (`<`, `<& file`) from outside the read roots | Ask, granting the resolved path |
| `/dev/null`, `/dev/stdin`, `/dev/stdout`, `/dev/stderr` | always allowed |
| a path under `/dev/tcp/` or `/dev/udp/` (bash opens a network connection for a redirect to one), as a redirect target or an argument | Deny in both modes; `CheckOpen` and `CheckLaunch` refuse it whatever the grant |
| redirects on file descriptors above 2, `coproc`, bats `@test` | Deny (the interpreter does not support them) |
| a command that does not parse as bash | Deny, with the parser's message |

Relative paths resolve against every directory the command can change to (see "Changing directory"). Paths are resolved the way the kernel opens them: one component at a time, so a `..` after a symlink leaves the symlink's target (`link/../x` is next to the target, not next to `link`). They are compared by whole path components. A path written to is judged at what the write reaches: a final symlink is followed even when its target does not exist yet, since `>` and `>>` create the target (PR #430 review F6: `echo x > ol`, with `ol` a symlink to a new file outside the session, created that file). So a write through such a symlink outside the write roots is a Deny, and one into a git directory is an Ask granting the target.

Paths, `cd` targets and globs are judged after the whole command is walked, so the order of the parts does not matter. Findings are reported in order of position.

### Changing directory

The analyzer collects every literal `cd` and `pushd` target in the command, wherever it is (a branch, loop, function, subshell or literal `eval`), and follows them from the working directory, from each directory reached, until no new directory turns up. For each target it follows both bash's logical result (`cd -L`) and the physical one (`cd -P`), and, for a target bash looks up in `CDPATH` (one that is not absolute and does not start with `.` or `..`), the same from each `CDPATH` entry in the process environment. Every relative path is then judged from every directory found. This set is larger than what one run reaches, so it can ask about a path that the run never reads (`cd sub; ...; cd ..` reaches the parent of the working directory too).

| Under `static`, the command… | Verdict |
|---|---|
| runs `cd` or `pushd` with no directory (`$HOME`, or a stack swap), `cd -` (`$OLDPWD`), `pushd +N` or `pushd -N`, or `popd` | Deny |
| gives `cd` or `pushd` a computed target | Deny |
| sets `CDPATH`, in any form, or through the call's `env` parameter | Deny |
| can reach more than 64 directories | Deny |

Under `runtime` the runner checks each launch against the shell's current directory, so none of these is refused there.

In both modes, a command with more than 64 literal `cd` and `pushd` targets is a Deny, and the targets past the limit are not followed. The hook runs before `shell_execute`'s own command-length limit, and the time to follow the targets grows with their number. Each directory found is followed through each target once, so the work is bounded by 64 directories times 64 targets.

### Arithmetic and subscripts (`static` only)

Bash evaluates an array subscript as arithmetic, and the command substitutions in an arithmetic subscript run. A subscript reaches bash in a variable name (`printf -v 'a[$(cmd)]' x`, `wait -p 'a[$(cmd)]'`) and in any value arithmetic reads (`x='a[$(cmd)]'; echo $(( x ))`, or `[[ $x -eq 1 ]]`). The runner sees each launch such a substitution makes, so under `runtime` none of this is refused. The jailed shell (mvdan.cc/sh v3.14.1) also refuses `wait -p` and does not prompt-expand `${name@P}`; `TestInterpreter_WaitPRefused` and `TestInterpreter_PromptExpansionRunsNothing` fail when an interpreter upgrade changes that. Under `static`:

| The command… | Verdict |
|---|---|
| names a variable with a subscript that is not an integer, `@` or `*`, in `printf -v`, `read`, `mapfile`, `getopts`, `wait -p`, `unset`, `declare` and its variants, a name reference target, `test -v`, `[ -v` or `[[ -v` | Deny |
| gives `test -v`, `[ -v` or `[[ -v` a computed name | Deny |
| uses `declare -i` (or `local`, `typeset` with `-i`): every later assignment to the name is arithmetic | Deny |
| has arithmetic (`$(( ))`, `(( ))`, `let`, a C-style `for`, `[[ … -eq … ]]` and the other comparisons, a subscript, a `${s:offset:length}` slice) that reads a variable, unless the variable is proven to hold an integer | Deny |
| has arithmetic over a command substitution, a positional parameter, or any expansion other than `$name`, `${name}`, `$#`, `$?`, `$$`, `$!` or a length `${#name}` | Deny |
| uses an indirect expansion `${!name}` (not `${!prefix*}` or `${!array[@]}`) | Deny |
| uses the prompt-expansion operator `${name@P}`: bash expands the value as a prompt string, which runs the command substitutions in it (`x='$(cmd)'; echo "${x@P}"` runs `cmd`). The other `@` operators (`@Q`, `@E`, `@A`, `@a`, `@U`, `@u`, `@L`, `@K`, `@k`) run nothing | Deny |

A variable is **proven to hold an integer** at a read when nothing in the command assigns it anything but an integer (a literal integer, an arithmetic expansion, `$#`/`$?`/`$$`/`$!` or a length), and either a top-level statement assigned it an integer before the read (a bare assignment, `declare`/`export`/`readonly`/`typeset` without `-n` or `-i`, `(( ))` or `let`; not in the background and without redirects), or the read is inside a C-style `for` whose init assigns it, or inside a `for NAME in` loop over literal integers. `read`, `mapfile`, `printf -v`, `getopts`, `select`, a name reference to it, `${NAME:=…}` with a non-integer value, and a `for NAME in` loop over anything else make the variable unproven everywhere. Variables bash sets from data (`REPLY`, `MAPFILE`, `OPTARG`, `BASH_REMATCH`, `PWD`, `OLDPWD`, `DIRSTACK`, `_`, and the `BASH_*`/`FUNCNAME` call-stack arrays) are never proven. The check does not know whether an array is associative, so a non-integer subscript of an associative array is refused too.

So `i=0; (( i++ )); echo $(( i + 1 ))`, `for ((i=0; i<3; i++)); do …; done` and `for i in 1 2 3; do echo $(( i * 2 )); done` are allowed, and `n=$(wc -l < f); echo $(( n + 1 ))` is refused.

| Variables the command sets | Verdict |
|---|---|
| a name `shellpolicy.ExecEnvName` reports (its value changes what runs or loads), set in any form: a prefix (`PATH=/x ls`), a bare assignment (`PATH=/x`), `export`/`declare`/`typeset`/`readonly`/`local`/`nameref` (with or without `-x`, and the target of `-n`), `env NAME=…`, a `for`/`select` variable, `read`, `mapfile`/`readarray`, `printf -v`, `wait -p`, `getopts`, `${NAME:=…}`/`${NAME=…}`, or arithmetic (`(( NAME = 1 ))`, `let`, `++`) | Ask in both modes, naming the variable; the grant lists it in `Env` |
| a variable whose name is computed at run time in one of those forms (`export "$n=…"`, `read "$v"`, `wait -p "$v"`, `env "$a" …`) | `runtime`: Ask. `static`: Deny |
| `env -S` or `env -P` (more assignments, or another search path) | Ask |
| an `IFS=` prefix on `read`, `mapfile` or `readarray` (it only sets that builtin's field separator) | no verdict |
| `CDPATH`, in any form | `static`: Deny (see "Changing directory"). `runtime`: no verdict |
| any other name (`FOO=1 ls`, `export FOO=1`) | no verdict |

`ExecEnvName` matches these names exactly: `PATH BASH_ENV ENV ZDOTDIR SHELL SHELLOPTS BASHOPTS PROMPT_COMMAND PS4 IFS PAGER MANPAGER EDITOR VISUAL BROWSER LESSOPEN LESSCLOSE SSH_ASKPASS SUDO_ASKPASS NODE_OPTIONS NODE_PATH PERL5OPT PERL5LIB PERLLIB RUBYOPT RUBYLIB JAVA_TOOL_OPTIONS _JAVA_OPTIONS JDK_JAVA_OPTIONS CLASSPATH TCLLIBPATH GCONV_PATH`, and every name starting with `LD_`, `DYLD_`, `GIT_`, `PYTHON`, `LUA_` or `BASH_FUNC_`. The run-time checks below take no environment. Outside jailed mode the Ask before the command runs is the only check on these variables; in jailed mode the jail also drops them from every program's environment (see [Jailed mode](#jailed-mode)).

The hook additionally turns an **Ask into a Deny** when the call's JSON-encoded params exceed `shuttle.ApprovalParamsMaxBytes` (8,192 bytes). An approval card cuts params past that size, and a person is never asked to approve text they cannot see. It also denies a call with no `command` string.

### The call's other parameters

| Parameter of the call | Verdict |
|---|---|
| `shell` set to `bash` | the command is judged as above |
| `shell` absent, empty or `default`, when `bash` is on the server's `PATH` (`shell_execute` then runs bash) | the command is judged as above |
| `shell` absent, empty or `default`, when `bash` is not on `PATH` (`shell_execute` then runs `sh`) | Deny |
| `shell` set to `sh` | Deny: `sh` can be dash (Debian, Ubuntu), which parses some commands differently from bash. Dash has no `$'…'` quoting, so ``echo $'\' ; touch x ; #'`` is one `echo` to bash and runs `touch` in dash |
| `shell` set to anything else (`powershell`, `cmd`, `zsh`), or not a string | Deny: the command is parsed as bash, so a command another shell runs cannot be judged |
| `env` (merged into the command's environment by `shell_execute`): a name `ExecEnvName` reports | Ask in both modes, granting the name in `Env`; the reason shows it as `NAME (env parameter)` |
| `env`: `CDPATH` | `static`: Deny |
| `env`: any other name, or an entry whose value is not a string (the tool ignores it) | no verdict |

On Windows every call is refused before these parameters are read (see [Platform support](#platform-support)).

## Reasons

```
needs approval under shell policy readonly: rm (1:1): not on the shell allowlist; npm (1:10):
not on the shell allowlist; /etc/hosts (2:1): reads outside the read roots. Everything else in
the command is allowed.

shell policy readonly refuses this command: sudo (1:1): never allowed by the shell policy
```

A Deny's reason lists only what is refused. A finding inside a literal `eval` or `trap` argument is reported at the `eval` or `trap`.

## The grant

On Ask, `Decision.Grant` is a `*shellpolicy.Grant`:

```go
type Grant struct {
    Programs   []string // names or exact paths that may run, with any arguments, for this call
    Builtins   []string
    ReadPaths  []string // resolved paths that may be read, with everything beneath them
    WritePaths []string // exact resolved paths in a git directory that may be written
    Env        []string // variables the command sets that change what runs (no run-time check reads this; the jail drops these names from every program's environment)
}
```

When the Ask is approved, the tool body reads the grants with `shellpolicy.MergeGrants(shuttle.AdmissionGrantsFromContext(ctx))`. A call allowed outright carries none.

## The `readonly` policy

`shellpolicy.Readonly()`. A program qualifies when it cannot start another program, cannot write a file except through an argument its profile refuses, and is run often.

**Builtins:** `echo printf test [ cd pwd read true false : set shift exit return break continue wait type getopts pushd popd dirs mapfile readarray eval trap unset`. Assignments (`export`, `local`, `declare`, `readonly`, `typeset`, `let`) are not calls and need no builtin entry. Each name they set is still judged: setting a variable that changes what runs, such as `PATH` or `LD_PRELOAD`, is an Ask (see "Variables the command sets" above).

| Program | Argument rules |
|---|---|
| `cat head tail cut comm cmp od stat realpath md5` | path confinement (BSD `md5 -c` compares against a string, not a file) |
| `md5sum sha1sum sha256sum shasum` | path confinement; refuses `-c` and `--check`, which read the names of the files to hash from a sums file that path confinement does not see, and tell whether each exists and matches |
| `ls` | path confinement; refuses `-L` or `--dereference` together with `-R` or `--recursive` (ls then lists the directories symlinks point to, out of the read roots). Each alone is allowed. Every letter of a short option group counts |
| `wc` | path confinement; refuses `--files0-from` (it reads the names of the files to count from a file, which path confinement does not see) |
| `grep egrep fgrep` | path confinement, including a file attached to `-f` (`-f/x`); refuses `-R`, `--dereference-recursive` and BSD grep's `-S`, which follow every symlink they meet out of the read roots (`-r` follows only the symlinks named on the command line, and path confinement checks those); `-e -f -m -A -B -C -d -D` take a value |
| `diff` | path confinement, including a file attached to `-X`; `-C -U -D -F -I -x -X -S -W -L` take a value. An operand that is an existing directory (also through `--from-file=` or `--to-file=`, and, under `static`, a glob match) is an Ask, with or without `-r`: diff compares the files in it and follows the symlinks it meets there, so `diff sub other` printed a file outside the session through a symlink in `sub`. `Profile.NoDirOperands` marks this; `CheckLaunch` applies it to the expanded operands |
| `du` | path confinement, including a file attached to `-X`; refuses `--files0-from`, and `-L` and `--dereference` (they follow every symlink out of the read roots; `-H`, `-D` and `--dereference-args` follow only the operands, which path confinement checks); `-X -B -d -t -I` take a value |
| `jq` | path confinement, including a directory attached to `-L`. Refuses `import`, `include` and `modulemeta` in any argument read as jq code (outside string literals and comments, not as a field `.import` or a variable `$import`): jq looks the module path up itself, so a symlink in the session leads it out of the read roots (jq refuses `..` in the path). Refuses `-f` and `--from-file`, since a program file can hold them. A file argument named like a module word (`include.json`) also needs approval |
| `tr basename dirname seq sleep uname whoami id which` | none (arguments are not files) |
| `sort` | refuses `-o`, `-T`, `--output`, `--compress-program`, `--temporary-directory`, `--files0-from` (GNU sort reads the files named in that file, so `sort --files0-from=list` printed a file outside the session); `-k -t -S` take a value |
| `uniq` | at most one positional argument (`-f`, `-s`, `-w` take a value). Under `static`, a glob argument is an Ask, since nothing counts the names it expands to |
| `find` | refuses `-exec`, `-execdir`, `-ok`, `-okdir`, `-delete`, `-fprint`, `-fprint0`, `-fprintf`, `-fls`; refuses GNU `-files0-from` (it reads the starting points from a file, which path confinement does not see); refuses `-L` and `-follow`, which follow every symlink out of the read roots, also inside a BSD option group (`-LE`, `-xL`). `-H` follows only the starting points, which path confinement resolves, so it is allowed (`find -H link` asks when `link` leads outside) |
| `file` | refuses `-C`, `--compile`, and `-f`, `--files-from` (they read the names of the files to check from a file, which path confinement does not see; file 5.41 named an SSH key and a database outside the session); path confinement includes a file attached to `-m`; `-e -F -f -m -P` take a value |
| `base64` | refuses `-o`, `--output`; path confinement includes a file attached to `-i` (BSD input file); `-w -b -i` take a value |
| `date` | refuses `-s`, `--set`, `-f`, `--file`, `-r`, `--reference` (date's arguments are not path-checked, and those options read a file), and a positional time such as `202601010000`; `-d -v` take a value. So BSD `date -j -f FORMAT` and `date -r SECONDS` need approval |
| `git` | the git profile below |

**Never:** `sudo su doas pkexec`.

**Option matching.** Short options are matched inside groups, so `-ro` contains `-o`. A letter that takes a value (`ShortWithValue`) ends the group: the rest is its value (`-t,` or `-k2`), and is not read as more options, so `date -d'last sunday'` does not contain `-s`. When that letter is last in its group, the next argument is its value. Long options are matched with or without `=value`, and by abbreviation, so `--out` matches `--output`. Arguments after `--` are positional.

**Path confinement.** Every positional argument, every `--option=value` value, and a value attached to a short option the profile marks as a file (`grep -f/x`), that names an existing path must lie within the read roots or a granted path. A value in the next argument (`grep -f /x`) is checked as a positional argument. Under `static`, an argument the analyzer cannot read as a literal (other than a plain glob, whose directory is checked) is denied, because nothing would check its expanded value.

**git profile.**
- **Subcommands:** `status diff log show rev-parse ls-files blame grep describe shortlog`, and `branch` with only `--list -a --all -r --remotes -v -vv --verbose --show-current`.
- **Global options:** only `--no-pager`, `-P`, `--no-optional-locks` and `-C <dir>`. This is what refuses `-c`.
- **Refused anywhere:** `--config-env --exec-path --attr-source --git-dir --work-tree --namespace --super-prefix --output --ext-diff --textconv --open-files-in-pager --paginate`, and any abbreviation of them of three or more characters (git's option parser accepts unambiguous abbreviations: `git grep --op=<cmd>` is `--open-files-in-pager`), except `--text`, an option of its own. Any single-dash argument containing `O` is refused too: `git grep -O<cmd>`, alone or in a group such as `-iO<cmd>`, runs the program, and `diff -O<file>` reads a file.
- **Hardening.** Under `static` enforcement no runner applies this hardening, so `git` is denied there. `Profile.Harden` prepends the following at run time, and adds `--no-ext-diff --no-textconv` to `diff`, `log` and `show`:

  ```
  -c core.fsmonitor=false -c core.pager=cat -c core.hooksPath=/dev/null -c diff.external= -c core.attributesFile=/dev/null -c log.showSignature=false -c gpg.program=gpg -c gpg.ssh.program=ssh-keygen -c gpg.x509.program=gpgsm -c safe.bareRepository=explicit -c protocol.allow=never -c protocol.ext.allow=never -c protocol.fd.allow=never -c protocol.file.allow=never -c protocol.git.allow=never -c protocol.ssh.allow=never -c protocol.http.allow=never -c protocol.https.allow=never -c protocol.ftp.allow=never -c protocol.ftps.allow=never --no-pager --attr-source=4b825dc642cb6eb9a060e54bf8d69288fbee4904
  ```

- **Environment.** `Profile.Env` sets `GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ATTR_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 GIT_NO_LAZY_FETCH=1`.

With the hardening applied, git 2.50.1 ran no program from the repository's own config for fsmonitor, the pager, an external diff, textconv, or a clean filter named in the worktree's `.gitattributes` (design Probe 9). The hardening does **not** stop every filter:

- `--attr-source` replaces only the worktree's attributes. A filter named in `$GIT_DIR/info/attributes` still runs: with this prefix, git 2.50.1 ran a `clean` filter from `.git/info/attributes` on `git diff` (PR #430 review F1). No option drops that file without naming the filter, so a write into a git directory is an Ask (see "Paths and redirects"). A repository that already holds such a file and filter when the command starts is not covered.
- **Bare repositories.** A clone can track a bare repository (`HEAD`, `objects/`, `refs/`, an `index`, and a `config` with `core.worktree=..` and a filter). `git -C evil diff`, or `git diff` run inside it, used that repository and ran its filter with git 2.50.1 and the prefix above, with nothing written (PR #430 review F5). `-c safe.bareRepository=explicit` (git 2.38 and later) makes git refuse it. Older git ignores that key, so the git profile also has a `RepoCheck`: when git would discover a bare repository from the directory it runs in (after each `-C`; the first ancestor holding a `.git` entry is a normal repository, and a git directory named `.git` is allowed), the analyzer asks under `runtime`, granting `git`, and `CheckLaunch` refuses the launch unless `git` is granted.
- **Lazy fetch.** In a partial clone missing a blob, `git show`, `log -p`, `diff` and `blame` fetch it from the promisor remote, over a transport the repository's config picks: `core.sshCommand`, or an `ext::` URL with `protocol.ext.allow=always`. git 2.50.1 ran `core.sshCommand` that way under the earlier prefix (PR #430 review F7). `GIT_NO_LAZY_FETCH=1` (git 2.44 and later) stops the fetch. For older git, every built-in transport is set to `never` on the command line; `protocol.allow=never` alone is not enough, since the repository's `protocol.<name>.allow` overrides it. With these, git 2.50.1 refused the transport and ran nothing. A remote helper outside that list (`git-remote-<name>` on `PATH`) is stopped only by `protocol.allow=never` and, on git 2.44 and later, by `GIT_NO_LAZY_FETCH`; a repository config that sets its `protocol.<name>.allow` reaches it on older git.
- `-c core.attributesFile=/dev/null` drops an attributes file the config names. Without it, git 2.50.1 ran the filter.
- `-c log.showSignature=false` and the three `gpg.*program` defaults stop `git log` and `git show` from running a signature program the config names. Without them, a repository config that set `log.showSignature`, `gpg.ssh.program` and `gpg.ssh.allowedSignersFile` made git 2.50.1 run its program on `git log` for a signed commit. `--show-signature` then runs the system `gpg`, `ssh-keygen` or `gpgsm`.

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
    search_path: [/usr/local/bin, /usr/bin, /bin]   # default; macOS adds /sbin (md5, md5sum, sha1sum, sha256sum)
    memory_bytes: 536870912   # default 512 MiB
    file_bytes: 268435456     # default 256 MiB
    strict: true              # set -u (default true)
    read_roots: []            # added to the session's own directory
    write_roots: []           # added to the session's scratchpad and artifact directories
  hooks:
    - kind: command-policy    # optional; without it, off-list programs are blocked and never approvable
      scope: shell_execute
      policy: readonly        # must equal tools.shell_execute.policy
      enforcement: runtime    # the jail re-checks every launch; static would also deny git and computed names
```

**Startup warning.** With the jail on, `serve` logs one warning for each `command-policy` binding that leaves `enforcement` unset. An unset value means static, which denies what the jail would re-check.

**Startup** (`serve`). The following abort startup:

- an unknown `mode`;
- a `policies` entry that is undefined, loops, redefines `readonly`, or fails `Validate`;
- a `command-policy` binding on `shell_execute` while `mode` is `bash`, because nothing would enforce what the hook approves;
- a binding naming a different policy than `tools.shell_execute.policy`;
- a failed self-test.

In jailed mode, `serve` also:

1. **Pins programs.** It resolves every allowlisted program once on `search_path` and logs one warning naming every program it cannot find. A missing program fails with exit 127. The readonly policy lists the GNU and BSD names of the checksum programs side by side (`Profile.Alternates`): `md5` stands in for `md5sum` and the reverse, and `shasum` for `sha1sum` and `sha256sum`. A missing name whose stand-in was found is not in the warning, so a Linux host is not told about `md5` and an older macOS host is not told about `md5sum`, `sha1sum` or `sha256sum`. A host with neither name is told.
2. **Checks git.** It drops git when git does not accept `--attr-source`, so that git calls need approval rather than running without the hardening.
3. **Runs a self-test.** It runs `echo` through the jail. A binary that does not call `shelljail.Main()` first in `main()` fails this check.

**A call**:

| | Jailed mode |
|---|---|
| Interpreter | `mvdan.cc/sh/v3/interp`, bash dialect, in a child process (the host binary re-executed with `LOOM_SHELLJAIL_CHILD=1`). The command spec goes on stdin, never argv. |
| Session | Required. Without one, the result is `JAIL_UNAVAILABLE`. |
| Working directory | The session scratchpad. A relative `working_dir` resolves against it. |
| Reads | Confined to the session's own directory, `$LOOM_DATA_DIR/artifacts/sessions/<id>/` (its `scratchpad/`, `agent/` artifacts and `user/` uploads), plus `read_roots` and `write_roots`. The rest of `LOOM_DATA_DIR` is outside: it can hold the database (`loom.db`), the server config (`looms.yaml`) and every other session's files. `/tmp` is outside too. A read outside these roots needs a person's approval for that path (an Ask from the hook) or is blocked. `serve` resolves `$LOOM_DATA_DIR/artifacts/sessions` once at startup, so `artifacts/` can be a symlink (for example to another disk); `serve` then logs `Jailed shell: session directories resolve outside the data directory` with both paths. A session whose directories resolve outside that sessions directory, for example after `artifacts/` is repointed while the server runs, is `JAIL_UNAVAILABLE`. The session's read root is resolved from the session's own path, not from its scratchpad, and a `scratchpad/` or `agent/` that resolves outside the session's own directory (a symlink to the data directory, the sessions directory or another session) is `JAIL_UNAVAILABLE` too. |
| `shell` param | Only `default`, `bash` or `sh`. Anything else is `INVALID_PARAMS`. |
| Program launch | `CheckLaunch` on the expanded argv and current directory. A bare name runs only from its pinned path, and a name containing `/` only when granted. A refusal fails that one command with exit 126; the shell continues, and `set -e` stops it. |
| Builtins, redirects, globs | `CheckBuiltin`, `CheckOpen` and `CheckListDir`. |
| What a person approved | The call's admission grants (`shellpolicy.MergeGrants(shuttle.AdmissionGrantsFromContext(ctx))`). |
| Environment | Built from scratch. No server variable reaches a command. The interpreter starts with `PATH` (`search_path`), `HOME` (scratchpad), `TERM=dumb`, `NO_COLOR=1`, `LANG`/`LC_ALL`/`TZ` from the server, `LOOM_DATA_DIR`, `SESSION_ID`, `SESSION_ARTIFACT_DIR`, `SESSION_SCRATCHPAD_DIR`, and the call's `env`. Each program gets the shell's exported variables with `PATH` forced to `search_path`, whatever the server's or the command's `PATH`. Credential names (as in bash mode) and every name `shellpolicy.ExecEnvName` reports (the list under "Variables the command sets", the same one the hook asks about: `SHELL`, `LD_*`, `GIT_*`, `PAGER`, `NODE_OPTIONS`, …) are dropped and reported in `env_dropped`; `PATH` is replaced, not reported. |
| Limits | Linux: `RLIMIT_DATA` (`memory_bytes`), `RLIMIT_FSIZE` (`file_bytes`), `RLIMIT_CPU` (timeout + 5 s) and `RLIMIT_CORE` = 0, all inherited by every program, plus `oom_score_adj` = 1000. Every OS: a heap watchdog that stops the interpreter at a third of `memory_bytes`, with exit 137 and `limit: memory`. On Linux one allocation can pass `RLIMIT_DATA` before the watchdog samples it; the Go runtime then dies with a crash dump and the child reports nothing. The host sees that case (no report, and a stderr line that starts a Go runtime or ThreadSanitizer memory-failure dump) and also sets `limit: memory`. The dump is cut from `stderr` and replaced by the line `jail: the shell passed its memory limit and was stopped`, the same line the watchdog prints. The server logs the dump's first line and size at Debug, never the dump. The watchdog samples the interpreter's own Go heap, so it does not see the memory of the programs the jail starts (`sort`, `grep`, `jq`, …), which are separate processes. macOS rejects `RLIMIT_DATA`, so there the watchdog is the only memory bound and the programs the jail starts have no memory limit: in a review run with `memory_bytes` at 96 MiB, `sort` on a 115 MB file peaked near 288 MB and finished. `file_bytes` and the CPU limit still apply to them. |
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
| `CheckLaunch(argv, dir, roots, grant)` | one program launch: the never list, the current directory, the grant, the allowlist, the profile (including `Profile.RepoCheck`, which for git refuses a bare repository found from `dir`), and path confinement |
| `CheckBuiltin(name, grant)` | a builtin |
| `CheckOpen(path, dir, write, roots, grant)` | a file the shell opens (a redirect or `source`). A read grant never covers a write. A write into a git directory needs its exact resolved path in `Grant.WritePaths`. The programs the command starts open their own files (git writes its index), which this does not judge |
| `CheckListDir(dir, roots, grant)` | a directory listed to expand a glob |

Each returns a `*shellpolicy.LaunchError{Name, Reason}`, or nil.

## Tests

- `pkg/shelljail` (87.9% statement coverage on macOS, 88.0% on Linux):
  - the corpus, through a real child and in-process under `-race`;
  - git runs nothing from repository config;
  - a timeout kills background programs;
  - the memory bomb is stopped and reports `limit: memory` with no runtime dump, whether the watchdog or `RLIMIT_DATA` stops it (Linux arm64 container: 20 of 20 runs, with and without `-race`);
  - policy and jail agree on fully literal commands;
  - the self-test catches a binary that is not a jail.
- `pkg/shuttle/builtin`: the full hook → approval → jail path through the executor.
- `cmd/looms`: config decoding, validation and `serve` setup.
- `pkg/shellpolicy`: 96.5% statement coverage.
- **Corpus:** the design's bypass corpus plus the environment-assignment and static-path cases, 144 commands, each run under both enforcement modes.
- **Review bypasses:** the commands from the PR #430 reviews (N1-N10 and R1-R6), the vectors found while fixing them, the other programs that follow symlinks out of the roots (follow-ups to R5), and GNU `find -files0-from` (a follow-up to R4) (`bypass_test.go`), 240 commands, each run under both enforcement modes. The review's commands and the new vectors were run in bash 5.2 (macOS bash 3.2 for `.*` matching `..`, git 2.50.1 for `git grep -O`) and did the harmful thing while the analyzer allowed them; the other rows are variants of those shapes and commands that must stay allowed.
- **Run-time checks:** a 22-case table for `CheckLaunch`, and a 14-case table for the review's argument rules.
- **Call parameters:** `TestDecide_ShellParam`, `TestDecide_ShellNeedsBash` (sets whether the hook finds bash on `PATH`, so it runs on every host) and `TestDecide_EnvParam`.
- **Windows refusal:** `TestDecide_RefusesOnWindows` sets the OS the hook judges for, so it runs on every host: on Windows every call is refused (default, `bash`, `sh` and other shells; `static` and `runtime`; through the factory and the executor), and the same calls are judged normally on Linux and macOS. The tests that check POSIX path semantics skip on Windows.
- **Interpreter drift:** `TestImplementedBuiltinsMatchInterpreter` fails when an interpreter upgrade implements or drops a builtin.
- **Executor:** an end-to-end test through `BuildChainFromConfig` and the executor.
- **Fuzzing:** `FuzzAnalyze`, which also runs in the CI fuzz job.
