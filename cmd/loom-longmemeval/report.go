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

//go:build fts5

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// HypothesisEntry is the JSONL output format expected by LongMemEval evaluation scripts.
type HypothesisEntry struct {
	QuestionID string `json:"question_id"`
	Hypothesis string `json:"hypothesis"`
}

// WriteJSONL writes results to a JSONL file for LongMemEval evaluation.
func WriteJSONL(path string, results []EntryResult) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	for _, r := range results {
		if r.Error != "" {
			continue // skip failed entries
		}
		if err := enc.Encode(HypothesisEntry{
			QuestionID: r.QuestionID,
			Hypothesis: r.Hypothesis,
		}); err != nil {
			return fmt.Errorf("encode result %s: %w", r.QuestionID, err)
		}
	}
	return nil
}

// WriteDetailedResults writes full results including timing and token metrics.
func WriteDetailedResults(path string, results []EntryResult) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create detailed output: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// RunSummary aggregates benchmark results.
type RunSummary struct {
	Mode          string                `json:"mode"`
	Model         string                `json:"model"`
	TotalEntries  int                   `json:"total_entries"`
	Succeeded     int                   `json:"succeeded"`
	Failed        int                   `json:"failed"`
	TotalDuration time.Duration         `json:"total_duration_ms"`
	AvgDuration   time.Duration         `json:"avg_duration_ms"`
	TotalInput    int                   `json:"total_input_tokens"`
	TotalOutput   int                   `json:"total_output_tokens"`
	TypeBreakdown map[string]*TypeStats `json:"type_breakdown"`
}

// TypeStats holds per-question-type statistics.
type TypeStats struct {
	Count       int           `json:"count"`
	Succeeded   int           `json:"succeeded"`
	Failed      int           `json:"failed"`
	AvgDuration time.Duration `json:"avg_duration_ms"`
	AvgInput    int           `json:"avg_input_tokens"`
	AvgOutput   int           `json:"avg_output_tokens"`
}

// Summarize computes aggregate statistics from results.
func Summarize(results []EntryResult, mode, model string) RunSummary {
	summary := RunSummary{
		Mode:          mode,
		Model:         model,
		TotalEntries:  len(results),
		TypeBreakdown: make(map[string]*TypeStats),
	}

	for _, r := range results {
		// Initialize type stats if needed
		ts, ok := summary.TypeBreakdown[r.QuestionType]
		if !ok {
			ts = &TypeStats{}
			summary.TypeBreakdown[r.QuestionType] = ts
		}
		ts.Count++

		if r.Error != "" {
			summary.Failed++
			ts.Failed++
			continue
		}

		summary.Succeeded++
		ts.Succeeded++
		summary.TotalDuration += r.Duration
		summary.TotalInput += r.InputTokens
		summary.TotalOutput += r.OutputTokens
		ts.AvgDuration += r.Duration
		ts.AvgInput += r.InputTokens
		ts.AvgOutput += r.OutputTokens
	}

	if summary.Succeeded > 0 {
		summary.AvgDuration = summary.TotalDuration / time.Duration(summary.Succeeded)
	}

	// Compute per-type averages
	for _, ts := range summary.TypeBreakdown {
		if ts.Succeeded > 0 {
			ts.AvgDuration = ts.AvgDuration / time.Duration(ts.Succeeded)
			ts.AvgInput = ts.AvgInput / ts.Succeeded
			ts.AvgOutput = ts.AvgOutput / ts.Succeeded
		}
	}

	return summary
}

// PrintSummary writes a human-readable summary to stdout.
func PrintSummary(s RunSummary) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println("LongMemEval Benchmark Results")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("Mode:       %s\n", s.Mode)
	fmt.Printf("Model:      %s\n", s.Model)
	fmt.Printf("Entries:    %d succeeded / %d failed / %d total\n",
		s.Succeeded, s.Failed, s.TotalEntries)
	fmt.Printf("Duration:   %s total, %s avg\n",
		s.TotalDuration.Round(time.Millisecond),
		s.AvgDuration.Round(time.Millisecond))
	fmt.Printf("Tokens:     %d input, %d output\n", s.TotalInput, s.TotalOutput)
	fmt.Println()

	// Sort types for consistent output
	types := make([]string, 0, len(s.TypeBreakdown))
	for t := range s.TypeBreakdown {
		types = append(types, t)
	}
	sort.Strings(types)

	fmt.Println("Per-Type Breakdown:")
	fmt.Printf("  %-30s %6s %6s %6s %12s %8s %8s\n",
		"Type", "Total", "OK", "Fail", "Avg Time", "Avg In", "Avg Out")
	fmt.Println("  " + strings.Repeat("-", 88))
	for _, t := range types {
		ts := s.TypeBreakdown[t]
		fmt.Printf("  %-30s %6d %6d %6d %12s %8d %8d\n",
			t, ts.Count, ts.Succeeded, ts.Failed,
			ts.AvgDuration.Round(time.Millisecond),
			ts.AvgInput, ts.AvgOutput)
	}
	fmt.Println(strings.Repeat("=", 70))
}
