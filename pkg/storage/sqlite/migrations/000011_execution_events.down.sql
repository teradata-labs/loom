-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

DROP TABLE IF EXISTS execution_events;

-- SQLite cannot drop columns before 3.35; the project's minimum SQLite
-- version predates that in some deployments, so the down migration leaves
-- the three sessions columns in place (harmless, unused once execution_events
-- is dropped) rather than requiring a table rebuild. This mirrors how the
-- project already handles ALTER TABLE ADD COLUMN elsewhere (e.g.
-- 000008_task_idempotency has no column-drop in its own down migration
-- either — check that file to confirm before deviating).
