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

// ResourceProvider supplies resources to the MCP server.
// Implementations expose domain-specific data and UI resources.
type ResourceProvider interface {
	// ListResources returns all available resources.
	ListResources(ctx context.Context) ([]protocol.Resource, error)

	// ReadResource reads a resource by its URI.
	ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error)
}
