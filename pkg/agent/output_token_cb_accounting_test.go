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
// state exactly what the tool demands — including through composite branches,
// which MCP tool schemas use and which a root-only "required" check misses.
type schemaTool struct {
	name     string
	required []string
	props    map[string]*shuttle.JSONSchema
	oneOf    []*shuttle.JSONSchema
	anyOf    []*shuttle.JSONSchema
	allOf    []*shuttle.JSONSchema
	noSchema bool
}

func (t *schemaTool) Name() string        { return t.name }
func (t *schemaTool) Description() string { return "test tool " + t.name }
func (t *schemaTool) Backend() string     { return "" }
func (t *schemaTool) InputSchema() *shuttle.JSONSchema {
	if t.noSchema {
		return nil
	}
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: t.props,
		Required:   t.required,
		OneOf:      t.oneOf,
		AnyOf:      t.anyOf,
		AllOf:      t.allOf,
	}
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

// TestClassifyToolCall_EmptyInput states the schema rule for an empty input
// directly, including the conservative fallback for a call the advertised set
// cannot describe.
func TestClassifyToolCall_EmptyInput(t *testing.T) {
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
		want toolCallState
		why  string
	}{
		{"required parameter", "needs_args", toolCallStateIncomplete, "the demanded property never arrived"},
		{"only optional parameters", "optional_args", toolCallStateComplete, "calling it with {} is legitimate"},
		{"no parameters at all", "no_args", toolCallStateComplete, "an empty input is the only valid input"},
		{"unknown tool name", "never_registered", toolCallStateIncomplete, "fall back to the historical heuristic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := ToolCall{Name: tt.tool, Input: map[string]interface{}{}}
			if got := classifyToolCall(call, tools); got != tt.want {
				t.Errorf("classifyToolCall(%q, {}) = %v, want %v — %s", tt.tool, got, tt.want, tt.why)
			}
		})
	}
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
			if got := classifyToolCalls(tt.toolCalls, tools) == toolCallStateIncomplete; got != tt.want {
				t.Errorf("classifyToolCalls() incomplete = %v, want %v", got, tt.want)
			}
		})
	}
}

// requiresSQL is the tool used by the containment tests below: one required
// property, so an input lacking it is incomplete however populated it looks.
func requiresSQL() *schemaTool {
	return &schemaTool{
		name:     "run_query",
		required: []string{"sql"},
		props:    map[string]*shuttle.JSONSchema{"sql": {Type: "string"}, "limit": {Type: "number"}},
	}
}

func truncatedCall() scriptedResponse {
	return scriptedResponse{
		toolCalls:  []llmtypes.ToolCall{{ID: "t-empty", Name: "run_query", Input: map[string]interface{}{}}},
		stopReason: "max_tokens",
	}
}

// alternate builds a script that interleaves a and b, n times each.
func alternate(a, b scriptedResponse, n int) []scriptedResponse {
	var script []scriptedResponse
	for i := 0; i < n; i++ {
		script = append(script, a, b)
	}
	return script
}

// TestOutputTokenCB_MalformedArgsDoNotClear is the containment regression for
// the reset path.
//
// When a tool call's arguments JSON does not parse, the OpenAI and Azure OpenAI
// clients store the partial string as Input{"_raw": ...} — which is exactly what
// truncation at max_tokens looks like on those providers, and which the old
// emptiness heuristic read as a fully populated input. Clearing on "not visibly
// empty" therefore let an alternating stream of broken calls reset the counter
// forever, disarming the breaker on the providers whose truncation is most
// visible. The breaker must still fire.
func TestOutputTokenCB_MalformedArgsDoNotClear(t *testing.T) {
	malformed := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{
			// A partial arguments string, cut off mid-JSON.
			{ID: "t-raw", Name: "run_query", Input: map[string]interface{}{"_raw": `{"sql":`}},
		},
		stopReason: "max_tokens",
	}

	llm := &scriptedStopReasonLLM{responses: alternate(truncatedCall(), malformed, 6)}
	ag := newOutputCBAgent(t, 3, llm, requiresSQL())

	_, err := ag.Chat(context.Background(), "s-malformed", "go")
	if !isCircuitBreakerErr(err) {
		t.Fatalf("breaker must still fire when the non-empty turns are unparseable arguments, got err=%v", err)
	}
}

// TestOutputTokenCB_MissingRequiredKeyDoesNotClear is the same containment
// property for a call that parsed cleanly but stopped before emitting the
// property its schema demands.
func TestOutputTokenCB_MissingRequiredKeyDoesNotClear(t *testing.T) {
	missingRequired := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{
			// "limit" is real and non-falsy, but the required "sql" never arrived.
			{ID: "t-partial", Name: "run_query", Input: map[string]interface{}{"limit": 5}},
		},
		stopReason: "max_tokens",
	}

	llm := &scriptedStopReasonLLM{responses: alternate(truncatedCall(), missingRequired, 6)}
	ag := newOutputCBAgent(t, 3, llm, requiresSQL())

	_, err := ag.Chat(context.Background(), "s-missing-required", "go")
	if !isCircuitBreakerErr(err) {
		t.Fatalf("breaker must still fire when the non-empty turns omit a required property, got err=%v", err)
	}
}

// TestOutputTokenCB_IndeterminateToolCallDoesNotClear covers the third state: a
// call whose completeness cannot be established must not count as truncation,
// but must not clear a run either — clearing asserts progress that has not been
// demonstrated.
func TestOutputTokenCB_IndeterminateToolCallDoesNotClear(t *testing.T) {
	// Advertises no schema, so nothing can be checked against it.
	opaque := &schemaTool{name: "opaque_tool", noSchema: true}

	indeterminate := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{
			{ID: "t-opaque", Name: "opaque_tool", Input: map[string]interface{}{"anything": "here"}},
		},
		stopReason: "max_tokens",
	}

	llm := &scriptedStopReasonLLM{responses: alternate(truncatedCall(), indeterminate, 6)}
	ag := newOutputCBAgent(t, 3, llm, requiresSQL(), opaque)

	_, err := ag.Chat(context.Background(), "s-indeterminate", "go")
	if !isCircuitBreakerErr(err) {
		t.Fatalf("an unverifiable call must not clear a truncation run, got err=%v", err)
	}
}

// TestClassifyToolCall_CompositeSchemas covers the requirement forms a
// root-level "required" check alone misses. MCP tool schemas use these.
func TestClassifyToolCall_CompositeSchemas(t *testing.T) {
	// Each branch demands a different property, and the root demands nothing —
	// so a root-only check would call this tool argument-less and wave {} through.
	oneOfTool := &schemaTool{
		name: "by_id_or_name",
		props: map[string]*shuttle.JSONSchema{
			"id":   {Type: "string"},
			"name": {Type: "string"},
		},
		oneOf: []*shuttle.JSONSchema{
			{Required: []string{"id"}},
			{Required: []string{"name"}},
		},
	}
	anyOfTool := &schemaTool{
		name:  "by_any",
		anyOf: []*shuttle.JSONSchema{{Required: []string{"a"}}, {Required: []string{"b"}}},
	}
	allOfTool := &schemaTool{
		name:  "needs_both",
		allOf: []*shuttle.JSONSchema{{Required: []string{"a"}}, {Required: []string{"b"}}},
	}
	tools := []shuttle.Tool{oneOfTool, anyOfTool, allOfTool}

	tests := []struct {
		name string
		call ToolCall
		want toolCallState
		why  string
	}{
		{
			name: "oneOf with no properties at all is incomplete",
			call: ToolCall{Name: "by_id_or_name", Input: map[string]interface{}{}},
			want: toolCallStateIncomplete,
			why:  "every branch demands a property, so {} satisfies none of them",
		},
		{
			name: "oneOf with one branch satisfied is complete",
			call: ToolCall{Name: "by_id_or_name", Input: map[string]interface{}{"id": "abc"}},
			want: toolCallStateComplete,
			why:  "satisfying a single branch is a finished call",
		},
		{
			name: "oneOf with the other branch satisfied is complete",
			call: ToolCall{Name: "by_id_or_name", Input: map[string]interface{}{"name": "abc"}},
			want: toolCallStateComplete,
			why:  "branch order must not matter",
		},
		{
			name: "anyOf with no branch satisfied is incomplete",
			call: ToolCall{Name: "by_any", Input: map[string]interface{}{"unrelated": "x"}},
			want: toolCallStateIncomplete,
			why:  "a populated input that satisfies nothing is not progress",
		},
		{
			name: "allOf with only one branch satisfied is incomplete",
			call: ToolCall{Name: "needs_both", Input: map[string]interface{}{"a": 1}},
			want: toolCallStateIncomplete,
			why:  "allOf demands every branch",
		},
		{
			name: "allOf fully satisfied is complete",
			call: ToolCall{Name: "needs_both", Input: map[string]interface{}{"a": 1, "b": 2}},
			want: toolCallStateComplete,
			why:  "both branches met",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyToolCall(tt.call, tools); got != tt.want {
				t.Errorf("classifyToolCall() = %v, want %v — %s", got, tt.want, tt.why)
			}
		})
	}
}

// TestClassifyToolCall_RawMarkerAlwaysIncomplete pins the parse-failure marker
// as a positive incompleteness signal, ahead of every other consideration —
// including on a tool that demands nothing, where an empty input would
// otherwise be perfectly valid.
func TestClassifyToolCall_RawMarkerAlwaysIncomplete(t *testing.T) {
	tools := []shuttle.Tool{
		requiresSQL(),
		&schemaTool{name: "list_skills"},
		&schemaTool{name: "opaque_tool", noSchema: true},
	}

	for _, name := range []string{"run_query", "list_skills", "opaque_tool", "never_advertised"} {
		t.Run(name, func(t *testing.T) {
			call := ToolCall{Name: name, Input: map[string]interface{}{"_raw": `{"sql":`}}
			if got := classifyToolCall(call, tools); got != toolCallStateIncomplete {
				t.Errorf("classifyToolCall(%q with _raw) = %v, want incomplete", name, got)
			}
		})
	}
}

// TestClassifyToolCalls_Aggregation states how a turn's verdict is combined
// across several calls: incomplete dominates, then unknown, and only an
// all-complete turn is complete.
func TestClassifyToolCalls_Aggregation(t *testing.T) {
	tools := []shuttle.Tool{requiresSQL(), &schemaTool{name: "opaque_tool", noSchema: true}}

	good := ToolCall{Name: "run_query", Input: map[string]interface{}{"sql": "SELECT 1"}}
	bad := ToolCall{Name: "run_query", Input: map[string]interface{}{}}
	opaque := ToolCall{Name: "opaque_tool", Input: map[string]interface{}{"x": 1}}

	tests := []struct {
		name  string
		calls []ToolCall
		want  toolCallState
	}{
		{"no calls at all", nil, toolCallStateComplete},
		{"all complete", []ToolCall{good, good}, toolCallStateComplete},
		{"one incomplete dominates", []ToolCall{good, bad}, toolCallStateIncomplete},
		{"incomplete outranks unknown", []ToolCall{opaque, bad}, toolCallStateIncomplete},
		{"unknown outranks complete", []ToolCall{good, opaque}, toolCallStateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyToolCalls(tt.calls, tools); got != tt.want {
				t.Errorf("classifyToolCalls() = %v, want %v", got, tt.want)
			}
		})
	}
}

// visualizationShapedTool mirrors pkg/visualization's real schema: the root
// requires "datasets", and each dataset ITEM requires "name" and "data". A
// root-only requirement check calls {"datasets":[{}],...} complete, while the
// tool itself rejects the dataset.
func visualizationShapedTool() *schemaTool {
	item := &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"name": {Type: "string"},
			"data": {Type: "string"},
		},
		Required: []string{"name", "data"},
	}
	return &schemaTool{
		name:     "generate_visualization",
		required: []string{"datasets", "title", "summary", "output_path"},
		props: map[string]*shuttle.JSONSchema{
			"datasets":    {Type: "array", Items: item},
			"title":       {Type: "string"},
			"summary":     {Type: "string"},
			"output_path": {Type: "string"},
		},
	}
}

// TestOutputTokenCB_NestedMissingRequiredDoesNotClear is the round-3 blocker:
// every ROOT key is present, so a root-only check reports the turn complete and
// clears the run, even though the nested dataset is missing what its own schema
// demands and the real tool rejects it. Alternating that with an empty call
// would keep the breaker permanently disarmed.
func TestOutputTokenCB_NestedMissingRequiredDoesNotClear(t *testing.T) {
	nestedPartial := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{{
			ID:   "t-nested",
			Name: "generate_visualization",
			Input: map[string]interface{}{
				"datasets":    []interface{}{map[string]interface{}{}},
				"title":       "t",
				"summary":     "s",
				"output_path": "x",
			},
		}},
		stopReason: "max_tokens",
	}
	empty := scriptedResponse{
		toolCalls:  []llmtypes.ToolCall{{ID: "t-empty", Name: "generate_visualization", Input: map[string]interface{}{}}},
		stopReason: "max_tokens",
	}

	llm := &scriptedStopReasonLLM{responses: alternate(empty, nestedPartial, 6)}
	ag := newOutputCBAgent(t, 3, llm, visualizationShapedTool())

	_, err := ag.Chat(context.Background(), "s-nested", "go")
	if !isCircuitBreakerErr(err) {
		t.Fatalf("a nested partial turn must not clear the run, got err=%v", err)
	}
}

// TestClassifyToolCall_NestedRequirements pins the walker directly, including a
// composite nested UNDER a required property.
func TestClassifyToolCall_NestedRequirements(t *testing.T) {
	viz := visualizationShapedTool()

	nestedOneOf := &schemaTool{
		name:     "nested_composite",
		required: []string{"target"},
		props: map[string]*shuttle.JSONSchema{
			"target": {
				Type: "object",
				OneOf: []*shuttle.JSONSchema{
					{Required: []string{"id"}},
					{Required: []string{"name"}},
				},
			},
		},
	}
	tools := []shuttle.Tool{viz, nestedOneOf}

	full := func() map[string]interface{} {
		return map[string]interface{}{
			"datasets": []interface{}{
				map[string]interface{}{"name": "n", "data": "d"},
			},
			"title": "t", "summary": "s", "output_path": "x",
		}
	}

	tests := []struct {
		name string
		call ToolCall
		want toolCallState
		why  string
	}{
		{
			name: "nested item missing its required keys",
			call: ToolCall{Name: "generate_visualization", Input: map[string]interface{}{
				"datasets": []interface{}{map[string]interface{}{}},
				"title":    "t", "summary": "s", "output_path": "x",
			}},
			want: toolCallStateIncomplete,
			why:  "the tool rejects a dataset without name/data",
		},
		{
			name: "one bad item among good ones",
			call: ToolCall{Name: "generate_visualization", Input: map[string]interface{}{
				"datasets": []interface{}{
					map[string]interface{}{"name": "n", "data": "d"},
					map[string]interface{}{"name": "n"},
				},
				"title": "t", "summary": "s", "output_path": "x",
			}},
			want: toolCallStateIncomplete,
			why:  "every item must satisfy the item schema",
		},
		{
			name: "fully populated nested input",
			call: ToolCall{Name: "generate_visualization", Input: full()},
			want: toolCallStateComplete,
			why:  "root and nested requirements both met",
		},
		{
			name: "empty array satisfies the item schema vacuously",
			call: ToolCall{Name: "generate_visualization", Input: map[string]interface{}{
				"datasets": []interface{}{}, "title": "t", "summary": "s", "output_path": "x",
			}},
			want: toolCallStateComplete,
			why:  "no item is present to be missing anything",
		},
		{
			name: "nested oneOf with no branch satisfied",
			call: ToolCall{Name: "nested_composite", Input: map[string]interface{}{
				"target": map[string]interface{}{"unrelated": 1},
			}},
			want: toolCallStateIncomplete,
			why:  "a composite under a required property is still checked",
		},
		{
			name: "nested oneOf with a branch satisfied",
			call: ToolCall{Name: "nested_composite", Input: map[string]interface{}{
				"target": map[string]interface{}{"id": "abc"},
			}},
			want: toolCallStateComplete,
			why:  "one satisfied branch is enough",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyToolCall(tt.call, tools); got != tt.want {
				t.Errorf("classifyToolCall() = %v, want %v — %s", got, tt.want, tt.why)
			}
		})
	}
}

// camelTool requires a camelCase property, which is what the executor's
// normalization exists to reconcile with the snake_case models commonly emit.
type camelTool struct {
	schemaTool
	mu       sync.Mutex
	gotKeys  []string
	executed int
}

func (t *camelTool) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.executed++
	for k := range params {
		t.gotKeys = append(t.gotKeys, k)
	}
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}

// TestOutputTokenCB_SnakeCaseInputIsNotIncomplete covers the normalization
// mismatch: Executor.Execute normalizes root keys to the schema's spelling
// before dispatch, so a call sending user_id for a required userId executes
// fine. Judging the raw provider spelling would count each such turn as
// incomplete and hard-fail a productive session at the threshold — reproducing
// the exact false positive this accounting is meant to prevent.
func TestOutputTokenCB_SnakeCaseInputIsNotIncomplete(t *testing.T) {
	tool := &camelTool{schemaTool: schemaTool{
		name:     "lookup_user",
		required: []string{"userId"},
		props:    map[string]*shuttle.JSONSchema{"userId": {Type: "string"}},
	}}

	snake := scriptedResponse{
		toolCalls: []llmtypes.ToolCall{
			{ID: "t-snake", Name: "lookup_user", Input: map[string]interface{}{"user_id": "123"}},
		},
		stopReason: "max_tokens",
	}
	llm := &scriptedStopReasonLLM{responses: []scriptedResponse{snake, snake, snake, snake, snake}}

	ag := newOutputCBAgent(t, 3, llm, tool)
	_, err := ag.Chat(context.Background(), "s-snake", "go")

	if isCircuitBreakerErr(err) {
		t.Fatalf("breaker fired on calls the executor normalizes and runs: %v", err)
	}

	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.executed == 0 {
		t.Fatal("expected the tool to actually execute")
	}
	for _, k := range tool.gotKeys {
		if k != "userId" {
			t.Errorf("tool received key %q, want the normalized \"userId\"", k)
		}
	}
}

// TestClassifyToolCall_RawIsAProperty guards the marker against a tool that
// legitimately declares "_raw": its own meaning must win over the provider
// parse-failure convention.
func TestClassifyToolCall_RawIsAProperty(t *testing.T) {
	rawTool := &schemaTool{
		name:     "store_blob",
		required: []string{"_raw"},
		props:    map[string]*shuttle.JSONSchema{"_raw": {Type: "string"}},
	}
	tools := []shuttle.Tool{rawTool}

	call := ToolCall{Name: "store_blob", Input: map[string]interface{}{"_raw": "payload"}}
	if got := classifyToolCall(call, tools); got != toolCallStateComplete {
		t.Errorf("classifyToolCall() = %v, want complete — the tool declares _raw itself", got)
	}
}

// TestCheckAgainstSchema_DepthExhaustionIsUnknown pins the traversal bound as
// failing to unknown, never to complete: past the bound nothing has been
// established, and reporting completeness there would clear a run on an unread
// schema.
func TestCheckAgainstSchema_DepthExhaustionIsUnknown(t *testing.T) {
	// A chain deeper than maxSchemaDepth, each level demanding the next.
	leaf := &shuttle.JSONSchema{Type: "object", Required: []string{"missing"}}
	schema := leaf
	value := map[string]interface{}{}
	for i := 0; i < maxSchemaDepth+4; i++ {
		schema = &shuttle.JSONSchema{
			Type:       "object",
			Properties: map[string]*shuttle.JSONSchema{"next": schema},
			Required:   []string{"next"},
		}
		value = map[string]interface{}{"next": value}
	}

	if got := checkAgainstSchema(schema, value, 0); got == toolCallStateComplete {
		t.Error("depth exhaustion must not report complete")
	}
}
