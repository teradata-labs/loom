-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000018_tool_outcomes_policy_denied.up.sql
-- Stop counting permission denials as tool failures in the analytics.
--
-- A denial from the permission checker (tool hard-disabled, or approval required
-- with no callback wired up) is an intentional policy decision, not a tool that
-- ran and failed. The original tool_outcomes view (000014) inferred failure from
-- `error IS NOT NULL`, so every denial inflated failure_count and dominated the
-- "top failure reasons" dashboard. The agent now hides such tools from the model
-- so these are rare, but a model can still emit a call to a tool it shouldn't, so
-- the view must classify the residual honestly rather than miscount it.
--
-- Recreate the view with a dedicated policy_denied_count and a failure_count that
-- excludes denials. Existing columns are preserved; failure_count is narrowed.

DROP VIEW IF EXISTS tool_outcomes;

CREATE VIEW tool_outcomes WITH (security_invoker = true) AS
SELECT
    (timestamp AT TIME ZONE 'UTC')::date                                       AS day,
    tool_name,
    COUNT(*) FILTER (WHERE error IS NULL)                                      AS success_count,
    -- Intentional policy decisions, not execution failures. Matches the
    -- permission checker's denial messages (pkg/shuttle/permission_checker.go)
    -- and the executor's permission_denied error code (pkg/shuttle/executor.go).
    COUNT(*) FILTER (
        WHERE error ILIKE '%disabled by configuration%'
           OR error ILIKE '%requires user approval%'
           OR error ILIKE '%permission_denied%'
    )                                                                          AS policy_denied_count,
    -- Genuine execution failures: errored AND not a policy denial.
    COUNT(*) FILTER (
        WHERE error IS NOT NULL
          AND error NOT ILIKE '%disabled by configuration%'
          AND error NOT ILIKE '%requires user approval%'
          AND error NOT ILIKE '%permission_denied%'
    )                                                                          AS failure_count,
    COUNT(*) FILTER (WHERE error ILIKE '%circuit%' OR error ILIKE '%breaker%') AS circuit_broken_estimate,
    ROUND(AVG(execution_time_ms))::int                                         AS avg_execution_time_ms,
    ROUND(AVG(execution_time_ms) FILTER (WHERE error IS NULL))::int            AS avg_success_time_ms
FROM tool_executions
WHERE deleted_at IS NULL
GROUP BY 1, 2;
