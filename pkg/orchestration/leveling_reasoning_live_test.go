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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/types"
)

// This file is the reasoning-bound live experiment for capability leveling. It
// shares the gating conventions of leveling_live_ollama_test.go — the same env
// var, the same reachability probe, the same assertLocalOnlyModel guard — and is
// SKIPPED BY DEFAULT so `go test -tags fts5 -race -short ./...` never touches a
// model.
//
// Where the earlier harness measured a knowledge/format task (country facts),
// this one measures the case the leveling design actually hinges on: a task the
// weak model gets NUMERICALLY WRONG while satisfying the output contract
// perfectly. That separates three claims that the earlier task conflated:
//
//	H1 — a JSON schema cannot detect a wrong-but-well-formed answer, so a
//	     schema-only signal escalates ~never and buys no accuracy.
//	H2 — with a judge signal, escalation becomes reachable and accuracy
//	     approaches the strong model's ceiling.
//	H3 — a judge critique fed back to the SAME weak model recovers some accuracy
//	     before any stronger model is paid for.
//
// Run it with:
//
//	LOOM_LIVE_OLLAMA=1 LOOM_LEVELING_RESULTS_DIR=/tmp/results \
//	  go test -tags fts5 -run TestLiveOllamaReasoningLeveling ./pkg/orchestration/ -v -timeout 150m
//
// Budget: a full run makes ~150 model calls, most of them to a reasoning model
// at ~21s each, so expect tens of minutes.

const (
	// Ladder models. All three are local; assertLocalOnlyModel refuses anything
	// that looks cloud-routed, on every name, before a socket is opened.
	rzWeakModel   = "llama2:latest"      // primary rung: 60% wrong on this task
	rzStrongModel = "deepseek-r1:latest" // escalation rung: 0% wrong on this task
	rzJudgeModel  = "llama3.1:latest"    // measured AS a judge, never used as one

	// Sampling is pinned to the calibration settings so the Go run and the
	// Python calibration are measuring the same thing. Seed is explicit: every
	// arm sees identical sampling for identical prompts.
	rzTemperature   = 0.1
	rzSeed          = int64(0)
	rzCallTimeout   = 300 * time.Second
	rzProblemBudget = 15 * time.Minute

	// Output budgets are PER MODEL, and this is not cosmetic. A reasoning model
	// spends its budget on reasoning before it writes any content: measured
	// directly against /api/chat, deepseek-r1 on this task stops at
	// done_reason="length" with 1024 tokens of thinking and an EMPTY content
	// field, so a 1024-token cap silently turns the strong rung into a
	// no-output rung (identical behavior streaming and non-streaming). At 3072
	// the same call finishes with done_reason="stop" after ~1355 tokens and
	// emits its JSON. llama2 does not think and needs ~100 tokens.
	rzWeakMaxTokens   = 1024
	rzStrongMaxTokens = 3072
	rzJudgeMaxTokens  = 1024

	// rzN is the default number of problems every arm runs, on the same indices
	// 0..N-1. rzNEnv overrides it (used for smoke runs of the harness itself);
	// the reported N always comes from rzProblemCount, never from this constant.
	rzN    = 30
	rzNEnv = "LOOM_LEVELING_N"

	// rzJudgeProbeN is how many (problem, weak answer) pairs the secondary
	// llama3.1-as-judge accuracy probe scores.
	rzJudgeProbeN = 20

	// rzResultsDirEnv names the directory per-trial JSONL is written to. Unset
	// means no files are written (t.Log still carries every row).
	rzResultsDirEnv = "LOOM_LEVELING_RESULTS_DIR"
)

// rzJudgeCritique is the oracle judge's rejection reason, and it is deliberately
// answer-free.
//
// This is the single most important design constraint in the experiment. The
// critique is fed verbatim into the escalation prompt, so if it named the
// expected value ("expected 537"), arm 3b would measure the weak model's ability
// to COPY a number out of its prompt, not its ability to repair its own
// arithmetic. It says only that the arithmetic is wrong and that the work should
// be redone.
const rzJudgeCritique = "the arithmetic in the worked solution is incorrect; recompute each step carefully"

// rzCritiqueTemplate is the escalation feedback template. A template is REQUIRED
// for the prior output to reach the escalation rung: buildRetryPromptWithOutput
// only substitutes {{previous_output}} when a FeedbackTemplate is set, and its
// default branch drops the previous output entirely. Without this, arm 3b's
// self-critique rung would receive the critique but not its own rejected work,
// which is a different (and much weaker) experiment.
//
// No role framing, per project rules.
const rzCritiqueTemplate = `A reviewer checked the previous answer and rejected it.

Reviewer note: {{error}}

The rejected reply was:
{{previous_output}}

Redo the calculation from the beginning, one step at a time, and reply with a single JSON object and nothing else, in this form:
{"answer": <integer>}`

// rzProblemCount is the number of problems each arm runs: rzN unless rzNEnv
// overrides it with a positive integer.
func rzProblemCount() int {
	if v := os.Getenv(rzNEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return rzN
}

// ─────────────────────────── models and recording ───────────────────────────

// rzModel is one ladder rung's client plus its own call counter, so per-rung
// call counts are attributable rather than inferred.
type rzModel struct {
	provider string
	model    string
	client   *ollama.Client
	calls    int
}

// rzMaxTokensFor returns the output budget for a model. See the constants: a
// reasoning model needs enough room to think AND then answer.
func rzMaxTokensFor(model string) int {
	switch model {
	case rzStrongModel:
		return rzStrongMaxTokens
	case rzJudgeModel:
		return rzJudgeMaxTokens
	default:
		return rzWeakMaxTokens
	}
}

func newRZModel(t *testing.T, endpoint, model string) *rzModel {
	t.Helper()
	assertLocalOnlyModel(t, model)
	seed := rzSeed
	return &rzModel{
		provider: "ollama",
		model:    model,
		client: ollama.NewClient(ollama.Config{
			Endpoint:    endpoint,
			Model:       model,
			Temperature: rzTemperature,
			MaxTokens:   rzMaxTokensFor(model),
			Timeout:     rzCallTimeout,
			Seed:        &seed,
		}),
	}
}

// rzAttempt is one model call inside one trial.
type rzAttempt struct {
	Rung       int    `json:"rung"`
	Model      string `json:"model"`
	Output     string `json:"output"`
	Answer     int    `json:"answer"`
	Parsed     bool   `json:"parsed"`
	Correct    bool   `json:"correct"`
	SchemaPass bool   `json:"schema_pass"`
	// Empty marks a reply with no content at all — for a reasoning model that
	// means the output budget was spent before it wrote an answer.
	Empty   bool    `json:"empty"`
	Seconds float64 `json:"seconds"`
}

// rzTrial is one (arm, problem) observation.
type rzTrial struct {
	Arm          string      `json:"arm"`
	Index        int         `json:"index"`
	Expression   string      `json:"expression"`
	Truth        int         `json:"truth"`
	FinalAnswer  int         `json:"final_answer"`
	Parsed       bool        `json:"parsed"`
	Correct      bool        `json:"correct"`
	SchemaPass   bool        `json:"schema_pass"`
	Escalations  int         `json:"escalations"`
	JudgeCalls   int         `json:"judge_calls"`
	Calls        int         `json:"calls"`
	Coerced      bool        `json:"coercion_applied"`
	ShortCircuit bool        `json:"short_circuited"`
	LevelingPass bool        `json:"leveling_passed"`
	Seconds      float64     `json:"seconds"`
	Attempts     []rzAttempt `json:"attempts"`
	Err          string      `json:"error,omitempty"`
}

// rzJudgeStats counts oracle-judge invocations, split by whether the output the
// judge examined was already correct. The split is the number that decides
// whether a REAL judge is affordable: every invocation on an already-correct
// output is pure added latency on the happy path.
type rzJudgeStats struct {
	calls          int
	callsOnCorrect int
	callsOnWrong   int
}

// rzArm runs one arm: a ladder of models under one leveling policy, over the
// same rzN problems as every other arm.
//
// The runner holds the problem currently in flight so the rung closures and the
// oracle judge can score against ground truth. Every call the executor makes is
// sequential and on this goroutine, so plain fields are race-clean here.
type rzArm struct {
	name     string
	models   []*rzModel
	problem  arithProblem
	attempts []rzAttempt
	judge    rzJudgeStats
}

// ladder wraps each model as a LevelingRung that records its own attempt.
func (a *rzArm) ladder() []LevelingRung {
	rungs := make([]LevelingRung, 0, len(a.models))
	for i := range a.models {
		i := i
		m := a.models[i]
		rungs = append(rungs, LevelingRung{
			Provider: m.provider,
			Model:    m.model,
			Execute: func(ctx context.Context, _ string, prompt string) (*loomv1.AgentResult, error) {
				m.calls++
				start := time.Now()
				resp, err := m.client.Chat(ctx, []types.Message{{
					Role:      "user",
					Content:   prompt,
					Timestamp: start,
				}}, nil)
				elapsed := time.Since(start)
				if err != nil {
					return nil, fmt.Errorf("rung %d %s/%s: %w", i, m.provider, m.model, err)
				}
				answer, parsed := parseArithAnswer(resp.Content)
				a.attempts = append(a.attempts, rzAttempt{
					Rung:       i,
					Model:      m.model,
					Output:     resp.Content,
					Answer:     answer,
					Parsed:     parsed,
					Correct:    parsed && answer == a.problem.Answer,
					SchemaPass: validateJSONSchema(arithAnswerSchema, resp.Content) == nil,
					Empty:      strings.TrimSpace(resp.Content) == "",
					Seconds:    elapsed.Seconds(),
				})
				return &loomv1.AgentResult{
					AgentId:    "rz-harness",
					Output:     resp.Content,
					DurationMs: elapsed.Milliseconds(),
					Cost: &loomv1.AgentExecutionCost{
						InputTokens:  types.SafeInt32(resp.Usage.InputTokens),
						OutputTokens: types.SafeInt32(resp.Usage.OutputTokens),
						TotalTokens:  types.SafeInt32(resp.Usage.TotalTokens),
						CostUsd:      resp.Usage.CostUSD,
					},
				}, nil
			},
		})
	}
	return rungs
}

// oracleJudge is the primary quality signal for arms 3a and 3b: ground truth
// computed in Go, so the measurement isolates ladder mechanics from judge noise.
// It returns zero cost because it makes no model call — the price a real judge
// would charge is accounted separately via rzJudgeStats.
func (a *rzArm) oracleJudge() LevelingJudge {
	return func(_ context.Context, _ string, output string) (bool, string, float64, error) {
		answer, ok := parseArithAnswer(output)
		pass := ok && answer == a.problem.Answer
		a.judge.calls++
		if pass {
			a.judge.callsOnCorrect++
			return true, "", 0, nil
		}
		a.judge.callsOnWrong++
		return false, rzJudgeCritique, 0, nil
	}
}

// run executes the arm over problems 0..rzN-1.
func (a *rzArm) run(t *testing.T, exec *LevelingExecutor, policy *loomv1.OutputPolicy) []rzTrial {
	t.Helper()
	n := rzProblemCount()
	trials := make([]rzTrial, 0, n)
	ladder := a.ladder()

	for i := 0; i < n; i++ {
		a.problem = genArithChain11(i)
		a.attempts = nil
		before := make([]int, len(a.models))
		for k, m := range a.models {
			before[k] = m.calls
		}
		judgeBefore := a.judge.calls

		ctx, cancel := context.WithTimeout(context.Background(), rzProblemBudget)
		start := time.Now()
		result, report, err := exec.Execute(ctx, policy, ladder, a.problem.Prompt,
			fmt.Sprintf("%s-p%d", a.name, i))
		elapsed := time.Since(start)
		cancel()

		tr := rzTrial{
			Arm:        a.name,
			Index:      i,
			Expression: a.problem.Expression,
			Truth:      a.problem.Answer,
			Seconds:    elapsed.Seconds(),
			Attempts:   append([]rzAttempt(nil), a.attempts...),
			JudgeCalls: a.judge.calls - judgeBefore,
		}
		for k, m := range a.models {
			tr.Calls += m.calls - before[k]
		}
		if err != nil {
			tr.Err = err.Error()
		}
		if report != nil {
			tr.Escalations = report.Escalations
			tr.Coerced = report.CoercionApplied
			tr.ShortCircuit = report.ShortCircuited
			tr.LevelingPass = report.Passed
		}
		if err == nil && result != nil {
			// Scored here, not read off the report, so every arm is judged by
			// exactly the same yardstick regardless of its policy.
			tr.SchemaPass = validateJSONSchema(arithAnswerSchema, result.Output) == nil
			tr.FinalAnswer, tr.Parsed = parseArithAnswer(result.Output)
			tr.Correct = tr.Parsed && tr.FinalAnswer == a.problem.Answer
		}
		trials = append(trials, tr)

		t.Logf("[%s] i=%-2d %-22s truth=%-5d got=%-6s correct=%-5t schema=%-5t calls=%d esc=%d judge=%d %6.1fs",
			a.name, i, a.problem.Expression, a.problem.Answer, rzAnswerStr(tr),
			tr.Correct, tr.SchemaPass, tr.Calls, tr.Escalations, tr.JudgeCalls, tr.Seconds)
		if tr.Err != "" {
			t.Logf("[%s] i=%-2d ERROR: %s", a.name, i, tr.Err)
		}
	}
	return trials
}

func rzAnswerStr(tr rzTrial) string {
	if !tr.Parsed {
		return "UNPARSED"
	}
	return fmt.Sprintf("%d", tr.FinalAnswer)
}

// ─────────────────────────── aggregation ───────────────────────────

type rzSummary struct {
	name             string
	n                int
	correct          int
	schemaPass       int
	parsed           int
	calls            int
	escalations      int
	rungCalls        map[int]int
	coercions        int
	errors           int
	emptyReplies     int
	judgeCalls       int
	judgeOnCorrect   int
	minS, medS, maxS float64
	totalS           float64
}

func rzSummarize(name string, trials []rzTrial, judge rzJudgeStats) rzSummary {
	s := rzSummary{name: name, n: len(trials), rungCalls: map[int]int{},
		judgeCalls: judge.calls, judgeOnCorrect: judge.callsOnCorrect}
	lat := make([]float64, 0, len(trials))
	for _, tr := range trials {
		if tr.Correct {
			s.correct++
		}
		if tr.SchemaPass {
			s.schemaPass++
		}
		if tr.Parsed {
			s.parsed++
		}
		if tr.Coerced {
			s.coercions++
		}
		if tr.Err != "" {
			s.errors++
		}
		s.calls += tr.Calls
		s.escalations += tr.Escalations
		for _, at := range tr.Attempts {
			s.rungCalls[at.Rung]++
			if at.Empty {
				s.emptyReplies++
			}
		}
		lat = append(lat, tr.Seconds)
		s.totalS += tr.Seconds
	}
	if len(lat) > 0 {
		sort.Float64s(lat)
		s.minS, s.medS, s.maxS = lat[0], lat[len(lat)/2], lat[len(lat)-1]
	}
	return s
}

func rzRungCallString(s rzSummary) string {
	keys := make([]int, 0, len(s.rungCalls))
	for k := range s.rungCalls {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("r%d=%d", k, s.rungCalls[k]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

// ─────────────────────────── the experiment ───────────────────────────

// TestLiveOllamaReasoningLeveling is the five-arm reasoning-bound measurement.
// Skipped by default; see the file header.
func TestLiveOllamaReasoningLeveling(t *testing.T) {
	endpoint := requireLiveOllama(t, rzWeakModel, rzStrongModel)

	// Discovery → catalog bridge, without which TierOf("ollama", "llama2:latest")
	// is TierUnknown and every leveling arm short-circuits into arm 1.
	if err := factory.RegisterOllamaCatalogSource(endpoint); err != nil {
		t.Fatalf("registering discovered Ollama models: %v", err)
	}
	t.Cleanup(func() { catalog.Register(nil) })
	if got := catalog.TierOf("ollama", rzWeakModel); got != catalog.TierLocal {
		t.Fatalf("precondition failed: TierOf(ollama, %s) = %v, want %v — leveling would short-circuit",
			rzWeakModel, got, catalog.TierLocal)
	}

	// Contracts. The schema arms and the judge arms cannot share one policy: the
	// executor consults a judge ONLY when the effective OutputPolicy carries no
	// schema (primaryVerdict/evaluate give a present schema the whole verdict), so
	// a judge-driven arm must run with the schema removed. That exclusivity is a
	// finding in its own right and is recorded in the plan doc.
	schemaPolicy := &loomv1.OutputPolicy{OutputSchema: arithAnswerSchema}
	judgePolicy := &loomv1.OutputPolicy{
		// No schema: the judge owns the verdict. MaxRetries 0 keeps the primary
		// rung to a single call; the template exists purely so the escalation
		// prompt carries the critique AND the rejected output.
		RetryPolicy: &loomv1.OutputRetryPolicy{
			MaxRetries:       0,
			FeedbackTemplate: rzCritiqueTemplate,
		},
	}

	levelingOff := NewLevelingExecutor(nil, nil, nil, nil)

	runStart := time.Now()
	var allTrials []rzTrial
	var summaries []rzSummary

	// ── Arm 1: llama2 alone, leveling off. The floor. ──
	arm1 := &rzArm{name: "1-llama2-off", models: []*rzModel{newRZModel(t, endpoint, rzWeakModel)}}
	tr1 := arm1.run(t, levelingOff, schemaPolicy)
	allTrials = append(allTrials, tr1...)
	summaries = append(summaries, rzSummarize(arm1.name, tr1, arm1.judge))

	// ── Arm 2: llama2 + leveling, schema-only signal, ladder [llama2 → r1]. ──
	arm2 := &rzArm{name: "2-llama2-schema", models: []*rzModel{
		newRZModel(t, endpoint, rzWeakModel), newRZModel(t, endpoint, rzStrongModel)}}
	tr2 := arm2.run(t, NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled: true, ShortCircuitMid: true, MaxEscalations: 1,
	}, nil, nil), schemaPolicy)
	allTrials = append(allTrials, tr2...)
	summaries = append(summaries, rzSummarize(arm2.name, tr2, arm2.judge))

	// ── Arm 3a: llama2 + leveling, oracle judge, ladder [llama2 → r1]. ──
	arm3a := &rzArm{name: "3a-judge-r1", models: []*rzModel{
		newRZModel(t, endpoint, rzWeakModel), newRZModel(t, endpoint, rzStrongModel)}}
	tr3a := arm3a.run(t, NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled: true, ShortCircuitMid: true, MaxEscalations: 1, Judge: arm3a.oracleJudge(),
	}, nil, nil), judgePolicy)
	allTrials = append(allTrials, tr3a...)
	summaries = append(summaries, rzSummarize(arm3a.name, tr3a, arm3a.judge))

	// ── Arm 3b: the self-critique rung. Ladder [llama2 → llama2 → r1]: rung 1 is
	// the SAME weak model re-run with the judge's critique and its own rejected
	// output, and only a still-failing rung 1 pays for r1. ──
	arm3b := &rzArm{name: "3b-judge-self-r1", models: []*rzModel{
		newRZModel(t, endpoint, rzWeakModel),
		newRZModel(t, endpoint, rzWeakModel),
		newRZModel(t, endpoint, rzStrongModel)}}
	tr3b := arm3b.run(t, NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled: true, ShortCircuitMid: true, MaxEscalations: 2, Judge: arm3b.oracleJudge(),
	}, nil, nil), judgePolicy)
	allTrials = append(allTrials, tr3b...)
	summaries = append(summaries, rzSummarize(arm3b.name, tr3b, arm3b.judge))

	// ── Arm 4: r1 alone, leveling off. The ceiling. ──
	arm4 := &rzArm{name: "4-r1-off", models: []*rzModel{newRZModel(t, endpoint, rzStrongModel)}}
	tr4 := arm4.run(t, levelingOff, schemaPolicy)
	allTrials = append(allTrials, tr4...)
	summaries = append(summaries, rzSummarize(arm4.name, tr4, arm4.judge))

	wall := time.Since(runStart)

	// ── Reporting ──
	rzReport(t, summaries)
	rzPerProblem(t, []([]rzTrial){tr1, tr2, tr3a, tr3b, tr4},
		[]string{arm1.name, arm2.name, arm3a.name, arm3b.name, arm4.name})
	rzSelfCritiqueBreakdown(t, tr3b)
	rzJudgeHappyPathCost(t, arm3a, tr3a)
	rzJudgeHappyPathCost(t, arm3b, tr3b)

	// ── Secondary: a REAL local judge's accuracy on the same weak outputs. ──
	probe := rzJudgeAccuracyProbe(t, endpoint, tr1)

	t.Logf("")
	t.Logf("total experiment wall-clock: %s (seed=%d, temp=%.1f, num_predict weak/strong=%d/%d, N=%d)",
		wall.Round(time.Second), rzSeed, rzTemperature, rzWeakMaxTokens, rzStrongMaxTokens, rzProblemCount())

	rzWriteResults(t, allTrials, probe)
}

// rzReport prints the per-arm table.
func rzReport(t *testing.T, summaries []rzSummary) {
	t.Helper()
	t.Logf("")
	t.Logf("── reasoning-bound leveling: arith_chain L11, N=%d identical problems/arm ──", rzProblemCount())
	t.Logf("%-18s %9s %9s %6s %-14s %5s %6s %7s %7s %7s",
		"arm", "accuracy", "schema", "calls", "calls-by-rung", "esc", "judge", "min", "med", "max")
	for _, s := range summaries {
		t.Logf("%-18s %4d/%-4d %4d/%-4d %6d %-14s %5d %6d %6.1fs %6.1fs %6.1fs",
			s.name, s.correct, s.n, s.schemaPass, s.n, s.calls, rzRungCallString(s),
			s.escalations, s.judgeCalls, s.minS, s.medS, s.maxS)
	}
	for _, s := range summaries {
		t.Logf("%-18s parsed=%d/%d coercions=%d errors=%d empty_replies=%d total_model_time=%.1fs",
			s.name, s.parsed, s.n, s.coercions, s.errors, s.emptyReplies, s.totalS)
	}
}

// rzPerProblem prints the per-problem comparison across arms: the whole point of
// running identical problem sets is that arms can be compared row by row.
func rzPerProblem(t *testing.T, arms [][]rzTrial, names []string) {
	t.Helper()
	t.Logf("")
	t.Logf("── per-problem outcomes (value = final answer, ✓ = correct) ──")
	header := fmt.Sprintf("%-3s %-22s %-6s", "i", "expression", "truth")
	for _, n := range names {
		header += fmt.Sprintf(" %-16s", n)
	}
	t.Logf("%s", header)
	for i := 0; i < len(arms[0]); i++ {
		row := fmt.Sprintf("%-3d %-22s %-6d", i, arms[0][i].Expression, arms[0][i].Truth)
		for _, arm := range arms {
			tr := arm[i]
			mark := " "
			if tr.Correct {
				mark = "✓"
			}
			row += fmt.Sprintf(" %-16s", fmt.Sprintf("%s%s(e%d)", mark, rzAnswerStr(tr), tr.Escalations))
		}
		t.Logf("%s", row)
	}
}

// rzSelfCritiqueBreakdown reports the central number for H3: of the problems the weak model
// got wrong at rung 0, how many did the SAME model repair at rung 1 when handed
// an answer-free critique plus its own rejected output, and how many still had to
// be escalated to the strong model at rung 2.
func rzSelfCritiqueBreakdown(t *testing.T, trials []rzTrial) {
	t.Helper()
	wrongAtRung0, repairedAtRung1, reachedRung2, fixedAtRung2, stillWrong := 0, 0, 0, 0, 0
	for _, tr := range trials {
		if len(tr.Attempts) == 0 || tr.Attempts[0].Correct {
			continue
		}
		wrongAtRung0++
		var r1, r2 *rzAttempt
		for i := range tr.Attempts {
			switch tr.Attempts[i].Rung {
			case 1:
				r1 = &tr.Attempts[i]
			case 2:
				r2 = &tr.Attempts[i]
			}
		}
		switch {
		case r1 != nil && r1.Correct:
			repairedAtRung1++
		case r2 != nil:
			reachedRung2++
			if r2.Correct {
				fixedAtRung2++
			} else {
				stillWrong++
			}
		default:
			stillWrong++
		}
	}
	t.Logf("")
	t.Logf("── arm 3b self-critique rung (H3) ──")
	t.Logf("wrong at rung 0 (llama2, first try): %d", wrongAtRung0)
	t.Logf("repaired at rung 1 (SAME llama2 + answer-free critique): %d  → repair rate %s",
		repairedAtRung1, rzPct(repairedAtRung1, wrongAtRung0))
	t.Logf("escalated to rung 2 (r1): %d, of which fixed there: %d", reachedRung2, fixedAtRung2)
	t.Logf("still wrong after the whole ladder: %d", stillWrong)
}

// rzJudgeHappyPathCost reports what a REAL judge would have cost on the happy
// path: one extra LLM call for every judge invocation on an output that was
// already correct. This is the number that decides whether C3's judge can meet
// the project's no-added-latency-on-the-happy-path requirement.
func rzJudgeHappyPathCost(t *testing.T, arm *rzArm, trials []rzTrial) {
	t.Helper()
	if arm.judge.calls == 0 {
		t.Logf("[%s] judge never invoked", arm.name)
		return
	}
	var modelTime float64
	for _, tr := range trials {
		modelTime += tr.Seconds
	}
	t.Logf("")
	t.Logf("── [%s] judge cost accounting ──", arm.name)
	t.Logf("judge invocations total: %d (on already-correct outputs: %d, on wrong outputs: %d)",
		arm.judge.calls, arm.judge.callsOnCorrect, arm.judge.callsOnWrong)
	t.Logf("the oracle judge is free (pure Go); a REAL judge would add %d extra LLM calls, "+
		"%d of them on outputs that needed no help at all (%s of all invocations)",
		arm.judge.calls, arm.judge.callsOnCorrect,
		rzPct(arm.judge.callsOnCorrect, arm.judge.calls))
	t.Logf("arm wall-clock without judge calls: %.1fs over %d problems", modelTime, len(trials))
}

func rzPct(num, den int) string {
	if den == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(num)/float64(den))
}

// ─────────────────── secondary: llama3.1 as a real judge ───────────────────

// rzJudgeProbeRow is one scored (problem, weak answer) pair.
type rzJudgeProbeRow struct {
	Index          int     `json:"index"`
	Expression     string  `json:"expression"`
	Truth          int     `json:"truth"`
	WeakAnswer     int     `json:"weak_answer"`
	WeakParsed     bool    `json:"weak_parsed"`
	WeakCorrect    bool    `json:"weak_correct"`
	JudgeVerdict   string  `json:"judge_verdict"`
	JudgeSaysRight bool    `json:"judge_says_right"`
	JudgeAgrees    bool    `json:"judge_agrees_with_truth"`
	JudgeRaw       string  `json:"judge_raw"`
	Seconds        float64 `json:"seconds"`
}

const rzJudgeProbePrompt = `Expression: %s

Below is a submitted solution for that expression. Check the arithmetic and decide whether the submitted final answer is correct.

Submitted solution:
<<<
%s
>>>

Reply with a single JSON object and nothing else, in this form:
{"verdict": "correct"} or {"verdict": "incorrect"}`

var rzVerdictRE = regexp.MustCompile(`(?i)"verdict"\s*:\s*"?(correct|incorrect)"?`)

// rzJudgeAccuracyProbe measures a real local model's accuracy AS a judge on the
// same weak outputs the arms saw. It is a measurement, never the driving signal
// for any arm: a noisy judge would confound the ladder mechanics H1–H3 are about.
// It quantifies the plan's known risk "an unreliable judge misroutes escalation".
func rzJudgeAccuracyProbe(t *testing.T, endpoint string, weakTrials []rzTrial) []rzJudgeProbeRow {
	t.Helper()
	if !rzModelInstalled(t, endpoint, rzJudgeModel) {
		t.Logf("judge accuracy probe skipped: %q is not installed", rzJudgeModel)
		return nil
	}
	judge := newRZModel(t, endpoint, rzJudgeModel)

	n := rzJudgeProbeN
	if len(weakTrials) < n {
		n = len(weakTrials)
	}
	rows := make([]rzJudgeProbeRow, 0, n)
	for i := 0; i < n; i++ {
		wt := weakTrials[i]
		if len(wt.Attempts) == 0 {
			continue
		}
		weak := wt.Attempts[0]
		prompt := fmt.Sprintf(rzJudgeProbePrompt, wt.Expression, weak.Output)

		ctx, cancel := context.WithTimeout(context.Background(), rzCallTimeout)
		start := time.Now()
		resp, err := judge.client.Chat(ctx, []types.Message{{
			Role: "user", Content: prompt, Timestamp: start,
		}}, nil)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			t.Logf("[judge-probe] i=%d ERROR: %v", i, err)
			continue
		}

		verdict := ""
		if m := rzVerdictRE.FindStringSubmatch(thinkTagRE.ReplaceAllString(resp.Content, "")); len(m) == 2 {
			verdict = strings.ToLower(m[1])
		}
		saysRight := verdict == "correct"
		row := rzJudgeProbeRow{
			Index:          wt.Index,
			Expression:     wt.Expression,
			Truth:          wt.Truth,
			WeakAnswer:     weak.Answer,
			WeakParsed:     weak.Parsed,
			WeakCorrect:    weak.Correct,
			JudgeVerdict:   verdict,
			JudgeSaysRight: saysRight,
			JudgeAgrees:    verdict != "" && saysRight == weak.Correct,
			JudgeRaw:       resp.Content,
			Seconds:        elapsed.Seconds(),
		}
		rows = append(rows, row)
		t.Logf("[judge-probe] i=%-2d %-22s truth=%-5d weak=%-6s weak_ok=%-5t verdict=%-9s agrees=%-5t %5.1fs",
			wt.Index, wt.Expression, wt.Truth, rzAnswerStr(wt), weak.Correct,
			rzOrDash(verdict), row.JudgeAgrees, elapsed.Seconds())
	}

	agree, falsePass, falseFail, unparsed := 0, 0, 0, 0
	var totalS float64
	for _, r := range rows {
		totalS += r.Seconds
		if r.JudgeVerdict == "" {
			unparsed++
			continue
		}
		switch {
		case r.JudgeAgrees:
			agree++
		case r.JudgeSaysRight && !r.WeakCorrect:
			falsePass++ // the expensive error: a wrong answer waved through
		default:
			falseFail++ // the wasteful error: a right answer escalated anyway
		}
	}
	t.Logf("")
	t.Logf("── secondary: %s AS a judge, on %d real (problem, llama2-answer) pairs ──", rzJudgeModel, len(rows))
	t.Logf("agreement with ground truth: %d/%d (%s)", agree, len(rows), rzPct(agree, len(rows)))
	t.Logf("false PASS (wrong answer judged correct → escalation never fires): %d", falsePass)
	t.Logf("false FAIL (right answer judged incorrect → needless escalation): %d", falseFail)
	t.Logf("unparseable judge replies: %d", unparsed)
	if len(rows) > 0 {
		t.Logf("mean judge latency: %.1fs — this is what one real judge call adds per problem", totalS/float64(len(rows)))
	}
	return rows
}

func rzOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// rzModelInstalled reports whether a model is installed, without failing the
// test when it is not. The cloud guard applies here too.
func rzModelInstalled(t *testing.T, endpoint, model string) bool {
	t.Helper()
	assertLocalOnlyModel(t, model)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint + "/api/tags") //nolint:noctx // reachability probe only
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false
	}
	for _, m := range tags.Models {
		if m.Name == model {
			return true
		}
	}
	return false
}

// ─────────────────────────── per-trial artifacts ───────────────────────────

// rzWriteResults writes per-trial JSONL when rzResultsDirEnv is set. A write
// failure is logged, never fatal: the measurement already happened and t.Log
// carries every row.
func rzWriteResults(t *testing.T, trials []rzTrial, probe []rzJudgeProbeRow) {
	t.Helper()
	dir := os.Getenv(rzResultsDirEnv)
	if dir == "" {
		t.Logf("per-trial JSONL not written: set %s to a directory to capture it", rzResultsDirEnv)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("cannot create results dir %q: %v", dir, err)
		return
	}
	write := func(name string, rows []any) {
		path := filepath.Join(dir, name)
		var buf strings.Builder
		for _, r := range rows {
			b, err := json.Marshal(r)
			if err != nil {
				t.Logf("marshaling a %s row: %v", name, err)
				continue
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // local measurement artifact
			t.Logf("writing %s: %v", path, err)
			return
		}
		t.Logf("wrote %d rows to %s", len(rows), path)
	}

	rows := make([]any, 0, len(trials))
	for _, tr := range trials {
		rows = append(rows, tr)
	}
	write("reasoning_arms.jsonl", rows)

	if len(probe) > 0 {
		prows := make([]any, 0, len(probe))
		for _, r := range probe {
			prows = append(prows, r)
		}
		write("judge_probe.jsonl", prows)
	}
}
