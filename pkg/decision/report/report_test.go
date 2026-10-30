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

package report

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func row(site, cand, ref string, conf float64, latency int64, path loomv1.DecisionPath) *loomv1.DecisionShadowRecord {
	return &loomv1.DecisionShadowRecord{
		Site: site, QuestionId: "q", Kind: "choice",
		CandidateAnswer: cand, CandidateConfidence: conf,
		ReferenceAnswer: ref, ReferenceSource: "ref",
		LatencyMs: latency, InputTokens: 10, CostUsd: 0.001, Path: path,
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	fb := loomv1.DecisionPath_DECISION_PATH_FALLBACK
	rows := []*loomv1.DecisionShadowRecord{
		row("s", "a", "a", 0.95, 100, fb),                                   // agree, high conf
		row("s", "a", "a", 0.92, 200, fb),                                   // agree, high conf
		row("s", "b", "a", 0.91, 300, fb),                                   // disagree, high conf
		row("s", "b", "b", 0.55, 400, fb),                                   // agree, mid conf
		row("s", "a", "b", 0.15, 500, fb),                                   // disagree, low conf
		row("s", "", "a", 0, 0, loomv1.DecisionPath_DECISION_PATH_ERROR),    // decider error: not compared
		row("s", "", "a", 0, 0, loomv1.DecisionPath_DECISION_PATH_DISABLED), // disabled: not compared, no latency
		row("s", "a", "", 0.99, 50, fb),                                     // no reference: not compared
		nil,
	}
	s := Summarize("s", rows)

	assert.Equal(t, "s", s.Site)
	assert.Equal(t, 8, s.Rows)
	assert.Equal(t, 5, s.Compared)
	assert.Equal(t, 3, s.Agreed)
	assert.InDelta(t, 0.6, s.Agreement, 1e-9)
	assert.Equal(t, 1, s.DeciderErrors)
	assert.Equal(t, map[string]int{"choice": 8}, s.Kinds)
	assert.Equal(t, 8, s.ReferenceSources["ref"])

	// Confusion: ref a → {a:2, b:1}; ref b → {b:1, a:1}
	assert.Equal(t, 2, s.Confusion["a"]["a"])
	assert.Equal(t, 1, s.Confusion["a"]["b"])
	assert.Equal(t, 1, s.Confusion["b"]["b"])
	assert.Equal(t, 1, s.Confusion["b"]["a"])

	// Calibration bins (10): 0.95/0.92/0.91 → bin 9 (acc 2/3, mean conf 0.9267);
	// 0.55 → bin 5 (acc 1, conf 0.55); 0.15 → bin 1 (acc 0, conf 0.15).
	require.Len(t, s.Bins, 10)
	assert.Equal(t, 3, s.Bins[9].Count)
	assert.Equal(t, 2, s.Bins[9].Correct)
	assert.InDelta(t, (0.95+0.92+0.91)/3, s.Bins[9].MeanConfidence, 1e-9)
	assert.Equal(t, 1, s.Bins[5].Count)
	assert.Equal(t, 1, s.Bins[1].Count)
	wantECE := (3.0/5)*abs(2.0/3-(0.95+0.92+0.91)/3) + (1.0/5)*abs(1-0.55) + (1.0/5)*abs(0-0.15)
	assert.InDelta(t, wantECE, s.ECE, 1e-9)

	// Latency over rows with a decider call: 100,200,300,400,500,0(error),50 → sorted 0,50,100,200,300,400,500
	assert.Equal(t, int64(200), s.LatencyP50)
	assert.Equal(t, int64(500), s.LatencyP95)
	assert.Equal(t, int64(500), s.LatencyP99)
	assert.Equal(t, int64(70), s.TotalInputTokens, "7 rows with a decider call × 10")
	assert.InDelta(t, 0.007, s.TotalCostUSD, 1e-12)
}

func TestSummarizeEmptyAndBins(t *testing.T) {
	t.Parallel()
	s := Summarize("", nil)
	assert.Equal(t, 0, s.Rows)
	assert.Equal(t, 0.0, s.Agreement)
	assert.Equal(t, 0.0, s.ECE)
	assert.Len(t, s.Bins, DefaultBins)

	s4 := SummarizeWithBins("x", []*loomv1.DecisionShadowRecord{
		row("x", "a", "a", 1.0, 1, loomv1.DecisionPath_DECISION_PATH_DECIDER),
		row("x", "a", "a", 0.0, 1, loomv1.DecisionPath_DECISION_PATH_DECIDER),
	}, 4)
	require.Len(t, s4.Bins, 4)
	assert.Equal(t, 1, s4.Bins[3].Count, "confidence 1.0 lands in the top bin")
	assert.Equal(t, 1, s4.Bins[0].Count, "confidence 0 lands in the bottom bin")
	assert.Len(t, SummarizeWithBins("x", nil, -3).Bins, DefaultBins, "invalid bin count falls back")
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()
	fb := loomv1.DecisionPath_DECISION_PATH_FALLBACK
	s := Summarize("tool.failure_kind", []*loomv1.DecisionShadowRecord{
		row("tool.failure_kind", "auth", "auth", 0.95, 120, fb),
		row("tool.failure_kind", "other", "auth", 0.4, 130, fb),
	})
	md := RenderMarkdown(s)
	for _, want := range []string{
		"# Decision shadow report: tool.failure_kind",
		"| Agreement | 50.0% (1 / 2) |",
		"| Expected calibration error (10 bins) |",
		"| Latency p50 / p95 / p99 (ms) | 120 / 130 / 130 |",
		"## Calibration",
		"## Confusion (reference → candidate)",
		"| auth | **1** | 1 | 2 |",
		"not with ground truth",
	} {
		assert.Contains(t, md, want)
	}
	empty := RenderMarkdown(Summarize("", nil))
	assert.True(t, strings.HasPrefix(empty, "# Decision shadow report: all sites"))
	assert.NotContains(t, empty, "## Calibration", "no compared rows, no calibration table")
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int64(0), percentile(nil, 0.5))
	sorted := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	assert.Equal(t, int64(5), percentile(sorted, 0.5))
	assert.Equal(t, int64(10), percentile(sorted, 0.95))
	assert.Equal(t, int64(1), percentile(sorted, 0))
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
