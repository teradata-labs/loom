-- The model that answered, on a message an LLM call billed; '' on every
-- other message and on rows written before it was recorded.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
