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
	"bufio"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// ConsoleProgressListener listens to progress events and handles user interaction via console
type ConsoleProgressListener struct {
	logger *zap.Logger
	reader *bufio.Reader
}

// NewConsoleProgressListener creates a new console progress listener
func NewConsoleProgressListener(logger *zap.Logger) *ConsoleProgressListener {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &ConsoleProgressListener{
		logger: logger,
		reader: bufio.NewReader(os.Stdin),
	}
}

// OnProgress handles progress events, specifically interactive questions
func (c *ConsoleProgressListener) OnProgress(event *ProgressEvent) {
	switch event.Type {
	case EventSubAgentStarted:
		c.logger.Info("→ Sub-agent started",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message))

	case EventSubAgentCompleted:
		c.logger.Info("✓ Sub-agent completed",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message))

	case EventSubAgentFailed:
		c.logger.Warn("✗ Sub-agent failed",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message),
			zap.String("error", event.Error))

	case EventQuestionAsked:
		c.handleQuestion(event.Question)

	case EventQuestionAnswered:
		c.logger.Info("✓ Clarification received",
			zap.String("message", event.Message))

	case EventValidationStarted:
		c.logger.Info("→ Validation started", zap.String("message", event.Message))

	case EventValidationPassed:
		c.logger.Info("✓ Validation passed", zap.String("message", event.Message))

	case EventValidationFailed:
		c.logger.Warn("✗ Validation failed", zap.String("message", event.Message))

	case EventWorkflowStarted:
		c.logger.Info("→ Workflow started", zap.String("message", event.Message))

	case EventWorkflowCompleted:
		c.logger.Info("✓ Workflow completed", zap.String("message", event.Message))

	case EventAgentStarted:
		c.logger.Info("→ Agent started",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message))

	case EventAgentCompleted:
		c.logger.Info("✓ Agent completed",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message))

	case EventAgentFailed:
		c.logger.Warn("✗ Agent failed",
			zap.String("agent", event.AgentName),
			zap.String("message", event.Message),
			zap.String("error", event.Error))
	}
}

// handleQuestion prompts the user for input and sends the answer back via the answer channel
func (c *ConsoleProgressListener) handleQuestion(question *Question) {
	if question == nil {
		c.logger.Warn("Received nil question")
		return
	}

	// Print question to console
	fmt.Println("\n" + strings.Repeat("=", 70))
	fmt.Println("❓ CLARIFICATION NEEDED")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println()
	fmt.Println(question.Prompt)
	fmt.Println()

	// Show options if provided
	if len(question.Options) > 0 {
		fmt.Println("Options:")
		for i, opt := range question.Options {
			fmt.Printf("  %d. %s\n", i+1, opt)
		}
		fmt.Println()
	}

	// Show context if provided
	if question.Context != "" {
		fmt.Println("Context:")
		fmt.Println(question.Context)
		fmt.Println()
	}

	// Show timeout if set
	if question.Timeout > 0 {
		fmt.Printf("(Timeout: %v)\n\n", question.Timeout)
	}

	// Prompt for answer
	fmt.Print("Your answer: ")

	// Read answer from stdin
	answer, err := c.reader.ReadString('\n')
	if err != nil {
		c.logger.Error("Failed to read answer from stdin", zap.Error(err))
		if question.AnswerChan != nil {
			close(question.AnswerChan)
		}
		return
	}

	// Trim whitespace
	answer = strings.TrimSpace(answer)

	// Send answer back via channel
	if question.AnswerChan != nil {
		select {
		case question.AnswerChan <- answer:
			c.logger.Debug("Answer sent to channel",
				zap.String("question_id", question.ID),
				zap.Int("answer_length", len(answer)))
			// Don't close the channel here - let the receiver handle it
		default:
			c.logger.Warn("Failed to send answer - channel full or closed",
				zap.String("question_id", question.ID))
		}
	} else {
		c.logger.Warn("Question has no answer channel", zap.String("question_id", question.ID))
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println()
}
