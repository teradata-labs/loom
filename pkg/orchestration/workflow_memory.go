// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/collaboration"
	"go.uber.org/zap"
)

// The executors stash full outputs in the WORKFLOW namespace of SharedMemory
// so a later agent can fetch what a bounded prompt cut. That namespace has no
// scoping of its own, and one store serves every workflow a looms server
// runs, so every executor-managed key carries the workflow id:
//
//	<workflow-id>-stage-N-output             pipeline / iterative stage output
//	<workflow-id>-branch-N-output            fork-join / parallel branch output
//	<workflow-id>-round-R-position-<agent>   debate position
//
// Keys that agents write themselves (shared_memory_write) are theirs; the
// prompts ask them to start such keys with the workflow id too, but nothing
// enforces it. The helpers here tell a managed key's workflow apart from the
// rest, so a checkpoint snapshot, a restart reset and the end-of-run cleanup
// never touch another workflow's stash.

// stageOutputKey is the SharedMemory key holding a pipeline stage's full
// output.
func stageOutputKey(workflowID string, stageNum int) string {
	return fmt.Sprintf("%s-stage-%d-output", workflowID, stageNum)
}

// managedKeyPatterns match the executor-managed keys; group 1 is the
// workflow id.
var managedKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(.+)-stage-\d+-output$`),
	regexp.MustCompile(`^(.+)-branch-\d+-output$`),
	regexp.MustCompile(`^(.+)-round-\d+-position-.+$`),
}

// managedKeyWorkflow returns the workflow id an executor-managed key belongs
// to, or "" for any other key: one an agent wrote, or a legacy unscoped
// stage-N-output.
func managedKeyWorkflow(key string) string {
	for _, re := range managedKeyPatterns {
		if m := re.FindStringSubmatch(key); m != nil {
			return m[1]
		}
	}
	return ""
}

// workflowOwnsKey reports whether key belongs to workflowID: an
// executor-managed key whose embedded id is workflowID, or any other key
// that starts with "<workflow-id>-", the prefix the prompt header asks agents
// to use for the keys they write. Keys without a workflow prefix belong to
// no run (on a shared store they are shared) and are never reset,
// snapshotted or deleted on a run's behalf.
func workflowOwnsKey(key, workflowID string) bool {
	if workflowID == "" {
		return false
	}
	if owner := managedKeyWorkflow(key); owner != "" {
		return owner == workflowID
	}
	return strings.HasPrefix(key, workflowID+"-")
}

// isStageOutputKey reports whether key is one of this workflow's stage
// output keys (re-seeded from checkpoint snapshots, so never snapshotted).
func isStageOutputKey(key, workflowID string) bool {
	return strings.HasPrefix(key, workflowID+"-stage-") && managedKeyPatterns[0].MatchString(key)
}

// sharedMemoryWriteHint is the line every SharedMemory header carries so an
// agent that writes its own keys keeps them with its workflow; one text for
// the pipeline, join and debate headers.
func sharedMemoryWriteHint(workflowID string) string {
	return collaboration.SharedMemoryWriteHint(workflowID)
}

// cleanupWorkflowMemory deletes one workflow's executor-managed keys once a
// run has returned, a HITL suspension included: the checkpoint carries every
// stage output and restoreWorkflowMemory re-seeds the keys from it on resume,
// so nothing in the store is needed across the pause, and a host that drops
// the checkpoint (the gRPC server has no resume path) would otherwise keep the
// stash for the life of the process. Agent-written keys and other workflows'
// keys are left alone. It runs on a context that survives the run's
// cancellation, bounded by a short timeout, and only logs on failure.
func (o *Orchestrator) cleanupWorkflowMemory(ctx context.Context, workflowID string) {
	if o.sharedMemory == nil || workflowID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	resp, err := o.sharedMemory.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
	})
	if err != nil {
		o.logger.Warn("workflow SharedMemory cleanup: failed to list keys; stash left in place",
			zap.String("workflow_id", workflowID), zap.Error(err))
		return
	}
	deleted := 0
	for _, key := range resp.Keys {
		if managedKeyWorkflow(key) != workflowID {
			continue
		}
		if _, err := o.sharedMemory.Delete(ctx, &loomv1.DeleteSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       key,
		}); err != nil {
			o.logger.Warn("workflow SharedMemory cleanup: failed to delete key",
				zap.String("workflow_id", workflowID), zap.String("key", key), zap.Error(err))
			continue
		}
		deleted++
	}
	if deleted > 0 {
		o.logger.Debug("workflow SharedMemory stash cleaned up",
			zap.String("workflow_id", workflowID), zap.Int("keys", deleted))
	}
}

// CleanupHandoffs implements collaboration.HandoffCleaner: a collaboration
// pattern that stored hand-off entries through BoundHandoff asks for them to
// be removed when its run ends.
func (o *Orchestrator) CleanupHandoffs(ctx context.Context, workflowID string) {
	o.cleanupWorkflowMemory(ctx, workflowID)
}
