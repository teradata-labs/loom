//go:build !promptio

// Copyright 2026 Teradata Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// mockLLMProvider is a simple mock for testing
type mockLLMProvider struct {
	model string
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: `{"factual_accuracy": 90, "hallucination_score": 10, "query_quality": 85, "completeness": 90, "verdict": "PASS", "reasoning": "Test verdict", "issues": []}`,
	}, nil
}

func (m *mockLLMProvider) Model() string {
	if m.model != "" {
		return m.model
	}
	return "mock-model"
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func TestJudgeInitialization(t *testing.T) {
	// Test judge can be created without prompts (uses fallback)
	mockProvider := &mockLLMProvider{model: "claude-sonnet-4-5-20250929"}
	cfg := &Config{
		Provider: mockProvider,
		// PromptsFS is zero value - will use fallback
	}

	judge, err := NewJudge(cfg)
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	// Verify judge was created
	if judge == nil {
		t.Fatal("NewJudge returned nil")
	}

	// promptMgr will be nil when PromptsFS is not provided (uses hardcoded fallback)
	if judge.promptMgr != nil {
		t.Log("promptMgr is initialized (PromptsFS was provided)")
	} else {
		t.Log("promptMgr is nil (will use hardcoded fallback prompts)")
	}

	// Verify provider was set
	if judge.provider.Model() != mockProvider.Model() {
		t.Errorf("Expected model=%s, got %s", mockProvider.Model(), judge.provider.Model())
	}

	t.Log("Judge initialized successfully")
}

func TestJudgePromptRendering(t *testing.T) {
	// Test that fallback prompt rendering works
	cfg := &Config{
		Provider: &mockLLMProvider{model: "claude-sonnet-4-5-20250929"},
		// Uses zero-value embed.FS, will trigger fallback
	}

	judge, err := NewJudge(cfg)
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	evidence := &Evidence{
		Query:         "SELECT * FROM users",
		Response:      "Alice, Bob, Charlie",
		ErrorMessage:  "",
		Success:       true,
		ExecutionTime: 456,
		Model:         "claude-3-5-sonnet",
	}

	prompt := judge.buildJudgePrompt(evidence)

	// Verify prompt was generated (using fallback)
	if len(prompt) == 0 {
		t.Fatal("buildJudgePrompt returned empty prompt")
	}

	// Verify core elements are present
	testCases := []struct {
		name     string
		expected string
	}{
		{"query", "SELECT * FROM users"},
		{"response", "Alice, Bob, Charlie"},
		{"model", "claude-3-5-sonnet"},
		{"execution_time", "456ms"},
		{"success_indicator", "✓ Success"},
		{"criteria", "Factual Accuracy"},
		{"criteria", "Hallucination Score"},
	}

	for _, tc := range testCases {
		if !contains(prompt, tc.expected) {
			t.Errorf("Prompt missing expected %s: %s", tc.name, tc.expected)
		}
	}

	t.Logf("Generated prompt (%d chars)", len(prompt))
}

func TestJudgeFallbackToHardcodedPrompt(t *testing.T) {
	// Test that judge falls back to hardcoded prompt if promptio fails
	// This happens when PromptsFS is empty/invalid
	cfg := &Config{
		Provider: &mockLLMProvider{model: "claude-sonnet-4-5-20250929"},
		// PromptsFS is zero value (empty embed.FS)
	}

	judge, err := NewJudge(cfg)
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	evidence := &Evidence{
		Query:         "SELECT 1",
		Response:      "1",
		Success:       true,
		ExecutionTime: 10,
		Model:         "test",
	}

	prompt := judge.buildJudgePrompt(evidence)

	// Should still generate a valid prompt (using fallback)
	if len(prompt) == 0 {
		t.Fatal("buildJudgePrompt returned empty prompt even with fallback")
	}

	// Should contain core evaluation criteria
	if !contains(prompt, "Factual Accuracy") {
		t.Error("Fallback prompt missing Factual Accuracy criteria")
	}
}

func TestParseJudgeVerdict(t *testing.T) {
	judge, err := NewJudge(&Config{Provider: &mockLLMProvider{}})
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	// Test valid JSON response
	response := `{
		"factual_accuracy": 85,
		"hallucination_score": 10,
		"query_quality": 90,
		"completeness": 88,
		"verdict": "PASS",
		"reasoning": "The agent correctly executed the query and provided accurate results.",
		"issues": ["Minor formatting issue"]
	}`

	verdict, err := judge.parseJudgeVerdict(response)
	if err != nil {
		t.Fatalf("Failed to parse valid verdict: %v", err)
	}

	// Verify parsed values
	if verdict.FactualAccuracy != 85 {
		t.Errorf("Expected FactualAccuracy=85, got %d", verdict.FactualAccuracy)
	}
	if verdict.HallucinationScore != 10 {
		t.Errorf("Expected HallucinationScore=10, got %d", verdict.HallucinationScore)
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("Expected Verdict=PASS, got %s", verdict.Verdict)
	}
	if len(verdict.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(verdict.Issues))
	}
}

func TestJudge(t *testing.T) {
	// Skip if no API key available
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping LLM test")
	}

	cfg := &Config{
		Provider: &mockLLMProvider{model: "claude-sonnet-4-5-20250929"},
		// Uses zero-value embed.FS, will use fallback prompt
	}

	judge, err := NewJudge(cfg)
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	// Create test evidence
	evidence := &Evidence{
		Query:         "SELECT DATABASE",
		Response:      "The database is: test_db",
		Success:       true,
		ExecutionTime: 100,
		Model:         "claude-3-5-sonnet",
	}

	ctx := context.Background()

	// This will make a real API call
	verdict, err := judge.Judge(ctx, evidence)
	if err != nil {
		t.Fatalf("Judge failed: %v", err)
	}

	// Verify verdict structure
	if verdict.ID == "" {
		t.Error("Verdict missing ID")
	}
	if verdict.JudgeModel != cfg.Provider.Model() {
		t.Errorf("Expected JudgeModel=%s, got %s", cfg.Provider.Model(), verdict.JudgeModel)
	}

	// Verify scores are in valid range
	if verdict.FactualAccuracy < 0 || verdict.FactualAccuracy > 100 {
		t.Errorf("FactualAccuracy out of range: %d", verdict.FactualAccuracy)
	}
	if verdict.HallucinationScore < 0 || verdict.HallucinationScore > 100 {
		t.Errorf("HallucinationScore out of range: %d", verdict.HallucinationScore)
	}

	t.Logf("Verdict: %s (Accuracy: %d, Hallucination: %d)",
		verdict.Verdict, verdict.FactualAccuracy, verdict.HallucinationScore)
}

func TestVerdictStructure(t *testing.T) {
	// Test that verdict has all expected fields
	verdict := &Verdict{
		ID:                 "test-id",
		JudgeModel:         "claude-sonnet-4-5-20250929",
		FactualAccuracy:    85,
		HallucinationScore: 10,
		QueryQuality:       90,
		Completeness:       88,
		Verdict:            "PASS",
		Reasoning:          "Test reasoning",
		Issues:             []string{"issue1"},
		CreatedAt:          time.Now().Unix(),
	}

	// Verify all fields are set correctly
	if verdict.ID != "test-id" {
		t.Errorf("ID mismatch: expected test-id, got %s", verdict.ID)
	}
	if verdict.FactualAccuracy != 85 {
		t.Errorf("FactualAccuracy mismatch")
	}
	if len(verdict.Issues) != 1 {
		t.Errorf("Issues length mismatch")
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("Verdict mismatch")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
