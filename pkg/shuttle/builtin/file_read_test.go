// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileReadTool_Name(t *testing.T) {
	tool := NewFileReadTool("")
	assert.Equal(t, "file_read", tool.Name())
}

func TestFileReadTool_Description(t *testing.T) {
	tool := NewFileReadTool("")
	desc := tool.Description()
	assert.NotContains(t, desc, "DEPRECATED")
	assert.Contains(t, desc, "glob")
	assert.Contains(t, desc, "pattern")
}

func TestFileReadTool_InputSchema(t *testing.T) {
	tool := NewFileReadTool("")
	schema := tool.InputSchema()

	assert.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
	// paths/path are alternatives — neither is schema-required; Execute validates.
	assert.Empty(t, schema.Required)
	assert.Contains(t, schema.Properties, "paths")
	assert.Contains(t, schema.Properties, "pattern")
}

func TestFileReadTool_Execute_Success(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "Hello, World!\nLine 2\nLine 3"
	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	tool := NewFileReadTool(tmpDir)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "test.txt",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, content, data["content"])
	assert.Equal(t, "text", data["encoding"])
	assert.Equal(t, int64(len(content)), data["size_bytes"])
	assert.Equal(t, 3, data["total_lines"])
	assert.Equal(t, 3, data["lines_read"])
	assert.False(t, data["truncated"].(bool))
}

func TestFileReadTool_Execute_AbsolutePath(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "absolute.txt")
	content := "Absolute path content"
	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	tool := NewFileReadTool("")

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": testFile,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, content, data["content"])
}

func TestFileReadTool_Execute_FileNotFound(t *testing.T) {
	tool := NewFileReadTool(t.TempDir())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "nonexistent.txt",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "FILE_NOT_FOUND", result.Error.Code)
}

func TestFileReadTool_Execute_MissingPath(t *testing.T) {
	tool := NewFileReadTool("")

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

func TestFileReadTool_Execute_Directory(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewFileReadTool("")

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": tmpDir,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "IS_DIRECTORY", result.Error.Code)
}

func TestFileReadTool_Execute_MaxLines(t *testing.T) {
	// Create a file with many lines
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "manylines.txt")

	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "Line content")
	}
	content := strings.Join(lines, "\n")
	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	tool := NewFileReadTool(tmpDir)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":      "manylines.txt",
		"max_lines": float64(10), // JSON numbers are float64
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, 100, data["total_lines"])
	assert.Equal(t, 10, data["lines_read"])
	assert.True(t, data["truncated"].(bool))
}

func TestFileReadTool_Execute_StartLine(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "numbered.txt")
	content := "Line1\nLine2\nLine3\nLine4\nLine5"
	err := os.WriteFile(testFile, []byte(content), 0644)
	require.NoError(t, err)

	tool := NewFileReadTool(tmpDir)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":       "numbered.txt",
		"start_line": float64(3),
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "Line3\nLine4\nLine5", data["content"])
	assert.Equal(t, 5, data["total_lines"])
	assert.Equal(t, 3, data["lines_read"])
	assert.Equal(t, 3, data["start_line"])
}

func TestFileReadTool_Execute_Base64Encoding(t *testing.T) {
	// Create a file with binary-like content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "binary.bin")
	binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	err := os.WriteFile(testFile, binaryContent, 0644)
	require.NoError(t, err)

	tool := NewFileReadTool(tmpDir)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":     "binary.bin",
		"encoding": "base64",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "base64", data["encoding"])

	// Decode and verify
	decoded, err := base64.StdEncoding.DecodeString(data["content"].(string))
	require.NoError(t, err)
	assert.Equal(t, binaryContent, decoded)
}

func TestFileReadTool_Execute_SensitivePath(t *testing.T) {
	tool := NewFileReadTool("")

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "/etc/shadow",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "UNSAFE_PATH", result.Error.Code)
}

func TestFileReadTool_Backend(t *testing.T) {
	tool := NewFileReadTool("")
	assert.Empty(t, tool.Backend())
}

func TestIsSensitiveReadPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/etc/shadow", true},
		{"/etc/passwd", true},
		{"/etc/sudoers", true},
		{"/proc/1/status", true},
		{"/sys/kernel", true},
		{"/dev/null", true},
		{"/home/user/file.txt", false},
		{"/tmp/test.txt", false},
		{"/var/log/app.log", false},
		{"/etc/hosts", false}, // /etc/hosts is readable and not sensitive
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			result := isSensitiveReadPath(tc.path)
			assert.Equal(t, tc.expected, result, "path: %s", tc.path)
		})
	}
}

// One call reads many files via globs; blocks carry === path === headers.
func TestFileReadTool_MultiGlobRead(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "models", "dim"), 0750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "a.sql"), []byte("select 1"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "dim", "b.sql"), []byte("select 2"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hi"), 0600))

	tool := NewFileReadTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"models/**/*.sql", "readme.md"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "=== models/a.sql ===")
	assert.Contains(t, out, "=== models/dim/b.sql ===")
	assert.Contains(t, out, "=== readme.md ===")
	assert.Contains(t, out, "select 2")
}

// pattern mode returns matching lines as path:line, not full contents.
func TestFileReadTool_PatternSearch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.sql"), []byte("select a\nfrom snap__hosts\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "y.sql"), []byte("select b\nfrom t\n"), 0600))

	tool := NewFileReadTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths":   []interface{}{"*.sql"},
		"pattern": "snap__",
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "x.sql:2: from snap__hosts")
	assert.NotContains(t, out, "select a")
	assert.NotContains(t, out, "y.sql:")
}

// A missing path fails its block only; the call still succeeds.
func TestFileReadTool_MultiPartialFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("fine"), 0600))
	tool := NewFileReadTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"ok.txt", "missing.txt"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "fine")
	assert.Contains(t, out, "ERROR: not found")
}

// Wildcard sweeps skip gitignored machinery and hidden dirs; explicit paths
// and globs aimed into ignored folders still work.
func TestFileReadTool_SweepHonorsGitignore(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("target/\ndbt_packages/\nlogs/\n"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "models"), 0750))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "target", "compiled"), 0750))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".hidden"), 0750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "a.sql"), []byte("select 1"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target", "compiled", "a.sql"), []byte("compiled"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden", "h.sql"), []byte("hidden"), 0600))

	tool := NewFileReadTool(dir)

	// project-wide sweep: only source survives
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"**/*.sql"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "models/a.sql")
	assert.NotContains(t, out, "target/compiled")
	assert.NotContains(t, out, ".hidden")

	// explicit literal path into ignored dir: untouched
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"target/compiled/a.sql"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Contains(t, res.Data.(string), "compiled")

	// glob whose literal prefix aims inside the ignored dir: declared intent, passes
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"target/**/*.sql"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Contains(t, res.Data.(string), "compiled")
}

// Without a .gitignore, only the hidden-dir convention applies.
func TestFileReadTool_SweepNoGitignore(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "target"), 0750))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target", "b.sql"), []byte("select 2"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "c.sql"), []byte("gitfile"), 0600))

	tool := NewFileReadTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"**/*.sql"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "target/b.sql") // no .gitignore → no project opinion
	assert.NotContains(t, out, ".git")      // hidden convention still holds
}

// A glob matching nothing is reported as a note — an empty folder is
// information in a survey; the call still succeeds on the other entries.
func TestFileReadTool_ZeroMatchReported(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.sql"), []byte("select 1"), 0600))
	tool := NewFileReadTool(dir)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"paths": []interface{}{"*.sql", "snapshots/**/*.sql"},
	})
	require.NoError(t, err)
	require.True(t, res.Success)
	out := res.Data.(string)
	assert.Contains(t, out, "select 1")
	assert.Contains(t, out, "(no matches: snapshots/**/*.sql)")
}
