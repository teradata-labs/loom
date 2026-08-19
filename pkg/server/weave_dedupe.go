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
	"sync"
	"sync/atomic"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/grpc/metadata"
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
