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

func TestFileRegistry_LoadAndGet(t *testing.T) {
	// Create temporary directory with test prompts
	tmpDir := t.TempDir()

	// Create test prompt file
	promptContent := `---
key: agent.system.base
version: 1.0.0
author: test@example.com
description: Test system prompt
tags: [agent, system]
variants: [default]
variables: [backend_type, session_id]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
You are a {{.backend_type}} agent for session {{.session_id}}.`

	// Write file
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "system.yaml"), []byte(promptContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create registry and load
	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	// Test Get with interpolation
	vars := map[string]interface{}{
		"backend_type": "Teradata",
		"session_id":   "sess-123",
	}
	result, err := registry.Get(ctx, "agent.system.base", vars)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}

	want := "You are a Teradata agent for session sess-123."
	if result != want {
		t.Errorf("Get() =\n%q\nwant\n%q", result, want)
	}
}

func TestFileRegistry_Variants(t *testing.T) {
	tmpDir := t.TempDir()

	// Create default variant
	defaultContent := `---
key: agent.system
version: 1.0.0
author: test@example.com
description: Test prompt
tags: [agent]
variants: [default, concise]
variables: [name]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Hello {{.name}}, this is the default variant.`

	// Create concise variant
	conciseContent := `---
key: agent.system
version: 1.0.0
author: test@example.com
description: Test prompt
tags: [agent]
variants: [default, concise]
variables: [name]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Hi {{.name}}!`

	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "system.yaml"), []byte(defaultContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "system.concise.yaml"), []byte(conciseContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	vars := map[string]interface{}{"name": "Alice"}

	// Test default variant
	result, err := registry.GetWithVariant(ctx, "agent.system", "default", vars)
	if err != nil {
		t.Fatalf("GetWithVariant(default) failed: %v", err)
	}
	if want := "Hello Alice, this is the default variant."; result != want {
		t.Errorf("default variant = %q, want %q", result, want)
	}

	// Test concise variant
	result, err = registry.GetWithVariant(ctx, "agent.system", "concise", vars)
	if err != nil {
		t.Fatalf("GetWithVariant(concise) failed: %v", err)
	}
	if want := "Hi Alice!"; result != want {
		t.Errorf("concise variant = %q, want %q", result, want)
	}
}

func TestFileRegistry_GetMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	promptContent := `---
key: test.prompt
version: 2.5.1
author: developer@example.com
description: A test prompt for metadata
tags: [test, metadata, example]
variants: [default, alt]
variables: [var1, var2, var3]
created_at: 2025-01-15T10:30:00Z
updated_at: 2025-01-16T14:45:00Z
---
Content here.`

	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(promptContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	metadata, err := registry.GetMetadata(ctx, "test.prompt")
	if err != nil {
		t.Fatalf("GetMetadata() failed: %v", err)
	}

	// Verify metadata fields
	if metadata.Key != "test.prompt" {
		t.Errorf("Key = %q, want %q", metadata.Key, "test.prompt")
	}
	if metadata.Version != "2.5.1" {
		t.Errorf("Version = %q, want %q", metadata.Version, "2.5.1")
	}
	if metadata.Author != "developer@example.com" {
		t.Errorf("Author = %q, want %q", metadata.Author, "developer@example.com")
	}
	if len(metadata.Tags) != 3 {
		t.Errorf("len(Tags) = %d, want 3", len(metadata.Tags))
	}
	if len(metadata.Variables) != 3 {
		t.Errorf("len(Variables) = %d, want 3", len(metadata.Variables))
	}
}

func TestFileRegistry_List(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple prompts with different tags
	prompts := []struct {
		filename string
		key      string
		tags     string
	}{
		{"agent1.yaml", "agent.system", "[agent, system]"},
		{"agent2.yaml", "agent.user", "[agent, user]"},
		{"tool1.yaml", "tool.execute", "[tool, sql]"},
		{"tool2.yaml", "tool.schema", "[tool, metadata]"},
	}

	for _, p := range prompts {
		content := `---
key: ` + p.key + `
version: 1.0.0
author: test@example.com
description: Test
tags: ` + p.tags + `
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Content.`
		if err := os.WriteFile(filepath.Join(tmpDir, p.filename), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	tests := []struct {
		name    string
		filters map[string]string
		want    int
	}{
		{"no filters", nil, 4},
		{"tag:agent", map[string]string{"tag": "agent"}, 2},
		{"tag:tool", map[string]string{"tag": "tool"}, 2},
		{"prefix:agent", map[string]string{"prefix": "agent."}, 2},
		{"prefix:tool", map[string]string{"prefix": "tool."}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := registry.List(ctx, tt.filters)
			if err != nil {
				t.Fatalf("List() failed: %v", err)
			}
			if len(keys) != tt.want {
				t.Errorf("List() returned %d keys, want %d", len(keys), tt.want)
			}
		})
	}
}

func TestFileRegistry_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	// Test missing key
	_, err := registry.Get(ctx, "nonexistent.key", nil)
	if err == nil {
		t.Error("Get() with missing key should return error")
	}

	// Test missing variant
	promptContent := `---
key: test.key
version: 1.0.0
author: test@example.com
description: Test
tags: []
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Content.`

	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(promptContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	_, err = registry.GetWithVariant(ctx, "test.key", "nonexistent", nil)
	if err == nil {
		t.Error("GetWithVariant() with missing variant should return error")
	}
}

func TestFileRegistry_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	// Should succeed even with no files
	if err := registry.Reload(ctx); err != nil {
		t.Errorf("Reload() on empty directory failed: %v", err)
	}

	keys, err := registry.List(ctx, nil)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List() returned %d keys, want 0", len(keys))
	}
}

func TestFileRegistry_NestedDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	agentDir := filepath.Join(tmpDir, "agent", "system")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	promptContent := `---
key: agent.system.base
version: 1.0.0
author: test@example.com
description: Test
tags: []
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Nested content.`

	if err := os.WriteFile(filepath.Join(agentDir, "base.yaml"), []byte(promptContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	result, err := registry.Get(ctx, "agent.system.base", nil)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}

	if result != "Nested content." {
		t.Errorf("Get() = %q, want %q", result, "Nested content.")
	}
}

func TestExtractVariant(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"system.yaml", "default"},
		{"system.yml", "default"},
		{"system.concise.yaml", "concise"},
		{"system.verbose.yaml", "verbose"},
		{"/path/to/system.yaml", "default"},
		{"/path/to/system.concise.yaml", "concise"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractVariant(tt.path)
			if got != tt.want {
				t.Errorf("extractVariant(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFileRegistry_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()

	promptContent := `---
key: concurrent.test
version: 1.0.0
author: test@example.com
description: Test
tags: []
variants: [default]
variables: [id]
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
ID: {{.id}}`

	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(promptContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	// Concurrent reads
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				vars := map[string]interface{}{"id": id}
				_, err := registry.Get(ctx, "concurrent.test", vars)
				if err != nil {
					t.Errorf("Concurrent Get() failed: %v", err)
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestFileRegistry_Reload(t *testing.T) {
	tmpDir := t.TempDir()

	// Initial content
	content1 := `---
key: reload.test
version: 1.0.0
author: test@example.com
description: Test
tags: []
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Version 1`

	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(content1), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewFileRegistry(tmpDir)
	ctx := context.Background()

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("First Reload() failed: %v", err)
	}

	result, err := registry.Get(ctx, "reload.test", nil)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	if result != "Version 1" {
		t.Errorf("First load = %q, want %q", result, "Version 1")
	}

	// Update file
	time.Sleep(10 * time.Millisecond) // Ensure different mtime
	content2 := `---
key: reload.test
version: 2.0.0
author: test@example.com
description: Test
tags: []
variants: [default]
variables: []
created_at: 2025-01-01T00:00:00Z
updated_at: 2025-01-01T00:00:00Z
---
Version 2`

	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(content2), 0644); err != nil {
		t.Fatal(err)
	}

	if err := registry.Reload(ctx); err != nil {
		t.Fatalf("Second Reload() failed: %v", err)
	}

	result, err = registry.Get(ctx, "reload.test", nil)
	if err != nil {
		t.Fatalf("Get() after reload failed: %v", err)
	}
	if result != "Version 2" {
		t.Errorf("After reload = %q, want %q", result, "Version 2")
	}
}
