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
package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// D-2 acceptance tests: a pending human request surfaces on the run's progress
// stream from BOTH origins (hook-held ask and contact_human) through the shared
// agent.NewProgressNotifier bridge, and its resolution reaches the held caller
// verbatim. Each test drives a real surface — the shuttle Executor's Execute for
// the ask origin, ContactHumanTool.Execute for the question origin — with a
// recording ProgressCallback installed in ctx and a scripted actor resolving the
// stored request (no model, no network). The bridge reads the callback from ctx
// (ProgressCallbackFromContext), so the emit is observed exactly as a live run
// would deliver it.

// progressRecorder captures every ProgressEvent delivered to the installed
// callback. Safe for the concurrent callers of AC7's two-hold turn.
type progressRecorder struct {
	mu     sync.Mutex
	events []types.ProgressEvent
}

// callback returns the ProgressCallback to install in ctx; it appends each event.
func (r *progressRecorder) callback() types.ProgressCallback {
	return func(e types.ProgressEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, e)
	}
}

// hitlEvents returns the recorded events that carry a HITL request on the
// human-in-the-loop stage — the pending-human cards a stream consumer renders.
func (r *progressRecorder) hitlEvents() []types.ProgressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []types.ProgressEvent
	for _, e := range r.events {
		if e.Stage == agent.StageHumanInTheLoop && e.HITLRequest != nil {
			out = append(out, e)
		}
	}
	return out
}

// waitForHITLEvents blocks until at least n HITL-stage events are recorded and
// returns them, failing the test if they do not arrive within the deadline.
func (r *progressRecorder) waitForHITLEvents(t *testing.T, n int) []types.ProgressEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ev := r.hitlEvents(); len(ev) >= n {
			return ev
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected at least %d HITL progress event(s); the pending hold did not surface on the stream", n)
	return nil
}

// askAllHook is an admission hook that asks for approval on every call, so the
// executor routes each Execute through the HITL ask resolver (the hook-held
// origin). It carries no policy of its own — it exists to force the ask path.
type askAllHook struct{}

func (askAllHook) Matches(shuttle.AdmissionRequest) bool { return true }
func (askAllHook) Evaluate(shuttle.AdmissionRequest) shuttle.Decision {
	return shuttle.Decision{Kind: shuttle.Ask}
}

// askEmitFixture wires an Executor whose sole admission hook asks for approval,
// resolved by the concrete NewHITLAskResolver over a real in-memory HITL store,
// with the agent.NewProgressNotifier bridge as the pending-emit collaborator. A
// recording ProgressCallback is installed in ctx so the emit is observable.
type askEmitFixture struct {
	exec  *shuttle.Executor
	store *shuttle.InMemoryHumanRequestStore
	rec   *progressRecorder
	tools map[string]*shuttle.MockTool
	ctx   context.Context
}

// newAskEmitFixture builds an askEmitFixture registering one MockTool per name.
// timeout bounds the resolver's blocking wait; poll is its store poll interval;
// sessionID/userID are the identities the raised approval request is scoped to.
func newAskEmitFixture(t *testing.T, sessionID, userID string, timeout, poll time.Duration, toolNames ...string) *askEmitFixture {
	t.Helper()

	reg := shuttle.NewRegistry()
	tools := make(map[string]*shuttle.MockTool, len(toolNames))
	for _, name := range toolNames {
		tool := &shuttle.MockTool{MockName: name}
		reg.Register(tool)
		tools[name] = tool
	}

	store := shuttle.NewInMemoryHumanRequestStore()
	rec := &progressRecorder{}

	exec := shuttle.NewExecutor(reg)
	exec.SetIdentityResolver(func(context.Context) string { return userID })
	exec.SetAdmissionChain(shuttle.NewChain(
		[]shuttle.Hook{askAllHook{}},
		nil,
		shuttle.NewHITLAskResolver(store, timeout, poll, agent.NewProgressNotifier()),
	))

	ctx := agent.ContextWithProgressCallback(
		session.WithSessionID(context.Background(), sessionID),
		rec.callback(),
	)

	return &askEmitFixture{exec: exec, store: store, rec: rec, tools: tools, ctx: ctx}
}

// waitForPending blocks until exactly one pending request is present in the store
// and returns it. Called on the test goroutine while the resolver blocks.
func waitForPending(t *testing.T, store *shuttle.InMemoryHumanRequestStore) *shuttle.HumanRequest {
	t.Helper()
	return waitForPendingN(t, store, 1)[0]
}

// waitForPendingN blocks until at least n pending requests are present and returns
// them, failing the test if they do not appear within the deadline.
func waitForPendingN(t *testing.T, store *shuttle.InMemoryHumanRequestStore, n int) []*shuttle.HumanRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := store.ListPending(context.Background())
		require.NoError(t, err)
		if len(pending) >= n {
			return pending
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d pending request(s) at the seam; none appeared", n)
	return nil
}

// respondOnce resolves the first request whose stored tool matches toolName with
// the given status and note, mirroring the human review interface's
// RespondToRequest. A background actor — the model never self-resolves.
func respondOnce(store *shuttle.InMemoryHumanRequestStore, toolName, status, note string) {
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := store.ListPending(ctx)
			if err == nil {
				for _, hr := range pending {
					if toolName == "" || hr.Context["tool"] == toolName {
						_ = store.RespondToRequest(ctx, hr.ID, status, note, "reviewer@example.com", nil)
						return
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// AC1: when a hook-held call becomes pending, a StageHumanInTheLoop ProgressEvent
// carrying a populated HITLRequest — non-empty RequestID, Kind "approval", the
// stored Summary and ExpiresAt — is delivered to the installed callback. This
// proves the Notifier→ProgressEvent bridge lands the hook-held pending on the
// stream even though admit() itself installs no callback: the resolver fires the
// injected bridge, which reads the run's callback from ctx.
func TestPendingEmit_AskHold_SurfacesApprovalOnStream(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "needs_approval")

	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "needs_approval", map[string]interface{}{"k": "v"})
		done <- res
	}()

	hr := waitForPending(t, f.store)
	ev := f.rec.waitForHITLEvents(t, 1)[0]

	require.Equal(t, agent.StageHumanInTheLoop, ev.Stage)
	require.NotNil(t, ev.HITLRequest)
	require.NotEmpty(t, ev.HITLRequest.RequestID, "the emitted card carries the stored request id")
	require.Equal(t, hr.ID, ev.HITLRequest.RequestID)
	require.Equal(t, "approval", ev.HITLRequest.Kind)
	require.NotEmpty(t, ev.HITLRequest.Summary)
	require.Equal(t, hr.Summary, ev.HITLRequest.Summary)
	require.Equal(t, hr.ExpiresAt, ev.HITLRequest.ExpiresAt)

	// Release the blocked call so the driving goroutine exits.
	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC2: when a contact_human request becomes pending, its id-bearing event is
// delivered through the same bridge — routed from contact_human's post-Store
// Notify — so the HITLRequest carries a non-empty RequestID, the stored
// ExpiresAt, Kind "question", and Summary = the question text. The non-empty
// RequestID is what distinguishes this authoritative event from the agent loop's
// id-less pre-creation ping (which a stream consumer ignores).
func TestPendingEmit_ContactHumanHold_SurfacesQuestionOnStream(t *testing.T) {
	store := shuttle.NewInMemoryHumanRequestStore()
	rec := &progressRecorder{}
	tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
		Store:        store,
		Notifier:     agent.NewProgressNotifier(),
		Timeout:      2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})

	ctx := agent.ContextWithProgressCallback(
		session.WithSessionID(context.Background(), "sess-1"),
		rec.callback(),
	)

	const question = "Approve deleting table X?"
	done := make(chan *shuttle.Result, 1)
	go func() {
		res, _ := tool.Execute(ctx, map[string]interface{}{"question": question})
		done <- res
	}()

	hr := waitForPending(t, store)
	ev := rec.waitForHITLEvents(t, 1)[0]

	require.Equal(t, agent.StageHumanInTheLoop, ev.Stage)
	require.NotNil(t, ev.HITLRequest)
	require.NotEmpty(t, ev.HITLRequest.RequestID, "the id-bearing event distinguishes it from the id-less ping")
	require.Equal(t, hr.ID, ev.HITLRequest.RequestID)
	require.Equal(t, "question", ev.HITLRequest.Kind)
	require.Equal(t, question, ev.HITLRequest.Summary)
	require.Equal(t, hr.ExpiresAt, ev.HITLRequest.ExpiresAt)

	require.NoError(t, store.RespondToRequest(context.Background(), hr.ID, "responded", "yes", "reviewer@example.com", nil))
	<-done
}

// AC3: on approve, the executor decision is Allow and the held call body runs —
// the tool executes exactly once and returns its success Result.
func TestPendingEmit_Approve_AllowsAndRunsBody(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "needs_approval")

	respondOnce(f.store, "needs_approval", "approved", "")

	res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success)
	require.Nil(t, res.Error)
	require.Equal(t, 1, f.tools["needs_approval"].ExecuteCount, "an approved hold runs the body exactly once")
}

// AC4: on reject with a note, the executor decision is Deny whose reason is
// byte-identical to the stored note, and the body does not run.
func TestPendingEmit_RejectWithNote_DeniesWithVerbatimReason(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "needs_approval")

	const note = "denied: destructive op on prod not permitted"
	respondOnce(f.store, "needs_approval", "rejected", note)

	res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
	require.NoError(t, err, "a policy deny is a Result, not a transport error")
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, note, res.Error.Message, "the deny reason is the reviewer's note byte-for-byte")
	require.Equal(t, 0, f.tools["needs_approval"].ExecuteCount, "a rejected hold never runs the body")
}

// AC5: on answer, the Result returned into the agent loop carries the stored
// answer byte-identically in Data["response"] — the return path summarizes
// nothing.
func TestPendingEmit_Answer_ReturnsVerbatimResponse(t *testing.T) {
	store := shuttle.NewInMemoryHumanRequestStore()
	tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
		Store:        store,
		Notifier:     agent.NewProgressNotifier(),
		Timeout:      2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})
	ctx := agent.ContextWithProgressCallback(
		session.WithSessionID(context.Background(), "sess-1"),
		(&progressRecorder{}).callback(),
	)

	const answer = "Use the staging replica, not prod."
	respondOnce(store, "", "responded", answer)

	res, err := tool.Execute(ctx, map[string]interface{}{"question": "Which database should I query?"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success)
	data, ok := res.Data.(map[string]interface{})
	require.True(t, ok, "contact_human returns its answer in a map Data payload")
	require.Equal(t, answer, data["response"], "the answer reaches the loop byte-for-byte")
}

// AC6: with no resolution past ExpiresAt, the executor decision is Deny (timeout)
// and the body does not run — the in-process waiter fails closed at expiry.
func TestPendingEmit_NoResolutionPastExpiry_DeniesTimeout(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 60*time.Millisecond, 10*time.Millisecond, "needs_approval")

	res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Contains(t, res.Error.Message, "timed out", "an unresolved hold denies on the timeout path")
	require.Equal(t, 0, f.tools["needs_approval"].ExecuteCount, "an unresolved hold never runs the body")
}

// AC7: two scripted holds in one turn emit two distinct RequestIDs, each resolved
// independently — approving one and rejecting the other yields the two outcomes
// with no cross-effect. Proves each hold owns its store row and its emitted card.
func TestPendingEmit_TwoHolds_DistinctIDsResolvedIndependently(t *testing.T) {
	f := newAskEmitFixture(t, "sess-1", "user-1", 2*time.Second, 10*time.Millisecond, "op_a", "op_b")

	resA := make(chan *shuttle.Result, 1)
	resB := make(chan *shuttle.Result, 1)
	go func() { r, _ := f.exec.Execute(f.ctx, "op_a", nil); resA <- r }()
	go func() { r, _ := f.exec.Execute(f.ctx, "op_b", nil); resB <- r }()

	// Both holds are pending with distinct request ids, and each has surfaced its
	// own card on the stream.
	pending := waitForPendingN(t, f.store, 2)
	require.NotEqual(t, pending[0].ID, pending[1].ID, "each hold mints its own request id")

	events := f.rec.waitForHITLEvents(t, 2)
	ids := map[string]bool{}
	for _, e := range events {
		ids[e.HITLRequest.RequestID] = true
	}
	require.Len(t, ids, 2, "two holds emit two distinct RequestIDs on the stream")

	// Resolve each hold by its own tool: op_a approved, op_b rejected.
	respondOnce(f.store, "op_a", "approved", "")
	respondOnce(f.store, "op_b", "rejected", "no")

	a := <-resA
	b := <-resB
	require.True(t, a.Success, "the approved hold runs and succeeds")
	require.Equal(t, 1, f.tools["op_a"].ExecuteCount)
	require.False(t, b.Success, "the rejected hold is denied")
	require.Equal(t, "permission_denied", b.Error.Code)
	require.Equal(t, 0, f.tools["op_b"].ExecuteCount, "rejecting one hold leaves the other's outcome untouched")
}
