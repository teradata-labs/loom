# Loom v1.1.0 Evaluation Feedback & Action Items

**Source:** Dan Bo, Pre-Sales Data Scientist, Teradata (Healthcare AI/ML & Analytics)
**Evaluation Date:** February 25, 2026
**Reviewed:** February 26, 2026
**Overall Verdict:** Recommended for Pre-Sales Use (with caveats)

---

## What Worked

- Installation successful (after troubleshooting)
- Weaver created custom agent (`td-analyzer`) from natural language and persisted to disk
- Healthcare analytics suite: 4 specialized agents + pipeline workflow created
- Agent functionality: 11s response time, quality score 82/100, actionable output
- MCP integration with real Teradata database: connected and queryable
- Pattern library recognized as valuable: 34 Teradata-specific patterns

---

## Issues & Action Items

### P0 — Output Token Circuit Breaker Fires Too Often

**Dan's Report:** Agents hit 8,192 token output limit 3x consecutively. Had to explicitly request "brief/concise" output to work around it.
**Other Reports:** Confirmed from Anthropic and OpenAI users independently.
**Assessment:** ⚠️ This is a **fundamental bug in circuit breaker logic**, not a UX or configuration issue.

**Root Cause (diagnosed):** See `docs/plans/circuit-breaker-diagnosis.md`

**Status:** ✅ Fixed in v1.2.0 — CB now only counts truncated tool calls as failures; text responses that hit `max_tokens` clear the counter. Threshold raised from 3 to 8 (configurable via `output_token_cb_threshold` in agent YAML). See `docs/plans/circuit-breaker-diagnosis.md` for the archived diagnosis.

---

### P1 — Multi-Agent Pipeline Timeout (1+ hour, 4-agent pipeline)

**Dan's Report:** A 4-agent `healthcare-analytics-pipeline` workflow never completed.
**Root Cause:** Unknown — reported as "shared memory or communication overhead."
**Assessment:** This could be a deadlock, resource exhaustion, or the output token CB firing silently inside pipeline steps.

**Action Items:**
- [ ] Reproduce with the healthcare pipeline config
- [ ] Run with `-race` to check for deadlocks
- [ ] Add per-step timeout to pipeline executor (`pkg/orchestration/pipeline_executor.go`) — note: `PipelineStage` proto has no `timeout_seconds` field yet
- [ ] Add pipeline-level logging: which agent, which step, how long each step took — note: per-stage spans exist (`pipeline.stage.N`) and `AgentResult.DurationMs` is tracked, but no zap log line per stage duration
- [x] Investigate whether CB fires inside pipeline steps and swallows the error — CB logic fixed in v1.2.0 (P0); text responses no longer trigger CB

---

### P2 — Setup Complexity (~2 hours to be operational)

**Issues reported:**
1. Port 60051 conflict with corporate webfilter — no detection or clear guidance
2. AWS Bedrock: access keys vs SSO confusion during quickstart
3. Multiple terminals required, unclear which does what

**Action Items:**
- [ ] Add port conflict detection at `looms serve` startup — print actionable message if 60051 is in use (📋 not yet implemented)
- [ ] Separate quickstart paths for Anthropic API vs AWS Bedrock (with auth method clearly called out)
- [ ] Add pre-flight check command: `loom doctor` that validates all dependencies and connectivity (📋 not yet implemented — no `doctor` subcommand exists)

---

### P3 — AWS Bedrock API Failure (end of session)

**Dan's Report:** `bedrock invocation failed` at end of testing. Unknown cause (credentials, rate limit, model access).
**Assessment:** Likely external, but error messaging is insufficient.

**Action Items:**
- [ ] Improve Bedrock error messages to distinguish (📋 not yet implemented — `pkg/llm/bedrock/` does not classify AWS error codes):
  - Expired credentials
  - Rate limiting / throttling
  - Model access not provisioned (wrong region, not enabled in console)
- [ ] Log the HTTP status code and AWS error code, not just the message

---

### P4 — Agent Discovery UX in `--thread` Mode

**Dan's Report:** `Ctrl+E` filter didn't show newly created agents when running with `--thread` flag. Required restart without `--thread`.

**Action Items:**
- [ ] Add a clear note to the TUI when running in `--thread` mode that agent browser is locked to that thread
- [ ] Consider refreshing the agent list dynamically (watch `~/.loom/agents/` for changes)
- [ ] Document `loom --thread <n>` vs `loom` (browse all) distinction prominently

---

### P5 — Agent Output Verbosity Defaults

**Dan's Report:** Healthcare agents default to comprehensive reports, hit token limits, require explicit prompting for brevity.
**Assessment:** This is partly configuration (Weaver-generated agents should have sensible defaults), partly a consequence of the CB bug (P0).

**Action Items:**
- [ ] Add `output_style: concise|balanced|comprehensive` field to agent config (📋 not in proto or config yet)
- [ ] Weaver should default to `balanced` unless user specifies a reporting use case
- [ ] Consider adding `max_output_tokens` as a first-class agent config field — currently exists on `ModelInfo` in `loom.proto` but not on `AgentConfig` in `agent_config.proto`; `max_tokens` on `LLMConfig` controls overall token limit but is not output-specific

---

## Positive Signals Worth Noting

- Healthcare suite created in minutes, not days — core value proposition validated
- Pattern library (34 Teradata patterns) recognized immediately as a differentiator
- Weaver's natural language → agent pipeline working end-to-end
- MCP + real Teradata DB connection confirmed working
- Dan's comparison section: correctly identifies why Loom differs from LangChain/AutoGPT

---

## Pre-Sales Readiness Assessment

| Area | Status | Blocker? |
|------|--------|----------|
| Weaver (agent creation) | ✅ Works | No |
| Pattern library | ✅ Works | No |
| MCP / Teradata integration | ✅ Works | No |
| Single agent queries | ✅ Works | No |
| Output token CB | ✅ Fixed (v1.2.0) | No — logic corrected, threshold configurable |
| Multi-agent pipelines | ⚠️ Unstable | Yes for demos — CB fix (v1.2.0) may have resolved root cause but pipeline timeout not independently verified |
| Setup / onboarding | ⚠️ Rough | Yes for customer-facing demos |
| Error messages | ⚠️ Poor | No (confusing but not blocking) |
