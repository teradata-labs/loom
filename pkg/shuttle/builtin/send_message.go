// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// AgentRegistry provides read-only access to agent configurations for agent ID resolution.
type AgentRegistry interface {
	GetConfig(name string) *loomv1.AgentConfig
}

// SendMessageTool provides agent-to-agent messaging for workflows.
// Enables point-to-point async communication between agents.
type SendMessageTool struct {
	queue    *communication.MessageQueue
	agentID  string // The agent that owns this tool
	registry AgentRegistry
	logger   *zap.Logger
}

// NewSendMessageTool creates a new send message tool for an agent.
func NewSendMessageTool(queue *communication.MessageQueue, agentID string) *SendMessageTool {
	return &SendMessageTool{
		queue:    queue,
		agentID:  agentID,
		registry: nil, // Will be set via SetAgentRegistry if available
		logger:   zap.NewNop(),
	}
}

// SetAgentRegistry sets the agent registry for agent ID resolution.
// This enables auto-healing of short agent names to workflow-prefixed names.
func (t *SendMessageTool) SetAgentRegistry(registry AgentRegistry, logger *zap.Logger) {
	t.registry = registry
	if logger != nil {
		t.logger = logger
	}
}

func (t *SendMessageTool) Name() string {
	return "send_message"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/communication.yaml).
// This fallback is used only when prompts are not configured.
func (t *SendMessageTool) Description() string {
	return `Sends a message to another agent in the workflow. Use this for agent-to-agent communication.

Use this tool to:
- Send questions to other agents (e.g., ask Stage 10 for specific data)
- Pass results or updates between workflow stages
- Request clarifications from previous stages
- Coordinate work between parallel agents

Messages are automatically delivered to the receiving agent.`
}

func (t *SendMessageTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for sending a message to another agent",
		map[string]*shuttle.JSONSchema{
			"to_agent": shuttle.NewStringSchema("Target agent ID (required)"),
			"message_type": shuttle.NewStringSchema("Message type (e.g., 'data.request', 'data.response', 'task.update')").
				WithDefault("message"),
			"message": shuttle.NewStringSchema("The message content to send (required)"),
			"metadata": shuttle.NewObjectSchema(
				"Optional metadata (key-value pairs)",
				nil,
				nil,
			),
		},
		[]string{"to_agent", "message"},
	)
}

func (t *SendMessageTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate queue availability
	if t.queue == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "QUEUE_NOT_AVAILABLE",
				Message:    "Message queue not configured for this agent",
				Suggestion: "Communication tools require MultiAgentServer with message queue configured",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract required parameters
	toAgent, ok := params["to_agent"].(string)
	if !ok || toAgent == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "to_agent is required",
				Suggestion: "Specify the target agent ID (e.g., 'td-expert-analytics-stage-10')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// AUTO-HEAL: Use sender's workflow context to resolve short agent names
	// If the target is a short name (no ":"), try to resolve it using the sender's workflow context
	originalToAgent := toAgent
	if t.registry != nil && !strings.Contains(toAgent, ":") {
		// Extract workflow name from sender's agent ID
		workflowName := extractWorkflowName(t.agentID)
		if workflowName != "" {
			// Try workflow-prefixed name: "workflow:agent"
			candidate := fmt.Sprintf("%s:%s", workflowName, toAgent)
			if t.registry.GetConfig(candidate) != nil {
				// Found! Use the resolved ID
				toAgent = candidate
				t.logger.Info("Auto-healed agent ID using sender's workflow context",
					zap.String("sender", t.agentID),
					zap.String("workflow", workflowName),
					zap.String("original_target", originalToAgent),
					zap.String("resolved_target", toAgent))
			}
		}
	}

	message, ok := params["message"].(string)
	if !ok || message == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "message is required",
				Suggestion: "Provide a message to send",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract optional parameters
	messageType := "message"
	if mt, ok := params["message_type"].(string); ok && mt != "" {
		messageType = mt
	}

	var metadata map[string]string
	if m, ok := params["metadata"].(map[string]interface{}); ok {
		metadata = make(map[string]string)
		for k, v := range m {
			if vs, ok := v.(string); ok {
				metadata[k] = vs
			}
		}
	}

	// Create payload
	payload := &loomv1.MessagePayload{
		Data: &loomv1.MessagePayload_Value{
			Value: []byte(message),
		},
	}

	// Validate target agent exists before sending
	if !t.queue.AgentExists(toAgent) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "AGENT_NOT_FOUND",
				Message:    fmt.Sprintf("Target agent '%s' does not exist or is not registered", toAgent),
				Retryable:  false,
				Suggestion: "Check the agent ID spelling and ensure the agent is loaded. Use tool_search to see available agents.",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Send message via queue
	messageID, err := t.queue.Send(ctx, t.agentID, toAgent, messageType, payload, metadata)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SEND_FAILED",
				Message:    fmt.Sprintf("Failed to send message: %v", err),
				Retryable:  true,
				Suggestion: "Check if target agent exists and queue is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build success result with detailed ACK
	result := map[string]interface{}{
		"status":       "enqueued",
		"message_id":   messageID,
		"sent_at":      time.Now().Format(time.RFC3339),
		"from_agent":   t.agentID,
		"to_agent":     toAgent,
		"message_type": messageType,
		"message_size": len(message),
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"message_id":   messageID,
			"from_agent":   t.agentID,
			"to_agent":     toAgent,
			"message_type": messageType,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *SendMessageTool) Backend() string {
	return "" // Backend-agnostic
}

// extractWorkflowName extracts the workflow name from an agent ID.
// Examples:
//   - "time-reporter" → "time-reporter" (coordinator IS the workflow)
//   - "time-reporter:time-printer" → "time-reporter" (sub-agent)
//   - "time-reporter:sub:nested" → "time-reporter" (nested sub-agent)
//   - "regular-agent" → "" (not a workflow agent)
//
// This function uses a simple heuristic: if the agent ID contains ":",
// the workflow name is the prefix before the first ":". Otherwise,
// we assume the agent ID itself might be a workflow coordinator.
func extractWorkflowName(agentID string) string {
	if idx := strings.Index(agentID, ":"); idx != -1 {
		return agentID[:idx]
	}
	// For coordinator agents, the agent ID IS the workflow name
	return agentID
}
