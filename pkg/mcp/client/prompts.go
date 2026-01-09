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
// Package client implements MCP client prompts support.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// ListPrompts returns all available prompts from the server
func (c *Client) ListPrompts(ctx context.Context) ([]protocol.Prompt, error) {
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "prompts/list",
		Params:  json.RawMessage(`{}`),
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var result protocol.PromptListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse prompts/list result: %w", err)
	}

	return result.Prompts, nil
}

// GetPrompt retrieves a prompt with arguments
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]interface{}) (*protocol.GetPromptResult, error) {
	params := protocol.GetPromptParams{
		Name:      name,
		Arguments: arguments,
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "prompts/get",
		Params:  paramsJSON,
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var result protocol.GetPromptResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse prompts/get result: %w", err)
	}

	return &result, nil
}
