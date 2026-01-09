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
package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// errorMockServer returns errors for testing error handling.
type errorMockServer struct {
	loomv1.UnimplementedLoomServiceServer
}

func (m *errorMockServer) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	return nil, status.Error(codes.Internal, "mock LLM error")
}

func (m *errorMockServer) StreamWeave(req *loomv1.WeaveRequest, stream loomv1.LoomService_StreamWeaveServer) error {
	return status.Error(codes.Unavailable, "stream unavailable")
}

func (m *errorMockServer) CreateSession(ctx context.Context, req *loomv1.CreateSessionRequest) (*loomv1.Session, error) {
	return nil, status.Error(codes.ResourceExhausted, "too many sessions")
}

func (m *errorMockServer) GetSession(ctx context.Context, req *loomv1.GetSessionRequest) (*loomv1.Session, error) {
	return nil, status.Error(codes.NotFound, "session not found")
}

func (m *errorMockServer) DeleteSession(ctx context.Context, req *loomv1.DeleteSessionRequest) (*loomv1.DeleteSessionResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "cannot delete session")
}

func (m *errorMockServer) GetConversationHistory(ctx context.Context, req *loomv1.GetConversationHistoryRequest) (*loomv1.ConversationHistory, error) {
	return nil, status.Error(codes.DataLoss, "history corrupted")
}

func (m *errorMockServer) ListSessions(ctx context.Context, req *loomv1.ListSessionsRequest) (*loomv1.ListSessionsResponse, error) {
	return nil, status.Error(codes.DeadlineExceeded, "timeout listing sessions")
}

func (m *errorMockServer) ListTools(ctx context.Context, req *loomv1.ListToolsRequest) (*loomv1.ListToolsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "tools not implemented")
}

func (m *errorMockServer) GetHealth(ctx context.Context, req *loomv1.GetHealthRequest) (*loomv1.HealthStatus, error) {
	return nil, status.Error(codes.Unavailable, "server unhealthy")
}

// setupErrorServer creates a server that returns errors.
func setupErrorServer(t *testing.T) (*grpc.Server, *bufconn.Listener) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, &errorMockServer{})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	return server, lis
}

func TestWeaveError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	resp, err := client.Weave(ctx, "test query", "sess_123", "test-agent")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if resp != nil {
		t.Errorf("Expected nil response on error, got: %v", resp)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Internal {
		t.Errorf("Expected Internal code, got: %v", st.Code())
	}

	if st.Message() != "mock LLM error" {
		t.Errorf("Expected 'mock LLM error', got: %s", st.Message())
	}
}

func TestStreamWeaveError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	err := client.StreamWeave(ctx, "test query", "sess_123", "test-agent", func(progress *loomv1.WeaveProgress) {
		t.Fatal("Should not receive progress on error")
	})

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unavailable {
		t.Errorf("Expected Unavailable code, got: %v", st.Code())
	}
}

func TestCreateSessionError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	session, err := client.CreateSession(ctx, "test", "")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if session != nil {
		t.Errorf("Expected nil session on error, got: %v", session)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.ResourceExhausted {
		t.Errorf("Expected ResourceExhausted code, got: %v", st.Code())
	}
}

func TestGetSessionError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	_, err := client.GetSession(ctx, "sess_456")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.NotFound {
		t.Errorf("Expected NotFound code, got: %v", st.Code())
	}
}

func TestDeleteSessionError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	err := client.DeleteSession(ctx, "sess_789")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.PermissionDenied {
		t.Errorf("Expected PermissionDenied code, got: %v", st.Code())
	}
}

func TestGetConversationHistoryError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	messages, err := client.GetConversationHistory(ctx, "sess_123")

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if messages != nil {
		t.Errorf("Expected nil messages on error, got: %v", messages)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.DataLoss {
		t.Errorf("Expected DataLoss code, got: %v", st.Code())
	}
}

func TestListSessionsError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	sessions, err := client.ListSessions(ctx, 10, 0)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if sessions != nil {
		t.Errorf("Expected nil sessions on error, got: %v", sessions)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded code, got: %v", st.Code())
	}
}

func TestListToolsError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	tools, err := client.ListTools(ctx)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if tools != nil {
		t.Errorf("Expected nil tools on error, got: %v", tools)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

func TestGetHealthError(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	health, err := client.GetHealth(ctx)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if health != nil {
		t.Errorf("Expected nil health on error, got: %v", health)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unavailable {
		t.Errorf("Expected Unavailable code, got: %v", st.Code())
	}
}

// Test timeout scenarios
func TestWeaveWithTimeout(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	// Create context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	time.Sleep(10 * time.Millisecond) // Ensure timeout occurs

	_, err := client.Weave(ctx, "test query", "sess_123", "test-agent")

	// Should get deadline exceeded error
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}

	st, ok := status.FromError(err)
	if ok && st.Code() == codes.DeadlineExceeded {
		// Expected timeout
		return
	}

	// Or context deadline exceeded
	if ctx.Err() == context.DeadlineExceeded {
		// Also acceptable
		return
	}
}

func TestStreamWeaveWithCancellation(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	err := client.StreamWeave(ctx, "test query", "sess_123", "test-agent", func(progress *loomv1.WeaveProgress) {
		t.Fatal("Should not receive progress after cancellation")
	})

	if err == nil {
		t.Fatal("Expected error after cancellation, got nil")
	}
}

// Test invalid inputs
// Note: TestWeaveWithEmptySessionID removed - tests server-side session ID generation,
// which is already tested in pkg/server/integration_test.go

func TestListSessionsWithPagination(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()

	// Test with different limits and offsets
	tests := []struct {
		name   string
		limit  int32
		offset int32
	}{
		{"zero limit", 0, 0},
		{"small limit", 10, 0},
		{"with offset", 10, 5},
		{"large limit", 1000, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions, err := client.ListSessions(ctx, tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("ListSessions failed: %v", err)
			}

			// Mock server returns 2 sessions regardless
			if len(sessions) != 2 {
				t.Errorf("Expected 2 sessions, got: %d", len(sessions))
			}
		})
	}
}

// Note: TestGetConversationHistoryWithEmptySession removed - requires server-side GetConversationHistory implementation,
// which is tested in pkg/server/integration_test.go

// Test connection failure scenarios
func TestNewClientConnectionFailure(t *testing.T) {
	// grpc.NewClient creates lazy connections, so NewClient won't fail immediately.
	// Instead, verify that RPC calls fail when server is unreachable.
	client, err := NewClient(Config{
		ServerAddr: "localhost:99999", // Invalid port
		Timeout:    100 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("NewClient should not fail immediately with lazy connections: %v", err)
	}
	defer client.Close()

	// Try to make an RPC call - this should fail
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = client.Weave(ctx, "test", "session", "test-agent")
	if err == nil {
		t.Fatal("Expected RPC error when connecting to invalid server")
	}

	// Verify error contains connection details
	if err.Error() == "" {
		t.Error("Error message should not be empty")
	}
}

func TestNewClientWithDefaultConfig(t *testing.T) {
	// grpc.NewClient creates lazy connections, so NewClient won't fail immediately.
	// Verify that default config is applied and RPC calls fail when no server is running.
	client, err := NewClient(Config{
		Timeout: 100 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("NewClient should not fail immediately with lazy connections: %v", err)
	}
	defer client.Close()

	// Verify default address is used
	if client.ServerAddr() != "localhost:9090" {
		t.Errorf("Expected default address localhost:9090, got: %s", client.ServerAddr())
	}

	// Try to make an RPC call - this should fail (no server running)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = client.Weave(ctx, "test", "session", "test-agent")
	if err == nil {
		t.Fatal("Expected RPC error when no server is running")
	}

	// Error should contain connection details
	if err.Error() == "" {
		t.Error("Error should contain connection details")
	}
}

// Test edge cases
func TestWeaveWithNilProgress(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()

	// Progress function is nil - should not panic
	err := client.StreamWeave(ctx, "test", "sess_123", "test-agent", nil)
	if err != nil {
		t.Fatalf("StreamWeave with nil progress should succeed: %v", err)
	}
}

func TestStreamWeaveEOF(t *testing.T) {
	// This tests that EOF is handled gracefully
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	receivedProgress := false

	err := client.StreamWeave(ctx, "test", "sess_123", "test-agent", func(progress *loomv1.WeaveProgress) {
		receivedProgress = true
	})

	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	if !receivedProgress {
		t.Error("Expected to receive at least one progress update")
	}
}

// Test concurrent error scenarios
func TestConcurrentErrorHandling(t *testing.T) {
	server, lis := setupErrorServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	const numRequests = 50

	done := make(chan bool, numRequests)

	// All requests should fail, but gracefully
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			_, err := client.Weave(ctx, fmt.Sprintf("query %d", id), "sess_test", "test-agent")
			if err == nil {
				t.Errorf("Request %d: expected error, got nil", id)
			} else {
				// Verify it's a proper gRPC error
				if _, ok := status.FromError(err); !ok {
					t.Errorf("Request %d: expected gRPC error, got: %v", id, err)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numRequests; i++ {
		<-done
	}
}

// Test slow server responses
type slowMockServer struct {
	loomv1.UnimplementedLoomServiceServer
	delay time.Duration
}

func (m *slowMockServer) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	select {
	case <-time.After(m.delay):
		return &loomv1.WeaveResponse{
			Text:      "Slow response",
			SessionId: req.SessionId,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestWeaveWithSlowServer(t *testing.T) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, &slowMockServer{delay: 100 * time.Millisecond})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	// Should succeed with adequate timeout
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := client.Weave(ctx, "test", "sess_123", "test-agent")
	if err != nil {
		t.Fatalf("Weave failed: %v", err)
	}

	if resp.Text != "Slow response" {
		t.Errorf("Expected 'Slow response', got: %s", resp.Text)
	}
}

func TestWeaveWithSlowServerTimeout(t *testing.T) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	// Very slow server (500ms delay)
	loomv1.RegisterLoomServiceServer(server, &slowMockServer{delay: 500 * time.Millisecond})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	// Short timeout (50ms) - should fail
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := client.Weave(ctx, "test", "sess_123", "test-agent")
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}

	if resp != nil {
		t.Errorf("Expected nil response on timeout, got: %v", resp)
	}
}

// Test malformed responses
type malformedMockServer struct {
	loomv1.UnimplementedLoomServiceServer
}

func (m *malformedMockServer) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	// Return response with nil cost (potential nil pointer issue)
	return &loomv1.WeaveResponse{
		Text:      "response",
		SessionId: req.SessionId,
		Cost:      nil, // Missing cost
	}, nil
}

func TestWeaveWithMalformedResponse(t *testing.T) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, &malformedMockServer{})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	resp, err := client.Weave(ctx, "test", "sess_123", "test-agent")

	// Should succeed even with nil cost
	if err != nil {
		t.Fatalf("Weave should handle nil cost: %v", err)
	}

	if resp.Cost != nil {
		t.Logf("Note: Cost is not nil (proto may default it)")
	}
}

// Test stream with multiple progress updates
type multiProgressMockServer struct {
	loomv1.UnimplementedLoomServiceServer
}

func (m *multiProgressMockServer) StreamWeave(req *loomv1.WeaveRequest, stream loomv1.LoomService_StreamWeaveServer) error {
	// Send multiple progress updates
	stages := []loomv1.ExecutionStage{
		loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION,
		loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION,
		loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION,
		loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
	}

	for i, stage := range stages {
		progress := &loomv1.WeaveProgress{
			Stage:    stage,
			Progress: int32((i + 1) * 25),
			Message:  fmt.Sprintf("Stage %d", i),
		}

		if err := stream.Send(progress); err != nil {
			return err
		}
	}

	return nil
}

func TestStreamWeaveMultipleProgress(t *testing.T) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, &multiProgressMockServer{})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	progressCount := 0
	lastProgress := int32(0)

	err := client.StreamWeave(ctx, "test", "sess_123", "test-agent", func(progress *loomv1.WeaveProgress) {
		progressCount++
		if progress.Progress <= lastProgress {
			t.Errorf("Progress should increase: %d -> %d", lastProgress, progress.Progress)
		}
		lastProgress = progress.Progress
	})

	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	if progressCount != 4 {
		t.Errorf("Expected 4 progress updates, got: %d", progressCount)
	}

	if lastProgress != 100 {
		t.Errorf("Expected final progress 100, got: %d", lastProgress)
	}
}

// Test connection cleanup
func TestClientCleanup(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)

	// Use client
	ctx := context.Background()
	_, err := client.Weave(ctx, "test", "sess_123", "test-agent")
	if err != nil {
		t.Fatalf("Weave failed: %v", err)
	}

	// Close client
	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Try to use after close - should fail
	_, err = client.Weave(ctx, "test2", "sess_123", "test-agent")
	if err == nil {
		t.Error("Expected error after close, got nil")
	}
}

// Test server address retrieval
func TestServerAddrPersistence(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	addr1 := client.ServerAddr()
	addr2 := client.ServerAddr()

	if addr1 != addr2 {
		t.Error("ServerAddr should be consistent")
	}

	if addr1 != "passthrough:///bufnet" {
		t.Errorf("Expected passthrough:///bufnet, got: %s", addr1)
	}
}
