-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000026_execution_events.up.sql
-- Durable, tool-call-granularity execution log (issue #229). Records
-- write-ahead intent/result event pairs for LLM calls and tool calls, so a
-- crashed conversation loop can resume without re-running a call whose
-- outcome is already known.
--
-- Correlation key is (session_id, step_index, batch_position), not
-- tool_use_id: ToolCall.ID is not reliably unique across providers (Ollama
-- can omit it; Gemini reuses the function name as the ID for parallel calls
-- to the same tool). tool_use_id is kept for debugging only.
--
-- step_index counts LLM calls (one per conversation-loop iteration). It is
-- not Message.Turn, which advances once per user message and can span many
-- loop iterations.
--
-- Both tables are per-user, like the tables in 000007: RLS is enabled and
-- forced (a non-superuser table owner otherwise bypasses it), and WITH CHECK
-- also requires the session to be visible to the caller, so a tenant cannot
-- attach events or a lease to another tenant's session.

CREATE TABLE execution_events (
    id             BIGSERIAL PRIMARY KEY,
    user_id        TEXT NOT NULL,
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

ALTER TABLE execution_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE execution_events FORCE ROW LEVEL SECURITY;

CREATE POLICY execution_events_user_isolation ON execution_events
    USING (user_id = loom_current_user_id())
    WITH CHECK (
        user_id = loom_current_user_id()
        AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = execution_events.session_id)
    );

-- One lease per session: the process currently driving the session's
-- conversation loop (a live turn or a crash resume). Held for a TTL and
-- renewed while the loop runs; expires_at uses the database clock.
CREATE TABLE execution_leases (
    user_id    TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    holder     TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, session_id)
);

ALTER TABLE execution_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE execution_leases FORCE ROW LEVEL SECURITY;

CREATE POLICY execution_leases_user_isolation ON execution_leases
    USING (user_id = loom_current_user_id())
    WITH CHECK (
        user_id = loom_current_user_id()
        AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = execution_leases.session_id)
    );
