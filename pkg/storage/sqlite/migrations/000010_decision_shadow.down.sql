-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000010_decision_shadow.down.sql

DROP INDEX IF EXISTS idx_decision_shadow_site_time;
DROP TABLE IF EXISTS decision_shadow;
