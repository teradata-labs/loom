// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package metadata provides rich, self-describing tool metadata loading.
//
// This package implements the self-describing tool system that enables LLM-driven
// tool selection by replacing hardcoded keyword matching with semantic reasoning.
//
// # Architecture
//
// Tools describe themselves using YAML metadata files that include:
//   - use_cases: When to use (and when NOT to use) the tool
//   - conflicts: Tools that conflict, with severity levels and reasoning
//   - alternatives: Better options for specific scenarios
//   - complements: Tools that work well together
//   - examples: Real-world usage examples
//   - prerequisites: Required API keys, env vars, etc.
//   - best_practices: Guidelines for effective use
//
// # Performance
//
// The Loader uses an in-memory cache with sync.RWMutex for thread-safe,
// high-performance metadata access. Subsequent loads return cached results
// without file I/O.
//
// # Usage
//
//	// Singleton loader (recommended - builtin registry pattern)
//	var metadataLoader = metadata.NewLoader("tool_metadata")
//
//	// Load metadata with caching
//	meta, err := metadataLoader.Load("web_search")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if meta != nil {
//	    fmt.Printf("Tool: %s\nCategory: %s\n", meta.Name, meta.Category)
//	    for _, uc := range meta.UseCases {
//	        fmt.Printf("Use case: %s\n", uc.Title)
//	    }
//	}
//
// # Thread Safety
//
// All Loader methods are thread-safe and can be called concurrently.
// The cache uses RWMutex for efficient concurrent reads.
package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// Loader loads tool metadata from YAML files with caching.
type Loader struct {
	metadataDir string
	cache       map[string]*ToolMetadata
	mu          sync.RWMutex
}

// NewLoader creates a new metadata loader with caching.
// If metadataDir is empty, uses ./tool_metadata/ relative to working directory.
func NewLoader(metadataDir string) *Loader {
	if metadataDir == "" {
		// Default to tool_metadata in project root
		metadataDir = "tool_metadata"
	}
	return &Loader{
		metadataDir: metadataDir,
		cache:       make(map[string]*ToolMetadata),
	}
}

// Load loads metadata for a specific tool by name with caching.
// Returns nil if metadata file doesn't exist (tool may not have metadata yet).
// Cached results are returned immediately without file I/O.
func (l *Loader) Load(toolName string) (*ToolMetadata, error) {
	// Check cache first (read lock)
	l.mu.RLock()
	if cached, found := l.cache[toolName]; found {
		l.mu.RUnlock()
		return cached, nil
	}
	l.mu.RUnlock()

	// Not in cache, load from disk
	path := filepath.Join(l.metadataDir, toolName+".yaml")

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// No metadata file - cache nil to avoid repeated stat calls
		l.mu.Lock()
		l.cache[toolName] = nil
		l.mu.Unlock()
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata file %s: %w", path, err)
	}

	var metadata ToolMetadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata file %s: %w", path, err)
	}

	// Cache the result (write lock)
	l.mu.Lock()
	l.cache[toolName] = &metadata
	l.mu.Unlock()

	return &metadata, nil
}

// LoadAll loads metadata for all tools in the metadata directory.
// Returns a map of tool name to metadata.
func (l *Loader) LoadAll() (map[string]*ToolMetadata, error) {
	result := make(map[string]*ToolMetadata)

	// Check if directory exists
	if _, err := os.Stat(l.metadataDir); os.IsNotExist(err) {
		// No metadata directory - return empty map (not an error)
		return result, nil
	}

	// Read directory
	entries, err := os.ReadDir(l.metadataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata directory: %w", err)
	}

	// Load each YAML file
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		// Extract tool name from filename
		toolName := entry.Name()
		toolName = toolName[:len(toolName)-len(filepath.Ext(toolName))]

		metadata, err := l.Load(toolName)
		if err != nil {
			// Log error but continue loading others
			fmt.Fprintf(os.Stderr, "Warning: failed to load metadata for %s: %v\n", toolName, err)
			continue
		}

		if metadata != nil {
			result[toolName] = metadata
		}
	}

	return result, nil
}

// DefaultLoader creates a loader for the default metadata directory.
// Looks for tool_metadata/ relative to working directory.
func DefaultLoader() *Loader {
	return NewLoader("")
}
