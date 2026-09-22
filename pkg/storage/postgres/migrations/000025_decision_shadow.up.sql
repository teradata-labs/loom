-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000025_decision_shadow.up.sql
-- Shadow comparisons for the typed decision layer (pkg/decision).
--
-- One row per question per routed request: what the decider answered and
-- what the call site's existing mechanism answered for the same input. The
-- `loom decision report` command reads this table to compute agreement,
-- calibration error and confusion per site; a site's band is set from that
-- report, never from vendor defaults. Nothing here is sent to a vendor.
--
-- Tenant-scoped like the other per-user tables: user_id defaults to
-- 'default-user' and the RLS policy follows task_boards (000011).

CREATE TABLE IF NOT EXISTS decision_shadow (
    id BIGSERIAL PRIMARY KEY,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    site TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    question_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    candidate_answer TEXT NOT NULL DEFAULT '',
    candidate_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    candidate_probabilities_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    reference_answer TEXT NOT NULL DEFAULT '',
    reference_source TEXT NOT NULL DEFAULT '',
    latency_ms BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT 'default-user'
);

CREATE INDEX IF NOT EXISTS idx_decision_shadow_site_time ON decision_shadow(site, recorded_at);
CREATE INDEX IF NOT EXISTS idx_decision_shadow_user ON decision_shadow(user_id);

ALTER TABLE decision_shadow ENABLE ROW LEVEL SECURITY;
CREATE POLICY decision_shadow_user_isolation ON decision_shadow
    USING (user_id = current_setting('app.current_user_id', true)
        OR current_setting('app.current_user_id', true) = ''
        OR current_setting('app.current_user_id', true) IS NULL)
    WITH CHECK (user_id = current_setting('app.current_user_id', true)
        OR current_setting('app.current_user_id', true) = ''
        OR current_setting('app.current_user_id', true) IS NULL);
