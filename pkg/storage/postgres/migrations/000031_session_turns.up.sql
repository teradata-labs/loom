-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000031_session_turns.up.sql
-- turns is how many turns a session has had. One turn at a time runs in a
-- session (the session lease); an agent object holding a copy of the session
-- from fewer turns than this reloads it. 0 is every existing row.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS turns BIGINT NOT NULL DEFAULT 0;
