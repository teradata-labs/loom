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
	"fmt"
	"sync"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Helper to create a test agent with mock LLM
func createTestAgent() *agent.Agent {
	mockLLM := &mockLLMProvider{
		responses: []string{
			"Mock response",
		},
	}
	mockBackend := &mockBackend{}
	return agent.NewAgent(mockBackend, mockLLM)
}

// Helper to create a test agent with custom LLM behavior
func createTestAgentWithLLM(llm *mockLLMProvider) *agent.Agent {
	mockBackend := &mockBackend{}
	return agent.NewAgent(mockBackend, llm)
}

// Test Server RPC Handlers

func TestServer_Weave_Success(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.WeaveRequest{
		Query:     "What is 2+2?",
		SessionId: "sess_test123",
	}

	resp, err := srv.Weave(context.Background(), req)
	if err != nil {
		t.Fatalf("Weave failed: %v", err)
	}

	if resp.Text == "" {
		t.Error("Expected non-empty response text")
	}

	if resp.SessionId != "sess_test123" {
		t.Errorf("Expected session sess_test123, got: %s", resp.SessionId)
	}

	if resp.Cost == nil {
		t.Fatal("Expected cost info, got nil")
	}

	if resp.Cost.TotalCostUsd <= 0 {
		t.Errorf("Expected positive cost, got: %f", resp.Cost.TotalCostUsd)
	}
}

func TestServer_Weave_EmptyQuery(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.WeaveRequest{
		Query: "",
	}

	resp, err := srv.Weave(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for empty query, got nil")
	}

	if resp != nil {
		t.Errorf("Expected nil response, got: %v", resp)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument code, got: %v", st.Code())
	}
}

func TestServer_Weave_GeneratesSessionID(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.WeaveRequest{
		Query: "test query",
		// No SessionId provided
	}

	resp, err := srv.Weave(context.Background(), req)
	if err != nil {
		t.Fatalf("Weave failed: %v", err)
	}

	if resp.SessionId == "" {
		t.Error("Expected auto-generated session ID")
	}

	if len(resp.SessionId) < 5 || resp.SessionId[:5] != "sess_" {
		t.Errorf("Expected sess_ prefix, got: %s", resp.SessionId)
	}
}

func TestServer_Weave_AgentError(t *testing.T) {
	mockLLM := &mockLLMProvider{
		shouldError: true,
		errorMsg:    "LLM unavailable",
	}
	ag := createTestAgentWithLLM(mockLLM)
	srv := NewServer(ag, nil)

	req := &loomv1.WeaveRequest{
		Query: "test",
	}

	resp, err := srv.Weave(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if resp != nil {
		t.Errorf("Expected nil response, got: %v", resp)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Internal {
		t.Errorf("Expected Internal code, got: %v", st.Code())
	}
}

func TestServer_CreateSession_Success(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.CreateSessionRequest{
		Name: "test-session",
	}

	resp, err := srv.CreateSession(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if resp.Id == "" {
		t.Error("Expected session ID, got empty")
	}

	if resp.State != "active" {
		t.Errorf("Expected active state, got: %s", resp.State)
	}
}

func TestServer_GetSession_NotFound(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetSessionRequest{
		SessionId: "sess_nonexistent",
	}

	resp, err := srv.GetSession(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for non-existent session, got nil")
	}

	if resp != nil {
		t.Errorf("Expected nil response, got: %v", resp)
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.NotFound {
		t.Errorf("Expected NotFound code, got: %v", st.Code())
	}
}

func TestServer_GetSession_EmptyID(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetSessionRequest{
		SessionId: "",
	}

	_, err := srv.GetSession(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for empty session ID, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument code, got: %v", st.Code())
	}
}

func TestServer_ListSessions_Empty(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.ListSessionsRequest{}

	resp, err := srv.ListSessions(context.Background(), req)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	if len(resp.Sessions) != 0 {
		t.Errorf("Expected 0 sessions, got: %d", len(resp.Sessions))
	}
}

func TestServer_DeleteSession_EmptyID(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.DeleteSessionRequest{
		SessionId: "",
	}

	_, err := srv.DeleteSession(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for empty session ID, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument code, got: %v", st.Code())
	}
}

func TestServer_GetConversationHistory_NotFound(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetConversationHistoryRequest{
		SessionId: "sess_nonexistent",
	}

	_, err := srv.GetConversationHistory(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for non-existent session, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.NotFound {
		t.Errorf("Expected NotFound code, got: %v", st.Code())
	}
}

func TestServer_GetConversationHistory_EmptyID(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetConversationHistoryRequest{
		SessionId: "",
	}

	_, err := srv.GetConversationHistory(context.Background(), req)
	if err == nil {
		t.Fatal("Expected error for empty session ID, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument code, got: %v", st.Code())
	}
}

func TestServer_ListTools_Empty(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.ListToolsRequest{}

	resp, err := srv.ListTools(context.Background(), req)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	// Agent has 3 built-in tools: query_tool_result, shell_execute, record_finding
	// Note: get_tool_result removed - inline metadata makes it unnecessary
	// Note: recall_conversation, clear_recalled_context, search_conversation removed in scratchpad experiment
	// Note: get_error_details not registered (no errorStore in basic test setup)
	// Note: record_finding added for working memory feature
	if len(resp.Tools) != 3 {
		t.Errorf("Expected 3 built-in tools, got: %d", len(resp.Tools))
	}

	// Verify built-in tools are present
	toolNames := make(map[string]bool)
	for _, tool := range resp.Tools {
		toolNames[tool.Name] = true
	}
	expectedTools := []string{"query_tool_result", "shell_execute", "record_finding"}
	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("Expected built-in tool '%s' not found", expected)
		}
	}
}

// Note: TestServer_ListTools_Multiple removed - would require mockTool implementation
// which is already in server_test.go. ListTools is tested by TestServer_ListTools_Empty.

func TestServer_GetHealth_Success(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetHealthRequest{}

	resp, err := srv.GetHealth(context.Background(), req)
	if err != nil {
		t.Fatalf("GetHealth failed: %v", err)
	}

	if resp.Status != "healthy" {
		t.Errorf("Expected healthy status, got: %s", resp.Status)
	}

	if resp.Version != "0.1.0" {
		t.Errorf("Expected version 0.1.0, got: %s", resp.Version)
	}
}

func TestServer_RegisterTool_Unimplemented(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.RegisterToolRequest{}

	_, err := srv.RegisterTool(context.Background(), req)
	if err == nil {
		t.Fatal("Expected unimplemented error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

func TestServer_LoadPatterns_Unimplemented(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.LoadPatternsRequest{}

	_, err := srv.LoadPatterns(context.Background(), req)
	if err == nil {
		t.Fatal("Expected unimplemented error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

func TestServer_ListPatterns_Unimplemented(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.ListPatternsRequest{}

	_, err := srv.ListPatterns(context.Background(), req)
	if err == nil {
		t.Fatal("Expected unimplemented error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

func TestServer_GetPattern_Unimplemented(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetPatternRequest{Name: "test"}

	_, err := srv.GetPattern(context.Background(), req)
	if err == nil {
		t.Fatal("Expected unimplemented error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

func TestServer_GetTrace_Unimplemented(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	req := &loomv1.GetTraceRequest{TraceId: "trace_123"}

	_, err := srv.GetTrace(context.Background(), req)
	if err == nil {
		t.Fatal("Expected unimplemented error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.Unimplemented {
		t.Errorf("Expected Unimplemented code, got: %v", st.Code())
	}
}

// Integration Tests - Multiple Operations

func TestServer_FullConversationFlow(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	ctx := context.Background()

	// 1. Create session
	createResp, err := srv.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: "test-flow",
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	sessionID := createResp.Id

	// 2. Send first query
	weaveResp1, err := srv.Weave(ctx, &loomv1.WeaveRequest{
		Query:     "Hello",
		SessionId: sessionID,
	})
	if err != nil {
		t.Fatalf("First Weave failed: %v", err)
	}

	if weaveResp1.SessionId != sessionID {
		t.Errorf("Session ID mismatch: expected %s, got %s", sessionID, weaveResp1.SessionId)
	}

	// 3. Send second query (multi-turn)
	_, err = srv.Weave(ctx, &loomv1.WeaveRequest{
		Query:     "What did I just say?",
		SessionId: sessionID,
	})
	if err != nil {
		t.Fatalf("Second Weave failed: %v", err)
	}

	// 4. Get session info
	sessionResp, err := srv.GetSession(ctx, &loomv1.GetSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if sessionResp.Id != sessionID {
		t.Errorf("Session ID mismatch in get")
	}

	// 5. List sessions (should have our session)
	listResp, err := srv.ListSessions(ctx, &loomv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	found := false
	for _, sess := range listResp.Sessions {
		if sess.Id == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created session not found in list")
	}

	// 6. Delete session
	deleteResp, err := srv.DeleteSession(ctx, &loomv1.DeleteSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if !deleteResp.Success {
		t.Error("Delete should succeed")
	}

	// 7. Verify deleted
	_, err = srv.GetSession(ctx, &loomv1.GetSessionRequest{
		SessionId: sessionID,
	})
	if err == nil {
		t.Fatal("Expected NotFound error after deletion")
	}
}

func TestServer_ConcurrentRequests(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	ctx := context.Background()
	const numConcurrent = 50

	// Run 50 concurrent Weave requests
	done := make(chan error, numConcurrent)
	for i := 0; i < numConcurrent; i++ {
		go func(id int) {
			req := &loomv1.WeaveRequest{
				Query:     fmt.Sprintf("Query %d", id),
				SessionId: fmt.Sprintf("sess_%d", id),
			}
			_, err := srv.Weave(ctx, req)
			done <- err
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numConcurrent; i++ {
		err := <-done
		if err != nil {
			t.Errorf("Concurrent request %d failed: %v", i, err)
		}
	}
}

// Test with real agent and mock LLM/backend

func TestServer_WithRealAgent(t *testing.T) {
	// Create a real agent with mock LLM
	mockLLM := &mockLLMProvider{
		responses: []string{
			"Response 1",
			"Response 2",
			"Response 3",
		},
	}

	mockBackend := &mockBackend{}

	ag := agent.NewAgent(mockBackend, mockLLM)
	srv := NewServer(ag, nil)

	ctx := context.Background()

	// Test real conversation flow
	resp1, err := srv.Weave(ctx, &loomv1.WeaveRequest{
		Query:     "First query",
		SessionId: "sess_real",
	})
	if err != nil {
		t.Fatalf("First weave failed: %v", err)
	}

	if resp1.SessionId != "sess_real" {
		t.Errorf("Expected sess_real, got: %s", resp1.SessionId)
	}

	// Send another query to same session
	_, err = srv.Weave(ctx, &loomv1.WeaveRequest{
		Query:     "Second query",
		SessionId: "sess_real",
	})
	if err != nil {
		t.Fatalf("Second weave failed: %v", err)
	}

	// Verify session exists
	sessResp, err := srv.GetSession(ctx, &loomv1.GetSessionRequest{
		SessionId: "sess_real",
	})
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if sessResp.Id != "sess_real" {
		t.Errorf("Session ID mismatch")
	}
}

// mockLLMProvider for integration testing
type mockLLMProvider struct {
	responses   []string
	currentIdx  int
	shouldError bool
	errorMsg    string
	mu          sync.Mutex // Protect currentIdx for concurrent access
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if m.shouldError {
		return nil, fmt.Errorf("%s", m.errorMsg)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.currentIdx >= len(m.responses) {
		m.currentIdx = 0
	}

	resp := &llmtypes.LLMResponse{
		Content: m.responses[m.currentIdx],
		Usage: llmtypes.Usage{
			InputTokens:  50,
			OutputTokens: 25,
			CostUSD:      0.0037,
		},
	}

	m.currentIdx++
	return resp, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func (m *mockLLMProvider) Model() string {
	return "mock-model-v1"
}

// mockBackend for integration testing
type mockBackend struct{}

func (m *mockBackend) Name() string { return "mock" }
func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "rows", RowCount: 0}, nil
}
func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: resource}, nil
}
func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}
func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}
func (m *mockBackend) Close() error                   { return nil }
func (m *mockBackend) Ping(ctx context.Context) error { return nil }
func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not supported")
}

// StreamWeave Integration Tests

func TestServer_StreamWeave_Success(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	// Create mock stream
	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query:     "What is 2+2?",
		SessionId: "sess_stream_test",
	}

	err := srv.StreamWeave(req, stream)
	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	// Should receive at least completion message
	if len(stream.messages) < 1 {
		t.Fatal("Expected at least 1 progress update (completion)")
	}

	// Last message should be completed
	lastMsg := stream.messages[len(stream.messages)-1]
	if lastMsg.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
		t.Errorf("Expected final stage COMPLETED, got: %v", lastMsg.Stage)
	}

	if lastMsg.Progress != 100 {
		t.Errorf("Expected final progress 100, got: %d", lastMsg.Progress)
	}

	if lastMsg.PartialResult == nil {
		t.Fatal("Expected partial result in final message")
	}

	// Verify result contains response
	if lastMsg.PartialResult.DataJson == "" {
		t.Error("Expected non-empty response data")
	}
}

func TestServer_StreamWeave_ProgressStages(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query:     "Test query",
		SessionId: "sess_stages",
	}

	err := srv.StreamWeave(req, stream)
	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	// Should receive at least one message
	if len(stream.messages) == 0 {
		t.Fatal("Expected at least one progress update")
	}

	// Verify stages seen
	seenStages := make(map[loomv1.ExecutionStage]bool)
	for _, msg := range stream.messages {
		seenStages[msg.Stage] = true
	}

	// Must see completion stage
	if !seenStages[loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED] {
		t.Error("Expected to see COMPLETED stage")
	}

	// If we see multiple stages, verify progress increases
	if len(stream.messages) > 1 {
		for i := 1; i < len(stream.messages); i++ {
			prev := stream.messages[i-1].Progress
			curr := stream.messages[i].Progress
			if curr < prev && curr != 0 { // 0 is for failure
				t.Errorf("Progress decreased: %d -> %d", prev, curr)
			}
		}
	}

	// Final progress should be 100
	lastMsg := stream.messages[len(stream.messages)-1]
	if lastMsg.Progress != 100 {
		t.Errorf("Expected final progress 100, got: %d", lastMsg.Progress)
	}
}

func TestServer_StreamWeave_EmptyQuery(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query: "",
	}

	err := srv.StreamWeave(req, stream)
	if err == nil {
		t.Fatal("Expected error for empty query, got nil")
	}

	// Should be InvalidArgument error
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument code, got: %v", st.Code())
	}
}

func TestServer_StreamWeave_AgentError(t *testing.T) {
	mockLLM := &mockLLMProvider{
		shouldError: true,
		errorMsg:    "LLM service down",
	}
	ag := createTestAgentWithLLM(mockLLM)
	srv := NewServer(ag, nil)

	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query:     "This will fail",
		SessionId: "sess_error",
	}

	err := srv.StreamWeave(req, stream)
	if err == nil {
		t.Fatal("Expected error from agent failure, got nil")
	}

	// Should receive failure stage
	foundFailure := false
	for _, msg := range stream.messages {
		if msg.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_FAILED {
			foundFailure = true
			if msg.Message == "" {
				t.Error("Expected error message in failure stage")
			}
		}
	}

	if !foundFailure {
		t.Error("Expected FAILED stage in progress updates")
	}
}

func TestServer_StreamWeave_ContextCancellation(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stream := &mockStreamWeaveServer{
		ctx:      ctx,
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	// Cancel immediately
	cancel()

	req := &loomv1.WeaveRequest{
		Query:     "This will be cancelled",
		SessionId: "sess_cancel",
	}

	err := srv.StreamWeave(req, stream)
	if err == nil {
		t.Fatal("Expected error from context cancellation, got nil")
	}

	// Should be context cancelled error
	if err != context.Canceled {
		t.Logf("Got error: %v (expected context.Canceled, but may be wrapped)", err)
	}
}

func TestServer_StreamWeave_GeneratesSessionID(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query: "Test query",
		// No SessionId provided
	}

	err := srv.StreamWeave(req, stream)
	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	// Should still succeed and generate session ID internally
	if len(stream.messages) == 0 {
		t.Fatal("Expected progress updates")
	}

	lastMsg := stream.messages[len(stream.messages)-1]
	if lastMsg.Stage != loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
		t.Error("Expected successful completion despite missing session ID")
	}
}

func TestServer_StreamWeave_MultipleClients(t *testing.T) {
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	const numClients = 10
	done := make(chan error, numClients)

	for i := 0; i < numClients; i++ {
		go func(id int) {
			stream := &mockStreamWeaveServer{
				ctx:      context.Background(),
				messages: make([]*loomv1.WeaveProgress, 0),
			}

			req := &loomv1.WeaveRequest{
				Query:     fmt.Sprintf("Query %d", id),
				SessionId: fmt.Sprintf("sess_%d", id),
			}

			err := srv.StreamWeave(req, stream)
			done <- err
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numClients; i++ {
		err := <-done
		if err != nil {
			t.Errorf("Client %d failed: %v", i, err)
		}
	}
}

func TestServer_StreamWeave_RealProgressEvents(t *testing.T) {
	// Use standard test agent which will generate real progress events
	ag := createTestAgent()
	srv := NewServer(ag, nil)

	stream := &mockStreamWeaveServer{
		ctx:      context.Background(),
		messages: make([]*loomv1.WeaveProgress, 0),
	}

	req := &loomv1.WeaveRequest{
		Query:     "Test query for real progress",
		SessionId: "sess_real_progress",
	}

	err := srv.StreamWeave(req, stream)
	if err != nil {
		t.Fatalf("StreamWeave failed: %v", err)
	}

	// Verify we received real progress events from agent (not synthetic)
	stream.mu.Lock()
	messages := stream.messages
	stream.mu.Unlock()

	if len(messages) == 0 {
		t.Fatal("Expected progress updates, got none")
	}

	// Real agent progress should include these stages:
	// 1. PATTERN_SELECTION (emitted at start of conversation loop)
	// 2. LLM_GENERATION (emitted for each LLM call)
	// 3. COMPLETED (emitted at end)

	foundPatternSelection := false
	foundLLMGeneration := false
	foundCompletion := false

	for _, msg := range messages {
		// Verify timestamp is set (real events have timestamps)
		if msg.Timestamp == 0 {
			t.Errorf("Progress event missing timestamp: stage=%v", msg.Stage)
		}

		// Verify message is non-empty
		if msg.Message == "" {
			t.Errorf("Progress event missing message: stage=%v", msg.Stage)
		}

		switch msg.Stage {
		case loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION:
			foundPatternSelection = true
			// Progress should be low at start
			if msg.Progress > 20 {
				t.Errorf("Pattern selection progress too high: %d", msg.Progress)
			}

		case loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION:
			foundLLMGeneration = true
			// Progress should be mid-range
			if msg.Progress < 20 || msg.Progress > 90 {
				t.Errorf("LLM generation progress out of range: %d", msg.Progress)
			}

		case loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED:
			foundCompletion = true
			// Completion should be 100%
			if msg.Progress != 100 {
				t.Errorf("Completion progress should be 100, got: %d", msg.Progress)
			}
			// Should include result
			if msg.PartialResult == nil {
				t.Error("Completion event missing partial result")
			}
		}
	}

	// Verify all expected stages were present
	if !foundPatternSelection {
		t.Error("Expected PATTERN_SELECTION stage from real agent progress")
	}
	if !foundLLMGeneration {
		t.Error("Expected LLM_GENERATION stage from real agent progress")
	}
	if !foundCompletion {
		t.Error("Expected COMPLETED stage")
	}

	// Progress events should be in chronological order
	for i := 1; i < len(messages); i++ {
		if messages[i].Timestamp < messages[i-1].Timestamp {
			t.Error("Progress events not in chronological order")
		}
	}
}

// mockStreamWeaveServer implements the stream interface for testing
type mockStreamWeaveServer struct {
	ctx      context.Context
	messages []*loomv1.WeaveProgress
	mu       sync.Mutex
}

func (m *mockStreamWeaveServer) Send(progress *loomv1.WeaveProgress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, progress)
	return nil
}

func (m *mockStreamWeaveServer) Context() context.Context {
	return m.ctx
}

func (m *mockStreamWeaveServer) SendMsg(msg interface{}) error {
	return nil
}

func (m *mockStreamWeaveServer) RecvMsg(msg interface{}) error {
	return nil
}

func (m *mockStreamWeaveServer) SetHeader(md metadata.MD) error {
	return nil
}

func (m *mockStreamWeaveServer) SendHeader(md metadata.MD) error {
	return nil
}

func (m *mockStreamWeaveServer) SetTrailer(md metadata.MD) {
}
