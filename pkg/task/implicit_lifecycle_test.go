// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"context"
	"fmt"
	"github.com/teradata-labs/loom/pkg/taskctx"
	"github.com/teradata-labs/loom/pkg/types"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// The emitter's three maps grow with the conversation and are freed only by
// EndTurn (per turn) and ForgetSession (per session). Both had zero callers
// when this file was written, so on a long-running server the emitter grew for
// the life of the process. These tests hold the reclamation contract:
//
//   - EndTurn drains the per-turn memo WITHOUT resetting the cap counter. If it
//     reset the counter, MaxPerSession would become unenforceable — every turn
//     would start from zero and a single session could mint without bound.
//   - ForgetSession drains all three maps, including the board entry, which in
//     the default configuration is keyed by the session id.
//   - ForgetSession does NOT drop a configured shared board, whose id is not a
//     session id.

// --- fake store -------------------------------------------------------------

// lifecycleStore is the minimum TaskStore the emitter's mint path touches:
// idempotency lookup, task insert, and board probe/create. Everything else is
// a stub — these tests assert on the emitter's in-memory maps, not on rows.
type lifecycleStore struct {
	mu     sync.Mutex
	byKey  map[string]*Task
	boards map[string]*TaskBoard
	nextID int
}

// taskCount reports how many distinct tasks the store holds, under its lock.
func (s *lifecycleStore) taskCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byKey)
}

func newLifecycleStore() *lifecycleStore {
	return &lifecycleStore{
		byKey:  map[string]*Task{},
		boards: map[string]*TaskBoard{},
	}
}

func (s *lifecycleStore) CreateTask(_ context.Context, t *Task) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	out := *t
	out.ID = fmt.Sprintf("task-%d", s.nextID)
	if out.SkillIdempotencyKey != "" {
		s.byKey[out.SkillIdempotencyKey] = &out
	}
	return &out, nil
}

func (s *lifecycleStore) GetTaskByIdempotencyKey(_ context.Context, key string) (*Task, error) {
	if key == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.byKey[key]; ok {
		return t, nil
	}
	return nil, nil
}

func (s *lifecycleStore) CreateBoard(_ context.Context, b *TaskBoard) (*TaskBoard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := *b
	s.boards[out.ID] = &out
	return &out, nil
}

func (s *lifecycleStore) GetBoard(_ context.Context, id string) (*TaskBoard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.boards[id]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("board %q not found", id)
}

func (s *lifecycleStore) boardCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.boards)
}

// Unused interface methods — stubs to satisfy TaskStore.
// GetTask is REAL in this fake for the same reason CloseTask is: a nil return
// made CompleteForTurn bail before its close, so a "completed" task silently
// stayed open and any test about spent keys asserted against nothing.
func (s *lifecycleStore) GetTask(_ context.Context, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.byKey {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, nil
}
func (s *lifecycleStore) HasOpenSkillTasks(context.Context, string, string) (bool, error) {
	return false, nil
}
func (s *lifecycleStore) ListBySkillRun(context.Context, string, string) ([]*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) UpdateTask(context.Context, *Task, []string) (*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) SetAcceptanceCriteria(context.Context, string, string) (*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) DeleteTask(context.Context, string) error { return nil }
func (s *lifecycleStore) ListTasks(context.Context, ListTasksOpts) ([]*Task, int, error) {
	return nil, 0, nil
}
func (s *lifecycleStore) ClaimTask(context.Context, string, string, string) (*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) ReleaseTask(context.Context, string, string) (*Task, error) {
	return nil, nil
}

// CloseTask is REAL in this fake — a no-op here made a spent-key test pass
// against a task that never actually closed, the exact fixture-supplied-
// invariant failure the round-4 review called out in two other tests.
func (s *lifecycleStore) CloseTask(_ context.Context, id, reason string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.byKey {
		if t.ID == id {
			t.Status = loomv1.TaskStatus_TASK_STATUS_DONE
			t.CloseReason = reason
			return t, nil
		}
	}
	return nil, nil
}
func (s *lifecycleStore) TransitionTask(context.Context, string, loomv1.TaskStatus) (*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) AddDependency(context.Context, *TaskDependency) error   { return nil }
func (s *lifecycleStore) RemoveDependency(context.Context, string, string) error { return nil }
func (s *lifecycleStore) GetDependencies(context.Context, string) ([]*TaskDependency, error) {
	return nil, nil
}
func (s *lifecycleStore) GetDependents(context.Context, string) ([]*TaskDependency, error) {
	return nil, nil
}
func (s *lifecycleStore) GetReadyFront(context.Context, string, ReadyFrontOpts) ([]*Task, error) {
	return nil, nil
}
func (s *lifecycleStore) GetBlockedTasks(context.Context, string) ([]*Task, error) { return nil, nil }
func (s *lifecycleStore) ListBoards(context.Context) ([]*TaskBoard, error)         { return nil, nil }
func (s *lifecycleStore) RecordHistory(context.Context, *TaskHistoryEntry) error   { return nil }
func (s *lifecycleStore) GetHistory(context.Context, string) ([]*TaskHistoryEntry, error) {
	return nil, nil
}
func (s *lifecycleStore) Close() error { return nil }

// --- locked map readers -----------------------------------------------------
//
// The emitter's maps are mutex-guarded and these tests run under -race, so
// every read takes e.mu. Defined here rather than in implicit.go: this is test
// observability, not production surface.

func (e *ImplicitEmitter) sizes() (minted, perSession, boards int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.minted), len(e.perSession), len(e.boardsKnown)
}

func (e *ImplicitEmitter) capFor(sessionID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.perSession[sessionID]
}

func (e *ImplicitEmitter) knowsBoard(boardID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.boardsKnown[boardCacheKey(context.Background(), boardID)]
	return ok
}

// --- rig --------------------------------------------------------------------

func newLifecycleEmitter(t *testing.T) (*ImplicitEmitter, *lifecycleStore) {
	t.Helper()
	store := newLifecycleStore()
	mgr := NewManager(store, nil, nil, zap.NewNop())
	return NewImplicitEmitter(mgr, ResolveImplicitPolicy(nil), nil, zap.NewNop()), store
}

func sessionIDs(m int) []string {
	out := make([]string, m)
	for i := range out {
		out[i] = fmt.Sprintf("sess-%d", i)
	}
	return out
}

// mintTurns runs n turns for each of the given sessions. boardFor chooses the
// board id, so a caller can model either the default (board == session) or a
// configured shared board.
func mintTurns(t *testing.T, e *ImplicitEmitter, sessions []string, n int, boardFor func(string) string) {
	t.Helper()
	for _, sid := range sessions {
		for turn := 0; turn < n; turn++ {
			_, created, err := e.EnsureForTurn(context.Background(), TurnRequest{
				SessionID:   sid,
				AgentID:     "agent-1",
				BoardID:     boardFor(sid),
				TurnIndex:   turn,
				Trigger:     trToolCall,
				UserMessage: fmt.Sprintf("do work %d", turn),
			})
			if err != nil {
				t.Fatalf("EnsureForTurn(%s, turn %d): %v", sid, turn, err)
			}
			if created == nil {
				t.Fatalf("EnsureForTurn(%s, turn %d) minted nothing; the rig must mint on every turn", sid, turn)
			}
		}
	}
}

func boardIsSession(sid string) string { return sid }

// --- tests ------------------------------------------------------------------

// TestImplicitEmitter_EndTurnDrainsMemoAndKeepsCap is the per-turn half of the
// reclamation contract. A turn's memo must go; the session's cap must stay.
func TestImplicitEmitter_EndTurnDrainsMemoAndKeepsCap(t *testing.T) {
	const turns, sessions = 3, 4
	e, _ := newLifecycleEmitter(t)
	ids := sessionIDs(sessions)

	mintTurns(t, e, ids, turns, boardIsSession)

	minted, perSession, boards := e.sizes()
	if minted != turns*sessions {
		t.Fatalf("expected %d memo entries before cleanup, got %d", turns*sessions, minted)
	}
	if perSession != sessions {
		t.Fatalf("expected %d cap counters, got %d", sessions, perSession)
	}
	if boards != sessions {
		t.Fatalf("expected %d cached boards (board id defaults to session id), got %d", sessions, boards)
	}

	for _, sid := range ids {
		for turn := 0; turn < turns; turn++ {
			e.EndTurn(sid, turn)
		}
	}

	minted, perSession, boards = e.sizes()
	if minted != 0 {
		t.Errorf("EndTurn must drain the per-turn memo; %d entries left", minted)
	}
	// The cap counter is deliberately NOT reset: EndTurn releases a turn, not a
	// session's budget. Resetting it would make MaxPerSession unenforceable.
	if perSession != sessions {
		t.Errorf("EndTurn must NOT drop cap counters, or the per-session limit becomes unenforceable; %d of %d left", perSession, sessions)
	}
	for _, sid := range ids {
		if got := e.capFor(sid); got != turns {
			t.Errorf("cap counter for %s: want %d after EndTurn, got %d", sid, turns, got)
		}
	}
	// EndTurn is a per-TURN release and says nothing about boards.
	if boards != sessions {
		t.Errorf("EndTurn must leave the board cache alone; want %d, got %d", sessions, boards)
	}
}

// TestImplicitEmitter_ForgetSessionDrainsAllThreeMaps is the leak this file
// exists for: on the default configuration the board id IS the session id, so
// all three maps accumulate one-per-session and all three must drain.
func TestImplicitEmitter_ForgetSessionDrainsAllThreeMaps(t *testing.T) {
	const turns, sessions = 5, 6
	e, store := newLifecycleEmitter(t)
	ids := sessionIDs(sessions)

	mintTurns(t, e, ids, turns, boardIsSession)

	if minted, perSession, boards := e.sizes(); minted == 0 || perSession == 0 || boards == 0 {
		t.Fatalf("rig minted nothing to reclaim: minted=%d perSession=%d boards=%d", minted, perSession, boards)
	}
	// Sanity: the boards really are session-derived, which is what makes the
	// session-keyed delete in ForgetSession the right thing.
	if store.boardCount() != sessions {
		t.Fatalf("expected one auto-created board per session, got %d", store.boardCount())
	}
	for _, sid := range ids {
		if !e.knowsBoard(sid) {
			t.Fatalf("board cache should hold the session-derived board %q", sid)
		}
	}

	for _, sid := range ids {
		e.ForgetSession(sid)
	}

	minted, perSession, boards := e.sizes()
	if minted != 0 {
		t.Errorf("ForgetSession must drain the per-turn memo; %d entries left", minted)
	}
	if perSession != 0 {
		t.Errorf("ForgetSession must drain the cap counters; %d entries left", perSession)
	}
	if boards != 0 {
		t.Errorf("ForgetSession must drain the board cache for session-derived boards; %d entries left", boards)
	}
}

// TestImplicitEmitter_ForgetSessionKeepsConfiguredBoard is the other side of
// the boardsKnown delete: a board id an operator configured is not a session
// id, so retiring sessions must not evict it.
func TestImplicitEmitter_ForgetSessionKeepsConfiguredBoard(t *testing.T) {
	const turns, sessions = 2, 3
	const sharedBoard = "team-board"
	e, _ := newLifecycleEmitter(t)
	ids := sessionIDs(sessions)

	mintTurns(t, e, ids, turns, func(string) string { return sharedBoard })

	if _, _, boards := e.sizes(); boards != 1 {
		t.Fatalf("a shared board must be cached once, not per session; got %d entries", boards)
	}

	for _, sid := range ids {
		e.ForgetSession(sid)
	}

	minted, perSession, boards := e.sizes()
	if minted != 0 || perSession != 0 {
		t.Errorf("ForgetSession must still drain the session-keyed maps: minted=%d perSession=%d", minted, perSession)
	}
	if boards != 1 || !e.knowsBoard(sharedBoard) {
		t.Errorf("ForgetSession must not evict a configured shared board; boards=%d knows=%v", boards, e.knowsBoard(sharedBoard))
	}
}

// TestImplicitEmitter_ReclamationIsRaceFree exercises the mutex the reclamation
// paths share with the mint path. Meaningful only under -race, which this repo
// always runs.
func TestImplicitEmitter_ReclamationIsRaceFree(t *testing.T) {
	e, _ := newLifecycleEmitter(t)

	var wg sync.WaitGroup
	for s := 0; s < 8; s++ {
		sid := fmt.Sprintf("sess-%d", s)
		wg.Add(3)
		go func() {
			defer wg.Done()
			for turn := 0; turn < 10; turn++ {
				_, _, _ = e.EnsureForTurn(context.Background(), TurnRequest{
					SessionID: sid,
					BoardID:   sid,
					TurnIndex: turn,
					Trigger:   trToolCall,
				})
			}
		}()
		go func() {
			defer wg.Done()
			for turn := 0; turn < 10; turn++ {
				e.EndTurn(sid, turn)
			}
		}()
		go func() {
			defer wg.Done()
			e.ForgetSession(sid)
		}()
	}
	wg.Wait()

	// Whatever interleaving won, a final sweep must leave nothing behind.
	for s := 0; s < 8; s++ {
		e.ForgetSession(fmt.Sprintf("sess-%d", s))
	}
	if minted, perSession, boards := e.sizes(); minted != 0 || perSession != 0 || boards != 0 {
		t.Errorf("emitter must be empty after every session is forgotten: minted=%d perSession=%d boards=%d",
			minted, perSession, boards)
	}
}

// mintAt runs one turn with an explicit session epoch and returns the task it
// resolved to — minted fresh or rebound through the durable key.
func mintAt(t *testing.T, e *ImplicitEmitter, sid string, turn int, epoch int64, msg string) *Task {
	t.Helper()
	_, created, err := e.EnsureForTurn(context.Background(), TurnRequest{
		SessionID:    sid,
		AgentID:      "agent-1",
		BoardID:      sid,
		TurnIndex:    turn,
		SessionEpoch: epoch,
		Trigger:      trToolCall,
		UserMessage:  msg,
	})
	if err != nil {
		t.Fatalf("EnsureForTurn(%s, turn %d, epoch %d): %v", sid, turn, epoch, err)
	}
	if created == nil {
		t.Fatalf("EnsureForTurn(%s, turn %d, epoch %d) resolved no task; the rig must resolve one on every turn", sid, turn, epoch)
	}
	return created
}

// TestImplicitEmitter_RecreatedSessionDoesNotInheritThePriorTask reproduces the
// review finding: session ids come from outside and may be reused, and the
// durable idempotency key was only (session, turn) — so a session deleted and
// recreated under the same id started again at turn 0, matched the OLD key, and
// CreateTaskIdempotent handed the new conversation its predecessor's terminal
// task. New messages then attached to a task marked DONE before they existed,
// titled with the previous conversation's opening message.
//
// SessionEpoch is the fix: the durable key now carries the session's creation
// time, so a recreated id is a new incarnation with fresh keys — while the SAME
// incarnation restored after a process restart keeps its keys and rebinds,
// which is what a resume-after-restart requires. Both directions are asserted,
// because either alone can be satisfied by a broken implementation: never
// rebinding passes the first, always rebinding passes the second.
func TestImplicitEmitter_RecreatedSessionDoesNotInheritThePriorTask(t *testing.T) {
	e, store := newLifecycleEmitter(t)

	// Epochs are derived here EXACTLY as production derives them —
	// CreatedAt.UnixNano() of two back-to-back creations — rather than
	// hard-coded, because the hard-coded version proved vacuous: at the old
	// second resolution (CreatedAt.Unix()), a delete-and-recreate inside one
	// wall-clock second produced EQUAL epochs and rebound to the dead task,
	// while the test's invented 1000/2000 sailed past. The assertion below
	// fails if the derivation ever coarsens again.
	epochFirst := time.Now().UnixNano()
	epochSecond := time.Now().UnixNano()
	// Two back-to-back Now() calls CAN land on one clock tick (observed on
	// macOS: ~500ns granularity), so wait out the tick the way any real
	// recreation does — deleting and recreating a session writes to a store,
	// which no platform completes inside one tick. What this loop documents is
	// that the epoch's collision window is now one clock tick, down from one
	// SECOND — the old resolution, at which a same-second delete-and-recreate
	// (a normal harness loop shape) rebound new work to the dead task.
	for epochSecond == epochFirst {
		epochSecond = time.Now().UnixNano()
	}

	// First incarnation: mint at turn 0, and the conversation ends.
	first := mintAt(t, e, "sess-reuse", 0, epochFirst, "first conversation")
	e.CompleteForTurn(context.Background(), first.ID, "done")
	e.ForgetSession("sess-reuse")

	// Same id recreated: a NEW conversation, new CreatedAt, back at turn 0.
	second := mintAt(t, e, "sess-reuse", 0, epochSecond, "second conversation")
	if second.ID == first.ID {
		t.Fatalf("a recreated session id rebound to the prior incarnation's terminal task %s (title %q)", first.ID, first.Title)
	}
	if second.Title != "second conversation" {
		t.Errorf("new task title = %q; a fresh incarnation is named after the NEW conversation's opening message", second.Title)
	}
	if n := store.taskCount(); n < 2 {
		t.Errorf("store holds %d task(s), want 2: both conversations must exist as distinct tasks", n)
	}

	// The other direction: the SAME incarnation after a process restart.
	// ForgetSession stands in for the restart (memory gone, durable rows kept):
	// the restored session carries the same CreatedAt, so the same key, so the
	// turn rebinds to ITS OWN task rather than minting a duplicate.
	e.ForgetSession("sess-reuse")
	rebound := mintAt(t, e, "sess-reuse", 0, epochSecond, "second conversation")
	if rebound.ID != second.ID {
		t.Errorf("the same incarnation restored after a restart minted %s, want a rebind to its own task %s", rebound.ID, second.ID)
	}
}

// --- round-4 emitter concurrency and integrity ------------------------------

// TestImplicitEmitter_CapHoldsUnderConcurrentTurns: the cap check used to be
// check-then-act with three store round trips between the read and the
// increment, so every concurrent turn of one session cleared the check before
// any of them incremented — a cap of 1 admitted 16 concurrent mints. The slot
// is now RESERVED under the same lock that checks it.
func TestImplicitEmitter_CapHoldsUnderConcurrentTurns(t *testing.T) {
	store := newLifecycleStore()
	mgr := NewManager(store, nil, nil, zap.NewNop())
	e := NewImplicitEmitter(mgr, ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		MaxPerSession: 1,
	}), nil, zap.NewNop())

	var wg sync.WaitGroup
	for turn := 0; turn < 16; turn++ {
		wg.Add(1)
		go func(turn int) {
			defer wg.Done()
			_, _, err := e.EnsureForTurn(context.Background(), TurnRequest{
				SessionID:   "sess-cap",
				AgentID:     "agent-1",
				BoardID:     "sess-cap",
				TurnIndex:   turn,
				Trigger:     trToolCall,
				UserMessage: "concurrent work",
			})
			if err != nil {
				t.Errorf("EnsureForTurn(turn %d): %v", turn, err)
			}
		}(turn)
	}
	wg.Wait()

	if n := store.taskCount(); n != 1 {
		t.Fatalf("cap of 1 admitted %d tasks under 16 concurrent turns; the reservation must hold the cap in-process", n)
	}
}

// raceLoserStore simulates losing the create race for one idempotency key: the
// FIRST GetTaskByIdempotencyKey (Manager's pre-lookup) misses, CreateTask then
// fails the unique constraint, and every LATER lookup returns the winner —
// exactly the interleaving two concurrent same-turn triggers produce.
type raceLoserStore struct {
	*lifecycleStore
	winner    *Task
	lookups   int32
	created   int32
	boardGets int32
}

func (s *raceLoserStore) GetTaskByIdempotencyKey(ctx context.Context, key string) (*Task, error) {
	if atomic.AddInt32(&s.lookups, 1) == 1 {
		return nil, nil // the pre-lookup ran before the winner's insert landed
	}
	return s.winner, nil
}

func (s *raceLoserStore) CreateTask(ctx context.Context, t *Task) (*Task, error) {
	atomic.AddInt32(&s.created, 1)
	return nil, fmt.Errorf("UNIQUE constraint failed: tasks.skill_idempotency_key")
}

func (s *raceLoserStore) GetBoard(ctx context.Context, id string) (*TaskBoard, error) {
	atomic.AddInt32(&s.boardGets, 1)
	return s.lifecycleStore.GetBoard(ctx, id)
}

// TestImplicitEmitter_CreateRaceLoserConvergesOnTheWinner: the loser's insert
// fails the unique constraint even though a good row for this turn now exists.
// The loser must re-probe the key and bind the winner — its writers' rows went
// unattributed before — and a bare key conflict must NOT evict the board from
// the cache (that eviction was for genuinely-broken boards, and it was firing
// on every lost race, including against operator-configured shared boards).
func TestImplicitEmitter_CreateRaceLoserConvergesOnTheWinner(t *testing.T) {
	inner := newLifecycleStore()
	mgr0 := NewManager(inner, nil, nil, zap.NewNop())
	_, err := mgr0.CreateBoard(context.Background(), &TaskBoard{ID: "sess-race", Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	winner := &Task{ID: "task-winner", Title: "the winner",
		Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS, SkillIdempotencyKey: "k"}
	store := &raceLoserStore{lifecycleStore: inner, winner: winner}
	mgr := NewManager(store, nil, nil, zap.NewNop())
	e := NewImplicitEmitter(mgr, ResolveImplicitPolicy(nil), nil, zap.NewNop())

	ctx, binding := taskctx.ContextWithBinding(context.Background())
	_, created, err := e.EnsureForTurn(ctx, TurnRequest{
		SessionID: "sess-race", AgentID: "agent-1", BoardID: "sess-race",
		TurnIndex: 0, Trigger: trToolCall, UserMessage: "raced work",
	})
	if err != nil {
		t.Fatalf("the loser must not error: %v", err)
	}
	if created != nil {
		t.Fatalf("the loser minted nothing; created = %v", created)
	}
	attr, ok := binding.Get()
	if !ok || attr.TaskID != "task-winner" {
		t.Fatalf("the loser must bind the winner's row; binding = %+v ok=%v", attr, ok)
	}
	if !e.knowsBoard("sess-race") {
		t.Fatal("a bare idempotency-key conflict must not evict a healthy board from the cache")
	}
}

// TestImplicitEmitter_SpentKeyIsDeclinedNotRebound: CreateTaskIdempotent hands
// back an existing row when the key already resolves, and the emitter used to
// bind it with no status check — so a live turn hitting a spent key filed its
// rows under a task that closed before they existed, silently and permanently
// (CompleteForTurn's IsTerminal guard then declines to correct it). A terminal
// row now declines the bind and logs.
func TestImplicitEmitter_SpentKeyIsDeclinedNotRebound(t *testing.T) {
	e, _ := newLifecycleEmitter(t)

	first := mintAt(t, e, "sess-spent", 0, 42, "the first conversation")
	e.CompleteForTurn(context.Background(), first.ID, "done")
	e.ForgetSession("sess-spent") // memory gone; the durable key remains

	ctx, binding := taskctx.ContextWithBinding(context.Background())
	_, created, err := e.EnsureForTurn(ctx, TurnRequest{
		SessionID: "sess-spent", AgentID: "agent-1", BoardID: "sess-spent",
		TurnIndex: 0, SessionEpoch: 42, // the same incarnation, the same key
		Trigger: trToolCall, UserMessage: "new work on a spent key",
	})
	if err != nil {
		t.Fatalf("declining is not an error: %v", err)
	}
	if created != nil {
		t.Fatalf("nothing must be minted on a spent key; got %v", created.ID)
	}
	if attr, ok := binding.Get(); ok {
		t.Fatalf("new work must NOT bind to the finished task; bound %s", attr.TaskID)
	}
}

// TestImplicitEmitter_BoardCacheIsScopedByIdentity: the cache used to be keyed
// by board id alone, so with an operator-configured shared default_board_id,
// tenant A's first mint cached the board and tenant B's mint skipped the probe
// entirely — under downstream per-user RLS, B's task then referenced a board
// row B cannot see. The cache key now carries the caller's identity, so B's
// first mint probes for itself.
func TestImplicitEmitter_BoardCacheIsScopedByIdentity(t *testing.T) {
	inner := newLifecycleStore()
	store := &raceLoserStore{lifecycleStore: inner} // reused only for its GetBoard counter
	store.winner = nil
	mgr := NewManager(store, nil, nil, zap.NewNop())
	e := NewImplicitEmitter(mgr, ResolveImplicitPolicy(nil), nil, zap.NewNop())

	ctxA := types.ContextWithUserID(context.Background(), "tenant-a")
	ctxB := types.ContextWithUserID(context.Background(), "tenant-b")

	if err := e.ensureBoard(ctxA, "shared-board"); err != nil {
		t.Fatal(err)
	}
	probesAfterA := atomic.LoadInt32(&store.boardGets)
	if err := e.ensureBoard(ctxA, "shared-board"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&store.boardGets); got != probesAfterA {
		t.Fatalf("A's second mint must hit the cache; probes went %d -> %d", probesAfterA, got)
	}
	if err := e.ensureBoard(ctxB, "shared-board"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&store.boardGets); got == probesAfterA {
		t.Fatal("B's first mint must PROBE for its own identity, not ride A's cache entry")
	}
}

// TestReleaseTaskMemo_DropsOnlyTheNamedTask: the lapsed-park reclamation holds
// a task id but not the turn index that minted it, so the release sweeps the
// session's memo entries by VALUE. Other turns' memos must survive.
func TestReleaseTaskMemo_DropsOnlyTheNamedTask(t *testing.T) {
	e, _ := newLifecycleEmitter(t)
	t0 := mintAt(t, e, "sess-rel", 0, 7, "turn zero")
	t1 := mintAt(t, e, "sess-rel", 1, 7, "turn one")

	if m, _, _ := e.sizes(); m != 2 {
		t.Fatalf("rig sanity: two memo entries, got %d", m)
	}
	e.ReleaseTaskMemo("sess-rel", t0.ID)
	if m, _, _ := e.sizes(); m != 1 {
		t.Fatalf("releasing one task must drop exactly its entry; memo len = %d", m)
	}
	e.ReleaseTaskMemo("other-session", t1.ID)
	if m, _, _ := e.sizes(); m != 1 {
		t.Fatal("a release scoped to another session must not touch this one's memo")
	}
}
