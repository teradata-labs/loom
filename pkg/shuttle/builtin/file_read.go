// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// MaxFileReadSize is the maximum file size we'll read (10MB).
	// Prevents memory issues with very large files.
	MaxFileReadSize = 10 * 1024 * 1024

	// DefaultMaxLines limits text output to prevent context bloat.
	DefaultMaxLines = 1000
)

// FileReadTool provides safe file reading capabilities for agents.
// Enables data grounding by reading actual file content rather than guessing.
//
// DEPRECATED: Use workspace tool for session-scoped file operations (read, write, search, list)
// or shell_execute for direct filesystem access (cat, ls, etc.). The workspace tool provides
// superior functionality with session isolation, artifact indexing, and full-text search.
// This tool remains for backwards compatibility but is not recommended for new agents.
type FileReadTool struct {
	baseDir string // Optional base directory for safety
}

// NewFileReadTool creates a new file read tool.
// If baseDir is empty, reads from current directory (with safety checks).
func NewFileReadTool(baseDir string) *FileReadTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &FileReadTool{
		baseDir: baseDir,
	}
}

func (t *FileReadTool) Name() string {
	return "file_read"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/file.yaml).
// This fallback is used only when prompts are not configured.
func (t *FileReadTool) Description() string {
	return `⚠️ DEPRECATED: Use workspace tool instead (session-scoped, indexed, searchable) or shell_execute (cat command).

Reads content from files on the local filesystem.
Use this tool to ground your responses in actual data rather than guessing.

Use this tool to:
- Read data files saved by previous workflow stages
- Verify file contents before summarizing
- Load configuration or results files
- Read markdown, JSON, XML, or other text files

Safety: Won't read sensitive system files. Max file size: 10MB.

RECOMMENDED ALTERNATIVES:
- workspace tool: action=read, scope=artifact (session-scoped, indexed, searchable)
- shell_execute: cat /path/to/file (direct filesystem access)`
}

func (t *FileReadTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for reading files",
		map[string]*shuttle.JSONSchema{
			"path": shuttle.NewStringSchema("File path to read (required). Relative paths are resolved from working directory."),
			"encoding": shuttle.NewStringSchema("Output encoding: 'text' (default) or 'base64' for binary files").
				WithEnum("text", "base64").
				WithDefault("text"),
			"max_lines":  shuttle.NewNumberSchema("Maximum lines to return for text files (default: 1000, 0 = unlimited)"),
			"start_line": shuttle.NewNumberSchema("Start reading from this line number (1-based, default: 1)"),
		},
		[]string{"path"},
	)
}

func (t *FileReadTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "path is required",
				Suggestion: "Provide a file path (e.g., 'data/results.json' or 'npath_analysis_results.md')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	encoding := "text"
	if e, ok := params["encoding"].(string); ok && e != "" {
		encoding = e
	}

	maxLines := DefaultMaxLines
	if m, ok := params["max_lines"].(float64); ok {
		maxLines = int(m)
	}

	startLine := 1
	if s, ok := params["start_line"].(float64); ok && s > 0 {
		startLine = int(s)
	}

	// Safety: Clean the path and make it absolute
	cleanPath := filepath.Clean(path)

	// If relative, make it relative to baseDir
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(t.baseDir, cleanPath)
	}

	// Safety: Prevent reading sensitive locations
	if isSensitiveReadPath(cleanPath) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "UNSAFE_PATH",
				Message:    fmt.Sprintf("Cannot read from sensitive location: %s", cleanPath),
				Suggestion: "Read files from your project directory or user data directories",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if file exists
	info, err := os.Stat(cleanPath)
	if os.IsNotExist(err) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("File not found: %s", cleanPath),
				Suggestion: "Check the file path and ensure the file exists",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "STAT_FAILED",
				Message: fmt.Sprintf("Failed to stat file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check if it's a directory
	if info.IsDir() {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "IS_DIRECTORY",
				Message:    fmt.Sprintf("Path is a directory, not a file: %s", cleanPath),
				Suggestion: "Provide a path to a file, not a directory",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Check file size
	if info.Size() > MaxFileReadSize {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_TOO_LARGE",
				Message:    fmt.Sprintf("File too large: %d bytes (max: %d bytes)", info.Size(), MaxFileReadSize),
				Suggestion: "Use start_line and max_lines to read a portion of large files",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Read the file
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "READ_FAILED",
				Message: fmt.Sprintf("Failed to read file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	var content string
	var totalLines int
	var returnedLines int
	var truncated bool

	if encoding == "base64" {
		// Binary mode: return base64-encoded content
		content = base64.StdEncoding.EncodeToString(data)
		totalLines = 0
		returnedLines = 0
	} else {
		// Text mode: handle line limits
		lines := strings.Split(string(data), "\n")
		totalLines = len(lines)

		// Apply start_line (1-based)
		if startLine > 1 {
			if startLine > len(lines) {
				lines = []string{}
			} else {
				lines = lines[startLine-1:]
			}
		}

		// Apply max_lines limit
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[:maxLines]
			truncated = true
		}

		returnedLines = len(lines)
		content = strings.Join(lines, "\n")
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"path":        cleanPath,
			"content":     content,
			"encoding":    encoding,
			"size_bytes":  info.Size(),
			"total_lines": totalLines,
			"lines_read":  returnedLines,
			"start_line":  startLine,
			"truncated":   truncated,
			"modified_at": info.ModTime().Format(time.RFC3339),
		},
		Metadata: map[string]interface{}{
			"file_path": cleanPath,
			"size":      info.Size(),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *FileReadTool) Backend() string {
	return "" // Backend-agnostic
}

// isSensitiveReadPath checks if a path is in a sensitive system location.
// Reading is less dangerous than writing, but we still protect some paths.
func isSensitiveReadPath(path string) bool {
	sensitive := []string{
		"/etc/shadow",
		"/etc/passwd",
		"/etc/sudoers",
		"/private/etc/shadow",
		"/private/etc/passwd",
		"/private/etc/sudoers",
	}

	// Exact match for very sensitive files
	for _, s := range sensitive {
		if path == s {
			return true
		}
	}

	// Prevent reading from certain directories entirely
	protectedDirs := []string{
		"/proc",
		"/sys",
		"/dev",
	}

	for _, prefix := range protectedDirs {
		if strings.HasPrefix(path, prefix+"/") || path == prefix {
			return true
		}
	}

	return false
}
