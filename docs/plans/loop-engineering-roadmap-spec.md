# Loop Engineering Roadmap — Specification

**Status**: 📋 Proposed (nothing in this document is implemented yet)
**Baseline**: `main` @ `68208a0a` (v1.3.0)
**Source analysis**: [docs/architecture/loop-engineering.md](../architecture/loop-engineering.md)
**Scope**: P0–P3 roadmap closing the gaps identified in the loop-engineering fit/gap analysis. Every claim about existing code below was verified against the baseline commit.

This is the *what* (contracts, semantics, proto surfaces). The *how* (step ordering, file-level changes, test matrices) lives in the implementation plan. All proto changes are append-only; field numbers stated here are verified free at the baseline and are binding.

---

## Contents

1. [P0 — Final-output verification in the agent loop](#p0--final-output-verification-in-the-agent-loop)
2. [P1a — Per-conversation cost ceiling](#p1a--per-conversation-cost-ceiling)
3. [P1b — Pattern effectiveness recording](#p1b--pattern-effectiveness-recording)
4. [P2c — Loop-config fingerprint](#p2c--loop-config-fingerprint)
5. [P2a — Verifier subagent (maker/checker)](#p2a--verifier-subagent-makerchecker)
6. [P2b — TeleprompterService and scheduled optimization](#p2b--teleprompterservice-and-scheduled-optimization)
7. [P3 — Webhook triggers and lifecycle hooks](#p3--webhook-triggers-and-lifecycle-hooks)
8. [Compatibility](#compatibility)

---

## P0 — Final-output verification in the agent loop

### Proto

```proto
// agent_config.proto (new import: loom/v1/collaboration.proto)
message BehaviorConfig {
  // ... existing fields 1-8 unchanged ...

  // Verifies the final assistant output of a conversation before it is
  // returned to the caller. In this (agent-loop) context only output_schema
  // and acceptance_criteria are honored. validator_agent_id and
  // judge_config_id are workflow-only and are REJECTED at config load.
  // retry_policy.session_mode must be CONTINUE or unspecified
  // (unspecified maps to CONTINUE); FRESH and ESCALATE are REJECTED.
  // Retries are capped at min(retry_policy.max_retries, 10).
  OutputPolicy output_policy = 9;
}
```

`OutputPolicy`, `OutputRetryPolicy`, and `RetrySessionMode` are the existing messages in `collaboration.proto` (same `loom.v1` package); no new sibling messages.

### Runtime semantics

1. **Trigger point.** Verification runs when the conversation loop reaches a terminal response (no tool calls), after the empty-response nudge and end-of-turn skill hygiene, immediately before the response is returned. The forced-synthesis output produced on budget exhaustion is **never** verified.
2. **Verifier order** (cost/determinism hierarchy):
   a. `output_schema` — JSON Schema validation (gojsonschema) against JSON extracted from the response text. Deterministic, no LLM call.
   b. `acceptance_criteria` — one no-tools LLM call using the agent's own provider. The evaluation prompt is task-oriented (no role prompting), supports the documented `{{output}}` placeholder, and demands a strict first-line verdict: `PASS` or `FAIL: <reason>`. Runs only if (a) passed or no schema is set. The evaluation call is **never streamed to the client**: it runs with the progress callback stripped from its context, and a self-correction progress event marks each retry injection so streaming clients get a semantic boundary between rejected and replacement generations.
3. **On failure**: a feedback message (role `user`) is injected into the session and the loop continues. The feedback includes the failure reason, the truncated previous output (≤500 chars), and the schema or criteria text. `retry_policy.feedback_template` overrides the default; supported variables: `{{error}}`, `{{previous_output}}`, `{{attempt}}`, `{{max_retries}}`. `cooldown_ms` waits are context-aware (cancellation aborts the wait).
4. **On exhaustion**: graceful degradation. The last output is returned unchanged with failure metadata — verification never turns a completed conversation into an error.
5. **Fail-open judge**: a malformed verdict (neither `PASS` nor `FAIL:` on the first line) does not burn a retry; the response is returned with `output_verification: "judge_inconclusive"`.
6. **Content is validated, never rewritten.** Prose-plus-JSON responses are legal; machine consumers extract downstream.
7. **Budget interaction**: verification retries consume conversation turns; `max_turns` bounds total work. If the loop budget trips during verification retries, the synthesized response carries `output_verification: "skipped_budget_exhausted"`.
8. **Layering with workflows**: `behavior.output_policy` applies to *every* Chat, including pipeline stage executions and each stage retry — nesting it inside stage-level `output_schema`/`validation_prompt`/`retry_policy` multiplies retries and judge cost. Guidance: set one layer, not both (or keep the schemas identical).

### Config-load validation (hard errors, not warnings)

| Field | Rule |
|---|---|
| `validator_agent_id`, `judge_config_id` | Rejected: "not supported in behavior.output_policy; use workflow-level OutputPolicy" |
| `retry_policy.session_mode` = FRESH or ESCALATE | Rejected: the live conversation is the session; only CONTINUE semantics exist in-loop |
| `retry_policy.max_retries` > 10 | Clamped to 10 |

### Response metadata contract

| Key | Values |
|---|---|
| `output_verification` | `passed` \| `failed` \| `judge_inconclusive` \| `skipped_budget_exhausted` |
| `output_verification_attempts` | int |
| `output_verification_error` | last failure string (only when `failed`) |

### Observability contract

Span events: `output_verification.retry_injected`, `output_verification.passed`, `output_verification.exhausted`, `output_verification.judge_inconclusive`. Span attributes: `verification.attempts`, `verification.outcome`; root attribute `config.output_verification` (bool).

### YAML example

```yaml
behavior:
  output_policy:
    output_schema: |
      {"type": "object", "required": ["answer", "confidence"]}
    acceptance_criteria: "The answer cites at least one table name from the schema."
    retry_policy:
      max_retries: 2
      cooldown_ms: 500
```

### Shared code consequence

A new leaf package `pkg/validation/output` (JSON extraction, schema validation, feedback building, verdict parsing) becomes the single implementation used by both the pipeline executor and the agent loop. The dead `pkg/orchestration/output_validator.go` (zero callers, zero tests) is deleted.

---

## P1a — Per-conversation cost ceiling

### Proto

```proto
// agent_config.proto
message BehaviorConfig {
  // ... fields 1-9 ...

  // Maximum estimated LLM spend (USD) for one conversation
  // (one Chat invocation), 0 = disabled. Cost values are best-effort
  // estimates from the pricing catalog; Bedrock prices vary by region.
  // The limit may overshoot by up to one turn plus one synthesis call.
  double max_cost_usd = 10;
}
```

### Runtime semantics

1. **Scope**: one conversation loop invocation — *not* session-lifetime spend (`Session.TotalCostUSD` continues to accumulate independently).
2. **Accumulation**: every main-loop LLM call adds its `Usage.CostUSD` to a conversation total — including empty-nudge, hygiene, P0 verification-eval calls, and **L2 compression calls** (which grow on exactly the long conversations the ceiling targets). Background graph-memory extraction is **excluded** (asynchronous, post-turn) and documented as such.
3. **Enforcement**: checked in the loop guard alongside `max_turns`/`max_tool_executions`. The loop is never broken mid-iteration (that would split assistant/tool_result pairing); enforcement happens at the top of the next iteration.
4. **On trip**: the loop exits into the existing forced tools-disabled synthesis call, so the caller always receives a coherent text answer. The synthesis call's own cost is added to the reported total (the documented overshoot).
5. **Zero-cost providers** (e.g., Ollama) report `CostUSD == 0` and never trip the limit. When `max_cost_usd > 0` and a turn reports zero cost, a debug log flags possible undercounting (streaming Usage propagation is provider-dependent).
6. **Sub-agents budget independently**: agents spawned via `manage_ephemeral_agents` run their own conversations — each bus message they process gets a fresh budget, and the parent's `max_cost_usd` does not aggregate their spend. (`EphemeralAgentPolicy.cost_limit_usd` covers only the swarm spawn path.)

### Metadata / observability contract

`total_cost_usd` (all exit paths) and `cost_limit_hit: true` (trip only) in response metadata and the `conversation.completed` event; span event `cost_budget.exceeded {total_cost_usd, max_cost_usd, turns}`; root attribute `config.max_cost_usd`; metric `agent.conversations.cost_limit_hit`.

---

## P1b — Pattern effectiveness recording

No proto change. Re-wires the existing, currently caller-less `patterns.Orchestrator.RecordPatternUsage` → `PatternEffectivenessTracker` pipeline to the `load_pattern` era.

### Semantics

1. **Recording a load**: when `load_pattern` successfully loads a pattern inside a conversation (session identity present in tool context), the canonical `pattern.Name` is recorded against the session (deduplicated; aliases aggregate under the canonical name). Loads outside a conversation are silently not recorded and do not affect the tool result.
2. **Attribution window**: per Chat invocation, drain-on-read — each recorded pattern is attributed exactly once, at the end of the Chat that follows its load.
3. **Outcome attribution**: each pattern loaded during the conversation receives one `RecordPatternUsage` call carrying the **whole-conversation** outcome:
   - `success` = Chat returned without error AND the final output was not `output_verification: failed/exhausted` AND the cost ceiling did not trip (P0/P1a metadata folds into the flag)
   - `costUSD` = conversation total (P1a metadata) when available, else final-call cost (documented degraded fallback)
   - `latency` = Chat wall time; `errorType` ∈ {`""`, `timeout`, `canceled`, `llm_error`}
   - live `llmProvider`/`llmModel` (fixes the historical hardcoding defect)
4. **Gate**: `patterns.enable_tracking` (existing config, default true; already fully plumbed proto→YAML→registry).
5. **Documented caveat**: when multiple patterns are loaded in one conversation, each receives the full conversation cost — summing cost across patterns double-counts. The stored metric answers "what do conversations using this pattern cost/succeed like," not "what did this pattern cost."
6. **Concurrency caveat**: concurrent Chats on the *same session* produce best-effort attribution — drain-on-read means patterns attribute to whichever Chat drains first. Race-safe; documented.

Downstream storage and bus publication are unchanged (existing `pattern_effectiveness` SQLite table; `meta.pattern.effectiveness` topic). New span event: `pattern.effectiveness_recorded {patterns, success, cost_usd}`.

---

## P2c — Loop-config fingerprint

No proto change.

### Contract

Every root conversation span carries:

- `config.loop_fingerprint` = `"v1:" + hex(sha256(sorted key=value lines))` over an **explicit, versioned field list**: `MaxTurns`, `MaxToolExecutions`, `MaxIterations`, `OutputTokenCBThreshold`, `MaxContextTokens`, `ReservedOutputTokens`, `EnableSelfHealing`, retry-policy scalars, `MaxCostUSD` (P1a), and the output-verification fields (P0).
- Individual attributes for fields not already emitted: `config.max_iterations`, `config.max_context_tokens`, `config.self_healing`, `config.retry_max_attempts`, `config.output_token_cb_threshold`.

Rules:

1. **Model/provider are excluded** — they are already separate span attributes. Harness-Bench-style attribution = model attrs + loop fingerprint together.
2. **Versioning**: any change to the field list bumps the version prefix (`v2:` …). Fingerprints with different versions are never comparable.
3. **Effective values**: computed lazily at first chat (post config-loader defaulting), cached per agent. A config built through the loader and one with identical explicit values MUST produce identical fingerprints.
4. **Zero downstream wiring**: the attribute flows to OTLP as `loom.config.loop_fingerprint` (automatic unmapped-key prefixing) and into `EvalRun.ConfigurationJSON` (spanToEvalRun marshals all span attributes). Eval runs become attributable to a specific loop design; A/B-ing loop changes uses existing infrastructure.

---

## P2a — Verifier subagent (maker/checker)

Three deliverables: one behavior fix, one template, one documented convention.

### Spawn `initial_message` delivery (behavior fix)

Current behavior: `manage_ephemeral_agents(spawn).initial_message` is stored in metadata and never delivered. New contract:

1. If `auto_subscribe` is non-empty, `initial_message` is published to the **first successfully subscribed** topic immediately after subscriptions are registered (registration is synchronous inside SpawnSubAgent, before the child loop starts — buffered channels make publish-after-subscribe race-free; subscribe failures are tolerated per topic, so the target is `subscribedTopics[0]`, and all-subscriptions-failed with an initial_message is an error, not silence). `FromAgent` = parent agent ID, so the child's self-echo filter does not suppress it.
2. `initial_message` without `auto_subscribe` → `InvalidArgument` ("initial_message requires auto_subscribe"). No silent drop. **Behavior change** (today the message is silently stored in metadata) — called out in CHANGELOG.
3. Documented limitations (pre-existing, not new): the spawned-agent loop services only its first subscription; delivery is asynchronous and advisory (parent does not block on a reply); 10 spawns per parent.

### Verifier template

`examples/reference/agent-templates/verifier.yaml` — `kind: AgentTemplate`, `extends: base-expert`. Parameters: `artifact_description`, `verification_criteria`, `output_contract`. Task-oriented system prompt (no role prompting) requiring the strict verdict contract:

```json
{
  "passed": false,
  "critique": "one-paragraph summary",
  "violations": [
    {"criterion": "…", "severity": "BLOCKER|MAJOR|MINOR", "detail": "…"}
  ]
}
```

Checker discipline: `temperature: 0.1`, `max_turns: 3`, and an **empty tool set** (a pure checker reads; it does not act). Note: `max_tool_executions: 0` cannot express this — proto3 zero-values are rewritten to the default (50) by config mapping, and a true 0 would prevent the loop from running.

### Maker/checker conventions (documented in docs/guides/maker-checker.md)

- **Synchronous gating** → pipeline variant: maker stage → checker stage with `output_schema` enforcing the verdict JSON and a stage `retry_policy` routing the critique back into the maker's retry prompt. Expressible entirely with existing PipelineStage fields.
- **Advisory checks** → ephemeral variant: spawn the verifier with `auto_subscribe` + `initial_message` (works after the fix above); verdict arrives on the shared topic.
- Known limitation stated: the preset/policy-template spawn path is unwired at baseline and out of scope.

---

## P2b — TeleprompterService and scheduled optimization

Fixes a real defect — the CLI targets `loomv1.TeleprompterServiceClient`, but **no server implementation exists**; additionally there is **no learned-layer runtime at all** (`teleprompter.Memory` has no production implementation and `getSystemPrompt` has no injection point). Two PRs.

### PR 1 — Service + durable learned layer

**Proto (teleprompter.proto):**

```proto
message CompileRequest {
  // ... fields 1-6 ...
  AutoApplyMode apply_mode = 7;  // UNSPECIFIED => MANUAL
}

enum CompilationStatus {
  COMPILATION_STATUS_UNSPECIFIED = 0;
  COMPILATION_STATUS_PENDING = 1;
  COMPILATION_STATUS_APPLIED = 2;
  COMPILATION_STATUS_REJECTED = 3;
  COMPILATION_STATUS_ROLLED_BACK = 4;
  COMPILATION_STATUS_DRY_RUN = 5;
}

message CompilationResult {
  // ... fields 1-15 ...
  CompilationStatus status = 16;
}

message GetCompilationHistoryRequest {
  // ... fields 1-3 ...
  CompilationStatus status_filter = 4;
}
```

**Durable store** (SQLite, WAL): `compilations` (history; `result_json` = protojson of CompilationResult) and `learned_layers` (exactly one active layer per agent = truth). Learned layers survive restart.

**Learned-layer application semantics** (normative):

1. The active learned layer is appended to the agent's system prompt as a supplement — it **never replaces** the configured prompt. Demonstrations render as a few-shot block.
2. **Applies at session ROM build — creation, or restore after a server restart.** The system prompt is baked into per-session ROM (deliberately byte-stable for provider prompt caching); session *restore* re-renders ROM, so restarted servers rebuild in-flight sessions with the then-active layer. This is a documented property, not "hot reload."
3. The learned version is **stamped on the session at ROM render time**, and that per-session stamp — never the store's current active version — is emitted as `config.learned_version` on conversation spans (ties into P2c for before/after attribution; guarantees the span never disagrees with the ROM actually in use).
4. Rollback restores a prior compilation's layer transactionally; the displaced compilation is marked `ROLLED_BACK`.

**RPC semantics:**

| RPC | Behavior |
|---|---|
| `Compile` | Validates (agent exists, trainset non-empty, cap ~200 examples, per-compile timeout, one in-flight compile per agent). Optimizers: BootstrapFewShot, MIPRO; others → `Unimplemented` with supported list. Metrics: MultiJudge (`FailedPrecondition` without judge orchestrator), ExactMatch; others → `InvalidArgument`. Compile-time prompt candidates run on **one agent instance per candidate** (non-registering construction, in-memory memory — compile sessions never touch production storage) with a **fresh session per example**, flowing through the same injection path as production; the live agent is never mutated during compile. Examples execute **real tools** — read-only agents/backends are the recommended optimization targets. |
| apply modes | `MANUAL` → result saved `PENDING` (nothing applied). `DRY_RUN` → report only. `VALIDATED` → applied iff devset score does not regress, else `PENDING`; **`VALIDATED` with no devset → `InvalidArgument`**. `AUTONOMOUS` → applied immediately. |
| `GetCompilationHistory` | Paginated, honors `status_filter`. |
| `RollbackCompilation` | Target must belong to the agent; applies target layer, marks previous `ROLLED_BACK`. |
| `CompareCompilations` | Runs the testset through both layers with the requested metric; fills the comparison including paired-sign-test significance. |

TextGrad remains local/CLI-only (its DRY_RUN/AUTONOMOUS stubs are out of scope and stay documented as such).

### PR 2 — Scheduling + approval

**Proto:**

```proto
// teleprompter.proto
rpc ApplyCompilation(ApplyCompilationRequest) returns (ApplyCompilationResponse);
rpc RejectCompilation(RejectCompilationRequest) returns (RejectCompilationResponse);

// orchestration.proto
message ScheduledWorkflow {
  // ... fields 1-12 ...
  ScheduledJobType job_type = 13;          // UNSPECIFIED = workflow (back-compat)
  OptimizationJobSpec optimization = 14;
}

message OptimizationJobSpec {
  string agent_id = 1;
  TeleprompterType teleprompter = 2;
  TeleprompterConfig config = 3;
  MetricConfig metric = 4;
  string trainset_path = 5;      // JSONL
  string eval_suite_path = 6;    // alternative source
  string devset_path = 7;
  AutoApplyMode apply_mode = 8;  // AUTONOMOUS is REJECTED for scheduled jobs
  string notify_topic = 9;       // default "loom.optimization.completed"
}
```

**Semantics:**

1. **Back-compat is absolute**: `job_type` unset behaves byte-for-byte like today's workflow scheduling. The loader continues to key on `schedule:` presence; the optimization section is additive.
2. Scheduled runs call Compile in-process and publish `{compilation_id, agent_id, trainset_score, devset_score, status}` to `notify_topic`.
3. **No unattended prompt mutation**: scheduled jobs may use `MANUAL` or `VALIDATED` only; `AUTONOMOUS` is rejected at schedule validation.
4. **Approval**: `ApplyCompilation`/`RejectCompilation` transition `PENDING` results only (`FailedPrecondition` otherwise); CLI gains `pending`/`apply`/`reject`. A contact_human approver-agent pattern is documented but not built.
5. **Explicitly deferred**: mining accumulated eval runs as trainsets (the observability eval-run store is write-only). Follow-up: `ListEvalRuns` + an `eval_runs` trainset source. Trainset v1 = JSONL and eval-suite YAML.

---

## P3 — Webhook triggers and lifecycle hooks

User decision: declarative config policies + bus events. **No arbitrary command execution.**

### Proto

```proto
// agent_config.proto
message AgentConfig {
  // ... fields 1-20 ...
  HooksConfig hooks = 21;
}

message HooksConfig {
  repeated ToolHookRule pre_tool_use = 1;
  repeated ToolHookRule post_tool_use = 2;
  LifecycleEventsConfig events = 3;
}

message ToolHookRule {
  string name = 1;          // rule identifier (appears in logs/events/deny messages)
  string tool_matcher = 2;  // glob over tool name
  HookAction action = 3;    // ALLOW | DENY | WARN
  string reason = 4;
  bool emit_event = 5;
  string topic = 6;         // override event topic
}

message LifecycleEventsConfig {
  optional bool enabled = 1;      // default false
  string topic_prefix = 2;        // default "loom.lifecycle"
  bool include_arguments = 3;     // default false (privacy)
  repeated string emit = 4;       // "tool.pre" | "tool.post" | "turn.completed" | "conversation.completed"
}

// orchestration.proto
message ScheduleConfig {
  // ... fields 1-7 ...
  WebhookTrigger webhook = 8;
}

message WebhookTrigger {
  string path = 1;             // suffix under /v1/webhooks/
  string hmac_secret_env = 2;  // NAME of env var holding the secret — never a literal
  map<string, string> variable_mapping = 3;  // schedule variable <- JSON body field
  bool skip_if_running = 4;
}
```

Plus a `LifecycleEvent` bus payload message: `agent_id, session_id, event_type, tool_name, duration_ms, success, error (truncated), arguments_json (only when include_arguments)`.

### Hook semantics (normative)

1. **Evaluation**: ordered rules, first match wins, default ALLOW when no rule matches or no config present.
2. **Placement and restrictiveness**: pre-hooks run before circuit breaker and executor dispatch. Hooks are **restrict-only** — a hook ALLOW never bypasses the executor-level permission checker or circuit breakers; DENY short-circuits before them. Post-hooks run after execution and may only WARN/emit (no retroactive deny).
3. **DENY is model-visible but is not a failure**: the tool call returns a structured error — `tool <name> denied by policy '<rule>': <reason>` — never a silent drop, but it is classified **`policy_denied`** for analytics (message aligned with the migration-000018 outcome patterns) and does **not** feed the circuit breaker or guardrail failure tracking (an open breaker would replace the actionable policy reason with an opaque error).
4. Tools matched by an **unconditional DENY** rule are hidden from tool advertisement (consistent with the disabled-tools discovery filter) so the model does not burn turns calling them.
5. **WARN**: log + span event, execution proceeds.
6. A configuration whose first matching rule set denies everything (catch-all DENY) produces a startup warning.

### Lifecycle events

Published to `<topic_prefix>.<event_type>` only for event types in the explicit `emit` list (`enabled` default false — no firehose). Tool arguments are excluded unless `include_arguments: true`; errors are truncated. Publish failures are logged and never fail the tool call or turn.

### Webhook endpoint

`POST /v1/webhooks/<path>` on the existing HTTP server (registered before the grpc-gateway catch-all).

| Aspect | Contract |
|---|---|
| Auth | HMAC-SHA256 over **`timestamp + "." + rawBody`** (Stripe/Slack scheme — the timestamp is covered by the MAC, so a captured pair cannot be replayed with a fresh timestamp) with the secret from `hmac_secret_env`; headers `X-Loom-Signature` (hex) + `X-Loom-Timestamp`; constant-time compare |
| Freshness | timestamp within ±5 minutes; replayed signatures within the window rejected (LRU) |
| Resolution | `path → schedule` resolved **dynamically per request** (schedules mutate at runtime via RPCs and YAML hot-reload); path collisions rejected at schedule create/update; removed schedule → 404; per-schedule replay/rate-limit state is lifecycle-managed |
| Scheduling | cron is **optional** when a webhook trigger is present — webhook-only schedules are valid |
| Limits | 1 MiB body cap enforced before read; per-hook rate limit → 429 |
| Missing secret env | hook **disabled**; requests → 503 (auth is never skipped) |
| Success | body fields mapped per `variable_mapping` → `scheduler.TriggerNow` → `202 {execution_id}`; **variables are interpolated into stage prompt templates** before pattern execution (P3 completes the scheduler's existing variable-interpolation TODO — `variable_mapping` is not inert config) |
| Failures | 401 bad/stale/replayed signature; 404 unknown path; 405 non-POST; 413 oversize |
| Privacy | request body and signatures never logged or echoed |

Out of scope (stated in docs): queue consumers and file-watch trigger sources.

---

## Compatibility

- All proto changes are **append-only**; every phase independently passes `buf breaking` against `main`. Field allocations are binding: `BehaviorConfig.output_policy = 9`, `BehaviorConfig.max_cost_usd = 10`, `AgentConfig.hooks = 21`, `ScheduleConfig.webhook = 8`, `ScheduledWorkflow.job_type = 13` / `optimization = 14`, `CompileRequest.apply_mode = 7`, `CompilationResult.status = 16`, `GetCompilationHistoryRequest.status_filter = 4`.
- All new behavior is **opt-in and default-off**: no output_policy → no verification; `max_cost_usd = 0` → no ceiling; no hooks config → all tools allowed, no events; no webhook config → no endpoint for that schedule; `job_type` unset → workflow scheduling unchanged; no applied compilation → system prompts unchanged.
- Landing order: P0 → P1a → P1b → P2c → P2a → P2b-1 → P2b-2 → P3. Later phases reference earlier metadata (`total_cost_usd`, fingerprint fields) with documented fallbacks, so no phase hard-depends on another except P2b-2 on P2b-1.
- Each phase flips its section of `docs/architecture/loop-engineering.md` from 📋/⚠️ to ✅ — that document is the roadmap's scoreboard.
