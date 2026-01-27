// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package operator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/internal/agent"
)

// mockCoordinator implements agent.Coordinator for testing
type mockCoordinator struct {
	agents []agent.AgentInfo
	err    error
}

func (m *mockCoordinator) Run(ctx context.Context, sessionID, prompt string, attachments ...interface{}) (interface{}, error) {
	return nil, nil
}

func (m *mockCoordinator) IsBusy(agentID string) bool {
	return false
}

func (m *mockCoordinator) IsSessionBusy(sessionID string) bool {
	return false
}

func (m *mockCoordinator) GetAgentID() string {
	return "test-agent"
}

func (m *mockCoordinator) Cancel() {}

func (m *mockCoordinator) CancelAll() {}

func (m *mockCoordinator) ClearQueue(sessionID string) {}

func (m *mockCoordinator) UpdateModels(ctx context.Context) error {
	return nil
}

func (m *mockCoordinator) Summarize(ctx context.Context, sessionID string) error {
	return nil
}

func (m *mockCoordinator) QueuedPrompts() int {
	return 0
}

func (m *mockCoordinator) QueuedPromptsList(sessionID string) []string {
	return nil
}

func (m *mockCoordinator) ListAgents(ctx context.Context) ([]agent.AgentInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.agents, nil
}

func TestOperator_HandleMessage_NoAgents(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "I need help with SQL")

	require.NoError(t, err)
	assert.Contains(t, response, "don't see any agents available")
	assert.Contains(t, response, "Weaver")
	assert.Empty(t, suggestions)
}

func TestOperator_HandleMessage_SQLQuery(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-expert", Name: "SQL Expert", Status: "ready"},
			{ID: "code-reviewer", Name: "Code Reviewer", Status: "ready"},
			{ID: "data-analyst", Name: "Data Analyst", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "I need help writing a SQL query")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.NotEmpty(t, suggestions)

	// Should suggest SQL Expert first
	assert.Equal(t, "sql-expert", suggestions[0].AgentID)
	assert.Equal(t, "SQL Expert", suggestions[0].Name)
}

func TestOperator_HandleMessage_CodeReview(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-expert", Name: "SQL Expert", Status: "ready"},
			{ID: "code-reviewer", Name: "Code Reviewer", Status: "ready"},
			{ID: "data-analyst", Name: "Data Analyst", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "Can you review my code?")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.NotEmpty(t, suggestions)

	// Should suggest Code Reviewer first
	assert.Equal(t, "code-reviewer", suggestions[0].AgentID)
	assert.Equal(t, "Code Reviewer", suggestions[0].Name)
}

func TestOperator_HandleMessage_NoMatch(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-expert", Name: "SQL Expert", Status: "ready"},
			{ID: "code-reviewer", Name: "Code Reviewer", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "What's the weather today?")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.Empty(t, suggestions)
	assert.Contains(t, response, "not sure which one best fits")
	assert.Contains(t, response, "ctrl+e")
	assert.Contains(t, response, "ctrl+w")
}

func TestOperator_HandleMessage_DirectNameMatch(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "teradata-helper", Name: "Teradata Helper", Status: "ready"},
			{ID: "postgres-helper", Name: "Postgres Helper", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "I need the Teradata helper")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.NotEmpty(t, suggestions)

	// Direct name match should score highest
	assert.Equal(t, "teradata-helper", suggestions[0].AgentID)
}

func TestOperator_HandleMessage_SingleSuggestion(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "api-helper", Name: "API Helper", Status: "ready"},
			{ID: "random-agent", Name: "Random Agent", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "Help me with REST APIs")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.Contains(t, response, "I found an agent that might help")
	assert.Len(t, suggestions, 1)
	assert.Equal(t, "api-helper", suggestions[0].AgentID)
}

func TestOperator_HandleMessage_MultipleSuggestions(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-expert", Name: "SQL Expert", Status: "ready"},
			{ID: "data-analyst", Name: "Data Analyst", Status: "ready"},
			{ID: "code-reviewer", Name: "Code Reviewer", Status: "ready"},
		},
	}

	op := New(coord)
	response, suggestions, err := op.HandleMessage(context.Background(), "analyze this data")

	require.NoError(t, err)
	assert.NotEmpty(t, response)
	assert.Contains(t, response, "I found")
	// Response can be singular or plural depending on matches
	assert.NotEmpty(t, suggestions)
}

func TestOperator_ConversationHistory(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "test-agent", Name: "Test Agent", Status: "ready"},
		},
	}

	op := New(coord)

	// Initially empty
	assert.Empty(t, op.GetConversationHistory())

	// Send a message
	_, _, err := op.HandleMessage(context.Background(), "Hello")
	require.NoError(t, err)

	// Should have one message
	history := op.GetConversationHistory()
	assert.Len(t, history, 1)
	assert.Equal(t, "operator-user", history[0].ID)

	// Send another message
	_, _, err = op.HandleMessage(context.Background(), "Help me with SQL")
	require.NoError(t, err)

	// Should have two messages
	history = op.GetConversationHistory()
	assert.Len(t, history, 2)

	// Clear history
	op.ClearConversationHistory()
	assert.Empty(t, op.GetConversationHistory())
}

func TestOperator_AnalyzeAndSuggest_Scoring(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		agents         []agent.AgentInfo
		expectedFirst  string
		expectedCount  int
		shouldBeEmpty  bool
	}{
		{
			name:  "SQL keyword matches SQL agent",
			query: "write a SQL query",
			agents: []agent.AgentInfo{
				{ID: "sql-agent", Name: "SQL Agent", Status: "ready"},
				{ID: "code-agent", Name: "Code Agent", Status: "ready"},
			},
			expectedFirst: "sql-agent",
			expectedCount: 1,
		},
		{
			name:  "Multiple keyword matches",
			query: "analyze database performance",
			agents: []agent.AgentInfo{
				{ID: "performance-agent", Name: "Performance Agent", Status: "ready"},
				{ID: "data-agent", Name: "Data Agent", Status: "ready"},
				{ID: "code-agent", Name: "Code Agent", Status: "ready"},
			},
			expectedFirst: "data-agent", // "database" keyword matches data category
			expectedCount: 2,            // data and performance should match
		},
		{
			name:  "No matches",
			query: "completely unrelated weather",
			agents: []agent.AgentInfo{
				{ID: "api-agent", Name: "API Agent", Status: "ready"},
				{ID: "deploy-agent", Name: "Deploy Agent", Status: "ready"},
			},
			shouldBeEmpty: true,
		},
		{
			name:  "Case insensitive matching",
			query: "HELP WITH SQL QUERIES",
			agents: []agent.AgentInfo{
				{ID: "sql-agent", Name: "sql agent", Status: "ready"},
			},
			expectedFirst: "sql-agent",
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := New(&mockCoordinator{})
			suggestions := op.analyzeAndSuggest(tt.query, tt.agents)

			if tt.shouldBeEmpty {
				assert.Empty(t, suggestions)
			} else {
				assert.NotEmpty(t, suggestions)
				assert.Equal(t, tt.expectedFirst, suggestions[0].AgentID)
				assert.LessOrEqual(t, len(suggestions), tt.expectedCount)
			}
		})
	}
}

func TestOperator_ListAvailableAgents(t *testing.T) {
	expectedAgents := []agent.AgentInfo{
		{ID: "agent1", Name: "Agent 1", Status: "ready"},
		{ID: "agent2", Name: "Agent 2", Status: "ready"},
	}

	coord := &mockCoordinator{
		agents: expectedAgents,
	}

	op := New(coord)
	agents, err := op.ListAvailableAgents(context.Background())

	require.NoError(t, err)
	assert.Equal(t, expectedAgents, agents)
}

func TestOperator_HandleMessage_Concurrency(t *testing.T) {
	// Test that operator handles concurrent requests safely
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-agent", Name: "SQL Agent", Status: "ready"},
			{ID: "code-agent", Name: "Code Agent", Status: "ready"},
		},
	}

	op := New(coord)

	// Run multiple concurrent queries
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			query := "help with SQL query"
			if idx%2 == 0 {
				query = "review my code"
			}
			_, _, err := op.HandleMessage(context.Background(), query)
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify conversation history was updated correctly
	history := op.GetConversationHistory()
	assert.Len(t, history, 10)
}

func TestOperator_SuggestionConfidence(t *testing.T) {
	coord := &mockCoordinator{
		agents: []agent.AgentInfo{
			{ID: "sql-expert", Name: "SQL Expert", Status: "ready"},
		},
	}

	op := New(coord)

	// High confidence - direct name match
	_, suggestions, _ := op.HandleMessage(context.Background(), "I need the SQL expert")
	if len(suggestions) > 0 {
		assert.Equal(t, "high", suggestions[0].Confidence)
	}

	// Lower confidence - keyword match
	_, suggestions, _ = op.HandleMessage(context.Background(), "help with queries")
	if len(suggestions) > 0 {
		// Confidence should be set based on score
		assert.Contains(t, []string{"high", "medium", "low"}, suggestions[0].Confidence)
	}
}

func TestOperator_SuggestionLimits(t *testing.T) {
	// Create many agents
	agents := make([]agent.AgentInfo, 20)
	for i := 0; i < 20; i++ {
		agents[i] = agent.AgentInfo{
			ID:     strings.ToLower(fmt.Sprintf("sql-agent-%d", i)),
			Name:   fmt.Sprintf("SQL Agent %d", i),
			Status: "ready",
		}
	}

	coord := &mockCoordinator{agents: agents}
	op := New(coord)

	_, suggestions, err := op.HandleMessage(context.Background(), "help with SQL")

	require.NoError(t, err)
	// Should limit to 4 suggestions
	assert.LessOrEqual(t, len(suggestions), 4)
}
