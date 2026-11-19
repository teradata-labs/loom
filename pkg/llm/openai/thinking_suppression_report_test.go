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

package openai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// ThinkingOffWhenSuppressed says "off" exactly when the marked request drops
// the thinking field and the unmarked request sends it, and ThinkingForm
// names the field the unmarked request sends. The want columns are written
// out by hand, so a reporter that copies the builder's rules and misses one
// fails here. A max_tokens below 2048 leaves no valid budget (the API floor
// is 1024 and the budget must stay under max_tokens by 1024): the builder
// then sends no thinking at all, so the mark has nothing to turn off.
func TestThinkingOffWhenSuppressedMatchesTheRequest(t *testing.T) {
	t.Parallel()
	marked := llm.ContextSuppressThinking(context.Background())
	const budget = "claude-sonnet-4-5-20250929"
	tests := []struct {
		name, model, level string
		maxTokens          int
		want               bool
		wantForm           string
	}{
		{"budget form, thinking on", budget, "medium", 64000, true, llmtypes.ThinkingFormBudget},
		{"a model that is not Claude", "gpt-4o", "medium", 64000, false, llmtypes.ThinkingFormNone},
		{"adaptive form, thinking on", "claude-opus-5", "medium", 64000, false, llmtypes.ThinkingFormAdaptive},
		{"budget form, thinking off", budget, "", 64000, false, llmtypes.ThinkingFormNone},
		{"budget form, level none", budget, "none", 64000, false, llmtypes.ThinkingFormNone},
		{"budget form, max_tokens 1500 leaves no budget", budget, "medium", 1500, false, llmtypes.ThinkingFormNone},
		{"budget form, max_tokens 2047 leaves no budget", budget, "medium", 2047, false, llmtypes.ThinkingFormNone},
		{"budget form, max_tokens 2048 leaves the 1024 floor", budget, "medium", 2048, true, llmtypes.ThinkingFormBudget},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &Client{model: tc.model, thinkingLevel: tc.level, maxTokens: tc.maxTokens}
			assert.Equal(t, tc.want, c.ThinkingOffWhenSuppressed(), "the report does not match the request the client sends")
			assert.Equal(t, tc.wantForm, c.ThinkingForm(), "the form does not match the thinking field the client sends")
			dropped := c.thinkingParam(context.Background()) != nil && c.thinkingParam(marked) == nil
			assert.Equal(t, dropped, c.ThinkingOffWhenSuppressed(), "the report does not match what the marked request sends")
		})
	}
}
