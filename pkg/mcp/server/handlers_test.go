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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap/zaptest"
)

func TestToolsList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{
		tools: []protocol.Tool{
			{Name: "tool_a", Description: "Tool A"},
			{Name: "tool_b", Description: "Tool B"},
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/list",
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	var result protocol.ToolListResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Len(t, result.Tools, 2)
	assert.Equal(t, "tool_a", result.Tools[0].Name)
	assert.Equal(t, "tool_b", result.Tools[1].Name)
}

func TestToolsCall_Success(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{
		tools: []protocol.Tool{{Name: "echo"}},
		callFunc: func(_ context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
			return &protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: fmt.Sprintf("called %s with %v", name, args)},
				},
			}, nil
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	params, _ := json.Marshal(protocol.CallToolParams{
		Name:      "echo",
		Arguments: map[string]interface{}{"message": "hello"},
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/call",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	var result protocol.CallToolResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "called echo")
}

func TestToolsCall_Error(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{
		callFunc: func(_ context.Context, _ string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
			return nil, fmt.Errorf("tool execution failed")
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	params, _ := json.Marshal(protocol.CallToolParams{Name: "failing_tool"})
	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/call",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error) // No JSON-RPC error

	var result protocol.CallToolResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "tool execution failed")
}

func TestToolsCall_InvalidParams(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`invalid`),
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	// Invalid JSON in params triggers ParseError (-32700) since the outer request
	// contains an invalid Params field that fails initial JSON parse
	assert.Equal(t, protocol.ParseError, resp.Error.Code)
}

func TestToolsCall_EmptyName(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	params, _ := json.Marshal(protocol.CallToolParams{Name: ""})
	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/call",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
}

func TestResourcesList(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{
		resources: []protocol.Resource{
			{URI: "ui://loom/viewer", Name: "Conversation Viewer", MimeType: protocol.ResourceMIME},
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/list",
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	var result protocol.ResourceListResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "ui://loom/viewer", result.Resources[0].URI)
	assert.Equal(t, protocol.ResourceMIME, result.Resources[0].MimeType)
}

func TestResourcesRead_Success(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{
		readFunc: func(_ context.Context, uri string) (*protocol.ReadResourceResult, error) {
			return &protocol.ReadResourceResult{
				Contents: []protocol.ResourceContents{
					{URI: uri, MimeType: protocol.ResourceMIME, Text: "<html>test</html>"},
				},
			}, nil
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	params, _ := json.Marshal(protocol.ReadResourceParams{URI: "ui://loom/viewer"})
	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/read",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	var result protocol.ReadResourceResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, "<html>test</html>", result.Contents[0].Text)
}

func TestResourcesRead_EmptyURI(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	params, _ := json.Marshal(protocol.ReadResourceParams{URI: ""})
	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/read",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
}

func TestResourcesRead_Error(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{
		readFunc: func(_ context.Context, uri string) (*protocol.ReadResourceResult, error) {
			return nil, fmt.Errorf("resource not found: %s", uri)
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	params, _ := json.Marshal(protocol.ReadResourceParams{URI: "ui://loom/nonexistent"})
	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/read",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InternalError, resp.Error.Code)
}

// errorResourceProvider always returns errors.
type errorResourceProvider struct {
	err error
}

func (p *errorResourceProvider) ListResources(_ context.Context) ([]protocol.Resource, error) {
	return nil, p.err
}

func (p *errorResourceProvider) ReadResource(_ context.Context, _ string) (*protocol.ReadResourceResult, error) {
	return nil, p.err
}

// errorToolProvider always returns errors.
type errorToolProvider struct {
	err error
}

func (p *errorToolProvider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return nil, p.err
}

func (p *errorToolProvider) CallTool(_ context.Context, _ string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, p.err
}

func TestResourcesRead_InvalidParams(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/read",
		Params:  json.RawMessage(`"not an object"`),
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidParams, resp.Error.Code)
}

func TestResourcesList_ProviderError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	errorProvider := &errorResourceProvider{err: fmt.Errorf("database unavailable")}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(errorProvider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "resources/list",
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InternalError, resp.Error.Code)
}

func TestToolsList_ProviderError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	errorProvider := &errorToolProvider{err: fmt.Errorf("tool registry unavailable")}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(errorProvider))

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "tools/list",
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InternalError, resp.Error.Code)
}
