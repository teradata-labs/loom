-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000019_message_context_class.up.sql
-- Structural retention class for context pressure management: narrative
-- (default, the SQL NULL / empty-string value), charter, ledger, or
-- ballast. Admission (wrapping), the valve (yellow-zone eviction), and fold
-- (red-zone partitioning) key off this value rather than message age or
-- role. Nullable so rows written before this column existed reclassify
-- on restore by the same structural rules applied at construction.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS context_class TEXT;
CREATE INDEX IF NOT EXISTS idx_messages_context_class ON messages(context_class);
