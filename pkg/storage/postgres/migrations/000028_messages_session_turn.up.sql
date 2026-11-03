-- A row's turn and its turn's origin are derived at insert from the session's
-- latest turn, and a rewind selects by turn: both read by (session_id, turn).
CREATE INDEX IF NOT EXISTS idx_messages_session_turn ON messages(session_id, turn);
