-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000027_decision_shadow_subject.down.sql

ALTER TABLE decision_shadow DROP COLUMN IF EXISTS subject;
