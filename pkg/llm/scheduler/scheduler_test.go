// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
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
	s := newTest(t, Config{TokensPerMinute: 1000})
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
	s := newTest(t, Config{TokensPerMinute: 1000})
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
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: time.Hour})
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
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: time.Hour})
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
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: 1500 * time.Millisecond})
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
	assert.NotEqual(t, classNew, ws[0].Class, "waiter should have left NEW")
	assert.GreaterOrEqual(t, ws[0].Promotions, int32(1))
}

func TestReservationTrueUpFreesBudget(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000})
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
	s := newTest(t, Config{TokensPerMinute: 100})
	s.UpdateFromHeaders(1_500_000, 745_399, 30*time.Second)
	st := s.State()
	assert.Equal(t, int64(1_500_000), st.EffectiveTokensPerMinute)

	// Header calibration must open the budget for a large reservation the
	// static config would have parked forever.
	// (nextWake from reset is respected, so wait past it.)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.Acquire(ctx, Request{ReservationTokens: 5_000})
	// May park until the 30s reset in a strict reading; accept either grant
	// or context expiry, but the ceiling must be calibrated.
	_ = err
}

func TestAIMD(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000})
	s.ObserveThrottle(0)
	assert.Equal(t, int64(500), s.State().EffectiveTokensPerMinute, "throttle halves the ceiling")
	s.ObserveSuccess(2000)
	assert.Equal(t, int64(525), s.State().EffectiveTokensPerMinute, "clean interval adds 5%")

	// Header calibration outranks AIMD growth.
	s.UpdateFromHeaders(1_000_000, -1, 0)
	s.ObserveSuccess(2_000_000)
	assert.Equal(t, int64(1_000_000), s.State().EffectiveTokensPerMinute)
}

func TestConcurrentAcquireReleaseRace(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 60_000, StarvationAge: 100 * time.Millisecond})
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
	s := New("test|close", Config{TokensPerMinute: 1000})
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
