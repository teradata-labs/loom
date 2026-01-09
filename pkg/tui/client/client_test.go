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
	"net"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// mockLoomServiceServer is a mock implementation for testing.
type mockLoomServiceServer struct {
	loomv1.UnimplementedLoomServiceServer
}

func (m *mockLoomServiceServer) Weave(ctx context.Context, req *loomv1.WeaveRequest) (*loomv1.WeaveResponse, error) {
	return &loomv1.WeaveResponse{
		Text:      "Mock response to: " + req.Query,
		SessionId: req.SessionId,
		Cost: &loomv1.CostInfo{
			TotalCostUsd: 0.001,
			LlmCost: &loomv1.LLMCost{
				InputTokens:  10,
				OutputTokens: 20,
				CostUsd:      0.001,
			},
		},
	}, nil
}

func (m *mockLoomServiceServer) StreamWeave(req *loomv1.WeaveRequest, stream loomv1.LoomService_StreamWeaveServer) error {
	progress := &loomv1.WeaveProgress{
		Stage:    loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
		Progress: 100,
		Message:  "Test complete",
	}
	return stream.Send(progress)
}

func (m *mockLoomServiceServer) CreateSession(ctx context.Context, req *loomv1.CreateSessionRequest) (*loomv1.Session, error) {
	return &loomv1.Session{
		Id:        "sess_test123",
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		State:     "active",
	}, nil
}

func (m *mockLoomServiceServer) GetSession(ctx context.Context, req *loomv1.GetSessionRequest) (*loomv1.Session, error) {
	return &loomv1.Session{
		Id:        req.SessionId,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		State:     "active",
	}, nil
}

func (m *mockLoomServiceServer) ListSessions(ctx context.Context, req *loomv1.ListSessionsRequest) (*loomv1.ListSessionsResponse, error) {
	return &loomv1.ListSessionsResponse{
		Sessions: []*loomv1.Session{
			{Id: "sess_1", State: "active"},
			{Id: "sess_2", State: "active"},
		},
	}, nil
}

func (m *mockLoomServiceServer) GetHealth(ctx context.Context, req *loomv1.GetHealthRequest) (*loomv1.HealthStatus, error) {
	return &loomv1.HealthStatus{
		Status:  "healthy",
		Version: "0.1.0",
	}, nil
}

func (m *mockLoomServiceServer) ListAgents(ctx context.Context, req *loomv1.ListAgentsRequest) (*loomv1.ListAgentsResponse, error) {
	return &loomv1.ListAgentsResponse{
		Agents: []*loomv1.AgentInfo{
			{Id: "file-agent", Name: "File Agent", Status: "running"},
			{Id: "sql-agent", Name: "SQL Agent", Status: "running"},
		},
	}, nil
}

// setupTestServer creates a mock gRPC server for testing.
func setupTestServer(t *testing.T) (*grpc.Server, *bufconn.Listener) {
	buffer := 1024 * 1024
	lis := bufconn.Listen(buffer)

	server := grpc.NewServer()
	loomv1.RegisterLoomServiceServer(server, &mockLoomServiceServer{})

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("Server error: %v", err)
		}
	}()

	return server, lis
}

// createTestClient creates a client connected to the mock server.
func createTestClient(t *testing.T, lis *bufconn.Listener) *Client {
	conn, err := grpc.NewClient(
		"passthrough:///bufnet", // Use passthrough scheme for custom dialer
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	return &Client{
		conn:   conn,
		client: loomv1.NewLoomServiceClient(conn),
		addr:   "passthrough:///bufnet",
	}
}

func TestWeave(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	resp, err := client.Weave(ctx, "test query", "sess_123", "test-agent")
	if err != nil {
		t.Fatalf("Weave failed: %v", err)
	}

	if resp.Text != "Mock response to: test query" {
		t.Errorf("Expected mock response, got: %s", resp.Text)
	}

	if resp.SessionId != "sess_123" {
		t.Errorf("Expected session sess_123, got: %s", resp.SessionId)
	}

	if resp.Cost.TotalCostUsd != 0.001 {
		t.Errorf("Expected cost 0.001, got: %f", resp.Cost.TotalCostUsd)
	}
}

func TestStreamWeave(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	progressCount := 0
	err := client.StreamWeave(ctx, "test query", "sess_123", "test-agent", func(progress *loomv1.WeaveProgress) {
		progressCount++
		if progress.Message != "Test complete" {
			t.Errorf("Expected 'Test complete', got: %s", progress.Message)
		}
	})

	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	if progressCount != 1 {
		t.Errorf("Expected 1 progress update, got: %d", progressCount)
	}
}

func TestCreateSession(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	session, err := client.CreateSession(ctx, "test-session", "")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if session.Id != "sess_test123" {
		t.Errorf("Expected sess_test123, got: %s", session.Id)
	}

	if session.State != "active" {
		t.Errorf("Expected active state, got: %s", session.State)
	}
}

func TestGetSession(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	session, err := client.GetSession(ctx, "sess_456")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if session.Id != "sess_456" {
		t.Errorf("Expected sess_456, got: %s", session.Id)
	}
}

func TestListSessions(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	sessions, err := client.ListSessions(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got: %d", len(sessions))
	}

	if sessions[0].Id != "sess_1" {
		t.Errorf("Expected sess_1, got: %s", sessions[0].Id)
	}
}

func TestGetHealth(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	status, err := client.GetHealth(ctx)
	if err != nil {
		t.Fatalf("GetHealth failed: %v", err)
	}

	if status.Status != "healthy" {
		t.Errorf("Expected healthy, got: %s", status.Status)
	}

	if status.Version != "0.1.0" {
		t.Errorf("Expected version 0.1.0, got: %s", status.Version)
	}
}

func TestServerAddr(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	if client.ServerAddr() != "passthrough:///bufnet" {
		t.Errorf("Expected passthrough:///bufnet, got: %s", client.ServerAddr())
	}
}

func TestListAgents(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()
	agents, err := client.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}

	if len(agents) != 2 {
		t.Errorf("Expected 2 agents, got: %d", len(agents))
	}

	if agents[0].Id != "file-agent" {
		t.Errorf("Expected file-agent, got: %s", agents[0].Id)
	}

	if agents[0].Name != "File Agent" {
		t.Errorf("Expected File Agent, got: %s", agents[0].Name)
	}

	if agents[0].Status != "running" {
		t.Errorf("Expected running status, got: %s", agents[0].Status)
	}

	if agents[1].Id != "sql-agent" {
		t.Errorf("Expected sql-agent, got: %s", agents[1].Id)
	}
}

func TestClose(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)

	err := client.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Second close may return an error (connection already closed)
	// but should not panic
	_ = client.Close()
}

// TestRaceConditions runs multiple concurrent operations.
func TestRaceConditions(t *testing.T) {
	server, lis := setupTestServer(t)
	defer server.Stop()

	client := createTestClient(t, lis)
	defer client.Close()

	ctx := context.Background()

	// Run 100 concurrent Weave calls
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func(id int) {
			_, err := client.Weave(ctx, "test query", "sess_123", "test-agent")
			if err != nil {
				t.Errorf("Concurrent Weave %d failed: %v", id, err)
			}
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 100; i++ {
		<-done
	}
}
