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
package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/bedrockmantle"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// The wrappers that production puts around a client forward both reporters
// of the client they wrap: types.MaxTokensReporter and
// types.ThinkingSuppressionReporter. `looms serve` wraps every agent client
// in the instrumented provider, so the agent sees the clients' answers only
// through these forwarders. Both interfaces are optional and are found by a
// type assertion, so a wrapper that loses a forwarding method still compiles;
// this test then fails on the assertion. Each wrapper is checked with an
// inner client that answers "off" (budget form) and one that does not, so a
// forwarder that returns a fixed value fails too. (HuggingFace wraps an
// unexported OpenAI-shaped client; its forwarders are tested in its own
// package.)
func TestWrappersForwardTheReportersOfTheClientTheyWrap(t *testing.T) {
	clearMantleEnv(t)
	const sends = 64000
	tests := []struct {
		name     string
		build    func(t *testing.T) types.LLMProvider
		wantOff  bool
		wantForm string
	}{
		{
			name: "instrumented, inner budget form with thinking on",
			build: func(*testing.T) types.LLMProvider {
				return llm.NewInstrumentedProvider(
					anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-sonnet-4-5", ThinkingLevel: "medium", MaxTokens: sends}),
					observability.NewNoOpTracer())
			},
			wantOff: true, wantForm: types.ThinkingFormBudget,
		},
		{
			name: "instrumented, inner adaptive form",
			build: func(*testing.T) types.LLMProvider {
				return llm.NewInstrumentedProvider(
					anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-opus-5", ThinkingLevel: "medium", MaxTokens: sends}),
					observability.NewNoOpTracer())
			},
			wantOff: false, wantForm: types.ThinkingFormAdaptive,
		},
		{
			name: "instrumented, inner client that does not report thinking",
			build: func(*testing.T) types.LLMProvider {
				return llm.NewInstrumentedProvider(
					gemini.NewClient(gemini.Config{APIKey: "k", Model: "gemini-2.5-flash", MaxTokens: sends}),
					observability.NewNoOpTracer())
			},
			wantOff: false, wantForm: types.ThinkingFormUnknown,
		},
		{
			name: "bedrock-mantle, Claude route, budget form with thinking on",
			build: func(t *testing.T) types.LLMProvider {
				c, err := bedrockmantle.NewClientForModel(bedrockmantle.Config{
					Region: "us-west-2", APIKey: "ABSKtest", Model: "anthropic.claude-haiku-4-5", ThinkingLevel: "medium", MaxTokens: sends})
				require.NoError(t, err, "the Mantle client must build")
				return c
			},
			wantOff: true, wantForm: types.ThinkingFormBudget,
		},
		{
			name: "bedrock-mantle, Claude route, thinking off",
			build: func(t *testing.T) types.LLMProvider {
				c, err := bedrockmantle.NewClientForModel(bedrockmantle.Config{
					Region: "us-west-2", APIKey: "ABSKtest", Model: "anthropic.claude-haiku-4-5", MaxTokens: sends})
				require.NoError(t, err, "the Mantle client must build")
				return c
			},
			wantOff: false, wantForm: types.ThinkingFormNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.build(t)

			mt, ok := client.(types.MaxTokensReporter)
			require.True(t, ok, "the wrapper must forward types.MaxTokensReporter")
			assert.Equal(t, sends, mt.MaxTokens(), "the wrapper must report the inner client's max_tokens")

			r, ok := client.(types.ThinkingSuppressionReporter)
			require.True(t, ok, "the wrapper must forward types.ThinkingSuppressionReporter")
			assert.Equal(t, tt.wantOff, r.ThinkingOffWhenSuppressed(), "the wrapper must report the inner client's thinking_off answer")
			assert.Equal(t, tt.wantForm, r.ThinkingForm(), "the wrapper must report the inner client's thinking form")
		})
	}
}
