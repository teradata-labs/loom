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
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// recordingCompressor returns a fixed summary and keeps the input it was given.
type recordingCompressor struct {
	mu    sync.Mutex
	input []Message
}

func (c *recordingCompressor) CompressMessages(_ context.Context, msgs []Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = append([]Message(nil), msgs...)
	return "SUMMARY: kept the settled SQL and the open approvals", nil
}
func (c *recordingCompressor) IsEnabled() bool { return true }

// failingCompressor is an enabled compressor that always errors.
type failingCompressor struct{ calls int }

func (c *failingCompressor) CompressMessages(context.Context, []Message) (string, error) {
	c.calls++
	return "", errors.New("provider down")
}
func (c *failingCompressor) IsEnabled() bool { return true }

func unfoldedTurns(sm *SegmentedMemory) map[int64]bool {
	turns := map[int64]bool{}
	for _, m := range sm.contextMessages {
		if !m.Folded {
			turns[m.Turn] = true
		}
	}
	return turns
}

// proseMemory builds turns 1..n of user/assistant prose, each message ~3 dense
// rows.
func proseMemory(t *testing.T, maxTokens, reserved, turns int) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(16384)
	for turn := 1; turn <= turns; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseResult(turn) + denseResult(turn+100), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseResult(turn+200) + denseResult(turn+300), Turn: int64(turn)})
	}
	return sm
}

// toolTurnMemory builds turns 1..turns, each with one tool call and a large result.
func toolTurnMemory(t *testing.T, maxTokens, reserved, turns int) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(1 << 20)
	for turn := 1; turn <= turns; turn++ {
		id := fmt.Sprintf("c%d", turn)
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("question %d", turn), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Turn: int64(turn),
			ToolCalls: []ToolCall{{ID: id, Name: "run_query", Input: map[string]interface{}{"n": turn}}}})
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", turn*10), ToolUseID: id,
			Content: denseResult(turn) + denseResult(turn+50), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: fmt.Sprintf("answer %d", turn), Turn: int64(turn)})
	}
	return sm
}

func TestCompact_FoldsOldTurnsAndKeepsRecentVerbatim(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)

	assert.True(t, res.Compacted)
	assert.True(t, res.TurnsFolded)
	assert.Greater(t, res.TokensSaved(), 0)
	assert.Less(t, res.TokensAfter, res.TokensBefore)
	assert.Equal(t, 1, res.SummaryVersion)

	var summary string
	sent := map[int64]bool{}
	for _, m := range sm.GetMessagesForLLM() {
		if strings.HasPrefix(m.Content, summaryRowPrefix) {
			summary = m.Content
			continue
		}
		if m.Role != "system" {
			sent[m.Turn] = true
		}
	}
	assert.Contains(t, summary, "open approvals")
	assert.True(t, sent[5] && sent[6], "the newest two turns stay verbatim")
	assert.False(t, sent[1] || sent[2] || sent[3] || sent[4], "older turns live in the summary")
}

func TestCompact_FocusReachesTheCompressor(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 5)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	_, err := sm.Compact(context.Background(), CompactOptions{Focus: "keep the SQL we settled on", KeepTurns: 1})
	require.NoError(t, err)

	var joined []string
	for _, m := range comp.input {
		joined = append(joined, m.Content)
	}
	assert.Contains(t, strings.Join(joined, "\n"), "keep the SQL we settled on")
	assert.Empty(t, sm.compactFocus, "the focus is cleared once the compaction ends")
}

func TestCompact_NothingOlderThanKeepTurnsIsANoOp(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 2)
	sm.SetCompressor(&recordingCompressor{})

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Equal(t, res.TokensBefore, res.TokensAfter)
}

func TestCompact_RescuesASessionAlreadyOverItsWindow(t *testing.T) {
	// usable 11000; five turns of ~1.5K-token results plus the rest is over it.
	sm := toolTurnMemory(t, 12000, 1000, 12)
	sm.SetCompressor(&recordingCompressor{})
	sm.reliefInFlight = false
	sm.mu.Lock()
	start := sm.estimateLocked()
	sm.mu.Unlock()
	require.Greater(t, start, 11000, "fixture must start over the window")

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)

	require.True(t, res.Compacted)
	assert.LessOrEqual(t, res.TokensAfter, 11000, "the compacted session fits again")
}

func TestCompact_RefusesWhenAPassIsAlreadyRunning(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 4)
	sm.reliefInFlight = true

	_, err := sm.Compact(context.Background(), CompactOptions{})

	assert.ErrorIs(t, err, ErrCompactionBusy)
}

// CompactSession runs under its own span, which says what it did.
func TestCompactSession_RecordsASpan(t *testing.T) {
	tracer := &spanKeepingTracer{}
	sm := toolTurnMemory(t, 200000, 20000, 6)
	sm.SetCompressor(&recordingCompressor{})
	ag := reliefSeamAgent(t, &capturingLLM{}, "compact_span", sm, WithTracer(tracer))

	res, err := ag.CompactSession(context.Background(), "compact_span", CompactOptions{Focus: "the SQL"})

	require.NoError(t, err)
	assert.True(t, res.Compacted)
	assert.Equal(t, DefaultCompactKeepTurns, res.TurnsKept)
	var span *observability.Span
	for _, s := range tracer.spans {
		if s.Name == "agent.compact_session" {
			span = s
		}
	}
	require.NotNil(t, span)
	assert.Equal(t, "compact_span", span.Attributes[observability.AttrSessionID])
	assert.Equal(t, true, span.Attributes["compaction.turns_folded"])
	assert.Equal(t, true, span.Attributes["compaction.focus"])
	assert.Equal(t, res.TokensAfter, span.Attributes["compaction.tokens_after"])
}

// A requested compaction and the in-loop fold are one mechanism: on a session
// the in-loop fold has already folded, Compact hands the compressor that
// summary, replaces it, and keeps the newest turns; the request-size log
// counts the summary row as the summary.
func TestCompact_BuildsOnTheSummaryOfAnInloopFold(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 8)
	sm.SetProtectedRecentTurns(4)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	applied, _ := sm.ApplyFoldReply(context.Background(), "INLOOP: tables one to four are read")
	require.True(t, applied, "the in-loop fold did not commit")
	require.Equal(t, 1, sm.summary.n)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)
	assert.True(t, res.Compacted)
	assert.Equal(t, 2, res.SummaryVersion, "the requested compaction wrote the next summary version")

	var given []string
	for _, m := range comp.input {
		given = append(given, m.Content)
	}
	assert.Contains(t, strings.Join(given, "\n"), "INLOOP: tables one to four are read", "the compressor was not given the in-loop fold's summary")

	turns := map[int64]bool{}
	for turn := range unfoldedTurns(sm) {
		turns[turn] = true
	}
	assert.Equal(t, map[int64]bool{7: true, 8: true}, turns, "the newest two turns stay verbatim")

	b := measureRequest(sm.GetMessagesForLLM(), nil)
	assert.Positive(t, b.SummaryTokens, "the log did not count the summary row")
}

// The figures before and after come from one counter: a compaction that
// shrinks the context never reports growth, whichever side of the start mark
// each lands on.
func TestCompact_ReportsBeforeAndAfterFromOneCounter(t *testing.T) {
	prose := strings.Repeat("The quarterly revenue analysis shows that customers in the northern region increased their spending considerably during the holiday season. ", 14)
	for usable := 4000; usable <= 40000; usable += 500 {
		sm := NewSegmentedMemory("ROM", usable+1000, 1000)
		for turn := 1; turn <= 8; turn++ {
			sm.AddMessage(context.Background(), Message{Role: "user", Content: fmt.Sprintf("q%d ", turn) + prose, Turn: int64(turn), ID: fmt.Sprint(turn * 10)})
			sm.AddMessage(context.Background(), Message{Role: "assistant", Content: fmt.Sprintf("a%d ", turn) + prose, Turn: int64(turn), ID: fmt.Sprint(turn*10 + 1)})
		}
		sm.SetCompressor(&recordingCompressor{})

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 6})
		require.NoError(t, err)
		require.True(t, res.Compacted)
		assert.Less(t, res.TokensAfter, res.TokensBefore, "usable window %d", usable)
	}
}

// A compressor that panics inside a requested compaction leaves nothing set
// for a later fold: the rule that a requested fold needs a real summary is
// Compact's alone.
func TestCompact_APanicLeavesNoRuleForALaterFold(t *testing.T) {
	sm := toolTurnMemory(t, 12000, 1000, 12)
	sm.SetCompressor(&panickingCompressor{})
	func() {
		defer func() { _ = recover() }()
		_, _ = sm.Compact(context.Background(), CompactOptions{KeepTurns: 2, Focus: "keep the SQL"})
	}()
	assert.False(t, sm.foldRequireSummary, "the requested fold's rule outlived it")
	assert.Empty(t, sm.compactFocus)
	assert.False(t, sm.reliefInFlight)
}
