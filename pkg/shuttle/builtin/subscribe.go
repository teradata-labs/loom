// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// SubscribeTool provides topic subscription for pub-sub communication.
// Allows agents to subscribe to topics and receive broadcast messages.
type SubscribeTool struct {
	bus     *communication.MessageBus
	agentID string
}

// NewSubscribeTool creates a new subscribe tool for an agent.
func NewSubscribeTool(bus *communication.MessageBus, agentID string) *SubscribeTool {
	return &SubscribeTool{
		bus:     bus,
		agentID: agentID,
	}
}

func (t *SubscribeTool) Name() string {
	return "subscribe"
}

// Description returns the tool description.
func (t *SubscribeTool) Description() string {
	return `Subscribe to a topic to receive broadcast messages from other agents.

Use this tool to:
- Join group conversations (e.g., "party-chat", "team-updates")
- Listen for status broadcasts from other agents
- Receive notifications about system events
- Participate in multi-agent collaboration

After subscribing, use receive_broadcast to check for messages.
Messages arrive instantly via event-driven notifications.

Topic patterns supported:
- Exact match: "party-chat"
- Wildcard: "dnd.*" (matches "dnd.combat", "dnd.exploration", etc.)
- Multi-level: "game.*.events" (matches "game.combat.events", "game.social.events")

Returns subscription_id for later unsubscribe.`
}

func (t *SubscribeTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for subscribing to a topic",
		map[string]*shuttle.JSONSchema{
			"topic":             shuttle.NewStringSchema("Topic pattern to subscribe to (e.g., 'party-chat', 'dnd.*')"),
			"filter_from_agent": shuttle.NewStringSchema("Optional: Only receive messages from specific agent"),
		},
		[]string{"topic"},
	)
}

func (t *SubscribeTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
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
				Suggestion: "Provide a topic like 'party-chat' or 'dnd.*'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Optional filter
	var filterFromAgent string
	if fa, ok := params["filter_from_agent"].(string); ok {
		filterFromAgent = fa
	}

	// Create filter if specified
	var filter *loomv1.SubscriptionFilter
	if filterFromAgent != "" {
		filter = &loomv1.SubscriptionFilter{
			FromAgents: []string{filterFromAgent},
		}
	}

	// Subscribe to topic with buffer size of 100 messages
	bufferSize := 100
	subscription, err := t.bus.Subscribe(ctx, t.agentID, topic, filter, bufferSize)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SUBSCRIBE_FAILED",
				Message:    fmt.Sprintf("Failed to subscribe to topic: %v", err),
				Retryable:  true,
				Suggestion: "Check if topic name is valid",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build result
	result := map[string]interface{}{
		"subscribed":      true,
		"subscription_id": subscription.ID,
		"topic":           subscription.Topic,
		"agent_id":        subscription.AgentID,
		"subscribed_at":   subscription.Created.Format(time.RFC3339),
	}

	if filterFromAgent != "" {
		result["filtered_from"] = filterFromAgent
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"subscription_id": subscription.ID,
			"topic":           subscription.Topic,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *SubscribeTool) Backend() string {
	return "" // Backend-agnostic
}
