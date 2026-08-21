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
			require.NoError(t, err)
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			rel()
		}(i)
		time.Sleep(50 * time.Millisecond) // deterministic queue order
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
	defer r1()

	// One waiter fits the queue.
	go func() {
		rel, err := g.Enter(context.Background())
		if err == nil {
			rel()
		}
	}()
	time.Sleep(100 * time.Millisecond)

	// The next is refused with backpressure, before any waiting.
	_, err = g.Enter(context.Background())
	require.ErrorIs(t, err, ErrDoorFull)
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
	defer r1()
	done := make(chan struct{})
	go func() {
		r2, err := g.Enter(context.Background())
		require.NoError(t, err)
		r2()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second enter must wait: double release created a phantom slot")
	case <-time.After(200 * time.Millisecond):
	}
	r1()
	<-done
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
