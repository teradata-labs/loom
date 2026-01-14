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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkFileRegistry_Get measures prompt loading performance from FileRegistry
func BenchmarkFileRegistry_Get(b *testing.B) {
	// Create test directory
	tempDir := b.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts", "agent")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		b.Fatalf("Failed to create test directory: %v", err)
	}

	// Write test prompt
	promptContent := `name: agent
namespace: loom
prompts:
  - id: system
    content: |
      Help users with {{.backend_type}}. You have {{.tool_count}} tools available.
    variables:
      backend_type:
        type: 1
        required: true
      tool_count:
        type: 2
        required: true
    tags:
      - agent
      - system
`
	if err := os.WriteFile(filepath.Join(promptsDir, "system.yaml"), []byte(promptContent), 0644); err != nil {
		b.Fatalf("Failed to write test prompt: %v", err)
	}

	registry := NewFileRegistry(filepath.Join(tempDir, "prompts"))
	if err := registry.Reload(context.Background()); err != nil {
		b.Fatalf("Failed to reload: %v", err)
	}

	vars := map[string]interface{}{
		"backend_type": "teradata",
		"tool_count":   5,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := registry.Get(context.Background(), "agent.system", vars)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileRegistry_GetWithVariant measures variant loading performance
func BenchmarkFileRegistry_GetWithVariant(b *testing.B) {
	// Create test directory
	tempDir := b.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts", "agent")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		b.Fatalf("Failed to create test directory: %v", err)
	}

	// Write default prompt
	defaultContent := `name: agent
namespace: loom
prompts:
  - id: system
    content: "Default system prompt"
`
	if err := os.WriteFile(filepath.Join(promptsDir, "system.yaml"), []byte(defaultContent), 0644); err != nil {
		b.Fatalf("Failed to write default prompt: %v", err)
	}

	// Write variant prompt
	variantContent := `name: agent
namespace: loom
prompts:
  - id: system
    content: "Concise system prompt"
`
	if err := os.WriteFile(filepath.Join(promptsDir, "system.concise.yaml"), []byte(variantContent), 0644); err != nil {
		b.Fatalf("Failed to write variant prompt: %v", err)
	}

	registry := NewFileRegistry(filepath.Join(tempDir, "prompts"))
	if err := registry.Reload(context.Background()); err != nil {
		b.Fatalf("Failed to reload: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := registry.GetWithVariant(context.Background(), "agent.system", "concise", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileRegistry_Reload measures full reload performance
func BenchmarkFileRegistry_Reload(b *testing.B) {
	// Create test directory with multiple prompts
	tempDir := b.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts")

	// Create 10 prompt files (simulate realistic load)
	for i := 0; i < 10; i++ {
		dir := filepath.Join(promptsDir, "test")
		if err := os.MkdirAll(dir, 0755); err != nil {
			b.Fatalf("Failed to create directory: %v", err)
		}

		content := `name: test
namespace: loom
prompts:
  - id: test
    content: "Test prompt content"
`
		filename := filepath.Join(dir, "test.yaml")
		if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
			b.Fatalf("Failed to write prompt: %v", err)
		}
	}

	registry := NewFileRegistry(promptsDir)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := registry.Reload(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCachedRegistry_Get measures caching overhead
func BenchmarkCachedRegistry_Get(b *testing.B) {
	// Create test directory
	tempDir := b.TempDir()
	promptsDir := filepath.Join(tempDir, "prompts", "agent")
	if err := os.MkdirAll(promptsDir, 0755); err != nil {
		b.Fatalf("Failed to create test directory: %v", err)
	}

	// Write test prompt
	promptContent := `name: agent
namespace: loom
prompts:
  - id: system
    content: "System prompt"
`
	if err := os.WriteFile(filepath.Join(promptsDir, "system.yaml"), []byte(promptContent), 0644); err != nil {
		b.Fatalf("Failed to write test prompt: %v", err)
	}

	baseRegistry := NewFileRegistry(filepath.Join(tempDir, "prompts"))
	if err := baseRegistry.Reload(context.Background()); err != nil {
		b.Fatalf("Failed to reload: %v", err)
	}

	// Wrap with cache (5 minute TTL)
	registry := NewCachedRegistry(baseRegistry, 5*time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := registry.Get(context.Background(), "agent.system", nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
