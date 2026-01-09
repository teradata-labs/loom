// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ReceiveBroadcastTool provides message retrieval from subscribed topics.
// Works with event-driven notifications for instant message delivery.
type ReceiveBroadcastTool struct {
	bus     *communication.MessageBus
	agentID string
}

// NewReceiveBroadcastTool creates a new receive broadcast tool for an agent.
func NewReceiveBroadcastTool(bus *communication.MessageBus, agentID string) *ReceiveBroadcastTool {
	return &ReceiveBroadcastTool{
		bus:     bus,
		agentID: agentID,
	}
}

func (t *ReceiveBroadcastTool) Name() string {
	return "receive_broadcast"
}

// Description returns the tool description.
func (t *ReceiveBroadcastTool) Description() string {
	return `Receives broadcast messages from subscribed topics.

Use this tool to:
- Check for messages in group conversations
- Receive status updates from other agents
- Get notifications from system broadcasts
- Read messages from topics you've subscribed to

Event-driven: You are notified instantly when messages arrive on your subscribed topics.
No need to poll - you'll be woken up when new messages are available.

Returns messages from ALL subscribed topics.`
}

func (t *ReceiveBroadcastTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for receiving broadcast messages",
		map[string]*shuttle.JSONSchema{
			"timeout_seconds": shuttle.NewNumberSchema("How long to wait for messages (0-300 seconds, default: 0 = non-blocking)").
				WithDefault(0),
			"max_messages": shuttle.NewNumberSchema("Maximum number of messages to receive (default: 10)").
				WithDefault(10),
		},
		[]string{}, // No required parameters
	)
}

func (t *ReceiveBroadcastTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
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

	// Extract parameters
	timeoutSeconds := float64(0)
	if ts, ok := params["timeout_seconds"].(float64); ok {
		timeoutSeconds = ts
	}

	maxMessages := int(10)
	if mm, ok := params["max_messages"].(float64); ok {
		maxMessages = int(mm)
	}

	// Get agent's subscriptions
	subscriptions := t.bus.GetSubscriptionsByAgent(t.agentID)
	if len(subscriptions) == 0 {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"has_messages": false,
				"reason":       "no active subscriptions",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if agent has notification channels registered
	// (Integration with event-driven system)
	hasNotifications := false
	for _, sub := range subscriptions {
		if _, ok := t.bus.GetNotificationChannel(sub.ID); ok {
			hasNotifications = true
			break
		}
	}

	// Collect messages from all subscriptions
	messages := make([]map[string]interface{}, 0, maxMessages)

	// If timeout > 0, we may need to wait
	if timeoutSeconds > 0 && hasNotifications {
		// Wait for notification or timeout (event-driven)
		for _, sub := range subscriptions {
			if notifyChan, ok := t.bus.GetNotificationChannel(sub.ID); ok {
				timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
				defer timer.Stop()

				select {
				case <-notifyChan:
					// Message available, continue to collect
				case <-timer.C:
					// Timeout
				case <-ctx.Done():
					return &shuttle.Result{
						Success: true,
						Data: map[string]interface{}{
							"has_messages": false,
							"reason":       "context cancelled",
						},
						ExecutionTimeMs: time.Since(start).Milliseconds(),
					}, nil
				}
				break // Only wait on first subscription channel
			}
		}
	}

	// Collect messages from all subscriptions (non-blocking)
	for _, sub := range subscriptions {
		for len(messages) < maxMessages {
			select {
			case msg := <-sub.Channel:
				// Extract message content
				var messageContent string
				if msg.Payload != nil {
					if value := msg.Payload.GetValue(); value != nil {
						messageContent = string(value)
					} else if ref := msg.Payload.GetReference(); ref != nil {
						messageContent = fmt.Sprintf("[Reference: %s]", ref.Id)
					}
				}

				messageData := map[string]interface{}{
					"message_id":   msg.Id,
					"topic":        msg.Topic,
					"from_agent":   msg.FromAgent,
					"message":      messageContent,
					"published_at": time.UnixMilli(msg.Timestamp).Format(time.RFC3339),
					"received_at":  time.Now().Format(time.RFC3339),
				}

				if msg.Metadata != nil && len(msg.Metadata) > 0 {
					messageData["metadata"] = msg.Metadata
				}

				messages = append(messages, messageData)
			default:
				// No more messages on this subscription
				goto nextSubscription
			}
		}
	nextSubscription:
		if len(messages) >= maxMessages {
			break
		}
	}

	// Build result
	if len(messages) == 0 {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"has_messages":  false,
				"reason":        "no pending messages",
				"subscriptions": len(subscriptions),
				"event_driven":  hasNotifications,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"has_messages":  true,
			"messages":      messages,
			"message_count": len(messages),
			"subscriptions": len(subscriptions),
			"event_driven":  hasNotifications,
		},
		Metadata: map[string]interface{}{
			"message_count": len(messages),
			"event_driven":  hasNotifications,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ReceiveBroadcastTool) Backend() string {
	return "" // Backend-agnostic
}
