// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// forgetSpy records whether a session's approvals were discarded.
type forgetSpy struct {
	forgotten []string
	recorded  map[string][]shuttle.CallIdentity
}

func (f *forgetSpy) Record(_ context.Context, stateKey string, ids []shuttle.CallIdentity) error {
	if f.recorded == nil {
		f.recorded = map[string][]shuttle.CallIdentity{}
	}
	f.recorded[stateKey] = append(f.recorded[stateKey], ids...)
	return nil
}

func (f *forgetSpy) Contains(_ context.Context, stateKey string, id shuttle.CallIdentity) (bool, error) {
	for _, got := range f.recorded[stateKey] {
		if got == id {
			return true, nil
		}
	}
	return false, nil
}

func (f *forgetSpy) ForgetSession(sessionID string) {
	f.forgotten = append(f.forgotten, sessionID)
}

// TestRewindSession_KeepsWhatTheRewindDidNotUndo pins the blast radius.
//
// A rewind removes exchanges. It does not retire the session, so the state a
// session accumulated outside its message history must survive — above all the
// approvals a human already gave, because turns that remain may have earned
// them. Re-asking for permission the user granted is a defect the user
// experiences as the undo having done more than it said.
func TestRewindSession_KeepsWhatTheRewindDidNotUndo(t *testing.T) {
	store, err := NewSessionStore(t.TempDir()+"/scope.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.SaveSession(ctx, &Session{ID: "s"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.Enabled = false
	a := NewAgent(nil, nil, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)), WithoutSelfCorrection())

	spy := &forgetSpy{}
	a.AdoptApprovedSet(spy)
	id := shuttle.CallIdentity("shell_execute:ls")
	if err := spy.Record(ctx, "s", []shuttle.CallIdentity{id}); err != nil {
		t.Fatalf("record approval: %v", err)
	}

	undone, err := a.RewindSession(ctx, "s", 1)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if undone != 1 {
		t.Fatalf("undone = %d, want 1", undone)
	}

	if len(spy.forgotten) != 0 {
		t.Errorf("the rewind discarded the session's approvals: ForgetSession(%v)", spy.forgotten)
	}
	if ok, _ := spy.Contains(ctx, "s", id); !ok {
		t.Error("an approval the user already gave did not survive the rewind")
	}

	// And the history really was truncated: the point of the rewind.
	msgs, err := store.LoadMessages(ctx, "s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, m := range msgs {
		if m.Content == "second" || m.Content == "reply to second" {
			t.Fatalf("the undone turn is still in the history: %q", m.Content)
		}
	}
	if len(msgs) != 3 {
		t.Errorf("messages left = %d, want the 3 of the first turn: %v", len(msgs), contents(msgs))
	}
}
