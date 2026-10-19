ALTER TABLE messages DROP COLUMN IF EXISTS input_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS output_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_read_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_tokens;
ALTER TABLE sessions DROP COLUMN IF EXISTS run_id;
