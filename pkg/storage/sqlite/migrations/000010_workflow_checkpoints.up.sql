-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--	http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- 000010_workflow_checkpoints.up.sql
-- Durable storage for WorkflowCheckpoint (proto/loom/v1/orchestration.proto),
-- mirroring the Postgres 000025_workflow_checkpoints migration. SQLite is
-- single-tenant, so no user_id/RLS equivalent is added here.

CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    workflow_id        TEXT PRIMARY KEY,
    checkpoint_version INTEGER NOT NULL,
    checkpoint_proto   BLOB NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);
