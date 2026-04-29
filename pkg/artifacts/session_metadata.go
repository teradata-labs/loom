// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package artifacts provides session artifact directory layout, optional on-disk session
// attribution in metadata.json, and helpers to read or update that file safely.
package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/types"
)

// sessionMetadataEnabled gates all metadata.json reads/writes from the API and stores.
// Default is off; looms sets this from artifacts.session_metadata_enabled / LOOM_ARTIFACTS_SESSION_METADATA_ENABLED.
var sessionMetadataEnabled atomic.Bool

// SessionMetadataEnabled reports whether session artifact metadata.json integration is enabled.
// When false, [SyncSessionArtifactMetadata] and [CompleteSessionArtifactMetadata] are no-ops and
// callers that gate on this flag skip merging disk metadata into API responses.
func SessionMetadataEnabled() bool {
	return sessionMetadataEnabled.Load()
}

// SetSessionMetadataEnabled sets the process-wide metadata.json feature flag.
// The looms binary sets this from artifacts.session_metadata_enabled (or env
// LOOM_ARTIFACTS_SESSION_METADATA_ENABLED) during serve startup. The value is safe to read
// concurrently; typical usage is set once at startup rather than toggling at runtime.
func SetSessionMetadataEnabled(v bool) {
	sessionMetadataEnabled.Store(v)
}

// sessionMetaLocks serializes read-modify-write per session for metadata.json.
var sessionMetaLocks sync.Map // sessionID -> *sync.Mutex

func withSessionMetadataLock(sessionID string, fn func() error) error {
	if sessionID == "" {
		return fn()
	}
	v, _ := sessionMetaLocks.LoadOrStore(sessionID, new(sync.Mutex))
	m := v.(*sync.Mutex)
	m.Lock()
	defer m.Unlock()
	return fn()
}

// SessionMetadataFileName is the JSON file stored at the session artifact root.
// See https://github.com/teradata-labs/loom/issues/111
const SessionMetadataFileName = "metadata.json"

// SessionArtifactMetadata is persisted next to agent/, user/, and scratchpad/ under
// $LOOM_DATA_DIR/artifacts/sessions/<session_id>/.
type SessionArtifactMetadata struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	StartedAt string `json:"started_at"` // RFC3339 UTC
	EndedAt   string `json:"ended_at,omitempty"`
	Status    string `json:"status,omitempty"`
	// Context holds non-sensitive attribution keys only (IDs, not secrets).
	Context map[string]string `json:"context,omitempty"`
	// Artifacts is optional; populated when callers compute counts (e.g. future work).
	Artifacts *SessionArtifactStats `json:"artifacts,omitempty"`
}

// SessionArtifactStats summarizes indexed artifacts for a session (optional).
type SessionArtifactStats struct {
	Created        int   `json:"created"`
	TotalSizeBytes int64 `json:"total_size_bytes"`
}

// publicArtifactContextKeys are the only keys allowed in metadata.json "context" for API exposure.
// Keep in sync with BuildSessionArtifactMetadata.
var publicArtifactContextKeys = map[string]struct{}{
	"user_id": {}, "project_id": {}, "conversation_id": {},
}

// FilterPublicArtifactContext returns a copy of m containing only allowlisted non-empty keys.
// Used when merging on-disk metadata into API responses and when normalizing metadata before write.
func FilterPublicArtifactContext(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, v := range m {
		if v == "" {
			continue
		}
		if _, ok := publicArtifactContextKeys[k]; !ok {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateSessionArtifactPathSegment rejects values that could escape the session directory.
func validateSessionArtifactPathSegment(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	if strings.Contains(sessionID, "..") {
		return fmt.Errorf("invalid session ID")
	}
	if strings.ContainsAny(sessionID, "/\\") {
		return fmt.Errorf("invalid session ID")
	}
	return nil
}

// SessionArtifactsRoot returns $LOOM_DATA_DIR/artifacts/sessions/<sessionID>.
func SessionArtifactsRoot(sessionID string) (string, error) {
	if err := validateSessionArtifactPathSegment(sessionID); err != nil {
		return "", err
	}
	base := config.GetLoomDataDir()
	return filepath.Join(base, "artifacts", "sessions", sessionID), nil
}

// sessionMetadataPath returns the path to metadata.json for a session.
func sessionMetadataPath(sessionID string) (string, error) {
	root, err := SessionArtifactsRoot(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, SessionMetadataFileName), nil
}

// BuildSessionArtifactMetadata maps a conversation session into filesystem metadata.
// Only whitelisted context keys are copied to avoid persisting secrets from Context.
// Status is initialized to "active" for live sessions; SyncSessionArtifactMetadata preserves
// an existing on-disk "completed" status and ended_at so completed sessions are not resurrected.
func BuildSessionArtifactMetadata(session *types.Session) (*SessionArtifactMetadata, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.ID == "" {
		return nil, fmt.Errorf("session ID is empty")
	}
	meta := &SessionArtifactMetadata{
		SessionID: session.ID,
		AgentID:   session.AgentID,
		StartedAt: session.CreatedAt.UTC().Format(time.RFC3339),
		Status:    "active",
	}
	meta.AgentName = AgentNameFromSession(session)
	ctxOut := make(map[string]string)
	if session.UserID != "" {
		ctxOut["user_id"] = session.UserID
	}
	for _, key := range []string{"project_id", "conversation_id"} {
		if v, ok := StringFromContext(session.Context, key); ok {
			ctxOut[key] = v
		}
	}
	if len(ctxOut) > 0 {
		meta.Context = ctxOut
	}
	return meta, nil
}

// AgentNameFromSession returns a display name from session context keys agent_name / agentName.
func AgentNameFromSession(session *types.Session) string {
	if session == nil {
		return ""
	}
	if n, ok := StringFromContext(session.Context, "agent_name"); ok {
		return n
	}
	if n, ok := StringFromContext(session.Context, "agentName"); ok {
		return n
	}
	return ""
}

// StringFromContext returns a non-empty trimmed string for key when present.
func StringFromContext(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return "", false
	}
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", false
		}
		return s, true
	default:
		return "", false
	}
}

// WriteSessionArtifactMetadata writes metadata.json under the session artifact root.
// The session directory is created if missing. Uses atomic replace (temp + rename).
func WriteSessionArtifactMetadata(meta *SessionArtifactMetadata) error {
	if meta == nil {
		return fmt.Errorf("metadata is nil")
	}
	if meta.SessionID == "" {
		return fmt.Errorf("session_id is empty")
	}
	root, err := SessionArtifactsRoot(meta.SessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		return fmt.Errorf("create session artifact root: %w", err)
	}
	finalPath := filepath.Join(root, SessionMetadataFileName)
	tmp, err := os.CreateTemp(root, ".metadata-*.json")
	if err != nil {
		return fmt.Errorf("create temp metadata: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		cleanup()
		return fmt.Errorf("encode metadata: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync metadata temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close metadata temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename metadata file: %w", err)
	}
	return nil
}

// ReadSessionArtifactMetadata reads and parses metadata.json under the session artifact root.
// The resolved path is checked so the file cannot lie outside that directory. If the file
// is missing, the error typically wraps [os.ErrNotExist].
func ReadSessionArtifactMetadata(sessionID string) (*SessionArtifactMetadata, error) {
	path, err := sessionMetadataPath(sessionID)
	if err != nil {
		return nil, err
	}
	root, err := SessionArtifactsRoot(sessionID)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	rel, err := filepath.Rel(root, cleanPath)
	if err != nil {
		return nil, fmt.Errorf("invalid session metadata path: %w", err)
	}
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("invalid session metadata path")
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, err
	}
	var meta SessionArtifactMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	return &meta, nil
}

// SyncSessionArtifactMetadata builds metadata from session and atomically writes metadata.json.
// It is a no-op when [SessionMetadataEnabled] is false, when session is nil, or when session.ID is empty.
// If metadata.json already records status "completed", that status and ended_at are preserved
// so later saves do not resurrect an ended session; optional artifact stats are preserved.
// A per-session mutex serializes read-modify-write with [CompleteSessionArtifactMetadata].
// If ctx is non-nil, ctx.Done() is checked once before disk work; cancellation is not polled mid-write.
func SyncSessionArtifactMetadata(ctx context.Context, session *types.Session) error {
	if !SessionMetadataEnabled() {
		return nil
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if session == nil || session.ID == "" {
		return nil
	}
	return withSessionMetadataLock(session.ID, func() error {
		meta, err := BuildSessionArtifactMetadata(session)
		if err != nil {
			return err
		}
		existing, rerr := ReadSessionArtifactMetadata(session.ID)
		if rerr == nil {
			if strings.EqualFold(strings.TrimSpace(existing.Status), "completed") {
				meta.Status = existing.Status
				if existing.EndedAt != "" {
					meta.EndedAt = existing.EndedAt
				}
			}
			if existing.Artifacts != nil {
				meta.Artifacts = existing.Artifacts
			}
		}
		return WriteSessionArtifactMetadata(meta)
	})
}

// CompleteSessionArtifactMetadata sets ended_at and status to "completed" when metadata.json exists.
// It is a no-op when [SessionMetadataEnabled] is false or sessionID is empty.
// If the file is missing or unreadable, it returns nil (backward compatible). Context fields are
// rewritten through [FilterPublicArtifactContext] before save. Serialized with the same per-session
// lock as [SyncSessionArtifactMetadata].
func CompleteSessionArtifactMetadata(sessionID string) error {
	if !SessionMetadataEnabled() {
		return nil
	}
	if sessionID == "" {
		return nil
	}
	return withSessionMetadataLock(sessionID, func() error {
		meta, err := ReadSessionArtifactMetadata(sessionID)
		if err != nil {
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339)
		meta.EndedAt = now
		meta.Status = "completed"
		meta.Context = FilterPublicArtifactContext(meta.Context)
		return WriteSessionArtifactMetadata(meta)
	})
}
