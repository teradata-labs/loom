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

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAdaptiveThinkingModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6", true},
		{"anthropic/claude-sonnet-4.6", true}, // Vercel AI Gateway spelling
		{"anthropic/claude-opus-4.7", true},
		{"anthropic/claude-opus-5.5", true},
		{"anthropic/claude-haiku-5.5", true},
		{"claude-sonnet-4-5-20250929", false},
		{"anthropic/claude-sonnet-4.5", false},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", false},
		{"anthropic.claude-haiku-4-5", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, IsAdaptiveThinkingModel(tt.model), tt.model)
	}
}

func TestEffortForLevel_DottedID(t *testing.T) {
	assert.Equal(t, "high", EffortForLevel("anthropic/claude-opus-4.7", "high"))
	assert.Equal(t, "", EffortForLevel("anthropic/claude-sonnet-4.5", "high"))
}
