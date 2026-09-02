-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000024_task_attribution.down.sql

DROP INDEX IF EXISTS idx_human_requests_task;
DROP INDEX IF EXISTS idx_messages_task;

ALTER TABLE tasks DROP COLUMN IF EXISTS created_via;
ALTER TABLE human_requests DROP COLUMN IF EXISTS task_id;
ALTER TABLE messages DROP COLUMN IF EXISTS task_id;
