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
package prompts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// FileRegistry loads prompts from YAML files in a directory.
//
// Directory structure:
//
//	prompts/
//	  agent/
//	    system.yaml          # Key: "agent.system"
//	    system.concise.yaml  # Key: "agent.system", variant: "concise"
//	  tools/
//	    execute_sql.yaml     # Key: "tools.execute_sql"
//
// YAML format:
//
//	---
//	key: agent.system
//	version: 2.1.0
//	author:
//	description: Base system prompt for SQL agents
//	tags: [agent, system, sql]
//	variants: [default, concise, verbose]
//	variables: [backend_type, session_id, cost_threshold]
//	---
//	You are a {{.backend_type}} agent...
type FileRegistry struct {
	rootDir string
	mu      sync.RWMutex
	prompts map[string]*filePrompt // key -> prompt
}

// filePrompt represents a loaded prompt with all its variants.
type filePrompt struct {
	metadata PromptMetadata
	variants map[string]string // variant name -> content
}

// promptFile represents a single YAML file.
type promptFile struct {
	Metadata struct {
		Key         string    `yaml:"key"`
		Version     string    `yaml:"version"`
		Author      string    `yaml:"author"`
		Description string    `yaml:"description"`
		Tags        []string  `yaml:"tags"`
		Variants    []string  `yaml:"variants"`
		Variables   []string  `yaml:"variables"`
		CreatedAt   time.Time `yaml:"created_at"`
		UpdatedAt   time.Time `yaml:"updated_at"`
	} `yaml:"metadata"`
	Content string `yaml:"content"`
}

// NewFileRegistry creates a new file-based prompt registry.
//
// Example:
//
//	registry := prompts.NewFileRegistry("./prompts")
//	if err := registry.Reload(ctx); err != nil {
//	    log.Fatal(err)
//	}
func NewFileRegistry(rootDir string) *FileRegistry {
	return &FileRegistry{
		rootDir: rootDir,
		prompts: make(map[string]*filePrompt),
	}
}

// Get retrieves a prompt by key with variable interpolation.
// Returns the default variant.
func (r *FileRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	return r.GetWithVariant(ctx, key, "default", vars)
}

// GetWithVariant retrieves a specific variant for A/B testing.
func (r *FileRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	r.mu.RLock()
	prompt, ok := r.prompts[key]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("prompt not found: %s", key)
	}

	// Get variant content
	content, ok := prompt.variants[variant]
	if !ok {
		return "", fmt.Errorf("variant not found: %s (key: %s)", variant, key)
	}

	// Interpolate variables
	return Interpolate(content, vars), nil
}

// GetMetadata retrieves prompt metadata without the content.
func (r *FileRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	r.mu.RLock()
	prompt, ok := r.prompts[key]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("prompt not found: %s", key)
	}

	// Return a copy
	metadata := prompt.metadata
	return &metadata, nil
}

// List lists all available prompt keys, optionally filtered.
//
// Filters:
//   - "tag": "agent"
//   - "prefix": "tool."
func (r *FileRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var keys []string
	for key, prompt := range r.prompts {
		// Apply filters
		if !matchFilters(prompt, filters) {
			continue
		}
		keys = append(keys, key)
	}

	return keys, nil
}

// Reload reloads prompts from the filesystem.
func (r *FileRegistry) Reload(ctx context.Context) error {
	newPrompts := make(map[string]*filePrompt)

	// Walk the directory tree
	err := filepath.Walk(r.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Only process .yaml and .yml files
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		// Load the file
		prompt, variant, err := r.loadFile(path)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", path, err)
		}

		// Get or create the prompt entry
		key := prompt.metadata.Key
		if _, ok := newPrompts[key]; !ok {
			newPrompts[key] = &filePrompt{
				metadata: prompt.metadata,
				variants: make(map[string]string),
			}
		}

		// Add this variant
		newPrompts[key].variants[variant] = prompt.variants[variant]

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to reload prompts: %w", err)
	}

	// Atomically replace the prompt map
	r.mu.Lock()
	r.prompts = newPrompts
	r.mu.Unlock()

	return nil
}

// Watch returns a channel that receives updates when prompts change.
// Uses fsnotify to watch for file changes in the prompts directory.
func (r *FileRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	// Create fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	// Add root directory and all subdirectories
	if err := r.watchDirectory(watcher, r.rootDir); err != nil {
		watcher.Close()
		return nil, err
	}

	// Create update channel
	ch := make(chan PromptUpdate, 10)

	// Start watch goroutine
	go func() {
		defer watcher.Close()
		defer close(ch)

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// Only process YAML files
				if !strings.HasSuffix(event.Name, ".yaml") && !strings.HasSuffix(event.Name, ".yml") {
					continue
				}

				// Handle different event types
				if event.Op&fsnotify.Write == fsnotify.Write {
					r.handleFileChange(ch, event.Name, "modified")
				} else if event.Op&fsnotify.Create == fsnotify.Create {
					r.handleFileChange(ch, event.Name, "created")
				} else if event.Op&fsnotify.Remove == fsnotify.Remove {
					r.handleFileChange(ch, event.Name, "deleted")
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				// Send error as prompt update
				ch <- PromptUpdate{
					Action: "error",
					Error:  err,
				}
			}
		}
	}()

	return ch, nil
}

// watchDirectory recursively adds directories to the watcher.
func (r *FileRegistry) watchDirectory(watcher *fsnotify.Watcher, dir string) error {
	// Add the directory
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}

	// Walk subdirectories
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != dir {
			if err := watcher.Add(path); err != nil {
				return fmt.Errorf("failed to watch directory %s: %w", path, err)
			}
		}
		return nil
	})
}

// handleFileChange processes a file change event and sends an update.
func (r *FileRegistry) handleFileChange(ch chan<- PromptUpdate, path string, action string) {
	// Extract key from file path
	key := r.extractKeyFromPath(path)

	// Reload prompts from disk
	if err := r.Reload(context.Background()); err != nil {
		ch <- PromptUpdate{
			Key:    key,
			Action: "error",
			Error:  fmt.Errorf("failed to reload prompts: %w", err),
		}
		return
	}

	// Send update notification
	ch <- PromptUpdate{
		Key:       key,
		Action:    action,
		Timestamp: time.Now(),
	}
}

// extractKeyFromPath converts a file path to a prompt key.
// Example: "prompts/agent/system.yaml" -> "agent.system"
func (r *FileRegistry) extractKeyFromPath(path string) string {
	// Make path relative to root directory
	relPath, err := filepath.Rel(r.rootDir, path)
	if err != nil {
		return filepath.Base(path)
	}

	// Remove extension
	relPath = strings.TrimSuffix(relPath, ".yaml")
	relPath = strings.TrimSuffix(relPath, ".yml")

	// Convert path separators to dots
	key := strings.ReplaceAll(relPath, string(filepath.Separator), ".")

	// Remove variant suffix (e.g., "system.concise" -> "system")
	// The last component after splitting by dots might be a variant
	parts := strings.Split(key, ".")
	if len(parts) > 1 {
		// Check if the last part looks like a variant (common variant names)
		lastPart := parts[len(parts)-1]
		knownVariants := map[string]bool{
			"concise": true, "verbose": true, "detailed": true,
			"short": true, "long": true, "minimal": true,
			"streaming": true, "batch": true,
		}
		if knownVariants[lastPart] {
			// Remove the variant suffix
			key = strings.Join(parts[:len(parts)-1], ".")
		}
	}

	return key
}

// loadFile loads a single YAML file and extracts the variant name.
//
// Variant detection:
//   - "system.yaml" -> default variant
//   - "system.concise.yaml" -> "concise" variant
func (r *FileRegistry) loadFile(path string) (*filePrompt, string, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	// Parse YAML with document separator (---)
	parts := strings.SplitN(string(data), "---", 3)
	if len(parts) < 3 {
		return nil, "", fmt.Errorf("invalid format: expected YAML frontmatter with --- separator")
	}

	// Parse metadata (second part, first is empty)
	var pf promptFile
	if err := yaml.Unmarshal([]byte(parts[1]), &pf.Metadata); err != nil {
		return nil, "", fmt.Errorf("failed to parse metadata: %w", err)
	}

	// Content is the third part (after second ---)
	pf.Content = strings.TrimSpace(parts[2])

	// Extract variant from filename
	variant := extractVariant(path)

	// Build metadata
	metadata := PromptMetadata{
		Key:         pf.Metadata.Key,
		Version:     pf.Metadata.Version,
		Author:      pf.Metadata.Author,
		Description: pf.Metadata.Description,
		Tags:        pf.Metadata.Tags,
		Variants:    pf.Metadata.Variants,
		Variables:   pf.Metadata.Variables,
		CreatedAt:   pf.Metadata.CreatedAt,
		UpdatedAt:   pf.Metadata.UpdatedAt,
	}

	// Build prompt
	prompt := &filePrompt{
		metadata: metadata,
		variants: map[string]string{
			variant: pf.Content,
		},
	}

	return prompt, variant, nil
}

// extractVariant extracts the variant name from a filename.
//
// Examples:
//   - "system.yaml" -> "default"
//   - "system.concise.yaml" -> "concise"
//   - "system.verbose.yaml" -> "verbose"
func extractVariant(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	// Split by dots
	parts := strings.Split(nameWithoutExt, ".")
	if len(parts) == 1 {
		return "default"
	}

	// Last part is the variant
	return parts[len(parts)-1]
}

// matchFilters checks if a prompt matches the given filters.
func matchFilters(prompt *filePrompt, filters map[string]string) bool {
	for key, value := range filters {
		switch key {
		case "tag":
			if !contains(prompt.metadata.Tags, value) {
				return false
			}
		case "prefix":
			if !strings.HasPrefix(prompt.metadata.Key, value) {
				return false
			}
		}
	}
	return true
}

// contains checks if a slice contains a string.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
