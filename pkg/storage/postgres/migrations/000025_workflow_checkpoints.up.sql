-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000025_workflow_checkpoints.up.sql
-- Durable storage for WorkflowCheckpoint (proto/loom/v1/orchestration.proto),
-- the stage-boundary HITL-gate suspension record. Previously the CLI
-- (cmd/looms/cmd_workflow.go writeCheckpointAndPrintGate) wrote this only to
-- a local *.checkpoint.pb file, so a suspended workflow could be resumed only
-- on the machine that still had that file. The CLI now also saves the record
-- here, and `looms workflow resume --workflow-id` reads it back; `looms serve`
-- does not read this table. The resume deletes the row once the workflow
-- completes or is rejected. One row per (user_id, workflow_id): a new suspension replaces the
-- last one, so a workflow has at most one live pending gate at a time. The
-- key includes user_id because workflow IDs are often set by hand in the
-- workflow YAML, so two tenants can share one; keyed on workflow_id alone, one
-- tenant's upsert would hit the other tenant's row.
--
-- FORCE ROW LEVEL SECURITY matches the other per-user tables (000007): without
-- it, a table owner that is not a superuser bypasses the policy.

CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    user_id            TEXT NOT NULL,
    workflow_id        TEXT NOT NULL,
    checkpoint_version INTEGER NOT NULL,
    checkpoint_proto   BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, workflow_id)
);

ALTER TABLE workflow_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_checkpoints FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS workflow_checkpoints_user_isolation ON workflow_checkpoints;
CREATE POLICY workflow_checkpoints_user_isolation ON workflow_checkpoints
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());
