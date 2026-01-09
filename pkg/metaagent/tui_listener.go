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
package metaagent

import (
	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"
)

// TUIProgressListener listens to progress events and sends them to the TUI via Bubbletea messages
type TUIProgressListener struct {
	logger  *zap.Logger
	program *tea.Program
}

// NewTUIProgressListener creates a new TUI progress listener
func NewTUIProgressListener(logger *zap.Logger, program *tea.Program) *TUIProgressListener {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &TUIProgressListener{
		logger:  logger,
		program: program,
	}
}

// QuestionAskedMsg is sent when the agent asks a clarification question
type QuestionAskedMsg struct {
	Question *Question
}

// QuestionAnsweredMsg is sent when the user answers a clarification question
type QuestionAnsweredMsg struct {
	QuestionID string
	Answer     string
}

// SubAgentProgressMsg is sent when a sub-agent reports progress
type SubAgentProgressMsg struct {
	AgentName string
	Type      ProgressEventType
	Message   string
	Error     string
	Details   map[string]interface{}
}

// ValidationProgressMsg is sent when validation starts/completes/fails
type ValidationProgressMsg struct {
	Type    ProgressEventType
	Message string
	Score   float64
	Issues  int
}

// OnProgress handles progress events by converting them to Bubbletea messages
func (t *TUIProgressListener) OnProgress(event *ProgressEvent) {
	if t.program == nil {
		t.logger.Warn("TUI progress listener has no program attached, ignoring event",
			zap.String("event_type", string(event.Type)))
		return
	}

	switch event.Type {
	case EventSubAgentStarted, EventSubAgentCompleted, EventSubAgentFailed:
		t.program.Send(SubAgentProgressMsg{
			AgentName: event.AgentName,
			Type:      event.Type,
			Message:   event.Message,
			Error:     event.Error,
			Details:   event.Details,
		})

	case EventQuestionAsked:
		if event.Question != nil {
			t.logger.Info("Question asked, sending to TUI",
				zap.String("question_id", event.Question.ID),
				zap.String("prompt", event.Question.Prompt))
			t.program.Send(QuestionAskedMsg{
				Question: event.Question,
			})
		}

	case EventQuestionAnswered:
		t.logger.Info("Question answered",
			zap.String("message", event.Message))

	case EventValidationStarted, EventValidationPassed, EventValidationFailed:
		score := 0.0
		issues := 0
		if event.Details != nil {
			if s, ok := event.Details["score"].(float64); ok {
				score = s
			}
			if i, ok := event.Details["issues"].(int); ok {
				issues = i
			}
		}
		t.program.Send(ValidationProgressMsg{
			Type:    event.Type,
			Message: event.Message,
			Score:   score,
			Issues:  issues,
		})

	case EventWorkflowStarted, EventWorkflowCompleted:
		// Could add workflow-specific message types if needed
		t.logger.Debug("Workflow event",
			zap.String("type", string(event.Type)),
			zap.String("message", event.Message))

	case EventAgentStarted, EventAgentCompleted, EventAgentFailed:
		// Main agent events
		t.logger.Debug("Agent event",
			zap.String("type", string(event.Type)),
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message))
	}
}
