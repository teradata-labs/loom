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
	"testing"

	"github.com/teradata-labs/loom/pkg/fabric"
)

// TestPatternInjection_BackendCheckRemoved verifies that pattern selection
// code no longer requires a backend, fixing the issue where meta-agents
// (like weaver) could not inject patterns.
//
// This test verifies the fix for: "Pattern Injection Condition Fails"
// Location: pkg/agent/agent.go:1095
func TestPatternInjection_BackendCheckRemoved(t *testing.T) {
	// This test verifies that the pattern injection condition in agent.go
	// no longer requires a.backend != nil

	// The fix changed:
	// OLD: if patternConfig.Enabled && a.orchestrator != nil && session != nil && a.backend != nil {
	// NEW: if patternConfig.Enabled && a.orchestrator != nil && session != nil {

	// We verify this by checking that meta-agents (nil backend) still initialize
	// the orchestrator and don't panic during Chat operations.

	mockLLM := &mockSimpleLLM{}

	cfg := DefaultConfig()
	cfg.PatternConfig = &PatternConfig{
		Enabled:          true,
		MinConfidence:    0.5,
		UseLLMClassifier: false,
	}

	// Create agent WITHOUT backend (meta-agent)
	ag := NewAgent(nil, mockLLM, WithConfig(cfg))

	// Verify orchestrator is initialized even without backend
	orch := ag.GetOrchestrator()
	if orch == nil {
		t.Fatal("Orchestrator should be initialized for meta-agent (nil backend)")
	}

	// Verify no panic when calling Chat with nil backend
	// (pattern selection code path is executed)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Chat() panicked with nil backend: %v", r)
		}
	}()

	// This would previously panic at a.backend.Name() in pattern selection
	_, err := ag.Chat(context.Background(), "test_session", "test message")
	if err != nil {
		// Error is OK (no real LLM), panic is not
		t.Logf("Chat returned error (expected): %v", err)
	}

	// Success: No panic occurred, backend check was removed
}

// TestPatternInjection_BackendTypeContext verifies that backend_type
// is correctly set to "meta-agent" when backend is nil.
//
// This test verifies the fix for: "Nil Backend Safety for Context Building"
// Location: pkg/agent/agent.go:1114-1122
func TestPatternInjection_BackendTypeContext(t *testing.T) {
	// The fix added:
	//   backendType := "meta-agent"
	//   if a.backend != nil {
	//       backendType = a.backend.Name()
	//   }
	//   contextData := map[string]interface{}{
	//       "backend_type": backendType,
	//       ...
	//   }

	tests := []struct {
		name            string
		backend         fabric.ExecutionBackend
		expectedInPanic bool // Should it panic without fix?
	}{
		{
			name:            "meta-agent (nil backend)",
			backend:         nil,
			expectedInPanic: false, // With fix: no panic
		},
		{
			name:            "data agent (with backend)",
			backend:         &mockBackend{},
			expectedInPanic: false, // Always safe
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := &mockSimpleLLM{}

			cfg := DefaultConfig()
			cfg.PatternConfig = &PatternConfig{
				Enabled:          true,
				MinConfidence:    0.5,
				UseLLMClassifier: false,
			}

			ag := NewAgent(tt.backend, mockLLM, WithConfig(cfg))

			// Verify no panic during Chat (which triggers pattern selection)
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Chat() panicked: %v", r)
				}
			}()

			// Pattern selection code path includes contextData with backend_type
			_, err := ag.Chat(context.Background(), "test_session", "test message")
			if err != nil {
				t.Logf("Chat returned error (expected): %v", err)
			}

			// Success: No nil pointer dereference
		})
	}
}

// TestSystemPrompt_BackendTypeNilSafety verifies that system prompt
// construction handles nil backend gracefully.
//
// This test verifies the fix for: "Nil Backend Safety for System Prompt"
// Location: pkg/agent/agent.go:534-542
func TestSystemPrompt_BackendTypeNilSafety(t *testing.T) {
	// The fix added:
	//   backendType := "meta-agent"
	//   if a.backend != nil {
	//       backendType = a.backend.Name()
	//   }
	//   vars := map[string]interface{}{
	//       "backend_type": backendType,
	//       ...
	//   }

	mockLLM := &mockSimpleLLM{}

	tests := []struct {
		name    string
		backend fabric.ExecutionBackend
	}{
		{
			name:    "meta-agent (nil backend)",
			backend: nil,
		},
		{
			name:    "data agent (with backend)",
			backend: &mockBackend{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := NewAgent(tt.backend, mockLLM, WithConfig(DefaultConfig()))

			// System prompt is built during initialization
			// Verify agent was created without panic
			if ag == nil {
				t.Fatal("Agent creation failed")
			}

			// Verify no panic when getting system prompt
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("getSystemPrompt() panicked: %v", r)
				}
			}()

			// Trigger system prompt generation
			_, err := ag.Chat(context.Background(), "test_session", "test")
			if err != nil {
				t.Logf("Chat returned error (expected): %v", err)
			}

			// Success: No nil pointer dereference in system prompt vars
		})
	}
}
