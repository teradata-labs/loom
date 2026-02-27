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
package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionMerge(t *testing.T) {
	tests := []struct {
		name     string
		existing Session
		update   Session
		want     Session
	}{
		{
			name: "preserves existing Title when update has empty title",
			existing: Session{
				ID:    "s1",
				Title: "My Session",
				Cost:  0,
			},
			update: Session{
				ID:               "s1",
				Cost:             1.23,
				CompletionTokens: 500,
				PromptTokens:     200,
			},
			want: Session{
				ID:               "s1",
				Title:            "My Session", // preserved
				Cost:             1.23,
				CompletionTokens: 500,
				PromptTokens:     200,
			},
		},
		{
			name: "updates Model and Provider from cost event",
			existing: Session{
				ID:    "s1",
				Title: "My Session",
			},
			update: Session{
				ID:               "s1",
				Model:            "claude-sonnet-4-6",
				Provider:         "anthropic",
				CompletionTokens: 100,
				PromptTokens:     50,
				Cost:             0.05,
			},
			want: Session{
				ID:               "s1",
				Title:            "My Session",
				Model:            "claude-sonnet-4-6",
				Provider:         "anthropic",
				CompletionTokens: 100,
				PromptTokens:     50,
				Cost:             0.05,
			},
		},
		{
			name: "does not overwrite non-zero fields with zero values",
			existing: Session{
				ID:               "s1",
				Title:            "Keep Me",
				CompletionTokens: 999,
				PromptTokens:     888,
				Cost:             9.99,
				Model:            "gpt-4",
				Provider:         "openai",
			},
			update: Session{
				ID: "s1",
				// all other fields zero
			},
			want: Session{
				ID:               "s1",
				Title:            "Keep Me",
				CompletionTokens: 999,
				PromptTokens:     888,
				Cost:             9.99,
				Model:            "gpt-4",
				Provider:         "openai",
			},
		},
		{
			name: "updates Todos when non-empty",
			existing: Session{
				ID:    "s1",
				Title: "With Todos",
			},
			update: Session{
				ID: "s1",
				Todos: []Todo{
					{Content: "todo 1", Status: TodoStatusInProgress},
				},
			},
			want: Session{
				ID:    "s1",
				Title: "With Todos",
				Todos: []Todo{
					{Content: "todo 1", Status: TodoStatusInProgress},
				},
			},
		},
		{
			name: "updates Title when non-empty",
			existing: Session{
				ID:    "s1",
				Title: "Old Title",
			},
			update: Session{
				ID:    "s1",
				Title: "New Title",
			},
			want: Session{
				ID:    "s1",
				Title: "New Title",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.existing.Merge(tc.update)
			assert.Equal(t, tc.want, got)
		})
	}
}
