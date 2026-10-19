-- Accounting rewrite: per-turn token breakdown on messages, loop attribution
-- on sessions. Every cost/token roll-up in the product is a SUM over these
-- columns; there is no other authoritative source.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS input_tokens        BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS output_tokens       BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_read_tokens   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS cache_create_tokens BIGINT NOT NULL DEFAULT 0;

-- Loop attribution: the loop run that owns this session. Phase-agent sessions
-- of a run all share the same value. '' means the session was not created for
-- a loop (a person opened it directly, or a non-loop caller made it).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS run_id TEXT NOT NULL DEFAULT '';
