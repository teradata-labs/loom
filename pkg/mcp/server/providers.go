// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package server implements a Model Context Protocol (MCP) server.
// It provides a JSON-RPC dispatcher, method handlers, and provider interfaces
// that allow exposing tools and resources to MCP clients.
package server

import (
	"context"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// ToolProvider supplies tools to the MCP server.
// Implementations map domain-specific capabilities to MCP tool definitions.
type ToolProvider interface {
	// ListTools returns all available tools.
	ListTools(ctx context.Context) ([]protocol.Tool, error)

	// CallTool invokes a tool by name with the given arguments.
	CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error)
}

// ProgressEmitter delivers incremental progress for a streaming tool call as
// MCP `notifications/progress` events. Calls are a no-op when the client did
// not supply a progress token (progress is opt-in per the MCP spec).
type ProgressEmitter interface {
	// EmitProgress sends one progress update. progress and total are on the
	// same scale (e.g. 0..100); total may be 0 if unknown.
	EmitProgress(progress, total float64) error
	// EmitMessage sends a human-readable status (the MCP progress `message`
	// field) carrying a monotonically increasing progress counter so the
	// notification is spec-valid. loom uses this to stream the agent's
	// cumulative partial response text as it generates.
	EmitMessage(message string) error
}

// StreamingToolProvider is an optional extension of ToolProvider for tools that
// can stream progress while running. The MCP server type-asserts its registered
// ToolProvider to this interface; if absent, every tool uses the synchronous
// CallTool path.
type StreamingToolProvider interface {
	// SupportsStreaming reports whether the named tool streams progress.
	SupportsStreaming(name string) bool
	// CallToolStream invokes a tool, forwarding progress via emit, and returns
	// the final result. Implementations should fall back to a non-streaming
	// execution for tools that do not stream.
	CallToolStream(ctx context.Context, name string, args map[string]interface{}, progressToken string, emit ProgressEmitter) (*protocol.CallToolResult, error)
}

// ResourceProvider supplies resources to the MCP server.
// Implementations expose domain-specific data and UI resources.
type ResourceProvider interface {
	// ListResources returns all available resources.
	ListResources(ctx context.Context) ([]protocol.Resource, error)

	// ReadResource reads a resource by its URI.
	ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error)
}
