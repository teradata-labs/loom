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

// Resource-await park through the real conversation loop: a successful tool
// result carrying AwaitResource holds its row back and parks the turn on the
// HITL park machinery (kind "resource"); the embedder's handler is consulted
// BEFORE the park commits (subscribe-before-park); resume injects the
// recorded terminal payload as the call's result and NEVER re-executes the
// tool body — the property the whole feature exists for, since re-running a
// job starter starts a second job.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// awaitingTool starts a "job": success with a placeholder Data and an
// AwaitResource asking the agent to park on the job resource.
type awaitingTool struct {
	name string
	uri  string
	runs atomic.Int64
}

func (t *awaitingTool) Name() string        { return t.name }
func (t *awaitingTool) Description() string { return "starts a background job" }
func (t *awaitingTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *awaitingTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	return &shuttle.Result{
		Success:       true,
		Data:          map[string]interface{}{"status": "working", "job": "j-1"},
		AwaitResource: &shuttle.AwaitResource{URI: t.uri},
	}, nil
}
func (t *awaitingTool) Backend() string { return "" }

// fakeAwaitHandler records PrepareWait/AbandonWait calls; refuse makes
// PrepareWait fail.
type fakeAwaitHandler struct {
	mu        sync.Mutex
	prepared  []ResourceAwaitRequest
	abandoned []ResourceAwaitRequest
	refuse    error
}

func (h *fakeAwaitHandler) PrepareWait(_ context.Context, req ResourceAwaitRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refuse != nil {
		return h.refuse
	}
	h.prepared = append(h.prepared, req)
	return nil
}

func (h *fakeAwaitHandler) AbandonWait(req ResourceAwaitRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.abandoned = append(h.abandoned, req)
}

// failingParkStore refuses Store — the park-row persist failure path.
type failingParkStore struct {
	*shuttle.InMemoryHumanRequestStore
}

func (s *failingParkStore) Store(context.Context, *shuttle.HumanRequest) error {
	return errors.New("store down")
}

type resourceParkFixture struct {
	ag      *Agent
	store   shuttle.HumanRequestStore
	mem     *shuttle.InMemoryHumanRequestStore
	handler *fakeAwaitHandler
	job     *awaitingTool
	sibling *countingTool
}

// jobThenText scripts [start_job + read (plain)] then a final text — the
// mixed batch every test here drives.
func jobThenText() []mockLLMResponse {
	return []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-job", Name: "start_job", Input: map[string]interface{}{"v": "j"}},
			{ID: "c-read", Name: "read_table", Input: map[string]interface{}{"v": "r"}},
		}},
		{content: "continued"},
	}
}

// newResourceParkFixture mirrors newParkFixture (real session store — park's
// durability precondition) plus the await handler wiring and the mixed-batch
// tools. failStore swaps in a park store whose Store always fails.
func newResourceParkFixture(t *testing.T, handler *fakeAwaitHandler, failStore bool) *resourceParkFixture {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "resource-park.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close() })

	mem := shuttle.NewInMemoryHumanRequestStore()
	var store shuttle.HumanRequestStore = mem
	if failStore {
		store = &failingParkStore{InMemoryHumanRequestStore: mem}
	}
	opts := []Option{
		WithConfig(cfg),
		WithMemory(NewMemoryWithStore(sessions)),
		WithHITLPark(store, 0, NewProgressNotifier()),
	}
	if handler != nil {
		opts = append(opts, WithResourceAwait(handler))
	}
	llm := &mockToolCallingLLM{responses: jobThenText()}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	job := &awaitingTool{name: "start_job", uri: "gdp://jobs/42"}
	sibling := &countingTool{name: "read_table"}
	ag.RegisterTool(job)
	ag.RegisterTool(sibling)
	return &resourceParkFixture{ag: ag, store: store, mem: mem, handler: handler, job: job, sibling: sibling}
}

// toolRowCount counts persisted tool rows carrying the given ToolUseID.
func toolRowCount(f *resourceParkFixture, sessionID, callID string) int {
	msgs := rawSessionMessages(f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, f.ag.config.Name, ""))
	n := 0
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolUseID == callID {
			n++
		}
	}
	return n
}

func pendingResourcePark(t *testing.T, mem *shuttle.InMemoryHumanRequestStore, sessionID string) *shuttle.HumanRequest {
	t.Helper()
	reqs, err := mem.ListBySession(context.Background(), sessionID)
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

func TestResourceAwait_HoldsAndParksTheTurn(t *testing.T) {
	h := &fakeAwaitHandler{}
	f := newResourceParkFixture(t, h, false)

	_, err := f.ag.Chat(context.Background(), "s-await", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("Chat error = %v, want *TurnParkedError", err)
	}

	// The job STARTED (unlike HITL park, the body ran — that is the point).
	if got := f.job.runs.Load(); got != 1 {
		t.Fatalf("start_job ran %d times, want 1", got)
	}
	// The sibling ran and its row landed; the awaited call's row is withheld.
	if got := f.sibling.runs.Load(); got != 1 {
		t.Fatalf("read_table ran %d times, want 1", got)
	}
	if n := toolRowCount(f, "s-await", "c-read"); n != 1 {
		t.Fatalf("sibling tool rows = %d, want 1", n)
	}
	if n := toolRowCount(f, "s-await", "c-job"); n != 0 {
		t.Fatalf("awaited call tool rows = %d, want 0 (withheld)", n)
	}

	// Subscribe-before-park: the handler saw the wait before the row landed.
	if len(h.prepared) != 1 || h.prepared[0].URI != "gdp://jobs/42" ||
		h.prepared[0].CallID != "c-job" || h.prepared[0].Tool != "start_job" ||
		h.prepared[0].SessionID != "s-await" {
		t.Fatalf("prepared = %+v", h.prepared)
	}
	if len(h.abandoned) != 0 {
		t.Fatalf("abandoned = %+v, want none", h.abandoned)
	}

	// The row: kind resource, one descriptor for the awaited call, uri bound.
	hr := pendingResourcePark(t, f.mem, "s-await")
	if hr.ID != parked.RequestID || hr.Kind != "resource" {
		t.Fatalf("request shape wrong: kind=%q id match=%v", hr.Kind, hr.ID == parked.RequestID)
	}
	desc, ok := hr.Params["c-job"].(map[string]interface{})
	if !ok || len(hr.Params) != 1 {
		t.Fatalf("params = %+v, want single c-job descriptor", hr.Params)
	}
	if desc["kind"] != "resource" || desc["uri"] != "gdp://jobs/42" || desc["tool"] != "start_job" {
		t.Fatalf("descriptor = %+v", desc)
	}
}

func TestResourceAwait_NoHandlerPassesThrough(t *testing.T) {
	f := newResourceParkFixture(t, nil, false)

	resp, err := f.ag.Chat(context.Background(), "s-nohandler", "go")
	if err != nil {
		t.Fatalf("Chat error = %v, want pass-through completion", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want %q", resp.Content, "continued")
	}
	// The original result committed as an ordinary row.
	if n := toolRowCount(f, "s-nohandler", "c-job"); n != 1 {
		t.Fatalf("awaited call tool rows = %d, want 1 (passed through)", n)
	}
}

func TestResourceAwait_HandlerRefusalPassesThrough(t *testing.T) {
	h := &fakeAwaitHandler{refuse: fmt.Errorf("nobody can watch that")}
	f := newResourceParkFixture(t, h, false)

	resp, err := f.ag.Chat(context.Background(), "s-refused", "go")
	if err != nil {
		t.Fatalf("Chat error = %v, want pass-through completion", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want %q", resp.Content, "continued")
	}
	if n := toolRowCount(f, "s-refused", "c-job"); n != 1 {
		t.Fatalf("awaited call tool rows = %d, want 1 (passed through)", n)
	}
	if len(h.abandoned) != 0 {
		t.Fatalf("abandoned = %+v, want none (nothing was prepared)", h.abandoned)
	}
}

func TestResourceAwait_ParkRowFailureCommitsHeldResultAndAbandons(t *testing.T) {
	h := &fakeAwaitHandler{}
	f := newResourceParkFixture(t, h, true)

	resp, err := f.ag.Chat(context.Background(), "s-storefail", "go")
	if err != nil {
		t.Fatalf("Chat error = %v, want fail-safe completion", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want %q", resp.Content, "continued")
	}
	// The held original result was committed — nothing strands rowless.
	if n := toolRowCount(f, "s-storefail", "c-job"); n != 1 {
		t.Fatalf("awaited call tool rows = %d, want 1 (fail-safe commit)", n)
	}
	// The prepared wait was told to stand down.
	if len(h.abandoned) != 1 || h.abandoned[0].URI != "gdp://jobs/42" {
		t.Fatalf("abandoned = %+v, want the one prepared wait", h.abandoned)
	}
}

// parkForResume drives a fixture to the parked state and returns the row.
func parkForResume(t *testing.T, f *resourceParkFixture, sessionID string) *shuttle.HumanRequest {
	t.Helper()
	_, err := f.ag.Chat(context.Background(), sessionID, "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	return pendingResourcePark(t, f.mem, sessionID)
}

func TestResourceAwait_ResumeInjectsRecordedResultWithoutReExecution(t *testing.T) {
	h := &fakeAwaitHandler{}
	f := newResourceParkFixture(t, h, false)
	hr := parkForResume(t, f, "s-inject")

	payload := map[string]interface{}{"status": "completed", "result": "42 rows loaded"}
	resp, err := f.ag.ResumeChat(context.Background(), "s-inject", ParkDecision{
		RequestID: hr.ID,
		Approved:  true,
		Results:   map[string]interface{}{"c-job": payload},
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat error = %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want %q", resp.Content, "continued")
	}

	// THE property: the tool body did not run again.
	if got := f.job.runs.Load(); got != 1 {
		t.Fatalf("start_job ran %d times, want exactly 1 (no re-execution on resume)", got)
	}

	// The injected payload IS the call's result Data, verbatim.
	msgs := rawSessionMessages(f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-inject", f.ag.config.Name, ""))
	var row *Message
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolUseID == "c-job" {
			row = &msgs[i]
		}
	}
	if row == nil || row.ToolResult == nil || !row.ToolResult.Success {
		t.Fatalf("no successful injected row for c-job")
	}
	data, ok := row.ToolResult.Data.(map[string]interface{})
	if !ok || data["status"] != "completed" || data["result"] != "42 rows loaded" {
		t.Fatalf("injected Data = %+v", row.ToolResult.Data)
	}
}

func TestResourceAwait_ResumeNotApprovedSynthesizesWaitFailure(t *testing.T) {
	h := &fakeAwaitHandler{}
	f := newResourceParkFixture(t, h, false)
	hr := parkForResume(t, f, "s-reject")

	resp, err := f.ag.ResumeChat(context.Background(), "s-reject", ParkDecision{
		RequestID: hr.ID,
		Approved:  false,
		Reason:    "job did not complete before the wait expired",
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat error = %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want the loop to continue", resp.Content)
	}
	if got := f.job.runs.Load(); got != 1 {
		t.Fatalf("start_job ran %d times, want exactly 1", got)
	}
	msgs := rawSessionMessages(f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-reject", f.ag.config.Name, ""))
	var row *Message
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolUseID == "c-job" {
			row = &msgs[i]
		}
	}
	if row == nil || row.ToolResult == nil || row.ToolResult.Success {
		t.Fatalf("expected synthesized failure row for c-job")
	}
	if row.ToolResult.Error == nil || row.ToolResult.Error.Code != "resource_wait_failed" ||
		row.ToolResult.Error.Message != "job did not complete before the wait expired" {
		t.Fatalf("synthesized error = %+v", row.ToolResult.Error)
	}
}

func TestResourceAwait_ResumeApprovedWithoutPayloadSynthesizesMissingResult(t *testing.T) {
	h := &fakeAwaitHandler{}
	f := newResourceParkFixture(t, h, false)
	hr := parkForResume(t, f, "s-missing")

	_, err := f.ag.ResumeChat(context.Background(), "s-missing", ParkDecision{
		RequestID: hr.ID,
		Approved:  true,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat error = %v", err)
	}
	if got := f.job.runs.Load(); got != 1 {
		t.Fatalf("start_job ran %d times, want exactly 1", got)
	}
	msgs := rawSessionMessages(f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-missing", f.ag.config.Name, ""))
	var row *Message
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolUseID == "c-job" {
			row = &msgs[i]
		}
	}
	if row == nil || row.ToolResult == nil || row.ToolResult.Success ||
		row.ToolResult.Error == nil || row.ToolResult.Error.Code != "MISSING_RESULT" {
		t.Fatalf("expected MISSING_RESULT row, got %+v", row)
	}
}
