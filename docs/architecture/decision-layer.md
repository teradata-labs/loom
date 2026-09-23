# Decision Layer

**Status**: ⚠️ Partial. Core package (Phase 0), shadow harness (Phase 1) and the Jev client (Phase 2) implemented and exercised live; three call sites shadow-record; no site acts on a decider yet.
**Package**: `pkg/decision` (+ `jev`, `llm`, `mock`, `report`, `sites`)
**Proto**: `proto/loom/v1/decision.proto`
**Plan**: `docs/plans/jev-decision-layer-plan.md` · **Research**: `docs/research/jev-system-one-assessment.md`

## What it is

A typed decision is a narrow semantic judgment over supplied state that returns probabilities, never text:

| Primitive | Question | Answer |
|---|---|---|
| **Noul** | Is this true? | probability in [0, 1] |
| **Choice** | Which of these (2–255 named options)? | chosen key, probability per option, confidence |
| **Score** | Where on this ordered scale (2–10 levels)? | probability-weighted level index, probability per level, legend, confidence |

Every question in a request is evaluated independently against the same state. The layer exists because Loom makes many such judgments today (rerank memories, pick a workflow branch, classify intent, validate a stage output) with either a full generative call whose text is parsed, or a substring match. A decision model answers them in one round trip for a fraction of the cost and, more importantly at fleet scale, off the generative provider's rate limit and slot scheduler.

## Shape

```
call site ──► decision.Router ──► decision.Instrumented ──► Decider
                 │ per-site Band            │ span + metrics       ├─ jev.Client    (TypeSafe / gateway) ✅
                 │ per-session budget       │                      ├─ llm.Adapter   (any LLMProvider)    ✅
                 └─ Outcome.Path            └─ decision.evaluate   ├─ mock.Decider  (tests)              ✅
                                                                    └─ Off           (provider: off)      ✅
```

- **`Decider`** (`decider.go`): `Decide(ctx, *DecisionRequest) (*DecisionResponse, error)`, `Name()`, `Model()`. It is **not** an `LLMProvider` and is not registered in `pkg/llm/factory` or the model catalog.
- **Builders** (`build.go`): `Noul`, `Choice` (+`WithNoneOption`), `Score`, `NewRequest`, `Validate`. They enforce the option/level counts and the 64k-total / 32k-state token bounds client-side with a conservative estimator (`ValidateWith` takes a real counter).
- **Answer math** (`answer.go`): `Confidence` = (n·p_max − 1)/(n − 1), `NoulDecisiveness` = |2p − 1|, `MinConfidence` (the weakest answer governs), `CheckAnswers` (a response must answer exactly the request's questions with valid keys and ranges).
- **`Router`** (`router.go`): looks up the site's `Band`, checks the session budget, calls the decider, validates the answer, and returns an `Outcome` whose `Path` is one of DECIDER, FALLBACK, ERROR, DISABLED, BUDGET. It never returns an error: every failure is a path so the call site's fallback runs identically whatever went wrong. Sites without a configured band are **shadow**: the answer is attached, never acted on.
- **`Instrumented`** (`instrumented.go`): span `decision.evaluate` with provider, model, site, question ids, latency, tokens, cost, min and per-question confidence; metrics `decision.calls.total`, `decision.latency`, `decision.tokens.input`, `decision.cost`, `decision.errors.total`; the router adds `decision.fallbacks.total` labelled by path.
- **`llm.Adapter`** (`llm/adapter.go`): renders the request into one prompt (registry key `decision.llm_adapter`, fallback `DefaultTemplate`, kept identical by a test), calls any `LLMProvider` once, parses strictly. Tolerates fences and surrounding prose; rejects unknown ids, unknown option keys, missing distributions, out-of-range values as `ErrMalformedAnswer`. Reports the provider's real token usage and cost, thinking tokens included.

## Rules the code enforces or expects

1. **Proto is law.** Wire shapes are generated from `decision.proto`; instructions, options, levels and criteria are `google.protobuf.Value`, so structured JSON (a tool schema, a skill manifest) passes intact.
2. **Fallback is the existing mechanism.** A migrated site keeps its current code path and runs it when `Outcome.Act()` is false. Nothing in this package replaces behaviour on its own.
3. **Gates only tighten.** `DecisionBandMode_TIGHTEN_ONLY` marks sites (circuit breaker, admission hook) whose answers may add a failure count or raise `NoDecision → Ask`, never clear, allow, or bypass. The mode is carried on the `Outcome` for the site to honour; the router cannot enforce a site's semantics.
4. **State is filtered in code.** Callers send the fields a question needs. Tool payloads (query results) must never be placed in state. Accuracy of decision models degrades with irrelevant context, and egress is a deployment decision.
5. **Shadow by default.** A site with no band never acts. Bands are configured per site after a shadow report, not from vendor defaults.
6. **Budgets are counted.** `WithBudget(maxPerSession, maxCostUSD)` caps decisions per session; exhaustion is a `BUDGET` path, not an error.

## Shadow mode (Phase 1) ✅

A shadow comparison runs the decider **in the background, alongside** a call site's existing mechanism, and records both answers as one `DecisionShadowRecord` per question. Nothing branches on the decider; the rows are the evidence for a band.

- **Record and store** (`shadow.go`): `BuildShadowRecords` renders candidate and reference answers the same way ("true"/"false", option key, level index); `ShadowStore` is implemented on SQLite (migration 000010) and Postgres (000025, tenant-scoped with RLS) and exposed through `backend.DecisionShadowProvider`; `ShadowRecorder` swallows store errors into `decision.shadow.errors.total`.
- **Sites** (`sites/`): request builders and reference mappings live here, not inline, so `loom decision replay` builds the exact request a live agent builds.
  - `tool.failure_kind` (`sites/failure.go`): `Choice` over {not_a_failure, transient, server_saturated, auth, bad_input, not_found, other} plus a `Noul` "did the call fail in a way an identical retry would clear". State is `{tool, error_code, error_text[:2000], input_digest}`, never the input or payload. Reference maps `Success` plus `fabric.InferErrorType` onto the kinds. After the first shadow run, `InferErrorType` gained five classes (`server_saturated`, `numeric_overflow`, `constraint_violation`, `invalid_input`, `not_found`) so the reference stops answering `unknown` for 57% of failures; its six original classes and their consumers in the guardrail engine are unchanged.
  - `recall.rerank`, `tool_search.rerank` (`sites/rerank.go`): one `Noul` per candidate, up to 64; reference is the set the LLM rerank kept.
- **Wired sites**: `Agent.rerankMemories` (`pkg/agent/decision_shadow.go`), `Agent.executeToolWithSelfCorrection`, `registry.rerankWithLLM` (`pkg/tools/registry/decision_shadow.go`). Shadows run in goroutines detached from the turn's cancellation with a 30 s cap; `WaitDecisionShadows()` exists for tests and shutdown.
- **Configuration**: a `decision:` block in agent YAML (`provider: off|llm|mock|jev`, `llm_role`, `model` pinned unless `allow_alias`, budgets, `bands`), converted with validation in `config_loader.go`. The registry passes the server's shadow store to every agent; `looms serve` obtains it from the storage backend.
- **Report** (`report/`): agreement with the reference, expected calibration error over 10 confidence bins, confusion (reference → candidate), decider error rate, latency percentiles, token and cost totals; rendered as Markdown.
- **CLI**: `loom decision replay --decider llm|mock [--provider P] [--errors-only] [--sample newest|random] [--limit N] [--concurrency N] [--dry-run]` runs `tool_executions` rows through the failure-kind classifier and writes shadow rows; `loom decision report --site S [--since 7d]` prints the report. Both read the local `loom.db`.
- **First results** (2026-09-22, gpt-4o through the LLM adapter, 5,000 executions): `kind` agrees with today's classifier 95–100% wherever that classifier has a rule; ≥0.9-confidence rows agree 98–99.9%; the entire disagreement is the classifier's `other` bucket (57% of a random failure sample: graph-memory FK failures, Teradata overflow, parameter validation), which the decider reads as `bad_input`. The retry question was found ill-posed on successes and reworded. Full write-up: `docs/research/decision-layer-phase1-report.md`.

## Jev client (Phase 2) ✅

`pkg/decision/jev` is the `Decider` for TypeSafe's System One API: one JSON POST to `/v1/systemone`, answers mapped onto the proto shapes, `CheckAnswers` on the way out.

- **Endpoints.** `DefaultBaseURL` is `https://api.typesafe.ai`; the Vercel AI Gateway serves the same API at `https://ai-gateway.vercel.sh/typesafe`. Base URL, path, auth header and extra headers are configuration.
- **Credentials** come from the environment only, never from agent YAML: `TYPESAFE_API_KEY`, else `AI_GATEWAY_API_KEY` (which also selects the gateway URL when no base URL is set), else `JEV_API_KEY`; `TYPESAFE_BASE_URL` overrides the endpoint. `jev.FromDecisionConfig` does the resolution for both the agent and the CLI.
- **Model ids.** Direct: a pinned release (`jev-1.13.0`, the default). Through the gateway the only id is `typesafe-ai/jev`, which is a floating alias: the gateway exposes no pinned releases and echoes that id back. Config validation therefore requires `allow_alias: true` for it, and a pinned id against the gateway is refused at construction rather than sent.
- **Failure handling.** 401/403 → `ErrUnauthorized`, 400/422 → `ErrValidation`, 429 → `ErrRateLimited`, 5xx/529 → `ErrOverloaded`; only rate limits, overload and transport failures are retried (3 attempts, `Retry-After` honoured, jittered backoff capped at 2 s, context-aware). Requests are validated locally before anything is sent.
- **Budget.** A per-process token-bucket limiter at 1,000 requests per minute (the published cap is 1,200), deliberately separate from the LLM slot scheduler. One client per agent would multiply that; share the client.
- **Cost.** Input tokens × the configured price (list $0.042 per million; output is free). A gateway response carries its own cost figure under `provider_metadata.gateway.cost`, which wins when present.
- **Tests**: httptest contracts for every primitive and status, wire-shape assertions, retry and backoff, context cancellation during backoff, transport errors, auth-header variants, limiter pacing with an injected clock and under the race detector, and the env resolution table. Fixture keys are fake.

- **First Jev results** (2026-09-23, gateway free tier, paced at 28 rpm because the tier allows 30): on 600 seeded failed executions scored by both deciders, Jev agreed with the refined reference on 94.0% of rows and gpt-4o on 79.9%; under the pre-refinement reference the ranking was reversed. The whole difference is 201 graph-memory foreign-key rows that Jev reads as `not_found` at 0.9 confidence and gpt-4o as `bad_input`; the reference was refined to `not_found` on the strength of the request's own option wording, and a human-labelled sample is the follow-up. Network latency 170–450 ms against 1.5 s for the adapter; list-price cost two orders of magnitude lower. Write-up: `docs/research/decision-layer-phase1-report.md` §2.4.

## Live paths (Phase 3) ⚠️

Code for three sites has landed; **no band is live in any shipped configuration**. The default remains shadow, and a site acts only where an operator configures a band for it after reading its shadow report.

A live site follows one shape, implemented separately at each site so its fallback stays the code that ran before:

1. If the site's band is shadow, run the existing mechanism and shadow the decider in the background (Phase 1 behaviour).
2. Otherwise ask the decider **first**, synchronously, bounded by `liveDecideTimeout` (3 s). This is the latency a live band trades for skipping a generative call.
3. If `Outcome.Act()` and the answer is actionable, return it; the generative call never happens. The row is recorded with path `DECIDER` and no reference.
4. Otherwise run the existing mechanism and record the answer already in hand against it (path `FALLBACK`). One decider call per site visit, never two.

**Band aggregate.** `DecisionBand.aggregate` is `MIN` (default: the weakest answer must clear `act_min`) or `PER_QUESTION` (each answer is judged on its own). Fan-out reranks use `PER_QUESTION`: with `MIN` one uncertain candidate out of forty would disable the whole answer. YAML: `bands[].aggregate: min | per_question`.

| Site | Live behaviour | Actionable when | Fallback |
|---|---|---|---|
| `recall.rerank` (`Agent.rerankMemories`) ✅ code | keep a candidate when its Noul says relevant (p ≥ 0.5) **or** the answer does not clear the band (an uncertain memory is kept, never dropped) | at least one answer cleared the band (`sites.RerankContributed`); if every answer is uncertain the LLM rerank runs | `rerankMemoriesLLM` |
| `tool_search.rerank` (`registry.Search` stage 3) ✅ code | same keep rule; results ordered by relevance probability, `Confidence` = probability, a `RelevanceSignal{type: "decision"}` appended, span attribute `tool_search.rerank = decision \| llm_after_decision \| llm` | same | `rerankWithLLMOnly` |
| `workflow.branch` (`orchestration.ConditionalExecutor`) ✅ code | one `Choice` over the pattern's branch keys plus `none_of_these`; state is the condition prompt (≤4,000 runes) and the sorted keys; the selected key's branch runs without the condition agent's turn | the answer clears the band and names a configured branch, **or** is `none_of_these` and a default branch exists; an answer outside the option set is never acted on | the condition agent turn, coercion, retry policy, default branch (`evaluateAndSelect`), reference = the key that path selected (`none_of_these` when it fell to the default) |

The executor borrows the condition agent's decision layer through `Agent.LiveDecide`, `Agent.RecordDecisionAsync` and `Agent.RunDecisionShadow`; the band is therefore configured on the condition agent's YAML. Rows for the site are keyed to the condition session id.

**Status per site.** ⚠️ `recall.rerank`, `tool_search.rerank`, `workflow.branch`: live path implemented and tested with the mock decider; no shadow report has been produced for any of them against a real decider, so no band is recommended yet. 📋 `conversation.rerank`, intent, stage validation, swarm/debate: not started.

## Not yet implemented

- 📋 A Noul-specific band threshold. Jev answers easy "false" Nouls at probabilities of 0.1–0.4, which the decisiveness mapping treats as low confidence; a band keyed on probability for Noul questions would remove that artefact from ECE.
- 📋 Fleet-rate access. The gateway free tier is 30 requests per minute; fleets need the direct TypeSafe endpoint or a paid tier, with `RequestsPerMinute` set from the tier.
- 📋 Conversation-search rerank shadow (`segmented_memory.go`) and the plan's Phase 3.5–3.7 and Phase 4–5 sites.
- 📋 Shadow reports for the three Phase 3 sites against a real decider (the replay CLI covers `tool.failure_kind` only; these sites need live agent traffic with `provider: jev` in shadow, then `loom decision report --site recall.rerank` and friends).
- 📋 Baseline capture of scheduler queue wait and recall starvation rate on the gauntlet rig (an operations task; see the plan).

## Tests

`go test -tags fts5 -race ./pkg/decision/...` covers builders (table-driven), answer math, router paths and budgets (including a concurrent band-rewrite test), the instrumented wrapper against a recording tracer, the mock, the LLM adapter against a scripted provider, shadow record construction and the recorder, the report math against hand-computed ECE, and the site builders and reference mappings. Fuzz targets: `FuzzToValueRoundTrip`, `FuzzValidateState`, `FuzzParseAnswers`, `FuzzExtractObject`. Store tests: `pkg/storage/sqlite` (temp DB through the migrator, concurrent writes) and `pkg/storage/postgres` (integration, `TEST_POSTGRES_URL`, tenant isolation). Wiring tests: `pkg/agent/decision_shadow_test.go` (shadow and live rerank paths, exported helpers with the layer off), `pkg/tools/registry/decision_shadow_test.go` (shadow, live ordering and signals, shadow band never asks, below-band recording), `pkg/orchestration/conditional_decision_test.go` (live branch skips the condition agent, `none_of_these` with and without a default, below-band fallback, shadow band, decider error), `cmd/loom/decision_test.go` (seeded `loom.db`, replay + report), `cmd/looms/registry_subsystems_test.go`.
