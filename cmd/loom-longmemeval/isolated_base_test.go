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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestIsolatedBaseFromKeepsAgentAndAppliesBenchmarkMemory: temp agents are
// clones of the named agent (system prompt, LLM, decision layer) with the
// benchmark's extraction settings on top. Before this, --agent was ignored
// in isolate mode and every temp agent ran a hand-built config.
func TestIsolatedBaseFromKeepsAgentAndAppliesBenchmarkMemory(t *testing.T) {
	t.Parallel()
	src := &loomv1.AgentConfig{
		Name:         "longmemeval-jev",
		SystemPrompt: "Help the user and remember what they tell you.",
		Llm:          &loomv1.LLMConfig{Provider: "azure-openai", Model: "gpt-4o"},
		Decision: &loomv1.DecisionConfig{Provider: "jev", Model: "typesafe-ai/jev", AllowAlias: true,
			Bands: []*loomv1.DecisionBand{{Site: "recall.rerank", ActMin: 0.5}}},
		Memory: &loomv1.MemoryConfig{GraphMemory: &loomv1.GraphMemoryConfig{
			Enabled: true, EnableExtraction: false, MaxRecallCandidates: 50, ContextBudgetPercent: 25,
		}},
		Behavior: &loomv1.BehaviorConfig{MaxTurns: 10},
	}
	got := isolatedBaseFrom(src)

	assert.Equal(t, src.SystemPrompt, got.SystemPrompt)
	assert.Equal(t, "gpt-4o", got.GetLlm().GetModel())
	require.NotNil(t, got.Decision, "the decision layer configured on the agent reaches the temp agents")
	require.Len(t, got.Decision.Bands, 1)
	assert.Equal(t, "recall.rerank", got.Decision.Bands[0].Site)
	gm := got.GetMemory().GetGraphMemory()
	assert.True(t, gm.EnableExtraction, "benchmark turns extraction on")
	assert.Equal(t, int32(1), gm.ExtractionCadence)
	assert.Equal(t, int32(1), gm.ConversationExtractionCadence)
	assert.Equal(t, int32(50), gm.MaxRecallCandidates, "the agent's own recall limit is kept")
	assert.Equal(t, int32(25), gm.ContextBudgetPercent, "an explicit budget is kept")
	assert.Equal(t, int32(10), got.GetBehavior().GetMaxTurns(), "the agent's behavior is kept")
	assert.False(t, src.GetMemory().GetGraphMemory().EnableExtraction, "the source is not modified")

	def := defaultIsolatedBase()
	assert.NotContains(t, def.SystemPrompt, "You are")
	assert.True(t, def.GetMemory().GetGraphMemory().EnableExtraction)
	assert.Nil(t, def.Decision)

	bare := isolatedBaseFrom(&loomv1.AgentConfig{Name: "bare"})
	require.NotNil(t, bare.GetMemory().GetGraphMemory())
	assert.Equal(t, int32(30), bare.GetMemory().GetGraphMemory().ContextBudgetPercent)
	assert.Equal(t, int32(50), bare.GetBehavior().GetMaxTurns())
}
