// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"context"
	"fmt"
	"sync"
	"testing"

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
func (s *lifecycleStore) GetTask(context.Context, string) (*Task, error) { return nil, nil }
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
func (s *lifecycleStore) CloseTask(context.Context, string, string) (*Task, error) { return nil, nil }
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
	_, ok := e.boardsKnown[boardID]
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
