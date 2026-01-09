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
// Package client implements MCP client tools support.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// ListTools returns all available tools from the server
func (c *Client) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	// Create request
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
	}

	// Send request
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Parse result
	var result protocol.ToolListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools/list result: %w", err)
	}

	// Update cache
	c.toolsMu.Lock()
	c.tools = make(map[string]protocol.Tool)
	for _, tool := range result.Tools {
		c.tools[tool.Name] = tool
	}
	c.toolsMu.Unlock()

	return result.Tools, nil
}

// CallTool invokes a tool with given arguments
// Returns interface{} to avoid import cycles in shuttle package (actual type is *protocol.CallToolResult)
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	// Get tool definition for validation
	tool, err := c.getTool(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("tool %s not found: %w", name, err)
	}

	// Validate arguments against schema
	if err := protocol.ValidateToolArguments(tool, arguments); err != nil {
		return nil, fmt.Errorf("invalid arguments for tool %s: %w", name, err)
	}

	// Create params
	params := protocol.CallToolParams{
		Name:      name,
		Arguments: arguments,
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	// Create request
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "tools/call",
		Params:  paramsJSON,
	}

	// Send request
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Parse result
	var result protocol.CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools/call result: %w", err)
	}

	// Check if tool returned error
	if result.IsError {
		// Extract error message from content
		if len(result.Content) > 0 && result.Content[0].Type == "text" {
			return nil, fmt.Errorf("tool error: %s", result.Content[0].Text)
		}
		return nil, fmt.Errorf("tool returned error")
	}

	return &result, nil
}

// getTool retrieves tool definition from cache or server
func (c *Client) getTool(ctx context.Context, name string) (protocol.Tool, error) {
	c.toolsMu.RLock()
	tool, exists := c.tools[name]
	c.toolsMu.RUnlock()

	if exists {
		return tool, nil
	}

	// Not in cache - fetch from server
	_, err := c.ListTools(ctx)
	if err != nil {
		return protocol.Tool{}, err
	}

	// Find tool
	c.toolsMu.RLock()
	tool, exists = c.tools[name]
	c.toolsMu.RUnlock()

	if !exists {
		return protocol.Tool{}, fmt.Errorf("tool %s not found", name)
	}

	return tool, nil
}
