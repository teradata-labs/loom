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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
)

// TestIntegration_FullMCPFlow exercises the complete MCP lifecycle:
//
//	initialize → list tools → call tool → list resources → read UI resource
//
// It wires up a real MCPServer with a LoomBridge (backed by a mock gRPC client)
// over a pipe-based stdio transport, and drives it using the real MCP Client.
func TestIntegration_FullMCPFlow(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// -- Mock gRPC backend --
	mock := &mockLoomClient{
		getHealthFunc: func(_ context.Context, _ *loomv1.GetHealthRequest, _ ...grpc.CallOption) (*loomv1.HealthStatus, error) {
			return &loomv1.HealthStatus{Status: "healthy"}, nil
		},
		weaveFunc: func(_ context.Context, in *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
			return &loomv1.WeaveResponse{
				Text: "woven: " + in.GetQuery(),
			}, nil
		},
		listSessionsFunc: func(_ context.Context, _ *loomv1.ListSessionsRequest, _ ...grpc.CallOption) (*loomv1.ListSessionsResponse, error) {
			return &loomv1.ListSessionsResponse{
				Sessions: []*loomv1.Session{
					{Id: "sess-1", Name: "test-agent"},
				},
			}, nil
		},
	}

	// -- Bridge (tool + resource provider) --
	uiRegistry := apps.NewUIResourceRegistry()
	require.NoError(t, apps.RegisterEmbeddedApps(uiRegistry))

	bridge := NewLoomBridgeFromClient(mock, uiRegistry, logger)

	// -- MCP Server --
	mcpServer := NewMCPServer("integration-test", "0.0.1", logger,
		WithToolProvider(bridge),
		WithResourceProvider(bridge),
		WithExtensions(protocol.ServerAppsExtension()),
	)

	// -- Pipe-based transport (bidirectional) --
	// Client writes to serverIn, server reads from serverIn.
	// Server writes to clientIn, client reads from clientIn.
	serverInR, serverInW := io.Pipe()
	clientInR, clientInW := io.Pipe()

	serverTransport := transport.NewStdioServerTransport(serverInR, clientInW)
	clientTransport := transport.NewStdioServerTransport(clientInR, serverInW)

	// -- Start the server in background --
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- mcpServer.Serve(serverCtx, serverTransport)
	}()

	// -- Create MCP Client --
	mcpClient := client.NewClient(client.Config{
		Transport:      clientTransport,
		Logger:         logger,
		RequestTimeout: 5 * time.Second,
	})
	defer mcpClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ========================================================
	// Step 1: Initialize
	// ========================================================
	err := mcpClient.Initialize(ctx, protocol.Implementation{
		Name:    "test-client",
		Version: "0.0.1",
	})
	require.NoError(t, err, "initialize should succeed")
	assert.True(t, mcpClient.IsInitialized())

	serverInfo := mcpClient.ServerInfo()
	assert.Equal(t, "integration-test", serverInfo.Name)
	assert.Equal(t, "0.0.1", serverInfo.Version)

	caps := mcpClient.ServerCapabilities()
	assert.NotNil(t, caps.Tools, "server should advertise tools capability")
	assert.NotNil(t, caps.Resources, "server should advertise resources capability")

	// ========================================================
	// Step 2: List tools
	// ========================================================
	tools, err := mcpClient.ListTools(ctx)
	require.NoError(t, err, "list tools should succeed")
	require.NotEmpty(t, tools, "should have tools")

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}
	assert.True(t, toolNames["loom_weave"], "loom_weave should be listed")
	assert.True(t, toolNames["loom_get_health"], "loom_get_health should be listed")
	assert.True(t, toolNames["loom_list_sessions"], "loom_list_sessions should be listed")

	// ========================================================
	// Step 3: Call tool — loom_get_health
	// ========================================================
	healthResult, err := mcpClient.CallTool(ctx, "loom_get_health", map[string]interface{}{})
	require.NoError(t, err, "call loom_get_health should succeed")
	require.NotNil(t, healthResult)

	// ========================================================
	// Step 4: Call tool — loom_weave with args
	// ========================================================
	weaveResult, err := mcpClient.CallTool(ctx, "loom_weave", map[string]interface{}{
		"query": "explain Teradata",
	})
	require.NoError(t, err, "call loom_weave should succeed")
	require.NotNil(t, weaveResult)

	// ========================================================
	// Step 5: List resources
	// ========================================================
	resources, err := mcpClient.ListResources(ctx)
	require.NoError(t, err, "list resources should succeed")
	require.NotEmpty(t, resources, "should have at least one UI resource")

	var foundViewer bool
	for _, r := range resources {
		if r.URI == "ui://loom/conversation-viewer" {
			foundViewer = true
			assert.Equal(t, protocol.ResourceMIME, r.MimeType)
		}
	}
	assert.True(t, foundViewer, "conversation-viewer resource should be listed")

	// ========================================================
	// Step 6: Read UI resource
	// ========================================================
	readResult, err := mcpClient.ReadResource(ctx, "ui://loom/conversation-viewer")
	require.NoError(t, err, "read resource should succeed")
	require.NotNil(t, readResult)
	require.NotEmpty(t, readResult.Contents)
	assert.Contains(t, readResult.Contents[0].Text, "<!DOCTYPE html>",
		"conversation viewer should return an HTML document")

	// ========================================================
	// Cleanup: stop the server
	// ========================================================
	serverCancel()

	// Close the pipes so the server transport gets EOF and exits
	serverInW.Close()
	clientInW.Close()

	select {
	case err := <-serverDone:
		// context.Canceled is expected
		if err != nil {
			assert.ErrorIs(t, err, context.Canceled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}
