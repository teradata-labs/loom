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
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/task"
)

// spyRegistrySink records which subsystem setters were invoked.
type spyRegistrySink struct {
	taskManagerSet bool
	graphStoreSet  bool
	suppressedSet  []string
}

func (s *spyRegistrySink) SetTaskManager(*task.Manager, *task.Decomposer) { s.taskManagerSet = true }
func (s *spyRegistrySink) SetGraphMemoryStore(memory.GraphMemoryStore, memory.Embedder) {
	s.graphStoreSet = true
}
func (s *spyRegistrySink) SetSuppressedBuiltinTools(names []string) { s.suppressedSet = names }

// stubGraphStore is a non-nil memory.GraphMemoryStore whose methods are never
// invoked here (the helper only checks for nil). Embedding the interface
// satisfies all ~29 methods without hand-stubbing them.
type stubGraphStore struct{ memory.GraphMemoryStore }

// TestWireRegistrySubsystems is the regression guard for the single-provider
// nil-graph-store bug: the registry subsystem injections must run whenever the
// subsystems are present, with no dependence on a provider pool. This helper
// has no pool parameter at all — that is the point. Previously these calls were
// nested inside `providerPool != nil`, so a single-provider server left every
// registry-built agent (including benchmark --isolate temp agents) with a nil
// graph-memory store, disabling extraction and cross-session recall.
func TestWireRegistrySubsystems(t *testing.T) {
	t.Run("present subsystems are all wired (no pool involved)", func(t *testing.T) {
		spy := &spyRegistrySink{}
		// Non-nil task manager and graph store; a nil provider pool is not even
		// expressible here because the helper takes none.
		tm := &task.Manager{}
		gm := memory.GraphMemoryStore(&stubGraphStore{})
		wireRegistrySubsystems(spy, tm, nil, gm, nil, []string{"graph_memory", "task_board"}, zap.NewNop())

		assert.True(t, spy.taskManagerSet, "task manager must be injected")
		assert.True(t, spy.graphStoreSet, "graph memory store must be injected")
		assert.Equal(t, []string{"graph_memory", "task_board"}, spy.suppressedSet,
			"tool-surface policy must be propagated")
	})

	t.Run("nil subsystems are skipped, empty suppression is a no-op", func(t *testing.T) {
		spy := &spyRegistrySink{}
		wireRegistrySubsystems(spy, nil, nil, nil, nil, nil, zap.NewNop())

		assert.False(t, spy.taskManagerSet, "nil task manager must not be injected")
		assert.False(t, spy.graphStoreSet, "nil graph store must not be injected")
		assert.Nil(t, spy.suppressedSet, "empty suppression list must not call the setter")
	})
}
