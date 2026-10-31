-- The effort an LLM call was made at, the cache write split by how long the
-- entry lives, and the part of the output spent thinking, on the message the
-- call produced; '' and 0 where the provider reported none and on rows
-- written before they were recorded.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS effort                 TEXT   NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_1h_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_5m_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS thinking_tokens        BIGINT NOT NULL DEFAULT 0;
