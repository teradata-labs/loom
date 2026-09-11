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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func statsFixture() []Entry {
	turn := []Turn{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	return []Entry{
		{QuestionID: "q1", QuestionType: "temporal-reasoning", HaystackSessions: [][]Turn{turn, turn}},
		{QuestionID: "q2", QuestionType: "temporal-reasoning", HaystackSessions: [][]Turn{turn}},
		{QuestionID: "q3", QuestionType: "multi-session", HaystackSessions: [][]Turn{turn}},
		{QuestionID: "q4", QuestionType: "knowledge-update"},
	}
}

func TestComputeDatasetStats(t *testing.T) {
	stats := ComputeDatasetStats("/data/lme.json", statsFixture())

	assert.Equal(t, "/data/lme.json", stats.Dataset)
	assert.Equal(t, 4, stats.Entries)
	assert.Equal(t, 4, stats.TotalSessions)
	assert.Equal(t, 8, stats.TotalTurns)

	// Ordering follows QuestionTypes() (sorted), so the slice loop that
	// consumes this iterates deterministically across invocations.
	assert.Equal(t, []DatasetTypeCount{
		{Type: "knowledge-update", Count: 1},
		{Type: "multi-session", Count: 1},
		{Type: "temporal-reasoning", Count: 2},
	}, stats.QuestionTypes)
}

// The per-type counts must sum to the entry count: the slice loop walks each
// type to its count, so an under-count would silently omit questions from a
// published number and an over-count would run the harness past its last entry.
func TestComputeDatasetStats_CountsCoverEveryEntry(t *testing.T) {
	stats := ComputeDatasetStats("d.json", statsFixture())

	total := 0
	for _, tc := range stats.QuestionTypes {
		assert.NotEmpty(t, tc.Type)
		assert.Positive(t, tc.Count)
		total += tc.Count
	}
	assert.Equal(t, stats.Entries, total)
}

func TestComputeDatasetStats_Empty(t *testing.T) {
	stats := ComputeDatasetStats("empty.json", nil)

	assert.Equal(t, 0, stats.Entries)
	assert.Empty(t, stats.QuestionTypes)
	// Marshals as [] rather than null, so `jq '.question_types[]'` on an
	// empty dataset yields no rows instead of erroring.
	blob, err := json.Marshal(stats)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"question_types":[]`)
}

// ratio must not emit NaN/Inf into the human-readable output for a dataset
// with no entries or no sessions.
func TestRatio(t *testing.T) {
	assert.Equal(t, "2.0", ratio(4, 2))
	assert.Equal(t, "n/a", ratio(4, 0))
	assert.Equal(t, "n/a", ratio(0, 0))
}

// The JSON emitted by `info --json` is what the AKS slice loop parses with
// jq, so pin the field names and the shape end-to-end.
func TestInfoCmd_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dataset.json")
	blob, err := json.Marshal(statsFixture())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, blob, 0o600))

	cmd := infoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{path, "--json"})
	require.NoError(t, cmd.Execute())

	var got DatasetStats
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))

	assert.Equal(t, path, got.Dataset)
	assert.Equal(t, 4, got.Entries)
	assert.Equal(t, 4, got.TotalSessions)
	assert.Equal(t, 8, got.TotalTurns)
	assert.Equal(t, []DatasetTypeCount{
		{Type: "knowledge-update", Count: 1},
		{Type: "multi-session", Count: 1},
		{Type: "temporal-reasoning", Count: 2},
	}, got.QuestionTypes)
}
