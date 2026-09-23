# Jev (TypeSafe AI) — Deep Dive and Fit Assessment for Loom

**Status**: Research, read-only. No code changed.
**Date**: 2026-09-22
**Measured on**: branch `fix/hitl-hold-heartbeat` @ `bf64528d` (= main + heartbeat fix), v1.4.0
**Sources**: TypeSafe docs (`docs.typesafe.ai`), LiteLLM/OpenRouter/Cloudflare provider pages, independent write-ups (pearpages, beri.net, KDnuggets, jock.pl, eesel), community integrations (LangChain `langchain-typesafe`, browser-use `jev-ultrafast`, `pi-jev`, `jev-mcp`, four community Go SDKs). Full source list in §9.

---

## 0. Verdict first

Jev is a **hosted, text-only classifier with calibrated probabilities**, not an LLM. It answers three kinds of bounded question against a supplied state in one round trip, in roughly 70–500 ms, for $0.042 per million *input* tokens with output free. It cannot generate text, count, compare dates, or reason across steps. Its vendor accuracy figures are agreement with two frontier models, not human ground truth, and its calibration claim is unproven by independent measurement.

For Loom that shape is a near-exact match for one specific problem the tool-calling assessment already quantified: **a default turn makes ~8 LLM calls for 4 visible ones, and the extra 4 are narrow judgments** (extraction cadence, recall query distillation, recall rerank, tool_search rerank, compression triage, hygiene audit). Those are Jev-shaped decisions running on generative models. Two further gaps have no mechanism at all: nothing classifies a tool result as a failure kind, and nothing computes tool-call risk before a human is asked. The placements that matter most (§5 has all fifteen, ranked):

- **Lowest risk, measurable now** — workflow branch selection (a whole agent turn plus up to ten retries to emit one enum), the two per-turn rerank calls, intent classification, stage-output validation.
- **Highest value, needs guard rails** — result-level failure classification feeding the circuit breaker (the P0 gap in `tool-calling-assessment.md` §5 RC1), tool-call risk scoring for HITL (`risk_level` is never computed today), two-stage skill/pattern selection (vendor recipe cut wrong loads 16.8% → 7.3% on 182 skills), and a typed judge cheap enough to run on every production turn.

The right integration is **not** "add `jev-latest` to the model catalog". Jev has no chat surface. It needs a small new abstraction (a `Decider` interface, proto-first, §6) that every call site targets, with the current mechanism kept as the fallback for the uncertain band. Shadow mode comes first: Loom's own telemetry already holds 46,934 labeled tool executions, a free eval set. §8 has the checklist.

What would kill it: Jev is early-access, waitlist-gated, hosted-only, with no weights and no self-host path. Enterprise deployments of Loom that cannot send tool results to a third party cannot use it. Design the abstraction so the same interface can be backed by a local model (Laya, GLiNER2, or a small Ollama classifier) and Jev is one implementation.

---

## 1. What Jev is

### 1.1 The one-paragraph version

TypeSafe AI (founder Diogo Almeida, ex-OpenAI; $40M raised; launched 2026-09-15) ships Jev as the first of what it calls **System One models**: models that evaluate *predefined typed questions* against *supplied state* and return *probability distributions*, never prose. The vendor's framing is Kahneman's System 1 vs System 2: fast intuitive judgment vs deliberate reasoning. The vendor's stated design rule is "code owns control flow; the model supplies narrow semantic judgments where ordinary logic fails."

The mental model the vendor uses is "a five-second expert judgment at machine scale." If a domain expert could answer it in five seconds without research, planning, or explanation, it is a Jev question.

### 1.2 The three primitives

| Primitive | Question shape | Returns | Bounds |
|---|---|---|---|
| **Noul** | "Is this true?" | `noul`: probability 0–1 | optional `criteria.true` / `criteria.false` descriptions |
| **Choice** | "Which of these?" | `choice`, `probabilities` per option, `confidence` | up to **255** options, each with a description |
| **Score** | "Where on this ordered scale?" | `score` (probability-weighted), `probabilities` per level, `legend`, `confidence` | **2–10** ordered level descriptions |

Every question in a request is evaluated **independently and in parallel** against the same state. Answer A is never context for answer B. Adding questions adds tokens (cost) but almost no latency. The vendor's recommended pattern is "fan out semantic questions; compose in code."

**Confidence** is not max-probability. For a Choice with n options it is `(n × p_max − 1) / (n − 1)`, i.e. how concentrated the distribution is. A 90% winner among 3 options yields 0.85; a uniform spread yields 0. The vendor's default routing bands are >0.9 act, 0.5–0.9 confirm/review, <0.5 escalate, with the explicit instruction to set thresholds per question and per consequence.

### 1.3 The API

```
POST https://api.typesafe.ai/v1/systemone
Authorization: Bearer <TYPESAFE_API_KEY>
Content-Type: application/json
```

```json
{
  "model": "jev-latest",
  "state": "<string | JSON object | JSON array>",
  "questions": {
    "is_failure": { "type": "noul",
                    "instructions": "Did this tool call fail to do what was asked?",
                    "criteria": { "true": "...", "false": "..." } },
    "failure_kind": { "type": "choice",
                      "instructions": "What kind of failure is this?",
                      "criteria": { "transient": "...", "auth": "...", "bad_input": "...",
                                    "server_saturated": "...", "not_a_failure": "..." } },
    "severity": { "type": "score",
                  "instructions": "How severe?",
                  "criteria": ["Cosmetic", "Degraded", "Blocking"] }
  }
}
```

Response:

```json
{
  "model": "jev-1.13.0",
  "answers": {
    "is_failure":   { "type": "noul",   "noul": 0.97 },
    "failure_kind": { "type": "choice", "choice": "server_saturated",
                      "probabilities": { "transient": 0.05, "auth": 0.01, "bad_input": 0.02,
                                         "server_saturated": 0.91, "not_a_failure": 0.01 },
                      "confidence": 0.89 },
    "severity":     { "type": "score",  "score": 1.84,
                      "legend": { "0": "Cosmetic", "1": "Degraded", "2": "Blocking" },
                      "probabilities": { "0": 0.02, "1": 0.12, "2": 0.86 }, "confidence": 0.79 }
  },
  "usage": { "input_tokens": 412, "output_tokens": 0 }
}
```

`instructions`, Choice option descriptions, Score levels, and Noul criteria all accept JSON (object/array), not only strings. That matters for Loom: a tool schema, a skill manifest, or a pattern header can be passed intact as a criterion rather than flattened to prose.

Errors: 401 bad key, 422 validation, 429 rate limit, 529 overloaded. No streaming. No batch endpoint; concurrency is client-side.

### 1.4 Hard numbers

| Fact | Value | Source |
|---|---|---|
| Current model | `jev-1.13.0`; aliases `jev-latest`, `jev-preview` both → 1.13.0 | models.md |
| Context | 64k tokens per request total; 32k for state + longest question | models.md |
| Rate limits | 250,000 tokens/s, 1,200 req/min, "adjust dynamically" | models.md |
| Latency (vendor) | 70–500 ms end-to-end | docs, LiteLLM page |
| Latency (independent) | ~300–370 ms per call from EU; ~580 ms via Vercel gateway; ~750 ms from Taiwan | jock.pl, jev-mcp, eesel |
| Price | $0.042 / 1M input tokens; output free | models.md |
| Cost per decision | ~$0.0004 (vendor benchmark cases); ~$0.00002 for a short ticket | vendor, jock.pl |
| Input | text only (string / JSON / array); no images, audio, binary | models.md |
| Language | English primary; CJK and others "less reliable, test first" | models.md |
| Availability | hosted only, early-access waitlist; no weights, no self-host, no paper | pearpages |
| Data | not trained on customer traffic; zero-retention on enterprise plan | models.md |
| Training | "RLCD — Reinforcement Learning for Calibrated Decisions"; architecture undisclosed | vendor |

### 1.5 Distribution

Native SDKs: Python, JavaScript/TypeScript. **No official Go SDK.** Four community Go SDKs exist (`unimtx/typesafe-sdk-go`, `ajayk/jev-go-sdk`, `Stumble/jev-go`, `guillemus/jev-go`), all stdlib-only wrappers over one POST. Gateways: LiteLLM (true passthrough at `/typesafe/v1/systemone`, cost tracked via `usage`), OpenRouter (`typesafe/jev-1.13`), Vercel AI Gateway, Netlify, Cloudflare AI. Framework adapters: LangChain `langchain-typesafe` (`TypeSafeClassifier`, `ModelRouterMiddleware`, `AutoModeMiddleware`), Pydantic AI. Agent surfaces: `jev-mcp` (Node MCP server exposing `jev_classify/score/check/gate/decide`), a vendor "agent skill" for Claude Code.

Given the API is a single JSON POST with four error codes, **Loom should write its own ~200-line client** rather than take a dependency on an unofficial SDK. The LiteLLM provider Loom already has (`pkg/llm/litellm`) does not help directly because Jev is not a chat completion; the LiteLLM path is only useful as a key-management proxy.

---

## 2. What the evidence actually says

### 2.1 Vendor benchmark (treat as vendor-reported)

Four workflows: security incident response, agent-trace observability, invoice processing, customer service. Labels = **average of GPT-6 Astra and Claude Fable 5.1 outputs**. No human ground truth anywhere.

| Model | Agreement | Cost/case | Latency |
|---|---|---|---|
| Jev 1.13 | 67.8% | $0.0004 | 0.4 s |
| GPT-5.6 Luna | 66.8% | $0.0033 | 12.9 s |
| GPT-5.6 Terra | 67.9% | $0.0304 | 10.1 s |
| Claude Sonnet 5 | 67.8% | $0.1174 | 78.1 s |
| GPT-5.6 Sol | 74.1% | $0.0836 | 23.3 s |
| Claude Opus 5 | 73.1% | $0.1761 | 37.8 s |

Per-workflow Jev vs best: security 61.7 vs 66.2; **agent-trace observability 71.6 vs 76.6**; invoice 61.8 vs 79.1 (its weakest); customer service 76.0 vs 78.3. The headline "193.6× faster, 444.6× cheaper" compares against the single slowest and most expensive baseline.

### 2.2 Independent measurements

- **Vercel CEO, production safety-review step**: 18× faster at p95 than the LLM it replaced. The most credible speed number available.
- **jock.pl, 40 support tickets, one human labeler**: routing 39/40; anger MAE lowest of the set; 370 ms vs 1.2 s Haiku 4.5; lowest-confidence answers were the errors.
- **beri.net, 2,000 phishing emails**: asked one question, Jev 62.6% vs Haiku 4.5 81.3%. Decomposed into five atomic Nouls with a logistic regression fitted on 1,000 labels, **95%** on held-out 1,000. The gain came from decomposition plus fitting, not from the model. Same source measured **expected calibration error 0.107** (4.4× a perfectly calibrated model's noise floor), Nouls under-confident, Choice/Score over-confident, and 0.74 mean probability on unknowable questions answered 44.7% correctly.
- **Vendor self-consistency cookbook**: 15 repeats on a 14-question rubric, mean per-question probability std-dev 0.0102, far below temperature-0 LLMs. Stability is real; calibration is a separate and open question.
- **Vendor skill-suggestion cookbook, 182 skills / 488 requests**: wrong loads 16.8% → 7.3%, needless loads 9.8% → 4.0%, oracle 2.5% / 1.2%.
- **Vendor rerank cookbook, 40 legal queries × 30 BM25 candidates**: top-10 38% → 62%, 1,200 calls for $0.0645.
- **Vendor entity-alignment cookbook, 450 candidate pairs**: 80% auto-unlinked, 8.9% auto-merged, 11.1% to curators, using level names as the decision rule with no fitted thresholds.

### 2.3 Documented jaggedness (vendor's own list for 1.13)

1. Answers literally, not what you meant — spell out boundary cases.
2. **Cannot count** — do counting in code.
3. Poor on hex / RGB / raw numerics — bucket in code first.
4. Score cannot be read as exact magnitude — threshold only.
5. **Reads dates as text**, cannot order them — extract components, order in code.
6. Double negatives and indirection degrade — write directly.
7. **Accuracy falls as irrelevant state grows** — filter state in code. (This is the one that bites agent harnesses: send the tool result, not the whole transcript.)
8. **Not adversarially robust by default**; injected instructions in state can steer it. Test before using as a guardrail.
9. Conflicting instructions vs criteria confuse it.
10. No structural invariants across questions: P(X) + P(¬X) ≠ 1 is possible.
11. Cannot generate text; forcing it is slow and bad.

### 2.4 What the critics agree on

- "Zero hallucination" means **schema** hallucination only. It will confidently pick Billing when Technical was right.
- Individually calibrated judgments **do not compose** into a calibrated workflow once thresholded, weighted, and branched. The safety-gate use case is exactly where this matters.
- The decomposition technique that makes Jev look good also improves every LLM. Part of the win is the method, not the model.
- Shadow-eval before trusting: 1,000–2,000 decisions with existing ground truth costs under $0.20; pin `jev-1.13.0`, never the alias; add an explicit "none of these" option to every Choice; calibrate thresholds per question.

---

## 3. The vendor's suitability test

Six yes/no questions; 5–6 yes = good fit, 3–4 = decompose further, 0–2 = wrong tool.

| Test | Meaning |
|---|---|
| Judgment | Is the model deciding rather than creating? |
| Bounded | Can the answer space be enumerated up front? |
| Atomic | One focused judgment, not a chain? |
| Context-contained | Does everything needed fit in the state? |
| Fast-human | Could an expert answer in ~5 seconds? |
| Machine-consumed | Will code branch on the result directly? |

The rule of thumb the vendor and every reviewer converge on: **code calculates, Jev judges, LLMs reason and write, humans set objectives and risk tolerance.**

---

## 4. Where Loom already makes Jev-shaped decisions

Inventory from a code sweep of this branch. "Mechanism" is what runs today. "Class" is one of: **LLM** (a full generative `Chat()` call whose text is parsed), **heuristic** (regex / substring / additive score / counter), **config** (author- or operator-declared, never computed), **none** (no mechanism exists). Suitability is the vendor's six-question test (§3), scored by hand.

| # | Decision | Where | Mechanism today | Class | Jev shape | Fit |
|---|---|---|---|---|---|---|
| 1 | Which workflow branch | `pkg/orchestration/conditional_executor.go:166,181` | Whole agent turn emits one key; `selectBranch` substring-matches; `retryConditionEvaluation` up to 10 fresh sessions; `coerceBranchKey` regex salvage | LLM | Choice over branch keys | 6/6 |
| 2 | Memory recall relevance | `pkg/agent/agent.go:3398` `rerankMemories` | LLM call, "return only the numbers", 20 s timeout on the hot path; unparseable → return all | LLM | Noul per candidate, one fan-out | 6/6 |
| 3 | Recall query distillation | `pkg/agent/agent.go:3467` `extractSearchQuery` | LLM call, 10 s timeout; falls back to OR-joined keywords under load | LLM | Not Jev (text generation) | 1/6 |
| 4 | tool_search rerank | `pkg/tools/registry/registry.go:799` `rerankWithLLM` | LLM call over 20 candidates; BM25 confidence is `1/(1+(-score/10))`, uncalibrated | LLM | Score per candidate | 6/6 |
| 5 | tool_search query expansion | `registry.go:767` `expandQuery` | LLM call for synonyms | LLM | Not Jev (generation) | 1/6 |
| 6 | Conversation search rerank | `pkg/agent/segmented_memory.go:844` | LLM call, `[{index, score 0-10}]` | LLM | Score per candidate | 6/6 |
| 7 | User intent | `pkg/patterns/llm_classifier.go:84`; fallback `orchestrator.go:497` | LLM call → JSON `{intent, confidence}`; fallback is 8 keyword buckets with literal confidences (0.75–0.90) | LLM + heuristic | Choice over `Intents` + `none` | 6/6 |
| 8 | Escalate pattern pick to LLM? | `pkg/patterns/llm_reranker.go:167` | Magic thresholds on the heuristic score vector (0.70 / 0.20 / 0.60) | heuristic | Falls out of #7's confidence | — |
| 9 | Pattern rerank | `llm_reranker.go:48` | LLM call, 30-min cache | LLM | Choice over candidates | 6/6 |
| 10 | Stage output valid? | `pkg/orchestration/pipeline_executor.go:720,756` | LLM call, then `contains("valid"‖"yes"‖"true")` | LLM | Noul (+ Score on rubric) | 6/6 |
| 11 | Output acceptable (semantic criteria) | `pkg/orchestration/output_validator.go:184-189` | **Not implemented**; JSON Schema only | none | Noul battery over criteria | 5/6 |
| 12 | Swarm tie-break / vote | `pkg/orchestration/swarm_executor.go:601,407` | LLM judge call; `parseVote` defaults confidence 0.5 | LLM + heuristic | Choice over candidates | 5/6 |
| 13 | Debate converged? | `pkg/collaboration/debate.go:729` | `avgConfidence >= 0.8` over self-reported numbers | heuristic | Noul "positions agree" | 5/6 |
| 14 | LLM-as-judge | `pkg/evals/judges/llm/judge.go:90`; interface `judges/judge.go:37` | LLM call, 4 int scores + verdict scraped | LLM | Noul battery + Score per dimension, pre-screen | 5/6 |
| 15 | Tool result is a failure? | `pkg/shuttle/tool.go:49` `Success bool`; `conversation_helpers.go:179`; `pkg/fabric/guardrails.go:215` `InferErrorType`; dup at `circuit_breaker.go:464` | Tool-set flag + two duplicate 7-rule substring ladders; breaker counts only Go errors (`agent.go:3026`) | heuristic | Choice over failure kinds + Noul "retry with same input would help" | 6/6 |
| 16 | Tool call risk (HITL) | `pkg/shuttle/admission_hook.go:55` `Hook`; `permission_checker.go:141,163 (TODO)`; `skills/types.go:156` `IsHighRisk`; `loom.proto:2320 risk_level` | Static globs; `risk_level` is author- or caller-declared, **never computed**; `params` never inspected | config | Score {read-only, reversible, irreversible, touches-prod} + Nouls | 5/6 |
| 17 | Which skill(s) activate | `pkg/skills/index/router.go:170,264,444`; `library.go:300`; `orchestrator.go:238` | Router: **one LLM call per tree level**; fallback: keyword hit-rate × decayed confidence, raw substring | LLM + heuristic | Choice with nested criteria (taxonomy walk), 1–2 calls | 6/6 |
| 18 | Task hygiene: BLOCKED needs a human? | `pkg/skills/hygiene/auditor.go:228` | Status switch; "treats every BLOCKED as needing surfacing" by design | heuristic | Noul | 5/6 |
| 19 | Parent task done? | `pkg/task/manager.go:614` | All children DONE; output never read | heuristic | Noul "output satisfies goal" | 4/6 |
| 20 | Compression: keep or drop a tool result | `pkg/agent/context_compilation.go:561` | Size floor `len >= 2*len(stub)` and turn boundary | heuristic | Score "still useful" | 4/6 |
| 21 | When to run memory extraction | `pkg/agent/agent.go:2884`, cadence default 5 (`:124-127`) | Counter | heuristic | Noul "window holds durable facts" | 5/6 |
| 22 | Entity dedupe | `graph_memory_extractor.go:583` `normalizeEntityName` | `ToLower` + `TrimSpace` | heuristic | Score same/related/different per pair | 6/6 |
| 23 | Which model tier for a step | `pkg/agent/registry.go:613-677` roles from YAML | Static | config | Choice over rungs | 5/6 |
| 24 | Deliver a bus message? | `pkg/communication/bus.go:163` | Glob topic + filter | heuristic | Not needed; code is right here | — |

Sanity check on the five "not Jev" or "not needed" rows: generation tasks (#3, #5) stay on an LLM, and deterministic matching (#24) stays in code. That is the vendor's own rule and it holds.

Also worth naming: **Loom has no non-chat decision abstraction at all.** The whole provider contract is `LLMProvider{Chat, Name, Model}` (`pkg/types/types.go:203`), and every one of the LLM rows above is a text call whose output is JSON-parsed or substring-scraped by its caller. The five "aux" slots (`classifierLLM`, `compressorLLM`, `judgeLLM`, `orchestratorLLM`, merge LLM) are ordinary providers picked by YAML role, not a different kind of thing.

---

## 5. Ranked placements

Ranked by (measured pain) × (fit) × (blast radius if Jev is wrong). Each keeps today's path as the fallback for the uncertain band.

### Tier 1 — drop-in replacements of existing generative calls (low risk, measurable now)

**P1. Workflow branch selection** (#1). A whole agent turn, up to ten retries in fresh sessions, and a regex salvage layer exist to extract one enum token. A `Choice` over the branch keys with an explicit `none_of_these` returns the token plus a distribution in ~300 ms. Keep `retryConditionEvaluation` only for the low-confidence band. This also removes the anchoring problem the fresh-session retry exists to dodge.

**P2. The two per-turn recall calls and the tool_search rerank** (#2, #4, #6). The tool-calling assessment counted ~8 LLM calls per default turn for 4 visible ones; rerank calls are two of the hidden four. Recall rerank sits on the hot path with a 20 s timeout and degrades to "return everything" when the model's number list doesn't parse. One `Noul` fan-out ("could this memory bear on the message?") per candidate, in one request, replaces the call and yields a threshold instead of a parse. Vendor rerank data: top-10 recall 38% → 62% on legal passages. Query distillation (#3) stays on the LLM; it is generation.

**P3. Intent classification** (#7, #8, #9). The seam already exists: `IntentClassifierFunc` returning `(intent, confidence)`, with the keyword classifier as the fallback. A `Choice` over `Intents` + `none` returns a real confidence, which fixes `shouldInvokeLLMReRanker`'s three magic thresholds for free and makes the LLM pattern rerank (#9) an escalation rather than a default.

**P4. Stage output validation** (#10, #11). `validateStageOutput` makes an LLM call and then checks whether the reply contains "valid". Replace with a `Noul` per acceptance criterion; the same battery implements the semantic `acceptance_criteria` that `OutputValidator` explicitly leaves unimplemented, without making that package depend on an LLM.

**P5. Swarm and debate arbitration** (#12, #13). `Choice` over candidate outputs for the tie-break; `Noul` "these positions agree" for consensus instead of averaging self-reported confidences.

### Tier 2 — filling gaps where no mechanism exists today (highest value, needs guard rails)

**P6. Result-level failure classification feeding the circuit breaker** (#15). This is the P0 in `tool-calling-assessment.md`: 29.2% of tool executions fail, 75.9% of failures are followed by the identical call, and the breaker cannot see MCP failures because they arrive as `Result{Success:false}` with a nil error. Today's classifier is two duplicate 7-rule substring ladders. A `Choice` {`not_a_failure`, `transient`, `server_saturated`, `auth`, `bad_input`, `not_found`, `other`} plus a `Noul` "would retrying with identical input help?" over `{tool, input digest, result text}` gives the breaker and the identical-failure refusal a typed signal. **Guard**: tool results are untrusted text and Jev is not adversarially robust (jaggedness #8). Use the answer only to *brake* (count toward the breaker, refuse duplicates), never to *unlock* anything. Wrong answers then cost a spurious pause, not a bypass. The telemetry DB already holds 46,934 labeled executions (`Success`, error text) — a free shadow-eval set, roughly $0.80 at list price to run.

**P7. Tool-call risk scoring for HITL** (#16). `risk_level` is declared by skill authors or passed by callers and never computed; `CheckPermission` never looks at `params`; the approval callback is a TODO. The `Hook` interface (`Matches`/`Evaluate` over `AdmissionRequest{ToolName, Params, UserID, SessionID}` → `Decision{Allow|Deny|Ask}`) is exactly where a typed policy belongs. A `Score` {read-only, reversible write, irreversible, touches production} plus Nouls (`deletes_data`, `affects_other_users`, `names_prod_resource`) over the canonicalized call maps to `Ask` above a threshold. **Guards**: (a) the hook may only *raise* restrictiveness (`NoDecision` → `Ask`), never lower it; existing Deny and fail-closed stay; (b) `params` can carry injected text, so the state sent is the canonicalized call and tool description, not free prose; (c) individually calibrated judgments don't compose through thresholds, so the threshold is per-tool-class and validated on the pending-permission history. Precedents: LangChain `AutoModeMiddleware`, `jev-mcp`'s `jev_gate` (allow/confirm/block, advisory).

**P8. Skill and pattern selection** (#17). The skills router spends one LLM call per tree level; the non-router path is substring hit-rate. The vendor's own two-stage recipe (rank all N on truncated descriptions, re-rank top 3 with full text) cut wrong skill loads 16.8% → 7.3% on 182 skills, and `Choice` criteria accept nested JSON so a taxonomy walk is one or two requests. Directly relevant to the Teradata pattern library (84 patterns) and the tool_search index.

**P9. Judge pre-screen and runtime verification** (#14). A `TypedJudge` implementing `judges.Judge.Evaluate` slots into the aggregator with no changes. A `Noul` battery ("response answers the question", "response claims success while a tool result shows an error", "response names a table absent from the schema") plus `Score` per dimension screens every response; only the uncertain band goes to the LLM judge. This is cheap enough to run on **every production turn**, which is the P0 "runtime verification loop" gap in `docs/architecture/loop-engineering.md`. Vendor citation-check recipe is the template (supports / contradicts / says-nothing, auto-accept ≥ 0.8).

**P10. Hygiene and done-ness** (#18, #19). The auditor's "every BLOCKED needs a human" is documented as a conservative guess because it cannot read the chat; a `Noul` over `{task, close_reason, notes}` can. Parent-task completion is structural only; a `Noul` "output satisfies the task goal" is the missing quality check.

### Tier 3 — plausible, needs data before committing

**P11. Extraction trigger** (#21). A `Noul` "does this window contain durable facts about the user, entities, or decisions?" before paying for the extraction LLM call. That call fires on every `Chat()` entry and every 5 tool executions and is the single largest uncounted aux cost.

**P12. Entity dedupe** (#22). `ToLower`+`TrimSpace` is the entire entity-resolution strategy; the vendor's entity-alignment recipe (one 3-level `Score` + three `Noul`s per pair, level names as the rule) auto-resolved 89% of 450 pairs. Addresses the entity-confusion class fixed once already in 2026-03.

**P13. Compression keep/drop** (#20). `Score` "still useful for the remaining task" per tool result instead of a byte floor. Cost scales with results retained; measure first.

**P14. Model tier routing** (#23). `Choice` over rungs per step, LangChain `ModelRouterMiddleware` precedent, and a natural complement to PR #317 capability leveling.

**P15. Offline trace classification.** The vendor benchmarks an "agent-trace observability" workflow and pitches "classify giant agent traces". Bulk-labelling `tool_executions` and spans (failure kind, retry-was-pointless, result-was-reused) is the `loom analytics tools` yardstick the assessment proposes, at ~$0.0004 per row.

---

## 6. Integration design

**Not a provider.** The vendor says it and the code agrees: there is no `model: jev-latest`. Jev has no chat surface, so it does not belong in `pkg/llm/factory`'s switch or the model catalog. It needs a sibling abstraction.

Proto first, in a new small file (`proto/loom/v1/decision.proto`):

```
message DecisionRequest  { google.protobuf.Value state; map<string, Question> questions; string model; }
message Question         { string instructions; oneof kind { NoulQuestion noul; ChoiceQuestion choice; ScoreQuestion score; } }
message ChoiceQuestion   { map<string, google.protobuf.Value> options; }   // ≤255
message ScoreQuestion    { repeated google.protobuf.Value levels; }         // 2–10
message NoulQuestion     { optional google.protobuf.Value criteria_true; optional google.protobuf.Value criteria_false; }
message DecisionResponse { string model; map<string, Answer> answers; DecisionUsage usage; }
message Answer           { oneof kind { NoulAnswer noul; ChoiceAnswer choice; ScoreAnswer score; } }
message ChoiceAnswer     { string choice; map<string, double> probabilities; double confidence; }
message ScoreAnswer      { double score; map<string, string> legend; map<string, double> probabilities; double confidence; }
message NoulAnswer       { double noul; }
```

Go surface, `pkg/decision`:

```go
type Decider interface {
    Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error)
    Name() string
    Model() string
}
```

Implementations, in order:

1. **`decision/llm`** — adapts any existing `types.LLMProvider` to the same request/response by prompting for the JSON shape. This is the fallback for the uncertain band, the offline comparator, and the thing that makes every call site testable before a Jev key exists.
2. **`decision/jev`** — ~200 lines, stdlib `net/http`, `TYPESAFE_API_KEY`, pinned `jev-1.13.0`, maps 401/422/429/529 to typed errors, honours the 64k/32k limits client-side. Own it; don't depend on an unofficial SDK.
3. **`decision/mock`** — table-driven answers for unit tests.
4. **`decision.Instrumented`** — mirrors `pkg/llm/instrumented_provider.go`: span `decision.evaluate` with provider, model, question count, per-question confidence, input tokens, cost, latency; metrics alongside `MetricLLMCalls`. This is how the invisible aux calls become visible (assessment RC6).
5. **`decision.Router`** — the act / review / escalate helper: per-call-site bands in config, uncertain → the existing path. Every call site keeps its current mechanism as the fallback, the way `llm_classifier.go` already falls back to keywords.

Wiring: a `decision:` block in agent YAML next to the role LLMs (`provider: jev|llm|off`, `model`, `bands` per site), default **off**. `LLM_ROLE_CLASSIFIER` and `compressorLLM` remain the fallback providers. Rate limits (1,200 rpm) are per account, so a 512-agent fleet needs its own budget in the slot scheduler, or a shared limiter.

**Shadow mode is the first milestone, not an option.** Wire `decision.Instrumented` at P6 and P2 with `shadow: true`: call Jev, log the answer and the existing mechanism's answer side by side, branch on neither. The local `loom.db` and the MCP gauntlet rig supply the ground truth. Only after a per-site agreement/ECE report does a band get a live threshold.

**Data egress.** Tool results can carry customer rows. State must be filtered in code (vendor jaggedness #7 also demands it): for P6 send `{tool, error code, error text, input digest}`, not the payload; for P7 send the canonicalized call, not the transcript. Enterprise zero-retention is contractual, not a request flag. Deployments that cannot egress at all run `provider: llm` against a local Ollama model behind the same interface, or `off`.

Testing: table-driven, mock decider, `-race` on the instrumented wrapper and router (they sit inside the conversation loop), golden request JSON per call site, fuzz on state marshalling and answer parsing.

---

## 7. Risks and what would kill it

| Risk | Severity | Mitigation |
|---|---|---|
| Calibration unproven (ECE 0.107 independently; Choice/Score over-confident) | High for P7, medium elsewhere | Shadow-eval per site; thresholds per question; P7 only raises restrictiveness |
| Prompt injection via tool results / params steers the answer | High for P6/P7 | Brake-only semantics; canonicalized state; adversarial test set before enabling |
| Hosted-only, waitlist, no weights, no SLA published | High for enterprise | Interface-first; `decision/llm` and local classifier as peers; feature flag default off |
| Customer data egress in state | High | Filter state in code; contractual zero-retention; per-deployment `off` |
| Accuracy drops with irrelevant context | Medium | Send fields, not transcripts (same fix as above) |
| Rate limits (1,200 rpm) vs fleet scale | Medium | Budget in slot scheduler; batch questions per request |
| English-first; CJK weaker | Medium for Teradata field deployments | Measure on real traffic before enabling for non-English tenants |
| Vendor accuracy is LLM-agreement, not truth | Medium | Treat as unknown until shadow-eval says otherwise |
| Alias drift (`jev-latest`) | Low | Pin `jev-1.13.0`; response carries version |
| Composition: thresholded judgments don't stay calibrated | Medium | One decision per gate; no chained thresholds; escalate, don't multiply |

---

## 8. Checklist

### P0 — prove the shape without a vendor dependency
- [ ] `proto/loom/v1/decision.proto` + `buf generate`; `pkg/decision` interface, `mock`, `llm` adapter, `Instrumented`, `Router` — all tested `-race`, no Jev key needed
- [ ] Wire `Instrumented` into the three rerank sites (P2) and the failure classifier (P6) in **shadow** mode; emit both answers to hawk
- [ ] Replay `loom.db` `tool_executions` through the failure classifier (P6) offline; report agreement, ECE, and per-kind confusion

### P1 — first live bands
- [ ] Add `decision/jev` client; pin `jev-1.13.0`; contract tests against recorded fixtures
- [ ] Turn on P1 (branch selection) and P2 (reranks) with conservative bands; measure calls/turn and recall quality against the LongMemEval harness
- [ ] P6 live in brake-only mode: `Result.Success == false` + typed kind feeds the breaker and the identical-failure refusal

### P2 — the gaps
- [ ] P7 risk hook as an `admission_hook.Hook` that can only raise to `Ask`; adversarial fixture set; validate against pending-permission history
- [ ] P9 `TypedJudge` behind `judges.Judge`; run on every turn in a canary agent; compare with the LLM judge on the eval suites
- [ ] P8 two-stage skill/pattern selection; measure wrong-load rate on the Teradata library

### P3 — measure first
- [ ] P11 extraction trigger, P12 entity dedupe, P13 compression score, P14 tier routing — each behind shadow mode with its own agreement report before a live band
- [ ] `loom analytics tools` bulk classification (P15) as the regression yardstick

---

## 9. Sources

- TypeSafe docs: introduction, `api`, `models`, `confidence`, `concepts/state`, `primitives/advanced`, `model-jaggedness/jev-1.13`, `introduction/coding-agents`; cookbooks `skill_suggestion`, `rerank_typesafe`, `entity_alignment`, `llm_guardrails`, `citation_check`, `classifying_rag_passages`, `function_calling`, `consistency_noul_cookbook` — https://docs.typesafe.ai
- LiteLLM passthrough — https://docs.litellm.ai/docs/pass_through/typesafe
- LangChain harness post — https://www.langchain.com/blog/building-a-harness-with-jev
- browser-use jev-ultrafast — https://github.com/browser-use/jev-ultrafast
- codaaiteam jev-mcp — https://github.com/codaaiteam/jev-mcp
- lmathia2 pi-jev — https://github.com/lmathia2/pi-jev
- unimtx typesafe-sdk-go (community) — https://github.com/unimtx/typesafe-sdk-go
- pearpages, "Jev, Sorted" — https://pearpages.com/blog/2026/09/16/jev-sorted-what-typesafes-system-one-model-actually-is-and-what-is-still-just-a-claim
- beri.net, calibration / decomposition / shadow eval — https://www.beri.net/article/typesafe-jev-typed-decision-model-calibration-decomposition-shadow-eval
- jock.pl 40-ticket benchmark — https://thoughts.jock.pl/p/jev-typesafe-system-one-model-benchmark-2026
- dev.to, "GPT-6 and Claude wrote the answer key" — https://dev.to/gabrielanhaia/jev-beat-gpt-luna-by-1-point-gpt-6-and-claude-wrote-the-answer-key-314k
- KDnuggets — https://www.kdnuggets.com/what-everyone-is-getting-wrong-about-typesafe-ais-jev
- eesel, Vercel p95 figure — https://www.eesel.ai/blog/jev-ultrafast
- Internal: `docs/research/tool-calling-assessment.md`, `docs/architecture/loop-engineering.md`
