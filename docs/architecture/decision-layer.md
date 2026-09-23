# Decision Layer

**Status**: ⚠️ Partial. Core package (Phase 0), shadow harness (Phase 1) and the Jev client (Phase 2) implemented; three call sites shadow-record; no site acts on a decider yet; no Jev replay has been run yet (needs a gateway key).
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

## Not yet implemented

- 📋 A Jev shadow replay. The harness is ready (`loom decision replay --decider jev`); it needs `AI_GATEWAY_API_KEY` in the environment. The Vercel free tier for Jev ends 2026-09-25 and requires a card on file.
- 📋 Conversation-search rerank shadow (`segmented_memory.go`) and every other site in the plan's Phase 3–5 list.
- 📋 Any live band. No site acts on a decider answer until its shadow report has been reviewed.
- 📋 Baseline capture of scheduler queue wait and recall starvation rate on the gauntlet rig (an operations task; see the plan).

## Tests

`go test -tags fts5 -race ./pkg/decision/...` covers builders (table-driven), answer math, router paths and budgets (including a concurrent band-rewrite test), the instrumented wrapper against a recording tracer, the mock, the LLM adapter against a scripted provider, shadow record construction and the recorder, the report math against hand-computed ECE, and the site builders and reference mappings. Fuzz targets: `FuzzToValueRoundTrip`, `FuzzValidateState`, `FuzzParseAnswers`, `FuzzExtractObject`. Store tests: `pkg/storage/sqlite` (temp DB through the migrator, concurrent writes) and `pkg/storage/postgres` (integration, `TEST_POSTGRES_URL`, tenant isolation). Wiring tests: `pkg/agent/decision_shadow_test.go`, `pkg/tools/registry/decision_shadow_test.go`, `cmd/loom/decision_test.go` (seeded `loom.db`, replay + report), `cmd/looms/registry_subsystems_test.go`.
