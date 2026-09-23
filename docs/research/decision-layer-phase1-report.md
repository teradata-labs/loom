# Decision Layer, Phase 1 Report: Baseline and First Shadow Run

**Date**: 2026-09-22
**Branch**: `feat/decision-layer` (Phase 0 + Phase 1 committed)
**Plan**: `docs/plans/jev-decision-layer-plan.md` §3 Phase 1, the two operator steps
**Companion**: `docs/research/tool-calling-assessment.md` (the local-telemetry measurements this extends)

This report closes Phase 1 with the two measurements the plan required before any call site gets a live confidence band: a **baseline** of what the generative provider is doing under fleet load today, and a **first shadow report** comparing an LLM-adapter decider against Loom's existing failure classification over recorded tool executions. Both are measurements, not claims. No Jev call has been made yet (Phase 2); the decider here is the LLM adapter, which is the comparator the plan specifies for exactly this step.

---

## 1. Baseline: generative bandwidth under fleet load

**Source**: the Azure gauntlet rig (`loom-mcp-rg`, 512 agents, gpt-4o via Azure OpenAI, `azure-openai|…|gpt-4o` scheduler scope, 1.5M TPM ceiling), TPC-H-style SQL gauntlet run `aztpch-cc-1790111812` on 2026-09-22 with binaries from `main@37b6e606`. Graph memory was **off** for these agents, so the recall-starvation rate (`recall.query_source=keyword`) is not measurable from this run; see §1.3.

### 1.1 What the fleet asked of the provider

| Measure | Value | Source |
|---|---|---|
| Sessions | 512 | `loom.db` `sessions` |
| Main-loop LLM calls (assistant messages) | 1,494 total; **p50 3 / p90 3 / max 4 per session** | `messages` where role = assistant |
| Tool executions | 1,053 total; p50 2 / max 3 per session; **0 errors** | `tool_executions` |
| Session wall time (first to last message) | **p50 611 s / p90 710 s** | `messages` timestamps |
| Run wall time | 748 s for all 512 | `tpch-cc-run.log` |

A session that makes three LLM calls and two tool calls takes ten minutes. Almost none of that is model latency; it is time spent parked in the LLM slot scheduler waiting for token capacity, which is the "generative bandwidth" cost §0 of the plan describes.

### 1.2 What the scheduler recorded

`loom scheduler state` on the rig after the run:

| Scope | TPM | Grants | Promotions | Parked now |
|---|---|---|---|---|
| `azure-openai\|https://eastus2.api.cognitive.microsoft.com/\|gpt-4o` | 1,500,000 | **1,494** | **987** | 0 |

Grants equal the 1,494 main-loop calls exactly. **987 of them, 66%, were starvation promotions**: the request sat parked past the aging threshold (60 s) and had to be promoted a priority class before it was admitted. Two thirds of every generative call in this fleet waited more than a minute for a slot.

This is the number the decision layer targets. Every auxiliary judgment that moves off the generative provider is a request that no longer competes for those 1,494 slots. In this run the aux calls were suppressed (graph memory off, tool_search not used), so the 1,494 are the floor; a default configuration adds roughly one aux call per main-loop call (`tool-calling-assessment.md` §4) on the same scope.

### 1.3 Gaps this baseline exposes

- **No queue-wait histogram.** The scheduler exposes grants, promotions, and current parked/reserved counts, and logs only calibration events. It records no per-request wait duration, so p50/p95 queue wait cannot be read back after a run. Promotions/grants is the available proxy. Follow-up: emit `llm.scheduler.wait_ms` as a span attribute on `llm.completion` and a histogram metric, so before/after comparisons in Phase 3 rest on wait time rather than a promotion ratio.
- **Recall starvation not measured here.** Graph memory was off in this run. The 2026-08 memory-curve runs (`az512h`) established the failure mode (recall's query-distillation side-call starving at a saturated pipe and falling back to keywords); the fix landed in PR #356 and records `recall.query_source` on the recall span. A graph-memory-on rerun at 512 agents is the right place to read that rate, and it should be done when Phase 3.1 (recall rerank) goes live, as the before/after pair.
- **Aux calls are still invisible in the telemetry DB.** Only main-loop calls persist as messages. The decision layer's `decision.evaluate` spans and `decision.*` metrics fix this for decisions; the remaining generative aux calls (extraction, distillation, compression) still need the metering the assessment recommends.

---

## 2. First shadow runs: `tool.failure_kind`

**Setup.** `loom decision replay` (this branch, cross-compiled for the rig) over a slim copy of the local telemetry DB (`~/.loom/loom.db`, 46,934 tool executions from 2026-03 to 2026-09, only `sessions` + `tool_executions` carried across). Decider: the LLM adapter over **gpt-4o via Azure OpenAI** on the rig, 8 calls in flight. Reference: what Loom decides today, the `Success` flag plus `fabric.InferErrorType`, mapped onto the seven failure kinds by `sites.FailureKindReference`. Two questions per execution: `kind` (Choice) and `retry_same_input_helps` (Noul). No Jev call was made; this is the comparator run the plan specifies, and the numbers below say how a generative decider agrees with the substring ladder, not how either agrees with truth.

### 2.1 Newest 2,000 executions (mixed)

| Metric | Value |
|---|---|
| Rows | 4,000 (2 questions × 2,000) |
| Agreement, both questions | 77.8% (3,113 / 4,000) |
| Agreement, `kind` alone | **95.1%** (1,901 / 2,000) |
| Agreement, `retry_same_input_helps` alone | 60.6% (1,212 / 2,000) |
| Expected calibration error, 10 bins | 0.165 |
| Decider errors | 0 |
| Latency p50 / p95 / p99 | 1,268 / 1,965 / 3,078 ms |
| Tokens / cost | 2.64M input / $10.48 |

Composition of the slice: 1,387 successes, 482 `server_saturated` (the `session_handle_budget_full` connect storms from the August gauntlet), 131 `other`. No syntax, not-found, permission or timeout rows: newest-first reads the tail of the last campaign. Tools: `teradata:execute_statement` 789, `teradata:connect` 545, `teradata:execute_query` 522.

`kind` confusion (reference → candidate):

| reference | not_a_failure | server_saturated | bad_input | other | transient | not_found | total | agree |
|---|---|---|---|---|---|---|---|---|
| not_a_failure | **1,383** | 0 | 3 | 1 | 0 | 0 | 1,387 | 99.7% |
| server_saturated | 0 | **482** | 0 | 0 | 0 | 0 | 482 | 100% (mean confidence 0.974) |
| other | 0 | 0 | 92 | **36** | 2 | 1 | 131 | 27.5% |

**Finding 1, the reference's blind spot.** The 131 `other` rows are Teradata `db_error`s the substring ladder has no rule for: 118 are `[Error 2616] Numeric overflow occurred during computation` (the card-number-into-INTEGER trap from the gauntlet), plus 2666 invalid date and 3803 table exists. The decider called 92 of them `bad_input` at mean confidence 0.641. That is the defensible answer: only a changed SQL statement can succeed. Where the reference and the decider disagree on `kind`, the reference is the one without an opinion.

**Finding 2, an ill-posed question.** On the 1,387 successful executions the retry Noul split 786 "true" / 597 "false". The question was "would calling the same tool again with the identical input probably succeed?", which after a success is trivially yes, while the reference says false (nothing to retry). The split is the question's fault, not the decider's: it accounts for 788 of the 887 total disagreements and for most of the 0.165 calibration error. Reworded on this branch to ask whether the call *failed in a way a retry would clear*, so success and "failure that will not clear" both read false. Rows in §2.1 and §2.2 were produced with the old wording.

### 2.2 Newest 1,500 failed executions (errors only)

| Metric | Value |
|---|---|
| Rows | 3,000 |
| Agreement, both questions | **90.3%** (2,710 / 3,000) |
| Expected calibration error, 10 bins | **0.044** |
| Decider errors | 0 |
| Latency p50 / p95 / p99 | 1,299 / 2,915 / 7,735 ms |
| Tokens / cost | 2.40M input / $8.85 |

Composition: 1,138 `server_saturated`, 362 `other` (again almost all 2616 overflow), 1 `unknown_session_handle`. `server_saturated` agreed 1,138 / 1,138. `other` → `bad_input` 277, `other` 74, `not_found` 9, `transient` 2: the same blind spot at scale.

Calibration in this slice is the band-setting evidence the plan asked for:

| Confidence bin | Rows | Agreement |
|---|---|---|
| [0.9, 1.0) | 2,354 | **99.9%** |
| [0.8, 0.9) | 306 | 86.6% |
| [0.5, 0.8) | 305 | 19–39% |
| below 0.5 | 35 | 22–35% |

Above 0.9 the decider and the ladder agree on all but two of 2,354 rows. Every disagreement of substance sits in the 0.5–0.8 bins, and those are the `other` rows where the reference has no rule. For this site, with this decider, an act threshold at 0.9 would have branched on 78% of rows and been wrong against the reference on 0.1% of them.

### 2.3 Random 1,500 failed executions across the whole history

Same decider, `--sample random --errors-only`, so every month from 2026-03 to 2026-09 and every tool contributes. This run used the **reworded** retry question.

| Metric | Value |
|---|---|
| Rows | 3,000 |
| Agreement, both questions | 72.0% (2,161 / 3,000) |
| Agreement, `retry_same_input_helps` alone | **100%** (1,500 / 1,500) after rewording |
| Agreement, `kind` alone | 44.1% overall; **95.1% outside the reference's `other` bucket** (637 / 640) |
| Expected calibration error, 10 bins | 0.181 |
| Decider errors | 0 |
| Latency p50 / p95 / p99 | 1,293 / 2,039 / 3,844 ms |
| Tokens / cost | 2.17M input / $8.34 |

`kind` confusion (reference → candidate):

| reference | agree | disagree | note |
|---|---|---|---|
| auth | 9 / 9 | 0 | |
| bad_input | 22 / 25 | 1 auth, 2 not_found | |
| not_found | 21 / 21 | 0 | |
| transient | 1 / 1 | 0 | |
| server_saturated | 584 / 584 | 0 | |
| **other** | 24 / 860 | 774 bad_input, 62 not_found | the reference has no rule here |

Calibration: the [0.9, 1.0) bin holds 2,010 rows at **98.1%** agreement; every bin from 0.3 to 0.9 is dominated by disagreement, and those rows are almost entirely the `other` bucket. Read together with §2.2, the decider is confident exactly where it and the ladder agree, and hedges exactly where the ladder has nothing to say.

**What `other` is.** Across all 13,726 errored executions in the history, the substring ladder leaves these unclassified (top prefixes):

| Rows | Error prefix | Decider's reading |
|---|---|---|
| 3,799 | `STORE_ERROR: link entity user: FOREIGN KEY constraint failed` (the live `graph_memory` bug from the tool-calling assessment) | bad_input |
| 1,251 | `MCP_CALL_FAILED … db_error … [Error 2616] Numeric overflow` and other Teradata errors | bad_input |
| 678 | `invalid_input: Data type 'text' requires specific query method` (`query_tool_result`) | bad_input |
| 284 | `INVALID_PARAMS: … parameter is required` (`agent_management`) | bad_input |
| 84 | `EXIT_ERROR: Command exited with code 1` (`shell_execute`) | other / bad_input |
| 61 | `STORE_ERROR: get old memory: sql: no rows in result set` | not_found |

Every one of those is a call that an identical retry cannot fix. Today's classifier files them as `unknown`, which is why the identical-failure tracker sees them as undifferentiated noise and why 75.9% of errors are followed by the same call again.

---

## 2.4 Phase 2: Jev versus gpt-4o on identical rows (2026-09-23)

**Setup.** Jev through the Vercel AI Gateway (`typesafe-ai/jev`, the gateway's only id; the free tier caps Jev at **30 requests per minute**, discovered when eight parallel workers drew 429s with 60-second `Retry-After`), paced at 28 rpm with one worker. gpt-4o through Azure OpenAI as before. Both deciders scored the **same executions**: `--sample random --seed 42`, 600 failed executions and 600 mixed executions, 1,200 shadow rows each. Two references are shown because the run changed the reference: the ladder as extended after Phase 1 (`FOREIGN KEY constraint failed → constraint_violation → bad_input`) and the refined ladder committed after this run (`foreign key → not_found`). Jev's rows were re-scored against the refined reference by pairing them back to their executions in sample order; gpt-4o's slices were rerun with the refined binary.

### 2.4.1 Failed executions, seeded (600 executions, 1,200 rows)

| | gpt-4o (adapter) | Jev (gateway) |
|---|---|---|
| Agreement, original reference | **96.9%** (1,163) | 77.2% (927) |
| Agreement, refined reference | 79.9% (959) | **94.0%** (1,128) |
| `kind` agreement, refined | 59.8% (359 / 600) | **88.0%** (528 / 600) |
| `retry_same_input_helps` agreement | 100% | 100% |
| `server_saturated` / `not_found` / `auth` | 230/230 · 22/22 · 1/1 | 230/230 · 22/22 · 1/1 |
| Foreign-key rows (201): answer | bad_input (0.9+) | **not_found** (0.86–0.90) |
| Teradata engine errors (43): answer | bad_input | other at ~0.5 |
| Expected calibration error, refined | 0.103 | 0.204 (see note) |
| Latency, network only | p50 1.5 s | **170–450 ms** (smoke test) |
| Latency as recorded | 1,598 / 2,765 / 5,000 ms | 2,142 / 2,332 / 2,582 ms, dominated by the 28 rpm pacing wait |
| Tokens / cost | 0.88M / **$3.37** | 0.93M / **$0.00** on the free tier; $0.039 at list price (86× cheaper) |
| Decider errors | 0 | 0 |

**Reading it.** Everywhere the reference has a rule that both deciders accept, they agree with it and with each other: saturation, auth, missing tables and columns, the retry question. The ranking is decided by one class, the 201 graph-memory `link entity X: FOREIGN KEY constraint failed` rows. Jev reads them as `not_found` at 0.9 confidence; gpt-4o and the pre-refinement ladder read them as `bad_input`. By the option wording the request itself carries ("not_found: a referenced object does not exist: table, column, file, resource, id"), Jev's reading is the literal one: the entity being linked does not exist. The ladder was refined accordingly, and the Phase 1 statement that "where they disagree, the reference is the party without an opinion" now has a second form: **where two deciders disagree, the reference decides the ranking, so the reference has to be defended on its own terms, not inferred from either decider.** A human-labelled sample of the FK and overflow classes is the right way to settle it; the seeded 600 are the sample to label.

Jev's calibration number needs a caveat. Its ECE of 0.204 is mostly the retry Noul: Jev answers "false" at probabilities around 0.1–0.4, which the report's decisiveness mapping treats as low confidence, yet 600 of 600 agree. That is under-confidence on an easy question, not wrong answers, and a Noul-specific threshold (probability, not decisiveness) would remove it. On `kind`, Jev's confidence separates the regimes as the vendor claims: 0.86–0.99 where it agrees, ~0.5 on the Teradata engine errors it is unsure about.

### 2.4.2 Mixed executions, seeded (600 executions, 1,200 rows)

| | gpt-4o (adapter) | Jev (gateway) |
|---|---|---|
| Agreement, original reference | **99.3%** (1,192) | 93.6% (1,123) |
| Agreement, refined reference | 94.5% (1,134) | **98.2%** (1,178) |
| Composition (reference) | 428 not_a_failure · 66 saturated · 62 not_found · 36 bad_input · 8 other | same rows |
| `not_a_failure` / `server_saturated` / `not_found` / retry | 427/428 · 66/66 · 7/62 · 599/600 | **428/428 · 66/66 · 62/62 · 600/600** |
| Disagreements, refined | 55 FK rows → bad_input; 5 other → bad_input; 3 other → not_found; 1 success → bad_input; 1 retry | 14 overflow rows → other (~0.5); 5 other → bad_input; 3 other → not_found |
| Expected calibration error, refined | 0.029 | 0.393 (Noul under-confidence, see §2.4.1) |
| Tokens / cost | 0.79M / **$3.14** | 0.83M / **$0.00** (list $0.035) |

Same shape as the failures slice: the two deciders agree with each other and the reference on everything the reference has a rule for, and part company only on the foreign-key rows (55 here). Jev's residual misses are the Teradata overflow errors it declines to classify, answered `other` at about 0.5 confidence, which is what a calibrated hedge looks like.

**Combined, 1,200 seeded executions, refined reference:** Jev 96.1% (2,306 / 2,400 rows), gpt-4o 87.2% (2,093 / 2,400). Under the original reference: gpt-4o 98.1%, Jev 85.4%. Same rows, same answers, different label on one error class.

### 2.4.3 What Phase 2 changes in the plan

- **The gateway free tier is a benchmark tool, not a fleet path.** 30 requests per minute is one agent's worth. A 512-agent fleet needs the direct TypeSafe endpoint (1,200 rpm published) or a paid gateway tier, and the client's own limiter should be set from the tier in use. The `--rpm` flag and `RequestsPerMinute` exist for this.
- **Latency claim holds.** Sub-second per call through the gateway (170–450 ms observed) against 1.5 s p50 for the generative adapter, before any pacing.
- **Cost claim holds.** Two orders of magnitude at list price on identical rows.
- **Agreement claims need the reference settled first.** Phase 4.1's shadow-eval gate must include the human-labelled FK/overflow sample before a brake-only band is set from either decider's agreement.

---

## 3. What this establishes, and what it does not

**Established.**

1. **The failure-kind site is a real gap, and a typed decider fills it.** Where today's mechanism has a rule (saturated, auth, not-found, syntax), the decider agrees 95–100%. Where it has no rule, which is 57% of a random sample of failures, the decider produces a specific, defensible kind. The largest single class it recovers is the live graph-memory FK failure, 3,799 rows.
2. **Confidence separates the two regimes.** In every slice, agreement above 0.9 confidence is 98–99.9%; below 0.9 it collapses to the `other` disputes. A brake-only band at `act_min: 0.9` would have acted on 67–78% of rows and disagreed with the reference on 0.1–1.9% of those, all of them rows where the reference is the weaker party.
3. **Shadow mode caught a question-design bug before it could matter.** The retry Noul was ill-posed on successes; the first slice made that visible (788 of 887 disagreements), the rewording took it to 100% agreement on the third slice. This is the workflow the plan prescribes, working.
4. **The harness is cheap and fast enough to run routinely.** Three runs, 5,000 executions, 10,000 shadow rows, about 16 minutes of wall time at 8-way concurrency, $27.67 in gpt-4o tokens. The same rows through Jev at list price would be under $2 and, at the vendor's latency, under a minute.

**Not established.**

- Correctness. Agreement is with the substring ladder, and in the `other` bucket the ladder is by construction the party without an opinion. Establishing that `bad_input` is *right* for a 2616 overflow needs a human-labelled sample; the plan's Phase 4.1 shadow-eval gate should include one (a few hundred rows is enough).
- Anything about Jev. This decider was gpt-4o through the LLM adapter. Jev's agreement, calibration and latency on the same rows is the first thing Phase 2 measures, with `loom decision replay --decider jev` against the same slim DB.
- Latency at the site. The adapter ran at 1.3 s p50; in production the shadow is off the hot path, and a live band would add a decision call to every tool result. That is Phase 4.1's cost to weigh, and the reason it is brake-only.

**Recommended next steps** (feeding Phase 3–4 of the plan)

- Phase 2: build the Jev client, rerun these three replays with `--decider jev`, publish the side-by-side.
- Phase 4.1 prerequisite (independent of Jev): extend `fabric.InferErrorType` with the classes this run surfaced (`FOREIGN KEY constraint failed`, `Numeric overflow`, `invalid_input`, `INVALID_PARAMS`) so the reference stops saying `other` for 57% of failures. The decision layer should not be the only thing that knows what these errors are.
- Add a per-request `llm.scheduler.wait_ms` metric so the bandwidth claim in §1 becomes a wait-time histogram rather than a promotion ratio.
- Keep the replay harness on the rig (`~/replay/`: binary, slim DB copies, three reports) as the regression yardstick; rerun after any change to the site's request wording.

---

## 4. Reproduction

```
# local: slim copy of the telemetry DB (sessions, tool_executions, schema_migrations)
sqlite3 loom-slim.db "attach '~/.loom/loom.db' as src; \
  create table sessions as select * from src.sessions; \
  create table tool_executions as select * from src.tool_executions;"
# carry the migration ledger with its PRIMARY KEY (CREATE TABLE AS drops it)

# rig: three replays, gpt-4o via Azure OpenAI from the environment
loom decision replay --db loom-slim.db --decider llm --provider azure-openai --model gpt-4o \
     --limit 2000 --concurrency 8
loom decision replay --db loom-slim-errors.db … --limit 1500 --errors-only
loom decision replay --db loom-slim-random.db … --limit 1500 --errors-only --sample random
loom decision report --db <db> --site tool.failure_kind
```

Reports as generated: `~/replay/replay-{mixed,errors,random}-report.md` on the rig; copies in the session scratchpad.
