// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
)

func TestMergeArtifactMetadataIntoProto_disabled(t *testing.T) {
	t.Setenv("LOOM_DATA_DIR", t.TempDir())
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(false)
	t.Cleanup(func() { artifacts.SetSessionMetadataEnabled(prev) })

	p := &loomv1.Session{Id: "s1", AgentId: "mem-agent"}
	mergeArtifactMetadataIntoProto(p, "s1")
	require.Equal(t, "mem-agent", p.AgentId)
	require.Empty(t, p.MetadataStatus)
}

func TestMergeArtifactMetadataIntoProto_malformedJSONIgnored(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(true)
	t.Cleanup(func() { artifacts.SetSessionMetadataEnabled(prev) })

	dir := filepath.Join(root, "artifacts", "sessions", "bad-json")
	require.NoError(t, os.MkdirAll(dir, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, artifacts.SessionMetadataFileName), []byte("{not json"), 0640))

	p := &loomv1.Session{Id: "bad-json"}
	mergeArtifactMetadataIntoProto(p, "bad-json")
	require.Empty(t, p.MetadataStatus)
}

func TestMergeArtifactMetadataIntoProto_allowlist(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(true)
	t.Cleanup(func() { artifacts.SetSessionMetadataEnabled(prev) })

	doc := struct {
		SessionID string            `json:"session_id"`
		Status    string            `json:"status,omitempty"`
		StartedAt string            `json:"started_at"`
		Context   map[string]string `json:"context,omitempty"`
	}{
		SessionID: "allow1",
		Status:    "active",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Context:   map[string]string{"user_id": "u1", "project_id": "p1", "evil": "x"},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	dir := filepath.Join(root, "artifacts", "sessions", "allow1")
	require.NoError(t, os.MkdirAll(dir, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, artifacts.SessionMetadataFileName), raw, 0640))

	p := &loomv1.Session{Id: "allow1"}
	mergeArtifactMetadataIntoProto(p, "allow1")
	require.NotNil(t, p.Metadata)
	require.Equal(t, "u1", p.Metadata["user_id"])
	require.Equal(t, "p1", p.Metadata["project_id"])
	require.Empty(t, p.Metadata["evil"])
}

func TestMergeArtifactMetadataIntoProto_missingFileWithFiltersPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(true)
	t.Cleanup(func() { artifacts.SetSessionMetadataEnabled(prev) })

	now := time.Now().UTC().Truncate(time.Second)
	s := &agent.Session{
		ID: "no-disk", CreatedAt: now, UpdatedAt: now,
		Context: map[string]interface{}{"project_id": "from-ctx"},
	}
	req := &loomv1.ListSessionsRequest{ProjectId: "from-ctx"}
	enrich := listSessionsNeedsArtifactDisk(req)
	require.True(t, enrich)
	p := convertSession(s, enrich)
	require.Equal(t, "from-ctx", p.Metadata["project_id"])
}

func TestConvertSession_metadataStatusWhenEnrichOff(t *testing.T) {
	prev := artifacts.SessionMetadataEnabled()
	artifacts.SetSessionMetadataEnabled(true)
	t.Cleanup(func() { artifacts.SetSessionMetadataEnabled(prev) })

	now := time.Now().UTC().Truncate(time.Second)
	s := &agent.Session{ID: "x", CreatedAt: now, UpdatedAt: now}
	p := convertSession(s, false)
	require.Empty(t, p.MetadataStatus)
}
