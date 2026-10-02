-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000011_decision_shadow_error.up.sql
-- Decider error text for ERROR-path rows. Without it an ERROR row cannot be
-- triaged (the first campaign produced 256 of them with no reason on file).

ALTER TABLE decision_shadow ADD COLUMN error TEXT NOT NULL DEFAULT '';
