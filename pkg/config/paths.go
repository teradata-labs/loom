// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package config

import (
	"os"
	"path/filepath"
	"strings"
)

// GetLoomDataDir returns the Loom data directory.
//
// Priority:
// 1. LOOM_DATA_DIR environment variable (if set and non-empty)
// 2. ~/.loom (default)
//
// The returned path is always absolute. Tilde (~) in LOOM_DATA_DIR is expanded to the user's home directory.
// Relative paths in LOOM_DATA_DIR are converted to absolute paths.
//
// This function is called during bootstrap (before config file is loaded) to locate the config file itself.
// After config is loaded, use config.DataDir for consistency.
//
// Examples:
//
//	LOOM_DATA_DIR=/custom/loom        -> /custom/loom
//	LOOM_DATA_DIR=~/my-loom           -> /home/user/my-loom
//	LOOM_DATA_DIR=relative/path       -> /current/dir/relative/path
//	LOOM_DATA_DIR not set             -> /home/user/.loom
//
// Note: This function reads directly from os.Getenv(), not from viper, to avoid
// circular dependency during config initialization.
func GetLoomDataDir() string {
	// Check environment variable first
	if dataDir := os.Getenv("LOOM_DATA_DIR"); dataDir != "" {
		return expandPath(dataDir)
	}

	// Fall back to ~/.loom
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home dir cannot be determined
		return ".loom"
	}
	return filepath.Join(homeDir, ".loom")
}

// GetLoomSandboxDir returns the agent execution sandbox directory.
//
// Priority:
// 1. LOOM_SANDBOX_DIR environment variable (if set and non-empty)
// 2. LOOM_DATA_DIR (default)
//
// This directory is where shell_execute runs commands by default.
// It is separate from LOOM_DATA_DIR (which stores internal loom data like databases, artifacts, and configs).
//
// The returned path is always absolute. Tilde (~) in LOOM_SANDBOX_DIR is expanded to the user's home directory.
//
// Examples:
//
//	LOOM_SANDBOX_DIR=/project/myapp    -> /project/myapp
//	LOOM_SANDBOX_DIR=~/workspace       -> /home/user/workspace
//	LOOM_SANDBOX_DIR not set           -> /home/user/.loom (LOOM_DATA_DIR)
//
// Note: This provides clear separation of concerns:
//   - LOOM_DATA_DIR: Internal loom data (databases, artifacts, configs)
//   - LOOM_SANDBOX_DIR: Agent execution context (where shell commands run)
func GetLoomSandboxDir() string {
	// Check environment variable first
	if sandboxDir := os.Getenv("LOOM_SANDBOX_DIR"); sandboxDir != "" {
		return expandPath(sandboxDir)
	}

	// Default to LOOM_DATA_DIR (changed from cwd)
	return GetLoomDataDir()
}

// GetLoomSubDir returns a subdirectory within the Loom data directory.
// Example: GetLoomSubDir("agents") returns ~/.loom/agents
func GetLoomSubDir(subdir string) string {
	return filepath.Join(GetLoomDataDir(), subdir)
}

// expandPath expands ~ and resolves to absolute path
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path // Return as-is if we can't get home dir
		}
		return filepath.Join(homeDir, path[2:])
	}

	// Make path absolute
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path // Return as-is if we can't make it absolute
	}
	return absPath
}
