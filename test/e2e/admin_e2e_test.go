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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_Admin_ListAllSessions verifies that the AdminService can see sessions
// from multiple users. Skipped unless backend is PostgreSQL.
func TestE2E_Admin_ListAllSessions(t *testing.T) {
	if !isPostgres() {
		t.Skip("admin e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	loom := loomClient(t)
	admin := adminClient(t)

	userA := uniqueTestID("admin-user-a")
	userB := uniqueTestID("admin-user-b")

	ctxA := withUserID(context.Background(), userA)
	ctxB := withUserID(context.Background(), userB)

	// User A creates a session
	respA, err := loom.CreateSession(ctxA, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("admin-sess-a"),
	})
	require.NoError(t, err)
	sessionA := respA.GetId()
	cleanupSession(t, loom, userA, sessionA)

	// User B creates a session
	respB, err := loom.CreateSession(ctxB, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("admin-sess-b"),
	})
	require.NoError(t, err)
	sessionB := respB.GetId()
	cleanupSession(t, loom, userB, sessionB)

	// Admin lists all sessions (bypasses RLS)
	ctx := context.Background()
	allResp, err := admin.ListAllSessions(ctx, &loomv1.ListAllSessionsRequest{
		Limit: 1000,
	})
	require.NoError(t, err, "AdminService.ListAllSessions should succeed")
	require.GreaterOrEqual(t, allResp.GetTotalCount(), int32(2),
		"admin should see at least 2 sessions")

	foundA := false
	foundB := false
	for _, s := range allResp.GetSessions() {
		if s.GetId() == sessionA {
			foundA = true
		}
		if s.GetId() == sessionB {
			foundB = true
		}
	}
	assert.True(t, foundA, "admin should see User A's session")
	assert.True(t, foundB, "admin should see User B's session")
}

// TestE2E_Admin_CountSessionsByUser verifies that the AdminService returns
// accurate per-user session counts.
func TestE2E_Admin_CountSessionsByUser(t *testing.T) {
	if !isPostgres() {
		t.Skip("admin e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	loom := loomClient(t)
	admin := adminClient(t)

	userA := uniqueTestID("count-user-a")
	userB := uniqueTestID("count-user-b")

	ctxA := withUserID(context.Background(), userA)
	ctxB := withUserID(context.Background(), userB)

	// User A creates 2 sessions
	for i := 0; i < 2; i++ {
		resp, err := loom.CreateSession(ctxA, &loomv1.CreateSessionRequest{
			Name: uniqueTestID("count-a"),
		})
		require.NoError(t, err)
		cleanupSession(t, loom, userA, resp.GetId())
	}

	// User B creates 1 session
	respB, err := loom.CreateSession(ctxB, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("count-b"),
	})
	require.NoError(t, err)
	cleanupSession(t, loom, userB, respB.GetId())

	// Admin counts sessions by user
	ctx := context.Background()
	countResp, err := admin.CountSessionsByUser(ctx, &loomv1.CountSessionsByUserRequest{})
	require.NoError(t, err, "AdminService.CountSessionsByUser should succeed")

	counts := countResp.GetUserCounts()
	require.NotNil(t, counts, "user_counts should not be nil")

	assert.GreaterOrEqual(t, counts[userA], int32(2),
		"User A should have at least 2 sessions")
	assert.GreaterOrEqual(t, counts[userB], int32(1),
		"User B should have at least 1 session")

	t.Logf("Session counts: userA=%d userB=%d", counts[userA], counts[userB])
}

// TestE2E_Admin_GetSystemStats verifies that AdminService.GetSystemStats returns
// aggregate statistics across all users.
func TestE2E_Admin_GetSystemStats(t *testing.T) {
	if !isPostgres() {
		t.Skip("admin e2e tests only run against PostgreSQL (LOOM_E2E_BACKEND=postgres)")
	}

	loom := loomClient(t)
	admin := adminClient(t)

	// Create a session with a distinct user to ensure stats include at least 1 user
	userID := uniqueTestID("stats-user")
	ctx := withUserID(context.Background(), userID)

	resp, err := loom.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("stats-sess"),
	})
	require.NoError(t, err)
	cleanupSession(t, loom, userID, resp.GetId())

	// Get system stats
	statsResp, err := admin.GetSystemStats(context.Background(), &loomv1.GetSystemStatsRequest{})
	require.NoError(t, err, "AdminService.GetSystemStats should succeed")

	assert.GreaterOrEqual(t, statsResp.GetTotalSessions(), int32(1),
		"total_sessions should be at least 1")
	assert.GreaterOrEqual(t, statsResp.GetTotalUsers(), int32(1),
		"total_users should be at least 1")

	t.Logf("System stats: sessions=%d messages=%d tool_execs=%d users=%d cost=$%.4f tokens=%d",
		statsResp.GetTotalSessions(), statsResp.GetTotalMessages(),
		statsResp.GetTotalToolExecutions(), statsResp.GetTotalUsers(),
		statsResp.GetTotalCostUsd(), statsResp.GetTotalTokens())
}
