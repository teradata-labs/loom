// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package patterns

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/types"
)

// LLMReRankerConfig configures the LLM-based pattern re-ranker
type LLMReRankerConfig struct {
	// LLM provider to use for re-ranking
	LLMProvider types.LLMProvider

	// Enable caching of re-ranking results
	EnableCache bool

	// Cache TTL (default: 30 minutes - longer than intent classification)
	CacheTTL time.Duration
}

// DefaultLLMReRankerConfig returns sensible defaults for re-ranking
func DefaultLLMReRankerConfig(llm types.LLMProvider) *LLMReRankerConfig {
	return &LLMReRankerConfig{
		LLMProvider: llm,
		EnableCache: true,
		CacheTTL:    30 * time.Minute,
	}
}

// reRankingResult represents the result of LLM-based re-ranking
type reRankingResult struct {
	SelectedPattern string  `json:"selected_pattern"`
	Confidence      float64 `json:"confidence"`
	Reasoning       string  `json:"reasoning"`
}

// reRankPatternsWithLLM uses LLM to re-rank a set of candidate patterns based on user query.
// This provides semantic understanding beyond keyword matching.
func reRankPatternsWithLLM(
	llmProvider types.LLMProvider,
	userMessage string,
	candidates []scoredPattern,
	summaries map[string]PatternSummary,
) (string, float64, error) {
	if llmProvider == nil {
		return "", 0.0, fmt.Errorf("LLM provider is nil")
	}

	if len(candidates) == 0 {
		return "", 0.0, fmt.Errorf("no candidates to re-rank")
	}

	// Build prompt with pattern candidates
	var promptBuilder strings.Builder
	promptBuilder.WriteString("User Query: \"")
	promptBuilder.WriteString(userMessage)
	promptBuilder.WriteString("\"\n\n")
	promptBuilder.WriteString("Candidate Patterns (ranked by keyword matching):\n\n")

	for i, candidate := range candidates {
		summary := summaries[candidate.name]
		promptBuilder.WriteString(fmt.Sprintf("%d. %s\n", i+1, candidate.name))
		promptBuilder.WriteString(fmt.Sprintf("   Title: %s\n", summary.Title))
		promptBuilder.WriteString(fmt.Sprintf("   Category: %s\n", summary.Category))
		promptBuilder.WriteString(fmt.Sprintf("   Description: %s\n", summary.Description[:min(200, len(summary.Description))]))

		if len(summary.UseCases) > 0 {
			promptBuilder.WriteString(fmt.Sprintf("   Use Cases: %s\n",
				strings.Join(summary.UseCases[:min(3, len(summary.UseCases))], ", ")))
		}

		promptBuilder.WriteString(fmt.Sprintf("   Keyword Score: %.2f\n\n", candidate.score))
	}

	promptBuilder.WriteString("\nTask: Select the most relevant pattern for the user's query.\n")
	promptBuilder.WriteString("Consider:\n")
	promptBuilder.WriteString("- Semantic match between query and pattern purpose\n")
	promptBuilder.WriteString("- Use case alignment\n")
	promptBuilder.WriteString("- Category appropriateness\n\n")
	promptBuilder.WriteString("Respond with JSON:\n")
	promptBuilder.WriteString("{\n")
	promptBuilder.WriteString("  \"selected_pattern\": \"pattern_name\",\n")
	promptBuilder.WriteString("  \"confidence\": 0.85,\n")
	promptBuilder.WriteString("  \"reasoning\": \"Brief explanation why this pattern is best\"\n")
	promptBuilder.WriteString("}")

	prompt := promptBuilder.String()

	// Call LLM
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []types.Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}

	response, err := llmProvider.Chat(ctx, messages, nil)
	if err != nil {
		return "", 0.0, fmt.Errorf("LLM generation failed: %w", err)
	}

	// Parse JSON response
	responseText := response.Content

	// Extract JSON (handle markdown code blocks if present)
	if strings.Contains(responseText, "```json") {
		start := strings.Index(responseText, "```json") + 7
		end := strings.Index(responseText[start:], "```")
		if end > 0 {
			responseText = responseText[start : start+end]
		}
	} else if strings.Contains(responseText, "```") {
		start := strings.Index(responseText, "```") + 3
		end := strings.Index(responseText[start:], "```")
		if end > 0 {
			responseText = responseText[start : start+end]
		}
	}

	responseText = strings.TrimSpace(responseText)

	var result reRankingResult
	if err := json.Unmarshal([]byte(responseText), &result); err != nil {
		return "", 0.0, fmt.Errorf("failed to parse LLM response: %w\nResponse: %s", err, responseText)
	}

	// Validate selected pattern is in candidates
	found := false
	for _, candidate := range candidates {
		if candidate.name == result.SelectedPattern {
			found = true
			break
		}
	}

	if !found {
		// Fallback to highest keyword score
		return candidates[0].name, candidates[0].score,
			fmt.Errorf("LLM selected pattern not in candidates: %s", result.SelectedPattern)
	}

	// Ensure confidence is in valid range
	if result.Confidence < 0.0 {
		result.Confidence = 0.0
	}
	if result.Confidence > 1.0 {
		result.Confidence = 1.0
	}

	return result.SelectedPattern, result.Confidence, nil
}

// shouldInvokeLLMReRanker determines if LLM re-ranking should be used.
// With accuracy preference, we invoke LLM more aggressively.
func shouldInvokeLLMReRanker(
	scored []scoredPattern,
	intent string,
	llmProvider types.LLMProvider,
) bool {
	// No LLM provider available
	if llmProvider == nil {
		return false
	}

	// No patterns to re-rank
	if len(scored) == 0 {
		return false
	}

	// AGGRESSIVE TRIGGERS (accuracy over speed)

	// 1. Always use LLM for unknown intent
	if intent == "" {
		return true
	}

	// 2. Use LLM when top score is uncertain (< 0.70)
	// Raised threshold from 0.60 to 0.70 for better accuracy
	if scored[0].score < 0.70 {
		return true
	}

	// 3. Use LLM when there's a close race (top 2 within 0.20)
	// Increased from 0.15 to 0.20 to catch more ambiguous cases
	if len(scored) >= 2 && (scored[0].score-scored[1].score) < 0.20 {
		return true
	}

	// 4. Use LLM when there are multiple strong candidates (3+ patterns > 0.60)
	strongCandidates := 0
	for _, s := range scored {
		if s.score > 0.60 {
			strongCandidates++
		}
	}
	if strongCandidates >= 3 {
		return true
	}

	// Clear winner - use fast path
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
