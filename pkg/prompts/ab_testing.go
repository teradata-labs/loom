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
	"hash/fnv"
	"math/rand"
)

// VariantSelector determines which prompt variant to use for A/B testing.
//
// Strategies:
//   - Explicit: User specifies the variant (no selection logic)
//   - Hash-based: Deterministic based on session ID (consistent experience)
//   - Random: Random selection (true A/B testing)
//   - Weighted: Weighted random (e.g., 80% default, 20% experimental)
type VariantSelector interface {
	// SelectVariant chooses a variant from the available options.
	SelectVariant(ctx context.Context, key string, variants []string, sessionID string) (string, error)
}

// ExplicitSelector always returns the specified variant.
//
// Example:
//
//	selector := prompts.NewExplicitSelector("concise")
//	variant, _ := selector.SelectVariant(ctx, "agent.system", [...], "sess-123")
//	// Returns: "concise"
type ExplicitSelector struct {
	variant string
}

// NewExplicitSelector creates a selector that always returns the specified variant.
func NewExplicitSelector(variant string) *ExplicitSelector {
	return &ExplicitSelector{variant: variant}
}

func (s *ExplicitSelector) SelectVariant(ctx context.Context, key string, variants []string, sessionID string) (string, error) {
	// Verify variant exists
	for _, v := range variants {
		if v == s.variant {
			return s.variant, nil
		}
	}
	return "", fmt.Errorf("variant %q not found in available variants: %v", s.variant, variants)
}

// HashSelector uses consistent hashing based on session ID.
// Same session always gets the same variant (deterministic).
//
// Example:
//
//	selector := prompts.NewHashSelector()
//	variant, _ := selector.SelectVariant(ctx, "agent.system", ["default", "concise"], "sess-123")
//	// sess-123 always gets the same variant
type HashSelector struct{}

// NewHashSelector creates a hash-based selector.
func NewHashSelector() *HashSelector {
	return &HashSelector{}
}

func (s *HashSelector) SelectVariant(ctx context.Context, key string, variants []string, sessionID string) (string, error) {
	if len(variants) == 0 {
		return "", fmt.Errorf("no variants available")
	}

	// Hash session ID + key for deterministic selection
	h := fnv.New64a()
	h.Write([]byte(sessionID))
	h.Write([]byte(key))
	hashValue := h.Sum64()

	// Map hash to variant index
	idx := int(hashValue % uint64(len(variants)))
	return variants[idx], nil
}

// RandomSelector randomly selects a variant (uniform distribution).
//
// Example:
//
//	selector := prompts.NewRandomSelector()
//	variant, _ := selector.SelectVariant(ctx, "agent.system", ["default", "concise"], "sess-123")
//	// 50% chance of each variant
type RandomSelector struct {
	rng *rand.Rand
}

// NewRandomSelector creates a random selector with a given seed.
// Pass 0 for a random seed based on current time.
// Note: Uses math/rand (not crypto/rand) as A/B testing doesn't require cryptographic randomness.
func NewRandomSelector(seed int64) *RandomSelector {
	var rng *rand.Rand
	if seed == 0 {
		// #nosec G404 -- A/B testing statistical distribution doesn't need crypto randomness
		rng = rand.New(rand.NewSource(rand.Int63()))
	} else {
		// #nosec G404 -- A/B testing statistical distribution doesn't need crypto randomness
		rng = rand.New(rand.NewSource(seed))
	}
	return &RandomSelector{rng: rng}
}

func (s *RandomSelector) SelectVariant(ctx context.Context, key string, variants []string, sessionID string) (string, error) {
	if len(variants) == 0 {
		return "", fmt.Errorf("no variants available")
	}

	idx := s.rng.Intn(len(variants))
	return variants[idx], nil
}

// WeightedSelector randomly selects based on weights.
//
// Example:
//
//	// 80% default, 20% experimental
//	selector := prompts.NewWeightedSelector(map[string]int{
//	    "default": 80,
//	    "experimental": 20,
//	})
type WeightedSelector struct {
	weights map[string]int
	rng     *rand.Rand
}

// NewWeightedSelector creates a weighted random selector.
// Weights don't need to sum to 100 - they're relative.
//
// Example:
//
//	selector := prompts.NewWeightedSelector(map[string]int{
//	    "default": 4,      // 80% (4/5)
//	    "experimental": 1, // 20% (1/5)
//	})
func NewWeightedSelector(weights map[string]int, seed int64) *WeightedSelector {
	var rng *rand.Rand
	if seed == 0 {
		// #nosec G404 -- A/B testing statistical distribution doesn't need crypto randomness
		rng = rand.New(rand.NewSource(rand.Int63()))
	} else {
		// #nosec G404 -- A/B testing statistical distribution doesn't need crypto randomness
		rng = rand.New(rand.NewSource(seed))
	}
	return &WeightedSelector{
		weights: weights,
		rng:     rng,
	}
}

func (s *WeightedSelector) SelectVariant(ctx context.Context, key string, variants []string, sessionID string) (string, error) {
	if len(variants) == 0 {
		return "", fmt.Errorf("no variants available")
	}

	// Calculate total weight for available variants
	totalWeight := 0
	availableWeights := make(map[string]int)
	for _, variant := range variants {
		if weight, ok := s.weights[variant]; ok {
			availableWeights[variant] = weight
			totalWeight += weight
		}
	}

	// If no weights defined for any variant, fall back to uniform
	if totalWeight == 0 {
		idx := s.rng.Intn(len(variants))
		return variants[idx], nil
	}

	// Select based on weights
	roll := s.rng.Intn(totalWeight)
	cumulative := 0
	for _, variant := range variants {
		if weight, ok := availableWeights[variant]; ok {
			cumulative += weight
			if roll < cumulative {
				return variant, nil
			}
		}
	}

	// Fallback (shouldn't reach here)
	return variants[0], nil
}

// ABTestingRegistry wraps a PromptRegistry with automatic variant selection.
//
// Example:
//
//	fileRegistry := prompts.NewFileRegistry("./prompts")
//	selector := prompts.NewHashSelector() // Consistent per session
//	abRegistry := prompts.NewABTestingRegistry(fileRegistry, selector)
//
//	// Automatically selects variant based on session ID
//	prompt, _ := abRegistry.GetForSession(ctx, "agent.system", "sess-123", vars)
type ABTestingRegistry struct {
	underlying PromptRegistry
	selector   VariantSelector
}

// NewABTestingRegistry creates an A/B testing registry wrapper.
func NewABTestingRegistry(underlying PromptRegistry, selector VariantSelector) *ABTestingRegistry {
	return &ABTestingRegistry{
		underlying: underlying,
		selector:   selector,
	}
}

// Get retrieves a prompt by key with automatic variant selection based on context.
// Uses "default" as session ID if not found in context.
func (r *ABTestingRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	sessionID := GetSessionIDFromContext(ctx)
	return r.GetForSession(ctx, key, sessionID, vars)
}

// GetWithVariant retrieves a specific variant (bypasses selector).
func (r *ABTestingRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	return r.underlying.GetWithVariant(ctx, key, variant, vars)
}

// GetForSession retrieves a prompt with variant selection based on session ID.
func (r *ABTestingRegistry) GetForSession(ctx context.Context, key string, sessionID string, vars map[string]interface{}) (string, error) {
	// Get metadata to find available variants
	metadata, err := r.underlying.GetMetadata(ctx, key)
	if err != nil {
		return "", err
	}

	// Select variant
	variant, err := r.selector.SelectVariant(ctx, key, metadata.Variants, sessionID)
	if err != nil {
		return "", err
	}

	// Get prompt with selected variant
	return r.underlying.GetWithVariant(ctx, key, variant, vars)
}

// GetMetadata retrieves prompt metadata without the content.
func (r *ABTestingRegistry) GetMetadata(ctx context.Context, key string) (*PromptMetadata, error) {
	return r.underlying.GetMetadata(ctx, key)
}

// List lists all available prompt keys, optionally filtered.
func (r *ABTestingRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	return r.underlying.List(ctx, filters)
}

// Reload reloads prompts from the underlying registry.
func (r *ABTestingRegistry) Reload(ctx context.Context) error {
	return r.underlying.Reload(ctx)
}

// Watch returns a channel that receives updates when prompts change.
func (r *ABTestingRegistry) Watch(ctx context.Context) (<-chan PromptUpdate, error) {
	return r.underlying.Watch(ctx)
}

// Context key for session ID
type contextKey string

const sessionIDKey contextKey = "session_id"

// WithSessionID adds a session ID to the context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// GetSessionIDFromContext retrieves the session ID from context.
// Returns "default" if not found.
// This is exported so other packages (like patterns) can use the same session ID logic.
func GetSessionIDFromContext(ctx context.Context) string {
	if sessionID, ok := ctx.Value(sessionIDKey).(string); ok {
		return sessionID
	}
	return "default"
}
