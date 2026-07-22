-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000014_analytics_views.up.sql
-- Read-only reporting views over Loom's runtime telemetry, intended to be
-- consumed by an external analytics tool (e.g. Dreambase) reading the same
-- Supabase/Postgres database.
--
-- POSTGRES-ONLY: these views use PG-specific syntax (aggregate FILTER,
-- `security_invoker`, `AT TIME ZONE`) and have no SQLite parallel. SQLite is
-- the local/dev backend; the analytics dashboards target Postgres/Supabase.
--
-- REQUIRES PostgreSQL 15+ for `WITH (security_invoker = true)`. Loom storage on
-- Supabase is verified on PostgreSQL 17.6.
--
-- SECURITY: every view is `security_invoker = true` so base-table RLS is
-- evaluated against the *querying* role and its `app.current_user_id` session
-- GUC, never the view owner's. This guarantees a view can never expose more
-- than the caller could already read directly. NOTE: Loom's base-table RLS on
-- sessions/messages (000007) is strict — when `app.current_user_id` is unset,
-- `current_setting(..., true)` is NULL so no rows match (deny-by-default); a
-- connection without that GUC therefore sees nothing through these views. The
-- views only avoid *elevating* privilege; they never loosen it.

-- View 1: cost and token totals per agent, per role, per UTC day.
-- Sourced from per-message `cost_usd` / `token_count` (000001).
CREATE VIEW cost_per_agent_day WITH (security_invoker = true) AS
SELECT
    (timestamp AT TIME ZONE 'UTC')::date         AS day,
    COALESCE(agent_id, '(unknown)')              AS agent_id,
    role,
    COUNT(DISTINCT session_id)                   AS sessions,
    COUNT(*)                                      AS messages,
    COALESCE(SUM(token_count), 0)::bigint         AS total_tokens,
    COALESCE(SUM(cost_usd), 0)::numeric(18, 8)    AS total_cost_usd
FROM messages
WHERE deleted_at IS NULL
GROUP BY 1, 2, 3;

-- View 2: tool execution outcomes per tool, per UTC day.
-- HONEST CAVEAT: `tool_executions` has no success/status/circuit-broken column.
-- Success is inferred from `error IS NULL`. `circuit_broken_estimate` is a text
-- heuristic only (the schema cannot distinguish a circuit-breaker trip from any
-- other failure); treat it as an estimate, not an authoritative count.
CREATE VIEW tool_outcomes WITH (security_invoker = true) AS
SELECT
    (timestamp AT TIME ZONE 'UTC')::date                                      AS day,
    tool_name,
    COUNT(*) FILTER (WHERE error IS NULL)                                     AS success_count,
    COUNT(*) FILTER (WHERE error IS NOT NULL)                                 AS failure_count,
    COUNT(*) FILTER (WHERE error ILIKE '%circuit%' OR error ILIKE '%breaker%') AS circuit_broken_estimate,
    ROUND(AVG(execution_time_ms))::int                                        AS avg_execution_time_ms,
    ROUND(AVG(execution_time_ms) FILTER (WHERE error IS NULL))::int           AS avg_success_time_ms
FROM tool_executions
WHERE deleted_at IS NULL
GROUP BY 1, 2;

-- View 3: kanban task throughput and work-in-progress per UTC day.
-- `created_count` / `closed_count` come from `created_at` / `closed_at`.
-- HONEST CAVEAT: open/wip/blocked/done are bucketed by `updated_at` (the day a
-- task last changed and currently sits in that state), NOT a point-in-time daily
-- closing balance. A precise daily WIP balance would require replaying
-- `task_history`; that is intentionally out of scope here.
-- status ints (proto/loom/v1/task.proto TaskStatus): 1 OPEN, 2 IN_PROGRESS,
-- 3 BLOCKED, 4 DONE, 5 DEFERRED, 6 CANCELLED.
CREATE VIEW task_throughput WITH (security_invoker = true) AS
WITH created AS (
    SELECT (created_at AT TIME ZONE 'UTC')::date AS day, COUNT(*) AS created_count
    FROM tasks
    WHERE deleted_at IS NULL
    GROUP BY 1
),
closed AS (
    SELECT (closed_at AT TIME ZONE 'UTC')::date AS day, COUNT(*) AS closed_count
    FROM tasks
    WHERE deleted_at IS NULL AND closed_at IS NOT NULL
    GROUP BY 1
),
state AS (
    SELECT
        (updated_at AT TIME ZONE 'UTC')::date AS day,
        COUNT(*) FILTER (WHERE status = 1) AS open_count,
        COUNT(*) FILTER (WHERE status = 2) AS wip_count,
        COUNT(*) FILTER (WHERE status = 3) AS blocked_count,
        COUNT(*) FILTER (WHERE status = 4) AS done_count
    FROM tasks
    WHERE deleted_at IS NULL
    GROUP BY 1
)
SELECT
    COALESCE(c.day, cl.day, s.day) AS day,
    COALESCE(c.created_count, 0)   AS created_count,
    COALESCE(cl.closed_count, 0)   AS closed_count,
    COALESCE(s.open_count, 0)      AS open_count,
    COALESCE(s.wip_count, 0)       AS wip_count,
    COALESCE(s.blocked_count, 0)   AS blocked_count,
    COALESCE(s.done_count, 0)      AS done_count
FROM created c
FULL OUTER JOIN closed cl ON cl.day = c.day
FULL OUTER JOIN state s ON s.day = COALESCE(c.day, cl.day);
