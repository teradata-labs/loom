-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000018_tool_outcomes_policy_denied.down.sql
-- Revert tool_outcomes to the 000014 form, where failure_count counts every
-- errored execution (including permission denials) and there is no
-- policy_denied_count column.

DROP VIEW IF EXISTS tool_outcomes;

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
