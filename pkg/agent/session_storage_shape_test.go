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
