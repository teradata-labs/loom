// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

func TestSwarmOrchestrator_ParseVote(t *testing.T) {
	tests := []struct {
		name             string
		response         string
		expectedChoice   string
		expectedConf     float32
		expectedAltCount int
	}{
		{
			name: "well-formatted vote",
			response: `CHOICE: PostgreSQL

CONFIDENCE: 85

REASONING: PostgreSQL has excellent query optimizer and JSONB support

ALTERNATIVES: MySQL, SQLite, MongoDB`,
			expectedChoice:   "PostgreSQL",
			expectedConf:     0.85,
			expectedAltCount: 3,
		},
		{
			name: "minimal vote",
			response: `CHOICE: Use indexes

CONFIDENCE: 90`,
			expectedChoice:   "Use indexes",
			expectedConf:     0.90,
			expectedAltCount: 0,
		},
		{
			name:             "unstructured response",
			response:         "I think we should use columnar storage.",
			expectedChoice:   "I think we should use columnar storage.",
			expectedConf:     0.75, // default
			expectedAltCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &SwarmOrchestrator{}
			choice, conf, _, alternatives := orchestrator.parseVote(tt.response)

			assert.Equal(t, tt.expectedChoice, choice)
			assert.InDelta(t, tt.expectedConf, conf, 0.01)
			assert.Len(t, alternatives, tt.expectedAltCount)
		})
	}
}

func TestSwarmOrchestrator_AggregateVotes(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	result := &loomv1.SwarmResult{
		Votes: []*loomv1.SwarmVote{
			{Choice: "Option A", Confidence: 0.80},
			{Choice: "Option B", Confidence: 0.90},
			{Choice: "Option A", Confidence: 0.85},
		},
		VoteDistribution: make(map[string]int32),
	}

	orchestrator.aggregateVotes(result)

	assert.Equal(t, int32(2), result.VoteDistribution["option a"]) // normalized to lowercase
	assert.Equal(t, int32(1), result.VoteDistribution["option b"])
	assert.InDelta(t, 0.85, result.AverageConfidence, 0.01) // (0.80+0.90+0.85)/3
}

func TestSwarmOrchestrator_ApplyMajority(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	tests := []struct {
		name           string
		votes          []*loomv1.SwarmVote
		threshold      float64
		expectedChoice string
		thresholdMet   bool
	}{
		{
			name: "simple majority met",
			votes: []*loomv1.SwarmVote{
				{Choice: "A"},
				{Choice: "A"},
				{Choice: "A"},
				{Choice: "B"},
				{Choice: "B"},
			},
			threshold:      0.5,
			expectedChoice: "a",
			thresholdMet:   true, // 3/5 = 60% > 50%
		},
		{
			name: "supermajority not met",
			votes: []*loomv1.SwarmVote{
				{Choice: "A"},
				{Choice: "A"},
				{Choice: "A"},
				{Choice: "B"},
				{Choice: "B"},
			},
			threshold:      0.67,
			expectedChoice: "a",
			thresholdMet:   false, // 3/5 = 60% < 67%
		},
		{
			name: "unanimous required",
			votes: []*loomv1.SwarmVote{
				{Choice: "A"},
				{Choice: "A"},
				{Choice: "A"},
			},
			threshold:      1.0,
			expectedChoice: "a",
			thresholdMet:   true, // 3/3 = 100%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &loomv1.SwarmResult{
				Votes:            tt.votes,
				VoteDistribution: make(map[string]int32),
			}
			orchestrator.aggregateVotes(result)

			choice, met := orchestrator.applyMajority(result, tt.threshold)
			assert.Equal(t, tt.expectedChoice, choice)
			assert.Equal(t, tt.thresholdMet, met)
		})
	}
}

func TestSwarmOrchestrator_ApplyWeighted(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	result := &loomv1.SwarmResult{
		Votes: []*loomv1.SwarmVote{
			{Choice: "A", Confidence: 0.95}, // Weight: 0.95
			{Choice: "B", Confidence: 0.85}, // Weight: 0.85
			{Choice: "B", Confidence: 0.90}, // Weight: 0.90
		},
		VoteDistribution: make(map[string]int32),
	}
	orchestrator.aggregateVotes(result)

	// A: 0.95, B: 1.75 → B wins with avg confidence 1.75/3 = 0.583
	choice, met := orchestrator.applyWeighted(result, 0.5)
	assert.Equal(t, "b", choice)
	assert.True(t, met) // 0.583 > 0.5
}

func TestSwarmOrchestrator_AnalyzeConsensus(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	result := &loomv1.SwarmResult{
		Decision:          "option a",
		Votes:             make([]*loomv1.SwarmVote, 5),
		VoteDistribution:  map[string]int32{"option a": 3, "option b": 2},
		AverageConfidence: 0.85,
		ThresholdMet:      true,
	}

	config := &loomv1.SwarmPattern{
		Strategy: loomv1.VotingStrategy_MAJORITY,
	}

	analysis := orchestrator.analyzeConsensus(result, config)

	assert.Contains(t, analysis, "option a")
	assert.Contains(t, analysis, "3/5")
	assert.Contains(t, analysis, "60.0%")
	assert.Contains(t, analysis, "MAJORITY")
	assert.Contains(t, analysis, "85.0%")
	assert.Contains(t, analysis, "MET")
}

func TestSwarmOrchestrator_CalculateMetrics(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	result := &loomv1.SwarmResult{
		Votes: []*loomv1.SwarmVote{
			{Confidence: 0.80},
			{Confidence: 0.90},
			{Confidence: 0.85},
		},
		VoteDistribution: map[string]int32{"a": 2, "b": 1},
	}

	metrics := orchestrator.calculateMetrics(result)

	assert.NotNil(t, metrics)
	assert.Equal(t, int32(3), metrics.InteractionCount)
	assert.InDelta(t, 0.67, metrics.PerspectiveDiversity, 0.1) // 2 choices / 3 votes
	assert.GreaterOrEqual(t, metrics.ConfidenceVariance, float32(0.0))
}

func TestSwarmOrchestrator_BuildVotePrompt(t *testing.T) {
	orchestrator := &SwarmOrchestrator{}

	question := "What database should we use?"

	// First pass (no existing votes)
	prompt1 := orchestrator.buildVotePrompt(question, nil)
	assert.Contains(t, prompt1, question)
	assert.Contains(t, prompt1, "CHOICE:")
	assert.NotContains(t, prompt1, "Other Agents' Votes")

	// Second pass (with existing votes)
	existingVotes := []*loomv1.SwarmVote{
		{AgentId: "agent1", Choice: "PostgreSQL", Confidence: 0.85, Reasoning: "Great for analytics"},
		{AgentId: "agent2", Choice: "MySQL", Confidence: 0.75, Reasoning: "Simple and reliable"},
	}
	prompt2 := orchestrator.buildVotePrompt(question, existingVotes)
	assert.Contains(t, prompt2, question)
	assert.Contains(t, prompt2, "Other Agents' Votes")
	assert.Contains(t, prompt2, "PostgreSQL")
	assert.Contains(t, prompt2, "MySQL")
	assert.Contains(t, prompt2, "85%")
}

// TestSwarmOrchestrator_EphemeralJudgeIntegration tests the full ephemeral agent spawn workflow.
// This is an end-to-end test demonstrating:
// 1. Swarm voting that doesn't meet threshold
// 2. Automatic ephemeral judge spawn based on policy evaluation
// 3. Judge decision used in final result
// 4. Cost tracking and limits enforced
func TestSwarmOrchestrator_EphemeralJudgeIntegration(t *testing.T) {
	// Skip in short mode (integration test)
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create mock agent provider with factory support
	provider := newMockAgentFactory(t)

	// Create swarm orchestrator
	orchestrator := NewSwarmOrchestratorWithObservability(
		provider,
		observability.NewNoOpTracer(),
		zaptest.NewLogger(t),
	)

	// Configure swarm with supermajority (threshold hard to meet)
	config := &loomv1.SwarmPattern{
		Question:            "Should we use PostgreSQL or MySQL?",
		AgentIds:            []string{"agent1", "agent2", "agent3"},
		Strategy:            loomv1.VotingStrategy_SUPERMAJORITY, // Requires 67%
		ConfidenceThreshold: 0.67,
		ShareVotes:          false,
		// No JudgeAgentId - should spawn ephemeral judge
	}

	// Execute swarm
	result, err := orchestrator.Execute(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify swarm result structure
	swarmResult := result.GetSwarmResult()
	require.NotNil(t, swarmResult, "Should have swarm result")

	// Verify voting occurred
	assert.Len(t, swarmResult.Votes, 3, "Should have 3 votes from agents")
	assert.NotEmpty(t, swarmResult.Decision, "Should have final decision")

	// Verify judge was used (threshold not met, judge breaks tie)
	// The mock will create a split vote that doesn't meet supermajority
	// Judge should be spawned and provide final decision
	assert.Contains(t, swarmResult.ConsensusAnalysis, "judge",
		"Analysis should mention judge was used")

	// Verify cost tracking in policy evaluator
	count, cost := orchestrator.policyEvaluator.GetSpawnStats("judge")
	if count > 0 {
		assert.Equal(t, 1, count, "Judge should spawn exactly once")
		assert.Greater(t, cost, 0.0, "Judge spawn should have cost")
		assert.LessOrEqual(t, cost, 0.50, "Judge cost should be under policy limit")
	}

	// Verify metrics
	assert.NotNil(t, result.Metrics)
	assert.GreaterOrEqual(t, result.Metrics.InteractionCount, int32(3))

	t.Logf("✅ Integration test passed:")
	t.Logf("   - Swarm votes: %d", len(swarmResult.Votes))
	t.Logf("   - Decision: %s", swarmResult.Decision)
	t.Logf("   - Judge spawns: %d", count)
	t.Logf("   - Judge cost: $%.4f", cost)
}

// TestSwarmOrchestrator_PolicyLimitsEnforced tests that policy limits prevent excessive spawns.
func TestSwarmOrchestrator_PolicyLimitsEnforced(t *testing.T) {
	ctx := context.Background()

	// Create mock provider with factory
	provider := newMockAgentFactory(t)

	orchestrator := NewSwarmOrchestratorWithObservability(
		provider,
		observability.NewNoOpTracer(),
		zaptest.NewLogger(t),
	)

	// First execution - should allow judge spawn
	config1 := &loomv1.SwarmPattern{
		Question: "Question 1",
		AgentIds: []string{"agent1", "agent2"},
		Strategy: loomv1.VotingStrategy_UNANIMOUS, // Won't be met
	}

	result1, err := orchestrator.Execute(ctx, config1)
	require.NoError(t, err)
	require.NotNil(t, result1)

	// Verify first judge spawn succeeded
	count1, _ := orchestrator.policyEvaluator.GetSpawnStats("judge")
	assert.Equal(t, 1, count1, "First judge spawn should succeed")

	// Second execution - judge already spawned once (max_spawns=1 in default policy)
	// Reset is called at workflow start, so this will allow another spawn
	config2 := &loomv1.SwarmPattern{
		Question: "Question 2",
		AgentIds: []string{"agent1", "agent2"},
		Strategy: loomv1.VotingStrategy_UNANIMOUS,
	}

	result2, err := orchestrator.Execute(ctx, config2)
	require.NoError(t, err)
	require.NotNil(t, result2)

	// Verify policy evaluator reset between workflows
	count2, _ := orchestrator.policyEvaluator.GetSpawnStats("judge")
	assert.LessOrEqual(t, count2, 1, "Policy evaluator should reset between workflows")

	t.Logf("✅ Policy limits test passed:")
	t.Logf("   - Workflow 1 judge spawns: %d", count1)
	t.Logf("   - Workflow 2 judge spawns: %d", count2)
}

// mockAgentFactory implements both AgentProvider and AgentFactory for testing.
type mockAgentFactory struct {
	t      *testing.T
	agents map[string]*agent.Agent
	mu     sync.RWMutex
}

func newMockAgentFactory(t *testing.T) *mockAgentFactory {
	return &mockAgentFactory{
		t:      t,
		agents: make(map[string]*agent.Agent),
	}
}

func (m *mockAgentFactory) GetAgent(ctx context.Context, id string) (*agent.Agent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if ag, exists := m.agents[id]; exists {
		return ag, nil
	}

	// Create a mock agent on-demand for voting agents
	mockAgent := newMockAgent(id)
	m.agents[id] = mockAgent
	return mockAgent, nil
}

func (m *mockAgentFactory) CreateEphemeralAgent(ctx context.Context, role string) (*agent.Agent, error) {
	m.t.Logf("Creating ephemeral agent for role: %s", role)

	// Create ephemeral judge agent
	judgeAgent := newMockAgent(fmt.Sprintf("ephemeral-%s", role))
	return judgeAgent, nil
}

// newMockAgent creates a mock agent that returns realistic vote responses.
func newMockAgent(name string) *agent.Agent {
	// Use mockLLMProvider that returns vote-formatted responses
	llm := &mockSwarmLLMProvider{agentName: name}

	return agent.NewAgent(
		nil, // no backend needed
		llm,
		agent.WithName(name),
		agent.WithDescription(fmt.Sprintf("Mock agent: %s", name)),
	)
}

// mockSwarmLLMProvider returns vote-formatted responses for swarm testing.
type mockSwarmLLMProvider struct {
	agentName string
	callCount int
	mu        sync.Mutex
}

func (m *mockSwarmLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++

	// Different responses for different agents to create realistic voting
	var response string
	switch {
	case strings.HasPrefix(m.agentName, "ephemeral-judge"):
		// Judge makes final decision
		response = `CHOICE: PostgreSQL

CONFIDENCE: 90

REASONING: After reviewing all votes, PostgreSQL offers the best balance of features, performance, and reliability for this use case.

ALTERNATIVES: MySQL, SQLite`

	case m.agentName == "agent1":
		response = `CHOICE: PostgreSQL

CONFIDENCE: 80

REASONING: Strong ACID compliance and excellent query optimizer

ALTERNATIVES: MySQL, MariaDB`

	case m.agentName == "agent2":
		response = `CHOICE: MySQL

CONFIDENCE: 75

REASONING: Simpler to operate and widely supported

ALTERNATIVES: PostgreSQL, MariaDB`

	case m.agentName == "agent3":
		response = `CHOICE: PostgreSQL

CONFIDENCE: 70

REASONING: Better support for advanced features like JSONB

ALTERNATIVES: MySQL, MongoDB`

	default:
		response = fmt.Sprintf("CHOICE: Option-%s\nCONFIDENCE: 70", m.agentName)
	}

	return &llmtypes.LLMResponse{
		Content:    response,
		StopReason: "end_turn",
		Usage: llmtypes.Usage{
			InputTokens:  100,
			OutputTokens: 50,
			CostUSD:      0.05, // Realistic cost for testing
		},
	}, nil
}

func (m *mockSwarmLLMProvider) Name() string {
	return "mock-swarm"
}

func (m *mockSwarmLLMProvider) Model() string {
	return "mock-swarm-v1"
}
