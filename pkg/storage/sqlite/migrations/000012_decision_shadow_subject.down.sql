-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000012_decision_shadow_subject.down.sql
-- The bundled SQLite predates DROP COLUMN; the column stays (see 000011).
-- Running this migration's up again after this down fails with "duplicate
-- column name", because the column is still there.
SELECT 1;
