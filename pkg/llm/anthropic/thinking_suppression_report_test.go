// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package anthropic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/llm"
)

// ThinkingOffWhenSuppressed says "off" exactly when the marked request drops
// the thinking field and the unmarked request sends it.
func TestThinkingOffWhenSuppressedMatchesTheRequest(t *testing.T) {
	t.Parallel()
	marked := llm.ContextSuppressThinking(context.Background())
	tests := []struct {
		name, model, level string
		want               bool
	}{
		{"budget form, thinking on", "claude-sonnet-4-5-20250929", "medium", true},
		{"adaptive form, thinking on", "claude-opus-5", "medium", false},
		{"budget form, thinking off", "claude-sonnet-4-5-20250929", "", false},
		{"budget form, level none", "claude-sonnet-4-5-20250929", "none", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{model: tc.model, thinkingLevel: tc.level, maxTokens: 64000}
			assert.Equal(t, tc.want, c.ThinkingOffWhenSuppressed(), "the report does not match the client's rule")
			dropped := c.thinkingParam(context.Background()) != nil && c.thinkingParam(marked) == nil
			assert.Equal(t, dropped, c.ThinkingOffWhenSuppressed(), "the report does not match what the marked request sends")
		})
	}
}
