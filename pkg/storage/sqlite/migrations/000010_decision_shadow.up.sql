-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000010_decision_shadow.up.sql
-- Shadow comparisons for the typed decision layer (pkg/decision).
--
-- One row per question per routed request: what the decider answered and
-- what the call site's existing mechanism answered for the same input. The
-- `loom decision report` command reads this table to compute agreement,
-- calibration error and confusion per site; a site's band is set from that
-- report, never from vendor defaults. Nothing here is sent to a vendor.

CREATE TABLE IF NOT EXISTS decision_shadow (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at INTEGER NOT NULL,                       -- unix milliseconds
    site TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    question_id TEXT NOT NULL,
    kind TEXT NOT NULL,                                 -- noul | choice | score
    candidate_answer TEXT NOT NULL DEFAULT '',
    candidate_confidence REAL NOT NULL DEFAULT 0,
    candidate_probabilities_json TEXT NOT NULL DEFAULT '{}',
    reference_answer TEXT NOT NULL DEFAULT '',
    reference_source TEXT NOT NULL DEFAULT '',
    latency_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT ''                       -- DecisionPath enum name
);

CREATE INDEX IF NOT EXISTS idx_decision_shadow_site_time ON decision_shadow(site, recorded_at);
