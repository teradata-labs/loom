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
	"context"
	"fmt"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/permission"
	"github.com/teradata-labs/loom/internal/pubsub"
)

// Event types for TUI consumption.
// These match Crush's pubsub.Event pattern.

// Topics for TUI events when using Loom's MessageBus.
const (
	TopicMessages    = "tui.messages"
	TopicSessions    = "tui.sessions"
	TopicPermissions = "tui.permissions"
	TopicProgress    = "tui.progress"
)

// StageInfo tracks information about a completed or current execution stage
type StageInfo struct {
	Stage    loomv1.ExecutionStage
	Message  string
	ToolName string // Tool name for TOOL_EXECUTION stages
	Content  string // Actual content for LLM_GENERATION stages
	Failed   bool   // Whether this stage failed
	Done     bool
}

// ProgressToMessageWithHistory converts a WeaveProgress to a message.Message with stage history.
// Shows completed stages with ⏺ and the current stage with ◌ (spinner).
func ProgressToMessageWithHistory(progress *loomv1.WeaveProgress, sessionID string, messageID string, history []StageInfo) message.Message {
	msg := message.NewMessage(
		messageID,
		sessionID,
		message.Assistant,
	)

	// Build multi-line thinking content with stage history
	var thinkingLines []string

	// Add completed stages
	for _, stage := range history {
		var stageText string

		// For tool execution, show tool name with pass/fail status
		if stage.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION && stage.ToolName != "" {
			if stage.Failed {
				stageText = fmt.Sprintf("Tool: %s ✗", stage.ToolName)
			} else {
				stageText = fmt.Sprintf("Tool: %s ✓", stage.ToolName)
			}
		} else if stage.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION && stage.Content != "" {
			// Skip LLM generation in history - the response content is already shown
			// in the message content area below (via PartialContent). No need to
			// repeat it in the thinking/progress history for every tool execution.
			continue
		} else {
			// Use the full message if available
			// Otherwise fall back to the formatted stage name
			stageText = stage.Message
			if stageText == "" {
				stageText = formatStageName(stage.Stage)
			}
			// Add failure indicator for non-tool stages
			if stage.Failed && stageText != "" {
				stageText = "✗ " + stageText
			}
		}

		if stageText != "" {
			thinkingLines = append(thinkingLines, fmt.Sprintf("⏺ %s", stageText))
		}
	}

	// Add current stage with spinner indicator
	if progress.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_UNSPECIFIED &&
		progress.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {

		var progressParts []string
		stageName := formatStageName(progress.Stage)

		// For tool execution, integrate tool name into stage name
		toolMessage := ""
		if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION && progress.ToolName != "" {
			toolMessage = fmt.Sprintf("Executing tool: %s", progress.ToolName)
			progressParts = append(progressParts, toolMessage)
		} else {
			// For other stages, add stage name if present
			if stageName != "" {
				progressParts = append(progressParts, stageName)
			}
			// Add tool name separately for non-tool-execution stages that have tools
			if progress.ToolName != "" && progress.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION {
				progressParts = append(progressParts, progress.ToolName)
			}
		}

		// Add operation message only if it's not redundant with what we already added
		if progress.Message != "" && progress.Message != toolMessage {
			progressParts = append(progressParts, progress.Message)
		}

		// Add progress percentage if available
		if progress.Progress > 0 && progress.Progress < 100 {
			progressParts = append(progressParts, fmt.Sprintf("%d%%", progress.Progress))
		}

		if len(progressParts) > 0 {
			// Don't use static symbols - let the message component show the animated loader
			currentLine := strings.Join(progressParts, " • ")
			if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED {
				currentLine = "✗ " + currentLine
			}
			thinkingLines = append(thinkingLines, currentLine)
		}
	}

	// If we have thinking content, add it
	if len(thinkingLines) > 0 {
		msg.AddPart(message.ReasoningContent{
			Thinking: strings.Join(thinkingLines, "\n"),
		})
	}

	// Add content from partial content (this is the actual response text streaming in)
	if progress.PartialContent != "" {
		msg.AddPart(message.ContentText{Text: progress.PartialContent})
	} else if progress.PartialResult != nil && progress.PartialResult.DataJson != "" {
		msg.AddPart(message.ContentText{Text: progress.PartialResult.DataJson})
	}

	// Mark message as finished when execution completes or fails
	if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
		msg.AddPart(message.FinishPart{
			Reason: message.FinishReasonEndTurn,
			Time:   progress.Timestamp,
		})
	} else if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED {
		msg.AddPart(message.FinishPart{
			Reason:  message.FinishReasonError,
			Message: progress.Message,
			Time:    progress.Timestamp,
		})
	}

	return msg
}

// ProgressToMessage converts a WeaveProgress to a message.Message for display.
// The messageID parameter should be consistent across all progress events for the same turn.
// Deprecated: Use ProgressToMessageWithHistory for better progress tracking.
func ProgressToMessage(progress *loomv1.WeaveProgress, sessionID string, messageID string) message.Message {
	msg := message.NewMessage(
		messageID,
		sessionID,
		message.Assistant,
	)

	// Build progress/thinking message from stage, message, and tool info
	var progressParts []string
	var failed bool
	var completed bool

	if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
		completed = true
	} else if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED {
		failed = true
	}

	// Add stage information
	toolMessage := ""
	if progress.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_UNSPECIFIED {
		stageName := formatStageName(progress.Stage)

		// For tool execution, integrate tool name into stage name
		if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION && progress.ToolName != "" {
			toolMessage = fmt.Sprintf("Executing tool: %s", progress.ToolName)
			progressParts = append(progressParts, toolMessage)
		} else {
			// For other stages, add stage name if present
			if stageName != "" {
				progressParts = append(progressParts, stageName)
			}
			// Add tool name separately for non-tool-execution stages that have tools
			if progress.ToolName != "" && progress.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION {
				progressParts = append(progressParts, progress.ToolName)
			}
		}
	}

	// Add operation message only if it's not redundant with what we already added
	if progress.Message != "" && progress.Message != toolMessage {
		progressParts = append(progressParts, progress.Message)
	}

	// Add progress percentage if available
	if progress.Progress > 0 && progress.Progress < 100 {
		progressParts = append(progressParts, fmt.Sprintf("%d%%", progress.Progress))
	}

	// If we have progress information, add it as reasoning/thinking content
	if len(progressParts) > 0 {
		thinkingText := strings.Join(progressParts, " • ")
		// Only add prefix for failed or completed states
		if failed {
			thinkingText = "✗ " + thinkingText
		} else if completed {
			thinkingText = "⏺ " + thinkingText
		}
		// For in-progress states, let the message component show the animated loader
		msg.AddPart(message.ReasoningContent{
			Thinking: thinkingText,
		})
	}

	// Add content from partial content (this is the actual response text streaming in)
	if progress.PartialContent != "" {
		msg.AddPart(message.ContentText{Text: progress.PartialContent})
	} else if progress.PartialResult != nil && progress.PartialResult.DataJson != "" {
		msg.AddPart(message.ContentText{Text: progress.PartialResult.DataJson})
	}

	// Mark message as finished when execution completes or fails
	if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
		msg.AddPart(message.FinishPart{
			Reason: message.FinishReasonEndTurn,
			Time:   progress.Timestamp,
		})
	} else if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED {
		msg.AddPart(message.FinishPart{
			Reason:  message.FinishReasonError,
			Message: progress.Message,
			Time:    progress.Timestamp,
		})
	}

	return msg
}

// formatStageName converts an ExecutionStage enum to a human-readable string
func formatStageName(stage loomv1.ExecutionStage) string {
	switch stage {
	case loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION:
		return "" // Hidden - pattern selection happens automatically
	case loomv1.ExecutionStage_EXECUTION_STAGE_SCHEMA_DISCOVERY:
		return "Discovering schema"
	case loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION:
		return "Generating response"
	case loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION:
		return "Executing tool"
	case loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP:
		return "Waiting for approval"
	case loomv1.ExecutionStage_EXECUTION_STAGE_GUARDRAIL_CHECK:
		return "Checking guardrails"
	case loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION:
		return "Self-correcting"
	case loomv1.ExecutionStage_EXECUTION_STAGE_FAILED:
		return "Failed"
	default:
		return ""
	}
}

// HITLRequestToPermission converts a HITL request to a permission.PermissionRequest.
func HITLRequestToPermission(hitl *loomv1.HITLRequestInfo) permission.PermissionRequest {
	return permission.PermissionRequest{
		ID:          hitl.RequestId,
		ToolName:    "hitl_request",
		ToolCallID:  hitl.RequestId,
		Description: hitl.Question,
		Priority:    hitl.Priority,
		Timeout:     hitl.TimeoutSeconds,
	}
}

// EventSubscriber defines a function that returns an event channel.
// Matches Crush's subscription pattern.
type EventSubscriber[T any] func(ctx context.Context) <-chan pubsub.Event[T]

// UpdateAvailableMsg is sent when an update is available.
// Matches Crush's pubsub.UpdateAvailableMsg.
type UpdateAvailableMsg struct {
	CurrentVersion string
	LatestVersion  string
	IsDevelopment  bool
}
