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

// EditFilesTool applies targeted in-place changes to existing files. Each
// edit replaces one exact literal occurrence of find with replace; zero or
// multiple matches fail that edit loudly — a silently skipped edit the agent
// believes landed is worse than any rewrite. Edits apply in declared order,
// so later edits to the same file see earlier results.
type EditFilesTool struct {
	baseDir string // Optional base directory for safety
}

// NewEditFilesTool creates the edit tool. If baseDir is empty, edits resolve
// against the current directory (with safety checks).
func NewEditFilesTool(baseDir string) *EditFilesTool {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	return &EditFilesTool{baseDir: baseDir}
}

// Name returns the tool name.
func (t *EditFilesTool) Name() string { return "edit_files" }

// Backend returns "" — the tool is backend-independent.
func (t *EditFilesTool) Backend() string { return "" }

// Description returns the tool description.
func (t *EditFilesTool) Description() string {
	return `Change lines inside existing files. Each edit replaces one exact occurrence of find with replace and fails loudly if find is absent or ambiguous. Use for targeted fixes; use file_write for new or fully reshaped files.`
}

// InputSchema returns the JSON schema for the tool input.
func (t *EditFilesTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for editing files",
		map[string]*shuttle.JSONSchema{
			"edits": {
				Type:        "array",
				Description: "Edits applied in order. Each replaces exactly one literal occurrence of find within the file at path.",
				Items: &shuttle.JSONSchema{
					Type: "object",
					Properties: map[string]*shuttle.JSONSchema{
						"path":    shuttle.NewStringSchema("File to edit (must exist)."),
						"find":    shuttle.NewStringSchema("Exact text to replace — literal, not regex. Must occur exactly once; include surrounding lines to disambiguate."),
						"replace": shuttle.NewStringSchema("Replacement text."),
					},
					Required: []string{"path", "find", "replace"},
				},
			},
		},
		[]string{"edits"},
	)
}

// Execute applies the edits in order. Per-edit result lines; one failure does
// not stop the rest; the call fails only when every edit fails.
func (t *EditFilesTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	raw, ok := params["edits"].([]interface{})
	if !ok || len(raw) == 0 {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "edits is required",
				Suggestion: "Provide edits: [{path, find, replace}]",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	lines := make([]string, 0, len(raw))
	failed := 0
	for _, r := range raw {
		m, _ := r.(map[string]interface{})
		path, _ := m["path"].(string)
		find, _ := m["find"].(string)
		replace, rok := m["replace"].(string)
		if path == "" || find == "" || !rok {
			lines = append(lines, "FAILED: edit missing path, find, or replace")
			failed++
			continue
		}
		if err := t.applyOne(path, find, replace); err != nil {
			lines = append(lines, fmt.Sprintf("FAILED %s: %v", path, err))
			failed++
		} else {
			lines = append(lines, fmt.Sprintf("edited %s", path))
		}
	}

	return &shuttle.Result{
		Success:         failed < len(raw),
		Data:            strings.Join(lines, "\n"),
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// applyOne performs a single exactly-once literal replacement.
func (t *EditFilesTool) applyOne(path, find, replace string) error {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(t.baseDir, cleanPath)
	}
	if isSensitivePath(cleanPath) {
		return fmt.Errorf("sensitive location, not editable")
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("not found")
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if info.Size() > MaxFileReadSize {
		return fmt.Errorf("too large (%d bytes, max %d)", info.Size(), MaxFileReadSize)
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("read failed: %v", err)
	}
	content := string(data)
	switch n := strings.Count(content, find); n {
	case 0:
		return fmt.Errorf("find text not found")
	case 1:
		// exactly once — proceed
	default:
		return fmt.Errorf("find text matches %d times — include surrounding lines to disambiguate", n)
	}
	content = strings.Replace(content, find, replace, 1)
	if err := os.WriteFile(cleanPath, []byte(content), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write failed: %v", err)
	}
	return nil
}
