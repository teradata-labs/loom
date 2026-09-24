# Implementation Plan: Typed Decision Layer (Jev-backed)

**Status**: 📋 Planned. Nothing implemented.
**Date**: 2026-09-22
**Baseline**: `fix/hitl-hold-heartbeat` @ `bf64528d` (= main + heartbeat fix), v1.4.0
**Research**: `docs/research/jev-system-one-assessment.md` (what Jev is, evidence, 24-point inventory, 15 ranked placements)
**Companion**: `docs/research/tool-calling-assessment.md` (the measured problem this addresses)

---

## 0. What this plan delivers, and what it does not

**Delivers.** A proto-defined typed decision abstraction (`Decider`) with four implementations (mock, LLM-adapter, Jev, instrumented wrapper), a shadow-evaluation harness that compares a candidate decider against whatever mechanism a call site uses today, and the staged migration of ranked call sites onto it, each behind a confidence band with the existing mechanism kept as fallback.

**Does not deliver.** A faster main conversation loop. The four budgeted generative calls per median turn and serial tool execution are untouched. What changes:

| Effect | Where | Expected size | How we will know |
|---|---|---|---|
| Latency removed | Recall rerank before first token (20 s timeout today) | 1–3 s per turn on turns with recall candidates | `recall.rerank.duration_ms` span attr, before/after |
| Latency removed | Workflow branch selection (whole agent turn + up to 10 retries) | seconds → ~0.3 s per branch | `conditional.agent.*` span duration |
| Latency removed | `tool_search` balanced-mode rerank | one generative call per search | `tool_search.rerank.duration_ms` |
| Cost removed | The above three, plus intent classification and stage validation | 2–4 aux generative calls per turn → Jev calls at ~1/100 the cost | `decision.cost_usd` vs `llm.cost_usd` on aux spans |
| **Latency added** | Failure-kind classification, risk scoring, per-turn judge | +~0.3 s each, where **nothing** runs today | `decision.evaluate` span duration |
| Efficiency | Fewer retry storms once failure kinds reach the breaker | 29.2% error rate / 75.9% retry-after-error today | §3 metrics in the tool-calling assessment, re-run |
| **Generative bandwidth freed** | Every aux call moved off the LLM provider stops consuming its RPM/TPM quota and a slot in the LLM slot scheduler | ~4 of ~8 calls per median turn leave the provider (fewer by tokens, since aux prompts are smaller than main-loop prompts); under fleet load this is the binding constraint | scheduler queue wait p50/p95 for main-loop calls; `recall.query_source=keyword` rate (starvation signal); 512-agent gauntlet throughput |

The bandwidth row is the one that matters at scale. The aux calls are not just slow on their own turn: they queue in the same rate limiter and slot scheduler as the main conversation loop, so a fleet's reasoning calls wait behind reranks and cadence extractions. The recall side-call already degrades to keyword search when starved (`recall.query_source`), and the 512-agent runs showed slot holding as the contention problem. Jev has its own quota (1,200 rpm, separate limiter in Phase 2), so those decisions leave the generative queue entirely.

The honest framing: **cheaper aux path, faster in three places, more correct in three others, more generative headroom under load, and measurable at every step.** No number is promised until Phase 1's shadow report produces it. Phase 1 therefore also records scheduler queue wait before and after.

---

## 1. Decisions (resolved 2026-09-22)

| # | Decision | Resolved | Consequence |
|---|---|---|---|
| D1 | TypeSafe API access | **Via Vercel AI Gateway**, which serves Jev on its free tier (owner, 2026-09-22). No direct TypeSafe key needed. | Phase 2 is unblocked. The client must take a configurable `base_url` and auth header so the same code speaks to `api.typesafe.ai` directly or to the gateway path; independent measurements put the gateway at ~580 ms per call vs ~300 ms direct, which the shadow report will show. |
| D2 | Package name and location | `pkg/decision` (framework-generic, importable) | Sibling of `pkg/llm`, not inside it: Jev is not an `LLMProvider`. |
| D3 | Shadow record storage | hawk span attributes **and** a `decision_shadow` table on **both SQLite and Postgres** | Two migrations (`pkg/storage/sqlite/migrations/000010_*`, `pkg/storage/postgres/migrations/000025_*`) and a `DecisionShadowStore` interface in `pkg/storage/backend`, implemented by both backends. The report CLI reads through the interface. |
| D4 | Config block placement | `decision:` block in agent YAML beside `judge_llm` / `classifier_llm`; server-level default in `looms` config | Per-agent bands, one shared client. |
| D5 | Default state | `provider: off` | Enterprise deployments that cannot egress must not need to opt out. |
| D6 | Branch | `feat/decision-layer` off `main` | Docs moved here; the heartbeat PR is untouched. |
| D7 | First PR scope | **Phase 0 only** | Proto + `pkg/decision` + mock + LLM adapter + tests. No behaviour change anywhere. |

---

## 2. Architecture

```
call site (e.g. rerankMemories)
   │  builds DecisionRequest with a per-site StateFilter (send fields, not transcripts)
   ▼
decision.Router ── band lookup per site ──► high confidence: return typed answer
   │                                        uncertain: run today's mechanism (fallback func)
   │                                        gate sites: brake-only / raise-only semantics
   ▼
decision.Instrumented ── span decision.evaluate; metrics; cost; per-question confidence
   ▼
decision.Shadow (Phase 1 only) ── also records reference answer + candidate answer to decision_shadow
   ▼
Decider ── one of: mock | llm (adapter over any LLMProvider) | jev (HTTP) | off (always uncertain)
```

Design rules, all derived from the research doc:

1. **Proto is law.** Request/response shapes live in `proto/loom/v1/decision.proto`. Go types are generated.
2. **Not a provider.** No `case "jev"` in `pkg/llm/factory`; no catalog entry. `Decider` is a separate interface.
3. **Fallback is the existing mechanism**, not a different model. Every migrated site keeps its current code path and calls it when the router says "uncertain" or the decider errors.
4. **Gates only tighten.** For the circuit breaker and admission hook, a decision may add a failure count or raise `NoDecision → Ask`. It may never clear a failure, lower restrictiveness, or bypass fail-closed.
5. **State is filtered in code.** Each site has a `StateFilter` that selects fields. Tool result payloads (customer rows) never leave the process; error text, codes, tool names, input digests do.
6. **Pin the model.** `jev-1.13.0` in config; the alias is rejected by validation unless `allow_alias: true`.
7. **Every decision is counted.** Per-session decision budget and cost, visible in traces and in `loom analytics`, closing the "aux calls are unbounded and unattributed" root cause (RC6).

---

## 3. Phases

### Phase 0 — Proto and core package (no vendor dependency)

**Goal**: the abstraction, fully tested, with an LLM-backed implementation so every downstream phase is testable before a Jev key exists.

Files to create:

| Path | Content |
|---|---|
| `proto/loom/v1/decision.proto` | `DecisionRequest`, `Question` (oneof `NoulQuestion` / `ChoiceQuestion` / `ScoreQuestion`), `DecisionResponse`, `Answer` (oneof), `DecisionUsage`; `DecisionConfig` (provider, model, allow_alias, timeout_ms, budget), `DecisionBand` (site, act_min, escalate_max), `DecisionSiteConfig`. Values as `google.protobuf.Value` so instructions/criteria can be JSON per the API. |
| `pkg/decision/decider.go` | `Decider` interface: `Decide(ctx, *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error)`, `Name()`, `Model()`. Typed errors: `ErrValidation`, `ErrRateLimited`, `ErrOverloaded`, `ErrUnauthorized`, `ErrStateTooLarge`. |
| `pkg/decision/build.go` | Request builders with API-limit validation: `Noul(instr, opts...)`, `Choice(instr, options, opts...)` (≤255, rejects duplicate keys, `WithNoneOption()` adds `none_of_these`), `Score(instr, levels)` (2–10). Client-side token guard against 64k total / 32k state+longest-question using the existing tiktoken counter. |
| `pkg/decision/answer.go` | Accessors: `resp.Choice(id)`, `resp.Score(id)`, `resp.Noul(id)`; `Confidence()` recomputed locally as `(n·p_max−1)/(n−1)` and compared to the wire value (drift detector). |
| `pkg/decision/router.go` | `Router{bands map[site]Band}`; `Route(ctx, site, req, fallback func() (T, error))`; band semantics `act / uncertain / escalate`; gate mode `TightenOnly`. Records which path answered (`decision.path ∈ {decider, fallback, error}`) for observability. |
| `pkg/decision/instrumented.go` | Mirrors `pkg/llm/instrumented_provider.go`. Span `decision.evaluate` with `decision.provider`, `decision.model`, `decision.site`, `decision.questions`, `decision.confidence.<id>`, `decision.input_tokens`, `decision.cost_usd`, `decision.latency_ms`. Metrics `decision.calls.total`, `decision.latency`, `decision.cost`, `decision.errors`, `decision.fallbacks`. |
| `pkg/decision/mock/` | Table-driven `Decider` for tests: scripted answers per question id, optional error/latency injection. |
| `pkg/decision/llm/` | Adapter: renders a `DecisionRequest` to a prompt that asks any `types.LLMProvider` for the exact JSON answer shape, parses strictly, normalizes probabilities to sum 1, computes confidence. This is the fallback comparator and the egress-free path (Ollama). |
| `pkg/decision/off.go` | `Off` decider: always returns `ErrDisabled`; router treats as fallback. Makes `provider: off` a first-class no-op. |
| `pkg/observability/instrumentation.go` | New span/metric name constants (`SpanDecisionEvaluate`, `MetricDecision*`). |
| `docs/architecture/decision-layer.md` | Architecture doc with status indicators. |

Tests (all `-tags fts5 -race`):
- Table-driven builder validation (limits, duplicates, none-option).
- Fuzz: `FuzzParseAnswer` (llm adapter JSON parsing), `FuzzStateMarshal` (state → JSON, token guard).
- Router: band edges, fallback on error, `TightenOnly` never lowers, path attribution.
- Instrumented: attributes present, cost computed, no goroutine leaks (race).
- Golden request JSON per builder.

Acceptance: `just check` green; `buf lint` and `buf breaking` clean; coverage >80% in `pkg/decision`.

### Phase 1 — Shadow harness and offline replay

**Goal**: measure before branching. Produce the first agreement/ECE report using only the LLM adapter and existing telemetry.

| Path | Content |
|---|---|
| `pkg/decision/shadow.go` | `Shadow` wraps a `Decider`; `Record(site, req, candidateResp, referenceAnswer, referenceSource, latency)`; writes to hawk (span attrs) and to storage. |
| `pkg/storage/backend/decision_shadow.go` | `DecisionShadowStore` interface: `RecordShadow(ctx, *ShadowRecord) error`, `QueryShadow(ctx, site, since, limit) ([]*ShadowRecord, error)`; wired through the backend factory like the other stores. |
| `pkg/storage/sqlite/migrations/000010_decision_shadow.{up,down}.sql` and `pkg/storage/postgres/migrations/000025_decision_shadow.{up,down}.sql` | `decision_shadow(id, ts, site, session_id, question_id, kind, candidate_answer, candidate_confidence, candidate_probabilities_json, reference_answer, reference_source, latency_ms, input_tokens, model)` + index on `(site, ts)`. Postgres version carries the tenant column and RLS policy the other tenant-scoped tables use. Both backends get store implementations and integration tests. |
| `pkg/decision/report/` | `Agreement(site)`, `ECE(site, bins=10)`, `Confusion(site)`, `LatencyPercentiles(site)`; renders Markdown. |
| `cmd/loom/cmd_decision.go` | `loom decision replay --site failure_kind --db ~/.loom/loom.db --decider llm|jev --limit N` (replays `tool_executions` rows through the P6 request builder; reference = `Success` flag + `InferErrorType`); `loom decision report --site X`. Follows the `loom scheduler` subcommand pattern. |

Shadow wiring (branch on nothing yet): recall rerank (`agent.go:3398`), tool_search rerank (`registry.go:799`), failure classifier (new builder over `Result` + `InferErrorType` reference at `guardrails.go:215`).

Baseline capture (before any site goes live): record per-turn generative call count, aux vs main token share, LLM slot-scheduler queue wait p50/p95, and `recall.query_source=keyword` rate on the MCP gauntlet rig at R3 and R7 load. These are the numbers the bandwidth claim in §0 is measured against.

Acceptance: `loom decision report --site failure_kind` renders agreement, ECE, confusion over ≥1,000 replayed rows using the LLM adapter; shadow adds no failure path to any call site (errors are swallowed and counted); `-race` clean under the concurrent recall path; baseline capture committed as a doc table.

### Phase 2 — Jev client

**Goal**: a production-grade client, no unofficial SDK dependency. **Blocked on D1.**

| Path | Content |
|---|---|
| `pkg/decision/jev/client.go` | stdlib `net/http`; `POST {base_url}/v1/systemone`; bearer auth from `TYPESAFE_API_KEY` or config; JSON marshal from proto; maps 401/422/429/529 to typed errors; honours `Retry-After`; exponential backoff on 429/529 with jitter, capped attempts; per-request timeout (default 2 s, since p95 independent measurements are <1 s). |
| `pkg/decision/jev/limiter.go` | Process-wide token bucket at configured rpm (default 1,000 of the 1,200 published) and tokens/s; shared across agents in one `looms`; does **not** use the LLM slot scheduler (different resource, different SLO). |
| `pkg/decision/jev/testdata/` | Recorded fixtures (request/response pairs) with **fake keys** (`jv_test_…`, never real-looking tokens). |
| `pkg/decision/jev/client_test.go` | httptest server: happy path per primitive, each error code, alias rejection, size guard, limiter behaviour under `-race` with 100 goroutines. |
| `pkg/agent/config_loader.go` | `decision:` YAML block → `DecisionConfig`; validation (model pinned unless `allow_alias`); wiring into agent options `WithDecider`. |
| `cmd/looms/config.go` | Server-level default decision config; env var documentation. |
| `docs/reference/configuration.md` (or the existing config reference) | Config keys with status indicators. |

Acceptance: contract tests green offline; one live smoke test behind `LOOM_INTEGRATION=1` and a real key; `loom decision replay --decider jev --site failure_kind --limit 2000` produces the first Jev shadow report (≈$0.80).

### Phase 3 — Tier 1 call sites (drop-in replacements)

Each site follows the same four steps: **shadow → report → band → live**, with the existing code path as fallback. Order:

| Step | Site | Request shape | Fallback | Success metric |
|---|---|---|---|---|
| 3.1 | Recall rerank `pkg/agent/agent.go:3398` | `Noul` per candidate ("could this memory bear on the message?"), state = `{message[:500], candidate}` fan-out in one request; batch >~40 candidates | current LLM rerank | recall precision on LongMemEval harness ≥ current; `rerank.duration_ms` p50 |
| 3.2 | Conversation search rerank `segmented_memory.go:844` — **unreachable on `main` (no production caller), skipped 2026-09-23** | `Score` 0–10 collapsed to 5 levels per candidate | BM25 order | same harness |
| 3.3 | tool_search rerank `registry.go:799` | `Score` per candidate; replaces the `1/(1+(-bm25/10))` confidence with a calibrated one | current LLM rerank | wrong-tool rate on the tool_search eval |
| 3.4 | Workflow branch `conditional_executor.go:166` | `Choice` over `Branches` keys + `none_of_these`; state = condition prompt rendered with stage output | existing agent turn + `retryConditionEvaluation` | branch-selection latency; retry count → 0 on confident cases |
| 3.5 | Intent `patterns/llm_classifier.go:84` — **unreachable on `main` (`ClassifyIntent` never called), skipped 2026-09-23** | `Choice` over `Intents` + `none`; confidence feeds `shouldInvokeLLMReRanker` directly, deleting the 0.70/0.20/0.60 constants | keyword classifier | pattern-selection accuracy on the Teradata library eval |
| 3.6 | Stage validation `pipeline_executor.go:866` (done); acceptance criteria `output_validator.go:274` (deferred: nothing evaluates the field today) | one `Noul` {requirement, output}; `tighten_only` honoured | current LLM call + scraped verdict | replaces `contains("valid")` scraping when a band is live |
| 3.7 | Swarm tie-break `swarm_executor.go` / debate consensus `collaboration/debate.go` (done) | `Choice` over the tied choices / `Noul` agree | judge agent turn / confidence-mean heuristic | n/a, correctness only; consensus needs human labels |

Acceptance per step: shadow agreement ≥ the site's threshold (set from the report, default ≥0.9 vs reference), ECE reported, band configured, `-race` clean, docs status flipped to ✅ for that site.

- [x] Decision judge (`JUDGE_TYPE_DECISION`, `JudgeConfig.decision`) — 2026-09-23 on `feat/decision-layer-phase3`: one Noul per criterion + a quality Score in one request at `judge.<id>`; verdict from criterion probabilities; `judges.NewJudgeFromConfig` dispatches by type at all three judge call sites (judge service, A/B scoring, `looms eval`). Tests against the mock decider. See `docs/architecture/decision-layer.md` "Decision judge".
- [x] Direct use — 2026-09-23: `sites.Ask*` (site `tool.decide`), the `decide` builtin behind `decision.expose_tool`, and `loom decision ask`. Tests against the mock decider (site builders, tool registration/execution/recording, CLI text and JSON output).
- [ ] LongMemEval A/B for 3.1 (A = LLM rerank + Jev shadow, B = Jev live on `recall.rerank`, N=40 multi-session + knowledge-update, gpt-4o server, per-decision grading against evidence sessions via `grade_recall.py`). Relaunched 2026-09-23 19:55Z after finding that isolate-mode temp agents were named by question id alone, so a rerun recalled memories left by earlier runs (`DeleteAgent` does not purge graph memory); fixed with a per-run nonce in the agent name and session provenance on `graph_memory`-tool writes. Second fix 2026-09-24 00:19Z: `decision.SessionIDFromContext` ignored the agent session, so every live-turn row had an empty `session_id`. **Cell A multi-session readout (1,061 graded decisions):** Jev precision 95.0% / recall 56.0% vs LLM rerank 97.9% / 40.4%; end-to-end 20/37 (54.1%). Cell B and the knowledge-update cells still running.

### Phase 4 — Tier 2 gaps (guard-railed)

| Step | Site | Semantics | Guard |
|---|---|---|---|
| 4.1 | **Failure kind → circuit breaker** `agent.go:3026` + `conversation_helpers.go:179` | `Choice` {not_a_failure, transient, server_saturated, auth, bad_input, not_found, other} + `Noul` "identical retry would help"; feeds `breaker` failure count and the identical-failure refusal | **Brake-only.** Independent of, and layered on, the P0 fix "count `Result.Success==false`" from the assessment, which lands first regardless. Jev adds *kind* so backpressure/transient are not over-penalized. State = `{tool, error_code, error_text[:2k], input_digest}`; never the payload. |
| 4.2 | **Risk hook** `shuttle/admission_hook.go:55` | New `Hook`: `Score` {read-only, reversible write, irreversible, touches production} + Nouls (deletes_data, affects_other_users, names_prod_resource) over `Canonicalize(call)` + tool description | **Raise-only.** May return `Ask`, never `Allow`; sits after `permHook` and denylist; fail-closed unchanged. Adversarial fixture set (injected instructions in params) must not lower the score. Thresholds per tool class from pending-permission history. Also populates the never-computed `ToolPermissionRequest.risk_level`. |
| 4.3 | **TypedJudge** `evals/judges/judge.go:37` | Implements `Judge.Evaluate`: `Noul` battery (answers the question; claims success while a tool result errored; names a table absent from schema) + `Score` per dimension; aggregator unchanged | Runs on every turn in a canary agent; uncertain band → LLM judge. Ties to the runtime-verification P0 in `loop-engineering.md`. |
| 4.4 | **Skill / pattern selection** `skills/index/router.go:170`, `library.go:300` | Two-stage: `Choice` over all N with truncated descriptions + gate Nouls; re-rank top 3 with full text | Fallback: current router / FTS. Metric: wrong-load rate on the 84-pattern library. |
| 4.5 | Hygiene `auditor.go:228`, done-ness `task/manager.go:614` | `Noul` each | Advisory first; surfaces a field, does not change state transitions until measured. |

### Phase 5 — Tier 3 (measure first, no commitment)

Extraction trigger (`agent.go:2884`), entity dedupe (`graph_memory_extractor.go:583`, vendor entity-alignment recipe), compression keep/drop (`context_compilation.go:561`), model tier routing (`registry.go:613`), offline bulk trace classification (`loom analytics tools`). Each enters shadow with its own report before any band.

---

## 4. Cross-cutting requirements

- **Budgets.** `DecisionConfig.max_per_session`, `max_cost_usd_per_session`; exceeded → router falls back, emits `decision.budget_exhausted`. Counted next to `MaxTurns` / `MaxToolExecutions`.
- **Observability.** Every call traced; `loom analytics` gains decision columns; shadow path attribution lets a trace say which mechanism answered.
- **Security review** (`/security-review` before Phase 4 merges): injection fixtures for 4.1 and 4.2; state filters audited for payload leakage; key handling; zero-retention noted as contractual.
- **Docs.** `docs/architecture/decision-layer.md` (✅/⚠️/📋 per site), config reference, this plan updated per phase, README "📋 Planned" entry until Phase 3.1 ships, then "✅ Implemented" for the shipped sites only.
- **CI.** `just check` green at every commit; `gofmt -l` clean; `buf breaking` against main.
- **Rollback.** `provider: off` disables everything at runtime with no code change; each site's fallback is the pre-plan behaviour.

---

## 5. Test strategy summary

| Layer | Tests |
|---|---|
| Builders / parsing | table-driven + `FuzzParseAnswer`, `FuzzStateMarshal` |
| Router | band edges, error → fallback, `TightenOnly`, concurrency `-race` |
| Jev client | httptest contract per code path; limiter under 100 goroutines |
| Shadow | never fails the caller; storage under concurrent writes |
| Call sites | existing tests keep passing with `provider: off`; new tests with `mock` decider per band |
| Integration | `LOOM_INTEGRATION=1` live smoke; LongMemEval recall harness before/after 3.1; MCP gauntlet rig for 4.1 storms |
| Regression yardstick | re-run tool-calling assessment §3 queries after Phase 4.1 |

---

## 6. Checklist

### Phase 0 — core (no key needed) — ✅ implemented 2026-09-22 on `feat/decision-layer`
- [x] `decision.proto` + `buf generate`, lint, breaking
- [x] `pkg/decision`: decider, build, answer, router, instrumented, off
- [x] `pkg/decision/mock`, `pkg/decision/llm` (+ `prompts/decision/llm_adapter.yaml`, drift-guarded by a test)
- [x] observability constants (`decision.evaluate` span, `decision.*` metrics)
- [x] tests: table-driven, fuzz, race; coverage 92.8% / 89.9% / 93.9% (core / llm / mock)
- [x] `docs/architecture/decision-layer.md`
- Finding from fuzzing: invalid UTF-8 in state made `ToValue` fail and the builders panic. Fixed by sanitizing to U+FFFD; tool-result-derived state can carry arbitrary bytes.

### Phase 1 — shadow (no key needed) — ✅ code landed 2026-09-22 on `feat/decision-layer`
- [x] `shadow.go` (record, store contract, recorder) + SQLite 000010 + Postgres 000025 (RLS) + `backend.DecisionShadowProvider` + `report/`
- [x] `loom decision replay|report` (replay reads `tool_executions`, writes shadow rows; mock and LLM-adapter deciders; dry run)
- [x] shadow wiring: recall rerank, tool_search rerank, failure classifier; `decision:` config block (moved forward from Phase 2 so shadows can be switched on per agent)
- [x] `sites/` package so replay and live agents build identical requests and references
- [x] first report over ≥1,000 rows with the LLM adapter — three replays on the rig (gpt-4o via Azure OpenAI): newest 2,000 mixed, newest 1,500 errors, random 1,500 errors across the full history; results and findings in `docs/research/decision-layer-phase1-report.md` (kind agreement 95%+ outside the reference's `other` blind spot; ≥0.9-confidence rows agree 99.9%; retry question reworded after the run exposed it as ill-posed on successes)
- [x] baseline capture on the gauntlet rig — from the 2026-09-22 TPC-H run: 1,494 grants / **987 starvation promotions (66%)**, sessions of 3 LLM calls taking p50 611 s; same doc §1. Gaps recorded: no per-request queue-wait metric (follow-up), recall starvation needs a graph-memory-on rerun paired with Phase 3.1.

### Phase 2 — Jev client — ✅ client landed 2026-09-23
- [x] `pkg/decision/jev`: client (TypeSafe direct or Vercel AI Gateway; configurable base URL, path, auth header), typed status mapping, retries with Retry-After, per-process 1,000 rpm limiter, cost from list price or the gateway's own figure, contract tests with fake keys
- [x] `jev.FromDecisionConfig`: credentials from env only (`TYPESAFE_API_KEY` → `AI_GATEWAY_API_KEY` → `JEV_API_KEY`); gateway implies `typesafe-ai/jev`, which config treats as a floating alias (`allow_alias: true` required); a pinned id against the gateway is refused
- [x] `provider: jev` wired in the agent and in `loom decision replay --decider jev [--base-url]`
- [x] `fabric.InferErrorType` extended with the five classes the Phase 1 report surfaced; `sites.FailureKindReference` maps them
- [x] **live smoke + first Jev shadow report** — 2026-09-23 through the Vercel AI Gateway after card verification. Gateway free tier caps Jev at **30 rpm** (429 + `Retry-After: 60`); paced at 28 rpm, one worker. Paired with gpt-4o on identical seeded rows (`--seed 42`): failed-executions slice **Jev 94.0% vs gpt-4o 79.9%** and mixed slice **Jev 98.2% vs gpt-4o 94.5%** under the refined reference (77.2/93.6% vs 96.9/99.3% under the original), the difference being the foreign-key rows Jev reads as `not_found`; combined 1,200 executions: Jev 96.1%, gpt-4o 87.2%; latency 170–450 ms vs 1.5 s; cost $0.04 list vs $3.37. Details: `docs/research/decision-layer-phase1-report.md` §2.4. Follow-ups: human-label the FK/overflow classes; Noul-specific band threshold (probability, not decisiveness); direct TypeSafe endpoint for fleet rates. Command, on the rig: `AI_GATEWAY_API_KEY=… ./loom decision replay --db loom-slim-jev.db --decider jev --limit 1500 --errors-only --sample random --concurrency 8` then `report`; compare with §2.3 of the Phase 1 report

### Phase 3 — Tier 1 sites (each: shadow → report → band → live)
- [x] `DecisionBand.aggregate` (MIN | PER_QUESTION) in proto, router, YAML (`bands[].aggregate`) — 2026-09-23 on `feat/decision-layer-phase3`. Fan-out reranks need it: under MIN one uncertain candidate disables the whole answer.
- [x] 3.1 recall rerank — live path code: decider first when the band is live, keep relevant-or-uncertain, LLM rerank when no answer clears the band; one decider call per visit. **Shadow report against a real decider: not yet run** (needs live agent traffic with graph memory on; the replay CLI only covers `tool.failure_kind`). No band recommended yet.
- [~] 3.2 conversation rerank — **SKIPPED, unreachable**: `SegmentedMemory.SearchMessages` (the BM25 + LLM rerank at `segmented_memory.go`) has no production caller on `main` as of 2026-09-23; only tests reach it. Instrumenting it would measure nothing. Revisit if a `search_conversation` tool or equivalent lands.
- [x] 3.3 tool_search rerank — live path code in `Registry.Search` stage 3: ordered by probability, `decision` RelevanceSignal, span attribute `tool_search.rerank`. Shadow report: not yet run.
- [x] 3.4 workflow branch — new site `workflow.branch` (`sites/branch.go`): Choice over branch keys + `none_of_these`; live path in `ConditionalExecutor.Execute` skips the condition agent's turn; `none_of_these` acts only with a default branch. Shadow report: not yet run (needs conditional workflows in traffic).
- [~] 3.5 intent — **SKIPPED, unreachable**: the agent builds and installs `patterns.NewLLMIntentClassifier` (agent.go, `UseLLMClassifier`) but nothing ever calls `Orchestrator.ClassifyIntent` or `RecommendPattern` on `main` (pattern auto-injection was removed in the 2026-07 recut). The `shouldInvokeLLMReRanker` constants this step was to delete are equally dead. Revisit if pattern recommendation returns to the turn loop.
- [x] 3.6 stage validation — new site `stage.validation` (`sites/validation.go`): one Noul over {requirement, output}; live path in `PipelineExecutor.validateStageOutput` skips the validator LLM; **first site honouring `DecisionBandMode`** (`tighten_only` = decider may fail an output, never pass one). Shadow report: not yet run. `OutputPolicy.acceptance_criteria` left as a separate feature: nothing evaluates it today.
- [x] 3.7 swarm/debate — `swarm.tie_break` (`sites/tiebreak.go`, `SwarmExecutor.breakTie`, judge agent's layer; Choice over the tied choices only) and `debate.consensus` (`sites/consensus.go`, `DebateOrchestrator.decideConsensus`, internal moderator's layer; Noul over positions; `tighten_only` = may withhold consensus, never declare it). Note: `debate.consensus` replaces a no-call heuristic, so a live band there **adds** a call; acceptance needs a human-labelled sample, not just agreement with the heuristic. Judge prompt lost its "You are acting as a judge" framing (rule 8). Shadow reports: not yet run.
- [x] Phase 3 evidence, first campaign — 2026-09-23 on the rig, Jev via the gateway in shadow: 5,727 rows / 357 requests across all seven sites. Failure kind 95.5% (310 rows), stage validation 95% (20), branch 90.5% (42), debate consensus 80% (15; Jev right on all three real disagreements), recall rerank 61.6% (4,693; Jev confidently keeps far more), tie-break 6 rows, tool_search 12.7% under a wrong reference (fixed: LLM score ≥ 0.5). Write-up: `docs/research/decision-layer-phase3-report.md`. Found and fixed en route: serve never passed the decision config on its own agent paths; workflow CLI persisted nothing; ERROR rows had no reason; PG tenant leak; workflow cost under-reported in all four executors. **No band yet**; next: labelled samples for failure kind (`other` bucket) and recall, rerun tool_search under the corrected reference. **Follow-up found by reading outputs**: registry-built agents (workflow CLI, dynamic creation) lost their system prompt to a later `WithConfig`; fixed in `WithConfig` + `buildAgent`. After the fix, debates opposed in 14/15 (was 4/15) and all 20 swarms tied; `debate.consensus` then scored 15/15 by inspection against a heuristic that scored 1/15.
- [ ] Phase 3 evidence, second pass: labelled sample (a few hundred rows) for failure kind and recall; tool_search rerun; more branch/validation rows with `provider: jev` on the rig (graph memory on, tool_search BALANCED, a conditional workflow in the mix), produce `loom decision report --site` for each, then pick bands. Recall precision on the LongMemEval harness before/after is the 3.1 acceptance gate.

### Phase 4 — Tier 2 gaps
- [ ] P0 prerequisite: breaker counts `Result.Success==false` (from tool-calling assessment)
- [ ] 4.1 failure kind, brake-only · [ ] 4.2 risk hook, raise-only + adversarial fixtures · [ ] 4.3 TypedJudge canary · [ ] 4.4 two-stage skill selection · [ ] 4.5 hygiene/done-ness advisory
- [ ] security review

### Phase 5 — Tier 3
- [ ] shadow reports for extraction trigger, entity dedupe, compression, tier routing, bulk trace classification
