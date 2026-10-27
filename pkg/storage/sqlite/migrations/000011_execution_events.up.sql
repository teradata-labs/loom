-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000011_execution_events.up.sql
-- SQLite counterpart of postgres/migrations/000026_execution_events.up.sql.
-- See that file's header for the full rationale.

CREATE TABLE execution_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    step_index     INTEGER NOT NULL,
    batch_position INTEGER NOT NULL DEFAULT 0,
    event_type     TEXT NOT NULL CHECK (event_type IN (
                       'llm_call_intent', 'llm_call_result',
                       'tool_call_intent', 'tool_call_result'
                   )),
    tool_use_id    TEXT,
    payload        TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (session_id, step_index, batch_position, event_type)
);

CREATE INDEX idx_execution_events_session ON execution_events(session_id, step_index, batch_position);

ALTER TABLE sessions ADD COLUMN resume_claimed_by TEXT;
ALTER TABLE sessions ADD COLUMN resume_claim_token TEXT;
ALTER TABLE sessions ADD COLUMN resume_claimed_at TEXT;
