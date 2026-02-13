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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/observability"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Helper functions ---

// newTestRegistry creates a temporary tool registry backed by a temp SQLite file.
// The caller should defer os.Remove(path) and registry.Close().
func newTestRegistry(t *testing.T) (*toolregistry.Registry, string) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	reg, err := toolregistry.New(toolregistry.Config{
		DBPath: tmpFile.Name(),
	})
	require.NoError(t, err)
	return reg, tmpFile.Name()
}

// newTestServerWithRegistry creates a MultiAgentServer with a tool registry configured.
func newTestServerWithRegistry(t *testing.T) (*MultiAgentServer, func()) {
	t.Helper()
	reg, dbPath := newTestRegistry(t)
	server := &MultiAgentServer{
		logger:             zap.NewNop(),
		pendingQuestions:   make(map[string]*metaagent.Question),
		pendingPermissions: make(map[string]*pendingPermission),
	}
	server.toolRegistry = reg

	cleanup := func() {
		if err := reg.Close(); err != nil {
			t.Logf("warning: failed to close registry: %v", err)
		}
		os.Remove(dbPath)
	}
	return server, cleanup
}

// newTestServerWithTracer creates a MultiAgentServer with a mock tracer and trace store.
func newTestServerWithTracer(t *testing.T) *MultiAgentServer {
	t.Helper()
	tracer := observability.NewMockTracer()
	server := &MultiAgentServer{
		logger:          zap.NewNop(),
		tracer:          tracer,
		traceStoreLocal: newTraceStore(1 * time.Hour),
	}
	return server
}

// newTestServerForPermissions creates a MultiAgentServer for tool permission testing.
func newTestServerForPermissions(t *testing.T) *MultiAgentServer {
	t.Helper()
	return &MultiAgentServer{
		logger:             zap.NewNop(),
		pendingPermissions: make(map[string]*pendingPermission),
	}
}

// --- RegisterTool Tests ---

func TestRegisterTool(t *testing.T) {
	tests := []struct {
		name          string
		req           *loomv1.RegisterToolRequest
		setupRegistry bool
		wantSuccess   bool
		wantCode      codes.Code
		wantErrSubstr string
		wantMsgSubstr string
	}{
		{
			name: "valid tool registration",
			req: &loomv1.RegisterToolRequest{
				Tool: &loomv1.ToolDefinition{
					Name:            "my_custom_tool",
					Description:     "A custom tool for testing",
					InputSchemaJson: `{"type": "object"}`,
					Capabilities:    []string{"test", "custom"},
					Keywords:        []string{"testing"},
				},
			},
			setupRegistry: true,
			wantSuccess:   true,
			wantMsgSubstr: "registered successfully",
		},
		{
			name:          "nil tool definition",
			req:           &loomv1.RegisterToolRequest{},
			setupRegistry: true,
			wantSuccess:   false,
			wantCode:      codes.InvalidArgument,
			wantErrSubstr: "tool definition is required",
		},
		{
			name: "empty tool name",
			req: &loomv1.RegisterToolRequest{
				Tool: &loomv1.ToolDefinition{
					Name:        "",
					Description: "Missing name",
				},
			},
			setupRegistry: true,
			wantSuccess:   false,
			wantCode:      codes.InvalidArgument,
			wantErrSubstr: "tool name is required",
		},
		{
			name: "registry not configured",
			req: &loomv1.RegisterToolRequest{
				Tool: &loomv1.ToolDefinition{
					Name:        "orphan_tool",
					Description: "No registry available",
				},
			},
			setupRegistry: false,
			wantSuccess:   false,
			wantCode:      codes.FailedPrecondition,
			wantErrSubstr: "tool registry not configured",
		},
		{
			name: "tool with all fields",
			req: &loomv1.RegisterToolRequest{
				Tool: &loomv1.ToolDefinition{
					Name:             "full_tool",
					Description:      "A fully specified tool",
					InputSchemaJson:  `{"type": "object", "properties": {"query": {"type": "string"}}}`,
					OutputSchemaJson: `{"type": "object", "properties": {"result": {"type": "string"}}}`,
					Capabilities:     []string{"search", "query"},
					Keywords:         []string{"database", "search"},
					Category:         "data",
					BestPractices:    "Use specific queries",
				},
			},
			setupRegistry: true,
			wantSuccess:   true,
			wantMsgSubstr: "registered successfully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *MultiAgentServer
			var cleanup func()

			if tt.setupRegistry {
				server, cleanup = newTestServerWithRegistry(t)
				defer cleanup()
			} else {
				server = &MultiAgentServer{
					logger: zap.NewNop(),
				}
			}

			resp, err := server.RegisterTool(context.Background(), tt.req)

			if tt.wantCode != codes.OK {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				if tt.wantErrSubstr != "" {
					assert.Contains(t, st.Message(), tt.wantErrSubstr)
				}
			}

			require.NotNil(t, resp)
			assert.Equal(t, tt.wantSuccess, resp.Success)
			if tt.wantMsgSubstr != "" {
				assert.Contains(t, resp.Message, tt.wantMsgSubstr)
			}
		})
	}
}

func TestRegisterTool_DuplicateUpdates(t *testing.T) {
	server, cleanup := newTestServerWithRegistry(t)
	defer cleanup()

	ctx := context.Background()

	// Register a tool
	req := &loomv1.RegisterToolRequest{
		Tool: &loomv1.ToolDefinition{
			Name:        "updatable_tool",
			Description: "Version 1",
		},
	}

	resp, err := server.RegisterTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Register the same tool with updated description (should upsert)
	req.Tool.Description = "Version 2 - updated"
	resp, err = server.RegisterTool(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Verify the tool was updated by retrieving it
	tool, err := server.toolRegistry.GetTool(ctx, "custom:updatable_tool")
	require.NoError(t, err)
	assert.Equal(t, "Version 2 - updated", tool.Description)
}

func TestRegisterTool_ConcurrentRegistrations(t *testing.T) {
	server, cleanup := newTestServerWithRegistry(t)
	defer cleanup()

	ctx := context.Background()
	numTools := 10
	var wg sync.WaitGroup

	for i := 0; i < numTools; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := &loomv1.RegisterToolRequest{
				Tool: &loomv1.ToolDefinition{
					Name:        "concurrent_tool_" + string(rune('A'+idx)),
					Description: "Concurrently registered tool",
				},
			}
			resp, err := server.RegisterTool(ctx, req)
			assert.NoError(t, err)
			assert.True(t, resp.Success)
		}(i)
	}

	wg.Wait()
}

// --- GetTrace Tests ---

func TestGetTrace(t *testing.T) {
	tests := []struct {
		name          string
		traceID       string
		setupTracer   bool
		setupStore    bool
		addSpans      bool
		wantCode      codes.Code
		wantErrSubstr string
		wantSpanCount int
	}{
		{
			name:          "valid trace retrieval",
			traceID:       "trace-123",
			setupTracer:   true,
			setupStore:    true,
			addSpans:      true,
			wantCode:      codes.OK,
			wantSpanCount: 2,
		},
		{
			name:          "empty trace_id",
			traceID:       "",
			setupTracer:   true,
			setupStore:    true,
			wantCode:      codes.InvalidArgument,
			wantErrSubstr: "trace_id is required",
		},
		{
			name:          "tracer not configured",
			traceID:       "trace-123",
			setupTracer:   false,
			setupStore:    false,
			wantCode:      codes.FailedPrecondition,
			wantErrSubstr: "tracer not configured",
		},
		{
			name:          "trace store not initialized",
			traceID:       "trace-123",
			setupTracer:   true,
			setupStore:    false,
			wantCode:      codes.FailedPrecondition,
			wantErrSubstr: "trace store not initialized",
		},
		{
			name:          "trace not found",
			traceID:       "non-existent-trace",
			setupTracer:   true,
			setupStore:    true,
			wantCode:      codes.NotFound,
			wantErrSubstr: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &MultiAgentServer{
				logger: zap.NewNop(),
			}

			if tt.setupTracer {
				server.tracer = observability.NewMockTracer()
			}
			if tt.setupStore {
				server.traceStoreLocal = newTraceStore(1 * time.Hour)
			}

			if tt.addSpans {
				// Add test spans to the store
				rootSpan := &observability.Span{
					TraceID:   tt.traceID,
					SpanID:    "span-root",
					ParentID:  "",
					Name:      "agent.conversation",
					StartTime: time.Now().Add(-100 * time.Millisecond),
					EndTime:   time.Now(),
					Duration:  100 * time.Millisecond,
					Status:    observability.Status{Code: observability.StatusOK},
					Attributes: map[string]interface{}{
						observability.AttrSessionID: "session-abc",
						observability.AttrLLMModel:  "claude-3",
					},
				}
				childSpan := &observability.Span{
					TraceID:   tt.traceID,
					SpanID:    "span-child",
					ParentID:  "span-root",
					Name:      "llm.completion",
					StartTime: time.Now().Add(-50 * time.Millisecond),
					EndTime:   time.Now(),
					Duration:  50 * time.Millisecond,
					Status:    observability.Status{Code: observability.StatusOK},
					Attributes: map[string]interface{}{
						"llm.tokens.total": 42,
					},
					Events: []observability.Event{
						{
							Timestamp:  time.Now(),
							Name:       "token.received",
							Attributes: map[string]interface{}{"count": 10},
						},
					},
				}
				server.traceStoreLocal.AddSpan(rootSpan)
				server.traceStoreLocal.AddSpan(childSpan)
			}

			req := &loomv1.GetTraceRequest{
				TraceId: tt.traceID,
			}

			resp, err := server.GetTrace(context.Background(), req)

			if tt.wantCode != codes.OK {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok, "expected gRPC status error")
				assert.Equal(t, tt.wantCode, st.Code())
				if tt.wantErrSubstr != "" {
					assert.Contains(t, st.Message(), tt.wantErrSubstr)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.traceID, resp.Id)
			assert.Equal(t, "session-abc", resp.SessionId)
			assert.Len(t, resp.Spans, tt.wantSpanCount)

			// Verify root span is set
			require.NotNil(t, resp.RootSpan)
			assert.Equal(t, "span-root", resp.RootSpan.Id)
			assert.Equal(t, "agent.conversation", resp.RootSpan.Name)
			assert.Empty(t, resp.RootSpan.ParentId)

			// Verify child span
			var childProto *loomv1.Span
			for _, s := range resp.Spans {
				if s.Id == "span-child" {
					childProto = s
					break
				}
			}
			require.NotNil(t, childProto, "child span should be in flat list")
			assert.Equal(t, "span-root", childProto.ParentId)
			assert.Equal(t, "llm.completion", childProto.Name)
			assert.Equal(t, "ok", childProto.Status)

			// Verify events were converted
			require.Len(t, childProto.Events, 1)
			assert.Equal(t, "token.received", childProto.Events[0].Name)

			// Verify total duration is calculated
			assert.True(t, resp.TotalDurationMs > 0)
		})
	}
}

func TestGetTrace_SpanConversion(t *testing.T) {
	server := newTestServerWithTracer(t)
	ctx := context.Background()

	// Create a span with error status
	errorSpan := &observability.Span{
		TraceID:   "trace-err",
		SpanID:    "span-err",
		Name:      "tool.execute",
		StartTime: time.Now().Add(-200 * time.Millisecond),
		EndTime:   time.Now(),
		Duration:  200 * time.Millisecond,
		Status:    observability.Status{Code: observability.StatusError, Message: "connection refused"},
		Attributes: map[string]interface{}{
			"error.message": "connection refused",
			"tool.name":     "web_search",
			"numeric":       42,
		},
	}
	server.traceStoreLocal.AddSpan(errorSpan)

	resp, err := server.GetTrace(ctx, &loomv1.GetTraceRequest{TraceId: "trace-err"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Spans, 1)

	span := resp.Spans[0]
	assert.Equal(t, "error", span.Status)
	assert.Equal(t, "connection refused", span.Attributes["error.message"])
	assert.Equal(t, "web_search", span.Attributes["tool.name"])
	assert.Equal(t, "42", span.Attributes["numeric"]) // interface{} -> fmt.Sprintf("%v", ...)
	assert.True(t, span.DurationUs > 0)
	assert.True(t, span.StartTimeUs > 0)
	assert.True(t, span.EndTimeUs > 0)
}

func TestGetTrace_ConcurrentAccess(t *testing.T) {
	server := newTestServerWithTracer(t)
	ctx := context.Background()

	// Add some spans
	for i := 0; i < 5; i++ {
		span := &observability.Span{
			TraceID:    "concurrent-trace",
			SpanID:     "span-" + string(rune('A'+i)),
			Name:       "test.span",
			StartTime:  time.Now(),
			EndTime:    time.Now(),
			Duration:   10 * time.Millisecond,
			Status:     observability.Status{Code: observability.StatusOK},
			Attributes: map[string]interface{}{},
		}
		server.traceStoreLocal.AddSpan(span)
	}

	// Read concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := server.GetTrace(ctx, &loomv1.GetTraceRequest{TraceId: "concurrent-trace"})
			assert.NoError(t, err)
			assert.NotNil(t, resp)
			assert.Len(t, resp.Spans, 5)
		}()
	}
	wg.Wait()
}

// --- TraceStore Tests ---

func TestTraceStore_AddAndGet(t *testing.T) {
	store := newTraceStore(1 * time.Hour)

	span := &observability.Span{
		TraceID:   "trace-1",
		SpanID:    "span-1",
		Name:      "test",
		StartTime: time.Now(),
		Attributes: map[string]interface{}{
			observability.AttrSessionID: "session-1",
		},
	}
	store.AddSpan(span)

	stored := store.GetTrace("trace-1")
	require.NotNil(t, stored)
	assert.Equal(t, "trace-1", stored.ID)
	assert.Equal(t, "session-1", stored.SessionID)
	assert.Len(t, stored.Spans, 1)
}

func TestTraceStore_NilSpan(t *testing.T) {
	store := newTraceStore(1 * time.Hour)
	store.AddSpan(nil) // Should not panic

	stored := store.GetTrace("any-id")
	assert.Nil(t, stored)
}

func TestTraceStore_MultipleSpansSameTrace(t *testing.T) {
	store := newTraceStore(1 * time.Hour)

	for i := 0; i < 5; i++ {
		span := &observability.Span{
			TraceID:    "shared-trace",
			SpanID:     "span-" + string(rune('0'+i)),
			Name:       "operation",
			Attributes: map[string]interface{}{},
		}
		store.AddSpan(span)
	}

	stored := store.GetTrace("shared-trace")
	require.NotNil(t, stored)
	assert.Len(t, stored.Spans, 5)
}

func TestTraceStore_EvictExpired(t *testing.T) {
	store := newTraceStore(50 * time.Millisecond)

	span := &observability.Span{
		TraceID:    "old-trace",
		SpanID:     "span-old",
		Attributes: map[string]interface{}{},
	}
	store.AddSpan(span)

	// Wait for expiry
	time.Sleep(100 * time.Millisecond)

	evicted := store.EvictExpired()
	assert.Equal(t, 1, evicted)

	stored := store.GetTrace("old-trace")
	assert.Nil(t, stored, "expired trace should be evicted")
}

func TestTraceStore_EvictExpired_KeepRecent(t *testing.T) {
	store := newTraceStore(1 * time.Hour)

	span := &observability.Span{
		TraceID:    "recent-trace",
		SpanID:     "span-recent",
		Attributes: map[string]interface{}{},
	}
	store.AddSpan(span)

	evicted := store.EvictExpired()
	assert.Equal(t, 0, evicted)

	stored := store.GetTrace("recent-trace")
	assert.NotNil(t, stored, "recent trace should not be evicted")
}

func TestTraceStore_ConcurrentReadWrite(t *testing.T) {
	store := newTraceStore(1 * time.Hour)

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			span := &observability.Span{
				TraceID:    "concurrent-store-trace",
				SpanID:     "span-" + string(rune('A'+idx)),
				Attributes: map[string]interface{}{},
			}
			store.AddSpan(span)
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.GetTrace("concurrent-store-trace")
		}()
	}

	wg.Wait()
}

// --- RequestToolPermission Tests ---

func TestRequestToolPermission(t *testing.T) {
	tests := []struct {
		name          string
		req           *loomv1.ToolPermissionRequest
		wantCode      codes.Code
		wantErrSubstr string
	}{
		{
			name: "missing session_id",
			req: &loomv1.ToolPermissionRequest{
				SessionId: "",
				ToolName:  "web_search",
			},
			wantCode:      codes.InvalidArgument,
			wantErrSubstr: "session_id is required",
		},
		{
			name: "missing tool_name",
			req: &loomv1.ToolPermissionRequest{
				SessionId: "session-1",
				ToolName:  "",
			},
			wantCode:      codes.InvalidArgument,
			wantErrSubstr: "tool_name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServerForPermissions(t)

			resp, err := server.RequestToolPermission(context.Background(), tt.req)

			require.Error(t, err)
			assert.Nil(t, resp)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, st.Code())
			assert.Contains(t, st.Message(), tt.wantErrSubstr)
		})
	}
}

func TestRequestToolPermission_Timeout(t *testing.T) {
	server := newTestServerForPermissions(t)

	req := &loomv1.ToolPermissionRequest{
		SessionId:      "session-1",
		ToolName:       "dangerous_tool",
		ArgsJson:       `{"action": "delete"}`,
		Description:    "Will delete all data",
		RiskLevel:      "high",
		TimeoutSeconds: 1, // 1 second timeout for fast testing
	}

	start := time.Now()
	resp, err := server.RequestToolPermission(context.Background(), req)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, resp.Granted)
	assert.True(t, resp.TimedOut)
	assert.Contains(t, resp.Message, "timed out")

	// Verify timeout duration is approximately correct (allow 500ms slack)
	assert.True(t, elapsed >= 900*time.Millisecond, "should wait at least ~1 second")
	assert.True(t, elapsed < 3*time.Second, "should not wait much longer than timeout")

	// Verify pending permission was cleaned up
	server.pendingPermissionsMu.RLock()
	assert.Empty(t, server.pendingPermissions)
	server.pendingPermissionsMu.RUnlock()
}

func TestRequestToolPermission_DefaultTimeout(t *testing.T) {
	server := newTestServerForPermissions(t)

	req := &loomv1.ToolPermissionRequest{
		SessionId:      "session-1",
		ToolName:       "some_tool",
		TimeoutSeconds: 0, // Should default to 300
	}

	// Use a context with short deadline to avoid waiting 300 seconds
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	resp, err := server.RequestToolPermission(ctx, req)

	// Context cancellation should result in Canceled error
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Canceled, st.Code())
	assert.Nil(t, resp)

	// Verify cleanup
	server.pendingPermissionsMu.RLock()
	assert.Empty(t, server.pendingPermissions)
	server.pendingPermissionsMu.RUnlock()
}

func TestRequestToolPermission_GrantedByUser(t *testing.T) {
	server := newTestServerForPermissions(t)

	req := &loomv1.ToolPermissionRequest{
		SessionId:      "session-1",
		ToolName:       "web_search",
		ArgsJson:       `{"query": "test"}`,
		Description:    "Search the web",
		RiskLevel:      "low",
		TimeoutSeconds: 5,
	}

	// Launch the permission request in a goroutine
	type result struct {
		resp *loomv1.ToolPermissionResponse
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := server.RequestToolPermission(context.Background(), req)
		resultCh <- result{resp, err}
	}()

	// Give the goroutine time to register the pending permission
	time.Sleep(50 * time.Millisecond)

	// Find and grant the pending permission
	permissions := server.GetPendingPermissions()
	require.Len(t, permissions, 1)

	var permID string
	for id := range permissions {
		permID = id
	}

	err := server.GrantToolPermission(permID, true, "approved by admin", true)
	require.NoError(t, err)

	// Wait for the result
	select {
	case r := <-resultCh:
		require.NoError(t, r.err)
		require.NotNil(t, r.resp)
		assert.True(t, r.resp.Granted)
		assert.Equal(t, "approved by admin", r.resp.Message)
		assert.True(t, r.resp.RememberDecision)
		assert.False(t, r.resp.TimedOut)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission response")
	}

	// Verify cleanup
	server.pendingPermissionsMu.RLock()
	assert.Empty(t, server.pendingPermissions)
	server.pendingPermissionsMu.RUnlock()
}

func TestRequestToolPermission_DeniedByUser(t *testing.T) {
	server := newTestServerForPermissions(t)

	req := &loomv1.ToolPermissionRequest{
		SessionId:      "session-1",
		ToolName:       "delete_all",
		Description:    "Delete everything",
		RiskLevel:      "high",
		TimeoutSeconds: 5,
	}

	type result struct {
		resp *loomv1.ToolPermissionResponse
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := server.RequestToolPermission(context.Background(), req)
		resultCh <- result{resp, err}
	}()

	time.Sleep(50 * time.Millisecond)

	permissions := server.GetPendingPermissions()
	require.Len(t, permissions, 1)

	var permID string
	for id := range permissions {
		permID = id
	}

	err := server.GrantToolPermission(permID, false, "too risky", false)
	require.NoError(t, err)

	select {
	case r := <-resultCh:
		require.NoError(t, r.err)
		require.NotNil(t, r.resp)
		assert.False(t, r.resp.Granted)
		assert.Equal(t, "too risky", r.resp.Message)
		assert.False(t, r.resp.RememberDecision)
		assert.False(t, r.resp.TimedOut)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission response")
	}
}

func TestGrantToolPermission_NotFound(t *testing.T) {
	server := newTestServerForPermissions(t)

	err := server.GrantToolPermission("non-existent-id", true, "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or already answered")
}

func TestRequestToolPermission_ContextCancelled(t *testing.T) {
	server := newTestServerForPermissions(t)

	req := &loomv1.ToolPermissionRequest{
		SessionId:      "session-1",
		ToolName:       "slow_tool",
		TimeoutSeconds: 60,
	}

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		resp *loomv1.ToolPermissionResponse
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := server.RequestToolPermission(ctx, req)
		resultCh <- result{resp, err}
	}()

	// Wait for registration then cancel
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case r := <-resultCh:
		require.Error(t, r.err)
		st, ok := status.FromError(r.err)
		require.True(t, ok)
		assert.Equal(t, codes.Canceled, st.Code())
		assert.Nil(t, r.resp)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}

	// Verify cleanup
	server.pendingPermissionsMu.RLock()
	assert.Empty(t, server.pendingPermissions)
	server.pendingPermissionsMu.RUnlock()
}

func TestRequestToolPermission_ConcurrentRequests(t *testing.T) {
	server := newTestServerForPermissions(t)

	numRequests := 5
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := &loomv1.ToolPermissionRequest{
				SessionId:      "session-1",
				ToolName:       "tool_" + string(rune('A'+idx)),
				TimeoutSeconds: 1,
			}
			resp, err := server.RequestToolPermission(context.Background(), req)
			assert.NoError(t, err)
			assert.NotNil(t, resp)
			assert.True(t, resp.TimedOut) // All will timeout since nobody answers
		}(i)
	}

	wg.Wait()

	// Verify all cleaned up
	server.pendingPermissionsMu.RLock()
	assert.Empty(t, server.pendingPermissions)
	server.pendingPermissionsMu.RUnlock()
}

func TestGetPendingPermissions_Empty(t *testing.T) {
	server := newTestServerForPermissions(t)
	perms := server.GetPendingPermissions()
	assert.Empty(t, perms)
}

// --- RecordTraceSpan Tests ---

func TestRecordTraceSpan(t *testing.T) {
	server := newTestServerWithTracer(t)

	span := &observability.Span{
		TraceID:    "record-trace",
		SpanID:     "record-span",
		Name:       "test.operation",
		Attributes: map[string]interface{}{},
	}

	server.RecordTraceSpan(span)

	// Verify span is retrievable
	stored := server.traceStoreLocal.GetTrace("record-trace")
	require.NotNil(t, stored)
	assert.Len(t, stored.Spans, 1)
}

func TestRecordTraceSpan_NilStore(t *testing.T) {
	server := &MultiAgentServer{
		logger: zap.NewNop(),
		// traceStoreLocal is nil
	}

	// Should not panic
	span := &observability.Span{
		TraceID:    "orphan-trace",
		SpanID:     "orphan-span",
		Attributes: map[string]interface{}{},
	}
	server.RecordTraceSpan(span)
}

// --- SetTraceStore Tests ---

func TestSetTraceStore(t *testing.T) {
	server := &MultiAgentServer{
		logger: zap.NewNop(),
	}

	assert.Nil(t, server.traceStoreLocal)

	server.SetTraceStore(30 * time.Minute)

	assert.NotNil(t, server.traceStoreLocal)
	assert.Equal(t, 30*time.Minute, server.traceStoreLocal.maxAge)
}

// TestSetTracer_InitializesTraceStore verifies that SetTracer also initializes
// the trace store as a safety net for servers not created via NewMultiAgentServer.
func TestSetTracer_InitializesTraceStore(t *testing.T) {
	server := &MultiAgentServer{
		logger: zap.NewNop(),
	}

	// Before SetTracer, trace store should be nil
	assert.Nil(t, server.traceStoreLocal)

	tracer := observability.NewMockTracer()
	server.SetTracer(tracer)

	// After SetTracer, trace store should be initialized
	assert.NotNil(t, server.traceStoreLocal)
	assert.Equal(t, 1*time.Hour, server.traceStoreLocal.maxAge)
}

// TestSetTracer_DoesNotOverrideExistingStore verifies that SetTracer does not
// overwrite an existing trace store (e.g., one set by NewMultiAgentServer or SetTraceStore).
func TestSetTracer_DoesNotOverrideExistingStore(t *testing.T) {
	server := &MultiAgentServer{
		logger: zap.NewNop(),
	}

	// Set a custom trace store with 30-minute max age
	server.SetTraceStore(30 * time.Minute)
	assert.Equal(t, 30*time.Minute, server.traceStoreLocal.maxAge)

	// SetTracer should NOT override the existing store
	tracer := observability.NewMockTracer()
	server.SetTracer(tracer)
	assert.Equal(t, 30*time.Minute, server.traceStoreLocal.maxAge)
}

// TestGetTrace_WorksAfterNewMultiAgentServer verifies the core bug fix:
// GetTrace should not return FailedPrecondition when the server is created
// via NewMultiAgentServer and a tracer is set. Previously, traceStoreLocal
// was never initialized in the constructor, causing GetTrace to always fail.
func TestGetTrace_WorksAfterNewMultiAgentServer(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockLLMForMultiAgent{}
	ag := agent.NewAgent(backend, llm)

	server := NewMultiAgentServer(map[string]*agent.Agent{"default": ag}, nil)
	require.NotNil(t, server)

	// The trace store should be initialized by the constructor
	assert.NotNil(t, server.traceStoreLocal, "traceStoreLocal must be initialized by NewMultiAgentServer")

	// Set a tracer so GetTrace does not fail on the tracer check
	tracer := observability.NewMockTracer()
	server.SetTracer(tracer)

	// Add a span to the store
	span := &observability.Span{
		TraceID:    "test-trace-b2",
		SpanID:     "test-span-b2",
		Name:       "server.Weave",
		Attributes: map[string]interface{}{observability.AttrSessionID: "sess-1"},
	}
	server.RecordTraceSpan(span)

	// GetTrace should now succeed instead of returning FailedPrecondition
	resp, err := server.GetTrace(context.Background(), &loomv1.GetTraceRequest{
		TraceId: "test-trace-b2",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "test-trace-b2", resp.Id)
	assert.Len(t, resp.Spans, 1)
	assert.Equal(t, "server.Weave", resp.Spans[0].Name)
}

// --- convertSpanToProto Tests ---

func TestConvertSpanToProto_EmptySpan(t *testing.T) {
	span := &observability.Span{
		SpanID: "empty-span",
		Name:   "empty",
		Status: observability.Status{Code: observability.StatusUnset},
	}

	proto := convertSpanToProto(span)
	assert.Equal(t, "empty-span", proto.Id)
	assert.Equal(t, "empty", proto.Name)
	assert.Equal(t, "unset", proto.Status)
	assert.Nil(t, proto.Attributes)
	assert.Nil(t, proto.Events)
}

func TestConvertSpanToProto_FullSpan(t *testing.T) {
	now := time.Now()
	span := &observability.Span{
		SpanID:    "full-span",
		ParentID:  "parent-span",
		Name:      "llm.completion",
		StartTime: now.Add(-100 * time.Millisecond),
		EndTime:   now,
		Duration:  100 * time.Millisecond,
		Status:    observability.Status{Code: observability.StatusOK},
		Attributes: map[string]interface{}{
			"llm.model":    "claude-3",
			"token.count":  100,
			"is.streaming": true,
			"cost":         0.005,
		},
		Events: []observability.Event{
			{
				Timestamp: now.Add(-50 * time.Millisecond),
				Name:      "first_token",
				Attributes: map[string]interface{}{
					"ttft_ms": 50,
				},
			},
			{
				Timestamp:  now,
				Name:       "completion",
				Attributes: nil,
			},
		},
	}

	proto := convertSpanToProto(span)
	assert.Equal(t, "full-span", proto.Id)
	assert.Equal(t, "parent-span", proto.ParentId)
	assert.Equal(t, "llm.completion", proto.Name)
	assert.Equal(t, "ok", proto.Status)
	assert.True(t, proto.DurationUs > 0)
	assert.True(t, proto.StartTimeUs > 0)
	assert.True(t, proto.EndTimeUs > 0)
	assert.True(t, proto.EndTimeUs > proto.StartTimeUs)

	// Check attributes were converted to strings
	assert.Equal(t, "claude-3", proto.Attributes["llm.model"])
	assert.Equal(t, "100", proto.Attributes["token.count"])
	assert.Equal(t, "true", proto.Attributes["is.streaming"])
	assert.Equal(t, "0.005", proto.Attributes["cost"])

	// Check events
	require.Len(t, proto.Events, 2)
	assert.Equal(t, "first_token", proto.Events[0].Name)
	assert.Equal(t, "50", proto.Events[0].Attributes["ttft_ms"])
	assert.Equal(t, "completion", proto.Events[1].Name)
	assert.Nil(t, proto.Events[1].Attributes) // nil Attributes preserved as nil
}
