-- Accounting: per-call token breakdown on messages. Every cost/token roll-up
-- in the product is a SUM over these columns; there is no other authoritative
-- source.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS input_tokens        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS output_tokens       BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_read_tokens   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_tokens BIGINT NOT NULL DEFAULT 0;
