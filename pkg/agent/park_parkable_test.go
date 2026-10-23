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
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// formTool is a fake ParkableQuestion tool.
type formTool struct {
	runs      atomic.Int64
	okToPark  atomic.Bool
	nilResult atomic.Bool
	parkCalls atomic.Int64

	gotInput  atomic.Value // map[string]interface{}
	gotAnswer atomic.Value // string
}

var _ shuttle.ParkableQuestion = (*formTool)(nil)

func (t *formTool) Name() string        { return "form_tool" }
func (t *formTool) Description() string { return "form tool" }
func (t *formTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"title": {Type: "string"}}}
}
func (t *formTool) Backend() string { return "" }
func (t *formTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	return &shuttle.Result{Success: true, Data: "executed"}, nil
}
func (t *formTool) ParkQuestion(ctx context.Context, input map[string]interface{}) (string, bool) {
	t.parkCalls.Add(1)
	if !t.okToPark.Load() {
		return "", false
	}
	title, _ := input["title"].(string)
	return title, true
}
func (t *formTool) ParkAnswerResult(ctx context.Context, input map[string]interface{}, answer string) *shuttle.Result {
	if t.nilResult.Load() {
		return nil
	}
	t.gotInput.Store(input)
	t.gotAnswer.Store(answer)
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"status": "submitted", "echo": answer}}
}

func newFormTool(okToPark bool) *formTool {
	t := &formTool{}
	t.okToPark.Store(okToPark)
	return t
}

func formScript(extra ...llmtypes.ToolCall) []mockLLMResponse {
	calls := append([]llmtypes.ToolCall{
		{ID: "c-form", Name: "form_tool", Input: map[string]interface{}{"title": "Name it", "fields": map[string]interface{}{"a": "b"}}},
	}, extra...)
	return []mockLLMResponse{
		{content: "", toolCalls: calls},
		{content: "done"},
	}
}

func newFormFixture(t *testing.T, hooks []shuttle.Hook, ft *formTool, script []mockLLMResponse, tools ...string) *parkFixture {
	t.Helper()
	f := newParkFixture(t, hooks, script, tools...)
	f.ag.RegisterTool(ft)
	return f
}

func toolRow(t *testing.T, f *parkFixture, sessionID, callID string) Message {
	t.Helper()
	sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, f.ag.config.Name, "")
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == callID {
			return m
		}
	}
	t.Fatalf("no tool row for %s", callID)
	return Message{}
}

func TestParkable_ParksAsQuestionWithoutExecuting(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())

	_, err := f.ag.Chat(context.Background(), "s-pq", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-pq")
	if hr.RequestType != "parked" || hr.Kind != "question" {
		t.Fatalf("request shape: %+v", hr)
	}
	desc, ok := hr.Params["c-form"].(map[string]interface{})
	if !ok {
		t.Fatalf("descriptor missing: %#v", hr.Params)
	}
	if desc["kind"] != "question" || desc["tool"] != "form_tool" || desc["question"] != "Name it" {
		t.Fatalf("descriptor = %#v", desc)
	}
	params, _ := desc["params"].(map[string]interface{})
	if params["title"] != "Name it" || params["fields"] == nil {
		t.Fatalf("params = %#v", desc["params"])
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("Execute ran for a parked call")
	}
}

func TestParkable_NotOKRunsExecute(t *testing.T) {
	ft := newFormTool(false)
	f := newFormFixture(t, nil, ft, formScript())
	resp, err := f.ag.Chat(context.Background(), "s-pq-no", "go")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("content = %q", resp.Content)
	}
	if ft.runs.Load() != 1 {
		t.Fatalf("Execute ran %d times, want 1", ft.runs.Load())
	}
}

func TestParkable_DenyIsNotParked(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, []shuttle.Hook{scopedDenyHook{tool: "form_tool"}}, ft, formScript())
	resp, err := f.ag.Chat(context.Background(), "s-pq-deny", "go")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("content = %q", resp.Content)
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("denied tool ran")
	}
	row := toolRow(t, f, "s-pq-deny", "c-form")
	if row.ToolResult == nil || row.ToolResult.Success {
		t.Fatalf("row = %#v, want a denial", row.ToolResult)
	}
}

func parkForm(t *testing.T, f *parkFixture, sid string) *shuttle.HumanRequest {
	t.Helper()
	_, err := f.ag.Chat(context.Background(), sid, "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	return f.pendingParked(t, sid)
}

func TestParkable_ResumeApprovedBuildsResultFromAnswer(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-ok")

	resp, err := f.ag.ResumeChat(context.Background(), "s-pq-ok", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": `{"name":"x"}`},
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "done" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got, _ := ft.gotAnswer.Load().(string); got != `{"name":"x"}` {
		t.Fatalf("answer = %q", got)
	}
	in, _ := ft.gotInput.Load().(map[string]interface{})
	if in["title"] != "Name it" {
		t.Fatalf("input = %#v", in)
	}
	row := toolRow(t, f, "s-pq-ok", "c-form")
	data, _ := row.ToolResult.Data.(map[string]interface{})
	if !row.ToolResult.Success || data["status"] != "submitted" || data["echo"] != `{"name":"x"}` {
		t.Fatalf("row = %#v", row.ToolResult)
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("Execute ran on resume")
	}
}

func TestParkable_ResumeRejected(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-rej")

	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-rej", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: false, Reason: "rejected by user: nope",
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	row := toolRow(t, f, "s-pq-rej", "c-form")
	if row.ToolResult.Success || row.ToolResult.Error == nil ||
		row.ToolResult.Error.Code != "permission_denied" || row.ToolResult.Error.Message != "rejected by user: nope" {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}

func TestParkable_ResumeEmptyAnswerIsMissing(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-empty")

	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-empty", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true, Answers: map[string]string{"c-form": ""},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	row := toolRow(t, f, "s-pq-empty", "c-form")
	if row.ToolResult.Success || row.ToolResult.Error == nil || row.ToolResult.Error.Code != "MISSING_ANSWER" {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}

func TestParkable_MixedBatchWithApprovalItem(t *testing.T) {
	ft := newFormTool(true)
	script := formScript(llmtypes.ToolCall{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}})
	f := newFormFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, ft, script, "export_csv")

	hr := parkForm(t, f, "s-pq-mixed")
	if hr.Kind != "approval" {
		t.Fatalf("kind = %q, want approval for a mixed batch", hr.Kind)
	}
	if len(hr.Params) != 2 {
		t.Fatalf("params keys = %v", paramKeys(hr))
	}
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-mixed", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": `{"a":1}`},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Fatalf("export_csv ran %d times, want 1", got)
	}
	if got, _ := ft.gotAnswer.Load().(string); got != `{"a":1}` {
		t.Fatalf("answer = %q", got)
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("form Execute ran")
	}
}

// The park-time classification is recorded on the row and is the authority on
// resume: a ParkQuestion that flips to ok=false afterwards (input re-decoded
// from JSON, changed config, re-registered tool) must not run Execute or drop
// the human's answer.
func TestParkable_ResumeTrustsRecordedKindWhenClassifierFlipsToNotOK(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-flip")

	ft.okToPark.Store(false)
	callsBefore := ft.parkCalls.Load()
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-flip", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "the answer"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("Execute ran %d times on resume, want 0", ft.runs.Load())
	}
	if got, _ := ft.gotAnswer.Load().(string); got != "the answer" {
		t.Fatalf("answer seen = %q, want the human's answer", got)
	}
	if ft.parkCalls.Load() != callsBefore {
		t.Fatalf("ParkQuestion was re-evaluated on resume")
	}
	row := toolRow(t, f, "s-pq-flip", "c-form")
	if !row.ToolResult.Success {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}

// The reverse flip: a call parked as an approval must stay an approval even if
// ParkQuestion now says ok; it must execute under the grant, not become
// MISSING_ANSWER.
func TestParkable_ResumeTrustsRecordedKindWhenClassifierFlipsToOK(t *testing.T) {
	ft := newFormTool(false)
	f := newFormFixture(t, []shuttle.Hook{scopedAskHook{tool: "form_tool"}}, ft, formScript())
	hr := parkForm(t, f, "s-pq-flip2")
	if hr.Kind != "approval" {
		t.Fatalf("kind = %q, want approval", hr.Kind)
	}

	ft.okToPark.Store(true)
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-flip2", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if ft.runs.Load() != 1 {
		t.Fatalf("Execute ran %d times, want 1 under the approval", ft.runs.Load())
	}
	row := toolRow(t, f, "s-pq-flip2", "c-form")
	if !row.ToolResult.Success || row.ToolResult.Data != "executed" {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}

func TestParkable_ResumeNilAnswerResultIsInternalError(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-nil")

	ft.nilResult.Store(true)
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-nil", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "x"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	row := toolRow(t, f, "s-pq-nil", "c-form")
	if row.ToolResult.Success || row.ToolResult.Error == nil || row.ToolResult.Error.Code != "internal_error" {
		t.Fatalf("row = %#v", row.ToolResult)
	}
	if ft.runs.Load() != 0 {
		t.Fatalf("Execute ran")
	}
}

// A recorded question whose tool is no longer a ParkableQuestion synthesizes
// an error instead of dispatching Execute.
func TestParkable_ResumeToolNoLongerParkableSynthesizesError(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-plain")

	plain := &plainFormTool{}
	f.ag.RegisterTool(plain)
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-plain", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "x"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if plain.runs.Load() != 0 {
		t.Fatalf("Execute ran for a recorded question")
	}
	row := toolRow(t, f, "s-pq-plain", "c-form")
	if row.ToolResult.Success || row.ToolResult.Error == nil || row.ToolResult.Error.Code != "internal_error" {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}

// plainFormTool re-registers form_tool without the ParkableQuestion methods.
type plainFormTool struct{ runs atomic.Int64 }

func (t *plainFormTool) Name() string        { return "form_tool" }
func (t *plainFormTool) Description() string { return "form tool" }
func (t *plainFormTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (t *plainFormTool) Backend() string { return "" }
func (t *plainFormTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	return &shuttle.Result{Success: true, Data: "executed"}, nil
}

// An empty call ID cannot bind a card item, so the batch is not parked and
// Execute runs inline.
func TestParkable_EmptyCallIDRunsExecute(t *testing.T) {
	ft := newFormTool(true)
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{{ID: "", Name: "form_tool", Input: map[string]interface{}{"title": "t"}}}},
		{content: "done"},
	}
	f := newFormFixture(t, nil, ft, script)
	if _, err := f.ag.Chat(context.Background(), "s-pq-noid", "go"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if ft.runs.Load() != 1 {
		t.Fatalf("Execute ran %d times, want 1", ft.runs.Load())
	}
}

// A ParkableQuestion under a policy Ask parks as a question only; there is no
// separate approval item.
func TestParkable_AskPolicyYieldsQuestionOnly(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, []shuttle.Hook{scopedAskHook{tool: "form_tool"}}, ft, formScript())
	hr := parkForm(t, f, "s-pq-ask")
	if hr.Kind != "question" {
		t.Fatalf("kind = %q, want question", hr.Kind)
	}
	desc, _ := hr.Params["c-form"].(map[string]interface{})
	if desc["kind"] != "question" {
		t.Fatalf("descriptor = %#v", desc)
	}
}

// After a restart the parked call's input has been through JSON, so a numeric
// field reaches ParkAnswerResult as float64 (documented on
// shuttle.ParkableQuestion). The call is scripted with an int so a pass proves
// the JSON round trip, not the mock's literal.
func TestParkable_ResumeAfterRestartDeliversNumbersAsFloat64(t *testing.T) {
	ft := newFormTool(true)
	script := []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{
			ID: "c-form", Name: "form_tool",
			Input: map[string]interface{}{"title": "How many?", "count": 3},
		}}},
		{content: "done"},
	}
	f := newFormFixture(t, nil, ft, script)
	hr := parkForm(t, f, "s-pq-json")

	// Agent B: same session and park stores, fresh process state.
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	restored := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: []mockLLMResponse{{content: "done"}}},
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(f.sessions)),
		WithHITLPark(f.store, 0, NewProgressNotifier()),
	)
	rt := newFormTool(true)
	restored.RegisterTool(rt)

	if _, err := restored.ResumeChat(context.Background(), "s-pq-json", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "yes"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	in, _ := rt.gotInput.Load().(map[string]interface{})
	if in == nil {
		t.Fatalf("ParkAnswerResult was not called on the restored agent")
	}
	if n, ok := in["count"].(float64); !ok || n != 3 {
		t.Fatalf("count = %#v (%T), want float64(3)", in["count"], in["count"])
	}
	if rt.runs.Load() != 0 {
		t.Fatalf("Execute ran on resume")
	}
}

// The park and resume seams are traced: agent.park_question carries the tool
// name and the parks decision; agent.park_answer_result carries the tool name.
func TestParkable_EmitsParkSpans(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	tracer := observability.NewMockTracer()
	f.ag.tracer = tracer
	hr := parkForm(t, f, "s-pq-span")

	pq := tracer.GetSpanByName("agent.park_question")
	if pq == nil {
		t.Fatalf("no agent.park_question span")
	}
	if pq.Attributes["tool"] != "form_tool" || pq.Attributes["parks"] != true {
		t.Fatalf("park_question attributes = %#v", pq.Attributes)
	}
	if tracer.GetSpanByName("agent.park_answer_result") != nil {
		t.Fatalf("park_answer_result span emitted before resume")
	}

	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-span", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "x"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	ar := tracer.GetSpanByName("agent.park_answer_result")
	if ar == nil {
		t.Fatalf("no agent.park_answer_result span")
	}
	if ar.Attributes["tool"] != "form_tool" {
		t.Fatalf("park_answer_result attributes = %#v", ar.Attributes)
	}
}

// ParkQuestion reporting ok=false is recorded on the span too.
func TestParkable_SpanRecordsNotParking(t *testing.T) {
	ft := newFormTool(false)
	f := newFormFixture(t, nil, ft, formScript())
	tracer := observability.NewMockTracer()
	f.ag.tracer = tracer
	if _, err := f.ag.Chat(context.Background(), "s-pq-span-no", "go"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	pq := tracer.GetSpanByName("agent.park_question")
	if pq == nil || pq.Attributes["parks"] != false {
		t.Fatalf("park_question span = %#v", pq)
	}
}

// A recorded question whose tool has been unregistered entirely reports
// tool_not_found. Only the message matches dispatch's unknown-tool error;
// dispatch returns a Go error with no result or code.
func TestParkable_ResumeToolUnregisteredIsToolNotFound(t *testing.T) {
	ft := newFormTool(true)
	f := newFormFixture(t, nil, ft, formScript())
	hr := parkForm(t, f, "s-pq-gone")

	f.ag.tools.Unregister("form_tool")
	if _, err := f.ag.ResumeChat(context.Background(), "s-pq-gone", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		Answers: map[string]string{"c-form": "x"},
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	row := toolRow(t, f, "s-pq-gone", "c-form")
	if row.ToolResult.Success || row.ToolResult.Error == nil ||
		row.ToolResult.Error.Code != "tool_not_found" ||
		!strings.Contains(row.ToolResult.Error.Message, "form_tool") {
		t.Fatalf("row = %#v", row.ToolResult)
	}
}
