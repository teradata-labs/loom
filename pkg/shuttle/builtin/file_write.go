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
	"strings"
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
// Secure by default, creates directories automatically. files carries a
// batch of complete files; whole-file replace is the default intent.
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
func (t *FileWriteTool) Description() string {
	return `Write complete files: create or fully replace (replacing an existing file? read it first). files carries every file that is ready — batch all determined deliverables into one call. For line changes inside an existing file use edit_files; never rewrite a large file to change a few lines. Creates parent directories automatically; won't overwrite system files.`
}

func (t *FileWriteTool) InputSchema() *shuttle.JSONSchema {
	maxContentLen := MaxSafeContentSize
	return shuttle.NewObjectSchema(
		"Parameters for writing files",
		map[string]*shuttle.JSONSchema{
			"files": {
				Type:        "array",
				Description: "Files to write — batch every file that is ready into one call. Each entry is {path, content, mode?}.",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"path":    shuttle.NewStringSchema("File path to write."),
						"content": shuttle.NewStringSchema("Complete file content. Max 50KB per file."),
						"mode": shuttle.NewStringSchema("'overwrite' (default), 'create' (fail if exists), or 'append'").
							WithEnum("create", "overwrite", "append"),
					},
					Required: []string{"path", "content"},
				},
			},
			"path": shuttle.NewStringSchema("File path to write (single-file form; prefer files)."),
			"content": shuttle.NewStringSchema("Content to write to the file (single-file form). Max 50KB per call - use append mode for larger content.").
				WithLength(nil, &maxContentLen),
			"mode": shuttle.NewStringSchema("Write mode: 'overwrite' (default), 'create' (fail if exists), or 'append'").
				WithEnum("create", "overwrite", "append").
				WithDefault("overwrite"),
		},
		[]string{},
	)
}

func (t *FileWriteTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Batch form: files applied in order, each reported on its own line; one
	// failure does not stop the rest.
	if rawFiles, ok := params["files"].([]interface{}); ok && len(rawFiles) > 0 {
		lines := make([]string, 0, len(rawFiles))
		failed := 0
		for _, r := range rawFiles {
			m, _ := r.(map[string]interface{})
			p, _ := m["path"].(string)
			c, cok := m["content"].(string)
			mode, _ := m["mode"].(string)
			if p == "" || !cok {
				lines = append(lines, "FAILED: entry missing path or content")
				failed++
				continue
			}
			if line, err := t.writeOne(p, c, mode); err != nil {
				lines = append(lines, fmt.Sprintf("FAILED %s: %v", p, err))
				failed++
			} else {
				lines = append(lines, line)
			}
		}
		return &shuttle.Result{
			Success:         failed < len(rawFiles),
			Data:            strings.Join(lines, "\n"),
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

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

	mode := "overwrite"
	if m, ok := params["mode"].(string); ok && m != "" {
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
			_ = f.Close()
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

// writeOne applies a single batch entry with the same semantics as the
// single-file form: sensitive-path guard, 50KB cap, mode handling (default
// overwrite), parent-directory creation. Returns a one-line report.
func (t *FileWriteTool) writeOne(path, content, mode string) (string, error) {
	if mode == "" {
		mode = "overwrite"
	}
	if len(content) > MaxSafeContentSize {
		return "", fmt.Errorf("content exceeds 50KB limit (%d bytes)", len(content))
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(t.baseDir, cleanPath)
	}
	if isSensitivePath(cleanPath) {
		return "", fmt.Errorf("sensitive location, not writable")
	}
	_, statErr := os.Stat(cleanPath)
	fileExists := statErr == nil
	if fileExists && mode == "create" {
		return "", fmt.Errorf("already exists (mode create)")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0750); err != nil {
		return "", fmt.Errorf("mkdir failed: %v", err)
	}
	switch mode {
	case "append":
		f, err := os.OpenFile(cleanPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		n, werr := f.WriteString(content)
		_ = f.Close()
		if werr != nil {
			return "", werr
		}
		return fmt.Sprintf("appended %s (%d bytes)", path, n), nil
	default: // create or overwrite
		if err := os.WriteFile(cleanPath, []byte(content), 0600); err != nil {
			return "", err
		}
		verb := "wrote"
		if !fileExists {
			verb = "created"
		}
		return fmt.Sprintf("%s %s (%d bytes)", verb, path, len(content)), nil
	}
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
		// Check if path equals prefix or is within prefix directory
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return true
		}
	}

	return false
}
