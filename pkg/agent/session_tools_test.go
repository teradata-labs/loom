// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// A tool call has been recorded since the beginning and nothing could read it, so
// the most actionable record the product keeps — every failure, with its error and
// its duration — was write-only.

// newSessionFor makes a session the store will accept writes against.
func newSessionFor(t *testing.T, store *SessionStore, agentID string) string {
	t.Helper()
	sess := &Session{ID: agentID + "-" + t.Name(), AgentID: agentID}
	if err := store.SaveSession(context.Background(), sess); err != nil {
		t.Fatalf("session: %v", err)
	}
	return sess.ID
}

func toolStore(t *testing.T) (*SessionStore, string) {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := newSessionFor(t, store, "agent-under-test")
	return store, session
}

// TestToolCallsComeBackWithWhatTheyCost — time and bytes, which are the two things
// a tool spends that nobody sees: one is the wall-clock nobody attributes, the
// other is the context nobody notices filling.
func TestToolCallsComeBackWithWhatTheyCost(t *testing.T) {
	store, session := toolStore(t)
	ctx := context.Background()

	if err := store.SaveToolExecution(ctx, session, ToolExecution{
		ToolName: "shell_execute",
		Input:    map[string]interface{}{"command": "node build.js"},
		Result:   &shuttle.Result{Success: true, Data: "a result long enough to measure"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	recs, err := store.ToolExecutions(ctx, session)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("read %d records, want 1", len(recs))
	}
	if recs[0].Tool != "shell_execute" {
		t.Errorf("tool is %q", recs[0].Tool)
	}
	if recs[0].ResultBytes == 0 {
		t.Error("the size of what came back was not recorded, so nothing explains the context growth")
	}
	if recs[0].Failed() {
		t.Errorf("a successful call reads as failed: %q", recs[0].Error)
	}
}

// TestAFailedCallSaysWhy. A failure is a wasted turn and a poisoned context, and
// the error text is what makes it fixable rather than merely countable.
func TestAFailedCallSaysWhy(t *testing.T) {
	store, session := toolStore(t)
	ctx := context.Background()

	if err := store.SaveToolExecution(ctx, session, ToolExecution{
		ToolName: "file_read",
		Input:    map[string]interface{}{"file_path": "/nope"},
		Error:    fmt.Errorf("no files matched: /nope"),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	recs, err := store.ToolExecutions(ctx, session)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 1 || !recs[0].Failed() {
		t.Fatalf("the failure did not come back as one: %+v", recs)
	}
	if recs[0].Error != "no files matched: /nope" {
		t.Errorf("the error text is %q, so the failure is countable but not fixable", recs[0].Error)
	}
}

// TestCallsComeBackInTheOrderTheyHappened. A sequence is how a stuck agent shows
// itself — the same command three times running means something a count does not.
func TestCallsComeBackInTheOrderTheyHappened(t *testing.T) {
	store, session := toolStore(t)
	ctx := context.Background()

	for _, name := range []string{"first", "second", "third"} {
		if err := store.SaveToolExecution(ctx, session, ToolExecution{
			ToolName: name, Result: &shuttle.Result{Success: true, Data: "ok"},
		}); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}

	recs, err := store.ToolExecutions(ctx, session)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Tool)
	}
	if len(got) != 3 || got[0] != "first" || got[2] != "third" {
		t.Errorf("order is %v, want the order they happened in", got)
	}
}

// TestOneSessionsToolsAreNotAnothers, because every number built on this is
// attributed to an agent and a leak would bill the wrong one.
func TestOneSessionsToolsAreNotAnothers(t *testing.T) {
	store, mine := toolStore(t)
	ctx := context.Background()
	theirs := newSessionFor(t, store, "another-agent")

	if err := store.SaveToolExecution(ctx, mine, ToolExecution{ToolName: "mine"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveToolExecution(ctx, theirs, ToolExecution{ToolName: "theirs"}); err != nil {
		t.Fatal(err)
	}

	recs, err := store.ToolExecutions(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Tool != "mine" {
		t.Errorf("a session's tools include another's: %+v", recs)
	}
}

// TestASessionWithNoToolsIsEmptyRatherThanAnError — most conversations never call
// one, and a view that errors on that is a view nobody can open.
func TestASessionWithNoToolsIsEmptyRatherThanAnError(t *testing.T) {
	store, session := toolStore(t)
	recs, err := store.ToolExecutions(context.Background(), session)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("read %d records from a session that used no tools", len(recs))
	}
}
