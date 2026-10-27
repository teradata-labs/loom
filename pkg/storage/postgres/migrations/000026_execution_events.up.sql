-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000026_execution_events.up.sql
-- Durable, tool-call-granularity execution log (issue #229). Records
-- write-ahead intent/result event pairs for LLM calls and tool calls so a
-- crashed conversation loop can resume without re-executing a call whose
-- outcome is already known, and without re-spending on an LLM call whose
-- response was never durably persisted.
--
-- Correlation key is (session_id, step_index, batch_position), NOT
-- tool_use_id: ToolCall.ID is not reliably unique across providers (Ollama
-- can omit it; Gemini reuses the function name as the ID for parallel calls
-- to the same tool, colliding within one batch — see pkg/agent/park.go:252-258
-- and its own verifyItemBinding, which had to stop trusting ID alone for the
-- same reason). tool_use_id is kept as a nullable, auxiliary column for
-- human debugging only.
--
-- step_index is a separate, event-log-only counter — one per LLM call (one
-- per runConversationLoop iteration) — deliberately NOT the same as the
-- persisted Message.Turn, which increments only once per new user message
-- and can span many loop iterations. See the design doc's "step_index vs.
-- Message.Turn" section.

CREATE TABLE execution_events (
    id             BIGSERIAL PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    step_index     INTEGER NOT NULL,
    batch_position INTEGER NOT NULL DEFAULT 0,
    event_type     TEXT NOT NULL CHECK (event_type IN (
                       'llm_call_intent', 'llm_call_result',
                       'tool_call_intent', 'tool_call_result'
                   )),
    tool_use_id    TEXT,
    payload        JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, step_index, batch_position, event_type)
);

CREATE INDEX idx_execution_events_session ON execution_events(session_id, step_index, batch_position);

-- Resume-claim columns: a session-level CAS lock so two ResumeSession
-- attempts (an operator retry, or two server replicas) can never both
-- dispatch the same unresolved call. See pkg/agent/event_log.go
-- ClaimSessionResume / claimParkedRequest (park.go:310) for the analogous
-- HITL-side pattern.
ALTER TABLE sessions ADD COLUMN resume_claimed_by TEXT;
ALTER TABLE sessions ADD COLUMN resume_claim_token TEXT;
ALTER TABLE sessions ADD COLUMN resume_claimed_at TIMESTAMPTZ;
