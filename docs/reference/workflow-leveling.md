# Workflow Capability Leveling Reference

**Version**: v1.3.0

**Status**: ✅ Implemented (v1.3.0) — pipeline stages, iterative pipeline stages, and parallel tasks

Capability leveling lets a weak primary model run a stage, checks its output against a free structural signal (a JSON Schema), and escalates to stronger models only when that signal says the output is unusable. When the primary's output passes, leveling spends nothing beyond one memoized catalog lookup.

Leveling is opt-in per stage and per task through a `leveling:` block. A stage with no block behaves exactly as it did before this feature existed.

## Table of Contents

- [Quick Reference](#quick-reference)
- [How a Stage Is Leveled](#how-a-stage-is-leveled)
- [Where Leveling Applies](#where-leveling-applies)
- [Model Tiers](#model-tiers)
- [YAML Key Reference](#yaml-key-reference)
- [Examples](#examples)
- [The Verdict Signal](#the-verdict-signal)
- [Escalation Rungs](#escalation-rungs)
- [Failure Semantics](#failure-semantics)
- [Cost Gate](#cost-gate)
- [Rejected Configurations](#rejected-configurations)
- [Observability](#observability)
- [Measured Results](#measured-results)
- [Limitations](#limitations)
- [See Also](#see-also)

## Quick Reference

| Aspect | Behavior |
|--------|----------|
| Opt-in | `leveling:` block on a pipeline stage or parallel task; alias `leveling_policy:` |
| Off switch | Omit the block entirely, or set `enabled: false` (parsed, inert) |
| `enabled` | Required when the block is present — a present block must say what it wants |
| Path decision | The primary model's catalog tier alone: `local` and `small-open` take the active path; `frontier` and `unknown` always short-circuit; `mid` short-circuits unless `short_circuit_mid: false` |
| Verdict signal | `output_policy.output_schema` (JSON Schema) when present; free JSON extraction from fenced/prose-wrapped text is applied on every path before a failure is declared |
| Judge | Go-only (`LevelingPolicy.Judge`); deliberately not reachable from YAML or proto |
| On failure | Same-model retries per tier `retry_budget`, then escalation up `ladder:` in order |
| Escalation bounds | `max_escalations` (default 1) and `max_cost_usd` (0 = no gate), whichever bites first |
| On exhaustion | ⚠️ Continues with the best output obtained plus a `validation_warnings` entry — it does **not** fail the workflow |
| Cost attribution | The winning result's cost carries the whole ladder's spend |

## How a Stage Is Leveled

1. **Gate.** The executor checks `stage.leveling_policy.enabled`. Absent or false: the pre-existing path runs unchanged. True: leveling owns validation for this stage, and the legacy validation block is skipped.
2. **Resolve the contract.** The stage's `output_policy` is the validation contract. A stage written against the legacy `output_schema`/`retry_policy` pair with no `output_policy` has those synthesized into one, so enabling leveling on an older stage still validates against them.
3. **Resolve the ladder.** Rung 0 is the executing agent's own conversation (its own LLM, tools, system prompt and session). Each `ladder:` rung resolves to an LLM already configured on that agent — a role LLM, or an entry in the agent's provider pool. No provider is constructed and no routing is added.
4. **Classify the primary.** One memoized catalog lookup derives the primary's tier from the model's pricing and reasoning flag. This is the entire cost of the short-circuiting path.
5. **Short-circuit or escalate.** A `frontier` or `unknown` primary — and a `mid` one under the default `short_circuit_mid: true` — skips the ladder and the judge. Its `tier_policies` `retry_budget` still applies, and free JSON extraction still runs.
6. **Attempt and validate.** The primary runs. Its output is validated against the schema; on failure, free JSON extraction is tried on the same attempt, so a fenced-but-valid payload never burns a paid retry.
7. **Same-model retries.** On a real failure, the effective `retry_policy` drives retries against the same model, carrying feedback (`session_mode`, `feedback_template`, `cooldown_ms`). A caller-supplied `retry_policy` inside `output_policy` always wins over the tier's `retry_budget`.
8. **Escalate.** Rungs run in ladder order as one-shot calls, each carrying the previous failure reason and output. The first rung whose output satisfies the schema wins and is returned.
9. **Exhaust.** When the rungs, `max_escalations` or `max_cost_usd` run out, the best output obtained is returned with a warning — not an error. See [Failure Semantics](#failure-semantics).

## Where Leveling Applies

| Workflow type | YAML location of the block | Status |
|---------------|---------------------------|--------|
| `type: pipeline` | `spec.stages[].leveling` | ✅ Implemented |
| `type: iterative` | `spec.pipeline.stages[].leveling` | ✅ Implemented |
| `type: parallel` | `spec.tasks[].leveling` | ✅ Implemented |
| `type: conditional`, `type: swarm`, `type: debate`, `type: fork-join` | not carried | 📋 Not supported |

`type: fork-join` describes its agents with `agent_ids`, not per-task maps, so it has nowhere to hang a per-task policy. Use `type: parallel` with `tasks:` when you need leveling on fan-out work.

## Model Tiers

Tiers are derived in `pkg/llm/catalog/tier.go` from fields the catalog already carries — per-token pricing and the reasoning flag. Nothing is persisted; the tier is recomputed on every call. Only the **primary** rung's tier decides the path.

| Tier | Derivation (rules applied in this order) | Short-circuits? | Built-in `retry_budget` |
|------|------------------------------------------|-----------------|-------------------------|
| `unknown` | The (provider, model) pair is not in the catalog | Always — leveling refuses to guess | 0 |
| `local` | Catalog pricing is 0 input **and** 0 output (self-hosted, e.g. Ollama) | No — active path | 2 |
| `small-open` | Priced, not a reasoning model, output price below the mid cutoff | No — active path | 2 |
| `mid` | A reasoning model priced below the frontier cutoff, **or** a non-reasoning model at output price ≥ the mid cutoff | Yes, unless `short_circuit_mid: false` | 1 |
| `frontier` | A reasoning model at output price ≥ the frontier cutoff | Always | 0 |

Order matters: a zero-cost model is `local` even when it advertises reasoning (a self-hosted deepseek-r1 is a local model), and an expensive non-reasoning model lands in `mid` because `frontier` requires reasoning.

Cutoffs are stated in USD per 1M output tokens, default 10.0 (frontier) and 1.5 (mid), and are shiftable per policy with `frontier_min_output_cost_usd` / `mid_min_output_cost_usd`.

**Short-circuit means no ladder and no judge. It does not mean the tier's `retry_budget` is ignored** — `tier_policies` is honored on every tier, so `frontier: {retry_budget: 1}` yields one same-model retry on a frontier primary.

## YAML Key Reference

```yaml
leveling:                              # alias: leveling_policy (setting both is an error)
  enabled: true                        # bool, REQUIRED when the block is present
  short_circuit_mid: true              # bool, default true
  max_escalations: 1                   # int32, default 1, >= 0 (0 disables escalation)
  max_cost_usd: 0.50                   # double, default 0 (no gate), >= 0 and finite
  frontier_min_output_cost_usd: 10.0   # double, default 0 -> built-in 10.0
  mid_min_output_cost_usd: 1.5         # double, default 0 -> built-in 1.5
  ladder:                              # list of rungs, tried in order; default empty
    - provider: ollama                 # resolved from the agent's provider pool
      model: deepseek-r1:latest        # optional; catalog key for tier reporting
    - role: orchestrator               # or LLM_ROLE_ORCHESTRATOR
  tier_policies:                       # keys: unknown | local | small-open | mid | frontier
    local:
      retry_budget: 2                  # int32, >= 0
```

### Policy keys

| Key | Type | Default | Range | Notes |
|-----|------|---------|-------|-------|
| `enabled` | bool | — (required) | `true` / `false` | A present block with no `enabled` is a load error. `false` is accepted and inert. Omitting the block entirely is how "this feature does not exist for this stage" is written on the wire. |
| `short_circuit_mid` | bool | `true` | `true` / `false` | `false` puts a `mid` primary on the active path (retries, ladder, judge). |
| `max_escalations` | int32 | `1` | `>= 0` | Ladder rungs allowed beyond the primary. `0` disables escalation while keeping per-tier retries and free coercion. An attempted rung counts even if its call errors. |
| `max_cost_usd` | double | `0` | `>= 0`, finite | Between-actions gate across leveling-visible calls. `0` disables it. See [Cost Gate](#cost-gate). |
| `frontier_min_output_cost_usd` | double | `0` → built-in `10.0` | `>= 0`, finite | $/1M output tokens. A cutoff of exactly 0 cannot be expressed; use a small positive number such as `0.0001`. |
| `mid_min_output_cost_usd` | double | `0` → built-in `1.5` | `>= 0`, finite | Same units and same 0-means-default rule. |
| `ladder` | list | empty | — | Escalation rungs beyond the primary, tried in order. |
| `tier_policies` | map | built-in defaults | keys: `unknown`, `local`, `small-open`, `mid`, `frontier` | A tier absent from the map uses its built-in entry. |

### Rung keys

Each rung names **either** a `role` **or** a `provider`. A rung with neither is a load error.

| Key | Type | Resolution |
|-----|------|------------|
| `provider` | string | Looked up in the executing agent's provider pool (`Agent.GetProviderPool()`). A name absent from the pool — or an agent with no pool at all — is an error. |
| `model` | string | Optional. Used as the catalog key and reported in result metadata. When omitted, the resolved LLM's own model is used. |
| `role` | string | `judge`, `orchestrator`, `classifier`, `compressor` (hyphens and any case accepted, as are the full enum names such as `LLM_ROLE_ORCHESTRATOR`). Resolved strictly from the agent's role LLMs — a role with no LLM of its own is an error rather than a silent fall back to the agent's own model. |

### Tier policy keys

| Key | Type | Default | Notes |
|-----|------|---------|-------|
| `retry_budget` | int32 | Per-tier built-in (see [Model Tiers](#model-tiers)) | `>= 0`. Same-model retry count applied when `output_policy.retry_policy` is absent. An explicit `0` means no retries. |

A tier entry that overrides nothing — `local:` (null) or `local: {}` — means that tier's built-in values, not an all-zero policy.

Two tier keys that existed only while this feature was on its branch, `scaffolding_depth` and `aggressive_coercion`, are **load errors** rather than ignored keys. See [Rejected Configurations](#rejected-configurations).

## Examples

### Pipeline stage with a ladder and tier policies

A local primary (Ollama, tier `local`) with a schema contract, two same-model retries, and one escalation rung on Anthropic. The rung's provider must already be in the `worker` agent's provider pool.

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: leveled-extraction
  version: "1.0.0"
spec:
  type: pipeline
  initial_prompt: "Extract the customer records from the attached notes."
  stages:
    - agent_id: worker
      prompt_template: "Extract structured records from: {{previous}}"
      output_policy:
        output_schema: '{"type":"object","required":["customers"],"properties":{"customers":{"type":"array","items":{"type":"object"}}}}'
        retry_policy:
          max_retries: 2
          session_mode: continue
          cooldown_ms: 250
      leveling:
        enabled: true
        max_escalations: 1
        max_cost_usd: 0.50
        ladder:
          - provider: anthropic
            model: claude-sonnet-4-5
        tier_policies:
          local:
            retry_budget: 2
          frontier:
            retry_budget: 1
    - agent_id: formatter
      prompt_template: "Format as a markdown table: {{previous}}"
```

Because `output_policy.retry_policy` is present, it wins over `tier_policies.local.retry_budget` — the `retry_budget: 2` entry only takes effect for a run where the stage carries no `retry_policy`.

### Parallel task with a role-resolved rung

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: leveled-fanout
  version: "1.0.0"
spec:
  type: parallel
  merge_strategy: concatenate
  tasks:
    - agent_id: classifier-worker
      prompt: "Classify each row and return JSON."
      output_policy:
        output_schema: '{"type":"object","required":["labels"],"properties":{"labels":{"type":"array"}}}'
      leveling:
        enabled: true
        short_circuit_mid: false
        max_escalations: 2
        ladder:
          - role: orchestrator
          - provider: anthropic
    - agent_id: summarizer
      prompt: "Summarize the same rows in prose."
```

The `orchestrator` rung requires an orchestrator role LLM configured on the `classifier-worker` agent. `short_circuit_mid: false` keeps leveling active even if the primary classifies as `mid`.

## The Verdict Signal

| Signal | Availability | Cost |
|--------|-------------|------|
| JSON Schema (`output_policy.output_schema`) | ✅ YAML, proto, Go | Free — structural, deterministic |
| Free JSON extraction from mixed text | ✅ Applied on every schema-bearing path, every tier | Free |
| LLM judge (`LevelingPolicy.Judge`) | ⚠️ Go only, by construction | One paid call per verdict |

A schema, when present, owns the verdict and the judge is never consulted. Free JSON extraction is tried on a failing attempt before that attempt is written off, so a payload wrapped in prose or code fences is accepted instead of burning a retry. When extraction succeeds, the stage output is rewritten to the extracted JSON so downstream stages receive clean structured data.

**The judge is deliberately unreachable from configuration.** Neither `LevelingPolicy` in the proto nor the `leveling:` YAML block carries a judge reference, so config alone can never add a paid call. Supplying one requires Go code that constructs `LevelingPolicy` directly.

With no schema and no judge there is no signal at all, so nothing is spent proving the output wrong — the output stands.

## Escalation Rungs

Escalation rungs are one-shot `LLMProvider.Chat` calls:

- ❌ **No tools.** A tool-dependent stage cannot be rescued by escalation.
- ❌ **No system prompt.**
- ❌ **No session and no memory** — there is no conversation for the rung to join.
- ✅ The rung receives the original prompt, the failure reason, and the previous output, shaped by `output_policy.retry_policy.feedback_template` when one is set.

A rung's winning output is attributed to the stage's own agent, so workflow results never name an agent the workflow did not declare. Result metadata records which model actually produced it:

| Metadata key | Value |
|--------------|-------|
| `leveling_rung_provider` | The winning rung's provider name |
| `leveling_rung_model` | The winning rung's model ID |

These keys are absent when the primary's own output won. The executor's standard result metadata (`stage`, `agent_name`, or `task_index` and the task's own metadata) is backfilled onto a rung result, so downstream consumers read the same keys whichever rung won.

`WorkflowResult` cost carries the **whole ladder's** spend, not the winning call's: producing a leveled stage's output also paid for the primary's retries, every losing rung, and every judge call. Token counts still describe the winning call.

### Ladder ordering

A rung the catalog classifies **below** the primary's tier still runs, and is reported with a warning rather than rejected — the tiers come from catalog pricing, which is the wrong authority to fail a run on:

```
escalation rung 1 (ollama/llama3.2) is tier local, below the primary's tier mid — the ladder escalates downward
```

Same-tier rungs are left alone on purpose: a same-tier rung carrying a different signal is a working pattern, not a mistake. Each attempted rung's own tier is resolved and recorded, so the trace says what the ladder actually climbed rather than only where it started.

## Failure Semantics

⚠️ **Enabling leveling turns a hard-failing stage into a warn-and-continue stage.** This is the behavior change to read before enabling it on an existing workflow.

| Configuration | Stage output fails validation, retries unavailable or exhausted |
|---------------|------------------------------------------------------------------|
| Legacy `output_schema` with **no** `retry_policy`, leveling off | ❌ The workflow **fails**: `stage N output validation failed: <reason>` |
| Legacy `output_schema` **with** `retry_policy`, leveling off | ⚠️ Continues with the unvalidated output; a warning lands in `WorkflowResult.Metadata["validation_warnings"]` |
| `leveling.enabled: true` | ⚠️ Always continues with the best output obtained; a warning lands in `WorkflowResult.Metadata["validation_warnings"]` |

Exhaustion is not an error. The caller decides whether an unvalidated output is usable, and the warning is how it finds out.

### What comes back on exhaustion

The last rung's output when the last rung attempted produced one; the primary's own last result when that rung errored or returned nothing. The caller always gets a real output, and never an output no model actually produced.

### Warning text

Warnings are stage-prefixed and joined with `; ` into `WorkflowResult.Metadata["validation_warnings"]`. The plain pipeline and the iterative pipeline use the same key and format.

Exhausted rungs on the active path:

```
stage 1 (worker): continuing with unvalidated output — leveling exhausted its rungs (tier=local, escalations=1, budget_exhausted=false)
```

A short-circuited primary whose output failed:

```
stage 1 (worker): continuing with unvalidated output — leveling short-circuited on a strong primary and did not escalate (tier=frontier, escalations=0, budget_exhausted=false)
```

An unclassified primary (the model is not in the catalog):

```
stage 1 (worker): continuing with unvalidated output — leveling short-circuited on an unclassified primary (model not in the catalog) and did not escalate (tier=unknown, escalations=0, budget_exhausted=false)
```

Per-attempt and per-rung warnings appear alongside these, with the same stage prefix — for example `escalation rung 1 output rejected: <reason>` and `escalation rung 1 (anthropic/claude-sonnet-4-5) failed: <error>`.

Parallel tasks record the same facts as a structured `zap` warning (`Task continuing with unvalidated output after capability leveling`) carrying `agent_id`, `task_index`, `tier`, `escalations`, `budget_exhausted` and the report's warnings.

**A coercion-rescued attempt records no warning.** A free rewrite that satisfied the schema on the same attempt is not a failure the caller needs to know about. On the leveling path, `validation_warnings` counts failures that were *not* rescued — do not read it as an attempt counter.

### Cancellation

Cancelling the context stops the retry loop and the ladder at the next iteration boundary and returns the cancellation error. An in-flight call is not interrupted by the gate logic itself.

## Cost Gate

`max_cost_usd` is an honest bound on runaway escalation. It is **not** a billing limit, and the doc comment on `LevelingPolicy.MaxCostUSD` says so in the same words:

- **It is checked between actions** — before each same-model retry, each escalation rung, and each judge call. The action already in progress is never interrupted, so the ceiling can be, and normally is, **overshot by the cost of one call**.
- **The running total is a floor, not an exact figure.** Per-attempt cost is read from the result the attempt returned, which carries the final LLM response's usage. An attempt that ran a tool loop inside the agent reports less than it spent, so the gate can let a call through that precise accounting would have blocked.
- **A judge skipped by the ceiling accepts the output unexamined**, and says so rather than rejecting an output it never looked at:

```
judge skipped: cost ceiling reached (0.500000 of 0.500000 USD) — output accepted unexamined
```

- `budget_exhausted=true` in the stage warning and on the `leveling.execute` span means the ceiling stopped something.
- Rung spend comes from each provider's own catalog-priced `Usage.CostUSD`. Leveling adds no pricing table of its own.

## Rejected Configurations

Loader errors are wrapped with `invalid workflow structure: ` and the YAML path of the offending block (for example `spec.stages[0].leveling`). Semantic checks run at load time **only when `enabled: true`** — a policy that was never enabled can never fail conversion — and again at execution time, because a workflow submitted as raw proto over gRPC never passes through the YAML loader.

| Configuration | Error text |
|---------------|-----------|
| Both `leveling:` and `leveling_policy:` | `spec.stages[0] sets both 'leveling' and 'leveling_policy': keep one (they configure the same policy)` |
| Block present, no `enabled` | `spec.stages[0].leveling.enabled is required: set 'enabled: true' to turn capability leveling on for this stage, or remove the 'leveling' block entirely to leave it off` |
| Rung naming neither `role` nor `provider` | `leveling ladder: rung 1 needs role or provider` |
| Provider not in the agent's pool | `leveling ladder: rung 1 provider "anthropic" is not in agent "worker"'s provider pool; a provider-based rung requires the provider to be present in the agent's provider pool` |
| Agent has no provider pool at all | `leveling ladder: rung 1 names provider "anthropic" but agent "worker" has no provider pool; a provider-based rung requires the provider to be present in the agent's provider pool` |
| Role with no LLM on the agent | `leveling ladder: rung 1 role LLM_ROLE_JUDGE has no LLM configured on agent "worker"` |
| `role: agent` (names the agent's own LLM) | `leveling ladder: rung 1 role LLM_ROLE_AGENT names the agent's own LLM — an escalation rung must be a different model than the primary; use a role with its own LLM (judge, orchestrator, classifier, compressor) or a provider from the agent's provider pool` |
| A rung that resolves to the primary's own model | `leveling ladder: rung 1 resolves to the primary's own model anthropic/claude-sonnet-4-5 — an escalation rung must be a different model than the primary` |
| Unknown role name | `spec.stages[0].leveling.ladder[0].role "wizard" is not a known LLM role (valid: judge, orchestrator, classifier, compressor; the full enum name such as LLM_ROLE_ORCHESTRATOR is also accepted)` |
| Leveling plus the legacy `validation_prompt` | `spec.stages[0].leveling cannot be combined with the legacy validation_prompt — leveling has no semantic-prompt signal, so the criteria would be silently dropped; move the criteria into output_policy.output_schema or disable leveling on this stage` |
| Leveling plus `output_policy` **and** a legacy `output_schema`/`retry_policy` on the same stage | `leveling_policy cannot be combined with output_policy AND the legacy output_schema/retry_policy on the same stage — with leveling enabled only output_policy is enforced, so the legacy contract would be silently dropped; move it into output_policy.output_schema / output_policy.retry_policy or remove the legacy fields` |
| Removed key `scaffolding_depth` | `spec.stages[0].leveling.tier_policies[local].scaffolding_depth was removed: C2 capability-adaptive scaffolding was rejected on measurement (it made a weak model worse), so the knob is gone rather than dead — remove the key (see docs/plan-capability-leveling.md)` |
| Removed key `aggressive_coercion` | `spec.stages[0].leveling.tier_policies[local].aggressive_coercion was removed: free JSON extraction now runs on every schema-bearing leveling path, so the knob gated nothing — remove the key (see docs/plan-capability-leveling.md)` |
| Unknown tier name | `leveling policy: unknown tier name "gpu" in tier_policies (valid names: unknown, local, small-open, mid, frontier)` |
| Negative `retry_budget` | `leveling policy: tier "local" retry_budget must be >= 0, got -1` |
| Negative `max_escalations` | `leveling policy: max_escalations must be >= 0, got -1` |
| Negative cost or threshold | `leveling policy: max_cost_usd must be >= 0, got -1.000000` |
| NaN cost or threshold | `leveling policy: max_cost_usd must be a number, got NaN` |
| Infinite cost or threshold | `leveling policy: max_cost_usd must be finite, got +Inf` |
| Negative `output_policy.retry_policy.max_retries` under leveling | `leveling policy: output_policy.retry_policy.max_retries must be >= 0, got -1` |
| Wrong YAML type on any leveling key | Always a load error regardless of `enabled`, e.g. `spec.stages[0].leveling.enabled must be a boolean (true or false), got string` |

The last two legacy-conflict rows are pipeline-only. A parallel task is built from `agent_id`, `prompt`, `metadata`, `output_policy` and the leveling block, so `validation_prompt`, `output_schema` and `retry_policy` written at task level are keys the task surface never converts — there is no conflict to report.

**Legacy `output_schema`/`retry_policy` *without* an `output_policy` are honored** — they are synthesized into the contract leveling enforces, so enabling leveling on a stage written against the older fields still validates against them.

NaN and infinity are rejected for the same reason a negative bound is: every comparison against NaN is false, so a NaN ceiling would silently disable the cost gate and a NaN threshold would silently reclassify every model.

## Observability

| Span | Emitted | Attributes |
|------|---------|-----------|
| `leveling.execute` | Once per leveled stage or task, on the enabled path only | `leveling.tier`, `leveling.short_circuited`, `leveling.escalations`, `leveling.judge_calls`, `leveling.passed`, `leveling.budget_exhausted`, `leveling.total_cost_usd`, plus `leveling.rung.<i>.tier` per attempted rung |
| `leveling.rung` | Once per escalation call | `leveling.rung.provider`, `leveling.rung.model`, `agent.id`, `agent.session_id`, then `llm.cost_usd`, `llm.input_tokens`, `llm.output_tokens`, `llm.duration_ms` — or `error` when the call fails |
| `leveling.judge` | Once per judge call | `leveling.judge.pass`, `leveling.judge.cost_usd`, `leveling.judge.reason`, and `leveling.judge.error` when the judge itself fails |
| `output_validator.validate_and_retry` | Once per validation loop | — |
| `pipeline.agent.<agent_id>` / `parallel.agent.<agent_id>` | Per primary attempt, as before | The executors' existing attributes |

The provider's own spans nest under `leveling.rung`, because the rung call carries the tracer on its context the way the merge and validation LLM calls do. The escalation session ID is `<stage session id>-lvl<rung index>`, which is what makes a rung span joinable to the rest of the stage even though a bare LLM call has no conversation to join.

With leveling disabled there is no `leveling.execute` span, no catalog lookup, and no wrapping closures — the call path is what it was before this feature existed.

Debug-level `zap` logs cover the decisions that do not fit on a span: `leveling short-circuited`, `leveling escalating` (with rung index, provider, model and reason), `leveling escalation blocked by cost ceiling`, `leveling coerced output to schema-valid JSON without escalating`, `leveling judge skipped by cost ceiling`, and `leveling judge verdict`.

## Measured Results

The design record carries the full measurement history. Briefly, from `../plan-capability-leveling.md`:

- **Format failures close, and close for free.** Free JSON extraction rescued **30/30** malformed llama2 replies that scored **0/30** raw schema validity without it. A separate arm closed the same gap with same-model retry carrying schema feedback (5/10 → 10/10) at zero coercions in 40 trials.
- **A free programmatic signal pays on Loom's own workload.** On a 30-question SQL task with llama3.2, one same-model rung carrying the sqlite execution error took accuracy from **17/30 (57%)** to **23/30 (77%)** and eliminated all 8 execution failures. All 8 escalations went to that same-model rung; the strong model (deepseek-r1) was called zero times — the cheap rung fixed everything the signal could see. The harness builds that ladder in Go. From YAML, same-model retry is `tier_policies.<tier>.retry_budget` or an explicit `retry_policy`: a ladder rung naming the primary's own model is rejected (see [Escalation Rungs](#escalation-rungs)).
- **Reasoning gaps are not closable by retry, self-critique, or scaffolding.** They can only be routed around, by escalating to a reasoning model behind a trustworthy signal. Capability-adaptive scaffolding was rejected on measurement — it made a weak model worse.
- **The schema signal is structurally blind to wrong answers.** In the SQL arms, 7 silently-wrong queries executed cleanly and drew zero escalations. A schema tells you the shape is right, not that the answer is.

## Limitations

- ⚠️ **Escalation rungs run without tools, system prompt, session, or memory.** A stage that depends on tool use cannot be rescued by escalation. 📋 Tool-carrying rungs are future work.
- ⚠️ **The judge is Go-only.** No proto or YAML judge reference exists, by design: configuration alone must never be able to add a paid call. 📋 A config-reachable judge is not planned for this release.
- ⚠️ **Cost accounting is a floor.** Per-attempt cost comes from the final LLM response's usage, so an attempt that ran a tool loop under-reports. `max_cost_usd` bounds runaway escalation; it does not guarantee a billing limit.
- ⚠️ **Tier is a price heuristic, not a capability measurement.** It is derived from catalog pricing and a reasoning flag. A mispriced or uncatalogued model gets the wrong tier, and `unknown` always short-circuits rather than guessing.
- ⚠️ **Enabling leveling changes failure semantics** on a stage that previously hard-failed validation. See [Failure Semantics](#failure-semantics).
- 📋 **Conditional, swarm, debate and fork-join patterns carry no leveling block.**

## See Also

- [Workflow Output Retry Reference](workflow-output-retry.md) — `output_policy.retry_policy`, session modes, feedback templates, and the non-leveled retry paths
- [Iterative Workflow Reference](workflow-iterative.md) — iterative pipelines, which carry leveling on their stages
- [Ollama](llm-ollama.md) — configuring the local models that classify as tier `local`
- [Capability Leveling design record](../plan-capability-leveling.md) — measured results, rejected components, and the decisions behind this surface
