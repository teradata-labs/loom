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

// ReceiveMessageTool provides message retrieval for agent-to-agent communication.
// Receives messages sent by other agents via send_message tool.
type ReceiveMessageTool struct {
	queue   *communication.MessageQueue
	agentID string // The agent that owns this tool
}

// NewReceiveMessageTool creates a new receive message tool for an agent.
func NewReceiveMessageTool(queue *communication.MessageQueue, agentID string) *ReceiveMessageTool {
	return &ReceiveMessageTool{
		queue:   queue,
		agentID: agentID,
	}
}

func (t *ReceiveMessageTool) Name() string {
	return "receive_message"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/communication.yaml).
// This fallback is used only when prompts are not configured.
func (t *ReceiveMessageTool) Description() string {
	return `Receives messages sent to this agent by other agents in the workflow.

Use this tool to:
- Check for incoming questions from other agents
- Receive data requests from downstream stages
- Get coordination messages from parallel agents
- Poll for updates from upstream stages

Returns the next pending message in the queue, or null if no messages are available.
Messages should be acknowledged after processing.`
}

func (t *ReceiveMessageTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for receiving messages",
		map[string]*shuttle.JSONSchema{
			"timeout_seconds": shuttle.NewNumberSchema("How long to wait for a message (0-300 seconds, default: 0 = non-blocking)").
				WithDefault(0),
		},
		[]string{}, // No required parameters
	)
}

func (t *ReceiveMessageTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
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

	// Extract timeout
	timeoutSeconds := float64(0)
	if ts, ok := params["timeout_seconds"].(float64); ok {
		timeoutSeconds = ts
	}

	// Check if agent is registered for event-driven notifications
	notifyChan, hasNotifications := t.queue.GetNotificationChannel(t.agentID)

	// If registered for notifications and timeout > 0, wait for notification OR timeout
	if hasNotifications && timeoutSeconds > 0 {
		timeoutDuration := time.Duration(timeoutSeconds) * time.Second
		timer := time.NewTimer(timeoutDuration)
		defer timer.Stop()

		select {
		case <-notifyChan:
			// Received notification, message available - continue to dequeue
		case <-timer.C:
			// Timeout - check queue anyway (might have arrived just before timeout)
		case <-ctx.Done():
			return &shuttle.Result{
				Success: true,
				Data: map[string]interface{}{
					"has_message": false,
					"reason":      "context cancelled",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
	} else if hasNotifications && timeoutSeconds == 0 {
		// Non-blocking check with notifications - just check once
		// Don't wait on notification channel for non-blocking
	} else if !hasNotifications && timeoutSeconds > 0 {
		// Not registered for notifications, use legacy timeout-based polling
		// Create context with timeout
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
		defer cancel()
	}

	// Dequeue message
	msg, err := t.queue.Dequeue(ctx, t.agentID)
	if err != nil {
		// Check if it's a timeout or no messages available
		if ctx.Err() == context.DeadlineExceeded {
			return &shuttle.Result{
				Success: true,
				Data: map[string]interface{}{
					"has_message": false,
					"reason":      "timeout - no messages received within timeout period",
				},
				Metadata: map[string]interface{}{
					"timeout_seconds": timeoutSeconds,
					"event_driven":    hasNotifications,
					"wait_time_ms":    time.Since(start).Milliseconds(),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "DEQUEUE_FAILED",
				Message:    fmt.Sprintf("Failed to receive message: %v", err),
				Retryable:  true,
				Suggestion: "Check if message queue is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// No message available (non-blocking)
	if msg == nil {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"has_message": false,
				"reason":      "no pending messages",
			},
			Metadata: map[string]interface{}{
				"event_driven": hasNotifications,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Auto-acknowledge the message
	if err := t.queue.Acknowledge(ctx, msg.ID); err != nil {
		// Log warning but don't fail - message was received
		// (acknowledgment failure means it might be redelivered)
	}

	// Extract message payload
	var messageContent string
	if msg.Payload != nil {
		if value := msg.Payload.GetValue(); value != nil {
			messageContent = string(value)
		} else if ref := msg.Payload.GetReference(); ref != nil {
			messageContent = fmt.Sprintf("[Reference: %s]", ref.Id)
		}
	}

	// Build result with message details
	result := map[string]interface{}{
		"has_message":  true,
		"message_id":   msg.ID,
		"from_agent":   msg.FromAgent,
		"message_type": msg.MessageType,
		"message":      messageContent,
		"received_at":  time.Now().Format(time.RFC3339),
		"enqueued_at":  msg.EnqueuedAt.Format(time.RFC3339),
	}

	if msg.Metadata != nil && len(msg.Metadata) > 0 {
		result["metadata"] = msg.Metadata
	}

	if msg.CorrelationID != "" {
		result["correlation_id"] = msg.CorrelationID
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"from_agent":   msg.FromAgent,
			"message_type": msg.MessageType,
			"message_id":   msg.ID,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ReceiveMessageTool) Backend() string {
	return "" // Backend-agnostic
}
