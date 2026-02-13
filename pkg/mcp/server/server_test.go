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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap/zaptest"
)

func TestNewMCPServer(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	require.NotNil(t, s)
	assert.Equal(t, "test-server", s.info.Name)
	assert.Equal(t, "1.0.0", s.info.Version)

	// Built-in handlers should be registered
	s.mu.RLock()
	_, hasInit := s.handlers["initialize"]
	_, hasNotif := s.handlers["notifications/initialized"]
	_, hasPing := s.handlers["ping"]
	s.mu.RUnlock()

	assert.True(t, hasInit)
	assert.True(t, hasNotif)
	assert.True(t, hasPing)
}

func TestNewMCPServer_NilLogger(t *testing.T) {
	s := NewMCPServer("test", "1.0.0", nil)
	require.NotNil(t, s)
	require.NotNil(t, s.logger)
}

func TestMCPServer_HandleInitialize(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger,
		WithExtensions(protocol.ServerAppsExtension()),
	)

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)
	require.NotNil(t, resp.Result)

	var result protocol.InitializeResult
	err = json.Unmarshal(resp.Result, &result)
	require.NoError(t, err)

	assert.Equal(t, protocol.ProtocolVersion, result.ProtocolVersion)
	assert.Equal(t, "test-server", result.ServerInfo.Name)
	assert.Equal(t, "1.0.0", result.ServerInfo.Version)
	assert.True(t, protocol.ClientSupportsApps(result.Extensions))
}

func TestMCPServer_HandlePing(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "ping",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)
}

func TestMCPServer_HandleNotificationsInitialized(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Notification has no ID
	req := protocol.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	assert.Nil(t, respBytes) // Notifications return no response
}

func TestMCPServer_HandleUnknownMethod(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "unknown/method",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.MethodNotFound, resp.Error.Code)
}

func TestMCPServer_HandleUnknownNotification(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Notification (no ID) for unknown method - should be ignored
	req := protocol.Request{
		JSONRPC: "2.0",
		Method:  "notifications/unknown",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	assert.Nil(t, respBytes) // Silently ignored
}

func TestMCPServer_HandleInvalidJSONRPCVersion(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	req := protocol.Request{
		JSONRPC: "1.0", // Wrong version
		ID:      protocol.NewNumericRequestID(1),
		Method:  "ping",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidRequest, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "invalid jsonrpc version")
}

func TestMCPServer_HandleMissingMethod(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Send a request with valid jsonrpc version but empty method
	reqBytes := []byte(`{"jsonrpc":"2.0","id":1}`)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InvalidRequest, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "method is required")
}

func TestMCPServer_HandleInvalidJSON(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	respBytes, err := s.HandleMessage(context.Background(), []byte("not json"))
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.ParseError, resp.Error.Code)
}

func TestMCPServer_RegisterHandler(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	called := false
	s.RegisterHandler("custom/method", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		called = true
		return map[string]string{"status": "ok"}, nil
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "custom/method",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)
	assert.True(t, called)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)
}

func TestMCPServer_HandlerError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	s.RegisterHandler("failing/method", func(_ context.Context, _ json.RawMessage, _ json.RawMessage) (interface{}, error) {
		return nil, assert.AnError
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "failing/method",
	}
	reqBytes, err := json.Marshal(req)
	require.NoError(t, err)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)
	require.NotNil(t, respBytes)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.InternalError, resp.Error.Code)
}

func TestMCPServer_WithToolProvider(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{
		tools: []protocol.Tool{
			{Name: "test_tool", Description: "a test tool"},
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	require.NotNil(t, s.capabilities.Tools)

	// tools/list should be registered
	s.mu.RLock()
	_, hasList := s.handlers["tools/list"]
	_, hasCall := s.handlers["tools/call"]
	s.mu.RUnlock()
	assert.True(t, hasList)
	assert.True(t, hasCall)
}

func TestMCPServer_WithResourceProvider(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{
		resources: []protocol.Resource{
			{URI: "ui://test/resource", Name: "test"},
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	require.NotNil(t, s.capabilities.Resources)

	s.mu.RLock()
	_, hasList := s.handlers["resources/list"]
	_, hasRead := s.handlers["resources/read"]
	s.mu.RUnlock()
	assert.True(t, hasList)
	assert.True(t, hasRead)
}

// mockToolProvider implements ToolProvider for testing.
type mockToolProvider struct {
	tools    []protocol.Tool
	callFunc func(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error)
}

func (m *mockToolProvider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return m.tools, nil
}

func (m *mockToolProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if m.callFunc != nil {
		return m.callFunc(ctx, name, args)
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{{Type: "text", Text: "mock result"}},
	}, nil
}

// mockResourceProvider implements ResourceProvider for testing.
type mockResourceProvider struct {
	resources []protocol.Resource
	readFunc  func(ctx context.Context, uri string) (*protocol.ReadResourceResult, error)
}

func (m *mockResourceProvider) ListResources(_ context.Context) ([]protocol.Resource, error) {
	return m.resources, nil
}

func (m *mockResourceProvider) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	if m.readFunc != nil {
		return m.readFunc(ctx, uri)
	}
	return &protocol.ReadResourceResult{
		Contents: []protocol.ResourceContents{
			{URI: uri, Text: "mock content"},
		},
	}, nil
}

func TestMCPServer_HandleInitialize_WithClientInfo(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	params, _ := json.Marshal(protocol.InitializeParams{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientInfo: protocol.Implementation{
			Name:    "claude-desktop",
			Version: "1.2.3",
		},
		Capabilities: protocol.ClientCapabilities{
			Sampling: &protocol.SamplingCapability{},
			Roots:    &protocol.RootsCapability{},
		},
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	// Verify client info was stored
	info := s.ClientInfo()
	require.NotNil(t, info)
	assert.Equal(t, "claude-desktop", info.Name)
	assert.Equal(t, "1.2.3", info.Version)

	// Verify client capabilities were stored
	caps := s.ClientCapabilities()
	require.NotNil(t, caps)
	assert.NotNil(t, caps.Sampling, "sampling capability should be stored")
	assert.NotNil(t, caps.Roots, "roots capability should be stored")
}

func TestMCPServer_HandleInitialize_NilCapabilities(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	params, _ := json.Marshal(protocol.InitializeParams{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientInfo: protocol.Implementation{
			Name:    "simple-client",
			Version: "0.1.0",
		},
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	// Capabilities should be stored (even if empty)
	caps := s.ClientCapabilities()
	require.NotNil(t, caps)
	assert.Nil(t, caps.Sampling, "no sampling capability")
	assert.Nil(t, caps.Roots, "no roots capability")
}

func TestMCPServer_HandleInitialize_VersionMismatch(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	params, _ := json.Marshal(protocol.InitializeParams{
		ProtocolVersion: "2099-01-01",
		ClientInfo: protocol.Implementation{
			Name:    "future-client",
			Version: "9.0.0",
		},
	})

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
		Params:  params,
	}
	reqBytes, _ := json.Marshal(req)

	// Should succeed (with warning logged) - we don't reject mismatched versions
	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	// Client info should still be stored
	info := s.ClientInfo()
	require.NotNil(t, info)
	assert.Equal(t, "future-client", info.Name)
}

func TestMCPServer_HandleInitialize_EmptyParams(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
	}
	reqBytes, _ := json.Marshal(req)

	// Should succeed even with no params (backwards compatibility)
	respBytes, err := s.HandleMessage(context.Background(), reqBytes)
	require.NoError(t, err)

	var resp protocol.Response
	err = json.Unmarshal(respBytes, &resp)
	require.NoError(t, err)
	assert.Nil(t, resp.Error)

	// No client info
	assert.Nil(t, s.ClientInfo())
}

func TestMCPServer_HandleInitialize_InvalidParams(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test-server", "1.0.0", logger)

	req := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.NewNumericRequestID(1),
		Method:  "initialize",
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

func TestMCPServer_NotifyResourceListChanged(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Send a notification
	s.NotifyResourceListChanged()

	// Read from the channel
	select {
	case notif := <-s.notifyCh:
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			ID      json.RawMessage `json:"id,omitempty"`
		}
		err := json.Unmarshal(notif, &msg)
		require.NoError(t, err)
		assert.Equal(t, "2.0", msg.JSONRPC)
		assert.Equal(t, "notifications/resources/list_changed", msg.Method)
		assert.Nil(t, msg.ID) // Notifications have no id
	default:
		t.Fatal("expected notification in channel")
	}
}

func TestMCPServer_NotifyResourceListChanged_ChannelFull(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Fill the channel (capacity 16)
	for i := 0; i < 16; i++ {
		s.NotifyResourceListChanged()
	}

	// One more should be dropped (not panic or block)
	s.NotifyResourceListChanged()

	// Verify channel is still full
	assert.Len(t, s.notifyCh, 16)
}

func TestMCPServer_NotifyResourceListChanged_Concurrent(t *testing.T) {
	logger := zaptest.NewLogger(t)
	s := NewMCPServer("test", "1.0.0", logger)

	// Drain channel in background to prevent blocking
	stopDrain := make(chan struct{})
	go func() {
		for {
			select {
			case <-s.notifyCh:
			case <-stopDrain:
				return
			}
		}
	}()

	// Send from multiple goroutines concurrently - the race detector is the
	// primary assertion here (no data races allowed).
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.NotifyResourceListChanged()
		}()
	}
	wg.Wait()
	close(stopDrain)
}

func TestMCPServer_WithResourceProvider_ListChanged(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockResourceProvider{
		resources: []protocol.Resource{
			{URI: "ui://test/resource", Name: "test"},
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithResourceProvider(provider))

	require.NotNil(t, s.capabilities.Resources)
	assert.True(t, s.capabilities.Resources.ListChanged, "ListChanged should be true when resource provider is configured")
}

func TestMCPServer_ConcurrentHandleMessage(t *testing.T) {
	logger := zaptest.NewLogger(t)
	provider := &mockToolProvider{
		tools: []protocol.Tool{
			{Name: "tool_a", Description: "Tool A"},
		},
		callFunc: func(_ context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
			return &protocol.CallToolResult{
				Content: []protocol.Content{{Type: "text", Text: "result"}},
			}, nil
		},
	}

	s := NewMCPServer("test", "1.0.0", logger, WithToolProvider(provider))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var req protocol.Request
			switch i % 4 {
			case 0:
				req = protocol.Request{JSONRPC: "2.0", ID: protocol.NewNumericRequestID(int64(i)), Method: "ping"}
			case 1:
				req = protocol.Request{JSONRPC: "2.0", ID: protocol.NewNumericRequestID(int64(i)), Method: "tools/list"}
			case 2:
				params, _ := json.Marshal(protocol.CallToolParams{Name: "tool_a"})
				req = protocol.Request{JSONRPC: "2.0", ID: protocol.NewNumericRequestID(int64(i)), Method: "tools/call", Params: params}
			case 3:
				req = protocol.Request{JSONRPC: "2.0", Method: "notifications/initialized"}
			}
			reqBytes, _ := json.Marshal(req)
			resp, err := s.HandleMessage(context.Background(), reqBytes)
			assert.NoError(t, err)
			if i%4 == 3 {
				assert.Nil(t, resp) // notification
			} else {
				assert.NotNil(t, resp)
			}
		}(i)
	}
	wg.Wait()
}
