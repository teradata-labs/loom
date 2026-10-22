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

// Relief rung 0 unit routes — the current turn's consumed region becomes
// sheddable (sweep the §4.3 query pairs, evict consumed results), while the
// pending region — the last assistant message and everything after it — is
// untouchable by every op. These routes drive the ops directly on a
// SegmentedMemory; the e2e ladder behavior lives in test/context-optimiser.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRung0Evict_PendingGuard — evict at b = t marks consumed results and
// never the pending batch.
func TestRung0Evict_PendingGuard(t *testing.T) {
	sm := newCompileMemory(t)
	big := strings.Repeat("x", 5000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c1", Name: "shell"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "2", ToolUseID: "c1",
		Content: big, Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c2", Name: "shell"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "4", ToolUseID: "c2",
		Content: big, Turn: 1})

	sm.mu.Lock()
	changed := sm.evictLocked(context.Background(), 1)
	sm.mu.Unlock()

	require.True(t, changed, "the consumed result clears the floor — must evict")
	msgs := sm.GetMessages()
	assert.True(t, msgs[2].Evicted, "consumed result evicted")
	assert.False(t, msgs[4].Evicted, "pending result (after the last assistant message) untouched")
}

// TestRung0Evict_FirstIterationNoop — no assistant message yet: everything is
// pending, evict at b = t must change nothing.
func TestRung0Evict_FirstIterationNoop(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})

	sm.mu.Lock()
	evictChanged := sm.evictLocked(context.Background(), 1)
	sm.mu.Unlock()

	assert.False(t, evictChanged)
	require.Len(t, sm.GetMessages(), 1, "L1 unchanged")
}
