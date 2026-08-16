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

The executor calls `ValidateAndRetry` for the primary rung, so same-model retry semantics,
feedback templates, session modes, and cooldowns are inherited rather than reimplemented.

⚠️ As written at Phase 1 this said "unmodified for every rung". Two corrections: escalation
rungs were never routed through the validator (Phase 2 executes them as one-shot
`LLMProvider.Chat` calls), and Phase 2c *did* modify `ValidateAndRetry` — it now returns a
`ValidationOutcome` and accepts a `CoerceFunc`. The retry semantics it inherits are still
the validator's, not reimplemented.

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
| Flag on, low tier, first pass passes schema | One JSON-schema check (already free in the validator) re-applied once ⚠️ *corrected at Phase 2b (the re-check cost ~9.2 µs / 203 allocs, not "free"); removed entirely in Phase 2c — the validator's verdict is now reused* | 0 |
| Flag on, low tier, fenced-but-valid JSON | Free `extractJSONFromText` coercion instead of any retry/escalation | 0 |
| Flag on, low tier, no schema, no judge | Nothing — no signal exists, output stands (`:428-429`) | 0 |
| Flag on, low tier, failure signal | Escalation rungs, each budget-checked first | ≤ `MaxEscalations`, hard-capped by `MaxCostUSD` |

Signal precedence is fixed (`evaluate`, `leveling_executor.go:359-430`): schema (free) owns the
verdict when present and the judge is then never called; the judge (one LLM call, cost counted)
runs only when no schema exists; escalation fires only on an explicit failure signal.

⚠️ Line refs in this section are as of Phase 1 and have drifted. Since Phase 2c the *primary*
rung's verdict comes from `primaryVerdict`, not `evaluate`; `evaluate` still governs the
escalation rungs, which do not go through the validator. The precedence rules themselves are
unchanged.

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
schema failure; since Phase 2c this runs *inside* the validator on the failing attempt, so it no
longer waits for the retry budget to drain), `ScaffoldingDepth` (📋 reserved for C2, documented
as not consumed).

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
   ✅ **Fixed 2026-08-16** — all three are parsed now; see "Phase 2d: the workflow YAML
   surface".

## Known limitations (honest)

- ✅ **Fixed in Phase 2c** (was: coercion runs after `ValidateAndRetry` returns, so with a tier
  retry budget a fenced-but-valid payload burns same-model retries before free coercion rescues
  it). `ValidateAndRetry` now takes a `CoerceFunc` and applies the rewrite on the failing
  attempt itself, so no retry is burned — see the Phase 2c section.
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

---

# Phase 2 results (2026-08-15)

**Status**: ✅ Implemented — proto config surface, proto→Go construction, and gated
executor wiring, in the working tree, not yet committed. Off by default.

## Design decisions closed this phase

| Open question (Phase 1) | Decision |
|---|---|
| #1 Where does the policy live? | A new `LevelingPolicy` proto message carried on **both** `PipelineStage` and `AgentTask`, mirroring how `OutputPolicy` is carried |
| #2 Judge a schema-passing low-tier output? | **No.** The free structural signal owns the verdict; the proto surface carries no judge reference at all, so config alone can never add a paid call to the pass path |
| #3 Tier thresholds configurable? | Yes — `frontier_min_output_cost_usd` / `mid_min_output_cost_usd` on the policy; `0` (or unset) means the existing constants, so absent config reproduces prior behavior exactly |
| #4 `unknown`-tier handling | Unchanged: short-circuit, never spend on a model we cannot classify |

## Proto surface (`proto/loom/v1/orchestration.proto`)

Three new messages, plus one new import (`loom/v1/agent_config.proto`, for `LLMRole`):

- `LevelingPolicy` — fields 1–8: `enabled` (field 1, `bool`, zero value off),
  `short_circuit_mid` (2, `optional bool`, absent ⇒ true), `max_escalations`
  (3, `optional int32`, absent ⇒ 1; explicit 0 disables escalation while keeping
  per-tier retry/coercion), `max_cost_usd` (4), `ladder` (5, `repeated LevelingRung`),
  `frontier_min_output_cost_usd` (6), `mid_min_output_cost_usd` (7),
  `tier_policies` (8, `map<string, LevelingTierPolicy>` keyed by tier name:
  `unknown`, `local`, `small-open`, `mid`, `frontier`).
- `LevelingRung` — `provider` (1), `model` (2), `role` (3, `LLMRole`).
- `LevelingTierPolicy` — `retry_budget` (1), `aggressive_coercion` (2),
  `scaffolding_depth` (3, 📋 reserved for C2; carried in config, consumed by nothing).

Carrier fields: `PipelineStage.leveling_policy = 8` (previous max was
`hitl_gate = 7`) and `AgentTask.leveling_policy = 5` (previous max was
`output_policy = 4`). No `reserved` ranges exist in the file; both numbers were
verified free before use. `buf format`/`buf lint` clean;
`buf breaking --against '.git#branch=main'` clean (additive only).

The two `optional` fields exist so that proto3 zero values do not invert the Go
defaults (`DefaultLevelingPolicy` has `ShortCircuitMid: true`,
`MaxEscalations: 1`); absent means "the default", present means "what you wrote".

## Construction (`pkg/orchestration/leveling_config.go`)

- `LevelingPolicyFromProto`: nil → `(nil, nil)` (disabled); `enabled=false` →
  disabled policy **without validating any other field**, so stored configs that
  were never enabled can never fail conversion. Negative `max_escalations`,
  `max_cost_usd`, `retry_budget`, or `scaffolding_depth` and unrecognized tier
  keys are rejected with errors, not clamped. The returned policy's `Judge` is
  always nil (decision #2 above).
- `resolveLevelingLadder`: rung 0 is the executing agent's own conversation;
  each proto rung resolves through exactly one of two existing lookups —
  `Agent.GetLLMForRole(role)` when `role` is set, else
  `Agent.GetProviderPool()[provider]`. No provider construction, no new router.
  Escalation rungs execute as one-shot `LLMProvider.Chat` calls: no tools, no
  session, no agent mutation (same precedent as the pipeline's merge-LLM
  validation calls). Rung spend comes from the provider's own `Usage.CostUSD`
  (catalog-priced by every client), so it counts against `max_cost_usd` without
  this package pricing anything.
- Tier thresholds flow through new `catalog.TierThresholds` /
  `catalog.TierOfWith` / `catalog.TierFromInfoWith` (`pkg/llm/catalog/tier.go`),
  with `catalog.ParseModelTier` as the exact inverse of `ModelTier.String()`.
  Non-positive threshold fields fall back to the existing constants; the
  zero-alloc property of tier lookup is preserved (~16 ns/op, 0 allocs/op,
  test-pinned). A 0 threshold is therefore inexpressible — documented on the
  struct and locked by a test.

## Wiring (where leveling is now reachable)

- `pkg/orchestration/pipeline_executor.go` — `executeFrom` branches on
  `stage.GetLevelingPolicy().GetEnabled()`. Disabled/absent: the pre-existing
  path runs unchanged; the only added work is that one proto getter check.
  Enabled: `executeStageWithLeveling` converts the policy, resolves the ladder,
  and runs `LevelingExecutor.Execute`; the legacy validation block is skipped
  (leveling owns validation; running both would validate twice). Leveling
  exhaustion is graceful degradation (warning in `validation_warnings`), not an
  error — matching the existing retries-exhausted behavior.
- `pkg/orchestration/parallel_executor.go` — same gate on
  `task.GetLevelingPolicy().GetEnabled()` in `executeTaskWithSpan`. One
  executor per task and tool-less rung calls keep it race-safe under
  concurrent tasks (race-detector tested).
- A stage combining `leveling_policy` with the legacy `validation_prompt` is a
  config error (leveling has no semantic-prompt signal; silently dropping the
  criteria would be worse). Legacy `output_schema`/`retry_policy` are honored:
  when the stage has no unified `output_policy`, they are synthesized into one
  for the leveling path only.

## The OutputPolicy safety constraint and how it is honored

`OutputPolicy` has been **persisted but never enforced**: the storage layer
round-trips it, and no executor read it before this phase. Users may therefore
have `OutputPolicy` values in their databases that have never had any effect.
Phase 2 does not change that:

- Leveling activates only when `leveling_policy.enabled` is explicitly true.
- A stage/task with an `OutputPolicy` but no leveling policy (or
  `enabled: false`) behaves exactly as before — no validator constructed, no
  validation, no retries, no judge.
- The stage/task `OutputPolicy` becomes the validation contract **only inside
  an explicitly enabled leveling run**.

Proven by `TestPipelineOutputPolicyInertWithoutLeveling` and
`TestParallelOutputPolicyInertWithoutLeveling`
(`pkg/orchestration/pipeline_leveling_test.go`): a schema the output violates,
with leveling absent or disabled, still passes through unvalidated with exactly
one agent call — the disabled case even carries a deliberately invalid policy
(bad tier key, negative bounds, unresolvable rung) to prove nothing reads it.

## Latency profile (honest)

| Path | Added work | Extra LLM calls |
|---|---|---|
| No leveling policy / `enabled: false` | One proto getter check per stage/task | 0 |
| Enabled, frontier/mid (default)/unknown primary | Policy conversion + one memoized catalog lookup + one tracer span | 0 |
| Enabled, low tier, output passes schema | The above + one free JSON-schema check ⚠️ *see Phase 2b: this was a second, non-free re-check; removed in Phase 2c* | 0 |
| Enabled, low tier, failure signal | Escalation rungs, each budget-checked first | ≤ `max_escalations`, capped by `max_cost_usd` |

## Verification record (2026-08-15, Phase 2)

- ✅ `buf format -w`, `buf lint`, `buf generate`,
  `buf breaking --against '.git#branch=main'` — all clean (no output, exit 0).
- ✅ `go build -tags fts5 ./...` — clean.
- ✅ `go test -tags fts5 -race ./pkg/orchestration/ ./pkg/llm/catalog/ ./pkg/agent/` — pass.
- ✅ `gofmt -l` and `go vet -tags fts5` clean on the touched packages.
- ⚠️ `just lint` still fails on this machine for the pre-existing environmental
  reason recorded in the Phase 0/1 section (golangci-lint 1.57.2 vs Go 1.26.5
  export data), on untouched packages too. `go vet` is the usable gate.

## Still not built (unchanged claims)

- 📋 C2 adaptive-scaffolding pattern domain (`scaffolding_depth` is carried in
  proto but consumed by nothing — documented on the field).
- 📋 C4 self-consistency.
- 📋 Phase 4 eval harness; no live low-tier-model quality/cost measurement yet —
  the escalation loop is test-verified with mocks.
- 📋 Workflow **YAML** surface: `workflow_config.go` does not parse
  `leveling_policy`, so YAML-defined workflows cannot enable leveling yet; only
  clients constructing the proto directly (gRPC/Go) can.
  ✅ **Shipped 2026-08-16** — a `leveling:` block on pipeline stages and parallel
  tasks now populates this proto; see "Phase 2d: the workflow YAML surface".
- 📋 A proto judge reference (deliberately excluded this phase; see decision #2).
- ⚠️ Escalation rungs run without agent tools (one-shot chat). A rung that
  needs tool use is future work.

---

# Phase 2b: the Ollama blocker, and the first live measurement (2026-08-15)

**Status**: ✅ Implemented (blocker fix) + ✅ Measured (first live run against a real
local Ollama instance). In the working tree, not committed. Leveling remains off by
default.

Everything before this section was verified with mocks. This section is the first time
the escalation loop ran against real models, and it changes the story: the mechanism
that delivered the measured improvement was **not** the escalation router.

## The blocker: leveling was inert on every real Ollama model

`catalog.TierOf` → `catalog.Lookup` → `staticSource.Lookup` normalized the *provider*
but did an **exact map lookup on the model ID**. The catalog's Ollama entries are keyed
on bare names (`llama3.1`, `deepseek-r1`), while Ollama identifies installed models as
`name:tag`. Every installed model therefore missed, returned `TierUnknown`, and
`TierUnknown` short-circuits leveling (`leveling_executor.go`, `shouldShortCircuit`).

The second half of the disconnect: `ModelRegistry.DiscoverOllamaModels`
(`pkg/llm/factory/model_registry.go:113`) does query `/api/tags` and does build correct
`ModelInfo` entries keyed on the full tagged name at 0/0 pricing — but it writes to the
registry's own `r.models` map. **Neither `catalog.Register` nor `DiscoverOllamaModels`
was called anywhere in production**; the only references to `Register` were doc comments
and the only callers of the discovery function were its own tests. Both ends of the
bridge existed and nothing crossed it.

Measured on this machine, `ollama list` reports six models; the classification before
the fix was `TierUnknown` for all six.

### Fix (a): tag-tolerant model-ID resolution

`catalog.BaseModelID` (`pkg/llm/catalog/catalog.go`) strips a trailing `:tag`, ignoring a
colon that precedes a `/` because that is a registry port and not a tag separator
(`localhost:5000/library/llama3`). The result is a substring, so it allocates nothing.

The fallback is applied in the **package-level `catalog.Lookup`**, not inside a `Source`
and not in tier derivation. Three reasons:

1. `Lookup`'s own doc comment already declared this the sanctioned layer: *"This is an
   exact-match lookup at each source; callers that need prefix or version-suffix matching
   must handle that above."* The `Source` interface contract stays exact-match.
2. Retrying at the chain level makes an exact match beat a tag-stripped match **including
   an exact match in a later chain entry**. Per-source stripping would let
   `StaticSource`'s bare `llama3.1` shadow a later source's exact `llama3.1:latest` when
   the static source comes first. Locked by
   `TestLookupExactMatchPreferredAcrossChainOrder`.
3. Putting it in tier derivation would fix `TierOf` only, leaving every other catalog
   consumer (context windows, capabilities) still blind to tagged IDs.

The fallback runs **only after a nil return**, so no lookup that succeeds today can
change answer, and an exact hit costs zero extra source queries
(`TestLookupFallbackDoesNotDoubleQueryOnHit`). The zero-alloc property of `TierOf` is
preserved on the tagged path (`TestLookupTaggedZeroAllocs`).

**What (a) does and does not fix**, against the six models actually installed here:

| Installed model | Tag-stripped base | Catalog entry exists? | Tier after fix (a) alone |
|---|---|---|---|
| `llama3.1:latest` | `llama3.1` | ✅ | `local` |
| `llama3.2:latest` | `llama3.2` | ✅ | `local` |
| `deepseek-r1:latest` | `deepseek-r1` | ✅ | `local` |
| `llama2:latest` | `llama2` | ❌ no entry under any tag | `unknown` |
| `llama3:latest` | `llama3` | ❌ catalog has 3.1/3.2/3.3/4, not `llama3` | `unknown` |
| `deepseek-v3.1:671b-cloud` | `deepseek-v3.1` | ❌ catalog has `deepseek-v3` | `unknown` |

So (a) fixes **3 of 6**. Tag stripping is not a synonym table and deliberately is not one
— guessing that `deepseek-v3.1` means `deepseek-v3` would invent pricing. This is why
(b) matters.

### Fix (b): discovery reaches the catalog `Source`

`pkg/llm/factory/catalog_source.go` adds:

- `snapshotSource` — an immutable `catalog.Source` over a deep-copied set of
  `ModelInfo`. Immutability is what makes it satisfy the interface's concurrency
  requirement; `ModelRegistry.models` has no lock and must not be aliased into a
  `Source`.
- `(*ModelRegistry).CatalogSource()` — a snapshot view, taken at call time.
- `OllamaCatalogSource(endpoint)` — runs the existing `DiscoverOllamaModels` and returns
  a `Source` scoped to the `ollama` provider only, so composing it ahead of the static
  catalog cannot shadow another provider.
- `RegisterOllamaCatalogSource(endpoint)` — the opt-in convenience:
  `catalog.Register(catalog.MultiSource{discovered, catalog.StaticSource()})`.

No parallel registry, no new router; the existing `Register`/`Source`/`MultiSource`
mechanism carries it. Properties held by tests:

- **Opt-in.** Nothing calls it at init, so importing the package never triggers I/O to
  `localhost:11434`.
- **Additive.** The built-in catalog stays reachable behind the discovered source
  (`TierOf("anthropic", "claude-opus-4-7")` is still `frontier` after registering).
- **Non-nesting.** Re-registering replaces the chain rather than wrapping it, so a
  process that re-discovers on a timer cannot grow an unbounded `Source` chain
  (`TestRegisterOllamaCatalogSource_RepeatedCallsDoNotNest`).
- **Fail-safe.** On an unreachable endpoint nothing is registered and the previous source
  stays in place (`TestRegisterOllamaCatalogSource_UnreachableLeavesSourceIntact`).
- **Race-safe.** `TestCatalogSource_ConcurrentLookupsAreSafe` under `-race`.

With (b), all six installed models classify as `local`, including `llama2:latest`, which
is what let it serve as the weak rung below.

### One behavior change this caused, and why it was accepted

`ResolveContextLimits` (`pkg/agent/model_context_limits.go:230`) tries `catalog.Lookup`
first and falls through to a legacy prefix table. A tagged Ollama ID used to miss the
catalog and take the legacy path; it now hits the catalog. For `llama3.1:70b-instruct`
the reserved-output budget changed **12800 → 8192**; the context window is 128000 either
way.

8192 is the catalog's documented `MaxOutputTokens` for `llama3.1`, and the untagged
`llama3.1` **already** resolved to 8192 before this change — the tagged and untagged
forms of the same model disagreed, and now they agree. The 12800 came from the legacy
table's flat 10% heuristic, i.e. it over-reserved for a model documented at 8192 max
output. `TestResolveContextLimits` was updated with that reasoning recorded inline, plus
a new case (`llama2:70b`) proving the legacy prefix path is still live for models absent
from the catalog.

## Experiment design

| | |
|---|---|
| Harness | `pkg/orchestration/leveling_live_ollama_test.go` |
| Gating | `LOOM_LIVE_OLLAMA=1` **and** a `/api/tags` reachability + model-presence probe; `t.Skip` otherwise, and skipped in `-short`. Never fails on a machine without Ollama. |
| Weak rung | `llama2:latest` (7B, Q4_0) — old enough to predate reliable JSON instruction-following, which is what makes the schema signal fire at all |
| Escalation rung | `llama3.1:latest` (8B, Q4_K_M) — still local, still free, instruction-tuned generations later |
| Excluded | `deepseek-v3.1:671b-cloud`. `assertLocalOnlyModel` hard-refuses any model whose name contains "cloud": routed through Ollama but cloud-hosted, so it can bill the operator. |
| Sampling | temperature 0.1, max 512 output tokens. **Not seeded — Ollama sampling is not deterministic across arms**, which is a real confound (see caveats). |
| Tasks | 10 countries; output must be JSON with exactly `capital` / `currency_code` / `calling_code`, `additionalProperties: false`, currency matched on `^[A-Z]{3}$`, calling code typed `integer` |
| Signal | JSON Schema only — the free structural signal, which is what leveling actually escalates on. No judge (the proto surface carries none by design). |
| Scoring | `schema` recomputed by the harness for every arm identically, so leveling-off arms face the same yardstick. `factual` additionally compares against a ground-truth table, so a schema-pass rate is never mistaken for an accuracy rate. |
| Arms | (1) weak, leveling off · (2) weak + leveling, ladder weak→strong · (3) strong, leveling off · (4) strong + leveling, escalation rung wired but expected never to fire |

Arm 4 is how the happy-path latency requirement is measured with a real model: if
enabling leveling costs a call when it is not needed, arm 4 makes more than 10 calls.

Hardware: Apple M4 Pro. Two full runs, `-race`, `-count=1`.

## Results — run 1

| Arm | Schema pass | Factual | LLM calls | Latency min / median / max | Escalations | Coercions | Cost |
|---|---|---|---|---|---|---|---|
| 1 weak, leveling off | 5/10 | 4/10 | 10 | 0.94s / 0.97s / 3.70s | 0 | 0 | $0.0000 |
| 2 weak + leveling | **10/10** | 7/10 | 16 | 0.92s / 0.97s / 13.22s | **1** | 0 | $0.0000 |
| 3 strong, leveling off | 10/10 | 8/10 | 10 | 0.86s / 0.90s / 0.96s | 0 | 0 | $0.0000 |
| 4 strong + leveling | 10/10 | 8/10 | **10** | 0.87s / 0.90s / 0.98s | 0 | 0 | $0.0000 |

Arm 1's 3.70s max is the first call of the run (cold model load), not a task effect.
Arm 2's 13.22s max is its single escalation trial: 3 weak calls + 1 strong call.

## Results — run 2

| Arm | Schema pass | Factual | LLM calls | Latency min / median / max | Escalations | Coercions | Cost |
|---|---|---|---|---|---|---|---|
| 1 weak, leveling off | 5/10 | 4/10 | 10 | 0.93s / 0.98s / 1.06s | 0 | 0 | $0.0000 |
| 2 weak + leveling | **10/10** | 8/10 | 15 | 0.98s / 1.81s / 1.98s | **0** | 0 | $0.0000 |
| 3 strong, leveling off | 10/10 | 8/10 | 10 | 0.86s / 0.94s / 0.98s | 0 | 0 | $0.0000 |
| 4 strong + leveling | 10/10 | 8/10 | **10** | 0.88s / 1.03s / 1.08s | 0 | 0 | $0.0000 |

**Cost is $0.0000 in every arm because Ollama models are priced 0/0 in the catalog. This
experiment demonstrates no dollar saving and says nothing about one.** The currency here
is model calls and wall-clock latency.

### Per-trial detail, run 2, arms 1 and 2

| Task | Arm 1 output | Arm 1 | Arm 2 calls | Arm 2 output | Arm 2 |
|---|---|---|---|---|---|
| Peru | `"calling_code": +51` | ✗ not valid JSON | 2 | `..."calling_code": 51` | ✓ ✓ |
| Norway | `..."calling_code": 47` | ✓ ✓ | 1 | same | ✓ ✓ |
| Vietnam | `..."calling_code": 84` | ✓ ✓ | 1 | same | ✓ ✓ |
| Morocco | `..."calling_code": 212` | ✓ ✓ | 1 | same | ✓ ✓ |
| Chile | `"calling_code": +56` | ✗ not valid JSON | 2 | `..."calling_code": 56` | ✓ ✓ |
| Hungary | `..."calling_code": 36` | ✓ ✓ | 1 | same | ✓ ✓ |
| Kenya | `"calling_code": "+254"` | ✗ wrong type | 2 | `..."calling_code": 254` | ✓ ✓ |
| Malaysia | `"calling_code": "+60"` | ✗ wrong type | 2 | `..."calling_code": 60` | ✓ ✓ |
| Croatia | `"currency_code": "HRK"` | ✓ schema, ✗ fact | 1 | `HRK` | ✓ schema, ✗ fact |
| Sri Lanka | `"currency_code": "LKR", "calling_code": "+94"` | ✗ wrong type | 2 | `"currency_code": "LKA"` | ✓ schema, ✗ fact |

Every failure mode the weak model produced was **format**, in exactly two shapes: a bare
`+51` (not valid JSON at all) and a quoted `"+254"` (valid JSON, wrong type). Both are
precisely what a JSON Schema catches for free.

## The happy-path requirement: measured, and met

`TestLiveOllamaLevelingHappyPathAddsNoCall`, same prompt, real model, leveling off vs on:

| | Schema | Primary calls | Escalation-rung calls | Wall clock |
|---|---|---|---|---|
| leveling off | ✓ | 1 | 0 | 0.92s |
| leveling on | ✓ | **1** | **0** | 0.62s |

Arm 4 confirms it across 10 tasks in both runs: **10 calls for 10 tasks**, zero
escalations, medians 0.90s (on) vs 0.90s (off) in run 1 and 1.03s vs 0.94s in run 2 —
inside run-to-run noise, and in the isolated test the enabled run was the faster of the
two. The harness asserts this rather than only reporting it: any arm-4 trial that passes
the schema on its first attempt and makes more than one call fails the test.

### What enabling leveling actually costs in Go, with no model involved

`BenchmarkLevelingEnabledOverhead`, synthetic instant `Execute`, 200000 iterations. **This
table is a historical record of what the code measured at Phase 2b; the redundancy it
identified was removed in Phase 2c and these enabled-path numbers no longer describe the
current code** (see "Phase 2c results" for the after numbers):

| Path | ns/op | B/op | allocs/op |
|---|---|---|---|
| disabled | 10479 | 11965 | 211 |
| enabled, `local` primary, output passes schema | 22114 | 24543 | 436 |
| enabled, `frontier` primary (short-circuits) | 21580 | 24464 | 435 |

One `validateJSONSchema` call in isolation measures **9211 ns / 11436 B / 203 allocs**.
That accounts for essentially the whole delta.

⚠️ **This corrected the Phase 1 and Phase 2 latency tables in this document, and has since
itself been superseded — see Phase 2c.** Both earlier tables state that the
enabled-but-short-circuiting path adds "one memoized catalog lookup + one tracer span". As
measured at Phase 2b it also added **a second, redundant JSON-Schema validation**:
`ValidateAndRetry` validated the output, and then the executor re-validated it — at
`leveling_executor.go` `:221-223` on the short-circuit path and inside `evaluate` (`:390`) on
the active path. The catalog lookup is ~16 ns; the redundant validation was ~9.2 µs, roughly
570× larger, and was the actual cost of enabling leveling. Leveling bookkeeping (span, report,
policy plumbing) is the remaining ~2.4 µs.

In practice that was 11.6 µs against a ~900,000 µs model call — **0.0013%**, which is why
arms 3 and 4 are indistinguishable. The requirement held comfortably even then. But the
earlier doc claim was wrong and the redundancy was real; removing it needed the validator to
surface its verdict to the caller, which was the same refactor the Phase 1 section had
deferred for the coercion hook. **Phase 2c did that refactor**, so the Phase 1/Phase 2 latency
tables ("one memoized catalog lookup + one tracer span") are accurate again for the
short-circuit path, and the enabled paths each lost 203 allocs / ~11,446 B.

## Verdict (honest, including what did not work)

**✅ The blocker was real and the fix is load-bearing.** Without (a)+(b),
`TierOf("ollama", "llama2:latest")` is `TierUnknown`, leveling short-circuits, and arm 2
is bit-for-bit arm 1. The harness asserts this as a precondition and fails loudly rather
than silently measuring nothing.

**✅ The format-gap claim held, at full strength.** 5/10 → 10/10 schema pass in both
runs, reaching the strong model's 10/10. The plan's "closes the format gap fully" is the
one claim this experiment supports without qualification.

**✅ The happy-path latency requirement is met.** Zero added model calls, confirmed live
across 20 arm-4 trials and one isolated A/B, and asserted by the test.

**❌ The escalation router — C3, the piece the plan called "the key new control loop" —
did essentially nothing.** It fired **once in 20 trials** (run 1) and **zero times in 10**
(run 2). Every other repair came from **same-model retry with schema feedback**, which is
pre-existing `ValidateAndRetry` behavior; leveling's only contribution there was turning
it on, by supplying `TierPolicy.RetryBudget: 2` for the `local` tier. On this task, ~95%
of the measured benefit is attributable to *"activate the retry loop Loom already had for
low-tier models"* and not to *"escalate to a stronger model."*

That is a genuinely deflating result for the feature's headline mechanism, and it should
not be reported as an escalation win. It is also a cheap win worth keeping: the whole
gain came from the cheaper rung of the ladder.

**⚠️ Free coercion never fired either.** 0 coercions across 40 trials. `llama2` did not
wrap its JSON in prose or fences on this task — it emitted almost-correct JSON with a
type error. `AggressiveCoercion` was untested by this experiment, not validated by it.

**⚠️ The schema signal is blind to correctness, and satisfying it can destroy a correct
fact.** Sri Lanka, run 2: arm 1 produced `"currency_code": "LKR"` (correct) with
`"calling_code": "+94"` (wrong type → schema fail). Arm 2's retry produced valid JSON
with `"currency_code": "LKA"` — **wrong**. Leveling converted a right-answer/wrong-format
into a wrong-answer/right-format and reported `Passed: true`. Nothing in the free signal
can see this. Any claim that leveling "improves quality" must be read as "improves
schema conformance", which is all it measures.

**⚠️ The factual numbers move for a mechanical reason, not a reasoning one.** 4/10 →
7–8/10 factual looks like a large quality gain, but a `+51` output scores factually wrong
because it is unparseable. Fixing the format un-masks knowledge the weak model already
had. This is consistent with the plan's claim (knowledge/format gaps, not reasoning) and
is **not** evidence that leveling improved reasoning. Nothing here tests the reasoning
axis at all.

**⚠️ Neither model has current knowledge, and leveling cannot manufacture it.** Every arm
answered `HRK` for Croatia, which adopted the euro in 2023. When no rung on the ladder
knows the answer, escalation has nowhere to go. (Sri Lanka's `Colombo` vs the official
`Sri Jayawardenepura Kotte` is arguably the ground-truth table being pedantic rather than
a model error; it is scored as a miss in every arm, so it does not favour any arm.)

**⚠️ Confounds, stated plainly.** Ollama sampling is not seeded, so arms did not see
identical draws — visible in run 1, where arm 1 and arm 2 both got Peru wrong in
*different* ways (`+51` vs a schema-valid but factually wrong `512`). N=10 per arm over
two runs is enough to see a 5/10→10/10 format effect and nowhere near enough to resolve
1–2 task differences in the factual column. `llama2` (7B) vs `llama3.1` (8B) is a
generation gap in instruction tuning, not a parameter-count gap; a larger escalation rung
would likely make C3 look better, and was not tested.

**Did leveling earn its latency on this hardware with these models?** For a
schema-bearing task on a weak local model: **yes** — 5/10 → 10/10 schema conformance for
+50–60% model calls (15–16 calls per 10 tasks), median latency 0.97s → 0.97s–1.81s, and
still under the *unbounded* cost of a wrong-format answer reaching a downstream consumer.
When it was not needed it cost nothing measurable, which was the hard requirement. But it
earned that latency through same-model retry, not escalation — so the honest framing of
this result is **"leveling's cheap rung works; its expensive rung is unproven"**, not
"escalation works".

## Verification record (2026-08-15, Phase 2b)

Exit codes below were observed by redirecting to a file and reading it, never via a pipe
into `head`/`grep` (which reports the pipeline's exit code, not the command's).

- ✅ `go build -tags fts5 ./...` — exit 0, no output.
- ✅ `go test -tags fts5 -race -short ./...` — exit 0, 95 packages `ok`, 0 failures.
- ✅ `go test -tags fts5 -race ./pkg/llm/catalog/ ./pkg/llm/factory/ ./pkg/orchestration/` — exit 0.
- ✅ `go vet -tags fts5` on `./pkg/orchestration/ ./pkg/llm/catalog/ ./pkg/llm/factory/` — exit 0.
- ✅ `gofmt -l ./pkg ./cmd ./internal` — exit 0, no files listed.
- ✅ Live harness skips by default: `go test -tags fts5 -race -run TestLiveOllama ./pkg/orchestration/`
  → both tests `SKIP`, exit 0, with no Ollama contact.
- ✅ Live runs: `LOOM_LIVE_OLLAMA=1 go test -tags fts5 -race -run 'TestLiveOllamaLeveling$' -v -count=1 ./pkg/orchestration/`
  — exit 0, twice (54.70s and comparable).
- ✅ No proto change this phase, so no `buf` regeneration was required.
- ⚠️ `just lint` / `just check` **were not run and are not claimed**. golangci-lint 1.57.2
  on this machine cannot read Go 1.26.5 export data and fails identically on untouched
  packages; this is the same environmental failure recorded in the Phase 0/1 section.
  `go vet` + `gofmt` are the usable gates.

## Follow-ups this phase opened

- ✅ **Done in Phase 2c.** Remove the redundant second `validateJSONSchema` on the leveling
  path (~9.2 µs and 203 allocs per execution). `ValidateAndRetry` now returns a
  `ValidationOutcome` the executor reuses; the coercion hook landed in the same refactor.
  Measured: each enabled path lost exactly 203 allocs and ~11,446 B.
- 📋 Re-run the measurement with a genuinely larger escalation rung (e.g. a 70B local
  model, or a hosted frontier model) before making any claim about C3. As measured, C3 is
  unproven, not validated.
- 📋 Design a task set where the failure mode is prose-wrapped or fenced JSON, so
  `AggressiveCoercion` is actually exercised.
- 📋 Add a seeded/deterministic sampling option to the harness so arms see identical
  draws, or raise N enough that the factual column means something.
- 📋 Decide whether `RegisterOllamaCatalogSource` should be wired into `loom-server`
  startup behind a flag. Today it is opt-in and **nothing calls it**, so out of the box
  leveling still short-circuits on `llama2:latest` and `llama3:latest`.
- 📋 `catalog.LookupPricing` (`pkg/llm/catalog/pricing.go:46`) has the same exact-match
  blind spot and was deliberately **not** changed — it is a separate index on a
  provider-cost path, outside the tier-resolution scope of this phase. Tagged models
  still miss it and fall back to provider-local rates.

---

# Phase 2c results (2026-08-15): validator verdict refactor + in-validator coercion

**Status**: ✅ Implemented — the refactor this document deferred twice (Phase 1's coercion hook,
Phase 2b's redundant-validation follow-up) is done. In the working tree, not committed. Leveling
remains off by default, and the disabled path is byte-for-byte unchanged.

Both deferrals were the *same* refactor: the validator knew the verdict and threw it away, so the
executor had to re-derive it. Surfacing the verdict fixes the redundancy and makes room for the
coercion hook in one change.

## What changed

**1. `ValidateAndRetry` returns its verdict** (`pkg/orchestration/output_validator.go`).

```go
type ValidationOutcome struct {
    Passed          bool     // final attempt satisfied the policy (vacuously true for a nil policy)
    Err             error    // validation error from the final attempt; nil when Passed
    Warnings        []string // one entry per failed attempt
    CoercionApplied bool     // coerce produced the pass; result carries the rewrite
}

func (v *OutputValidator) ValidateAndRetry(
    ctx context.Context, policy *loomv1.OutputPolicy,
    execute ExecuteFunc, feedback FeedbackFunc,
    originalPrompt string, workflowID string,
    coerce CoerceFunc,
) (*loomv1.AgentResult, ValidationOutcome, error)
```

A non-nil `error` still means execution or the context failed, not that validation failed — an
output that fails every attempt comes back with `Passed=false` and a nil error, as before.

**2. Coercion moved inside the retry loop.** The new
`CoerceFunc func(output string) (coerced string, ok bool)` (nil = no coercion) is consulted on a
schema failure *before* the attempt is written off. When the rewrite validates, the validator
rewrites `result.Output` in place, sets `CoercionApplied`, and returns a pass immediately: no
retries burned, and no warning recorded for the rescued attempt.

**3. The executor reuses the verdict instead of re-deriving it**
(`pkg/orchestration/leveling_executor.go`).

| Path | Before | After |
|---|---|---|
| Disabled | `ValidateAndRetry`, nil report | Unchanged — passes nil `coerce`, discards the outcome |
| Short-circuit | `ValidateAndRetry`, then `validateJSONSchema` again to set `report.Passed` | `report.Passed = outcome.Passed`; the re-check is deleted |
| Active, primary rung | `ValidateAndRetry`, then `evaluate` re-validated the schema *and* ran a second coercion pass | New `primaryVerdict` helper reads the outcome; no re-validation, no second coercion |
| Active, escalation rungs | `evaluate` | Unchanged — rungs are one-shot `Chat` calls that never touch the validator, so `evaluate` still owns their schema check and coercion |

The coerce hook is handed to the validator only when `TierPolicy.AggressiveCoercion` is on *and*
a schema is present; it wraps the existing `extractJSONFromText`. The judge branch, previously
duplicated between the primary and escalation paths, was extracted into a shared `judgeVerdict`
helper so the budget ceiling and fail-open rules live in one place.

## Measured: the redundancy is gone

`BenchmarkLevelingEnabledOverhead`,
`go test -tags fts5 -run '^$' -bench BenchmarkLevelingEnabledOverhead -benchtime=2s -count=3
./pkg/orchestration/`, Apple M4 Pro, same machine and session for both halves:

| Path | ns/op before (3 runs) | ns/op after (3 runs) | B/op before → after | allocs/op before → after |
|---|---|---|---|---|
| disabled | 9999 / 10042 / 10078 | 10139 / 10394 / 11400 | 11965 → 11964–11965 | 211 → **211** |
| enabled, `local` primary, passes schema | 20844 / 20892 / 21983 | 11805 / 14316 / 14589 | 24543–24544 → 13097–13098 | 436 → **233** |
| enabled, `frontier` primary (short-circuits) | 20688 / 20824 / 20946 | 11713 / 11721 / 11767 | 24463–24464 → 13017–13018 | 435 → **232** |

The **alloc delta is the clean signal**: each enabled path lost exactly **203 allocs and 11,446
B** — precisely the one `validateJSONSchema` call the Phase 2b section measured in isolation at
9211 ns / 11436 B / 203 allocs. One redundant call removed, one call's worth of allocations gone,
on both enabled paths.

ns/op medians: enabled-`local` 20.9 µs → ~14 µs (noisy run to run, 11.8–14.6 µs);
enabled-`frontier` ~20.8 µs → ~11.7 µs. Enabled-vs-disabled overhead is now **~1.3–4 µs** of
leveling bookkeeping (span, report, policy plumbing, catalog lookup) instead of ~11 µs. The
disabled path is unchanged, which was the requirement — the small ns/op spread there
(10.1–11.4 µs vs 10.0–10.1 µs) is run-to-run noise on an identical code path, and the
211 allocs/op is byte-for-byte identical.

Against a ~900,000 µs model call this was already 0.0013% and is now less. The point of the
change is not latency headroom; it is that the code no longer does the same work twice, and that
coercion now rescues on the attempt that failed.

## One deliberate behavior change

On the **enabled active path**, a coercion-rescued attempt no longer contributes an
`"attempt N: ..."` entry to `report.Warnings`. Previously the validator recorded the failure and
returned, and the executor coerced afterwards — so the warning survived even though the output
was rescued for free. Now the rescue happens before the warning is recorded, so a run that was
fully repaired by coercion reports empty warnings and `CoercionApplied=true`.

This is intended: a warning is a record of a failure the caller should know about, and a free
rewrite that satisfied the schema on the same attempt is not one. It is pinned by tests rather
than left to chance — the fenced-but-valid case with retry budget 3 asserts **exactly one execute
call, empty warnings, `CoercionApplied=true`**.

Anyone parsing `validation_warnings` to count attempts should know this: on the leveling path the
count is now failures that were *not* rescued.

## Breaking change (stated plainly)

`ValidateAndRetry` is exported and its signature changed — one added parameter, one added return
value. **No back-compat wrapper was kept.** The only callers in the repo were the leveling
executor and tests, all updated; an external caller would not compile. Given the validator's
Phase 2 status ("persisted but never enforced" — no executor read `OutputPolicy` before Phase 2),
an out-of-tree caller is unlikely, but the break is real and is not hidden behind a shim.

## Limitation fixed

The Phase 1 "Known limitations" bullet — *coercion runs after `ValidateAndRetry` returns, so with
a tier retry budget a fenced-but-valid payload burns same-model retries before free coercion
rescues it* — is **✅ fixed**. Coercion is attempted on the failing attempt itself, so the retry
budget is never spent on a payload a free rewrite could have saved. That bullet has been updated
in place.

Note this fixes an *ordering* bug that Phase 2b never observed live: `llama2` emitted
almost-correct JSON with a type error, not fenced or prose-wrapped JSON, so 0 coercions fired
across 40 trials. `AggressiveCoercion` is still exercised only by unit tests, and the Phase 2b
follow-up to design a task set that actually produces fenced JSON remains open.

## Verification record (2026-08-15, Phase 2c)

- ✅ `go build -tags fts5 ./...` — exit 0.
- ✅ `go vet -tags fts5 ./pkg/orchestration/` — exit 0; `gofmt -l pkg/orchestration` — no files
  listed.
- ✅ `go test -tags fts5 -race ./pkg/orchestration/ ./pkg/llm/catalog/ ./pkg/llm/factory/` — all
  `ok`, exit 0.
- ✅ Disabled path pinned unchanged by `TestPipelineOutputPolicyInertWithoutLeveling` and the
  leveling executor's "matches direct `ValidateAndRetry`" subtest — both passing.
- ✅ New table-driven tests in `pkg/orchestration/output_validator_test.go` cover the outcome
  struct, warning format, coercion skipping the retry prompt, execution-error and
  canceled-context outcomes, `coerce` not being consulted on a pass, and the primary verdict
  coming from the validator rather than a re-check.
- ✅ Full-repo `go test -tags fts5 -race ./...` — exit 0, 95 packages `ok`, 0 failures, 0 data
  races (result read from a redirected log file, not a pipe).
- ⚠️ `just lint` **was run and still fails environmentally** — golangci-lint 1.57.2 cannot read
  Go 1.26.5 export data and errors even on the stdlib (`could not import sync/atomic ...
  unsupported version: 2`), the same failure recorded in the Phase 0/1 and Phase 2b sections.
  `go vet` + `gofmt` are the usable gates.

## What this phase did not change

- 📋 C2 adaptive-scaffolding pattern domain, C4 self-consistency, Phase 4 eval harness — all
  still unbuilt.
- 📋 No new live-model measurement. The benchmark numbers above are Go-level only; C3 escalation
  remains **unproven**, exactly as Phase 2b concluded.
- 📋 No proto change, so no `buf` regeneration was required. The YAML surface still cannot enable
  leveling. ✅ **Changed 2026-08-16** — the YAML surface can enable leveling as of Phase 2d
  (still no proto change).
- ⚠️ The disabled path still discards the outcome and returns a nil report, so validator warnings
  are not surfaced there. That Phase 1 limitation stands by design.

---

# Phase 3 results — reasoning-bound experiment (2026-08-15)

**Status**: ✅ Measured — 5 arms × 30 identical seeded problems, live local Ollama, 39m8s
wall-clock, $0. Harness: `pkg/orchestration/leveling_reasoning_live_test.go` (env-gated on
`LOOM_LIVE_OLLAMA=1`, skipped in `-short`). Per-trial data:
`docs/experiments/reasoning_arms.jsonl` (150 rows) and `docs/experiments/judge_probe.jsonl`
(20 rows), committed alongside this doc.

## Hypotheses

- **H1**: a JSON schema cannot detect a wrong-but-well-formed answer, so schema-only leveling
  never escalates on reasoning errors.
- **H2**: with a judge signal, escalation becomes reachable and accuracy approaches the
  strong-model ceiling.
- **H3**: a judge critique fed back to the SAME weak model (self-critique rung) recovers some
  accuracy before a stronger model is paid for.

## Design

Task: `arith_chain` L11 — `(a+b)*c−d`, a,b∈[10,49], c∈[11,19], d∈[10,99] — ground truth
computed in Go, never model-judged. Problems reproduced from the calibration generator via
`crc32("arith_chain|11|<index>")`; all arms saw the identical 30 problems. Seed 0 and
temperature 0.1 on every model via the new `LLMConfig.seed` (commit 40a9335), so arms are
paired problem-by-problem: arms 1 and 2 returned byte-identical answers on all 30 problems.

Output schema `{"answer": <integer>}` — satisfiable by a wrong answer, deliberately.

Judge: an **oracle** (pure Go, compares to ground truth) so ladder mechanics are measured
without judge noise. Its critique never leaks the answer — exact reason string: *"the
arithmetic in the worked solution is incorrect; recompute each step carefully and give only the
corrected JSON."* A secondary probe measured a real local judge separately (below).

Models: weak `llama2:latest` (7B), strong `deepseek-r1:latest`, judge probe `llama3.1:latest`.
The cloud-routed model is structurally refused by `assertLocalOnlyModel`.

## Results

| Arm | Accuracy | Schema pass | Calls (by rung) | Esc | Latency med/max |
|---|---|---|---|---|---|
| 1 llama2, leveling off | 12/30 (40%) | 0/30 raw | 30 (r0=30) | 0 | 1.9s / 3.6s |
| 2 llama2 + schema-only | 12/30 (40%) | 30/30 (all via coercion) | 30 (r0=30) | **0** | 2.0s / 2.1s |
| 3a llama2 + oracle judge → r1 | 27/30 (90%) | — | 48 (r0=30, r1=18) | 18 | 25.1s / 87.6s |
| 3b … → llama2 self-critique → r1 | 29/30 (97%) | — | 66 (r0=30, r1=18, r2=18) | 36 | 23.3s / 85.3s |
| 4 r1 alone | 30/30 (100%) | 30/30 | 30 (r0=30) | 0 | 18.0s / 45.8s |

Total model time: arm 3a 859.8s, arm 3b 778.5s, arm 4 591.2s — **on this 60%-wrong mix,
leveling with escalation cost MORE total compute and wall-clock than simply running the strong
model, and scored lower.**

## Verdicts

**H1 — CONFIRMED, exactly.** Arm 2 escalated zero times in 30 problems; all 18 wrong answers
passed the schema (after free coercion rescued formatting on every reply). Accuracy identical
to arm 1 problem-by-problem. The free signal is structurally blind to reasoning errors. This
also retroactively explains Phase 2b: C3 wasn't underperforming — it was unreachable.

**H2 — CONFIRMED, with a cost asterisk.** Oracle-judged escalation fired on exactly the 18
wrong answers (no misroutes, by construction) and lifted accuracy 40% → 90%. The 3 misses were
r1 escalation replies that came back unparseable (empty), not wrong answers.

**H3 — FAILED.** Self-critique repair rate: **0/18 (0.0%)**. Given an answer-free critique,
llama2 never once fixed its own arithmetic; all 18 proceeded to r1 anyway. Arm 3b's higher
total (29 vs 27) is r1 reply-parsing variance at rung 2, not self-repair — the rung itself
contributed 18 extra weak-model calls and ~2s per wrong problem for zero repairs. A
self-critique rung should NOT become a default TierPolicy knob on this evidence.

**The finding that governs C3's viability: real local judges are not good enough.**
`llama3.1:latest` judging (problem, weak-answer) pairs agreed with ground truth **7/20 (35%)**
— 13 false PASSes (wrong answers it would have let through un-escalated), 0 false FAILs. With
that judge, arm 3a's 90% would collapse most of the way back toward arm 1. The plan's
"unreliable judge misroutes escalation" risk is now quantified: on arithmetic verification a
7–8B judge is worse than a coin flip at catching wrongness.

**Judge cost on the happy path**: 27 of 48 judge invocations in arm 3a (56%) were on outputs
that were already correct. With a real judge each is one extra LLM call (~0.8s measured with
llama3.1) added to every problem the weak model already got right — a direct violation of the
no-added-latency happy-path requirement. An oracle/programmatic judge costs nothing; an LLM
judge cannot be free.

## What this means for the feature

1. **Leveling's economics depend on the base wrong-rate.** At 60% wrong, escalate-on-fail costs
   more than strong-alone. Break-even here is roughly a weak model that is right ≳⅔ of the
   time; below that, just use the strong model.
2. **C3 is viable only where a programmatic verdict exists** — schema, tests, checksums, an
   oracle. That is precisely the free-signal regime the executor already prioritizes. For
   semantic wrongness with no programmatic check, local-LLM judges are the binding constraint
   (35% agreement), and no ladder can outrun its signal.
3. **The format-gap result from Phase 2b stands** — coercion rescued 30/30 malformed replies
   here too, for free.
4. 📋 Follow-ups: r1's 3 unparseable escalation replies (empty content under the retry prompt)
   deserve a look at `num_predict`/prompt shape; judge quality vs. judge size is unmeasured
   above 8B; C2/C4 remain unbuilt and this evidence lowers C4's priority (self-consistency
   voting shares H3's weakness: the weak model's votes cluster on plausible-wrong answers).

## Confounds, stated

Single task family (arithmetic); single weak model; N=30 (arm-level 95% CIs ≈ ±17pp); seed=0
temp=0.1 throughout — deterministic-ish, so these numbers are one draw, not a distribution;
oracle judge is an upper bound no real judge reached; r1's unparseable replies counted against
arms 3a/3b (they are real failures of the escalation path, not scoring artifacts).

---

# Phase 3b results — critic-quality and scaffolding probes (2026-08-15)

**Status**: ✅ Measured — two targeted probes following Phase 3, run against the same 30-problem
set and the same 20 judge pairs. Scripts and per-trial JSONL committed in `docs/experiments/`
(`probe_r1_judge.py`, `probe_scaffold.py`, `r1_judge_probe.jsonl`, `scaffold_probe.jsonl`).

## Probe 1 — a reasoning model as the judge

Identical 20 (problem, weak-solution) pairs and identical verdict prompt as the Phase 3
llama3.1 probe:

| Judge | Agreement | False pass | False fail | Latency med/max |
|---|---|---|---|---|
| llama3.1 (8B, non-reasoning) | 7/20 (35%) | 13 | 0 | 0.8s |
| deepseek-r1 (reasoning) | **20/20 (100%)** | 0 | 0 | 30.9s / 74.2s |

**Judge quality is a model-selection problem, not a local-model limit.** A local reasoning
model verified flawlessly where a same-scale non-reasoning model was worse than a coin flip.
This revises Phase 3's C3 verdict: judge-driven escalation is viable with a reasoning-class
critic. The surviving constraint is economics — an r1 verdict (31s median) costs more than an
r1 solution (18s median) on this task, so critic-driven ladders only pay where **verification
is cheaper than generation** (long-form output, code with tests, executable SQL). Cross-critique
between non-reasoning peers remains contraindicated (13 false passes).

## Probe 2 — forced-step scaffolding (C2's hypothesis, tested before building C2)

Same 30 problems as the arms; llama2 with two imposed procedures. Baseline: 12/30 (40%).

| Variant | Accuracy | Fixed vs baseline | Broke vs baseline |
|---|---|---|---|
| S1 — one step per line, re-check each line | 8/30 (27%) | 0 | 4 |
| S2 — forced partial-product decomposition + self-check | **1/30 (3%)** | 0 | 11 |

**Scaffolding was monotonically harmful: the more structure imposed, the worse the result.**
Zero problems were fixed by either variant. The captured failure mode (i=0): llama2 executed
all seven S2 steps fluently while mangling their semantics — it split the wrong operand
(`TENS = 31 * 10` instead of splitting the multiplier 18), invented `ONES = 31 - 1 = 30`,
computed the equivalent of 31 × 11, and its CHECK step then **passed**, because a self-check
without ground truth only confirms the internal consistency of the wrong chain.

Procedure adherence is itself a reasoning task. A model that cannot bind variables in a recipe
cannot be leveled by giving it a longer recipe. Caveat: two prompt designs do not exhaust the
scaffold space — but the direction was consistent and the failure mode mechanistic, so C2
(proactive scaffolding for low tiers) is **deprioritized on evidence**, not just untested.

## The unified picture after five measurements

For a 7B non-reasoning model, nothing that tries to extract more reasoning from the model
itself worked: same-model retry repaired 0 reasoning errors, self-critique repaired 0/18,
light scaffolding −13pp, heavy scaffolding −37pp. What worked: free format repair (coercion,
30/30), and genuine escalation to a reasoning model behind a trustworthy signal — programmatic
(oracle: 40%→90%) or, now, a reasoning-model critic (100% agreement). The weak model's
reasoning ceiling is its weights; the loop can route around the ceiling but not raise it.

📋 Open: knowledge-gap track (vector-store augmentation) — the one intervention that converts
reasoning problems into lookup problems and the plan's one untested original claim; and a
verify-cheap task family where the r1-critic economics flip positive.

---

# Phase 4 results — knowledge-bound experiment, vector-store augmentation (2026-08-15)

**Status**: ✅ Measured — 5 arms × 30 identical questions over 200 fictional device records,
live local Ollama, 5m28s, $0. Harness: `pkg/orchestration/leveling_knowledge_live_test.go`
(env-gated, skipped in `-short`; corpus generator pinned by non-live tests in
`leveling_knowledge_gen_test.go`). Per-trial data: `docs/experiments/knowledge_arms.jsonl`
(150 rows) and `knowledge_corpus.jsonl` (200 rows).

## Design

The corpus is **fictional by construction** (deterministic splitmix64 records seeded via
`crc32("knowledge|<index>")`), so no model can know the answers from weights — any lift over
the no-context floor is pure retrieval, cleanly separated from model capability. Questions ask
one integer attribute of one device (`{"answer": <integer>}`, same contract and parse machinery
as Phase 3), with `{"answer": -1}` as an explicit abstention escape so hallucination and
abstention are distinguishable. Near-collision device IDs make retrieval non-trivial.
Retrieval arms use Loom's own machinery: BM25 via the graph-memory FTS5 `Recall()` path
(questions must be quoted-OR rewritten — a raw question is an FTS5 syntax error), vector via
the previously **uncalled** `VectorRecall` with embeddings from `llama3.2:latest` through
Ollama's OpenAI-compatible endpoint (3072 dims, probed not hardcoded — the `dimensions` field
silently truncates if set wrong). Seed 0, temperature 0.1.

## Results

| Arm | Accuracy | Abstained | Hallucinated | Retrieval hit@5 | Latency med |
|---|---|---|---|---|---|
| 1 llama2, no context | 0/30 | 21 | **0** | — | 1.1s |
| 2 llama2 + oracle fact | **30/30** | 0 | 0 | 30/30 | 0.4s |
| 3 llama2 + BM25 top-5 | **30/30** | 0 | 0 | 30/30 | 0.8s |
| 4 llama2 + vector top-5 | 15/30 | 15 | 0 | 15/30 | 0.8s |
| 5 deepseek-r1, no context | 0/30 | 29 | 1 | — | 7.2s |

## Findings

1. **The knowledge-gap claim is CONFIRMED, completely.** Weak model + BM25 retrieval: 100%.
   Strong model alone: 0%. This is the only experiment in the program where the weak model
   beats the strong one outright — knowledge is the gap where weights genuinely are not the
   ceiling.
2. **Utilization is not a bottleneck.** Across every arm, whenever the gold fact was in the
   prompt the weak model answered correctly: 75/75 (oracle 30, BM25 30, vector hits 15).
   Every failure in the entire run was retrieval's, none the model's.
3. **BM25 was flawless; chat-model embeddings were the weak link.** Vector recall@5 was 50%
   over near-identical sentences, and each miss became an honest abstention. Caveat: exact-ID
   questions favor lexical matching — paraphrased queries would narrow BM25's edge; a dedicated
   embedding model (e.g. nomic-embed-text) was not tested because it is not installed.
4. **Neither model confabulated fictional facts.** Hallucination: llama2 0/30, r1 1/30, under
   "answer only from provided information" prompting with an abstention escape. The floor arms
   abstained or failed to parse instead of inventing values.
5. **Retrieval made the weak model faster, not slower.** Median 0.4-0.8s with context vs 1.1s
   without (an unanchored model rambles; a grounded one answers). Retrieval itself cost ~7ms
   (BM25) / ~140ms (vector embed+search) per question.

## The completed gap matrix

| Gap | Verdict across Phases 2b-4 | Mechanism |
|---|---|---|
| Format | ✅ closed, free | coercion + schema retry (30/30 twice) |
| Reasoning | ❌ cannot be closed locally — only routed around | escalate to a reasoning model behind a trustworthy signal; retry/self-critique/scaffolds all failed |
| Knowledge | ✅ closed fully, weak beats strong | retrieval injection; BM25 already in Loom |

For loom-knowledge: start delivery on the FTS5/BM25 path Loom already ships (it won here),
treat embedding-model choice as the make-or-break decision for the vector path, and expect
context utilization — the part loom-knowledge cannot control — to be a non-problem.

## Confounds, stated

Single-hop attribute lookup (multi-hop synthesis untested); 200-record corpus; exact-ID
queries favor BM25; embeddings from a chat model rather than a dedicated embedding model;
one seeded draw; `VectorRecall` is brute-force O(n) cosine — fine at 200 records, unmeasured
at scale.

---

# Phase 2d: the workflow YAML surface (2026-08-16)

**Status**: ✅ Implemented — the config-surface gap Phase 2 left open ("YAML-defined workflows
cannot enable leveling") is closed. In the working tree, not committed. **No proto change was
needed**: the messages and both carrier fields already existed, so this is purely a
YAML→proto loader change. Leveling remains off by default.

Chronologically this lands after Phase 4, but it completes Phase 2's config-surface work, hence
the number.

## What shipped

| File | Contents |
|---|---|
| `pkg/orchestration/workflow_config_leveling.go` (new) | `parseLevelingPolicy`, `parseLevelingLadder`, `parseLevelingTierPolicies`, `parseLLMRoleName`, `parseRetrySessionMode`, and the presence-reporting scalar readers (`yamlBoolField`, `yamlStringField`, `yamlInt32Field`, `yamlFloat64Field`) |
| `pkg/orchestration/workflow_config.go` | `parseOutputPolicy` (new), `parseOutputRetryPolicy` extended, stage/task wiring |
| `pkg/orchestration/leveling_config.go` | `validateLevelingLadderShape` — the agent-free half of ladder validation, so a malformed ladder fails at load instead of at execution |
| `pkg/orchestration/testdata/leveling-pipeline.yaml` (new) | A realistic leveling workflow used by the end-to-end test |
| `pkg/orchestration/workflow_config_leveling_test.go` (new) | 11 test functions / 58 subtests, all passing under `-race` |

## The YAML schema as implemented

`leveling:` is accepted on a **pipeline stage** and on a **parallel task** (and therefore on an
iterative pattern's stages, which reuse the pipeline converter). `leveling_policy:` is accepted
as an alias — every other block in this loader is keyed on its proto field name, and a silently
ignored key is worse than a second spelling. Setting both is an error.

```yaml
stages:
  - agent_id: worker
    prompt_template: "{{previous}}"
    output_policy:
      output_schema: '{"type":"object","required":["answer"], ... }'
    leveling:
      enabled: true                        # REQUIRED when the block is present
      short_circuit_mid: true              # optional; absent ⇒ UNSET ⇒ true
      max_escalations: 2                   # optional; absent ⇒ UNSET ⇒ 1; 0 disables escalation
      max_cost_usd: 0.50                   # optional; 0 = no ceiling
      frontier_min_output_cost_usd: 10.0   # optional; 0 = built-in default
      mid_min_output_cost_usd: 1.5         # optional; 0 = built-in default
      ladder:                              # optional
        - provider: ollama                 # resolved from the agent's provider pool
          model: deepseek-r1:latest
        - role: orchestrator               # or LLM_ROLE_ORCHESTRATOR / llm-role-orchestrator
      tier_policies:                       # optional; keys: unknown, local, small-open,
        local:                             #   mid, frontier
          retry_budget: 2
          aggressive_coercion: true
          scaffolding_depth: 0             # 📋 carried, consumed by nothing (C2)
```

Role names accept the generated enum name, the short form, either case, and `-` for `_`.
`unspecified` is rejected as an explicit value — on a rung it means "no role", which the shape
check reports more usefully.

## Semantics of absence (the whole point)

| YAML | Proto field | Executor gate |
|---|---|---|
| no `leveling:` key | **nil** — not an empty message | closed |
| `leveling:` with a null value | **nil** (matching `hitl_gate`'s precedent for an explicit null) | closed |
| `leveling: {enabled: false}` | non-nil, `enabled=false` | closed |
| `leveling: {enabled: true, …}` | non-nil, `enabled=true` | open |

Two absence rules are load-bearing and pinned by tests:

1. **An absent block produces a nil proto field.** Both executors gate on
   `GetLevelingPolicy().GetEnabled()`, so nil and disabled behave identically — but nil is what
   "the feature does not exist for this stage" looks like on the wire and in storage.
2. **`short_circuit_mid` and `max_escalations` stay UNSET when their key is absent.** They are
   proto3 `optional` precisely because their Go defaults are `true` and `1`: writing a proto3
   zero value for an absent key would invert them. The loader only assigns them when the key is
   present, via `proto.Bool`/`proto.Int32`. Presence is read through helpers that return
   `(value, present, error)` rather than through struct-tag unmarshalling, which cannot tell
   absent from zero.

A third rule is new policy, not inherited: **`enabled` is required when the block is present.**
Defaulting a missing `enabled` to false would parse a full, carefully written leveling block
into something that does nothing, with no diagnostic. `enabled: false` is accepted and inert.
Omitting the block entirely remains the way to say "off".

## Where each validation fires

Load time means `LoadWorkflowFromYAML` / `LoadWorkflowFromYAMLBytes`; all load errors wrap
`ErrInvalidWorkflow` and name the YAML path (`spec.stages[0].leveling.ladder[1].role`).

| Check | Fires at | Authority |
|---|---|---|
| Block/rung/tier-map shape, scalar types, fractional integers, unknown role name, unknown `session_mode`, both leveling keys set, missing `enabled` | **load** | the loader (nothing else can see YAML types) |
| Negative `max_escalations`/`max_cost_usd`/`retry_budget`/`scaffolding_depth`, unknown tier key | **load**, and again at convert | `LevelingPolicyFromProto`, called by the loader as a validation gate and discarded — no duplicated rules or messages |
| Rung names neither role nor provider | **load**, and again at resolve | new `validateLevelingLadderShape`; `resolveLevelingLadder` re-checks because it also serves callers that never touched a config loader, with identical wording |
| Rung's provider is absent from the agent's pool / role has no LLM | **execution only** | `resolveLevelingLadder` — needs the executing agent, so it is unknowable at load time |

Semantic checks are skipped for a disabled block, deliberately: `LevelingPolicyFromProto`'s
rule is that a policy which was never enabled can never fail conversion, and the YAML surface
inherits it. `leveling: {enabled: false, max_escalations: -1, tier_policies: {NOT-A-TIER: {}}}`
loads, stores, and stays inert. Type errors still fire — there is nothing to store otherwise.

## Two adjacent blocks this needed

**`output_policy` is now parsed** (stage and task, all five fields). Without it a parallel task
had no way to express a schema in YAML, so leveling on a task could never have a free signal —
the surface would have been reachable but useless. Parsing it does **not** make it enforced: the
only readers of `OutputPolicy` are the leveling paths
(`effectiveLevelingOutputPolicy`, `ParallelExecutor.executeTaskWithLeveling`), verified by grep,
so a stage or task carrying `output_policy` without an enabled leveling policy behaves exactly
as before. The Phase 2 safety constraint is unchanged and its inertness tests are untouched; a
new test loads a YAML stage with a hostile disabled policy plus a violated schema and asserts
one agent call, no validation, no warnings.

⚠️ `acceptance_criteria`, `validator_agent_id` and `judge_config_id` are parsed and stored but
consumed by nothing (`OutputValidator.validate` is deliberately LLM-free). They round-trip; they
do not evaluate.

**`parseOutputRetryPolicy` was extended** — gap #4 from the Phase 1 "What the code contradicted"
list. It now reads `session_mode` (short or full enum form), `feedback_template` and
`cooldown_ms` in addition to `max_retries`/`include_valid_values`. This matters for leveling
because escalation prompts are built from `FeedbackTemplate`. Preserved: a `retry_policy` with
`max_retries <= 0` still yields a nil policy, exactly as before. New: setting one of the three
new keys *with* `max_retries <= 0` is now an error rather than a silent drop, because that
combination is always a mistake. The function gained an error return and a path argument; it is
package-private with four call sites, all in `workflow_config.go`, all updated.

## Test coverage of the distinction that matters

| Test | Pins |
|---|---|
| `TestWorkflowYAMLLevelingAbsentBlockYieldsNilPolicy` | absent / null / alias-null block ⇒ nil field on both carriers |
| `TestWorkflowYAMLLevelingOptionalFieldsUnsetVsExplicitZero` | **the UNSET-vs-explicit-zero distinction**: nil pointers for absent keys, non-nil pointers for explicit `false`/`0`, and the defaults `LevelingPolicyFromProto` derives from each |
| `TestWorkflowYAMLLevelingFullRoundTrip` | every field, both carriers, plus the alias |
| `TestWorkflowYAMLLevelingDisabledBlockLoadsAndStaysInert` | disabled block with hostile fields loads, and the loaded stage is inert in the executor |
| `TestWorkflowYAMLLevelingErrors` (19 cases) | load-time messages, on stages and tasks |
| `TestWorkflowYAMLLevelingSemanticErrorsSkippedWhenDisabled` / `…TypeErrorsFireEvenWhenDisabled` | the enabled-gated vs always-on halves of validation |
| `TestWorkflowYAMLLevelingRoleForms` | short form, full enum name, case and `-`/`_` variants |
| `TestLoadWorkflowFromYAML_LevelingReachesExecutorGate` | end-to-end from `testdata/leveling-pipeline.yaml`: the gate opens, the YAML-declared ladder rung resolves from the provider pool, and the rung's output wins (1 primary call from `retry_budget: 0`, 1 rung call) — plus the same for a parallel task |
| `TestWorkflowYAMLOutputPolicyBlock`, `TestWorkflowYAMLRetryPolicyExtendedFields` | the two adjacent blocks |

## Verification record (2026-08-16, Phase 2d)

Exit codes were read from redirected files, never through a pipe.

- ✅ `go build -tags fts5 ./...` — exit 0, no output.
- ✅ `go test -tags fts5 -race ./pkg/orchestration/` — exit 0.
- ✅ `go test -tags fts5 -race -short ./...` — exit 0, 95 packages `ok`, 0 failures.
- ✅ `go vet -tags fts5 ./pkg/orchestration/` — exit 0.
- ✅ `gofmt -l ./pkg ./cmd ./internal` — exit 0, no files listed.
- ✅ No proto change, so no `buf` regeneration.
- ⚠️ `just lint` not run: the same golangci-lint/Go export-data incompatibility recorded in the
  Phase 0/1, 2b and 2c sections. `go vet` + `gofmt` are the usable gates.

## What this does not change

- 📋 **No judge can be configured from YAML**, by design (Phase 2 decision #2). Combined with
  Phase 3's finding that the free schema signal is blind to reasoning errors, a YAML-configured
  leveling policy can close the **format** gap and nothing else. Escalation on semantic
  wrongness still requires a Go caller that supplies a `LevelingJudge` — and Phase 3b says that
  judge must be reasoning-class to be worth its latency.
- 📋 `scaffolding_depth` is now expressible in YAML and still consumed by nothing (C2, which
  Phase 3b deprioritized on evidence).
- 📋 C4 self-consistency, and the follow-ups Phases 2b–4 opened, are untouched.
- ⚠️ A rung's provider/role must exist on the executing agent; that can only fail at execution
  time, so a YAML file can be valid and still fail its first run. The error names the rung index
  and the agent.

---

# Phase 5 results — SQL generation, the home-turf validation (2026-08-16)

**Status**: ✅ Measured — 4 arms × 30 identical NL→SQL questions over a deterministic synthetic
retail schema (4 tables, 1118 rows), generated SQL **actually executed** against SQLite and
scored by result-set equality against executed reference SQL. Weak model `llama3.2:latest`
(current-generation, replacing Phase 3's llama2), strong `deepseek-r1:latest`. 9m21s live run,
$0. Harness: `pkg/orchestration/leveling_sql_live_test.go` + generator/tests in
`leveling_sql_gen_test.go`; per-trial data `docs/experiments/sql_arms.jsonl` +
`sql_questions.jsonl`. Calibration (50-question probe, Python) and the Go generator were
parity-locked: identical row counts, four checksums, status distributions, and reference
results across both implementations.

## Why this experiment

Pre-PR validation on Loom's actual workload (SQL agents) with a weak model someone would
actually deploy, in the regime the program identified as leveling's home: a **free programmatic
signal** — does the query execute — with a known blind spot: queries that run cleanly and
return wrong data.

## Results (N=30, seed 0, temp 0.1; arms 2/3 signal = execution success, judged in 5ms of
sqlite per check, zero LLM calls, zero USD)

| Arm | Correct | Silent wrong | Exec error | LLM calls | r1 calls |
|---|---|---|---|---|---|
| 1 llama3.2 alone | 17/30 (57%) | 5 | 8 | 30 | 0 |
| 2 + retry-on-exec-error | **23/30 (77%)** | 7 | **0** | 38 | 0 |
| 3 + ladder → r1 | 23/30 (77%) | 7 | 0 | 38 | **0** |
| 4 r1 alone (ceiling) | 30/30 (100%) | 0 | 0 | 30 | 30 |

Determinism note: the first full run was externally killed one question before completion; the
rerun reproduced arms 1–3 identically (seed-pinned), which is itself a live demonstration of
the reproducibility the seed plumbing (40a9335) was built for.

## Findings

1. **The free rung pays on Loom's workload.** One same-model retry carrying the sqlite error
   text resolved all 8 execution failures (6 → correct, 2 → executable-but-wrong): +20pp
   accuracy for 8 extra ~1s weak-model calls and 0.19s of sqlite. Contrast Phase 3: llama2
   given a *semantic critique* repaired 0/18. **Error-message-driven repair of format-class
   failures works on a current 3B model; critique-driven repair of reasoning does not work on
   weak models.** Both halves are now measured.
2. **The escalation rung never fired — for the right reason this time.** Arm 3 made zero r1
   calls: the cheap rung fixed everything the signal could see before escalation was reachable.
   Ladder ordering (free retry before paid model) is behaving as designed.
3. **The blind spot is real and quantified.** 7 silently-wrong queries (wrong joins, invented
   arithmetic — they execute cleanly) were invisible to the execution signal in both leveled
   arms: zero escalations fired on them, and the judge's false-PASS rate on wrong outputs was
   46.7%. r1 solved all 7 — but the ladder never sent them. **On an execution-only signal,
   83.3% was the structural accuracy ceiling; result-set verification or a reasoning critic is
   required to pass it.** This is Phase 3's schema-blindness, reproduced on SQL.
4. **Retries convert crashes into confident wrong answers.** 2 of 8 exec errors became
   silent_wrongs after retry (arm 1: 5 silent → arms 2/3: 7). Free repair fixes form, not
   meaning; a fixed query that now runs is not therefore right.
5. **Failure structure is bimodal by join depth**, matching calibration: filter/count and
   2-table joins ~perfect; 3-table joins 0/6 (semantic errors); top-N 0/6 (one deterministic
   syntax bug, retry-recoverable). The weak model's SQL competence cliff is architectural,
   not random.

## Economics on this mix

Arm 3 (77%) cost 30.4s of model time; arm 4 (100%) cost 475s — the weak ladder is ~15× cheaper
in wall-clock but gives up 23pp of accuracy, all of it in the silent-wrong blind spot. The
rational production configs are therefore: execution-signal ladder when result verification
exists downstream or errors are tolerable; strong model (or a result-verifying judge per the
Phase 3b r1-critic finding) when silent wrongness is unacceptable.

## Confounds, stated

SQLite dialect, not Teradata; single schema and 5 template families; N=30, one seeded draw;
arms 2/3 identical by construction here (escalation unreachable — a signal that could see
silent wrongs would differentiate them); llama3.2's F4 failure is one deterministic bug with
multiplicity 6, so the +20pp free-rung gain leans heavily on one recoverable defect class.
