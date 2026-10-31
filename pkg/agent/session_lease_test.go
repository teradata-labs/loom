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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// leaseScriptLog is a recordingEventLog whose RenewSessionLease result is
// scripted, to drive the lease-lost paths.
type leaseScriptLog struct {
	recordingEventLog
	renewHeld atomic.Bool
	renewErr  atomic.Value // error
	// renewHangs makes a renewal block until its context ends, like a database
	// call that never returns.
	renewHangs atomic.Bool
	renewals   atomic.Int32
	released   atomic.Int32
}

func newLeaseScriptLog() *leaseScriptLog {
	l := &leaseScriptLog{}
	l.renewHeld.Store(true)
	return l
}

func (l *leaseScriptLog) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	l.renewals.Add(1)
	if l.renewHangs.Load() {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if v := l.renewErr.Load(); v != nil {
		if err, _ := v.(error); err != nil {
			return false, err
		}
	}
	return l.renewHeld.Load(), nil
}

func (l *leaseScriptLog) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	l.released.Add(1)
	return l.recordingEventLog.ReleaseSessionLease(ctx, sessionID, holder)
}

func leaseTestAgent(store *leaseScriptLog, ttl time.Duration) *Agent {
	a := &Agent{eventLog: store, leaseTTL: ttl}
	return a
}

func TestHoldSessionLease_NoEventLogIsNoOp(t *testing.T) {
	a := &Agent{}
	ctx := context.Background()
	got, release, err := a.holdSessionLease(ctx, "s")
	require.NoError(t, err)
	require.Equal(t, ctx, got)
	release()
}

func TestHoldSessionLease_BusyWhenHeldElsewhere(t *testing.T) {
	store := newLeaseScriptLog()
	won, err := store.AcquireSessionLease(context.Background(), "s", "other-process", time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	_, _, err = leaseTestAgent(store, time.Minute).holdSessionLease(context.Background(), "s")
	require.ErrorIs(t, err, ErrSessionBusy)
}

func TestHoldSessionLease_RenewsWhileHeldAndReleases(t *testing.T) {
	store := newLeaseScriptLog()
	a := leaseTestAgent(store, 60*time.Millisecond)

	ctx, release, err := a.holdSessionLease(context.Background(), "s")
	require.NoError(t, err)
	time.Sleep(150 * time.Millisecond)
	require.NoError(t, ctx.Err(), "a renewed lease must keep the turn running")
	require.GreaterOrEqual(t, store.renewals.Load(), int32(2))

	release()
	require.EqualValues(t, 1, store.released.Load())
	won, err := store.AcquireSessionLease(context.Background(), "s", "next", time.Minute)
	require.NoError(t, err)
	require.True(t, won, "release must free the lease for the next holder")
}

func TestHoldSessionLease_LostLeaseCancelsTurn(t *testing.T) {
	store := newLeaseScriptLog()
	store.renewHeld.Store(false) // another process took the lease over
	a := leaseTestAgent(store, 30*time.Millisecond)

	ctx, release, err := a.holdSessionLease(context.Background(), "s")
	require.NoError(t, err)
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("turn context still live after the lease was lost")
	}
	require.ErrorIs(t, context.Cause(ctx), ErrSessionLeaseLost)
}

func TestHoldSessionLease_PersistentRenewalErrorCancelsTurn(t *testing.T) {
	store := newLeaseScriptLog()
	store.renewErr.Store(errors.New("database unavailable"))
	a := leaseTestAgent(store, 30*time.Millisecond)

	ctx, release, err := a.holdSessionLease(context.Background(), "s")
	require.NoError(t, err)
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("turn context still live although renewal kept failing")
	}
	require.ErrorIs(t, context.Cause(ctx), ErrSessionLeaseLost)
}

// Review round 2, B7: a renewal that never returns must not leave the turn
// running past its lease. Each renewal has a deadline, and the turn is
// cancelled once the lease can no longer be vouched for.
func TestHoldSessionLease_HangingRenewalCancelsTurn(t *testing.T) {
	store := newLeaseScriptLog()
	store.renewHangs.Store(true)
	a := leaseTestAgent(store, 90*time.Millisecond)

	ctx, release, err := a.holdSessionLease(context.Background(), "s")
	require.NoError(t, err)
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("turn context still live although renewal never returned")
	}
	require.ErrorIs(t, context.Cause(ctx), ErrSessionLeaseLost)
}

// Concurrent holders on one session: exactly one gets the lease.
func TestHoldSessionLease_ConcurrentEntriesOneWinner(t *testing.T) {
	store := newLeaseScriptLog()
	a := leaseTestAgent(store, time.Minute)

	const n = 8
	var wg sync.WaitGroup
	var winners atomic.Int32
	releases := make(chan func(), n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := a.holdSessionLease(context.Background(), "s")
			if err == nil {
				winners.Add(1)
				releases <- release
			}
		}()
	}
	wg.Wait()
	close(releases)
	for r := range releases {
		r()
	}
	require.EqualValues(t, 1, winners.Load())
}

// leaseProbeTool counts its executions and optionally sleeps, so a test can
// lose the session lease while it runs.
type leaseProbeTool struct {
	name  string
	sleep time.Duration
	// onStart, when set, runs as the call begins, before it sleeps.
	onStart func()
	calls   atomic.Int32
}

func (p *leaseProbeTool) Name() string        { return p.name }
func (p *leaseProbeTool) Description() string { return "lease probe" }
func (p *leaseProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (p *leaseProbeTool) Backend() string { return "" }
func (p *leaseProbeTool) Execute(ctx context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	p.calls.Add(1)
	if p.onStart != nil {
		p.onStart()
	}
	time.Sleep(p.sleep)
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}

func leaseChatAgent(t *testing.T, store *leaseScriptLog, llm *mockToolCallingLLM, tools ...shuttle.Tool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(store))
	for _, tool := range tools {
		ag.tools.Register(tool)
	}
	return ag
}

// A Chat on a session whose lease another process holds must not run: the
// other process may be resuming the same turn.
func TestChat_SessionLeasedElsewhere_IsRefused(t *testing.T) {
	store := newLeaseScriptLog()
	_, err := store.AcquireSessionLease(context.Background(), "s-busy", "other-process", time.Minute)
	require.NoError(t, err)
	probe := &leaseProbeTool{name: "probe"}
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "probe", Input: map[string]interface{}{}}}},
	}}
	ag := leaseChatAgent(t, store, llm, probe)

	_, err = ag.Chat(context.Background(), "s-busy", "go")
	require.ErrorIs(t, err, ErrSessionBusy)
	require.Zero(t, probe.calls.Load(), "a refused turn ran a tool")
	require.Empty(t, store.AllRecordedEvents("s-busy"), "a refused turn recorded events")
}

// When the lease is lost during a tool call, the rest of the batch must not
// dispatch, and the turn reports the loss.
func TestChat_LeaseLostMidBatch_StopsDispatch(t *testing.T) {
	store := newLeaseScriptLog()
	// The takeover happens once the first call is running, so a slow runner
	// cannot lose the lease before that call dispatches.
	slow := &leaseProbeTool{name: "slow", sleep: 300 * time.Millisecond,
		onStart: func() { store.renewHeld.Store(false) }}
	next := &leaseProbeTool{name: "next"}
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c1", Name: "slow", Input: map[string]interface{}{}},
			{ID: "c2", Name: "next", Input: map[string]interface{}{}},
		}},
	}}
	ag := leaseChatAgent(t, store, llm, slow, next)
	ag.leaseTTL = 30 * time.Millisecond

	_, err := ag.Chat(context.Background(), "s-lost", "go")
	require.ErrorIs(t, err, ErrSessionLeaseLost)
	require.EqualValues(t, 1, slow.calls.Load())
	require.Zero(t, next.calls.Load(), "a call was dispatched after the lease was lost")
	for _, e := range store.AllRecordedEvents("s-lost") {
		require.False(t, e.EventType == eventlog.EventToolCallIntent && e.BatchPosition == 1,
			"an intent was recorded for a call after the lease was lost")
	}
}

// mixedRenewLog fails its first renewal fast and hangs every later one.
type mixedRenewLog struct {
	leaseScriptLog
	n atomic.Int32
}

func (l *mixedRenewLog) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	if l.n.Add(1) == 1 {
		return false, errors.New("injected: transient renewal error")
	}
	<-ctx.Done()
	return false, ctx.Err()
}

// Review round 3, B10: after a fast renewal failure, a later renewal that
// hangs must not carry the turn past the safety margin (ttl - ttl/3). Each
// renewal is bounded by the time left before the margin.
func TestHoldSessionLease_FastFailureThenHangCancelsBeforeMargin(t *testing.T) {
	store := &mixedRenewLog{leaseScriptLog: *newLeaseScriptLog()}
	ttl := 300 * time.Millisecond
	a := &Agent{eventLog: store, leaseTTL: ttl}

	start := time.Now()
	ctx, release, err := a.holdSessionLease(context.Background(), "s")
	require.NoError(t, err)
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("turn context still live although renewal kept failing")
	}
	require.ErrorIs(t, context.Cause(ctx), ErrSessionLeaseLost)
	margin := ttl - ttl/3
	require.Less(t, time.Since(start), margin+ttl/6,
		"turn cancelled at or past the safety margin %v", margin)
}

// blockingLLM blocks in Chat until its context ends, like a provider call in
// flight when the lease is lost.
type blockingLLM struct{ started chan struct{} }

func (l *blockingLLM) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	close(l.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (l *blockingLLM) Name() string  { return "blocking" }
func (l *blockingLLM) Model() string { return "blocking-v1" }

// Review round 3, B11: a lease lost while the LLM call is blocked comes back
// as ErrSessionLeaseLost, not as a bare context.Canceled, so the server maps
// it to ABORTED rather than CANCELED.
func TestChat_LeaseLostDuringLLMCall_ReportsLeaseLost(t *testing.T) {
	store := newLeaseScriptLog()
	llm := &blockingLLM{started: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(store))
	ag.leaseTTL = 30 * time.Millisecond
	go func() { <-llm.started; store.renewHeld.Store(false) }()

	_, err := ag.Chat(context.Background(), "s-llm-lost", "go")
	require.ErrorIs(t, err, ErrSessionLeaseLost)
	require.NotErrorIs(t, err, context.Canceled)
}

func TestLeaseLostError(t *testing.T) {
	lostCtx, cancel := context.WithCancelCause(context.Background())
	cancel(fmt.Errorf("%w: renewal failing", ErrSessionLeaseLost))
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	boom := errors.New("boom")

	require.NoError(t, leaseLostError(lostCtx, nil))
	require.ErrorIs(t, leaseLostError(lostCtx, context.Canceled), ErrSessionLeaseLost)
	require.Same(t, boom, leaseLostError(callerCtx, boom), "caller cancellation is left alone")
	require.ErrorIs(t, leaseLostError(callerCtx, context.Canceled), context.Canceled)
}
