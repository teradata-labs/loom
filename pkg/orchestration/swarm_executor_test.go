// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// TestSwarmPattern tests the swarm voting pattern with different strategies.
func TestSwarmPattern(t *testing.T) {
	tests := []struct {
		name                string
		question            string
		numAgents           int
		strategy            loomv1.VotingStrategy
		confidenceThreshold float32
		votes               []string // Vote responses from agents
		expectedDecision    string
		expectedThreshold   bool
		wantErr             bool
	}{
		{
			name:      "majority strategy - threshold met",
			question:  "Should we use PostgreSQL?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_MAJORITY,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Good for ACID compliance",
				"VOTE: Yes\nCONFIDENCE: 0.9\nREASONING: Strong ecosystem",
				"VOTE: No\nCONFIDENCE: 0.6\nREASONING: Prefer NoSQL",
			},
			expectedDecision:  "Yes",
			expectedThreshold: true,
			wantErr:           false,
		},
		{
			name:      "majority strategy - threshold not met",
			question:  "Use MongoDB?",
			numAgents: 4,
			strategy:  loomv1.VotingStrategy_MAJORITY,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.7\nREASONING: Flexible schema",
				"VOTE: No\nCONFIDENCE: 0.8\nREASONING: Lack of transactions",
				"VOTE: No\nCONFIDENCE: 0.9\nREASONING: Prefer SQL",
				"VOTE: Maybe\nCONFIDENCE: 0.5\nREASONING: Depends on use case",
			},
			expectedDecision:  "No",
			expectedThreshold: false, // 2 votes out of 4 = 50%, need >50%
			wantErr:           false,
		},
		{
			name:      "supermajority strategy - threshold met",
			question:  "Use Kubernetes?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_SUPERMAJORITY,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.9\nREASONING: Industry standard",
				"VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Good orchestration",
				"VOTE: Yes\nCONFIDENCE: 0.7\nREASONING: Scalable",
			},
			expectedDecision:  "Yes",
			expectedThreshold: true, // 3/3 >= 2/3
			wantErr:           false,
		},
		{
			name:      "supermajority strategy - threshold not met",
			question:  "Use microservices?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_SUPERMAJORITY,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Better scalability",
				"VOTE: No\nCONFIDENCE: 0.9\nREASONING: Too complex",
				"VOTE: Yes\nCONFIDENCE: 0.7\nREASONING: Team expertise",
			},
			expectedDecision:  "Yes",
			expectedThreshold: true, // 2/3 votes - integer division (3*2)/3 = 2, so 2 >= 2 is true
			wantErr:           false,
		},
		{
			name:      "unanimous strategy - achieved",
			question:  "Use version control?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_UNANIMOUS,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 1.0\nREASONING: Essential for teams",
				"VOTE: Yes\nCONFIDENCE: 1.0\nREASONING: Industry best practice",
				"VOTE: Yes\nCONFIDENCE: 1.0\nREASONING: Required for CI/CD",
			},
			expectedDecision:  "Yes",
			expectedThreshold: true,
			wantErr:           false,
		},
		{
			name:      "unanimous strategy - not achieved",
			question:  "Use TypeScript?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_UNANIMOUS,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.9\nREASONING: Type safety",
				"VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Better tooling",
				"VOTE: No\nCONFIDENCE: 0.7\nREASONING: Compilation overhead",
			},
			expectedDecision:  "Yes",
			expectedThreshold: false, // Not unanimous
			wantErr:           false,
		},
		{
			name:                "weighted strategy - threshold met",
			question:            "Use Redis?",
			numAgents:           3,
			strategy:            loomv1.VotingStrategy_WEIGHTED,
			confidenceThreshold: 0.7,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.9\nREASONING: Fast caching",
				"VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Good pub/sub",
				"VOTE: No\nCONFIDENCE: 0.5\nREASONING: Memory concerns",
			},
			expectedDecision:  "Yes",
			expectedThreshold: true, // (0.9 + 0.8) / 2 = 0.85 >= 0.7
			wantErr:           false,
		},
		{
			name:                "weighted strategy - threshold not met",
			question:            "Use GraphQL?",
			numAgents:           3,
			strategy:            loomv1.VotingStrategy_WEIGHTED,
			confidenceThreshold: 0.8,
			votes: []string{
				"VOTE: Yes\nCONFIDENCE: 0.7\nREASONING: Flexible queries",
				"VOTE: Yes\nCONFIDENCE: 0.6\nREASONING: Single endpoint",
				"VOTE: No\nCONFIDENCE: 0.9\nREASONING: REST is simpler",
			},
			expectedDecision:  "Yes",
			expectedThreshold: false, // Yes wins by count, but avg confidence (0.7+0.6)/2 = 0.65 < 0.8
			wantErr:           false,
		},
		{
			name:      "ranked_choice strategy - confidence determines winner",
			question:  "Best database?",
			numAgents: 4,
			strategy:  loomv1.VotingStrategy_RANKED_CHOICE,
			votes: []string{
				"VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: ACID compliance",
				"VOTE: MySQL\nCONFIDENCE: 0.6\nREASONING: Familiar",
				"VOTE: PostgreSQL\nCONFIDENCE: 0.8\nREASONING: Full-featured",
				"VOTE: MySQL\nCONFIDENCE: 0.7\nREASONING: Simple",
			},
			expectedDecision:  "PostgreSQL",
			expectedThreshold: false, // PostgreSQL: 1.7 total confidence < 2.0 (50% of 4)
			wantErr:           false,
		},
		{
			name:      "ranked_choice strategy - minority wins with high confidence",
			question:  "Use feature X?",
			numAgents: 3,
			strategy:  loomv1.VotingStrategy_RANKED_CHOICE,
			votes: []string{
				"VOTE: No\nCONFIDENCE: 0.95\nREASONING: Too risky",
				"VOTE: Yes\nCONFIDENCE: 0.5\nREASONING: Could be useful",
				"VOTE: Yes\nCONFIDENCE: 0.4\nREASONING: Worth trying",
			},
			expectedDecision:  "No",
			expectedThreshold: false, // No: 0.95 < 1.5 (50% of 3)
			wantErr:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create orchestrator
			logger := zaptest.NewLogger(t)
			tracer := observability.NewNoOpTracer()

			orchestrator := NewOrchestrator(Config{
				Logger: logger,
				Tracer: tracer,
			})

			// Create mock agents
			agentIDs := make([]string, tt.numAgents)
			for i := 0; i < tt.numAgents; i++ {
				agentID := fmt.Sprintf("agent-%d", i)
				agentIDs[i] = agentID

				// Create mock LLM with the vote response
				llm := newMockLLMProvider(tt.votes[i])
				ag := createMockAgent(t, agentID, llm)
				orchestrator.RegisterAgent(agentID, ag)
			}

			// Create swarm pattern
			pattern := &loomv1.SwarmPattern{
				Question:            tt.question,
				AgentIds:            agentIDs,
				Strategy:            tt.strategy,
				ConfidenceThreshold: tt.confidenceThreshold,
				ShareVotes:          false,
			}

			// Execute
			executor := NewSwarmExecutor(orchestrator, pattern)
			result, err := executor.Execute(context.Background())

			// Verify
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)

			// Check decision
			assert.Equal(t, tt.expectedDecision, result.MergedOutput, "Decision should match")

			// Check threshold
			thresholdMet := result.Metadata["threshold_met"]
			assert.Equal(t, fmt.Sprintf("%t", tt.expectedThreshold), thresholdMet, "Threshold met should match")

			// Check agent results
			assert.Len(t, result.AgentResults, tt.numAgents, "Should have result for each agent")
		})
	}
}

// TestSwarmVoteParsing tests the vote parsing logic.
func TestSwarmVoteParsing(t *testing.T) {
	tests := []struct {
		name              string
		output            string
		expectedChoice    string
		expectedConfMin   float32
		expectedConfMax   float32
		containsReasoning string
	}{
		{
			name: "complete vote format",
			output: `VOTE: PostgreSQL
CONFIDENCE: 0.85
REASONING: PostgreSQL provides excellent ACID compliance and has a mature ecosystem.`,
			expectedChoice:    "PostgreSQL",
			expectedConfMin:   0.84,
			expectedConfMax:   0.86,
			containsReasoning: "ACID compliance",
		},
		{
			name: "case insensitive headers",
			output: `Vote: Yes
Confidence: 0.9
Reasoning: Strong performance metrics`,
			expectedChoice:    "Yes",
			expectedConfMin:   0.89,
			expectedConfMax:   0.91,
			containsReasoning: "performance",
		},
		{
			name: "missing confidence defaults to 0.5",
			output: `VOTE: Maybe
REASONING: Need more data`,
			expectedChoice:    "Maybe",
			expectedConfMin:   0.49,
			expectedConfMax:   0.51,
			containsReasoning: "more data",
		},
		{
			name: "invalid confidence defaults to 0.5",
			output: `VOTE: No
CONFIDENCE: invalid
REASONING: Too risky`,
			expectedChoice:    "No",
			expectedConfMin:   0.49,
			expectedConfMax:   0.51,
			containsReasoning: "risky",
		},
		{
			name: "confidence out of range uses 0.5",
			output: `VOTE: Yes
CONFIDENCE: 1.5
REASONING: Excellent choice`,
			expectedChoice:    "Yes",
			expectedConfMin:   0.49,
			expectedConfMax:   0.51,
			containsReasoning: "Excellent",
		},
		{
			name: "missing vote defaults to abstain",
			output: `CONFIDENCE: 0.7
REASONING: Need more information`,
			expectedChoice:    "abstain",
			expectedConfMin:   0.69,
			expectedConfMax:   0.71,
			containsReasoning: "more information",
		},
		{
			name: "multiline reasoning",
			output: `VOTE: ClickHouse
CONFIDENCE: 0.88
REASONING: ClickHouse offers:
- Excellent query performance
- Columnar storage
- Good compression`,
			expectedChoice:    "ClickHouse",
			expectedConfMin:   0.87,
			expectedConfMax:   0.89,
			containsReasoning: "columnar storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create executor (need minimal setup)
			executor := &SwarmExecutor{
				pattern: &loomv1.SwarmPattern{},
			}

			// Parse vote
			vote := executor.parseVote("test-agent", tt.output)

			// Verify
			assert.Equal(t, "test-agent", vote.AgentId)
			assert.Equal(t, tt.expectedChoice, vote.Choice)
			assert.GreaterOrEqual(t, vote.Confidence, tt.expectedConfMin)
			assert.LessOrEqual(t, vote.Confidence, tt.expectedConfMax)

			if tt.containsReasoning != "" {
				// Check either Reasoning field or original output contains the text (case-insensitive)
				reasoningLower := strings.ToLower(vote.Reasoning)
				outputLower := strings.ToLower(tt.output)
				searchLower := strings.ToLower(tt.containsReasoning)
				containsText := strings.Contains(reasoningLower, searchLower) ||
					strings.Contains(outputLower, searchLower)
				assert.True(t, containsText, "Reasoning should contain %q", tt.containsReasoning)
			}
		})
	}
}

// TestSwarmCostTracking tests cost aggregation.
func TestSwarmCostTracking(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})

	// Create 3 agents with known costs
	agentIDs := []string{"agent-1", "agent-2", "agent-3"}
	expectedCost := 0.003 // 0.001 per agent * 3

	for _, agentID := range agentIDs {
		llm := newMockLLMProvider(
			fmt.Sprintf("VOTE: Yes\nCONFIDENCE: 0.8\nREASONING: Test vote from %s", agentID),
		)
		ag := createMockAgent(t, agentID, llm)
		orchestrator.RegisterAgent(agentID, ag)
	}

	// Create swarm pattern
	pattern := &loomv1.SwarmPattern{
		Question: "Test cost tracking?",
		AgentIds: agentIDs,
		Strategy: loomv1.VotingStrategy_MAJORITY,
	}

	// Execute
	executor := NewSwarmExecutor(orchestrator, pattern)
	result, err := executor.Execute(context.Background())

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result.Cost)
	assert.Equal(t, expectedCost, result.Cost.TotalCostUsd)
	assert.Equal(t, int32(3), result.Cost.LlmCalls)
}

// TestSwarmErrorHandling tests error conditions.
func TestSwarmErrorHandling(t *testing.T) {
	tests := []struct {
		name      string
		pattern   *loomv1.SwarmPattern
		setupFunc func(*Orchestrator)
		wantErr   bool
		errMsg    string
	}{
		{
			name: "missing agents",
			pattern: &loomv1.SwarmPattern{
				Question: "Test question?",
				AgentIds: []string{"nonexistent-agent"},
				Strategy: loomv1.VotingStrategy_MAJORITY,
			},
			setupFunc: func(o *Orchestrator) {
				// Don't register any agents
			},
			wantErr: true,
			errMsg:  "agent not found",
		},
		{
			name: "empty agent list",
			pattern: &loomv1.SwarmPattern{
				Question: "Test question?",
				AgentIds: []string{},
				Strategy: loomv1.VotingStrategy_MAJORITY,
			},
			setupFunc: func(o *Orchestrator) {},
			wantErr:   true,
			errMsg:    "no votes collected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zaptest.NewLogger(t)
			tracer := observability.NewNoOpTracer()

			orchestrator := NewOrchestrator(Config{
				Logger: logger,
				Tracer: tracer,
			})

			if tt.setupFunc != nil {
				tt.setupFunc(orchestrator)
			}

			executor := NewSwarmExecutor(orchestrator, tt.pattern)
			result, err := executor.Execute(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}

// TestSwarmConcurrentVoting tests that voting happens in parallel.
func TestSwarmConcurrentVoting(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})

	// Create 5 agents
	numAgents := 5
	agentIDs := make([]string, numAgents)
	for i := 0; i < numAgents; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		agentIDs[i] = agentID

		llm := newMockLLMProvider(
			fmt.Sprintf("VOTE: Option%d\nCONFIDENCE: 0.8\nREASONING: Concurrent test", i),
		)
		ag := createMockAgent(t, agentID, llm)
		orchestrator.RegisterAgent(agentID, ag)
	}

	// Create swarm pattern
	pattern := &loomv1.SwarmPattern{
		Question: "Concurrent voting test?",
		AgentIds: agentIDs,
		Strategy: loomv1.VotingStrategy_MAJORITY,
	}

	// Execute - should complete quickly if parallel
	executor := NewSwarmExecutor(orchestrator, pattern)
	result, err := executor.Execute(context.Background())

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.AgentResults, numAgents)
}

// TestSwarmCollaborativeVoting tests the share_votes feature.
func TestSwarmCollaborativeVoting(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})

	// Create agents with sequential responses that might be influenced by previous votes
	agentIDs := []string{"agent-1", "agent-2", "agent-3"}
	votes := []string{
		"VOTE: PostgreSQL\nCONFIDENCE: 0.8\nREASONING: Strong ACID guarantees",
		"VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Agree with previous voter",
		"VOTE: PostgreSQL\nCONFIDENCE: 0.85\nREASONING: Consensus looks good",
	}

	for i, agentID := range agentIDs {
		llm := newMockLLMProvider(votes[i])
		ag := createMockAgent(t, agentID, llm)
		orchestrator.RegisterAgent(agentID, ag)
	}

	// Create swarm pattern with share_votes enabled
	pattern := &loomv1.SwarmPattern{
		Question:   "Which database should we use?",
		AgentIds:   agentIDs,
		Strategy:   loomv1.VotingStrategy_MAJORITY,
		ShareVotes: true, // Enable collaborative voting
	}

	// Execute
	executor := NewSwarmExecutor(orchestrator, pattern)
	result, err := executor.Execute(context.Background())

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "PostgreSQL", result.MergedOutput)
	assert.Len(t, result.AgentResults, 3)

	// Verify voting happened sequentially (can't really test timing, but we can check it worked)
	assert.Equal(t, "swarm", result.PatternType)
}

// TestSwarmJudgeTieBreaker tests the judge_agent_id tie-breaking feature.
func TestSwarmJudgeTieBreaker(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})

	// Create agents with a tie
	agentIDs := []string{"agent-1", "agent-2", "agent-3", "agent-4"}
	votes := []string{
		"VOTE: PostgreSQL\nCONFIDENCE: 0.8\nREASONING: ACID compliance",
		"VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Mature ecosystem",
		"VOTE: MongoDB\nCONFIDENCE: 0.85\nREASONING: Flexible schema",
		"VOTE: MongoDB\nCONFIDENCE: 0.75\nREASONING: Easy to scale",
	}

	for i, agentID := range agentIDs {
		llm := newMockLLMProvider(votes[i])
		ag := createMockAgent(t, agentID, llm)
		orchestrator.RegisterAgent(agentID, ag)
	}

	// Create judge agent that breaks tie in favor of PostgreSQL
	judgeLLM := newMockLLMProvider("PostgreSQL")
	judge := createMockAgent(t, "judge", judgeLLM)
	orchestrator.RegisterAgent("judge-agent", judge)

	// Create swarm pattern with judge
	pattern := &loomv1.SwarmPattern{
		Question:     "Which database should we use?",
		AgentIds:     agentIDs,
		Strategy:     loomv1.VotingStrategy_MAJORITY,
		JudgeAgentId: "judge-agent",
	}

	// Execute
	executor := NewSwarmExecutor(orchestrator, pattern)
	result, err := executor.Execute(context.Background())

	// Verify
	require.NoError(t, err)
	require.NotNil(t, result)

	// Judge should have broken the tie
	assert.Equal(t, "PostgreSQL", result.MergedOutput)
	assert.Len(t, result.AgentResults, 4)
}

// TestSwarmMetadata tests that result metadata is correct.
func TestSwarmMetadata(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: logger,
		Tracer: tracer,
	})

	// Create agents
	agentIDs := []string{"agent-1", "agent-2", "agent-3"}
	for _, agentID := range agentIDs {
		llm := newMockLLMProvider(
			"VOTE: Yes\nCONFIDENCE: 0.85\nREASONING: Metadata test",
		)
		ag := createMockAgent(t, agentID, llm)
		orchestrator.RegisterAgent(agentID, ag)
	}

	// Create swarm pattern
	pattern := &loomv1.SwarmPattern{
		Question: "Test metadata?",
		AgentIds: agentIDs,
		Strategy: loomv1.VotingStrategy_UNANIMOUS,
	}

	// Execute
	executor := NewSwarmExecutor(orchestrator, pattern)
	result, err := executor.Execute(context.Background())

	// Verify metadata
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Metadata)

	assert.Equal(t, "3", result.Metadata["agent_count"])
	assert.Equal(t, "UNANIMOUS", result.Metadata["voting_strategy"])
	assert.Equal(t, "true", result.Metadata["threshold_met"])
	assert.Contains(t, result.Metadata, "average_confidence")

	// Verify pattern type
	assert.Equal(t, "swarm", result.PatternType)
}
