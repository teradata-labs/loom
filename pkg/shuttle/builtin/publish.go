// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// PublishTool provides topic publishing for pub-sub communication.
// Allows agents to broadcast messages to all subscribers of a topic.
type PublishTool struct {
	bus     *communication.MessageBus
	agentID string
}

// NewPublishTool creates a new publish tool for an agent.
func NewPublishTool(bus *communication.MessageBus, agentID string) *PublishTool {
	return &PublishTool{
		bus:     bus,
		agentID: agentID,
	}
}

func (t *PublishTool) Name() string {
	return "publish"
}

// Description returns the tool description.
func (t *PublishTool) Description() string {
	return `Publish a broadcast message to all subscribers of a topic.

Use this tool to:
- Send messages to group conversations (e.g., "party-chat")
- Broadcast status updates to interested agents
- Notify multiple agents simultaneously
- Coordinate multi-agent activities

All agents subscribed to the topic will receive the message instantly via event-driven notifications.

Examples:
- publish("party-chat", "I found a secret door!")
- publish("dnd.combat", "Rolling for initiative...")
- publish("team-updates", "Task completed successfully")`
}

func (t *PublishTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for publishing to a topic",
		map[string]*shuttle.JSONSchema{
			"topic":    shuttle.NewStringSchema("Topic to publish to (e.g., 'party-chat')"),
			"message":  shuttle.NewStringSchema("Message content to broadcast"),
			"metadata": shuttle.NewObjectSchema("Optional metadata key-value pairs", nil, nil),
		},
		[]string{"topic", "message"},
	)
}

func (t *PublishTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate bus availability
	if t.bus == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "BUS_NOT_AVAILABLE",
				Message:    "Message bus not configured for this agent",
				Suggestion: "Pub-sub communication requires MessageBus configured in server",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract topic
	topic, ok := params["topic"].(string)
	if !ok || topic == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_TOPIC",
				Message:    "Topic must be a non-empty string",
				Suggestion: "Provide a topic like 'party-chat'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract message
	message, ok := params["message"].(string)
	if !ok || message == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_MESSAGE",
				Message:    "Message must be a non-empty string",
				Suggestion: "Provide message content",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Optional metadata
	metadata := make(map[string]string)
	if md, ok := params["metadata"].(map[string]interface{}); ok {
		for k, v := range md {
			if strVal, ok := v.(string); ok {
				metadata[k] = strVal
			}
		}
	}

	// Create bus message
	messageID := uuid.New().String()
	busMessage := &loomv1.BusMessage{
		Id:        messageID,
		Topic:     topic,
		FromAgent: t.agentID,
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: []byte(message),
			},
		},
		Metadata:  metadata,
		Timestamp: time.Now().UnixMilli(),
	}

	// Publish message
	delivered, dropped, err := t.bus.Publish(ctx, topic, busMessage)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "PUBLISH_FAILED",
				Message:    fmt.Sprintf("Failed to publish message: %v", err),
				Retryable:  true,
				Suggestion: "Check if topic name is valid",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build result
	result := map[string]interface{}{
		"published":    true,
		"message_id":   messageID,
		"topic":        topic,
		"delivered":    delivered,
		"dropped":      dropped,
		"published_at": time.Now().Format(time.RFC3339),
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"message_id": messageID,
			"topic":      topic,
			"delivered":  delivered,
			"dropped":    dropped,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *PublishTool) Backend() string {
	return "" // Backend-agnostic
}
