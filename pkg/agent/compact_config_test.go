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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/prompts"
)

// behavior.preflight_fit and behavior.auto_compact_percent are retired: the
// in-loop fold and the step check cover their job. A file that still sets
// either one loads, through the registry too, with one warning per key, and
// the agent it builds is the agent the file builds without the key. An
// auto_compact_percent that was out of range (and refused at load) now also
// loads with the warning.
func TestCompactionBehaviorKeys_RetiredKeysLoadWithAWarning(t *testing.T) {
	yamlFor := func(extra string) string {
		return `
agent:
  name: compaction_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
  behavior:
    max_turns: 5
` + extra
	}
	registry, _ := createTestRegistry(t)
	build := func(t *testing.T, extra string) *Config {
		t.Helper()
		cfg, err := LoadConfigFromString(yamlFor(extra))
		require.NoError(t, err, "a retired key must not stop the file from loading")
		cfg.Llm = createTestAgentConfig("x").Llm // the registry's mock provider
		ag, err := registry.buildAgent(context.Background(), cfg)
		require.NoError(t, err, "a retired key must not stop the agent from building")
		return ag.GetConfig()
	}
	baseline := build(t, "")

	tests := []struct {
		name  string
		extra string
		keys  []string
	}{
		{"preflight_fit", "    preflight_fit: true\n", []string{"preflight_fit"}},
		{"auto_compact_percent", "    auto_compact_percent: 65\n", []string{"auto_compact_percent"}},
		{"auto_compact_percent out of the old range", "    auto_compact_percent: 150\n", []string{"auto_compact_percent"}},
		{"both", "    preflight_fit: true\n    auto_compact_percent: 65\n", []string{"preflight_fit", "auto_compact_percent"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.WarnLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()

			path := filepath.Join(t.TempDir(), "a.yaml")
			require.NoError(t, os.WriteFile(path, []byte(yamlFor(tt.extra)), 0o600))
			_, err := LoadAgentConfig(path)
			require.NoError(t, err, "the file loads")
			for _, key := range tt.keys {
				assert.Equal(t, 1, logs.FilterMessageSnippet("behavior."+key+" is no longer read").Len(),
					"the retired key %s must be reported once", key)
			}
			assert.Equal(t, len(tt.keys), logs.Len(), "no other warning is logged")

			got := build(t, tt.extra)
			assert.Equal(t, baseline.Metadata, got.Metadata, "the retired key leaves no behavior flag")
			assert.Equal(t, baseline.MaxTurns, got.MaxTurns, "the keys around it still load")
		})
	}
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

// The summary row the compile sends after a fold takes its heading and its
// caveat from the prompt registry. The estimate still recognises the row by
// the heading this memory sends, so the fold trigger leaves the summary out
// of its count, and the request log counts the row as the summary.
func TestSummaryRow_ComesFromTheRegistry(t *testing.T) {
	t.Parallel()
	reg := &keyedPrompts{values: map[string]string{
		summaryRowHeadingKey: "REGISTRY HEADING:",
		summaryRowCaveatKey:  "REGISTRY CAVEAT.",
	}}
	sm := toolTurnMemory(t, 200000, 20000, 6)
	sm.SetCompressor(NewLLMCompressor(newPromptRegistryCompressor(&capturingLLM{response: "S"}, reg)))
	sm.mu.Lock()
	sm.summary = summaryState{n: 1, text: strings.Repeat("the settled SQL. ", 200)}
	sm.l1Dirty = true
	sm.mu.Unlock()

	msgs := sm.GetMessagesForLLM()
	var row string
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "the settled SQL.") {
			row = m.Content
		}
	}
	require.NotEmpty(t, row, "the compile sent no summary row")
	assert.True(t, strings.HasPrefix(row, "<system-reminder>\nREGISTRY HEADING:\n\n"), "the row does not open with the registry heading")
	assert.Contains(t, row, "\n\nREGISTRY CAVEAT.\n</system-reminder>", "the row does not end with the registry caveat")
	assert.NotContains(t, row, foldPromptFallbacks[summaryRowHeadingKey], "the shipped heading was sent")
	assert.NotContains(t, row, foldPromptFallbacks[summaryRowCaveatKey], "the shipped caveat was sent")

	sm.mu.Lock()
	withSummary, withoutSummary := sm.estimateOptLocked(false), sm.estimateOptLocked(true)
	sm.mu.Unlock()
	assert.Less(t, withoutSummary, withSummary, "the fold trigger did not recognise the summary row")

	b := measureRequest(msgs, nil, sm.SummaryRowPrefix())
	assert.Positive(t, b.SummaryTokens, "the request log did not count the row as the summary")
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

// An AgentConfig stored by an earlier loom can carry _preflight_fit and
// _auto_compact_percent in its metadata. The agent built from it gets one
// warning per key in the process, through the registry too, and the key
// changes nothing.
func TestCompactionBehaviorKeys_RetiredMetadataKeysWarnOnce(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	retiredMetadataWarned = sync.Map{}
	t.Cleanup(func() { retiredMetadataWarned = sync.Map{} })

	registry, _ := createTestRegistry(t)
	build := func(md map[string]string) *Config {
		t.Helper()
		cfg := createTestAgentConfig("stored")
		cfg.Metadata = md
		ag, err := registry.buildAgent(context.Background(), cfg)
		require.NoError(t, err, "a retired metadata key must not stop the agent from building")
		return ag.GetConfig()
	}
	baseline := build(nil)
	require.Zero(t, logs.Len(), "an agent with no retired key logs no warning")

	stored := map[string]string{"_preflight_fit": "true", "_auto_compact_percent": "65"}
	got := build(stored)
	_ = build(stored)
	ApplyMetadataBehavior(&Config{Name: "direct"}, stored)

	for _, key := range []string{"_preflight_fit", "_auto_compact_percent"} {
		assert.Equal(t, 1, logs.FilterField(zap.String("metadata_key", key)).Len(), "the retired metadata key %s must be reported once", key)
	}
	assert.Equal(t, 2, logs.Len(), "no other warning is logged")
	assert.Equal(t, 1, logs.FilterMessageSnippet("llm.max_model_context_tokens").Len(), "the preflight_fit warning names the setting that does its job")
	assert.Equal(t, baseline.ParallelReadTools, got.ParallelReadTools, "the retired keys change no behavior flag")
	assert.Equal(t, baseline.MaxTurns, got.MaxTurns, "the retired keys change no setting")
}
