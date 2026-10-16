-- Routing: which run currently has a conversation.
-- '' means nobody else does, which is every existing row, so this changes no
-- behaviour until an agent is first handed a conversation.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS holding TEXT NOT NULL DEFAULT '';
