// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWriteTool_ContentSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewFileWriteTool(tmpDir)

	t.Run("content within 50KB limit succeeds", func(t *testing.T) {
		content := strings.Repeat("a", 40*1024) // 40KB - within limit
		params := map[string]interface{}{
			"path":    "test.txt",
			"content": content,
			"mode":    "create",
		}

		result, err := tool.Execute(context.Background(), params)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if !result.Success {
			t.Errorf("Expected success for 40KB content, got error: %v", result.Error)
		}

		// Verify file was created
		filePath := filepath.Join(tmpDir, "test.txt")
		fileContent, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}

		if len(fileContent) != 40*1024 {
			t.Errorf("Expected 40KB file, got %d bytes", len(fileContent))
		}
	})

	t.Run("content exceeding 50KB limit fails", func(t *testing.T) {
		content := strings.Repeat("a", 60*1024) // 60KB - exceeds limit
		params := map[string]interface{}{
			"path":    "large.txt",
			"content": content,
			"mode":    "create",
		}

		result, err := tool.Execute(context.Background(), params)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if result.Success {
			t.Error("Expected failure for 60KB content")
		}

		if result.Error == nil {
			t.Fatal("Expected error in result")
		}

		if result.Error.Code != "CONTENT_TOO_LARGE" {
			t.Errorf("Expected CONTENT_TOO_LARGE error, got: %s", result.Error.Code)
		}

		// Verify error message contains helpful suggestions
		if !strings.Contains(result.Error.Suggestion, "append mode") {
			t.Error("Expected suggestion to mention append mode")
		}
		if !strings.Contains(result.Error.Suggestion, "multiple files") {
			t.Error("Expected suggestion to mention multiple files")
		}
	})

	t.Run("exactly 50KB content succeeds", func(t *testing.T) {
		content := strings.Repeat("a", MaxSafeContentSize) // Exactly 50KB
		params := map[string]interface{}{
			"path":    "exact.txt",
			"content": content,
			"mode":    "create",
		}

		result, err := tool.Execute(context.Background(), params)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if !result.Success {
			t.Errorf("Expected success for exactly 50KB content, got error: %v", result.Error)
		}
	})
}

func TestFileWriteTool_AppendMode(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewFileWriteTool(tmpDir)

	t.Run("incremental writes with append mode", func(t *testing.T) {
		filePath := "incremental.txt"

		// Write section 1
		params1 := map[string]interface{}{
			"path":    filePath,
			"content": "Section 1\n",
			"mode":    "create",
		}
		result1, err := tool.Execute(context.Background(), params1)
		if err != nil {
			t.Fatalf("Execute() section 1 error = %v", err)
		}
		if !result1.Success {
			t.Fatalf("Section 1 failed: %v", result1.Error)
		}

		// Append section 2
		params2 := map[string]interface{}{
			"path":    filePath,
			"content": "Section 2\n",
			"mode":    "append",
		}
		result2, err := tool.Execute(context.Background(), params2)
		if err != nil {
			t.Fatalf("Execute() section 2 error = %v", err)
		}
		if !result2.Success {
			t.Fatalf("Section 2 failed: %v", result2.Error)
		}

		// Append section 3
		params3 := map[string]interface{}{
			"path":    filePath,
			"content": "Section 3\n",
			"mode":    "append",
		}
		result3, err := tool.Execute(context.Background(), params3)
		if err != nil {
			t.Fatalf("Execute() section 3 error = %v", err)
		}
		if !result3.Success {
			t.Fatalf("Section 3 failed: %v", result3.Error)
		}

		// Verify final content
		fullPath := filepath.Join(tmpDir, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}

		expected := "Section 1\nSection 2\nSection 3\n"
		if string(content) != expected {
			t.Errorf("Expected:\n%s\nGot:\n%s", expected, string(content))
		}
	})

	t.Run("large content via incremental append", func(t *testing.T) {
		filePath := "large_incremental.txt"

		// Simulate agent writing large summary incrementally
		// Each section is under 50KB limit
		sections := []string{
			strings.Repeat("Section 1: ", 3000), // ~33KB
			strings.Repeat("Section 2: ", 3000), // ~33KB
			strings.Repeat("Section 3: ", 3000), // ~33KB
		}

		// Create with first section
		params1 := map[string]interface{}{
			"path":    filePath,
			"content": sections[0],
			"mode":    "create",
		}
		result, err := tool.Execute(context.Background(), params1)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result.Success {
			t.Fatalf("Create failed: %v", result.Error)
		}

		// Append remaining sections
		for i, section := range sections[1:] {
			params := map[string]interface{}{
				"path":    filePath,
				"content": section,
				"mode":    "append",
			}
			result, err := tool.Execute(context.Background(), params)
			if err != nil {
				t.Fatalf("Execute() append %d error = %v", i+2, err)
			}
			if !result.Success {
				t.Fatalf("Append %d failed: %v", i+2, result.Error)
			}
		}

		// Verify total size (~99KB from 3 sections)
		fullPath := filepath.Join(tmpDir, filePath)
		info, err := os.Stat(fullPath)
		if err != nil {
			t.Fatalf("Failed to stat file: %v", err)
		}

		expectedSize := len(sections[0]) + len(sections[1]) + len(sections[2])
		if info.Size() != int64(expectedSize) {
			t.Errorf("Expected file size %d, got %d", expectedSize, info.Size())
		}

		t.Logf("Successfully wrote %d bytes via incremental append", info.Size())
	})
}

func TestFileWriteTool_Modes(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewFileWriteTool(tmpDir)

	t.Run("create mode fails if file exists", func(t *testing.T) {
		filePath := "existing.txt"

		// Create file
		params1 := map[string]interface{}{
			"path":    filePath,
			"content": "original",
			"mode":    "create",
		}
		result1, err := tool.Execute(context.Background(), params1)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result1.Success {
			t.Fatalf("Create failed: %v", result1.Error)
		}

		// Try to create again (should fail)
		params2 := map[string]interface{}{
			"path":    filePath,
			"content": "new content",
			"mode":    "create",
		}
		result2, err := tool.Execute(context.Background(), params2)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		if result2.Success {
			t.Error("Expected failure when creating existing file")
		}

		if result2.Error.Code != "FILE_EXISTS" {
			t.Errorf("Expected FILE_EXISTS error, got: %s", result2.Error.Code)
		}
	})

	t.Run("overwrite mode replaces existing file", func(t *testing.T) {
		filePath := "overwrite.txt"

		// Create file
		params1 := map[string]interface{}{
			"path":    filePath,
			"content": "original",
			"mode":    "create",
		}
		result1, err := tool.Execute(context.Background(), params1)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result1.Success {
			t.Fatalf("Create failed: %v", result1.Error)
		}

		// Overwrite
		params2 := map[string]interface{}{
			"path":    filePath,
			"content": "replaced",
			"mode":    "overwrite",
		}
		result2, err := tool.Execute(context.Background(), params2)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result2.Success {
			t.Fatalf("Overwrite failed: %v", result2.Error)
		}

		// Verify content was replaced
		fullPath := filepath.Join(tmpDir, filePath)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}

		if string(content) != "replaced" {
			t.Errorf("Expected 'replaced', got '%s'", string(content))
		}
	})
}
