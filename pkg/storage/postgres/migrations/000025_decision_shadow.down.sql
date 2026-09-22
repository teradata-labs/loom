-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000025_decision_shadow.down.sql

DROP POLICY IF EXISTS decision_shadow_user_isolation ON decision_shadow;
DROP INDEX IF EXISTS idx_decision_shadow_user;
DROP INDEX IF EXISTS idx_decision_shadow_site_time;
DROP TABLE IF EXISTS decision_shadow;
