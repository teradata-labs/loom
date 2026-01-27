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

// Package operator provides a built-in operator for helping users discover agents.
// The operator is NOT a Loom agent - it's built directly into the TUI.
package operator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/teradata-labs/loom/internal/agent"
	"github.com/teradata-labs/loom/internal/message"
)

// AgentSuggestion represents a suggested agent with reasoning.
type AgentSuggestion struct {
	AgentID     string
	Name        string
	Reason      string
	Confidence  string // "high", "medium", "low"
	Description string // From agent metadata
}

// Operator is a built-in helper that assists users in discovering agents.
type Operator struct {
	coordinator      agent.Coordinator
	mu               sync.Mutex
	conversationHist []message.Message
}

// New creates a new Operator instance.
func New(coordinator agent.Coordinator) *Operator {
	return &Operator{
		coordinator:      coordinator,
		conversationHist: make([]message.Message, 0),
	}
}

// HandleMessage processes a user's message and returns a response with optional suggestions.
// This is the core logic for the built-in operator - NOT an LLM agent.
func (o *Operator) HandleMessage(ctx context.Context, prompt string) (response string, suggestions []AgentSuggestion, err error) {
	// Store user message in conversation history (thread-safe)
	o.mu.Lock()
	userMsg := message.NewMessage("operator-user", "operator-session", message.User)
	userMsg.AddPart(message.ContentText{Text: prompt})
	o.conversationHist = append(o.conversationHist, userMsg)
	o.mu.Unlock()

	// Get available agents
	agents, err := o.coordinator.ListAgents(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list agents: %w", err)
	}

	if len(agents) == 0 {
		response = "I don't see any agents available yet. You can create one by talking to Weaver (select it from the sidebar).\n\nOr use keyboard shortcuts:\n• ctrl+e to browse agents\n• ctrl+w to browse workflows"
		return response, nil, nil
	}

	// Analyze query and suggest agents
	suggestions = o.analyzeAndSuggest(prompt, agents)

	if len(suggestions) == 0 {
		// No clear match, provide general guidance
		response = fmt.Sprintf("I found %d agents available, but I'm not sure which one best fits your request.\n\n", len(agents))
		response += "Try being more specific about what you want to do, or:\n"
		response += "• Press ctrl+e to browse all agents\n"
		response += "• Press ctrl+w to browse workflows\n"
		response += "• Talk to Weaver to create a new agent\n\n"
		response += "Available agents:\n"
		for i, ag := range agents {
			if i >= 5 {
				response += fmt.Sprintf("... and %d more\n", len(agents)-5)
				break
			}
			response += fmt.Sprintf("• %s\n", ag.Name)
		}
		return response, nil, nil
	}

	// Build response with suggestions
	if len(suggestions) == 1 {
		response = fmt.Sprintf("I found an agent that might help:\n\n%s\n\nWould you like to switch to this agent?",
			suggestions[0].Name)
	} else {
		response = fmt.Sprintf("I found %d agents that might help. Which one would you like to use?", len(suggestions))
	}

	return response, suggestions, nil
}

// analyzeAndSuggest performs simple keyword matching to suggest agents.
// This is intentionally simple - NOT using LLMs to keep operator lightweight.
func (o *Operator) analyzeAndSuggest(query string, agents []agent.AgentInfo) []AgentSuggestion {
	queryLower := strings.ToLower(query)
	var suggestions []AgentSuggestion

	// Define keyword patterns for common agent types
	keywords := map[string][]string{
		"sql":         {"sql", "query", "database", "teradata", "select", "table", "schema"},
		"code":        {"code", "review", "programming", "debug", "refactor", "function"},
		"data":        {"data", "analyze", "analysis", "statistics", "dataset"},
		"test":        {"test", "testing", "unit test", "integration test", "qa"},
		"document":    {"document", "documentation", "readme", "guide", "docs"},
		"deploy":      {"deploy", "deployment", "release", "production", "ci/cd"},
		"design":      {"design", "architecture", "diagram", "system", "planning"},
		"performance": {"performance", "optimize", "speed", "latency", "throughput"},
		"security":    {"security", "vulnerability", "auth", "authorization", "encryption"},
		"api":         {"api", "rest", "endpoint", "service", "http"},
	}

	// Score each agent based on keyword matches
	type scoredAgent struct {
		agent agent.AgentInfo
		score int
		match string
	}

	var scored []scoredAgent
	for _, ag := range agents {
		agentNameLower := strings.ToLower(ag.Name)
		agentIDLower := strings.ToLower(ag.ID)
		maxScore := 0
		matchedKeyword := ""

		// Check direct name match (highest priority)
		if strings.Contains(queryLower, agentNameLower) || strings.Contains(agentNameLower, queryLower) {
			maxScore = 100
			matchedKeyword = "name match"
		}

		// Check keyword patterns (only if query contains keyword AND agent name contains category)
		for category, words := range keywords {
			categoryScore := 0
			agentMatchesCategory := strings.Contains(agentNameLower, category) || strings.Contains(agentIDLower, category)

			if agentMatchesCategory {
				for _, word := range words {
					if strings.Contains(queryLower, word) {
						categoryScore += 10
					}
				}
			}

			if categoryScore > maxScore {
				maxScore = categoryScore
				matchedKeyword = category
			}
		}

		if maxScore > 0 {
			scored = append(scored, scoredAgent{
				agent: ag,
				score: maxScore,
				match: matchedKeyword,
			})
		}
	}

	// Sort by score (simple bubble sort for small lists)
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Convert top matches to suggestions (limit to 4)
	limit := min(4, len(scored))

	for i := 0; i < limit; i++ {
		sa := scored[i]
		confidence := "high"
		if sa.score < 50 {
			confidence = "medium"
		}
		if sa.score < 20 {
			confidence = "low"
		}

		suggestions = append(suggestions, AgentSuggestion{
			AgentID:     sa.agent.ID,
			Name:        sa.agent.Name,
			Reason:      fmt.Sprintf("Matches %s", sa.match),
			Confidence:  confidence,
			Description: fmt.Sprintf("Agent: %s", sa.agent.Name),
		})
	}

	return suggestions
}

// ListAvailableAgents returns all available agents.
func (o *Operator) ListAvailableAgents(ctx context.Context) ([]agent.AgentInfo, error) {
	return o.coordinator.ListAgents(ctx)
}

// GetConversationHistory returns the operator's conversation history.
func (o *Operator) GetConversationHistory() []message.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Return a copy to avoid race conditions
	result := make([]message.Message, len(o.conversationHist))
	copy(result, o.conversationHist)
	return result
}

// ClearConversationHistory clears the operator's conversation history.
func (o *Operator) ClearConversationHistory() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conversationHist = make([]message.Message, 0)
}
