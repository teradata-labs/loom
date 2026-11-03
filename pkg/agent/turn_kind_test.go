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
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

func kindStore(t *testing.T) (*SessionStore, context.Context) {
	t.Helper()
	store, err := NewSessionStore(t.TempDir()+"/kind.db", observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.SaveSession(ctx, &Session{ID: "conv"}); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func stored(t *testing.T, store *SessionStore, ctx context.Context, session string) []Message {
	t.Helper()
	msgs, err := store.LoadMessages(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// Every row of a turn is for whoever the turn is for: a row that does not say
// takes the session_context of its turn's first row, and the in-memory
// message is stamped with what was stored. A row that says keeps it, and a
// turn that says nothing is direct.
func TestARowIsForWhoeverItsTurnIsFor(t *testing.T) {
	store, ctx := kindStore(t)
	write := func(role string, kind llmtypes.SessionContext, start bool) *Message {
		m := &Message{Role: role, Content: role + " words", SessionContext: kind}
		if err := store.SaveMessage(ctx, "conv", m, start); err != nil {
			t.Fatal(err)
		}
		return m
	}
	write("user", "", true)                   // turn 1: the person
	write("assistant", "", false)             // Tera answers
	write("user", "loop:run-1@7", true)       // turn 2: a loop stopped
	thinking := write("assistant", "", false) // Tera works on it
	write("tool", "", false)                  // a tool result
	write("assistant", "coordinator", false)  // a row that says who it is for
	write("user", "speak", true)              // turn 3: Tera speaks to the person
	write("assistant", "", false)             // what Tera says
	write("user", "", true)                   // turn 4: the person again
	write("assistant", "", false)

	want := []llmtypes.SessionContext{"direct", "direct",
		"loop:run-1@7", "loop:run-1@7", "loop:run-1@7", "coordinator",
		"speak", "speak", "direct", "direct"}
	got := stored(t, store, ctx, "conv")
	if len(got) != len(want) {
		t.Fatalf("stored %d rows, want %d", len(got), len(want))
	}
	for i, m := range got {
		if m.SessionContext != want[i] {
			t.Errorf("row %d (%s, turn %d) is %q, want %q", i+1, m.Role, m.Turn, m.SessionContext, want[i])
		}
	}
	if thinking.SessionContext != "loop:run-1@7" {
		t.Errorf("the in-memory message was not stamped: %q", thinking.SessionContext)
	}
}

// A turn started with a stated origin is written, every row of it, with that
// origin: what the agent says, the tools it calls and what they return. The
// person's own turn afterwards is direct, as before.
func TestATurnStartedAsSomethingIsThatThroughout(t *testing.T) {
	store, ctx := kindStore(t)
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{content: "Let me check.", toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "calculator", Input: map[string]interface{}{"expression": "1+1"}}}},
		{content: "It is 2."},
		{content: "Hello."},
	}}
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	ag.RegisterTool(&mockCalculatorTool{})

	if _, err := ag.ChatWithProgressAs(ctx, "conv", "The loop stopped: what is 1+1?", "loop:run-1@7", nil); err != nil {
		t.Fatal(err)
	}
	loopTurn := stored(t, store, ctx, "conv")
	if len(loopTurn) < 3 {
		t.Fatalf("the loop's turn stored %d rows", len(loopTurn))
	}
	roles := map[string]bool{}
	for _, m := range loopTurn {
		roles[m.Role] = true
		if m.SessionContext != "loop:run-1@7" {
			t.Errorf("a %s row of the loop's turn is %q", m.Role, m.SessionContext)
		}
	}
	if !roles["assistant"] || !roles["tool"] {
		t.Fatalf("the turn did not store the agent's work: %v", roles)
	}

	if _, err := ag.Chat(ctx, "conv", "hi"); err != nil {
		t.Fatal(err)
	}
	for _, m := range stored(t, store, ctx, "conv")[len(loopTurn):] {
		if m.SessionContext != "direct" {
			t.Errorf("a %s row of the person's turn is %q", m.Role, m.SessionContext)
		}
	}
}
