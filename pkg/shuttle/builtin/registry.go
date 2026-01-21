// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"fmt"

	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/metadata"
)

// All creates all builtin tools.
// If promptRegistry is provided, tools will load descriptions from PromptRegistry.
// Falls back to hardcoded descriptions if prompts not found or registry is nil.
func All(promptRegistry prompts.PromptRegistry) []shuttle.Tool {
	tools := []shuttle.Tool{
		NewHTTPClientTool(),
		NewWebSearchTool(),
		NewFileWriteTool(""),
		NewFileReadTool(""),
		NewVisionTool(""),
		NewDocumentParseTool(""),
		NewGRPCClientTool(),
		NewShellExecuteTool(""),
		shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{}),
	}

	// Wrap with PromptAwareTool if registry provided
	if promptRegistry != nil {
		wrapped := make([]shuttle.Tool, len(tools))
		for i, tool := range tools {
			// Key format: tools.{tool_name}
			key := fmt.Sprintf("tools.%s", tool.Name())
			wrapped[i] = shuttle.NewPromptAwareTool(tool, promptRegistry, key)
		}
		return wrapped
	}

	return tools
}

// ByName returns a builtin tool by name. Returns nil if not found.
func ByName(name string) shuttle.Tool {
	switch name {
	case "http_request":
		return NewHTTPClientTool()
	case "web_search":
		return NewWebSearchTool()
	case "file_write":
		return NewFileWriteTool("")
	case "file_read":
		return NewFileReadTool("")
	case "analyze_image":
		return NewVisionTool("")
	case "parse_document":
		return NewDocumentParseTool("")
	case "grpc_call":
		return NewGRPCClientTool()
	case "shell_execute":
		return NewShellExecuteTool("")
	case "contact_human":
		return shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{})
	default:
		return nil
	}
}

// Names returns the names of all builtin tools.
// Note: spawn_agent is NOT included - it requires per-agent context (session ID, spawn handler)
// and must be created via NewSpawnAgentTool() when setting up agents.
func Names() []string {
	return []string{
		"http_request",
		"web_search",
		"file_write",
		"file_read",
		"analyze_image",
		"parse_document",
		"grpc_call",
		"shell_execute",
		"contact_human",
	}
}

// RegisterAll registers all builtin tools with a registry.
// Uses hardcoded tool descriptions (for backward compatibility).
func RegisterAll(registry *shuttle.Registry) {
	for _, tool := range All(nil) {
		registry.Register(tool)
	}
}

// RegisterByNames registers only the specified builtin tools.
// Apple-style: Only load what you need.
func RegisterByNames(registry *shuttle.Registry, names []string) {
	for _, name := range names {
		tool := ByName(name)
		if tool == nil {
			// Skip unknown tools (could be MCP or custom)
			continue
		}
		registry.Register(tool)
	}
}

// CommunicationTools creates communication tools for an agent.
// These tools require infrastructure (MessageQueue, SharedMemoryStore) and agent ID.
// They cannot be created via All() or ByName() since they need per-agent context.
//
// Includes:
// - send_message, receive_message (point-to-point messaging)
// - publish, subscribe, receive_broadcast (pub-sub broadcast messaging)
// - shared_memory_write, shared_memory_read (zero-copy data sharing)
// - top_n_query, group_by_query (presentation strategies)
//
// Note: Visualization tools (generate_workflow_visualization, generate_visualization)
// are NOT included by default. Use VisualizationTools() to get them for metaagent assignment.
func CommunicationTools(queue *communication.MessageQueue, bus *communication.MessageBus, store *communication.SharedMemoryStore, agentID string) []shuttle.Tool {
	tools := make([]shuttle.Tool, 0, 10)

	if queue != nil {
		tools = append(tools,
			NewSendMessageTool(queue, agentID),
			NewReceiveMessageTool(queue, agentID),
		)
	}

	if bus != nil {
		tools = append(tools,
			NewSubscribeTool(bus, agentID),
			NewPublishTool(bus, agentID),
			NewReceiveBroadcastTool(bus, agentID),
		)
	}

	if store != nil {
		// Shared memory read/write
		tools = append(tools,
			NewSharedMemoryWriteTool(store, agentID),
			NewSharedMemoryReadTool(store, agentID),
		)
	}

	// Presentation strategy tools (queries shared memory, visualizes workflows)
	// Note: Some presentation tools (workflow viz) don't require store
	tools = append(tools, PresentationTools(store, agentID)...)

	return tools
}

// CommunicationToolNames returns the names of communication tools.
// Note: Visualization tools are not included - use VisualizationToolNames() for those.
func CommunicationToolNames() []string {
	return []string{
		"send_message",
		"receive_message",
		"subscribe",
		"publish",
		"receive_broadcast",
		"shared_memory_write",
		"shared_memory_read",
		"top_n_query",
		"group_by_query",
	}
}

// ToolSearchName is the name of the tool_search tool.
const ToolSearchName = "tool_search"

// metadataLoader is a singleton loader with caching for optimal performance.
var metadataLoader = metadata.NewLoader("tool_metadata")

// LoadMetadata loads rich metadata for a builtin tool with caching.
// Returns nil if metadata file not found or tool is not a builtin.
// Metadata includes: use_cases, conflicts, alternatives, examples, best_practices, etc.
// Subsequent calls for the same tool return cached results without file I/O.
func LoadMetadata(toolName string) (*metadata.ToolMetadata, error) {
	return metadataLoader.Load(toolName)
}

// LoadAllMetadata loads metadata for all builtin tools with caching.
// Returns a map of tool name -> metadata.
// Tools without metadata files are omitted from the map.
func LoadAllMetadata() (map[string]*metadata.ToolMetadata, error) {
	return metadataLoader.LoadAll()
}
