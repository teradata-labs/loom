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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/prompts"
)

// behavior.preflight_fit and behavior.auto_compact_percent reach the agent's
// Config through the registry, and both are off unless set.
func TestCompactionBehaviorKeys_ReachTheAgentConfig(t *testing.T) {
	yamlFor := func(extra string) string {
		return `
agent:
  name: compaction_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
  behavior:
` + extra
	}
	registry, _ := createTestRegistry(t)
	build := func(extra string) *Config {
		cfg, err := LoadConfigFromString(yamlFor(extra))
		require.NoError(t, err)
		cfg.Llm = createTestAgentConfig("x").Llm // the registry's mock provider
		ag, err := registry.buildAgent(context.Background(), cfg)
		require.NoError(t, err)
		return ag.GetConfig()
	}

	on := build("    preflight_fit: true\n    auto_compact_percent: 65\n")
	assert.True(t, on.PreflightFit)
	assert.Equal(t, 65, on.AutoCompactPercent)

	off := build("    max_turns: 5\n")
	assert.False(t, off.PreflightFit)
	assert.Zero(t, off.AutoCompactPercent)
}

func TestCompactionBehaviorKeys_AutoCompactPercentIsValidated(t *testing.T) {
	for _, bad := range []string{"100", "150", "-1"} {
		_, err := LoadConfigFromString(`
agent:
  name: compaction_agent
  behavior:
    auto_compact_percent: ` + bad + "\n")
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "auto_compact_percent must be 0 (off) to 99")
	}

	cfg := &Config{}
	ApplyMetadataBehavior(cfg, map[string]string{"_auto_compact_percent": "100"})
	assert.Zero(t, cfg.AutoCompactPercent, "a hand-built out-of-range value stays off")
}

// The fold framing lines ship in the YAML with the same text as the fallback.
func TestFoldPromptsShippedYAMLMatchesFallback(t *testing.T) {
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	for key, fallback := range foldPromptFallbacks {
		got, err := reg.Get(context.Background(), key, nil)
		require.NoError(t, err, key)
		assert.Equal(t, fallback, got, key)
	}
}

// A registry entry replaces the built-in framing text the compressor sees.
func TestFoldPrompts_ComeFromTheRegistry(t *testing.T) {
	llm := &capturingLLM{response: "covers msg:1-2\nSUMMARY"}
	reg := &keyedPrompts{values: map[string]string{
		compactionPromptKey:    "Summarize.",
		foldPromptFocusPreface: "REGISTRY FOCUS:",
	}}
	sm := toolTurnMemory(t, 200000, 20000, 6)
	sm.SetCompressor(NewLLMCompressor(newPromptRegistryCompressor(llm, reg)))

	_, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2, Focus: "the settled SQL"})
	require.NoError(t, err)

	var sent string
	for _, m := range llm.lastMessages() {
		sent += m.Content
	}
	assert.Contains(t, sent, "REGISTRY FOCUS: the settled SQL")
	assert.NotContains(t, sent, foldPromptFallbacks[foldPromptFocusPreface])
}
