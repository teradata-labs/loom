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
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
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

	// x-mcp-header handling (SEP-2243) is scoped by the specification:
	// clients using the Streamable HTTP transport MUST reject tool
	// definitions with invalid annotations — rejection means excluding the
	// tool from tools/list, so one malformed definition cannot block the
	// rest — while clients on other transports (e.g. stdio) MAY ignore the
	// annotations entirely, and must not hide tools over them. The
	// annotation also only exists under the 2026-07-28 revision, so
	// legacy-negotiated connections ignore it on every transport. The
	// validated annotations are cached for CallTool to mirror into
	// Mcp-Param-* headers; when enforcement is off the cache stays empty
	// and nothing is mirrored.
	enforceHeaderAnnotations := c.IsStateless() && c.transportCarriesHeaders()

	valid := make([]protocol.Tool, 0, len(result.Tools))
	headerParams := make(map[string][]protocol.HeaderParam)
	for _, tool := range result.Tools {
		if !enforceHeaderAnnotations {
			valid = append(valid, tool)
			continue
		}
		hp, err := protocol.ToolHeaderParams(tool)
		if err != nil {
			c.logger.Warn("rejecting tool with invalid x-mcp-header annotation",
				zap.String("tool", tool.Name), zap.Error(err))
			continue
		}
		valid = append(valid, tool)
		if len(hp) > 0 {
			headerParams[tool.Name] = hp
		}
	}

	// Update cache
	c.toolsMu.Lock()
	c.tools = make(map[string]protocol.Tool)
	for _, tool := range valid {
		c.tools[tool.Name] = tool
	}
	c.toolHeaderParams = headerParams
	c.toolsMu.Unlock()

	return valid, nil
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

	// Mirror x-mcp-header-annotated parameters into Mcp-Param-* headers
	// (SEP-2243). The annotations were validated and cached by ListTools.
	c.toolsMu.RLock()
	hps := c.toolHeaderParams[name]
	c.toolsMu.RUnlock()
	if len(hps) > 0 {
		hdrs, err := protocol.HeaderValuesForCall(hps, arguments)
		if err != nil {
			return nil, fmt.Errorf("invalid header parameter for tool %s: %w", name, err)
		}
		if len(hdrs) > 0 {
			ctx = transport.WithExtraHeaders(ctx, hdrs)
		}
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
		// Preserve the full result: error content may carry more than the
		// message — e.g. a resource_link marking a watchable retry condition
		// (issue #343). The rendered message is unchanged.
		return nil, &ToolResultError{Result: &result}
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

// ToolResultError is a tool-level failure (CallToolResult.isError) that
// preserves the full result, so callers can inspect error content beyond the
// message — notably a resource_link marking a watchable retry condition
// (issue #343). Error() renders exactly what the historical flattened error
// did, so string-matching callers and analytics see no change.
type ToolResultError struct {
	Result *protocol.CallToolResult
}

func (e *ToolResultError) Error() string {
	if e.Result != nil && len(e.Result.Content) > 0 && e.Result.Content[0].Type == "text" {
		return fmt.Sprintf("tool error: %s", e.Result.Content[0].Text)
	}
	return "tool returned error"
}

// RetryResourceURI returns the URI of a resource the failed result links as
// its retry condition: the first resource_link (or embedded resource
// reference) in the error content. Empty when the result links nothing —
// the convention is opt-in per server, per error.
func (e *ToolResultError) RetryResourceURI() string {
	if e.Result == nil {
		return ""
	}
	for _, c := range e.Result.Content {
		switch c.Type {
		case "resource_link":
			if c.URI != "" {
				return c.URI
			}
		case "resource":
			if c.Resource != nil && c.Resource.URI != "" {
				return c.Resource.URI
			}
		}
	}
	return ""
}
