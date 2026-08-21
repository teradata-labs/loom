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
package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Free text with FTS5-hostile punctuation must become a syntax-safe bareword
// OR-query — a raw apostrophe/comma/'?' in MATCH is a parse error the recall
// callers swallow as zero results (the az512h fleet measured recall silently
// dead because of exactly this class of failure).
func TestToFTS5OrQuerySanitizes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain words", "volatile table overflow", "volatile OR table OR overflow"},
		{"punctuation stripped", "card_id: overflow? (DECIMAL, 14,2)", "card_id OR overflow OR decimal OR 14"},
		{"apostrophe split", "user's session won't persist", "user OR session OR won OR persist"},
		{"single word", "overflow", "overflow"},
		{"empty", "", ""},
		{"punctuation only", "?!,.;", ""},
		{"explicit operators pass through", `"volatile table" OR overflow`, `"volatile table" OR overflow`},
		{"unbalanced quote is sanitized, not passed", `overflow" OR table`, "overflow OR or OR table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toFTS5OrQuery(tt.in))
		})
	}
}
