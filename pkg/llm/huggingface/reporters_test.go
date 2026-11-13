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
package huggingface

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/types"
)

// The HuggingFace client forwards both reporters of the OpenAI-shaped client
// it wraps: types.MaxTokensReporter and types.ThinkingSuppressionReporter.
// Both interfaces are optional and are found by a type assertion, so a client
// that loses a forwarding method still compiles; this test then fails on the
// assertion. Config has no thinking level, so the "off" case puts an inner
// client with thinking on in place; a forwarder that returns a fixed value
// fails one of the two rows.
func TestHuggingFaceForwardsTheReportersOfItsInnerClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		inner     *openai.Client
		maxTokens int
		wantOff   bool
		wantForm  string
	}{
		{
			name:      "inner Claude budget form with thinking on",
			inner:     openai.NewClient(openai.Config{APIKey: "k", Model: "claude-sonnet-4-5", ThinkingLevel: "medium", MaxTokens: 64000}),
			maxTokens: 64000, wantOff: true, wantForm: types.ThinkingFormBudget,
		},
		{
			name:      "inner default model, no thinking",
			maxTokens: 3210, wantOff: false, wantForm: types.ThinkingFormNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(Config{Token: "k", MaxTokens: tt.maxTokens})
			if tt.inner != nil {
				c.openai = tt.inner
			}
			var client types.LLMProvider = c

			mt, ok := client.(types.MaxTokensReporter)
			require.True(t, ok, "the client must forward types.MaxTokensReporter")
			assert.Equal(t, tt.maxTokens, mt.MaxTokens(), "the client must report the inner client's max_tokens")

			r, ok := client.(types.ThinkingSuppressionReporter)
			require.True(t, ok, "the client must forward types.ThinkingSuppressionReporter")
			assert.Equal(t, tt.wantOff, r.ThinkingOffWhenSuppressed(), "the client must report the inner client's thinking_off answer")
			assert.Equal(t, tt.wantForm, r.ThinkingForm(), "the client must report the inner client's thinking form")
		})
	}
}
