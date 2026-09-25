-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000011_decision_shadow_error.down.sql
-- The bundled SQLite predates ALTER TABLE ... DROP COLUMN, so, like the other
-- column additions in this directory (000006, 000009), the column stays; the
-- store's INSERT names its columns and the default is '', so the older code
-- keeps working against the wider table.
SELECT 1;
