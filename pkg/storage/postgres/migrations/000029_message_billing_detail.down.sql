ALTER TABLE messages DROP COLUMN IF EXISTS thinking_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_5m_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS cache_create_1h_tokens;
ALTER TABLE messages DROP COLUMN IF EXISTS effort;
