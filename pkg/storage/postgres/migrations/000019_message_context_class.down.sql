-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000019_message_context_class.down.sql
-- Revert: drop the context_class column. The IF EXISTS on the index is
-- retained for cross-branch safety — if an older revision of the up
-- migration created idx_messages_context_class in a given database, the
-- down migration should still clean it up.

DROP INDEX IF EXISTS idx_messages_context_class;
ALTER TABLE messages DROP COLUMN IF EXISTS context_class;
