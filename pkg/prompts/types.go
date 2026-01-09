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
// Package prompts provides prompt management for Loom agents.
//
// All prompts (system prompts, tool descriptions, error messages, pattern templates)
// are externalized via PromptRegistry implementations. This enables:
//   - Version control (track prompt changes)
//   - A/B testing (test variants without code changes)
//   - Hot-reload (update prompts without restarts)
//   - Localization (i18n support)
//
// Example usage:
//
//	registry := prompts.NewFileRegistry("./prompts")
//	systemPrompt, err := registry.Get(ctx, "agent.system.base", map[string]interface{}{
//	    "backend_type": "teradata",
//	    "session_id": "sess-123",
//	})
package prompts

import "time"

// PromptMetadata contains information about a prompt.
type PromptMetadata struct {
	// Key is the unique identifier for this prompt.
	// Example: "agent.system.base", "tool.execute_sql.description"
	Key string

	// Version using semantic versioning (e.g., "2.1.0").
	Version string

	// Author of the prompt (email or username).
	Author string

	// Description of what this prompt does.
	Description string

	// Tags for categorization and search.
	Tags []string

	// Variants available for A/B testing.
	// Example: ["default", "concise", "verbose"]
	Variants []string

	// Variables that can be interpolated in the prompt.
	// Example: ["backend_type", "session_id", "cost_threshold"]
	Variables []string

	// Timestamps
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PromptUpdate represents a change notification for a prompt.
// Sent via Watch() channel when prompts are updated.
type PromptUpdate struct {
	Key       string
	Version   string
	Action    string // "created", "modified", "deleted", "error"
	Timestamp time.Time
	Error     error // Set if Action is "error"
}

// PromptContent represents the full prompt with metadata.
type PromptContent struct {
	Metadata PromptMetadata
	Content  string // The actual prompt text
}
