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
	assert.Contains(t, desc, "DEPRECATED")
	assert.Contains(t, desc, "local filesystem")
}

func TestFileReadTool_InputSchema(t *testing.T) {
	tool := NewFileReadTool("")
	schema := tool.InputSchema()

	assert.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Required, "path")
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
