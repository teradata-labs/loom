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
	originBatch       = loomv1.SlotOrigin_SLOT_ORIGIN_BATCH
	originInteractive = loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE
)

// A human waiting on a single turn outranks the entire batch band — even a
// batch RESOURCE_HOLDER.
func TestInteractiveBandOutranksBatchHolder(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: time.Hour})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Origin: originInteractive})
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	run := func(name string, origin loomv1.SlotOrigin, class loomv1.SlotPriorityClass) {
		g, err := s.Acquire(context.Background(), Request{
			ReservationTokens: 600, Origin: origin, Class: class, ConversationID: name})
		require.NoError(t, err)
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		g.Release(1)
	}

	var wg sync.WaitGroup
	// Batch holder enqueued FIRST; the interactive NEW must still win.
	wg.Add(2)
	go func() { defer wg.Done(); run("batch-holder", originBatch, classHolder) }()
	time.Sleep(100 * time.Millisecond)
	go func() { defer wg.Done(); run("human", originInteractive, classNew) }()
	time.Sleep(100 * time.Millisecond)

	hog.Release(1)
	wg.Wait()
	assert.Equal(t, []string{"human", "batch-holder"}, order,
		"interactive NEW must be served before batch RESOURCE_HOLDER")
}

// Batch admission is capped below the interactive headroom: a batch request
// that fits the raw budget but crosses into the headroom must park, while an
// interactive request of the same size passes.
func TestBatchCannotConsumeInteractiveHeadroom(t *testing.T) {
	// Budget 800 (1000 * 0.8); headroom 0.25 → batch cap 600.
	s := newTest(t, Config{TokensPerMinute: 1000, InteractiveHeadroom: 0.25, StarvationAge: time.Hour})

	// 500 batch tokens fit under the 600 cap.
	g1, err := s.Acquire(context.Background(), Request{ReservationTokens: 500, Origin: originBatch})
	require.NoError(t, err)
	defer g1.Release(0)

	// 150 more batch tokens would be 650 > 600: must park...
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = s.Acquire(ctx, Request{ReservationTokens: 150, Origin: originBatch})
	require.ErrorIs(t, err, context.DeadlineExceeded, "batch must not consume the interactive headroom")

	// ...while the same 150 tokens from a human pass immediately (650 <= 800).
	g2, err := s.Acquire(context.Background(), Request{ReservationTokens: 150, Origin: originInteractive})
	require.NoError(t, err, "the headroom exists precisely for this request")
	g2.Release(1)
}

// Aging must never promote a batch waiter into the interactive band: batch
// liveness comes from its own budget share, not the human lane.
func TestAgingStaysWithinBand(t *testing.T) {
	s := newTest(t, Config{TokensPerMinute: 1000, StarvationAge: 500 * time.Millisecond})
	hog, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Origin: originInteractive})
	require.NoError(t, err)
	defer hog.Release(0)

	done := make(chan struct{})
	go func() {
		g, err := s.Acquire(context.Background(), Request{ReservationTokens: 600, Origin: originBatch, Class: classNew})
		require.NoError(t, err)
		g.Release(0)
		close(done)
	}()

	// Wait until aging has demonstrably promoted the batch waiter twice
	// (NEW → IN_FLIGHT → RESOURCE_HOLDER within the batch band).
	require.Eventually(t, func() bool {
		return s.State().PromotionsTotal >= 2
	}, 15*time.Second, 100*time.Millisecond)

	ws := s.Waiters()
	require.Len(t, ws, 1)
	assert.Equal(t, originBatch, ws[0].Origin, "promotion must never change the band")
	assert.Equal(t, classHolder, ws[0].Class, "waiter should have aged to the top of its own band")
}
