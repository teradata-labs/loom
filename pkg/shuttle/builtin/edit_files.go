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
	"sync"
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
// against the current directory.
//
// A config or skill can mount this tool by name (builtin.ByName). Without safe
// mode it reaches any path the process can write that isSensitivePath does not
// refuse; --safe-files confines it to the working directory, the sandbox dir,
// the loom data dir and temp.
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

// editLocks serialises edits per file. An edit is read-modify-write, so two
// edits to one file could both read the original and the second write would
// drop the first's change. The lock is held for the whole cycle; it is keyed by
// the resolved path, so two names for one file (a symlink and its target)
// still serialise.
var editLocks sync.Map // resolved path -> *sync.Mutex

func lockForPath(path string) *sync.Mutex {
	actual, _ := editLocks.LoadOrStore(path, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// applyOne performs a single exactly-once literal replacement.
func (t *EditFilesTool) applyOne(path, find, replace string) error {
	cleanPath, err := resolveInScope(t.baseDir, path)
	if err != nil {
		return err
	}
	if isSensitivePath(cleanPath) {
		return fmt.Errorf("sensitive location, not editable")
	}
	// Follow symlinks before deciding anything. The write below replaces the
	// path by rename, which would replace the LINK with a regular file and
	// leave its target untouched — reporting success while changing nothing
	// the caller meant to change. Editing resolves to the real file, as an
	// in-place write always did.
	if resolved, rerr := filepath.EvalSymlinks(cleanPath); rerr == nil {
		cleanPath = resolved
	}
	mu := lockForPath(cleanPath)
	mu.Lock()
	defer mu.Unlock()

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
	// #nosec G304 -- the path is the tool input, cleaned, resolved through
	// symlinks and checked against isSensitivePath (and the workspace roots
	// in safe mode). Reaching a caller-named path is what a file tool is for.
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
	// Write through a sibling temp file and rename: os.WriteFile truncates
	// first, so a failure mid-write would leave the source half-written. The
	// temp lives in the same directory so the rename stays on one filesystem
	// and is atomic.
	dir := filepath.Dir(cleanPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(cleanPath)+".edit-*")
	if err != nil {
		// A writable file inside a directory that is not writable could be
		// edited in place before this change and must still be editable: the
		// temp file is an upgrade, not a new requirement. Falling back costs
		// the atomicity guarantee for exactly that case.
		// #nosec G304 -- see the read above: same resolved, checked path.
		if werr := os.WriteFile(cleanPath, []byte(content), info.Mode().Perm()); werr != nil {
			return fmt.Errorf("write failed: %v", werr)
		}
		return nil
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write failed: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write failed: %v", err)
	}
	// CreateTemp makes the file 0600; restore the original's permissions
	// before it takes the original's place.
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write failed: %v", err)
	}
	if err := os.Rename(tmpName, cleanPath); err != nil {
		return fmt.Errorf("write failed: %v", err)
	}
	return nil
}
