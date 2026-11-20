-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000030_message_billing.up.sql
-- Every billed message carries what it cost: the model that answered and at
-- what effort, input and output tokens, cache reads, cache writes split by TTL
-- (1h/5m), and thinking tokens, on the assistant row the LLM call produced.
-- SUM(cost_usd) over a session's rows is then what the session cost, and each
-- row says why.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS input_tokens           BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS output_tokens          BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_read_tokens      BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_tokens    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_1h_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_5m_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS thinking_tokens        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS model                  TEXT   NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN IF NOT EXISTS effort                 TEXT   NOT NULL DEFAULT '';
