-- Copyright 2026 Teradata
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.

-- 000025_workflow_checkpoints.up.sql
-- Durable storage for WorkflowCheckpoint (proto/loom/v1/orchestration.proto),
-- the stage-boundary HITL-gate suspension record. Previously the CLI
-- (cmd/looms/cmd_workflow.go writeCheckpointAndPrintGate) wrote this only to
-- a local *.checkpoint.pb file, so a suspended workflow's resume state did
-- not survive a server restart unless the caller separately persisted that
-- file. One row per workflow_id: a new suspension replaces the last one, a
-- workflow has at most one live pending gate at a time.

CREATE TABLE IF NOT EXISTS workflow_checkpoints (
    workflow_id        TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL,
    checkpoint_version INTEGER NOT NULL,
    checkpoint_proto   BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE workflow_checkpoints ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS workflow_checkpoints_user_isolation ON workflow_checkpoints;
CREATE POLICY workflow_checkpoints_user_isolation ON workflow_checkpoints
    USING (user_id = loom_current_user_id())
    WITH CHECK (user_id = loom_current_user_id());
