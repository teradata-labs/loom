// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"
)

// recordingLLMProvider is a scripted LLM provider that also records the user
// prompt of every call, so tests can assert on threaded revision feedback.
type recordingLLMProvider struct {
	mu        sync.Mutex
	responses []string
	callCount int
	prompts   []string
}

func newRecordingLLMProvider(responses ...string) *recordingLLMProvider {
	return &recordingLLMProvider{responses: responses}
}

func (m *recordingLLMProvider) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			m.prompts = append(m.prompts, messages[i].Content)
			break
		}
	}

	var response string
	if m.callCount < len(m.responses) {
		response = m.responses[m.callCount]
	} else {
		response = fmt.Sprintf("Recorded mock response %d", m.callCount)
	}
	m.callCount++

	return &llmtypes.LLMResponse{
		Content:    response,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *recordingLLMProvider) Name() string  { return "recording-mock" }
func (m *recordingLLMProvider) Model() string { return "recording-mock-model" }

func (m *recordingLLMProvider) recordedPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.prompts))
	copy(out, m.prompts)
	return out
}

// scriptedHITLHandler returns pre-scripted gate decisions in order.
type scriptedHITLHandler struct {
	mu        sync.Mutex
	decisions []*loomv1.GateDecision
	errs      []error
	calls     int
	requests  []*loomv1.HITLGateRequest
}

func (h *scriptedHITLHandler) RequestDecision(_ context.Context, req *loomv1.HITLGateRequest) (*loomv1.GateDecision, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, req)
	i := h.calls
	h.calls++
	var err error
	if i < len(h.errs) {
		err = h.errs[i]
	}
	var d *loomv1.GateDecision
	if i < len(h.decisions) {
		d = h.decisions[i]
	}
	return d, err
}

// gatedPipelinePattern builds a 3-stage pipeline (analyst → designer → executor)
// with a HITL gate on the designer stage.
func gatedPipelinePattern(gate *loomv1.HITLGate) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Create an orders table",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "analyst", PromptTemplate: "Analyze: {{previous}}"},
					{AgentId: "designer", PromptTemplate: "Design DDL for: {{previous}}", HitlGate: gate},
					{AgentId: "executor", PromptTemplate: "Execute exactly: {{previous}}"},
				},
			},
		},
	}
}

// newGateTestOrchestrator builds an orchestrator with three scripted agents.
func newGateTestOrchestrator(t *testing.T, handler HITLHandler) (*Orchestrator, *recordingLLMProvider, *recordingLLMProvider, *recordingLLMProvider) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	analystLLM := newRecordingLLMProvider("requirements: orders table")
	designerLLM := newRecordingLLMProvider("CREATE TABLE orders (...)", "CREATE MULTISET TABLE orders (...)")
	executorLLM := newRecordingLLMProvider("Table created successfully")

	orch := NewOrchestrator(Config{
		Logger:      logger,
		Tracer:      tracer,
		HITLHandler: handler,
	})
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", analystLLM))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", designerLLM))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", executorLLM))
	return orch, analystLLM, designerLLM, executorLLM
}

func TestWorkflowFingerprint_StableAndIgnoresWorkflowID(t *testing.T) {
	t.Parallel()

	p1 := gatedPipelinePattern(&loomv1.HITLGate{})
	p2 := gatedPipelinePattern(&loomv1.HITLGate{})
	p2.WorkflowId = "some-stable-id"

	fp1, err := WorkflowFingerprint(p1)
	require.NoError(t, err)
	fp2, err := WorkflowFingerprint(p2)
	require.NoError(t, err)
	assert.Equal(t, fp1, fp2, "workflow_id must not affect the fingerprint")

	p3 := gatedPipelinePattern(&loomv1.HITLGate{})
	p3.GetPipeline().Stages[1].PromptTemplate = "Design DIFFERENT DDL for: {{previous}}"
	fp3, err := WorkflowFingerprint(p3)
	require.NoError(t, err)
	assert.NotEqual(t, fp1, fp3, "definition changes must change the fingerprint")
}

func TestInjectRevisionFeedback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prompt   string
		feedback string
		source   string
		want     []string
		notWant  []string
	}{
		{
			name:     "placeholder replaced",
			prompt:   "Design DDL. Feedback: {{revision_feedback}}",
			feedback: "use MULTISET",
			source:   "human reviewer",
			want:     []string{"Feedback: use MULTISET"},
			notWant:  []string{"{{revision_feedback}}", "## REVISION FEEDBACK"},
		},
		{
			name:     "no placeholder appends labeled section",
			prompt:   "Design DDL.",
			feedback: "use MULTISET",
			source:   "human reviewer",
			want:     []string{"## REVISION FEEDBACK (from human reviewer)", "use MULTISET"},
		},
		{
			name:    "empty feedback blanks placeholder",
			prompt:  "Design DDL. {{revision_feedback}}",
			want:    []string{"Design DDL. "},
			notWant: []string{"{{revision_feedback}}", "## REVISION FEEDBACK"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := injectRevisionFeedback(tt.prompt, tt.feedback, tt.source)
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
			for _, nw := range tt.notWant {
				assert.NotContains(t, got, nw)
			}
		})
	}
}

func TestResolveReviseTarget(t *testing.T) {
	t.Parallel()

	stages := gatedPipelinePattern(nil).GetPipeline().Stages

	tests := []struct {
		name       string
		gatedIndex int
		gate       *loomv1.HITLGate
		wantIndex  int
		wantErr    string
	}{
		{name: "default is the gated stage", gatedIndex: 1, gate: &loomv1.HITLGate{}, wantIndex: 1},
		{name: "named earlier stage", gatedIndex: 1, gate: &loomv1.HITLGate{ReviseTargetStageId: "analyst"}, wantIndex: 0},
		{name: "forward jump rejected", gatedIndex: 1, gate: &loomv1.HITLGate{ReviseTargetStageId: "executor"}, wantErr: "forward jumps are invalid"},
		{name: "unknown stage rejected", gatedIndex: 1, gate: &loomv1.HITLGate{ReviseTargetStageId: "nope"}, wantErr: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveReviseTarget(stages, tt.gatedIndex, tt.gate)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantIndex, got)
		})
	}
}

func TestHITLGateYAMLParsing(t *testing.T) {
	t.Parallel()

	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: create-table-pipeline
spec:
  type: pipeline
  initial_prompt: "Create an orders table"
  stages:
    - agent_id: designer
      prompt_template: "Design DDL: {{previous}}"
      hitl_gate:
        prompt_template: "Review this DDL:\n{{output}}"
        request_type: approval
        timeout_seconds: 1800
        revise_target_stage_id: designer
        max_revisions: 2
        on_timeout: reject
    - agent_id: executor
      prompt_template: "Execute: {{previous}}"
`
	pattern, err := LoadWorkflowFromYAMLBytes([]byte(yaml))
	require.NoError(t, err)
	gate := pattern.GetPipeline().Stages[0].HitlGate
	require.NotNil(t, gate)
	assert.Contains(t, gate.PromptTemplate, "Review this DDL")
	assert.Equal(t, "approval", gate.RequestType)
	assert.Equal(t, int32(1800), gate.TimeoutSeconds)
	assert.Equal(t, "designer", gate.ReviseTargetStageId)
	assert.Equal(t, int32(2), gate.MaxRevisions)
	assert.Equal(t, loomv1.GateTimeoutAction_GATE_TIMEOUT_ACTION_REJECT, gate.OnTimeout)
	assert.Nil(t, pattern.GetPipeline().Stages[1].HitlGate)

	badTimeout := strings.Replace(yaml, "on_timeout: reject", "on_timeout: explode", 1)
	_, err = LoadWorkflowFromYAMLBytes([]byte(badTimeout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "on_timeout")

	forwardTarget := strings.Replace(yaml, "revise_target_stage_id: designer", "revise_target_stage_id: executor", 1)
	_, err = LoadWorkflowFromYAMLBytes([]byte(forwardTarget))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forward jumps are invalid")
}

func TestHITLGateYAML_RejectedInConditionalBranch(t *testing.T) {
	t.Parallel()

	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: gated-branch
spec:
  type: conditional
  condition_agent_id: router
  condition_prompt: "route it"
  branches:
    build:
      type: pipeline
      initial_prompt: "go"
      stages:
        - agent_id: designer
          prompt_template: "Design: {{previous}}"
          hitl_gate: {}
`
	_, err := LoadWorkflowFromYAMLBytes([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conditional branch")
}

func TestPipelineGate_SuspendsWithCheckpoint(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{
		PromptTemplate: "Review the DDL:\n{{output}}",
	})
	orch, _, _, executorLLM := newGateTestOrchestrator(t, nil)

	result, err := orch.ExecutePattern(context.Background(), pattern)
	assert.Nil(t, result)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	ckpt := suspended.Checkpoint
	require.NotNil(t, ckpt)

	assert.Equal(t, int32(CheckpointVersion), ckpt.CheckpointVersion)
	assert.Equal(t, "pipeline", ckpt.PatternType)
	assert.Equal(t, int32(2), ckpt.NextStageIndex)
	require.Len(t, ckpt.StageSnapshots, 2)
	assert.Equal(t, "analyst", ckpt.StageSnapshots[0].AgentId)
	assert.Equal(t, "designer", ckpt.StageSnapshots[1].AgentId)
	assert.Equal(t, "CREATE TABLE orders (...)", ckpt.StageSnapshots[1].FullOutput)

	require.NotNil(t, ckpt.PendingGate)
	assert.Equal(t, "designer", ckpt.PendingGate.StageAgentId)
	assert.Equal(t, int32(2), ckpt.PendingGate.StageNumber)
	assert.Contains(t, ckpt.PendingGate.Question, "CREATE TABLE orders (...)", "{{output}} must be rendered into the question")

	fp, err := WorkflowFingerprint(pattern)
	require.NoError(t, err)
	assert.Equal(t, fp, ckpt.ConfigFingerprint)

	// The executor stage must not have run.
	assert.Empty(t, executorLLM.recordedPrompts())
}

func TestPipelineGate_ResumeApprove_Completes(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	orch, _, _, executorLLM := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "Table created successfully", result.MergedOutput)

	prompts := executorLLM.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "CREATE TABLE orders (...)", "approved output must flow into the next stage as {{previous}}")
}

func TestPipelineGate_ResumeRevise_ThreadsFeedbackAndSuspendsAgain(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	orch, _, designerLLM, _ := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	// Revise: designer re-runs with feedback, then the gate fires again.
	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET and add COMPRESS"})
	var suspendedAgain *WorkflowSuspended
	require.ErrorAs(t, err, &suspendedAgain)

	prompts := designerLLM.recordedPrompts()
	require.Len(t, prompts, 2, "designer must run twice (original + revision)")
	assert.Contains(t, prompts[1], "REVISION FEEDBACK (from human reviewer)")
	assert.Contains(t, prompts[1], "Use MULTISET and add COMPRESS")

	// The new checkpoint carries the revised output and the revision count.
	ckpt := suspendedAgain.Checkpoint
	assert.Equal(t, "CREATE MULTISET TABLE orders (...)", ckpt.StageSnapshots[1].FullOutput)
	assert.Equal(t, int32(1), ckpt.GateRevisionCounts["designer"])

	// Approve the revised output and complete.
	result, err := orch.ResumeWorkflow(context.Background(), pattern, ckpt,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "Table created successfully", result.MergedOutput)
}

func TestPipelineGate_ResumeReject(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	orch, _, _, executorLLM := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REJECT, Feedback: "not needed"})
	var rejected *GateRejected
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, "designer", rejected.StageAgentID)
	assert.Equal(t, "not needed", rejected.Feedback)
	assert.Empty(t, executorLLM.recordedPrompts(), "nothing may execute after a rejection")
}

func TestPipelineGate_FingerprintMismatchRefusesResume(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	orch, _, _, _ := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	edited, ok := proto.Clone(pattern).(*loomv1.WorkflowPattern)
	require.True(t, ok)
	edited.GetPipeline().Stages[2].PromptTemplate = "DROP DATABASE; -- {{previous}}"

	_, err = orch.ResumeWorkflow(context.Background(), edited, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed since suspension")
}

func TestPipelineGate_MaxRevisionsExceeded(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{MaxRevisions: 1})
	orch, _, _, _ := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	// First revision: allowed, suspends again.
	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "again"})
	var suspendedAgain *WorkflowSuspended
	require.ErrorAs(t, err, &suspendedAgain)

	// Second revision: exceeds the budget.
	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspendedAgain.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "and again"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_revisions")
}

func TestPipelineGate_ResumeReviseRequiresFeedback(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(&loomv1.HITLGate{})
	orch, _, _, _ := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires feedback")
}

func TestPipelineGate_InlineHandlerDecisions(t *testing.T) {
	t.Parallel()

	t.Run("approve completes without suspension", func(t *testing.T) {
		t.Parallel()
		handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
			{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
		}}
		pattern := gatedPipelinePattern(&loomv1.HITLGate{})
		orch, _, _, _ := newGateTestOrchestrator(t, handler)

		result, err := orch.ExecutePattern(context.Background(), pattern)
		require.NoError(t, err)
		assert.Equal(t, "Table created successfully", result.MergedOutput)
		assert.Equal(t, 1, handler.calls)
	})

	t.Run("revise then approve in-process", func(t *testing.T) {
		t.Parallel()
		handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
			{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "Use MULTISET"},
			{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
		}}
		pattern := gatedPipelinePattern(&loomv1.HITLGate{})
		orch, _, designerLLM, _ := newGateTestOrchestrator(t, handler)

		result, err := orch.ExecutePattern(context.Background(), pattern)
		require.NoError(t, err)
		assert.Equal(t, "Table created successfully", result.MergedOutput)

		prompts := designerLLM.recordedPrompts()
		require.Len(t, prompts, 2)
		assert.Contains(t, prompts[1], "Use MULTISET")
	})

	t.Run("reject returns GateRejected", func(t *testing.T) {
		t.Parallel()
		handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
			{Action: loomv1.GateAction_GATE_ACTION_REJECT, Feedback: "nope"},
		}}
		pattern := gatedPipelinePattern(&loomv1.HITLGate{})
		orch, _, _, _ := newGateTestOrchestrator(t, handler)

		_, err := orch.ExecutePattern(context.Background(), pattern)
		var rejected *GateRejected
		require.ErrorAs(t, err, &rejected)
	})

	t.Run("handler ErrSuspendWorkflow forces durable suspension", func(t *testing.T) {
		t.Parallel()
		handler := &scriptedHITLHandler{errs: []error{ErrSuspendWorkflow}}
		pattern := gatedPipelinePattern(&loomv1.HITLGate{})
		orch, _, _, _ := newGateTestOrchestrator(t, handler)

		_, err := orch.ExecutePattern(context.Background(), pattern)
		var suspended *WorkflowSuspended
		require.ErrorAs(t, err, &suspended)
	})
}

func TestGateOnLastStage_ResumeApproveCompletes(t *testing.T) {
	t.Parallel()

	pattern := gatedPipelinePattern(nil)
	pattern.GetPipeline().Stages[2].HitlGate = &loomv1.HITLGate{}
	orch, _, _, _ := newGateTestOrchestrator(t, nil)

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, int32(3), suspended.Checkpoint.NextStageIndex)

	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "Table created successfully", result.MergedOutput)
}

func TestResumeWorkflow_RejectsUnsupportedPatterns(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{
			ForkJoin: &loomv1.ForkJoinPattern{Prompt: "x", AgentIds: []string{"a"}},
		},
	}
	_, err := orch.ResumeWorkflow(context.Background(), pattern, &loomv1.WorkflowCheckpoint{CheckpointVersion: 1},
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support HITL gates")
}

func TestIterativeGate_SuspendAndResume(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	analystLLM := newRecordingLLMProvider(`{"analysis": "orders table needed"}`)
	designerLLM := newRecordingLLMProvider(`{"ddl": "CREATE TABLE orders"}`)
	executorLLM := newRecordingLLMProvider(`{"result": "created"}`)

	memoryStore := communication.NewMemoryStore(5 * time.Minute)
	messageBus := communication.NewMessageBus(memoryStore, nil, tracer, logger)
	sharedMemory, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)

	orch := NewOrchestrator(Config{
		Logger:       logger,
		Tracer:       tracer,
		MessageBus:   messageBus,
		SharedMemory: sharedMemory,
	})
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", analystLLM))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", designerLLM))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", executorLLM))

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Create an orders table",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "analyst", PromptTemplate: "Analyze: {{previous}}"},
						{AgentId: "designer", PromptTemplate: "Design: {{previous}}", HitlGate: &loomv1.HITLGate{}},
						{AgentId: "executor", PromptTemplate: "Execute: {{previous}}"},
					},
				},
				MaxIterations: 3,
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled:              true,
					PreserveOutputs:      true,
					MaxValidationRetries: 0, // skip validation for deterministic mocks
				},
			},
		},
	}

	_, err = orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	ckpt := suspended.Checkpoint
	assert.Equal(t, "iterative_pipeline", ckpt.PatternType)
	assert.Equal(t, int32(2), ckpt.NextStageIndex)
	assert.Equal(t, int32(1), ckpt.Iteration)
	require.Len(t, ckpt.StageSnapshots, 2)
	assert.Empty(t, executorLLM.recordedPrompts())

	result, err := orch.ResumeWorkflow(context.Background(), pattern, ckpt,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "iterative_pipeline", result.PatternType)
	assert.Contains(t, result.MergedOutput, "created")

	prompts := executorLLM.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "CREATE TABLE orders", "approved designer output must reach the executor stage")
}

func TestIterativeGate_RestartDisabledFallbackRoundTrip(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	analystLLM := newRecordingLLMProvider("analysis done")
	designerLLM := newRecordingLLMProvider("CREATE TABLE t (...)")
	executorLLM := newRecordingLLMProvider("done")

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", analystLLM))
	orch.RegisterAgent("designer", createMockAgent(t, "designer", designerLLM))
	orch.RegisterAgent("executor", createMockAgent(t, "executor", executorLLM))

	// Iterative pattern with restart DISABLED — executes via the plain
	// pipeline fallback, but checkpoints must fingerprint the iterative
	// pattern so this exact pattern can resume.
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Create a table",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "analyst", PromptTemplate: "Analyze: {{previous}}"},
						{AgentId: "designer", PromptTemplate: "Design: {{previous}}", HitlGate: &loomv1.HITLGate{}},
						{AgentId: "executor", PromptTemplate: "Execute: {{previous}}"},
					},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, "pipeline", suspended.Checkpoint.PatternType)

	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "done", result.MergedOutput)
}

func TestFormatRestartFeedback(t *testing.T) {
	t.Parallel()

	got := formatRestartFeedback(&loomv1.RestartRequest{
		Reason:     "table too large",
		Parameters: map[string]string{"row_limit": "1000", "approach": "sample"},
	})
	assert.Contains(t, got, "table too large")
	assert.Contains(t, got, "- approach: sample")
	assert.Contains(t, got, "- row_limit: 1000")
	// Sorted parameter order for determinism.
	assert.Less(t, strings.Index(got, "approach"), strings.Index(got, "row_limit"))
}

func TestWorkflowSuspendedAndGateRejectedErrors(t *testing.T) {
	t.Parallel()

	s := &WorkflowSuspended{Checkpoint: &loomv1.WorkflowCheckpoint{
		PendingGate: &loomv1.HITLGateRequest{StageNumber: 2, StageAgentId: "designer"},
	}}
	assert.Contains(t, s.Error(), "stage 2")
	assert.Contains(t, s.Error(), "designer")
	assert.True(t, errors.As(error(s), new(*WorkflowSuspended)))

	r := &GateRejected{StageAgentID: "designer", Feedback: "no"}
	assert.Contains(t, r.Error(), "designer")
	assert.Contains(t, r.Error(), "no")
}
