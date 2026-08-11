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
package shuttle

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/session"
)

// D-7 acceptance tests for ask → HITL approval at the tool-admit seam. Each case
// drives the shuttle Executor's real Execute interface with an Ask-returning hook
// wired to the concrete NewHITLAskResolver over a real in-memory HITL store, and
// asserts the seam behavior a separate actor resolving the request produces: the
// pending approval request raised at the seam, and the terminal Result plus the
// tool's ExecuteCount after the request is approved, rejected, or left to time out.
//
// A separate actor resolves the request via the store's RespondToRequest (the SE-3
// poll-respond responder helper) — the model never self-approves.

// askFixture wires an Executor whose only admission hook returns Ask, resolved by
// the concrete hitlAskResolver over a real in-memory HITL store. The call context
// carries the session ID, and the identity resolver supplies the user ID, so the
// resolver scopes the raised request to both.
type askFixture struct {
	exec  *Executor
	tool  *MockTool
	store *InMemoryHumanRequestStore
	ctx   context.Context
}

// newAskFixture builds an askFixture. timeout bounds the resolver's blocking wait
// and poll is its store poll interval; sessionID/userID are the identities the
// raised approval request is scoped to.
func newAskFixture(t *testing.T, toolName, sessionID, userID string, timeout, poll time.Duration) askFixture {
	t.Helper()

	reg := NewRegistry()
	tool := &MockTool{MockName: toolName}
	reg.Register(tool)

	store := NewInMemoryHumanRequestStore()

	exec := NewExecutor(reg)
	exec.SetIdentityResolver(func(context.Context) string { return userID })
	exec.SetAdmissionChain(NewChain(
		[]Hook{fixedHook{decision: Decision{Kind: Ask}}},
		nil,
		// nil notifier: these seam tests assert the approve/reject/timeout
		// decision, not the pending-emit; a nil notifier disables the emit and
		// leaves the hold behavior unchanged.
		NewHITLAskResolver(store, timeout, poll, nil),
	))

	return askFixture{
		exec:  exec,
		tool:  tool,
		store: store,
		ctx:   session.WithSessionID(context.Background(), sessionID),
	}
}

// waitForPending blocks until exactly one pending request is present in the store
// and returns it, failing the test if none appears within the deadline. Called on
// the test goroutine while the resolver blocks in a separate goroutine.
func waitForPending(t *testing.T, store *InMemoryHumanRequestStore) *HumanRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := store.ListPending(context.Background())
		require.NoError(t, err)
		if len(pending) == 1 {
			return pending[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending approval request appeared at the seam")
	return nil
}

// respondOncePending is the SE-3 responder: a background actor that polls
// ListPending and resolves the first pending request with the given status,
// mirroring the human_tool poll-respond pattern (human_tool_test.go).
func respondOncePending(store *InMemoryHumanRequestStore, status string) {
	go func() {
		ctx := context.Background()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := store.ListPending(ctx)
			if err == nil && len(pending) > 0 {
				_ = store.RespondToRequest(ctx, pending[0].ID, status, "", "reviewer@example.com", nil)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// failingHumanStore is a HumanRequestStore whose Store always errors, exercising
// the resolver's fail-closed path when the approval request cannot be raised.
type failingHumanStore struct{}

func (failingHumanStore) Store(context.Context, *HumanRequest) error {
	return fmt.Errorf("hitl store unavailable")
}

func (failingHumanStore) ExpireRequest(context.Context, string, string) error {
	return fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) Get(context.Context, string) (*HumanRequest, error) {
	return nil, fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) Update(context.Context, *HumanRequest) error {
	return fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) ListPending(context.Context) ([]*HumanRequest, error) {
	return nil, fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) ListBySession(context.Context, string) ([]*HumanRequest, error) {
	return nil, fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) RespondToRequest(context.Context, string, string, string, string, map[string]interface{}) error {
	return fmt.Errorf("hitl store unavailable")
}
func (failingHumanStore) Close() error { return nil }

// AC1: on an ask decision, an approval HumanRequest is created at the seam and
// appears pending in the HITL store, scoped to the call's session and user, while
// the resolver blocks awaiting a response.
func TestAskHITL_AskRaisesPendingApprovalAtSeam(t *testing.T) {
	f := newAskFixture(t, "needs_approval", "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

	// Drive the gated call; it blocks in the resolver until the request resolves.
	done := make(chan *Result, 1)
	go func() {
		res, _ := f.exec.Execute(f.ctx, "needs_approval", map[string]interface{}{"k": "v"})
		done <- res
	}()

	// While the resolver blocks, exactly one pending "approval" request is present,
	// scoped to the call's session and user.
	hr := waitForPending(t, f.store)
	require.Equal(t, "approval", hr.RequestType)
	require.Equal(t, "pending", hr.Status)
	require.Equal(t, "sess-1", hr.SessionID)
	require.Equal(t, "user-1", hr.Context["user_id"])

	// Resolve so the blocked call returns and the driving goroutine exits.
	require.NoError(t, f.store.RespondToRequest(context.Background(), hr.ID, "approved", "", "reviewer@example.com", nil))
	<-done
}

// AC2: approved → the call is admitted and the tool runs.
func TestAskHITL_Approved_AdmitsAndRunsTool(t *testing.T) {
	f := newAskFixture(t, "needs_approval", "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

	respondOncePending(f.store, "approved")

	res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, res.Success)
	require.Nil(t, res.Error)
	require.Equal(t, 1, f.tool.ExecuteCount, "an approved call runs the tool exactly once")
}

// AC3: rejected → the call is denied and the tool does not run. Any non-"approved"
// terminal status resolves to Deny, mirroring the resolver's fail-closed default.
func TestAskHITL_NonApproved_DeniesAndSkipsTool(t *testing.T) {
	for _, status := range []string{"rejected", "timeout", "responded"} {
		t.Run(status, func(t *testing.T) {
			f := newAskFixture(t, "needs_approval", "sess-1", "user-1", 2*time.Second, 10*time.Millisecond)

			respondOncePending(f.store, status)

			res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
			require.NoError(t, err, "a policy deny is a Result, not a transport error")
			require.NotNil(t, res)
			require.False(t, res.Success)
			require.NotNil(t, res.Error)
			require.Equal(t, "permission_denied", res.Error.Code)
			require.Equal(t, 0, f.tool.ExecuteCount, "a non-approved response never runs the tool")
		})
	}
}

// AC4: no response within the configured timeout → deny, tool does not run
// (fail-closed). Guards C-005 edge "ask timeout → deny".
func TestAskHITL_Timeout_DeniesFailClosed(t *testing.T) {
	f := newAskFixture(t, "needs_approval", "sess-1", "user-1", 60*time.Millisecond, 10*time.Millisecond)

	res, err := f.exec.Execute(f.ctx, "needs_approval", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, 0, f.tool.ExecuteCount, "an unanswered request denies without running the tool")

	// The waiter closes the hold it abandons: the row's terminal state agrees
	// with the decision the model received — never a forever-pending ghost.
	pending, err := f.store.ListPending(context.Background())
	require.NoError(t, err)
	require.Empty(t, pending, "an expired hold is closed, not left pending")
	reqs, err := f.store.ListBySession(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "timeout", reqs[0].Status)
	require.Equal(t, "system:expiry", reqs[0].RespondedBy)
}

// AC4 (sibling fail-closed): a context canceled before a response denies and the
// tool does not run.
func TestAskHITL_ContextCanceled_DeniesFailClosed(t *testing.T) {
	f := newAskFixture(t, "needs_approval", "sess-1", "user-1", 5*time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan *Result, 1)
	go func() {
		res, _ := f.exec.Execute(ctx, "needs_approval", nil)
		done <- res
	}()

	// Cancel only after the request is raised, proving the resolver was blocking.
	waitForPending(t, f.store)
	cancel()

	res := <-done
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, 0, f.tool.ExecuteCount, "a canceled approval never runs the tool")
}

// AC4 (sibling fail-closed): a store that cannot raise the approval request denies
// and the tool does not run.
func TestAskHITL_StoreFailure_DeniesFailClosed(t *testing.T) {
	reg := NewRegistry()
	tool := &MockTool{MockName: "needs_approval"}
	reg.Register(tool)

	exec := NewExecutor(reg)
	exec.SetAdmissionChain(NewChain(
		[]Hook{fixedHook{decision: Decision{Kind: Ask}}},
		nil,
		// nil notifier: the store fails before any emit could fire; this case
		// only asserts the fail-closed deny.
		NewHITLAskResolver(failingHumanStore{}, time.Second, 10*time.Millisecond, nil),
	))

	res, err := exec.Execute(session.WithSessionID(context.Background(), "sess-1"), "needs_approval", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	require.Equal(t, "permission_denied", res.Error.Code)
	require.Equal(t, 0, tool.ExecuteCount, "a store that cannot raise the request never runs the tool")
}

// an absent row (the postgres (nil, nil) contract) denies after a
// bounded number of reads instead of nil-panicking or spinning.
func TestAskResolver_AbsentRow_FailsClosed(t *testing.T) {
	store := &absentRowStore{}
	r := NewHITLAskResolver(store, 5*time.Second, 5*time.Millisecond, nil)
	d := r.Resolve(AdmissionRequest{Ctx: context.Background(), ToolName: "x"}, Decision{Kind: Ask})
	require.Equal(t, Deny, d.Kind)
	require.Contains(t, d.Reason, "no longer exists")
}

// absentRowStore stores successfully, then reports the row absent as (nil, nil)
// — the postgres store's documented contract for a vanished row.
type absentRowStore struct{ InMemoryHumanRequestStore }

func (s *absentRowStore) Store(ctx context.Context, req *HumanRequest) error { return nil }
func (s *absentRowStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	return nil, nil
}

// a resolution landing in the final poll interval is honored: the
// give-up path re-reads before denying.
func TestAskResolver_LastIntervalApproval_Honored(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	notifier := &approveOnNotify{store: store, after: 40 * time.Millisecond}
	r := NewHITLAskResolver(store, 50*time.Millisecond, 30*time.Millisecond, notifier)
	d := r.Resolve(AdmissionRequest{Ctx: context.Background(), ToolName: "x", SessionID: "s"},
		Decision{Kind: Ask})
	require.Equal(t, Allow, d.Kind, "an approval that lands before the stored expiry must admit")
}

// approveOnNotify approves the request from a background goroutine shortly
// after it is raised — inside the final poll interval for the test's timings.
type approveOnNotify struct {
	store HumanRequestStore
	after time.Duration
}

func (n *approveOnNotify) Notify(ctx context.Context, req *HumanRequest) error {
	id := req.ID
	go func() {
		time.Sleep(n.after)
		_ = n.store.RespondToRequest(context.Background(), id, "approved", "", "human", nil)
	}()
	return nil
}

func TestAskResolver_CancelExit_ClosesAbandonedRow(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	r := NewHITLAskResolver(store, time.Hour, 10*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Decision, 1)
	go func() {
		done <- r.Resolve(AdmissionRequest{
			Ctx: ctx, ToolName: "execute_sql", SessionID: "s1",
			Params: map[string]interface{}{"stmt": "DROP TABLE t"},
		}, Decision{Kind: Ask, Reason: "gated"})
	}()

	// Wait until the row is raised, then cancel the turn.
	var id string
	require.Eventually(t, func() bool {
		pending, err := store.ListPending(context.Background())
		if err != nil || len(pending) == 0 {
			return false
		}
		id = pending[0].ID
		return true
	}, 5*time.Second, 5*time.Millisecond)
	cancel()

	d := <-done
	require.Equal(t, Deny, d.Kind)

	// The abandoned row is terminally closed — not pending, not approvable.
	require.Eventually(t, func() bool {
		hr, err := store.Get(context.Background(), id)
		return err == nil && hr.Status == "timeout"
	}, 5*time.Second, 5*time.Millisecond,
		"the cancel exit must close the row it abandons with a terminal write")
	hr, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "system:cancel", hr.RespondedBy)

	// A later approve is refused by the resolve CAS.
	require.NoError(t, store.RespondToRequest(context.Background(), id, "approved", "late", "human", nil))
	hr, err = store.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "timeout", hr.Status, "a closed row cannot be flipped to approved")
}

// raceApproveStore approves the row the moment expire()'s ExpireRequest is
// attempted, simulating an approve transaction landing between the final read
// and the CAS.
type raceApproveStore struct {
	*InMemoryHumanRequestStore
	mu      sync.Mutex
	approve func(id string)
	fired   bool
}

func (s *raceApproveStore) ExpireRequest(ctx context.Context, id, by string) error {
	s.mu.Lock()
	if !s.fired {
		s.fired = true
		s.mu.Unlock()
		s.approve(id) // the human's approve wins the race
	} else {
		s.mu.Unlock()
	}
	return s.InMemoryHumanRequestStore.ExpireRequest(ctx, id, by)
}

func TestAskResolver_ExpireHonorsRaceWinningApprove(t *testing.T) {
	inner := NewInMemoryHumanRequestStore()
	store := &raceApproveStore{InMemoryHumanRequestStore: inner}
	store.approve = func(id string) {
		// Lands the approve the way postgres can: an approve transaction that
		// OPENED before the expiry passes the CAS on transaction-start time
		// even when its write lands after expire()'s final read. The in-memory
		// CAS reads its clock under the mutex, so the postgres ordering is
		// simulated with a direct state write.
		hr, err := inner.Get(context.Background(), id)
		if err != nil || hr == nil {
			return
		}
		hr.Status = "approved"
		hr.Response = "yes"
		hr.RespondedBy = "anuj"
		_ = inner.Update(context.Background(), hr)
	}
	r := &hitlAskResolver{store: store, timeout: 150 * time.Millisecond, poll: 10 * time.Millisecond}

	d := r.Resolve(AdmissionRequest{
		Ctx: context.Background(), ToolName: "execute_sql", SessionID: "s1",
		Params: map[string]interface{}{"stmt": "DELETE FROM t"},
	}, Decision{Kind: Ask, Reason: "gated"})

	require.Equal(t, Allow, d.Kind,
		"an approve that lands before the terminal write must be honored — the persisted state and the returned decision cannot disagree")
	pending, err := inner.ListBySession(context.Background(), "s1")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "approved", pending[0].Status)
}

// flappingStore alternates (nil, nil) and a transient error on Get.
type flappingStore struct {
	*InMemoryHumanRequestStore
	mu    sync.Mutex
	reads int
}

func (s *flappingStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	s.mu.Lock()
	s.reads++
	n := s.reads
	s.mu.Unlock()
	if n%2 == 1 {
		return nil, nil // absent
	}
	return nil, fmt.Errorf("transient store error")
}

func (s *flappingStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func TestAskResolver_TransientErrorResetsAbsentCounter(t *testing.T) {
	store := &flappingStore{InMemoryHumanRequestStore: NewInMemoryHumanRequestStore()}
	r := &hitlAskResolver{store: store, timeout: time.Hour, poll: 5 * time.Millisecond}

	d := r.wait(context.Background(), "req-1", time.Now().Add(400*time.Millisecond))
	require.Equal(t, Deny, d.Kind)
	require.Equal(t, "approval timed out", d.Reason,
		"an alternating absent/error store must ride out the full hold — never 'no longer exists' after non-consecutive absences")
	require.GreaterOrEqual(t, store.readCount(), 6,
		"the waiter kept polling well past three total absences")
}

func TestAskResolver_ExpiryClampedToTurnDeadline(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	r := NewHITLAskResolver(store, time.Hour, 5*time.Millisecond, nil)

	deadline := time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	go func() {
		_ = r.Resolve(AdmissionRequest{
			Ctx: ctx, ToolName: "execute_sql", SessionID: "s1",
			Params: map[string]interface{}{"stmt": "DROP TABLE t"},
		}, Decision{Kind: Ask, Reason: "gated"})
	}()

	var hr *HumanRequest
	require.Eventually(t, func() bool {
		pending, err := store.ListPending(context.Background())
		if err != nil || len(pending) == 0 {
			return false
		}
		hr = pending[0]
		return true
	}, 5*time.Second, 5*time.Millisecond)
	cancel()

	require.False(t, hr.ExpiresAt.After(deadline),
		"the stored expiry must not outlive the turn deadline (window derived at hold time, not at build)")
	require.GreaterOrEqual(t, deadline.Sub(hr.ExpiresAt), askDeadlineMargin,
		"the margin is load-bearing: the fail-closed deny must travel back BEFORE the turn dies, so the expiry sits at least the margin inside the deadline")
	require.True(t, hr.ExpiresAt.After(time.Now().Add(-time.Second)),
		"the clamped window is still a real window")
}

// A turn too short to hold a call stores an already-dead window and denies at
// the first poll — the clamp has NO floor, because a floor pushing the expiry
// past the deadline would re-open the row-outlives-its-waiter gap.
func TestAskResolver_TooShortTurnDeniesInsteadOfOutliving(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	r := NewHITLAskResolver(store, time.Hour, 20*time.Millisecond, nil)

	deadline := time.Now().Add(1 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	d := r.Resolve(AdmissionRequest{
		Ctx: ctx, ToolName: "execute_sql", SessionID: "s1",
		Params: map[string]interface{}{"stmt": "DROP TABLE t"},
	}, Decision{Kind: Ask, Reason: "gated"})

	require.Equal(t, Deny, d.Kind)
	rows, err := store.ListBySession(context.Background(), "s1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.False(t, rows[0].ExpiresAt.After(deadline),
		"the floored branch must never store an expiry past the turn's own death")
	require.Equal(t, "timeout", rows[0].Status, "the abandoned row is terminally closed")
}

// ctxRecordingStore captures the context each terminal close arrives on, so
// the abandon write's two context properties are pinned: the caller's VALUES
// travel with it (the postgres store's tenant identity lives there) and the
// caller's CANCELLATION does not (the write must land after the turn died).
type ctxRecordingStore struct {
	*InMemoryHumanRequestStore
	mu          sync.Mutex
	expireValue any
	expireLive  bool
}

type ctxPinKey struct{}

func (s *ctxRecordingStore) ExpireRequest(ctx context.Context, id, by string) error {
	s.mu.Lock()
	s.expireValue = ctx.Value(ctxPinKey{})
	s.expireLive = ctx.Err() == nil
	s.mu.Unlock()
	return s.InMemoryHumanRequestStore.ExpireRequest(ctx, id, by)
}

func TestAskResolver_AbandonContextKeepsValuesDropsCancellation(t *testing.T) {
	store := &ctxRecordingStore{InMemoryHumanRequestStore: NewInMemoryHumanRequestStore()}
	r := NewHITLAskResolver(store, time.Hour, 10*time.Millisecond, nil)

	parent := context.WithValue(context.Background(), ctxPinKey{}, "tenant-42")
	ctx, cancel := context.WithCancel(parent)

	done := make(chan Decision, 1)
	go func() {
		done <- r.Resolve(AdmissionRequest{
			Ctx: ctx, ToolName: "execute_sql", SessionID: "s1",
			Params: map[string]interface{}{"stmt": "DROP TABLE t"},
		}, Decision{Kind: Ask, Reason: "gated"})
	}()
	require.Eventually(t, func() bool {
		pending, err := store.ListPending(context.Background())
		return err == nil && len(pending) == 1
	}, 5*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.expireValue != nil
	}, 5*time.Second, 5*time.Millisecond, "the abandon close must be attempted")
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, "tenant-42", store.expireValue,
		"the caller's values must travel with the detached write — on postgres the tenant identity lives there, and losing it makes the close match zero rows silently")
	require.True(t, store.expireLive,
		"the detached write must not carry the caller's cancellation — a canceled context would refuse the write the close exists to make")
}
