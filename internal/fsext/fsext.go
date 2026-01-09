// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package fsext provides filesystem extensions.
package fsext

import (
	"os"
	"path/filepath"
	"strings"
)

// Exists checks if a path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir checks if a path is a directory.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Ext returns the file extension.
func Ext(path string) string {
	return filepath.Ext(path)
}

// Base returns the base name.
func Base(path string) string {
	return filepath.Base(path)
}

// Dir returns the directory name.
func Dir(path string) string {
	return filepath.Dir(path)
}

// DirTrim returns a trimmed directory path.
func DirTrim(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	return "..." + path[len(path)-maxLen+3:]
}

// PrettyPath returns a pretty-formatted path.
func PrettyPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// ToUnixLineEndings converts Windows line endings to Unix.
// Returns the converted string and true if successful.
func ToUnixLineEndings(s string) (string, bool) {
	return strings.ReplaceAll(s, "\r\n", "\n"), true
}

// FileEntry represents a file or directory entry.
type FileEntry struct {
	Name  string
	Path  string
	IsDir bool
}

// ListDirectory lists files in a directory with optional filtering and limits.
// The exclude parameter can be a list of patterns to exclude.
// Returns file paths, truncated flag, and error.
func ListDirectory(path string, exclude []string, depth, limit int) ([]string, bool, error) {
	if depth <= 0 {
		depth = 3
	}
	if limit <= 0 {
		limit = 100
	}

	var files []string
	truncated := false

	err := walkDir(path, depth, 0, &files, limit, &truncated)
	if err != nil {
		return nil, false, err
	}

	return files, truncated, nil
}

func walkDir(path string, maxDepth, currentDepth int, files *[]string, limit int, truncated *bool) error {
	if currentDepth >= maxDepth || len(*files) >= limit {
		*truncated = len(*files) >= limit
		return nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}

	for _, e := range entries {
		if len(*files) >= limit {
			*truncated = true
			return nil
		}

		fullPath := filepath.Join(path, e.Name())
		*files = append(*files, fullPath)

		if e.IsDir() && currentDepth < maxDepth-1 {
			_ = walkDir(fullPath, maxDepth, currentDepth+1, files, limit, truncated)
		}
	}
	return nil
}
