# Vercel AI Gateway evaluation plan

**Status**: ⚠️ Partly run (2026-11-18). Phases 0, 2, 3, 4 and 5 ran; Phase 1 and Phase 6 partly; Phase 7 not run. What ran, what did not, and every deviation from this plan are in [the results document](../research/vercel-gateway-eval-results.md). The harness is `cmd/loom-vercel-eval`.
**Owner**: Ilsun Park
**Scope**: Evaluate the `vercel-ai-gateway` provider (`pkg/llm/vercelgateway`) with loom as the harness. Vercel only. No direct-provider or other-gateway arms this round (LiteLLM, direct Azure, and direct Anthropic are deferred).
**Related**: [Vercel gateway reference](../reference/llm-vercel-ai-gateway.md), [spike record](../research/vercel-ai-gateway-spike.md), [implementation plan](vercel-ai-gateway-plan.md)

## 1. Questions this evaluation must answer

1. **Conformance**: Does loom get correct, well-formed behaviour through the gateway for tool calls, streaming, thinking/reasoning replay, caching, and structured output? Judged against the expected API contract, since there is no direct arm to compare against.
2. **Routing control**: Do `order`, `only`, `sort`, and `fallback_models` change the upstream that actually serves the request? Are they honoured, or only accepted?
3. **Cost accuracy**: Does loom's recorded cost match the gateway's reported cost and the rate card? How often does loom fall back to an estimate or flag a call as unpriced?
4. **Reliability**: How does the gateway behave on 429, 5xx, timeouts, and 402 (credits exhausted)? Does loom retry and surface the right error class?
5. **Task quality**: On loom's own agent workload, what pass rate does each model reach through the gateway, and where do the two models disagree?
6. **Operability**: What does it take to set up, rotate keys, and debug a failure from loom's traces?

**Not answerable this round**: gateway *overhead* (latency added versus calling the provider directly) and *fidelity versus direct* (whether the gateway changes outputs versus a direct call). Both need a direct arm, which is deferred. Latency is reported per arm only, with no overhead claim.

Each question gets a pass/fail or a number, not a narrative. Where the answer is "not verified", the report says so.

## 2. Arms

Both arms go through the Vercel gateway with default routing. Harness, prompts, tools, seeds, and temperature stay fixed. Only the model changes.

| Arm | Gateway slug | Surface | Purpose |
|---|---|---|---|
| **G-S** | `anthropic/claude-sonnet-5.5` | Messages (`/v1/messages`) | Claude path: thinking replay, `cache_control`, cache-token usage |
| **G-O** | `openai/gpt-5.5` | Chat Completions (`/v1/chat/completions`) | OpenAI path: `reasoning_details` replay, streaming tool calls |

Both slugs are confirmed in the live catalog (`GET /v1/models`, 2026-11-18). Routing controls are exercised on `openai/gpt-5.5` in Phase 2. Routing needs a provider slug the gateway accepts, which Phase 0 discovers (the catalog lists models, not upstream providers).

**Judge (decider)**: `typesafe-ai/jev-1.14.0`, served through the same gateway. See §3 Phase 5 for the confound this creates and how it is handled.

## 3. Phases

Each phase has an exit criterion. A failed exit criterion stops the phase and gets reported before the next one starts.

### Phase 0: Preflight (no model spend beyond a few tokens)

- [ ] Health: `looms` starts with the Vercel key (`AI_GATEWAY_API_KEY`, already in `~/.zshrc`). The `GET /v1/credits` health check passes. Balance recorded before the run (2026-11-18: balance $504.88, used $0.12).
- [ ] Catalog snapshot: save `GET /v1/models` to the results directory. Confirm the rates for both arms and for Jev, which the reference cost in Phase 4 needs.
- [ ] Jev pin: use `typesafe-ai/jev-1.14.0`, not the floating `typesafe-ai/jev` alias. The floating alias is rejected unless `allow_alias: true`, and an unpinned decider would change between runs. Pin is recorded in the run manifest.
- [ ] Routing slug discovery: find one or more provider slugs the gateway accepts for `openai/gpt-5.5` (for R1–R3). Record them in the manifest. If none are discoverable, R1–R3 are marked *not tested* with the reason.
- [ ] Build: `just build-hawk` (`looms eval` needs the `hawk` build tag per the CLI reference). Tests use `-tags fts5`.
- [ ] Budget: a whole-run spend ceiling of **$100** (decided 2026-11-18). Every suite sets `max_cost_usd`. A run that hits the ceiling stops and is reported, not raised.
- [ ] Results directory: `~/loom-eval-results/vercel-<YYYY-MM-DD>/`. Raw outputs are kept there and never deleted or overwritten. The directory is outside the repo and is not `/tmp`.

**Exit**: both arms and the Jev pin return one successful call each, with cost and model fields populated. **Not yet met**: the Jev pin has not been called (see Progress below).

**Progress (2026-11-18)**
- Health and credits: passed. Balance $504.88.
- Catalog: both arm slugs and `typesafe-ai/jev-1.14.0` confirmed in `GET /v1/models`.
- Build: `just build-hawk` succeeds with `SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk`. The default SDK fails to link on this machine. The same setting is needed for `go test`.
- Live smoke tests: `go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/vercelgateway/` passes 13/13 subtests on both arms. Raw log and captures are in `~/loom-eval-results/vercel-2026-11-18/`. These are smoke checks in the existing suite, not the Phase 1 or Phase 2 results.
- Routing slugs: the captures report `providerAttempts` with provider names `anthropic` and `openai`. These are candidates for R1–R3. `only: ["openai"]` as a satisfiable case has not been run yet.
- Not yet done: the Jev pin probe, and the routing fallback check.

### Phase 1: Conformance (pass/fail per case)

Run the same case set on each arm where the feature applies. For each case, record the raw request, the raw response, and the loom-parsed result.

| ID | Case | Arms | Pass condition |
|---|---|---|---|
| F1 | Single tool call | both | Tool name and JSON arguments match the schema |
| F2 | Parallel tool calls | both | All calls returned, IDs matched to results, none dropped |
| F3 | 3-turn tool loop | both | Each turn's tool result is accepted and the model continues correctly |
| F4 | Streaming tool calls | both | Argument fragments reassemble to valid JSON. Index order preserved (see the OpenAI stream-order fix, #534) |
| F5 | Thinking replay | G-S | Signed thinking blocks round-trip through a 3-turn tool loop without a signature error |
| F6 | Reasoning replay | G-O | `reasoning_details` replayed on the assistant message does not cause an error |
| F7 | Prompt caching | G-S | `cache_control` breakpoint accepted. Cache-read and cache-write tokens appear in usage when the cache is warm |
| F8 | Structured output | both | Output parses against a JSON schema on 20 of 20 runs (temperature 0) |
| F9 | Context overflow | both | An over-limit request returns the expected error body, and loom classifies it correctly |
| F10 | Embeddings (optional) | — | `POST /v1/embeddings` returns a vector of the requested dimension. Confirms or refutes the *unverified* `dimensions` pass-through. Uses an embedding model from the catalog, not the 5.5 chat models |

**Exit**: every case has a recorded result. A failure on one arm is a finding about that arm, not a blocker for the other.

### Phase 2: Routing and fallback correctness

Run on G-O (`openai/gpt-5.5`) unless noted.

| ID | Case | Pass condition |
|---|---|---|
| R1 | `only: [<provider slug>]` | Served provider in the response metadata matches the slug. Served model matches the request |
| R2 | `only` with an unsatisfiable provider | Gateway rejects the request. Loom surfaces the rejection, not a silent retry on another upstream |
| R3 | `order: [A, B]` | Provider A is tried first. Shows in provider metadata |
| R4 | `sort: ttft` vs `sort: cost` | Served provider differs in the expected direction on at least some runs. If identical across 30 runs, `sort` is reported as having no observable effect |
| R5 | `fallback_models` with the primary forced to fail | `gateway.fallback_used` is `true` in the trace. Served model is the fallback |
| R6 | `zero_data_retention: true` | Request accepted. Served providers all report ZDR. If the metadata does not say, the field is marked *unverified* |

R5 needs a way to force the primary to fail. Options: an invalid slug on the primary (the gateway may reject it before fallback applies, which must be recorded as-is), or an `only` restriction the primary cannot meet. The method is chosen in Phase 0 and written into the manifest before the run.

### Phase 3: Latency and throughput

- **Prompts**: 3 fixed prompts, about 200, 2,000, and 20,000 input tokens. Output fixed at about 300 tokens (`max_tokens` set).
- **Runs**: N = 30 per arm per prompt, interleaved (arm order randomised per round) to limit time-of-day drift.
- **Measured**: TTFT (streaming), total time, and output tokens per second. Per-run timestamps go to the raw log.
- **Warm-up**: 3 discarded runs per arm before measurement.
- **Statistics**: median, p95, and a 95% bootstrap CI for the median, per arm per prompt size. Report the G-S vs G-O difference as a paired comparison per round, with the caveat that the two models differ, so this is a model comparison, not a gateway measurement.
- **Isolation**: no Jev calls run during Phase 3 (see Phase 5 rate-limit note).
- **Concurrency**: a separate 1-, 4-, and 16-concurrent sweep on G-O only, to see whether throughput is limited before the upstream is. Results are reported as observed. No claim about Vercel's rate limits beyond what the responses show.

**Exit**: CI and median tables for each prompt size. Per-arm raw logs kept.

### Phase 4: Cost accuracy

- Every Phase 1–3 run records loom's cost, the gateway-reported cost (where present), and token counts.
- **Reference cost**: raw token counts × the catalog rate for the same model. Source is the Vercel catalog snapshot from Phase 0 and the Vercel usage view. Record the export date and rate table used.
- **Checks**:
  - Loom's cost vs gateway-reported cost. Same call, same number, or a documented difference.
  - Loom's cost vs reference cost, per call. Report median absolute error.
  - Count of calls flagged `llm.cost_unpriced` or `llm.cost_estimated`, and the estimate error where a reference exists.
  - Cache-token pricing on G-S: with a warm cache, does the cost reflect cache-read rates?
- **Jev calls are excluded** from arm cost figures and reported separately (see Phase 5).
- **Honest limit**: usage views can lag. Reconciliation is done after a delay, and the report says which numbers are final.

**Exit**: a table of per-arm error stats, plus a list of every unpriced or estimated call.

### Phase 5a: Decider calibration (gate for any Jev score)

Purpose: show Jev agrees with known answers before any Jev score is reported. Runs before the Phase 5 arm runs.

- **Set**: 24 items built from the two decider-scored task types (D5 action items, E4 spec bullets). 12 items are correct outputs. 12 each carry one planted defect: a missing action item, an invented due date, a swapped owner, or a bullet with no traceable spec section.
- **Labels come from construction, not judgement**: the generator script records each item's planted defect, so every label is known before Jev sees the item. No one labels by hand, and no arm output grades itself.
- **Procedure**: Jev classifies each item using the same criterion text Phase 5 will use. Record agreement, false-pass rate (defective item passed), and false-fail rate (correct item failed).
- **Gate, set before the run**: agreement ≥ 90% and false-pass rate ≤ 10%. If the gate fails, Jev scores are reported as *unvalidated*, and D5 and E4 are scored on their deterministic sub-checks only.
- **Budget**: calibration calls count against the $100 ceiling. They are tagged as decider calls (see Phase 5).

### Phase 5: Task quality, with Jev as the decider

This phase answers whether the model behind the gateway changes outcomes on loom's agent work. Tasks are in [the task set](vercel-gateway-eval-tasks.md): 31 tasks, 29 deterministic, 2 decider-scored.

- **Suite**: a loom eval suite (`looms eval run <suite>`) built from the task set. Each task has a deterministic check where possible (exact output, file state, tool sequence). Only the two decider-scored tasks (D5, E4) go to Jev, and only if Phase 5a passed the gate.
- **Decider**: Jev (`typesafe-ai/jev-1.14.0`) for cases that need a judgement a deterministic check cannot make. Its role is to classify or score outputs against a stated criterion, not to write free-text verdicts.
- **Confound, and how it is handled**: Jev is served through the same gateway as the arms. Its calls share the key, the gateway limits, and the cost ledger. Mitigations:
  - Tag every Jev call so it can be filtered out of arm cost (Phase 4), arm latency (Phase 3), and arm error counts (Phase 6).
  - Run Jev judging after the arm runs finish, not interleaved with them, so Jev traffic never overlaps a latency measurement.
  - Record Jev's own errors and latency separately as part of the decider's results.
- **Rate limit**: the agent-configuration reference notes the gateway free tier allows 30 requests per minute. Set `requests_per_minute` for the Jev decider below that (28, as the reference example does), and confirm the tier on this key. If the tier is higher, record that instead.
- **Repetitions**: 3 runs per task per arm at temperature 0. Pass rate per arm with a Wilson 95% interval. Paired comparison per task.
- **Metrics**: pass rate, tool-call count, turns, arm cost (Jev excluded), and wall time.
- **Stop rule**: if one arm fails a task deterministically (same failure in all 3 runs) and the other passes, the case is listed as a finding with its trace ID. It is not dropped from the denominator.

**Exit**: calibration agreement reported, pass rate and paired differences per arm, and a list of tasks where arms disagree, each with trace IDs.

### Phase 6: Reliability and error handling

| ID | Fault | Expected |
|---|---|---|
| E1 | Invalid key | Health check fails before any model call. Error is clear |
| E2 | Credits exhausted (402) | Distinct error class. Not retried. Loom surfaces it |
| E3 | Concurrency burst (from the Phase 3 sweep) | 429 handling: retry with backoff, bounded attempts. Count of final failures |
| E4 | Upstream timeout (low `providerTimeouts` value) | Timeout classified as transient. Retry behaviour recorded |
| E5 | Stream cut mid-response | Loom returns an error, does not emit a partial tool call as complete |
| E6 | Gateway 5xx (if one occurs naturally) | Retry counts recorded. No silent success |

E2 is **not tested** in this run (decided 2026-11-18: no spend limit is set on the key to trigger it). It stays in the table so it can be run later. Do not invent a failure.

**Exit**: a table of observed behaviour per fault. Faults that could not be induced are marked *not tested*.

### Phase 7: Observability

- For every run, check the trace has: `gateway.fallback_used`, `llm.cost_unpriced`, `llm.cost_estimated`, the generation ID, and the served model. Jev calls must be distinguishable by tag.
- Confirm a generation ID from a trace can be found in the Vercel dashboard. This ties loom traces to gateway logs.
- Record any missing attribute as a finding.

**Exit**: a checklist with one result per attribute.

## 4. Metrics summary (what the final report shows)

- Conformance matrix: F-cases × arms, pass/fail.
- Routing matrix: R-cases, observed served provider, pass/fail.
- Latency: median, p95, CI, per prompt size, per arm. No overhead claim.
- Cost: per-arm error stats, unpriced and estimated counts. Jev cost reported separately.
- Decider: calibration agreement and false-pass/false-fail rates against construction labels. Labelled as validated or unvalidated.
- Task quality: pass rate with CI, paired per-task differences.
- Reliability: fault table with observed behaviour.
- Observability checklist.
- A "not verified" list: every claim from the reference doc's *unverified* set that this run did or did not settle, plus the overhead and direct-fidelity questions this round cannot answer.

## 5. Guardrails

- **Keys**: read from the environment only. No key or account identifier is written into this doc, the results, or any shared artifact.
- **Spend**: one ceiling for the whole run and a `max_cost_usd` per suite. The run stops at the ceiling.
- **Data**: raw results are kept, never deleted or overwritten. Re-runs go to a new dated directory.
- **Claims**: a number appears in the report only with its raw log path. Anything not measured is marked *not tested*.
- **Prompts**: eval prompts contain no real customer or internal data.
- **Decider**: Jev scores are never reported without the calibration result beside them.

## 6. Decisions and open items

**Settled by your direction (2026-11-18)**
- Models: `anthropic/claude-sonnet-5.5` and `openai/gpt-5.5`. Haiku and gpt-5-mini are dropped.
- Decider: Jev, pinned to `typesafe-ai/jev-1.14.0`.
- No direct-Azure arm (D-O dropped), no Azure upstream arm. Everything runs through the Vercel gateway.

**Decided (2026-11-18)**
- **Budget ceiling**: $100 for the whole run. Account balance is $504.88.
- **Phase 5 tasks**: 31 drafted real-world tasks in [the task set](vercel-gateway-eval-tasks.md). Fixtures are synthetic and are built in Phase 5 setup.
- **E2 (402 test)**: not tested this run.
- **Calibration labels**: built by construction (Phase 5a), so no hand labelling is needed.

**Still open**
- The `looms eval` suite schema, read from `pkg/evals/suite_loader.go` (2026-11-18). It supports output-contains and not-contains, regex, expected tools, `max_cost_usd`, `max_latency_ms`, and judges. It has **no file-state checks and no shell-command or test-run checks**. Tasks A1–A6 and F3 need a checker layer beyond the suite, so Phase 5 setup builds that layer.
- The Phase 5a gate thresholds (≥ 90% agreement, ≤ 10% false-pass) are my proposal. Change them before the calibration run if you want stricter or looser gates.

## 7. Order of work

1. Phase 0 preflight. Stop and report on any failure.
2. Phases 1 and 2 (conformance and routing). These are cheap and surface the most serious problems first.
3. Phase 3 (latency). Phase 4 (cost) runs on the same data, so it needs no extra spend.
4. Phase 5 (task quality): calibrate the decider first, then run the arms, then run Jev judging after the arms finish.
5. Phases 6 and 7.
6. Write the results report to `docs/` with the raw log paths, and mark each claim as verified, refuted, or not tested.

## 8. Open items I could not verify from the repo

- The exact `looms eval` suite YAML schema. The example file the CLI reference cites (`examples/dogfooding/config-loader-eval.yaml`) is not in the repo. The schema will be read from `pkg/evals/suite_loader.go` before any suite is written.
- Which provider slugs the gateway accepts for `only`/`order`. The catalog lists models only. Phase 0 discovers them.
- Whether `sort: ttft` produces a different served provider from `sort: cost` in practice (R4). The spike did not test this.
- Whether Jev's classify/score output agrees with human labels on loom's outputs. Unknown until the calibration step.
- Whether a Claude-to-OpenAI fallback on the Messages surface translates correctly (*unverified* in the reference doc). R5 covers this only if budget allows.
