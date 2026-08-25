// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/types"
)

// This file is the live measurement harness for capability leveling. It is the
// only place in the tree that talks to a real model, and it is SKIPPED BY
// DEFAULT: `go test -tags fts5 -race ./...` on a machine without Ollama (or
// without the opt-in env var) must stay green. Two independent gates must both
// open before a single token is generated:
//
//  1. LOOM_LIVE_OLLAMA must be set to a truthy value (opt-in), and
//  2. the endpoint must answer /api/tags with the required models installed.
//
// Run it with:
//
//	LOOM_LIVE_OLLAMA=1 go test -tags fts5 -run TestLiveOllama ./pkg/orchestration/ -v -timeout 30m
//
// Cost discipline: only locally-served models are used. Any model whose name
// looks cloud-routed is refused outright by assertLocalOnlyModel — a
// cloud-hosted model reached through Ollama can bill the operator.

const (
	liveOllamaEnvVar = "LOOM_LIVE_OLLAMA"

	// liveWeakModel is the primary (cheap) rung: a 7B Q4_0 model old enough to
	// predate reliable JSON instruction-following, which is what makes the
	// schema signal fire at all.
	liveWeakModel = "llama2:latest"
	// liveStrongModel is the escalation rung: still local and still free, but
	// instruction-tuned generations later.
	liveStrongModel = "llama3.1:latest"

	// liveTemperature is pinned low so the measurement is about the models
	// rather than about sampling noise.
	liveTemperature = 0.1
	// liveMaxTokens caps a rambling generation so one bad trial cannot stall
	// the run.
	liveMaxTokens = 512
	// livePerCallTimeout bounds a single model call.
	livePerCallTimeout = 120 * time.Second
)

// liveSchema is the free structural signal leveling escalates on. It is
// deliberately both format-bound (exactly three keys, no extras, an integer
// where a model wants to write "+81", an uppercase-only pattern) and
// knowledge-bound (the currency and calling codes have to be known).
const liveSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["capital", "currency_code", "calling_code"],
  "properties": {
    "capital": {"type": "string", "minLength": 2},
    "currency_code": {"type": "string", "pattern": "^[A-Z]{3}$"},
    "calling_code": {"type": "integer", "minimum": 1, "maximum": 998}
  }
}`

// livePromptTemplate is task-oriented with no role framing, per project rules.
const livePromptTemplate = `Return the capital city, ISO 4217 currency code, and international calling code for %s.

Respond with a single JSON object and nothing else: no markdown, no code fences, no explanation, no leading or trailing text.

The object must have exactly these three keys and no others:
- "capital": string, the capital city name
- "currency_code": string, exactly three uppercase letters (ISO 4217)
- "calling_code": integer, the country calling code with no plus sign and no leading zeros

Example for Japan: {"capital":"Tokyo","currency_code":"JPY","calling_code":81}`

// liveTask is one trial: a country plus its ground truth, so the harness can
// separate "passed the schema" from "was actually right". Leveling only ever
// sees the schema; correctness is reported alongside so the schema-pass rate is
// not mistaken for an accuracy rate.
type liveTask struct {
	country      string
	capital      string
	currencyCode string
	callingCode  int
}

// liveTasks are ten countries chosen to spread difficulty: some the weak model
// will know, some it will not.
var liveTasks = []liveTask{
	{"Peru", "Lima", "PEN", 51},
	{"Norway", "Oslo", "NOK", 47},
	{"Vietnam", "Hanoi", "VND", 84},
	{"Morocco", "Rabat", "MAD", 212},
	{"Chile", "Santiago", "CLP", 56},
	{"Hungary", "Budapest", "HUF", 36},
	{"Kenya", "Nairobi", "KES", 254},
	{"Malaysia", "Kuala Lumpur", "MYR", 60},
	{"Croatia", "Zagreb", "EUR", 385},
	{"Sri Lanka", "Sri Jayawardenepura Kotte", "LKR", 94},
}

// ─────────────────────────── gating ───────────────────────────

// assertLocalOnlyModel refuses any model tag that looks cloud-routed. Ollama can
// proxy hosted models (e.g. "deepseek-v3.1:671b-cloud") which may bill the
// operator or require account auth; this harness must never reach one.
func assertLocalOnlyModel(t *testing.T, model string) {
	t.Helper()
	if strings.Contains(strings.ToLower(model), "cloud") {
		t.Fatalf("refusing to use %q: it looks cloud-routed, and this harness is local-only", model)
	}
}

// liveOllamaEndpoint resolves the endpoint the same way the rest of Loom does.
func liveOllamaEndpoint() string {
	for _, k := range []string{"OLLAMA_ENDPOINT", "OLLAMA_BASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "http://localhost:11434"
}

// requireLiveOllama applies both gates and returns the endpoint. It skips —
// never fails — when either gate is closed, so CI stays green.
func requireLiveOllama(t *testing.T, models ...string) string {
	t.Helper()

	switch os.Getenv(liveOllamaEnvVar) {
	case "", "0", "false", "no":
		t.Skipf("skipping live Ollama measurement: set %s=1 to opt in", liveOllamaEnvVar)
	}
	if testing.Short() {
		t.Skip("skipping live Ollama measurement in -short mode")
	}

	endpoint := liveOllamaEndpoint()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint + "/api/tags") //nolint:noctx // reachability probe only
	if err != nil {
		t.Skipf("skipping live Ollama measurement: %s unreachable: %v", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("skipping live Ollama measurement: /api/tags returned %d", resp.StatusCode)
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Skipf("skipping live Ollama measurement: cannot decode /api/tags: %v", err)
	}
	installed := make(map[string]bool, len(tags.Models))
	for _, m := range tags.Models {
		installed[m.Name] = true
	}
	for _, want := range models {
		assertLocalOnlyModel(t, want)
		if !installed[want] {
			t.Skipf("skipping live Ollama measurement: model %q is not installed", want)
		}
	}
	return endpoint
}

// ─────────────────────────── rungs ───────────────────────────

// liveRung is one model bound to a call counter, so every arm can report the
// number of model calls it actually made rather than the number it should have.
type liveRung struct {
	provider string
	model    string
	client   *ollama.Client
	calls    atomic.Int64
}

func newLiveRung(t *testing.T, endpoint, model string) *liveRung {
	t.Helper()
	assertLocalOnlyModel(t, model)
	return &liveRung{
		provider: "ollama",
		model:    model,
		client: ollama.NewClient(ollama.Config{
			Endpoint:    endpoint,
			Model:       model,
			Temperature: liveTemperature,
			MaxTokens:   liveMaxTokens,
			Timeout:     livePerCallTimeout,
		}),
	}
}

// rung converts the live rung into the executor's LevelingRung. Feedback is nil
// so retries go through Execute with a rebuilt prompt, matching how
// resolveLevelingLadder wires real escalation rungs.
func (r *liveRung) rung() LevelingRung {
	return LevelingRung{
		Provider: r.provider,
		Model:    r.model,
		Execute: func(ctx context.Context, _ string, prompt string) (*loomv1.AgentResult, error) {
			r.calls.Add(1)
			start := time.Now()
			resp, err := r.client.Chat(ctx, []types.Message{{
				Role:      "user",
				Content:   prompt,
				Timestamp: start,
			}}, nil)
			if err != nil {
				return nil, fmt.Errorf("live rung %s/%s: %w", r.provider, r.model, err)
			}
			return &loomv1.AgentResult{
				AgentId:    "live-harness",
				Output:     resp.Content,
				DurationMs: time.Since(start).Milliseconds(),
				Cost: &loomv1.AgentExecutionCost{
					InputTokens:  types.SafeInt32(resp.Usage.InputTokens),
					OutputTokens: types.SafeInt32(resp.Usage.OutputTokens),
					TotalTokens:  types.SafeInt32(resp.Usage.TotalTokens),
					CostUsd:      resp.Usage.CostUSD,
				},
			}, nil
		},
	}
}

// ─────────────────────────── measurement ───────────────────────────

// trial is one (arm, task) observation.
type trial struct {
	task            string
	schemaPass      bool
	factuallyRight  bool
	calls           int64
	escalations     int
	coercionApplied bool
	shortCircuited  bool
	warnings        int
	latency         time.Duration
	costUSD         float64
	output          string
	err             error
}

// armResult aggregates one arm's trials.
type armResult struct {
	name   string
	trials []trial
}

func (a armResult) schemaPasses() int {
	n := 0
	for _, tr := range a.trials {
		if tr.schemaPass {
			n++
		}
	}
	return n
}

func (a armResult) factualPasses() int {
	n := 0
	for _, tr := range a.trials {
		if tr.factuallyRight {
			n++
		}
	}
	return n
}

func (a armResult) totalCalls() int64 {
	var n int64
	for _, tr := range a.trials {
		n += tr.calls
	}
	return n
}

func (a armResult) totalCostUSD() float64 {
	var c float64
	for _, tr := range a.trials {
		c += tr.costUSD
	}
	return c
}

// latencyStats returns min/median/max over the arm's trials.
func (a armResult) latencyStats() (minD, medD, maxD time.Duration) {
	if len(a.trials) == 0 {
		return 0, 0, 0
	}
	ds := make([]time.Duration, 0, len(a.trials))
	for _, tr := range a.trials {
		ds = append(ds, tr.latency)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[0], ds[len(ds)/2], ds[len(ds)-1]
}

// checkFactual reports whether the output, parsed as the task's schema, matches
// ground truth. A schema failure is automatically a factual failure. Capital
// comparison is prefix-tolerant because several capitals have long official
// forms.
func checkFactual(task liveTask, output string) bool {
	var got struct {
		Capital      string `json:"capital"`
		CurrencyCode string `json:"currency_code"`
		CallingCode  int    `json:"calling_code"`
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		return false
	}
	wantCap := strings.ToLower(task.capital)
	gotCap := strings.ToLower(strings.TrimSpace(got.Capital))
	capOK := gotCap != "" && (strings.HasPrefix(wantCap, gotCap) || strings.HasPrefix(gotCap, wantCap))
	return capOK && got.CurrencyCode == task.currencyCode && got.CallingCode == task.callingCode
}

// runArm executes every task once through the given executor and ladder.
func runArm(t *testing.T, name string, exec *LevelingExecutor, ladder []LevelingRung, counters ...*liveRung) armResult {
	t.Helper()
	arm := armResult{name: name}
	policy := &loomv1.OutputPolicy{OutputSchema: liveSchema}

	for i, task := range liveTasks {
		for _, c := range counters {
			c.calls.Store(0)
		}
		prompt := fmt.Sprintf(livePromptTemplate, task.country)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		start := time.Now()
		result, report, err := exec.Execute(ctx, policy, ladder, prompt, fmt.Sprintf("%s-t%d", name, i))
		elapsed := time.Since(start)
		cancel()

		tr := trial{task: task.country, latency: elapsed, err: err}
		for _, c := range counters {
			tr.calls += c.calls.Load()
		}
		if report != nil {
			tr.escalations = report.Escalations
			tr.coercionApplied = report.CoercionApplied
			tr.shortCircuited = report.ShortCircuited
			tr.warnings = len(report.Warnings)
			tr.costUSD = report.TotalCostUSD
		} else if result != nil {
			tr.costUSD = result.GetCost().GetCostUsd()
		}
		if err == nil && result != nil {
			tr.output = result.Output
			// The arm's verdict is always recomputed here rather than read from
			// the report, so arms with leveling off are judged by exactly the
			// same yardstick as arms with it on.
			tr.schemaPass = validateJSONSchema(liveSchema, result.Output) == nil
			tr.factuallyRight = tr.schemaPass && checkFactual(task, result.Output)
		}
		arm.trials = append(arm.trials, tr)

		t.Logf("[%s] %-12s schema=%-5t factual=%-5t calls=%d esc=%d coerced=%t %6.2fs  out=%s",
			name, task.country, tr.schemaPass, tr.factuallyRight, tr.calls, tr.escalations,
			tr.coercionApplied, elapsed.Seconds(), oneLine(tr.output, 120))
		if err != nil {
			t.Logf("[%s] %-12s ERROR: %v", name, task.country, err)
		}
	}
	return arm
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// TestLiveOllamaLeveling is the live measurement: four arms over the same ten
// tasks, on the real local Ollama instance. Skipped by default; see the file
// header.
func TestLiveOllamaLeveling(t *testing.T) {
	endpoint := requireLiveOllama(t, liveWeakModel, liveStrongModel)

	// Discovery → catalog bridge. Without this, TierOf("ollama",
	// "llama2:latest") is TierUnknown, leveling short-circuits, and arm 2 is
	// identical to arm 1. This is the Job 1(b) fix being exercised live.
	if err := factory.RegisterOllamaCatalogSource(endpoint); err != nil {
		t.Fatalf("registering discovered Ollama models: %v", err)
	}
	t.Cleanup(func() { catalog.Register(nil) })

	if got := catalog.TierOf("ollama", liveWeakModel); got != catalog.TierLocal {
		t.Fatalf("precondition failed: TierOf(ollama, %s) = %v, want %v — leveling would short-circuit",
			liveWeakModel, got, catalog.TierLocal)
	}
	if got := catalog.TierOf("ollama", liveStrongModel); got != catalog.TierLocal {
		t.Fatalf("precondition failed: TierOf(ollama, %s) = %v, want %v", liveStrongModel, got, catalog.TierLocal)
	}

	weak := newLiveRung(t, endpoint, liveWeakModel)
	strong := newLiveRung(t, endpoint, liveStrongModel)

	off := NewLevelingExecutor(nil, nil, nil, nil)
	on := NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled:         true,
		ShortCircuitMid: true,
		MaxEscalations:  1,
	}, nil, nil)

	var arms []armResult

	// Arm 1 — weak model alone, leveling off. One call per task, no validation
	// retries: the baseline.
	arms = append(arms, runArm(t, "1-weak-off", off, []LevelingRung{weak.rung()}, weak))

	// Arm 2 — weak model with leveling on and one escalation rung to the strong
	// model. Default local tier policy: retry budget 2, free coercion, then one
	// escalation.
	arms = append(arms, runArm(t, "2-weak-leveling", on, []LevelingRung{weak.rung(), strong.rung()}, weak, strong))

	// Arm 3 — strong model alone, leveling off. The ceiling for this task set on
	// this hardware.
	arms = append(arms, runArm(t, "3-strong-off", off, []LevelingRung{strong.rung()}, strong))

	// Arm 4 — the happy-path cost of leveling. Strong model as primary with an
	// escalation rung wired up but expected never to fire. Comparing 4 against 3
	// isolates what enabling leveling costs when it is not needed. The escalation
	// rung gets its own counter so a stray escalation is attributable.
	strongEsc := newLiveRung(t, endpoint, liveStrongModel)
	arms = append(arms, runArm(t, "4-strong-leveling", on,
		[]LevelingRung{strong.rung(), strongEsc.rung()}, strong, strongEsc))

	reportArms(t, arms)

	// The one hard requirement: enabled-but-not-needed must not add a model
	// call. The predicate has to identify trials that passed on the FIRST
	// attempt, so every way of passing later is excluded: no escalation, no
	// coercion, and no warnings — one warning per failed attempt is exactly how
	// a same-model retry that then succeeded shows up, and such a trial is
	// legitimately more than one call.
	for _, tr := range arms[3].trials {
		if tr.err == nil && tr.schemaPass && tr.escalations == 0 && !tr.coercionApplied &&
			tr.warnings == 0 && tr.calls != 1 {
			t.Errorf("arm 4 %q passed the schema on the first attempt but made %d model calls; "+
				"leveling must add no call on the happy path", tr.task, tr.calls)
		}
	}
}

// reportArms prints the summary table. t.Logf rather than fmt so the output is
// attributed to the test and suppressed unless -v is passed.
func reportArms(t *testing.T, arms []armResult) {
	t.Helper()
	n := len(liveTasks)
	t.Logf("")
	t.Logf("── live leveling measurement (%d tasks/arm, ollama, temp=%.1f) ──", n, liveTemperature)
	t.Logf("%-18s %11s %11s %6s %9s %9s %9s %9s",
		"arm", "schema", "factual", "calls", "min", "median", "max", "cost")
	for _, a := range arms {
		minD, medD, maxD := a.latencyStats()
		t.Logf("%-18s %5d/%-5d %5d/%-5d %6d %8.2fs %8.2fs %8.2fs %9.4f",
			a.name, a.schemaPasses(), n, a.factualPasses(), n, a.totalCalls(),
			minD.Seconds(), medD.Seconds(), maxD.Seconds(), a.totalCostUSD())
	}
	for _, a := range arms {
		esc, coerced, errs := 0, 0, 0
		for _, tr := range a.trials {
			esc += tr.escalations
			if tr.coercionApplied {
				coerced++
			}
			if tr.err != nil {
				errs++
			}
		}
		t.Logf("%-18s escalations=%d coercions=%d errors=%d", a.name, esc, coerced, errs)
	}
}

// TestLiveOllamaLevelingHappyPathAddsNoCall isolates the single claim the
// latency requirement rests on, with one real model call per case instead of a
// full sweep: with leveling enabled, an output that satisfies the schema on the
// first attempt costs exactly one model call, the same as leveling off.
func TestLiveOllamaLevelingHappyPathAddsNoCall(t *testing.T) {
	endpoint := requireLiveOllama(t, liveStrongModel)
	if err := factory.RegisterOllamaCatalogSource(endpoint); err != nil {
		t.Fatalf("registering discovered Ollama models: %v", err)
	}
	t.Cleanup(func() { catalog.Register(nil) })

	policy := &loomv1.OutputPolicy{OutputSchema: liveSchema}
	prompt := fmt.Sprintf(livePromptTemplate, "France")

	cases := []struct {
		name   string
		policy *LevelingPolicy
	}{
		{"leveling-off", nil},
		{"leveling-on", &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newLiveRung(t, endpoint, liveStrongModel)
			esc := newLiveRung(t, endpoint, liveStrongModel)
			exec := NewLevelingExecutor(nil, tc.policy, nil, nil)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			start := time.Now()
			result, _, err := exec.Execute(ctx, policy, []LevelingRung{r.rung(), esc.rung()}, prompt, "happy-path")
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}

			pass := validateJSONSchema(liveSchema, result.Output) == nil
			t.Logf("%s: schema=%t primary_calls=%d escalation_calls=%d %.2fs out=%s",
				tc.name, pass, r.calls.Load(), esc.calls.Load(), elapsed.Seconds(), oneLine(result.Output, 160))

			if esc.calls.Load() != 0 {
				t.Errorf("escalation rung was called %d times; it must never run when the primary passes",
					esc.calls.Load())
			}
			if pass && r.calls.Load() != 1 {
				t.Errorf("primary made %d calls for a first-attempt pass, want 1", r.calls.Load())
			}
			if !pass {
				t.Skipf("primary did not satisfy the schema this run, so the happy path was not exercised")
			}
		})
	}
}

// BenchmarkLevelingEnabledOverhead measures the pure-Go cost of having leveling
// enabled, with no model involved: a synthetic Execute that returns instantly,
// so what is left is the tier lookup, the span, and the policy plumbing. This
// needs no Ollama and is the counterpart to arm 4 — arm 4 shows the cost is
// invisible next to a real model call, this shows what the cost actually is.
func BenchmarkLevelingEnabledOverhead(b *testing.B) {
	const output = `{"capital":"Oslo","currency_code":"NOK","calling_code":47}`
	policy := &loomv1.OutputPolicy{OutputSchema: liveSchema}
	instant := func(_ context.Context, _ string, _ string) (*loomv1.AgentResult, error) {
		return &loomv1.AgentResult{Output: output}, nil
	}

	cases := []struct {
		name  string
		lvl   *LevelingPolicy
		ptier string // provider/model chosen to hit a specific tier
		model string
	}{
		{"disabled", nil, "ollama", "llama3.1"},
		{"enabled-local-passes-schema", &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1}, "ollama", "llama3.1"},
		{"enabled-frontier-shortcircuits", &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1}, "anthropic", "claude-opus-4-7"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			exec := NewLevelingExecutor(nil, tc.lvl, nil, nil)
			ladder := []LevelingRung{{Provider: tc.ptier, Model: tc.model, Execute: instant}}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := exec.Execute(ctx, policy, ladder, "p", "w"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
