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

// A compaction folds the older turns into the summary and keeps the newest
// turns verbatim. It folds only: no tool result is stubbed. The summary is the
// user-role system-reminder row (summaryRowPrefix), not a system message.
func TestCompact_FoldsOldTurnsAndKeepsRecentVerbatim(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)

	assert.True(t, res.Compacted, "the compaction reports that it compacted")
	assert.True(t, res.TurnsFolded, "the older turns were folded")
	assert.Greater(t, res.TokensSaved(), 0, "the fold saved tokens")
	assert.Less(t, res.TokensAfter, res.TokensBefore, "the context is smaller after the fold")
	assert.Equal(t, 1, res.SummaryVersion, "one fold writes summary version 1")

	var summary string
	sent := map[int64]bool{}
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "user" && strings.HasPrefix(m.Content, summaryRowPrefix) {
			summary = m.Content
			continue
		}
		if m.Role != "system" {
			sent[m.Turn] = true
		}
	}
	assert.Contains(t, summary, "open approvals", "the summary row carries the compressor's text")
	assert.True(t, sent[5] && sent[6], "the newest two turns stay verbatim")
	assert.False(t, sent[1] || sent[2] || sent[3] || sent[4], "older turns live in the summary")
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "tool" && m.Turn >= 5 {
			assert.Equal(t, denseResult(int(m.Turn))+denseResult(int(m.Turn)+50), m.Content, "kept turns keep their results whole")
		}
	}
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

// windowedCompressor refuses an input larger than its window, as a real
// compressor model does, and records the size of each input it was given.
// failAfter > 0 makes every call after the first failAfter calls fail.
type windowedCompressor struct {
	mu        sync.Mutex
	limit     int
	failAfter int
	inputs    []int
}

func (c *windowedCompressor) CompressMessages(_ context.Context, msgs []Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	c.inputs = append(c.inputs, tokenFigure(n))
	if tokenFigure(n) > c.limit {
		return "", errors.New("context too long for the compressor")
	}
	if c.failAfter > 0 && len(c.inputs) > c.failAfter {
		return "", errors.New("provider down")
	}
	return fmt.Sprintf("SUMMARY %d: kept the settled SQL", len(c.inputs)), nil
}
func (c *windowedCompressor) IsEnabled() bool { return true }

// A session already over its window is the case a compaction must rescue.
// Its older turns are larger than the compressor's window, so one fold call
// would be refused; the compaction folds them in chunks that fit. The stored
// conversation is not changed: the newest turns keep their results whole.
func TestCompact_RescuesASessionAlreadyOverItsWindow(t *testing.T) {
	// usable 11000; twelve turns of ~1.5K-token results is over it.
	sm := toolTurnMemory(t, 12000, 1000, 12)
	comp := &windowedCompressor{limit: 11000}
	sm.SetCompressor(comp)
	sm.mu.Lock()
	start := sm.estimateLocked()
	sm.mu.Unlock()
	require.Greater(t, start, 11000, "fixture must start over the window")

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err, "the compaction must not fail on a region larger than the compressor's window")

	require.True(t, res.Compacted, "the compaction ran")
	assert.Equal(t, 2, res.TurnsKept, "the newest two turns are kept")
	assert.Greater(t, len(comp.inputs), 1, "the region was folded in more than one call")
	for i, n := range comp.inputs {
		assert.LessOrEqual(t, n, comp.limit, "compressor input %d is over the compressor's window", i)
	}
	assert.LessOrEqual(t, res.TokensAfter, 11000, "the compacted session fits again")
	sm.mu.Lock()
	after, usable := sm.estimateLocked(), sm.usableLocked()
	sm.mu.Unlock()
	assert.LessOrEqual(t, after, usable, "the estimate is under the usable window")
	assert.Equal(t, map[int64]bool{11: true, 12: true}, unfoldedTurns(sm), "only the newest two turns stay verbatim")
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "tool" {
			assert.Equal(t, denseResult(int(m.Turn))+denseResult(int(m.Turn)+50), m.Content, "a kept result is not whole")
		}
	}
}

// A region that fits the compressor's window is folded in one call, as
// before: the bound adds no call to the common case.
func TestCompact_FoldsARegionThatFitsInOneCall(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 6)
	comp := &windowedCompressor{limit: 180000}
	sm.SetCompressor(comp)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)
	assert.True(t, res.Compacted)
	assert.Len(t, comp.inputs, 1, "a region that fits took more than one compressor call")
}

// A later chunk that fails after an earlier chunk folded leaves the earlier
// chunk folded, and the result says how many turns stay verbatim.
func TestCompact_AFailedLaterChunkReportsTheTurnsKept(t *testing.T) {
	sm := toolTurnMemory(t, 12000, 1000, 12)
	comp := &windowedCompressor{limit: 11000, failAfter: 1}
	sm.SetCompressor(comp)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err, "the first chunk folded, so the compaction did something")
	require.True(t, res.Compacted)
	kept := len(unfoldedTurns(sm))
	assert.Greater(t, kept, 2, "the fixture must leave more than the asked turns")
	assert.Equal(t, kept, res.TurnsKept, "the result must say how many turns stay verbatim")
	assert.False(t, sm.foldRequireSummary, "the rule does not leak into relief")
}

// The relief ladder uses the same bound: a rung whose region is larger than
// the compressor's window is folded in chunks with real summaries, and not
// dropped behind an "unsummarized" line.
func TestReleasePressure_FoldsARegionLargerThanTheCompressorWindowInChunks(t *testing.T) {
	sm := toolTurnMemory(t, 12000, 1000, 12)
	sm.SetProtectedRecentTurns(2)
	comp := &windowedCompressor{limit: 11000}
	sm.SetCompressor(comp)

	shed, _, _ := sm.ReleasePressure(context.Background(), pressureRecoveryPenalty)
	require.True(t, shed, "relief shed nothing")
	for i, n := range comp.inputs {
		assert.LessOrEqual(t, n, comp.limit, "compressor input %d is over the compressor's window", i)
	}
	sm.mu.Lock()
	summary := sm.summary.text
	sm.mu.Unlock()
	assert.NotContains(t, summary, "unsummarized", "relief dropped a region it could have summarized in chunks")
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

	b := measureRequest(sm.GetMessagesForLLM(), nil, sm.SummaryRowPrefix())
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
