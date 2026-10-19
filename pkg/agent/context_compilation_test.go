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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// --- write rule §4.1: truncation ---------------------------------------------

func TestTruncateToolRowContent_Bounds(t *testing.T) {
	threshold := 1024
	content := strings.Repeat("x", 5000)
	stored := truncateToolRowContent(content, threshold, "")
	require.LessOrEqual(t, len(stored), threshold,
		"a truncated row must never itself exceed the threshold (§4.1)")
	assert.True(t, strings.HasSuffix(stored, "and re-run to see more.]"),
		"the normative tail closes the stored row")
	assert.Contains(t, stored, "of 5000 bytes shown")
	// Below the bound: unchanged.
	small := strings.Repeat("y", 100)
	assert.Equal(t, small, truncateToolRowContent(small, threshold, ""))
	// At the bound exactly: unchanged (stored = threshold is legal).
	exact := strings.Repeat("z", threshold)
	assert.Equal(t, exact, truncateToolRowContent(exact, threshold, ""))
}

func TestTruncateToolRowContent_RuneBoundary(t *testing.T) {
	content := strings.Repeat("é", 4000) // 2-byte runes
	stored := truncateToolRowContent(content, 1024, "")
	require.LessOrEqual(t, len(stored), 1024)
	core := stored[:strings.LastIndex(stored, "[truncated: ")]
	assert.True(t, strings.HasSuffix(core, "é") || core == "",
		"the core is cut backward to a UTF-8 rune boundary")
}

// --- §5.5: preview -----------------------------------------------------------

func TestPreviewOf_CollapsesWhitespaceAndCaps(t *testing.T) {
	content := "{\n  \"rows\": [\n    1,\n    2\n  ]\n}" + strings.Repeat(" filler", 100)
	p := previewOf(content)
	assert.LessOrEqual(t, len(p), 160, "preview is capped at 160 bytes")
	assert.NotContains(t, p, "\n", "whitespace runs collapse to a single space")
	assert.True(t, strings.HasPrefix(p, "{ \"rows\": [ 1, 2 ] }"),
		"a structured payload previews as content, never as a lone bracket")
}

// --- §5.2 step 6: render cases -----------------------------------------------

func newCompileMemory(t *testing.T) *SegmentedMemory {
	t.Helper()
	sm := NewSegmentedMemory("ROM-CONTENT", 200000, 20000)
	sm.SetThreshold(1024)
	return sm
}

func TestCompile_AtThresholdRendersFull(t *testing.T) {
	sm := newCompileMemory(t)
	page := strings.Repeat("p", 1024) // exactly at the threshold
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "7", ToolUseID: "c1", Content: page, Turn: 1})

	out := sm.GetMessagesForLLM()
	for _, m := range out {
		if m.Role == "tool" {
			assert.Equal(t, page, m.Content, "strictly over the threshold, not at it — a page must never stub itself")
		}
	}
}

func TestCompile_UserTurnRendersArrivalStampInViewOnly(t *testing.T) {
	sm := newCompileMemory(t)
	ts := time.Date(2026, 8, 10, 14, 5, 0, 0, time.UTC)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "what ran today?", Turn: 1, Timestamp: ts})

	out := sm.GetMessagesForLLM()
	var rendered string
	for _, m := range out {
		if m.Role == "user" {
			rendered = m.Content
		}
	}
	assert.Equal(t, "[Mon 2026-08-10 14:05 UTC] what ran today?", rendered,
		"the compiled user turn carries the arrival stamp, rendered from Timestamp in UTC")

	// The stored row is untouched — the stamp is a pure render condition.
	stored := sm.GetRecentConversationTurns(1)
	require.Len(t, stored, 1)
	assert.Equal(t, "what ran today?", stored[0].Content,
		"stored Content stays verbatim; only the view is stamped")
}

func TestCompile_UserTurnWithoutTimestampRendersVerbatim(t *testing.T) {
	sm := newCompileMemory(t)
	// Legacy row with a zero Timestamp must render unchanged.
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "legacy turn", Turn: 1})

	out := sm.GetMessagesForLLM()
	for _, m := range out {
		if m.Role == "user" {
			assert.Equal(t, "legacy turn", m.Content, "no stamp is invented for a timestamp-less row")
		}
	}
}

// TestCompile_MultimodalUserTurnStampsLeadingTextBlock verifies the arrival
// stamp reaches multimodal providers: they build the request from ContentBlocks,
// so the stamp must land on the leading text block too, not just Content. The
// stored blocks stay verbatim — only the compiled view is stamped.
func TestCompile_MultimodalUserTurnStampsLeadingTextBlock(t *testing.T) {
	sm := newCompileMemory(t)
	ts := time.Date(2026, 8, 10, 14, 5, 0, 0, time.UTC)
	blocks := []ContentBlock{
		{Type: "text", Text: "what is in this image?"},
		{Type: "image", Image: &types.ImageContent{Type: "image", Source: types.ImageSource{Type: "base64", MediaType: "image/png", Data: "aGVsbG8="}}},
	}
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "what is in this image?", ContentBlocks: blocks, Turn: 1, Timestamp: ts})

	out := sm.GetMessagesForLLM()
	var rendered Message
	for _, m := range out {
		if m.Role == "user" {
			rendered = m
		}
	}
	require.Len(t, rendered.ContentBlocks, 2)
	assert.Equal(t, "[Mon 2026-08-10 14:05 UTC] what is in this image?", rendered.ContentBlocks[0].Text,
		"the leading text block carries the stamp so multimodal providers see it")
	assert.Equal(t, "image", rendered.ContentBlocks[1].Type, "the image block is untouched")

	// The stored blocks are untouched — the stamp is a pure render condition.
	stored := sm.GetRecentConversationTurns(1)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].ContentBlocks, 2)
	assert.Equal(t, "what is in this image?", stored[0].ContentBlocks[0].Text,
		"stored blocks stay verbatim; only the view is stamped")
}

func TestCompile_TwoCompilesAreByteIdentical(t *testing.T) {
	sm := newCompileMemory(t)
	ts := time.Date(2026, 8, 10, 14, 5, 0, 0, time.UTC)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "hello", Turn: 1, Timestamp: ts})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "hi", Turn: 1})

	first := sm.GetMessagesForLLM()
	second := sm.GetMessagesForLLM()
	require.Equal(t, len(first), len(second))
	for i := range first {
		assert.Equal(t, first[i].Content, second[i].Content,
			"the compiled view is byte-stable across calls (Timestamp is write-once)")
	}
}

func TestCompile_LegacyEvictedFlagRendersWhole(t *testing.T) {
	sm := newCompileMemory(t)
	content := strings.Repeat("e", 900) // under threshold, flagged by an older run
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c1", Name: "shell"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "9", ToolUseID: "c1", Content: content, Turn: 1, Evicted: true})

	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "tool" {
			assert.Equal(t, content, m.Content,
				"eviction was removed: a row carrying the legacy flag renders the content the store still holds")
		}
	}
}

func TestCompile_SyntheticFailedResultForMissingPair(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c-dead", Name: "crashy", Input: map[string]interface{}{}}}})

	out := sm.GetMessagesForLLM()
	var got *Message
	for i := range out {
		if out[i].Role == "tool" && out[i].ToolUseID == "c-dead" {
			got = &out[i]
		}
	}
	require.NotNil(t, got, "a call with no matching result gets a synthetic failed result — never strip the signature")
	assert.Equal(t, syntheticFailedResult, got.Content)
}

func TestCompile_SummaryEmittedAsConversationReminder(t *testing.T) {
	// The summary is the folded PAST — conversation content, not system. It
	// renders as a user-role reminder with no cache breakpoint of its own, so
	// it never consumes a marker slot (a system summary block displaced the
	// till-NOW marker and uncached the session tail after every fold).
	sm := newCompileMemory(t)
	sm.setSummary(2, "covers msg:1-9\nstate of work")
	out := sm.GetMessagesForLLM()
	require.GreaterOrEqual(t, len(out), 2)
	assert.Equal(t, "system", out[0].Role)
	assert.Equal(t, "ROM-CONTENT", out[0].Content)
	assert.Equal(t, "user", out[1].Role)
	assert.Contains(t, out[1].Content, "Session summary")
	assert.Contains(t, out[1].Content, "covers msg:1-9\nstate of work")
	// The summary carries no DEDICATED marker; the walk may still park the
	// ordinary lastStable marker on it — that is budget-neutral.
}

// --- §5.2 releasePressure over a real store ----------------------------------

type foldCompressor struct{ out string }

func (f *foldCompressor) CompressMessages(ctx context.Context, messages []Message) (string, error) {
	return f.out, nil
}
func (f *foldCompressor) IsEnabled() bool { return true }

func TestReleasePressure_FoldsUnderPressure(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	const sessionID = "sess-relief"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	// Window sized so seven turns of whole tool rows cross the start mark and
	// the release mark (60% of usable 5400 = 3240) sits above the irreducible
	// current turn (~1740-token tool row), so the pass has a reachable target.
	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000) // > the ~5KB tool rows, so they render WHOLE, not stubs
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-4\nsummary of the old turns"})

	// Persist + mirror seven turns so T−K and T−1 have material.
	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) Message {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
		return m
	}
	for turn := 1; turn <= 7; turn++ {
		// The whole tool rows (5000 B, under the 6000 threshold) are the pressure:
		// their sum crosses the start mark, so the pass must fold to reach target.
		persist("user", fmt.Sprintf("question %d", turn), "", nil, true)
		callID := fmt.Sprintf("c%d", turn)
		persist("assistant", "", "", []ToolCall{{ID: callID, Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
		// Token-DENSE content (varied, ~4 B/tok) so the estimate reflects real
		// weight — a compressible run like "rrrr" tokenizes so cheaply that seven
		// turns never reach the start mark and no relief fires at all.
		persist("tool", strings.Repeat("scan 12,345 db=SALES cpu=678 io=9; ", 145), callID, nil, false)
	}
	require.Equal(t, int64(7), sm.CurrentTurn())

	shed, estimate, target := sm.ReleasePressure(ctx, 0)
	assert.True(t, shed, "estimate should cross the start mark and shed")
	assert.Greater(t, target, 0)
	assert.Greater(t, estimate, 0)

	// Rows of the current turn are never touched.
	for _, m := range sm.GetMessages() {
		if m.Turn == 7 {
			assert.False(t, m.Folded, "turn T is untouchable")
		}
	}

	// Fold is the only shedding mechanism: folded rows leave the database read,
	// and the summary version row persisted in the same transaction.
	require.True(t, sm.HasL2Content(), "shedding this much can only have been a fold")
	rows, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	assert.Less(t, len(rows), 21, "folded rows are filtered at the read")
	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "fold", 0)
	require.NoError(t, err)
	require.NotEmpty(t, snaps, "the fold's version row persisted in the same transaction")
	assert.Contains(t, sm.GetL2Summary(), "covers msg:")
}

// --- §8: re-fire on the durable key ------------------------------------------

func TestReFireOnRestore_DurableKey(t *testing.T) {
	m := NewMemory()
	var activated []string
	messages := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c1", Name: "manage_skills",
			Input: map[string]interface{}{"action": "load", "name": "alpha-skill"},
		}}},
		{Role: "tool", ToolUseID: "c1", Content: "Skill loaded: alpha-skill"},
		// A list call must not re-fire.
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c2", Name: "manage_skills",
			Input: map[string]interface{}{"action": "list"},
		}}},
		{Role: "tool", ToolUseID: "c2", Content: "{}"},
		// A blocked load has no "Skill loaded: " confirmation.
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c3", Name: "manage_skills",
			Input: map[string]interface{}{"action": "load", "name": "blocked-skill"},
		}}},
		{Role: "tool", ToolUseID: "c3", Content: "Skill load blocked: approval required"},
	}
	m.reFireOnRestore("sess", messages, func(sessionID, skillName string) {
		activated = append(activated, skillName)
	}, nil)
	assert.Equal(t, []string{"alpha-skill"}, activated,
		"re-fire keys on the manage_skills load call paired with a 'Skill loaded: ' confirmation")
}

func TestPressureMarks_GrowthBudget(t *testing.T) {
	// The configured MaxTokens IS G — the growth budget. The start mark is G
	// itself; the output reservation plays no part in the marks (the
	// provider's refusal backstop owns the real ceiling).
	sm := NewSegmentedMemory("ROM", 50000, 64000)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	assert.Equal(t, 50000, sm.startMarkLocked(0))
	assert.Equal(t, 60*50000/100, sm.releaseMarkLocked(0))
	// The recovery pass lowers both marks by the penalty on the same base.
	assert.Equal(t, 80*50000/100, sm.startMarkLocked(pressureRecoveryPenalty))
	assert.Equal(t, 40*50000/100, sm.releaseMarkLocked(pressureRecoveryPenalty))
}

func TestMarks_IgnoreOutputReservation(t *testing.T) {
	// G semantics: the start mark is the configured budget itself. The output
	// reservation is a wire concern (the request's max_tokens); it must not
	// bend the fold policy. The provider's refusal backstop owns the ceiling.
	const g = 50000
	for _, reserve := range []int{0, 20000, 64000, 128000} {
		sm := NewSegmentedMemory("ROM", g, reserve)
		sm.mu.Lock()
		assert.Equal(t, g, sm.startMarkLocked(0),
			"reserve=%d must not move the start mark", reserve)
		sm.mu.Unlock()
	}
}

func TestPressureMarks_ProfileDrivenWithFallback(t *testing.T) {

	// Start is always G; the profile's warning threshold sets shed depth.
	custom := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	custom.CriticalThresholdPercent = 80
	custom.WarningThresholdPercent = 50
	sm := NewSegmentedMemoryWithCompression("ROM", 100000, 10000, custom)
	sm.mu.Lock()
	assert.Equal(t, 100000, sm.startMarkLocked(0))
	assert.Equal(t, 50*100000/100, sm.releaseMarkLocked(0))
	sm.mu.Unlock()

	// A hand-built profile with a missing or inverted pair falls back to 90/60.
	for _, bad := range []struct{ critical, warning int }{
		{0, 0},    // unset
		{50, 80},  // inverted band
		{90, 0},   // zero release
		{120, 60}, // start over 100
	} {
		p := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
		p.CriticalThresholdPercent = bad.critical
		p.WarningThresholdPercent = bad.warning
		sm := NewSegmentedMemoryWithCompression("ROM", 100000, 10000, p)
		sm.mu.Lock()
		assert.Equal(t, 100000, sm.startMarkLocked(0), "critical=%d warning=%d", bad.critical, bad.warning)
		assert.Equal(t, 60*100000/100, sm.releaseMarkLocked(0), "critical=%d warning=%d", bad.critical, bad.warning)
		sm.mu.Unlock()
	}
}

// gatedCompressor signals when the fold reaches it, then blocks until released.
type gatedCompressor struct {
	enterOnce sync.Once
	entered   chan struct{}
	release   chan struct{}
	out       string
}

func (g *gatedCompressor) CompressMessages(ctx context.Context, _ []Message) (string, error) {
	g.enterOnce.Do(func() { close(g.entered) })
	<-g.release
	return g.out, nil
}
func (g *gatedCompressor) IsEnabled() bool { return true }

// reliefConversation persists conversation-only turns (nothing evictable), so
// the relief ladder must escalate to fold.
func reliefConversation(t *testing.T, sm *SegmentedMemory, store *SessionStore, sessionID string, turns int) {
	t.Helper()
	ctx := context.Background()
	for turn := 1; turn <= turns; turn++ {
		u := Message{Role: "user", Content: fmt.Sprintf("turn %d: ", turn) + strings.Repeat("grant audit 123 db=SALES cpu=456; ", 100)}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &u, true))
		sm.AddMessage(ctx, u)
		a := Message{Role: "assistant", Content: fmt.Sprintf("noted %d", turn)}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &a, false))
		sm.AddMessage(ctx, a)
	}
}

// TestReleasePressure_ReadersRunDuringFoldCompress proves the lock is released
// across the fold's compressor call: a reader completes while the compressor
// is in flight. On a build that holds the write lock through the LLM call this
// fails — the reader blocks until the compressor returns.
func TestReleasePressure_ReadersRunDuringFoldCompress(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "sess-relief-readers"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	g := &gatedCompressor{entered: make(chan struct{}), release: make(chan struct{}), out: "covers msg:1-999\nfolded conversation"}
	sm.SetCompressor(g)

	reliefConversation(t, sm, store, sessionID, 7)

	done := make(chan struct{})
	go func() { defer close(done); sm.ReleasePressure(ctx, 0) }()

	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("relief never reached the fold compressor")
	}

	readerDone := make(chan struct{})
	go func() { defer close(readerDone); _ = sm.GetL2Summary(); _ = sm.GetTokenCount() }()
	select {
	case <-readerDone:
		// Readers run while the compressor is in flight — the lock breathes.
	case <-time.After(2 * time.Second):
		t.Fatal("reader blocked while the fold compressor was in flight — the write lock is held across the LLM call")
	}

	close(g.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("relief pass did not finish after the compressor was released")
	}
	assert.Contains(t, sm.GetL2Summary(), "folded conversation")
}

// racingCompressor mutates the summary DURING its first compress call —
// possible only because the lock is released there — to force the snapshot
// validation to fire.
type racingCompressor struct {
	sm    *SegmentedMemory
	raced bool
	out   string
}

func (r *racingCompressor) CompressMessages(ctx context.Context, _ []Message) (string, error) {
	if !r.raced {
		r.raced = true
		// setSummary takes sm.mu itself: on a build holding the lock across
		// the compressor call this self-deadlocks — which is the point.
		r.sm.setSummary(99, "raced summary")
	}
	return r.out, nil
}
func (r *racingCompressor) IsEnabled() bool { return true }

// TestReleasePressure_FoldDiscardsStaleSnapshot proves a fold whose snapshot
// was invalidated mid-compress is discarded, never committed: the pass folds
// again on top of the new state (version 100), instead of clobbering it with
// the stale result (version 1 — the lost update).
func TestReleasePressure_FoldDiscardsStaleSnapshot(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "sess-relief-stale"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	rc := &racingCompressor{sm: sm, out: "covers msg:1-999\nrebuilt on the raced state"}
	sm.SetCompressor(rc)

	reliefConversation(t, sm, store, sessionID, 7)

	shed, _, _ := sm.ReleasePressure(ctx, 0)
	assert.True(t, shed, "the pass sheds via the second, valid fold")

	sm.mu.Lock()
	n := sm.summary.n
	sm.mu.Unlock()
	assert.Equal(t, 100, n,
		"the stale fold (would-be version 1) must be discarded; the retry builds version 100 on top of the raced state")
	assert.Contains(t, sm.GetL2Summary(), "rebuilt on the raced state")
}

// TestThresholdFloor_AllConsumers pins the §4.1 invariant at every seam that
// carries the threshold: a configured value below minThreshold cannot hold
// stored = core + tail ≤ threshold, so each consumer clamps. Before the clamp,
// Memory.thresholdOrDefault and Agent.contextThreshold returned the raw value
// and a persisted row exceeded its own bound.
func TestThresholdFloor_AllConsumers(t *testing.T) {
	const tiny = 50

	m := NewMemory()
	m.SetThresholdBytes(tiny)
	assert.GreaterOrEqual(t, m.thresholdOrDefault(), minThreshold,
		"the persist-time row bound clamps to the floor")

	ag := NewAgent(nil, nil)
	ag.SetSharedMemoryThreshold(tiny)
	assert.GreaterOrEqual(t, ag.contextThreshold(), minThreshold,
		"the retrieval/page bound clamps to the floor")

	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.SetThreshold(tiny)
	assert.GreaterOrEqual(t, sm.Threshold(), minThreshold,
		"the compile-time offload bound clamps to the floor")

	// The invariant the floor exists to protect, at the clamped value.
	stored := truncateToolRowContent(strings.Repeat("x", 5000), m.thresholdOrDefault(), "")
	assert.LessOrEqual(t, len(stored), m.thresholdOrDefault(),
		"a truncated row never exceeds its own bound")
}

// panickingCompressor models a provider client that faults mid-call.
type panickingCompressor struct{}

func (p *panickingCompressor) CompressMessages(context.Context, []Message) (string, error) {
	panic("provider client faulted mid-compress")
}
func (p *panickingCompressor) IsEnabled() bool { return true }

// TestReleasePressure_CompressorPanicDoesNotCorruptTheLock pins the unwind
// contract. fold releases sm.mu across the compressor call while
// ReleasePressure holds `defer sm.mu.Unlock()` for the whole pass. If the
// re-lock were sequential rather than deferred, a panic in the compressor would
// leave the mutex unlocked, and that deferred unlock would hit an unlocked
// mutex — a FATAL runtime throw (recover cannot catch it), turning a
// recoverable provider fault into a process crash. With the deferred re-lock
// the panic stays an ordinary panic and the mutex is left usable.
func TestReleasePressure_CompressorPanicDoesNotCorruptTheLock(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const sessionID = "sess-relief-panic"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&panickingCompressor{})

	reliefConversation(t, sm, store, sessionID, 7)

	assert.Panics(t, func() { _, _, _ = sm.ReleasePressure(ctx, 0) },
		"the compressor's panic propagates as a panic, not a fatal double-unlock")

	// The decisive assertion: the mutex is in a sane state afterwards. A
	// sequential re-lock would have crashed the process before reaching here.
	done := make(chan struct{})
	go func() {
		sm.mu.Lock()
		sm.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mutex left in an unusable state after the compressor panic")
	}
}

// TestPressureMarks_PenaltyNeverGoesNegative pins the recovery pass against a
// configured profile whose release mark is below the penalty. Validate() admits
// warning=10/critical=30, and an unclamped subtraction then yields a negative
// target — which `estimate <= target` can never satisfy, so relief runs every
// rung of the ladder and folds the whole of L1 after one provider refusal.
func TestPressureMarks_PenaltyNeverGoesNegative(t *testing.T) {
	p := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	p.WarningThresholdPercent = 10
	p.CriticalThresholdPercent = 30
	require.NoError(t, p.Validate(), "the profile the marks must survive is a VALID one")

	sm := NewSegmentedMemoryWithCompression("ROM", 200000, 20000, p)
	sm.mu.Lock()
	defer sm.mu.Unlock()

	assert.Positive(t, sm.releaseMarkLocked(pressureRecoveryPenalty),
		"the recovery release mark must stay positive, or the shed loop can never exit")
	assert.Positive(t, sm.startMarkLocked(pressureRecoveryPenalty),
		"the recovery start mark must stay positive")
	assert.LessOrEqual(t, sm.releaseMarkLocked(pressureRecoveryPenalty), sm.startMarkLocked(pressureRecoveryPenalty),
		"the band must not invert under the penalty")
}

// flakyCompressor fails its first failN calls, then returns out. Counts calls.
type flakyCompressor struct {
	out   string
	failN int
	calls int
}

func (f *flakyCompressor) CompressMessages(ctx context.Context, messages []Message) (string, error) {
	f.calls++
	if f.calls <= f.failN {
		return "", fmt.Errorf("scripted compressor failure %d", f.calls)
	}
	return f.out, nil
}
func (f *flakyCompressor) IsEnabled() bool { return true }

// reliefSingleTurnFixture builds a single-turn session whose pressure mass is
// assistant TEXT (unevictable), so ReleasePressure must escalate to the
// rung-0 fold of the current turn.
func reliefSingleTurnFixture(t *testing.T) *SegmentedMemory {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	const sessionID = "sess-single-turn"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)

	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}

	persist("user", "TICKET: build the marts; payment_method_share = share of the source's transaction count", "", nil, true)
	for i := 0; i < 5; i++ {
		persist("assistant", strings.Repeat(fmt.Sprintf("analysis %d of models io=17 cpu=23; ", i), 120), "", nil, false)
	}
	persist("assistant", "", "", []ToolCall{{ID: "cLast", Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
	persist("tool", "small result", "cLast", nil, false)
	require.Equal(t, int64(1), sm.CurrentTurn())
	return sm
}

// Invariant: a rung-0 fold of the current turn NEVER folds the turn's
// user-role rows — the ticket survives in L1 verbatim while the assistant
// prose around it folds into a real summary.
func TestReleasePressure_NeverFoldsCurrentTurnUserRows(t *testing.T) {
	sm := reliefSingleTurnFixture(t)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "pressure must shed via rung-0 fold")

	msgs := sm.GetMessages()
	foundTicket := false
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "payment_method_share = share of the source's transaction count") {
			foundTicket = true
		}
	}
	assert.True(t, foundTicket, "the ticket must survive the fold verbatim in L1")
	assert.Contains(t, sm.summary.text, "summary of the analysis prose", "the fold itself must have committed")
}

// Invariant: with no working compressor the fold still sheds the region, but
// drops it with an "(unsummarized)" marker instead of inventing a summary.
func TestReleasePressure_FoldDropsUnsummarisedWhenTheCompressorFails(t *testing.T) {
	sm := reliefSingleTurnFixture(t)
	fc := &flakyCompressor{failN: 99}
	sm.SetCompressor(fc)

	before := len(sm.GetMessages())
	sm.ReleasePressure(context.Background(), 0)

	assert.Equal(t, 3, fc.calls, "compressor must be retried exactly compressAttempts times")
	assert.Contains(t, sm.summary.text, "unsummarized",
		"a region that could not be summarised is dropped and said to be dropped")
	assert.Less(t, len(sm.GetMessages()), before,
		"the fold still sheds — it is the only rung that reaches a turn whose bulk is reasoning")
}

// The same drop happens with no compressor at all, and without an LLM call:
// a caller that wires none still needs a lossy rung.
func TestReleasePressure_FoldDropsUnsummarisedWithNoCompressor(t *testing.T) {
	sm := reliefSingleTurnFixture(t)

	before := len(sm.GetMessages())
	sm.ReleasePressure(context.Background(), 0)

	assert.Contains(t, sm.summary.text, "unsummarized")
	assert.Less(t, len(sm.GetMessages()), before, "the region is shed, not abandoned")
}

// Invariant: a transient compressor failure is retried and the fold commits
// on a later attempt.
func TestReleasePressure_FoldRetriesThenSucceeds(t *testing.T) {
	sm := reliefSingleTurnFixture(t)
	fc := &flakyCompressor{failN: 2, out: "covers msg:1-99\nsummary after retry"}
	sm.SetCompressor(fc)

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)
	assert.Equal(t, 3, fc.calls, "two failures then the success")
	assert.Contains(t, sm.summary.text, "summary after retry")
}

// A load pair from a settled turn deactivates its skill when it folds; one
// from the CURRENT turn does not. Rung 0 folds the current turn, and taking
// the tools away there leaves the protected skill-body row in context telling
// the model to use tools the kernel no longer has.
func TestFoldedSkillLoads_SkipsTheCurrentTurn(t *testing.T) {
	pair := func(turn int64, id, skill string) []Message {
		return []Message{
			{Role: "assistant", Turn: turn, ToolCalls: []ToolCall{{
				ID: id, Name: "manage_skills",
				Input: map[string]interface{}{"action": "load", "name": skill},
			}}},
			{Role: "tool", Turn: turn, ToolUseID: id, Content: "Skill loaded: " + skill},
		}
	}

	region := append(pair(4, "load-settled", "alpha-skill"), pair(7, "load-current", "beta-skill")...)

	got := foldedSkillLoads(region, 7)
	assert.Equal(t, []string{"alpha-skill"}, got,
		"only the settled turn's load pair deactivates; the current turn's activation outlives its folded text")

	// With no turn in flight every pair in the region counts.
	assert.ElementsMatch(t, []string{"alpha-skill", "beta-skill"}, foldedSkillLoads(region, 0))
}
