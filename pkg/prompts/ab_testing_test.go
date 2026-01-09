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
	"testing"
)

func TestExplicitSelector(t *testing.T) {
	selector := NewExplicitSelector("concise")
	ctx := context.Background()
	variants := []string{"default", "concise", "verbose"}

	variant, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
	if err != nil {
		t.Fatalf("SelectVariant() failed: %v", err)
	}

	if variant != "concise" {
		t.Errorf("SelectVariant() = %q, want %q", variant, "concise")
	}
}

func TestExplicitSelector_NotFound(t *testing.T) {
	selector := NewExplicitSelector("nonexistent")
	ctx := context.Background()
	variants := []string{"default", "concise"}

	_, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
	if err == nil {
		t.Error("SelectVariant() should fail for nonexistent variant")
	}
}

func TestHashSelector_Consistency(t *testing.T) {
	selector := NewHashSelector()
	ctx := context.Background()
	variants := []string{"default", "concise", "verbose"}

	// Same session should get same variant multiple times
	variant1, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
	if err != nil {
		t.Fatalf("First SelectVariant() failed: %v", err)
	}

	variant2, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
	if err != nil {
		t.Fatalf("Second SelectVariant() failed: %v", err)
	}

	if variant1 != variant2 {
		t.Errorf("Hash selector not consistent: got %q then %q", variant1, variant2)
	}
}

func TestHashSelector_Distribution(t *testing.T) {
	selector := NewHashSelector()
	ctx := context.Background()
	variants := []string{"default", "concise", "verbose"}

	// Count distribution across many sessions
	counts := make(map[string]int)
	for i := 0; i < 300; i++ {
		sessionID := generateSessionID(i)
		variant, err := selector.SelectVariant(ctx, "test.key", variants, sessionID)
		if err != nil {
			t.Fatalf("SelectVariant() failed: %v", err)
		}
		counts[variant]++
	}

	// Each variant should get roughly 1/3 of sessions
	// Allow 60-140 per variant (40% tolerance)
	for variant, count := range counts {
		if count < 60 || count > 140 {
			t.Errorf("Variant %q got %d selections (expected ~100)", variant, count)
		}
	}
}

func TestHashSelector_DifferentKeys(t *testing.T) {
	selector := NewHashSelector()
	ctx := context.Background()
	variants := []string{"v1", "v2"}

	// Same session, different keys, might get different variants
	variant1, _ := selector.SelectVariant(ctx, "key1", variants, "sess-123")
	variant2, _ := selector.SelectVariant(ctx, "key2", variants, "sess-123")

	// They might be the same or different - just verify both are valid
	validVariants := map[string]bool{"v1": true, "v2": true}
	if !validVariants[variant1] || !validVariants[variant2] {
		t.Errorf("Got invalid variants: %q, %q", variant1, variant2)
	}
}

func TestRandomSelector(t *testing.T) {
	selector := NewRandomSelector(12345) // Fixed seed for reproducibility
	ctx := context.Background()
	variants := []string{"default", "concise"}

	// With fixed seed, we get predictable results
	results := make([]string, 10)
	for i := 0; i < 10; i++ {
		variant, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
		if err != nil {
			t.Fatalf("SelectVariant() failed: %v", err)
		}
		results[i] = variant
	}

	// With fixed seed, should get some variation (not all the same)
	allSame := true
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			allSame = false
			break
		}
	}

	if allSame {
		t.Error("Random selector returned same variant 10 times (unlikely)")
	}
}

func TestRandomSelector_Distribution(t *testing.T) {
	selector := NewRandomSelector(0) // Random seed
	ctx := context.Background()
	variants := []string{"default", "concise"}

	// Count distribution
	counts := make(map[string]int)
	for i := 0; i < 1000; i++ {
		variant, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
		if err != nil {
			t.Fatalf("SelectVariant() failed: %v", err)
		}
		counts[variant]++
	}

	// Each variant should get roughly 50% (allow 40-60%)
	for variant, count := range counts {
		if count < 400 || count > 600 {
			t.Errorf("Variant %q got %d selections (expected ~500)", variant, count)
		}
	}
}

func TestWeightedSelector_80_20(t *testing.T) {
	weights := map[string]int{
		"default":      80,
		"experimental": 20,
	}
	selector := NewWeightedSelector(weights, 12345)
	ctx := context.Background()
	variants := []string{"default", "experimental"}

	// Count distribution
	counts := make(map[string]int)
	for i := 0; i < 1000; i++ {
		variant, err := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
		if err != nil {
			t.Fatalf("SelectVariant() failed: %v", err)
		}
		counts[variant]++
	}

	// Default should get ~80% (allow 75-85%)
	defaultCount := counts["default"]
	if defaultCount < 750 || defaultCount > 850 {
		t.Errorf("Default got %d selections (expected ~800)", defaultCount)
	}

	// Experimental should get ~20% (allow 15-25%)
	expCount := counts["experimental"]
	if expCount < 150 || expCount > 250 {
		t.Errorf("Experimental got %d selections (expected ~200)", expCount)
	}
}

func TestWeightedSelector_RelativeWeights(t *testing.T) {
	// Weights don't need to sum to 100
	weights := map[string]int{
		"v1": 4, // 80%
		"v2": 1, // 20%
	}
	selector := NewWeightedSelector(weights, 12345)
	ctx := context.Background()
	variants := []string{"v1", "v2"}

	counts := make(map[string]int)
	for i := 0; i < 500; i++ {
		variant, _ := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
		counts[variant]++
	}

	// v1 should get ~80%
	v1Count := counts["v1"]
	if v1Count < 350 || v1Count > 450 {
		t.Errorf("v1 got %d selections (expected ~400)", v1Count)
	}
}

func TestWeightedSelector_NoWeights(t *testing.T) {
	// No weights defined - should fall back to uniform
	selector := NewWeightedSelector(map[string]int{}, 12345)
	ctx := context.Background()
	variants := []string{"v1", "v2"}

	counts := make(map[string]int)
	for i := 0; i < 200; i++ {
		variant, _ := selector.SelectVariant(ctx, "test.key", variants, "sess-123")
		counts[variant]++
	}

	// Should be roughly even (allow 40-60%)
	for variant, count := range counts {
		if count < 80 || count > 120 {
			t.Errorf("Variant %q got %d selections (expected ~100)", variant, count)
		}
	}
}

func TestABTestingRegistry_WithHashSelector(t *testing.T) {
	// Create mock registry
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Default: {{.name}}")
	mock.addPrompt("test.prompt", "concise", "Concise: {{.name}}")
	mock.metadata["test.prompt"] = &PromptMetadata{
		Key:      "test.prompt",
		Variants: []string{"default", "concise"},
	}

	// Wrap with A/B testing
	selector := NewHashSelector()
	abRegistry := NewABTestingRegistry(mock, selector)

	ctx := context.Background()
	vars := map[string]interface{}{"name": "Alice"}

	// Same session should get same variant
	result1, err := abRegistry.GetForSession(ctx, "test.prompt", "sess-123", vars)
	if err != nil {
		t.Fatalf("GetForSession() failed: %v", err)
	}

	result2, err := abRegistry.GetForSession(ctx, "test.prompt", "sess-123", vars)
	if err != nil {
		t.Fatalf("GetForSession() second call failed: %v", err)
	}

	if result1 != result2 {
		t.Errorf("Same session got different variants: %q vs %q", result1, result2)
	}
}

func TestABTestingRegistry_WithExplicitSelector(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Default")
	mock.addPrompt("test.prompt", "concise", "Concise")
	mock.metadata["test.prompt"] = &PromptMetadata{
		Key:      "test.prompt",
		Variants: []string{"default", "concise"},
	}

	// Explicit selector always returns "concise"
	selector := NewExplicitSelector("concise")
	abRegistry := NewABTestingRegistry(mock, selector)

	ctx := context.Background()
	result, err := abRegistry.GetForSession(ctx, "test.prompt", "sess-123", nil)
	if err != nil {
		t.Fatalf("GetForSession() failed: %v", err)
	}

	if result != "Concise" {
		t.Errorf("Got %q, want %q", result, "Concise")
	}
}

func TestABTestingRegistry_WithContextSessionID(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Content")
	mock.metadata["test.prompt"] = &PromptMetadata{
		Key:      "test.prompt",
		Variants: []string{"default"},
	}

	selector := NewHashSelector()
	abRegistry := NewABTestingRegistry(mock, selector)

	// Add session ID to context
	ctx := WithSessionID(context.Background(), "sess-456")

	_, err := abRegistry.Get(ctx, "test.prompt", nil)
	if err != nil {
		t.Errorf("Get() with context session ID failed: %v", err)
	}
}

func TestABTestingRegistry_GetWithVariant_BypassesSelector(t *testing.T) {
	mock := newMockRegistry()
	mock.addPrompt("test.prompt", "default", "Default")
	mock.addPrompt("test.prompt", "concise", "Concise")
	mock.metadata["test.prompt"] = &PromptMetadata{
		Key:      "test.prompt",
		Variants: []string{"default", "concise"},
	}

	// Selector that always chooses "default"
	selector := NewExplicitSelector("default")
	abRegistry := NewABTestingRegistry(mock, selector)

	ctx := context.Background()

	// But we explicitly request "concise"
	result, err := abRegistry.GetWithVariant(ctx, "test.prompt", "concise", nil)
	if err != nil {
		t.Fatalf("GetWithVariant() failed: %v", err)
	}

	if result != "Concise" {
		t.Errorf("GetWithVariant() = %q, want %q", result, "Concise")
	}
}

func TestWithSessionID(t *testing.T) {
	ctx := context.Background()
	ctx = WithSessionID(ctx, "sess-789")

	sessionID := GetSessionIDFromContext(ctx)
	if sessionID != "sess-789" {
		t.Errorf("GetSessionIDFromContext() = %q, want %q", sessionID, "sess-789")
	}
}

func TestGetSessionIDFromContext_Default(t *testing.T) {
	ctx := context.Background()
	sessionID := GetSessionIDFromContext(ctx)

	if sessionID != "default" {
		t.Errorf("GetSessionIDFromContext() with no session = %q, want %q", sessionID, "default")
	}
}

func TestHashSelector_EmptyVariants(t *testing.T) {
	selector := NewHashSelector()
	ctx := context.Background()

	_, err := selector.SelectVariant(ctx, "test.key", []string{}, "sess-123")
	if err == nil {
		t.Error("SelectVariant() with empty variants should return error")
	}
}

func TestRandomSelector_EmptyVariants(t *testing.T) {
	selector := NewRandomSelector(0)
	ctx := context.Background()

	_, err := selector.SelectVariant(ctx, "test.key", []string{}, "sess-123")
	if err == nil {
		t.Error("SelectVariant() with empty variants should return error")
	}
}

func TestWeightedSelector_EmptyVariants(t *testing.T) {
	selector := NewWeightedSelector(map[string]int{"v1": 1}, 0)
	ctx := context.Background()

	_, err := selector.SelectVariant(ctx, "test.key", []string{}, "sess-123")
	if err == nil {
		t.Error("SelectVariant() with empty variants should return error")
	}
}

// Helper function to generate predictable session IDs for testing
func generateSessionID(i int) string {
	return "sess-" + string(rune('A'+i%26)) + string(rune('0'+i%10))
}
