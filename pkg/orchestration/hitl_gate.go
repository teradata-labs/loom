// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// CheckpointVersion is the current WorkflowCheckpoint schema version.
const CheckpointVersion = 1

// DefaultMaxGateRevisions bounds REVISE round-trips per gate when
// HITLGate.max_revisions is unset.
const DefaultMaxGateRevisions = 3

// RevisionFeedbackPlaceholder is replaced in a restarted stage's prompt with
// the human's (or requesting stage's) revision feedback. Templates that omit
// it get the feedback appended as a clearly-labeled section instead.
const RevisionFeedbackPlaceholder = "{{revision_feedback}}"

// ErrSuspendWorkflow can be returned by an HITLHandler to request durable
// suspension instead of deciding inline. A nil handler suspends by default.
var ErrSuspendWorkflow = errors.New("workflow suspension requested")

// HITLHandler decides a HITL gate inline (e.g. a terminal prompt or a chat
// bridge). Hosts that persist checkpoints and resume later should leave the
// orchestrator's handler nil — gates then suspend by default.
type HITLHandler interface {
	// RequestDecision blocks until a decision is available or ctx expires.
	// Returning ErrSuspendWorkflow (or wrapping it) converts the gate into a
	// durable suspension.
	RequestDecision(ctx context.Context, req *loomv1.HITLGateRequest) (*loomv1.GateDecision, error)
}

// WorkflowSuspended is returned (as an error) when a HITL gate suspends
// execution. The host persists Checkpoint and later calls
// Orchestrator.ResumeWorkflow with a decision. Detect it with errors.As.
type WorkflowSuspended struct {
	Checkpoint *loomv1.WorkflowCheckpoint
}

// Error implements the error interface.
func (s *WorkflowSuspended) Error() string {
	gate := s.Checkpoint.GetPendingGate()
	return fmt.Sprintf("workflow suspended awaiting human decision at stage %d (%s)",
		gate.GetStageNumber(), gate.GetStageAgentId())
}

// GateRejected is returned (as an error) when a human rejects a gate. The
// workflow ends without executing later stages. Detect it with errors.As.
type GateRejected struct {
	StageAgentID string
	Feedback     string
}

// Error implements the error interface.
func (r *GateRejected) Error() string {
	if r.Feedback == "" {
		return fmt.Sprintf("workflow rejected by human at gate on stage %s", r.StageAgentID)
	}
	return fmt.Sprintf("workflow rejected by human at gate on stage %s: %s", r.StageAgentID, r.Feedback)
}

// WorkflowFingerprint returns the SHA-256 hex digest of the deterministically
// marshaled pattern, with the execution-identity workflow_id field cleared so
// two runs of the same definition fingerprint identically. Resume verifies
// this against the checkpoint before executing anything.
func WorkflowFingerprint(pattern *loomv1.WorkflowPattern) (string, error) {
	if pattern == nil {
		return "", fmt.Errorf("nil workflow pattern")
	}
	clone, ok := proto.Clone(pattern).(*loomv1.WorkflowPattern)
	if !ok {
		return "", fmt.Errorf("failed to clone workflow pattern")
	}
	clone.WorkflowId = ""
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	if err != nil {
		return "", fmt.Errorf("failed to marshal workflow pattern: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// pipelinePatternFingerprint fingerprints a bare PipelinePattern by wrapping
// it the way ExecutePattern dispatches it.
func pipelinePatternFingerprint(p *loomv1.PipelinePattern) (string, error) {
	return WorkflowFingerprint(&loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: p},
	})
}

// iterativePatternFingerprint fingerprints a bare IterativeWorkflowPattern by
// wrapping it the way ExecutePattern dispatches it.
func iterativePatternFingerprint(p *loomv1.IterativeWorkflowPattern) (string, error) {
	return WorkflowFingerprint(&loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: p},
	})
}

// buildGateRequest renders the HITLGateRequest handed to the host when a gate
// fires on the given stage.
func buildGateRequest(workflowID string, stage *loomv1.PipelineStage, stageNum int, output string) *loomv1.HITLGateRequest {
	gate := stage.HitlGate

	question := gate.PromptTemplate
	if question == "" {
		question = fmt.Sprintf("Review the output of stage %d (%s). Approve to continue, revise with feedback, or reject to stop.", stageNum, stage.AgentId)
	}
	question = strings.ReplaceAll(question, "{{output}}", output)

	requestType := gate.RequestType
	if requestType == "" {
		requestType = "approval"
	}

	return &loomv1.HITLGateRequest{
		WorkflowId:     workflowID,
		StageAgentId:   stage.AgentId,
		StageNumber:    int32(stageNum), // #nosec G115 -- stage counts are tiny
		Question:       question,
		StageOutput:    output,
		RequestType:    requestType,
		TimeoutSeconds: gate.TimeoutSeconds,
		Options:        []string{"approve", "revise", "reject"},
	}
}

// evaluateHITLGate asks the configured handler for an inline decision.
// A nil decision with nil error means "suspend": either no handler is
// configured (the durable default) or the handler chose ErrSuspendWorkflow.
func evaluateHITLGate(ctx context.Context, o *Orchestrator, gate *loomv1.HITLGate, req *loomv1.HITLGateRequest) (*loomv1.GateDecision, error) {
	handler := o.hitlHandler
	if handler == nil {
		return nil, nil
	}

	handlerCtx := ctx
	if gate.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		handlerCtx, cancel = context.WithTimeout(ctx, time.Duration(gate.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	decision, err := handler.RequestDecision(handlerCtx, req)
	if err != nil {
		if errors.Is(err, ErrSuspendWorkflow) {
			return nil, nil
		}
		// Timeout of the inline handler follows the gate's on_timeout action.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			switch gate.OnTimeout {
			case loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_APPROVE:
				o.logger.Warn("HITL gate timed out; auto-approving per on_timeout",
					zap.String("stage", req.StageAgentId))
				return &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE, DecidedBy: "on_timeout"}, nil
			case loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_REJECT:
				return &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REJECT, Feedback: "gate decision timed out", DecidedBy: "on_timeout"}, nil
			default:
				return nil, fmt.Errorf("HITL gate on stage %s timed out awaiting decision", req.StageAgentId)
			}
		}
		return nil, fmt.Errorf("HITL gate handler failed on stage %s: %w", req.StageAgentId, err)
	}
	if decision == nil || decision.Action == loomv1.GateAction_GATE_ACTION_UNSPECIFIED {
		return nil, fmt.Errorf("HITL gate handler returned no decision for stage %s", req.StageAgentId)
	}
	return decision, nil
}

// maxGateRevisions returns the gate's revision budget with the default applied.
func maxGateRevisions(gate *loomv1.HITLGate) int32 {
	if gate.MaxRevisions > 0 {
		return gate.MaxRevisions
	}
	return DefaultMaxGateRevisions
}

// resolveReviseTarget maps a REVISE decision to the stage index to restart.
// The target must be at or before the gated stage (no forward jumps).
func resolveReviseTarget(stages []*loomv1.PipelineStage, gatedIndex int, gate *loomv1.HITLGate) (int, error) {
	if gate.ReviseTargetStageId == "" {
		return gatedIndex, nil
	}
	for i, s := range stages {
		if s.AgentId == gate.ReviseTargetStageId {
			if i > gatedIndex {
				return 0, fmt.Errorf("hitl_gate revise_target_stage_id %q is after the gated stage (forward jumps are invalid)", gate.ReviseTargetStageId)
			}
			return i, nil
		}
	}
	return 0, fmt.Errorf("hitl_gate revise_target_stage_id %q not found in pipeline stages", gate.ReviseTargetStageId)
}

// injectRevisionFeedback threads feedback into a restarted stage's prompt.
// With empty feedback it only blanks a stray placeholder. The header names
// the feedback source ("human reviewer", "stage analyze-data", ...).
func injectRevisionFeedback(prompt, feedback, source string) string {
	if feedback == "" {
		return strings.ReplaceAll(prompt, RevisionFeedbackPlaceholder, "")
	}
	if strings.Contains(prompt, RevisionFeedbackPlaceholder) {
		return strings.ReplaceAll(prompt, RevisionFeedbackPlaceholder, feedback)
	}
	return fmt.Sprintf("%s\n\n## REVISION FEEDBACK (from %s)\nYour previous output for this stage was sent back for revision. Address this feedback:\n\n%s", prompt, source, feedback)
}

// formatRestartFeedback renders a RestartRequest's reason and parameters as
// revision feedback for the restarted stage's prompt.
func formatRestartFeedback(req *loomv1.RestartRequest) string {
	var b strings.Builder
	b.WriteString(req.Reason)
	if len(req.Parameters) > 0 {
		keys := make([]string, 0, len(req.Parameters))
		for k := range req.Parameters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n\nParameters:")
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("\n- %s: %s", k, req.Parameters[k]))
		}
	}
	return b.String()
}

// stageOutputMemoryKeyPattern matches the executor-managed SharedMemory keys
// that are re-seeded from checkpoint stage snapshots (excluded from the
// generic memory snapshot to avoid storing outputs twice).
var stageOutputMemoryKeyPattern = regexp.MustCompile(`^stage-\d+-output$`)

// snapshotWorkflowMemory captures agent-written WORKFLOW-namespace entries for
// a checkpoint. Best-effort: failures are logged and skipped so a flaky memory
// backend cannot block suspension.
func snapshotWorkflowMemory(ctx context.Context, o *Orchestrator) []*loomv1.CheckpointMemoryEntry {
	if o.sharedMemory == nil {
		return nil
	}
	listResp, err := o.sharedMemory.List(ctx, &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
	})
	if err != nil {
		o.logger.Warn("checkpoint: failed to list workflow SharedMemory keys; snapshot skipped", zap.Error(err))
		return nil
	}

	var entries []*loomv1.CheckpointMemoryEntry
	for _, key := range listResp.Keys {
		if stageOutputMemoryKeyPattern.MatchString(key) {
			continue
		}
		getResp, err := o.sharedMemory.Get(ctx, &loomv1.GetSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       key,
			AgentId:   "workflow-checkpoint",
		})
		if err != nil || !getResp.Found || getResp.Value == nil {
			o.logger.Warn("checkpoint: failed to read workflow SharedMemory key; entry skipped",
				zap.String("key", key), zap.Error(err))
			continue
		}
		entries = append(entries, &loomv1.CheckpointMemoryEntry{
			Key:      key,
			Value:    getResp.Value.Value,
			AgentId:  getResp.Value.CreatedBy,
			Metadata: getResp.Value.Metadata,
		})
	}
	return entries
}

// restoreWorkflowMemory writes checkpointed entries and the completed stages'
// full outputs back into the WORKFLOW namespace before a resumed run starts,
// so shared_memory_read and truncation notices keep working. Best-effort.
func restoreWorkflowMemory(ctx context.Context, o *Orchestrator, ckpt *loomv1.WorkflowCheckpoint) {
	if o.sharedMemory == nil {
		return
	}
	for _, entry := range ckpt.SharedMemory {
		if _, err := o.sharedMemory.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       entry.Key,
			Value:     entry.Value,
			AgentId:   entry.AgentId,
			Metadata:  entry.Metadata,
		}); err != nil {
			o.logger.Warn("resume: failed to restore SharedMemory entry",
				zap.String("key", entry.Key), zap.Error(err))
		}
	}
	for i, snap := range ckpt.StageSnapshots {
		if snap.FullOutput == "" {
			// Iterative checkpoints keep index-aligned placeholder snapshots
			// for stages whose outputs a restart discarded — nothing to seed.
			continue
		}
		key := fmt.Sprintf("stage-%d-output", i+1)
		if _, err := o.sharedMemory.Put(ctx, &loomv1.PutSharedMemoryRequest{
			Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
			Key:       key,
			Value:     []byte(snap.FullOutput),
			AgentId:   snap.AgentId,
			Metadata: map[string]string{
				"type":      "stage_output",
				"agent_id":  snap.AgentId,
				"full_size": fmt.Sprintf("%d", len(snap.FullOutput)),
			},
		}); err != nil {
			o.logger.Warn("resume: failed to re-seed stage output in SharedMemory",
				zap.String("key", key), zap.Error(err))
		}
	}
}

// validateGatePlacement rejects HITL gates on pattern types that cannot
// suspend/resume. Only pipeline and iterative pipelines support gates.
func validateGatePlacement(pattern *loomv1.WorkflowPattern) error {
	check := func(stages []*loomv1.PipelineStage) error {
		for i, s := range stages {
			if s.HitlGate == nil {
				continue
			}
			if _, err := resolveReviseTarget(stages, i, s.HitlGate); err != nil {
				return err
			}
		}
		return nil
	}

	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		return check(p.Pipeline.Stages)
	case *loomv1.WorkflowPattern_Iterative:
		if p.Iterative.Pipeline != nil {
			return check(p.Iterative.Pipeline.Stages)
		}
		return nil
	case *loomv1.WorkflowPattern_Conditional:
		for name, branch := range p.Conditional.Branches {
			if hasHITLGate(branch) {
				return fmt.Errorf("hitl_gate is not supported inside conditional branch %q — gates require a pipeline or iterative top-level pattern", name)
			}
		}
		if p.Conditional.DefaultBranch != nil && hasHITLGate(p.Conditional.DefaultBranch) {
			return fmt.Errorf("hitl_gate is not supported inside conditional default_branch")
		}
		return nil
	default:
		if hasHITLGate(pattern) {
			return fmt.Errorf("hitl_gate is only supported on pipeline and iterative patterns")
		}
		return nil
	}
}

// hasHITLGate reports whether any pipeline stage in the pattern declares a gate.
func hasHITLGate(pattern *loomv1.WorkflowPattern) bool {
	var stages []*loomv1.PipelineStage
	switch p := pattern.Pattern.(type) {
	case *loomv1.WorkflowPattern_Pipeline:
		stages = p.Pipeline.Stages
	case *loomv1.WorkflowPattern_Iterative:
		if p.Iterative.Pipeline != nil {
			stages = p.Iterative.Pipeline.Stages
		}
	case *loomv1.WorkflowPattern_Conditional:
		for _, branch := range p.Conditional.Branches {
			if hasHITLGate(branch) {
				return true
			}
		}
		if p.Conditional.DefaultBranch != nil {
			return hasHITLGate(p.Conditional.DefaultBranch)
		}
	}
	for _, s := range stages {
		if s.HitlGate != nil {
			return true
		}
	}
	return false
}
