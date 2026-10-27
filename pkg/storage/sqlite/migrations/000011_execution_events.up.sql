-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000011_execution_events.up.sql
-- SQLite counterpart of postgres/migrations/000026_execution_events.up.sql;
-- see that file's header for the design.
--
-- SQLite has no row-level security, so the store scopes every query by
-- user_id itself (types.UserIDOrDefault, the rule SQLite sessions use).
-- user_id is part of both keys so one user's rows can never collide with,
-- or block, another user's.
--
-- expires_at is a julianday() value so lease expiry compares numbers on the
-- database clock rather than formatted time strings.

CREATE TABLE execution_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id        TEXT NOT NULL,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    step_index     INTEGER NOT NULL,
    batch_position INTEGER NOT NULL DEFAULT 0,
    event_type     TEXT NOT NULL CHECK (event_type IN (
                       'llm_call_intent', 'llm_call_result',
                       'tool_call_intent', 'tool_call_result'
                   )),
    tool_use_id    TEXT,
    payload        TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (user_id, session_id, step_index, batch_position, event_type)
);

CREATE INDEX idx_execution_events_session ON execution_events(user_id, session_id, step_index, batch_position);

CREATE TABLE execution_leases (
    user_id    TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    holder     TEXT NOT NULL,
    expires_at REAL NOT NULL,
    PRIMARY KEY (user_id, session_id)
);
