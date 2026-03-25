// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// TestResolveWorkflowID tests the resolveWorkflowID function with various inputs.
func TestResolveWorkflowID(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zap.NewNop(),
	})

	tests := []struct {
		name        string
		patternType string
		pattern     *loomv1.WorkflowPattern
		wantExact   string // if non-empty, expect exact match
		wantPrefix  string // if non-empty, expect this prefix
		wantUUID    bool   // if true, expect a UUID segment after the prefix
	}{
		{
			name:        "empty workflow_id generates random",
			patternType: "pipeline",
			pattern: &loomv1.WorkflowPattern{
				WorkflowId: "",
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{},
				},
			},
			wantPrefix: "pipeline-",
			wantUUID:   true,
		},
		{
			name:        "set workflow_id is used as-is",
			patternType: "pipeline",
			pattern: &loomv1.WorkflowPattern{
				WorkflowId: "my-stable-id",
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{},
				},
			},
			wantExact: "my-stable-id",
		},
		{
			name:        "nil pattern generates random",
			patternType: "pipeline",
			pattern:     nil,
			wantPrefix:  "pipeline-",
			wantUUID:    true,
		},
		{
			name:        "pipeline prefix when workflow_id is empty",
			patternType: "pipeline",
			pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{},
				},
			},
			wantPrefix: "pipeline-",
			wantUUID:   true,
		},
		{
			name:        "fork-join prefix when workflow_id is empty",
			patternType: "fork-join",
			pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_ForkJoin{
					ForkJoin: &loomv1.ForkJoinPattern{},
				},
			},
			wantPrefix: "fork-join-",
			wantUUID:   true,
		},
		{
			name:        "swarm prefix when workflow_id is empty",
			patternType: "swarm",
			pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Swarm{
					Swarm: &loomv1.SwarmPattern{},
				},
			},
			wantPrefix: "swarm-",
			wantUUID:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := orchestrator.resolveWorkflowID(tt.patternType, tt.pattern)

			if tt.wantExact != "" {
				assert.Equal(t, tt.wantExact, result)
				return
			}

			require.NotEmpty(t, result, "workflow ID must not be empty")
			assert.True(t, strings.HasPrefix(result, tt.wantPrefix),
				"expected prefix %q, got %q", tt.wantPrefix, result)

			if tt.wantUUID {
				// After the prefix there should be an 8-character hex UUID segment
				suffix := strings.TrimPrefix(result, tt.wantPrefix)
				assert.Len(t, suffix, 8, "UUID segment should be 8 characters, got %q", suffix)
				for _, c := range suffix {
					assert.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
						"UUID segment should be hex, got character %q in %q", string(c), suffix)
				}
			}
		})
	}
}

// sessionCapturingLLMProvider is a mock LLM provider that records session IDs
// extracted from the context passed to Chat().
type sessionCapturingLLMProvider struct {
	mu         sync.Mutex
	sessionIDs []string
	response   string
}

func newSessionCapturingLLMProvider(response string) *sessionCapturingLLMProvider {
	return &sessionCapturingLLMProvider{
		response: response,
	}
}

func (m *sessionCapturingLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Extract session ID injected by agent.Chat via session.WithSessionID
	sid := session.SessionIDFromContext(ctx)

	m.mu.Lock()
	m.sessionIDs = append(m.sessionIDs, sid)
	m.mu.Unlock()

	return &llmtypes.LLMResponse{
		Content:    m.response,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *sessionCapturingLLMProvider) Name() string  { return "mock-capture" }
func (m *sessionCapturingLLMProvider) Model() string  { return "mock-model" }

func (m *sessionCapturingLLMProvider) capturedSessionIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.sessionIDs))
	copy(out, m.sessionIDs)
	return out
}

// TestPipelineExecutor_StableSessionIDs verifies that a PipelineExecutor with a
// fixed workflowID produces deterministic session IDs of the form
// "{workflowID}-stage{N}-{agentID}".
func TestPipelineExecutor_StableSessionIDs(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zap.NewNop(),
		Tracer: observability.NewNoOpTracer(),
	})

	// Create two capturing LLM providers so we can verify the session IDs
	llm1 := newSessionCapturingLLMProvider("Stage 1 output")
	llm2 := newSessionCapturingLLMProvider("Stage 2 output")

	agent1 := createMockAgent(t, "agent1", llm1)
	agent2 := createMockAgent(t, "agent2", llm2)

	orchestrator.RegisterAgent("agent1", agent1)
	orchestrator.RegisterAgent("agent2", agent2)

	pattern := &loomv1.PipelinePattern{
		InitialPrompt: "Start the pipeline",
		Stages: []*loomv1.PipelineStage{
			{AgentId: "agent1", PromptTemplate: "{{previous}}"},
			{AgentId: "agent2", PromptTemplate: "{{previous}}"},
		},
	}

	executor := NewPipelineExecutor(orchestrator, pattern, "stable-wf-123")
	result, err := executor.Execute(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "pipeline", result.PatternType)
	assert.Len(t, result.AgentResults, 2)

	// Verify deterministic session IDs.
	// The pipeline executor constructs: "{workflowID}-stage{N}-{agentID}"
	// where N is 1-indexed.
	captured1 := llm1.capturedSessionIDs()
	require.Len(t, captured1, 1, "agent1 LLM should have been called exactly once")
	assert.Equal(t, "stable-wf-123-stage1-agent1", captured1[0])

	captured2 := llm2.capturedSessionIDs()
	require.Len(t, captured2, 1, "agent2 LLM should have been called exactly once")
	assert.Equal(t, "stable-wf-123-stage2-agent2", captured2[0])
}

// TestPipelineExecutor_RandomSessionIDs verifies that when the workflowID
// is a generated value (e.g., "pipeline-abc12345"), the session IDs still
// follow the "{workflowID}-stage{N}-{agentID}" format using that prefix.
func TestPipelineExecutor_RandomSessionIDs(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zap.NewNop(),
		Tracer: observability.NewNoOpTracer(),
	})

	llm1 := newSessionCapturingLLMProvider("Output A")
	llm2 := newSessionCapturingLLMProvider("Output B")

	agent1 := createMockAgent(t, "analyzer", llm1)
	agent2 := createMockAgent(t, "reviewer", llm2)

	orchestrator.RegisterAgent("analyzer", agent1)
	orchestrator.RegisterAgent("reviewer", agent2)

	pattern := &loomv1.PipelinePattern{
		InitialPrompt: "Analyze and review",
		Stages: []*loomv1.PipelineStage{
			{AgentId: "analyzer", PromptTemplate: "{{previous}}"},
			{AgentId: "reviewer", PromptTemplate: "{{previous}}"},
		},
	}

	workflowID := "pipeline-abc12345"
	executor := NewPipelineExecutor(orchestrator, pattern, workflowID)
	result, err := executor.Execute(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.AgentResults, 2)

	// Verify session IDs use the provided workflow ID prefix
	captured1 := llm1.capturedSessionIDs()
	require.Len(t, captured1, 1)
	assert.Equal(t, fmt.Sprintf("%s-stage1-analyzer", workflowID), captured1[0])

	captured2 := llm2.capturedSessionIDs()
	require.Len(t, captured2, 1)
	assert.Equal(t, fmt.Sprintf("%s-stage2-reviewer", workflowID), captured2[0])
}
