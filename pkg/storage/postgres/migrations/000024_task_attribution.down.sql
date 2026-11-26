-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000024_task_attribution.down.sql

DROP INDEX IF EXISTS idx_human_requests_task;
DROP INDEX IF EXISTS idx_messages_task;

-- IF EXISTS on both levels: DROP COLUMN IF EXISTS guards the COLUMN only, so a
-- bare ALTER still fails when the TABLE is absent — which is exactly the shape
-- the up migration's guard admits (tasks present, messages/human_requests not).
-- golang-migrate runs this file in one transaction, so one failing line aborts
-- the whole rollback and schema_migrations stays at 24, wedging MigrateDown on
-- every retry with no route forward but manual SQL.
ALTER TABLE tasks DROP COLUMN IF EXISTS created_via;
ALTER TABLE IF EXISTS human_requests DROP COLUMN IF EXISTS task_id;
ALTER TABLE IF EXISTS messages DROP COLUMN IF EXISTS task_id;
