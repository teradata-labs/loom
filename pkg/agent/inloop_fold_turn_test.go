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
	if strings.Contains(msgs[len(msgs)-1].Content, "Context maintenance: summarise the work so far") {
		if f.refuseFolds > 0 {
			f.refuseFolds--
			return nil, fmt.Errorf("API error (status 400): prompt is too long: %w", llm.ErrContextTooLong)
		}
		f.folds++
		cb(foldSummaryJSON)
		return &LLMResponse{Content: foldSummaryJSON, StopReason: "end_turn", Usage: usage}, nil
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
