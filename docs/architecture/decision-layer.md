# Decision Layer

**Status**: ⚠️ Partial. Core package implemented (Phase 0); no call site uses it yet; no Jev client yet.
**Package**: `pkg/decision` (+ `pkg/decision/mock`, `pkg/decision/llm`)
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
                 │ per-site Band            │ span + metrics       ├─ llm.Adapter   (any LLMProvider)   ✅
                 │ per-session budget       │                      ├─ mock.Decider  (tests)             ✅
                 └─ Outcome.Path            └─ decision.evaluate   ├─ Off           (provider: off)     ✅
                                                                    └─ jev client    (Phase 2)          📋
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

## Not yet implemented

- 📋 Jev HTTP client (`pkg/decision/jev`), including a configurable base URL for gateways (Vercel AI Gateway serves Jev) and its own rate limiter separate from the LLM slot scheduler.
- 📋 Shadow harness and `decision_shadow` store on SQLite and Postgres; `loom decision replay|report`.
- 📋 Agent/server config block (`decision:`) and `WithDecider` wiring.
- 📋 Any call-site migration. The ranked list is in the plan, §3 Phase 3–5.

## Tests

`go test -tags fts5 -race ./pkg/decision/...` covers builders (table-driven), answer math, router paths and budgets (including a concurrent band-rewrite test), the instrumented wrapper against a recording tracer, the mock, and the LLM adapter against a scripted provider. Fuzz targets: `FuzzToValueRoundTrip`, `FuzzValidateState`, `FuzzParseAnswers`, `FuzzExtractObject`.
