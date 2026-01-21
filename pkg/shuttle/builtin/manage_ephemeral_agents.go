// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// EphemeralAgentHandler is implemented by MultiAgentServer to handle ephemeral agent lifecycle.
type EphemeralAgentHandler interface {
	// SpawnSubAgent spawns a new agent as a child of the current session
	SpawnSubAgent(ctx context.Context, req *SpawnSubAgentRequest) (*SpawnSubAgentResponse, error)
	// DespawnSubAgent terminates a spawned sub-agent
	DespawnSubAgent(ctx context.Context, req *DespawnSubAgentRequest) (*DespawnSubAgentResponse, error)
}

// SpawnSubAgentRequest contains parameters for spawning a new sub-agent.
type SpawnSubAgentRequest struct {
	ParentSessionID string            // Session ID of the parent agent
	ParentAgentID   string            // Agent ID of the parent
	AgentID         string            // Agent config to spawn (e.g., "fighter-spawnable")
	WorkflowID      string            // Optional: workflow namespace (auto-generated if empty)
	InitialMessage  string            // Optional: first message to send to spawned agent
	AutoSubscribe   []string          // Optional: topics to auto-subscribe
	Metadata        map[string]string // Optional: metadata for tracking
}

// SpawnSubAgentResponse contains the result of spawning a sub-agent.
type SpawnSubAgentResponse struct {
	SubAgentID       string   // Full agent ID (with namespace prefix)
	SessionID        string   // New session ID for the sub-agent
	Status           string   // Status: "spawned"
	SubscribedTopics []string // Topics the agent auto-subscribed to
}

// DespawnSubAgentRequest contains parameters for despawning a sub-agent.
type DespawnSubAgentRequest struct {
	ParentSessionID string // Session ID of the parent agent
	SubAgentID      string // Full ID of sub-agent to despawn (e.g., "workflow:agent")
	Reason          string // Optional: reason for despawn (for logging)
}

// DespawnSubAgentResponse contains the result of a despawn operation.
type DespawnSubAgentResponse struct {
	SubAgentID string // The sub-agent that was despawned
	SessionID  string // The session that was terminated
	Status     string // "despawned" or "not_found"
}

// ManageEphemeralAgentsTool enables agents to spawn and despawn sub-agents dynamically.
type ManageEphemeralAgentsTool struct {
	handler       EphemeralAgentHandler
	parentSession string
	parentAgentID string
}

// NewManageEphemeralAgentsTool creates a new manage_ephemeral_agents tool.
func NewManageEphemeralAgentsTool(handler EphemeralAgentHandler, parentSessionID, parentAgentID string) *ManageEphemeralAgentsTool {
	return &ManageEphemeralAgentsTool{
		handler:       handler,
		parentSession: parentSessionID,
		parentAgentID: parentAgentID,
	}
}

func (t *ManageEphemeralAgentsTool) Name() string {
	return "manage_ephemeral_agents"
}

func (t *ManageEphemeralAgentsTool) Description() string {
	return `Manage ephemeral sub-agents - spawn and despawn agents dynamically.

COMMANDS:
- spawn: Create a new agent instance to run in the background
- despawn: Terminate a spawned agent and clean up resources

SPAWN use cases:
- Interactive workflows (spawn party members for D&D campaigns)
- Parallel delegation (spawn specialists for concurrent tasks)
- Context isolation (spawn fresh agent when context is bloated)
- Dynamic scaling (create agents on demand)

Spawned agents:
- Run independently in background with own sessions
- Auto-subscribe to pub/sub topics for group communication
- Process messages and respond automatically
- Clean up when parent ends or when explicitly despawned

DESPAWN use cases:
- End agent lifecycle when work is complete
- Free up resources from inactive agents
- Clean up after workflow steps finish

Examples:
  spawn: {"command": "spawn", "agent_id": "fighter-spawnable", "workflow_id": "dungeon-crawl", "auto_subscribe": ["party-chat"]}
  despawn: {"command": "despawn", "sub_agent_id": "dungeon-crawl:fighter-spawnable", "reason": "adventure complete"}`
}

func (t *ManageEphemeralAgentsTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for managing ephemeral agents",
		map[string]*shuttle.JSONSchema{
			"command": shuttle.NewStringSchema("Command: 'spawn' or 'despawn'").
				WithEnum([]string{"spawn", "despawn"}),
			// Spawn parameters
			"agent_id": shuttle.NewStringSchema("(spawn) Agent config to spawn (e.g., 'fighter-spawnable')"),
			"workflow_id": shuttle.NewStringSchema("(spawn) Optional: workflow namespace (auto-generated if not provided)").
				WithDefault(""),
			"initial_message": shuttle.NewStringSchema("(spawn) Optional: first message to send to spawned agent").
				WithDefault(""),
			"auto_subscribe": shuttle.NewArraySchema("(spawn) Optional: topics to auto-subscribe", shuttle.NewStringSchema("Topic name")),
			"metadata":       shuttle.NewObjectSchema("(spawn) Optional: metadata for tracking", nil, nil),
			// Despawn parameters
			"sub_agent_id": shuttle.NewStringSchema("(despawn) Full ID of sub-agent to despawn (e.g., 'workflow:agent-name')"),
			"reason": shuttle.NewStringSchema("(despawn) Optional: reason for despawn").
				WithDefault(""),
		},
		[]string{"command"}, // Only command is required
	)
}

func (t *ManageEphemeralAgentsTool) Execute(ctx context.Context, params map[string]any) (*shuttle.Result, error) {
	start := time.Now()

	// Extract command
	command, ok := params["command"].(string)
	if !ok || command == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_COMMAND",
				Message:    "command parameter is required",
				Suggestion: "Specify 'spawn' or 'despawn' as the command",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	switch command {
	case "spawn":
		return t.executeSpawn(ctx, params, start)
	case "despawn":
		return t.executeDespawn(ctx, params, start)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_COMMAND",
				Message:    fmt.Sprintf("Unknown command: %s", command),
				Suggestion: "Use 'spawn' or 'despawn'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
}

func (t *ManageEphemeralAgentsTool) executeSpawn(ctx context.Context, params map[string]any, start time.Time) (*shuttle.Result, error) {
	// Extract agent_id (required for spawn)
	agentID, ok := params["agent_id"].(string)
	if !ok || agentID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_AGENT_ID",
				Message:    "agent_id parameter is required for spawn command",
				Suggestion: "Specify which agent config to spawn (e.g., 'fighter-spawnable')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract optional parameters
	workflowID, _ := params["workflow_id"].(string)
	initialMessage, _ := params["initial_message"].(string)

	var autoSubscribe []string
	if topicsRaw, ok := params["auto_subscribe"].([]any); ok {
		for _, topic := range topicsRaw {
			if topicStr, ok := topic.(string); ok {
				autoSubscribe = append(autoSubscribe, topicStr)
			}
		}
	}

	var metadata map[string]string
	if metaRaw, ok := params["metadata"].(map[string]any); ok {
		metadata = make(map[string]string)
		for k, v := range metaRaw {
			if vStr, ok := v.(string); ok {
				metadata[k] = vStr
			}
		}
	}

	// Build spawn request
	req := &SpawnSubAgentRequest{
		ParentSessionID: t.parentSession,
		ParentAgentID:   t.parentAgentID,
		AgentID:         agentID,
		WorkflowID:      workflowID,
		InitialMessage:  initialMessage,
		AutoSubscribe:   autoSubscribe,
		Metadata:        metadata,
	}

	// Call server handler
	resp, err := t.handler.SpawnSubAgent(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SPAWN_FAILED",
				Message:    fmt.Sprintf("Failed to spawn agent: %v", err),
				Suggestion: "Verify the agent_id exists in ~/.loom/agents/",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Return success
	return &shuttle.Result{
		Success: true,
		Data: map[string]any{
			"command":           "spawn",
			"sub_agent_id":      resp.SubAgentID,
			"session_id":        resp.SessionID,
			"status":            resp.Status,
			"subscribed_topics": resp.SubscribedTopics,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ManageEphemeralAgentsTool) executeDespawn(ctx context.Context, params map[string]any, start time.Time) (*shuttle.Result, error) {
	// Extract sub_agent_id (required for despawn)
	subAgentID, ok := params["sub_agent_id"].(string)
	if !ok || subAgentID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_SUB_AGENT_ID",
				Message:    "sub_agent_id parameter is required for despawn command",
				Suggestion: "Provide the full ID of the sub-agent to despawn (e.g., 'workflow:agent-name')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract optional reason
	reason, _ := params["reason"].(string)
	if reason == "" {
		reason = "despawned by parent agent"
	}

	// Build despawn request
	req := &DespawnSubAgentRequest{
		ParentSessionID: t.parentSession,
		SubAgentID:      subAgentID,
		Reason:          reason,
	}

	// Call server handler
	resp, err := t.handler.DespawnSubAgent(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "DESPAWN_FAILED",
				Message:    fmt.Sprintf("Failed to despawn agent: %v", err),
				Suggestion: "Verify the sub_agent_id is correct and the agent exists",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Return success
	return &shuttle.Result{
		Success: true,
		Data: map[string]any{
			"command":      "despawn",
			"sub_agent_id": resp.SubAgentID,
			"session_id":   resp.SessionID,
			"status":       resp.Status,
			"reason":       reason,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ManageEphemeralAgentsTool) Backend() string {
	return "" // Backend-agnostic
}
