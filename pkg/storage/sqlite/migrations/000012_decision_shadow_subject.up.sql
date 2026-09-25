-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000012_decision_shadow_subject.up.sql
-- What the question was about (a memory's source session, a tool name), so
-- shadow rows can be graded against an external truth such as a benchmark's
-- evidence sessions. Never content.

ALTER TABLE decision_shadow ADD COLUMN subject TEXT NOT NULL DEFAULT '';
