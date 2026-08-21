// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestAcquireForCallDisabledIsNoOp(t *testing.T) {
	SetEnabled(false)
	ctx := WithSlotInfo(context.Background(), originInteractive, 0)
	g, err := AcquireForCall(ctx, "noop-scope", 100)
	require.NoError(t, err)
	assert.Nil(t, g, "disabled scheduling must be a transparent no-op")
}

func TestAcquireForCallNoSlotInfoIsNoOp(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	g, err := AcquireForCall(context.Background(), "noinfo-scope", 100)
	require.NoError(t, err)
	assert.Nil(t, g, "a turn without SlotInfo (unwired path) must pass through")
}

func TestAcquireForCallClassProgression(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	ctx := WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)

	// First call of the conversation: NEW.
	g1, err := AcquireForCall(ctx, "prog-scope", 10)
	require.NoError(t, err)
	require.NotNil(t, g1)
	g1.Release(1)

	// Second call: IN_FLIGHT (call counter advanced in the shared SlotInfo).
	si := SlotInfoFrom(ctx)
	require.NotNil(t, si)
	assert.Equal(t, int64(1), si.calls.Load())

	// Acquiring an external resource lifts the class for later calls.
	MarkResourceHolder(ctx)
	assert.True(t, si.resourceHolder.Load())

	g2, err := AcquireForCall(ctx, "prog-scope", 10)
	require.NoError(t, err)
	require.NotNil(t, g2)
	g2.Release(1)
}

func TestWithSlotInfoPriorCallsSeedsInFlight(t *testing.T) {
	// A resumed conversation must not be re-classified as NEW.
	ctx := WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 7)
	si := SlotInfoFrom(ctx)
	require.NotNil(t, si)
	assert.Equal(t, int64(7), si.calls.Load())
}

type scopedStub struct{ scope string }

func (s scopedStub) SchedulerScope() string { return s.scope }

func TestScopeFor(t *testing.T) {
	assert.Equal(t, "azure-openai|https://e/|gpt-4o",
		ScopeFor("azure-openai", "gpt-4o", scopedStub{scope: "azure-openai|https://e/|gpt-4o"}),
		"a ScopeProvider names its own quota boundary")
	assert.Equal(t, "gemini|gemini-pro", ScopeFor("gemini", "gemini-pro", struct{}{}),
		"fallback is name|model")
	assert.Equal(t, "x|y", ScopeFor("x", "y", scopedStub{scope: ""}),
		"an empty ScopeProvider answer falls back too")
}

type wrappingStub struct{ inner any }

func (w wrappingStub) SchedulerScope() string {
	if sp, ok := w.inner.(ScopeProvider); ok {
		return sp.SchedulerScope()
	}
	return ""
}

// A wrapper that forwards an EMPTY scope (its inner provider names none)
// must fall back to name|model — the split-brain regression guard.
func TestScopeForWrapperForwardingEmptyFallsBack(t *testing.T) {
	assert.Equal(t, "azure-openai|gpt-4o", ScopeFor("azure-openai", "gpt-4o", wrappingStub{inner: struct{}{}}))
	assert.Equal(t, "real|scope", ScopeFor("azure-openai", "gpt-4o", wrappingStub{inner: scopedStub{scope: "real|scope"}}))
}
