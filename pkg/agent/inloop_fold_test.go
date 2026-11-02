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

// In-loop full collapse — the model's fold reply carries only the summary;
// the harness owns the range: operator rows verbatim, recent assistant text
// kept with pairs stripped whole, pending pair untouched, all other tool
// matter and old thread excised. An empty summary folds nothing.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/prompts"
)

func seedInloopSession(sm *SegmentedMemory) {
	big := strings.Repeat("r", 3000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "the JOB", Turn: 1})
	for turn := int64(1); turn <= 8; turn++ {
		sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "thinking turn " + string(rune('0'+turn)), Turn: turn,
			ToolCalls: []ToolCall{{ID: "c" + string(rune('0'+turn)), Name: "shell_execute"}}})
		sm.AddMessage(context.Background(), Message{Role: "tool", ToolUseID: "c" + string(rune('0'+turn)), Content: big, Turn: turn})
	}
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "operator ruling: keep it exact", Turn: 5})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "current work", Turn: 9,
		ToolCalls: []ToolCall{{ID: "c9", Name: "shell_execute"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ToolUseID: "c9", Content: "pending result", Turn: 9})
}

// TestInloopFold_ParseLenient — the summary is found inside prose and strict
// JSON alike; junk and empty summaries are refused.
func TestInloopFold_ParseLenient(t *testing.T) {
	s, ok := parseFoldReply(`Here you go: {"summary": "state of work"} hope that helps`)
	require.True(t, ok)
	assert.Equal(t, "state of work", s)
	_, ok = parseFoldReply("no json here")
	assert.False(t, ok)
	_, ok = parseFoldReply(`{"summary": "  "}`)
	assert.False(t, ok)
}

// TestInloopFold_FullCollapseSemantics — one call: the last k turns before the
// pending pair survive as text, whichever role they are; the pending pair is
// whole; everything else is gone, the opening ask with it, and the summary is
// L2.
func TestInloopFold_FullCollapseSemantics(t *testing.T) {
	sm := newCompileMemory(t)
	sm.SetProtectedRecentTurns(2)
	seedInloopSession(sm)

	applied, _ := sm.ApplyFoldReply(context.Background(), "LTV fix in hand; nulls in discounts ruled the cause")

	require.True(t, applied)
	assert.Contains(t, sm.summary.text, "nulls in discounts")
	var users []string
	var results, keptAssistants int
	var pendingWhole bool
	for _, m := range sm.GetMessages() {
		switch m.Role {
		case "user":
			users = append(users, m.Content)
		case "tool":
			results++
		case "assistant":
			keptAssistants++
			if m.Content == "current work" {
				pendingWhole = len(m.ToolCalls) == 1
			} else {
				assert.Nil(t, m.ToolCalls, "a kept turn is stripped of its calls")
			}
		}
	}
	assert.Equal(t, []string{"operator ruling: keep it exact"}, users,
		"the ruling is one of the last 2 turns and stays; the opening ask is older and folds")
	assert.Equal(t, 1, results, "only the pending pair's result remains")
	assert.True(t, pendingWhole, "the pending pair keeps its call signature")
	assert.Equal(t, 2, keptAssistants, "the last step's text + the pending assistant")
}

// A user's pasted file is a turn like any other: inside the last k turns it
// stays whole, and once the work has moved past it, it folds into the summary.
func TestInloopFold_APasteFoldsOnceTheWorkMovesPastIt(t *testing.T) {
	ctx := context.Background()
	paste := "here is the log:\n" + strings.Repeat("ERROR something failed at line 42\n", 500)
	build := func(stepsAfter int) *SegmentedMemory {
		sm := newCompileMemory(t)
		sm.SetProtectedRecentTurns(3)
		sm.AddMessage(ctx, Message{Role: "user", Content: paste, Turn: 1})
		for i := 0; i <= stepsAfter; i++ {
			id := "c" + strconv.Itoa(i)
			sm.AddMessage(ctx, Message{Role: "assistant", Content: "looking at it " + strconv.Itoa(i), Turn: 1,
				ToolCalls: []ToolCall{{ID: id, Name: "shell_execute"}}})
			sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: id, Content: "ok", Turn: 1})
		}
		return sm
	}
	holds := func(sm *SegmentedMemory) bool {
		for _, m := range sm.GetMessages() {
			if m.Role == "user" && m.Content == paste {
				return true
			}
		}
		return false
	}

	recent := build(2) // paste, two steps, the pending one: the paste is the 3rd turn back
	applied, _ := recent.ApplyFoldReply(ctx, "reading the log")
	require.True(t, applied)
	assert.True(t, holds(recent), "a paste inside the last k turns was folded")

	old := build(6) // six steps after it: the paste is far outside the window
	applied, _ = old.ApplyFoldReply(ctx, "the log showed a failure at line 42")
	require.True(t, applied)
	assert.False(t, holds(old), "a paste the work has moved past is still in context")
}

// TestInloopFold_EmptySummaryRefused — a fold without a summary is amnesia.
func TestInloopFold_EmptySummaryRefused(t *testing.T) {
	sm := newCompileMemory(t)
	seedInloopSession(sm)
	before := len(sm.GetMessages())

	applied, _ := sm.ApplyFoldReply(context.Background(), "  ")

	assert.False(t, applied)
	assert.Len(t, sm.GetMessages(), before)
}

// TestInloopFold_NeedsFoldTracksMark — under the mark no fold is asked for;
// past it, one is.
func TestInloopFold_NeedsFoldTracksMark(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 4000, 400) // tiny window: mark ≈ a few K tokens
	sm.SetThreshold(1024)
	assert.False(t, sm.NeedsFold())
	for turn := int64(1); turn <= 9; turn++ {
		sm.AddMessage(context.Background(), Message{Role: "assistant", Content: strings.Repeat("x ", 800), Turn: turn})
	}
	assert.True(t, sm.NeedsFold())
}

// TestInloopFold_InFlightResultsNeverTrigger — the estimate excludes tool
// results the model has not yet consumed (rows after the last assistant row).
// A fold fired on their mass cannot commit — the collapse protects the
// pending pair — so unconsumed mass must not arm the fold. Once the model's
// next response consumes the batch, the same mass counts and the fold arms.
func TestInloopFold_InFlightResultsNeverTrigger(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 4000, 400) // tiny window: mark ≈ a few K tokens
	sm.SetThreshold(100000)                    // no offload stubs — raw results in the estimate
	ctx := context.Background()

	sm.AddMessage(ctx, Message{Role: "user", Content: "find the bug", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c1", Name: "file_read", Input: map[string]interface{}{}}}})
	// A huge unconsumed batch: far past the mark on its own.
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "c1", Content: strings.Repeat("y ", 8000), Turn: 1})

	assert.False(t, sm.NeedsFold(),
		"unconsumed results are unshedable — they must not arm the fold")

	// The model consumes the batch: the same mass is now past, and sheddable.
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "the bug is in the parser", Turn: 2})

	assert.True(t, sm.NeedsFold(),
		"once consumed, the batch counts and the fold arms")
}

// TestInloopFold_SummaryNotCountedByTrigger — the summary is the floor of the
// next fold, not shed by it; it must not push the trigger. Right after a fold
// whose summary alone exceeds the mark, the trigger is quiet; it arms again
// only once the new past crosses the mark by itself.
func TestInloopFold_SummaryNotCountedByTrigger(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 4000, 400) // mark ≈ 4K tokens
	sm.SetThreshold(100000)
	sm.SetProtectedRecentTurns(1)
	ctx := context.Background()
	sm.AddMessage(ctx, Message{Role: "user", Content: "the job", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1, ToolCalls: []ToolCall{{ID: "c1", Name: "file_read", Input: map[string]interface{}{}}}})
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "c1", Content: strings.Repeat("x ", 5000), Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "read it", Turn: 2})
	require.True(t, sm.NeedsFold(), "the past alone is over the mark")

	applied, _ := sm.ApplyFoldReply(ctx, strings.Repeat("summary ", 3000)) // summary alone ≈ over the mark
	require.True(t, applied)
	sm.mu.Lock()
	full, noSummary := sm.estimateLocked(), sm.estimateOptLocked(true)
	sm.mu.Unlock()
	assert.Greater(t, full, noSummary, "the summary is in the full estimate")
	assert.False(t, sm.NeedsFold(), "the summary alone must not arm the fold")

	// New past: consumed results until the past by itself crosses the mark.
	for turn := int64(3); turn <= 12; turn++ {
		id := "c" + strconv.FormatInt(turn, 10)
		sm.AddMessage(ctx, Message{Role: "assistant", Turn: turn, ToolCalls: []ToolCall{{ID: id, Name: "file_read", Input: map[string]interface{}{}}}})
		sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: id, Content: strings.Repeat("y ", 1500), Turn: turn})
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "done reading", Turn: 13})
	assert.True(t, sm.NeedsFold(), "the past has crossed the mark on its own")
}

// A skill loaded long ago and loaded again by the step in flight is current:
// the in-loop fold excises the old load pair but neither deactivates the skill
// nor notes it as folded. One loaded only in what is excised is deactivated
// and noted.
func TestInloopFold_KeepsASkillReloadedByTheStepInFlight(t *testing.T) {
	sm := newCompileMemory(t)
	var deactivated []string
	sm.SetSkillDeactivationHook(func(_, name string) { deactivated = append(deactivated, name) })
	load := func(turn int64, id, name string) {
		sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: turn, ToolCalls: []ToolCall{{
			ID: id, Name: "manage_skills", Input: map[string]interface{}{"action": "load", "name": name}}}})
		sm.AddMessage(context.Background(), Message{Role: "tool", ToolUseID: id, Content: "Skill loaded: " + name, Turn: turn})
	}
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "the JOB", Turn: 1})
	load(1, "l-alpha", "alpha")
	load(1, "l-beta", "beta")
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "done 1", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "next", Turn: 2})
	load(2, "l-alpha-again", "alpha") // the step in flight

	applied, _ := sm.ApplyFoldReply(context.Background(), "state of work")
	require.True(t, applied)
	assert.Equal(t, []string{"beta"}, deactivated, "only the skill the fold took away is deactivated")
	assert.Contains(t, sm.summary.text, "beta")
	assert.NotContains(t, sm.summary.text, "alpha", "a skill loaded again by the step in flight is noted as folded")
}

// The fold ask lives in the prompt registry with every other prompt loom
// sends, so an embedder changes it without a rebuild. Shipped, it is the ask
// the harness was written against; an agent with no registry sends that one.
func TestInloopFold_TheAskComesFromTheRegistry(t *testing.T) {
	reg := prompts.NewFileRegistry("../../prompts")
	require.NoError(t, reg.Reload(context.Background()))
	shipped, err := reg.Get(context.Background(), foldPromptKey, nil)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(foldInstruction), strings.TrimSpace(shipped), "the shipped prompt file and the built-in ask differ")

	withRegistry := &Agent{prompts: reg}
	assert.Equal(t, strings.TrimSpace(shipped), withRegistry.foldAsk(context.Background()))
	assert.Equal(t, foldInstruction, (&Agent{}).foldAsk(context.Background()), "an agent with no registry sends the built-in ask")
}
