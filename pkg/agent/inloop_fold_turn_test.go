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
	"math"
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

const foldSummaryJSON = `{"summary": "STATE-OF-WORK-SUMMARY"}`

// foldingStream is a streaming model that works in steps — each one reasoning
// at length and calling a tool — until it has taken steps of them, then
// answers. It answers a fold request with the summary, streamed like any
// reply, and refuses the first refuseFolds fold requests as too long. Every
// call costs callCost.
type foldingStream struct {
	mu          sync.Mutex
	steps       int
	taken       int
	folds       int
	refuseFolds int
	// toolFolds answers the first toolFolds fold requests with tool calls.
	toolFolds int
	// sent is every call's messages, in order; foldAt marks the fold calls.
	sent   [][]Message
	foldAt []bool
	// summary is the fold reply's summary; empty means the short default.
	summary string
	// foldInput, when set, is the prompt size a fold call reports.
	foldInput int
}

const callCost = 0.01

func (f *foldingStream) Name() string  { return "folding" }
func (f *foldingStream) Model() string { return "folding-model" }

func (f *foldingStream) Chat(ctx context.Context, msgs []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	return f.ChatStream(ctx, msgs, tools, func(string) {})
}

func (f *foldingStream) ChatStream(_ context.Context, msgs []Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	usage := llmtypes.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: callCost}
	isFold := strings.Contains(msgs[len(msgs)-1].Content, "Context maintenance: summarise the work so far")
	f.sent = append(f.sent, append([]Message(nil), msgs...))
	f.foldAt = append(f.foldAt, isFold)
	if isFold {
		if f.toolFolds > 0 {
			f.toolFolds--
			return &LLMResponse{Content: "", StopReason: "tool_use", Usage: usage,
				ToolCalls: []ToolCall{{ID: "fold-tool", Name: "step", Input: map[string]interface{}{}}}}, nil
		}
		if f.refuseFolds > 0 {
			f.refuseFolds--
			return nil, fmt.Errorf("API error (status 400): prompt is too long: %w", llm.ErrContextTooLong)
		}
		f.folds++
		if f.foldInput > 0 {
			usage.InputTokens = f.foldInput
		}
		reply := foldSummaryJSON
		if f.summary != "" {
			reply = `{"summary": "` + f.summary + `"}`
		}
		cb(reply)
		return &LLMResponse{Content: reply, StopReason: "end_turn", Usage: usage}, nil
	}
	if f.taken >= f.steps {
		cb("final answer")
		return &LLMResponse{Content: "final answer", StopReason: "end_turn", Usage: usage}, nil
	}
	f.taken++
	reasoning := strings.Repeat("working the step through carefully. ", 120)
	cb(reasoning)
	return &LLMResponse{Content: reasoning, StopReason: "tool_use", Usage: usage,
		ToolCalls: []ToolCall{{ID: fmt.Sprintf("s%d", f.taken), Name: "step", Input: map[string]interface{}{}}}}, nil
}

func foldingAgent(t *testing.T, model *foldingStream) (*Agent, *SessionStore) {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 12000
	cfg.ReservedOutputTokens = 2000
	cfg.MaxIterations = 100
	cfg.MaxTurns = 200 // fold calls are loop passes too: the long jobs here must not hit the turn cap
	cfg.MaxToolExecutions = 100
	ag := NewAgent(&mockBackend{}, model, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "step",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: "ok"}, nil
		},
	})
	return ag, store
}

// A turn long enough to fold: the fold's summary is the harness's and never
// reaches the client's progress stream, and what the fold calls cost is on the
// session's rows, so the rows sum to what the turn spent.
func TestInloopFold_ATurnThatFoldsStreamsNoSummaryAndBillsEveryCall(t *testing.T) {
	model := &foldingStream{steps: 24}
	ag, store := foldingAgent(t, model)
	var mu sync.Mutex
	var streamed []string
	resp, err := ag.ChatWithProgress(context.Background(), "s-fold", "do the long job", func(e ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		streamed = append(streamed, e.PartialContent)
	})
	require.NoError(t, err)
	require.Equal(t, "final answer", resp.Content)
	require.Positive(t, model.folds, "the turn never folded; the test does not reach the fold")

	for _, s := range streamed {
		assert.NotContains(t, s, "STATE-OF-WORK-SUMMARY", "the fold's summary was streamed to the client")
	}

	// Every row, the folded ones too: a fold hides a row from the model, not
	// from the ledger.
	rows, err := store.SessionCost(context.Background(), "s-fold")
	require.NoError(t, err)
	assert.InDelta(t, resp.TurnUsage.CostUSD, rows, 1e-9, "the rows do not sum to what the turn spent")
	calls := model.taken + 1 + model.folds
	assert.InDelta(t, float64(calls)*callCost, resp.TurnUsage.CostUSD, 1e-9, "the turn did not count every call")
	assert.Less(t, math.Abs(rows-float64(calls)*callCost), 1e-9)
}

// A fold call refused as too long is re-sent with the fold still asked for:
// its reply is the summary, taken as the fold, and the model's real answers
// all reach the user.
func TestInloopFold_ARefusedFoldCallIsResentAsAFold(t *testing.T) {
	model := &foldingStream{steps: 24, refuseFolds: 1}
	ag, _ := foldingAgent(t, model)
	resp, err := ag.Chat(context.Background(), "s-refused", "do the long job")
	require.NoError(t, err)
	require.Positive(t, model.folds, "the fold never landed after its refusal")
	assert.Equal(t, "final answer", resp.Content)
	assert.NotContains(t, resp.Content, "STATE-OF-WORK-SUMMARY", "a fold reply was returned as the answer")
	assert.Equal(t, 24, model.taken, "a step's reply was taken as the fold and thrown away")
}

// A fold call sends no step results, so a fold call refused twice replaces
// none: the step's stored results stay as they arrived.
func TestInloopFold_ARefusedFoldCallReplacesNoResults(t *testing.T) {
	model := &foldingStream{steps: 24, refuseFolds: 2}
	ag, store := foldingAgent(t, model)
	_, _ = ag.Chat(context.Background(), "s-refused-twice", "do the long job")
	require.Contains(t, model.foldAt, true, "no fold call was made; the test does not reach the case")
	msgs, err := store.LoadMessages(context.Background(), "s-refused-twice")
	require.NoError(t, err)
	for _, m := range msgs {
		if m.Role == "tool" {
			assert.NotContains(t, m.Content, "together too large for the context", "a refused fold call replaced a result it never sent")
		}
	}
}

// k counts agent steps. In one user turn, each fold keeps the last k steps and
// the summary stands in for every step before them, so what a fold leaves is
// the same size after the twentieth step as after the fifth: the floor is
// flat, and a long turn can fold as many times as it needs.
func TestInloopFold_KCountsAgentStepsSoTheFloorIsFlat(t *testing.T) {
	sm := newCompileMemory(t)
	sm.SetProtectedRecentTurns(3)
	ctx := context.Background()
	sm.AddMessage(ctx, Message{Role: "user", Content: "do the long job", Turn: 1})
	step := func(i int) {
		id := fmt.Sprintf("s%d", i)
		sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1, Content: fmt.Sprintf("step %d: %s", i, strings.Repeat("reasoning ", 40)),
			ToolCalls: []ToolCall{{ID: id, Name: "step"}}})
		sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: id, Turn: 1, Content: strings.Repeat("result ", 400)})
	}

	var floors []int
	for i := 1; i <= 20; i++ {
		step(i)
		if i%5 != 0 {
			continue
		}
		applied, _ := sm.ApplyFoldReply(ctx, "state of the work so far") // one length: the floor is measured, not the summary
		require.True(t, applied, "fold after step %d", i)
		var text, results int
		for _, m := range sm.GetMessages() {
			switch m.Role {
			case "assistant":
				// Measured without the step's number, whose digits vary.
				if _, body, ok := strings.Cut(m.Content, ": "); ok {
					text += len(body)
				} else {
					text += len(m.Content)
				}
			case "tool":
				results++
			}
		}
		floors = append(floors, text)
		assert.Equal(t, 1, results, "after the fold at step %d only the pending result remains", i)
		kept := 0
		for _, m := range sm.GetMessages() {
			if m.Role == "assistant" && strings.HasPrefix(m.Content, "step ") {
				kept++
			}
		}
		assert.Equal(t, 4, kept, "after the fold at step %d: the last 3 steps and the pending one", i)
	}
	for i := 1; i < len(floors); i++ {
		assert.Equal(t, floors[0], floors[i], "the floor grew between folds: %v", floors)
	}
}

// A scripted turn calls no model, so it is never asked for a fold: its text is
// the turn's reply, never a summary — even past G, and even when the replayed
// text looks like a fold reply.
func TestInloopFold_AScriptedTurnIsNeverTakenAsAFold(t *testing.T) {
	model := &foldingStream{steps: 24}
	ag, _ := foldingAgent(t, model)
	ctx := context.Background()
	_, err := ag.Chat(ctx, "s-replay", "do the long job")
	require.NoError(t, err)

	// Grow the session past G again without a model: big tool rows.
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay", ag.config.Name, "")
	rows := strings.Repeat("id=12345 name=widget qty=7 status=open\n", 60)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("r%d", i)
		ag.appendMessage(ctx, sess, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "step"}}}, false)
		ag.appendMessage(ctx, sess, Message{Role: "tool", ToolUseID: id, Content: rows}, false)
	}
	segMem := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, segMem.NeedsFold(), "the session is not past G; the test does not reach the fold")

	replay := `{"summary": "REPLAYED-ASSISTANT-TEXT"}`
	resp, err := ag.Chat(WithScriptedResponse(ctx, replay), "s-replay", "next")
	require.NoError(t, err)
	assert.Equal(t, replay, resp.Content)
	assert.NotContains(t, segMem.summary.text, "REPLAYED-ASSISTANT-TEXT", "a scripted reply was taken as the fold's summary")
}

// A fold call is the previous call's request exactly, plus the fold ask: it
// carries no result of the step in flight — the fold keeps those whole — so
// its whole prefix is the one the previous call cached.
func TestInloopFold_TheFoldCallIsThePreviousCallsRequest(t *testing.T) {
	model := &foldingStream{steps: 24}
	ag, _ := foldingAgent(t, model)
	_, err := ag.Chat(context.Background(), "s-prefix", "do the long job")
	require.NoError(t, err)
	require.Positive(t, model.folds)
	checked := 0
	for i := 1; i < len(model.sent); i++ {
		if !model.foldAt[i] || model.foldAt[i-1] {
			continue
		}
		prev, fold := model.sent[i-1], model.sent[i]
		require.Equal(t, len(prev)+1, len(fold), "the fold call is the previous request plus the ask")
		for j := range prev {
			assert.Equal(t, prev[j].Role, fold[j].Role, "call %d message %d", i, j)
			assert.Equal(t, prev[j].Content, fold[j].Content, "call %d message %d", i, j)
		}
		checked++
	}
	require.Positive(t, checked, "no fold call followed a step call")
}

// countingCompressor counts the pressure ladder's summary calls.
type countingCompressor struct{ calls int }

func (c *countingCompressor) CompressMessages(context.Context, []Message) (string, error) {
	c.calls++
	return "compressed", nil
}
func (c *countingCompressor) IsEnabled() bool { return true }

// The in-loop fold is the only thing that sheds before a send: a turn that
// folds again and again never runs the pressure ladder, whose estimate counts
// the summary and so would cross G first on every cycle after the first.
func TestInloopFold_NoLadderRunsBeforeASend(t *testing.T) {
	// A summary the size real runs write (2-3K tokens): it is what puts the
	// ladder's estimate over G before the fold's.
	model := &foldingStream{steps: 40, summary: strings.Repeat("state of the work, decisions and paths. ", 200)}
	ag, store := foldingAgent(t, model)
	comp := &countingCompressor{}
	ag.memory.SetCompressor(comp)
	_, err := ag.Chat(context.Background(), "s-noladder", "do the long job")
	require.NoError(t, err)
	require.GreaterOrEqual(t, model.folds, 2, "the turn did not fold more than once")
	assert.Zero(t, comp.calls, "the pressure ladder shed before a send")
	snaps, err := store.LoadMemorySnapshots(context.Background(), "s-noladder", "fold", 100)
	require.NoError(t, err)
	assert.Equal(t, model.folds, len(snaps), "a summary was written by something other than the in-loop fold")
}

// A fold reply that is tool calls was made without the step's results, which
// the fold call leaves out: it is discarded, not executed, and the fold is
// asked again.
func TestInloopFold_AToolCallFoldReplyIsNotExecuted(t *testing.T) {
	model := &foldingStream{steps: 24, toolFolds: 1}
	ag, _ := foldingAgent(t, model)
	var stepRuns int
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "step",
		MockExecute: func(_ context.Context, in map[string]interface{}) (*shuttle.Result, error) {
			stepRuns++
			return &shuttle.Result{Success: true, Data: "ok"}, nil
		},
	})
	resp, err := ag.Chat(context.Background(), "s-toolfold", "do the long job")
	require.NoError(t, err)
	assert.Equal(t, "final answer", resp.Content)
	require.Positive(t, model.folds, "the fold was not asked again after a tool-call reply")
	assert.Equal(t, model.taken, stepRuns, "a fold reply's tool call was executed")
}

// Calibration compares what a call billed with loom's count of what it sent.
// A fold call sends only the settled context, so it is left out: the factor a
// turn ends with does not depend on what its fold calls reported.
func TestInloopFold_AFoldCallDoesNotMoveCalibration(t *testing.T) {
	factor := func(foldInput int) float64 {
		model := &foldingStream{steps: 24, foldInput: foldInput}
		ag, _ := foldingAgent(t, model)
		_, err := ag.Chat(context.Background(), "s-cal", "do the long job")
		require.NoError(t, err)
		require.Positive(t, model.folds, "the turn never folded; the test does not reach the fold")
		sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-cal", ag.config.Name, "")
		return sess.SegmentedMem.(*SegmentedMemory).EstimateCalibration()
	}
	assert.Equal(t, factor(100), factor(900000), "a fold call's reported prompt moved the calibration factor")
}

// A fold call does not carry the step in flight, so the step is not fitted to
// the window on that send: results that fit once the fold has shed the past
// are not replaced because they stood beside a past the fold was about to shed.
func TestInloopFold_AFoldCallDoesNotFitTheStepItDoesNotCarry(t *testing.T) {
	// Six steps of about 2,200 tokens each under a 10,000-token ceiling: the
	// fold arms with a step in flight beside a past that together pass the
	// ceiling, and every send after a fold fits.
	model := &foldingStream{steps: 6}
	ag, store := foldingAgent(t, model)
	result := strings.Repeat("id=12345 name=widget qty=7\n", 200)
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "step",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: result}, nil
		},
	})
	_, err := ag.Chat(context.Background(), "s-fold-step", "do the long job")
	require.NoError(t, err)
	require.Contains(t, model.foldAt, true, "no fold call was made; the test does not reach the case")
	msgs, err := store.LoadMessages(context.Background(), "s-fold-step")
	require.NoError(t, err)
	for _, m := range msgs {
		if m.Role == "tool" {
			assert.NotContains(t, m.Content, "together too large for the context", "a fold call fitted, and replaced, a step it does not carry")
		}
	}
}
