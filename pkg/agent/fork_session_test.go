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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// A fork starts from the context its parent works in: what the parent folded
// stays folded and the parent's summary is the fork's, so the fork is the
// parent's size, not the size of everything the parent ever said. Its own
// next fold builds on that summary, and the parent is left as it was.
func TestAForkStartsFromItsParentsFoldedContext(t *testing.T) {
	ctx := context.Background()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// The parent: eight turns of work, the first six folded under summary v2.
	require.NoError(t, store.SaveSession(ctx, &Session{ID: "parent", AgentID: "creator", Context: map[string]interface{}{}}))
	big := strings.Repeat("raw tool output ", 400)
	for turn := 1; turn <= 8; turn++ {
		k := strconv.Itoa(turn)
		require.NoError(t, store.SaveMessage(ctx, "parent", &Message{Role: "user", Content: "step " + k}, true))
		require.NoError(t, store.SaveMessage(ctx, "parent", &Message{Role: "assistant", Content: "working " + k,
			ToolCalls: []ToolCall{{ID: "c" + k, Name: "shell_execute"}}}, false))
		require.NoError(t, store.SaveMessage(ctx, "parent", &Message{Role: "tool", ToolUseID: "c" + k, Content: big}, false))
		require.NoError(t, store.SaveMessage(ctx, "parent", &Message{Role: "assistant", Content: "done " + k, CostUSD: 0.5}, false))
	}
	rows, err := store.ListMessagesBySeqRange(ctx, "parent", 0, 1<<62)
	require.NoError(t, err)
	var folded []int64
	for _, m := range rows {
		if m.Turn <= 6 && m.Role != "user" {
			seq, _ := strconv.ParseInt(m.ID, 10, 64)
			folded = append(folded, seq)
		}
	}
	require.NoError(t, store.FoldMessages(ctx, "parent", nil, 1, "summary v1"))
	require.NoError(t, store.FoldMessages(ctx, "parent", folded, 2, "summary v2: plan drafted, schema chosen"))

	a := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithMemory(NewMemoryWithStore(store)), WithoutSelfCorrection())
	require.NoError(t, a.ForkSession(ctx, "parent", "fork"))

	load := func(id string) *SegmentedMemory {
		s := NewMemoryWithStore(store).GetOrCreateSessionWithAgent(ctx, id, "creator", "")
		require.NotNil(t, s)
		sm, ok := s.SegmentedMem.(*SegmentedMemory)
		require.True(t, ok)
		return sm
	}
	parent, fork := load("parent"), load("fork")

	shape := func(sm *SegmentedMemory) []string {
		var out []string
		for _, m := range sm.GetMessages() {
			out = append(out, m.Role+":"+m.Content)
		}
		return out
	}
	assert.Equal(t, shape(parent), shape(fork), "the fork holds exactly the rows the parent works with")
	assert.Equal(t, parent.summary, fork.summary, "the parent's newest summary is the fork's")
	assert.Equal(t, parent.GetTokenCount(), fork.GetTokenCount(), "the fork is the parent's size")
	assert.NotContains(t, strings.Join(shape(fork), "\n"), "working 3", "what the parent folded stays folded")

	forkRows, err := store.ListMessagesBySeqRange(ctx, "fork", 0, 1<<62)
	require.NoError(t, err)
	for _, m := range forkRows {
		assert.Zero(t, m.CostUSD, "what the parent's turns cost stays the parent's")
	}

	// The fork's own fold builds on the copied summary; the parent's is untouched.
	fork.SetProtectedRecentTurns(1)
	applied, _ := fork.ApplyFoldReply(ctx, "fork went on: tests written")
	require.True(t, applied)
	n, text, err := newestFold(ctx, store, "fork")
	require.NoError(t, err)
	assert.Equal(t, 3, n, "the fork's next summary follows the parent's v2")
	assert.Contains(t, text, "tests written")
	n, text, err = newestFold(ctx, store, "parent")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "summary v2: plan drafted, schema chosen", text)
}
