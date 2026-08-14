// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

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
// paths accepts globs (including ** via directory walk) so one call surveys
// many files; pattern turns the call into a project-wide search.
//
// Wildcard sweeps honor the project's own .gitignore and skip hidden
// directories — the project declares its machinery folders (target/,
// dbt_packages/, …), the tool owns no list. Explicitly named paths, and
// globs whose literal prefix already points inside an ignored folder,
// bypass the filter: declared intent is never blocked, only accidental
// sweeps inherit the project's hygiene.
type FileReadTool struct {
	baseDir string // Optional base directory for safety

	ignoreOnce sync.Once
	ignorer    *gitignore.GitIgnore // nil when the project has no .gitignore
}

// MaxMultiReadFiles caps glob expansion — a wider match must be narrowed.
const MaxMultiReadFiles = 50

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
func (t *FileReadTool) Description() string {
	return `Read files. paths accepts globs (models/**/*.sql), so one call can read many files. With pattern, acts as a project-wide search returning matching lines (path:line). Use this for all file reading and searching. Max 10MB per file; won't read sensitive system paths.`
}

func (t *FileReadTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for reading files",
		map[string]*shuttle.JSONSchema{
			"paths": {
				Type:        "array",
				Description: "File paths and/or globs to read (e.g. [\"dbt_project.yml\", \"models/**/*.sql\"]). Preferred over path.",
				Items:       shuttle.NewStringSchema("file path or glob"),
			},
			"pattern": shuttle.NewStringSchema("Optional regex. When set, returns only matching lines as path:line: text (search mode) instead of full contents."),
			"path":    shuttle.NewStringSchema("Single file path to read (legacy form; prefer paths)."),
			"encoding": shuttle.NewStringSchema("Output encoding: 'text' (default) or 'base64' for binary files (single-path form only)").
				WithEnum("text", "base64").
				WithDefault("text"),
			"max_lines":  shuttle.NewNumberSchema("Maximum lines to return per text file (default: 1000, 0 = unlimited)"),
			"start_line": shuttle.NewNumberSchema("Start reading from this line number (1-based, default: 1; single-path form only)"),
		},
		[]string{},
	)
}

func (t *FileReadTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Multi-file / search form: paths array or pattern present.
	if rawPaths, ok := params["paths"].([]interface{}); ok && len(rawPaths) > 0 {
		return t.executeMulti(rawPaths, params, start)
	}
	if pat, ok := params["pattern"].(string); ok && pat != "" {
		if p, ok := params["path"].(string); ok && p != "" {
			return t.executeMulti([]interface{}{p}, params, start)
		}
	}

	// Extract parameters
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "paths (or path) is required",
				Suggestion: "Provide paths: [\"models/**/*.sql\"] or a single path",
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

// executeMulti serves the paths-array and pattern (search) forms.Each path may
// be a literal file or a glob; expansion is capped at MaxMultiReadFiles. A
// failed path becomes an error line in its block; the call fails only when
// every path fails.
func (t *FileReadTool) executeMulti(rawPaths []interface{}, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	maxLines := DefaultMaxLines
	if m, ok := params["max_lines"].(float64); ok {
		maxLines = int(m)
	}

	var re *regexp.Regexp
	if pat, ok := params["pattern"].(string); ok && pat != "" {
		var err error
		re, err = regexp.Compile(pat)
		if err != nil {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_PARAMS",
					Message: fmt.Sprintf("pattern does not compile: %v", err),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
	}

	// Expand entries: literals pass through, globs expand. A glob that
	// matches nothing is reported — in a survey, an empty folder is
	// information, not noise. (Literal paths report absence per-file later.)
	var files []string
	var zeroMatch []string
	seen := map[string]bool{}
	for _, r := range rawPaths {
		entry, _ := r.(string)
		if entry == "" {
			continue
		}
		expanded := t.expandEntry(entry)
		if len(expanded) == 0 && strings.ContainsAny(entry, "*?[") {
			zeroMatch = append(zeroMatch, entry)
			continue
		}
		for _, f := range expanded {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		msg := "no files matched the given paths"
		if len(zeroMatch) > 0 {
			msg = fmt.Sprintf("no files matched: %s", strings.Join(zeroMatch, ", "))
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    msg,
				Suggestion: "Check the paths/globs against the project tree (ls first if unsure)",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	if len(files) > MaxMultiReadFiles {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "TOO_MANY_FILES",
				Message:    fmt.Sprintf("%d files matched (max %d)", len(files), MaxMultiReadFiles),
				Suggestion: "Narrow the glob or split into multiple calls",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
	sort.Strings(files)

	var b strings.Builder
	okCount := 0
	matchCount := 0
	for _, f := range files {
		clean := f
		if !filepath.IsAbs(clean) {
			clean = filepath.Join(t.baseDir, clean)
		}
		clean = filepath.Clean(clean)
		display := f

		fail := func(msg string) {
			b.WriteString(fmt.Sprintf("=== %s ===\nERROR: %s\n", display, msg))
		}
		if isSensitiveReadPath(clean) {
			fail("sensitive location, not readable")
			continue
		}
		info, err := os.Stat(clean)
		if err != nil {
			fail("not found")
			continue
		}
		if info.IsDir() {
			fail("is a directory")
			continue
		}
		if info.Size() > MaxFileReadSize {
			fail(fmt.Sprintf("too large (%d bytes, max %d)", info.Size(), MaxFileReadSize))
			continue
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			fail(fmt.Sprintf("read failed: %v", err))
			continue
		}
		okCount++
		lines := strings.Split(string(data), "\n")
		if re != nil {
			for i, line := range lines {
				if re.MatchString(line) {
					b.WriteString(fmt.Sprintf("%s:%d: %s\n", display, i+1, line))
					matchCount++
				}
			}
			continue
		}
		truncated := false
		if maxLines > 0 && len(lines) > maxLines {
			lines = lines[:maxLines]
			truncated = true
		}
		b.WriteString(fmt.Sprintf("=== %s ===\n%s\n", display, strings.Join(lines, "\n")))
		if truncated {
			b.WriteString(fmt.Sprintf("[truncated at %d lines]\n", maxLines))
		}
	}

	out := b.String()
	for _, zm := range zeroMatch {
		out += fmt.Sprintf("(no matches: %s)\n", zm)
	}
	if re != nil && matchCount == 0 && okCount > 0 {
		out += fmt.Sprintf("no lines matched pattern in %d file(s)\n", okCount)
	}
	return &shuttle.Result{
		Success:         okCount > 0,
		Data:            out,
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// expandEntry turns one paths entry into concrete files. Literal paths pass
// through untouched (existence is judged later, per file). Globs expand via
// filepath.Glob; a ** glob walks the directory before the ** and matches the
// suffix pattern against the basename (or the relative path when the suffix
// itself contains a slash) — covers the models/**/*.sql shape. Wildcard
// results are filtered through the project's .gitignore and the hidden-dir
// convention unless the entry's literal prefix already aims inside an
// ignored folder (declared intent bypasses the filter).
func (t *FileReadTool) expandEntry(entry string) []string {
	hasGlob := strings.ContainsAny(entry, "*?[")
	if !hasGlob {
		return []string{entry}
	}
	abs := entry
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(t.baseDir, abs)
	}

	// Declared intent: the non-wildcard prefix of the pattern already points
	// inside an ignored/hidden folder — the caller aimed there deliberately.
	wildIdx := strings.IndexAny(abs, "*?[")
	literalPrefix := filepath.Dir(abs[:wildIdx+1])
	filterSweep := !t.sweepExcluded(literalPrefix)

	keep := func(p string) bool { return !filterSweep || !t.sweepExcluded(p) }

	if !strings.Contains(entry, "**") {
		matches, _ := filepath.Glob(abs)
		var out []string
		for _, m := range matches {
			if keep(m) {
				out = append(out, m)
			}
		}
		return trimBase(out, t.baseDir, filepath.IsAbs(entry))
	}
	idx := strings.Index(abs, "**")
	root := filepath.Dir(abs[:idx+1]) // directory before the **
	suffix := strings.TrimPrefix(abs[idx+2:], "/")
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && filterSweep && t.sweepExcluded(p) {
				return fs.SkipDir
			}
			return nil
		}
		if !keep(p) {
			return nil
		}
		var target string
		if strings.Contains(suffix, "/") {
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			target = rel
		} else {
			target = filepath.Base(p)
		}
		if okMatch, _ := filepath.Match(suffix, target); okMatch || suffix == "" {
			out = append(out, p)
		}
		return nil
	})
	return trimBase(out, t.baseDir, filepath.IsAbs(entry))
}

// projectIgnore lazily loads the project's .gitignore; nil when absent.
func (t *FileReadTool) projectIgnore() *gitignore.GitIgnore {
	t.ignoreOnce.Do(func() {
		if gi, err := gitignore.CompileIgnoreFile(filepath.Join(t.baseDir, ".gitignore")); err == nil {
			t.ignorer = gi
		}
	})
	return t.ignorer
}

// sweepExcluded reports whether a path is off-limits to wildcard sweeps:
// gitignored by the project, or inside a hidden (dot) directory. Paths
// outside the project draw no opinion.
func (t *FileReadTool) sweepExcluded(absPath string) bool {
	rel, err := filepath.Rel(t.baseDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if len(seg) > 1 && strings.HasPrefix(seg, ".") {
			return true
		}
	}
	if gi := t.projectIgnore(); gi != nil {
		// Directory patterns ("target/") match only slash-terminated paths in
		// the gitignore spec; test both forms so a bare directory path is
		// judged the same as its contents.
		if gi.MatchesPath(rel) || gi.MatchesPath(rel+"/") {
			return true
		}
	}
	return false
}

// trimBase renders matches relative to baseDir for display unless the caller
// asked in absolute terms.
func trimBase(paths []string, baseDir string, keepAbs bool) []string {
	if keepAbs {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, err := filepath.Rel(baseDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, rel)
		} else {
			out = append(out, p)
		}
	}
	return out
}
