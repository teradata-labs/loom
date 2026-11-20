-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000032_messages_session_turn.up.sql
-- A row's turn and its turn's origin are derived at insert from the session's
-- latest turn, and a rewind selects by turn: both read by (session_id, turn).
CREATE INDEX IF NOT EXISTS idx_messages_session_turn ON messages(session_id, turn);
