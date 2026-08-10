-- 000020_tool_exec_admission_decision.down.sql
ALTER TABLE tool_executions
    DROP COLUMN IF EXISTS admission_decision;
