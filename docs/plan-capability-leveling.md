# Plan: Capability-Leveling

**Status**: Draft — ready to start
**Author**: Josh Schoen
**Date**: 2026-08-15

## Goal

Let **weaker / smaller / open-weight / local** models approach frontier-model quality on Loom
tasks, by spending **structure + verification + selective escalation** instead of model
capability. Riding entirely on Loom's existing provider catalog, judge, and output-validator —
this is a thin *controller* on top of primitives Loom already has, not a new subsystem.

**Precise claim (do not overclaim):** capability-leveling closes the *knowledge* and *format*
gaps fully, and the *reasoning* gap partially (via decomposition + escalation), at extra
compute/latency. It does **not** make a 7B model equal to a frontier model on raw reasoning.
The pitch is: *"your local model's ceiling is set by the context and the loop, not by the
weights."*

## Why this belongs in Loom (not a separate product)

A code assessment (2026-08-15) found ~80–90% of this already exists as Loom primitives. The
missing sliver is small and generic enough that it should be **contributed upstream into Loom**
as a pattern domain + a thin orchestration policy — benefiting every Loom consumer
(loom-knowledge and the Voice product both ride it). Building it as a standalone product would
be repackaging Loom's own capabilities.

## What already exists in Loom (reuse — do not rebuild)

| Primitive | Location | Role in this plan |
|---|---|---|
| Model capability/cost/token **catalog** | `pkg/llm/catalog/catalog.go` (`Lookup`, `Capabilities`, `IsReasoning`) | Source of the **tier signal** |
| **Provider abstraction** + pools + mid-session switch | `pkg/llm/factory`, `Agent.SetLLMProvider` (`pkg/agent/agent.go`), `allowed_providers` | Execute the **escalation ladder** |
| **Per-role LLMs** | `LLMRole` enum + `Agent.GetLLMForRole` (`pkg/agent/agent.go:4110`) | Cheap primary vs. strong escalation target |
| **Judge / eval** (6 aggregations) | `pkg/evals/judges/` | The **quality signal** that triggers escalation |
| **Judge-and-retry loop** | `pkg/orchestration/output_validator.go` (`ValidateAndRetry`, `RetryPolicy`, feedback modes) | Shape to extend — today it only retries the **same** model |
| **Structured-output enforcement** | `pkg/orchestration/output_coercion.go` | Format-gap closing (aggressive coercion for low tiers) |
| **Orchestration patterns** | `pkg/orchestration/*_executor.go`, YAML pattern domains | Decomposition (pipeline) + home for the new pattern domain |

## The missing sliver (what to build)

Three components, each small:

### C1 — Model tier abstraction
Derive a capability tier from the catalog — `frontier | mid | small-open | local` — from existing
`Capabilities`, `IsReasoning`, context-window, and pricing fields. A pure mapping + config; no new
store. Output: `Tier(provider, model) → tier` + per-tier policy knobs (scaffolding depth, coercion
strictness, retry budget, escalation ladder).

### C2 — Capability-adaptive scaffolding (a `capability-leveling` pattern domain)
Given the tier, adjust before generation:
- prompt scaffolding depth (more explicit instructions + few-shot for lower tiers),
- output-schema strictness / coercion aggressiveness,
- optional task **decomposition** for low tiers (reuse the pipeline executor to split one
  frontier-shot into small reliable steps).

Delivered as Loom YAML pattern domain(s) so it's configurable and idiomatic to Loom.

### C3 — Quality-driven escalation router (the key new control loop)
The piece the assessment found genuinely missing. Loop:

```
run on assigned (cheap/local) model
  └─ evaluate with existing Judge / confidence threshold
       ├─ pass  → return
       └─ fail  → escalate one rung, retry, re-judge
                  ladder: (1) more scaffolding / coercion
                          (2) self-consistency N-sample (C4)
                          (3) stronger model from the provider pool
                  until pass OR budget (cost/latency/rungs) exhausted → return best + flag
```

Reuse `ValidateAndRetry`'s shape, `GetLLMForRole`/provider pool for the stronger model, and
Loom's existing **per-agent cost budget** as the hard ceiling. Closes the judge↔generation loop
that today stops at same-model retry.

### C4 — Self-consistency (parallel, optional)
N-sample the primary answer, aggregate via a generalization of the existing `majorityPass`
aggregator (`pkg/evals/judges/aggregator.go`) applied to primary outputs (today it votes over
judges, not generations).

## Phasing (start immediately)

- **Phase 0 — spike / de-risk (1–2 days).** Confirm the exact extension point: extend
  `output_validator.go`, or add a `pkg/orchestration/leveling_executor.go`? Prototype the C3 loop
  on ONE task: cheap/local model → judge → escalate-to-strong on fail. Measure quality + cost/latency
  delta. This validates the whole thesis cheaply before committing the design.
- **Phase 1 — C1 tier abstraction + C2 adaptive-scaffolding pattern domain.**
- **Phase 2 — C3 escalation router**, wired to judge/confidence + provider pool + cost budget.
- **Phase 3 — C4 self-consistency.**
- **Phase 4 — eval harness (the proof).** Measure "weak model + leveling vs. weak model alone
  vs. frontier alone" across knowledge-bound and reasoning-bound task sets. This is both the
  credibility artifact and the regression guard. Reuse `pkg/evals`.

## Key design decisions to lock in Phase 0

1. **Placement**: extend `output_validator` vs. new leveling executor. (Lean: new executor that
   *composes* the validator, to keep concerns separate.)
2. **Tier taxonomy** + exact derivation from the catalog fields.
3. **Escalation policy interface**: thresholds, budget, ladder ordering, and where the policy is
   configured (agent YAML vs. pattern).
4. **Contribution path**: this is a Loom PR per the contribute-back model — coordinate with the
   Loom maintainer (ties to the same conversation as the graph-memory-scale question for
   loom-knowledge).

## Risks

| Risk | Mitigation |
|---|---|
| Overclaim ("any model = frontier") | Bound messaging to knowledge/format axes; publish the eval deltas honestly |
| Cost/latency blow-up from escalation | Hard budget ceiling (reuse per-agent cost budget); cap rungs; escalate only on judge-fail |
| Unreliable judge misroutes escalation | Use existing multi-judge aggregation; conservative thresholds; log judge confidence |
| Duplicating Loom's provider routing | Do NOT build a router — consume `GetLLMForRole` + provider pools |

## First concrete step

Phase 0 spike: a minimal `leveling_executor` prototype composing `output_validator` + a judge +
a two-rung escalation (local model → frontier model on fail), measured on one task. Everything
else follows from what that spike learns about the right extension point.

---

# Phase 0 + Phase 1 results (2026-08-15)

**Status**: ✅ Implemented — Phase 0 spike (leveling executor) and Phase 1 (C1 tier
abstraction), in the working tree, not yet committed. Off by default.

## What was built

| File | Contents |
|---|---|
| `pkg/llm/catalog/tier.go` | C1: `ModelTier` (`unknown \| local \| small-open \| mid \| frontier`), `TierOf(provider, model)`, `TierFromInfo(info)` |
| `pkg/llm/catalog/tier_test.go` | Table-driven tests over synthetic and real catalog entries, zero-alloc assertion, benchmark |
| `pkg/orchestration/leveling_executor.go` | Phase 0 spike: `LevelingExecutor`, `LevelingPolicy`, `TierPolicy`, `LevelingRung`, `LevelingReport`, `LevelingJudge` |
| `pkg/orchestration/leveling_executor_test.go` | 16 test functions / 27 subtests, all `-race` |

## Extension-point decision (design decision #1 — resolved)

**New `pkg/orchestration/leveling_executor.go` that composes `OutputValidator`**, as the plan
leaned. The code confirmed the lean twice over:

1. `OutputValidator.validate()` deliberately carries no LLM dependency
   (`output_validator.go:186-190` says semantic validation belongs to the caller). Teaching the
   validator to switch models would break that contract.
2. `RETRY_SESSION_MODE_ESCALATE`'s proto comment claims it "switches to orchestrator_llm if
   available", but the implementation (`effectiveMode`, `output_validator.go:222-230`) only
   remaps session modes — **no LLM upgrade happens anywhere in Loom today**. The comment is
   aspirational. The leveling executor is where that upgrade now actually lives.

The executor calls `ValidateAndRetry` unmodified for every rung, so same-model retry semantics,
feedback templates, session modes, and cooldowns are inherited rather than reimplemented.

## Tier taxonomy + exact derivation (design decision #2 — resolved)

Pure mapping over existing `catalog.Lookup` fields. No new store, no persistence, no tier
cache (a cache would go stale when `Register`/`DiscoverOllamaModels` swaps the source; the
static source is already memoized behind `sync.Once`, `source.go:66-91`).

Rules, applied in order (`tier.go`):

| # | Condition (catalog fields only) | Tier |
|---|---|---|
| 1 | `Lookup` returns nil | `unknown` |
| 2 | `CostPer_1MInputUsd == 0 && CostPer_1MOutputUsd == 0` | `local` |
| 3 | `IsReasoning && CostPer_1MOutputUsd >= 10.0` | `frontier` |
| 4 | `IsReasoning \|\| CostPer_1MOutputUsd >= 1.5` | `mid` |
| 5 | otherwise | `small-open` |

Thresholds are exported constants (`FrontierMinOutputCostUSD`, `MidMinOutputCostUSD`).
Spot checks against the live catalog: Opus 4.7 → frontier, Gemini 2.5 Pro → frontier (exactly
at the $10 boundary), Haiku 4.5 and o3 → mid, Mistral Small → small-open, every Ollama entry →
local (including DeepSeek R1: rule 2 beats rule 3 on purpose — self-hosted reasoning models are
still free to retry, which is the property leveling cares about).

Measured: `BenchmarkTierOf` ≈ 15 ns/op, 0 allocs/op, with a `testing.AllocsPerRun` test pinning
the zero-alloc claim.

## The flag and the zero-cost paths (the latency requirement)

Leveling is configured by a Go-level `LevelingPolicy` whose **zero value is disabled**; a nil
policy is also disabled (`leveling_executor.go:55-86`).

| Path | Work added on top of today's `ValidateAndRetry` | Extra LLM calls |
|---|---|---|
| Flag off (nil policy or `Enabled: false`) | One nil/bool check, then direct delegation (`leveling_executor.go:177-181`) | 0 |
| Flag on, primary is `frontier` / `mid` (default) / `unknown` | One memoized catalog lookup (two map hits, 0 allocs) + one tracer span (`:200-223`) | 0 — the judge is never consulted on this path |
| Flag on, low tier, first pass passes schema | One JSON-schema check (already free in the validator) re-applied once | 0 |
| Flag on, low tier, fenced-but-valid JSON | Free `extractJSONFromText` coercion instead of any retry/escalation | 0 |
| Flag on, low tier, no schema, no judge | Nothing — no signal exists, output stands (`:428-429`) | 0 |
| Flag on, low tier, failure signal | Escalation rungs, each budget-checked first | ≤ `MaxEscalations`, hard-capped by `MaxCostUSD` |

Signal precedence is fixed (`evaluate`, `leveling_executor.go:359-430`): schema (free) owns the
verdict when present and the judge is then never called; the judge (one LLM call, cost counted)
runs only when no schema exists; escalation fires only on an explicit failure signal.

## Escalation policy interface (design decision #3 — partially resolved)

```go
type LevelingPolicy struct {
    Enabled         bool                            // zero value: off
    ShortCircuitMid bool                            // default true
    MaxEscalations  int                             // rungs beyond the primary
    MaxCostUSD      float64                         // hard ceiling incl. judge spend; 0 = none
    Judge           LevelingJudge                   // optional; func, not an import of pkg/evals/judges
    TierPolicies    map[catalog.ModelTier]TierPolicy // per-key overrides of DefaultTierPolicies()
}
```

`TierPolicy` knobs: `RetryBudget` (✅ consumed — becomes the validator's `MaxRetries` when the
caller supplied none), `AggressiveCoercion` (✅ consumed — free JSON extraction before declaring
schema failure), `ScaffoldingDepth` (📋 reserved for C2, documented as not consumed).

`LevelingJudge` is a local func type because `pkg/evals/judges` imports `pkg/orchestration` —
importing it back would be a cycle. An adapter from `judges.Judge` is a one-liner at the call
site.

*Where the policy is configured* (agent YAML vs. pattern vs. proto) is **still open** — see
open questions.

## What the code contradicted in this plan

1. **"Reuse Loom's existing per-agent cost budget" — that budget does not exist.** Cost is
   computed per call (`Usage.CostUSD`) and accumulated per session
   (`types.Session.TotalCostUSD`), but nothing enforces a ceiling mid-run. The closest primitive
   is `EphemeralAgentPolicy.cost_limit_usd` (spawn gating in `pkg/collaboration/policy_evaluator.go`),
   which is per-role-spawn, not per-agent. The spike therefore enforces its own `MaxCostUSD`
   from the per-attempt `AgentResult.Cost.CostUsd` values it can see (validator attempts are
   wrapped so retries are counted; judge spend is included).
2. **`RETRY_SESSION_MODE_ESCALATE` never upgrades the LLM** despite its proto comment (see
   extension-point section). Worth a doc fix or an implementation in Phase 2.
3. `Agent.GetLLMForRole` is at `pkg/agent/agent.go:3608`, not :4110 (stale line ref), and there
   is no escalation `LLMRole` — adding `LLM_ROLE_ESCALATION = 6` is additive if Phase 2 wants it.
4. Workflow YAML cannot express `session_mode`/`feedback_template`/`cooldown_ms` today —
   `parseOutputRetryPolicy` (`workflow_config.go:728`) only reads `max_retries` and
   `include_valid_values`. Relevant when the leveling policy grows a YAML surface.

## Known limitations (honest)

- ⚠️ Coercion runs after `ValidateAndRetry` returns, so with a tier retry budget a
  fenced-but-valid payload burns same-model retries before free coercion rescues it. Fixing
  this cleanly needs a coercion hook inside `OutputValidator.validate` — deferred so this phase
  would not modify existing files.
- ⚠️ On the disabled path the validator's warnings are not surfaced (nil report by design).
- ⚠️ Judge spend counts against `MaxCostUSD`, but a judge skipped by the ceiling fails open
  (`Passed=true`, `BudgetExhausted=true`) — the output was never examined, so rejecting it
  would be an unearned verdict.
- 📋 Not built (follow-ups per plan phasing): C2 pattern domain YAML, C4 self-consistency,
  Phase 4 eval harness, wiring the executor into `WorkflowPattern`/proto/YAML (Phase 2), and a
  real quality/cost measurement on a live low-tier model — the spike's escalation loop is
  test-verified with mocks, not benchmarked against real models yet.

## Verification record (2026-08-15)

- ✅ `go test -tags fts5 -race ./pkg/llm/catalog/ ./pkg/orchestration/` — pass (16 new leveling
  test functions / 27 subtests; 5 new tier test functions; no regressions in either package).
- ✅ `go vet -tags fts5` and `gofmt -l` clean on both packages.
- ✅ `BenchmarkTierOf`: ~15 ns/op, 0 B/op, 0 allocs/op.
- ⚠️ `just lint` fails on this machine for reasons unrelated to this change: golangci-lint
  1.57.2 (built with go1.22.1) cannot read Go 1.26.5 export data ("could not load export data
  ... unsupported version: 2"), and fails identically on untouched packages such as
  `pkg/visualization`. Fix is a toolchain upgrade of golangci-lint, not a code change.

## Open questions for design decision sign-off

1. Where does `LevelingPolicy` live once it grows a config surface: agent YAML
   (`agent.memory.*`-style `*bool` gating idiom), workflow pattern YAML, or a new proto message
   on `PipelineStage`/`AgentTask`? Proto-first says this must be decided before Phase 2.
2. Should a schema-passing output on a low tier *also* be judged (paid semantic check), or does
   the free signal always own the verdict as implemented?
3. Tier thresholds ($10 / $1.5 per 1M output tokens) — constants today; make them configurable?
4. Should `unknown`-tier models short-circuit (current, conservative) or be treated as low-tier?
5. Contribution path / upstream PR coordination (unchanged from decision #4).
