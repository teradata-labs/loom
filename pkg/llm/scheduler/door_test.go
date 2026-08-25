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
)

// waitQueued blocks until the gate reports exactly n parked waiters.
// Readiness is observed through DoorState instead of sleeps: pathological
// scheduling fails the Eventually assertion instead of deadlocking the test
// or silently testing the wrong interleaving.
func waitQueued(t *testing.T, g *DoorGate, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, queued := g.DoorState()
		return queued == n
	}, 5*time.Second, time.Millisecond, "expected %d parked waiter(s) at the door", n)
}

func TestDoorGateDisabledIsTransparent(t *testing.T) {
	g := NewDoorGate(0, 0)
	rel, err := g.Enter(context.Background())
	require.NoError(t, err)
	rel()
}

func TestDoorGateCapsAndAdmitsFIFO(t *testing.T) {
	g := NewDoorGate(2, 0)
	r1, err := g.Enter(context.Background())
	require.NoError(t, err)
	r2, err := g.Enter(context.Background())
	require.NoError(t, err)

	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel, err := g.Enter(context.Background())
			if err != nil {
				t.Errorf("waiter %d: unexpected enter error: %v", i, err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			rel()
		}(i)
		// Deterministic queue order: waiter i must be parked before waiter
		// i+1 is spawned.
		waitQueued(t, g, i)
	}

	active, queued := g.DoorState()
	assert.Equal(t, 2, active)
	assert.Equal(t, 2, queued)

	// A single release drives the whole chain deterministically: waiter 1 is
	// admitted by r1, and waiter 2 only by waiter 1's own release — so the
	// recorded order proves FIFO hand-off, not goroutine scheduling.
	r1()
	wg.Wait()
	assert.Equal(t, []int{1, 2}, order, "door admission must be FIFO")
	r2()

	active, queued = g.DoorState()
	assert.Equal(t, 0, active)
	assert.Equal(t, 0, queued)
}

func TestDoorGateQueueCapRejects(t *testing.T) {
	g := NewDoorGate(1, 1)
	r1, err := g.Enter(context.Background())
	require.NoError(t, err)

	// One waiter fits the queue.
	waiterErr := make(chan error, 1)
	go func() {
		rel, err := g.Enter(context.Background())
		if err == nil {
			rel()
		}
		waiterErr <- err
	}()
	waitQueued(t, g, 1)

	// The next is refused with backpressure, before any waiting.
	_, err = g.Enter(context.Background())
	require.ErrorIs(t, err, ErrDoorFull)

	// Releasing the active slot admits the parked waiter normally.
	r1()
	require.NoError(t, <-waiterErr, "parked waiter must be admitted after release")
}

func TestDoorGateCtxCancelLeavesNoLeak(t *testing.T) {
	g := NewDoorGate(1, 0)
	r1, err := g.Enter(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = g.Enter(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, queued := g.DoorState()
	assert.Equal(t, 0, queued, "cancelled waiter must leave the queue")

	r1()
	// The gate must still admit normally.
	r2, err := g.Enter(context.Background())
	require.NoError(t, err)
	r2()
}

func TestDoorGateReleaseIdempotent(t *testing.T) {
	g := NewDoorGate(1, 0)
	rel, err := g.Enter(context.Background())
	require.NoError(t, err)
	rel()
	rel() // double release must not free a phantom slot
	active, _ := g.DoorState()
	assert.Equal(t, 0, active)

	r1, err := g.Enter(context.Background())
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		r2, err := g.Enter(context.Background())
		if err == nil {
			r2()
		}
		done <- err
	}()
	waitQueued(t, g, 1)
	select {
	case <-done:
		t.Fatal("second enter must wait: double release created a phantom slot")
	case <-time.After(200 * time.Millisecond):
	}
	r1()
	require.NoError(t, <-done)
}

func TestDoorGateConcurrentChurnRace(t *testing.T) {
	g := NewDoorGate(4, 0)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := g.Enter(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			rel()
		}()
	}
	wg.Wait()
	active, queued := g.DoorState()
	assert.Equal(t, 0, active)
	assert.Equal(t, 0, queued)
}

// TestGetSlotStatePopulatesDoorCounters: the LLMSchedulerService surfaces the
// process-wide door gate's live counters on every scope's SlotState.
func TestGetSlotStatePopulatesDoorCounters(t *testing.T) {
	SetDoorLimits(1, 0)
	defer SetDoorLimits(0, 0)

	rel, err := Door().Enter(context.Background())
	require.NoError(t, err)

	parkedErr := make(chan error, 1)
	go func() {
		r, err := Door().Enter(context.Background())
		if err == nil {
			r()
		}
		parkedErr <- err
	}()
	waitQueued(t, Door(), 1)

	reg := NewRegistry(nil)
	defer reg.Close()
	reg.For("scope-a", Config{})
	svc := NewService(reg)

	resp, err := svc.GetSlotState(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, resp.States, 1)
	assert.Equal(t, int32(1), resp.States[0].ActiveConversations,
		"SlotState must report the door gate's active-conversation count")
	assert.Equal(t, int32(1), resp.States[0].DoorQueueDepth,
		"SlotState must report the door gate's queue depth")

	rel()
	require.NoError(t, <-parkedErr)
}
