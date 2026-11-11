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

// denseResult is one token-dense tool payload: structured rows, every row
// different, so the accurate tier counts it like real data.
func denseResult(i int) string {
	return strings.Repeat(fmt.Sprintf("row %d acct=%05d region=%d spend=%d.%02d; ", i, i*37, i%7, i*113, i%100), 60)
}

// parallelBatchMemory builds a settled turn 1 and a current turn 2 holding one
// assistant row with n tool calls followed by their n results (IDs 1000+i).
func parallelBatchMemory(t *testing.T, maxTokens, reserved, n int, toolName string) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(16384) // production default: every result below renders whole
	sm.AddMessage(ctx, Message{Role: "user", Content: "hi", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "hello", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "run them all in parallel", Turn: 2})
	calls := make([]ToolCall, n)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: toolName, Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2, ToolCalls: calls})
	for i := 0; i < n; i++ {
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", 1000+i),
			ToolUseID: fmt.Sprintf("c%d", i), Content: denseResult(i), Turn: 2})
	}
	return sm
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

// A step whose results take the context past G is not ended before the send:
// G is the budget a fold keeps the context under, not the provider's window.
// The turn goes on and the model answers. Before, a pre-send guard read G as
// the window and ended every such turn with current_turn_overflow — every
// SWE-bench task at G=36000 died at its second step.
func TestChat_AStepPastGIsNotEndedBeforeTheSend(t *testing.T) {
	mockLLM := &fanOutLLM{n: 300}
	patternCfg := DefaultPatternConfig()
	patternCfg.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, mockLLM, WithConfig(&Config{
		MaxTurns:             5,
		MaxToolExecutions:    1000,
		MaxIterations:        1000,
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
	resp, err := ag.Chat(context.Background(), "past-g", "run them all in parallel")
	require.NoError(t, err, "a step past G was ended before the send")
	assert.NotEmpty(t, resp.Content)
}

// --- the transient tail: sent, never stored, still counted -------------------

// denseText is token-dense conversation text (never stubbed, so a fold is the
// only way to shed it).
func denseText(tag string, reps int) string {
	var b strings.Builder
	for i := 0; i < reps; i++ {
		fmt.Fprintf(&b, "%s note %d: acct=%05d region=%d spend=%d.%02d; ", tag, i, i*37, i%7, i*113, i%100)
	}
	return b.String()
}

// TestTransientTail_CountedByReliefAndCalibration — the <system-reminder>
// tail (graph memory recall note, soft reminders) is appended to a copy of the
// compiled context and never stored, but it is sent. A large tail on a context
// just under the start mark must make relief shed before the send, and the
// calibration after the send must compare the provider's count with a count
// that includes the tail.
func TestTransientTail_CountedByReliefAndCalibration(t *testing.T) {
	ctx := context.Background()

	t.Run("relief sheds for the tail", func(t *testing.T) {
		// usable 15000 → start 13500. Prior turns fill to ~85% of the start
		// mark; the current turn is one short user message.
		sm := NewSegmentedMemory("ROM", 16000, 1000)
		sm.SetThreshold(16384)
		comp := &countingCompressor{}
		sm.SetCompressor(comp)
		sm.mu.Lock()
		start := sm.startMarkLocked(0)
		sm.mu.Unlock()
		turn := int64(1)
		for {
			sm.AddMessage(ctx, Message{Role: "user", Content: denseText(fmt.Sprintf("q%d", turn), 10), Turn: turn})
			sm.AddMessage(ctx, Message{Role: "assistant", Content: denseText(fmt.Sprintf("a%d", turn), 10), Turn: turn})
			turn++
			sm.mu.Lock()
			est := sm.accurateTokensLocked(sm.compileLocked()) // the tokenizer's count: the estimate is a byte bound far from the mark
			sm.mu.Unlock()
			if est >= start*85/100 {
				break
			}
		}
		sm.AddMessage(ctx, Message{Role: "user", Content: "next", Turn: turn})
		sm.mu.Lock()
		before := sm.accurateTokensLocked(sm.compileLocked())
		sm.mu.Unlock()
		require.Less(t, before, start, "fixture: the stored context alone is under the start mark")

		note := "[Graph Memory Context]\n" + denseText("mem", 150)
		tailTokens := GetTokenCounter().CountTokens(systemReminderContent(note))
		require.Greater(t, before+tailTokens, start, "fixture: context plus tail is over the start mark")

		sm.SetTransientTail(TurnReminder{Text: note})
		shed, estimate, target := sm.ReleasePressure(ctx, 0)
		assert.True(t, shed, "relief must count the tail and shed before the send")
		assert.LessOrEqual(t, estimate, target)

		// Without the tail the same context is under the mark: no shed.
		sm2 := NewSegmentedMemory("ROM", 16000, 1000)
		sm2.SetThreshold(16384)
		sm2.SetCompressor(&countingCompressor{})
		for i := int64(1); i < turn; i++ {
			sm2.AddMessage(ctx, Message{Role: "user", Content: denseText(fmt.Sprintf("q%d", i), 10), Turn: i})
			sm2.AddMessage(ctx, Message{Role: "assistant", Content: denseText(fmt.Sprintf("a%d", i), 10), Turn: i})
		}
		sm2.AddMessage(ctx, Message{Role: "user", Content: "next", Turn: turn})
		shed2, _, _ := sm2.ReleasePressure(ctx, 0)
		assert.False(t, shed2, "fixture: with no tail the context is under the start mark")
	})

	t.Run("calibration counts the tail", func(t *testing.T) {
		sm := parallelBatchMemory(t, 200000, 20000, 10, "run_query")
		note := "[Graph Memory Context]\n" + denseText("mem", 75)
		tail := systemReminderContent(note)
		sm.mu.Lock()
		stored := sm.accurateTokensLocked(sm.compileLocked())
		sm.mu.Unlock()
		require.GreaterOrEqual(t, stored, calibrationMinSampleTokens)

		// The provider counts exactly what was sent, tokenizer for tokenizer:
		// the compiled context plus the tail.
		sm.SetTransientTail(TurnReminder{Text: note})
		sent := stored + GetTokenCounter().CountTokens(tail)
		require.Greater(t, float64(sent)/float64(stored), 1.05,
			"fixture: the tail is large enough that leaving it out would skew the factor")
		for i := 0; i < 4; i++ {
			sm.ObservePromptTokens(sent)
		}
		assert.InDelta(t, 1.0, sm.EstimateCalibration(), 0.001,
			"a 1:1 count stays at 1.0 when the tail is counted")
	})

	t.Run("reset clears the tail", func(t *testing.T) {
		sm := NewSegmentedMemory("ROM", 200000, 20000)
		sm.SetTransientTail(TurnReminder{Text: "note"})
		sm.ResetContext()
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		assert.Empty(t, sm.transientTail)
	})
}
