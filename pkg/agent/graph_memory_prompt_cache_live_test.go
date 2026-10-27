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

//go:build fts5

package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Graph memory recall against the real Anthropic API: every call, the main
// chat calls and the graph memory side calls (extraction, recall query,
// re-rank), goes to the API, and the main calls' cache usage is what the API
// reports. Costs a few cents per run on the default model.
//
//	export ANTHROPIC_API_KEY=...
//	go test -tags fts5 -race ./pkg/agent/ -run TestGraphMemoryRecall_PromptCacheLive -v -timeout 15m
//
// LOOM_LIVE_CACHE_MODEL picks another model (default claude-haiku-5-5).

// usageRecordingLLM passes every call to the API and records the usage of the
// main chat calls (not the graph memory side calls), with the number of
// recall notes each one carried.
type usageRecordingLLM struct {
	LLMProvider
	mu    sync.Mutex
	calls []liveMainCall
}

type liveMainCall struct {
	usage llmtypes.Usage
	notes int
}

func isGraphMemorySideCall(msgs []llmtypes.Message) bool {
	return promptContains(msgs, "Extract entities, relationships, and memories") ||
		promptContains(msgs, "Distill this message into a concise search query") ||
		promptContains(msgs, "Candidate memories:")
}

func (l *usageRecordingLLM) Chat(ctx context.Context, msgs []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	resp, err := l.LLMProvider.Chat(ctx, msgs, tools)
	if err != nil || isGraphMemorySideCall(msgs) {
		return resp, err
	}
	notes := 0
	for _, m := range msgs {
		notes += strings.Count(m.Content, graphMemoryContextMarker)
	}
	l.mu.Lock()
	l.calls = append(l.calls, liveMainCall{usage: resp.Usage, notes: notes})
	l.mu.Unlock()
	return resp, nil
}

func (l *usageRecordingLLM) mainCalls() []liveMainCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]liveMainCall(nil), l.calls...)
}

// TestGraphMemoryRecall_PromptCacheLive has an eight-message conversation
// about a household's pets, one message right after another (inside the
// cache lifetime), with graph memory on. Each message after the first recalls
// what earlier ones said, so its recall note differs from the last one's. It
// logs, per message, the input tokens the API read from the cache, wrote to
// it and charged in full, and checks that each message reads back everything
// the previous one cached.
func TestGraphMemoryRecall_PromptCacheLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live API test")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	model := os.Getenv("LOOM_LIVE_CACHE_MODEL")
	if model == "" {
		model = "claude-haiku-5-5"
	}

	llm := &usageRecordingLLM{LLMProvider: anthropic.NewClient(anthropic.Config{
		APIKey:    apiKey,
		Model:     model,
		MaxTokens: 1024,
	})}

	store := newTestGraphMemoryStore(t)
	cfg := DefaultConfig()
	cfg.Name = "live-cache-agent"
	// Long enough to be cacheable on every model (the minimum is 4096 tokens
	// on some).
	cfg.SystemPrompt = "Help one household with everyday questions about their home and pets. " +
		"Answer in two or three sentences. Use what you remember about the household when it helps.\n\n" +
		strings.Repeat("When someone mentions a pet, keep its name, kind, age, habits, food, vet visits and "+
			"any worries about it straight, and do not mix one pet up with another. ", 160)
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	gm := DefaultGraphMemoryConfig()
	gm.Enabled = true
	gm.EnableExtraction = true
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithGraphMemoryStore(store, gm))

	conversation := []string{
		"We just adopted a beagle called Max. He is three years old.",
		"Max only eats salmon kibble; chicken upsets his stomach.",
		"Our cat Juniper is fourteen and doesn't like Max much yet.",
		"Max has his first vet visit on Friday at Oak Street Animal Clinic.",
		"Juniper takes a thyroid pill every morning with breakfast.",
		"Max keeps digging under the back fence. Any ideas?",
		"What should I tell the vet about Max's food on Friday?",
		"Remind me what each of our pets needs every day.",
	}
	ctx := context.Background()
	for _, msg := range conversation {
		_, err := ag.Chat(ctx, "s-live-cache", msg)
		require.NoError(t, err)
		ag.FlushGraphMemoryExtraction()
	}

	calls := llm.mainCalls()
	require.GreaterOrEqual(t, len(calls), len(conversation), "at least one main call per message")

	// Totals over every main call after the first message's.
	var read, written, full int
	var cost float64
	prevCached := 0
	for i, c := range calls {
		u := c.usage
		t.Logf("call %d: input %5d = cache read %5d + cache write %5d + uncached %4d, cost $%.5f, recall notes %d",
			i+1, u.CacheReadInputTokens+u.CacheCreationInputTokens+u.InputTokens,
			u.CacheReadInputTokens, u.CacheCreationInputTokens, u.InputTokens, u.CostUSD, c.notes)
		if i > 0 {
			read += u.CacheReadInputTokens
			written += u.CacheCreationInputTokens
			full += u.InputTokens
			cost += u.CostUSD

			assert.LessOrEqual(t, c.notes, 1, "call %d: one recall note per request", i+1)
			assert.GreaterOrEqual(t, u.CacheReadInputTokens, prevCached,
				"call %d: everything the previous call cached is read back", i+1)
		}
		prevCached = u.CacheReadInputTokens + u.CacheCreationInputTokens
	}
	total := read + written + full
	require.Positive(t, total)
	t.Logf("calls 2-%d (%s): %d input tokens: %d read from cache (%.0f%%), %d written to cache, %d uncached; cost $%.4f",
		len(calls), model, total, read, 100*float64(read)/float64(total), written, full, cost)
}
