// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"sync/atomic"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Wiring: the seams that connect the scheduler to the rest of loom without
// the rest of loom importing scheduler internals.
//
//   - looms enables scheduling at boot (SetEnabled) and registers the gRPC
//     Service over Default().
//   - The server's conversation entry (StreamWeave) installs a SlotInfo into
//     the request context, stamped with the turn's origin (interactive when
//     the CLI reports a human at a terminal — edge-triggered per turn).
//   - The agent's LLM funnel (chatWithRetry) acquires a slot per call via
//     AcquireForCall, which reads class and origin from the SlotInfo.
//   - Resource acquisition sites (MCP session handles) call
//     MarkResourceHolder to lift the conversation's remaining calls to the
//     RESOURCE_HOLDER class — priority inheritance.

var (
	defaultRegistry = NewRegistry(nil)
	schedEnabled    atomic.Bool
)

// Default returns the process-wide scheduler registry.
func Default() *Registry { return defaultRegistry }

// SetEnabled turns slot scheduling on or off process-wide. Off (the
// default) makes AcquireForCall a no-op, so unwired deployments behave
// exactly as before.
func SetEnabled(v bool) { schedEnabled.Store(v) }

// Enabled reports whether slot scheduling is active.
func Enabled() bool { return schedEnabled.Load() }

// SlotInfo is the per-conversation-turn scheduling state carried in the
// request context. Fields are updated in place (the pointer is shared), so
// a MarkResourceHolder call after the first LLM call is visible to later
// acquisitions in the same turn.
type SlotInfo struct {
	// origin is the turn's band; set once at the door.
	origin loomv1.SlotOrigin
	// calls counts LLM calls made in this conversation so far (across
	// turns when the caller reuses the SlotInfo; within the turn otherwise).
	// First call = NEW class, later calls = IN_FLIGHT.
	calls atomic.Int64
	// resourceHolder is set when the conversation acquires an external
	// scarce resource (database session handle, MCP slot).
	resourceHolder atomic.Bool
}

type slotInfoKey struct{}

// WithSlotInfo installs a SlotInfo for this conversation turn. priorCalls
// seeds the call counter so a resumed conversation classifies as IN_FLIGHT
// from its first call of the new turn.
func WithSlotInfo(ctx context.Context, origin loomv1.SlotOrigin, priorCalls int64) context.Context {
	si := &SlotInfo{origin: origin}
	si.calls.Store(priorCalls)
	return context.WithValue(ctx, slotInfoKey{}, si)
}

// SlotInfoFrom returns the turn's SlotInfo, or nil when none is installed.
func SlotInfoFrom(ctx context.Context) *SlotInfo {
	si, _ := ctx.Value(slotInfoKey{}).(*SlotInfo)
	return si
}

// MarkResourceHolder records that this conversation now holds an external
// scarce resource; its later LLM calls ride the RESOURCE_HOLDER class.
// No-op when no SlotInfo is installed.
func MarkResourceHolder(ctx context.Context) {
	if si := SlotInfoFrom(ctx); si != nil {
		si.resourceHolder.Store(true)
	}
}

// ScopeProvider is optionally implemented by LLM clients that know their
// quota boundary (endpoint/deployment/region). Clients that do not are
// scoped by provider name + model.
type ScopeProvider interface {
	SchedulerScope() string
}

// ScopeFor derives the scheduler scope for an LLM provider.
func ScopeFor(providerName, model string, p any) string {
	if sp, ok := p.(ScopeProvider); ok {
		if s := sp.SchedulerScope(); s != "" {
			return s
		}
	}
	return providerName + "|" + model
}

// AcquireForCall acquires a slot for one LLM call using the context's
// SlotInfo for class and origin. When scheduling is disabled or no SlotInfo
// is installed, it returns a nil grant and no error — callers treat a nil
// grant as "nothing to release". The returned grant must be released with
// the call's actual token usage.
func AcquireForCall(ctx context.Context, scope string, reservationTokens int64) (*Grant, error) {
	if !Enabled() {
		return nil, nil
	}
	si := SlotInfoFrom(ctx)
	if si == nil {
		return nil, nil
	}
	class := loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW
	if si.calls.Load() > 0 {
		class = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT
	}
	if si.resourceHolder.Load() {
		class = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER
	}
	g, err := defaultRegistry.For(scope, Config{}).Acquire(ctx, Request{
		Class:             class,
		Origin:            si.origin,
		ReservationTokens: reservationTokens,
	})
	if err != nil {
		return nil, err
	}
	si.calls.Add(1)
	return g, nil
}
