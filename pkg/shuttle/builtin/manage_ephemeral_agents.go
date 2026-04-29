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

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// EphemeralAgentHandler is implemented by MultiAgentServer (CLI) and
// cloudEphemeralHandler (cloud) to handle ephemeral agent lifecycle.
type EphemeralAgentHandler interface {
	SpawnSubAgent(ctx context.Context, req *SpawnSubAgentRequest) (*SpawnSubAgentResponse, error)
	DespawnSubAgent(ctx context.Context, req *DespawnSubAgentRequest) (*DespawnSubAgentResponse, error)
	ListAvailableAgents(ctx context.Context, req *ListAvailableAgentsRequest) (*ListAvailableAgentsResponse, error)
}

// ListAvailableAgentsRequest contains parameters for listing agents available to spawn.
type ListAvailableAgentsRequest struct {
	ParentSessionID string
	ParentAgentID   string
}

// AvailableAgentInfo describes an agent or preset that can be spawned.
type AvailableAgentInfo struct {
	ID          string   // Agent ID (UUID) or preset identifier
	Name        string   // Display name
	Description string   // What the agent does
	Tools       []string // Tool names the agent has
	Source      string   // "agent", "preset", or "config" (YAML file)
}

// ListAvailableAgentsResponse contains the list of spawnable agents.
type ListAvailableAgentsResponse struct {
	Agents []AvailableAgentInfo
}

// SpawnSubAgentRequest contains parameters for spawning a new sub-agent.
type SpawnSubAgentRequest struct {
	ParentSessionID string            // Session ID of the parent agent
	ParentAgentID   string            // Agent ID of the parent
	AgentID         string            // Existing agent ID or config name to spawn from
	Preset          string            // Preset template name (alternative to AgentID)
	WorkflowID      string            // Optional: workflow namespace (auto-generated if empty)
	InitialMessage  string            // Optional: first message to send to spawned agent
	AutoSubscribe   []string          // Optional: topics to auto-subscribe
	Metadata        map[string]string // Optional: metadata for tracking
}

// SpawnSubAgentResponse contains the result of spawning a sub-agent.
type SpawnSubAgentResponse struct {
	SubAgentID       string   // Full agent ID (with namespace prefix)
	SessionID        string   // New session ID for the sub-agent
	Status           string   // "spawned", "completed", or "failed"
	SubscribedTopics []string // Topics the agent auto-subscribed to
	Output           string   // Sub-agent's final response (when Status="completed")
	TokensUsed       int64    // Total tokens consumed by the sub-agent
	CostUSD          float64  // Total cost of the sub-agent's execution
	DurationMs       int64    // Wall-clock duration in milliseconds
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
	return `Manage ephemeral sub-agents: list available agents/presets, spawn sub-agents to delegate work, and despawn them when done.
Always call 'list' first to see what agents and presets are available before spawning.
Commands: list (discover), spawn (create from agent or preset), despawn (terminate).`
}

func (t *ManageEphemeralAgentsTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for managing ephemeral agents",
		map[string]*shuttle.JSONSchema{
			"command": shuttle.NewStringSchema("Command: 'list', 'spawn', or 'despawn'").
				WithEnum("list", "spawn", "despawn"),
			// Spawn parameters
			"agent_id":        shuttle.NewStringSchema("(spawn) ID or name of an existing agent to spawn — use 'list' to see options"),
			"preset":          shuttle.NewStringSchema("(spawn) Preset template name — use 'list' to see available presets"),
			"initial_message": shuttle.NewStringSchema("(spawn) Task description to send to the spawned agent"),
			"workflow_id":     shuttle.NewStringSchema("(spawn) Optional: workflow namespace (auto-generated if not provided)"),
			"auto_subscribe":  shuttle.NewArraySchema("(spawn) Optional: topics to auto-subscribe", shuttle.NewStringSchema("Topic name")),
			// Despawn parameters
			"sub_agent_id": shuttle.NewStringSchema("(despawn) Full ID of sub-agent to despawn"),
			"reason":       shuttle.NewStringSchema("(despawn) Optional: reason for despawn"),
		},
		[]string{"command"},
	)
}

func (t *ManageEphemeralAgentsTool) Execute(ctx context.Context, params map[string]any) (*shuttle.Result, error) {
	start := time.Now()

	command, ok := params["command"].(string)
	if !ok || command == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_COMMAND",
				Message:    "command parameter is required",
				Suggestion: "Use 'list' to see available agents, 'spawn' to create, or 'despawn' to terminate",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	switch command {
	case "list":
		return t.executeList(ctx, start)
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
				Suggestion: "Use 'list', 'spawn', or 'despawn'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
}

func (t *ManageEphemeralAgentsTool) executeList(ctx context.Context, start time.Time) (*shuttle.Result, error) {
	resp, err := t.handler.ListAvailableAgents(ctx, &ListAvailableAgentsRequest{
		ParentSessionID: t.parentSession,
		ParentAgentID:   t.parentAgentID,
	})
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "LIST_FAILED",
				Message: fmt.Sprintf("Failed to list available agents: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if len(resp.Agents) == 0 {
		return &shuttle.Result{
			Success:         true,
			Data:            "No agents or presets available. Create agents first, then use them here.",
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("Available agents and presets for spawning:\n\n")
	for _, a := range resp.Agents {
		toolStr := "none"
		if len(a.Tools) > 0 {
			toolStr = strings.Join(a.Tools, ", ")
		}
		sb.WriteString(fmt.Sprintf("- **%s** [%s] (id: %s)\n  %s\n  tools: %s\n\n",
			a.Name, a.Source, a.ID, a.Description, toolStr))
	}
	sb.WriteString("Use spawn with agent_id=<id> or preset=<name> to create a sub-agent.")

	return &shuttle.Result{
		Success:         true,
		Data:            sb.String(),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ManageEphemeralAgentsTool) executeSpawn(ctx context.Context, params map[string]any, start time.Time) (*shuttle.Result, error) {
	agentID, _ := params["agent_id"].(string)
	preset, _ := params["preset"].(string)

	if agentID == "" && preset == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_AGENT_OR_PRESET",
				Message:    "Either agent_id or preset is required for spawn",
				Suggestion: "Run 'list' first to see available agents and presets",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

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

	req := &SpawnSubAgentRequest{
		ParentSessionID: t.parentSession,
		ParentAgentID:   t.parentAgentID,
		AgentID:         agentID,
		Preset:          preset,
		WorkflowID:      workflowID,
		InitialMessage:  initialMessage,
		AutoSubscribe:   autoSubscribe,
		Metadata:        metadata,
	}

	resp, err := t.handler.SpawnSubAgent(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SPAWN_FAILED",
				Message:    fmt.Sprintf("Failed to spawn agent: %v", err),
				Suggestion: "Run 'list' to see valid agent IDs and preset names",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	data := map[string]any{
		"command":      "spawn",
		"sub_agent_id": resp.SubAgentID,
		"session_id":   resp.SessionID,
		"status":       resp.Status,
	}
	if resp.Output != "" {
		data["output"] = resp.Output
	}
	if resp.TokensUsed > 0 {
		data["tokens_used"] = resp.TokensUsed
	}
	if resp.CostUSD > 0 {
		data["cost_usd"] = resp.CostUSD
	}
	if resp.DurationMs > 0 {
		data["duration_ms"] = resp.DurationMs
	}
	if len(resp.SubscribedTopics) > 0 {
		data["subscribed_topics"] = resp.SubscribedTopics
	}

	return &shuttle.Result{
		Success:         true,
		Data:            data,
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *ManageEphemeralAgentsTool) executeDespawn(ctx context.Context, params map[string]any, start time.Time) (*shuttle.Result, error) {
	subAgentID, ok := params["sub_agent_id"].(string)
	if !ok || subAgentID == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_SUB_AGENT_ID",
				Message:    "sub_agent_id parameter is required for despawn command",
				Suggestion: "Provide the full ID of the sub-agent to despawn",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	reason, _ := params["reason"].(string)
	if reason == "" {
		reason = "despawned by parent agent"
	}

	req := &DespawnSubAgentRequest{
		ParentSessionID: t.parentSession,
		SubAgentID:      subAgentID,
		Reason:          reason,
	}

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
	return ""
}
