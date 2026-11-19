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
	"path/filepath"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// An answer an LLM call billed is stored with the model that gave it; the
// person's own message names no model.
func TestABilledAnswerIsStoredWithItsModel(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	if _, err := ag.Chat(context.Background(), "model-s", "hello"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`SELECT role, model FROM messages WHERE session_id = 'model-s' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var role, model string
		if err := rows.Scan(&role, &model); err != nil {
			t.Fatal(err)
		}
		got[role] = model
	}
	if got["assistant"] != "mock-v1" {
		t.Errorf("the answer was stored with model %q, not the one that gave it", got["assistant"])
	}
	if got["user"] != "" {
		t.Errorf("the person's message was stored with model %q", got["user"])
	}
}

// detailedLLM answers once with usage that carries the thinking tokens, the
// cache write split by lifetime and the effort, as the Bedrock client reports.
type detailedLLM struct{}

func (detailedLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "done", Usage: llmtypes.Usage{
		InputTokens: 3, OutputTokens: 500, CostUSD: 0.01,
		CacheReadInputTokens: 9000, CacheCreationInputTokens: 700,
		CacheCreation1hInputTokens: 600, CacheCreation5mInputTokens: 100,
		ThinkingTokens: 420, Effort: "medium",
	}}, nil
}
func (detailedLLM) Name() string  { return "detailed" }
func (detailedLLM) Model() string { return "detailed-v1" }

// An answer is stored with the effort it was made at, the thinking it spent
// and its cache write split by lifetime.
func TestABilledAnswerIsStoredWithItsEffortThinkingAndCacheLifetime(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, detailedLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	if _, err := ag.Chat(context.Background(), "detail-s", "hello"); err != nil {
		t.Fatal(err)
	}
	var model, effort string
	var thinking, cw, cw1h, cw5m int
	err = store.db.QueryRow(`SELECT model, effort, thinking_tokens, cache_create_tokens, cache_create_1h_tokens, cache_create_5m_tokens
		FROM messages WHERE session_id = 'detail-s' AND role = 'assistant'`).Scan(&model, &effort, &thinking, &cw, &cw1h, &cw5m)
	if err != nil {
		t.Fatal(err)
	}
	if model != "detailed-v1" || effort != "medium" || thinking != 420 || cw != 700 || cw1h != 600 || cw5m != 100 {
		t.Errorf("stored model %q effort %q thinking %d cache write %d (1h %d, 5m %d); want detailed-v1, medium, 420, 700 (600, 100)",
			model, effort, thinking, cw, cw1h, cw5m)
	}
}
