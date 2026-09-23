-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000026_decision_shadow_error.up.sql
-- Decider error text for ERROR-path rows, so they can be triaged.

ALTER TABLE decision_shadow ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT '';
