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

package sites

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestExtractRequestShape(t *testing.T) {
	t.Parallel()
	req, err := ExtractRequest(strings.Repeat("w", maxExtractWindowRunes+100))
	require.NoError(t, err)
	require.NoError(t, decision.Validate(req))
	assert.Equal(t, SiteMemoryExtract, req.Site)
	require.Len(t, req.Questions, 1, "one question: is anything here worth keeping")
	_, isNoul := req.Questions[QExtractDurable].Kind.(*loomv1.DecisionQuestion_Noul)
	assert.True(t, isNoul)

	state := req.GetState().GetStructValue().AsMap()
	w, _ := state["window"].(string)
	assert.LessOrEqual(t, len([]rune(w)), maxExtractWindowRunes+1, "the window is bounded")
	assert.Empty(t, req.FanOutKey, "one question, nothing to fan out")

	_, err = ExtractRequest("")
	require.Error(t, err, "an empty window is a validation error, not a request")
}

// The gate may skip an extraction and may never force one. Every case here
// is about that asymmetry: the only answer that can lose data is the one it
// takes the most confidence to act on.
func TestExtractVerdictOnlySkipsOnAConfidentNo(t *testing.T) {
	t.Parallel()
	band := decision.Band{ActMin: 0.5, TrueMin: 0.5}

	tests := []struct {
		name      string
		p         float64
		wantSkip  bool
		wantOK    bool
		rationale string
	}{
		{"confident no", 0.02, true, true, "nothing durable: the call is saved"},
		{"confident yes", 0.97, false, true, "something durable: extract"},
		{"uncertain low", 0.40, false, false, "not confident enough to skip; extract as before"},
		{"uncertain high", 0.60, false, false, "not confident enough either way; extract as before"},
		{"exactly at the line", 0.50, false, false, "no decisiveness at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mock.New().AnswerNoul(QExtractDurable, tt.p)
			req, err := ExtractRequest("[user] my daughter's name is Ada")
			require.NoError(t, err)
			out := decision.NewRouter(m).Decide(context.Background(), req)
			require.NoError(t, out.Err)

			skip, ok := ExtractVerdict(out.Response, band)
			assert.Equal(t, tt.wantSkip, skip, tt.rationale)
			assert.Equal(t, tt.wantOK, ok, tt.rationale)
		})
	}
}

// true_min moves where "durable" starts, and lowering it makes the gate
// skip less, never more — the safe direction.
func TestExtractVerdictTrueMinOnlyNarrowsSkipping(t *testing.T) {
	t.Parallel()
	m := mock.New().AnswerNoul(QExtractDurable, 0.2)
	req, err := ExtractRequest("[user] maybe something")
	require.NoError(t, err)
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)

	// At the default cut, 0.2 is "not durable" and confident, so it skips.
	skip, ok := ExtractVerdict(out.Response, decision.Band{ActMin: 0.5, TrueMin: 0.5})
	assert.True(t, ok)
	assert.True(t, skip)

	// Lower the cut and the same answer now counts as durable: extract.
	skip, ok = ExtractVerdict(out.Response, decision.Band{ActMin: 0.5, TrueMin: 0.1})
	assert.True(t, ok)
	assert.False(t, skip, "a lower bar for durable means fewer skips, never more")
}

func TestExtractVerdictNoAnswer(t *testing.T) {
	t.Parallel()
	band := decision.Band{ActMin: 0.5}
	skip, ok := ExtractVerdict(nil, band)
	assert.False(t, skip)
	assert.False(t, ok, "no response: extract as before")

	empty := &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{}}
	skip, ok = ExtractVerdict(empty, band)
	assert.False(t, skip)
	assert.False(t, ok, "no answer for the question: extract as before")
}

// The reference is what extraction actually stored, which makes these rows
// a labelled set rather than an agreement measurement.
func TestExtractReferenceIsObservedYield(t *testing.T) {
	t.Parallel()
	refs := ExtractReference(0)
	require.Contains(t, refs, QExtractDurable)
	assert.Equal(t, "false", refs[QExtractDurable].Answer, "extraction found nothing")
	assert.Equal(t, ReferenceSourceExtractionYield, refs[QExtractDurable].Source)

	refs = ExtractReference(3)
	assert.Equal(t, "true", refs[QExtractDurable].Answer, "extraction stored three memories")
}
