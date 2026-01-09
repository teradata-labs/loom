// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareWithGoldenFile(t *testing.T) {
	tests := []struct {
		name             string
		goldenContent    string
		actualOutput     string
		threshold        float64
		expectMatched    bool
		expectSimilarity float64
	}{
		{
			name:             "exact match",
			goldenContent:    "SELECT * FROM users;",
			actualOutput:     "SELECT * FROM users;",
			threshold:        0.9,
			expectMatched:    true,
			expectSimilarity: 1.0,
		},
		{
			name:             "whitespace differences (should still match)",
			goldenContent:    "SELECT * FROM users;",
			actualOutput:     "SELECT  *  FROM  users;",
			threshold:        0.9,
			expectMatched:    true,
			expectSimilarity: 1.0, // After normalization
		},
		{
			name:          "similar but not exact",
			goldenContent: "SELECT id, name FROM users;",
			actualOutput:  "SELECT id, email FROM users;",
			threshold:     0.9,
			expectMatched: false,
		},
		{
			name:             "completely different",
			goldenContent:    "SELECT * FROM users;",
			actualOutput:     "DROP TABLE users;",
			threshold:        0.9,
			expectMatched:    false,
			expectSimilarity: 0.0,
		},
		{
			name:          "similar with lower threshold",
			goldenContent: "SELECT id, name FROM users;",
			actualOutput:  "SELECT id, email FROM users;",
			threshold:     0.7,
			expectMatched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp golden file
			tmpDir := t.TempDir()
			goldenFile := filepath.Join(tmpDir, "golden.sql")
			err := os.WriteFile(goldenFile, []byte(tt.goldenContent), 0644)
			require.NoError(t, err)

			// Compare
			result, err := CompareWithGoldenFile(goldenFile, tt.actualOutput, tt.threshold)
			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Equal(t, tt.expectMatched, result.Matched, "match result mismatch")

			if tt.expectSimilarity > 0 {
				assert.InDelta(t, tt.expectSimilarity, result.SimilarityScore, 0.1, "similarity score mismatch")
			}

			if !result.Matched {
				assert.NotEmpty(t, result.Diff, "diff should be generated for non-matching results")
			}
		})
	}
}

func TestCompareWithGoldenFile_FileNotFound(t *testing.T) {
	result, err := CompareWithGoldenFile("/nonexistent/file.sql", "test output", 0.9)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.Matched)
	assert.Equal(t, 0.0, result.SimilarityScore)
	assert.Contains(t, result.Diff, "not found")
}

func TestUpdateGoldenFile(t *testing.T) {
	tmpDir := t.TempDir()
	goldenFile := filepath.Join(tmpDir, "subdir", "golden.sql")

	content := "SELECT * FROM users;"
	err := UpdateGoldenFile(goldenFile, content)
	require.NoError(t, err)

	// Verify file was created
	data, err := os.ReadFile(goldenFile)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestNormalizeWhitespace(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "SELECT  *  FROM  users",
			expected: "SELECT * FROM users",
		},
		{
			input:    "  SELECT * FROM users  ",
			expected: "SELECT * FROM users",
		},
		{
			input:    "SELECT\n*\nFROM\nusers",
			expected: "SELECT * FROM users",
		},
		{
			input:    "SELECT\t*\tFROM\tusers",
			expected: "SELECT * FROM users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeWhitespace(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateSimilarity(t *testing.T) {
	tests := []struct {
		a        string
		b        string
		expected float64
		delta    float64
	}{
		{
			a:        "SELECT * FROM users",
			b:        "SELECT * FROM users",
			expected: 1.0,
			delta:    0.01,
		},
		{
			a:        "SELECT * FROM users",
			b:        "SELECT * FROM orders",
			expected: 0.75,
			delta:    0.15,
		},
		{
			a:        "SELECT * FROM users",
			b:        "DROP TABLE users",
			expected: 0.3,
			delta:    0.25,
		},
		{
			a:        "",
			b:        "",
			expected: 1.0,
			delta:    0.01,
		},
		{
			a:        "test",
			b:        "",
			expected: 0.0,
			delta:    0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			result := calculateSimilarity(tt.a, tt.b)
			assert.InDelta(t, tt.expected, result, tt.delta)
		})
	}
}

func TestGenerateDiff(t *testing.T) {
	expected := "SELECT * FROM users;"
	actual := "SELECT * FROM orders;"

	diff := generateDiff(expected, actual)

	assert.NotEmpty(t, diff)
	assert.Contains(t, diff, "Expected")
	assert.Contains(t, diff, "Actual")
	// The diff algorithm may split words differently, just check for presence of key markers
	assert.Contains(t, diff, "-") // Deletion marker
	assert.Contains(t, diff, "+")
}

func TestSimilarityScore(t *testing.T) {
	// Test with whitespace differences
	score := SimilarityScore("SELECT  *  FROM  users", "SELECT * FROM users")
	assert.InDelta(t, 1.0, score, 0.01, "should be identical after normalization")

	// Test with actual differences
	score = SimilarityScore("SELECT * FROM users", "SELECT * FROM orders")
	assert.Less(t, score, 1.0, "should not be identical")
	assert.Greater(t, score, 0.5, "should have some similarity")
}

// Benchmark tests
func BenchmarkCalculateSimilarity(b *testing.B) {
	a := "SELECT id, name, email FROM users WHERE status = 'active' ORDER BY created_at DESC LIMIT 100;"
	b1 := "SELECT id, name, phone FROM users WHERE status = 'inactive' ORDER BY updated_at DESC LIMIT 50;"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculateSimilarity(a, b1)
	}
}

func BenchmarkNormalizeWhitespace(b *testing.B) {
	input := "SELECT  *  FROM  users  WHERE  status  =  'active'  ORDER  BY  created_at  DESC  LIMIT  100;"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		normalizeWhitespace(input)
	}
}
