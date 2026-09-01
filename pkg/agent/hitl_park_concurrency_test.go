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

// Concurrency and claim-order properties of ResumeChat. One human decision
// authorizes one execution of its batch — no matter how many times, or how
// simultaneously, that decision is delivered.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// slowTool holds the batch open long enough for a second resume to overlap
// the first — the window between the row's pending check and its close.
type slowTool struct {
	name string
	runs atomic.Int64
	wait time.Duration
}

func (t *slowTool) Name() string        { return t.name }
func (t *slowTool) Description() string { return "slow tool" }
func (t *slowTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *slowTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	time.Sleep(t.wait)
	return &shuttle.Result{Success: true, Data: "slow-ok"}, nil
}
func (t *slowTool) Backend() string { return "" }

// TestPark_ConcurrentResumesExecuteTheBatchOnce drives two resumes of ONE
// decision through the whole entry point and asserts the batch runs once,
// exactly one caller reports success, and one tool row lands per tool_use_id
// (duplicates would put two tool_result blocks for one id in the next
// provider request).
//
// It is a smoke test, NOT the proof of the claim mechanism. Because the claim
// now happens microseconds after the row is read, goroutine scheduling alone
// usually decides the winner here — the loser is typically refused at
// loadParkedRequest, before the claim is ever contended. Deleting the claim
// token check does not fail this test. The mechanism itself is pinned by
// TestClaimParkedRequest_ExactlyOneWinner, which contends the claim directly.
func TestPark_ConcurrentResumesExecuteTheBatchOnce(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-slow", Name: "slow_write", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "done"},
		{content: "done again"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "slow_write"}}, responses)
	slow := &slowTool{name: "slow_write", wait: 300 * time.Millisecond}
	f.ag.RegisterTool(slow)

	ctx := context.Background()
	_, err := f.ag.Chat(ctx, "s-conc", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-conc")
	decision := ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.ag.ResumeChat(ctx, "s-conc", decision, nil)
		}(i)
	}
	wg.Wait()

	if got := slow.runs.Load(); got != 1 {
		t.Errorf("approved tool body ran %d times for ONE human approval, want 1", got)
	}

	// Exactly one resume may succeed; the other must say so rather than
	// silently returning success for work it did not do. Which terminal the
	// loser gets depends on where it lost — refused at the claim
	// (ErrUnknownRequest), or arriving after the winner finished the turn
	// (ErrNothingParked). Both are correct refusals; silence is not.
	succeeded := 0
	for i, e := range errs {
		if e == nil {
			succeeded++
			continue
		}
		if !errors.Is(e, ErrUnknownRequest) && !errors.Is(e, ErrNothingParked) {
			t.Errorf("resume %d failed with %v, want nil or a terminal refusal", i, e)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d resumes reported success, want exactly 1 (errors: %v)", succeeded, errs)
	}

	// One tool row for the call — duplicates break tool_use/tool_result pairing.
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-conc", f.ag.config.Name, "")
	rows := 0
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == "c-slow" {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("%d tool rows for tool_use_id c-slow, want 1", rows)
	}
}

// TestPark_ClaimPrecedesExecution pins the ordering the at-most-once
// guarantee rests on: by the time any tool body runs, the row is already
// closed. If execution came first, a crash — or an expiry crossing mid-batch
// — would leave a decided batch with a pending row, and the session wedged.
func TestPark_ClaimPrecedesExecution(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-obs", Name: "observer", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "observer"}}, responses)

	var statusAtRun string
	obs := &funcTool{
		name: "observer",
		run: func(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
			if reqs, err := f.store.ListBySession(ctx, "s-claim"); err == nil {
				for _, r := range reqs {
					if r.RequestType == "parked" {
						statusAtRun = r.Status
					}
				}
			}
			return &shuttle.Result{Success: true, Data: "ok"}, nil
		},
	}
	f.ag.RegisterTool(obs)

	ctx := context.Background()
	_, err := f.ag.Chat(ctx, "s-claim", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-claim")

	if _, err := f.ag.ResumeChat(ctx, "s-claim", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if statusAtRun != "approved" {
		t.Errorf("row status while the approved body ran = %q, want \"approved\" "+
			"(the decision must be claimed before anything executes)", statusAtRun)
	}
}

// TestPark_ExpiryCrossingDuringTheBatchDoesNotWedge — the TTL lapses while
// the approved batch is still running. This cannot wedge the session BY
// CONSTRUCTION: the decision is claimed and the row closed before any tool
// body starts, so a lapse during the batch finds an already-closed row. The
// test pins that construction end to end — moving the claim back after the
// batch fails it, because then the close would meet an expired row,
// RespondToRequest would refuse it while returning nil, and the row would be
// left pending forever.
//
// The read-back that catches a refused write is pinned directly by
// TestClaimParkedRequest_LapsedRowIsClosedNotSilentlyLeftPending.
func TestPark_ExpiryCrossingDuringTheBatchDoesNotWedge(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-slow", Name: "slow_write", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "done"},
		{content: "next turn"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "slow_write"}}, responses)
	f.ag.RegisterTool(&slowTool{name: "slow_write", wait: 400 * time.Millisecond})

	ctx := context.Background()
	_, err := f.ag.Chat(ctx, "s-cross", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-cross")

	// Expire shortly after the resume starts, while the tool is still running.
	hr.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	if err := f.store.Update(ctx, hr); err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, _ = f.ag.ResumeChat(ctx, "s-cross", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil)

	after, _ := f.store.Get(ctx, hr.ID)
	if after != nil && after.Status == "pending" {
		t.Errorf("row left pending after the resume; the session is wedged")
	}
	if _, err := f.ag.Chat(ctx, "s-cross", "carry on"); err != nil {
		t.Errorf("turn after an expiry-crossing resume refused: %v", err)
	}
}

// TestPark_AbandonedParkStopsHoldingTheSessionOnceLapsed — nothing sweeps
// parked rows, so guardParkedTail must not treat a lapsed one as holding the
// session. Otherwise a park nobody ever decides kills the session forever.
func TestPark_AbandonedParkStopsHoldingTheSessionOnceLapsed(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "unreachable"},
		{content: "later turn"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, responses, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-aband", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-aband")

	// While the window is open the session IS held.
	if _, err := f.ag.Chat(ctx, "s-aband", "too soon"); !errors.As(err, new(*SessionParkedError)) {
		t.Fatalf("turn during an open park = %v, want SessionParkedError", err)
	}

	// Nobody ever decides; the window lapses.
	hr.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.store.Update(ctx, hr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := f.ag.Chat(ctx, "s-aband", "much later"); err != nil {
		t.Errorf("turn after an abandoned park lapsed was refused: %v", err)
	}
}

// funcTool is a tool whose body is supplied per test.
type funcTool struct {
	name string
	run  func(context.Context, map[string]interface{}) (*shuttle.Result, error)
}

func (t *funcTool) Name() string        { return t.name }
func (t *funcTool) Description() string { return "func tool" }
func (t *funcTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"v": {Type: "string"}},
	}
}
func (t *funcTool) Execute(ctx context.Context, p map[string]interface{}) (*shuttle.Result, error) {
	return t.run(ctx, p)
}
func (t *funcTool) Backend() string { return "" }

// TestPark_UnresumableTurnReleasesTheSession — when the tail has moved on, the
// decision can never be applied, but the row is still PENDING. Left that way
// it holds the session at guardParkedTail forever, so an unresumable turn must
// close its own row on the way out.
func TestPark_UnresumableTurnReleasesTheSession(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "unreachable"},
		{content: "later turn"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, responses, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-unres", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-unres")

	// History moves on: a user row of a LATER turn lands after the batch.
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-unres", f.ag.config.Name, "")
	f.ag.appendMessage(ctx, sess, Message{Role: "user", Content: "new message", AgentID: f.ag.id}, true)

	if _, err := f.ag.ResumeChat(ctx, "s-unres", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); !errors.Is(err, ErrNotParkedTail) {
		t.Fatalf("resume of a moved-on turn = %v, want ErrNotParkedTail", err)
	}

	after, _ := f.store.Get(ctx, hr.ID)
	if after == nil || after.Status == "pending" {
		t.Errorf("unresumable row left pending (%v); the session is wedged at guardParkedTail", after)
	}
	if err := f.ag.guardParkedTail(ctx, "s-unres"); err != nil {
		t.Errorf("session still held after the park became unresumable: %v", err)
	}
}

// TestPark_UnloadableSessionDoesNotDestroyTheApproval is the destructive-path
// guard. locateParkedBatch returns ErrNothingParked both for a turn that truly
// finished AND for a session with no assistant batch at all — and an empty
// session is reachable with the park perfectly intact, because
// GetOrCreateSessionWithAgent fails OPEN (a load error yields a fresh empty
// session under the same ID) and LoadSession is user-scoped. Closing the row
// on that signal erases the human's decision record for good.
//
// Simulated here by resuming a session id that holds a valid row but has no
// conversation: the refusal must be read-only.
func TestPark_UnloadableSessionDoesNotDestroyTheApproval(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-lost", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-lost")

	// Re-point the row at a session with no history at all, standing in for a
	// session the resume could not load.
	hr.SessionID = "s-lost-empty"
	if err := f.store.Update(ctx, hr); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := f.ag.ResumeChat(ctx, "s-lost-empty", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); !errors.Is(err, ErrNothingParked) {
		t.Fatalf("resume against an empty session = %v, want ErrNothingParked", err)
	}

	after, gerr := f.store.Get(ctx, hr.ID)
	if gerr != nil || after == nil {
		t.Fatalf("Get: %v", gerr)
	}
	if after.Status != "pending" {
		t.Errorf("APPROVAL DESTROYED: row status = %q after a refusal that proved nothing "+
			"about the turn; a correctly-scoped resume can never complete it now", after.Status)
	}
}
