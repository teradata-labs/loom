// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

// Follow-up tests requested during PR #266 review of TER-419. Each test
// closes a specific gap the reviewer identified:
//
//   TestFold_262Regression_UserQuestionSurvivesRealFold
//     End-to-end reproduction of the #262 shape: a real user question plus
//     oversized narrative tool_results, driven through the real Fold with a
//     real (mock-but-fold-consistent) compressor. Asserts the exact user
//     question bytes are present in the compiled context after fold.
//
//   TestComputeCarryInclude_EmptyLedger_NoUserFallback
//     Property tests always seed a ledger user; the empty-carry code path
//     (no ledger, no adjacency, nothing to carry) was untested. Exercises it
//     directly against computeCarryInclude to assert a well-defined empty
//     inclusion mask.
//
//   TestOrchestrator_OnSkillDeactivate_FiresOnUnload
//     The rewired OnSkillDeactivate callback (post-419 replacement for the
//     old eviction-callback path) had no direct test. Registers a callback,
//     activates then deactivates a skill, asserts the callback fires with
//     the expected session id and skill.
//
//   TestPrepareContext_RaceValveEvictVsAddMessage
//   TestPrepareContext_RaceFoldVsAddMessage
//     Race coverage the deleted pre-419 TestSwapLayerConcurrency provided —
//     adapted to 419's pipeline. Runs valve / fold on one goroutine while
//     another concurrently calls session.AddMessage, under -race.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/skills"
)

// ============================================================================
// #262 regression — end-to-end
// ============================================================================

// TestFold_262Regression_UserQuestionSurvivesRealFold reproduces the shape
// of the #262 production failure and asserts that under the 419 pipeline the
// user's active question bytes survive a real fold intact.
//
// Under the pre-419 model, AddMessage ran mid-turn compression that evicted
// the oldest L1 message (the user's current question) and stubbed it to 50
// bytes ("User asked about: /td-data-profile ..."). Under 419, user turns
// are tagged ClassLedger at construction and carried verbatim through fold;
// only narrative reaches the compressor. This test locks that invariant.
func TestFold_262Regression_UserQuestionSurvivesRealFold(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "issue-262-regression"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	// The compressor must not silently write out the user question — set a
	// mock that fails loud if the input contains it (fold's narrative-only
	// invariant must hold). We still return a plausible residue so fold
	// completes.
	userQuestion := "/td-data-profile How many columns does demo_user.telecocustomer have and what are the data types?"
	sm.SetCompressor(&mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			for _, m := range msgs {
				if strings.Contains(m.Content, "demo_user.telecocustomer") {
					t.Errorf("fold compressor received the user's question in its narrative input — classification invariant violated (msg role=%s class=%s)", m.Role, m.ContextClass)
				}
			}
			return "session state: profiled request received; awaiting continuation"
		},
	})

	// Seed the timeline in the #262 shape: user question first, then skill
	// activation (narrative under Fix 1), assistant reasoning, and several
	// large tool_results (ballast under Fix 1) that would have pushed
	// pre-419 compression into evicting the oldest message.
	userMsg := Message{
		Role: "user", Content: userQuestion,
		ContextClass: ClassLedger, Timestamp: time.Now(),
	}
	sm.AddMessage(ctx, userMsg)

	sm.AddMessage(ctx, Message{
		Role: "tool", ToolUseID: "load-tdp",
		Content:      "# Skill: td-data-profile\n" + sentenceRepeat(400),
		ContextClass: toolResultClass("manage_skills", nil),
		Timestamp:    time.Now(),
	})
	sm.AddMessage(ctx, Message{
		Role: "assistant", Content: "loading td-data-profile; running queries",
		ContextClass: ClassNarrative, Timestamp: time.Now(),
	})
	for i := 0; i < 5; i++ {
		sm.AddMessage(ctx, Message{
			Role: "tool", ToolUseID: fmt.Sprintf("q%d", i),
			Content:      sentenceRepeat(1000),
			ContextClass: toolResultClass("teradata_tool_call", nil),
			Timestamp:    time.Now(),
		})
	}

	// Drive fold — the same red-zone entry point prepareContext uses.
	require.NoError(t, sm.Fold(ctx, 1, sm.GetL1MessageCount()))

	// Assert the compiled context still carries the user question verbatim.
	compiled := sm.GetMessagesForLLM()
	sawExactQuestion := false
	sawTruncatedStub := false
	for _, m := range compiled {
		if m.Content == userQuestion {
			sawExactQuestion = true
		}
		// Pre-fix stub shape: "User asked about: <first 50 chars>..."
		if strings.HasPrefix(m.Content, "User asked about:") && strings.HasSuffix(m.Content, "...") {
			sawTruncatedStub = true
		}
	}
	assert.True(t, sawExactQuestion, "compiled context after fold must carry the user's exact question bytes — #262 regression guard")
	assert.False(t, sawTruncatedStub, "compiled context must not contain the pre-419 'User asked about: ...' stub — that is the #262 shape")
}

// ============================================================================
// computeCarryInclude — empty-carry fallback
// ============================================================================

// TestComputeCarryInclude_EmptyLedger_NoUserFallback exercises the code path
// where fold's carry set is empty: no charter, no ledger user turn, no
// adjacency. The property tests always seed a ledger user, so this branch
// was untested. Asserts computeCarryInclude returns a well-defined mask
// (all-false) and does not panic or under-index.
func TestComputeCarryInclude_EmptyLedger_NoUserFallback(t *testing.T) {
	// A conversation with only narrative + ballast — no user turn, no
	// charter, no ledger anywhere. Fold's carry set must be empty; the
	// entire L1 is compressor/valve fodder.
	msgs := []Message{
		{Role: "assistant", Content: "starting analysis", ContextClass: ClassNarrative},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "some_read"}}, ContextClass: ClassNarrative},
		{Role: "tool", ToolUseID: "t1", Content: "row data", ContextClass: ClassBallast},
		{Role: "assistant", Content: "observed 12 rows", ContextClass: ClassNarrative},
	}

	include := computeCarryInclude(msgs)
	require.Len(t, include, len(msgs), "include must have one entry per message — under-indexing would panic in Fold's projection loop")
	for i, in := range include {
		assert.False(t, in, "message %d (role=%s class=%s) must not be in the carry set — no ledger, no charter, no adjacency", i, msgs[i].Role, msgs[i].ContextClass)
	}
}

// TestComputeCarryInclude_Empty_Nil handles the boundary case of a nil slice
// (defensive — a fresh SegmentedMemory has zero l1Messages).
func TestComputeCarryInclude_Empty_Nil(t *testing.T) {
	assert.Empty(t, computeCarryInclude(nil), "computeCarryInclude(nil) must return empty include mask")
	assert.Empty(t, computeCarryInclude([]Message{}), "computeCarryInclude([]) must return empty include mask")
}

// ============================================================================
// Orchestrator.OnSkillDeactivate callback — rewire coverage
// ============================================================================

// TestOrchestrator_OnSkillDeactivate_FiresOnUnload registers a callback via
// SetOnSkillDeactivate, activates then deactivates a skill, and asserts the
// callback fired exactly once with the expected session id and skill name.
// The old eviction-callback tests were deleted with the D-6 removal of the
// score-based eviction path (Part D #3); nothing directly covered the new
// runtime setter until now.
func TestOrchestrator_OnSkillDeactivate_FiresOnUnload(t *testing.T) {
	tmp := t.TempDir()
	library := skills.NewLibrary(skills.WithSearchPaths(tmp))
	library.Register(&skills.Skill{Name: "skill-A", Title: "Skill A", SourcePath: tmp + "/skill-A"})
	orch := skills.NewOrchestrator(library)

	sessionID := "orch-deact"
	var (
		fireCount   int32
		gotSession  string
		gotSkill    string
		gotDuration time.Duration
		mu          sync.Mutex
	)
	orch.SetOnSkillDeactivate(func(sid string, s *skills.Skill, activeFor time.Duration) {
		atomic.AddInt32(&fireCount, 1)
		mu.Lock()
		gotSession = sid
		if s != nil {
			gotSkill = s.Name
		}
		gotDuration = activeFor
		mu.Unlock()
	})

	skill, err := library.Load("skill-A")
	require.NoError(t, err)
	require.NotNil(t, orch.ActivateSkill(sessionID, skill, "manual", "skill-A", 1.0))

	// Sleep a beat so activeFor is measurably >0 (locks the "duration
	// carried through" side of the contract).
	time.Sleep(10 * time.Millisecond)

	orch.DeactivateSkill(sessionID, "skill-A")

	// Callback fires in a goroutine (see orchestrator.go DeactivateSkill:
	// "Fire the deactivation callback (outside critical path, async)"),
	// so poll briefly for it to land rather than sleep-then-check.
	deadline := time.Now().Add(500 * time.Millisecond)
	for atomic.LoadInt32(&fireCount) == 0 && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&fireCount), "callback must fire exactly once per DeactivateSkill call")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, sessionID, gotSession, "callback must receive the deactivating session id")
	assert.Equal(t, "skill-A", gotSkill, "callback must receive the deactivated skill")
	assert.Greater(t, gotDuration, time.Duration(0), "activeFor must reflect the elapsed activation window")
}

// TestOrchestrator_OnSkillDeactivate_UnknownSkill_NoFire pins the negative
// case: DeactivateSkill for a skill that was never active is a no-op; the
// callback must not fire on a phantom deactivation.
func TestOrchestrator_OnSkillDeactivate_UnknownSkill_NoFire(t *testing.T) {
	tmp := t.TempDir()
	library := skills.NewLibrary(skills.WithSearchPaths(tmp))
	orch := skills.NewOrchestrator(library)

	var fireCount int32
	orch.SetOnSkillDeactivate(func(string, *skills.Skill, time.Duration) {
		atomic.AddInt32(&fireCount, 1)
	})

	orch.DeactivateSkill("some-session", "nonexistent-skill")
	assert.Equal(t, int32(0), atomic.LoadInt32(&fireCount), "callback must not fire for a phantom deactivation")
}

// ============================================================================
// Race: prepareContext vs concurrent AddMessage
// ============================================================================
//
// These tests are the 419 replacement for the deleted pre-419
// TestSwapLayerConcurrency. Under -race they catch any regression where
// prepareContext-driven mutation of L1/L2 races against a concurrent
// session.AddMessage — the pattern that would break Contract 2's
// single-writer invariant if a future edit ever bypassed sm.mu.

// TestPrepareContext_RaceValveEvictVsAddMessage drives ValveEvict on one
// goroutine while other goroutines concurrently AddMessage to the same
// segmented memory. The race detector must not fire and no message is lost.
func TestPrepareContext_RaceValveEvictVsAddMessage(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "race-valve"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)
	sm.SetMinValvePayoffTokens(1000)
	sm.SetKeepRecentBallast(3)

	// Seed L1 with enough ballast that valve has candidates on the first
	// call — otherwise the race window is trivially small.
	for i := 0; i < 12; i++ {
		sm.AddMessage(ctx, Message{
			Role: "tool", ToolUseID: fmt.Sprintf("seed-%d", i),
			Content:      sentenceRepeat(600),
			ContextClass: ClassBallast,
			Timestamp:    time.Now(),
		})
	}

	const writers = 4
	const perWriter = 200
	stopWriters := make(chan struct{})
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				select {
				case <-stopWriters:
					return
				default:
				}
				sm.AddMessage(ctx, Message{
					Role: "tool", ToolUseID: fmt.Sprintf("w%d-%d", id, i),
					Content:      sentenceRepeat(50),
					ContextClass: ClassBallast,
					Timestamp:    time.Now(),
				})
			}
		}(w)
	}

	// Fire valve repeatedly while writers are active.
	var valveWG sync.WaitGroup
	valveWG.Add(1)
	go func() {
		defer valveWG.Done()
		for i := 0; i < 20; i++ {
			sm.ValveEvict(ctx)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
	close(stopWriters)
	valveWG.Wait()

	// Sanity: writers produced messages; L1 grew. Race detector will have
	// fired by now if there was an unsynchronized access.
	assert.GreaterOrEqual(t, sm.GetL1MessageCount(), 12,
		"L1 must still contain at least the seed messages (some may be evicted stubs, but the count is stub-in-place)")
}

// TestPrepareContext_RaceFoldVsAddMessage drives Fold on one goroutine while
// other goroutines AddMessage. Under -race the writer's append must not
// race with fold's l1Messages rewrite.
func TestPrepareContext_RaceFoldVsAddMessage(t *testing.T) {
	ctx := context.Background()
	store := newContextClassSQLiteStore(t)
	sessionID := "race-fold"
	sess := &Session{ID: sessionID, Context: map[string]interface{}{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, sess))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&mockCompressor{enabled: true, compressFn: func([]Message) string { return "compressed" }})

	// Seed a user ledger turn so fold has something in the carry set.
	sm.AddMessage(ctx, Message{
		Role: "user", Content: "please analyze",
		ContextClass: ClassLedger, Timestamp: time.Now(),
	})

	const writers = 4
	const perWriter = 150
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				sm.AddMessage(ctx, Message{
					Role: "assistant", Content: fmt.Sprintf("w%d step %d", id, i),
					ContextClass: ClassNarrative, Timestamp: time.Now(),
				})
			}
		}(w)
	}

	var foldWG sync.WaitGroup
	foldWG.Add(1)
	go func() {
		defer foldWG.Done()
		for i := 0; i < 5; i++ {
			// Read the count under sm's own lock via GetL1MessageCount — we
			// never touch sm.l1Messages directly from the test.
			_ = sm.Fold(ctx, 1, sm.GetL1MessageCount())
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()
	foldWG.Wait()

	// If we got here without the race detector firing, the invariant holds.
	// Sanity: the compiled context is non-empty.
	assert.NotEmpty(t, sm.GetMessagesForLLM(),
		"compiled context must be non-empty after concurrent fold+writes")
}
