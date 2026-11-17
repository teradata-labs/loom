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

// F5: the grounding gate matches whole words only, and ignores fragments
// shorter than 3 runes.
func TestLessonIsGroundedWholeWord(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		fragments []string
		want      bool
	}{
		{"review example: Avoid contains id", "Avoid validation errors: disable the audit trigger before querying", []string{"id", "uid"}, false},
		{"review example: provide contains id", "Always provide admin credentials before running the job", []string{"id", "uid"}, false},
		{"short fragment ignored even as a word", "set the id column to BIGINT", []string{"id"}, false},
		{"substring of a longer word", "the BIGINTEGER type is not supported", []string{"BIGINT"}, false},
		{"fragment as a prefix of a word", "use INTEGERS here", []string{"INTEGER"}, false},
		{"whole word, case-insensitive", "declare card_id as bigint to avoid overflow", []string{"BIGINT"}, true},
		{"whole word at end of text", "declare the column BIGINT", []string{"BIGINT"}, true},
		{"fragment with punctuation inside", "widen to DECIMAL(18,2) for the sums", []string{"DECIMAL(18,2"}, true},
		{"underscore is a word rune", "rename uid_col first", []string{"uid"}, false},
		{"exactly 3 runes as a word", "join on uid instead of name", []string{"uid"}, true},
		{"later occurrence matches after an embedded one", "UIDs differ; compare the uid column", []string{"uid"}, true},
		{"multibyte fragment counted in runes", "the value ÄÖ is invalid", []string{"ÄÖ"}, false},
		{"no fragments", "declare BIGINT", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grounding := map[string]bool{}
			for _, f := range tt.fragments {
				grounding[strings.ToLower(f)] = true
			}
			assert.Equal(t, tt.want, lessonIsGrounded(tt.content, grounding))
		})
	}
}
