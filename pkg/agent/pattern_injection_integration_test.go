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

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestPatternInjection_MetaAgent verifies that meta-agents (agents without backends)
// can successfully inject patterns when pattern injection is enabled.
// This tests the fix for weaver agent pattern injection.
func TestPatternInjection_MetaAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp pattern library
	tmpDir := t.TempDir()
	patternFile := filepath.Join(tmpDir, "test-patterns.yaml")

	patternYAML := `# === METADATA START ===
name: workflow_multi_agent
title: "Multi-Agent Workflow Pattern"
description: |
  Design and implement multi-agent workflows with coordinators and sub-agents.
  This pattern helps structure complex workflows where multiple specialized agents
  collaborate to achieve a goal.
category: workflow
difficulty: intermediate
# === METADATA END ===

# === USE_CASES START ===
use_cases:
  - Multi-agent data processing pipelines
  - Distributed task execution
  - Agent collaboration systems
# === USE_CASES END ===

# === PARAMETERS START ===
parameters:
  - name: coordinator_name
    type: string
    required: true
    description: "Name of the coordinator agent"
  - name: sub_agents
    type: array[string]
    required: true
    description: "List of sub-agent names"
# === PARAMETERS END ===

# === TEMPLATES START ===
templates:
  basic_workflow:
    description: "Basic multi-agent workflow structure"
    sql: |
      -- Multi-agent workflow example
      -- Coordinator: {{coordinator_name}}
      -- Sub-agents: {{sub_agents}}
# === TEMPLATES END ===

# === EXAMPLES START ===
examples:
  - name: "Data Processing Workflow"
    description: "Multi-agent data processing"
    parameters:
      coordinator_name: "data-coordinator"
      sub_agents: ["ingestion-agent", "transform-agent", "quality-agent"]
# === EXAMPLES END ===
`

	err := os.WriteFile(patternFile, []byte(patternYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create pattern file: %v", err)
	}

	// Mock LLM that tracks received messages
	mockLLM := &mockPatternTrackingLLM{}

	// Create meta-agent WITHOUT backend (like weaver)
	cfg := DefaultConfig()
	cfg.Name = "test-weaver"
	cfg.Description = "Test meta-agent for workflow design"
	cfg.PatternsDir = tmpDir // Set patterns directory
	cfg.PatternConfig = &PatternConfig{
		Enabled:          true,
		MinConfidence:    0.5,
		UseLLMClassifier: false, // Use keyword-based for deterministic tests
	}

	// Create agent with NO backend (backend=nil)
	ag := NewAgent(
		nil, // No backend - this is a meta-agent
		mockLLM,
		WithConfig(cfg),
	)

	// Verify pattern library loaded
	orch := ag.GetOrchestrator()
	if orch == nil {
		t.Fatal("Orchestrator not initialized")
	}
	lib := orch.GetLibrary()
	if lib == nil {
		t.Fatal("Pattern library not initialized")
	}
	patterns := lib.ListAll()
	t.Logf("Loaded %d patterns: %v", len(patterns), patterns)

	// Test search directly to verify pattern matching
	searchResults := lib.Search("multi-agent workflow")
	t.Logf("Search results for 'multi-agent workflow': %+v", searchResults)

	// Test intent classification
	intent, confidence := orch.ClassifyIntent("Create a multi-agent workflow for data processing", map[string]interface{}{"backend_type": "meta-agent"})
	t.Logf("Intent: %s, Confidence: %.2f", intent, confidence)

	// Test pattern recommendation
	patternName, patternConf := orch.RecommendPattern("Create a multi-agent workflow for data processing", intent)
	t.Logf("Recommended pattern: %s, Confidence: %.2f", patternName, patternConf)

	// Test pattern injection with workflow-related query
	ctx := context.Background()
	resp, err := ag.Chat(ctx, "test_session", "Create a multi-agent workflow for data processing")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	// Verify response was generated
	if resp.Content == "" {
		t.Error("Expected non-empty response")
	}

	// Verify pattern was injected by checking mock LLM received it
	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()

	if len(mockLLM.receivedMessages) == 0 {
		t.Fatal("LLM did not receive any messages")
	}

	// Check that system messages include pattern content
	foundPattern := false
	for _, msg := range mockLLM.receivedMessages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Multi-Agent Workflow Pattern") {
			foundPattern = true
			break
		}
	}

	if !foundPattern {
		t.Error("Pattern was not injected into system messages")
		for i, msg := range mockLLM.receivedMessages {
			t.Logf("Message %d [%s]: %s", i, msg.Role, msg.Content[:min(100, len(msg.Content))])
		}
	}
}

// TestPatternInjection_DataAgent verifies that data agents (agents with backends)
// continue to work correctly with pattern injection after the backend check removal.
func TestPatternInjection_DataAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp pattern library with SQL patterns
	tmpDir := t.TempDir()
	patternFile := filepath.Join(tmpDir, "sql-patterns.yaml")

	patternYAML := `# === METADATA START ===
name: query_optimization
title: "SQL Query Optimization Pattern"
description: |
  Optimize slow SQL queries through systematic analysis and improvement.
category: performance
difficulty: intermediate
# === METADATA END ===

# === USE_CASES START ===
use_cases:
  - Slow query performance tuning
  - Index optimization
  - Query execution plan analysis
# === USE_CASES END ===

# === PARAMETERS START ===
parameters:
  - name: query
    type: string
    required: true
    description: "The query to optimize"
# === PARAMETERS END ===

# === TEMPLATES START ===
templates:
  optimization_steps:
    description: "Query optimization methodology"
    sql: |
      -- Query optimization steps
      -- 1. Check indexes
      -- 2. Analyze execution plan
      -- 3. Consider query rewriting
# === TEMPLATES END ===
`

	err := os.WriteFile(patternFile, []byte(patternYAML), 0644)
	if err != nil {
		t.Fatalf("Failed to create pattern file: %v", err)
	}

	mockLLM := &mockPatternTrackingLLM{}
	mockBackend := &mockBackend{}

	cfg := DefaultConfig()
	cfg.Name = "sql-agent"
	cfg.PatternsDir = tmpDir
	cfg.PatternConfig = &PatternConfig{
		Enabled:          true,
		MinConfidence:    0.5,
		UseLLMClassifier: false,
	}

	// Create agent WITH backend (data agent)
	ag := NewAgent(
		mockBackend,
		mockLLM,
		WithConfig(cfg),
	)

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "test_session", "My query is slow, how do I optimize it?")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp.Content == "" {
		t.Error("Expected non-empty response")
	}

	// Verify pattern injection worked
	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()

	foundPattern := false
	for _, msg := range mockLLM.receivedMessages {
		if msg.Role == "system" && strings.Contains(msg.Content, "SQL Query Optimization Pattern") {
			foundPattern = true
			break
		}
	}

	if !foundPattern {
		t.Error("Pattern was not injected for data agent")
	}
}

// TestPatternInjection_DisabledForMetaAgent verifies that when patterns
// are disabled, meta-agents work correctly without pattern injection.
func TestPatternInjection_DisabledForMetaAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mockLLM := &mockPatternTrackingLLM{}

	cfg := DefaultConfig()
	cfg.PatternConfig = &PatternConfig{
		Enabled: false, // Patterns disabled
	}

	// Meta-agent without backend
	ag := NewAgent(nil, mockLLM, WithConfig(cfg))

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "test_session", "Design a workflow")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if resp.Content == "" {
		t.Error("Expected non-empty response")
	}

	// Verify NO pattern was injected
	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()

	for _, msg := range mockLLM.receivedMessages {
		if strings.Contains(msg.Content, "Pattern") && msg.Role == "system" {
			// Check if it's actually a pattern injection, not just the word "pattern"
			if strings.Contains(msg.Content, "# ") && strings.Contains(msg.Content, "Pattern\n") {
				t.Error("Pattern was injected despite being disabled")
			}
		}
	}
}

// Mock LLM that tracks all received messages for verification
type mockPatternTrackingLLM struct {
	mu               sync.Mutex
	receivedMessages []llmtypes.Message
}

func (m *mockPatternTrackingLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Capture all messages for verification
	m.receivedMessages = append(m.receivedMessages, messages...)

	return &llmtypes.LLMResponse{
		Content:   "Mock response with pattern understanding",
		ToolCalls: []llmtypes.ToolCall{},
		Usage:     llmtypes.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.005},
	}, nil
}

func (m *mockPatternTrackingLLM) Name() string  { return "mock-pattern-tracking" }
func (m *mockPatternTrackingLLM) Model() string { return "mock-v1" }
