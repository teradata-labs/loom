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
package teleprompter

import (
	"context"
	"fmt"
	"sort"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TopKSelector selects the top K demonstrations by quality score.
// This is the simplest and most common strategy.
type TopKSelector struct{}

// NewTopKSelector creates a new TopK selector
func NewTopKSelector() *TopKSelector {
	return &TopKSelector{}
}

// Select implements DemonstrationSelector
func (s *TopKSelector) Select(
	ctx context.Context,
	traces []*ExecutionTrace,
	maxDemos int,
) ([]*loomv1.Demonstration, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("no traces provided")
	}

	// Sort traces by quality score (descending)
	sortedTraces := make([]*ExecutionTrace, len(traces))
	copy(sortedTraces, traces)
	sort.Slice(sortedTraces, func(i, j int) bool {
		return sortedTraces[i].QualityScore > sortedTraces[j].QualityScore
	})

	// Select top K
	k := maxDemos
	if k > len(sortedTraces) {
		k = len(sortedTraces)
	}

	demonstrations := make([]*loomv1.Demonstration, k)
	for i := 0; i < k; i++ {
		trace := sortedTraces[i]
		demonstrations[i] = &loomv1.Demonstration{
			PatternName: trace.Metadata["pattern_name"], // From execution context
			Input:       formatInputs(trace.Example.Inputs),
			Rationale:   trace.Result.Rationale,
			Output:      formatOutputs(trace.Result.Outputs),
			Confidence:  trace.QualityScore,
			Timestamp:   trace.Timestamp,
			Metadata:    trace.Metadata,
		}
	}

	return demonstrations, nil
}

// Strategy returns the selection strategy
func (s *TopKSelector) Strategy() loomv1.BootstrapStrategy {
	return loomv1.BootstrapStrategy_BOOTSTRAP_TOP_K
}

// DiverseSelector selects demonstrations that are diverse (minimize similarity).
// Uses a greedy algorithm to maximize pairwise distance.
type DiverseSelector struct {
	similarityThreshold float64 // Min distance required (0-1)
}

// NewDiverseSelector creates a new diverse selector
func NewDiverseSelector(similarityThreshold float64) *DiverseSelector {
	if similarityThreshold == 0 {
		similarityThreshold = 0.3 // Default: 30% different
	}
	return &DiverseSelector{
		similarityThreshold: similarityThreshold,
	}
}

// Select implements DemonstrationSelector
func (s *DiverseSelector) Select(
	ctx context.Context,
	traces []*ExecutionTrace,
	maxDemos int,
) ([]*loomv1.Demonstration, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("no traces provided")
	}

	// Sort by quality first
	sortedTraces := make([]*ExecutionTrace, len(traces))
	copy(sortedTraces, traces)
	sort.Slice(sortedTraces, func(i, j int) bool {
		return sortedTraces[i].QualityScore > sortedTraces[j].QualityScore
	})

	// Greedy selection for diversity
	selected := []*ExecutionTrace{sortedTraces[0]} // Start with best

	for i := 1; i < len(sortedTraces) && len(selected) < maxDemos; i++ {
		candidate := sortedTraces[i]

		// Check if candidate is sufficiently different from selected
		if s.isSufficientlyDifferent(candidate, selected) {
			selected = append(selected, candidate)
		}
	}

	// Convert to demonstrations
	demonstrations := make([]*loomv1.Demonstration, len(selected))
	for i, trace := range selected {
		demonstrations[i] = &loomv1.Demonstration{
			PatternName: trace.Metadata["pattern_name"],
			Input:       formatInputs(trace.Example.Inputs),
			Rationale:   trace.Result.Rationale,
			Output:      formatOutputs(trace.Result.Outputs),
			Confidence:  trace.QualityScore,
			Timestamp:   trace.Timestamp,
			Metadata:    trace.Metadata,
		}
	}

	return demonstrations, nil
}

// Strategy returns the selection strategy
func (s *DiverseSelector) Strategy() loomv1.BootstrapStrategy {
	return loomv1.BootstrapStrategy_BOOTSTRAP_DIVERSE
}

// isSufficientlyDifferent checks if candidate is different enough from selected traces
func (s *DiverseSelector) isSufficientlyDifferent(
	candidate *ExecutionTrace,
	selected []*ExecutionTrace,
) bool {
	for _, sel := range selected {
		similarity := s.computeSimilarity(candidate, sel)
		if similarity > (1.0 - s.similarityThreshold) {
			return false // Too similar
		}
	}
	return true
}

// computeSimilarity computes similarity between two traces (0=different, 1=identical)
func (s *DiverseSelector) computeSimilarity(a, b *ExecutionTrace) float64 {
	// Simple heuristic: compare input/output string similarity
	// In production, use embedding-based similarity

	inputSim := stringOverlap(
		formatInputs(a.Example.Inputs),
		formatInputs(b.Example.Inputs),
	)
	outputSim := stringOverlap(
		formatOutputs(a.Result.Outputs),
		formatOutputs(b.Result.Outputs),
	)

	return (inputSim + outputSim) / 2.0
}

// RecentSelector selects the most recent demonstrations.
// Useful when agent behavior is changing over time.
type RecentSelector struct{}

// NewRecentSelector creates a new recent selector
func NewRecentSelector() *RecentSelector {
	return &RecentSelector{}
}

// Select implements DemonstrationSelector
func (s *RecentSelector) Select(
	ctx context.Context,
	traces []*ExecutionTrace,
	maxDemos int,
) ([]*loomv1.Demonstration, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("no traces provided")
	}

	// Sort by timestamp (descending - most recent first)
	sortedTraces := make([]*ExecutionTrace, len(traces))
	copy(sortedTraces, traces)
	sort.Slice(sortedTraces, func(i, j int) bool {
		return sortedTraces[i].Timestamp > sortedTraces[j].Timestamp
	})

	// Select most recent K
	k := maxDemos
	if k > len(sortedTraces) {
		k = len(sortedTraces)
	}

	demonstrations := make([]*loomv1.Demonstration, k)
	for i := 0; i < k; i++ {
		trace := sortedTraces[i]
		demonstrations[i] = &loomv1.Demonstration{
			PatternName: trace.Metadata["pattern_name"],
			Input:       formatInputs(trace.Example.Inputs),
			Rationale:   trace.Result.Rationale,
			Output:      formatOutputs(trace.Result.Outputs),
			Confidence:  trace.QualityScore,
			Timestamp:   trace.Timestamp,
			Metadata:    trace.Metadata,
		}
	}

	return demonstrations, nil
}

// Strategy returns the selection strategy
func (s *RecentSelector) Strategy() loomv1.BootstrapStrategy {
	return loomv1.BootstrapStrategy_BOOTSTRAP_RECENT
}

// RepresentativeSelector selects demonstrations that cover common query patterns.
// Uses clustering to identify representative examples.
type RepresentativeSelector struct {
	numClusters int
}

// NewRepresentativeSelector creates a new representative selector
func NewRepresentativeSelector(numClusters int) *RepresentativeSelector {
	if numClusters == 0 {
		numClusters = 5 // Default clusters
	}
	return &RepresentativeSelector{
		numClusters: numClusters,
	}
}

// Select implements DemonstrationSelector
func (s *RepresentativeSelector) Select(
	ctx context.Context,
	traces []*ExecutionTrace,
	maxDemos int,
) ([]*loomv1.Demonstration, error) {
	if len(traces) == 0 {
		return nil, fmt.Errorf("no traces provided")
	}

	// TODO: Implement clustering-based selection
	// For now, fall back to diverse selection
	diverseSelector := NewDiverseSelector(0.4) // 40% difference threshold
	return diverseSelector.Select(ctx, traces, maxDemos)
}

// Strategy returns the selection strategy
func (s *RepresentativeSelector) Strategy() loomv1.BootstrapStrategy {
	return loomv1.BootstrapStrategy_BOOTSTRAP_REPRESENTATIVE
}

// ============================================================================
// Utility Functions
// ============================================================================

// formatInputs converts input map to a single string
func formatInputs(inputs map[string]string) string {
	if len(inputs) == 0 {
		return ""
	}

	// If single input, return value directly
	if len(inputs) == 1 {
		for _, v := range inputs {
			return v
		}
	}

	// Multiple inputs: format as key=value pairs
	result := ""
	for k, v := range inputs {
		if result != "" {
			result += "\n"
		}
		result += fmt.Sprintf("%s: %s", k, v)
	}
	return result
}

// formatOutputs converts output map to a single string
func formatOutputs(outputs map[string]string) string {
	if len(outputs) == 0 {
		return ""
	}

	// If single output, return value directly
	if len(outputs) == 1 {
		for _, v := range outputs {
			return v
		}
	}

	// Multiple outputs: format as key=value pairs
	result := ""
	for k, v := range outputs {
		if result != "" {
			result += "\n"
		}
		result += fmt.Sprintf("%s: %s", k, v)
	}
	return result
}

// stringOverlap computes token-level overlap between two strings (0-1)
func stringOverlap(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}

	// Simple word-level Jaccard similarity
	wordsA := tokenize(a)
	wordsB := tokenize(b)

	setA := make(map[string]bool)
	for _, w := range wordsA {
		setA[w] = true
	}

	intersection := 0
	for _, w := range wordsB {
		if setA[w] {
			intersection++
		}
	}

	union := len(wordsA) + len(wordsB) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}

// tokenize splits a string into words
func tokenize(s string) []string {
	words := []string{}
	current := ""

	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			current += string(c)
		} else {
			if current != "" {
				words = append(words, current)
				current = ""
			}
		}
	}

	if current != "" {
		words = append(words, current)
	}

	return words
}
