// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLoomDataDir(t *testing.T) {
	// Save original env var
	originalEnv := os.Getenv("LOOM_DATA_DIR")
	defer func() {
		if originalEnv != "" {
			_ = os.Setenv("LOOM_DATA_DIR", originalEnv)
		} else {
			_ = os.Unsetenv("LOOM_DATA_DIR")
		}
	}()

	t.Run("default to ~/.loom", func(t *testing.T) {
		_ = os.Unsetenv("LOOM_DATA_DIR")

		dataDir := GetLoomDataDir()

		homeDir, err := os.UserHomeDir()
		require.NoError(t, err)
		expected := filepath.Join(homeDir, ".loom")
		assert.Equal(t, expected, dataDir)
	})

	t.Run("use LOOM_DATA_DIR when set", func(t *testing.T) {
		customDir := "/custom/loom/data"
		_ = os.Setenv("LOOM_DATA_DIR", customDir)

		dataDir := GetLoomDataDir()

		assert.Equal(t, customDir, dataDir)
	})

	t.Run("expand ~ in LOOM_DATA_DIR", func(t *testing.T) {
		_ = os.Setenv("LOOM_DATA_DIR", "~/custom/.loom")

		dataDir := GetLoomDataDir()

		homeDir, err := os.UserHomeDir()
		require.NoError(t, err)
		expected := filepath.Join(homeDir, "custom", ".loom")
		assert.Equal(t, expected, dataDir)
	})

	t.Run("make relative path absolute in LOOM_DATA_DIR", func(t *testing.T) {
		_ = os.Setenv("LOOM_DATA_DIR", "relative/path")

		dataDir := GetLoomDataDir()

		// Should be absolute
		assert.True(t, filepath.IsAbs(dataDir))
		assert.True(t, strings.HasSuffix(dataDir, "relative/path") || strings.HasSuffix(dataDir, "relative\\path"))
	})
}

func TestGetLoomSubDir(t *testing.T) {
	// Save original env var
	originalEnv := os.Getenv("LOOM_DATA_DIR")
	defer func() {
		if originalEnv != "" {
			_ = os.Setenv("LOOM_DATA_DIR", originalEnv)
		} else {
			_ = os.Unsetenv("LOOM_DATA_DIR")
		}
	}()

	t.Run("return subdirectory path", func(t *testing.T) {
		_ = os.Unsetenv("LOOM_DATA_DIR")

		agentsDir := GetLoomSubDir("agents")

		homeDir, err := os.UserHomeDir()
		require.NoError(t, err)
		expected := filepath.Join(homeDir, ".loom", "agents")
		assert.Equal(t, expected, agentsDir)
	})

	t.Run("respect LOOM_DATA_DIR for subdirectories", func(t *testing.T) {
		customDir := "/custom/loom"
		_ = os.Setenv("LOOM_DATA_DIR", customDir)

		patternsDir := GetLoomSubDir("patterns")

		expected := filepath.Join(customDir, "patterns")
		assert.Equal(t, expected, patternsDir)
	})
}

func TestExpandPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "expand tilde",
			input:    "~/test/path",
			expected: filepath.Join(homeDir, "test", "path"),
		},
		{
			name:     "absolute path unchanged",
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name:  "relative path made absolute",
			input: "relative/path",
			// expected is checked for being absolute, not exact match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)

			if tt.name == "relative path made absolute" {
				assert.True(t, filepath.IsAbs(result))
				assert.True(t, strings.HasSuffix(result, "relative/path") || strings.HasSuffix(result, "relative\\path"))
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
