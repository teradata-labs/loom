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
	"fmt"
	"testing"
	"time"
)

// TestParseJudgeVerdict_EdgeCases tests various edge cases in verdict parsing
func TestParseJudgeVerdict_EdgeCases(t *testing.T) {
	judge, err := NewJudge(&Config{Provider: &mockLLMProvider{}})
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	tests := []struct {
		name     string
		response string
		wantErr  bool
		check    func(*testing.T, *Verdict)
	}{
		{
			name: "valid PASS verdict",
			response: `{
				"factual_accuracy": 95,
				"hallucination_score": 5,
				"query_quality": 90,
				"completeness": 92,
				"verdict": "PASS",
				"reasoning": "Excellent execution",
				"issues": []
			}`,
			wantErr: false,
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "PASS" {
					t.Errorf("Expected PASS, got %s", v.Verdict)
				}
				if v.FactualAccuracy != 95 {
					t.Errorf("Expected accuracy 95, got %d", v.FactualAccuracy)
				}
			},
		},
		{
			name: "valid FAIL verdict",
			response: `{
				"factual_accuracy": 45,
				"hallucination_score": 60,
				"query_quality": 50,
				"completeness": 40,
				"verdict": "FAIL",
				"reasoning": "High hallucination detected",
				"issues": ["hallucinated data", "incorrect SQL"]
			}`,
			wantErr: false,
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "FAIL" {
					t.Errorf("Expected FAIL, got %s", v.Verdict)
				}
				if len(v.Issues) != 2 {
					t.Errorf("Expected 2 issues, got %d", len(v.Issues))
				}
			},
		},
		{
			name: "valid PARTIAL verdict",
			response: `{
				"factual_accuracy": 75,
				"hallucination_score": 15,
				"query_quality": 80,
				"completeness": 70,
				"verdict": "PARTIAL",
				"reasoning": "Mostly correct but incomplete",
				"issues": ["missing details"]
			}`,
			wantErr: false,
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "PARTIAL" {
					t.Errorf("Expected PARTIAL, got %s", v.Verdict)
				}
			},
		},
		{
			name: "response with extra text before JSON",
			response: `Here is my evaluation:
			{
				"factual_accuracy": 85,
				"hallucination_score": 10,
				"query_quality": 90,
				"completeness": 88,
				"verdict": "PASS",
				"reasoning": "Good response",
				"issues": []
			}`,
			wantErr: false,
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "PASS" {
					t.Errorf("Expected PASS, got %s", v.Verdict)
				}
			},
		},
		{
			name: "response with text after JSON",
			response: `{
				"factual_accuracy": 85,
				"hallucination_score": 10,
				"query_quality": 90,
				"completeness": 88,
				"verdict": "PASS",
				"reasoning": "Good response",
				"issues": []
			}
			This is a good result.`,
			wantErr: false,
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "PASS" {
					t.Errorf("Expected PASS, got %s", v.Verdict)
				}
			},
		},
		{
			name:     "empty response",
			response: "",
			wantErr:  true,
		},
		{
			name:     "invalid JSON",
			response: `{this is not valid json}`,
			wantErr:  true,
		},
		{
			name:     "missing required fields",
			response: `{"verdict": "PASS"}`,
			wantErr:  false, // Should use default values
			check: func(t *testing.T, v *Verdict) {
				if v.Verdict != "PASS" {
					t.Errorf("Expected PASS, got %s", v.Verdict)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, err := judge.parseJudgeVerdict(tt.response)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tt.check != nil {
				tt.check(t, verdict)
			}
		})
	}
}

// TestBuildJudgePrompt tests prompt construction with various evidence
func TestBuildJudgePrompt_Variations(t *testing.T) {
	judge, err := NewJudge(&Config{Provider: &mockLLMProvider{}})
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	tests := []struct {
		name     string
		evidence *Evidence
		contains []string
	}{
		{
			name: "successful query",
			evidence: &Evidence{
				Query:         "SELECT COUNT(*) FROM users",
				Response:      "There are 100 users in the database",
				Success:       true,
				ExecutionTime: 250,
				Model:         "claude-3-5-sonnet",
			},
			contains: []string{
				"SELECT COUNT(*) FROM users",
				"There are 100 users",
				"✓ Success",
				"250ms",
				"claude-3-5-sonnet",
				"Factual Accuracy",
				"Hallucination Score",
			},
		},
		{
			name: "failed query with error",
			evidence: &Evidence{
				Query:         "SELECT * FROM nonexistent",
				Response:      "",
				ErrorMessage:  "Table 'nonexistent' does not exist",
				Success:       false,
				ExecutionTime: 50,
				Model:         "claude-3-5-sonnet",
			},
			contains: []string{
				"SELECT * FROM nonexistent",
				"✗ Failed",
				"Table 'nonexistent' does not exist",
				"50ms",
			},
		},
		{
			name: "empty response",
			evidence: &Evidence{
				Query:         "SELECT 1",
				Response:      "",
				Success:       false,
				ExecutionTime: 10,
				Model:         "test-model",
			},
			contains: []string{
				"SELECT 1",
				"(No response - execution failed)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := judge.buildJudgePrompt(tt.evidence)

			if len(prompt) == 0 {
				t.Fatal("Generated prompt is empty")
			}

			for _, expected := range tt.contains {
				if !contains(prompt, expected) {
					t.Errorf("Prompt missing expected content: %s", expected)
				}
			}

			t.Logf("Generated prompt length: %d characters", len(prompt))
		})
	}
}

// TestCallJudgeLLM_CredentialDetection tests that the right provider is selected
// Note: This test is now handled at the provider initialization level
func TestCallJudgeLLM_CredentialDetection(t *testing.T) {
	t.Skip("Provider credential detection is now handled at provider initialization, not in judge")
}

// TestJudge_WithMockProvider tests judge with mock provider
func TestJudge_WithMockProvider(t *testing.T) {
	judge, err := NewJudge(&Config{Provider: &mockLLMProvider{}})
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}
	ctx := context.Background()

	evidence := &Evidence{
		Query:         "SELECT 1",
		Response:      "Success",
		Success:       true,
		ExecutionTime: 100,
		Model:         "claude-3-5-sonnet",
	}

	verdict, err := judge.Judge(ctx, evidence)

	// Should succeed with mock provider
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if verdict == nil {
		t.Fatal("Expected verdict, got nil")
	}

	// Verify verdict fields
	if verdict.Verdict != "PASS" {
		t.Errorf("Expected PASS verdict, got %s", verdict.Verdict)
	}

	if verdict.FactualAccuracy != 90 {
		t.Errorf("Expected FactualAccuracy=90, got %d", verdict.FactualAccuracy)
	}

	t.Logf("Verdict: %+v", verdict)
}

// TestVerdict_AllFields tests that all verdict fields work correctly
func TestVerdict_AllFields(t *testing.T) {
	now := time.Now().Unix()

	verdict := &Verdict{
		ID:                 "verdict-123",
		JudgeModel:         "claude-sonnet-4-5-20250929",
		FactualAccuracy:    85,
		HallucinationScore: 10,
		QueryQuality:       90,
		Completeness:       88,
		Verdict:            "PASS",
		Reasoning:          "Well executed query with accurate results",
		Issues:             []string{"minor formatting issue"},
		CreatedAt:          now,
	}

	// Verify all fields are set correctly
	if verdict.ID != "verdict-123" {
		t.Errorf("ID mismatch: expected verdict-123, got %s", verdict.ID)
	}
	if verdict.JudgeModel != "claude-sonnet-4-5-20250929" {
		t.Errorf("JudgeModel mismatch")
	}
	if verdict.FactualAccuracy != 85 {
		t.Errorf("FactualAccuracy mismatch")
	}
	if verdict.HallucinationScore != 10 {
		t.Errorf("HallucinationScore mismatch")
	}
	if verdict.QueryQuality != 90 {
		t.Errorf("QueryQuality mismatch")
	}
	if verdict.Completeness != 88 {
		t.Errorf("Completeness mismatch")
	}
	if verdict.Verdict != "PASS" {
		t.Errorf("Verdict mismatch")
	}
	if verdict.Reasoning != "Well executed query with accurate results" {
		t.Errorf("Reasoning mismatch")
	}
	if len(verdict.Issues) != 1 {
		t.Errorf("Issues length mismatch: expected 1, got %d", len(verdict.Issues))
	}
	if verdict.CreatedAt != now {
		t.Errorf("CreatedAt mismatch")
	}
}

// TestNewJudge_DefaultConfig tests judge creation with nil config
func TestNewJudge_DefaultConfig(t *testing.T) {
	judge, err := NewJudge(nil)

	// Should fail with nil config since provider is required
	if err == nil {
		t.Fatal("Expected error for nil config, got nil")
	}

	if judge != nil {
		t.Error("Expected nil judge when config is nil")
	}

	t.Logf("Got expected error: %v", err)
}

// TestBuildJudgePromptHardcoded tests the hardcoded fallback directly
func TestBuildJudgePromptHardcoded(t *testing.T) {
	judge, err := NewJudge(&Config{Provider: &mockLLMProvider{}})
	if err != nil {
		t.Fatalf("NewJudge failed: %v", err)
	}

	evidence := &Evidence{
		Query:         "SELECT DATABASE()",
		Response:      "Current database: test_db",
		Success:       true,
		ExecutionTime: 50,
		Model:         "claude-3-5-sonnet",
	}

	prompt := judge.buildJudgePromptHardcoded(evidence)

	// Verify key sections are present
	requiredSections := []string{
		"USER QUESTION",
		"EXECUTION RESULT",
		"AGENT'S RESPONSE",
		"EVALUATION TASK",
		"Factual Accuracy",
		"Hallucination Score",
		"Query Quality",
		"Completeness",
		"PASS",
		"FAIL",
		"PARTIAL",
	}

	for _, section := range requiredSections {
		if !contains(prompt, section) {
			t.Errorf("Prompt missing required section: %s", section)
		}
	}

	// Verify evidence data is included
	if !contains(prompt, evidence.Query) {
		t.Error("Query not included in prompt")
	}
	if !contains(prompt, evidence.Response) {
		t.Error("Response not included in prompt")
	}
	if !contains(prompt, fmt.Sprintf("%dms", evidence.ExecutionTime)) {
		t.Error("Execution time not included in prompt")
	}
	if !contains(prompt, evidence.Model) {
		t.Error("Model not included in prompt")
	}
}
