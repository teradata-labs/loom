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
package prompts_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/teradata-labs/loom/pkg/prompts"
)

func ExampleFileRegistry() {
	// Create a temporary directory for this example
	tmpDir, err := os.MkdirTemp("", "prompts-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a sample prompt file
	promptContent := `---
key: agent.system.base
version: 1.0.0
author: developer@example.com
description: Base system prompt for agents
tags: [agent, system]
variants: [default]
variables: [backend_type, session_id, cost_threshold]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-17T00:00:00Z
---
You are a {{.backend_type}} agent.
Session: {{.session_id}}
Cost threshold: ${{.cost_threshold}}`

	// Write the prompt file
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "system.yaml"), []byte(promptContent), 0644); err != nil {
		log.Fatal(err)
	}

	// Create registry and load prompts
	registry := prompts.NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		log.Fatal(err)
	}

	// Get prompt with variable interpolation
	vars := map[string]interface{}{
		"backend_type":   "PostgreSQL",
		"session_id":     "sess-abc123",
		"cost_threshold": 50.00,
	}

	prompt, err := registry.Get(ctx, "agent.system.base", vars)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(prompt)
	// Output:
	// You are a PostgreSQL agent.
	// Session: sess-abc123
	// Cost threshold: $50
}

func ExampleFileRegistry_variants() {
	tmpDir, err := os.MkdirTemp("", "prompts-variants-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create default variant
	defaultContent := `---
key: greeting
version: 1.0.0
author: developer@example.com
description: Greeting prompt
tags: [greeting]
variants: [default, concise]
variables: [name]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Hello {{.name}}, welcome to our system!`

	// Create concise variant
	conciseContent := `---
key: greeting
version: 1.0.0
author: developer@example.com
description: Greeting prompt
tags: [greeting]
variants: [default, concise]
variables: [name]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Hi {{.name}}!`

	if err := os.WriteFile(filepath.Join(tmpDir, "greeting.yaml"), []byte(defaultContent), 0644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "greeting.concise.yaml"), []byte(conciseContent), 0644); err != nil {
		log.Fatal(err)
	}

	registry := prompts.NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		log.Fatal(err)
	}

	vars := map[string]interface{}{"name": "Alice"}

	// Get default variant
	defaultPrompt, _ := registry.GetWithVariant(ctx, "greeting", "default", vars)
	fmt.Println("Default:", defaultPrompt)

	// Get concise variant
	concisePrompt, _ := registry.GetWithVariant(ctx, "greeting", "concise", vars)
	fmt.Println("Concise:", concisePrompt)

	// Output:
	// Default: Hello Alice, welcome to our system!
	// Concise: Hi Alice!
}

func ExampleFileRegistry_List() {
	tmpDir, err := os.MkdirTemp("", "prompts-list-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create prompts with different tags
	testPrompts := []struct {
		filename string
		key      string
		tags     string
	}{
		{"agent.yaml", "agent.system", "[agent, system]"},
		{"tool1.yaml", "tool.execute", "[tool, sql]"},
		{"tool2.yaml", "tool.schema", "[tool, metadata]"},
	}

	for _, p := range testPrompts {
		content := `---
key: ` + p.key + `
version: 1.0.0
author: test@example.com
description: Test prompt
tags: ` + p.tags + `
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Content.`
		if err := os.WriteFile(filepath.Join(tmpDir, p.filename), []byte(content), 0644); err != nil {
			log.Fatal(err)
		}
	}

	registry := prompts.NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		log.Fatal(err)
	}

	// List all prompts
	all, _ := registry.List(ctx, nil)
	fmt.Printf("All prompts: %d\n", len(all))

	// List only tool prompts
	tools, _ := registry.List(ctx, map[string]string{"tag": "tool"})
	fmt.Printf("Tool prompts: %d\n", len(tools))

	// List prompts by prefix
	agentPrompts, _ := registry.List(ctx, map[string]string{"prefix": "agent."})
	fmt.Printf("Agent prompts: %d\n", len(agentPrompts))

	// Output:
	// All prompts: 3
	// Tool prompts: 2
	// Agent prompts: 1
}

func ExampleFileRegistry_GetMetadata() {
	tmpDir, err := os.MkdirTemp("", "prompts-metadata-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	promptContent := `---
key: agent.system
version: 2.1.0
author:
description: Base system prompt for SQL agents
tags: [agent, system, sql]
variants: [default, concise]
variables: [backend_type, session_id]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-17T00:00:00Z
---
You are a {{.backend_type}} agent.`

	if err := os.WriteFile(filepath.Join(tmpDir, "agent.yaml"), []byte(promptContent), 0644); err != nil {
		log.Fatal(err)
	}

	registry := prompts.NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		log.Fatal(err)
	}

	metadata, err := registry.GetMetadata(ctx, "agent.system")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Key: %s\n", metadata.Key)
	fmt.Printf("Version: %s\n", metadata.Version)
	fmt.Printf("Author: %s\n", metadata.Author)
	fmt.Printf("Tags: %v\n", metadata.Tags)
	fmt.Printf("Variables: %v\n", metadata.Variables)

	// Output:
	// Key: agent.system
	// Version: 2.1.0
	// Author:
	// Tags: [agent system sql]
	// Variables: [backend_type session_id]
}
