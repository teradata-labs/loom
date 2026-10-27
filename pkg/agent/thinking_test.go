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

// Extended-thinking render and persist laws: blocks replay verbatim within
// their producing turn, stay on a settled turn while the context before them
// is unchanged, are left out once it changes, and never reach the store
// (signatures are turn-scoped replay material; only the plain Thinking text
// persists, for thinking_content display).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// assistantByContent returns the compiled assistant message with content c.
func assistantByContent(t *testing.T, out []Message, c string) *Message {
	t.Helper()
	for i := range out {
		if out[i].Role == "assistant" && out[i].Content == c {
			return &out[i]
		}
	}
	t.Fatalf("no assistant message %q in the compiled context", c)
	return nil
}

// TestCompile_ThinkingSettledKeptWhilePrefixUnchanged — the render law, plus
// determinism (the same compile twice is byte-identical, so the till-NOW
// cache marker's append-only property holds with blocks present). A settled
// turn keeps its blocks when nothing before them has changed: current models
// keep earlier turns' thinking, and leaving it out would rewrite the request
// from that turn on, so the next message would miss the prompt cache there.
func TestCompile_ThinkingSettledKeptWhilePrefixUnchanged(t *testing.T) {
	sm := newCompileMemory(t)
	blocks := []ThinkingBlock{{Type: "thinking", Thinking: "old plan", Signature: "S1"}}
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q1", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "a1", Turn: 1,
		ThinkingBlocks: blocks})
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q2", Turn: 2})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "a2", Turn: 2,
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "new plan", Signature: "S2"}}})

	out := sm.GetMessagesForLLM()
	var settled, current *Message
	for i := range out {
		if out[i].Role != "assistant" {
			continue
		}
		switch out[i].Content {
		case "a1":
			settled = &out[i]
		case "a2":
			current = &out[i]
		}
	}
	require.NotNil(t, settled)
	require.NotNil(t, current)
	require.Len(t, settled.ThinkingBlocks, 1, "settled turn keeps its blocks while the context before them is unchanged")
	assert.Equal(t, "S1", settled.ThinkingBlocks[0].Signature, "signature verbatim")
	require.Len(t, current.ThinkingBlocks, 1, "current turn replays its blocks")
	assert.Equal(t, "S2", current.ThinkingBlocks[0].Signature, "signature verbatim")

	// Determinism: a second compile renders byte-identically.
	out2 := sm.GetMessagesForLLM()
	require.Equal(t, len(out), len(out2))
	for i := range out {
		assert.Equal(t, out[i].Content, out2[i].Content)
		assert.Equal(t, len(out[i].ThinkingBlocks), len(out2[i].ThinkingBlocks), "render must be deterministic at index %d", i)
	}

	// The source rows are untouched — keeping or stripping is a pure render
	// condition.
	msgs := sm.GetMessages()
	for i := range msgs {
		if msgs[i].Role == "assistant" && msgs[i].Content == "a1" {
			assert.Len(t, msgs[i].ThinkingBlocks, 1, "L1 keeps the settled row's blocks")
		}
	}
}

// TestCompile_ThinkingSettledStrippedAfterPrefixChange — once anything before
// a settled turn's blocks renders differently from what the model was sent
// when it wrote them, the blocks are left out: the provider rejects (or
// drops) a block replayed after an edit to the context before it. Here a
// current-turn oversize result renders as an offload stub and re-renders as
// the prior-turn stub once the turn settles.
func TestCompile_ThinkingSettledStrippedAfterPrefixChange(t *testing.T) {
	sm := newCompileMemory(t)
	ctx := context.Background()
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1,
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "search first", Signature: "S0"}},
		ToolCalls:      []ToolCall{{ID: "c1", Name: "web_search", Input: map[string]interface{}{"q": "x"}}}})
	sm.AddMessage(ctx, Message{Role: "tool", ID: "42", ToolUseID: "c1", Content: strings.Repeat("d", 5000), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "a1", Turn: 1,
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "answer", Signature: "S1"}}})

	inTurn := sm.GetMessagesForLLM()
	require.Len(t, assistantByContent(t, inTurn, "a1").ThinkingBlocks, 1, "in-turn blocks replay")

	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	settled := sm.GetMessagesForLLM()

	var call *Message
	for i := range settled {
		if settled[i].Role == "assistant" && len(settled[i].ToolCalls) > 0 {
			call = &settled[i]
		}
	}
	require.NotNil(t, call)
	assert.Len(t, call.ThinkingBlocks, 1, "the call's blocks precede the re-rendered result, so they are still valid")
	assert.Empty(t, assistantByContent(t, settled, "a1").ThinkingBlocks,
		"the result before a1 re-rendered at settle, so a1's blocks are left out")
}

// TestCompile_ThinkingSettledStrippedWithoutRecord — blocks the compile has
// no record for (rows replayed into memory rather than added after a call)
// are left out of settled turns, as before.
func TestCompile_ThinkingSettledStrippedWithoutRecord(t *testing.T) {
	sm := newCompileMemory(t)
	ctx := context.Background()
	sm.ReplayMessages(ctx, []Message{
		{Role: "user", Content: "q1", Turn: 1},
		{Role: "assistant", Content: "a1", Turn: 1,
			ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "plan", Signature: "S1"}}},
	})
	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	assert.Empty(t, assistantByContent(t, sm.GetMessagesForLLM(), "a1").ThinkingBlocks)
}

// TestPersist_ThinkingBlocksNeverStored — the persist seam drops blocks and
// keeps the plain text; a restore therefore rebuilds the row text-only, which
// the wire tolerates (stripped replay is a 200 — probed live).
func TestPersist_ThinkingBlocksNeverStored(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "sess-think"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	mem := NewMemoryWithStore(store)
	msg := Message{Role: "assistant", Content: "worked", Thinking: "the reasoning",
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "the reasoning", Signature: "SECRET-SIG"}}}
	require.NoError(t, mem.PersistMessage(ctx, sessionID, &msg, false))

	rows, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// The blocks invariant is this seam's law. The plain text's persistence
	// is the cloud adapter's seam (session_storage → thinking_content); the
	// framework-local store carries no thinking column.
	assert.Empty(t, rows[0].ThinkingBlocks, "blocks (signatures) never persist")

	// The in-memory copy keeps its blocks for the rest of the turn.
	assert.Len(t, msg.ThinkingBlocks, 1, "persist must not mutate the live row")
}
