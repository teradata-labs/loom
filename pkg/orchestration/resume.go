// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// ResumeWorkflow continues a workflow that suspended at a HITL gate.
//
// The host supplies the SAME workflow pattern the run was started with (the
// checkpoint's config fingerprint is verified against it — a mismatch fails
// fast rather than executing an edited, unreviewed definition), the persisted
// WorkflowCheckpoint, and the human's GateDecision.
//
//   - APPROVE continues at the stage after the gate.
//   - REVISE jumps back to the gate's revise target with GateDecision.feedback
//     threaded into that stage's prompt ({{revision_feedback}}).
//   - REJECT ends the run immediately with a *GateRejected error; nothing
//     executes.
//
// A resumed run can suspend again at a later gate — callers should handle
// *WorkflowSuspended exactly as they do for ExecutePattern.
func (o *Orchestrator) ResumeWorkflow(ctx context.Context, pattern *loomv1.WorkflowPattern, ckpt *loomv1.WorkflowCheckpoint, decision *loomv1.GateDecision) (*loomv1.WorkflowResult, error) {
	if pattern == nil {
		return nil, fmt.Errorf("resume: nil workflow pattern")
	}
	if ckpt == nil {
		return nil, fmt.Errorf("resume: nil checkpoint")
	}
	if ckpt.CheckpointVersion > CheckpointVersion {
		return nil, fmt.Errorf("resume: checkpoint version %d is newer than supported version %d", ckpt.CheckpointVersion, CheckpointVersion)
	}
	if decision == nil || decision.Action == loomv1.GateAction_GATE_ACTION_UNSPECIFIED {
		return nil, fmt.Errorf("resume: a gate decision (approve, revise, or reject) is required")
	}

	if decision.Action == loomv1.GateAction_GATE_ACTION_REJECT {
		o.logger.Info("Workflow rejected at HITL gate",
			zap.String("workflow_id", ckpt.WorkflowId),
			zap.String("stage", ckpt.GetPendingGate().GetStageAgentId()))
		return nil, &GateRejected{
			StageAgentID: ckpt.GetPendingGate().GetStageAgentId(),
			Feedback:     decision.Feedback,
		}
	}

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		executor := NewPipelineExecutor(o, p.Pipeline, ckpt.WorkflowId)
		return executor.Resume(ctx, ckpt, decision)

	case *loomv1.WorkflowPattern_Iterative:
		executor := NewIterativePipelineExecutor(o, p.Iterative, o.messageBus, ckpt.WorkflowId)
		return executor.Resume(ctx, ckpt, decision)

	default:
		return nil, fmt.Errorf("resume: pattern type %s does not support HITL gates", GetPatternType(pattern))
	}
}
