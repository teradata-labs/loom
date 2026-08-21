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
package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeywordSearchQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "content words survive, stopwords and punctuation drop",
			in:   "This task requires database session continuity. First call teradata_connect to obtain a session_handle!",
			want: "task requires database session continuity teradata_connect obtain session_handle",
		},
		{
			name: "dedupe and lowercase",
			in:   "Volatile VOLATILE volatile tables",
			want: "volatile tables",
		},
		{
			name: "stopword-only input yields empty",
			in:   "the and for with",
			want: "",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, keywordSearchQuery(tt.in))
		})
	}
}

func TestKeywordSearchQueryCap(t *testing.T) {
	long := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda omicron sigma tau ", 3)
	got := keywordSearchQuery(long)
	assert.LessOrEqual(t, len(strings.Fields(got)), 12, "query must cap at twelve words")
	assert.NotEmpty(t, got)
}
