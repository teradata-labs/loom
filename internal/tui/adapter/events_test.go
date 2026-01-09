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
package adapter

import (
	"strings"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/message"
)

func TestProgressToMessageWithHistory(t *testing.T) {
	tests := []struct {
		name           string
		progress       *loomv1.WeaveProgress
		history        []StageInfo
		wantThinking   bool
		wantContent    bool
		wantIndicators []string // Expected indicators in thinking content
	}{
		{
			name: "current stage with no history",
			progress: &loomv1.WeaveProgress{
				Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
				Message: "Analyzing query",
			},
			history:        []StageInfo{},
			wantThinking:   true,
			wantContent:    false,
			wantIndicators: []string{"Generating response", "Analyzing query"}, // No ◌ - animated loader shown by message component
		},
		{
			name: "current stage with history",
			progress: &loomv1.WeaveProgress{
				Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
				ToolName: "execute_sql",
				Message:  "Running query",
			},
			history: []StageInfo{
				{Stage: loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION, Done: true},
				{Stage: loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION, Done: true},
			},
			wantThinking:   true,
			wantContent:    false,
			wantIndicators: []string{"⏺", "Generating response", "Executing tool: execute_sql"}, // Pattern selection hidden, tool name integrated, no ◌
		},
		{
			name: "with partial content",
			progress: &loomv1.WeaveProgress{
				Stage:          loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
				PartialContent: "The answer is 42",
			},
			history:        []StageInfo{},
			wantThinking:   true,
			wantContent:    true,
			wantIndicators: []string{"Generating response"}, // No ◌ - animated loader shown by message component
		},
		{
			name: "with progress percentage",
			progress: &loomv1.WeaveProgress{
				Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
				Progress: 45,
				Message:  "Processing rows",
			},
			history:        []StageInfo{},
			wantThinking:   true,
			wantContent:    false,
			wantIndicators: []string{"Executing tool", "45%"}, // No ◌ - animated loader shown by message component
		},
		{
			name: "completed stage",
			progress: &loomv1.WeaveProgress{
				Stage:          loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
				PartialContent: "Done!",
			},
			history:      []StageInfo{},
			wantThinking: false,
			wantContent:  true,
		},
		{
			name: "failed stage",
			progress: &loomv1.WeaveProgress{
				Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_FAILED,
				Message: "Connection timeout",
			},
			history:        []StageInfo{},
			wantThinking:   true,
			wantContent:    false,
			wantIndicators: []string{"✗", "Failed", "Connection timeout"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ProgressToMessageWithHistory(tt.progress, "sess_123", "msg_123", tt.history)

			if msg.ID != "msg_123" {
				t.Errorf("Expected message ID msg_123, got %s", msg.ID)
			}

			if msg.SessionID != "sess_123" {
				t.Errorf("Expected session ID sess_123, got %s", msg.SessionID)
			}

			if msg.Role != message.Assistant {
				t.Errorf("Expected role Assistant, got %v", msg.Role)
			}

			// Check thinking content
			thinkingContent := msg.ReasoningContent().Thinking
			hasThinking := thinkingContent != ""

			if hasThinking != tt.wantThinking {
				t.Errorf("Expected thinking=%v, got thinking=%v (content: %s)", tt.wantThinking, hasThinking, thinkingContent)
			}

			// Check for expected indicators in thinking content
			if tt.wantThinking {
				for _, indicator := range tt.wantIndicators {
					if !strings.Contains(thinkingContent, indicator) {
						t.Errorf("Expected thinking content to contain %q, got: %s", indicator, thinkingContent)
					}
				}
			}

			// Check actual content
			contentText := msg.Content().Text
			hasContent := contentText != ""

			if hasContent != tt.wantContent {
				t.Errorf("Expected content=%v, got content=%v (text: %s)", tt.wantContent, hasContent, contentText)
			}

			if tt.wantContent && tt.progress.PartialContent != "" {
				if contentText != tt.progress.PartialContent {
					t.Errorf("Expected content %q, got %q", tt.progress.PartialContent, contentText)
				}
			}
		})
	}
}

func TestProgressToMessageWithHistoryMultiline(t *testing.T) {
	// Test that history creates multi-line thinking content
	progress := &loomv1.WeaveProgress{
		Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
		Message: "Processing",
	}
	history := []StageInfo{
		{Stage: loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION, Done: true}, // This stage is now hidden
		{Stage: loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION, Done: true},
	}

	msg := ProgressToMessageWithHistory(progress, "sess_1", "msg_1", history)
	thinking := msg.ReasoningContent().Thinking

	// Should have newlines separating stages (pattern selection is hidden, so we expect 2 lines now)
	lines := strings.Split(thinking, "\n")
	if len(lines) < 2 {
		t.Errorf("Expected at least 2 lines (1 visible history + 1 current), got %d: %s", len(lines), thinking)
	}

	// First line should have ⏺ (completed) - this is the LLM generation stage
	if !strings.HasPrefix(lines[0], "⏺") {
		t.Errorf("Line 0 should start with ⏺, got: %s", lines[0])
	}

	// Last line should NOT have ◌ (in-progress) - animated loader shown by message component
	lastLine := lines[len(lines)-1]
	if strings.HasPrefix(lastLine, "◌") {
		t.Errorf("Last line should NOT start with ◌ (animated loader shown separately), got: %s", lastLine)
	}
	// Should contain the tool execution text
	if !strings.Contains(lastLine, "Executing tool") {
		t.Errorf("Last line should contain 'Executing tool', got: %s", lastLine)
	}
}

func TestFormatStageName(t *testing.T) {
	tests := []struct {
		stage loomv1.ExecutionStage
		want  string
	}{
		{loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION, ""}, // Hidden
		{loomv1.ExecutionStage_EXECUTION_STAGE_SCHEMA_DISCOVERY, "Discovering schema"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION, "Generating response"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION, "Executing tool"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP, "Waiting for approval"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_GUARDRAIL_CHECK, "Checking guardrails"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION, "Self-correcting"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_FAILED, "Failed"},
		{loomv1.ExecutionStage_EXECUTION_STAGE_UNSPECIFIED, ""},
		{loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED, ""},
	}

	for _, tt := range tests {
		t.Run(tt.stage.String(), func(t *testing.T) {
			got := formatStageName(tt.stage)
			if got != tt.want {
				t.Errorf("formatStageName(%v) = %q, want %q", tt.stage, got, tt.want)
			}
		})
	}
}

func TestProgressToMessage(t *testing.T) {
	// Test the deprecated function still works
	progress := &loomv1.WeaveProgress{
		Stage:          loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
		Message:        "Thinking",
		PartialContent: "Response text",
	}

	msg := ProgressToMessage(progress, "sess_1", "msg_1")

	if msg.ID != "msg_1" {
		t.Errorf("Expected message ID msg_1, got %s", msg.ID)
	}

	if msg.SessionID != "sess_1" {
		t.Errorf("Expected session ID sess_1, got %s", msg.SessionID)
	}

	// Should have thinking content (without ◌ - animated loader shown by message component)
	thinking := msg.ReasoningContent().Thinking
	if thinking == "" {
		t.Errorf("Expected thinking content to be present, got empty string")
	}
	if strings.Contains(thinking, "◌") {
		t.Errorf("Expected thinking to NOT contain ◌ (animated loader shown separately), got: %s", thinking)
	}

	// Should have partial content
	content := msg.Content().Text
	if content != "Response text" {
		t.Errorf("Expected content 'Response text', got %q", content)
	}
}

func TestHITLRequestToPermission(t *testing.T) {
	hitl := &loomv1.HITLRequestInfo{
		RequestId:      "req_123",
		Question:       "Approve this action?",
		Priority:       "high",
		TimeoutSeconds: 60,
	}

	perm := HITLRequestToPermission(hitl)

	if perm.ID != hitl.RequestId {
		t.Errorf("Expected ID %s, got %s", hitl.RequestId, perm.ID)
	}

	if perm.ToolName != "hitl_request" {
		t.Errorf("Expected ToolName hitl_request, got %s", perm.ToolName)
	}

	if perm.ToolCallID != hitl.RequestId {
		t.Errorf("Expected ToolCallID %s, got %s", hitl.RequestId, perm.ToolCallID)
	}

	if perm.Description != hitl.Question {
		t.Errorf("Expected Description %s, got %s", hitl.Question, perm.Description)
	}

	if perm.Priority != hitl.Priority {
		t.Errorf("Expected Priority %s, got %s", hitl.Priority, perm.Priority)
	}

	if perm.Timeout != hitl.TimeoutSeconds {
		t.Errorf("Expected Timeout %d, got %d", hitl.TimeoutSeconds, perm.Timeout)
	}
}

func TestProgressToMessageWithPartialResult(t *testing.T) {
	// Test that PartialResult.DataJson is used when PartialContent is empty
	progress := &loomv1.WeaveProgress{
		Stage: loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
		PartialResult: &loomv1.ExecutionResult{
			DataJson: `{"result": "success"}`,
		},
	}

	msg := ProgressToMessageWithHistory(progress, "sess_1", "msg_1", []StageInfo{})

	content := msg.Content().Text
	if content != `{"result": "success"}` {
		t.Errorf("Expected JSON content, got %q", content)
	}
}

func TestStageInfoCreation(t *testing.T) {
	// Test StageInfo struct
	stage := StageInfo{
		Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION,
		Message: "Selected pattern X",
		Done:    true,
	}

	if stage.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION {
		t.Errorf("Expected pattern selection stage, got %v", stage.Stage)
	}

	if !stage.Done {
		t.Error("Expected Done to be true")
	}
}

func TestToolExecutionWithPassFailStatus(t *testing.T) {
	tests := []struct {
		name              string
		progress          *loomv1.WeaveProgress
		history           []StageInfo
		wantToolIndicator string
	}{
		{
			name: "successful tool execution",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
			},
			history: []StageInfo{
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "execute_sql",
					Failed:   false,
					Done:     true,
				},
			},
			wantToolIndicator: "⏺ Tool: execute_sql ✓",
		},
		{
			name: "failed tool execution",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION,
			},
			history: []StageInfo{
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "execute_sql",
					Failed:   true,
					Done:     true,
				},
			},
			wantToolIndicator: "⏺ Tool: execute_sql ✗",
		},
		{
			name: "multiple tools with mixed success",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
			},
			history: []StageInfo{
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "get_schema",
					Failed:   false,
					Done:     true,
				},
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "execute_sql",
					Failed:   true,
					Done:     true,
				},
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "execute_sql",
					Failed:   false,
					Done:     true,
				},
			},
			wantToolIndicator: "⏺ Tool: execute_sql ✓", // Last tool succeeds
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ProgressToMessageWithHistory(tt.progress, "sess_1", "msg_1", tt.history)
			thinking := msg.ReasoningContent().Thinking

			if !strings.Contains(thinking, tt.wantToolIndicator) {
				t.Errorf("Expected thinking to contain %q, got: %s", tt.wantToolIndicator, thinking)
			}
		})
	}
}

func TestLLMGenerationWithActualContent(t *testing.T) {
	tests := []struct {
		name          string
		progress      *loomv1.WeaveProgress
		history       []StageInfo
		wantIndicator string
	}{
		{
			name: "LLM response skipped in history (shown in current stage)",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
			},
			history: []StageInfo{
				{
					Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
					Content: "I'll help you query the database",
					Done:    true,
				},
			},
			wantIndicator: "Executing tool",
		},
		{
			name: "LLM response skipped (not shown after tool execution)",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
			},
			history: []StageInfo{
				{
					Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
					Content: "This is a very long response that should not be shown in thinking history",
					Done:    true,
				},
			},
			wantIndicator: "Executing tool",
		},
		{
			name: "LLM response with newlines skipped in history",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
			},
			history: []StageInfo{
				{
					Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
					Content: "I'll execute the SQL query.\n\nHere are the results:\n- Row 1\n- Row 2",
					Done:    true,
				},
			},
			wantIndicator: "Executing tool",
		},
		{
			name: "mixed stages with LLM responses and tools (only tools shown)",
			progress: &loomv1.WeaveProgress{
				Stage: loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
			},
			history: []StageInfo{
				{
					Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
					Content: "Let me check the schema first",
					Done:    true,
				},
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "get_schema",
					Failed:   false,
					Done:     true,
				},
				{
					Stage:   loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
					Content: "Now I'll run the query",
					Done:    true,
				},
				{
					Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
					ToolName: "execute_sql",
					Failed:   false,
					Done:     true,
				},
			},
			wantIndicator: "⏺ Tool: execute_sql ✓",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ProgressToMessageWithHistory(tt.progress, "sess_1", "msg_1", tt.history)
			thinking := msg.ReasoningContent().Thinking

			if !strings.Contains(thinking, tt.wantIndicator) {
				t.Errorf("Expected thinking to contain %q, got:\n%s", tt.wantIndicator, thinking)
			}
		})
	}
}
