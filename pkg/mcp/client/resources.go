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
// Package client implements MCP client resources support.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// ListResources returns all available resources from the server
func (c *Client) ListResources(ctx context.Context) ([]protocol.Resource, error) {
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "resources/list",
		Params:  json.RawMessage(`{}`),
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var result protocol.ResourceListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources/list result: %w", err)
	}

	return result.Resources, nil
}

// ReadResource reads a resource by URI
func (c *Client) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	params := protocol.ReadResourceParams{
		URI: uri,
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "resources/read",
		Params:  paramsJSON,
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	var result protocol.ReadResourceResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse resources/read result: %w", err)
	}

	return &result, nil
}

// SubscribeResource subscribes to resource changes
func (c *Client) SubscribeResource(ctx context.Context, uri string) error {
	// Check if server supports subscriptions
	c.mu.RLock()
	supportsSubscribe := c.serverCapabilities.Resources != nil && c.serverCapabilities.Resources.Subscribe
	c.mu.RUnlock()

	if !supportsSubscribe {
		return fmt.Errorf("server does not support resource subscriptions")
	}

	params := map[string]string{"uri": uri}
	paramsJSON, _ := json.Marshal(params)

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "resources/subscribe",
		Params:  paramsJSON,
	}

	_, err := c.sendRequest(ctx, req)
	return err
}

// UnsubscribeResource unsubscribes from resource changes
func (c *Client) UnsubscribeResource(ctx context.Context, uri string) error {
	params := map[string]string{"uri": uri}
	paramsJSON, _ := json.Marshal(params)

	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      c.nextRequestID(),
		Method:  "resources/unsubscribe",
		Params:  paramsJSON,
	}

	_, err := c.sendRequest(ctx, req)
	return err
}
