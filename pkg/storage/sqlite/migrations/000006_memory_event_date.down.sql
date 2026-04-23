-- 000004_memory_event_date.down.sql
-- SQLite doesn't support DROP COLUMN before 3.35.0, and even when it does
-- the rebuild is expensive. Matching the precedent set by migration 000003:
-- this is a no-op since the new columns are nullable and harmless if
-- application code stops reading them.

SELECT 1;
