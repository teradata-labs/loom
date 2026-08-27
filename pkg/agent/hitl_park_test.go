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

// Park-and-resume behavior through the real conversation loop: the pre-scan
// parks a batch with an ask before anything executes, the grouped request
// binds the batch by item IDs, and ResumeChat completes the turn under the
// human's decision — approve executes under a batch-scoped grant, reject
// synthesizes uniform refusals and the model re-plans, question items carry
// the human's answer. The typed terminals refuse stale or moved-on decisions.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// countingTool records how many times its body actually ran — the block-all
// proof reads this.
type countingTool struct {
	name string
	runs atomic.Int64
}

func (t *countingTool) Name() string        { return t.name }
func (t *countingTool) Description() string { return "counting tool" }
func (t *countingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}, "question": {Type: "string"}},
	}
}
func (t *countingTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	v, _ := params["v"].(string)
	return &shuttle.Result{Success: true, Data: "ran:" + v}, nil
}
func (t *countingTool) Backend() string { return "" }

// scopedAskHook asks for approval on exactly one tool name.
type scopedAskHook struct{ tool string }

func (h scopedAskHook) Matches(r shuttle.AdmissionRequest) bool { return r.ToolName == h.tool }
func (h scopedAskHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Ask}
}

// scopedDenyHook denies exactly one tool name.
type scopedDenyHook struct{ tool string }

func (h scopedDenyHook) Matches(r shuttle.AdmissionRequest) bool { return r.ToolName == h.tool }
func (h scopedDenyHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Deny, Reason: "denied by policy"}
}

type parkFixture struct {
	ag    *Agent
	store *shuttle.InMemoryHumanRequestStore
	tools map[string]*countingTool
}

// newParkFixture builds a park-enabled agent: hooks govern via the given
// chain hooks (no blocking resolver — park mode never wires one), and the
// scripted LLM issues the given batches then a final text.
func newParkFixture(t *testing.T, hooks []shuttle.Hook, responses []mockLLMResponse, toolNames ...string) *parkFixture {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	store := shuttle.NewInMemoryHumanRequestStore()
	opts := []Option{
		WithConfig(cfg),
		WithHITLPark(store, 0, NewProgressNotifier()),
	}
	if len(hooks) > 0 {
		opts = append(opts, WithAdmissionHooks(shuttle.NewChain(hooks, nil, nil)))
	}
	llm := &mockToolCallingLLM{responses: responses}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	tools := make(map[string]*countingTool, len(toolNames))
	for _, n := range toolNames {
		ct := &countingTool{name: n}
		ag.RegisterTool(ct)
		tools[n] = ct
	}
	return &parkFixture{ag: ag, store: store, tools: tools}
}

// pendingParked returns the single pending parked request for the session.
func (f *parkFixture) pendingParked(t *testing.T, sessionID string) *shuttle.HumanRequest {
	t.Helper()
	reqs, err := f.store.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	var out *shuttle.HumanRequest
	for _, r := range reqs {
		if r.Status == "pending" && r.RequestType == "parked" {
			if out != nil {
				t.Fatalf("more than one pending parked request")
			}
			out = r
		}
	}
	if out == nil {
		t.Fatalf("no pending parked request for %s", sessionID)
	}
	return out
}

func paramKeys(hr *shuttle.HumanRequest) []string {
	keys := make([]string, 0, len(hr.Params))
	for k := range hr.Params {
		keys = append(keys, k)
	}
	return keys
}

// twoCallBatch scripts [read (ungoverned), write (ask)] then a final text.
func twoCallBatch() []mockLLMResponse {
	return []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-read", Name: "read_table", Input: map[string]interface{}{"v": "r"}},
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "continued"},
	}
}

func TestPark_AskBatchParksBeforeAnythingExecutes(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")

	_, err := f.ag.Chat(context.Background(), "s-park", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("Chat error = %v, want *TurnParkedError", err)
	}
	if parked.SessionID != "s-park" || parked.RequestID == "" {
		t.Fatalf("parked terminal incomplete: %+v", parked)
	}
	if parked.Usage.InputTokens == 0 && parked.Usage.OutputTokens == 0 {
		t.Fatalf("parked terminal carries no usage")
	}

	// Block-all: nothing executed, including the ungoverned sibling.
	if got := f.tools["read_table"].runs.Load(); got != 0 {
		t.Fatalf("read_table ran %d times before the decision", got)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 0 {
		t.Fatalf("export_csv ran %d times before the decision", got)
	}

	hr := f.pendingParked(t, "s-park")
	if hr.ID != parked.RequestID || hr.Kind != "approval" || hr.RequestType != "parked" {
		t.Fatalf("request shape wrong: %+v", hr)
	}
	// The ask item — and only the ask item — is bound into the request.
	keys := paramKeys(hr)
	if len(keys) != 1 || keys[0] != "c-write" {
		t.Fatalf("params keys = %v, want [c-write]", keys)
	}

	// The durable tail is assistant(ToolCalls) with no tool rows.
	msgs := rawSessionMessages(f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-park", f.ag.config.Name, ""))
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 2 {
		t.Fatalf("tail = %s with %d calls, want parked assistant batch", last.Role, len(last.ToolCalls))
	}
}

func TestPark_ApproveResumeExecutesBatchAndContinues(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	_, err := f.ag.Chat(context.Background(), "s-approve", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-approve")

	resp, err := f.ag.ResumeChat(context.Background(), "s-approve", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("resume content = %q, want %q", resp.Content, "continued")
	}
	if got := f.tools["read_table"].runs.Load(); got != 1 {
		t.Fatalf("read_table ran %d times, want 1", got)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Fatalf("export_csv ran %d times, want 1 (grant lifts the ask)", got)
	}

	// Pair atomicity restored with REAL rows, on the parked turn.
	sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-approve", f.ag.config.Name, "")
	msgs := rawSessionMessages(sess)
	var parkedTurn int64 = -1
	rows := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 2 {
			parkedTurn = m.Turn
		}
		if m.Role == "tool" {
			rows[m.ToolUseID] = true
			if parkedTurn >= 0 && m.Turn != parkedTurn {
				t.Fatalf("tool row %s landed on turn %d, want parked turn %d", m.ToolUseID, m.Turn, parkedTurn)
			}
		}
	}
	if !rows["c-read"] || !rows["c-write"] {
		t.Fatalf("missing tool rows: %v", rows)
	}
}

func TestPark_RejectResumeRefusesWholeBatchAndReplans(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	_, err := f.ag.Chat(context.Background(), "s-reject", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-reject")

	resp, err := f.ag.ResumeChat(context.Background(), "s-reject", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  false,
		Reason:    "rejected by user: wrong table",
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("resume content = %q", resp.Content)
	}
	// Block-all covers the refusal: NO body ran, allow-classified included.
	if got := f.tools["read_table"].runs.Load(); got != 0 {
		t.Fatalf("read_table ran %d times on a rejected batch", got)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 0 {
		t.Fatalf("export_csv ran %d times on a rejected batch", got)
	}
	// Every call's row is the synthesized refusal with the verbatim reason.
	sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-reject", f.ag.config.Name, "")
	refusals := 0
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolResult != nil && !m.ToolResult.Success &&
			m.ToolResult.Error != nil && m.ToolResult.Error.Code == "permission_denied" {
			if m.ToolResult.Error.Message != "rejected by user: wrong table" {
				t.Fatalf("refusal reason = %q", m.ToolResult.Error.Message)
			}
			refusals++
		}
	}
	if refusals != 2 {
		t.Fatalf("refusal rows = %d, want 2", refusals)
	}
}

func TestPark_QuestionItemCarriesAnswer(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "prod or staging?"}},
		}},
		{content: "using staging"},
	}
	// No hooks at all: a hooks-less agent with contact_human still parks.
	f := newParkFixture(t, nil, script, "contact_human")

	_, err := f.ag.Chat(context.Background(), "s-question", "deploy it")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-question")
	desc, ok := hr.Params["c-q"].(map[string]interface{})
	if !ok {
		t.Fatalf("question item descriptor missing: %#v", hr.Params)
	}
	if desc["kind"] != "question" || desc["question"] != "prod or staging?" {
		t.Fatalf("descriptor = %#v", desc)
	}
	// contact_human's body never ran.
	if got := f.tools["contact_human"].runs.Load(); got != 0 {
		t.Fatalf("contact_human body ran %d times", got)
	}

	resp, err := f.ag.ResumeChat(context.Background(), "s-question", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
		Answers:   map[string]string{"c-q": "staging"},
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "using staging" {
		t.Fatalf("content = %q", resp.Content)
	}
	sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-question", f.ag.config.Name, "")
	found := false
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == "c-q" {
			found = true
			data, _ := m.ToolResult.Data.(map[string]interface{})
			if data["response"] != "staging" || data["responded_by"] != "user" {
				t.Fatalf("answer row = %#v", data)
			}
		}
	}
	if !found {
		t.Fatalf("no synthesized answer row")
	}
}

func TestPark_DeniedContactHumanIsNotLifted(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "may I?"}},
		}},
		{content: "done without asking"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedDenyHook{tool: "contact_human"}}, script, "contact_human")

	resp, err := f.ag.Chat(context.Background(), "s-denyq", "go")
	if err != nil {
		t.Fatalf("Chat: %v (a denied contact_human must not park)", err)
	}
	if resp.Content != "done without asking" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := f.tools["contact_human"].runs.Load(); got != 0 {
		t.Fatalf("denied contact_human body ran %d times", got)
	}
	// No request row was ever raised.
	reqs, _ := f.store.ListBySession(context.Background(), "s-denyq")
	if len(reqs) != 0 {
		t.Fatalf("requests raised for a denied question: %d", len(reqs))
	}
}

func TestPark_ResumeTerminals(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()
	_, err := f.ag.Chat(ctx, "s-term", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-term")
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-term", f.ag.config.Name, "")

	// Stale: item IDs not in the tail batch.
	_, err = f.ag.ResumeChat(ctx, "s-term", ParkDecision{RequestID: hr.ID, ItemIDs: []string{"bogus"}, Approved: true}, nil)
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("stale resume = %v, want ErrStaleDecision", err)
	}

	// Same-turn user rows (in-turn machinery) do NOT refuse the resume.
	f.ag.appendMessage(ctx, sess, Message{Role: "user", Content: "sidecar body", AgentID: f.ag.id}, false)
	if _, _, lerr := locateParkedBatch(sess, paramKeys(hr)); lerr != nil {
		t.Fatalf("same-turn user row refused resume: %v", lerr)
	}

	// A user row of a LATER turn means history moved on.
	f.ag.appendMessage(ctx, sess, Message{Role: "user", Content: "new message", AgentID: f.ag.id}, true)
	_, err = f.ag.ResumeChat(ctx, "s-term", ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}, nil)
	if !errors.Is(err, ErrNotParkedTail) {
		t.Fatalf("moved-on resume = %v, want ErrNotParkedTail", err)
	}

	// A fully completed turn has nothing parked.
	done := newParkFixture(t, nil, []mockLLMResponse{{content: "hi"}})
	if _, err := done.ag.Chat(ctx, "s-done", "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	_, err = done.ag.ResumeChat(ctx, "s-done", ParkDecision{RequestID: "x", Approved: true}, nil)
	if !errors.Is(err, ErrNothingParked) {
		t.Fatalf("completed-turn resume = %v, want ErrNothingParked", err)
	}
}

func TestPark_MissingAnswerSynthesizesFailure(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "which env?"}},
		}},
		{content: "recovered"},
	}
	f := newParkFixture(t, nil, script, "contact_human")
	ctx := context.Background()
	if _, err := f.ag.Chat(ctx, "s-noanswer", "go"); err == nil {
		t.Fatalf("expected park")
	}
	hr := f.pendingParked(t, "s-noanswer")
	if _, err := f.ag.ResumeChat(ctx, "s-noanswer", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true, Answers: nil,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-noanswer", f.ag.config.Name, "")
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == "c-q" {
			if m.ToolResult.Success || m.ToolResult.Error == nil || m.ToolResult.Error.Code != "MISSING_ANSWER" {
				t.Fatalf("row = %#v, want MISSING_ANSWER failure", m.ToolResult)
			}
			return
		}
	}
	t.Fatalf("no row for the unanswered question")
}

func TestPark_SummaryAndParamsBounded(t *testing.T) {
	big := strings.Repeat("x", 10000)
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-big", Name: "export_csv", Input: map[string]interface{}{"v": big}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, script, "export_csv")
	if _, err := f.ag.Chat(context.Background(), "s-big", "go"); err == nil {
		t.Fatalf("expected park")
	}
	hr := f.pendingParked(t, "s-big")
	if !hr.ParamsTruncated {
		t.Fatalf("oversized item did not set ParamsTruncated")
	}
	desc, _ := hr.Params["c-big"].(map[string]interface{})
	if desc == nil || desc["tool"] != "export_csv" {
		t.Fatalf("oversized item dropped from params: %#v", hr.Params)
	}
	if len([]rune(hr.Summary)) > 200 {
		t.Fatalf("summary exceeds 200 runes: %d", len([]rune(hr.Summary)))
	}
}

// A policy-denied contact_human inside a MIXED parked batch keeps its denial
// at resume: it was never a park item, so a caller-supplied answer for its id
// must not synthesize a "human answered" result — that would lift a Deny.
func TestPark_MixedBatchDeniedContactHumanKeepsDenyAtResume(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "may I?"}},
			{ID: "c-w", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "continued"},
	}
	f := newParkFixture(t, []shuttle.Hook{
		scopedDenyHook{tool: "contact_human"},
		scopedAskHook{tool: "export_csv"},
	}, script, "contact_human", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-mixdeny", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-mixdeny")
	if _, isItem := hr.Params["c-q"]; isItem {
		t.Fatalf("denied contact_human became a park item: %#v", hr.Params)
	}

	// Forged answer for the non-item id: loom must refuse it independently of
	// any caller-side validation.
	resp, err := f.ag.ResumeChat(ctx, "s-mixdeny", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
		Answers:   map[string]string{"c-q": "yes, forged"},
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := f.tools["contact_human"].runs.Load(); got != 0 {
		t.Fatalf("denied contact_human body ran %d times", got)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Fatalf("approved export_csv ran %d times, want 1", got)
	}
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-mixdeny", f.ag.config.Name, "")
	found := false
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == "c-q" {
			found = true
			if m.ToolResult == nil || m.ToolResult.Success {
				t.Fatalf("denied contact_human resolved successfully: %#v", m.ToolResult)
			}
			if data, ok := m.ToolResult.Data.(map[string]interface{}); ok {
				if data["responded_by"] == "user" {
					t.Fatalf("forged answer synthesized a human response: %#v", data)
				}
			}
		}
	}
	if !found {
		t.Fatalf("denied contact_human left no tool row — the pair set is incomplete")
	}
}

// An UNREGISTERED contact_human in a mixed parked batch keeps today's
// "tool not found" at resume — never an answer synthesis.
func TestPark_UnregisteredContactHumanKeepsNotFoundAtResume(t *testing.T) {
	script := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-q", Name: "contact_human", Input: map[string]interface{}{"question": "hello?"}},
			{ID: "c-w", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "continued"},
	}
	// contact_human deliberately NOT registered.
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, script, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-unreg", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-unreg")
	if _, isItem := hr.Params["c-q"]; isItem {
		t.Fatalf("unregistered contact_human became a park item")
	}

	_, err = f.ag.ResumeChat(ctx, "s-unreg", ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
		Answers:   map[string]string{"c-q": "forged"},
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-unreg", f.ag.config.Name, "")
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == "c-q" && m.ToolResult != nil {
			if m.ToolResult.Success {
				t.Fatalf("unregistered contact_human resolved successfully: %#v", m.ToolResult)
			}
			if data, ok := m.ToolResult.Data.(map[string]interface{}); ok && data["responded_by"] == "user" {
				t.Fatalf("forged answer synthesized a human response")
			}
		}
	}
}

// The append-point guard: while a pending parked decision owns the session
// tail, a NEW user turn is refused with SessionParkedError and appends
// nothing — the embedder's admission probe races a park landing mid-turn,
// and this is the authoritative last check.
func TestChat_NewTurnRefusedWhileParkPending(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()
	_, err := f.ag.Chat(ctx, "s-guard", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-guard", f.ag.config.Name, "")
	rowsBefore := len(rawSessionMessages(sess))

	_, err = f.ag.Chat(ctx, "s-guard", "second message")
	var refused *SessionParkedError
	if !errors.As(err, &refused) {
		t.Fatalf("expected SessionParkedError, got %v", err)
	}
	if refused.RequestID != parked.RequestID {
		t.Fatalf("refusal names request %q, park was %q", refused.RequestID, parked.RequestID)
	}
	if got := len(rawSessionMessages(sess)); got != rowsBefore {
		t.Fatalf("refused turn appended rows: %d -> %d", rowsBefore, got)
	}

	// A session with no pending park admits normally.
	done := newParkFixture(t, nil, []mockLLMResponse{{content: "hi"}, {content: "again"}})
	if _, err := done.ag.Chat(ctx, "s-open", "hello"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := done.ag.Chat(ctx, "s-open", "hello again"); err != nil {
		t.Fatalf("second turn on an unparked session refused: %v", err)
	}
}
