// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"go.uber.org/zap"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

const (
	classNew      = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW
	classInFlight = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT
	classHolder   = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER
)

func newTest(t *testing.T, cfg Config) *Scheduler {
	t.Helper()
	s := New("test|"+t.Name(), cfg)
	t.Cleanup(s.Close)
	return s
}

func TestAcquireGrantsWithinBudget(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 10000})
	g, err := s.Acquire(context.Background(), Request{ReservationTokens: 100, Class: classNew})
	require.NoError(t, err)
	require.NotNil(t, g)
	st := s.State()
	assert.Equal(t, int64(100), st.ReservedTokensOutstanding)
	g.Release(40)
	st = s.State()
	assert.Equal(t, int64(0), st.ReservedTokensOutstanding)
	assert.Equal(t, int64(1), st.GrantsTotal)
}

// The core contract: waiting for capacity NEVER produces an error of the
// scheduler's own — the waiter parks and is woken by completion churn.
func TestWaitingIsAStateNotAnError(t *testing.T) {
	// Budget is 800 (1000 * 0.8 utilization); one 600-token reservation at a
	// time fits, two do not — the second must park, then wake on release.
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	first, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Class: classNew})
	require.NoError(t, err)

	got := make(chan *Grant, 1)
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Class: classNew})
		require.NoError(t, err)
		got <- g
	}()

	select {
	case <-got:
		t.Fatal("second acquire must park while the budget is reserved")
	case <-time.After(300 * time.Millisecond):
	}

	first.Release(1) // completion churn: reservation credited, tiny actual usage
	select {
	case g := <-got:
		g.Release(1)
	case <-time.After(5 * time.Second):
		t.Fatal("parked waiter was not woken by completion")
	}
}

func TestAcquireHonorsCallerContextOnly(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)
	defer hog.Release(0)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = s.Acquire(ctx, Request{ReservationTokens: 800})
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the only error a waiter can see is its own context's")
	assert.Empty(t, s.Waiters(), "cancelled waiter must not linger in the queue")
}

func TestPriorityOrderAcrossClasses(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: time.Hour, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	acquire := func(name string, class loomv1.SlotPriorityClass) {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 700, Class: class, ConversationID: name})
		require.NoError(t, err)
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		g.Release(1) // tiny actual usage keeps the window open for the next class
	}

	var wg sync.WaitGroup
	// Enqueue lowest class first so FIFO cannot accidentally produce the
	// expected order.
	for _, w := range []struct {
		name  string
		class loomv1.SlotPriorityClass
	}{{"new", classNew}, {"inflight", classInFlight}, {"holder", classHolder}} {
		wg.Add(1)
		go func(name string, class loomv1.SlotPriorityClass) {
			defer wg.Done()
			acquire(name, class)
		}(w.name, w.class)
		time.Sleep(100 * time.Millisecond) // deterministic enqueue order
	}

	hog.Release(1)
	wg.Wait()

	require.Len(t, order, 3)
	assert.Equal(t, []string{"holder", "inflight", "new"}, order,
		"dispatch must serve RESOURCE_HOLDER > IN_FLIGHT > NEW regardless of arrival order")
}

func TestNewArrivalCannotOvertakeParkedHigherClass(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: time.Hour, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)

	parked := make(chan *Grant, 1)
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 100, Class: classInFlight})
		require.NoError(t, err)
		parked <- g
	}()
	time.Sleep(100 * time.Millisecond)

	hog.Release(1)
	g := <-parked

	// Budget now has room, but a fresh NEW request must queue behind nothing —
	// there is room and no higher-class waiter, so it may pass. Re-park the
	// scope first to prove overtaking is refused while a higher class waits.
	hog2, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Class: classInFlight})
	require.NoError(t, err)

	waiting := make(chan *Grant, 1)
	go func() {
		w, err := s.Acquire(context.Background(), Request{ReservationTokens: 200, Class: classHolder})
		require.NoError(t, err)
		waiting <- w
	}()
	time.Sleep(100 * time.Millisecond)

	// A NEW arrival that WOULD fit must still park: a RESOURCE_HOLDER waits.
	overtake := make(chan struct{})
	go func() {
		w, err := s.Acquire(context.Background(), Request{ReservationTokens: 50, Class: classNew})
		require.NoError(t, err)
		close(overtake)
		w.Release(1)
	}()

	select {
	case <-overtake:
		t.Fatal("NEW request overtook a parked RESOURCE_HOLDER")
	case <-time.After(300 * time.Millisecond):
	}

	g.Release(1)
	hog2.Release(1)
	select {
	case w := <-waiting:
		w.Release(1)
	case <-time.After(5 * time.Second):
		t.Fatal("holder never granted")
	}
	select {
	case <-overtake:
	case <-time.After(5 * time.Second):
		t.Fatal("NEW waiter starved after higher classes drained")
	}
}

func TestAgingPromotesStarvedWaiters(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: 1500 * time.Millisecond, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)
	defer hog.Release(0)

	done := make(chan struct{})
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 100, Class: classNew})
		require.NoError(t, err)
		g.Release(1)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return s.State().PromotionsTotal >= 1
	}, 10*time.Second, 100*time.Millisecond, "starved NEW waiter must be promoted")

	ws := s.Waiters()
	require.Len(t, ws, 1)
	assert.GreaterOrEqual(t, ws[0].Promotions, int32(1), "the starvation tier must advance")
	assert.Equal(t, classNew, ws[0].Class,
		"aging must NOT move the waiter's class: class says what a request is, "+
			"and waiting longer does not turn a request into a lease holder — "+
			"precedence for a starved waiter comes from dispatch")
}

// TestStarvedWaiterOutranksHolderWithoutBecomingOne is the liveness half of
// that contract: a starved NEW waiter must still be served ahead of a
// freshly-arrived RESOURCE_HOLDER, so removing the cross-class promotion
// cannot starve lower classes behind a stream of holders.
//
// Reservations are sized so exactly ONE of the two waiters fits once the hog
// releases (budget 800; two 500s cannot both be admitted) — otherwise both
// are granted in the same dispatch pass and the assertion is a coin flip.
func TestStarvedWaiterOutranksHolderWithoutBecomingOne(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: 300 * time.Millisecond, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 700})
	require.NoError(t, err)

	// A NEW waiter parks first and ages past a starvation threshold.
	newDone := make(chan struct{})
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 500, Class: classNew})
		if err == nil {
			g.Release(1)
		}
		close(newDone)
	}()
	require.Eventually(t, func() bool { return s.State().PromotionsTotal >= 1 },
		5*time.Second, 20*time.Millisecond, "the NEW waiter must reach starvation")

	// A holder arrives afterwards — higher class, but it has not starved.
	holderDone := make(chan struct{})
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 500, Class: classHolder})
		if err == nil {
			g.Release(1)
		}
		close(holderDone)
	}()
	require.Eventually(t, func() bool { return s.State().ParkedRequests >= 2 },
		5*time.Second, 20*time.Millisecond, "both must be parked")

	// True up the hog's reservation (Release(0) would book the whole 700 and
	// keep the window full until rollover), freeing room for exactly one.
	hog.Release(1)

	select {
	case <-newDone:
		// The starved waiter went first, without ever changing class.
	case <-holderDone:
		t.Fatal("the freshly-arrived holder was served before the starved waiter")
	case <-time.After(5 * time.Second):
		t.Fatal("neither waiter was served")
	}
	<-holderDone
}

func TestReservationTrueUpFreesBudget(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	// Reserve the whole budget, but the call actually used almost nothing.
	g, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)
	g.Release(10) // true-up: only 10 tokens actually consumed

	// The freed reservation must admit the next request immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g2, err := s.Acquire(ctx, Request{ReservationTokens: 700})
	require.NoError(t, err, "true-up must return unused reservation to the window")
	g2.Release(1)
}

func TestUpdateFromHeadersCalibrates(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 100, InteractiveHeadroom: -1})
	// Azure's own fixture: a HEALTHY scope — abundant remaining quota, with
	// the informational "window resets in 30s" header every response carries.
	s.UpdateFromHeaders(1_500_000, 745_399, 30*time.Second)
	st := s.State()
	assert.Equal(t, int64(1_500_000), st.EffectiveTokensPerMinute)
	assert.Nil(t, st.NextWake,
		"an informational reset with abundant remaining must not gate admission")

	// Header calibration must open the budget for a large reservation the
	// static config would have parked forever — PROMPTLY, not after the 30s
	// reset: gating every healthy response on the reset header froze the
	// scope for the rest of its window per success.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g, err := s.Acquire(ctx, Request{ReservationTokens: 5_000})
	require.NoError(t, err, "a healthy calibrated scope must admit promptly")
	require.NotNil(t, g)
	g.Release(100)
}

// The inverse: when the provider says remaining is actually exhausted, the
// reset header IS the gate — admission stops until the window renews.
func TestUpdateFromHeadersExhaustedGatesUntilReset(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	s.UpdateFromHeaders(1000, 0, 400*time.Millisecond)
	st := s.State()
	require.NotNil(t, st.NextWake, "exhausted remaining must arm the reset wake")

	// Gated while the wake is pending.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := s.Acquire(ctx, Request{ReservationTokens: 10})
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"an exhausted window must refuse admission until the reset")

	// Admitted after the reset passes (the provider's window renewed, so
	// ours renews with it — the stale windowUsed must not keep refusing).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	g, err := s.Acquire(ctx2, Request{ReservationTokens: 10})
	require.NoError(t, err, "the scope must recover once the provider's window resets")
	g.Release(1)
}

func TestAIMD(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	s.ObserveThrottle(0)
	assert.Equal(t, int64(500), s.State().EffectiveTokensPerMinute, "throttle halves the ceiling")
	s.ObserveSuccess()
	assert.Equal(t, int64(525), s.State().EffectiveTokensPerMinute, "clean interval adds 5%")

	// Header calibration outranks AIMD growth.
	s.UpdateFromHeaders(1_000_000, -1, 0)
	s.ObserveSuccess()
	assert.Equal(t, int64(1_000_000), s.State().EffectiveTokensPerMinute)
}

// resetThrottleHoldOff forces the next ObserveThrottle to count as a NEW
// congestion event (tests are in-package precisely for knobs like this).
func resetThrottleHoldOff(s *Scheduler) {
	s.mu.Lock()
	s.decreaseHoldOffUntil = time.Time{}
	s.mu.Unlock()
}

// A 429 retry storm — every attempt of one logical call, times concurrent
// callers — is ONE congestion event: exactly one multiplicative decrease.
func TestThrottleStormOneDecreasePerCongestionEvent(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 100_000, InteractiveHeadroom: -1})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ObserveThrottle(0)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(50_000), s.State().EffectiveTokensPerMinute,
		"50 throttle reports within one hold-off must apply exactly one halving")
}

// Repeated congestion events can never push the ceiling below the floor that
// keeps the scope's smallest reservation admissible.
func TestThrottleFloorKeepsSmallestReservationAdmissible(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 100_000, InteractiveHeadroom: -1})
	g, err := s.Acquire(context.Background(), Request{ReservationTokens: 8_000})
	require.NoError(t, err)
	g.Cancel() // never ran: seeds minSeenReservation, window stays untouched

	for i := 0; i < 20; i++ {
		s.ObserveThrottle(0)
		resetThrottleHoldOff(s)
	}
	// floor = minSeenReservation / utilization + 1 = 8000/0.8 + 1.
	assert.Equal(t, int64(10_001), s.State().EffectiveTokensPerMinute,
		"the ceiling must stop at the smallest-reservation floor")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	g2, err := s.Acquire(ctx, Request{ReservationTokens: 8_000})
	require.NoError(t, err, "the floor exists precisely so this stays admissible")
	g2.Release(1)
}

// The structural anti-wedge guarantee: an idle scope (nothing reserved,
// window untouched) ALWAYS admits its head request, however far the ceiling
// has collapsed — a scope that admits nothing can never observe the success
// it needs to recover.
func TestWedgeImpossibleIdleScopeAlwaysAdmits(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	// Collapse the ceiling as far as repeated congestion events can push it.
	for i := 0; i < 30; i++ {
		s.ObserveThrottle(0)
		resetThrottleHoldOff(s)
	}
	require.LessOrEqual(t, s.State().EffectiveTokensPerMinute, int64(1),
		"with no reservations seen the ceiling collapses to the minimal floor")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g, err := s.Acquire(ctx, Request{ReservationTokens: 50_000})
	require.NoError(t, err, "an idle scope must admit its head regardless of budget")
	g.Cancel()

	g2, err := s.Acquire(ctx, Request{ReservationTokens: 50_000})
	require.NoError(t, err, "and again: the valve is structural, not one-shot")
	g2.Release(10)
}

// After a collapse, clean traffic must rebuild the ceiling (AIMD growth is
// the repair path for uncalibrated scopes).
func TestRecoveryAfterCollapse(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 100_000, InteractiveHeadroom: -1})
	for i := 0; i < 10; i++ {
		s.ObserveThrottle(0)
		resetThrottleHoldOff(s)
	}
	low := s.State().EffectiveTokensPerMinute
	for i := 0; i < 40; i++ {
		s.ObserveSuccess()
	}
	assert.Greater(t, s.State().EffectiveTokensPerMinute, low,
		"clean completions must grow a collapsed ceiling back")
}

// Cancelled grants (the call never ran) must not charge the window: a
// mass-cancel wave that charged full reservations would exhaust the budget
// with phantom usage.
func TestCancelledGrantChargesNoUsage(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	// Mass-cancel wave: 20 full-budget grants acquired and cancelled. If any
	// one of them charged its reservation, the window would be exhausted 20
	// times over and the final acquire below would park until rollover.
	for i := 0; i < 20; i++ {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
		require.NoError(t, err)
		g.Cancel()
	}
	st := s.State()
	assert.Equal(t, int64(0), st.ReservedTokensOutstanding)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g, err := s.Acquire(ctx, Request{ReservationTokens: 800})
	require.NoError(t, err, "the wave must leave windowUsed unchanged")
	g.Release(1)
}

// M2: once a top-class waiter has starved past its aging point, smaller
// lower-class arrivals must stop backfilling past it — otherwise a large
// reservation starves forever behind a continuous stream of small NEW work.
func TestStarvedTopClassHeadHaltsBackfill(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: 200 * time.Millisecond, InteractiveHeadroom: -1})
	hog1, err := s.Acquire(context.Background(), Request{ReservationTokens: 500})
	require.NoError(t, err)
	hog2, err := s.Acquire(context.Background(), Request{ReservationTokens: 200})
	require.NoError(t, err)

	// A large RESOURCE_HOLDER parks (500+200+400 > 800)...
	holderGranted := make(chan *Grant, 1)
	go func() {
		g, gerr := s.Acquire(context.Background(), Request{ReservationTokens: 400, Class: classHolder, ConversationID: "big-holder"})
		require.NoError(t, gerr)
		holderGranted <- g
	}()
	// ...and starves past its aging point at the top class.
	time.Sleep(300 * time.Millisecond)

	// A stream of small NEW arrivals that would individually fit.
	smallGranted := make(chan *Grant, 2)
	for i := 0; i < 2; i++ {
		go func() {
			g, gerr := s.Acquire(context.Background(), Request{ReservationTokens: 100, Class: classNew})
			require.NoError(t, gerr)
			smallGranted <- g
		}()
	}
	time.Sleep(100 * time.Millisecond) // let them park behind the holder

	// Free 200 tokens: the starved holder still does not fit (500+400 > 800)
	// and the smalls (500+100 <= 800) MUST NOT backfill past it.
	hog2.Release(1)
	select {
	case <-holderGranted:
		t.Fatal("holder cannot fit while hog1 holds 500")
	case g := <-smallGranted:
		g.Release(1)
		t.Fatal("small NEW arrival backfilled past a starved RESOURCE_HOLDER head")
	case <-time.After(400 * time.Millisecond):
	}

	// Drain the rest: the holder must be admitted within bounded churn.
	hog1.Release(1)
	select {
	case g := <-holderGranted:
		g.Release(1)
	case <-time.After(5 * time.Second):
		t.Fatal("starved holder never admitted after capacity drained")
	}
	for i := 0; i < 2; i++ {
		select {
		case g := <-smallGranted:
			g.Release(1)
		case <-time.After(5 * time.Second):
			t.Fatal("small waiters starved after the holder drained")
		}
	}
}

func TestConcurrentAcquireReleaseRace(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 60_000, StarvationAge: 100 * time.Millisecond, InteractiveHeadroom: -1})
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			class := classNew
			switch i % 3 {
			case 1:
				class = classInFlight
			case 2:
				class = classHolder
			}
			g, err := s.Acquire(context.Background(), Request{ReservationTokens: 500, Class: class})
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			g.Release(100)
		}(i)
	}
	wg.Wait()
	st := s.State()
	assert.Equal(t, int64(n), st.GrantsTotal)
	assert.Equal(t, int64(0), st.ReservedTokensOutstanding)
	assert.Equal(t, int32(0), st.ParkedRequests)
}

func TestCloseWakesParkedWaiters(t *testing.T) {
	s := New("test|close", Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)
	defer hog.Release(0)

	done := make(chan struct{})
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
		require.NoError(t, err)
		g.Release(0)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close must wake parked waiters, not strand them")
	}
}

// Acquire AFTER Close must not park on a scheduler nothing will ever wake:
// it grants immediately (shutdown semantics — pacing no longer matters), and
// the grant flows through the normal accounting.
func TestAcquireAfterCloseGrantsImmediately(t *testing.T) {
	s := New("test|closed-acquire", Config{TokensPerMinute: 1000, InteractiveHeadroom: -1})
	// Saturate the scope first so a non-closed scheduler WOULD park.
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 800})
	require.NoError(t, err)
	s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g, err := s.Acquire(ctx, Request{ReservationTokens: 800})
	require.NoError(t, err, "Acquire after Close must fail fast into a grant, never park forever")
	require.NotNil(t, g)
	g.Release(1)
	hog.Release(1)
	st := s.State()
	assert.Equal(t, int64(0), st.ReservedTokensOutstanding,
		"Close-time and post-Close grants must flow through grantLocked accounting")
	assert.Equal(t, int64(2), st.GrantsTotal)
}

// TestOperatorCeilingSurvivesHeaderCalibration pins the override contract: an
// operator sets a ceiling to drain a scope or to reproduce contention, and
// the provider's very next successful response must not silently undo it.
func TestOperatorCeilingSurvivesHeaderCalibration(t *testing.T) {
	s := New("scope", Config{})
	defer s.Close()

	s.SetConfig(600_000, 0, 0, 0)
	require.True(t, s.State().CeilingPinned, "an explicit ceiling is pinned")
	require.Equal(t, int64(600_000), s.State().EffectiveTokensPerMinute)

	// The provider says 1.5M on the next response — exactly what clobbered
	// the override before.
	s.UpdateFromHeaders(1_500_000, 1_499_000, 0)
	assert.Equal(t, int64(600_000), s.State().EffectiveTokensPerMinute,
		"header calibration must not move a pinned ceiling")

	// AIMD must not move it either, in either direction.
	s.ObserveThrottle(0)
	assert.Equal(t, int64(600_000), s.State().EffectiveTokensPerMinute,
		"a throttle must not halve a pinned ceiling")
	s.ObserveSuccess()
	assert.Equal(t, int64(600_000), s.State().EffectiveTokensPerMinute,
		"AIMD must not grow a pinned ceiling")

	// 0 releases the pin and hands the scope back to calibration.
	s.SetConfig(0, 0, 0, 0)
	assert.False(t, s.State().CeilingPinned)
	s.UpdateFromHeaders(1_500_000, 1_499_000, 0)
	assert.Equal(t, int64(1_500_000), s.State().EffectiveTokensPerMinute,
		"released scopes calibrate again")
}

// TestListWaitersAllScopes pins the empty-scope contract: an operator asking
// "who is parked?" without naming a scope must get every scope's waiters,
// matching GetSlotState. It returned nothing before, while the state view
// reported a non-zero parked count — a contradiction between two views of
// the same queue.
func TestListWaitersAllScopes(t *testing.T) {
	prev := Enabled()
	SetEnabled(true)
	defer SetEnabled(prev)

	reg := NewRegistry(zap.NewNop())
	defer reg.Close()
	svc := NewService(reg)

	// A tight ceiling plus one outstanding grant: the scope is no longer idle,
	// so the work-conserving escape valve (which always admits the head of an
	// idle scope) does not apply and the second request genuinely parks.
	sched := reg.For("scope-a", Config{TokensPerMinute: 10_000})
	sched.SetConfig(10_000, 0, 0, 0)

	held, err := sched.Acquire(context.Background(), Request{ConversationID: "holder", ReservationTokens: 9_000})
	require.NoError(t, err)
	require.NotNil(t, held)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = sched.Acquire(ctx, Request{ConversationID: "conv-1", AgentName: "agent-1", ReservationTokens: 9_000})
	}()
	require.Eventually(t, func() bool { return sched.State().ParkedRequests > 0 },
		2*time.Second, 10*time.Millisecond, "the request must park")

	all, err := svc.ListWaiters(context.Background(), &loomv1.ListWaitersRequest{})
	require.NoError(t, err)
	require.Len(t, all.Waiters, 1, "empty scope must list every scope's waiters")
	assert.Equal(t, "conv-1", all.Waiters[0].ConversationId, "the waiter must be attributable")
	assert.Equal(t, "agent-1", all.Waiters[0].AgentName)

	scoped, err := svc.ListWaiters(context.Background(), &loomv1.ListWaitersRequest{Scope: "scope-a"})
	require.NoError(t, err)
	assert.Len(t, scoped.Waiters, 1, "naming the scope still works")

	cancel()
	held.Release(0)
}
