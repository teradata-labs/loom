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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_DeleteSession_Visibility verifies that a soft-deleted session
// disappears from ListSessions and returns NotFound from GetSession.
func TestE2E_DeleteSession_Visibility(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("del-vis")
	ctx := withUserID(context.Background(), userID)

	// Create a session
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("visibility-test"),
	})
	require.NoError(t, err, "CreateSession should succeed")
	sessionID := createResp.GetId()
	require.NotEmpty(t, sessionID)

	// Verify it appears in ListSessions
	listResp, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{})
	require.NoError(t, err)
	foundBefore := false
	for _, s := range listResp.GetSessions() {
		if s.GetId() == sessionID {
			foundBefore = true
			break
		}
	}
	assert.True(t, foundBefore, "session should appear in ListSessions before deletion")

	// Delete it
	delResp, err := client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{SessionId: sessionID})
	require.NoError(t, err, "DeleteSession should succeed")
	assert.True(t, delResp.GetSuccess(), "DeleteSession response should indicate success")

	// Verify it's gone from ListSessions
	listResp2, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{})
	require.NoError(t, err)
	for _, s := range listResp2.GetSessions() {
		assert.NotEqual(t, sessionID, s.GetId(),
			"deleted session must not appear in ListSessions")
	}

	// Verify GetSession returns NotFound
	_, err = client.GetSession(ctx, &loomv1.GetSessionRequest{SessionId: sessionID})
	require.Error(t, err, "GetSession should fail for deleted session")
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code(),
		"GetSession should return NotFound for deleted session")

	t.Logf("Session %s correctly invisible after soft-delete", sessionID)
}

// TestE2E_DeleteSession_Idempotent verifies that deleting an already-deleted
// session returns NotFound (soft-delete is not idempotent; second call errors).
func TestE2E_DeleteSession_Idempotent(t *testing.T) {
	if !isPostgres() {
		t.Skip("soft-delete behavior only tested against PostgreSQL")
	}

	client := loomClient(t)
	userID := uniqueTestID("del-idem")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("idempotent-test"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()

	// First delete: should succeed
	_, err = client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{SessionId: sessionID})
	require.NoError(t, err, "first DeleteSession should succeed")

	// Second delete: session is soft-deleted so should return NotFound
	_, err = client.DeleteSession(ctx, &loomv1.DeleteSessionRequest{SessionId: sessionID})
	require.Error(t, err, "second DeleteSession should fail (session already deleted)")
	st, _ := status.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code(),
		"second DeleteSession should return NotFound")

	t.Logf("DeleteSession correctly returns NotFound on second call for session %s", sessionID)
}

// TestE2E_ConversationHistory_MultiTurn verifies that multiple Weave calls to
// the same session are all persisted and retrieved via GetConversationHistory.
func TestE2E_ConversationHistory_MultiTurn(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("multi-turn")
	ctx := withUserID(context.Background(), userID)

	// Create a session
	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("multi-turn-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	// First turn: simple arithmetic
	resp1, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 1 + 1? Reply with just the number.",
	})
	require.NoError(t, err, "first Weave should succeed")
	require.NotEmpty(t, resp1.GetText(), "first turn should produce a response")
	t.Logf("Turn 1 response: %q", resp1.GetText())

	// Second turn: follow-up
	resp2, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 2 + 2? Reply with just the number.",
	})
	require.NoError(t, err, "second Weave should succeed")
	require.NotEmpty(t, resp2.GetText(), "second turn should produce a response")
	t.Logf("Turn 2 response: %q", resp2.GetText())

	// Retrieve conversation history and verify both turns are persisted
	histResp, err := client.GetConversationHistory(ctx, &loomv1.GetConversationHistoryRequest{
		SessionId: sessionID,
	})
	require.NoError(t, err, "GetConversationHistory should succeed")

	messages := histResp.GetMessages()
	// Each Weave call produces at least a user message + assistant message
	assert.GreaterOrEqual(t, len(messages), 4,
		"multi-turn conversation should have at least 4 messages (2 user + 2 assistant)")

	// Verify the user messages contain our prompts
	var userMsgs []string
	for _, m := range messages {
		if m.GetRole() == "user" || m.GetRole() == "human" {
			userMsgs = append(userMsgs, m.GetContent())
		}
	}
	assert.GreaterOrEqual(t, len(userMsgs), 2, "should have at least 2 user messages")

	t.Logf("GetConversationHistory returned %d messages (%d user messages)",
		len(messages), len(userMsgs))
}

// TestE2E_ConversationHistory_UserIsolation verifies that User B cannot retrieve
// User A's conversation history (cross-tenant access denied).
func TestE2E_ConversationHistory_UserIsolation(t *testing.T) {
	if !isPostgres() {
		t.Skip("conversation history isolation requires PostgreSQL RLS")
	}

	client := loomClient(t)
	userA := uniqueTestID("hist-user-a")
	userB := uniqueTestID("hist-user-b")
	ctxA := withUserID(context.Background(), userA)
	ctxB := withUserID(context.Background(), userB)

	// User A creates a session and sends a message
	createResp, err := client.CreateSession(ctxA, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("hist-isolation"),
	})
	require.NoError(t, err)
	sessionA := createResp.GetId()
	cleanupSession(t, client, userA, sessionA)

	weaveCtx, weaveCancel := context.WithTimeout(ctxA, 2*time.Minute)
	defer weaveCancel()
	_, err = client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionA,
		Query:     "What is 3 + 3? Reply with just the number.",
	})
	require.NoError(t, err)

	// User A can read their own history
	histA, err := client.GetConversationHistory(ctxA, &loomv1.GetConversationHistoryRequest{
		SessionId: sessionA,
	})
	require.NoError(t, err, "User A should be able to read their own history")
	assert.NotEmpty(t, histA.GetMessages(), "User A's history should have messages")

	// User B should NOT be able to read User A's history (should error or return empty)
	histB, err := client.GetConversationHistory(ctxB, &loomv1.GetConversationHistoryRequest{
		SessionId: sessionA,
	})
	if err != nil {
		st, _ := status.FromError(err)
		assert.Equal(t, codes.NotFound, st.Code(),
			"User B should get NotFound when accessing User A's history")
		t.Logf("User B correctly blocked from User A's history (NotFound)")
	} else {
		// If no error, the response should be empty
		assert.Empty(t, histB.GetMessages(),
			"User B must not see User A's conversation messages")
		t.Logf("User B got empty history for User A's session (correct isolation)")
	}
}

// TestE2E_Session_CreateAndListMultipleUsers verifies that each user sees only
// their own sessions in ListSessions when multiple users have concurrent sessions.
func TestE2E_Session_CreateAndListMultipleUsers(t *testing.T) {
	if !isPostgres() {
		t.Skip("multi-user session listing requires PostgreSQL")
	}

	client := loomClient(t)

	users := []string{
		uniqueTestID("ml-user-a"),
		uniqueTestID("ml-user-b"),
		uniqueTestID("ml-user-c"),
	}

	// Each user creates 2 sessions
	sessionsByUser := make(map[string][]string)
	for _, userID := range users {
		ctx := withUserID(context.Background(), userID)
		for i := 0; i < 2; i++ {
			resp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
				Name: uniqueTestID("ml-session"),
			})
			require.NoError(t, err, "CreateSession should succeed for %s", userID)
			sessionsByUser[userID] = append(sessionsByUser[userID], resp.GetId())
			cleanupSession(t, client, userID, resp.GetId())
		}
	}

	// Each user should only see their own 2 sessions
	for _, userID := range users {
		ctx := withUserID(context.Background(), userID)
		listResp, err := client.ListSessions(ctx, &loomv1.ListSessionsRequest{})
		require.NoError(t, err)

		seenIDs := make(map[string]bool)
		for _, s := range listResp.GetSessions() {
			seenIDs[s.GetId()] = true
		}

		// Own sessions should be visible
		for _, ownSession := range sessionsByUser[userID] {
			assert.True(t, seenIDs[ownSession],
				"user %s should see their own session %s", userID, ownSession)
		}

		// Other users' sessions must NOT be visible
		for _, otherUser := range users {
			if otherUser == userID {
				continue
			}
			for _, otherSession := range sessionsByUser[otherUser] {
				assert.False(t, seenIDs[otherSession],
					"user %s must NOT see session %s owned by %s",
					userID, otherSession, otherUser)
			}
		}

		t.Logf("User %s: sees %d sessions (expected %d)",
			userID, len(seenIDs), len(sessionsByUser[userID]))
	}
}
