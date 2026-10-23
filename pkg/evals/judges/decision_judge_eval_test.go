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

package judges

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// judgeRow is one labelled judgement: a question, the gold answer, what a
// model actually replied, and whether the benchmark's own judge called it
// correct. The label is the accepted metric for that benchmark, so it is
// ground truth for this purpose in a way that "another model agreed" is not.
type judgeRow struct {
	QuestionID   string `json:"question_id"`
	QuestionType string `json:"question_type"`
	Question     string `json:"question"`
	Gold         string `json:"gold"`
	Hypothesis   string `json:"hypothesis"`
	Label        bool   `json:"label"`
}

func loadJudgeSet(t *testing.T, path string) []judgeRow {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test fixture path from the operator
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var rows []judgeRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var r judgeRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Question == "" || r.Hypothesis == "" {
			continue
		}
		rows = append(rows, r)
	}
	require.NoError(t, sc.Err())
	return rows
}

// TestDecisionJudgeAgainstLabelledSet measures the typed judge against real
// labelled judgements rather than against another model's opinion.
//
// The set comes from finished LongMemEval cells: each row is a question, the
// gold answer, a model's actual reply, and the benchmark's official verdict.
// The judge sees the question and the gold answer as the request and the
// reply as the response, which is the same information the official judge
// had.
//
// Skipped unless both a judge set and Jev credentials are present, so CI
// never runs it:
//
//	LOOM_JUDGESET=/path/judgeset.jsonl AI_GATEWAY_API_KEY=... \
//	  go test -tags fts5 -run TestDecisionJudgeAgainstLabelledSet -v -timeout 60m ./pkg/evals/judges/
func TestDecisionJudgeAgainstLabelledSet(t *testing.T) {
	path := os.Getenv("LOOM_JUDGESET")
	if path == "" {
		t.Skip("set LOOM_JUDGESET to a labelled judge set to run this")
	}
	rows := loadJudgeSet(t, path)
	require.NotEmpty(t, rows)

	cfg := &loomv1.JudgeConfig{
		Id:   "typed-correctness",
		Name: "typed correctness judge",
		Type: loomv1.JudgeType_JUDGE_TYPE_DECISION,
		Criteria: "The response contains the correct answer to the question, " +
			"or is equivalent to it. A response holding only part of what the answer requires does not count.",
		Decision: &loomv1.DecisionConfig{
			Provider: "jev", Model: "typesafe-ai/jev", AllowAlias: true, RequestsPerMinute: 120,
		},
	}
	judge, err := NewDecisionJudgeFromConfig(cfg, nil, nil)
	if err != nil {
		t.Skipf("no Jev credentials: %v", err)
	}

	type scored struct {
		score float64
		label bool
		qtype string
	}
	var out []scored
	var totalCost float64
	var failures int
	var failReasons []string
	var latencies []float64
	start := time.Now()
	for _, r := range rows {
		evalCtx := &loomv1.EvaluationContext{
			Prompt:   fmt.Sprintf("Question: %s\n\nThe correct answer is: %s", r.Question, r.Gold),
			Response: r.Hypothesis,
		}
		callStart := time.Now()
		res, err := judge.Evaluate(context.Background(), evalCtx)
		latencies = append(latencies, float64(time.Since(callStart).Milliseconds()))
		if err != nil {
			failures++
			if len(failReasons) < 5 {
				failReasons = append(failReasons, err.Error())
			}
			continue
		}
		out = append(out, scored{score: res.OverallScore, label: r.Label, qtype: r.QuestionType})
		totalCost += res.CostUsd
	}
	elapsed := time.Since(start)
	require.NotEmpty(t, out, "every judgement failed")

	pos := 0
	for _, s := range out {
		if s.label {
			pos++
		}
	}
	t.Logf("%d judged (%d failed), %d labelled correct, %d incorrect, %s total, %s each, $%.4f",
		len(out), failures, pos, len(out)-pos, elapsed.Round(time.Second),
		(elapsed / time.Duration(len(out))).Round(time.Millisecond), totalCost)
	sort.Float64s(latencies)
	t.Logf("latency ms: p50 %.0f p90 %.0f p99 %.0f",
		pct(latencies, 0.50), pct(latencies, 0.90), pct(latencies, 0.99))
	for i, r := range failReasons {
		t.Logf("  failure %d: %s", i+1, r)
	}
	t.Logf("")
	t.Logf(" cut | agree | precision | recall |    F1 | says-pass")
	type cutResult struct {
		cut, agree, f1 float64
	}
	var best cutResult
	for _, cut := range []float64{5, 10, 20, 30, 40, 50, 60, 70, 80, 90} {
		var tp, fp, fn, tn int
		for _, s := range out {
			pass := s.score >= cut
			switch {
			case pass && s.label:
				tp++
			case pass && !s.label:
				fp++
			case !pass && s.label:
				fn++
			default:
				tn++
			}
		}
		agree := float64(tp+tn) / float64(len(out))
		prec := ratioOf(tp, tp+fp)
		rec := ratioOf(tp, tp+fn)
		f1 := 0.0
		if prec+rec > 0 {
			f1 = 2 * prec * rec / (prec + rec)
		}
		t.Logf(" %3.0f | %4.1f%% | %8.1f%% | %5.1f%% | %5.1f%% | %4d/%d",
			cut, 100*agree, 100*prec, 100*rec, 100*f1, tp+fp, len(out))
		if f1 > best.f1 {
			best = cutResult{cut: cut, agree: agree, f1: f1}
		}
	}
	t.Logf("")
	t.Logf("best F1 at cut %.0f: %.1f%% agreement with the official judge", best.cut, 100*best.agree)

	// The score distribution says whether the judge is discriminating at all
	// or just answering the same way every time.
	scores := make([]float64, len(out))
	for i, s := range out {
		scores[i] = s.score
	}
	sort.Float64s(scores)
	t.Logf("score spread: p10 %.0f p25 %.0f p50 %.0f p75 %.0f p90 %.0f",
		pct(scores, 0.10), pct(scores, 0.25), pct(scores, 0.50), pct(scores, 0.75), pct(scores, 0.90))

	// A judge that cannot beat calling everything correct is not a judge.
	baseline := float64(pos) / float64(len(out))
	if baseline < 0.5 {
		baseline = 1 - baseline
	}
	assert.Greater(t, best.agree, baseline,
		"the typed judge must beat always guessing the majority class (%.1f%%)", 100*baseline)
}

func ratioOf(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
