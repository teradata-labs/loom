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
// config on a server whose llm.max_tokens is serverMaxTokens: the runtime
// config from RegistryAgentConfig, the memory's limits from the same figures.
// A nil llmCfg is an agent with no llm block, on the server's client. Each
// rows call returns resultTokens tokens.
func realLineAgent(t *testing.T, model *tokModel, llmCfg *loomv1.LLMConfig, serverMaxTokens, resultTokens int) *Agent {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	if llmCfg != nil {
		llmCfg.Provider, llmCfg.Model = model.provider, model.model
	}
	cfg := RegistryAgentConfig(WithServerMaxTokens(&loomv1.AgentConfig{Name: "real-line", Llm: llmCfg}, serverMaxTokens), nil)
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
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxTokens: 8192}, 4096, 2450)
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
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxTokens: 8192}, 4096, 2450)
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
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxModelContextTokens: 20000, MaxTokens: 1000, ReservedOutputTokens: 1000}, 4096, 2380)
	resp, err := ag.Chat(context.Background(), "s-first", "read seven tables")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a first step that fits was replaced")
	assert.Greater(t, model.seen, 16000)
}

// Review round 5, Q9. An agent that sets no max_tokens sends the server's
// llm.max_tokens, 4,096 here, so the provider takes a prompt up to 200,000 -
// 4,096. A 56-result step of about 137k tokens fits and is sent whole;
// reserving the catalog's 64,000 output replaced it.
func TestAnAgentWithNoMaxTokensReservesTheServers(t *testing.T) {
	model := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 4096, fanOut: 56}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{}, 4096, 2450)
	resp, err := ag.Chat(context.Background(), "s-server-max", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a step the provider accepts was replaced")
	assert.Greater(t, model.seen, 130000, "the provider never saw the step whole")
}

// An agent with no llm block runs on the server's client, whose max_tokens is
// 32,000 here: llama3.3 takes a prompt up to 128,000 - 32,000. A step of about
// 100k tokens is over that line and under the window less the memory's
// default tenth; it is caught, not sent and refused.
func TestAnAgentOnTheServersClientReservesTheServersMaxTokens(t *testing.T) {
	model := &tokModel{provider: "ollama", model: "llama3.3", limit: 128000 - 32000, fanOut: 41}
	ag := realLineAgent(t, model, nil, 32000, 2450)
	resp, err := ag.Chat(context.Background(), "s-server-client", "read every table")
	require.NoError(t, err, "a step over the provider's line was sent and refused")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told)
}

// The max_tokens on an agent's config, as every build starts from it, is the
// agent's when it names a provider and sets one, otherwise the server's: an
// agent with no provider is on the server's client, whatever it states. The
// stored config is not changed.
func TestTheMaxTokensOnTheConfigIsTheAgentsOrTheServers(t *testing.T) {
	resolved := func(llm *loomv1.LLMConfig, server int) int32 {
		return WithServerMaxTokens(&loomv1.AgentConfig{Name: "a", Llm: llm}, server).GetLlm().GetMaxTokens()
	}
	assert.EqualValues(t, 8192, resolved(&loomv1.LLMConfig{Provider: "anthropic", MaxTokens: 8192}, 4096))
	assert.EqualValues(t, 4096, resolved(&loomv1.LLMConfig{Provider: "anthropic"}, 4096))
	assert.EqualValues(t, 4096, resolved(&loomv1.LLMConfig{MaxTokens: 8192}, 4096))
	assert.EqualValues(t, 4096, resolved(nil, 4096))
	assert.EqualValues(t, 0, resolved(&loomv1.LLMConfig{Provider: "anthropic"}, 0))

	stored := &loomv1.AgentConfig{Name: "a", Llm: &loomv1.LLMConfig{MaxTokens: 8192}}
	WithServerMaxTokens(stored, 4096)
	assert.EqualValues(t, 8192, stored.Llm.MaxTokens)
}

// The step is fitted against the max_tokens that is sent, not the fold's wider
// reservation. llama3.3 with max_tokens 2,000 and reserved_output_tokens
// 10,000: the provider takes a prompt up to 128,000 - 2,000. A step of about
// 122k tokens fits and is sent whole; fitted against the 10,000 it was
// replaced.
func TestAStepIsFittedAgainstMaxTokensNotTheFoldReservation(t *testing.T) {
	model := &tokModel{provider: "ollama", model: "llama3.3", limit: 128000 - 2000, fanOut: 50}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{MaxTokens: 2000, ReservedOutputTokens: 10000}, 4096, 2450)
	resp, err := ag.Chat(context.Background(), "s-exact", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a step the provider accepts was replaced")
	assert.Greater(t, model.seen, 118000, "the provider never saw the step whole")
}

// An agent built in Go with no reservation, on a client that sends its
// default 4,096: the provider takes a prompt up to 200,000 - 4,096. A
// 56-result step of about 137k tokens fits and is sent whole; the model's
// listed 64,000 output is not what such a client sends.
func TestAGoBuiltAgentIsNotFittedToTheListedOutput(t *testing.T) {
	model := &tokModel{provider: "anthropic", model: "claude-sonnet-4-5-20250929", limit: 200000 - 4096, fanOut: 56}
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := DefaultConfig()
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
	resp, err := ag.Chat(context.Background(), "s-go-built", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a step the provider accepts was replaced")
	assert.Greater(t, model.seen, 130000)
}

// A model nothing lists, with no window configured, is not fitted to loom's
// own default window: a proxy alias for a large model takes a step past
// 200,000 less a tenth, and the step is sent whole.
func TestAnUnlistedModelWithNoWindowIsNotFitted(t *testing.T) {
	model := &tokModel{provider: "litellm", model: "prod-large", limit: 1000000 - 4096, fanOut: 80}
	ag := realLineAgent(t, model, &loomv1.LLMConfig{}, 4096, 2450)
	resp, err := ag.Chat(context.Background(), "s-unlisted", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "a step the provider accepts was fitted to loom's default window")
	assert.Greater(t, model.seen, 185000)
}

// The first provider count is the calibration, not half of it: after one
// sample at 1.25, the next step is counted at 1.25.
func TestTheFirstSampleIsTheCalibration(t *testing.T) {
	sm := newCompileMemory(t)
	ctx := context.Background()
	sm.AddMessage(ctx, Message{Role: "user", Content: strings.Repeat("the quarterly report shows growth across every region. ", 400), Turn: 1})
	sm.mu.Lock()
	counted := sm.accurateTokensLocked(sm.compileLocked())
	sm.mu.Unlock()
	require.GreaterOrEqual(t, counted, calibrationMinSampleTokens)
	sm.ObservePromptTokens(counted * 5 / 4)
	assert.InDelta(t, 1.25, sm.EstimateCalibration(), 0.01)
}
