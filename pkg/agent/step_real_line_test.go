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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// tokModel refuses, without a label, any prompt over limit tokens as the
// tokenizer counts them: a provider at its real line, not a line a test
// chose in bytes. Asked to work it fans out fanOut calls; shown their results
// it answers; shown the step-results note it says so. seen is the largest
// prompt it accepted.
type tokModel struct {
	mu              sync.Mutex
	provider, model string
	limit, fanOut   int
	told            bool
	seen            int
}

func (m *tokModel) Name() string  { return m.provider }
func (m *tokModel) Model() string { return m.model }
func (m *tokModel) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tc := GetTokenCounter()
	tokens := 0
	for _, msg := range msgs {
		tokens += tc.CountTokens(msg.Content)
	}
	if tokens > m.limit {
		return nil, fmt.Errorf("API error (status 400): input is longer than the model accepts (%d > %d)", tokens, m.limit)
	}
	if tokens > m.seen {
		m.seen = tokens
	}
	usage := llmtypes.Usage{InputTokens: tokens, OutputTokens: 10}
	sawTool := false
	for _, msg := range msgs {
		if msg.Role == "tool" {
			sawTool = true
			if strings.Contains(msg.Content, "together too large for the context") {
				m.told = true
			}
		}
	}
	if m.told {
		return &LLMResponse{Content: "narrowed and done", StopReason: "end_turn", Usage: usage}, nil
	}
	if sawTool {
		return &LLMResponse{Content: "read them all", StopReason: "end_turn", Usage: usage}, nil
	}
	calls := make([]ToolCall, m.fanOut)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("q%d", i), Name: "rows", Input: map[string]interface{}{"i": i}}
	}
	return &LLMResponse{StopReason: "tool_use", ToolCalls: calls, Usage: usage}, nil
}

// realLineAgent is an agent built as the registry builds one from an LLM
// config: the runtime config from RegistryAgentConfig, the memory's limits
// from the same figures. Each rows call returns resultTokens tokens.
func realLineAgent(t *testing.T, model *tokModel, llmCfg *loomv1.LLMConfig, resultTokens int) *Agent {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	llmCfg.Provider, llmCfg.Model = model.provider, model.model
	cfg := RegistryAgentConfig(&loomv1.AgentConfig{Name: "real-line", Llm: llmCfg}, nil)
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxToolExecutions = 200
	cfg.MaxIterations = 200
	ag := NewAgent(&mockBackend{}, model, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	line := "id=12345 name=widget qty=7\n"
	per := GetTokenCounter().CountTokens(strings.Repeat(line, 100)) / 100
	rows := strings.Repeat(line, resultTokens/per)
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(_ context.Context, in map[string]interface{}) (*shuttle.Result, error) {
			// Each call's result is its own: a repeated result is not stored twice.
			return &shuttle.Result{Success: true, Data: fmt.Sprintf("table %v\n%s", in["i"], rows)}, nil
		},
	})
	return ag
}

// Review round 5, B15. claude-sonnet-4-5 with max_tokens 8192: the provider
// takes a prompt up to 200,000 - 8,192. A 56-result step of about 137k tokens
// fits and is sent whole; reserving the catalog's 64,000 output replaced it.
func TestAStepUnderTheWireLineIsSentWhole(t *testing.T) {
	model := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 8192, fanOut: 56}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxTokens: 8192}, 2450)
	resp, err := ag.Chat(context.Background(), "s-wire", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a step the provider accepts was replaced")
	assert.Greater(t, model.seen, 130000, "the provider never saw the step whole")
}

// The same model, one step over its wire line: caught before the unlabelled
// refusal, so the turn answers.
func TestAStepOverTheWireLineIsCaught(t *testing.T) {
	model := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 8192, fanOut: 80}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxTokens: 8192}, 2450)
	resp, err := ag.Chat(context.Background(), "s-over", "read every table")
	require.NoError(t, err, "a step over the wire line reached the provider and was refused")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told)
}

// Review round 5, B16. Provider openai, served window 20,000, reservation
// 1,000: a first step of about 16.8k tokens fits under 19,000 by the
// provider's own tokenizer and is sent whole; Claude's prior replaced it.
func TestAFirstStepOnAnotherProviderIsCountedAsItIs(t *testing.T) {
	model := &tokModel{provider: "openai", model: "served-model", limit: 19000, fanOut: 7}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxModelContextTokens: 20000, MaxTokens: 1000, ReservedOutputTokens: 1000}, 2380)
	resp, err := ag.Chat(context.Background(), "s-first", "read seven tables")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a first step that fits was replaced")
	assert.Greater(t, model.seen, 16000)
}

// Review round 5, Q9. An agent built with no limits and no max_tokens: the
// client sends the model's listed output size, so the provider's line is the
// window less that. The memory's default reservation is a tenth of the
// window; a step between the two lines is caught, not sent and refused.
func TestAnAgentWithNoLimitsReservesTheListedOutput(t *testing.T) {
	model := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 64000, fanOut: 57}
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := DefaultConfig() // a Go build that names no window and no reservation
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxToolExecutions, cfg.MaxIterations = 200, 200
	ag := NewAgent(&mockBackend{}, model, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	line := "id=12345 name=widget qty=7\n"
	rows := strings.Repeat(line, 2450/(GetTokenCounter().CountTokens(strings.Repeat(line, 100))/100))
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(_ context.Context, in map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: fmt.Sprintf("table %v\n%s", in["i"], rows)}, nil
		},
	})
	resp, err := ag.Chat(context.Background(), "s-nolimits", "read every table")
	require.NoError(t, err, "a step over the provider's line was sent and refused")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told)
}
