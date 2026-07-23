// loom-v5-eval reads state+context pairs dumped by the segmented-memory
// e2e tests and asks an LLM to judge each compiled context from the
// consumer's seat: given the state the system had available, is this the
// context best suited for producing the next response? (See evalPrompt.)
//
// It measures context usefulness for the work directly, rather than
// conformance to the pipeline's rules.
//
// Requires ANTHROPIC_API_KEY. Not a CI gate — an on-demand quality lens.
//
// Usage:
//
//	# 1) Produce dumps
//	LOOM_TEST_DUMP_DIR=/tmp/dumps \
//	  go test -tags fts5 -run TestE2E_V5 ./pkg/agent/
//
//	# 2) Eval them
//	loom-v5-eval --dir /tmp/dumps [--model claude-sonnet-4-5-20250929]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

const evalPrompt = `Judge the CONTEXT below from the consumer's seat: the model that must reason on it to answer the user's most recent turn.

The STATE block shows what the segmented-memory system HAD AVAILABLE at this moment (ROM base, catalog of discovered skills, currently loaded skills, L1 messages in order, L2 residue summary, fold count). The CONTEXT block shows what was actually compiled for the model.

Given this state, is this the context best suited for producing the next response? Consider order of information, whether anything is mangled, jumbled, duplicated, or garbage, whether fidelity is at the right level (too much detail? too little? summary too opaque?), whether the user's current intent is easy to find, whether anything the response needs is missing.

Judge usefulness for the work, not conformance to the pipeline's rules. Concrete concerns only. No praise. No general observations. If the context is well-shaped for the job, return an empty concerns array.

Return JSON only (no prose, no markdown fences), matching this schema exactly:

{
  "verdict": "good" | "suboptimal",
  "concerns": [
    {
      "issue": "short description of the concrete problem",
      "why_it_hurts_me": "why this specifically makes reasoning harder for the LLM consumer",
      "what_would_be_better": "what shape/order/fidelity would have served better"
    }
  ]
}`

type concern struct {
	Issue             string `json:"issue"`
	WhyItHurtsMe      string `json:"why_it_hurts_me"`
	WhatWouldBeBetter string `json:"what_would_be_better"`
}

type verdict struct {
	Verdict  string    `json:"verdict"`
	Concerns []concern `json:"concerns"`
}

type callResult struct {
	CallIdx int
	Err     error
	Raw     string
	Verdict verdict
}

func main() {
	dir := flag.String("dir", "", "directory containing call-NNN.{state,context}.txt files")
	model := flag.String("model", "", "Anthropic model (default: use ANTHROPIC_DEFAULT_MODEL or claude-sonnet-4-5-20250929)")
	concurrency := flag.Int("concurrency", 4, "max concurrent eval requests")
	only := flag.String("only", "", "comma-separated list of call indices to evaluate (default: all)")
	flag.Parse()

	if *dir == "" {
		fatal("--dir is required")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fatal("ANTHROPIC_API_KEY must be set")
	}

	pairs, err := discoverPairs(*dir)
	if err != nil {
		fatal("discover: %v", err)
	}
	if len(pairs) == 0 {
		fatal("no call-NNN.{state,context}.txt pairs found in %s", *dir)
	}

	if *only != "" {
		pairs = filterOnly(pairs, *only)
	}

	fmt.Printf("evaluating %d call(s) from %s\n", len(pairs), *dir)

	client := anthropic.NewClient(anthropic.Config{
		APIKey:      apiKey,
		Model:       *model,
		Timeout:     90 * time.Second,
		MaxTokens:   2048,
		Temperature: 0.0,
	})

	results := evalAll(client, pairs, *concurrency)

	printReport(results)
}

type pair struct {
	Idx     int
	State   string // path
	Context string // path
}

var pairPattern = regexp.MustCompile(`^call-(\d+)\.(state|context)\.txt$`)

func discoverPairs(dir string) ([]pair, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	byIdx := map[int]*pair{}
	for _, e := range entries {
		m := pairPattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(m[1], "%d", &idx); err != nil {
			continue
		}
		if _, ok := byIdx[idx]; !ok {
			byIdx[idx] = &pair{Idx: idx}
		}
		full := filepath.Join(dir, e.Name())
		if m[2] == "state" {
			byIdx[idx].State = full
		} else {
			byIdx[idx].Context = full
		}
	}
	var out []pair
	for _, p := range byIdx {
		if p.State == "" || p.Context == "" {
			continue // incomplete pair — skip
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Idx < out[j].Idx })
	return out, nil
}

func filterOnly(pairs []pair, only string) []pair {
	want := map[int]bool{}
	for _, s := range strings.Split(only, ",") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil {
			want[n] = true
		}
	}
	var out []pair
	for _, p := range pairs {
		if want[p.Idx] {
			out = append(out, p)
		}
	}
	return out
}

func evalAll(client *anthropic.Client, pairs []pair, concurrency int) []callResult {
	sem := make(chan struct{}, concurrency)
	results := make([]callResult, len(pairs))
	done := make(chan struct{})
	for i, p := range pairs {
		i, p := i, p
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				done <- struct{}{}
			}()
			results[i] = evalOne(client, p)
			fmt.Printf("  call #%d: %s\n", p.Idx, resultLabel(results[i]))
		}()
	}
	for range pairs {
		<-done
	}
	return results
}

func evalOne(client *anthropic.Client, p pair) callResult {
	stateBytes, err := os.ReadFile(p.State)
	if err != nil {
		return callResult{CallIdx: p.Idx, Err: fmt.Errorf("read state: %w", err)}
	}
	ctxBytes, err := os.ReadFile(p.Context)
	if err != nil {
		return callResult{CallIdx: p.Idx, Err: fmt.Errorf("read context: %w", err)}
	}
	userContent := fmt.Sprintf("STATE:\n%s\n\nCONTEXT:\n%s", string(stateBytes), string(ctxBytes))

	msgs := []llmtypes.Message{
		{Role: "system", Content: evalPrompt},
		{Role: "user", Content: userContent},
	}
	resp, err := client.Chat(context.Background(), msgs, nil)
	if err != nil {
		return callResult{CallIdx: p.Idx, Err: fmt.Errorf("chat: %w", err)}
	}
	r := callResult{CallIdx: p.Idx, Raw: resp.Content}
	if err := json.Unmarshal([]byte(resp.Content), &r.Verdict); err != nil {
		r.Err = fmt.Errorf("parse verdict: %w (raw: %s)", err, truncate(resp.Content, 200))
	}
	return r
}

func resultLabel(r callResult) string {
	if r.Err != nil {
		return "ERROR: " + r.Err.Error()
	}
	if r.Verdict.Verdict == "good" && len(r.Verdict.Concerns) == 0 {
		return "good"
	}
	return fmt.Sprintf("%s (%d concern(s))", r.Verdict.Verdict, len(r.Verdict.Concerns))
}

func printReport(results []callResult) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 72))
	fmt.Println("REPORT")
	fmt.Println(strings.Repeat("=", 72))
	good, bad, errs := 0, 0, 0
	for _, r := range results {
		if r.Err != nil {
			errs++
		} else if r.Verdict.Verdict == "good" && len(r.Verdict.Concerns) == 0 {
			good++
		} else {
			bad++
		}
	}
	fmt.Printf("summary: %d good  |  %d suboptimal  |  %d errors  |  %d total\n\n", good, bad, errs, len(results))

	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("--- call #%d — ERROR ---\n%s\n\n", r.CallIdx, r.Err)
			continue
		}
		if r.Verdict.Verdict == "good" && len(r.Verdict.Concerns) == 0 {
			continue
		}
		fmt.Printf("--- call #%d — %s ---\n", r.CallIdx, r.Verdict.Verdict)
		for i, c := range r.Verdict.Concerns {
			fmt.Printf("  [%d] %s\n", i+1, c.Issue)
			if c.WhyItHurtsMe != "" {
				fmt.Printf("      why: %s\n", c.WhyItHurtsMe)
			}
			if c.WhatWouldBeBetter != "" {
				fmt.Printf("      fix: %s\n", c.WhatWouldBeBetter)
			}
		}
		fmt.Println()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "loom-v5-eval: "+format+"\n", args...)
	os.Exit(1)
}
