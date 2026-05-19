# Skills Import Guide

How to import Anthropic-style Agent Skill directories into Loom and
optionally classify them into a hierarchical routing tree.

For internals — pipeline phases, classifier prompt shape, validator
rules — see [`architecture/skills-import.md`](../architecture/skills-import.md).

## Quick start

```bash
# Import without classification (default).
# Skills land at ~/.loom/skills with parent_index_path unset, so the
# router places them at "unclassified/<domain>".
loom skills import ~/Projects/skills

# Import + classify into a hierarchical tree.
# The classifier uses Bedrock (or any other configured provider) to
# assign each skill a parent_index_path like "teradata/performance".
LOOM_CLASSIFY_PROVIDER=bedrock \
LOOM_CLASSIFY_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
loom skills import ~/Projects/skills --classify
```

After import, restart `looms serve` to pick up the new skills (the
library is read once at boot; see [Hot-reload caveat](#hot-reload-caveat)).

## What the importer does

1. **Walks `<src-dir>`** for subdirectories containing a `SKILL.md`.
2. **Parses each one** — frontmatter (name, description, version),
   body, references/*.md, cross-skill markdown links.
3. **Optionally classifies** — when `--classify` is set, calls an LLM
   to assign each skill a hierarchical `parent_index_path` from a
   per-domain taxonomy.
4. **Renders `loom/v1` YAML** with synthesized keywords, slash commands,
   and inlined references.
5. **Round-trips through `skills.LoadSkill`** — the same loader the
   server uses at boot — so a successful import guarantees runtime load.
6. **Writes** to `--out` (default `$LOOM_SKILLS_DIR` or `~/.loom/skills`).

## Flags

| Flag | Default | Effect |
|---|---|---|
| `<src-dir>` | required | Directory containing one subdir per skill |
| `--out <path>` | `$LOOM_SKILLS_DIR` or `~/.loom/skills` | Where to write generated YAML |
| `--dry-run` | false | Print what would be written; touch nothing |
| `--force` | false | Overwrite existing destination YAMLs (without this, the importer skips them) |
| `--classify` | false | Run the LLM classifier; assigns `parent_index_path` |
| `--taxonomy <path>` | built-in default | Path to a custom taxonomy YAML; only consulted when `--classify` is set. See [Custom taxonomy](#custom-taxonomy) below. |

## Environment variables

Read only when `--classify` is set:

| Variable | Required? | Notes |
|---|---|---|
| `LOOM_CLASSIFY_PROVIDER` | yes | One of `anthropic`, `bedrock`, `ollama`, `openai`, `azure-openai`, `mistral`, `gemini`, `huggingface`. Falls back to `LOOM_DEFAULT_PROVIDER`. |
| `LOOM_CLASSIFY_MODEL` | no | Model identifier; falls back to provider's catalog default. |

Plus the provider's standard credential env vars (read by `pkg/llm/factory`,
not by the importer):

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

Recommended for catalogs of 5+ skills under a single domain:

```bash
LOOM_CLASSIFY_PROVIDER=bedrock \
LOOM_CLASSIFY_MODEL=us.anthropic.claude-haiku-4-5-20251001-v1:0 \
loom skills import ~/Projects/skills --classify
```

Result: each skill assigned a sub-bucket like `teradata/performance`.
Most queries hit a bucket with ≤5 skills, bypassing the per-leaf filter
entirely (saves one LLM call per turn). The 23-skill teradata library
distributes across 9 buckets; largest is 4-5 skills.

Cost: ~1 LLM call per skill at import time, ~1-2 seconds each on Haiku.
Total ~30 seconds for 23 skills.

### Adding a new skill to an existing import set

Drop the new SKILL.md into the source dir, then:

```bash
LOOM_CLASSIFY_PROVIDER=bedrock loom skills import ~/Projects/skills --classify --force
```

`--force` is required to overwrite the existing YAMLs. Without it, only
the new skill's YAML gets written and the existing skills retain their
prior classifications (good for stability).

### Stable re-import (preserve existing classifications)

Skip `--force` and the importer writes only the new skills:

```bash
loom skills import ~/Projects/skills --classify --out /tmp/new-skills
# Move only files that aren't already in ~/.loom/skills:
for f in /tmp/new-skills/*.yaml; do
  base=$(basename "$f")
  [ -e ~/.loom/skills/$base ] || cp "$f" ~/.loom/skills/
done
```

### Re-classifying existing YAMLs without re-running the full import

**Not directly supported.** Two workarounds:

1. **Re-import from source.** If `<src>/SKILL.md` is still around,
   `loom skills import ... --classify --force` covers it.
2. **Hand-edit the YAML.** `parent_index_path: teradata/<bucket>` is a
   top-level field. Drop it in, restart the server, the index builder
   picks it up.

A standalone `loom skills classify` subcommand that operates on existing
YAMLs is on the follow-up list — the parser, validator, and prompt
builder already exist, ~50 lines of plumbing to expose them.

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

## Hot-reload caveat

The skill library is read **once per agent at server boot**. Importing
or re-classifying skills while `looms serve` is running won't update
the router until you restart:

```bash
pkill -f "looms serve"
nohup ./bin/looms serve > /tmp/looms.log 2>&1 &
```

Watch for `Skill router warmed from fresh build` in the log — that
confirms the new tree is live. The persisted index in `~/.loom/loom.db`
is content-hashed against `parent_index_path`, so changing
classifications invalidates the cached index automatically; no manual
cache purge needed.

## Troubleshooting

**`--classify requires LOOM_CLASSIFY_PROVIDER or LOOM_DEFAULT_PROVIDER`**
The flag is set but no provider is configured. Set
`LOOM_CLASSIFY_PROVIDER=<one of the 8 providers>` in the environment.

**`create classify provider "bedrock": no credentials`**
The classifier provider is wired but its standard creds aren't reachable.
For Bedrock: confirm `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, or IAM role is
set. The factory uses the same chain `looms serve` does.

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

**Routing decisions don't change after re-classifying**
The server didn't restart. Check `tail -f /tmp/looms.log | grep "Skill
router warmed"` to confirm a fresh router build fires.

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

## Cross-references

- [Architecture: skills import internals](../architecture/skills-import.md)
- [Architecture: skills overhaul (router + bindings + tasks)](../architecture/skills-overhaul.md)
- [Reference: skill YAML schema](../reference/agent-configuration.md)
