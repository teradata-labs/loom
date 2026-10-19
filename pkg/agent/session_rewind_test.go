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
package agent

import (
	"context"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
)

// rewindStore opens a store with one session ready to take messages.
func rewindStore(t *testing.T) (*SessionStore, context.Context) {
	t.Helper()
	store, err := NewSessionStore(t.TempDir()+"/rewind.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.SaveSession(ctx, &Session{ID: "s"}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	return store, ctx
}

// exchange writes one complete turn: the user message that starts it, the tool
// call it caused, and the assistant message that closes it. Writing all three
// is the point — a rewind that kept the tool row and dropped its user message
// would leave a history the provider rejects.
func exchange(t *testing.T, store *SessionStore, ctx context.Context, prompt string) {
	t.Helper()
	if err := store.SaveMessage(ctx, "s", &Message{Role: "user", Content: prompt}, true); err != nil {
		t.Fatalf("save user message: %v", err)
	}
	if err := store.SaveMessage(ctx, "s", &Message{Role: "tool", Content: "tool output for " + prompt, ToolUseID: "tu-" + prompt}, false); err != nil {
		t.Fatalf("save tool message: %v", err)
	}
	if err := store.SaveMessage(ctx, "s", &Message{Role: "assistant", Content: "reply to " + prompt}, false); err != nil {
		t.Fatalf("save assistant message: %v", err)
	}
}

func contents(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

// TestTruncateFrom_RemovesAWholeExchange is the core invariant: rewinding a turn
// takes the user message and everything it caused, leaving earlier turns intact.
func TestTruncateFrom_RemovesAWholeExchange(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")

	rows, err := store.TruncateFrom(ctx, "s", 2)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if rows != 3 {
		t.Errorf("expected the whole exchange (user + tool + assistant) to go, removed %d rows", rows)
	}

	msgs, err := store.LoadMessages(ctx, "s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected the first exchange to survive intact, got %v", contents(msgs))
	}
	for _, m := range msgs {
		if m.Content == "second" || m.Content == "reply to second" || m.Content == "tool output for second" {
			t.Errorf("a message from the rewound turn survived: %q", m.Content)
		}
	}
}

// TestTruncateFrom_NextTurnNumbersContinue proves no counter needs rewinding:
// turn is derived from MAX(turn) at insert, so the turn after a truncation
// reuses the number that was freed rather than skipping it.
func TestTruncateFrom_NextTurnNumbersContinue(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")

	if _, err := store.TruncateFrom(ctx, "s", 2); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	exchange(t, store, ctx, "replacement")

	turn, err := store.MaxTurn(ctx, "s")
	if err != nil {
		t.Fatalf("max turn: %v", err)
	}
	if turn != 2 {
		t.Errorf("expected the replacement turn to be 2, got %d — a stale counter would have skipped to 3", turn)
	}
}

// TestMaxTurn_EmptySessionIsZero covers the case a rewind has to detect before
// it computes a range: there is nothing to undo.
func TestMaxTurn_EmptySessionIsZero(t *testing.T) {
	store, ctx := rewindStore(t)
	turn, err := store.MaxTurn(ctx, "s")
	if err != nil {
		t.Fatalf("max turn: %v", err)
	}
	if turn != 0 {
		t.Errorf("expected 0 for a session with no messages, got %d", turn)
	}
}

// TestTruncateFrom_RejectsNonPositiveTurn guards the caller's arithmetic: turn 0
// would delete the whole history, which is DeleteSession's job and must not
// happen by an off-by-one.
func TestTruncateFrom_RejectsNonPositiveTurn(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")

	for _, turn := range []int{0, -1} {
		if _, err := store.TruncateFrom(ctx, "s", turn); err == nil {
			t.Errorf("turn %d was accepted; it would erase the session", turn)
		}
	}
	msgs, err := store.LoadMessages(ctx, "s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("a rejected truncation still changed the history: %v", contents(msgs))
	}
}

// TestRewindSession_ReloadsTheTruncatedHistory is the whole-primitive test: an
// agent whose session is loaded in memory must see the shorter history on the
// next access. This is what fails if the memory eviction is dropped.
func TestRewindSession_ReloadsTheTruncatedHistory(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "first")
	exchange(t, store, ctx, "second")

	ag := NewAgent(&mockBackend{}, nil,
		WithName("rewind"),
		WithMemory(NewMemoryWithStore(store)),
	)

	// Load the session into memory, so the rewind has a stale copy to beat.
	loaded := ag.memory.GetOrCreateSession(ctx, "s")
	if loaded == nil {
		t.Fatal("expected the session to load")
	}
	if _, ok := ag.memory.GetSession("s"); !ok {
		t.Fatal("expected the session to be held in memory before the rewind")
	}

	undone, err := ag.RewindSession(ctx, "s", 1)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if undone != 1 {
		t.Errorf("expected 1 turn undone, got %d", undone)
	}

	// The reload, not the stale copy, is what the next turn would compile.
	after := ag.memory.GetOrCreateSession(ctx, "s")
	for _, m := range after.Messages {
		if m.Content == "second" {
			t.Error("the rewound prompt came back on reload — the in-memory copy was not evicted")
		}
	}
	if turn, err := store.MaxTurn(ctx, "s"); err != nil || turn != 1 {
		t.Errorf("expected one turn left in the store, got turn=%d err=%v", turn, err)
	}
}

// TestRewindSession_MoreTurnsThanExist stops at the beginning rather than
// deleting into turn 0, and reports what it actually undid.
func TestRewindSession_MoreTurnsThanExist(t *testing.T) {
	store, ctx := rewindStore(t)
	exchange(t, store, ctx, "only")

	ag := NewAgent(&mockBackend{}, nil,
		WithName("rewind"),
		WithMemory(NewMemoryWithStore(store)),
	)

	undone, err := ag.RewindSession(ctx, "s", 5)
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if undone != 1 {
		t.Errorf("expected the one existing turn to be undone, got %d", undone)
	}
	msgs, err := store.LoadMessages(ctx, "s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected an empty history, got %v", contents(msgs))
	}
}

// TestRewindSession_EmptySessionIsANoOp keeps a keypress on a fresh session from
// being an error the user has to read.
func TestRewindSession_EmptySessionIsANoOp(t *testing.T) {
	store, ctx := rewindStore(t)
	ag := NewAgent(&mockBackend{}, nil,
		WithName("rewind"),
		WithMemory(NewMemoryWithStore(store)),
	)

	undone, err := ag.RewindSession(ctx, "s", 1)
	if err != nil {
		t.Fatalf("rewind on an empty session should not error: %v", err)
	}
	if undone != 0 {
		t.Errorf("expected 0 turns undone, got %d", undone)
	}
}
