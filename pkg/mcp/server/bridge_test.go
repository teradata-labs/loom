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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/apps"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// mockLoomClient implements loomv1.LoomServiceClient for testing.
type mockLoomClient struct {
	loomv1.LoomServiceClient // embed to satisfy interface; only override what we need

	getHealthFunc               func(ctx context.Context, in *loomv1.GetHealthRequest, opts ...grpc.CallOption) (*loomv1.HealthStatus, error)
	listSessionsFunc            func(ctx context.Context, in *loomv1.ListSessionsRequest, opts ...grpc.CallOption) (*loomv1.ListSessionsResponse, error)
	listAgentsFunc              func(ctx context.Context, in *loomv1.ListAgentsRequest, opts ...grpc.CallOption) (*loomv1.ListAgentsResponse, error)
	weaveFunc                   func(ctx context.Context, in *loomv1.WeaveRequest, opts ...grpc.CallOption) (*loomv1.WeaveResponse, error)
	getConversationHistoryFunc  func(ctx context.Context, in *loomv1.GetConversationHistoryRequest, opts ...grpc.CallOption) (*loomv1.ConversationHistory, error)
	listToolsFunc               func(ctx context.Context, in *loomv1.ListToolsRequest, opts ...grpc.CallOption) (*loomv1.ListToolsResponse, error)
	listModelsFunc              func(ctx context.Context, in *loomv1.ListAvailableModelsRequest, opts ...grpc.CallOption) (*loomv1.ListAvailableModelsResponse, error)
	deleteScheduledWorkflowFunc func(ctx context.Context, in *loomv1.DeleteScheduledWorkflowRequest, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

func (m *mockLoomClient) GetHealth(ctx context.Context, in *loomv1.GetHealthRequest, opts ...grpc.CallOption) (*loomv1.HealthStatus, error) {
	if m.getHealthFunc != nil {
		return m.getHealthFunc(ctx, in, opts...)
	}
	return &loomv1.HealthStatus{Status: "healthy"}, nil
}

func (m *mockLoomClient) ListSessions(ctx context.Context, in *loomv1.ListSessionsRequest, opts ...grpc.CallOption) (*loomv1.ListSessionsResponse, error) {
	if m.listSessionsFunc != nil {
		return m.listSessionsFunc(ctx, in, opts...)
	}
	return &loomv1.ListSessionsResponse{}, nil
}

func (m *mockLoomClient) ListAgents(ctx context.Context, in *loomv1.ListAgentsRequest, opts ...grpc.CallOption) (*loomv1.ListAgentsResponse, error) {
	if m.listAgentsFunc != nil {
		return m.listAgentsFunc(ctx, in, opts...)
	}
	return &loomv1.ListAgentsResponse{}, nil
}

func (m *mockLoomClient) Weave(ctx context.Context, in *loomv1.WeaveRequest, opts ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
	if m.weaveFunc != nil {
		return m.weaveFunc(ctx, in, opts...)
	}
	return &loomv1.WeaveResponse{Text: "mock response"}, nil
}

func (m *mockLoomClient) GetConversationHistory(ctx context.Context, in *loomv1.GetConversationHistoryRequest, opts ...grpc.CallOption) (*loomv1.ConversationHistory, error) {
	if m.getConversationHistoryFunc != nil {
		return m.getConversationHistoryFunc(ctx, in, opts...)
	}
	return &loomv1.ConversationHistory{}, nil
}

func (m *mockLoomClient) ListTools(ctx context.Context, in *loomv1.ListToolsRequest, opts ...grpc.CallOption) (*loomv1.ListToolsResponse, error) {
	if m.listToolsFunc != nil {
		return m.listToolsFunc(ctx, in, opts...)
	}
	return &loomv1.ListToolsResponse{}, nil
}

func (m *mockLoomClient) ListAvailableModels(ctx context.Context, in *loomv1.ListAvailableModelsRequest, opts ...grpc.CallOption) (*loomv1.ListAvailableModelsResponse, error) {
	if m.listModelsFunc != nil {
		return m.listModelsFunc(ctx, in, opts...)
	}
	return &loomv1.ListAvailableModelsResponse{}, nil
}

func (m *mockLoomClient) DeleteScheduledWorkflow(ctx context.Context, in *loomv1.DeleteScheduledWorkflowRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if m.deleteScheduledWorkflowFunc != nil {
		return m.deleteScheduledWorkflowFunc(ctx, in, opts...)
	}
	return &emptypb.Empty{}, nil
}

func TestLoomBridge_ListTools(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}
	registry := apps.NewUIResourceRegistry()
	require.NoError(t, apps.RegisterEmbeddedApps(registry))

	bridge := NewLoomBridgeFromClient(mockClient, registry, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	// Verify we have a good number of tools
	assert.GreaterOrEqual(t, len(tools), 40, "should have at least 40 tools")

	// Verify key tools exist
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{
		"loom_weave",
		"loom_list_sessions",
		"loom_get_health",
		"loom_list_agents",
		"loom_list_patterns",
		"loom_list_models",
		"loom_execute_workflow",
		"loom_list_artifacts",
		"loom_get_conversation_history",
	}

	for _, expected := range expectedTools {
		assert.True(t, toolNames[expected], "missing expected tool: %s", expected)
	}
}

func TestLoomBridge_ToolsHaveUIMetadata(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}
	registry := apps.NewUIResourceRegistry()
	require.NoError(t, apps.RegisterEmbeddedApps(registry))

	bridge := NewLoomBridgeFromClient(mockClient, registry, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	// Find loom_weave and check its UI metadata
	for _, tool := range tools {
		if tool.Name == "loom_weave" {
			meta := protocol.GetUIToolMeta(tool)
			require.NotNil(t, meta, "loom_weave should have UI metadata")
			assert.Equal(t, "ui://loom/conversation-viewer", meta.ResourceURI)
			assert.Contains(t, meta.Visibility, "model")
			assert.Contains(t, meta.Visibility, "app")
			return
		}
	}
	t.Fatal("loom_weave tool not found")
}

func TestLoomBridge_ConversationHistoryIsAppOnly(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	for _, tool := range tools {
		if tool.Name == "loom_get_conversation_history" {
			meta := protocol.GetUIToolMeta(tool)
			require.NotNil(t, meta, "loom_get_conversation_history should have UI metadata")
			assert.Equal(t, []string{"app"}, meta.Visibility, "should be app-only visibility")
			return
		}
	}
	t.Fatal("loom_get_conversation_history tool not found")
}

func TestLoomBridge_CallTool_GetHealth(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		getHealthFunc: func(_ context.Context, _ *loomv1.GetHealthRequest, _ ...grpc.CallOption) (*loomv1.HealthStatus, error) {
			return &loomv1.HealthStatus{Status: "healthy"}, nil
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_get_health", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "healthy")
}

func TestLoomBridge_CallTool_Weave(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		weaveFunc: func(_ context.Context, in *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
			return &loomv1.WeaveResponse{
				Text: "Analyzed the data: " + in.GetQuery(),
			}, nil
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_weave", map[string]interface{}{
		"query": "analyze this data",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Content[0].Text, "Analyzed the data")
}

func TestLoomBridge_CallTool_ListSessions(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		listSessionsFunc: func(_ context.Context, _ *loomv1.ListSessionsRequest, _ ...grpc.CallOption) (*loomv1.ListSessionsResponse, error) {
			return &loomv1.ListSessionsResponse{
				Sessions: []*loomv1.Session{
					{Id: "sess-1", Name: "agent-1"},
					{Id: "sess-2", Name: "agent-2"},
				},
			}, nil
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_list_sessions", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Content[0].Text, "sess-1")
	assert.Contains(t, result.Content[0].Text, "sess-2")
}

func TestLoomBridge_CallTool_UnknownTool(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	_, err := bridge.CallTool(context.Background(), "nonexistent_tool", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

func TestLoomBridge_ListResources(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}
	registry := apps.NewUIResourceRegistry()
	require.NoError(t, apps.RegisterEmbeddedApps(registry))

	bridge := NewLoomBridgeFromClient(mockClient, registry, logger)

	resources, err := bridge.ListResources(context.Background())
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "ui://loom/conversation-viewer", resources[0].URI)
	assert.Equal(t, protocol.ResourceMIME, resources[0].MimeType)
}

func TestLoomBridge_ListResources_NilRegistry(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	resources, err := bridge.ListResources(context.Background())
	require.NoError(t, err)
	assert.Empty(t, resources)
}

func TestLoomBridge_ReadResource(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}
	registry := apps.NewUIResourceRegistry()
	require.NoError(t, apps.RegisterEmbeddedApps(registry))

	bridge := NewLoomBridgeFromClient(mockClient, registry, logger)

	result, err := bridge.ReadResource(context.Background(), "ui://loom/conversation-viewer")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Contents, 1)
	assert.Contains(t, result.Contents[0].Text, "<!DOCTYPE html>")
}

func TestLoomBridge_ReadResource_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}
	registry := apps.NewUIResourceRegistry()

	bridge := NewLoomBridgeFromClient(mockClient, registry, logger)

	_, err := bridge.ReadResource(context.Background(), "ui://loom/nonexistent")
	assert.Error(t, err)
}

func TestLoomBridge_ToolInputSchemas(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	// All tools should have valid input schemas
	for _, tool := range tools {
		require.NotNil(t, tool.InputSchema, "tool %s should have an input schema", tool.Name)
		assert.Equal(t, "object", tool.InputSchema["type"], "tool %s schema should be type=object", tool.Name)
	}
}

func TestLoomBridge_CallTool_ListModels(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		listModelsFunc: func(_ context.Context, _ *loomv1.ListAvailableModelsRequest, _ ...grpc.CallOption) (*loomv1.ListAvailableModelsResponse, error) {
			return &loomv1.ListAvailableModelsResponse{}, nil
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_list_models", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

func TestLoomBridge_CallTool_ListAgents(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_list_agents", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestLoomBridge_CallTool_ListTools(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	result, err := bridge.CallTool(context.Background(), "loom_list_tools", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestLoomBridge_RequestTimeout(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		getHealthFunc: func(ctx context.Context, _ *loomv1.GetHealthRequest, _ ...grpc.CallOption) (*loomv1.HealthStatus, error) {
			// Simulate a slow/hung server by waiting until the context deadline fires
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger,
		WithRequestTimeout(50*time.Millisecond),
	)

	start := time.Now()
	_, err := bridge.CallTool(context.Background(), "loom_get_health", nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	// Should return quickly (within ~200ms), not hang forever
	assert.Less(t, elapsed, 500*time.Millisecond,
		"request should time out quickly, not block indefinitely")
}

func TestLoomBridge_DefaultTimeout(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)
	assert.Equal(t, DefaultRequestTimeout, bridge.requestTimeout)
}

func TestLoomBridge_AllToolHandlersRegistered(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	handlers := bridge.handlers

	// Every listed tool should have a handler
	for _, tool := range tools {
		_, ok := handlers[tool.Name]
		assert.True(t, ok, "tool %s has no handler registered", tool.Name)
	}

	// Every handler should correspond to a listed tool
	toolMap := make(map[string]bool)
	for _, tool := range tools {
		toolMap[tool.Name] = true
	}
	for name := range handlers {
		assert.True(t, toolMap[name], "handler %s has no corresponding tool definition", name)
	}
}

func TestLoomBridge_CallTool_GRPCError_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		getHealthFunc: func(_ context.Context, _ *loomv1.GetHealthRequest, _ ...grpc.CallOption) (*loomv1.HealthStatus, error) {
			return nil, status.Error(codes.NotFound, "health endpoint not found")
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	_, err := bridge.CallTool(context.Background(), "loom_get_health", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLoomBridge_CallTool_GRPCError_Internal(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		listSessionsFunc: func(_ context.Context, _ *loomv1.ListSessionsRequest, _ ...grpc.CallOption) (*loomv1.ListSessionsResponse, error) {
			return nil, status.Error(codes.Internal, "database connection failed")
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	_, err := bridge.CallTool(context.Background(), "loom_list_sessions", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database connection failed")
}

func TestLoomBridge_CallTool_GRPCError_Unavailable(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		weaveFunc: func(_ context.Context, _ *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
			return nil, status.Error(codes.Unavailable, "server shutting down")
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	_, err := bridge.CallTool(context.Background(), "loom_weave", map[string]interface{}{"query": "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server shutting down")
}

func TestLoomBridge_CallTool_GRPCError_InvalidArgument(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{
		weaveFunc: func(_ context.Context, _ *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "query is required")
		},
	}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	_, err := bridge.CallTool(context.Background(), "loom_weave", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

func TestLoomBridge_CallTool_InvalidArgs(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	// Pass args that can't be marshaled to proto - use a channel which json.Marshal will reject
	_, err := bridge.CallTool(context.Background(), "loom_weave", map[string]interface{}{
		"query": make(chan int), // channels can't be JSON marshaled
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal args")
}

func TestLoomBridge_ToolAnnotations(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockClient := &mockLoomClient{}

	bridge := NewLoomBridgeFromClient(mockClient, nil, logger)

	tools, err := bridge.ListTools(context.Background())
	require.NoError(t, err)

	// Build a map for quick lookup by name.
	toolMap := make(map[string]protocol.Tool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}

	// Every tool must have annotations set.
	for _, tool := range tools {
		assert.NotNilf(t, tool.Annotations, "tool %s should have annotations", tool.Name)
	}

	// Read-only tools: readOnlyHint=true, destructiveHint=false, idempotentHint=true
	readOnlyTools := []string{
		"loom_list_patterns", "loom_get_pattern",
		"loom_list_sessions", "loom_get_session", "loom_get_conversation_history",
		"loom_list_tools", "loom_get_health", "loom_get_trace",
		"loom_list_agents", "loom_get_agent",
		"loom_list_models",
		"loom_get_workflow_execution", "loom_list_workflow_executions",
		"loom_get_scheduled_workflow", "loom_list_scheduled_workflows", "loom_get_schedule_history",
		"loom_list_artifacts", "loom_get_artifact", "loom_get_artifact_content",
		"loom_get_artifact_stats", "loom_search_artifacts",
	}
	for _, name := range readOnlyTools {
		tool, ok := toolMap[name]
		require.Truef(t, ok, "read-only tool %s not found", name)
		require.NotNilf(t, tool.Annotations, "tool %s should have annotations", name)
		require.NotNilf(t, tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint", name)
		assert.Truef(t, *tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint=true", name)
		require.NotNilf(t, tool.Annotations.DestructiveHint, "tool %s should have destructiveHint", name)
		assert.Falsef(t, *tool.Annotations.DestructiveHint, "tool %s should have destructiveHint=false", name)
		require.NotNilf(t, tool.Annotations.IdempotentHint, "tool %s should have idempotentHint", name)
		assert.Truef(t, *tool.Annotations.IdempotentHint, "tool %s should have idempotentHint=true", name)
	}

	// Destructive tools: destructiveHint=true, readOnlyHint=false
	destructiveTools := []string{
		"loom_delete_session",
		"loom_delete_agent",
		"loom_delete_scheduled_workflow",
		"loom_delete_artifact",
	}
	for _, name := range destructiveTools {
		tool, ok := toolMap[name]
		require.Truef(t, ok, "destructive tool %s not found", name)
		require.NotNilf(t, tool.Annotations, "tool %s should have annotations", name)
		require.NotNilf(t, tool.Annotations.DestructiveHint, "tool %s should have destructiveHint", name)
		assert.Truef(t, *tool.Annotations.DestructiveHint, "tool %s should have destructiveHint=true", name)
		require.NotNilf(t, tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint", name)
		assert.Falsef(t, *tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint=false", name)
	}

	// loom_weave should have openWorldHint=true
	weaveTool, ok := toolMap["loom_weave"]
	require.True(t, ok, "loom_weave not found")
	require.NotNil(t, weaveTool.Annotations)
	require.NotNil(t, weaveTool.Annotations.OpenWorldHint, "loom_weave should have openWorldHint")
	assert.True(t, *weaveTool.Annotations.OpenWorldHint, "loom_weave should have openWorldHint=true")
	require.NotNil(t, weaveTool.Annotations.ReadOnlyHint, "loom_weave should have readOnlyHint")
	assert.False(t, *weaveTool.Annotations.ReadOnlyHint, "loom_weave should have readOnlyHint=false")
	require.NotNil(t, weaveTool.Annotations.DestructiveHint, "loom_weave should have destructiveHint")
	assert.False(t, *weaveTool.Annotations.DestructiveHint, "loom_weave should have destructiveHint=false")

	// Create/mutate tools: readOnlyHint=false, destructiveHint=false
	mutatingTools := []string{
		"loom_load_patterns", "loom_create_pattern",
		"loom_create_session", "loom_answer_clarification",
		"loom_register_tool", "loom_request_tool_permission",
		"loom_create_agent", "loom_start_agent", "loom_stop_agent", "loom_reload_agent",
		"loom_switch_model",
		"loom_execute_workflow",
		"loom_schedule_workflow", "loom_update_scheduled_workflow",
		"loom_trigger_scheduled_workflow", "loom_pause_schedule", "loom_resume_schedule",
		"loom_upload_artifact",
	}
	for _, name := range mutatingTools {
		tool, ok := toolMap[name]
		require.Truef(t, ok, "mutating tool %s not found", name)
		require.NotNilf(t, tool.Annotations, "tool %s should have annotations", name)
		require.NotNilf(t, tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint", name)
		assert.Falsef(t, *tool.Annotations.ReadOnlyHint, "tool %s should have readOnlyHint=false", name)
		require.NotNilf(t, tool.Annotations.DestructiveHint, "tool %s should have destructiveHint", name)
		assert.Falsef(t, *tool.Annotations.DestructiveHint, "tool %s should have destructiveHint=false", name)
	}
}

// ============================================================================
// TLS option tests
// ============================================================================

func TestWithTLS_SetsFields(t *testing.T) {
	tests := []struct {
		name         string
		certFile     string
		skipVerify   bool
		wantEnabled  bool
		wantCertFile string
		wantSkipVfy  bool
	}{
		{
			name:         "TLS with custom CA cert",
			certFile:     "/path/to/ca.pem",
			skipVerify:   false,
			wantEnabled:  true,
			wantCertFile: "/path/to/ca.pem",
			wantSkipVfy:  false,
		},
		{
			name:         "TLS with system cert pool",
			certFile:     "",
			skipVerify:   false,
			wantEnabled:  true,
			wantCertFile: "",
			wantSkipVfy:  false,
		},
		{
			name:         "TLS with skip verify",
			certFile:     "",
			skipVerify:   true,
			wantEnabled:  true,
			wantCertFile: "",
			wantSkipVfy:  true,
		},
		{
			name:         "TLS with cert and skip verify",
			certFile:     "/path/to/ca.pem",
			skipVerify:   true,
			wantEnabled:  true,
			wantCertFile: "/path/to/ca.pem",
			wantSkipVfy:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &LoomBridge{}
			opt := WithTLS(tc.certFile, tc.skipVerify)
			opt(bridge)

			assert.Equal(t, tc.wantEnabled, bridge.tlsEnabled)
			assert.Equal(t, tc.wantCertFile, bridge.tlsCertFile)
			assert.Equal(t, tc.wantSkipVfy, bridge.tlsSkipVerify)
		})
	}
}

func TestLoomBridge_DefaultIsInsecure(t *testing.T) {
	bridge := &LoomBridge{}
	assert.False(t, bridge.tlsEnabled, "TLS should be disabled by default")
	assert.Empty(t, bridge.tlsCertFile)
	assert.False(t, bridge.tlsSkipVerify)
}

func TestBuildTransportCredentials_InsecureByDefault(t *testing.T) {
	bridge := &LoomBridge{}
	creds, err := bridge.buildTransportCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "insecure", creds.Info().SecurityProtocol)
}

func TestBuildTransportCredentials_TLSSystemPool(t *testing.T) {
	bridge := &LoomBridge{tlsEnabled: true}
	creds, err := bridge.buildTransportCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "tls", creds.Info().SecurityProtocol)
}

func TestBuildTransportCredentials_TLSSkipVerify(t *testing.T) {
	bridge := &LoomBridge{tlsEnabled: true, tlsSkipVerify: true}
	creds, err := bridge.buildTransportCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "tls", creds.Info().SecurityProtocol)
}

func TestBuildTransportCredentials_TLSWithValidCert(t *testing.T) {
	// Write a self-signed CA cert to a temp file for testing
	certPEM := generateSelfSignedCACert(t)
	tmpFile := t.TempDir() + "/ca.pem"
	require.NoError(t, os.WriteFile(tmpFile, certPEM, 0644))

	bridge := &LoomBridge{tlsEnabled: true, tlsCertFile: tmpFile}
	creds, err := bridge.buildTransportCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "tls", creds.Info().SecurityProtocol)
}

func TestBuildTransportCredentials_TLSCertFileNotFound(t *testing.T) {
	bridge := &LoomBridge{tlsEnabled: true, tlsCertFile: "/nonexistent/path/ca.pem"}
	_, err := bridge.buildTransportCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read TLS CA cert")
}

func TestBuildTransportCredentials_TLSInvalidCertContent(t *testing.T) {
	tmpFile := t.TempDir() + "/bad-ca.pem"
	require.NoError(t, os.WriteFile(tmpFile, []byte("not a valid PEM certificate"), 0644))

	bridge := &LoomBridge{tlsEnabled: true, tlsCertFile: tmpFile}
	_, err := bridge.buildTransportCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse CA certificate")
}

func TestNewLoomBridge_WithTLS_SystemPool(t *testing.T) {
	// Verify that NewLoomBridge accepts the WithTLS option and creates a bridge
	// with TLS enabled. We use a dummy address since we do not need a real connection.
	logger := zaptest.NewLogger(t)
	registry := apps.NewUIResourceRegistry()

	bridge, err := NewLoomBridge("localhost:60051", registry, logger,
		WithTLS("", false),
	)
	require.NoError(t, err)
	require.NotNil(t, bridge)
	defer bridge.Close()

	assert.True(t, bridge.tlsEnabled)
	assert.Empty(t, bridge.tlsCertFile)
	assert.False(t, bridge.tlsSkipVerify)
}

func TestNewLoomBridge_WithTLS_InvalidCert(t *testing.T) {
	logger := zaptest.NewLogger(t)
	registry := apps.NewUIResourceRegistry()

	_, err := NewLoomBridge("localhost:60051", registry, logger,
		WithTLS("/nonexistent/ca.pem", false),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configure transport credentials")
}

// generateSelfSignedCACert creates a minimal self-signed CA certificate in PEM
// format for testing purposes only.
func generateSelfSignedCACert(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Loom Test CA"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	return buf.Bytes()
}
