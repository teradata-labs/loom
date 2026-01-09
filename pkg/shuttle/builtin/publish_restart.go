// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// PublishRestartTool enables agents to request workflow stage restarts via pub/sub.
// Used in iterative workflows to enable autonomous agent coordination and self-correction.
type PublishRestartTool struct {
	messageBus *communication.MessageBus
	agentID    string // The agent (stage) that owns this tool
}

// NewPublishRestartTool creates a new publish restart tool for an agent.
func NewPublishRestartTool(messageBus *communication.MessageBus, agentID string) *PublishRestartTool {
	return &PublishRestartTool{
		messageBus: messageBus,
		agentID:    agentID,
	}
}

func (t *PublishRestartTool) Name() string {
	return "publish_restart_request"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/communication.yaml).
// This fallback is used only when prompts are not configured.
func (t *PublishRestartTool) Description() string {
	return `Requests a restart of an earlier workflow stage. Use this when you need a previous stage to re-execute with different parameters.

Use this tool when:
- You hit processing limits and need a different data subset (e.g., "Ask Stage 2 to find a table with fewer rows")
- You discover data quality issues that require earlier stage correction
- You need to negotiate parameters with a previous stage
- You need to trigger self-correction in an iterative workflow

Important:
- You can only restart EARLIER stages (no forward jumps)
- The workflow must have restart_policy.enabled: true
- There may be cooldown periods between restarts
- Provide a clear reason to explain why restart is needed

The orchestrator will validate your request and send a response indicating success or failure.`
}

func (t *PublishRestartTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for requesting a stage restart",
		map[string]*shuttle.JSONSchema{
			"target_stage_id": shuttle.NewStringSchema("ID of the stage to restart (must be an earlier stage)"),
			"reason":          shuttle.NewStringSchema("Clear explanation of why restart is needed (e.g., 'Need table with fewer than 10M rows due to processing limits')"),
			"parameters": shuttle.NewObjectSchema(
				"Optional parameters to pass to the restarted stage (key-value pairs)",
				nil,
				nil,
			),
		},
		[]string{"target_stage_id", "reason"},
	)
}

func (t *PublishRestartTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate MessageBus availability
	if t.messageBus == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MESSAGEBUS_NOT_AVAILABLE",
				Message:    "MessageBus not configured for this workflow",
				Suggestion: "Restart coordination requires iterative workflow with restart_policy.enabled: true",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract required parameters
	targetStageID, ok := params["target_stage_id"].(string)
	if !ok || targetStageID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "target_stage_id is required",
				Suggestion: "Specify the ID of the earlier stage to restart (e.g., 'stage-2-table-discovery')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	reason, ok := params["reason"].(string)
	if !ok || reason == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "reason is required",
				Suggestion: "Provide a clear explanation for why restart is needed",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract optional parameters
	var parameters map[string]string
	if p, ok := params["parameters"].(map[string]interface{}); ok {
		parameters = make(map[string]string)
		for k, v := range p {
			if vs, ok := v.(string); ok {
				parameters[k] = vs
			}
		}
	}

	// Create RestartRequest message
	restartReq := &loomv1.RestartRequest{
		RequesterStageId: t.agentID,
		TargetStageId:    targetStageID,
		Reason:           reason,
		Parameters:       parameters,
		Iteration:        0, // Will be set by orchestrator
		TimestampMs:      time.Now().UnixMilli(),
	}

	// Encode as JSON
	payload, err := json.Marshal(restartReq)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "ENCODING_FAILED",
				Message: fmt.Sprintf("Failed to encode restart request: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create BusMessage
	busMsg := &loomv1.BusMessage{
		Id:        fmt.Sprintf("restart-request-%d", time.Now().UnixNano()),
		Topic:     "workflow.restart",
		FromAgent: t.agentID,
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: payload,
			},
		},
		Metadata: map[string]string{
			"requester":    t.agentID,
			"target":       targetStageID,
			"type":         "restart_request",
			"timestamp_ms": fmt.Sprintf("%d", restartReq.TimestampMs),
		},
	}

	// Publish to MessageBus
	publishedCount, filteredCount, err := t.messageBus.Publish(ctx, "workflow.restart", busMsg)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "PUBLISH_FAILED",
				Message:    fmt.Sprintf("Failed to publish restart request: %v", err),
				Retryable:  true,
				Suggestion: "Check if MessageBus is operational and restart policy is enabled",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build success result
	result := map[string]interface{}{
		"status":          "request_published",
		"requester":       t.agentID,
		"target_stage":    targetStageID,
		"reason":          reason,
		"published_count": publishedCount,
		"filtered_count":  filteredCount,
		"timestamp":       time.Now().Format(time.RFC3339),
		"response_topic":  fmt.Sprintf("workflow.restart.response.%s", t.agentID),
		"message":         "Restart request published. The orchestrator will validate and process your request.",
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"requester":    t.agentID,
			"target_stage": targetStageID,
			"reason":       reason,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *PublishRestartTool) Backend() string {
	return "" // Backend-agnostic
}
