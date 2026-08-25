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
// Tests for door admission at the turn-executing entry points: EVERY entry
// point (unary Weave included) must respect max_active_conversations, door
// rejections must never be cached as durable dedupe outcomes, and HTTP/SSE
// clients must be able to assert the interactive band via header.
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmscheduler "github.com/teradata-labs/loom/pkg/llm/scheduler"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// withDoorLimits configures the process-wide door gate for one test and
// restores the disabled default afterwards (the gate is global state shared
// by every test in the package).
func withDoorLimits(t *testing.T, maxActive, maxQueue int) {
	t.Helper()
	llmscheduler.SetDoorLimits(maxActive, maxQueue)
	t.Cleanup(func() { llmscheduler.SetDoorLimits(0, 0) })
}

// waitDoorQueued blocks until the process-wide door gate reports exactly n
// parked waiters — readiness signaling instead of sleeps.
func waitDoorQueued(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, queued := llmscheduler.Door().DoorState()
		return queued == n
	}, 5*time.Second, time.Millisecond, "expected %d turn(s) parked at the door", n)
}

// blockingLLMProvider signals when a Chat call begins and holds it until
// release is closed, so tests can pin a conversation turn in its active
// phase.
type blockingLLMProvider struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingLLMProvider() *blockingLLMProvider {
	return &blockingLLMProvider{
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (m *blockingLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.entered <- struct{}{}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llmtypes.LLMResponse{
		Content: "done",
		Usage:   llmtypes.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.001},
	}, nil
}

func (m *blockingLLMProvider) Name() string  { return "mock-blocking" }
func (m *blockingLLMProvider) Model() string { return "mock-blocking-v1" }

// TestUnaryWeaveRespectsDoorCeiling (review finding 1, PR #353): unary Weave
// — the MCP bridge, TUI unary path, and grpc-gateway /v1/weave all drive it —
// must park at the door exactly like StreamWeave, so
// max_active_conversations is a real ceiling. Two concurrent unary Weaves
// against maxActive=1: the second must park at the door (never reaching the
// agent) until the first releases.
func TestUnaryWeaveRespectsDoorCeiling(t *testing.T) {
	withDoorLimits(t, 1, 0)

	llm := newBlockingLLMProvider()
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent-1": ag}, nil)

	type result struct {
		resp *loomv1.WeaveResponse
		err  error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			resp, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{Query: "q"})
			results <- result{resp, err}
		}()
	}

	// Exactly one turn reaches the LLM; the other parks at the door.
	<-llm.entered
	waitDoorQueued(t, 1)
	select {
	case <-llm.entered:
		t.Fatal("second unary Weave reached the agent past the max_active ceiling")
	case <-time.After(200 * time.Millisecond):
	}

	// Releasing the first turn admits the second; both complete.
	close(llm.release)
	for i := 0; i < 2; i++ {
		r := <-results
		require.NoError(t, r.err)
		require.NotNil(t, r.resp)
		assert.Equal(t, "done", r.resp.Text)
	}

	active, queued := llmscheduler.Door().DoorState()
	assert.Equal(t, 0, active, "all turns released their door slot")
	assert.Equal(t, 0, queued)
}

// TestSingleAgentUnaryWeaveRespectsDoorCeiling: the single-agent Server's
// unary Weave is its own entry point and must also go through the door.
func TestSingleAgentUnaryWeaveRespectsDoorCeiling(t *testing.T) {
	withDoorLimits(t, 1, 0)

	llm := newBlockingLLMProvider()
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewServer(ag, nil)

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{Query: "q"})
			errs <- err
		}()
	}

	<-llm.entered
	waitDoorQueued(t, 1)
	select {
	case <-llm.entered:
		t.Fatal("second unary Weave reached the agent past the max_active ceiling")
	case <-time.After(200 * time.Millisecond):
	}

	close(llm.release)
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
	}
}

// TestEnterTurnDoorInteractiveBypassesFullDoor: interactive turns bypass the
// gate entirely, even when the door is saturated — a web or terminal human
// never parks behind fleets.
func TestEnterTurnDoorInteractiveBypassesFullDoor(t *testing.T) {
	withDoorLimits(t, 1, 1)

	// Saturate the active slot.
	rel, err := llmscheduler.Door().Enter(context.Background())
	require.NoError(t, err)
	defer rel()

	release, err := enterTurnDoor(mdCtx(SlotOriginMetadataKey, "interactive"), nil)
	require.NoError(t, err, "interactive turns must bypass the door")
	release()
}

// TestEnterTurnDoorFullRejectsResourceExhausted: a full door queue surfaces
// as RESOURCE_EXHAUSTED backpressure at the admission helper.
func TestEnterTurnDoorFullRejectsResourceExhausted(t *testing.T) {
	withDoorLimits(t, 1, 1)

	rel, err := llmscheduler.Door().Enter(context.Background())
	require.NoError(t, err)

	parkedErr := make(chan error, 1)
	go func() {
		r, err := llmscheduler.Door().Enter(context.Background())
		if err == nil {
			r()
		}
		parkedErr <- err
	}()
	waitDoorQueued(t, 1)

	_, err = enterTurnDoor(context.Background(), nil)
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))

	rel()
	require.NoError(t, <-parkedErr)
}

// TestDoorRejectionNotCachedByDedupe (review finding 2, PR #353): a
// door-full rejection is transient backpressure, not a durable outcome. A
// client retrying with the same idempotency key after capacity frees must
// re-attempt admission and succeed — never be served the stale
// RESOURCE_EXHAUSTED from the dedupe cache for the 10-minute TTL.
func TestDoorRejectionNotCachedByDedupe(t *testing.T) {
	withDoorLimits(t, 1, 1)

	llm := &mockLLMProvider{responses: []string{"answer"}}
	srv := newDedupeServer(llm)

	// Occupy the active slot and fill the one-deep queue.
	rel, err := llmscheduler.Door().Enter(context.Background())
	require.NoError(t, err)
	parkedErr := make(chan error, 1)
	go func() {
		r, err := llmscheduler.Door().Enter(context.Background())
		if err == nil {
			r()
		}
		parkedErr <- err
	}()
	waitDoorQueued(t, 1)

	// Door full: the keyed request is rejected with backpressure.
	_, err = srv.Weave(keyedCtx("user-a", "door-retry-key"), &loomv1.WeaveRequest{Query: "q"})
	require.Error(t, err)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	// Capacity frees: the direct holders drain.
	rel()
	require.NoError(t, <-parkedErr)
	waitDoorQueued(t, 0)

	// Same-key retry must re-attempt admission and succeed, not join the
	// cached rejection.
	resp, err := srv.Weave(keyedCtx("user-a", "door-retry-key"), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err, "same-key retry after the queue drained must re-execute, not replay the cached RESOURCE_EXHAUSTED")
	require.NotNil(t, resp)
	assert.Equal(t, "answer", resp.Text)
}

// TestIsTransientOutcomeResourceExhausted: the dedupe release classifier
// treats capacity backpressure as transient alongside cancel/deadline.
func TestIsTransientOutcomeResourceExhausted(t *testing.T) {
	assert.True(t, isTransientOutcome(status.Error(codes.ResourceExhausted, "door queue full")))
	assert.True(t, isTransientOutcome(context.Canceled))
	assert.True(t, isTransientOutcome(status.Error(codes.DeadlineExceeded, "deadline")))
	assert.False(t, isTransientOutcome(status.Error(codes.InvalidArgument, "bad query")))
	assert.False(t, isTransientOutcome(nil))
}

// TestWithHTTPSlotOriginMapsHeader (review finding 3, PR #353): the HTTP/SSE
// path carries no gRPC metadata, so the X-Loom-Slot-Origin header must be
// mapped into the context for slotOriginFromMetadata to see — otherwise web
// humans can never be INTERACTIVE.
func TestWithHTTPSlotOriginMapsHeader(t *testing.T) {
	newReq := func(headerVal string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/weave:stream", nil)
		if headerVal != "" {
			r.Header.Set(SlotOriginHTTPHeader, headerVal)
		}
		return r
	}

	r := newReq("interactive")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE,
		slotOriginFromMetadata(withHTTPSlotOrigin(r.Context(), r)),
		"X-Loom-Slot-Origin: interactive must band INTERACTIVE")

	r = newReq("Interactive")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE,
		slotOriginFromMetadata(withHTTPSlotOrigin(r.Context(), r)),
		"header values are case-insensitive")

	r = newReq("batch")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
		slotOriginFromMetadata(withHTTPSlotOrigin(r.Context(), r)))

	r = newReq("")
	assert.Equal(t, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH,
		slotOriginFromMetadata(withHTTPSlotOrigin(r.Context(), r)),
		"absent header defaults to BATCH")
}

// TestHTTPInteractiveHeaderBypassesFullDoor composes the header mapping with
// the admission helper: an SSE turn asserting interactive walks past a
// saturated door.
func TestHTTPInteractiveHeaderBypassesFullDoor(t *testing.T) {
	withDoorLimits(t, 1, 1)

	rel, err := llmscheduler.Door().Enter(context.Background())
	require.NoError(t, err)
	defer rel()

	r := httptest.NewRequest(http.MethodPost, "/v1/weave:stream", nil)
	r.Header.Set(SlotOriginHTTPHeader, "interactive")
	release, err := enterTurnDoor(withHTTPSlotOrigin(r.Context(), r), nil)
	require.NoError(t, err, "an interactive HTTP turn must bypass a full door")
	release()
}
