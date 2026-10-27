-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

ALTER TABLE sessions DROP COLUMN IF EXISTS resume_claimed_by;
ALTER TABLE sessions DROP COLUMN IF EXISTS resume_claim_token;
ALTER TABLE sessions DROP COLUMN IF EXISTS resume_claimed_at;

DROP TABLE IF EXISTS execution_events;
