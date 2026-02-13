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

package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// newToolsListHandler creates a handler for tools/list.
func newToolsListHandler(provider ToolProvider) MethodHandler {
	return func(ctx context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		tools, err := provider.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		return protocol.ToolListResult{Tools: tools}, nil
	}
}

// newToolsCallHandler creates a handler for tools/call.
func newToolsCallHandler(provider ToolProvider) MethodHandler {
	return func(ctx context.Context, _ json.RawMessage, params json.RawMessage) (interface{}, error) {
		var callParams protocol.CallToolParams
		if err := json.Unmarshal(params, &callParams); err != nil {
			return nil, protocol.NewError(protocol.InvalidParams, fmt.Sprintf("invalid tool call params: %v", err), nil)
		}

		if callParams.Name == "" {
			return nil, protocol.NewError(protocol.InvalidParams, "tool name is required", nil)
		}

		result, err := provider.CallTool(ctx, callParams.Name, callParams.Arguments)
		if err != nil {
			// Return the error as an MCP tool error result (isError: true)
			return &protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: err.Error()},
				},
				IsError: true,
			}, nil
		}

		return result, nil
	}
}

// newResourcesListHandler creates a handler for resources/list.
func newResourcesListHandler(provider ResourceProvider) MethodHandler {
	return func(ctx context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		resources, err := provider.ListResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("list resources: %w", err)
		}
		return protocol.ResourceListResult{Resources: resources}, nil
	}
}

// newResourcesReadHandler creates a handler for resources/read.
func newResourcesReadHandler(provider ResourceProvider) MethodHandler {
	return func(ctx context.Context, _ json.RawMessage, params json.RawMessage) (interface{}, error) {
		var readParams protocol.ReadResourceParams
		if err := json.Unmarshal(params, &readParams); err != nil {
			return nil, protocol.NewError(protocol.InvalidParams, fmt.Sprintf("invalid resource read params: %v", err), nil)
		}

		if readParams.URI == "" {
			return nil, protocol.NewError(protocol.InvalidParams, "resource URI is required", nil)
		}

		result, err := provider.ReadResource(ctx, readParams.URI)
		if err != nil {
			return nil, fmt.Errorf("read resource %q: %w", readParams.URI, err)
		}

		return result, nil
	}
}
