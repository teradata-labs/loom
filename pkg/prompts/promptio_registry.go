// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build promptio

package prompts

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/promptio/pkg/loader"
	"github.com/teradata-labs/promptio/pkg/prompt"
	"github.com/teradata-labs/promptio/pkg/render"
)

// PromptioRegistry implements PromptRegistry using the promptio library.
// This is the recommended registry for production use.
type PromptioRegistry struct {
	mgr        *prompt.Manager
	promptsDir string
}

// NewPromptioRegistry creates a new registry using promptio.
//
// The promptsDir should point to a directory containing YAML prompt files.
// Prompts are cached for performance and support variable interpolation.
//
// Example:
//
//	registry := NewPromptioRegistry("./prompts")
//	systemPrompt, err := registry.Get(ctx, "system", map[string]interface{}{
//	    "backend_type": "teradata",
//	    "tool_count": 5,
//	})
func NewPromptioRegistry(promptsDir string) *PromptioRegistry {
	mgr := prompt.New(
		prompt.WithLoader(loader.NewFile(promptsDir)),
		prompt.WithCache(1000), // Cache up to 1000 rendered prompts
		prompt.WithRenderer(render.NewTemplateEngine()),
	)

	return &PromptioRegistry{
		mgr:        mgr,
		promptsDir: promptsDir,
	}
}

// Get retrieves a prompt by key with variable interpolation.
// Uses promptio's RenderContext for safe variable substitution.
func (r *PromptioRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	rendered, err := r.mgr.RenderContext(ctx, key, vars)
	if err != nil {
		return "", fmt.Errorf("failed to render prompt %q: %w", key, err)
	}
	return rendered, nil
}

// GetWithVariant retrieves a specific variant for A/B testing.
// Tries loading the prompt with variant suffix (e.g., "key.variant").
// Falls back to the default key if the variant is not found.
func (r *PromptioRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	// If variant is empty or "default", just use the base key
	if variant == "" || variant == "default" {
		return r.Get(ctx, key, vars)
	}

	// Try loading with variant suffix: "key.variant"
	variantKey := fmt.Sprintf("%s.%s", key, variant)
	rendered, err := r.mgr.RenderContext(ctx, variantKey, vars)
	if err != nil {
		// Variant not found, fall back to default
		return r.Get(ctx, key, vars)
	}

	return rendered, nil
}

// GetMetadata retrieves prompt metadata without rendering the content.
func (r *PromptioRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	// Get the prompt without rendering
	p, err := r.mgr.GetContext(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get prompt metadata for %q: %w", key, err)
	}

	// Convert promptio metadata to our format
	metadata := &PromptMetadata{
		Key:         p.GetID(),
		Version:     getMetadataVersion(p),
		Description: getMetadataDescription(p),
		Tags:        p.Prompt.Tags, // Tags are on Prompt, not Metadata
		Variables:   extractVariableNames(p),
		Variants:    []string{"default"}, // Promptio doesn't have variants yet
		UpdatedAt:   time.Now(),          // Use current time as fallback
	}

	// Set timestamps from metadata if available
	if p.Prompt.Metadata != nil {
		if p.Prompt.Metadata.UpdatedAt > 0 {
			metadata.UpdatedAt = time.Unix(p.Prompt.Metadata.UpdatedAt, 0)
		}
		if p.Prompt.Metadata.CreatedAt > 0 {
			metadata.CreatedAt = time.Unix(p.Prompt.Metadata.CreatedAt, 0)
		}
		if p.Prompt.Metadata.Author != "" {
			metadata.Author = p.Prompt.Metadata.Author
		}
	}

	return metadata, nil
}

// List lists all available prompt keys, optionally filtered.
func (r *PromptioRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	// Get all prompts from promptio
	prompts := r.mgr.ListContext(ctx)

	// Convert to keys and apply filters
	var keys []string
	for _, p := range prompts {
		// Apply tag filter if specified
		if tagFilter, ok := filters["tag"]; ok {
			if !containsTag(p.Prompt.Tags, tagFilter) { // Tags are on Prompt
				continue
			}
		}

		// Apply prefix filter if specified
		if prefix, ok := filters["prefix"]; ok {
			if !hasPrefix(p.GetID(), prefix) {
				continue
			}
		}

		keys = append(keys, p.GetID())
	}

	return keys, nil
}

// Reload reloads prompts from the source.
func (r *PromptioRegistry) Reload(ctx context.Context) error {
	// Promptio manager automatically reloads on access
	// We can force a cache clear by creating a new manager
	r.mgr = prompt.New(
		prompt.WithLoader(loader.NewFile(r.promptsDir)),
		prompt.WithCache(1000),
		prompt.WithRenderer(render.NewTemplateEngine()),
	)
	return nil
}

// Watch returns a channel that receives updates when prompts change.
// Uses polling-based change detection (checks every 5 seconds).
// Note: This is a workaround until promptio adds native file watching support.
func (r *PromptioRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	ch := make(chan PromptUpdate, 10)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Track last update time for each prompt
		lastCheck := time.Now()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Check context before potentially blocking operation
				if ctx.Err() != nil {
					return
				}

				// Get all prompts and check for updates
				// Use a timeout context to prevent indefinite blocking
				listCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				prompts := r.mgr.ListContext(listCtx)
				cancel()

				// Check context again after potentially blocking call
				if ctx.Err() != nil {
					return
				}

				for _, p := range prompts {
					// Check if prompt was updated since last check
					if p.Prompt.Metadata != nil && p.Prompt.Metadata.UpdatedAt > 0 {
						updateTime := time.Unix(p.Prompt.Metadata.UpdatedAt, 0)
						if updateTime.After(lastCheck) {
							// Prompt was updated, send notification
							select {
							case ch <- PromptUpdate{
								Key:       p.GetID(),
								Version:   getMetadataVersion(p),
								Action:    "modified",
								Timestamp: updateTime,
							}:
							case <-ctx.Done():
								return
							}
						}
					}
				}
				lastCheck = time.Now()
			}
		}
	}()

	return ch, nil
}

// Helper functions

func getMetadataVersion(p *prompt.Prompt) string {
	if p.Prompt.Metadata != nil {
		return p.Prompt.Metadata.Version
	}
	return ""
}

func getMetadataDescription(p *prompt.Prompt) string {
	if p.Prompt.Metadata != nil {
		return p.Prompt.Metadata.Description
	}
	return ""
}

func extractVariableNames(p *prompt.Prompt) []string {
	var names []string
	for name := range p.Prompt.Variables {
		names = append(names, name)
	}
	return names
}

func containsTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
