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
// This file implements idempotency-key dedupe for weave execution (MCP
// 2026-07-28 migration, decision D1). The stateless revision removed SSE
// resumption: a client that loses a response stream MUST re-issue the request,
// and the re-issue carries the same com.teradata.loom/idempotencyKey the MCP
// bridge forwards as gRPC metadata. Deduping at looms — the state owner —
// makes that re-issue join the in-flight run instead of executing the turn
// twice, regardless of which bridge replica it lands on.
//
// The map is per looms process. That is correct for the deployed topology
// (N stateless bridge replicas in front of one stateful looms); multi-replica
// looms would need the durable run store deferred by decision D3.
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmscheduler "github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// weaveDedupeTTL bounds how long a completed result is replayable. Ten
// minutes covers the longest streaming call a client would re-issue after a
// stream loss (migration spec §7.5).
const weaveDedupeTTL = 10 * time.Minute

// weaveDedupeMaxKeyLen bounds accepted idempotency keys: the client mints
// UUIDs (36 bytes); anything an order of magnitude beyond that is abuse, and
// unbounded keys make the map a memory-amplification vector.
const weaveDedupeMaxKeyLen = 256

// weaveDedupeMaxEntries bounds the registry. On overflow — after evicting
// expired and then oldest completed entries — new keys are not admitted and
// their weaves run without dedupe, which is the at-least-once semantics the
// specification prescribes for key-less requests anyway: availability is
// never sacrificed to bookkeeping.
const weaveDedupeMaxEntries = 4096

// weaveDedupeSweepEvery amortizes expiry: instead of scanning the whole map
// on every admission (O(N) per begin), a full sweep runs at most this often.
const weaveDedupeSweepEvery = time.Minute

// weaveDedupeEntry is one logical weave identified by (caller, key). The
// first arrival owns execution; duplicates wait on done and read the result.
// Join semantics are terminal-result-only by design: replaying progress
// events would be rebuilding the stream resumption the revision deleted.
type weaveDedupeEntry struct {
	done chan struct{}
	// resp and err are written once before done closes and read only after
	// it closes (channel happens-before), so they need no lock. expiresAtNano
	// is read by begin's sweep concurrently with finish, hence atomic; zero
	// means in flight.
	resp          *loomv1.WeaveResponse
	err           error
	expiresAtNano atomic.Int64
}

func (e *weaveDedupeEntry) finish(resp *loomv1.WeaveResponse, err error) {
	e.resp = resp
	e.err = err
	e.expiresAtNano.Store(time.Now().Add(weaveDedupeTTL).UnixNano())
	close(e.done)
}

// finishAndRelease resolves an owned entry. Durable outcomes — a response, or
// a deterministic execution error — stay cached for the TTL so a re-issue
// joins them. A cancellation or deadline outcome is not durable: it is the
// stream-loss casualty D1 exists to recover from, so the entry is released
// first and the re-issued request re-executes instead of joining a cached
// failure. Joiners already waiting still receive the error through done.
//
// The request context is consulted as well as the error shape (round-3
// finding 2): intermediate layers can launder a cancellation into another
// code — an %v-wrapped Internal, a mid-stream Send failure surfacing as
// Unavailable — but a dead request context means the outcome was never
// delivered and is not durable, whatever the error looks like.
func (d *weaveDeduper) finishAndRelease(ctx context.Context, scopeKey string, entry *weaveDedupeEntry, resp *loomv1.WeaveResponse, err error) {
	if resp == nil && (isTransientOutcome(err) || ctx.Err() != nil) {
		d.mu.Lock()
		// Guard against releasing a successor entry that reused the key
		// after this one was already swept or evicted.
		if d.entries[scopeKey] == entry {
			delete(d.entries, scopeKey)
		}
		d.mu.Unlock()
	}
	entry.finish(resp, err)
}

// wrapAgentError maps an agent execution failure to its gRPC status.
// Cancellation and deadline keep their own codes: correct for callers, and
// the dedupe release classifies on them — flattening them into Internal via
// %v would cache a caller disconnect as a durable outcome for the full TTL
// (round-3 finding 2). Everything else is Internal.
func wrapAgentError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Errorf(codes.Canceled, "agent execution canceled: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.DeadlineExceeded, "agent execution deadline exceeded: %v", err)
	}
	return status.Errorf(codes.Internal, "agent execution failed: %v", err)
}

// isTransientOutcome reports whether err reflects an interrupted run
// (caller disconnect, deadline) or a capacity rejection rather than a
// deterministic result. RESOURCE_EXHAUSTED — the door-full backpressure code
// — means "retry later" by definition: caching it for the dedupe TTL would
// keep serving the stale rejection to a same-key retry long after the door
// queue drained, so it must be released, never cached.
func isTransientOutcome(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	}
	return false
}

// weaveDeduper tracks in-flight and recently completed weaves per
// (caller identity, idempotency key), with bounded admission.
type weaveDeduper struct {
	mu        sync.Mutex
	entries   map[string]*weaveDedupeEntry
	lastSweep time.Time
}

func newWeaveDeduper() *weaveDeduper {
	return &weaveDeduper{entries: make(map[string]*weaveDedupeEntry), lastSweep: time.Now()}
}

// begin registers interest in a logical weave. admitted is false when the
// key is not deduplicable (oversized, or the registry is full of in-flight
// runs): the caller executes without dedupe — at-least-once, exactly as a
// key-less request. isOwner is true for the first admitted arrival, which
// must call finish exactly once; joiners wait on the returned entry.
func (d *weaveDeduper) begin(scopeKey string) (entry *weaveDedupeEntry, isOwner, admitted bool) {
	if len(scopeKey) > weaveDedupeMaxKeyLen {
		return nil, false, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if now.Sub(d.lastSweep) >= weaveDedupeSweepEvery {
		d.sweepLocked(now.UnixNano())
	}

	if existing, ok := d.entries[scopeKey]; ok {
		// A completed entry past its TTL must not be joined just because
		// the amortized sweep has not run yet: expiry is per-entry O(1).
		if exp := existing.expiresAtNano.Load(); exp != 0 && now.UnixNano() > exp {
			delete(d.entries, scopeKey)
		} else {
			return existing, false, true
		}
	}

	if len(d.entries) >= weaveDedupeMaxEntries {
		d.sweepLocked(now.UnixNano())
		if len(d.entries) >= weaveDedupeMaxEntries {
			d.evictOldestCompletedLocked()
		}
		if len(d.entries) >= weaveDedupeMaxEntries {
			return nil, false, false
		}
	}

	entry = &weaveDedupeEntry{done: make(chan struct{})}
	d.entries[scopeKey] = entry
	return entry, true, true
}

// sweepLocked removes expired completed entries; the caller holds d.mu.
func (d *weaveDeduper) sweepLocked(nowNano int64) {
	for k, e := range d.entries {
		if exp := e.expiresAtNano.Load(); exp != 0 && nowNano > exp {
			delete(d.entries, k)
		}
	}
	d.lastSweep = time.Now()
}

// evictOldestCompletedLocked frees one slot by dropping the completed entry
// closest to expiry; in-flight entries are never evicted (a joiner may be
// waiting on them). The caller holds d.mu.
func (d *weaveDeduper) evictOldestCompletedLocked() {
	var oldestKey string
	var oldestExp int64
	for k, e := range d.entries {
		if exp := e.expiresAtNano.Load(); exp != 0 && (oldestExp == 0 || exp < oldestExp) {
			oldestKey, oldestExp = k, exp
		}
	}
	if oldestKey != "" {
		delete(d.entries, oldestKey)
	}
}

// dedupeScope builds the map key: the idempotency key is scoped per caller
// identity so one user's key can never join (or observe) another user's run.
func dedupeScope(userID, key string) string {
	return userID + "\x00" + key
}

// incomingIdempotencyKey reads the bridge-forwarded idempotency key from
// incoming gRPC metadata; empty when the caller sent none (at-least-once
// semantics, exactly as the specification prescribes for key-less clients).
func incomingIdempotencyKey(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(types.IdempotencyKeyMetadataKey)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// awaitDedupeResult blocks until the owning run finishes or the joiner's
// context ends.
func awaitDedupeResult(ctx context.Context, entry *weaveDedupeEntry) (*loomv1.WeaveResponse, error) {
	select {
	case <-entry.done:
		return entry.resp, entry.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// completedProgressFromResponse renders a joined run's terminal result as the
// single COMPLETED event a StreamWeave duplicate receives.
func completedProgressFromResponse(resp *loomv1.WeaveResponse) *loomv1.WeaveProgress {
	return &loomv1.WeaveProgress{
		Stage:     loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED,
		Progress:  100,
		Message:   "Query completed (joined in-flight duplicate request)",
		Timestamp: time.Now().Unix(),
		PartialResult: &loomv1.ExecutionResult{
			Type:     "text",
			DataJson: resp.Text,
		},
		PartialContent: resp.Text,
		ContextState:   resp.ContextState,
		Cost:           resp.Cost,
	}
}

// SlotOriginMetadataKey is the gRPC metadata key the CLI uses to report
// this turn's scheduling band: "interactive" when a human at a terminal is
// waiting on the response, anything else (or absence) is batch.
const SlotOriginMetadataKey = "loom-slot-origin"

// SlotOriginHTTPHeader is the HTTP header counterpart of SlotOriginMetadataKey.
// HTTP/SSE callers set this header; withHTTPSlotOrigin injects it into gRPC
// incoming metadata so that slotOriginFromMetadata works on both transports.
const SlotOriginHTTPHeader = "X-Loom-Slot-Origin"

// withHTTPSlotOrigin copies the X-Loom-Slot-Origin HTTP header from r into ctx
// as gRPC incoming metadata so that slotOriginFromMetadata (which reads from
// metadata) works on the HTTP/SSE path. The header value is trimmed and
// lower-cased before injection to match the gRPC path where the CLI always
// sends "interactive". Existing incoming metadata (if any) is preserved.
func withHTTPSlotOrigin(ctx context.Context, r *http.Request) context.Context {
	val := strings.ToLower(strings.TrimSpace(r.Header.Get(SlotOriginHTTPHeader)))
	// Preserve any existing incoming metadata (e.g. set by earlier interceptors).
	existing, _ := metadata.FromIncomingContext(ctx)
	md := metadata.Join(existing, metadata.Pairs(SlotOriginMetadataKey, val))
	return metadata.NewIncomingContext(ctx, md)
}

// slotOriginFromMetadata reads the turn's origin from incoming metadata.
// The origin is client-asserted and trusted as-is by design (see SlotOrigin
// in proto/loom/v1/llm_scheduler.proto for the trust model).
func slotOriginFromMetadata(ctx context.Context) loomv1.SlotOrigin {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return loomv1.SlotOrigin_SLOT_ORIGIN_BATCH
	}
	vals := md.Get(SlotOriginMetadataKey)
	if len(vals) > 0 && vals[0] == "interactive" {
		return loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE
	}
	return loomv1.SlotOrigin_SLOT_ORIGIN_BATCH
}

// installTurnSlotInfo stamps the turn's LLM slot-scheduling state on ctx:
// the band from client-asserted metadata (slotOriginFromMetadata) and the
// priority-class seed from whether the conversation already has history — a
// resumed conversation is mid-task, so its first LLM call of the new turn
// classifies IN_FLIGHT rather than NEW. EVERY turn-executing entry point
// (Weave and StreamWeave, single- and multi-agent) must install this: a
// bypassed entry point would run unscheduled — jumping every parked waiter —
// while its 429s still lower the scope's shared calibrated ceiling through
// the funnel's capacity observers.
func installTurnSlotInfo(ctx context.Context, resumed bool, sessionID, agentName string) context.Context {
	var priorCalls int64
	if resumed {
		priorCalls = 1
	}
	ctx = llmscheduler.WithSlotInfo(ctx, slotOriginFromMetadata(ctx), priorCalls)
	// Attribution is observability only: a parked slot request that names its
	// conversation is actionable, an anonymous one is a number.
	return llmscheduler.WithIdentity(ctx, sessionID, agentName)
}

// doorParkLogThreshold separates the un-parked fast path (a mutex
// acquisition, microseconds) from an actual park at the door: an admission
// that took at least this long waited behind the active ceiling and is worth
// a debug line.
const doorParkLogThreshold = time.Millisecond

// enterTurnDoor admits one batch-origin conversation turn through the
// process-wide door gate (llmscheduler.Door): batch turns beyond the
// configured active ceiling park FIFO at the front door — starving a turn at
// the door is free, starving it mid-task wastes held resources and partial
// work. Interactive turns bypass entirely (the slot scheduler's interactive
// headroom protects their capacity). A full door queue surfaces as
// RESOURCE_EXHAUSTED backpressure, never silence.
//
// EVERY turn-executing entry point (Weave and StreamWeave, single- and
// multi-agent alike) must call this right after installTurnSlotInfo: a
// bypassed entry point would make max_active_conversations a suggestion, not
// a ceiling. On success the returned release is non-nil and idempotent;
// callers defer it for the life of the turn. logger may be nil.
//
// Observability: rejections log at Warn with the live door counters; a turn
// that actually parked logs its door wait at Debug. There is no span seam
// before admission (entry-point spans start after the turn is admitted), so
// the wait is surfaced through the door_wait log field and the SlotState
// proto counters rather than a new tracing seam.
func enterTurnDoor(ctx context.Context, logger *zap.Logger) (release func(), err error) {
	if slotOriginFromMetadata(ctx) == loomv1.SlotOrigin_SLOT_ORIGIN_INTERACTIVE {
		return func() {}, nil
	}
	door := llmscheduler.Door()
	start := time.Now()
	release, err = door.Enter(ctx)
	if err != nil {
		if errors.Is(err, llmscheduler.ErrDoorFull) {
			if logger != nil {
				active, queued := door.DoorState()
				logger.Warn("door admission rejected: queue full",
					zap.Int("active_conversations", active),
					zap.Int("door_queue_depth", queued))
			}
			return nil, status.Error(codes.ResourceExhausted, "server at capacity: conversation door queue full, retry later")
		}
		return nil, err
	}
	if wait := time.Since(start); wait >= doorParkLogThreshold && logger != nil {
		active, queued := door.DoorState()
		logger.Debug("door admission: turn parked at the front door",
			zap.Duration("door_wait", wait),
			zap.Int("active_conversations", active),
			zap.Int("door_queue_depth", queued))
	}
	return release, nil
}
