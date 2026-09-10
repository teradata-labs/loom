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

// The parked request's lifecycle, and the reach of the decision that closes
// it. Three properties, each of which failed before the review fixes:
//
//   - a decided row is CLOSED, so the session keeps taking turns;
//   - the request row — not the caller's payload — binds the decision to a
//     batch, so an unbound decision cannot approve a batch nobody saw;
//   - the grant reaches only the items the human's card described.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// TestPark_DecidedRequestIsClosedAndSessionContinues pins the property the
// whole feature rests on: after a decision is applied, the next user message
// is accepted. guardParkedTail refuses a new turn while a parked row is
// pending, so a row left pending by the resume wedges the session forever.
func TestPark_DecidedRequestIsClosedAndSessionContinues(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "first turn done"},
		{content: "second turn done"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, responses, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-life", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-life")

	resp, err := f.ag.ResumeChat(ctx, "s-life", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if resp.Content != "first turn done" {
		t.Fatalf("resume content = %q", resp.Content)
	}

	// The row is terminally closed, carrying the verdict.
	after, err := f.store.Get(ctx, hr.ID)
	if err != nil || after == nil {
		t.Fatalf("Get after resume: %v", err)
	}
	if after.Status != "approved" {
		t.Fatalf("request status = %q, want approved", after.Status)
	}

	// And the session takes its next turn.
	next, err := f.ag.Chat(ctx, "s-life", "now the next thing")
	if err != nil {
		t.Fatalf("turn after resume refused: %v", err)
	}
	if next.Content != "second turn done" {
		t.Fatalf("next turn content = %q", next.Content)
	}
}

// TestPark_RejectedRequestIsClosedToo — the refusal path closes the row on the
// same contract; a rejected batch must not wedge the session either.
func TestPark_RejectedRequestIsClosedToo(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "replanned"},
		{content: "next turn"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, responses, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-rej", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-rej")

	if _, err := f.ag.ResumeChat(ctx, "s-rej", ParkDecision{
		RequestID: hr.ID, Approved: false, Reason: "rejected by user: not on prod",
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	after, _ := f.store.Get(ctx, hr.ID)
	if after == nil || after.Status != "rejected" {
		t.Fatalf("request status = %v, want rejected", after)
	}
	if _, err := f.ag.Chat(ctx, "s-rej", "carry on"); err != nil {
		t.Fatalf("turn after rejected resume refused: %v", err)
	}
}

// TestPark_DecisionCannotBeReapplied — a closed row is no longer resumable, so
// a redelivered decision cannot double-execute its batch.
func TestPark_DecisionCannotBeReapplied(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-once", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-once")
	decision := ParkDecision{RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true}

	if _, err := f.ag.ResumeChat(ctx, "s-once", decision, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Fatalf("export_csv ran %d times on the first resume, want 1", got)
	}

	// A decided row is still loadable (the embedder-recorded flow depends on
	// it), so the replay guard is the tail walk: the applied batch has its
	// rows and a final reply, and the replay lands in ErrNothingParked with
	// nothing re-executed.
	if _, err := f.ag.ResumeChat(ctx, "s-once", decision, nil); !errors.Is(err, ErrNothingParked) {
		t.Fatalf("replayed decision = %v, want ErrNothingParked", err)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Fatalf("export_csv ran %d times after the replay, want 1", got)
	}
}

// TestPark_UnboundDecisionCannotApproveANestedBatch is the binding proof. A
// nested park chains, so two requests can exist for one session; a decision
// that names request A must never be applied to batch B — including when the
// caller supplies no ItemIDs at all, which is exactly when a payload-derived
// binding would check nothing.
func TestPark_UnboundDecisionCannotApproveANestedBatch(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "a-write", Name: "export_csv", Input: map[string]interface{}{"v": "a"}},
		}},
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "b-drop", Name: "drop_table", Input: map[string]interface{}{"v": "b"}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t,
		[]shuttle.Hook{scopedAskHook{tool: "export_csv"}, scopedAskHook{tool: "drop_table"}},
		responses, "export_csv", "drop_table")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-bind", "go")
	var parkedA *TurnParkedError
	if !errors.As(err, &parkedA) {
		t.Fatalf("expected park on batch A, got %v", err)
	}
	reqA := parkedA.RequestID

	// Approve A; the continuation parks again on batch B.
	_, err = f.ag.ResumeChat(ctx, "s-bind", ParkDecision{
		RequestID: reqA, ItemIDs: []string{"a-write"}, Approved: true,
	}, nil)
	var parkedB *TurnParkedError
	if !errors.As(err, &parkedB) {
		t.Fatalf("expected nested park on batch B, got %v", err)
	}
	if parkedB.RequestID == reqA {
		t.Fatalf("nested park reused request A's id")
	}

	// A's decision, redelivered with NO item ids, must not touch batch B.
	// A's row is decided (loadable — the embedder-recorded flow depends on
	// it), so the binding does the refusing: A's items are not in B's batch.
	_, err = f.ag.ResumeChat(ctx, "s-bind", ParkDecision{RequestID: reqA, Approved: true}, nil)
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("unbound stale decision = %v, want ErrStaleDecision", err)
	}
	if got := f.tools["drop_table"].runs.Load(); got != 0 {
		t.Fatalf("drop_table ran %d times under request A's decision", got)
	}

	// B's own decision still works.
	if _, err := f.ag.ResumeChat(ctx, "s-bind", ParkDecision{
		RequestID: parkedB.RequestID, ItemIDs: []string{"b-drop"}, Approved: true,
	}, nil); err != nil {
		t.Fatalf("resume B: %v", err)
	}
	if got := f.tools["drop_table"].runs.Load(); got != 1 {
		t.Fatalf("drop_table ran %d times under its OWN decision, want 1", got)
	}
}

// TestPark_MismatchedItemIDsRefused — when a caller DOES supply item ids they
// are honored as a cross-check: a list that does not name exactly the row's
// items is refused rather than silently ignored.
func TestPark_MismatchedItemIDsRefused(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-xcheck", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-xcheck")

	// The row's single item is c-write; a list naming a real-but-different
	// call of the same batch is still the wrong decision.
	_, err = f.ag.ResumeChat(ctx, "s-xcheck", ParkDecision{
		RequestID: hr.ID, ItemIDs: []string{"c-read"}, Approved: true,
	}, nil)
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("mismatched item ids = %v, want ErrStaleDecision", err)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 0 {
		t.Fatalf("export_csv ran under a mismatched decision")
	}
}

// driftHook governs one tool: Allow until armed, Ask afterwards — a host gate
// that trips on budget, quota, or time of day while the turn waits. The
// library's own hooks are deterministic; the Hook interface is public and a
// host's need not be.
type driftHook struct {
	tool  string
	armed atomic.Bool
}

func (h *driftHook) Matches(r shuttle.AdmissionRequest) bool { return r.ToolName == h.tool }
func (h *driftHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	if h.armed.Load() {
		return shuttle.Decision{Kind: shuttle.Ask}
	}
	return shuttle.Decision{Kind: shuttle.Allow}
}

// TestPark_GrantDoesNotReachCallsOffTheCard pins the grant's blast radius. An
// approval answers the question it was asked: a call that was not a park item
// never appeared on the human's card, so it may not borrow that approval when
// its own verdict has since become Ask.
func TestPark_GrantDoesNotReachCallsOffTheCard(t *testing.T) {
	drift := &driftHook{tool: "read_table"}
	f := newParkFixture(t,
		[]shuttle.Hook{scopedAskHook{tool: "export_csv"}, drift},
		twoCallBatch(), "read_table", "export_csv")
	ctx := context.Background()

	// read_table preflights Allow, so only export_csv is a card item.
	_, err := f.ag.Chat(ctx, "s-drift", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-drift")
	items := paramKeys(hr)
	if len(items) != 1 || items[0] != "c-write" {
		t.Fatalf("card items = %v, want only [c-write]", items)
	}

	// The gate trips while the human deliberates.
	drift.armed.Store(true)

	if _, err := f.ag.ResumeChat(ctx, "s-drift", ParkDecision{
		RequestID: hr.ID, ItemIDs: items, Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}

	if got := f.tools["read_table"].runs.Load(); got != 0 {
		t.Errorf("read_table now requires its own approval and was never on the card, "+
			"yet it ran %d time(s) under the batch grant", got)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 1 {
		t.Errorf("export_csv (the approved item) ran %d times, want 1", got)
	}
}

// TestPark_ExpiredDecisionIsRefusedNotExecuted — an approval that lands after
// the row's window has closed is applied as a refusal, and the row is closed
// through the one path allowed to close past expiry, so the session survives.
func TestPark_ExpiredDecisionIsRefusedNotExecuted(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "c-write", Name: "export_csv", Input: map[string]interface{}{"v": "w"}},
		}},
		{content: "replanned after timeout"},
		{content: "later turn"},
	}
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, responses, "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-exp", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-exp")

	// Backdate the window: the human took too long.
	hr.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.store.Update(ctx, hr); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := f.ag.ResumeChat(ctx, "s-exp", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil); err != nil {
		t.Fatalf("ResumeChat: %v", err)
	}
	if got := f.tools["export_csv"].runs.Load(); got != 0 {
		t.Fatalf("export_csv ran %d times on a lapsed approval", got)
	}
	after, _ := f.store.Get(ctx, hr.ID)
	if after == nil || after.Status != "timeout" {
		t.Fatalf("expired request status = %v, want timeout", after)
	}
	if _, err := f.ag.Chat(ctx, "s-exp", "carry on"); err != nil {
		t.Fatalf("turn after an expired park refused: %v", err)
	}
}

// TestResumeChat_RefusedWithoutParkWiring — the entry point is closed on an
// agent that never enabled park, so it can never complete a rowless batch
// under a grant and bypass whatever approval path that agent does have.
func TestResumeChat_RefusedWithoutParkWiring(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: []mockLLMResponse{{content: "hi"}}},
		WithConfig(cfg))

	if _, err := ag.ResumeChat(context.Background(), "s-nopark",
		ParkDecision{RequestID: "x", Approved: true}, nil); !errors.Is(err, ErrParkDisabled) {
		t.Fatalf("ResumeChat without park = %v, want ErrParkDisabled", err)
	}
}

// TestPark_RequiresDurableSessionStore — Memory's Persist* methods are silent
// no-ops without a store, so "persisted without error" is not the same as
// durable. Park must not raise a durable request row against a batch that
// lives only in RAM; without a store the batch dispatches inline instead,
// where a govened call fails closed with no resolver wired.
func TestPark_RequiresDurableSessionStore(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	store := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: twoCallBatch()},
		WithConfig(cfg),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "export_csv"}}, nil, nil)),
	) // NOTE: no WithMemory(NewMemoryWithStore(...)) — storeless on purpose.
	read := &countingTool{name: "read_table"}
	write := &countingTool{name: "export_csv"}
	ag.RegisterTool(read)
	ag.RegisterTool(write)

	ctx := context.Background()
	_, err := ag.Chat(ctx, "s-nostore", "go")
	var parked *TurnParkedError
	if errors.As(err, &parked) {
		t.Fatalf("parked against a non-durable batch")
	}
	if reqs, _ := store.ListBySession(ctx, "s-nostore"); len(reqs) != 0 {
		t.Fatalf("raised %d request row(s) for a batch that was never stored", len(reqs))
	}
	// Fail-closed inline: the ask has no resolver, so the body never runs.
	if got := write.runs.Load(); got != 0 {
		t.Fatalf("export_csv ran %d times with no approval path", got)
	}
}

// TestPark_EmbedderDecidedRowResumes pins the embedder-recorded flow: the
// embedder's respond door decides the row FIRST (its own expiry CAS applies),
// then resumes with a decision derived from the row. The decided row must
// load, the row's status must be authoritative over the caller's payload, and
// ResumeChat must not try to re-close a row that is already closed.
func TestPark_EmbedderDecidedRowResumes(t *testing.T) {
	ctx := context.Background()

	t.Run("approved row executes the batch", func(t *testing.T) {
		f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
			"read_table", "export_csv")
		_, err := f.ag.Chat(ctx, "s-embed-a", "go")
		var parked *TurnParkedError
		if !errors.As(err, &parked) {
			t.Fatalf("expected park, got %v", err)
		}
		hr := f.pendingParked(t, "s-embed-a")
		// The embedder decides the row before resuming — cloud's respond door.
		if rerr := f.store.RespondToRequest(ctx, hr.ID, "approved", "", "user-1", nil); rerr != nil {
			t.Fatalf("RespondToRequest: %v", rerr)
		}
		resp, err := f.ag.ResumeChat(ctx, "s-embed-a", ParkDecision{
			RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		}, nil)
		if err != nil {
			t.Fatalf("ResumeChat on decided row: %v", err)
		}
		if resp.Content != "continued" {
			t.Fatalf("content = %q", resp.Content)
		}
		if got := f.tools["export_csv"].runs.Load(); got != 1 {
			t.Fatalf("export_csv ran %d times, want 1", got)
		}
		// The row keeps the embedder's verdict — ResumeChat did not touch it.
		after, _ := f.store.Get(ctx, hr.ID)
		if after == nil || after.Status != "approved" {
			t.Fatalf("row status after resume = %+v, want approved", after)
		}
	})

	t.Run("rejected row refuses even an approving payload", func(t *testing.T) {
		f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
			"read_table", "export_csv")
		_, err := f.ag.Chat(ctx, "s-embed-r", "go")
		var parked *TurnParkedError
		if !errors.As(err, &parked) {
			t.Fatalf("expected park, got %v", err)
		}
		hr := f.pendingParked(t, "s-embed-r")
		if rerr := f.store.RespondToRequest(ctx, hr.ID, "rejected", "wrong table", "user-1", nil); rerr != nil {
			t.Fatalf("RespondToRequest: %v", rerr)
		}
		// Hostile/buggy caller says Approved — the row's verdict wins.
		if _, err := f.ag.ResumeChat(ctx, "s-embed-r", ParkDecision{
			RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		}, nil); err != nil {
			t.Fatalf("ResumeChat: %v", err)
		}
		if got := f.tools["export_csv"].runs.Load(); got != 0 {
			t.Fatalf("export_csv ran %d times against a rejected row", got)
		}
	})
}

// TestPark_AppliedBatchWithoutFinalReplyResumesToCompletion — the crash
// window between the batch's last tool row and the continuation's final
// reply. All rows exist, no final reply: the retry must RE-ENTER the loop so
// the model sees the results and finishes the turn — refusing it as
// "already complete" would strand the turn's answer permanently — and must
// not re-execute any tool body.
func TestPark_AppliedBatchWithoutFinalReplyResumesToCompletion(t *testing.T) {
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
		"read_table", "export_csv")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-halfdone", "go")
	var parked *TurnParkedError
	if !errors.As(err, &parked) {
		t.Fatalf("expected park, got %v", err)
	}
	hr := f.pendingParked(t, "s-halfdone")

	// Simulate the crash window: every batch call already has its tool row
	// (persisted before the death), but the continuation never ran.
	sess := f.ag.memory.GetOrCreateSessionWithAgent(ctx, "s-halfdone", f.ag.config.Name, "")
	for _, callID := range []string{"c-read", "c-write"} {
		f.ag.appendMessage(ctx, sess, Message{
			Role: "tool", Content: "ran", ToolUseID: callID,
			ToolResult: &shuttle.Result{Success: true, Data: "ran"},
			AgentID:    f.ag.id, Timestamp: time.Now(),
		}, false)
	}

	resp, err := f.ag.ResumeChat(ctx, "s-halfdone", ParkDecision{
		RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
	}, nil)
	if err != nil {
		t.Fatalf("resume of an applied-but-unfinished turn refused: %v", err)
	}
	if resp.Content != "continued" {
		t.Fatalf("content = %q, want the turn's answer", resp.Content)
	}
	// Recovery re-enters the loop; it never re-runs tool bodies.
	if got := f.tools["export_csv"].runs.Load(); got != 0 {
		t.Fatalf("export_csv re-ran %d times during recovery", got)
	}
	if got := f.tools["read_table"].runs.Load(); got != 0 {
		t.Fatalf("read_table re-ran %d times during recovery", got)
	}
}

// TestPark_CallIDCollisionCannotReplayAcrossBatches — LLM-assigned call IDs
// can collide across batches (per-response counters). A redelivered decision
// for request A whose item IDs happen to exist in nested batch B must be
// refused by CONTENT binding (tool/seq), never applied by ID coincidence.
func TestPark_CallIDCollisionCannotReplayAcrossBatches(t *testing.T) {
	responses := []mockLLMResponse{
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "call_0", Name: "export_csv", Input: map[string]interface{}{"v": "a"}},
		}},
		{content: "", toolCalls: []llmtypes.ToolCall{
			{ID: "call_0", Name: "drop_table", Input: map[string]interface{}{"v": "b"}},
		}},
		{content: "done"},
	}
	f := newParkFixture(t,
		[]shuttle.Hook{scopedAskHook{tool: "export_csv"}, scopedAskHook{tool: "drop_table"}},
		responses, "export_csv", "drop_table")
	ctx := context.Background()

	_, err := f.ag.Chat(ctx, "s-collide", "go")
	var parkedA *TurnParkedError
	if !errors.As(err, &parkedA) {
		t.Fatalf("expected park on batch A, got %v", err)
	}
	hrA := f.pendingParked(t, "s-collide")

	_, err = f.ag.ResumeChat(ctx, "s-collide", ParkDecision{
		RequestID: hrA.ID, ItemIDs: paramKeys(hrA), Approved: true,
	}, nil)
	var parkedB *TurnParkedError
	if !errors.As(err, &parkedB) {
		t.Fatalf("expected nested park on batch B, got %v", err)
	}

	// Redeliver A's decision: its item id "call_0" EXISTS in B's batch, but
	// it describes export_csv — not B's drop_table.
	_, err = f.ag.ResumeChat(ctx, "s-collide", ParkDecision{
		RequestID: hrA.ID, ItemIDs: paramKeys(hrA), Approved: true,
	}, nil)
	if !errors.Is(err, ErrStaleDecision) {
		t.Fatalf("colliding redelivery = %v, want ErrStaleDecision", err)
	}
	if got := f.tools["drop_table"].runs.Load(); got != 0 {
		t.Fatalf("drop_table ran %d times under request A's redelivered decision", got)
	}
}

// TestPark_HandleContinuityAcrossTheGap — same-process lifecycle: the parked
// turn's handle collector survives the park (chat's first park AND a nested
// re-park) and is adopted by the resume, so handles minted before the park
// are never released mid-turn; the exit that finishes the turn drains the
// slot. Pooled embedders instead drain explicitly via ReleaseParkedHandles.
func TestPark_HandleContinuityAcrossTheGap(t *testing.T) {
	ctx := context.Background()

	parkedCollector := func(f *parkFixture, sessionID string) bool {
		f.ag.parkedHandlesMu.Lock()
		defer f.ag.parkedHandlesMu.Unlock()
		return f.ag.parkedHandles[sessionID] != nil
	}

	t.Run("first park parks the collector; the finishing resume drains it", func(t *testing.T) {
		f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
			"read_table", "export_csv")
		_, err := f.ag.Chat(ctx, "s-handles", "go")
		var parked *TurnParkedError
		if !errors.As(err, &parked) {
			t.Fatalf("expected park, got %v", err)
		}
		if !parkedCollector(f, "s-handles") {
			t.Fatalf("first park released the collector instead of parking it")
		}
		hr := f.pendingParked(t, "s-handles")
		if _, err := f.ag.ResumeChat(ctx, "s-handles", ParkDecision{
			RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		}, nil); err != nil {
			t.Fatalf("ResumeChat: %v", err)
		}
		if parkedCollector(f, "s-handles") {
			t.Fatalf("finished turn left a parked collector behind")
		}
	})

	t.Run("a nested re-park keeps the slot; the second resume drains it", func(t *testing.T) {
		responses := []mockLLMResponse{
			{content: "", toolCalls: []llmtypes.ToolCall{
				{ID: "a-1", Name: "export_csv", Input: map[string]interface{}{"v": "a"}},
			}},
			{content: "", toolCalls: []llmtypes.ToolCall{
				{ID: "b-1", Name: "drop_table", Input: map[string]interface{}{"v": "b"}},
			}},
			{content: "done"},
		}
		f := newParkFixture(t,
			[]shuttle.Hook{scopedAskHook{tool: "export_csv"}, scopedAskHook{tool: "drop_table"}},
			responses, "export_csv", "drop_table")
		_, err := f.ag.Chat(ctx, "s-nested-h", "go")
		var parkedA *TurnParkedError
		if !errors.As(err, &parkedA) {
			t.Fatalf("expected park A, got %v", err)
		}
		hrA := f.pendingParked(t, "s-nested-h")
		_, err = f.ag.ResumeChat(ctx, "s-nested-h", ParkDecision{
			RequestID: hrA.ID, ItemIDs: paramKeys(hrA), Approved: true,
		}, nil)
		var parkedB *TurnParkedError
		if !errors.As(err, &parkedB) {
			t.Fatalf("expected nested park B, got %v", err)
		}
		if !parkedCollector(f, "s-nested-h") {
			t.Fatalf("nested re-park dropped the collector")
		}
		hrB := f.pendingParked(t, "s-nested-h")
		if _, err := f.ag.ResumeChat(ctx, "s-nested-h", ParkDecision{
			RequestID: hrB.ID, ItemIDs: paramKeys(hrB), Approved: true,
		}, nil); err != nil {
			t.Fatalf("resume B: %v", err)
		}
		if parkedCollector(f, "s-nested-h") {
			t.Fatalf("finished turn left a parked collector behind")
		}
	})

	t.Run("ReleaseParkedHandles drains the slot for pooled embedders", func(t *testing.T) {
		f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "export_csv"}}, twoCallBatch(),
			"read_table", "export_csv")
		if _, err := f.ag.Chat(ctx, "s-pooled", "go"); err == nil {
			t.Fatalf("expected park")
		}
		if !parkedCollector(f, "s-pooled") {
			t.Fatalf("park did not park the collector")
		}
		f.ag.ReleaseParkedHandles("s-pooled")
		if parkedCollector(f, "s-pooled") {
			t.Fatalf("ReleaseParkedHandles left the slot occupied")
		}
		// Idempotent: draining an empty slot is a no-op.
		f.ag.ReleaseParkedHandles("s-pooled")
		// The resume still works with a fresh collector.
		hr := f.pendingParked(t, "s-pooled")
		if _, err := f.ag.ResumeChat(ctx, "s-pooled", ParkDecision{
			RequestID: hr.ID, ItemIDs: paramKeys(hr), Approved: true,
		}, nil); err != nil {
			t.Fatalf("ResumeChat after explicit drain: %v", err)
		}
	})
}
