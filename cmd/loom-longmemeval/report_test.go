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

//go:build fts5

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSONL(t *testing.T) {
	results := []EntryResult{
		{
			QuestionID: "q1",
			Hypothesis: "answer one",
		},
		{
			QuestionID: "q2",
			Hypothesis: "",
			Error:      "some error", // should be skipped
		},
		{
			QuestionID: "q3",
			Hypothesis: "answer three",
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")

	require.NoError(t, WriteJSONL(path, results))

	// Read back and verify
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	var entries []HypothesisEntry
	for scanner.Scan() {
		var e HypothesisEntry
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &e))
		entries = append(entries, e)
	}

	// q2 should be excluded (has error)
	require.Len(t, entries, 2)
	assert.Equal(t, "q1", entries[0].QuestionID)
	assert.Equal(t, "answer one", entries[0].Hypothesis)
	assert.Equal(t, "q3", entries[1].QuestionID)
	assert.Equal(t, "answer three", entries[1].Hypothesis)
}

func TestWriteDetailedResults(t *testing.T) {
	results := []EntryResult{
		{
			QuestionID:   "q1",
			Hypothesis:   "test",
			QuestionType: "temporal-reasoning",
			Duration:     2 * time.Second,
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "detailed.json")

	require.NoError(t, WriteDetailedResults(path, results))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var loaded []EntryResult
	require.NoError(t, json.Unmarshal(data, &loaded))
	require.Len(t, loaded, 1)
	assert.Equal(t, "q1", loaded[0].QuestionID)
}

func TestSummarize(t *testing.T) {
	results := []EntryResult{
		{
			QuestionID:   "q1",
			QuestionType: "temporal-reasoning",
			Duration:     2 * time.Second,
			InputTokens:  100,
			OutputTokens: 50,
		},
		{
			QuestionID:   "q2",
			QuestionType: "temporal-reasoning",
			Duration:     3 * time.Second,
			InputTokens:  200,
			OutputTokens: 100,
		},
		{
			QuestionID:   "q3",
			QuestionType: "multi-session",
			Duration:     1 * time.Second,
			InputTokens:  80,
			OutputTokens: 40,
		},
		{
			QuestionID:   "q4",
			QuestionType: "multi-session",
			Error:        "some error",
		},
	}

	s := Summarize(results, "graph-memory", "claude-sonnet")

	assert.Equal(t, "graph-memory", s.Mode)
	assert.Equal(t, "claude-sonnet", s.Model)
	assert.Equal(t, 4, s.TotalEntries)
	assert.Equal(t, 3, s.Succeeded)
	assert.Equal(t, 1, s.Failed)
	assert.Equal(t, 380, s.TotalInput)
	assert.Equal(t, 190, s.TotalOutput)

	// Check type breakdown
	tr := s.TypeBreakdown["temporal-reasoning"]
	require.NotNil(t, tr)
	assert.Equal(t, 2, tr.Count)
	assert.Equal(t, 2, tr.Succeeded)
	assert.Equal(t, 0, tr.Failed)
	assert.Equal(t, 150, tr.AvgInput) // (100+200)/2
	assert.Equal(t, 75, tr.AvgOutput) // (50+100)/2

	ms := s.TypeBreakdown["multi-session"]
	require.NotNil(t, ms)
	assert.Equal(t, 2, ms.Count)
	assert.Equal(t, 1, ms.Succeeded)
	assert.Equal(t, 1, ms.Failed)
}

func TestSummarize_AllFailed(t *testing.T) {
	results := []EntryResult{
		{QuestionID: "q1", QuestionType: "x", Error: "err"},
	}

	s := Summarize(results, "context-stuffing", "model")
	assert.Equal(t, 0, s.Succeeded)
	assert.Equal(t, 1, s.Failed)
	assert.Equal(t, time.Duration(0), s.AvgDuration)
}
