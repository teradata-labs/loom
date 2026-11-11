-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000030_message_billing.down.sql

ALTER TABLE messages DROP COLUMN IF EXISTS effort;
ALTER TABLE messages DROP COLUMN IF EXISTS model;
ALTER TABLE messages DROP COLUMN IF EXISTS thinking_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_5m_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_1h_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_read_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS output_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS input_tokens;
