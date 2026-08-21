// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"errors"
	"sync"
)

// ErrDoorFull is returned when the door queue itself is at capacity: the
// caller should surface backpressure (RESOURCE_EXHAUSTED + retry-later)
// instead of queueing more work. This is the ONLY capacity-shaped error in
// the scheduler, and it exists precisely because it fires BEFORE any work
// starts — starving a conversation at the door is free; starving it
// mid-task wastes held resources and partial work.
var ErrDoorFull = errors.New("scheduler: door queue full")

// DoorGate caps concurrently active conversation turns. Batch turns beyond
// MaxActive queue FIFO at the door until a running turn completes;
// interactive turns bypass the gate entirely (the slot scheduler's
// interactive headroom protects their capacity). Zero-value MaxActive
// disables the gate.
type DoorGate struct {
	mu sync.Mutex
	// maxActive is the active-turn ceiling; <= 0 disables gating.
	maxActive int
	// maxQueue caps the door queue; <= 0 means unbounded queueing.
	maxQueue int
	active   int
	queue    []chan struct{}
}

// NewDoorGate builds a gate. maxActive <= 0 disables it.
func NewDoorGate(maxActive, maxQueue int) *DoorGate {
	return &DoorGate{maxActive: maxActive, maxQueue: maxQueue}
}

// Enter admits one conversation turn, blocking FIFO at the door while the
// active ceiling is reached. It returns a release function that MUST be
// called when the turn completes (idempotent). Waiting is bounded only by
// ctx; the only capacity error is ErrDoorFull, raised before any waiting
// when the door queue itself is at its cap.
func (g *DoorGate) Enter(ctx context.Context) (func(), error) {
	g.mu.Lock()
	if g.maxActive <= 0 {
		g.mu.Unlock()
		return func() {}, nil
	}
	if g.active < g.maxActive {
		g.active++
		g.mu.Unlock()
		return g.releaseFunc(), nil
	}
	if g.maxQueue > 0 && len(g.queue) >= g.maxQueue {
		g.mu.Unlock()
		return nil, ErrDoorFull
	}
	admit := make(chan struct{})
	g.queue = append(g.queue, admit)
	g.mu.Unlock()

	select {
	case <-admit:
		// The releaser incremented active on our behalf before signalling.
		return g.releaseFunc(), nil
	case <-ctx.Done():
		g.mu.Lock()
		for i, ch := range g.queue {
			if ch == admit {
				g.queue = append(g.queue[:i], g.queue[i+1:]...)
				break
			}
		}
		g.mu.Unlock()
		// The admit signal may have raced the cancellation: if it did, the
		// slot was transferred to us and must be returned.
		select {
		case <-admit:
			g.releaseFunc()()
		default:
		}
		return nil, ctx.Err()
	}
}

// releaseFunc returns the idempotent completion callback for one admission.
func (g *DoorGate) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if len(g.queue) > 0 {
				// Hand the slot directly to the next waiter (FIFO): active
				// stays constant, so the ceiling cannot be overshot by a
				// release/enter race.
				next := g.queue[0]
				g.queue = g.queue[1:]
				close(next)
				return
			}
			g.active--
			if g.active < 0 {
				g.active = 0
			}
		})
	}
}

// DoorState reports the gate's live counters.
func (g *DoorGate) DoorState() (active, queued int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active, len(g.queue)
}
