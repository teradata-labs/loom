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

import "context"

// PromptRegistry manages prompt retrieval and lifecycle.
//
// Implementations can load from files, HTTP APIs (Promptio), databases, etc.
// All prompts support variable interpolation and A/B testing variants.
type PromptRegistry interface {
	// Get retrieves a prompt by key with variable interpolation.
	//
	// Variables are safely substituted using {{.variable_name}} syntax.
	// Returns the default variant unless GetWithVariant is used.
	//
	// Example:
	//   prompt, err := registry.Get(ctx, "agent.system.base", map[string]interface{}{
	//       "backend_type": "teradata",
	//       "session_id": "sess-123",
	//   })
	Get(ctx context.Context, key string, vars map[string]interface{}) (string, error)

	// GetWithVariant retrieves a specific variant for A/B testing.
	//
	// Example:
	//   prompt, err := registry.GetWithVariant(ctx, "agent.system.base", "concise", vars)
	GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error)

	// GetMetadata retrieves prompt metadata without the content.
	GetMetadata(ctx context.Context, key string) (*PromptMetadata, error)

	// List lists all available prompt keys, optionally filtered.
	//
	// Filters can include:
	//   - "tag": "agent"
	//   - "prefix": "tool."
	List(ctx context.Context, filters map[string]string) ([]string, error)

	// Reload reloads prompts from the source.
	// Useful for manually triggering updates.
	Reload(ctx context.Context) error

	// Watch returns a channel that receives updates when prompts change.
	// Used for hot-reload functionality.
	//
	// Example:
	//   updates, err := registry.Watch(ctx)
	//   for update := range updates {
	//       log.Printf("Prompt %s updated to version %s", update.Key, update.Version)
	//   }
	Watch(ctx context.Context) (<-chan PromptUpdate, error)
}
