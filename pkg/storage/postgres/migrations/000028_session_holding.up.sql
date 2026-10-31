-- Routing: which run currently has a conversation.
-- '' means nobody else does, which is every existing row, so this changes no
-- behaviour until an agent is first handed a conversation.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS holding TEXT NOT NULL DEFAULT '';

-- Loop attribution: the loop run that owns this session. Phase-agent sessions
-- of a run all share the same value. '' means the session was not created for
-- a loop (a person opened it directly, or a non-loop caller made it).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS run_id TEXT NOT NULL DEFAULT '';
