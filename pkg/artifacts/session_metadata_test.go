// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/types"
)

func enableSessionMetadataForTest(t *testing.T) {
	t.Helper()
	prev := SessionMetadataEnabled()
	SetSessionMetadataEnabled(true)
	t.Cleanup(func() { SetSessionMetadataEnabled(prev) })
}

func TestBuildSessionArtifactMetadata_minimal(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 3, 5, 10, 30, 0, 0, time.UTC)
	s := &types.Session{
		ID:        "sess_abc123",
		AgentID:   "agent-guid-1",
		CreatedAt: start,
	}
	meta, err := BuildSessionArtifactMetadata(s)
	require.NoError(t, err)
	require.Equal(t, "sess_abc123", meta.SessionID)
	require.Equal(t, "agent-guid-1", meta.AgentID)
	require.Equal(t, "2026-03-05T10:30:00Z", meta.StartedAt)
	require.Equal(t, "active", meta.Status)
	require.Empty(t, meta.AgentName)
}

func TestBuildSessionArtifactMetadata_contextAndName(t *testing.T) {
	t.Parallel()
	start := time.Now().UTC().Truncate(time.Second)
	s := &types.Session{
		ID:        "sess_x",
		AgentID:   "a1",
		UserID:    "user@acme.com",
		Context:   map[string]interface{}{"agent_name": "guide", "project_id": "p-1", "conversation_id": "c-9"},
		CreatedAt: start,
	}
	meta, err := BuildSessionArtifactMetadata(s)
	require.NoError(t, err)
	require.Equal(t, "guide", meta.AgentName)
	require.Equal(t, "user@acme.com", meta.Context["user_id"])
	require.Equal(t, "p-1", meta.Context["project_id"])
	require.Equal(t, "c-9", meta.Context["conversation_id"])
}

func TestWriteAndReadSessionArtifactMetadata_roundTrip(t *testing.T) {
	enableSessionMetadataForTest(t)
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)

	start := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	s := &types.Session{
		ID:        "sess_rt",
		AgentID:   "ag1",
		CreatedAt: start,
		Context:   map[string]interface{}{"agentName": "Weaver"},
	}
	require.NoError(t, SyncSessionArtifactMetadata(context.Background(), s))

	path := filepath.Join(root, "artifacts", "sessions", "sess_rt", SessionMetadataFileName)
	_, err := os.Stat(path)
	require.NoError(t, err)

	meta, err := ReadSessionArtifactMetadata("sess_rt")
	require.NoError(t, err)
	require.Equal(t, "sess_rt", meta.SessionID)
	require.Equal(t, "ag1", meta.AgentID)
	require.Equal(t, "Weaver", meta.AgentName)
	require.Equal(t, "active", meta.Status)
}

func TestSessionArtifactsRoot_rejectsBadPath(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"../x", "a/b", `a\b`, "ok"} {
		_, err := SessionArtifactsRoot(id)
		switch id {
		case "ok":
			require.NoError(t, err)
		default:
			require.Error(t, err)
		}
	}
}

func TestSyncSessionArtifactMetadata_preservesCompleted(t *testing.T) {
	enableSessionMetadataForTest(t)
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)

	start := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	s := &types.Session{
		ID:        "sess_persist",
		AgentID:   "ag1",
		CreatedAt: start,
	}
	require.NoError(t, SyncSessionArtifactMetadata(context.Background(), s))
	require.NoError(t, CompleteSessionArtifactMetadata("sess_persist"))

	meta, err := ReadSessionArtifactMetadata("sess_persist")
	require.NoError(t, err)
	require.Equal(t, "completed", meta.Status)

	// Simulate another SaveSession: sync must not flip back to active.
	s.AgentID = "ag2"
	require.NoError(t, SyncSessionArtifactMetadata(context.Background(), s))
	meta, err = ReadSessionArtifactMetadata("sess_persist")
	require.NoError(t, err)
	require.Equal(t, "completed", meta.Status)
	require.Equal(t, "ag2", meta.AgentID)
}

func TestFilterPublicArtifactContext(t *testing.T) {
	t.Parallel()
	in := map[string]string{
		"user_id": "u1", "project_id": "p1", "conversation_id": "c1",
		"evil": "token", "password": "x",
	}
	out := FilterPublicArtifactContext(in)
	require.Len(t, out, 3)
	require.Equal(t, "u1", out["user_id"])
	require.Equal(t, "p1", out["project_id"])
	require.Equal(t, "c1", out["conversation_id"])
	require.Empty(t, FilterPublicArtifactContext(map[string]string{"evil": "only"}))
}

func TestCompleteSessionArtifactMetadata(t *testing.T) {
	enableSessionMetadataForTest(t)
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)

	start := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	s := &types.Session{
		ID:        "sess_done",
		AgentID:   "ag1",
		CreatedAt: start,
	}
	require.NoError(t, SyncSessionArtifactMetadata(context.Background(), s))

	require.NoError(t, CompleteSessionArtifactMetadata("sess_done"))

	meta, err := ReadSessionArtifactMetadata("sess_done")
	require.NoError(t, err)
	require.Equal(t, "completed", meta.Status)
	require.NotEmpty(t, meta.EndedAt)
}

func TestSyncSessionArtifactMetadata_disabledNoOp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)
	prev := SessionMetadataEnabled()
	SetSessionMetadataEnabled(false)
	t.Cleanup(func() { SetSessionMetadataEnabled(prev) })

	s := &types.Session{
		ID:        "off",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, SyncSessionArtifactMetadata(context.Background(), s))
	_, err := os.Stat(filepath.Join(root, "artifacts", "sessions", "off", SessionMetadataFileName))
	require.True(t, errors.Is(err, os.ErrNotExist))
}
