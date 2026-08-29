-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000024_task_attribution.up.sql
-- Joins the existing durable records of "what happened" to the task they
-- happened for, and records how the task came to exist.
--
-- See docs/architecture/task-activity.md. Postgres can do this in one migration
-- where SQLite cannot, because ADD COLUMN IF NOT EXISTS exists here — but the
-- table-existence guard is still required: a pre-migration database may have
-- had 000001 baselined rather than executed.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS created_via TEXT NOT NULL DEFAULT '';

-- Tool calls, tool results, and agent narrative already live in `messages`.
-- Only the join key was missing.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables
               WHERE table_schema = current_schema() AND table_name = 'messages') THEN
        ALTER TABLE messages ADD COLUMN IF NOT EXISTS task_id TEXT;
        CREATE INDEX IF NOT EXISTS idx_messages_task
            ON messages(task_id, timestamp) WHERE task_id IS NOT NULL;
    END IF;
END $$;

-- Human-in-the-loop exchanges. This join is what lets a pending approval be
-- surfaced on the task it blocks.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables
               WHERE table_schema = current_schema() AND table_name = 'human_requests') THEN
        ALTER TABLE human_requests ADD COLUMN IF NOT EXISTS task_id TEXT;
        CREATE INDEX IF NOT EXISTS idx_human_requests_task
            ON human_requests(task_id, created_at) WHERE task_id IS NOT NULL;
    END IF;
END $$;
