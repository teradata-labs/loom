// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"fmt"
	"os"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// CompareWithGoldenFile compares actual output with a golden file
func CompareWithGoldenFile(goldenFilePath string, actualOutput string, threshold float64) (*loomv1.GoldenFileResult, error) {
	// Read golden file
	goldenData, err := os.ReadFile(goldenFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &loomv1.GoldenFileResult{
				Matched:         false,
				SimilarityScore: 0,
				Diff:            fmt.Sprintf("Golden file not found: %s", goldenFilePath),
			}, nil
		}
		return nil, fmt.Errorf("failed to read golden file %s: %w", goldenFilePath, err)
	}

	goldenContent := string(goldenData)

	// Normalize whitespace for comparison
	normalizedGolden := normalizeWhitespace(goldenContent)
	normalizedActual := normalizeWhitespace(actualOutput)

	// Calculate similarity
	similarity := calculateSimilarity(normalizedGolden, normalizedActual)

	// Generate diff if not matched
	var diff string
	matched := similarity >= threshold
	if !matched {
		diff = generateDiff(goldenContent, actualOutput)
	}

	return &loomv1.GoldenFileResult{
		Matched:         matched,
		SimilarityScore: similarity,
		Diff:            diff,
	}, nil
}

// UpdateGoldenFile updates a golden file with new content
func UpdateGoldenFile(goldenFilePath string, content string) error {
	// Create directory if it doesn't exist
	dir := strings.TrimSuffix(goldenFilePath, "/"+strings.Split(goldenFilePath, "/")[len(strings.Split(goldenFilePath, "/"))-1])
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create golden file directory: %w", err)
	}

	// Write file
	if err := os.WriteFile(goldenFilePath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write golden file %s: %w", goldenFilePath, err)
	}

	return nil
}

// normalizeWhitespace normalizes whitespace for comparison
func normalizeWhitespace(s string) string {
	// Replace multiple spaces with single space
	s = strings.Join(strings.Fields(s), " ")
	// Trim whitespace
	s = strings.TrimSpace(s)
	return s
}

// calculateSimilarity calculates similarity between two strings (0.0 to 1.0)
// Uses Levenshtein distance-based similarity
func calculateSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}

	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}

	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	// Use diffmatchpatch for similarity calculation
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(a, b, false)

	// Calculate similarity based on common text
	commonLength := 0
	totalLength := 0

	for _, diff := range diffs {
		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			commonLength += len(diff.Text)
			totalLength += len(diff.Text)
		case diffmatchpatch.DiffInsert:
			totalLength += len(diff.Text)
		case diffmatchpatch.DiffDelete:
			totalLength += len(diff.Text)
		}
	}

	if totalLength == 0 {
		return 1.0
	}

	return float64(commonLength) / float64(totalLength)
}

// generateDiff generates a human-readable diff
func generateDiff(expected, actual string) string {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(expected, actual, false)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var result strings.Builder
	result.WriteString("--- Expected\n")
	result.WriteString("+++ Actual\n")
	result.WriteString("@@ Differences @@\n")

	for _, diff := range diffs {
		text := diff.Text
		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			result.WriteString("+ ")
			result.WriteString(strings.ReplaceAll(text, "\n", "\n+ "))
			result.WriteString("\n")
		case diffmatchpatch.DiffDelete:
			result.WriteString("- ")
			result.WriteString(strings.ReplaceAll(text, "\n", "\n- "))
			result.WriteString("\n")
		case diffmatchpatch.DiffEqual:
			// Show context (first/last line of equal text)
			lines := strings.Split(text, "\n")
			if len(lines) > 4 {
				result.WriteString("  " + lines[0] + "\n")
				result.WriteString("  ...\n")
				result.WriteString("  " + lines[len(lines)-1] + "\n")
			} else {
				for _, line := range lines {
					if line != "" {
						result.WriteString("  " + line + "\n")
					}
				}
			}
		}
	}

	return result.String()
}

// SimilarityScore calculates a simple similarity score between two strings
// This is a simpler alternative to calculateSimilarity for basic use cases
func SimilarityScore(a, b string) float64 {
	return calculateSimilarity(normalizeWhitespace(a), normalizeWhitespace(b))
}
