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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// --- fold gate: no lossy fold when the current turn alone is over target ------

// countingCompressor records every fold compressor call.
type countingCompressor struct {
	mu    sync.Mutex
	calls int
}

func (c *countingCompressor) CompressMessages(context.Context, []Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return "covers msg:1-999\nsummary", nil
}
func (c *countingCompressor) IsEnabled() bool { return true }
func (c *countingCompressor) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// denseText is token-dense conversation text (never stubbed, so a fold is the
// only way to shed it).
func denseText(tag string, reps int) string {
	var b strings.Builder
	for i := 0; i < reps; i++ {
		fmt.Fprintf(&b, "%s note %d: acct=%05d region=%d spend=%d.%02d; ", tag, i, i*37, i%7, i*113, i%100)
	}
	return b.String()
}

func TestReleasePressure_OffloadsCurrentTurnBeforeFoldWhenTurnAloneExceedsTarget(t *testing.T) {
	ctx := context.Background()
	// usable 15000 → target 9000. Prior turn: ~1K tokens of conversation text
	// (foldable, not evictable). Current turn: 40 × ~750-token results (~30K) —
	// alone far over target, and comfortably under it once stubbed.
	sm := NewSegmentedMemory("ROM", 16000, 1000)
	sm.SetThreshold(16384)
	comp := &countingCompressor{}
	sm.SetCompressor(comp)
	sm.AddMessage(ctx, Message{Role: "user", Content: denseText("q1", 40), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: denseText("a1", 40), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "run them all", Turn: 2})
	calls := make([]ToolCall, 40)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: "run_query", Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2, ToolCalls: calls})
	for i := range calls {
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", 1000+i), ToolUseID: fmt.Sprintf("c%d", i),
			Content: denseResult(i), Turn: 2})
	}

	shed, estimate, target := sm.ReleasePressure(ctx, 0)
	require.True(t, shed)
	assert.LessOrEqual(t, estimate, target)
	assert.Equal(t, 0, comp.n(), "the lossless current-turn offload reached target — no lossy fold was needed")
	assert.False(t, sm.HasL2Content(), "prior-turn conversation survives intact")
}

func TestReleasePressure_FoldSkippedWhenItCannotReachTarget(t *testing.T) {
	ctx := context.Background()
	// usable 11000 → start 9900, target 6600. The current turn carries an
	// unsheddable ~7.5K-token user message (floor over target on its own) plus
	// a tool batch that pushes the total over the start mark. Offloading the
	// batch brings the estimate back under the start mark but the floor stays
	// over target — a fold of turn 1 could not reach it, so it must not run.
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.SetThreshold(16384)
	comp := &countingCompressor{}
	sm.SetCompressor(comp)
	sm.AddMessage(ctx, Message{Role: "user", Content: denseText("q1", 10), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: denseText("a1", 10), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: denseText("big", 330), Turn: 2})
	calls := make([]ToolCall, 8)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: "run_query", Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2, ToolCalls: calls})
	for i := range calls {
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", 1000+i), ToolUseID: fmt.Sprintf("c%d", i),
			Content: denseResult(i), Turn: 2})
	}

	sm.mu.Lock()
	start, tgt := sm.startMarkLocked(0), sm.releaseMarkLocked(0)
	floor, before := sm.currentTurnFloorLocked(2), sm.estimateLocked()
	sm.mu.Unlock()
	require.GreaterOrEqual(t, before, start, "fixture: the pass must trigger")
	require.Greater(t, floor, tgt, "fixture: the current turn alone is over target")

	shed, estimate, target := sm.ReleasePressure(ctx, 0)
	assert.True(t, shed, "the current-turn offload shed")
	assert.Greater(t, estimate, target, "no operation can reach target here")
	assert.Less(t, estimate, start, "but the estimate is back under the start mark")
	assert.Equal(t, 0, comp.n(), "a fold that cannot reach target is a lossy call for nothing — skipped")
}

// --- calibration: the estimate follows the provider's prompt count ------------

func TestObservePromptTokens_Calibration(t *testing.T) {
	tests := []struct {
		name       string
		ratio      float64 // provider count ÷ loom's count
		samples    int
		wantFactor func(f float64) bool
	}{
		{"claude-like undercount raises the factor", 1.25, 6, func(f float64) bool { return f > 1.2 && f <= 1.25 }},
		{"provider counting fewer never lowers below 1.0", 0.7, 3, func(f float64) bool { return f == 1.0 }},
		{"a skewed sample is clamped at 2.0", 5.0, 12, func(f float64) bool { return f > 1.99 && f <= calibrationMax }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := parallelBatchMemory(t, 200000, 20000, 10, "run_query")
			sm.mu.Lock()
			counted := sm.accurateTokensLocked(sm.compileLocked())
			sm.mu.Unlock()
			require.GreaterOrEqual(t, counted, calibrationMinSampleTokens)
			for i := 0; i < tt.samples; i++ {
				sm.ObservePromptTokens(int(float64(counted) * tt.ratio))
			}
			f := sm.EstimateCalibration()
			assert.True(t, tt.wantFactor(f), "factor %.3f", f)
		})
	}
}

func TestObservePromptTokens_SmallPromptIgnored(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "hi", Turn: 1})
	sm.ObservePromptTokens(5000) // 5000 ÷ ~10 counted would be a wild ratio
	assert.Equal(t, 1.0, sm.EstimateCalibration(), "fixed provider overhead dominates tiny prompts")
}

func TestEstimate_ScalesWithCalibration(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query") // over the start mark → accurate tier
	sm.mu.Lock()
	before := sm.estimateLocked()
	counted := sm.accurateTokensLocked(sm.compileLocked())
	sm.mu.Unlock()
	for i := 0; i < 8; i++ {
		sm.ObservePromptTokens(counted * 3 / 2)
	}
	sm.mu.Lock()
	after := sm.estimateLocked()
	sm.mu.Unlock()
	assert.InDelta(t, float64(before)*sm.EstimateCalibration(), float64(after), float64(before)*0.01)
	assert.Greater(t, after, before)
}

// --- synthesis: relief and the refusal backstop before the final call ---------

// synthesisRefusalLLM keeps calling tools (exhausting MaxTurns), then refuses
// the tool-free synthesis call `refusals` times with a context-too-long error.
type synthesisRefusalLLM struct {
	mockToolCallingLLM
	refusals int

	smu        sync.Mutex
	synthCalls int
}

func (s *synthesisRefusalLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	if len(tools) > 0 {
		return s.mockToolCallingLLM.Chat(ctx, messages, tools)
	}
	s.smu.Lock()
	s.synthCalls++
	n := s.synthCalls
	s.smu.Unlock()
	if n <= s.refusals {
		return nil, fmt.Errorf("stream error: 400 prompt is too long: %w", llm.ErrContextTooLong)
	}
	return &llmtypes.LLMResponse{Content: "synthesized answer", Usage: llmtypes.Usage{InputTokens: 50, OutputTokens: 10}}, nil
}

func (s *synthesisRefusalLLM) calls() int {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.synthCalls
}

func TestSynthesis_ResendsOnceAfterContextTooLong(t *testing.T) {
	tests := []struct {
		name        string
		refusals    int
		wantCalls   int
		wantContent string
	}{
		{"one refusal: relief + resend succeeds", 1, 2, "synthesized answer"},
		{"two refusals: exactly one resend, then the guidance fallback", 2, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := &synthesisRefusalLLM{mockToolCallingLLM: mockToolCallingLLM{alwaysCallTools: true}, refusals: tt.refusals}
			patternCfg := DefaultPatternConfig()
			patternCfg.UseLLMClassifier = false
			ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(&Config{
				MaxTurns:          3,
				MaxToolExecutions: 50,
				PatternConfig:     patternCfg,
			}))
			ag.RegisterTool(&mockCalculatorTool{})

			resp, err := ag.Chat(context.Background(), "synth-"+fmt.Sprint(tt.refusals), "keep calling tools")
			require.NoError(t, err)
			assert.Equal(t, tt.wantCalls, mockLLM.calls())
			if tt.wantContent != "" {
				assert.Equal(t, tt.wantContent, resp.Content)
			} else {
				assert.NotEqual(t, "synthesized answer", resp.Content)
				assert.Contains(t, resp.Metadata, "synthesis_error")
			}
		})
	}
}

// --- current-turn overflow ends the turn instead of looping -----------------

// fanOutLLM answers the first call with n parallel calls to dense_rows and
// every later call with a final answer, counting calls.
type fanOutLLM struct {
	mockToolCallingLLM
	n     int
	fmu   sync.Mutex
	calls int
}

func (f *fanOutLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.calls++
	if f.calls > 1 {
		return &llmtypes.LLMResponse{Content: "answer from previews", Usage: llmtypes.Usage{InputTokens: 50, OutputTokens: 10}}, nil
	}
	calls := make([]llmtypes.ToolCall, f.n)
	for i := range calls {
		calls[i] = llmtypes.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "dense_rows", Input: map[string]interface{}{"i": i}}
	}
	return &llmtypes.LLMResponse{ToolCalls: calls, Usage: llmtypes.Usage{InputTokens: 50, OutputTokens: 10}}, nil
}

func (f *fanOutLLM) count() int {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	return f.calls
}

func TestChat_CurrentTurnOverflowEndsTurnWithRecoverableError(t *testing.T) {
	tests := []struct {
		name      string
		fanOut    int
		wantError bool
	}{
		{"stubs alone over start: turn ends before the next send", 300, true},
		{"relief fits the turn: the model answers", 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := &fanOutLLM{n: tt.fanOut}
			patternCfg := DefaultPatternConfig()
			patternCfg.UseLLMClassifier = false
			ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(&Config{
				MaxTurns:             5,
				MaxToolExecutions:    1000,
				MaxIterations:        1000, // per-turn call cap: the whole fan-out runs
				MaxContextTokens:     20000,
				ReservedOutputTokens: 1000,
				PatternConfig:        patternCfg,
			}))
			var row atomic.Int64
			ag.RegisterTool(&shuttle.MockTool{
				MockName: "dense_rows",
				MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
					return &shuttle.Result{Success: true, Data: denseResult(int(row.Add(1)))}, nil
				},
			})

			resp, err := ag.Chat(context.Background(), "overflow-"+fmt.Sprint(tt.fanOut), "run them all in parallel")
			if !tt.wantError {
				require.NoError(t, err)
				assert.Equal(t, "answer from previews", resp.Content)
				return
			}
			var re *RecoverableError
			require.ErrorAs(t, err, &re)
			assert.Equal(t, "current_turn_overflow", re.ErrorType)
			assert.False(t, re.Retryable, "resending the same turn cannot fit")
			assert.Contains(t, re.Message, "split the work into smaller batches")
			assert.Equal(t, tt.fanOut, re.RecoveryPayload["tool_results"])
			assert.Equal(t, 1, mockLLM.count(), "no send after relief has nothing left to shed")
		})
	}
}
