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
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// FlexString is a string that can be unmarshalled from a JSON string or number.
// LongMemEval's "answer" field is sometimes a bare number (e.g. 3 instead of "3").
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	// Fall back to raw representation (handles numbers, booleans, etc.)
	*f = FlexString(strings.TrimSpace(string(b)))
	return nil
}

// Entry represents a single LongMemEval question entry.
type Entry struct {
	QuestionID         string     `json:"question_id"`
	QuestionType       string     `json:"question_type"`
	Question           string     `json:"question"`
	Answer             FlexString `json:"answer"`
	QuestionDate       string     `json:"question_date"`
	HaystackDates      []string   `json:"haystack_dates"`
	HaystackSessionIDs []string   `json:"haystack_session_ids"`
	HaystackSessions   [][]Turn   `json:"haystack_sessions"`
	AnswerSessionIDs   []string   `json:"answer_session_ids"`
}

// Turn represents a single conversation turn in a session.
type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer"`
}

// IsAbstention returns true if this is an abstention question (unanswerable).
func (e *Entry) IsAbstention() bool {
	return strings.Contains(e.QuestionID, "_abs")
}

// dateLayout is the Go time parse layout for LongMemEval dates.
// Format: "2023/04/10 (Mon) 17:50"
const dateLayout = "2006/01/02 (Mon) 15:04"

// ParseDate parses a LongMemEval date string.
func ParseDate(s string) (time.Time, error) {
	return time.Parse(dateLayout, s)
}

// SessionWithDate pairs a session with its date for sorting.
type SessionWithDate struct {
	SessionID string
	Date      string
	ParsedAt  time.Time
	Turns     []Turn
}

// SortedSessions returns sessions sorted chronologically by date.
func (e *Entry) SortedSessions() ([]SessionWithDate, error) {
	if len(e.HaystackSessions) != len(e.HaystackDates) {
		return nil, fmt.Errorf("session count (%d) != date count (%d)",
			len(e.HaystackSessions), len(e.HaystackDates))
	}

	sessions := make([]SessionWithDate, len(e.HaystackSessions))
	for i := range e.HaystackSessions {
		t, err := ParseDate(e.HaystackDates[i])
		if err != nil {
			return nil, fmt.Errorf("parse date %q: %w", e.HaystackDates[i], err)
		}
		sid := ""
		if i < len(e.HaystackSessionIDs) {
			sid = e.HaystackSessionIDs[i]
		}
		sessions[i] = SessionWithDate{
			SessionID: sid,
			Date:      e.HaystackDates[i],
			ParsedAt:  t,
			Turns:     e.HaystackSessions[i],
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ParsedAt.Before(sessions[j].ParsedAt)
	})

	return sessions, nil
}

// FormatSession formats a session's turns into a readable string for ingestion.
func FormatSession(s SessionWithDate) string {
	var b strings.Builder
	for _, turn := range s.Turns {
		fmt.Fprintf(&b, "%s: %s\n\n", turn.Role, turn.Content)
	}
	return b.String()
}

// FormatAllSessions formats all sessions for context-stuffing mode.
func FormatAllSessions(sessions []SessionWithDate) string {
	var b strings.Builder
	for i, s := range sessions {
		fmt.Fprintf(&b, "### Session %d:\nSession Date: %s\nSession Content:\n\n", i+1, s.Date)
		b.WriteString(FormatSession(s))
	}
	return b.String()
}

// LoadDataset loads a LongMemEval JSON dataset from disk.
func LoadDataset(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset: %w", err)
	}

	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}

	return entries, nil
}

// FilterByType returns only entries matching the given question types.
func FilterByType(entries []Entry, types []string) []Entry {
	if len(types) == 0 {
		return entries
	}
	typeSet := make(map[string]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	var filtered []Entry
	for _, e := range entries {
		if typeSet[e.QuestionType] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// QuestionTypes returns the set of unique question types in the dataset.
func QuestionTypes(entries []Entry) []string {
	seen := make(map[string]bool)
	var types []string
	for _, e := range entries {
		if !seen[e.QuestionType] {
			seen[e.QuestionType] = true
			types = append(types, e.QuestionType)
		}
	}
	sort.Strings(types)
	return types
}
