-- Rollback migration 009: Remove name column from sessions table
ALTER TABLE sessions DROP COLUMN IF EXISTS name;
