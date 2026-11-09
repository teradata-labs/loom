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

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// windowedModel refuses, as a provider does, any prompt over limit tokens
// (counted at 3 bytes a token). Asked to work, it fans out fanOut calls in one
// step; told its step was too large, it answers.
type windowedModel struct {
	mu     sync.Mutex
	limit  int
	fanOut int
	calls  int
	told   bool
	// plain refuses with an error not labelled ErrContextTooLong, as
	// providers that do not classify their refusal do.
	plain bool
	// provider and model, when set, name a catalog model.
	provider, model string
}

func (w *windowedModel) Name() string {
	if w.provider != "" {
		return w.provider
	}
	return "windowed"
}

func (w *windowedModel) Model() string {
	if w.model != "" {
		return w.model
	}
	return "windowed-model"
}

func (w *windowedModel) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	bytes := 0
	for _, m := range msgs {
		bytes += len(m.Content)
	}
	if bytes/3 > w.limit {
		if w.plain {
			return nil, fmt.Errorf("API error (status 400): input is longer than the model accepts")
		}
		return nil, fmt.Errorf("API error (status 400): prompt is too long: %w", llm.ErrContextTooLong)
	}
	usage := llmtypes.Usage{InputTokens: bytes / 3, OutputTokens: 10}
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "together too large for the context") {
			w.told = true
		}
	}
	if w.told {
		return &LLMResponse{Content: "narrowed and done", StopReason: "end_turn", Usage: usage}, nil
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			return &LLMResponse{Content: "read them all", StopReason: "end_turn", Usage: usage}, nil
		}
	}
	calls := make([]ToolCall, w.fanOut)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("q%d", i), Name: "rows", Input: map[string]interface{}{"i": i}}
	}
	return &LLMResponse{StopReason: "tool_use", ToolCalls: calls, Usage: usage}, nil
}

func windowedAgent(t *testing.T, model *windowedModel, resultBytes int) (*Agent, *SessionStore) {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 20000
	cfg.ReservedOutputTokens = 1000
	cfg.MaxIterations = 100
	cfg.MaxToolExecutions = 100
	ag := NewAgent(&mockBackend{}, model, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	rows := strings.Repeat("id=12345 name=widget qty=7\n", resultBytes/27)
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: rows}, nil
		},
	})
	return ag, store
}

// One step returning more results than the window holds — each under the
// bound — does not end the turn. The provider refuses; the results are
// replaced with a note that says so, each call having run; the model re-plans
// and the turn goes on. The stored rows hold the note the model was sent.
func TestAStepTooLargeToSendBecomesANoteTheModelActsOn(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 40}
	ag, store := windowedAgent(t, model, 4000)

	resp, err := ag.Chat(context.Background(), "s-wide", "read every table")
	require.NoError(t, err, "a step too large to send ended the turn")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told, "the model was never told its step was too large")

	msgs, err := store.LoadMessages(context.Background(), "s-wide")
	require.NoError(t, err)
	var notes, rowsLeft int
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(m.Content, "together too large for the context") {
			notes++
		}
		if strings.Contains(m.Content, "name=widget") {
			rowsLeft++
		}
	}
	assert.Equal(t, 40, notes, "every call keeps a result: the note")
	assert.Zero(t, rowsLeft, "a stored row still holds the results the model was not sent")
}

// A provider that does not label its refusal gets the same answer: the step's
// results are counted before the send, and past the window they are replaced
// with the note, so the model re-plans and the turn goes on.
func TestAStepTooLargeIsCaughtBeforeAnUnlabelledRefusal(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 40, plain: true}
	ag, _ := windowedAgent(t, model, 4000)
	resp, err := ag.Chat(context.Background(), "s-plain", "read every table")
	require.NoError(t, err, "a step too large to send ended the turn on a provider that does not label its refusal")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told)
}

// The check is against the model's own window, not max_context_tokens: Opus 5
// configured to fold at 60k still accepts ten full results — more than 60k
// holds — so they are sent whole.
func TestAStepIsCheckedAgainstTheModelsWindowNotTheConfiguredOne(t *testing.T) {
	model := &windowedModel{limit: 900000, fanOut: 10, provider: "anthropic", model: "claude-opus-5"}
	ag, _ := windowedAgent(t, model, 16000)
	ag.config.MaxContextTokens = 60000
	ag.config.ReservedOutputTokens = 10000
	resp, err := ag.Chat(context.Background(), "s-opus", "read ten tables")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "results that fit the model's window were replaced")
}

// A step that uses up the tool budget ends the loop, and the synthesis send
// carries its results: they get the same check, so the model is told and
// answers instead of the turn falling back to the budget message.
func TestAStepThatEndsTheToolBudgetIsCheckedAtSynthesis(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 40, plain: true}
	ag, store := windowedAgent(t, model, 4000)
	ag.config.MaxToolExecutions = 40
	resp, err := ag.Chat(context.Background(), "s-synth", "read every table")
	require.NoError(t, err)
	assert.Equal(t, "narrowed and done", resp.Content, "the synthesis send was not checked")
	assert.True(t, model.told)

	// The final answer has no tools, so the note asks for an answer, not
	// another call.
	msgs, err := store.LoadMessages(context.Background(), "s-synth")
	require.NoError(t, err)
	answer := 0
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "Answer from what you had before them") {
			answer++
		}
	}
	assert.Equal(t, 40, answer, "the synthesis note asks for more calls on a send with no tools")
}

// A model served with a window below its listed one — a vLLM --max-model-len,
// a TGI --max-total-tokens — refuses what the listing says it takes. The served
// window, max_model_context_tokens, is the ceiling when it is set: a step the
// listed window would carry is fitted to the served one before the send, and
// a server that refuses without a label never sees it.
func TestAStepIsFittedToTheServedWindowWhenOneIsSet(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 10, plain: true, provider: "anthropic", model: "claude-opus-5"}
	ag, _ := windowedAgent(t, model, 16000)
	ag.config.MaxModelContextTokens = 20000
	resp, err := ag.Chat(context.Background(), "s-served", "read ten tables")
	require.NoError(t, err, "the step was sent against the listed window and the server refused")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told, "the step was not fitted to the served window")
}

// The served window comes first, on any provider; without one the listed
// window is the ceiling, as before.
func TestTheServedWindowIsTheCeilingBeforeTheListedOne(t *testing.T) {
	served := ProviderPromptCeiling("anthropic", "claude-opus-5", 400, 60000, 32000)
	assert.Equal(t, 32000-EffectiveOutputReservation("anthropic", "claude-opus-5", 0, 400, 32000), served)
	listed := ProviderPromptCeiling("anthropic", "claude-opus-5", 400, 60000, 0)
	assert.Greater(t, listed, 60000, "the listed window is not above the configured one; the test does not separate them")
}

// Ollama's window is the server's context length, which the model's listing
// does not give: when no served window is set, the configured window is the
// ceiling there, and the model's listed window only when none is configured.
func TestOllamasCeilingIsTheConfiguredWindow(t *testing.T) {
	listed := ProviderPromptCeiling("ollama", "llama3.1:8b", 400, 0, 0)
	configured := ProviderPromptCeiling("ollama", "llama3.1:8b", 400, 4096, 0)
	assert.Equal(t, 4096-EffectiveOutputReservation("ollama", "llama3.1:8b", 0, 400, 4096), configured)
	assert.Greater(t, listed, configured, "the listed window is not larger than 4096; the test does not separate them")
}

// A step that fits is not replaced because the cheap count says it might not:
// ten prose results that a count of 2 bytes a token puts over the ceiling are
// counted with the tokenizer, fit, and are sent and stored whole.
func TestAStepTheCheapCountOverstatesIsSentWhole(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 10}
	ag, store := windowedAgent(t, model, 0)
	prose := strings.Repeat("The quarterly report shows revenue growth across every region, led by the western offices. ", 38)
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: prose}, nil
		},
	})
	resp, err := ag.Chat(context.Background(), "s-prose", "read ten reports")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told, "results that fit the window were replaced")

	msgs, err := store.LoadMessages(context.Background(), "s-prose")
	require.NoError(t, err)
	whole := 0
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "western offices") {
			whole++
		}
	}
	assert.Equal(t, 10, whole, "a stored row was rewritten though its step fit")
}

// The reply reservation a builder resolved into the memory alone — max_tokens
// included, as the registry does — comes off the ceiling too: a step that
// would leave the reply no room is replaced before the send.
func TestTheCeilingReservesTheMemorysReply(t *testing.T) {
	model := &windowedModel{limit: 12000, fanOut: 10, plain: true}
	ag, _ := windowedAgent(t, model, 4000)
	ag.config.ReservedOutputTokens = 0
	ag.memory.SetContextLimits(20000, 8000)
	resp, err := ag.Chat(context.Background(), "s-reserve", "read ten tables")
	require.NoError(t, err, "the step was sent into the room kept for the reply")
	assert.Equal(t, "narrowed and done", resp.Content)
	assert.True(t, model.told)
}

// A step that fits is sent as it is: nothing is replaced.
func TestAStepThatFitsIsSentWhole(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 3}
	ag, _ := windowedAgent(t, model, 4000)
	resp, err := ag.Chat(context.Background(), "s-narrow", "read three tables")
	require.NoError(t, err)
	assert.Equal(t, "read them all", resp.Content)
	assert.False(t, model.told)
}

// A step so wide that even its notes do not fit ends the turn as before, with
// the recoverable context_exhausted error: the swap is one more send, not a
// loop.
func TestAStepTooWideEvenForItsNotesEndsTheTurn(t *testing.T) {
	model := &windowedModel{limit: 19000, fanOut: 300}
	ag, _ := windowedAgent(t, model, 4000)
	ag.config.MaxIterations = 400
	ag.config.MaxToolExecutions = 400
	_, err := ag.Chat(context.Background(), "s-widest", "read every table")
	var re *RecoverableError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "context_exhausted", re.ErrorType)
	assert.LessOrEqual(t, model.calls, 4, "the swap must be one more send, not a loop")
}

// RewriteToolResults rewrites the owner's tool rows and nothing else: another
// owner's call leaves them as they were.
func TestRewriteToolResultsIsTheOwners(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	var rw ResultRewriter = store
	ann := llmtypes.ContextWithUserID(context.Background(), "ann")
	require.NoError(t, store.SaveSession(ann, &Session{ID: "s-rw"}))
	for _, m := range []Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "rows"}}}, {Role: "tool", ToolUseID: "c1", Content: "rows"}} {
		mm := m
		require.NoError(t, store.SaveMessage(ann, "s-rw", &mm, m.Role == "user"))
	}
	msgs, _ := store.LoadMessages(ann, "s-rw")
	var seq int64
	_, _ = fmt.Sscan(msgs[2].ID, &seq)

	bob := llmtypes.ContextWithUserID(context.Background(), "bob")
	_ = rw.RewriteToolResults(bob, "s-rw", []int64{seq}, "bob was here")
	msgs, _ = store.LoadMessages(ann, "s-rw")
	assert.Equal(t, "rows", msgs[2].Content, "another owner rewrote the row")

	require.NoError(t, rw.RewriteToolResults(ann, "s-rw", []int64{seq, 1}, "the note"))
	msgs, _ = store.LoadMessages(ann, "s-rw")
	assert.Equal(t, "the note", msgs[2].Content)
	assert.Equal(t, "q", msgs[0].Content, "a row that is not a tool result was rewritten")
}
