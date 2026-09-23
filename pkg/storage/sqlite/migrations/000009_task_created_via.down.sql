-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000009_task_created_via.down.sql
-- The column is LEFT IN PLACE, following the convention in 000005, 000006, and
-- 000008: the CGO build links go-sqlcipher (SQLite 3.33.0), and
-- `ALTER TABLE ... DROP COLUMN` was not added until 3.35.0, so dropping a
-- column fails with a syntax error on the default build.
--
-- created_via is NOT NULL DEFAULT '' with no index, so leaving it is inert.

-- Intentionally empty.
SELECT 1;
