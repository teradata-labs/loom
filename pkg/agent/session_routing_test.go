// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// routingStore opens a store with a conversation and two agents' sessions under
// it: one that was handed the conversation, one that was only given work.
func routingStore(t *testing.T) (*SessionStore, context.Context) {
	t.Helper()
	store, err := NewSessionStore(t.TempDir()+"/routing.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	for _, s := range []*Session{
		{ID: "root"},
		{ID: "planner", ParentSessionID: "root", AgentID: "spec-writer"},
		{ID: "worker", ParentSessionID: "root", AgentID: "builder"},
	} {
		if err := store.SaveSession(ctx, s); err != nil {
			t.Fatalf("save session %s: %v", s.ID, err)
		}
	}
	return store, ctx
}

func say(t *testing.T, store *SessionStore, ctx context.Context, session, role, content string, kind types.SessionContext) {
	t.Helper()
	msg := &Message{Role: role, Content: content, SessionContext: kind, AgentID: session}
	if err := store.SaveMessage(ctx, session, msg, role == "user"); err != nil {
		t.Fatalf("save message: %v", err)
	}
}

// TestNobodyHoldsANewConversation is the invariant that makes this additive: a
// conversation nothing has been handed to reads as unheld, so every existing
// session behaves exactly as it did before routing existed.
func TestNobodyHoldsANewConversation(t *testing.T) {
	store, ctx := routingStore(t)

	holding, err := store.Holding(ctx, "root")
	if err != nil {
		t.Fatalf("holding: %v", err)
	}
	if holding != "" {
		t.Errorf("a fresh conversation reads as held by %q, want nobody", holding)
	}
}

// TestHoldingIsHandedOverAndBack walks the only two transitions there are. The
// hand-back has to return exactly the empty string rather than any other
// sentinel, because that is what the delivery read tests for.
func TestHoldingIsHandedOverAndBack(t *testing.T) {
	store, ctx := routingStore(t)

	if err := store.SetHolding(ctx, "root", "r1"); err != nil {
		t.Fatalf("set holding: %v", err)
	}
	if got, _ := store.Holding(ctx, "root"); got != "r1" {
		t.Errorf("holding = %q, want r1", got)
	}

	if err := store.SetHolding(ctx, "root", ""); err != nil {
		t.Fatalf("release holding: %v", err)
	}
	if got, _ := store.Holding(ctx, "root"); got != "" {
		t.Errorf("after release holding = %q, want nobody", got)
	}
}

// TestHoldingSurvivesReopening is what makes a restart find the conversation
// where it was left. Kept in the row rather than in memory precisely so that
// closing the terminal does not silently return the user to the router.
func TestHoldingSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewSessionStore(dir+"/routing.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SaveSession(ctx, &Session{ID: "root"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := store.SetHolding(ctx, "root", "r1"); err != nil {
		t.Fatalf("set holding: %v", err)
	}
	_ = store.Close()

	reopened, err := NewSessionStore(dir+"/routing.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	if got, _ := reopened.Holding(ctx, "root"); got != "r1" {
		t.Errorf("after reopen holding = %q, want r1", got)
	}
}

// TestThreadInterleavesTheSessionsItIsGiven is the render rule. Two agents spoke
// in one conversation and their rows live in different sessions, so the order on
// screen can only come from the message id — which is one sequence across every
// session.
func TestThreadInterleavesTheSessionsItIsGiven(t *testing.T) {
	store, ctx := routingStore(t)

	say(t, store, ctx, "root", "user", "add a release loop", types.SessionContextDirect)
	say(t, store, ctx, "root", "assistant", "getting the spec writer on it", types.SessionContextDirect)
	say(t, store, ctx, "planner", "user", "plan a release loop spec", types.SessionContextDirect)
	say(t, store, ctx, "planner", "assistant", "which envs?", types.SessionContextDirect)
	say(t, store, ctx, "root", "user", "preprod and prod", types.SessionContextDirect)

	msgs, err := LoadThreadMessages(ctx, store, []string{"root", "planner"})
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	want := []string{
		"add a release loop",
		"getting the spec writer on it",
		"plan a release loop spec",
		"which envs?",
		"preprod and prod",
	}
	if len(msgs) != len(want) {
		t.Fatalf("thread has %d messages, want %d", len(msgs), len(want))
	}
	for i, w := range want {
		if msgs[i].Content != w {
			t.Errorf("thread[%d] = %q, want %q", i, msgs[i].Content, w)
		}
	}
}

// TestThreadLeavesOutWhatItWasNotGiven: a spawned worker's session is a child of
// the conversation and every one of its messages is `direct`, so any rule based
// on parentage or on the discriminator alone pours its whole run into the
// transcript. Only the passed set decides.
func TestThreadLeavesOutWhatItWasNotGiven(t *testing.T) {
	store, ctx := routingStore(t)

	say(t, store, ctx, "root", "user", "build it", types.SessionContextDirect)
	for _, line := range []string{"reading the spec", "writing the files", "running the tests"} {
		say(t, store, ctx, "worker", "assistant", line, types.SessionContextDirect)
	}

	msgs, err := LoadThreadMessages(ctx, store, []string{"root"})
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("thread has %d messages, want only the one said in it", len(msgs))
	}
	if msgs[0].Content != "build it" {
		t.Errorf("thread[0] = %q", msgs[0].Content)
	}
}

// TestThreadCarriesMachineryTaggedForTheHostToFold: a report handed back on a
// disconnect, a loop's raise and a hand-up are traffic between the parts. The
// thread carries them, still tagged, so the host can fold them away from the
// person and open them on request. What is shared across sessions only for
// another agent's memory stays out.
func TestThreadCarriesMachineryTaggedForTheHostToFold(t *testing.T) {
	store, ctx := routingStore(t)

	say(t, store, ctx, "planner", "assistant", "spec is at units/rel/spec.md", types.SessionContextDirect)
	say(t, store, ctx, "root", "user", "spec at units/rel/spec.md, 6 states", types.SessionContextCoordinator)
	say(t, store, ctx, "root", "user", "The loop stopped at ASK", types.SessionContext("loop:run-1@1"))
	say(t, store, ctx, "root", "user", "planner could not steer it", types.SessionContext("handup:planner"))
	say(t, store, ctx, "root", "user", "for another agent's memory", types.SessionContextShared)
	say(t, store, ctx, "root", "assistant", "spec is done — building it", types.SessionContextDirect)

	msgs, err := LoadThreadMessages(ctx, store, []string{"root", "planner"})
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	tags := map[types.SessionContext]bool{}
	for _, m := range msgs {
		tags[m.SessionContext] = true
		if m.SessionContext == types.SessionContextShared {
			t.Errorf("shared memory reached the thread: %q", m.Content)
		}
	}
	if len(msgs) != 5 {
		t.Fatalf("thread has %d messages, want 5", len(msgs))
	}
	for _, want := range []types.SessionContext{types.SessionContextCoordinator, "loop:run-1@1", "handup:planner"} {
		if !tags[want] {
			t.Errorf("the thread lost its %q tag", want)
		}
	}
}

// TestThreadCarriesWhoSpokeEachLine: with several voices in one transcript,
// attribution is what makes it readable at all. The column has existed and gone
// unwritten, so this asserts it comes back out.
func TestThreadCarriesWhoSpokeEachLine(t *testing.T) {
	store, ctx := routingStore(t)

	say(t, store, ctx, "root", "assistant", "getting the spec writer on it", types.SessionContextDirect)
	say(t, store, ctx, "planner", "assistant", "which envs?", types.SessionContextDirect)

	msgs, err := LoadThreadMessages(ctx, store, []string{"root", "planner"})
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("thread has %d messages", len(msgs))
	}
	if msgs[0].AgentID == msgs[1].AgentID {
		t.Fatalf("both lines attributed to %q — two voices merged into one", msgs[0].AgentID)
	}
}

// TestThreadToleratesRepeatsAndBlanks: the set is assembled from a run list, so a
// caller can hand the same session twice or an empty string for a run whose
// session was never recorded. Neither should double a message or fail a read.
func TestThreadToleratesRepeatsAndBlanks(t *testing.T) {
	store, ctx := routingStore(t)
	say(t, store, ctx, "root", "user", "hello", types.SessionContextDirect)

	msgs, err := LoadThreadMessages(ctx, store, []string{"root", "", "root"})
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("thread has %d messages, want 1", len(msgs))
	}
}
