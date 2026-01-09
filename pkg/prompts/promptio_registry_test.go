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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPromptioRegistry_Get(t *testing.T) {
	// Create test prompts directory
	tempDir := t.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write test prompt file
	promptContent := `name: test
namespace: loom
prompts:
  - id: greeting
    content: |
      Hello {{.name}}! Welcome to {{.app}}.
    variables:
      name:
        name: name
        type: 1  # STRING
        required: true
        description: "User name"
      app:
        name: app
        type: 1  # STRING
        required: false
        default_value: "Loom"
        description: "Application name"
    tags:
      - test
      - greeting
    metadata:
      version: "1.0.0"
      description: "Test greeting prompt"
`
	promptFile := filepath.Join(promptsDir, "test.yaml")
	if err := os.WriteFile(promptFile, []byte(promptContent), 0644); err != nil {
		t.Fatalf("Failed to write test prompt: %v", err)
	}

	// Create registry
	registry := NewPromptioRegistry(promptsDir)

	ctx := context.Background()

	// Test Get with all variables
	t.Run("Get with all variables", func(t *testing.T) {
		result, err := registry.Get(ctx, "greeting", map[string]interface{}{
			"name": "Alice",
			"app":  "TestApp",
		})
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		// YAML multiline strings (|) preserve trailing newline
		expected := "Hello Alice! Welcome to TestApp.\n"
		if result != expected {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	// Test Get with explicit default value
	// Note: promptio default_value requires explicit passing or custom logic
	t.Run("Get with explicit app value", func(t *testing.T) {
		result, err := registry.Get(ctx, "greeting", map[string]interface{}{
			"name": "Bob",
			"app":  "Loom", // Explicitly pass the default
		})
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		expected := "Hello Bob! Welcome to Loom.\n"
		if result != expected {
			t.Errorf("Expected %q, got %q", expected, result)
		}
	})

	// Test Get with missing prompt
	t.Run("Get nonexistent prompt", func(t *testing.T) {
		_, err := registry.Get(ctx, "nonexistent", nil)
		if err == nil {
			t.Error("Expected error for nonexistent prompt")
		}
	})
}

func TestPromptioRegistry_GetMetadata(t *testing.T) {
	// Create test prompts directory
	tempDir := t.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write test prompt file
	promptContent := `name: test
namespace: loom
prompts:
  - id: system
    content: "You are a helpful assistant."
    variables:
      backend_type:
        name: backend_type
        type: 1
        required: true
        description: "Backend type"
    tags:
      - system
      - agent
    metadata:
      version: "2.0.1"
      author: "test@example.com"
      description: "System prompt"
`
	promptFile := filepath.Join(promptsDir, "test.yaml")
	if err := os.WriteFile(promptFile, []byte(promptContent), 0644); err != nil {
		t.Fatalf("Failed to write test prompt: %v", err)
	}

	registry := NewPromptioRegistry(promptsDir)
	ctx := context.Background()

	metadata, err := registry.GetMetadata(ctx, "system")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if metadata.Key != "system" {
		t.Errorf("Expected key 'system', got %q", metadata.Key)
	}

	if metadata.Version != "2.0.1" {
		t.Errorf("Expected version '2.0.1', got %q", metadata.Version)
	}

	if metadata.Description != "System prompt" {
		t.Errorf("Expected description 'System prompt', got %q", metadata.Description)
	}

	if len(metadata.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(metadata.Tags))
	}

	if !contains(metadata.Tags, "system") || !contains(metadata.Tags, "agent") {
		t.Errorf("Expected tags 'system' and 'agent', got %v", metadata.Tags)
	}

	if len(metadata.Variables) != 1 {
		t.Errorf("Expected 1 variable, got %d", len(metadata.Variables))
	}

	if !contains(metadata.Variables, "backend_type") {
		t.Errorf("Expected variable 'backend_type', got %v", metadata.Variables)
	}
}

func TestPromptioRegistry_List(t *testing.T) {
	// Create test prompts directory
	tempDir := t.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write multiple test prompts
	promptContent := `name: test
namespace: loom
prompts:
  - id: agent.system
    content: "Agent system prompt"
    tags:
      - agent
      - system
    metadata:
      version: "1.0.0"

  - id: agent.tool
    content: "Tool description prompt"
    tags:
      - agent
      - tool
    metadata:
      version: "1.0.0"

  - id: user.greeting
    content: "User greeting prompt"
    tags:
      - user
    metadata:
      version: "1.0.0"
`
	promptFile := filepath.Join(promptsDir, "test.yaml")
	if err := os.WriteFile(promptFile, []byte(promptContent), 0644); err != nil {
		t.Fatalf("Failed to write test prompts: %v", err)
	}

	registry := NewPromptioRegistry(promptsDir)
	ctx := context.Background()

	// Test List all
	t.Run("List all", func(t *testing.T) {
		keys, err := registry.List(ctx, nil)
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(keys) != 3 {
			t.Errorf("Expected 3 prompts, got %d", len(keys))
		}
	})

	// Test List with tag filter
	t.Run("List with tag filter", func(t *testing.T) {
		keys, err := registry.List(ctx, map[string]string{"tag": "agent"})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(keys) != 2 {
			t.Errorf("Expected 2 prompts with 'agent' tag, got %d", len(keys))
		}

		if !contains(keys, "agent.system") || !contains(keys, "agent.tool") {
			t.Errorf("Expected 'agent.system' and 'agent.tool', got %v", keys)
		}
	})

	// Test List with prefix filter
	t.Run("List with prefix filter", func(t *testing.T) {
		keys, err := registry.List(ctx, map[string]string{"prefix": "agent."})
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(keys) != 2 {
			t.Errorf("Expected 2 prompts with 'agent.' prefix, got %d", len(keys))
		}

		if !contains(keys, "agent.system") || !contains(keys, "agent.tool") {
			t.Errorf("Expected 'agent.system' and 'agent.tool', got %v", keys)
		}
	})
}

func TestPromptioRegistry_Reload(t *testing.T) {
	// Create test prompts directory
	tempDir := t.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write initial prompt
	promptContent1 := `name: test
namespace: loom
prompts:
  - id: dynamic
    content: "Version 1"
    tags:
      - test
    metadata:
      version: "1.0.0"
`
	promptFile := filepath.Join(promptsDir, "test.yaml")
	if err := os.WriteFile(promptFile, []byte(promptContent1), 0644); err != nil {
		t.Fatalf("Failed to write test prompt: %v", err)
	}

	registry := NewPromptioRegistry(promptsDir)
	ctx := context.Background()

	// Get initial version
	result1, err := registry.Get(ctx, "dynamic", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if result1 != "Version 1" {
		t.Errorf("Expected 'Version 1', got %q", result1)
	}

	// Update prompt file
	promptContent2 := `name: test
namespace: loom
prompts:
  - id: dynamic
    content: "Version 2"
    tags:
      - test
    metadata:
      version: "2.0.0"
`
	if err := os.WriteFile(promptFile, []byte(promptContent2), 0644); err != nil {
		t.Fatalf("Failed to update test prompt: %v", err)
	}

	// Reload
	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Get updated version
	result2, err := registry.Get(ctx, "dynamic", nil)
	if err != nil {
		t.Fatalf("Get after reload failed: %v", err)
	}

	if result2 != "Version 2" {
		t.Errorf("Expected 'Version 2' after reload, got %q", result2)
	}
}

func TestPromptioRegistry_Watch(t *testing.T) {
	// Create test prompts directory
	tempDir := t.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Write test prompt
	promptContent := `name: test
namespace: loom
prompts:
  - id: test
    content: "Test"
    tags:
      - test
    metadata:
      version: "1.0.0"
`
	promptFile := filepath.Join(promptsDir, "test.yaml")
	if err := os.WriteFile(promptFile, []byte(promptContent), 0644); err != nil {
		t.Fatalf("Failed to write test prompt: %v", err)
	}

	registry := NewPromptioRegistry(promptsDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Watch now implements polling-based change detection
	ch, err := registry.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Channel should be open (polling goroutine running)
	select {
	case <-ch:
		t.Error("Expected watch channel to remain open (no updates yet)")
	case <-time.After(100 * time.Millisecond):
		// Good - channel is open and no updates sent yet
	}

	// Cancel context and verify channel closes
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Expected watch channel to close after context cancellation")
		}
	case <-time.After(1 * time.Second):
		t.Error("Watch channel did not close after context cancellation")
	}
}
