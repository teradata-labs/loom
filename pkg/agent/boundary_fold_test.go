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

// Task-span fold — a finished item's transcript span (bracketed by its own
// transitions) is replaced by its completion summary; the groundwork before
// the list and the live tail after the last done are never touched; user rows
// survive whatever span they fall in; thin spans wait at the economics gate.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSpanSession: turn 1 groundwork (shared discovery), turns 2-6 item #1's
// heavy work, turn 7 the live tail. Span for item #1 is (2, 6): the
// transition turns themselves stay.
func seedSpanSession(sm *SegmentedMemory) {
	big := strings.Repeat("r", 4000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "the JOB", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "discovery: profiles loaded", Turn: 1})
	for turn := int64(2); turn <= 6; turn++ {
		// Results render as stored (no truncation): the transition turns 2 and 6
		// carry small results so the interior is the heavy part, as in a real span.
		content := big
		if turn == 2 || turn == 6 {
			content = "transition"
		}
		sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: turn,
			ToolCalls: []ToolCall{{ID: "c" + string(rune('0'+turn)), Name: "shell_execute"}}})
		sm.AddMessage(context.Background(), Message{Role: "tool", ToolUseID: "c" + string(rune('0'+turn)), Content: content, Turn: turn})
	}
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "starting item 2", Turn: 7})
}

// TestSpanFold_DeclarationAloneMutatesNothing — recording a closed span
// changes no context until the compile seam runs.
func TestSpanFold_DeclarationAloneMutatesNothing(t *testing.T) {
	sm := newCompileMemory(t)
	seedSpanSession(sm)
	before := len(sm.GetMessages())

	sm.DeclareClosedSpan(2, 6, "staging model — built, tests green", false)

	assert.Len(t, sm.GetMessages(), before)
	assert.Equal(t, 0, sm.summary.n)
}

// TestSpanFold_InteriorFoldsEdgesAndGroundworkSurvive — the span's interior
// is excised; its edge turns, the turn-1 groundwork and the live tail stay.
func TestSpanFold_InteriorFoldsEdgesAndGroundworkSurvive(t *testing.T) {
	sm := newCompileMemory(t)
	seedSpanSession(sm)
	sm.DeclareClosedSpan(2, 6, "staging model — built, tests green", false)

	shed, _ := sm.ReleaseDeclared(context.Background())

	require.True(t, shed, "interior is ~12K of dead results against a small remainder")
	turnsLeft := map[int64]bool{}
	for _, m := range sm.GetMessages() {
		turnsLeft[m.Turn] = true
	}
	assert.True(t, turnsLeft[1], "groundwork survives — the open list keeps it live")
	assert.True(t, turnsLeft[2] && turnsLeft[6], "edge turns survive — transitions can carry neighbours' material")
	assert.False(t, turnsLeft[3] || turnsLeft[4] || turnsLeft[5], "interior excised")
	assert.True(t, turnsLeft[7], "live tail untouched")
	assert.Contains(t, sm.summary.text, "staging model — built, tests green")
}

// TestSpanFold_UserRowsSurviveInsideSpan — an operator interjection inside a
// folded span is kept verbatim.
func TestSpanFold_UserRowsSurviveInsideSpan(t *testing.T) {
	sm := newCompileMemory(t)
	seedSpanSession(sm)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "operator ruling mid-span", Turn: 4})
	sm.DeclareClosedSpan(2, 6, "item one done", false)

	shed, _ := sm.ReleaseDeclared(context.Background())

	require.True(t, shed)
	found := false
	for _, m := range sm.GetMessages() {
		if m.Content == "operator ruling mid-span" {
			found = true
		}
	}
	assert.True(t, found, "the operator's words never fold")
}

// TestSpanFold_EconomicsGateDefers — a thin span stays pending; nothing moves.
func TestSpanFold_EconomicsGateDefers(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "the JOB", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "tiny step", Turn: 2})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "tiny middle", Turn: 3})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: strings.Repeat("live work ", 900), Turn: 4})
	sm.DeclareClosedSpan(2, 4, "small item done", false)
	before := len(sm.GetMessages())

	shed, _ := sm.ReleaseDeclared(context.Background())

	assert.False(t, shed)
	assert.Len(t, sm.GetMessages(), before)
	assert.True(t, sm.board.pending, "span waits for more mass at a later done")
}

// TestSpanFold_CancelledItemFilesUnderCancelled — a cancelled span's line
// renders in the Cancelled section, not among completed work.
func TestSpanFold_CancelledItemFilesUnderCancelled(t *testing.T) {
	sm := newCompileMemory(t)
	seedSpanSession(sm)
	sm.DeclareClosedSpan(2, 6, "wrong approach — task text superseded it", true)

	shed, _ := sm.ReleaseDeclared(context.Background())

	require.True(t, shed)
	assert.Contains(t, sm.summary.text, "Cancelled:")
	assert.Contains(t, sm.summary.text, "wrong approach")
}

// TestSpanFold_PriorCompressorSummaryCarriesForward — a summary written by
// another regime is carried, never superseded away.
func TestSpanFold_PriorCompressorSummaryCarriesForward(t *testing.T) {
	sm := newCompileMemory(t)
	seedSpanSession(sm)
	sm.summary = summaryState{n: 3, text: "covers msg:1-9\ncompressor facts: schema X is authoritative"}
	sm.DeclareClosedSpan(2, 6, "item one done", false)

	shed, _ := sm.ReleaseDeclared(context.Background())

	require.True(t, shed)
	assert.Contains(t, sm.summary.text, "item one done")
	assert.Contains(t, sm.summary.text, "schema X is authoritative")
	assert.Equal(t, 4, sm.summary.n)
}
