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
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/agent"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// CoordinatorAdapter wraps the gRPC client for agent coordination.
// Implements agent.Coordinator interface.
type CoordinatorAdapter struct {
	client  *client.Client
	events  chan<- tea.Msg
	agentID string // Default agent ID

	mu                   sync.Mutex
	busyAgents           map[string]bool               // Track busy state per agent ID
	agentCancelFunc      map[string]context.CancelFunc // Track cancel functions per agent
	sessionToAgent       map[string]string             // Map session ID to agent ID
	sessionSubscriptions map[string]context.CancelFunc // Track active session subscriptions
}

// NewCoordinatorAdapter creates a new coordinator adapter.
func NewCoordinatorAdapter(c *client.Client, events chan<- tea.Msg) *CoordinatorAdapter {
	return &CoordinatorAdapter{
		client:               c,
		events:               events,
		busyAgents:           make(map[string]bool),
		agentCancelFunc:      make(map[string]context.CancelFunc),
		sessionToAgent:       make(map[string]string),
		sessionSubscriptions: make(map[string]context.CancelFunc),
	}
}

// SetAgentID sets the default agent ID for operations.
func (c *CoordinatorAdapter) SetAgentID(agentID string) {
	c.agentID = agentID
}

// GetAgentID returns the current agent ID.
func (c *CoordinatorAdapter) GetAgentID() string {
	return c.agentID
}

// AgentResult represents the result of an agent run.
type AgentResult struct {
	SessionID string
	Response  string
	Cost      *CostInfo
	Error     error
}

// CostInfo represents cost information.
type CostInfo struct {
	TotalCost    float64
	InputTokens  int32
	OutputTokens int32
	Provider     string
	Model        string
}

// Run starts agent processing for a prompt.
// Implements agent.Coordinator interface.
// Attachments[0] can be the agent ID if provided.
func (c *CoordinatorAdapter) Run(ctx context.Context, sessionID, prompt string, attachments ...any) (any, error) {
	// Get agent ID from attachments or use default
	agentID := c.agentID
	if len(attachments) > 0 {
		if id, ok := attachments[0].(string); ok && id != "" {
			agentID = id
		}
	}

	// Check if THIS specific agent is busy
	c.mu.Lock()
	if c.busyAgents[agentID] {
		c.mu.Unlock()
		return nil, ErrAgentBusy
	}

	// Mark THIS agent as busy and track session-to-agent mapping
	runCtx, cancel := context.WithCancel(ctx)
	c.busyAgents[agentID] = true
	c.agentCancelFunc[agentID] = cancel
	c.sessionToAgent[sessionID] = agentID

	// Start session subscription ONLY for coordinator agents
	// This allows us to receive async responses from workflow sub-agents
	// Regular agents don't need this as their responses come via StreamWeave
	isCoordinator := strings.HasSuffix(strings.ToLower(agentID), "-coordinator")
	if isCoordinator {
		if _, exists := c.sessionSubscriptions[sessionID]; !exists {
			subscribeCtx, subscribeCancel := context.WithCancel(context.Background())
			c.sessionSubscriptions[sessionID] = subscribeCancel

			// Start subscription in background goroutine
			go func() {
				err := c.client.SubscribeToSession(subscribeCtx, sessionID, agentID, func(update *loomv1.SessionUpdate) {
					// Only process assistant messages (skip user/tool messages already shown)
					if newMsg := update.GetNewMessage(); newMsg != nil && newMsg.Role == "assistant" {
						// Generate message ID for this async response
						messageID := fmt.Sprintf("assistant-%d", time.Now().UnixNano())

						// Create message using the internal message format
						msg := message.NewMessage(messageID, sessionID, message.Assistant)
						msg.AgentID = agentID
						msg.CreatedAt = time.Now().Unix()
						msg.AddPart(message.ContentText{Text: newMsg.Content})

						// Send as a new message event to TUI
						if c.events != nil {
							c.events <- pubsub.Event[message.Message]{
								Type:    pubsub.CreatedEvent,
								Payload: msg,
							}
						}
					}
				})
				if err != nil && subscribeCtx.Err() == nil {
					// Log error if it wasn't a cancellation
					if f, errLog := os.OpenFile("/tmp/loom-subscription-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); errLog == nil {
						fmt.Fprintf(f, "[%s] Subscription error for session %s: %v\n", time.Now().Format("15:04:05"), sessionID, err)
						_ = f.Close()
					}
				}
			}()
		}
	}

	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.busyAgents[agentID] = false
		delete(c.agentCancelFunc, agentID)
		// Don't cancel subscription here - it should continue listening for async responses
		c.mu.Unlock()
	}()

	var result AgentResult
	result.SessionID = sessionID

	// Track if this is the first message for this session
	firstMessage := true

	// Generate a consistent message ID for all progress events in this turn
	// Also capture the start time for duration calculation
	now := time.Now()
	messageID := fmt.Sprintf("assistant-%d", now.UnixNano())
	startTimestamp := now.Unix()

	// Track stage history for building up progress display
	var stageHistory []StageInfo
	var lastStage loomv1.ExecutionStage
	var lastToolName string
	var lastMessage string
	var lastContent string

	// Stream the response
	err := c.client.StreamWeave(runCtx, prompt, sessionID, agentID, func(progress *loomv1.WeaveProgress) {
		// Convert progress to message event for TUI
		if c.events != nil {
			// Track stage transitions to build history
			if progress.Stage != lastStage && lastStage != loomv1.ExecutionStage_EXECUTION_STAGE_UNSPECIFIED {
				// Previous stage is complete, add to history
				// Determine if the previous stage failed (next stage is FAILED or SELF_CORRECTION)
				failed := progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED ||
					progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION

				stageHistory = append(stageHistory, StageInfo{
					Stage:    lastStage,
					Message:  lastMessage,
					ToolName: lastToolName,
					Content:  lastContent,
					Failed:   failed,
					Done:     true,
				})
			}
			lastStage = progress.Stage
			lastToolName = progress.ToolName
			lastMessage = progress.Message
			// Capture actual content for LLM generation stages
			if progress.PartialContent != "" {
				lastContent = progress.PartialContent
			}

			// Check for HITL (Human-In-The-Loop) request and emit clarification question
			if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP && progress.HitlRequest != nil {
				// Convert HITLRequestInfo to metaagent.Question
				question := &metaagent.Question{
					ID:      progress.HitlRequest.RequestId,
					Prompt:  progress.HitlRequest.Question,
					Context: progress.HitlRequest.RequestType, // Use request_type as context
				}

				// Send QuestionAskedMsg so the TUI shows the clarification dialog
				c.events <- metaagent.QuestionAskedMsg{
					Question: question,
				}
			}

			msg := ProgressToMessageWithHistory(progress, sessionID, messageID, agentID, startTimestamp, stageHistory)
			eventType := pubsub.UpdatedEvent
			if firstMessage {
				eventType = pubsub.CreatedEvent
				firstMessage = false
			}
			c.events <- pubsub.Event[message.Message]{
				Type:    eventType,
				Payload: msg,
			}
		}

		// Track final result and cost
		if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
			result.Response = progress.PartialContent
			if progress.Cost != nil {
				result.Cost = &CostInfo{
					TotalCost:    progress.Cost.TotalCostUsd,
					InputTokens:  progress.Cost.LlmCost.GetInputTokens(),
					OutputTokens: progress.Cost.LlmCost.GetOutputTokens(),
					Provider:     progress.Cost.LlmCost.GetProvider(),
					Model:        progress.Cost.LlmCost.GetModel(),
				}
				// Send session update with cost and token counts
				if c.events != nil {
					c.events <- pubsub.Event[session.Session]{
						Type: pubsub.UpdatedEvent,
						Payload: session.Session{
							ID:               sessionID,
							Cost:             progress.Cost.TotalCostUsd,
							CompletionTokens: int(progress.Cost.LlmCost.GetOutputTokens()),
							PromptTokens:     int(progress.Cost.LlmCost.GetInputTokens()),
						},
					}
				}
			}
		}
	})

	if err != nil {
		result.Error = err
		return &result, err
	}

	return &result, nil
}

// IsBusy returns true if the specified agent is processing.
// If agentID is empty, checks if the default agent is busy.
func (c *CoordinatorAdapter) IsBusy(agentID string) bool {
	if agentID == "" {
		agentID = c.agentID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busyAgents[agentID]
}

// IsSessionBusy returns true if the agent associated with the given session is busy.
func (c *CoordinatorAdapter) IsSessionBusy(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Look up which agent owns this session
	agentID, exists := c.sessionToAgent[sessionID]
	if !exists {
		// Session not tracked, check default agent
		agentID = c.agentID
	}

	return c.busyAgents[agentID]
}

// QueuedPrompts returns the number of queued prompts.
func (c *CoordinatorAdapter) QueuedPrompts() int {
	return 0
}

// QueuedPromptsList returns the list of queued prompts for a session.
func (c *CoordinatorAdapter) QueuedPromptsList(sessionID string) []string {
	return nil
}

// Cancel cancels the current agent's processing.
func (c *CoordinatorAdapter) Cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel the current default agent
	if cancelFunc, exists := c.agentCancelFunc[c.agentID]; exists {
		cancelFunc()
	}
}

// CancelAll cancels all active agent processing.
func (c *CoordinatorAdapter) CancelAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Cancel all agents
	for _, cancelFunc := range c.agentCancelFunc {
		if cancelFunc != nil {
			cancelFunc()
		}
	}
}

// ClearQueue clears the prompt queue for a session.
func (c *CoordinatorAdapter) ClearQueue(sessionID string) {}

// UpdateModels is a stub - Loom handles model updates differently.
func (c *CoordinatorAdapter) UpdateModels(ctx context.Context) error {
	return nil
}

// Summarize is a stub for context summarization.
func (c *CoordinatorAdapter) Summarize(ctx context.Context, sessionID string) error {
	return nil
}

// ListAgents returns the list of available agents from the server.
func (c *CoordinatorAdapter) ListAgents(ctx context.Context) ([]agent.AgentInfo, error) {
	agentInfos, err := c.client.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}

	result := make([]agent.AgentInfo, len(agentInfos))
	for i, info := range agentInfos {
		result[i] = agent.AgentInfo{
			ID:     info.Id,
			Name:   info.Name,
			Status: info.Status,
		}
	}

	return result, nil
}

// ProgressEvent wraps a WeaveProgress for TUI consumption.
type ProgressEvent struct {
	Progress *loomv1.WeaveProgress
}

// ErrAgentBusy is returned when trying to run while already busy.
var ErrAgentBusy = &AgentBusyError{}

// AgentBusyError indicates the agent is already processing.
type AgentBusyError struct{}

func (e *AgentBusyError) Error() string {
	return "agent is busy, please wait..."
}
