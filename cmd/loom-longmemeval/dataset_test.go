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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		year    int
		month   int
		day     int
		hour    int
		minute  int
	}{
		{
			name:   "valid date",
			input:  "2023/04/10 (Mon) 17:50",
			year:   2023,
			month:  4,
			day:    10,
			hour:   17,
			minute: 50,
		},
		{
			name:   "different day",
			input:  "2023/01/15 (Sun) 09:30",
			year:   2023,
			month:  1,
			day:    15,
			hour:   9,
			minute: 30,
		},
		{
			name:    "invalid format",
			input:   "2023-04-10 17:50",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDate(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.year, got.Year())
			assert.Equal(t, tt.month, int(got.Month()))
			assert.Equal(t, tt.day, got.Day())
			assert.Equal(t, tt.hour, got.Hour())
			assert.Equal(t, tt.minute, got.Minute())
		})
	}
}

func TestEntry_IsAbstention(t *testing.T) {
	tests := []struct {
		name       string
		questionID string
		want       bool
	}{
		{"regular", "gpt4_2655b836", false},
		{"abstention", "gpt4_2655b836_abs", true},
		{"abstention middle", "gpt4_abs_2655b836", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Entry{QuestionID: tt.questionID}
			assert.Equal(t, tt.want, e.IsAbstention())
		})
	}
}

func TestEntry_SortedSessions(t *testing.T) {
	e := Entry{
		HaystackDates: []string{
			"2023/04/10 (Mon) 17:50",
			"2023/04/08 (Sat) 09:00",
			"2023/04/09 (Sun) 14:30",
		},
		HaystackSessionIDs: []string{"s3", "s1", "s2"},
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "third"}},
			{{Role: "user", Content: "first"}},
			{{Role: "user", Content: "second"}},
		},
	}

	sorted, err := e.SortedSessions()
	require.NoError(t, err)
	require.Len(t, sorted, 3)

	// Should be in chronological order
	assert.Equal(t, "s1", sorted[0].SessionID)
	assert.Equal(t, "first", sorted[0].Turns[0].Content)
	assert.Equal(t, "s2", sorted[1].SessionID)
	assert.Equal(t, "second", sorted[1].Turns[0].Content)
	assert.Equal(t, "s3", sorted[2].SessionID)
	assert.Equal(t, "third", sorted[2].Turns[0].Content)
}

func TestEntry_SortedSessions_MismatchedCounts(t *testing.T) {
	e := Entry{
		HaystackDates:    []string{"2023/04/10 (Mon) 17:50"},
		HaystackSessions: [][]Turn{{}, {}},
	}
	_, err := e.SortedSessions()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session count")
}

func TestFormatSession(t *testing.T) {
	s := SessionWithDate{
		Date: "2023/04/10 (Mon) 17:50",
		Turns: []Turn{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}

	got := FormatSession(s)
	assert.Contains(t, got, "user: Hello")
	assert.Contains(t, got, "assistant: Hi there!")
}

func TestFormatAllSessions(t *testing.T) {
	sessions := []SessionWithDate{
		{
			Date:  "2023/04/08 (Sat) 09:00",
			Turns: []Turn{{Role: "user", Content: "First session"}},
		},
		{
			Date:  "2023/04/09 (Sun) 14:30",
			Turns: []Turn{{Role: "user", Content: "Second session"}},
		},
	}

	got := FormatAllSessions(sessions)
	assert.Contains(t, got, "### Session 1:")
	assert.Contains(t, got, "### Session 2:")
	assert.Contains(t, got, "Session Date: 2023/04/08 (Sat) 09:00")
	assert.Contains(t, got, "First session")
	assert.Contains(t, got, "Second session")
}

func TestLoadDataset(t *testing.T) {
	// Create a temp dataset file
	entries := []Entry{
		{
			QuestionID:   "test_001",
			QuestionType: "single-session-user",
			Question:     "What is my name?",
			Answer:       "Alice",
			QuestionDate: "2023/04/10 (Mon) 23:07",
			HaystackDates: []string{
				"2023/04/10 (Mon) 17:50",
			},
			HaystackSessionIDs: []string{"s1"},
			HaystackSessions: [][]Turn{
				{
					{Role: "user", Content: "My name is Alice", HasAnswer: true},
					{Role: "assistant", Content: "Nice to meet you, Alice!", HasAnswer: false},
				},
			},
			AnswerSessionIDs: []string{"s1"},
		},
	}

	data, err := json.Marshal(entries)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	loaded, err := LoadDataset(path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "test_001", loaded[0].QuestionID)
	assert.Equal(t, FlexString("Alice"), loaded[0].Answer)
	assert.Len(t, loaded[0].HaystackSessions, 1)
	assert.True(t, loaded[0].HaystackSessions[0][0].HasAnswer)
}

func TestLoadDataset_FileNotFound(t *testing.T) {
	_, err := LoadDataset("/nonexistent/path.json")
	assert.Error(t, err)
}

func TestLoadDataset_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, err := LoadDataset(path)
	assert.Error(t, err)
}

func TestFilterByType(t *testing.T) {
	entries := []Entry{
		{QuestionID: "1", QuestionType: "temporal-reasoning"},
		{QuestionID: "2", QuestionType: "multi-session"},
		{QuestionID: "3", QuestionType: "temporal-reasoning"},
		{QuestionID: "4", QuestionType: "knowledge-update"},
	}

	t.Run("filter single type", func(t *testing.T) {
		got := FilterByType(entries, []string{"temporal-reasoning"})
		assert.Len(t, got, 2)
		assert.Equal(t, "1", got[0].QuestionID)
		assert.Equal(t, "3", got[1].QuestionID)
	})

	t.Run("filter multiple types", func(t *testing.T) {
		got := FilterByType(entries, []string{"multi-session", "knowledge-update"})
		assert.Len(t, got, 2)
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		got := FilterByType(entries, nil)
		assert.Len(t, got, 4)
	})
}

func TestQuestionTypes(t *testing.T) {
	entries := []Entry{
		{QuestionType: "b-type"},
		{QuestionType: "a-type"},
		{QuestionType: "b-type"},
		{QuestionType: "c-type"},
	}

	types := QuestionTypes(entries)
	assert.Equal(t, []string{"a-type", "b-type", "c-type"}, types)
}
