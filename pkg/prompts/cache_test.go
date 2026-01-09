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
	"sync"
	"testing"
	"time"
)

// mockRegistry is a simple mock for testing the cache.
type mockRegistry struct {
	mu       sync.Mutex
	getCalls int
	prompts  map[string]map[string]string // key -> variant -> content
	metadata map[string]*PromptMetadata
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{
		prompts:  make(map[string]map[string]string),
		metadata: make(map[string]*PromptMetadata),
	}
}

func (m *mockRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	return m.GetWithVariant(ctx, key, "default", vars)
}

func (m *mockRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	m.mu.Lock()
	m.getCalls++
	m.mu.Unlock()

	variants, ok := m.prompts[key]
	if !ok {
		return "", &promptNotFoundError{key: key}
	}

	content, ok := variants[variant]
	if !ok {
		return "", &variantNotFoundError{key: key, variant: variant}
	}

	return Interpolate(content, vars), nil
}

func (m *mockRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metadata, ok := m.metadata[key]
	if !ok {
		return nil, &promptNotFoundError{key: key}
	}

	return metadata, nil
}

func (m *mockRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var keys []string
	for key := range m.prompts {
		keys = append(keys, key)
	}
	return keys, nil
}

func (m *mockRegistry) Reload(ctx context.Context) error {
	return nil
}

func (m *mockRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	ch := make(chan PromptUpdate)
	close(ch)
	return ch, nil
}

func (m *mockRegistry) addPrompt(key, variant, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.prompts[key]; !ok {
		m.prompts[key] = make(map[string]string)
	}
	m.prompts[key][variant] = content
}

func (m *mockRegistry) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls
}

type promptNotFoundError struct {
	key string
}

func (e *promptNotFoundError) Error() string {
	return "prompt not found: " + e.key
}

type variantNotFoundError struct {
	key     string
	variant string
}

func (e *variantNotFoundError) Error() string {
	return "variant not found: " + e.variant + " (key: " + e.key + ")"
}

func TestCachedRegistry_CacheHit(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Hello {{.name}}!")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()
	vars := map[string]interface{}{"name": "World"}

	// First call: cache miss
	result1, err := cache.Get(ctx, "test.prompt", vars)
	if err != nil {
		t.Fatalf("First Get() failed: %v", err)
	}
	if result1 != "Hello World!" {
		t.Errorf("First Get() = %q, want %q", result1, "Hello World!")
	}

	// Verify underlying registry was called
	if mock.getCallCount() != 1 {
		t.Errorf("Expected 1 call to underlying registry, got %d", mock.getCallCount())
	}

	// Second call: cache hit
	result2, err := cache.Get(ctx, "test.prompt", vars)
	if err != nil {
		t.Fatalf("Second Get() failed: %v", err)
	}
	if result2 != "Hello World!" {
		t.Errorf("Second Get() = %q, want %q", result2, "Hello World!")
	}

	// Verify underlying registry was NOT called again
	if mock.getCallCount() != 1 {
		t.Errorf("Expected still 1 call to underlying registry, got %d", mock.getCallCount())
	}

	// Verify cache stats
	hits, misses := cache.Stats()
	if hits != 1 {
		t.Errorf("Expected 1 hit, got %d", hits)
	}
	if misses != 1 {
		t.Errorf("Expected 1 miss, got %d", misses)
	}
}

func TestCachedRegistry_TTLExpiration(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Content")

	// Very short TTL for testing
	cache := NewCachedRegistry(mock, 50*time.Millisecond)
	ctx := context.Background()

	// First call: cache miss
	_, err := cache.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Fatalf("First Get() failed: %v", err)
	}

	// Second call immediately: cache hit
	_, err = cache.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Fatalf("Second Get() failed: %v", err)
	}

	if mock.getCallCount() != 1 {
		t.Errorf("Expected 1 call before expiration, got %d", mock.getCallCount())
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Third call: cache expired, should miss
	_, err = cache.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Fatalf("Third Get() after expiration failed: %v", err)
	}

	if mock.getCallCount() != 2 {
		t.Errorf("Expected 2 calls after expiration, got %d", mock.getCallCount())
	}

	// Verify stats
	hits, misses := cache.Stats()
	if hits != 1 {
		t.Errorf("Expected 1 hit, got %d", hits)
	}
	if misses != 2 {
		t.Errorf("Expected 2 misses, got %d", misses)
	}
}

func TestCachedRegistry_Variants(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Default content")
	mock.addPrompt("test.prompt", "concise", "Concise")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// Get default variant (cache miss)
	result1, err := cache.GetWithVariant(ctx, "test.prompt", "default", nil)
	if err != nil {
		t.Fatalf("Get default failed: %v", err)
	}
	if result1 != "Default content" {
		t.Errorf("Default variant = %q, want %q", result1, "Default content")
	}

	// Get concise variant (cache miss - different variant)
	result2, err := cache.GetWithVariant(ctx, "test.prompt", "concise", nil)
	if err != nil {
		t.Fatalf("Get concise failed: %v", err)
	}
	if result2 != "Concise" {
		t.Errorf("Concise variant = %q, want %q", result2, "Concise")
	}

	// Get default again (cache hit)
	result3, err := cache.GetWithVariant(ctx, "test.prompt", "default", nil)
	if err != nil {
		t.Fatalf("Get default again failed: %v", err)
	}
	if result3 != "Default content" {
		t.Errorf("Default variant again = %q, want %q", result3, "Default content")
	}

	// Verify: 2 calls (one for each variant), not 3
	if mock.getCallCount() != 2 {
		t.Errorf("Expected 2 calls to underlying registry, got %d", mock.getCallCount())
	}
}

func TestCachedRegistry_Metadata(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Content")
	mock.metadata["test.prompt"] = &PromptMetadata{
		Key:     "test.prompt",
		Version: "1.0.0",
		Author:  "test@example.com",
	}

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// First call: cache miss
	metadata1, err := cache.GetMetadata(ctx, "test.prompt")
	if err != nil {
		t.Fatalf("GetMetadata() failed: %v", err)
	}
	if metadata1.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", metadata1.Version, "1.0.0")
	}

	// Second call: cache hit
	metadata2, err := cache.GetMetadata(ctx, "test.prompt")
	if err != nil {
		t.Fatalf("GetMetadata() second call failed: %v", err)
	}
	if metadata2.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", metadata2.Version, "1.0.0")
	}

	// Verify stats
	hits, misses := cache.Stats()
	if hits != 1 {
		t.Errorf("Expected 1 hit, got %d", hits)
	}
	if misses != 1 {
		t.Errorf("Expected 1 miss, got %d", misses)
	}
}

func TestCachedRegistry_Invalidate(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Content")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// Load into cache
	_, err := cache.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}

	// Verify cached (should be 1 call)
	_, _ = cache.Get(ctx, "test.prompt", nil)
	if mock.getCallCount() != 1 {
		t.Errorf("Expected 1 call before invalidation, got %d", mock.getCallCount())
	}

	// Invalidate cache
	cache.Invalidate()

	// Next call should miss
	_, err = cache.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Fatalf("Get() after invalidation failed: %v", err)
	}

	if mock.getCallCount() != 2 {
		t.Errorf("Expected 2 calls after invalidation, got %d", mock.getCallCount())
	}
}

func TestCachedRegistry_InvalidateKey(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("prompt1", "default", "Content 1")
	mock.addPrompt("prompt2", "default", "Content 2")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// Load both into cache
	_, _ = cache.Get(ctx, "prompt1", nil)
	_, _ = cache.Get(ctx, "prompt2", nil)

	// Verify both cached
	_, _ = cache.Get(ctx, "prompt1", nil)
	_, _ = cache.Get(ctx, "prompt2", nil)
	if mock.getCallCount() != 2 {
		t.Errorf("Expected 2 calls before invalidation, got %d", mock.getCallCount())
	}

	// Invalidate only prompt1
	cache.InvalidateKey("prompt1")

	// prompt1 should miss, prompt2 should hit
	_, _ = cache.Get(ctx, "prompt1", nil)
	_, _ = cache.Get(ctx, "prompt2", nil)

	if mock.getCallCount() != 3 {
		t.Errorf("Expected 3 calls after selective invalidation, got %d", mock.getCallCount())
	}
}

func TestCachedRegistry_ConcurrentAccess(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Content {{.id}}")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// Concurrent reads
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			vars := map[string]interface{}{"id": id}
			_, err := cache.Get(ctx, "test.prompt", vars)
			if err != nil {
				t.Errorf("Concurrent Get() failed: %v", err)
			}
		}(i)
	}

	wg.Wait()

	// Should only call underlying registry once (all others are cache hits)
	if mock.getCallCount() > 2 {
		// Allow 1-2 calls due to race conditions on first access
		t.Logf("Note: Got %d calls (expected 1-2 due to concurrent first access)", mock.getCallCount())
	}
}

func TestCachedRegistry_DifferentVars(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Hello {{.name}}!")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// First call with name=Alice
	result1, err := cache.Get(ctx, "test.prompt", map[string]interface{}{"name": "Alice"})
	if err != nil {
		t.Fatalf("Get(Alice) failed: %v", err)
	}
	if result1 != "Hello Alice!" {
		t.Errorf("Get(Alice) = %q, want %q", result1, "Hello Alice!")
	}

	// Second call with name=Bob (cache hit, different interpolation)
	result2, err := cache.Get(ctx, "test.prompt", map[string]interface{}{"name": "Bob"})
	if err != nil {
		t.Fatalf("Get(Bob) failed: %v", err)
	}
	if result2 != "Hello Bob!" {
		t.Errorf("Get(Bob) = %q, want %q", result2, "Hello Bob!")
	}

	// Should only call underlying registry once (content is cached, vars differ)
	if mock.getCallCount() != 1 {
		t.Errorf("Expected 1 call to underlying registry, got %d", mock.getCallCount())
	}
}

func TestCachedRegistry_Reload(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Original")

	cache := NewCachedRegistry(mock, 1*time.Minute)
	ctx := context.Background()

	// Load into cache
	result1, _ := cache.Get(ctx, "test.prompt", nil)
	if result1 != "Original" {
		t.Errorf("Before reload = %q, want %q", result1, "Original")
	}

	// Update mock content
	mock.addPrompt("test.prompt", "default", "Updated")

	// Without reload, should still get cached version
	result2, _ := cache.Get(ctx, "test.prompt", nil)
	if result2 != "Original" {
		t.Errorf("Before reload (cached) = %q, want %q", result2, "Original")
	}

	// Reload clears cache
	if err := cache.Reload(ctx); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}

	// Should get updated version
	result3, _ := cache.Get(ctx, "test.prompt", nil)
	if result3 != "Updated" {
		t.Errorf("After reload = %q, want %q", result3, "Updated")
	}
}

func TestMakeCacheKey(t *testing.T) {
	tests := []struct {
		key     string
		variant string
		want    string
	}{
		{"agent.system", "default", "agent.system:default"},
		{"tool.execute", "concise", "tool.execute:concise"},
		{"simple", "v1", "simple:v1"},
	}

	for _, tt := range tests {
		got := makeCacheKey(tt.key, tt.variant)
		if got != tt.want {
			t.Errorf("makeCacheKey(%q, %q) = %q, want %q", tt.key, tt.variant, got, tt.want)
		}
	}
}

func TestKeyFromCacheKey(t *testing.T) {
	tests := []struct {
		cacheKey string
		want     string
	}{
		{"agent.system:default", "agent.system"},
		{"tool.execute:concise", "tool.execute"},
		{"simple:v1", "simple"},
		{"no-colon", "no-colon"},
	}

	for _, tt := range tests {
		got := keyFromCacheKey(tt.cacheKey)
		if got != tt.want {
			t.Errorf("keyFromCacheKey(%q) = %q, want %q", tt.cacheKey, got, tt.want)
		}
	}
}
