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
	"errors"
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
)

// baseOnlyStore is a SessionStorage written against the base interface and
// nothing else: the shape an embedder's own store has. Embedding the interface
// type, not the concrete store, hides every optional method.
type baseOnlyStore struct{ SessionStorage }

// A store that implements only SessionStorage still serves a turn. The optional
// features it lacks are unavailable, and say so, rather than breaking the turn.
func TestABaseOnlyStoreServesATurn(t *testing.T) {
	sqlite, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlite.Close() }()
	store := baseOnlyStore{sqlite}

	for name, ok := range map[string]bool{
		"RunAttribution":      implements[RunAttribution](store),
		"ConversationRouting": implements[ConversationRouting](store),
		"ToolHistory":         implements[ToolHistory](store),
		"Rewinder":            implements[Rewinder](store),
	} {
		if ok {
			t.Fatalf("the base-only store should not implement %s", name)
		}
	}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))

	ctx := context.Background()
	if _, err := ag.Chat(ctx, "base-s", "hello"); err != nil {
		t.Fatalf("a turn on a base-only store: %v", err)
	}
	if err := ag.StampSessionRunID(ctx, "base-s", "run-1"); err != nil {
		t.Fatalf("run attribution on a store without it is a no-op, got %v", err)
	}
	if _, err := ag.RewindSession(ctx, "base-s", 1); !errors.Is(err, ErrRewindUnsupported) {
		t.Fatalf("rewind on a store without it says so, got %v", err)
	}
}

func implements[I any](v any) bool {
	_, ok := v.(I)
	return ok
}
