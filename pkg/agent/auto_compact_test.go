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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

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

func TestAutoCompact_BelowTheThresholdDoesNothing(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Empty(t, comp.input, "no summary call below the threshold")
}

func TestAutoCompact_StubbingAloneSparesTheSummaryCall(t *testing.T) {
	// ~8 turns of 1.5K-token results: over 65% of 11000, but stubs shed nearly all of it.
	sm := toolTurnMemory(t, 12000, 1000, 8)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.True(t, res.ToolResultsStubbed)
	assert.False(t, res.TurnsFolded)
	assert.Empty(t, comp.input, "lossless stubbing was enough, so no LLM call")
	assert.Less(t, res.TokensAfter, res.TokensBefore)
	assert.Len(t, unfoldedTurns(sm), 8, "no turn left the context")
}

func TestAutoCompact_FoldsOlderTurnsWhenStubbingCannotHelp(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.True(t, res.TurnsFolded)
	assert.NotEmpty(t, comp.input, "the compressor summarized the older turns")
	sent := sentTurns(sm)
	assert.True(t, sent[5] && sent[6], "the newest two turns stay verbatim")
	assert.False(t, sent[1], "older turns now live in the summary")
}

func TestAutoCompact_NeverDropsHistoryWhenTheCompressorFails(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	comp := &failingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.Positive(t, comp.calls, "the compressor was tried")
	assert.False(t, res.TurnsFolded)
	assert.Len(t, unfoldedTurns(sm), 6, "a failed summary must not cost the history")
	assert.Equal(t, 0, sm.summary.n)
	assert.False(t, sm.foldRequireSummary, "the flag does not leak into relief")
}

func TestAutoCompact_WithoutACompressorOnlyStubs(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.False(t, res.TurnsFolded)
	assert.Len(t, unfoldedTurns(sm), 6)
}

func TestAutoCompact_AttemptsAreSpacedOut(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	comp := &failingCompressor{}
	sm.SetCompressor(comp)

	_, err := sm.AutoCompact(context.Background(), 65)
	require.NoError(t, err)
	first := comp.calls
	require.Positive(t, first)

	_, err = sm.AutoCompact(context.Background(), 65)
	require.NoError(t, err)
	assert.Equal(t, first, comp.calls, "no second attempt within the turn gap")
}

func TestAutoCompact_OffWhenPercentIsNotSet(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	for _, pct := range []int{0, -1, 100, 150} {
		res, err := sm.AutoCompact(context.Background(), pct)
		require.NoError(t, err)
		assert.False(t, res.Compacted, "percent %d", pct)
	}
	assert.Empty(t, comp.input)
}

func TestMaybeAutoCompact_RecordsTheCompactionOnTheResponse(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	sm.SetCompressor(&recordingCompressor{})
	a := &Agent{config: &Config{AutoCompactPercent: 65}}
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "t")
	resp := &Response{Metadata: map[string]interface{}{}}

	a.maybeAutoCompact(context.Background(), &Session{ID: "s", SegmentedMem: sm}, span, resp)

	meta, ok := resp.Metadata["auto_compaction"].(map[string]interface{})
	require.True(t, ok, "the response says the context was compacted")
	assert.Equal(t, true, meta["turns_folded"])
	assert.Greater(t, meta["tokens_saved"], 0)
}

func TestMaybeAutoCompact_OffByDefault(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	sm.SetCompressor(&recordingCompressor{})
	a := &Agent{config: &Config{}}
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "t")
	resp := &Response{Metadata: map[string]interface{}{}}

	a.maybeAutoCompact(context.Background(), &Session{ID: "s", SegmentedMem: sm}, span, resp)

	assert.NotContains(t, resp.Metadata, "auto_compaction")
	assert.Len(t, unfoldedTurns(sm), 6)
}
