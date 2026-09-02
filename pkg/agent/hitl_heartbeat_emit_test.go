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
package agent_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// The progress bridge must advertise the heartbeat capability, or the ask
// resolver's interface assertion silently skips it and holds stay silent.
func TestHeartbeatEmit_BridgeImplementsHeartbeater(t *testing.T) {
	_, ok := agent.NewProgressNotifier().(shuttle.Heartbeater)
	require.True(t, ok, "the progress notifier must implement shuttle.Heartbeater")
}

// A heartbeat produces traffic on the run's progress stream, and does so
// WITHOUT a HITLRequest payload — that is what keeps it from being mistaken for
// a second approval card by any consumer that keys off the id-bearing event.
func TestHeartbeatEmit_EmitsIDLessHITLStageEvent(t *testing.T) {
	var mu sync.Mutex
	var events []types.ProgressEvent

	ctx := agent.ContextWithProgressCallback(context.Background(), func(e types.ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	hb, ok := agent.NewProgressNotifier().(shuttle.Heartbeater)
	require.True(t, ok)
	require.NoError(t, hb.Heartbeat(ctx))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 1, "a heartbeat must produce exactly one progress event")

	got := events[0]
	require.Equal(t, agent.StageHumanInTheLoop, got.Stage,
		"the heartbeat rides the HITL stage so consumers can scope it to a hold")
	require.Nil(t, got.HITLRequest,
		"a heartbeat must carry no request payload at all — absent payload is the "+
			"contract, which is STRICTER than an empty request id (the pre-creation "+
			"ping carries a payload with an empty id and IS a card)")
	require.NotEmpty(t, got.Message)
	require.False(t, got.Timestamp.IsZero())
	require.True(t, got.Droppable,
		"a heartbeat must be droppable — it emits from the turn goroutine parked in "+
			"the hold's poll loop, so a blocking send on a wedged consumer would stall "+
			"the poll that reads the human's decision")
}

// The card and the heartbeats that follow it must carry the SAME progress
// percentage. A consumer renders a percentage only while it is in (0, 100), so
// a heartbeat at a different value makes the indicator flicker for the whole
// length of a hold.
func TestHeartbeatEmit_ProgressMatchesTheCard(t *testing.T) {
	var mu sync.Mutex
	var events []types.ProgressEvent

	ctx := agent.ContextWithProgressCallback(context.Background(), func(e types.ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	n := agent.NewProgressNotifier()
	require.NoError(t, n.Notify(ctx, &shuttle.HumanRequest{ID: "req-1", Kind: "approval"}))

	hb, ok := n.(shuttle.Heartbeater)
	require.True(t, ok)
	require.NoError(t, hb.Heartbeat(ctx))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 2)

	card, beat := events[0], events[1]
	require.NotNil(t, card.HITLRequest, "the first emit is the card")
	require.Nil(t, beat.HITLRequest, "the second emit is the heartbeat")
	require.Equal(t, card.Progress, beat.Progress,
		"a heartbeat must not move the progress indicator the card set")
	require.False(t, card.Droppable, "a card carries state and must never be dropped")
}

// Fail-open: with no progress callback installed there is nothing to emit on,
// and a heartbeat must stay a silent no-op rather than erroring or panicking —
// a missing progress stream may never affect a hold.
func TestHeartbeatEmit_NoCallbackIsNoOp(t *testing.T) {
	hb, ok := agent.NewProgressNotifier().(shuttle.Heartbeater)
	require.True(t, ok)
	require.NotPanics(t, func() {
		require.NoError(t, hb.Heartbeat(context.Background()))
	})
}
