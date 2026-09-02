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
	"strings"
	"sync"
	"testing"

	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// These tests drive the REAL conversation-loop accounting rather than
// re-deriving the branch decisions in the test body, which is what the
// tracker-level tests in circuit_breaker_test.go do. That distinction matters:
// both defects fixed here lived in the switch in conversationLoop, so no
// tracker-level test could observe them — and TestOutputTokenCircuitBreaker_
// Integration actually encoded the corrected behavior ("else → clear") while
// the code did something different, with nothing driving the code to notice.

// scriptedStopReasonLLM returns a scripted sequence of responses, including
// each one's stop reason. It exists because the shared mockToolCallingLLM's
// mockLLMResponse cannot express a stop reason, and the output-token circuit
// breaker keys entirely off it.
type scriptedStopReasonLLM struct {
	mu        sync.Mutex
	responses []scriptedResponse
	idx       int
}

type scriptedResponse struct {
	content    string
	toolCalls  []llmtypes.ToolCall
	stopReason string
}

func (m *scriptedStopReasonLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Past the end of the script, end the turn cleanly so a test that does not
	// trip the breaker terminates instead of looping to MaxTurns.
	if m.idx >= len(m.responses) {
		return &llmtypes.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return &llmtypes.LLMResponse{
		Content:    r.content,
		ToolCalls:  r.toolCalls,
		StopReason: r.stopReason,
		Usage:      llmtypes.Usage{InputTokens: 10, OutputTokens: 10},
	}, nil
}

func (m *scriptedStopReasonLLM) Name() string  { return "scripted-stop-reason" }
func (m *scriptedStopReasonLLM) Model() string { return "scripted-v1" }

// schemaTool is a no-op tool with a caller-supplied input schema, so a test can
// state exactly whether the tool requires arguments.
type schemaTool struct {
	name     string
	required []string
	props    map[string]*shuttle.JSONSchema
}

func (t *schemaTool) Name() string        { return t.name }
func (t *schemaTool) Description() string { return "test tool " + t.name }
func (t *schemaTool) Backend() string     { return "" }
func (t *schemaTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object", Properties: t.props, Required: t.required}
}
func (t *schemaTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}

// newOutputCBAgent builds an agent whose only variable is the scripted LLM and
// the registered tools, with a low CB threshold so a scenario stays short.
func newOutputCBAgent(t *testing.T, threshold int, llm *scriptedStopReasonLLM, tools ...shuttle.Tool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxTurns = 50
	cfg.MaxToolExecutions = 50
	cfg.OutputTokenCBThreshold = threshold

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	for _, tl := range tools {
		ag.RegisterTool(tl)
	}
	return ag
}

func isCircuitBreakerErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "circuit breaker")
}

// TestOutputTokenCB_CompleteToolCallEndsTheRun is the ratchet regression.
//
// A turn that hits max_tokens but returns COMPLETE, executable tool calls is
// forward progress, so it must end the run of consecutive truncated turns. The
// default branch of the accounting switch previously neither counted nor
// cleared, which made the counter a lifetime tally: under sustained output
// pressure no turn ever cleared it, so truncated turns scattered among
// productive ones still summed to the threshold and failed the whole message.
func TestOutputTokenCB_CompleteToolCallEndsTheRun(t *testing.T) {
	tool := &schemaTool{
		name:     "run_query",
		required: []string{"sql"},
		props:    map[string]*shuttle.JSONSchema{"sql": {Type: "string"}},
	}

	truncated := scriptedResponse{
		toolCalls:  []llmtypes.ToolCall{{ID: "t1", Name: "run_query", Input: map[string]interface{}{}}},
		stopReason: "max_tokens",
	}
	complete := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{
			{ID: "t2", Name: "run_query", Input: map[string]interface{}{"sql": "SELECT 1"}},
		},
		stopReason: "max_tokens",
	}

	// Alternate truncated / complete well past the threshold. Every truncated
	// turn is separated by real progress, so no run of 3 ever forms.
	var script []scriptedResponse
	for i := 0; i < 6; i++ {
		script = append(script, truncated, complete)
	}
	llm := &scriptedStopReasonLLM{responses: script}

	ag := newOutputCBAgent(t, 3, llm, tool)
	_, err := ag.Chat(context.Background(), "s-ratchet", "go")

	if isCircuitBreakerErr(err) {
		t.Fatalf("circuit breaker fired on interleaved truncated/complete turns: %v", err)
	}
}

// TestOutputTokenCB_ConsecutiveTruncatedStillFires is the counterpart: the fix
// above must not disarm the breaker for the case it exists to catch.
func TestOutputTokenCB_ConsecutiveTruncatedStillFires(t *testing.T) {
	tool := &schemaTool{
		name:     "run_query",
		required: []string{"sql"},
		props:    map[string]*shuttle.JSONSchema{"sql": {Type: "string"}},
	}

	truncated := scriptedResponse{
		toolCalls:  []llmtypes.ToolCall{{ID: "t1", Name: "run_query", Input: map[string]interface{}{}}},
		stopReason: "max_tokens",
	}
	llm := &scriptedStopReasonLLM{responses: []scriptedResponse{truncated, truncated, truncated, truncated}}

	ag := newOutputCBAgent(t, 3, llm, tool)
	_, err := ag.Chat(context.Background(), "s-consecutive", "go")

	if !isCircuitBreakerErr(err) {
		t.Fatalf("expected the circuit breaker to fire on 3 consecutive truncated turns, got err=%v", err)
	}
}

// TestOutputTokenCB_NoArgumentToolIsNotTruncation covers the schema-aware half.
//
// A tool that requires no arguments is invoked with an empty input by design.
// That used to read as a truncated call, so repeatedly calling a tool like
// list_skills on turns that also hit the output limit tripped the breaker and
// failed the message even though nothing was ever truncated.
func TestOutputTokenCB_NoArgumentToolIsNotTruncation(t *testing.T) {
	noArgs := &schemaTool{name: "list_skills"}

	call := scriptedResponse{
		toolCalls:  []llmtypes.ToolCall{{ID: "t1", Name: "list_skills", Input: map[string]interface{}{}}},
		stopReason: "max_tokens",
	}
	llm := &scriptedStopReasonLLM{responses: []scriptedResponse{call, call, call, call, call}}

	ag := newOutputCBAgent(t, 3, llm, noArgs)
	_, err := ag.Chat(context.Background(), "s-noargs", "go")

	if isCircuitBreakerErr(err) {
		t.Fatalf("circuit breaker fired on a legitimately argument-less tool: %v", err)
	}
}

// TestToolRequiresArguments states the schema rule directly, including the
// conservative fallback for a call the advertised set cannot describe.
func TestToolRequiresArguments(t *testing.T) {
	required := &schemaTool{
		name:     "needs_args",
		required: []string{"path"},
		props:    map[string]*shuttle.JSONSchema{"path": {Type: "string"}},
	}
	optionalOnly := &schemaTool{
		name:  "optional_args",
		props: map[string]*shuttle.JSONSchema{"recursive": {Type: "boolean"}},
	}
	noArgs := &schemaTool{name: "no_args"}
	tools := []shuttle.Tool{required, optionalOnly, noArgs}

	tests := []struct {
		name string
		tool string
		want bool
		why  string
	}{
		{"required parameter", "needs_args", true, "emptiness is evidence of truncation"},
		{"only optional parameters", "optional_args", false, "calling it with {} is legitimate"},
		{"no parameters at all", "no_args", false, "an empty input is the only valid input"},
		{"unknown tool name", "never_registered", true, "fall back to the historical heuristic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolRequiresArguments(tt.tool, tools); got != tt.want {
				t.Errorf("toolRequiresArguments(%q) = %v, want %v — %s", tt.tool, got, tt.want, tt.why)
			}
		})
	}

	t.Run("nil tool set keeps historical behavior", func(t *testing.T) {
		if !toolRequiresArguments("anything", nil) {
			t.Error("a nil advertised set must apply the emptiness heuristics unchanged")
		}
	})
}

// TestDetectEmptyToolCall_SchemaAware pins the detector against a real
// advertised set, complementing the nil-schema table in circuit_breaker_test.go.
func TestDetectEmptyToolCall_SchemaAware(t *testing.T) {
	needsArgs := &schemaTool{
		name:     "run_query",
		required: []string{"sql"},
		props:    map[string]*shuttle.JSONSchema{"sql": {Type: "string"}},
	}
	noArgs := &schemaTool{name: "list_skills"}
	tools := []shuttle.Tool{needsArgs, noArgs}

	tests := []struct {
		name      string
		toolCalls []ToolCall
		want      bool
	}{
		{
			name:      "argument-less tool with empty input is not truncation",
			toolCalls: []ToolCall{{ID: "a", Name: "list_skills", Input: map[string]interface{}{}}},
			want:      false,
		},
		{
			name:      "argument-less tool with nil input is not truncation",
			toolCalls: []ToolCall{{ID: "a", Name: "list_skills", Input: nil}},
			want:      false,
		},
		{
			name:      "required-argument tool with empty input is truncation",
			toolCalls: []ToolCall{{ID: "a", Name: "run_query", Input: map[string]interface{}{}}},
			want:      true,
		},
		{
			name: "required-argument tool with real input is fine",
			toolCalls: []ToolCall{
				{ID: "a", Name: "run_query", Input: map[string]interface{}{"sql": "SELECT 1"}},
			},
			want: false,
		},
		{
			name: "a truncated call alongside an argument-less one is still detected",
			toolCalls: []ToolCall{
				{ID: "a", Name: "list_skills", Input: map[string]interface{}{}},
				{ID: "b", Name: "run_query", Input: map[string]interface{}{}},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectEmptyToolCall(tt.toolCalls, tools); got != tt.want {
				t.Errorf("detectEmptyToolCall() = %v, want %v", got, tt.want)
			}
		})
	}
}
