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
	return `Write complete files. files carries every file that is ready — batch all determined deliverables into one call. A new file needs no mode; replacing an existing one needs mode:"overwrite" and reading it first. For line changes inside an existing file use edit_files; never rewrite a large file to change a few lines. Creates parent directories automatically; won't overwrite system files.`
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
						"mode": shuttle.NewStringSchema("'create' (default; fails if the file exists), 'overwrite', or 'append'").
							WithEnum("create", "overwrite", "append"),
					},
					Required: []string{"path", "content"},
				},
			},
			"path": shuttle.NewStringSchema("File path to write (single-file form; prefer files)."),
			"content": shuttle.NewStringSchema("Content to write to the file (single-file form). Max 50KB per call - use append mode for larger content.").
				WithLength(nil, &maxContentLen),
			"mode": shuttle.NewStringSchema("Write mode: 'create' (default; fail if exists), 'overwrite', or 'append'").
				WithEnum("create", "overwrite", "append").
				WithDefault("create"),
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
			if out, err := t.writeOne(p, c, mode); err != nil {
				lines = append(lines, fmt.Sprintf("FAILED %s: %v", p, err))
				failed++
			} else {
				lines = append(lines, out.line(p))
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

	mode := "create"
	if m, ok := params["mode"].(string); ok && m != "" {
		mode = m
	}

	// One implementation for both shapes. This form used to carry its own
	// copy — its own mode switch, its own stat-then-write, its own default
	// branch into os.WriteFile — so a fix to one shape left the other
	// untouched, and this is the shape a model actually sends.
	out, err := t.writeOne(path, content, mode)
	if err != nil {
		code := out.code
		if code == "" {
			code = "WRITE_FAILED"
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       code,
				Message:    err.Error(),
				Suggestion: writeFailureSuggestion(code),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"path":          out.path,
			"bytes_written": out.bytes,
			"mode":          out.mode,
			"created":       out.created,
		},
		Metadata: map[string]interface{}{
			"file_path": out.path,
			"size":      out.bytes,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// writeFailureSuggestion gives the caller the next move for a failure code.
func writeFailureSuggestion(code string) string {
	switch code {
	case "FILE_EXISTS":
		return "Use mode='overwrite' to replace, or mode='append' to add content"
	case "UNSAFE_PATH":
		return "Write inside the workspace, the loom data dir, or /tmp"
	case "INVALID_PARAMS":
		return "mode must be create, overwrite, or append"
	case "CONTENT_TOO_LARGE":
		return "Split the content across files, or append in parts"
	default:
		return ""
	}
}

// writeOutcome is what one write did, so the batch form can render its line
// and the single-path form its Data map from the same implementation.
type writeOutcome struct {
	path    string // resolved, absolute
	bytes   int
	mode    string
	created bool
	code    string // shuttle.Error code when err is non-nil
}

// line renders the batch form's one-line report.
func (o writeOutcome) line(reported string) string {
	switch o.mode {
	case "append":
		return fmt.Sprintf("appended %s (%d bytes)", reported, o.bytes)
	case "create":
		return fmt.Sprintf("created %s (%d bytes)", reported, o.bytes)
	default:
		return fmt.Sprintf("wrote %s (%d bytes)", reported, o.bytes)
	}
}

// writeOne applies a single batch entry with the same semantics as the
// single-file form: sensitive-path guard, 50KB cap, mode handling (default
// overwrite), parent-directory creation. Returns a one-line report.
func (t *FileWriteTool) writeOne(path, content, mode string) (writeOutcome, error) {
	if mode == "" {
		// Same default as the single-file form and the schema: create. An
		// omitted mode must never clobber — a batch that means to replace an
		// existing file says so with mode:"overwrite".
		mode = "create"
	}
	// An unrecognized mode is refused, never guessed. The write path used to
	// be the switch's default, so "Create", "write" or "replace" — all far
	// likelier from a model than an empty string — reached os.WriteFile and
	// clobbered the file. Only the three advertised modes write.
	switch mode {
	case "create", "overwrite", "append":
	default:
		return writeOutcome{code: "INVALID_PARAMS"}, fmt.Errorf("unknown mode %q — use create, overwrite, or append", mode)
	}
	if len(content) > MaxSafeContentSize {
		return writeOutcome{code: "CONTENT_TOO_LARGE"}, fmt.Errorf("content exceeds 50KB limit (%d bytes)", len(content))
	}
	cleanPath, scopeErr := resolveInScope(t.baseDir, path)
	if scopeErr != nil {
		return writeOutcome{code: "UNSAFE_PATH"}, scopeErr
	}
	if isSensitivePath(cleanPath) {
		return writeOutcome{code: "UNSAFE_PATH"}, fmt.Errorf("sensitive location, not writable")
	}
	_, statErr := os.Stat(cleanPath)
	fileExists := statErr == nil
	if fileExists && mode == "create" {
		return writeOutcome{code: "FILE_EXISTS"}, fmt.Errorf("already exists (mode create)")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0750); err != nil {
		return writeOutcome{code: "MKDIR_FAILED"}, fmt.Errorf("mkdir failed: %v", err)
	}
	switch mode {
	case "create":
		// O_EXCL is the only honest form of "create": the Stat above is a
		// check-then-write, and it reads a dangling symlink as "does not
		// exist" and then writes through it to wherever it points. O_EXCL
		// refuses both — the existing file and the dangling link.
		// #nosec G304 -- the path is the tool input, cleaned, resolved through
		// symlinks and checked against isSensitivePath (and the workspace roots
		// in safe mode); O_EXCL additionally refuses an existing target.
		f, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			if os.IsExist(err) {
				return writeOutcome{code: "FILE_EXISTS"}, fmt.Errorf("already exists (mode create)")
			}
			return writeOutcome{code: "WRITE_FAILED"}, err
		}
		n, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return writeOutcome{code: "WRITE_FAILED"}, werr
		}
		return writeOutcome{path: cleanPath, bytes: n, mode: mode, created: true}, nil
	case "append":
		// #nosec G304 -- see the create branch: same resolved, checked path.
		f, err := os.OpenFile(cleanPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return writeOutcome{code: "WRITE_FAILED"}, err
		}
		n, werr := f.WriteString(content)
		_ = f.Close()
		if werr != nil {
			return writeOutcome{code: "WRITE_FAILED"}, werr
		}
		return writeOutcome{path: cleanPath, bytes: n, mode: mode, created: !fileExists}, nil
	default: // overwrite — the only remaining mode
		// #nosec G304 -- see the create branch: same resolved, checked path.
		if err := os.WriteFile(cleanPath, []byte(content), 0600); err != nil {
			return writeOutcome{code: "WRITE_FAILED"}, err
		}
		return writeOutcome{path: cleanPath, bytes: len(content), mode: mode, created: !fileExists}, nil
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
		"C:/Windows/System32",
		"C:/Windows/SysWOW64",
	}

	// Compare on a slash-normalized form: callers pass paths through
	// filepath.Clean, which renders the OS-native separator (backslash on
	// Windows), so these Unix entries would otherwise never match.
	slashPath := filepath.ToSlash(path)
	for _, prefix := range sensitive {
		// Check if path equals prefix or is within prefix directory
		if slashPath == prefix || strings.HasPrefix(slashPath, prefix+"/") {
			return true
		}
	}

	return false
}
