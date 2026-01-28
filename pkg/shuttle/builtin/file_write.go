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

	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// MaxSafeContentSize prevents LLM output limit errors.
	// Set below typical provider output limits (4K-16K tokens = 16KB-64KB).
	// 50KB (~12,500 tokens) is safe for all providers.
	// For larger content, agents should use append mode or multiple files.
	MaxSafeContentSize = 50 * 1024 // 50KB
)

// FileWriteTool provides safe file writing capabilities for agents.
// Apple-style: Secure by default, creates directories automatically.
//
// DEPRECATED: Use workspace tool for session-scoped file operations (write, read, search, list)
// or shell_execute for direct filesystem access (echo, tee, etc.). The workspace tool provides
// superior functionality with session isolation, artifact indexing, and full-text search.
// This tool remains for backwards compatibility but is not recommended for new agents.
type FileWriteTool struct {
	baseDir string // Optional base directory for safety
}

// NewFileWriteTool creates a new file write tool.
// If baseDir is empty, writes to current directory (with safety checks).
func NewFileWriteTool(baseDir string) *FileWriteTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &FileWriteTool{
		baseDir: baseDir,
	}
}

func (t *FileWriteTool) Name() string {
	return "file_write"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/file.yaml).
// This fallback is used only when prompts are not configured.
func (t *FileWriteTool) Description() string {
	return `⚠️ DEPRECATED: Use workspace tool instead (session-scoped, indexed, searchable) or shell_execute (echo command).

Writes content to files on the local filesystem. Creates parent directories automatically.
Safe by default - won't overwrite system files.

Use this tool to:
- Save API responses to files
- Create data files from agent operations
- Store results for later processing
- Generate output files

RECOMMENDED ALTERNATIVES:
- workspace tool: action=write, scope=artifact (session-scoped, indexed, searchable)
- shell_execute: echo "content" > /path/to/file (direct filesystem access)`
}

func (t *FileWriteTool) InputSchema() *shuttle.JSONSchema {
	maxContentLen := MaxSafeContentSize
	return shuttle.NewObjectSchema(
		"Parameters for writing files",
		map[string]*shuttle.JSONSchema{
			"path": shuttle.NewStringSchema("File path to write (required). Relative paths are safe."),
			"content": shuttle.NewStringSchema("Content to write to the file (required). Max 50KB per call - use append mode for larger content.").
				WithLength(nil, &maxContentLen),
			"mode": shuttle.NewStringSchema("Write mode: 'create' (fail if exists), 'overwrite', or 'append' (default: create)").
				WithEnum("create", "overwrite", "append").
				WithDefault("create"),
		},
		[]string{"path", "content"},
	)
}

func (t *FileWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "path is required",
				Suggestion: "Provide a file path (e.g., 'output.txt' or 'data/results.json')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "content is required",
				Suggestion: "Provide content to write to the file",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Enforce max content size to prevent LLM output token limit errors
	if len(content) > MaxSafeContentSize {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "CONTENT_TOO_LARGE",
				Message: fmt.Sprintf("content parameter exceeds 50KB limit (actual: %d bytes / ~%d tokens)", len(content), len(content)/4),
				Suggestion: `For large content, use one of these approaches:
1. Write incrementally using append mode:
   - file_write(path="output.md", content="Section 1...", mode="create")
   - file_write(path="output.md", content="Section 2...", mode="append")

2. Write multiple files:
   - file_write(path="output_part1.md", content="...")
   - file_write(path="output_part2.md", content="...")

3. Summarize your content (meta-summarization):
   - Extract key insights only
   - Reduce detail level`,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	mode := "create"
	if m, ok := params["mode"].(string); ok {
		mode = m
	}

	// Safety: Clean the path and make it absolute
	cleanPath := filepath.Clean(path)

	// If relative, make it relative to baseDir
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(t.baseDir, cleanPath)
	}

	// Safety: Prevent writing to sensitive locations
	if isSensitivePath(cleanPath) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSAFE_PATH",
				Message:    fmt.Sprintf("Cannot write to sensitive location: %s", cleanPath),
				Suggestion: "Use a path in the current directory or a user data directory",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if file exists
	_, err := os.Stat(cleanPath)
	fileExists := err == nil

	if fileExists && mode == "create" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_EXISTS",
				Message:    fmt.Sprintf("File already exists: %s", cleanPath),
				Suggestion: "Use mode='overwrite' to replace, or mode='append' to add content",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create parent directories (Apple-style: just works)
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "MKDIR_FAILED",
				Message: fmt.Sprintf("Failed to create directory: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write the file
	var writeErr error
	var bytesWritten int

	switch mode {
	case "append":
		f, err := os.OpenFile(cleanPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			writeErr = err
		} else {
			n, err := f.WriteString(content)
			bytesWritten = n
			writeErr = err
			f.Close()
		}
	default: // create or overwrite
		data := []byte(content)
		writeErr = os.WriteFile(cleanPath, data, 0600)
		bytesWritten = len(data)
	}

	if writeErr != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_FAILED",
				Message: fmt.Sprintf("Failed to write file: %v", writeErr),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"path":          cleanPath,
			"bytes_written": bytesWritten,
			"mode":          mode,
			"created":       !fileExists,
		},
		Metadata: map[string]interface{}{
			"file_path": cleanPath,
			"size":      bytesWritten,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *FileWriteTool) Backend() string {
	return "" // Backend-agnostic
}

// isSensitivePath checks if a path is in a sensitive system location.
func isSensitivePath(path string) bool {
	sensitive := []string{
		"/etc",
		"/bin",
		"/sbin",
		"/usr/bin",
		"/usr/sbin",
		"/System",
		"/Library",
		"/boot",
		"/dev",
		"/proc",
		"/sys",
	}

	for _, prefix := range sensitive {
		if filepath.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}
