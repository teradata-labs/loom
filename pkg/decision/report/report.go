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

// Package report turns shadow rows into the numbers that set a site's band:
// agreement with the existing mechanism, expected calibration error,
// confusion by answer, decider error rate, and latency percentiles.
//
// It says nothing about correctness. Agreement is agreement with whatever
// the call site did before, which may itself be wrong; the report is the
// evidence for a band, not a claim about truth.
package report

import (
	"fmt"
	"math"
	"sort"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// DefaultBins is the calibration histogram width.
const DefaultBins = 10

// Bin is one calibration bucket.
type Bin struct {
	// Lo and Hi bound the confidence range [Lo, Hi).
	Lo, Hi float64
	// Count is rows in the bin; Correct is rows whose candidate matched the
	// reference; MeanConfidence is the average candidate confidence.
	Count, Correct int
	MeanConfidence float64
}

// Accuracy is Correct / Count, or 0.
func (b Bin) Accuracy() float64 {
	if b.Count == 0 {
		return 0
	}
	return float64(b.Correct) / float64(b.Count)
}

// Summary is everything a report says about one site.
type Summary struct {
	Site string
	// Rows is every row seen; Compared is rows with both a candidate and a
	// reference answer; Agreed is compared rows where they match.
	Rows, Compared, Agreed int
	// DeciderErrors is rows whose path is ERROR (no candidate).
	DeciderErrors int
	// Errors counts ERROR rows by their recorded error text, so a report
	// says why the decider failed, not only how often.
	Errors map[string]int
	// Agreement is Agreed / Compared, or 0.
	Agreement float64
	// ECE is the expected calibration error over Compared rows using
	// candidate confidence as the predicted probability of agreement.
	ECE float64
	// Bins is the calibration histogram behind ECE.
	Bins []Bin
	// Confusion maps reference answer → candidate answer → count.
	Confusion map[string]map[string]int
	// Latency percentiles in milliseconds over rows with a decider call.
	LatencyP50, LatencyP95, LatencyP99 int64
	// Cost totals over rows with a decider call.
	TotalInputTokens int64
	TotalCostUSD     float64
	// ReferenceSources counts rows by what produced the reference.
	ReferenceSources map[string]int
	// Kinds counts rows by question kind.
	Kinds map[string]int
}

// Summarize computes a Summary over rows, which may span sites when site is
// "" (the report then labels itself "all sites").
func Summarize(site string, rows []*loomv1.DecisionShadowRecord) Summary {
	return SummarizeWithBins(site, rows, DefaultBins)
}

// SummarizeWithBins is Summarize with a chosen calibration histogram width.
func SummarizeWithBins(site string, rows []*loomv1.DecisionShadowRecord, bins int) Summary {
	if bins <= 0 {
		bins = DefaultBins
	}
	s := Summary{
		Site:             site,
		Confusion:        make(map[string]map[string]int),
		ReferenceSources: make(map[string]int),
		Kinds:            make(map[string]int),
		Bins:             make([]Bin, bins),
	}
	for i := range s.Bins {
		s.Bins[i].Lo = float64(i) / float64(bins)
		s.Bins[i].Hi = float64(i+1) / float64(bins)
	}

	var latencies []int64
	confSum := make([]float64, bins)
	for _, r := range rows {
		if r == nil {
			continue
		}
		s.Rows++
		s.Kinds[r.Kind]++
		if r.ReferenceSource != "" {
			s.ReferenceSources[r.ReferenceSource]++
		}
		if r.Path == loomv1.DecisionPath_DECISION_PATH_ERROR {
			s.DeciderErrors++
			if r.Error != "" {
				if s.Errors == nil {
					s.Errors = make(map[string]int)
				}
				s.Errors[r.Error]++
			}
		}
		if r.Path != loomv1.DecisionPath_DECISION_PATH_DISABLED && r.Path != loomv1.DecisionPath_DECISION_PATH_BUDGET {
			latencies = append(latencies, r.LatencyMs)
			s.TotalInputTokens += r.InputTokens
			s.TotalCostUSD += r.CostUsd
		}
		if r.CandidateAnswer == "" || r.ReferenceAnswer == "" {
			continue
		}
		s.Compared++
		match := r.CandidateAnswer == r.ReferenceAnswer
		if match {
			s.Agreed++
		}
		if s.Confusion[r.ReferenceAnswer] == nil {
			s.Confusion[r.ReferenceAnswer] = make(map[string]int)
		}
		s.Confusion[r.ReferenceAnswer][r.CandidateAnswer]++

		b := binIndex(r.CandidateConfidence, bins)
		s.Bins[b].Count++
		if match {
			s.Bins[b].Correct++
		}
		confSum[b] += r.CandidateConfidence
	}

	if s.Compared > 0 {
		s.Agreement = float64(s.Agreed) / float64(s.Compared)
		ece := 0.0
		for i := range s.Bins {
			if s.Bins[i].Count == 0 {
				continue
			}
			s.Bins[i].MeanConfidence = confSum[i] / float64(s.Bins[i].Count)
			weight := float64(s.Bins[i].Count) / float64(s.Compared)
			ece += weight * math.Abs(s.Bins[i].Accuracy()-s.Bins[i].MeanConfidence)
		}
		s.ECE = ece
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		s.LatencyP50 = percentile(latencies, 0.50)
		s.LatencyP95 = percentile(latencies, 0.95)
		s.LatencyP99 = percentile(latencies, 0.99)
	}
	return s
}

func binIndex(conf float64, bins int) int {
	if math.IsNaN(conf) || conf <= 0 {
		return 0
	}
	if conf >= 1 {
		return bins - 1
	}
	return int(conf * float64(bins))
}

// percentile returns the nearest-rank percentile of a sorted slice.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// RenderMarkdown formats a Summary as the report the plan calls for.
func RenderMarkdown(s Summary) string {
	var b strings.Builder
	site := s.Site
	if site == "" {
		site = "all sites"
	}
	fmt.Fprintf(&b, "# Decision shadow report: %s\n\n", site)
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Rows | %d |\n", s.Rows)
	fmt.Fprintf(&b, "| Compared (candidate and reference present) | %d |\n", s.Compared)
	fmt.Fprintf(&b, "| Agreement | %.1f%% (%d / %d) |\n", 100*s.Agreement, s.Agreed, s.Compared)
	fmt.Fprintf(&b, "| Expected calibration error (%d bins) | %.3f |\n", len(s.Bins), s.ECE)
	fmt.Fprintf(&b, "| Decider errors | %d (%.1f%% of rows) |\n", s.DeciderErrors, pct(s.DeciderErrors, s.Rows))
	fmt.Fprintf(&b, "| Latency p50 / p95 / p99 (ms) | %d / %d / %d |\n", s.LatencyP50, s.LatencyP95, s.LatencyP99)
	fmt.Fprintf(&b, "| Input tokens / cost | %d / $%.4f |\n", s.TotalInputTokens, s.TotalCostUSD)
	b.WriteString("\n")

	if len(s.Errors) > 0 {
		b.WriteString("## Decider errors\n\n| Rows | Error |\n|---|---|\n")
		keys := sortedKeys(s.Errors)
		sort.SliceStable(keys, func(i, j int) bool { return s.Errors[keys[i]] > s.Errors[keys[j]] })
		if len(keys) > 10 {
			keys = keys[:10]
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "| %d | %s |\n", s.Errors[k], strings.ReplaceAll(k, "|", "\\|"))
		}
		b.WriteString("\n")
	}

	if len(s.Kinds) > 0 || len(s.ReferenceSources) > 0 {
		b.WriteString("## Composition\n\n| Dimension | Value | Rows |\n|---|---|---|\n")
		for _, k := range sortedKeys(s.Kinds) {
			fmt.Fprintf(&b, "| kind | %s | %d |\n", k, s.Kinds[k])
		}
		for _, k := range sortedKeys(s.ReferenceSources) {
			fmt.Fprintf(&b, "| reference source | %s | %d |\n", k, s.ReferenceSources[k])
		}
		b.WriteString("\n")
	}

	if s.Compared > 0 {
		b.WriteString("## Calibration\n\nConfidence is the candidate's decisiveness; accuracy is agreement with the reference in that bin.\n\n")
		b.WriteString("| Confidence | Rows | Agreement | Mean confidence | Gap |\n|---|---|---|---|---|\n")
		for _, bin := range s.Bins {
			if bin.Count == 0 {
				continue
			}
			fmt.Fprintf(&b, "| [%.1f, %.1f) | %d | %.1f%% | %.3f | %+.3f |\n",
				bin.Lo, bin.Hi, bin.Count, 100*bin.Accuracy(), bin.MeanConfidence, bin.Accuracy()-bin.MeanConfidence)
		}
		b.WriteString("\n")

		b.WriteString("## Confusion (reference → candidate)\n\n")
		candidates := map[string]struct{}{}
		for _, row := range s.Confusion {
			for c := range row {
				candidates[c] = struct{}{}
			}
		}
		cols := make([]string, 0, len(candidates))
		for c := range candidates {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		fmt.Fprintf(&b, "| reference \\ candidate | %s | total |\n", strings.Join(cols, " | "))
		fmt.Fprintf(&b, "|---|%s---|\n", strings.Repeat("---|", len(cols)))
		for _, ref := range sortedKeys(s.Confusion) {
			total := 0
			cells := make([]string, len(cols))
			for i, c := range cols {
				n := s.Confusion[ref][c]
				total += n
				if c == ref {
					cells[i] = fmt.Sprintf("**%d**", n)
				} else {
					cells[i] = fmt.Sprintf("%d", n)
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %d |\n", ref, strings.Join(cells, " | "), total)
		}
		b.WriteString("\n")
	}

	b.WriteString("Agreement is with the site's existing mechanism, not with ground truth. A band should be set from the calibration table's high-confidence bins, per question, and re-checked after any model change.\n")
	return b.String()
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
