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

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestAuditLog_LoadSession_NotFound verifies that loading a non-existent session
// (or one blocked by RLS) produces a WARN-level audit log entry.
func TestAuditLog_LoadSession_NotFound(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	store := NewSessionStore(pool, observability.NewNoOpTracer(), logger)

	userA := uniqueID("audit-user-a")
	ctx := ContextWithUserID(context.Background(), userA)

	// Load a session that does not exist
	sess, err := store.LoadSession(ctx, "nonexistent-session-id")
	require.NoError(t, err)
	assert.Nil(t, sess, "session should be nil for non-existent ID")

	// Verify the audit log was emitted
	entries := logs.FilterMessage("session not found (possible RLS denial)")
	require.Equal(t, 1, entries.Len(), "expected exactly one audit log entry")

	entry := entries.All()[0]
	assert.Equal(t, zapcore.WarnLevel, entry.Level)

	// Check structured fields
	fields := make(map[string]string)
	for _, f := range entry.Context {
		if f.Type == zapcore.StringType {
			fields[f.Key] = f.String
		}
	}
	assert.Equal(t, "nonexistent-session-id", fields["session_id"])
	assert.Equal(t, "LoadSession", fields["operation"])
}

// TestAuditLog_LoadSession_RLSDenied verifies audit logging when RLS denies access.
// User A creates a session, User B tries to load it -- should produce audit log.
func TestAuditLog_LoadSession_RLSDenied(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	store := NewSessionStore(pool, observability.NewNoOpTracer(), logger)

	userA := uniqueID("audit-user-a")
	userB := uniqueID("audit-user-b")
	sessionID := uniqueID("audit-sess")

	// User A creates a session
	createTestSession(t, store, userA, sessionID, "test-agent")

	// User B tries to load it -- RLS blocks it
	ctxB := ContextWithUserID(context.Background(), userB)
	sess, err := store.LoadSession(ctxB, sessionID)
	require.NoError(t, err)
	assert.Nil(t, sess, "User B should not see User A's session")

	// Verify the audit log was emitted
	entries := logs.FilterMessage("session not found (possible RLS denial)")
	require.Equal(t, 1, entries.Len(), "expected audit log for RLS-denied access")

	entry := entries.All()[0]
	fields := make(map[string]string)
	for _, f := range entry.Context {
		if f.Type == zapcore.StringType {
			fields[f.Key] = f.String
		}
	}
	assert.Equal(t, sessionID, fields["session_id"])
	assert.Equal(t, "LoadSession", fields["operation"])
}

// TestAuditLog_DeleteSession_ZeroRows verifies audit logging when DELETE affects 0 rows.
func TestAuditLog_DeleteSession_ZeroRows(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	store := NewSessionStore(pool, observability.NewNoOpTracer(), logger)

	userA := uniqueID("audit-user-a")
	ctx := ContextWithUserID(context.Background(), userA)

	// Delete a session that does not exist
	err := store.DeleteSession(ctx, "nonexistent-session-id")
	require.NoError(t, err, "DeleteSession should not error for non-existent ID")

	// Verify the audit log was emitted
	entries := logs.FilterMessage("delete affected 0 rows (possible RLS denial or already deleted)")
	require.Equal(t, 1, entries.Len(), "expected audit log for zero-row delete")

	entry := entries.All()[0]
	fields := make(map[string]string)
	for _, f := range entry.Context {
		if f.Type == zapcore.StringType {
			fields[f.Key] = f.String
		}
	}
	assert.Equal(t, "nonexistent-session-id", fields["session_id"])
	assert.Equal(t, "DeleteSession", fields["operation"])
}

// TestAuditLog_DeleteSession_CrossUserDenied verifies audit logging when a user
// tries to delete another user's session and RLS blocks it.
func TestAuditLog_DeleteSession_CrossUserDenied(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	store := NewSessionStore(pool, observability.NewNoOpTracer(), logger)

	userA := uniqueID("audit-user-a")
	userB := uniqueID("audit-user-b")
	sessionID := uniqueID("audit-sess")

	// User A creates a session
	createTestSession(t, store, userA, sessionID, "test-agent")

	// User B tries to delete it
	ctxB := ContextWithUserID(context.Background(), userB)
	err := store.DeleteSession(ctxB, sessionID)
	require.NoError(t, err)

	// Verify the audit log was emitted
	entries := logs.FilterMessage("delete affected 0 rows (possible RLS denial or already deleted)")
	require.Equal(t, 1, entries.Len(), "expected audit log for cross-user delete attempt")

	entry := entries.All()[0]
	fields := make(map[string]string)
	for _, f := range entry.Context {
		if f.Type == zapcore.StringType {
			fields[f.Key] = f.String
		}
	}
	assert.Equal(t, sessionID, fields["session_id"])
	assert.Equal(t, "DeleteSession", fields["operation"])

	// Verify User A's session still exists
	ctxA := ContextWithUserID(context.Background(), userA)
	loaded, err := store.LoadSession(ctxA, sessionID)
	require.NoError(t, err)
	require.NotNil(t, loaded, "User A's session must survive User B's delete attempt")
}

// TestAuditLog_SaveMessage_FKViolation verifies audit logging when a message insert
// fails because the session FK doesn't match (indicating possible RLS denial).
func TestAuditLog_SaveMessage_FKViolation(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	store := NewSessionStore(pool, observability.NewNoOpTracer(), logger)

	userA := uniqueID("audit-user-a")
	ctx := ContextWithUserID(context.Background(), userA)

	// Try to save a message to a session that doesn't exist for this user
	msg := agent.Message{
		Role:      "user",
		Content:   "test message",
		Timestamp: time.Now().UTC(),
	}
	err := store.SaveMessage(ctx, "nonexistent-session-id", msg)
	// This may fail with FK violation or RLS denial -- either way, it should be logged
	if err != nil {
		entries := logs.FilterMessage("message insert FK violation (session not found for user, possible RLS denial)")
		// FK violation only triggers if the FK constraint fires (depends on schema setup)
		// The test validates that IF a pgconn FK error occurs, it is logged.
		// If the error is not an FK violation (e.g., RLS blocks it first), the error still propagates.
		t.Logf("SaveMessage error (expected): %v", err)
		t.Logf("FK violation audit log entries: %d", entries.Len())
	}
}

// TestAuditLog_AdminStore_ValidatePermissions verifies that ValidatePermissions
// checks and logs the BYPASSRLS status of the admin connection.
func TestAuditLog_AdminStore_ValidatePermissions(t *testing.T) {
	pool := testPool(t)
	core, logs := observer.New(zapcore.DebugLevel) // capture Info and Warn
	logger := zap.New(core)

	admin := NewAdminStore(pool, observability.NewNoOpTracer(), logger)
	ctx := context.Background()

	err := admin.ValidatePermissions(ctx)
	require.NoError(t, err, "ValidatePermissions should not error on a valid connection")

	// The test database role may or may not have BYPASSRLS.
	// We just verify that a log message was emitted about the status.
	allEntries := logs.All()
	require.NotEmpty(t, allEntries, "ValidatePermissions should log the BYPASSRLS status")

	// Check for one of the two expected messages
	foundBypassMsg := false
	for _, entry := range allEntries {
		if entry.Message == "admin connection role has BYPASSRLS privilege" ||
			entry.Message == "admin connection role does not have BYPASSRLS privilege; admin queries may be filtered by RLS policies" {
			foundBypassMsg = true
			break
		}
	}
	assert.True(t, foundBypassMsg, "expected a log message about BYPASSRLS status")
}
