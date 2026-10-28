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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestConsecutiveFailureTracker(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	params := map[string]interface{}{
		"sql": "SELECT * FROM test",
	}

	// Record first failure
	count1 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 1, count1)

	// Record second failure (same signature)
	count2 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 2, count2)

	// Clear on success
	tracker.clear("query_tool", params)

	// Should restart counting
	count3 := tracker.record("query_tool", params, "syntax_error")
	assert.Equal(t, 1, count3)
}

func TestConsecutiveFailureTracker_DifferentSignatures(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	params1 := map[string]interface{}{"sql": "SELECT 1"}
	params2 := map[string]interface{}{"sql": "SELECT 2"}

	// Different params = different signatures
	count1 := tracker.record("query_tool", params1, "error")
	count2 := tracker.record("query_tool", params2, "error")

	assert.Equal(t, 1, count1)
	assert.Equal(t, 1, count2) // Separate counter
}

func TestGetEscalationMessage(t *testing.T) {
	tracker := newConsecutiveFailureTracker()

	// Below threshold
	msg1 := tracker.getEscalationMessage(1, 2)
	assert.Empty(t, msg1)

	// At threshold
	msg2 := tracker.getEscalationMessage(2, 2)
	assert.NotEmpty(t, msg2)
	assert.Contains(t, msg2, "ESCALATION")
	assert.Contains(t, msg2, "2 times in a row")
}

func TestBuildSoftReminderLegacy(t *testing.T) {
	// Test with default config (MaxToolExecutions=50, threshold at 37)
	maxTools := 50

	// Below threshold (75% of 50 = 37)
	msg1 := buildSoftReminder(5, maxTools)
	assert.Empty(t, msg1)

	// At threshold
	msg2 := buildSoftReminder(38, maxTools)
	assert.NotEmpty(t, msg2)
	assert.Contains(t, msg2, "38")
	assert.Contains(t, msg2, "IMPORTANT")

	// Above threshold but within window
	msg3 := buildSoftReminder(40, maxTools)
	assert.NotEmpty(t, msg3)

	// At 90% (stop reminding)
	msg4 := buildSoftReminder(45, maxTools)
	assert.Empty(t, msg4)
}

func TestKeealiveProgress(t *testing.T) {
	callCount := 0
	callback := func(p ProgressEvent) {
		callCount++
		assert.Equal(t, StageToolExecution, p.Stage)
		assert.Contains(t, p.Message, "Still executing")
		assert.Contains(t, p.ToolName, "test_tool")
	}

	ctx := context.Background()
	done := make(chan struct{})
	startTime := time.Now()

	// Start keepalive in goroutine
	go keepaliveProgress(ctx, done, "test_tool", startTime, callback)

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Stop keepalive
	close(done)

	// Give it time to stop
	time.Sleep(50 * time.Millisecond)

	// Should not have called callback (interval is 10 seconds)
	assert.Equal(t, 0, callCount)
}

func TestKeealiveProgress_NilCallback(t *testing.T) {
	ctx := context.Background()
	done := make(chan struct{})

	// Should not panic with nil callback
	go keepaliveProgress(ctx, done, "test_tool", time.Now(), nil)

	time.Sleep(50 * time.Millisecond)
	close(done)
}

func TestFormatToolResultWithEscalation(t *testing.T) {
	// No escalation
	result1 := formatToolResultWithEscalation("Success", nil, "")
	assert.Equal(t, "Success", result1)

	// With error
	result2 := formatToolResultWithEscalation(nil, assert.AnError, "")
	assert.Contains(t, result2, "Error:")

	// With escalation
	result3 := formatToolResultWithEscalation("Failed", nil, "\n\nESCALATION: Stop!")
	assert.Contains(t, result3, "Failed")
	assert.Contains(t, result3, "ESCALATION")
}

func TestBuildSoftReminder(t *testing.T) {
	tests := []struct {
		name               string
		toolExecutionCount int
		maxToolExecutions  int
		expectReminder     bool
		expectedThreshold  int
	}{
		{
			name:               "Below 75% threshold - no reminder",
			toolExecutionCount: 5,
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "At 75% threshold - show reminder",
			toolExecutionCount: 38, // 75% of 50 = 37.5 -> 37
			maxToolExecutions:  50,
			expectReminder:     true,
			expectedThreshold:  37,
		},
		{
			name:               "At 80% threshold - show reminder",
			toolExecutionCount: 40,
			maxToolExecutions:  50,
			expectReminder:     true,
		},
		{
			name:               "At 90% threshold - no reminder (upper bound)",
			toolExecutionCount: 45, // 90% of 50 = 45
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "Above 90% - no reminder",
			toolExecutionCount: 48,
			maxToolExecutions:  50,
			expectReminder:     false,
		},
		{
			name:               "Low max with minimum threshold - uses minimum of 10",
			toolExecutionCount: 8,
			maxToolExecutions:  10, // 75% = 7.5, but minimum is 10
			expectReminder:     false,
		},
		{
			name:               "High max for weaver - 75% is 750",
			toolExecutionCount: 750,
			maxToolExecutions:  1000,
			expectReminder:     true,
		},
		{
			name:               "Weaver below threshold - no reminder",
			toolExecutionCount: 500,
			maxToolExecutions:  1000,
			expectReminder:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reminder := buildSoftReminder(tt.toolExecutionCount, tt.maxToolExecutions)
			if tt.expectReminder {
				assert.NotEmpty(t, reminder, "Expected reminder but got empty string")
				assert.Contains(t, reminder, fmt.Sprintf("%d of %d max", tt.toolExecutionCount, tt.maxToolExecutions))
			} else {
				assert.Empty(t, reminder, "Expected no reminder but got: %s", reminder)
			}
		})
	}
}

func TestBuildTurnReminder(t *testing.T) {
	tests := []struct {
		name           string
		turnCount      int
		maxTurns       int
		expectReminder bool
	}{
		{
			name:           "Below 75% threshold - no reminder",
			turnCount:      10,
			maxTurns:       25,
			expectReminder: false,
		},
		{
			name:           "At 75% threshold - show reminder",
			turnCount:      19, // 75% of 25 = 18.75 -> 18
			maxTurns:       25,
			expectReminder: true,
		},
		{
			name:           "At 90% threshold - no reminder (upper bound)",
			turnCount:      23, // 90% of 25 = 22.5 -> 22
			maxTurns:       25,
			expectReminder: false,
		},
		{
			name:           "High turn limit for weaver - 75% is 750",
			turnCount:      750,
			maxTurns:       1000,
			expectReminder: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reminder := buildTurnReminder(tt.turnCount, tt.maxTurns)
			if tt.expectReminder {
				assert.NotEmpty(t, reminder, "Expected reminder but got empty string")
				assert.Contains(t, reminder, fmt.Sprintf("%d of %d max", tt.turnCount, tt.maxTurns))
			} else {
				assert.Empty(t, reminder, "Expected no reminder but got: %s", reminder)
			}
		})
	}
}

const reliefWarningText = "Automatic context compaction ran and left the context at"

// reliefSeamAgent returns an agent whose session already holds sm, so the Chat
// turn starts from a known relief state (the session keeps an existing
// SegmentedMem, see ensureSessionMemory). The warning is on, as an agent opts
// in with behavior.context_relief_warning.
func reliefSeamAgent(t *testing.T, llm interface {
	Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error)
	Name() string
	Model() string
}, sessionID string, sm *SegmentedMemory, opts ...Option) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.ContextReliefWarning = true
	ag := NewAgent(&mockBackend{}, llm, append([]Option{WithConfig(cfg)}, opts...)...)
	session := ag.memory.GetOrCreateSession(context.Background(), sessionID)
	session.SegmentedMem = sm
	return ag
}

// Relief evicts the prior turn's batch and gets the context under target: no
// warning in the request, and the shed is real (rows render as evicted).
func TestContextReminder_SilentWhenShedReachesTarget(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	ag := reliefSeamAgent(t, llm, "ctx_reminder_quiet", sm)

	_, err := ag.Chat(context.Background(), "ctx_reminder_quiet", "hi")
	require.NoError(t, err)

	shedRows := 0
	for i := 0; i < 40; i++ {
		if toolRenders(sm)[fmt.Sprintf("c%d", i)] != denseResult(i) {
			shedRows++
		}
	}
	require.Greater(t, shedRows, 0, "relief must have shed rows for this case to mean anything")
	for _, m := range llm.lastMessages() {
		assert.NotContains(t, m.Content, reliefWarningText)
	}
}

// The current turn's own user message (about 8000 tokens) sits between the
// release mark (6600) and the start mark (9900). Relief evicts the batch, still
// cannot reach target, and the warning must reach the request only, not history.
func TestContextReminder_WarnsOnlyWhenReliefCannotReachTarget(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	ag := reliefSeamAgent(t, llm, "ctx_reminder_warn", sm)

	_, err := ag.Chat(context.Background(), "ctx_reminder_warn", strings.Repeat("lorem ipsum ", 4000))
	require.NoError(t, err)

	last := llm.lastMessages()
	require.NotEmpty(t, last)
	assert.Equal(t, "user", last[len(last)-1].Role)
	assert.Contains(t, last[len(last)-1].Content, reliefWarningText)

	session, ok := ag.GetSession("ctx_reminder_warn")
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		assert.NotContains(t, m.Content, reliefWarningText, "warning must not be persisted")
	}
}

// Nothing is sheddable: a fresh session whose only content is one user message
// of about 10.8k tokens, over the 9900 start mark. Relief runs, sheds nothing,
// and returns estimate > target. The warning must still reach the request.
func TestContextReminder_WarnsWhenStuckOverStartWithoutShed(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "ctx_reminder_stuck", sm)

	_, err := ag.Chat(context.Background(), "ctx_reminder_stuck", strings.Repeat("lorem ipsum ", 5300))
	require.NoError(t, err)

	last := llm.lastMessages()
	require.NotEmpty(t, last)
	assert.Equal(t, "user", last[len(last)-1].Role)
	assert.Contains(t, last[len(last)-1].Content, reliefWarningText)
}

// The agent that has not opted in is never warned, in the same stuck case that
// warns an opted-in one, and emits no context_reminder.added event.
func TestContextReminder_OffByDefault(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	tracer := &spanKeepingTracer{}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "ctx_reminder_off", sm, WithTracer(tracer))
	ag.config.ContextReliefWarning = false

	_, err := ag.Chat(context.Background(), "ctx_reminder_off", strings.Repeat("lorem ipsum ", 5300))
	require.NoError(t, err)

	for _, m := range llm.lastMessages() {
		assert.NotContains(t, m.Content, reliefWarningText)
	}
	assert.Empty(t, tracer.eventsNamed("context_reminder.added"))
}

// The warning rides only the sends where relief ran. The first Chat evicts the
// batch and lands between the release mark (6600) and the start mark (9900): it
// warns. The second adds a few tokens to a context that is still over the
// release mark but under the start mark, so relief does not run and there is no
// warning. This pins the documented gating across more than one send.
func TestContextReminder_OnlyOnSendsWhereReliefRan(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	ag := reliefSeamAgent(t, llm, "ctx_reminder_multi", sm)

	_, err := ag.Chat(context.Background(), "ctx_reminder_multi", strings.Repeat("lorem ipsum ", 4000))
	require.NoError(t, err)
	first := llm.lastMessages()
	require.Contains(t, first[len(first)-1].Content, reliefWarningText)

	_, err = ag.Chat(context.Background(), "ctx_reminder_multi", "hi")
	require.NoError(t, err)
	for _, m := range llm.lastMessages() {
		assert.NotContains(t, m.Content, reliefWarningText, "the second send did not run relief")
	}
}

// The event carries the figures the warning was built from, soft_reminder.added
// is not emitted for a context-only warning, and the event comes before the
// current-turn-overflow return.
func TestContextReminder_SpanEvent(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	tracer := &spanKeepingTracer{}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "ctx_reminder_event", sm, WithTracer(tracer))

	_, err := ag.Chat(context.Background(), "ctx_reminder_event", strings.Repeat("lorem ipsum ", 5300))
	require.NoError(t, err)

	evs := tracer.eventsNamed("context_reminder.added")
	require.Len(t, evs, 1)
	est, _ := evs[0].Attributes["estimate_tokens"].(int)
	target, _ := evs[0].Attributes["target_tokens"].(int)
	usable, _ := evs[0].Attributes["usable_tokens"].(int)
	assert.Equal(t, 11000, usable, "12000 window less 1000 reserved")
	assert.Greater(t, est, target)
	assert.Equal(t, 6600, target, "60% release mark of the usable window")
	assert.Empty(t, tracer.eventsNamed("soft_reminder.added"))
}

// refuseOnceLLM refuses its first call with ErrContextTooLong, then answers.
// It records the messages of every call.
type refuseOnceLLM struct {
	mu    sync.Mutex
	calls [][]llmtypes.Message
}

func (l *refuseOnceLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, append([]llmtypes.Message(nil), messages...))
	if len(l.calls) == 1 {
		return nil, fmt.Errorf("prompt is too long: %w", llm.ErrContextTooLong)
	}
	return &llmtypes.LLMResponse{Content: "done", Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}
func (l *refuseOnceLLM) Name() string  { return "mock-refuse-once" }
func (l *refuseOnceLLM) Model() string { return "mock-v1" }

// A refused send is resent once after a relief pass; the resend carries the same
// transient warning and it is still not stored.
func TestContextReminder_ResendAfterRefusalKeepsWarning(t *testing.T) {
	l := &refuseOnceLLM{}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, l, "ctx_reminder_resend", sm)

	_, err := ag.Chat(context.Background(), "ctx_reminder_resend", strings.Repeat("lorem ipsum ", 5300))
	require.NoError(t, err)

	require.Len(t, l.calls, 2, "one refusal, one resend")
	for i, msgs := range l.calls {
		last := msgs[len(msgs)-1]
		assert.Equal(t, "user", last.Role, "call %d", i)
		assert.Contains(t, last.Content, reliefWarningText, "call %d", i)
	}
	session, ok := ag.GetSession("ctx_reminder_resend")
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		assert.NotContains(t, m.Content, reliefWarningText, "warning must not be persisted")
	}
}

func TestBuildContextReminder(t *testing.T) {
	// 7000 of 10000 usable tokens is 70%, target 6000 is 60%, whatever the reserved output is.
	got := buildContextReminder(7000, 6000, 10000)
	assert.Contains(t, got, "~70% of the window")
	assert.Contains(t, got, "target is 60%")
	// An estimate past the window reads as full, not as 120%.
	assert.Contains(t, buildContextReminder(12000, 6000, 10000), "~100% of the window")
	// No usable window: no division by zero, and the warning still reads.
	assert.NotPanics(t, func() { _ = buildContextReminder(5, 0, 0) })
}

// The YAML key is the only way to opt in, and it reaches the agent's Config
// through the registry.
func TestContextReliefWarning_OptIn(t *testing.T) {
	yamlFor := func(extra string) string {
		return `
agent:
  name: relief_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
  behavior:
` + extra
	}
	registry, _ := createTestRegistry(t)
	build := func(extra string) *Agent {
		cfg, err := LoadConfigFromString(yamlFor(extra))
		require.NoError(t, err)
		cfg.Llm = createTestAgentConfig("x").Llm // the registry's mock provider
		ag, err := registry.buildAgent(context.Background(), cfg)
		require.NoError(t, err)
		return ag
	}

	assert.True(t, build("    context_relief_warning: true\n").GetConfig().ContextReliefWarning)
	assert.False(t, build("    context_relief_warning: false\n").GetConfig().ContextReliefWarning)
	assert.False(t, build("    max_turns: 5\n").GetConfig().ContextReliefWarning)
}

// spanKeepingTracer keeps every span it starts so a test can read the events
// added to it.
type spanKeepingTracer struct {
	mu    sync.Mutex
	spans []*observability.Span
}

func (tr *spanKeepingTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{Name: name, Attributes: map[string]interface{}{}}
	for _, o := range opts {
		o(span)
	}
	tr.mu.Lock()
	tr.spans = append(tr.spans, span)
	tr.mu.Unlock()
	return ctx, span
}
func (tr *spanKeepingTracer) EndSpan(_ *observability.Span)                                     {}
func (tr *spanKeepingTracer) RecordMetric(_ string, _ float64, _ map[string]string)             {}
func (tr *spanKeepingTracer) RecordEvent(_ context.Context, _ string, _ map[string]interface{}) {}
func (tr *spanKeepingTracer) Flush(_ context.Context) error                                     { return nil }

func (tr *spanKeepingTracer) eventsNamed(name string) []observability.Event {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []observability.Event
	for _, sp := range tr.spans {
		for _, ev := range sp.Events {
			if ev.Name == name {
				out = append(out, ev)
			}
		}
	}
	return out
}

func TestExtractErrorType(t *testing.T) {
	// Nil result
	errType1 := extractErrorType(nil)
	assert.Empty(t, errType1)

	// Map with error_type
	result2 := map[string]interface{}{
		"error_type": "syntax_error",
	}
	errType2 := extractErrorType(result2)
	assert.Equal(t, "syntax_error", errType2)

	// Map without error_type
	result3 := map[string]interface{}{
		"other_field": "value",
	}
	errType3 := extractErrorType(result3)
	assert.Empty(t, errType3)

	// Non-map result
	errType4 := extractErrorType("string result")
	assert.Empty(t, errType4)
}

func TestDefaultTokenBudgetConfig(t *testing.T) {
	config := DefaultTokenBudgetConfig()

	assert.Equal(t, 200000, config.MaxContextTokens)
	assert.Equal(t, 20000, config.ReservedOutputTokens)
	assert.Equal(t, 70.0, config.WarningThresholdPct)
	assert.Equal(t, 85.0, config.CriticalThresholdPct)
	assert.Equal(t, 8192, config.MaxOutputTokens)
	assert.Equal(t, 2048, config.MinOutputTokens)
	assert.Equal(t, 0.5, config.OutputBudgetFraction)
}

func TestDefaultFailureEscalationConfig(t *testing.T) {
	config := DefaultFailureEscalationConfig()

	assert.Equal(t, 2, config.MaxConsecutiveFailures)
	assert.True(t, config.TrackFailureSignature)
}

func TestDefaultSoftReminderConfig(t *testing.T) {
	config := DefaultSoftReminderConfig()

	assert.Equal(t, 10, config.ToolExecutionThreshold)
	assert.Equal(t, 20, config.StopThreshold)
	assert.True(t, config.Enabled)
}

// newTestContextWithProgress creates an agentContext with a progress callback for testing.
func newTestContextWithProgress(callback ProgressCallback) Context {
	return &agentContext{
		Context:          context.Background(),
		tracer:           observability.NewNoOpTracer(),
		progressCallback: callback,
	}
}

func TestEmitToolStarted(t *testing.T) {
	var captured ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		captured = e
	})

	tc := ToolCall{
		ID:    "call-123",
		Name:  "query_tool",
		Input: map[string]interface{}{"sql": "SELECT 1"},
	}

	emitToolStarted(ctx, 55, tc)

	assert.True(t, captured.IsToolStarted)
	assert.False(t, captured.IsToolCompleted)
	assert.Equal(t, StageToolExecution, captured.Stage)
	assert.Equal(t, "query_tool", captured.ToolName)
	assert.Equal(t, "call-123", captured.ToolCallID)
	assert.Equal(t, int32(55), captured.Progress)
	assert.Contains(t, captured.Message, "Executing tool: query_tool")
	assert.Equal(t, map[string]interface{}{"sql": "SELECT 1"}, captured.ToolInput)
	assert.False(t, captured.Timestamp.IsZero())
}

func TestEmitToolCompleted_Success(t *testing.T) {
	var captured ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		captured = e
	})

	tc := ToolCall{
		ID:   "call-456",
		Name: "query_tool",
	}
	result := &shuttle.Result{
		Success:         true,
		Data:            map[string]interface{}{"rows": 42},
		ExecutionTimeMs: 150,
	}

	emitToolCompleted(ctx, 60, tc, result, nil)

	assert.True(t, captured.IsToolCompleted)
	assert.False(t, captured.IsToolStarted)
	assert.Equal(t, StageToolExecution, captured.Stage)
	assert.Equal(t, "query_tool", captured.ToolName)
	assert.Equal(t, "call-456", captured.ToolCallID)
	assert.True(t, captured.ToolSuccess)
	assert.Empty(t, captured.ToolError)
	assert.Equal(t, int64(150), captured.ToolDurationMs)
	assert.Equal(t, map[string]interface{}{"rows": 42}, captured.ToolResult)
	assert.Contains(t, captured.Message, "Tool completed: query_tool")
}

func TestEmitToolCompleted_GoError(t *testing.T) {
	var captured ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		captured = e
	})

	tc := ToolCall{
		ID:   "call-789",
		Name: "file_read",
	}

	emitToolCompleted(ctx, 55, tc, nil, fmt.Errorf("file not found"))

	assert.True(t, captured.IsToolCompleted)
	assert.False(t, captured.ToolSuccess)
	assert.Equal(t, "file not found", captured.ToolError)
	assert.Equal(t, "call-789", captured.ToolCallID)
	// result is nil so these should be zero-value
	assert.Equal(t, int64(0), captured.ToolDurationMs)
	assert.Nil(t, captured.ToolResult)
}

func TestEmitToolCompleted_ResultError(t *testing.T) {
	var captured ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		captured = e
	})

	tc := ToolCall{
		ID:   "call-abc",
		Name: "query_tool",
	}
	result := &shuttle.Result{
		Success:         false,
		ExecutionTimeMs: 50,
		Error: &shuttle.Error{
			Code:    "SYNTAX_ERROR",
			Message: "invalid SQL syntax",
		},
	}

	emitToolCompleted(ctx, 55, tc, result, nil)

	assert.True(t, captured.IsToolCompleted)
	assert.False(t, captured.ToolSuccess)
	assert.Equal(t, "invalid SQL syntax", captured.ToolError)
	assert.Equal(t, int64(50), captured.ToolDurationMs)
}

func TestToolCallID_Correlation(t *testing.T) {
	var events []ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		events = append(events, e)
	})

	tc := ToolCall{
		ID:    "call-corr-001",
		Name:  "web_search",
		Input: map[string]interface{}{"query": "loom framework"},
	}
	result := &shuttle.Result{
		Success:         true,
		Data:            "search results",
		ExecutionTimeMs: 200,
	}

	emitToolStarted(ctx, 50, tc)
	emitToolCompleted(ctx, 50, tc, result, nil)

	require.Len(t, events, 2)
	assert.True(t, events[0].IsToolStarted)
	assert.True(t, events[1].IsToolCompleted)
	assert.Equal(t, "call-corr-001", events[0].ToolCallID)
	assert.Equal(t, events[0].ToolCallID, events[1].ToolCallID)
}

func TestEmitToolCompleted_NilResult(t *testing.T) {
	var captured ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) {
		captured = e
	})

	tc := ToolCall{ID: "call-nil", Name: "broken_tool"}

	// nil result with error — should not panic
	emitToolCompleted(ctx, 50, tc, nil, fmt.Errorf("timeout"))

	assert.True(t, captured.IsToolCompleted)
	assert.False(t, captured.ToolSuccess)
	assert.Equal(t, "timeout", captured.ToolError)
	assert.Equal(t, int64(0), captured.ToolDurationMs)
	assert.Nil(t, captured.ToolResult)

	// nil result with nil error — edge case
	emitToolCompleted(ctx, 50, tc, nil, nil)
	// result==nil && err==nil => toolSuccess = true (vacuously)
	assert.True(t, captured.ToolSuccess)
}

func TestEmitToolStarted_NilCallback(t *testing.T) {
	ctx := &agentContext{
		Context:          context.Background(),
		tracer:           observability.NewNoOpTracer(),
		progressCallback: nil,
	}

	tc := ToolCall{ID: "call-nil-cb", Name: "test_tool"}

	// Should not panic
	assert.NotPanics(t, func() {
		emitToolStarted(ctx, 50, tc)
	})
}

func TestEmitToolCompleted_NilCallback(t *testing.T) {
	ctx := &agentContext{
		Context:          context.Background(),
		tracer:           observability.NewNoOpTracer(),
		progressCallback: nil,
	}

	tc := ToolCall{ID: "call-nil-cb2", Name: "test_tool"}
	result := &shuttle.Result{Success: true}

	// Should not panic
	assert.NotPanics(t, func() {
		emitToolCompleted(ctx, 50, tc, result, nil)
	})
}
