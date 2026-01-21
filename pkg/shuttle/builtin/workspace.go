// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// WorkspaceTool provides unified session-scoped file management for agents.
// Handles both artifacts (indexed, searchable) and scratchpad (ephemeral notes).
type WorkspaceTool struct {
	artifactStore artifacts.ArtifactStore
}

// NewWorkspaceTool creates a new workspace tool.
func NewWorkspaceTool(artifactStore artifacts.ArtifactStore) *WorkspaceTool {
	return &WorkspaceTool{
		artifactStore: artifactStore,
	}
}

func (t *WorkspaceTool) Name() string {
	return "workspace"
}

func (t *WorkspaceTool) Backend() string {
	return "" // Backend-agnostic
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/workspace.yaml).
// This fallback is used only when prompts are not configured.
func (t *WorkspaceTool) Description() string {
	return `Unified file management tool for session-scoped artifacts and scratchpad.

**Artifacts** (scope="artifact"):
- Persistent files indexed in database
- Full-text searchable with metadata
- Automatic content type detection
- Use for: data files, results, outputs, generated code

**Scratchpad** (scope="scratchpad"):
- Ephemeral notes and scratch work
- Fast filesystem-only storage (no indexing)
- Use for: draft notes, temporary calculations, work-in-progress

All files are automatically organized by session. No manual path management needed.`
}

func (t *WorkspaceTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for workspace operations",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Action to perform: write, read, list, search, delete").
				WithEnum("write", "read", "list", "search", "delete"),
			"scope": shuttle.NewStringSchema("Storage scope: artifact (indexed) or scratchpad (ephemeral)").
				WithEnum("artifact", "scratchpad").
				WithDefault("artifact"),
			"filename": shuttle.NewStringSchema("Filename (no paths needed - session handles organization)"),
			"content":  shuttle.NewStringSchema("File content (for write action)"),
			"purpose":  shuttle.NewStringSchema("Human-readable description of artifact purpose (for write action)"),
			"tags":     shuttle.NewArraySchema("Categorization tags (for write action)", shuttle.NewStringSchema("")),
			"query":    shuttle.NewStringSchema("Search query (for search action - FTS5 syntax)"),
		},
		[]string{"action"},
	)
}

func (t *WorkspaceTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	// Extract session ID from context
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		// Fallback to temp directory if no session
		sessionID = "temp"
	}

	// Extract parameters
	action, ok := params["action"].(string)
	if !ok || action == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_params",
				Message: "action parameter is required",
			},
		}, nil
	}

	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "artifact" // Default to artifacts
	}

	// Route to appropriate handler
	switch action {
	case "write":
		return t.executeWrite(ctx, sessionID, scope, params)
	case "read":
		return t.executeRead(ctx, sessionID, scope, params)
	case "list":
		return t.executeList(ctx, sessionID, scope, params)
	case "search":
		return t.executeSearch(ctx, sessionID, scope, params)
	case "delete":
		return t.executeDelete(ctx, sessionID, scope, params)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "invalid_action",
				Message: fmt.Sprintf("unknown action: %s (must be write, read, list, search, or delete)", action),
			},
		}, nil
	}
}

// executeWrite handles file writing
func (t *WorkspaceTool) executeWrite(ctx context.Context, sessionID, scope string, params map[string]interface{}) (*shuttle.Result, error) {
	filename, ok := params["filename"].(string)
	if !ok || filename == "" {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "filename parameter is required for write action", Code: "error"}}, nil
	}

	content, ok := params["content"].(string)
	if !ok {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "content parameter is required for write action", Code: "error"}}, nil
	}

	if scope == "artifact" {
		return t.writeArtifact(ctx, sessionID, filename, content, params)
	} else {
		return t.writeScratchpad(ctx, sessionID, filename, content)
	}
}

// writeArtifact writes and indexes an artifact
func (t *WorkspaceTool) writeArtifact(ctx context.Context, sessionID, filename, content string, params map[string]interface{}) (*shuttle.Result, error) {
	// Get artifact directory
	dir, err := artifacts.GetArtifactDir(sessionID, artifacts.SourceAgent)
	if err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get artifact directory: %v", err)}}, nil
	}

	// Ensure directory exists
	if err := artifacts.EnsureArtifactDir(sessionID, artifacts.SourceAgent); err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to create artifact directory: %v", err)}}, nil
	}

	// Write file
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to write file: %v", err)}}, nil
	}

	// Analyze file for metadata
	analyzer := artifacts.NewAnalyzer()
	result, err := analyzer.Analyze(path)
	if err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to analyze artifact: %v", err)}}, nil
	}

	// Extract optional parameters
	purpose, _ := params["purpose"].(string)
	tags := []string{}
	if tagsParam, ok := params["tags"].([]interface{}); ok {
		for _, tag := range tagsParam {
			if tagStr, ok := tag.(string); ok {
				tags = append(tags, tagStr)
			}
		}
	}

	// Merge inferred tags with provided tags
	allTags := append(result.Tags, tags...)

	// Create artifact metadata
	now := time.Now()
	artifact := &artifacts.Artifact{
		ID:          uuid.New().String(),
		Name:        filename,
		Path:        path,
		Source:      artifacts.SourceAgent,
		Purpose:     purpose,
		ContentType: result.ContentType,
		SizeBytes:   result.SizeBytes,
		Checksum:    result.Checksum,
		CreatedAt:   now,
		UpdatedAt:   now,
		Tags:        allTags,
		Metadata:    result.Metadata,
		SessionID:   sessionID,
	}

	// Index in database
	if err := t.artifactStore.Index(ctx, artifact); err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to index artifact: %v", err)}}, nil
	}

	return &shuttle.Result{Success: true, Data: map[string]interface{}{
		"action":       "write",
		"scope":        "artifact",
		"filename":     filename,
		"path":         path,
		"artifact_id":  artifact.ID,
		"content_type": artifact.ContentType,
		"size_bytes":   artifact.SizeBytes,
		"indexed":      true,
		"session_id":   sessionID,
	}}, nil
}

// writeScratchpad writes to ephemeral scratchpad (no indexing)
func (t *WorkspaceTool) writeScratchpad(ctx context.Context, sessionID, filename, content string) (*shuttle.Result, error) {
	// Get scratchpad directory
	dir, err := artifacts.GetScratchpadDir(sessionID)
	if err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get scratchpad directory: %v", err)}}, nil
	}

	// Ensure directory exists
	if err := artifacts.EnsureScratchpadDir(sessionID); err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to create scratchpad directory: %v", err)}}, nil
	}

	// Write file
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to write file: %v", err)}}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     "write",
			"scope":      "scratchpad",
			"filename":   filename,
			"path":       path,
			"size_bytes": len(content),
			"indexed":    false,
			"session_id": sessionID,
		},
	}, nil
}

// executeRead handles file reading
func (t *WorkspaceTool) executeRead(ctx context.Context, sessionID, scope string, params map[string]interface{}) (*shuttle.Result, error) {
	filename, ok := params["filename"].(string)
	if !ok || filename == "" {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "filename parameter is required for read action", Code: "error"}}, nil
	}

	var path string
	var err error

	if scope == "artifact" {
		// Try to get artifact from database first
		artifact, err := t.artifactStore.GetByName(ctx, filename, sessionID)
		if err == nil {
			path = artifact.Path
			// Record access
			_ = t.artifactStore.RecordAccess(ctx, artifact.ID)
		} else {
			// Fallback to filesystem
			dir, err := artifacts.GetArtifactDir(sessionID, artifacts.SourceAgent)
			if err != nil {
				return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get artifact directory: %v", err)}}, nil
			}
			path = filepath.Join(dir, filename)
		}
	} else {
		// Scratchpad - filesystem only
		dir, err := artifacts.GetScratchpadDir(sessionID)
		if err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get scratchpad directory: %v", err)}}, nil
		}
		path = filepath.Join(dir, filename)
	}

	// Read file
	// #nosec G304 - path is constructed from validated session directories (GetArtifactDir/GetScratchpadDir)
	// and constrained to LOOM_DATA_DIR boundaries. No user-controlled path traversal possible.
	content, err := os.ReadFile(path)
	if err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to read file: %v", err)}}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     "read",
			"scope":      scope,
			"filename":   filename,
			"content":    string(content),
			"size_bytes": len(content),
			"session_id": sessionID,
		},
	}, nil
}

// executeList handles listing files
func (t *WorkspaceTool) executeList(ctx context.Context, sessionID, scope string, params map[string]interface{}) (*shuttle.Result, error) {
	if scope == "artifact" {
		// List indexed artifacts
		filter := &artifacts.Filter{
			SessionID: &sessionID,
			Limit:     100, // Reasonable default
		}

		artifactList, err := t.artifactStore.List(ctx, filter)
		if err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to list artifacts: %v", err)}}, nil
		}

		results := make([]map[string]interface{}, len(artifactList))
		for i, art := range artifactList {
			results[i] = map[string]interface{}{
				"filename":     art.Name,
				"artifact_id":  art.ID,
				"content_type": art.ContentType,
				"size_bytes":   art.SizeBytes,
				"created_at":   art.CreatedAt.Format(time.RFC3339),
				"purpose":      art.Purpose,
				"tags":         art.Tags,
			}
		}

		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"action":     "list",
				"scope":      "artifact",
				"count":      len(results),
				"artifacts":  results,
				"session_id": sessionID,
			},
		}, nil
	} else {
		// List scratchpad files (filesystem only)
		dir, err := artifacts.GetScratchpadDir(sessionID)
		if err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get scratchpad directory: %v", err)}}, nil
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			// Directory might not exist yet
			if os.IsNotExist(err) {
				return &shuttle.Result{Success: true, Data: map[string]interface{}{
					"action":     "list",
					"scope":      "scratchpad",
					"count":      0,
					"files":      []map[string]interface{}{},
					"session_id": sessionID,
				}}, nil
			}
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to list scratchpad: %v", err)}}, nil
		}

		results := make([]map[string]interface{}, 0)
		for _, entry := range entries {
			if !entry.IsDir() {
				info, _ := entry.Info()
				results = append(results, map[string]interface{}{
					"filename":   entry.Name(),
					"size_bytes": info.Size(),
					"modified":   info.ModTime().Format(time.RFC3339),
				})
			}
		}

		return &shuttle.Result{
			Success: true,
			Data: map[string]interface{}{
				"action":     "list",
				"scope":      "scratchpad",
				"count":      len(results),
				"files":      results,
				"session_id": sessionID,
			},
		}, nil
	}
}

// executeSearch handles full-text search (artifacts only)
func (t *WorkspaceTool) executeSearch(ctx context.Context, sessionID, scope string, params map[string]interface{}) (*shuttle.Result, error) {
	if scope != "artifact" {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "search is only supported for artifacts (not scratchpad)", Code: "error"}}, nil
	}

	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "query parameter is required for search action", Code: "error"}}, nil
	}

	// Search artifacts (FTS5)
	artifactList, err := t.artifactStore.Search(ctx, query, sessionID, 20)
	if err != nil {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to search artifacts: %v", err)}}, nil
	}

	results := make([]map[string]interface{}, len(artifactList))
	for i, art := range artifactList {
		results[i] = map[string]interface{}{
			"filename":     art.Name,
			"artifact_id":  art.ID,
			"content_type": art.ContentType,
			"size_bytes":   art.SizeBytes,
			"purpose":      art.Purpose,
			"tags":         art.Tags,
			"created_at":   art.CreatedAt.Format(time.RFC3339),
		}
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     "search",
			"scope":      "artifact",
			"query":      query,
			"count":      len(results),
			"results":    results,
			"session_id": sessionID,
		},
	}, nil
}

// executeDelete handles file deletion
func (t *WorkspaceTool) executeDelete(ctx context.Context, sessionID, scope string, params map[string]interface{}) (*shuttle.Result, error) {
	filename, ok := params["filename"].(string)
	if !ok || filename == "" {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Message: "filename parameter is required for delete action", Code: "error"}}, nil
	}

	if scope == "artifact" {
		// Delete artifact (soft delete in database)
		artifact, err := t.artifactStore.GetByName(ctx, filename, sessionID)
		if err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("artifact not found: %v", err)}}, nil
		}

		if err := t.artifactStore.Delete(ctx, artifact.ID, false); err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to delete artifact: %v", err)}}, nil
		}

		return &shuttle.Result{Success: true, Data: map[string]interface{}{
			"action":      "delete",
			"scope":       "artifact",
			"filename":    filename,
			"artifact_id": artifact.ID,
			"deleted":     "soft",
			"session_id":  sessionID,
		}}, nil
	} else {
		// Delete scratchpad file (hard delete)
		dir, err := artifacts.GetScratchpadDir(sessionID)
		if err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to get scratchpad directory: %v", err)}}, nil
		}

		path := filepath.Join(dir, filename)
		if err := os.Remove(path); err != nil {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "error", Message: fmt.Sprintf("failed to delete file: %v", err)}}, nil
		}

		return &shuttle.Result{Success: true, Data: map[string]interface{}{
			"action":     "delete",
			"scope":      "scratchpad",
			"filename":   filename,
			"deleted":    "hard",
			"session_id": sessionID,
		}}, nil
	}
}
