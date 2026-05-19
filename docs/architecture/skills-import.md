# Skills Import + Classification

**Status:** ✅ Implemented (pkg/skills/importer + parent_index_path persistence + router-side leaf filter + Taxonomy type + graph-aware classifier path)
**Branches:** `feat/skills-import-converter` (PR #182, merged); `feat/skills-import-rpc` (in flight, RPC surface)
**Version target:** post-v1.2.0

Architecture reference for `loom skills import [--classify --taxonomy]`. For
end-user usage see [`guides/skills-import-guide.md`](../guides/skills-import-guide.md).

## What it does

Converts an Anthropic-style Agent Skill source tree:

```
~/Projects/skills/
├── teradata-statistics/
│   ├── SKILL.md
│   └── references/
│       ├── collect-stats.md
│       └── histograms.md
├── teradata-partitioning/
│   ├── SKILL.md
│   └── references/...
└── ...
```

into `loom/v1` Skill YAMLs the loader can read:

```
~/.loom/skills/
├── teradata-statistics.yaml
├── teradata-partitioning.yaml
└── ...
```

When `--classify` is set, each skill is also assigned a hierarchical
`parent_index_path` so the [PageIndex-style router](skills-overhaul.md#discovery-pipeline)
can sub-categorize within a domain instead of dumping every same-domain
skill into a flat leaf.

## Status legend

- ✅ Implemented and tested with `-race`
- ⚠️ Partial (interface or scaffolding shipped; some wiring deferred)
- 📋 Planned (not yet started)

## Pipeline

```
phase 1: Discovery        ─►  walk srcRoot, find every <name>/SKILL.md
                              skip "agent-skill-builder" (meta-tooling)
                              build a knownNames set for ref filtering

phase 2: Parse            ─►  for each: split frontmatter from body
                              parse YAML frontmatter
                              extract "## When to Use" bullets
                              extract cross-skill links (markdown + backticks)
                              read references/*.md, sorted by filename

phase 3: Classify (opt-in)─►  if --classify:
                                build LLM provider from env
                                load taxonomy (--taxonomy or default)
                                call LLM once per skill with taxonomy hint
                                  (and graph context, when graph-aware)
                                validate response, set imp.ParentIndexPath
                              else: leave ParentIndexPath empty
                                    (router falls back to unclassified/<domain>)

phase 4: Render YAML      ─►  BuildKeywords() — priority-tiered extraction
                              BuildInstructions() — body + inlined references
                              BuildSlashCommands() — derived from skill name
                              ChooseDomain() — teradata-* → teradata, etc.
                              emit loom/v1 Skill YAML; include
                              parent_index_path when set

phase 5: Validate + write ─►  round-trip through skills.LoadSkill (real loader)
                              fail fast if anything wouldn't load at runtime
                              write to outDir, or print on --dry-run
```

The implementation lives in `pkg/skills/importer/` (transformational
package, no I/O surface beyond filesystem reads/writes). The CLI in
`cmd/loom/skills_import.go` is a thin client that wires flags into the
pipeline and renders progress events to stderr. The validator in phase
5 is the same `skills.LoadSkill` that runs on every server boot, so a
successful import guarantees the YAML will load at runtime.

## Phase 3: classifier internals

### Provider construction

`buildClassifyLLM` in `skills_classify.go` reads two env vars and any
provider-standard credential vars:

| Env var | Required | Notes |
|---|---|---|
| `LOOM_CLASSIFY_PROVIDER` | yes (when `--classify` set) | One of `anthropic`, `bedrock`, `ollama`, `openai`, `azure-openai`, `mistral`, `gemini`, `huggingface`. Falls back to `LOOM_DEFAULT_PROVIDER`. |
| `LOOM_CLASSIFY_MODEL` | no | Model identifier; falls back to the provider's catalog default. |

Provider creds are read by the existing `pkg/llm/factory` (e.g.,
`ANTHROPIC_API_KEY`, AWS standard env/IAM chain for Bedrock,
`OPENAI_API_KEY`, etc.). The importer doesn't duplicate them.

Temperature is forced to **0.0** because classification is a deterministic
task. (Bedrock's Claude is still mildly non-deterministic at temp 0; see
"Stability" below.)

### Suggested taxonomy

A per-domain seed map in `skills_classify.go`:

```go
var suggestedTaxonomies = map[string][]string{
    "teradata": {
        "teradata/performance", "teradata/security", "teradata/storage",
        "teradata/sql", "teradata/cloud", "teradata/admin",
        "teradata/ml", "teradata/architecture", "teradata/i18n",
    },
}
```

The taxonomy is a **suggestion, not a closed list**. The prompt instructs
the LLM to propose a new sibling under the same domain root if none of
the suggested buckets fit. A future skill that doesn't match any
suggestion is allowed to land at, say, `teradata/recovery` rather than
being forced into a poor fit.

When a skill's domain has no taxonomy entry yet, the prompt falls back
to a generic `<domain>/<topic>` placeholder. Quality drops; add a
taxonomy entry for new domains.

### Per-skill prompt

```
Assign a hierarchical parent_index_path for this skill, rooted at "<domain>".
The path drives a routing tree: a router uses it to find the right skill for a user message.

Constraints:
- The path MUST start with "<domain>/".
- Use 2 segments total when possible (e.g., teradata/performance). Avoid going deeper than 3.
- Prefer one of the suggested buckets when it fits the skill.
- Propose a NEW sibling bucket only when none of the suggestions are a clear fit.
- Use kebab-case lowercase segments (no spaces, no underscores).

Suggested buckets:
  - teradata/performance
  - teradata/security
  - ... [seeded from suggestedTaxonomies[domain]]

Skill to classify:
  name: <imp.Name>
  description: <imp.Description, truncated to 400 chars>
  when-to-use:
    - <bullet 1, truncated>
    - <bullet 2, ...>

Respond with a single JSON object: {"path":"<chosen-path>","reason":"<short>"}
Output ONLY the JSON object. No markdown fences.
```

The prompt is short (~400 tokens including the skill description), so
classification cost is small even on a large catalog.

### Response validation

`parseClassifyResponse` enforces every constraint the prompt mentions:

| Rule | Failure mode |
|---|---|
| Must start with `<domain>/` (or equal `<domain>`) | Hallucinated top-levels rejected (e.g., `general/foo` when domain=teradata) |
| No prefix collisions | `teradatacloud/foo` rejected (substring-match-on-root would otherwise pass) |
| All segments lowercase | `Performance` rejected |
| No underscores or spaces in segments | `data_types` and `data types` rejected |
| No empty segments | `teradata//foo` rejected |
| Tolerates LLM quirks | Markdown fences, leading prose, surrounding slashes stripped |

Validation failures and the LLM call itself can fail (timeout = 30s,
network errors, malformed JSON). All failures fall back to leaving
`ParentIndexPath` empty, which triggers the legacy `unclassified/<domain>`
placement at `pkg/skills/index.SkillPath()`. The importer never blocks
on a flaky provider.

### Special cases

**Parent-index skills** (`-skill-index` suffix) are deliberately skipped
by the classifier. They live at their own well-known position
(`unclassified/meta-agent` after `chooseDomain` sees `*-skill-index`)
because their `mode: ALWAYS` makes them surface every turn regardless of
routing.

## Phase 4: YAML emission

### Field provenance

The rendered YAML for a non-parent-index skill looks like:

```yaml
apiVersion: loom/v1
kind: Skill
parent_index_path: teradata/performance     # only when --classify produced a path
metadata:
  name: teradata-statistics
  title: Teradata Statistics                # deriveTitle() from skill name
  description: |
    <imp.Description from frontmatter>
  version: 1.0.0
  domain: teradata                          # chooseDomain()
  author: teradata                          # frontmatter, "imported" if absent
  labels:
    source: agent-skill-import              # constant marker for re-imports
    upstream: teradata-statistics
trigger:
  slash_commands:
    - /teradata-statistics                  # buildSlashCommands(): kebab name
  keywords: [...]                           # buildKeywords(): up to 32
  intent_categories: []
  mode: AUTO                                # ALWAYS for *-skill-index
  min_confidence: 0.6
prompt:
  instructions: |
    <imp.Body, with references/*.md inlined under "## Reference: <Title>">
tools:
  required_tools: []
  preferred_order: []
  excluded_tools: []
  mcp_servers: []
pattern_refs: []
skill_refs:                                  # subset of LinkedSkills that
  - teradata-partitioning                    # resolve to importable skills,
  - teradata-architecture                    # capped at 3 by loader rule
```

### Keyword synthesis

`buildKeywords` extracts keyword candidates with source-priority scoring,
then ranks before truncating to 32 entries. Priorities:

| Priority | Source |
|---:|---|
| 100 | Skill name + bare suffix (`teradata-partitioning`, `partitioning`) |
| 90 | CAPS acronyms + ngrams from the description |
| 80 | Markdown inline code spans from SKILL body (` ``MLPPI`` `, ` ``RANGE_N`` `) |
| 70 | All-caps acronyms from SKILL body (≥3 chars, no digits, no generic SQL verbs) |
| 60 | 2- and 3-word non-stopword-bounded ngrams from "When to Use" bullets |

Rationale and full filtering rules in
[`skills-overhaul.md`](skills-overhaul.md#discovery-pipeline).

### Cross-skill ref filtering

`extractLinkedSkillNames` walks the body for two patterns:

```
[label](../<skill-name>/SKILL.md)     # markdown links
`<skill-name>`                          # backtick code spans (prefix: teradata-)
```

The full list lands in `imp.LinkedSkills` and is preserved in the prompt
body for parent-index skills. For `skill_refs` field emission, the
importer filters against `knownNames` — the set of skills the importer
will actually produce in this run. References that don't resolve (typos,
upstream packages like `teradata-python-addons`, external deps) are
dropped from `skill_refs` and surfaced on the per-skill output as
`refs-dropped: <names>`.

The loader caps `skill_refs` at 3 (validation in `pkg/skills/loader.go`),
so resolved entries beyond that stay in the prompt body but aren't in the
top-level field. The progress output's `refs=N/M` reports `min(resolved, 3) / total-declared`.

## Phase 5: validation

Every rendered YAML round-trips through `skills.LoadSkill` (the real
loader, same one used at server boot) before being written to disk. A
parse or schema validation failure here counts as a `[fail]` in the
summary and the file isn't written — but the user still sees the
metadata tags the importer was attempting to emit, so the failure mode
is debuggable from output alone.

## Persistence interaction with the router

The hierarchical index lives in SQLite at `~/.loom/loom.db` (table
`skill_index_nodes`). Index ID is content-hashed against:

- skill name, title, description, domain
- skill `parent_index_path` (when set)
- the model name that built the index

So when an existing skill gains or changes `parent_index_path` (e.g.,
re-classification), its content hash drifts → the index ID changes →
the warm-up's `LatestIndex` returns the previous tree first, then
`Build()` rebuilds the new one and `SaveIndex`s. **One server restart
is enough**; manual cache purge isn't needed.

The router's leaf-filter (`pickFromFatLeaf` in `pkg/skills/index/router.go`)
is the safety net for buckets that classify large. When a terminal leaf
has more `skill_refs` than `maxCandidates` (default 5), the router
makes one extra LLM call to pick the relevant subset for the user
message. So the classifier doesn't need perfect bucketing — even an
8-skill bucket still produces precise routing.

## Stability of LLM-assigned paths

Across multiple `--classify --force` runs against the same source set,
classification is mostly stable but not deterministic — even at
temperature 0, Bedrock can drift, and rare descriptions will flip
buckets between runs. Observed example: `teradata-elasticity` flipped
between `teradata/performance` and `teradata/cloud` across two
consecutive runs.

The router degrades gracefully: a skill in `teradata/cloud` for an
elasticity question still routes correctly via the `teradata/cloud`
descend decision (the LLM router asks "does this user message belong
under teradata/cloud or teradata/performance?" and picks the right
subtree). Classification doesn't have to be perfect — only consistent
with the routing LLM's intuition for the same domain.

For workflows where classification stability matters (e.g., committing
generated YAMLs to a repo), import once with `--classify`, commit the
YAMLs, then re-import only new skills with `--classify --out /tmp/new`
and selectively merge.

## Package boundaries and entry points

The package separates **transformation** (input source → output YAML)
from **delivery** (where source comes from, how progress is reported,
where output goes). Three entry points share the same per-skill
processor (`ProcessSkill`) so the gRPC server-side path and the CLI
in-process path don't drift:

```
┌─────────────────────────────────────────────────────────────┐
│                   pkg/skills/importer                        │
│                                                              │
│   ┌────────────┐    ┌───────────────┐                       │
│   │ RunFromDir │    │ RunFromSkills │  (peer entry points)  │
│   │ (CLI path) │    │ (server path) │                       │
│   └─────┬──────┘    └────────┬──────┘                       │
│         │                    │                              │
│         │ phase 1+2          │ banner only                  │
│         │ (discovery)        │                              │
│         │                    │                              │
│         └─────────┬──────────┘                              │
│                   ▼                                          │
│         ┌───────────────────┐                               │
│         │   processLoop     │   ← shared per-skill iterator │
│         │   (phases 3-5)    │                               │
│         └─────────┬─────────┘                               │
│                   │                                          │
│                   ▼                                          │
│         ┌───────────────────┐                               │
│         │  ProcessSkill     │   ← single-skill entry point  │
│         │  (phases 3-5      │     also exported for direct  │
│         │   for one skill)  │     use by AddSkill RPC       │
│         └───────────────────┘                               │
└─────────────────────────────────────────────────────────────┘
```

**RunFromDir(ctx, DirConfig)** is what the CLI calls. It owns
discovery: walks the source directory, parses each `SKILL.md`,
partitions into `pendingSkills + skipped + failed`, then delegates to
`processLoop` with all parsed skills.

**RunFromSkills(ctx, SkillsConfig)** is what the gRPC server calls
when source arrived as a zip archive (extracted server-side to a temp
dir then re-parsed) or as `InlineSkill` records over the wire. It skips
discovery and goes straight to the loop.

**ProcessSkill(ctx, idx, total, skill, knownNames, opts)** processes
exactly one skill and returns a `SkillResult`. Used internally by
`processLoop`, exported for direct use by the gRPC `AddSkill` RPC where
the client added one skill, not a tree.

### Trade-offs of this split

**Chosen**: Three entry points sharing `ProcessSkill`.

**Alternative 1**: One `Run` function with a "source mode" enum.
Smaller surface area but conflates two unrelated concerns (discovery
shape vs. per-skill processing) into a single switch statement.
Rejected.

**Alternative 2**: Functional options pattern with a single `Run`.
Idiomatic Go but obscures that discovery is qualitatively different
from per-skill work. The current shape makes the dependency graph
explicit at the type level.

**Consequence**: Three exported `Run*` symbols where one might do.
Acceptable because each has a distinct caller (CLI, server-bulk,
server-single) and the shared processor keeps logic in one place.

## Taxonomy: customizable seed buckets

The classifier prompt presents bucket suggestions to the LLM. These
suggestions used to be hardcoded in Go source (`SuggestedTaxonomies`
map for `teradata` only); they now live in `embedded/taxonomy.yaml`
and load through `LoadTaxonomy` so users with their own skill libraries
can supply a custom YAML via `--taxonomy <path>`.

### Why YAML, not Go source

**Chosen**: YAML file, embedded as the default via `go:embed`.

**Rationale**:
- Users can read the canonical seed without grepping Go source.
- A user extending Loom for their own domain copies the YAML and edits
  it, instead of forking the Go package.
- The same loader handles default + user paths uniformly.

**Alternative considered**: Hardcoded Go map. Simpler self-contained
package but invisible to users — to know "what's the default
taxonomy?" they'd have to read source.

### Schema

```yaml
domains:
  <domain>:
    description: "<one-line summary>"
    buckets:
      - path: <domain>/<bucket-segment>
        description: "<distinctive vocabulary; reaches the LLM>"
```

### Validation invariants (enforced by `LoadTaxonomy`)

- Every bucket `path` MUST start with its declaring domain root, or
  equal the domain root exactly. Hallucinated unrelated top-levels
  rejected.
- Path segments MUST be lowercase kebab-case (no underscores, no
  uppercase, no embedded spaces).
- Domains correspond to `ChooseDomain()` output.
- Empty taxonomy (`domains: {}`) rejected — at least one domain required.

The validator is the **same set of rules** the response-parser
(`ParseClassifyResponse`) enforces on LLM output, so a taxonomy that
loads cleanly cannot suggest paths the response-parser would later
reject.

### How it reaches the LLM

Bucket descriptions are the primary signal. Where the old Go source
had a comment `// optimizer, statistics, partitioning, ...` next to
the path (which never reached the model), the YAML now puts that
text in a `description` field that the prompt builder concatenates
onto each bucket suggestion:

```
Suggested buckets:
  - teradata/performance — optimizer, statistics, partitioning,
    intelligent memory, query plans, EXPLAIN, plan caching, adaptive
    optimizer
  - teradata/security — row-level access control (RLAC), constraint
    columns, encryption, role-based auth, GRANT/REVOKE, TLS
  - ...
```

The LLM uses these descriptions as a vocabulary signal: a skill whose
description mentions "EXPLAIN", "vacuum", or "pg_stat_statements"
matches the description signal of a postgres/performance bucket better
than a generic postgres/<topic> placeholder.

## Graph-aware classification

`ClassifyAgainstGraph` extends `Classify` with a `GraphContext`
parameter describing the current state of the live router tree:

```go
type GraphContext struct {
    Buckets []GraphBucket
}
type GraphBucket struct {
    Path    string   // existing bucket path (e.g., "teradata/performance")
    Members []string // skill names already attached
}
```

The prompt grows a "Current catalog" section listing every populated
bucket plus its members. The LLM is instructed to "prefer joining a
bucket that already holds related skills over inventing a parallel
sibling." Same prompt builder, same response validator — the only
difference is the additional context block.

### Why this matters

The stateless `Classify` (used by the CLI's offline `--classify` flag)
sees only the seed taxonomy. Two skills imported in separate runs that
should logically share a bucket can land in different ones because
neither call knows where the other landed. For a one-shot bulk import
this is benign — they all classify in the same run, against the same
taxonomy. For incremental adds (the gRPC server's `AddSkill` RPC), the
graph context is the difference between a skill joining `teradata/admin`
where four siblings already live versus inventing a new
`teradata/operations` because the LLM chose differently this turn.

### Source of GraphContext

Constructed by the gRPC server from `pkg/skills/index.Store.LatestIndex`.
The CLI's offline path doesn't populate it — it has no live router tree
to read.

## Files

| File | Role |
|---|---|
| `pkg/skills/importer/types.go` | `Skill`, `Reference`, `Frontmatter` |
| `pkg/skills/importer/parse.go` | `ReadSkill`, frontmatter splitter, references reader, link extractor, `IsSafeSkillName`, `IsSkippedSkill` |
| `pkg/skills/importer/keywords.go` | `BuildKeywords`, `BuildSlashCommands`, priority-tier extraction, stopword filtering |
| `pkg/skills/importer/render.go` | `RenderYAML`, `ChooseDomain`, `DeriveTitle`, `BuildInstructions`, YAML node helpers |
| `pkg/skills/importer/classify.go` | `Classify` (stateless), `ClassifyAgainstGraph` (graph-aware), `ParseClassifyResponse`, `GraphContext` + `GraphBucket` |
| `pkg/skills/importer/taxonomy.go` | `Taxonomy`, `TaxonomyDomain`, `TaxonomyBucket`, `LoadTaxonomy`, `DefaultTaxonomy` (embeds `embedded/taxonomy.yaml`) |
| `pkg/skills/importer/pipeline.go` | `RunFromDir`, `RunFromSkills`, `ProcessSkill`, `Outcome`, `SkillResult`, `Totals`, `DiscoveryReport` |
| `embedded/taxonomy.yaml` | Default seed taxonomy (built-in `teradata` domain); user-extensible |
| `cmd/loom/skills_import.go` | Cobra command, flag binding, per-skill output renderer (consumes `OnResult` events) |
| `cmd/loom/skills_classify.go` | Env-driven LLM provider construction (`buildClassifyLLM`) |
| `pkg/skills/loader.go` | YAML schema + `parent_index_path` field |
| `pkg/skills/index/node.go` | `SkillPath()`: routes `parent_index_path` → tree position |
| `pkg/skills/index/router.go` | `pickFromFatLeaf`: per-leaf LLM filter for fat buckets |
| `pkg/skills/binding/binding.go` | Glob match falls back to bare skill name when FQN match misses |

## Cross-references

- [End-user usage guide](../guides/skills-import-guide.md)
- [Skills overhaul (broader skills subsystem doc)](skills-overhaul.md)
- [Discovery pipeline + router](skills-overhaul.md#discovery-pipeline)
