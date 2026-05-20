# Skills Import Guide

How to import Anthropic-style Agent Skill directories into Loom,
add or re-classify a single skill, and have running agents pick up
the change without a server restart.

> **Architecture note:** all three operations go through the
> `SkillsImportService` gRPC service that `looms serve` exposes on
> the same port as `LoomService`. The CLI (`loom skills import` /
> `add` / `classify`) is a thin client. The server is where YAMLs
> land, where the LLM classifier runs (with its credentials), and
> where running agents' routers get reloaded after writes. This
> means: **`looms serve` must be running** for any of these
> operations to succeed.

For internals — pipeline phases, classifier prompt shape, validator
rules, gRPC service surface — see
[`architecture/skills-import.md`](../architecture/skills-import.md).

## Subcommands at a glance

| Subcommand | Use for | RPC |
|---|---|---|
| `loom skills import <src-dir>` | Bulk import a tree of `<name>/SKILL.md` directories | `BulkImportSkills` (streaming) |
| `loom skills add <path>` | Add a single skill (from a directory or `.zip`) | `AddSkill` |
| `loom skills classify <name>` | Re-classify an already-imported skill | `ClassifySkill` |

After any successful write, the server reloads every running agent's
router so the change takes effect on the next chat turn.

## Quick start

```bash
# Make sure the server is running.
looms serve &

# Import without classification.
# Skills land at the server's $LOOM_SKILLS_DIR (default ~/.loom/skills)
# with parent_index_path unset.
loom skills import ~/Projects/skills

# Import + classify into a hierarchical tree.
# The server uses its configured classifier provider (see "Environment
# variables" below).
loom skills import ~/Projects/skills --classify

# Add a single skill from a directory:
loom skills add ~/skills-author/teradata-recovery

# Or from a zip archive of the same shape:
loom skills add ~/Downloads/teradata-recovery.zip --classify

# Re-classify an existing skill in the catalog:
loom skills classify teradata-statistics
```

## What each subcommand does

### `loom skills import <src-dir>`

1. **Resolves `<src-dir>`** to an absolute path on the client.
2. **Decides source delivery**: if `--server` resolves to a loopback
   address (the local-serve case), passes the path as `src_dir` and
   the server reads it directly. For non-loopback servers, zips the
   tree client-side and uploads the bytes. Force zip mode with `--zip`.
3. **Streams progress events** from the server: a banner with totals,
   one result per skill, a final summary with the classification
   tally.
4. **After the run**, the server reloads every running agent's
   router. The CLI summary reports converted/skipped/failed counts.

### `loom skills add <path>`

1. **`<path>`** is either a directory containing exactly one
   `<name>/SKILL.md` or a `.zip` of the same shape.
2. **Sends `AddSkill`** with `skill_dir`, `zip_archive`, or (for
   weaver-internal use) `inline` source. CLI auto-zips for non-
   loopback servers.
3. **Returns** the per-skill outcome plus a `router_reloaded` flag
   that says whether running agents picked up the change.

### `loom skills classify <skill-name>`

1. **Server reads the existing YAML** from the catalog directory.
2. **Builds graph context** from the persisted router index — what
   buckets exist, what skills already live in each — so the LLM
   prefers joining a populated bucket over inventing a sibling.
3. **Calls the classifier**, validates the response, writes the
   updated `parent_index_path` back to the YAML, reloads routers.
4. **Returns** previous → new path, the LLM's reason, and the
   reload status.

## Flags

### `loom skills import`

| Flag | Default | Effect |
|---|---|---|
| `<src-dir>` | required | Directory containing one subdir per skill |
| `--out <path>` | server's `$LOOM_SKILLS_DIR` or `~/.loom/skills` | Server-side output directory |
| `--dry-run` | false | Server reports what would be written; no files written |
| `--force` | false | Overwrite existing destination YAMLs |
| `--classify` | false | Server runs LLM classifier (server must have `LOOM_CLASSIFY_PROVIDER` configured) |
| `--taxonomy <path>` | server's built-in seed | Path to a custom taxonomy YAML; uploaded to the server, validated server-side. See [Custom taxonomy](#custom-taxonomy). |
| `--zip` | false | Force zip upload even when server is on a loopback address |

### `loom skills add`

| Flag | Default | Effect |
|---|---|---|
| `<path>` | required | Directory containing one `<name>/SKILL.md`, or a `.zip` of the same shape |
| `--out <path>` | server default | Server-side output directory |
| `--force` | false | Overwrite an existing skill with the same name |
| `--classify` | false | Run the graph-aware classifier |
| `--taxonomy <path>` | server default | Custom taxonomy YAML |
| `--zip` | false | Force zip upload (auto-set for `.zip` source paths and non-loopback servers) |

### `loom skills classify`

| Flag | Default | Effect |
|---|---|---|
| `<skill-name>` | required | Bare skill name (e.g., `teradata-statistics`) |
| `--taxonomy <path>` | server default | Custom taxonomy YAML |

### Global flags

These come from the `loom` root command and apply to all subcommands:

| Flag | Default | Effect |
|---|---|---|
| `--server <addr>` | `127.0.0.1:60051` | gRPC server address |
| `--tls` | false | Enable TLS |
| `--tls-insecure` | false | Skip cert verification |
| `--tls-ca-file <path>` |  | Custom CA cert |
| `--tls-server-name <name>` |  | Override TLS server name |

## Environment variables

> **Important:** these variables are now read by the **server** at
> boot, not by the CLI. To run the classifier, restart `looms serve`
> with these set in its environment, then call
> `loom skills import --classify`.

Read by `looms serve` only when `--classify` is requested by a
client:

| Variable | Required? | Notes |
|---|---|---|
| `LOOM_CLASSIFY_PROVIDER` | yes | One of `anthropic`, `bedrock`, `ollama`, `openai`, `azure-openai`, `mistral`, `gemini`, `huggingface`. Falls back to `LOOM_DEFAULT_PROVIDER`. |
| `LOOM_CLASSIFY_MODEL` | no | Model identifier; falls back to provider's catalog default. |

Plus the provider's standard credential env vars (read by
`pkg/llm/factory`):

| Provider | Vars |
|---|---|
| `anthropic` | `ANTHROPIC_API_KEY` |
| `bedrock` | AWS standard chain (`AWS_REGION`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE`, IAM role, etc.) |
| `openai` | `OPENAI_API_KEY` |
| `azure-openai` | `AZURE_OPENAI_ENDPOINT`, `AZURE_OPENAI_API_KEY` |
| `gemini` | `GEMINI_API_KEY` |
| `mistral` | `MISTRAL_API_KEY` |
| `huggingface` | `HUGGINGFACE_TOKEN` |
| `ollama` | `OLLAMA_ENDPOINT` (defaults to `http://localhost:11434`) |

If the server boots without `LOOM_CLASSIFY_PROVIDER`, requests with
`--classify` return `FailedPrecondition` from the server with a clear
"server has no classify provider configured" message — the CLI
translates this to an actionable error.

## Per-skill output

Each skill produces one progress line. Tag order matches the data flow:

```
[12/23] teradata-statistics    classify=teradata/performance  refs=0/2  domain=teradata  trigger=AUTO  keywords=32  slash-commands=1  refs-inlined=3  [wrote] ~/.loom/skills/teradata-statistics.yaml (40 KB)
```

| Tag | Meaning |
|---|---|
| `[N/M]` | Sequence position (right-padded to align under the name column) |
| `classify=<path>` | LLM-assigned `parent_index_path`. Variants: `fallback(unclassified)` (LLM call failed), `skip(parent-index)` (meta-skill, deliberately not classified) |
| `refs=N/M` | Cross-skill references: how many made it into `skill_refs` (capped at 3) vs how many were declared in markdown. Only printed when M>0 |
| `domain=` | The `metadata.domain` field |
| `trigger=AUTO\|ALWAYS` | The `trigger.mode` field |
| `keywords=<n>` | Count of synthesized FTS5-fallback keywords (capped at 32) |
| `slash-commands=<n>` | Count of slash command aliases |
| `refs-inlined=<n>` | Count of `references/*.md` files inlined into the prompt |
| `[wrote]` / `[would-write]` / `[skip]` / `[fail]` | Outcome with destination path and YAML size |

When cross-skill links don't resolve to importable skills (typos,
upstream packages, external deps), they're shown on a follow-up
indented line:

```
[ 2/23] teradata-analytics      refs=0/3  ...  [wrote] ~/.loom/skills/teradata-analytics.yaml (57 KB)
                                refs-dropped: teradata-python, teradata-python-addons, teradata-udfgpl
```

## Common workflows

### Initial import (no classification)

Quickest path; works offline, no LLM creds needed:

```bash
loom skills import ~/Projects/skills
```

Result: every skill lands at `unclassified/<domain>` in the router's
tree. For a single-domain catalog (e.g., 23 teradata-* skills), this
puts everything under one flat node — the router's
[per-leaf LLM filter](../architecture/skills-import.md#persistence-interaction-with-the-router)
still picks the right subset per query, but every query pays the
filter's LLM call.

### Initial import with classification

Recommended for catalogs of 5+ skills under a single domain. The
classifier env vars go on the **server**, not the client:

```bash
# Server side (one-time):
LOOM_CLASSIFY_PROVIDER=bedrock \
LOOM_CLASSIFY_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
looms serve &

# Client side:
loom skills import ~/Projects/skills --classify
```

Result: each skill assigned a sub-bucket like `teradata/performance`.
Most queries hit a bucket with ≤3 skills, bypassing the per-leaf
filter (saves one LLM call per turn). Larger buckets engage
`pickFromFatLeaf` so the LLM picks the relevant subset for the user
message instead of the alphabetical-first slice.

Cost: ~1 LLM call per skill at import time, ~1-2 seconds each on
Haiku. Total ~30 seconds for 23 skills.

### Adding one skill (most common day-2 case)

```bash
loom skills add ~/skills-author/teradata-recovery --classify
```

The server runs the **graph-aware** classifier so the new skill tends
to join an existing bucket where related skills already live, rather
than inventing a new sibling. The router reloads automatically — the
new skill is routable on the next chat turn.

### Re-classifying an existing skill

Useful after updating the seed taxonomy or after an LLM mis-bucketing
shows up in routing decisions:

```bash
loom skills classify teradata-statistics
# ==> teradata-statistics: teradata/admin -> teradata/performance
#     reason: "Statistics collection is fundamentally a performance optimization concern..."
#     router: reloaded (running agents see the new path immediately)
```

### Bulk re-classify existing import set

```bash
loom skills import ~/Projects/skills --classify --force
```

`--force` overwrites the existing YAMLs. Without it the importer
skips any skill whose YAML already exists in the catalog.

### Stable bulk re-import (preserve existing YAMLs)

Skip `--force`. The importer only writes skills that don't already
exist in the catalog directory. Use this when you've added 1-2 new
skills to the source tree and want them to land alongside the
existing ones without disturbing them.

### Dry run before committing

```bash
loom skills import ~/Projects/skills --classify --dry-run
```

Same output as a real run, but no files are touched. Useful for sanity-
checking the classifier's bucket assignments before overwriting your
live skills directory.

## Custom taxonomy

The classifier prompt presents a list of suggested `parent_index_path`
buckets to the LLM. The default seed lives in
[`embedded/taxonomy.yaml`](https://github.com/teradata-labs/loom/blob/main/embedded/taxonomy.yaml)
and currently covers only the `teradata` domain. If you're importing
skills for a different domain (Postgres, Elasticsearch, your own
internal product), pass a custom taxonomy via `--taxonomy <path>`.

### File format

```yaml
domains:
  postgres:
    description: "Postgres skills"
    buckets:
      - path: postgres/performance
        description: "EXPLAIN, pg_stat_statements, vacuum tuning, bloat, index strategy"
      - path: postgres/replication
        description: "streaming + logical replication, hot standby, pg_basebackup"
      - path: postgres/security
        description: "row-level security, RLS, role + grant model, SCRAM auth, TLS"
      - path: postgres/extensions
        description: "PostGIS, pg_partman, pg_cron, TimescaleDB, custom extensions"
```

The `description` field is the load-bearing part: it reaches the LLM
verbatim alongside each bucket path, giving the model a vocabulary
signal so a skill whose own description mentions "EXPLAIN" or
"pg_stat_statements" matches `postgres/performance` over
`postgres/<topic>` placeholder.

### Validation rules

The taxonomy file is validated at load time, before the classifier
makes any LLM calls. A malformed file fails fast:

- Every bucket `path` MUST start with `<domain>/` or equal `<domain>`.
  Hallucinated unrelated top-levels (e.g., `general/foo` under domain
  `postgres`) are rejected.
- Path segments MUST be lowercase kebab-case. No underscores, no
  uppercase, no embedded spaces. (`postgres/data_types`, `postgres/Foo`,
  and `postgres/data types` are all rejected.)
- At least one domain must be declared.

### Example

```bash
# Save the YAML somewhere stable:
mkdir -p ~/.loom/taxonomies
$EDITOR ~/.loom/taxonomies/postgres.yaml

# Use it for an import:
LOOM_CLASSIFY_PROVIDER=bedrock \
loom skills import ~/Projects/postgres-skills \
  --classify \
  --taxonomy ~/.loom/taxonomies/postgres.yaml
```

### Replacement, not merge

A user-supplied taxonomy **replaces** the built-in default; it does
not merge. If you need both `teradata` and your custom domain, copy
the relevant `domains: teradata: ...` block from
`embedded/taxonomy.yaml` into your own file and add your domain
alongside.

### Empty path → fallback to default

`--taxonomy ""` (the default when the flag is omitted) loads
`embedded/taxonomy.yaml`. Use this when classifying built-in `teradata-*`
skills.

## Reload behavior

After a successful `import`, `add`, or `classify`, the server
automatically reloads every running agent's router so the new or
changed skill takes effect on the next chat turn. The CLI prints
the reload status:

```
==> router reloaded (running agents see the new skill immediately)
```

If a partial failure happens during reload (e.g., one agent's index
build errored), the CLI reports:

```
==> router NOT reloaded (restart looms serve to surface the new skill)
```

In that case the YAML is still on disk and a `looms serve` restart
will pick it up.

The persisted index in `~/.loom/loom.db` is content-hashed against
`parent_index_path`, so changing classifications invalidates the
cached index automatically — no manual cache purge needed.

> **Pre-RPC behavior**: before the SkillsImportService landed,
> imports happened in the CLI process and the server had to be
> restarted manually to see new skills. That restart step is no
> longer required.

## Troubleshooting

**`bulk import: server at 127.0.0.1:60051 is not reachable`**
`looms serve` isn't running, or `--server` points at a different
instance. Start the server (`looms serve`) and try again. The CLI
prints this for any subcommand against an unreachable server.

**`server rejected request (FailedPrecondition usually means a server-side configuration is missing, e.g., classify=true requires LOOM_CLASSIFY_PROVIDER + creds in the server env)`**
You passed `--classify` but the server boot didn't have
`LOOM_CLASSIFY_PROVIDER` set. Stop the server, restart it with the
provider env vars, then retry:

```bash
pkill -f "looms serve"
LOOM_CLASSIFY_PROVIDER=bedrock LOOM_CLASSIFY_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
  looms serve &
loom skills import ~/Projects/skills --classify
```

**`SkillsImportService: classify provider construction failed`** (in server log)
The server tried to construct the classifier at boot but the
provider's standard creds weren't reachable. For Bedrock: confirm
`AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, or IAM role is set in the
server's environment. The classifier provider construction uses the
same chain `looms serve`'s main LLM does, so if your other agents
work, the classifier should too.

**`classify <name>: ... (falling back to unclassified/<domain>)`**
The LLM call failed or returned a malformed/invalid path. The skill
still imports — its `parent_index_path` is left empty so the router
places it at `unclassified/<domain>`. Re-running with the same args
usually resolves transient failures.

**`fail <name>: validate: ...`**
The generated YAML didn't pass `skills.LoadSkill`. The error message
points at the offending field. Most common cause: a malformed
frontmatter `name` (Loom requires kebab-case starting with a letter).

**`skip <name>: <path> exists (use --force to overwrite)`**
The destination already has this skill. Pass `--force` if you mean to
overwrite.

**`Unable to find skills in ~/.loom/skills` after import**
The server is reading from a different `LOOM_SKILLS_DIR`. Check
`~/.loom/looms.yaml` for `skills_dir` and confirm `--out` matched.

**Routing decisions don't change after import / classify / add**
Check that the CLI printed `==> router reloaded`. If it printed
`router NOT reloaded`, the server hit an error rebuilding one or more
agent indices — check the server log around the same timestamp:

```bash
tail -100 /tmp/looms.log | grep -E "Skill router|reload|build"
```

A `looms serve` restart will pick up any YAML on disk regardless of
whether the live reload succeeded.

**`taxonomy: parse taxonomy <path>: domain "X" bucket N: path "Y" does not start with domain root "X/"`**
The custom taxonomy file has a bucket whose path doesn't anchor to its
declaring domain. Move the bucket into the right domain block, or
correct the path prefix. Validation runs at file-load time so this
error always points at the offending file before any LLM call.

**`taxonomy: parse taxonomy <path>: domain "X" bucket N: non-lowercase segment "Foo" in path "X/Foo"`**
Path segments must be lowercase kebab-case. Rename the segment.
Underscores and embedded spaces are also rejected — use hyphens.

**`taxonomy: parse taxonomy <path>: taxonomy has no domains`**
The file parsed as YAML but has an empty `domains` map. Declare at
least one domain with at least one bucket.

## Special cases

### Parent-index skills

Skills whose name ends in `-skill-index` (e.g., `teradata-skill-index`)
are treated as meta-skills:

- `mode` is forced to `ALWAYS` so they surface every turn.
- `domain` is set to `meta-agent`.
- The classifier deliberately skips them; their progress line shows
  `classify=skip(parent-index)`.
- Their full routing table inlines in the prompt body, so `skill_refs`
  is intentionally left empty (loader caps it at 3).

### `agent-skill-builder`

The Anthropic skill-authoring meta-tool is intentionally skipped during
discovery. Its progress line shows `[skip] agent-skill-builder
(meta-tooling, not convertible)`.

### Weaver-authored skills

When the `weaver` agent (or any agent with the `agent_management`
builtin tool) creates a skill via the `create_skill` action, the same
post-write router-reload runs in-process — no gRPC round trip. The
end-user effect is identical to `loom skills add`: the new skill is
routable on the next chat turn without restarting `looms serve`.

## Cross-references

- [Architecture: skills import internals](../architecture/skills-import.md)
- [Architecture: skills overhaul (router + bindings + tasks)](../architecture/skills-overhaul.md)
- [Reference: skill YAML schema](../reference/agent-configuration.md)
