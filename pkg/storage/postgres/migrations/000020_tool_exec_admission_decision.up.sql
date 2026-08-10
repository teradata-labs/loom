-- 000020_tool_exec_admission_decision.up.sql
-- Add the persisted admission verdict to tool_executions (HLD §5.6),
-- distinct from the run outcome. The column carries two populations:
--   * a call matched by an audit binding persists that binding's decision
--     ("allow"|"deny"|"ask");
--   * ANY denied call persists "deny", audited or not — a refused call must
--     never read back as an ordinary failure (000023's outcome view keys on
--     this).
-- NULL therefore means "not audited AND not denied" (ungoverned/legacy rows),
-- not "not audited" alone; an audit-coverage query (SC-004) cannot count
-- non-NULL rows — it must exclude unaudited denials or count via the audit
-- log. Nullable, no default. user_id RLS is inherited unchanged.
ALTER TABLE tool_executions
    ADD COLUMN IF NOT EXISTS admission_decision TEXT;
