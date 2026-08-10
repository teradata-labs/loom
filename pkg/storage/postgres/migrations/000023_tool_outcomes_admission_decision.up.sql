-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000023_tool_outcomes_admission_decision.up.sql
-- Classify policy denials by the authoritative column, not text heuristics.
--
-- 000020 added tool_executions.admission_decision — the chain's own verdict —
-- but the tool_outcomes view still classified denials by grepping the error
-- text for three literals, none of which match the admission chain's deny
-- reasons ("denied by denylist", "call not in approved set", "approval timed
-- out", a human's verbatim rejection note, …). Every chain denial therefore
-- counted as a tool failure — the exact miscount 000018 fixed, re-opened — and
-- a rejection note containing "breaker" even inflated circuit_broken_estimate.
--
-- Recreate the view with admission_decision = 'deny' as the primary
-- policy-denied predicate. The text arms stay as a fallback for legacy rows
-- (NULL admission_decision: pre-column rows, and denials stamped before the
-- decision rode every deny) and for the checker's pre-chain messages.

DROP VIEW IF EXISTS tool_outcomes;

CREATE VIEW tool_outcomes WITH (security_invoker = true) AS
SELECT
    (timestamp AT TIME ZONE 'UTC')::date                                       AS day,
    tool_name,
    COUNT(*) FILTER (WHERE error IS NULL)                                      AS success_count,
    -- Intentional policy decisions, not execution failures. The admission
    -- chain's own verdict is authoritative; the text arms cover legacy rows.
    COUNT(*) FILTER (
        WHERE admission_decision = 'deny'
           OR error ILIKE '%disabled by configuration%'
           OR error ILIKE '%requires user approval%'
           OR error ILIKE '%permission_denied%'
    )                                                                          AS policy_denied_count,
    -- Genuine execution failures: errored AND not a policy denial.
    COUNT(*) FILTER (
        WHERE error IS NOT NULL
          AND (admission_decision IS NULL OR admission_decision <> 'deny')
          AND error NOT ILIKE '%disabled by configuration%'
          AND error NOT ILIKE '%requires user approval%'
          AND error NOT ILIKE '%permission_denied%'
    )                                                                          AS failure_count,
    -- Circuit-breaker estimate: excludes denials so a human's rejection note
    -- mentioning a breaker cannot inflate it.
    COUNT(*) FILTER (
        WHERE (error ILIKE '%circuit%' OR error ILIKE '%breaker%')
          AND (admission_decision IS NULL OR admission_decision <> 'deny')
    )                                                                          AS circuit_broken_estimate,
    ROUND(AVG(execution_time_ms))::int                                         AS avg_execution_time_ms,
    ROUND(AVG(execution_time_ms) FILTER (WHERE error IS NULL))::int            AS avg_success_time_ms
FROM tool_executions
WHERE deleted_at IS NULL
GROUP BY 1, 2;
