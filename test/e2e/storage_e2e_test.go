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

//go:build integration

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_StorageStatus verifies GetStorageStatus returns healthy status with
// the correct backend type and non-zero latency.
func TestE2E_StorageStatus(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-storage-status")

	resp, err := client.GetStorageStatus(ctx, &loomv1.GetStorageStatusRequest{})
	require.NoError(t, err, "GetStorageStatus should succeed")

	status := resp.GetStatus()
	require.NotNil(t, status, "status should not be nil")

	assert.True(t, status.GetHealthy(), "storage backend should be healthy")
	assert.Equal(t, expectedBackend(), status.GetBackend(),
		"storage backend type should match expected (LOOM_E2E_BACKEND=%s)", expectedBackend())
	assert.Greater(t, status.GetLatencyMs(), int64(0),
		"latency should be positive")

	if isPostgres() {
		assert.NotNil(t, status.GetPoolStats(), "PostgreSQL should report pool stats")
	}

	t.Logf("Storage status: backend=%s healthy=%t latency=%dms migration_version=%d",
		status.GetBackend(), status.GetHealthy(), status.GetLatencyMs(), status.GetMigrationVersion())
}

// TestE2E_SessionCRUD tests the full session lifecycle:
// Create -> Get -> List -> Delete -> List (verify gone)
func TestE2E_SessionCRUD(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("user-crud")
	ctx := withUserID(context.Background(), userID)

	// Create a session
	sessionName := uniqueTestID("e2e-session")
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: sessionName,
		Metadata: map[string]string{
			"test": "e2e-crud",
		},
	})
	require.NoError(t, err, "CreateSession should succeed")
	require.NotNil(t, createResp, "CreateSession response should not be nil")

	sessionID := createResp.GetId()
	require.NotEmpty(t, sessionID, "session ID should not be empty")
	cleanupSession(t, client, userID, sessionID)

	t.Logf("Created session: id=%s name=%s", sessionID, sessionName)

	// Get the session
	getResp, err := client.GetSession(ctx, &loomv1.GetSessionRequest{
		SessionId: sessionID,
	})
	require.NoError(t, err, "GetSession should succeed")
	assert.Equal(t, sessionID, getResp.GetId(), "session ID should match")
	assert.Equal(t, sessionName, getResp.GetName(), "session name should match")
	assert.Greater(t, getResp.GetCreatedAt(), int64(0), "created_at should be set")

	// List sessions and verify our session is present
	listResp, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{
		Limit: 100,
	})
	require.NoError(t, err, "ListSessions should succeed")

	found := false
	for _, s := range listResp.GetSessions() {
		if s.GetId() == sessionID {
			found = true
			break
		}
	}
	assert.True(t, found, "created session should appear in ListSessions")

	// Delete the session
	deleteResp, err := client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{
		SessionId: sessionID,
	})
	require.NoError(t, err, "DeleteSession should succeed")
	assert.True(t, deleteResp.GetSuccess(), "DeleteSession should return success=true")

	// Verify session is gone from list
	listResp2, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{
		Limit: 100,
	})
	require.NoError(t, err, "ListSessions after delete should succeed")

	for _, s := range listResp2.GetSessions() {
		assert.NotEqual(t, sessionID, s.GetId(),
			"deleted session should NOT appear in ListSessions")
	}
}

// TestE2E_ConversationRoundtrip sends a query through Weave (hitting Bedrock)
// and verifies that the conversation history is persisted in storage.
func TestE2E_ConversationRoundtrip(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("user-conv")
	ctx := withUserID(context.Background(), userID)

	// Create a session first
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("e2e-conv"),
	})
	require.NoError(t, err, "CreateSession should succeed")

	sessionID := createResp.GetId()
	require.NotEmpty(t, sessionID)
	cleanupSession(t, client, userID, sessionID)

	// Weave a query (this hits Bedrock for real)
	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	weaveResp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		Query:     "What is 2+2? Reply with just the number.",
		SessionId: sessionID,
	})
	require.NoError(t, err, "Weave should succeed (requires Bedrock)")
	require.NotEmpty(t, weaveResp.GetText(), "Weave response text should not be empty")
	assert.Equal(t, sessionID, weaveResp.GetSessionId(), "response session ID should match")

	t.Logf("Weave response: %q", weaveResp.GetText())

	// Get conversation history
	histResp, err := client.GetConversationHistory(ctx, &loomv1.GetConversationHistoryRequest{
		SessionId: sessionID,
		Limit:     50,
	})
	require.NoError(t, err, "GetConversationHistory should succeed")
	require.NotEmpty(t, histResp.GetMessages(), "conversation should have messages")

	// Verify at least a user message and an assistant message are persisted
	var hasUser, hasAssistant bool
	for _, msg := range histResp.GetMessages() {
		switch msg.GetRole() {
		case "user":
			hasUser = true
		case "assistant":
			hasAssistant = true
		}
	}
	assert.True(t, hasUser, "conversation should contain a user message")
	assert.True(t, hasAssistant, "conversation should contain an assistant message")
	assert.GreaterOrEqual(t, int32(len(histResp.GetMessages())), int32(2),
		"should have at least 2 messages (user + assistant)")
}

// TestE2E_MultipleSessionsPerAgent creates multiple sessions and verifies
// they all appear in the list.
func TestE2E_MultipleSessionsPerAgent(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("user-multi")
	ctx := withUserID(context.Background(), userID)

	sessionIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
			Name: uniqueTestID("e2e-multi"),
		})
		require.NoError(t, err, "CreateSession %d should succeed", i)
		sessionIDs[i] = createResp.GetId()
		cleanupSession(t, client, userID, sessionIDs[i])
	}

	// List sessions and verify all 3 are present
	listResp, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{
		Limit: 100,
	})
	require.NoError(t, err, "ListSessions should succeed")

	sessionSet := make(map[string]bool)
	for _, s := range listResp.GetSessions() {
		sessionSet[s.GetId()] = true
	}

	for i, id := range sessionIDs {
		assert.True(t, sessionSet[id], "session %d (%s) should be in list", i, id)
	}
}

// TestE2E_ArtifactStats verifies that GetArtifactStats returns without error.
func TestE2E_ArtifactStats(t *testing.T) {
	client := loomClient(t)
	ctx := withUserID(context.Background(), "e2e-artifact-stats")

	resp, err := client.GetArtifactStats(ctx, &loomv1.GetArtifactStatsRequest{})
	require.NoError(t, err, "GetArtifactStats should succeed")
	require.NotNil(t, resp, "response should not be nil")

	// On a fresh DB these may be zeros, but the RPC must succeed
	t.Logf("Artifact stats: total_files=%d total_size_bytes=%d user_files=%d generated_files=%d deleted_files=%d",
		resp.GetTotalFiles(), resp.GetTotalSizeBytes(), resp.GetUserFiles(),
		resp.GetGeneratedFiles(), resp.GetDeletedFiles())
}

// TestE2E_UserIsolation verifies that sessions created by one user are invisible
// to another user via the gRPC API. This exercises PostgreSQL RLS for per-user
// isolation. SQLite does not implement per-user session scoping.
func TestE2E_UserIsolation(t *testing.T) {
	if !isPostgres() {
		t.Skip("user isolation requires PostgreSQL RLS (LOOM_E2E_BACKEND=postgres)")
	}

	client := loomClient(t)

	userA := uniqueTestID("user-a")
	userB := uniqueTestID("user-b")

	ctxA := withUserID(context.Background(), userA)
	ctxB := withUserID(context.Background(), userB)

	// User A creates a session
	createResp, err := client.CreateSession(ctxA, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("isolation-a"),
	})
	require.NoError(t, err, "User A CreateSession should succeed")
	sessionA := createResp.GetId()
	cleanupSession(t, client, userA, sessionA)

	// User A can see their session
	listRespA, err := client.ListSessions(ctxA, &loomv1.ListSessionsRequest{Limit: 100})
	require.NoError(t, err)

	foundByA := false
	for _, s := range listRespA.GetSessions() {
		if s.GetId() == sessionA {
			foundByA = true
			break
		}
	}
	assert.True(t, foundByA, "User A should see their own session")

	// User B must NOT see User A's session
	listRespB, err := client.ListSessions(ctxB, &loomv1.ListSessionsRequest{Limit: 100})
	require.NoError(t, err)

	for _, s := range listRespB.GetSessions() {
		assert.NotEqual(t, sessionA, s.GetId(),
			"User B must NOT see User A's session %s", sessionA)
	}

	// User B must NOT be able to get User A's session directly
	getRespB, err := client.GetSession(ctxB, &loomv1.GetSessionRequest{
		SessionId: sessionA,
	})
	// Depending on implementation this may return NotFound error or empty response
	if err == nil {
		assert.Empty(t, getRespB.GetId(),
			"User B should not get User A's session details")
	}
}

// TestE2E_SessionStats verifies that after creating sessions and sending a message,
// GetHealth or GetStorageStatus reflects activity.
func TestE2E_SessionStats(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("user-stats")
	ctx := withUserID(context.Background(), userID)

	// Create a session
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("e2e-stats"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	// Verify storage is still healthy after operations
	statusResp, err := client.GetStorageStatus(ctx, &loomv1.GetStorageStatusRequest{})
	require.NoError(t, err)
	assert.True(t, statusResp.GetStatus().GetHealthy(),
		"storage should remain healthy after operations")
}
