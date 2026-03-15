-- Migration 000010: Add permission_mode to sessions table
-- Adds runtime permission mode control for Canvas AI integration
-- Allows sessions to switch between ASK_BEFORE/AUTO_ACCEPT/PLAN modes dynamically

ALTER TABLE sessions ADD COLUMN permission_mode INTEGER DEFAULT 0;

-- permission_mode values:
-- 0 = PERMISSION_MODE_UNSPECIFIED (use agent config)
-- 1 = PERMISSION_MODE_ASK_BEFORE (ask before each tool)
-- 2 = PERMISSION_MODE_AUTO_ACCEPT (execute automatically, YOLO mode)
-- 3 = PERMISSION_MODE_PLAN (create plan, wait for approval)

COMMENT ON COLUMN sessions.permission_mode IS 'Permission mode: 0=UNSPECIFIED, 1=ASK_BEFORE, 2=AUTO_ACCEPT, 3=PLAN';

-- Index for filtering sessions by permission mode
CREATE INDEX IF NOT EXISTS idx_sessions_permission_mode ON sessions(permission_mode);
