// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestDebateOrchestrator_ParseAgentResponse(t *testing.T) {
	tests := []struct {
		name            string
		response        string
		expectedPos     string
		expectedArgs    int
		expectedConfMin float64
		expectedConfMax float64
	}{
		{
			name: "well-formatted response",
			response: `POSITION: Use indexes

ARGUMENTS:
1. Faster query performance
2. Reduced I/O operations
3. Better for large tables

CONFIDENCE: 80`,
			expectedPos:     "Use indexes",
			expectedArgs:    3,
			expectedConfMin: 79.0,
			expectedConfMax: 81.0,
		},
		{
			name: "minimal response",
			response: `POSITION: Partitioning is better

CONFIDENCE: 90`,
			expectedPos:     "Partitioning is better",
			expectedArgs:    0,
			expectedConfMin: 89.0,
			expectedConfMax: 91.0,
		},
		{
			name:            "unstructured response",
			response:        `I think we should use columnar storage because it's more efficient for analytics workloads.`,
			expectedPos:     "I think we should use columnar storage because it's more efficient for analytics workloads.",
			expectedArgs:    0,
			expectedConfMin: 74.0, // default is 75.0
			expectedConfMax: 76.0,
		},
		{
			name: "arguments with various formats",
			response: `POSITION: Use materialized views

ARGUMENTS:
1. Pre-computed results
* Faster query times
- Automatic refresh options

CONFIDENCE: 75`,
			expectedPos:     "Use materialized views",
			expectedArgs:    3,
			expectedConfMin: 74.0,
			expectedConfMax: 76.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := &DebateOrchestrator{}
			pos, args, conf := orchestrator.parseAgentResponse(tt.response)

			assert.Equal(t, tt.expectedPos, pos)
			assert.Len(t, args, tt.expectedArgs)
			assert.GreaterOrEqual(t, conf, tt.expectedConfMin)
			assert.LessOrEqual(t, conf, tt.expectedConfMax)
		})
	}
}

func TestDebateOrchestrator_FormatRoundHistory(t *testing.T) {
	orchestrator := &DebateOrchestrator{}
	ctx := context.Background()

	// Create a sample round
	round := &loomv1.DebateRound{
		RoundNumber: 1,
		Positions: []*loomv1.AgentPosition{
			{
				AgentId:    "agent1",
				Position:   "Use indexing",
				Confidence: 0.85,
			},
			{
				AgentId:    "agent2",
				Position:   "Use partitioning",
				Confidence: 0.90,
			},
		},
		Synthesis: "Both approaches have merit",
	}

	// Test without moderator (should use fallback)
	history := orchestrator.formatRoundHistory(ctx, "test-workflow", round, nil)

	assert.Contains(t, history, "agent1")
	assert.Contains(t, history, "Use indexing")
	assert.Contains(t, history, "85%")
	assert.Contains(t, history, "agent2")
	assert.Contains(t, history, "Use partitioning")
	assert.Contains(t, history, "90%")
	assert.Contains(t, history, "Both approaches have merit")
}

func TestDebateOrchestrator_BuildDebateContext(t *testing.T) {
	orchestrator := &DebateOrchestrator{}

	topic := "What is the best database?"

	// Round 1 (no history)
	context1 := orchestrator.buildDebateContext(topic, 1, nil)
	assert.Contains(t, context1, topic)
	assert.Contains(t, context1, "Round 1")
	assert.Contains(t, context1, "opening round")

	// Round 2 (with history)
	history := []string{"Round 1: agents debated indexes vs partitioning"}
	context2 := orchestrator.buildDebateContext(topic, 2, history)
	assert.Contains(t, context2, topic)
	assert.Contains(t, context2, "Round 2")
	assert.Contains(t, context2, "Previous Rounds")
	assert.Contains(t, context2, "Round 1: agents debated indexes vs partitioning")
}

func TestDebateOrchestrator_SynthesizeRound(t *testing.T) {
	orchestrator := &DebateOrchestrator{}

	round := &loomv1.DebateRound{
		RoundNumber: 1,
		Positions: []*loomv1.AgentPosition{
			{
				AgentId:    "agent1",
				Position:   "Indexes are best",
				Confidence: 0.80,
			},
			{
				AgentId:    "agent2",
				Position:   "Partitioning is best",
				Confidence: 0.85,
			},
		},
	}

	synthesis := orchestrator.synthesizeRound(round)

	assert.NotEmpty(t, synthesis)
	assert.Contains(t, synthesis, "Indexes are best")
	assert.Contains(t, synthesis, "Partitioning is best")
	assert.Contains(t, synthesis, "80%")
	assert.Contains(t, synthesis, "85%")
}

func TestDebateOrchestrator_CheckConsensus(t *testing.T) {
	orchestrator := &DebateOrchestrator{}

	tests := []struct {
		name      string
		round     *loomv1.DebateRound
		consensus bool
	}{
		{
			name: "high confidence consensus",
			round: &loomv1.DebateRound{
				Positions: []*loomv1.AgentPosition{
					{Confidence: 0.85},
					{Confidence: 0.90},
					{Confidence: 0.88},
				},
			},
			consensus: true,
		},
		{
			name: "low confidence no consensus",
			round: &loomv1.DebateRound{
				Positions: []*loomv1.AgentPosition{
					{Confidence: 0.60},
					{Confidence: 0.70},
					{Confidence: 0.65},
				},
			},
			consensus: false,
		},
		{
			name: "mixed confidence at threshold",
			round: &loomv1.DebateRound{
				Positions: []*loomv1.AgentPosition{
					{Confidence: 0.80},
					{Confidence: 0.80},
				},
			},
			consensus: true, // avg = 0.80, threshold = 0.80
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := orchestrator.checkConsensus(tt.round)
			assert.Equal(t, tt.consensus, result)
		})
	}
}

func TestDebateOrchestrator_SummarizePosition(t *testing.T) {
	orchestrator := &DebateOrchestrator{}
	ctx := context.Background()

	tests := []struct {
		name             string
		position         string
		arguments        []string
		expectTruncation bool
		checkContent     []string
	}{
		{
			name:             "short position no truncation",
			position:         "Use indexes for better performance.",
			arguments:        []string{"Faster queries", "Lower I/O"},
			expectTruncation: false,
			checkContent:     []string{"Use indexes for better performance.", "Key points:", "Faster queries", "Lower I/O"},
		},
		{
			name:             "long position gets truncated",
			position:         "I believe we should implement a comprehensive indexing strategy that involves creating multiple types of indexes including B-tree indexes for general purpose queries, bitmap indexes for low cardinality columns, and full-text indexes for text search capabilities. This approach will give us the best overall performance across different query patterns and workload types while maintaining reasonable storage overhead.",
			arguments:        []string{"Better query performance across all workload types", "Moderate storage overhead compared to alternatives"},
			expectTruncation: true,
			checkContent:     []string{"...", "Key points:", "Better query performance"},
		},
		{
			name:             "position without arguments",
			position:         "Partitioning is the way to go.",
			arguments:        nil,
			expectTruncation: false,
			checkContent:     []string{"Partitioning is the way to go."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test without moderator (should use fallback truncation)
			result := orchestrator.summarizePosition(ctx, "test-workflow", "agent1", tt.position, tt.arguments, nil)

			for _, content := range tt.checkContent {
				assert.Contains(t, result, content)
			}

			if tt.expectTruncation {
				assert.Less(t, len(result), len(tt.position), "Should be shorter than original")
			}

			// Should never exceed reasonable length (500 chars for position + 2 args)
			assert.LessOrEqual(t, len(result), 600, "Summary should be reasonably sized")
		})
	}
}

func TestDebateOrchestrator_GeneratePerspectiveGuidance(t *testing.T) {
	orchestrator := &DebateOrchestrator{}

	tests := []struct {
		name          string
		agentID       string
		checkKeywords []string
	}{
		{
			name:          "performance agent",
			agentID:       "td-expert-performance",
			checkKeywords: []string{"Performance", "performance", "speed", "efficiency"},
		},
		{
			name:          "analytics agent",
			agentID:       "td-expert-analytics",
			checkKeywords: []string{"Analytics", "data analysis", "statistical"},
		},
		{
			name:          "quality agent",
			agentID:       "td-expert-quality",
			checkKeywords: []string{"Quality", "correctness", "reliability", "testing"},
		},
		{
			name:          "architecture agent",
			agentID:       "td-expert-architecture",
			checkKeywords: []string{"Architecture", "system design", "modularity"},
		},
		{
			name:          "unknown agent gets generic guidance",
			agentID:       "some-random-agent-xyz",
			checkKeywords: []string{"perspective", "unique", "some-random-agent-xyz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guidance := orchestrator.generatePerspectiveGuidance(tt.agentID)

			assert.NotEmpty(t, guidance)

			// Check that at least one keyword appears
			foundKeyword := false
			for _, keyword := range tt.checkKeywords {
				if assert.Contains(t, guidance, keyword) {
					foundKeyword = true
					break
				}
			}
			assert.True(t, foundKeyword, "Should contain at least one expected keyword")
		})
	}
}
