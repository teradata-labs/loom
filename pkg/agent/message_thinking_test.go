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
	"path/filepath"
	"testing"
	"time"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// thinkingLLM answers once, with the thinking that led to the answer.
type thinkingLLM struct{}

func (thinkingLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "done", Thinking: "two ways to do it; the first is shorter",
		Usage: llmtypes.Usage{InputTokens: 3, OutputTokens: 50, ThinkingTokens: 40}}, nil
}
func (thinkingLLM) Name() string  { return "thinking" }
func (thinkingLLM) Model() string { return "thinking-v1" }

// An answer is stored with the thinking that led to it, and reading the
// session back returns it.
func TestAnAnswerIsStoredWithItsThinking(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, thinkingLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	ctx := context.Background()
	if _, err := ag.Chat(ctx, "think-s", "hello"); err != nil {
		t.Fatal(err)
	}
	const want = "two ways to do it; the first is shorter"
	var stored string
	if err := store.db.QueryRow(`SELECT thinking_content FROM messages
		WHERE session_id = 'think-s' AND role = 'assistant'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != want {
		t.Errorf("stored thinking %q; want %q", stored, want)
	}
	msgs, err := store.LoadMessages(ctx, "think-s")
	if err != nil {
		t.Fatal(err)
	}
	var read string
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			read = m.Thinking
		default:
			if m.Thinking != "" {
				t.Errorf("a %s message was read back with thinking %q", m.Role, m.Thinking)
			}
		}
	}
	if read != want {
		t.Errorf("thinking read back %q; want %q", read, want)
	}
}

// Every read of a message returns its thinking: the session's own messages, a
// span of them, an agent's, a parent session's, and both searches.
func TestEveryReadOfAMessageReturnsItsThinking(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Now()
	for _, s := range []*Session{
		{ID: "parent", AgentID: "coord", Context: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now},
		{ID: "child", AgentID: "sub", ParentSessionID: "parent", Context: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.SaveSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	const want = "the zebra question has one answer"
	msg := Message{Role: "assistant", Content: "zebra", Thinking: want, AgentID: "coord",
		SessionContext: types.SessionContextShared, Timestamp: now}
	if err := store.SaveMessage(ctx, "parent", &msg, true); err != nil {
		t.Fatal(err)
	}

	reads := map[string]func() ([]Message, error){
		"LoadMessages":           func() ([]Message, error) { return store.LoadMessages(ctx, "parent") },
		"ListMessagesBySeqRange": func() ([]Message, error) { return store.ListMessagesBySeqRange(ctx, "parent", 1, 100) },
		"LoadMessagesForAgent":   func() ([]Message, error) { return store.LoadMessagesForAgent(ctx, "sub") },
		"LoadMessagesFromParentSession": func() ([]Message, error) {
			return store.LoadMessagesFromParentSession(ctx, "child")
		},
		"SearchMessages in a session":     func() ([]Message, error) { return store.SearchMessages(ctx, "parent", "zebra", 5) },
		"SearchMessages in every session": func() ([]Message, error) { return store.SearchMessages(ctx, "", "zebra", 5) },
		"SearchMessagesByAgent":           func() ([]Message, error) { return store.SearchMessagesByAgent(ctx, "coord", "zebra", 5) },
	}
	for name, read := range reads {
		got, err := read()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s returned %d messages; want 1", name, len(got))
			continue
		}
		if got[0].Thinking != want {
			t.Errorf("%s returned thinking %q; want %q", name, got[0].Thinking, want)
		}
	}
}

// A store written before thinking was kept gains the column when it is
// opened: its messages read back with no thinking, and a new one keeps its.
func TestAnOlderStoreGainsTheThinkingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	now := time.Now()

	store, err := NewSessionStore(path, observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, &Session{ID: "old-s", Context: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, "old-s", &Message{Role: "assistant", Content: "before", Timestamp: now}, true); err != nil {
		t.Fatal(err)
	}
	// The store as it was before the column: the bundled SQLite cannot drop
	// a column, so it is renamed out of the way.
	if _, err := store.db.Exec(`ALTER TABLE messages RENAME COLUMN thinking_content TO was_thinking_content`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSessionStore(path, observability.NewNoOpTracer())
	if err != nil {
		t.Fatalf("opening the older store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SaveMessage(ctx, "old-s", &Message{Role: "assistant", Content: "after", Thinking: "kept", Timestamp: now.Add(time.Second)}, true); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.LoadMessages(ctx, "old-s")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range msgs {
		got[m.Content] = m.Thinking
	}
	if len(msgs) != 2 || got["before"] != "" || got["after"] != "kept" {
		t.Errorf("read back %v; want before with no thinking and after with %q", got, "kept")
	}
}

// silentLLM thinks and says nothing, every time it is asked.
type silentLLM struct{ calls int }

func (l *silentLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.calls++
	thinking := "first: nothing to say yet"
	if l.calls > 1 {
		thinking = "second: still nothing"
	}
	return &llmtypes.LLMResponse{Thinking: thinking, StopReason: "end_turn",
		Usage: llmtypes.Usage{InputTokens: 3, OutputTokens: 40, ThinkingTokens: 40}}, nil
}
func (*silentLLM) Name() string  { return "silent" }
func (*silentLLM) Model() string { return "silent-v1" }

// A reply with thinking and no words is not lost: the thinking of the one
// that is asked again is stored with the asking, and the thinking of the one
// that ends the turn with the turn's last row. Each row says why the provider
// ended that reply.
func TestTheThinkingOfAReplyWithNoWordsIsStored(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &silentLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	if _, err := ag.Chat(context.Background(), "silent-s", "hello"); err != nil {
		t.Fatal(err)
	}
	for role, want := range map[string]string{
		roleEmptyResponseRetry: "first: nothing to say yet",
		"assistant":            "second: still nothing",
	} {
		var got, stop string
		if err := store.db.QueryRow(`SELECT thinking_content, stop_reason FROM messages
			WHERE session_id = 'silent-s' AND role = ?`, role).Scan(&got, &stop); err != nil {
			t.Fatalf("the %s row: %v", role, err)
		}
		if stop != "end_turn" {
			t.Errorf("the %s row was stored with stop reason %q; want end_turn", role, stop)
		}
		if got != want {
			t.Errorf("the %s row was stored with thinking %q; want %q", role, got, want)
		}
	}
}
