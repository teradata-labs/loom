-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000025_workflow_checkpoints.down.sql

DROP POLICY IF EXISTS workflow_checkpoints_user_isolation ON workflow_checkpoints;
DROP TABLE IF EXISTS workflow_checkpoints;
