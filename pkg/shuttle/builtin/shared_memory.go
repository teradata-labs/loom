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

// SharedMemoryWriteTool provides shared memory write access for agents.
// Stores data that can be accessed by other agents in the workflow.
type SharedMemoryWriteTool struct {
	store   *communication.SharedMemoryStore
	agentID string
}

// NewSharedMemoryWriteTool creates a new shared memory write tool.
func NewSharedMemoryWriteTool(store *communication.SharedMemoryStore, agentID string) *SharedMemoryWriteTool {
	return &SharedMemoryWriteTool{
		store:   store,
		agentID: agentID,
	}
}

func (t *SharedMemoryWriteTool) Name() string {
	return "shared_memory_write"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/communication.yaml).
// This fallback is used only when prompts are not configured.
func (t *SharedMemoryWriteTool) Description() string {
	return `Writes data to shared memory accessible by all agents in the workflow.

Use this tool to:
- Store comprehensive results for other agents to query
- Share large datasets between workflow stages
- Cache frequently accessed data
- Maintain workflow state accessible to all agents

Data is organized in namespaces:
- GLOBAL: Accessible to all agents
- WORKFLOW: Scoped to current workflow
- SWARM: Scoped to agent swarm

Use shared_memory_read to retrieve stored data.`
}

func (t *SharedMemoryWriteTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for writing to shared memory",
		map[string]*shuttle.JSONSchema{
			"key":   shuttle.NewStringSchema("Unique key to store data under (required)"),
			"value": shuttle.NewStringSchema("Data to store (required, JSON string or plain text)"),
			"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', 'swarm', or 'agent' (default: global). Use 'agent' for agent-private data.").
				WithEnum("global", "workflow", "swarm", "agent").
				WithDefault("global"),
			"metadata": shuttle.NewObjectSchema(
				"Optional metadata (key-value pairs)",
				map[string]*shuttle.JSONSchema{},
				nil,
			),
		},
		[]string{"key", "value"},
	)
}

func (t *SharedMemoryWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate store availability
	if t.store == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "STORE_NOT_AVAILABLE",
				Message:    "Shared memory store not configured for this agent",
				Suggestion: "Communication tools require MultiAgentServer with shared memory configured",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract required parameters
	key, ok := params["key"].(string)
	if !ok || key == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "key is required",
				Suggestion: "Provide a unique key to store data under (e.g., 'stage-10-results')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	value, ok := params["value"].(string)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "value is required and must be a string",
				Suggestion: "Provide data to store (JSON string or plain text)",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Parse namespace
	namespaceStr := "global"
	if ns, ok := params["namespace"].(string); ok && ns != "" {
		namespaceStr = ns
	}

	var namespace loomv1.SharedMemoryNamespace
	switch namespaceStr {
	case "global":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL
	case "workflow":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	case "swarm":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM
	case "agent":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_NAMESPACE",
				Message:    fmt.Sprintf("Unknown namespace: %s", namespaceStr),
				Suggestion: "Use 'global', 'workflow', 'swarm', or 'agent'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract metadata
	var metadata map[string]string
	if m, ok := params["metadata"].(map[string]interface{}); ok {
		metadata = make(map[string]string)
		for k, v := range m {
			if vs, ok := v.(string); ok {
				metadata[k] = vs
			}
		}
	}

	// Write to shared memory
	req := &loomv1.PutSharedMemoryRequest{
		Namespace: namespace,
		Key:       key,
		Value:     []byte(value),
		AgentId:   t.agentID,
		Metadata:  metadata,
	}

	resp, err := t.store.Put(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "WRITE_FAILED",
				Message:    fmt.Sprintf("Failed to write to shared memory: %v", err),
				Retryable:  true,
				Suggestion: "Check if shared memory store is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Build success result
	result := map[string]interface{}{
		"written_at": time.Now().Format(time.RFC3339),
		"key":        key,
		"namespace":  namespaceStr,
		"size_bytes": len(value),
		"version":    resp.Version,
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"key":       key,
			"namespace": namespaceStr,
			"version":   resp.Version,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *SharedMemoryWriteTool) Backend() string {
	return "" // Backend-agnostic
}

// SharedMemoryReadTool provides shared memory read access for agents.
// Retrieves data stored by other agents in the workflow.
type SharedMemoryReadTool struct {
	store   *communication.SharedMemoryStore
	agentID string
}

// NewSharedMemoryReadTool creates a new shared memory read tool.
func NewSharedMemoryReadTool(store *communication.SharedMemoryStore, agentID string) *SharedMemoryReadTool {
	return &SharedMemoryReadTool{
		store:   store,
		agentID: agentID,
	}
}

func (t *SharedMemoryReadTool) Name() string {
	return "shared_memory_read"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/communication.yaml).
// This fallback is used only when prompts are not configured.
func (t *SharedMemoryReadTool) Description() string {
	return `Reads data from shared memory written by other agents in the workflow.

Use this tool to:
- Access comprehensive results from previous stages
- Read large datasets stored by other agents
- Query workflow state
- Retrieve cached data

Data is organized in namespaces:
- GLOBAL: Accessible to all agents
- WORKFLOW: Scoped to current workflow
- AGENT: Private to specific agents

Use shared_memory_write to store data for others to read.`
}

func (t *SharedMemoryReadTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for reading from shared memory",
		map[string]*shuttle.JSONSchema{
			"key": shuttle.NewStringSchema("Key to retrieve data from (required)"),
			"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', 'swarm', or 'agent' (default: global). Use 'agent' for agent-private data.").
				WithEnum("global", "workflow", "swarm", "agent").
				WithDefault("global"),
		},
		[]string{"key"},
	)
}

func (t *SharedMemoryReadTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Validate store availability
	if t.store == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "STORE_NOT_AVAILABLE",
				Message:    "Shared memory store not configured for this agent",
				Suggestion: "Communication tools require MultiAgentServer with shared memory configured",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract required parameters
	key, ok := params["key"].(string)
	if !ok || key == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "key is required",
				Suggestion: "Provide the key to read from (e.g., 'stage-10-results')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Parse namespace
	namespaceStr := "global"
	if ns, ok := params["namespace"].(string); ok && ns != "" {
		namespaceStr = ns
	}

	var namespace loomv1.SharedMemoryNamespace
	switch namespaceStr {
	case "global":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL
	case "workflow":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW
	case "swarm":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM
	case "agent":
		namespace = loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_NAMESPACE",
				Message:    fmt.Sprintf("Unknown namespace: %s", namespaceStr),
				Suggestion: "Use 'global', 'workflow', 'swarm', or 'agent'",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Read from shared memory
	req := &loomv1.GetSharedMemoryRequest{
		Namespace: namespace,
		Key:       key,
		AgentId:   t.agentID,
	}

	resp, err := t.store.Get(ctx, req)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "READ_FAILED",
				Message:    fmt.Sprintf("Failed to read from shared memory: %v", err),
				Retryable:  true,
				Suggestion: "Check if key exists and shared memory store is operational",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if key was found
	if !resp.Found {
		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"found":     false,
				"key":       key,
				"namespace": namespaceStr,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract value
	valueStr := string(resp.Value.Value)

	// Try to parse as JSON for better readability
	var jsonData interface{}
	isJSON := false
	if json.Valid(resp.Value.Value) {
		if err := json.Unmarshal(resp.Value.Value, &jsonData); err == nil {
			isJSON = true
		}
	}

	// Build result
	result := map[string]interface{}{
		"found":      true,
		"key":        key,
		"namespace":  namespaceStr,
		"version":    resp.Value.Version,
		"written_by": resp.Value.UpdatedBy,
		"written_at": resp.Value.UpdatedAt,
		"size_bytes": len(resp.Value.Value),
	}

	if isJSON {
		result["value"] = jsonData
		result["value_type"] = "json"
	} else {
		result["value"] = valueStr
		result["value_type"] = "text"
	}

	if len(resp.Value.Metadata) > 0 {
		result["metadata"] = resp.Value.Metadata
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"key":        key,
			"namespace":  namespaceStr,
			"written_by": resp.Value.UpdatedBy,
			"is_json":    isJSON,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *SharedMemoryReadTool) Backend() string {
	return "" // Backend-agnostic
}
