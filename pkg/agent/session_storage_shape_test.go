// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"strconv"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// An embedder's store written against SessionStorage before results were
// bounded at arrival still implements it: MarkEvicted is part of the
// interface. loom's own store takes the call and changes nothing, since no
// tool result is evicted any more.
func TestMarkEvictedIsKeptAndChangesNothing(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var _ SessionStorage = store

	ctx := context.Background()
	sess := &Session{ID: "s-evict"}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, sess.ID, &Message{Role: "user", Content: "q"}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, sess.ID, &Message{Role: "tool", ToolUseID: "c1", Content: "42 rows"}, false); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.LoadMessages(ctx, sess.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("load: %v %d", err, len(msgs))
	}
	var seqs []int64
	for _, m := range msgs {
		seq, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			t.Fatalf("message id %q: %v", m.ID, err)
		}
		seqs = append(seqs, seq)
	}
	if err := store.MarkEvicted(ctx, sess.ID, seqs); err != nil {
		t.Fatalf("MarkEvicted: %v", err)
	}
	after, err := store.LoadMessages(ctx, sess.ID)
	if err != nil || len(after) != 2 || after[1].Content != "42 rows" {
		t.Fatalf("MarkEvicted changed the conversation: %v %+v", err, after)
	}
}

// A session's cost is summed where its rows are kept, folded rows included,
// and is the owner's to read.
func TestSessionCost_SumsEveryRowForItsOwner(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var cs CostStorage = store

	ctx := types.ContextWithUserID(context.Background(), "ann")
	if err := store.SaveSession(ctx, &Session{ID: "s-cost"}); err != nil {
		t.Fatal(err)
	}
	for i, m := range []Message{
		{Role: "user", Content: "q"}, {Role: "assistant", Content: "a", CostUSD: 0.25},
		{Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2", CostUSD: 0.5},
	} {
		if err := store.SaveMessage(ctx, "s-cost", &m, m.Role == "user"); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	usd, err := cs.SessionCost(ctx, "s-cost")
	if err != nil || usd < 0.75-1e-9 || usd > 0.75+1e-9 {
		t.Fatalf("the rows sum to 0.75, got %v %v", usd, err)
	}
	msgs, _ := store.LoadMessages(ctx, "s-cost")
	seq, _ := strconv.ParseInt(msgs[1].ID, 10, 64)
	if err := store.FoldMessages(ctx, "s-cost", []int64{seq}, 1, "covers msg:1-1\nsummary"); err != nil {
		t.Fatal(err)
	}
	if usd, _ = cs.SessionCost(ctx, "s-cost"); usd < 0.75-1e-9 {
		t.Fatalf("a folded row left the ledger: %v", usd)
	}
	if usd, _ = cs.SessionCost(types.ContextWithUserID(context.Background(), "bob"), "s-cost"); usd != 0 {
		t.Fatalf("another owner read the session's cost: %v", usd)
	}
}

// What a billed row carries — the model, its effort, and every token lane —
// comes back on load as it was written: a row's cost can then be read as
// well as summed.
func TestABilledRowReadsBackWhole(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	if err := store.SaveSession(ctx, &Session{ID: "s-billed"}); err != nil {
		t.Fatal(err)
	}
	billed := Message{Role: "assistant", Content: "a", CostUSD: 0.5, TokenCount: 60,
		InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreateTokens: 7,
		CacheCreate1hTokens: 4, CacheCreate5mTokens: 3, ThinkingTokens: 12,
		Model: "claude-opus-5-5", Effort: "high"}
	if err := store.SaveMessage(ctx, "s-billed", &billed, true); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.LoadMessages(ctx, "s-billed")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("load: %v %d", err, len(msgs))
	}
	got := msgs[0]
	for name, pair := range map[string][2]any{
		"model": {billed.Model, got.Model}, "effort": {billed.Effort, got.Effort},
		"cache_create_1h": {billed.CacheCreate1hTokens, got.CacheCreate1hTokens},
		"cache_create_5m": {billed.CacheCreate5mTokens, got.CacheCreate5mTokens},
		"thinking":        {billed.ThinkingTokens, got.ThinkingTokens},
		"input":           {billed.InputTokens, got.InputTokens},
		"cache_create":    {billed.CacheCreateTokens, got.CacheCreateTokens},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: wrote %v, read back %v", name, pair[0], pair[1])
		}
	}
}
