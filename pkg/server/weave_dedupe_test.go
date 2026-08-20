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
// Tests for idempotency-key weave dedupe (MCP 2026-07-28 migration, D1):
// a re-issued request with the same key joins the original run instead of
// executing the turn twice.
package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func keyedCtx(user, key string) context.Context {
	ctx := context.Background()
	if user != "" {
		ctx = postgres.ContextWithUserID(ctx, user)
	}
	if key != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(types.IdempotencyKeyMetadataKey, key))
	}
	return ctx
}

func newDedupeServer(llm *mockLLMProvider) *MultiAgentServer {
	ag := createTestAgentWithLLM(llm)
	return NewMultiAgentServer(map[string]*agent.Agent{"agent-1": ag}, nil)
}

func TestWeaveDedupeSequentialDuplicateReturnsCachedResult(t *testing.T) {
	llm := &mockLLMProvider{responses: []string{"answer-one", "answer-two"}}
	srv := newDedupeServer(llm)

	first, err := srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)

	second, err := srv.Weave(keyedCtx("user-a", "key-1"), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)
	assert.Equal(t, first.Text, second.Text, "duplicate must return the original run's result")
	assert.Equal(t, first.SessionId, second.SessionId)
}

func TestWeaveDedupeConcurrentDuplicatesJoinOneRun(t *testing.T) {
	llm := &mockLLMProvider{responses: []string{"only-answer"}}
	srv := newDedupeServer(llm)

	const n = 8
	results := make([]*loomv1.WeaveResponse, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = srv.Weave(keyedCtx("user-a", "key-concurrent"), &loomv1.WeaveRequest{Query: "q"})
		}(i)
	}
	wg.Wait()

	var sessionIDs []string
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "call %d", i)
		require.NotNil(t, results[i])
		sessionIDs = append(sessionIDs, results[i].SessionId)
	}
	for _, id := range sessionIDs {
		assert.Equal(t, sessionIDs[0], id, "all duplicates must join the same run (same session)")
	}
}

func TestWeaveDedupeScopedPerUser(t *testing.T) {
	llm := &mockLLMProvider{responses: []string{"a1", "a2"}}
	srv := newDedupeServer(llm)

	first, err := srv.Weave(keyedCtx("user-a", "shared-key"), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)
	second, err := srv.Weave(keyedCtx("user-b", "shared-key"), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)

	assert.NotEqual(t, first.SessionId, second.SessionId,
		"the same key from different users must never join runs")
}

func TestWeaveNoKeyMeansNoDedupe(t *testing.T) {
	llm := &mockLLMProvider{responses: []string{"r1", "r2"}}
	srv := newDedupeServer(llm)

	first, err := srv.Weave(keyedCtx("user-a", ""), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)
	second, err := srv.Weave(keyedCtx("user-a", ""), &loomv1.WeaveRequest{Query: "q"})
	require.NoError(t, err)
	assert.NotEqual(t, first.SessionId, second.SessionId, "key-less callers get at-least-once semantics")
}

func TestWeaveDeduperExpiry(t *testing.T) {
	d := newWeaveDeduper()

	entry, owner, _ := d.begin("scope")
	require.True(t, owner)
	entry.finish(&loomv1.WeaveResponse{Text: "done"}, nil)

	// Within TTL: joined.
	_, owner, _ = d.begin("scope")
	assert.False(t, owner)

	// Force expiry: a fresh begin becomes owner again.
	d.mu.Lock()
	d.entries["scope"].expiresAtNano.Store(time.Now().Add(-time.Second).UnixNano())
	d.mu.Unlock()
	_, owner, _ = d.begin("scope")
	assert.True(t, owner, "expired entries are swept lazily")
}

// TestDedupeBoundedAdmission (review finding 12, PR #328): oversized keys
// and a saturated registry are not admitted — the weave runs without dedupe
// (at-least-once) instead of growing the map without bound.
func TestDedupeBoundedAdmission(t *testing.T) {
	d := newWeaveDeduper()

	_, _, admitted := d.begin(strings.Repeat("k", weaveDedupeMaxKeyLen+1))
	assert.False(t, admitted, "oversized keys are not deduplicable")

	// Fill the registry with in-flight entries (never evictable).
	for i := 0; i < weaveDedupeMaxEntries; i++ {
		_, isOwner, ok := d.begin(fmt.Sprintf("user\x00key-%d", i))
		require.True(t, ok)
		require.True(t, isOwner)
	}
	_, _, admitted = d.begin("user\x00one-more")
	assert.False(t, admitted, "a registry full of in-flight runs must refuse admission, not evict joinable entries")

	// Completing one entry frees a slot via completed-eviction.
	entry, isOwner, ok := d.begin("user\x00key-0")
	require.True(t, ok)
	require.False(t, isOwner)
	_ = entry
	d.mu.Lock()
	d.entries["user\x00key-0"].finish(nil, nil)
	d.mu.Unlock()

	_, isOwner, admitted = d.begin("user\x00after-completion")
	assert.True(t, admitted, "eviction of a completed entry must free a slot")
	assert.True(t, isOwner)
}

func TestDedupeReleasesCancellationOutcomes(t *testing.T) {
	d := newWeaveDeduper()
	scope := dedupeScope("user-a", "key-1")

	// Owner's stream dies mid-run: the cancellation must not be cached.
	entry, isOwner, admitted := d.begin(scope)
	require.True(t, admitted)
	require.True(t, isOwner)
	d.finishAndRelease(scope, entry, nil, context.Canceled)

	// A joiner that was already waiting still observes the error.
	_, err := awaitDedupeResult(context.Background(), entry)
	assert.ErrorIs(t, err, context.Canceled)

	// The re-issue re-executes: it becomes a fresh owner, never a joiner.
	entry2, isOwner2, admitted2 := d.begin(scope)
	require.True(t, admitted2)
	assert.True(t, isOwner2, "re-issue after a canceled run must re-execute, not join the cached cancellation")
	d.finishAndRelease(scope, entry2, &loomv1.WeaveResponse{Text: "ok"}, nil)

	// gRPC-shaped cancellations release too.
	scope2 := dedupeScope("user-a", "key-2")
	e3, _, _ := d.begin(scope2)
	d.finishAndRelease(scope2, e3, nil, status.Error(codes.DeadlineExceeded, "deadline"))
	_, own4, _ := d.begin(scope2)
	assert.True(t, own4)
}

func TestDedupeKeepsDeterministicErrorsJoinable(t *testing.T) {
	d := newWeaveDeduper()
	scope := dedupeScope("user-a", "key-1")

	entry, _, _ := d.begin(scope)
	d.finishAndRelease(scope, entry, nil, status.Error(codes.InvalidArgument, "bad query"))

	// A deterministic failure is a durable outcome: the re-issue joins it
	// instead of burning a second run on the same doomed input.
	joined, isOwner, admitted := d.begin(scope)
	require.True(t, admitted)
	require.False(t, isOwner)
	_, err := awaitDedupeResult(context.Background(), joined)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
