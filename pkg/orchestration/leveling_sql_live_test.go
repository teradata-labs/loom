// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// This file is the SQL-GENERATION live experiment for capability leveling — the
// fourth live harness, after leveling_live_ollama_test.go (format-bound),
// leveling_reasoning_live_test.go (reasoning-bound) and
// leveling_knowledge_live_test.go (knowledge-bound). It shares their gating
// conventions exactly: opt-in via LOOM_LIVE_OLLAMA=1, a reachability probe over
// /api/tags, assertLocalOnlyModel on every model name before a socket opens, and
// SKIPPED in -short so `go test -tags fts5 -race -short ./...` never touches a
// model.
//
// Two things are new here.
//
// First, the weak model is MODERN. Every earlier experiment used llama2 as the
// primary rung, and the obvious objection to all three results is that llama2 is
// a 2023 model that fails at things no current small model fails at. This harness
// runs llama3.2 as the primary, so "does leveling still hold once the weak rung
// is competent" gets a direct answer instead of an extrapolation.
//
// Second, the failure signal is EXECUTION, not a schema and not an oracle. The
// judge parses the model's SQL, runs it against the real fixture database on a
// fresh read-only connection, and passes it if and only if it executes. That is
// the strongest signal a production system can have for free: it needs no
// reference answer, no second model, and no human. It is also the signal a real
// text-to-SQL product would actually reach for first.
//
// ── Hypotheses ──
//
//	H1 — an execution signal sees exec_error and NOTHING ELSE. A query that runs
//	     but answers the wrong question (silent_wrong) passes the judge on the
//	     first rung, so no escalation fires and arms 2 and 3 cannot repair it. This
//	     is structural, not empirical: it is the same shape as the reasoning
//	     experiment's H1 (a JSON schema cannot detect a wrong-but-well-formed
//	     answer), one level up. The experiment MEASURES the size of that invisible
//	     bucket — what fraction of a modern weak model's SQL failures are silently
//	     wrong rather than broken.
//
//	H2 — same-model retries (arm 2) recover only the exec_error subset, and only
//	     the part of it the model can fix when told the sqlite error text. The two
//	     earlier experiments both found same-model retry recovers ~nothing; a
//	     concrete error message is a much stronger critique than "the arithmetic is
//	     wrong", so this is the first fair test of it.
//
//	H3 — the strong rung (arm 3) only ever fires on questions arm 2 also failed,
//	     so its accuracy gain over arm 2 is bounded by the residual exec_error
//	     rate — a small number, no matter how good deepseek-r1 is. Arm 4 (r1 alone)
//	     is the ceiling that shows how much accuracy the ladder LEAVES on the table
//	     because it never gets consulted on the silent_wrong questions.
//
// ── Arms, all over the same questions 0..N-1 ──
//
//	1 llama3.2 alone, leveling off                     — the floor.
//	2 llama3.2 + leveling, exec judge, [w,w,w] esc=2   — retries only, no stronger model.
//	3 llama3.2 + leveling, exec judge, [w,w,w,r1] esc=3 — retries then escalate.
//	4 deepseek-r1 alone, leveling off                  — the ceiling.
//
// Arms 2 and 3 express "retry the same model, then escalate" as DUPLICATE LADDER
// RUNGS, which is forced by executor semantics: with a schema-less OutputPolicy
// the primary rung runs exactly once through ValidateAndRetry (the validator's
// retry loop is schema-driven, so TierPolicy.RetryBudget never engages), and every
// subsequent attempt is an escalation rung carrying the judge's reason plus the
// previous output. Three llama3.2 rungs therefore mean "one attempt plus two
// critique-driven retries of the same model".
//
// Run the full experiment with:
//
//	LOOM_LIVE_OLLAMA=1 LOOM_LEVELING_RESULTS_DIR=/tmp/sql-results \
//	  go test -tags fts5 -run TestLiveOllamaSQLGenLeveling ./pkg/orchestration/ -v -timeout 150m
//
// Smoke-test the harness itself with LOOM_LEVELING_N=3.
//
// Budget: a full N=30 run makes at most 30*(1+3+4+1) = 270 model calls. Most are
// llama3.2 at a few seconds each, but arms 3 and 4 reach deepseek-r1, which thinks
// for ~20-60s per call on this task, so expect the better part of an hour.

const (
	// Ladder models. Both are local; assertLocalOnlyModel refuses anything that
	// looks cloud-routed, on every name, before a socket is opened.
	sqWeakModel   = "llama3.2:latest"    // primary rung: modern, small, competent
	sqStrongModel = "deepseek-r1:latest" // escalation rung and arm 4's ceiling

	// Sampling is pinned so the Go run and the Python calibration probe measure
	// the same thing. Seed is explicit: every arm sees identical sampling for
	// identical prompts, so arm 2 and 3's rung-0 output should match arm 1's.
	sqTemperature = 0.1
	sqSeed        = int64(0)
	sqCallTimeout = 300 * time.Second

	// sqQuestionBudget bounds one whole trial — up to four model calls plus the
	// judge's SQL executions.
	sqQuestionBudget = 20 * time.Minute

	// Output budgets are PER MODEL, and this is not cosmetic. A reasoning model
	// spends its budget thinking before it writes any content: measured directly
	// against /api/chat, deepseek-r1 stops at done_reason="length" with an EMPTY
	// content field when capped at 1024, which would silently turn the strong rung
	// into a no-output rung. 3072 leaves room to think AND emit the query. A SQL
	// statement itself is short, so llama3.2 needs very little.
	sqWeakMaxTokens   = 1024
	sqStrongMaxTokens = 3072

	// sqJudgeTimeout bounds the judge's SQL execution. Same value as
	// sqModelQueryTimeout — the judge and the scorer must agree on what "executes"
	// means, or a query could pass the judge and be scored exec_error.
	sqJudgeTimeout = sqModelQueryTimeout

	// sqN is the default number of questions every arm runs, on the same indices
	// 0..N-1. LOOM_LEVELING_N overrides it (used for smoke runs of the harness
	// itself); the reported N always comes from sqQuestionCount, never this
	// constant. The env var name is shared with the other live harnesses.
	sqN = 30
)

// sqCritiqueTemplate is the escalation feedback template. A template is REQUIRED
// for the prior output to reach the escalation rung: buildRetryPromptWithOutput
// only substitutes {{previous_output}} when a FeedbackTemplate is set, and its
// default branch drops the previous output entirely. Without this, the retry rungs
// would receive the sqlite error but not the query that produced it, which is a
// different and much weaker experiment.
//
// {{error}} is filled with the judge's reason, which is sqlite's own error text
// (or sqNoSQLFound). It NEVER contains the reference answer — that is the whole
// reason the judge is answer-free by construction, and it is what keeps the retry
// rungs measuring repair rather than copying.
//
// No role framing, per project rules.
const sqCritiqueTemplate = `The previous SQL statement was executed against the database and failed.

Database error: {{error}}

The statement that failed was:
{{previous_output}}

Write a corrected SQLite SELECT statement that answers the same question. Reply with only the SQL statement in a ` + "```" + `sql fenced block. No explanation.`

// sqQuestionCount is the number of questions each arm runs: sqN unless the shared
// LOOM_LEVELING_N env var overrides it with a positive integer.
func sqQuestionCount() int {
	if v := os.Getenv(rzNEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return sqN
}

// ─────────────────────────── models and recording ───────────────────────────

// sqModel is one ladder rung's client plus its own call counter, so per-rung call
// counts are attributable rather than inferred.
type sqModel struct {
	provider string
	model    string
	client   *ollama.Client
	calls    int
}

// sqMaxTokensFor returns the output budget for a model. See the constants: a
// reasoning model needs enough room to think AND then answer.
func sqMaxTokensFor(model string) int {
	if model == sqStrongModel {
		return sqStrongMaxTokens
	}
	return sqWeakMaxTokens
}

func newSQModel(t *testing.T, endpoint, model string) *sqModel {
	t.Helper()
	assertLocalOnlyModel(t, model)
	seed := sqSeed
	return &sqModel{
		provider: "ollama",
		model:    model,
		client: ollama.NewClient(ollama.Config{
			Endpoint:    endpoint,
			Model:       model,
			Temperature: sqTemperature,
			MaxTokens:   sqMaxTokensFor(model),
			Timeout:     sqCallTimeout,
			Seed:        &seed,
		}),
	}
}

// sqAttempt is one model call inside one trial, scored independently of the
// executor's own verdict.
type sqAttempt struct {
	Rung    int     `json:"rung"`
	Model   string  `json:"model"`
	Output  string  `json:"output"`
	Seconds float64 `json:"seconds"`
	// Empty marks a reply with no content at all — for a reasoning model that
	// means the output budget was spent before it wrote a query.
	Empty bool `json:"empty"`
	sqOutcome
}

// sqTrial is one (arm, question) observation.
type sqTrial struct {
	Arm      string `json:"arm"`
	Index    int    `json:"index"`
	Family   int    `json:"family"`
	Question string `json:"question"`
	RefSQL   string `json:"reference_sql"`
	RefRows  string `json:"reference_result"`

	// Final scoring, computed OUTSIDE the executor so every arm — leveled or not
	// — is measured by the same yardstick.
	Final sqOutcome `json:"final"`

	Escalations  int         `json:"escalations"`
	JudgeCalls   int         `json:"judge_calls"`
	Calls        int         `json:"calls"`
	StrongCalls  int         `json:"strong_model_calls"`
	LevelingPass bool        `json:"leveling_passed"`
	ShortCircuit bool        `json:"short_circuited"`
	Seconds      float64     `json:"seconds"`
	Attempts     []sqAttempt `json:"attempts"`
	Err          string      `json:"error,omitempty"`
}

// sqJudgeStats counts execution-judge invocations, split by whether the query the
// judge examined was already correct. The split is the number that decides
// whether the judge is affordable: an invocation on an already-correct query is
// pure added latency on the happy path. Unlike an LLM judge this one is cheap
// (one sqlite query), and quantifying that is part of the point.
type sqJudgeStats struct {
	calls          int
	callsOnCorrect int
	callsOnWrong   int
	// passesOnWrong is the count that makes H1 concrete: the judge PASSED a query
	// that was actually wrong. Every one of these is an escalation that never
	// fired.
	passesOnWrong int
	seconds       float64
}

// sqArm runs one arm: a ladder of models under one leveling policy, over the same
// questions as every other arm.
//
// The runner holds the question currently in flight plus its reference result, so
// the rung closures and the judge can work against them. Every call the executor
// makes is sequential and on this goroutine, so plain fields are race-clean here.
type sqArm struct {
	name     string
	dbPath   string
	models   []*sqModel
	question sqQuestion
	ref      sqResult
	attempts []sqAttempt
	judge    sqJudgeStats
}

// ladder wraps each model as a LevelingRung that records and scores its own
// attempt. Scoring here is for the per-rung breakdown only; the trial's verdict
// comes from re-scoring the executor's chosen result.
func (a *sqArm) ladder() []LevelingRung {
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
				a.attempts = append(a.attempts, sqAttempt{
					Rung:      i,
					Model:     m.model,
					Output:    resp.Content,
					Seconds:   elapsed.Seconds(),
					Empty:     strings.TrimSpace(resp.Content) == "",
					sqOutcome: sqScore(ctx, a.dbPath, a.question, a.ref, resp.Content),
				})
				return &loomv1.AgentResult{
					AgentId:    "sq-harness",
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

// executionJudge is the arms' quality signal: parse the SQL, run it read-only
// against the fixture, pass if and only if it executes.
//
// Three properties make it the right signal to measure:
//
//   - It is ANSWER-FREE BY CONSTRUCTION. The reason it returns is sqlite's error
//     text or sqNoSQLFound, neither of which can mention the reference result. A
//     judge that leaked the answer would turn the retry rungs into a copying
//     exercise, exactly as an answer-naming critique would have in the reasoning
//     experiment.
//   - It costs no LLM call, so it reports cost 0 and never touches
//     LevelingPolicy.MaxCostUSD. Its real price is one sqlite query, counted in
//     sqJudgeStats.seconds.
//   - It is BLIND to correctness, which is the point. It passes every query that
//     runs, including the wrong ones, and passesOnWrong counts how often.
func (a *sqArm) executionJudge() LevelingJudge {
	return func(ctx context.Context, _ string, output string) (bool, string, float64, error) {
		start := time.Now()
		a.judge.calls++

		// Whether the output was ACTUALLY correct — used only for accounting, never
		// for the verdict. The judge must not be able to see this.
		truth := sqScore(ctx, a.dbPath, a.question, a.ref, output)
		if truth.Correct {
			a.judge.callsOnCorrect++
		} else {
			a.judge.callsOnWrong++
		}

		stmt, ok := sqParseSQL(output)
		if !ok {
			a.judge.seconds += time.Since(start).Seconds()
			return false, sqNoSQLFound, 0, nil
		}
		if _, err := sqExecuteSQL(ctx, a.dbPath, stmt, sqJudgeTimeout); err != nil {
			a.judge.seconds += time.Since(start).Seconds()
			return false, err.Error(), 0, nil
		}
		if !truth.Correct {
			a.judge.passesOnWrong++
		}
		a.judge.seconds += time.Since(start).Seconds()
		return true, "", 0, nil
	}
}

// run executes the arm over questions 0..n-1.
func (a *sqArm) run(t *testing.T, exec *LevelingExecutor, policy *loomv1.OutputPolicy,
	ds sqDataset, questions []sqQuestion, refs []sqResult,
) []sqTrial {
	t.Helper()
	trials := make([]sqTrial, 0, len(questions))
	ladder := a.ladder()

	for i, q := range questions {
		a.question = q
		a.ref = refs[i]
		a.attempts = nil
		before := make([]int, len(a.models))
		for k, m := range a.models {
			before[k] = m.calls
		}
		judgeBefore := a.judge.calls

		prompt := sqRenderPrompt(ds, q.Text)
		ctx, cancel := context.WithTimeout(context.Background(), sqQuestionBudget)
		start := time.Now()
		result, report, err := exec.Execute(ctx, policy, ladder, prompt,
			fmt.Sprintf("%s-q%d", a.name, q.Index))
		elapsed := time.Since(start)

		tr := sqTrial{
			Arm:        a.name,
			Index:      q.Index,
			Family:     q.Family,
			Question:   q.Text,
			RefSQL:     q.RefSQL,
			RefRows:    sqRenderResult(refs[i]),
			Seconds:    elapsed.Seconds(),
			Attempts:   append([]sqAttempt(nil), a.attempts...),
			JudgeCalls: a.judge.calls - judgeBefore,
		}
		for k, m := range a.models {
			delta := m.calls - before[k]
			tr.Calls += delta
			if m.model == sqStrongModel {
				tr.StrongCalls += delta
			}
		}
		if err != nil {
			tr.Err = err.Error()
		}
		if report != nil {
			tr.Escalations = report.Escalations
			tr.ShortCircuit = report.ShortCircuited
			tr.LevelingPass = report.Passed
		}
		if err == nil && result != nil {
			tr.Final = sqScore(ctx, a.dbPath, q, refs[i], result.Output)
		} else {
			tr.Final = sqOutcome{Category: sqCatExecError, ExecErr: "no result"}
		}
		cancel()
		trials = append(trials, tr)

		t.Logf("[%s] q=%-2d f%d %-12s %-12s calls=%d(r1=%d) esc=%d judge=%d %6.1fs  got=%s",
			a.name, q.Index, q.Family, sqFamilyNames[q.Family], tr.Final.Category,
			tr.Calls, tr.StrongCalls, tr.Escalations, tr.JudgeCalls, tr.Seconds,
			sqTruncate(tr.Final.SQL, 90))
		if tr.Final.Category != sqCatCorrect {
			t.Logf("[%s] q=%-2d ref=%s got=%s err=%s",
				a.name, q.Index, tr.RefRows, sqOrDash(tr.Final.Result), sqOrDash(tr.Final.ExecErr))
		}
		if tr.Err != "" {
			t.Logf("[%s] q=%-2d ERROR: %s", a.name, q.Index, tr.Err)
		}
	}
	return trials
}

func sqTruncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func sqOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ─────────────────────────── aggregation ───────────────────────────

type sqSummary struct {
	name             string
	n                int
	correct          int
	silentWrong      int
	execError        int
	parsed           int
	calls            int
	strongCalls      int
	escalations      int
	rungCalls        map[int]int
	errors           int
	emptyReplies     int
	judgeCalls       int
	judgeOnCorrect   int
	judgePassOnWrong int
	judgeSeconds     float64
	minS, medS, maxS float64
	totalS           float64
}

func sqSummarize(name string, trials []sqTrial, judge sqJudgeStats) sqSummary {
	s := sqSummary{name: name, n: len(trials), rungCalls: map[int]int{},
		judgeCalls: judge.calls, judgeOnCorrect: judge.callsOnCorrect,
		judgePassOnWrong: judge.passesOnWrong, judgeSeconds: judge.seconds}
	lat := make([]float64, 0, len(trials))
	for _, tr := range trials {
		switch tr.Final.Category {
		case sqCatCorrect:
			s.correct++
		case sqCatSilentWrong:
			s.silentWrong++
		default:
			s.execError++
		}
		if tr.Final.Parsed {
			s.parsed++
		}
		if tr.Err != "" {
			s.errors++
		}
		s.calls += tr.Calls
		s.strongCalls += tr.StrongCalls
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

func sqRungCallString(s sqSummary) string {
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

// TestLiveOllamaSQLGenLeveling is the four-arm SQL-generation measurement.
// Skipped by default; see the file header.
func TestLiveOllamaSQLGenLeveling(t *testing.T) {
	endpoint := requireLiveOllama(t, sqWeakModel, sqStrongModel)

	// Discovery → catalog bridge, without which TierOf("ollama", "llama3.2:latest")
	// is TierUnknown and every leveling arm short-circuits into arm 1.
	if err := factory.RegisterOllamaCatalogSource(endpoint); err != nil {
		t.Fatalf("registering discovered Ollama models: %v", err)
	}
	t.Cleanup(func() { catalog.Register(nil) })
	if got := catalog.TierOf("ollama", sqWeakModel); got != catalog.TierLocal {
		t.Fatalf("precondition failed: TierOf(ollama, %s) = %v, want %v — leveling would short-circuit",
			sqWeakModel, got, catalog.TierLocal)
	}

	// ── Fixture: one database, built once, on a read-write connection. Every
	// scored query afterwards opens its own read-only connection, so nothing a
	// model emits can change the data a later trial is scored against. ──
	ds := genSQDataset()
	dbPath := filepath.Join(t.TempDir(), "sqlgen.db")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := sqBuildDB(buildCtx, dbPath, ds); err != nil {
		buildCancel()
		t.Fatalf("building fixture database: %v", err)
	}
	n := sqQuestionCount()
	questions, refs, err := sqBuildQuestions(buildCtx, dbPath, n)
	buildCancel()
	if err != nil {
		t.Fatalf("building questions: %v", err)
	}
	t.Logf("fixture: %d customers, %d products, %d orders, %d order_items at %s",
		len(ds.Customers), len(ds.Products), len(ds.Orders), len(ds.OrderItems), dbPath)
	for i, q := range questions {
		t.Logf("q%-2d f%d %-12s salt=%d ordered=%t ref=%s | %s",
			q.Index, q.Family, sqFamilyNames[q.Family], q.Salt, q.Ordered,
			sqRenderResult(refs[i]), q.Text)
	}

	// ── The contract. No schema on purpose: with a schema present the executor
	// gives the schema the whole verdict and never consults the judge
	// (primaryVerdict/evaluate short-circuit on a non-empty schema), so an
	// execution-judged arm MUST run schema-less. MaxRetries 0 keeps the primary
	// rung to a single call; the template exists purely so each escalation prompt
	// carries the sqlite error AND the query that produced it. ──
	policy := &loomv1.OutputPolicy{
		RetryPolicy: &loomv1.OutputRetryPolicy{
			MaxRetries:       0,
			FeedbackTemplate: sqCritiqueTemplate,
		},
	}

	levelingOff := NewLevelingExecutor(nil, nil, nil, nil)

	runStart := time.Now()
	var allTrials []sqTrial
	var summaries []sqSummary

	// ── Arm 1: llama3.2 alone, leveling off. The floor, and the source of the
	// error-budget split H1 is about. ──
	arm1 := &sqArm{name: "1-llama3.2-off", dbPath: dbPath,
		models: []*sqModel{newSQModel(t, endpoint, sqWeakModel)}}
	tr1 := arm1.run(t, levelingOff, policy, ds, questions, refs)
	allTrials = append(allTrials, tr1...)
	summaries = append(summaries, sqSummarize(arm1.name, tr1, arm1.judge))

	// ── Arm 2: execution signal, retries only. Ladder [llama3.2 x3]: rung 0 is the
	// primary, rungs 1 and 2 are the SAME model re-run with the sqlite error and
	// its own failed query. No stronger model is ever paid for. ──
	arm2 := &sqArm{name: "2-exec-retries", dbPath: dbPath, models: []*sqModel{
		newSQModel(t, endpoint, sqWeakModel),
		newSQModel(t, endpoint, sqWeakModel),
		newSQModel(t, endpoint, sqWeakModel)}}
	tr2 := arm2.run(t, NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled: true, ShortCircuitMid: true, MaxEscalations: 2, Judge: arm2.executionJudge(),
	}, nil, nil), policy, ds, questions, refs)
	allTrials = append(allTrials, tr2...)
	summaries = append(summaries, sqSummarize(arm2.name, tr2, arm2.judge))

	// ── Arm 3: the full ladder. [llama3.2 x3 → deepseek-r1]: same two retries as
	// arm 2, and only a still-failing rung 2 pays for the strong model at rung 3. ──
	arm3 := &sqArm{name: "3-exec-ladder-r1", dbPath: dbPath, models: []*sqModel{
		newSQModel(t, endpoint, sqWeakModel),
		newSQModel(t, endpoint, sqWeakModel),
		newSQModel(t, endpoint, sqWeakModel),
		newSQModel(t, endpoint, sqStrongModel)}}
	tr3 := arm3.run(t, NewLevelingExecutor(nil, &LevelingPolicy{
		Enabled: true, ShortCircuitMid: true, MaxEscalations: 3, Judge: arm3.executionJudge(),
	}, nil, nil), policy, ds, questions, refs)
	allTrials = append(allTrials, tr3...)
	summaries = append(summaries, sqSummarize(arm3.name, tr3, arm3.judge))

	// ── Arm 4: deepseek-r1 alone, leveling off. The ceiling. ──
	arm4 := &sqArm{name: "4-r1-off", dbPath: dbPath,
		models: []*sqModel{newSQModel(t, endpoint, sqStrongModel)}}
	tr4 := arm4.run(t, levelingOff, policy, ds, questions, refs)
	allTrials = append(allTrials, tr4...)
	summaries = append(summaries, sqSummarize(arm4.name, tr4, arm4.judge))

	wall := time.Since(runStart)

	// ── Reporting ──
	sqReport(t, summaries)
	sqPerQuestion(t, [][]sqTrial{tr1, tr2, tr3, tr4},
		[]string{arm1.name, arm2.name, arm3.name, arm4.name}, questions)
	sqFamilyBreakdown(t, [][]sqTrial{tr1, tr2, tr3, tr4},
		[]string{arm1.name, arm2.name, arm3.name, arm4.name})
	sqInvisibilityFinding(t, tr1, tr2, tr3)
	sqLadderBreakdown(t, "2-exec-retries", tr2)
	sqLadderBreakdown(t, "3-exec-ladder-r1", tr3)
	sqJudgeCost(t, arm2, tr2)
	sqJudgeCost(t, arm3, tr3)
	sqCeilingGap(t, tr1, tr3, tr4)

	t.Logf("")
	t.Logf("total experiment wall-clock: %s (seed=%d, temp=%.1f, num_predict weak/strong=%d/%d, N=%d)",
		wall.Round(time.Second), sqSeed, sqTemperature, sqWeakMaxTokens, sqStrongMaxTokens, n)

	sqWriteResults(t, allTrials, questions, refs)
}

// sqReport prints the per-arm table: the three-way category split, the call
// budget, and latency.
func sqReport(t *testing.T, summaries []sqSummary) {
	t.Helper()
	t.Logf("")
	t.Logf("── SQL generation under leveling: N=%d identical questions/arm ──", sqQuestionCount())
	t.Logf("%-18s %9s %9s %9s %6s %6s %-18s %5s %6s %7s %7s %7s",
		"arm", "correct", "silent", "execerr", "calls", "r1", "calls-by-rung",
		"esc", "judge", "min", "med", "max")
	for _, s := range summaries {
		t.Logf("%-18s %4d/%-4d %4d/%-4d %4d/%-4d %6d %6d %-18s %5d %6d %6.1fs %6.1fs %6.1fs",
			s.name, s.correct, s.n, s.silentWrong, s.n, s.execError, s.n,
			s.calls, s.strongCalls, sqRungCallString(s), s.escalations, s.judgeCalls,
			s.minS, s.medS, s.maxS)
	}
	for _, s := range summaries {
		t.Logf("%-18s accuracy=%s parsed=%d/%d harness_errors=%d empty_replies=%d "+
			"total_model_time=%.1fs judge_sql_time=%.2fs",
			s.name, rzPct(s.correct, s.n), s.parsed, s.n, s.errors, s.emptyReplies,
			s.totalS, s.judgeSeconds)
	}
}

// sqPerQuestion prints the per-question comparison across arms: the whole point of
// running identical question sets is that arms can be compared row by row.
func sqPerQuestion(t *testing.T, arms [][]sqTrial, names []string, questions []sqQuestion) {
	t.Helper()
	t.Logf("")
	t.Logf("── per-question outcomes (C=correct, S=silent_wrong, E=exec_error; (eN)=escalations) ──")
	header := fmt.Sprintf("%-3s %-13s", "q", "family")
	for _, n := range names {
		header += fmt.Sprintf(" %-14s", n)
	}
	t.Logf("%s", header)
	for i := range questions {
		row := fmt.Sprintf("%-3d %-13s", questions[i].Index, sqFamilyNames[questions[i].Family])
		for _, arm := range arms {
			if i >= len(arm) {
				row += fmt.Sprintf(" %-14s", "-")
				continue
			}
			tr := arm[i]
			row += fmt.Sprintf(" %-14s", fmt.Sprintf("%s(e%d,c%d)",
				sqCatMark(tr.Final.Category), tr.Escalations, tr.Calls))
		}
		t.Logf("%s", row)
	}
}

func sqCatMark(category string) string {
	switch category {
	case sqCatCorrect:
		return "C"
	case sqCatSilentWrong:
		return "S"
	default:
		return "E"
	}
}

// sqFamilyBreakdown reports accuracy per question family. It answers the question
// a single accuracy number cannot: whether the weak model's failures are spread
// evenly or concentrated in the join-heavy families, which is what decides whether
// a leveling policy could be scoped by query shape at all.
func sqFamilyBreakdown(t *testing.T, arms [][]sqTrial, names []string) {
	t.Helper()
	t.Logf("")
	t.Logf("── accuracy by question family (correct/total, then S=silent E=execerr) ──")
	header := fmt.Sprintf("%-13s", "family")
	for _, n := range names {
		header += fmt.Sprintf(" %-18s", n)
	}
	t.Logf("%s", header)
	for f := 0; f < sqQuestionFamilies; f++ {
		row := fmt.Sprintf("%-13s", sqFamilyNames[f])
		for _, arm := range arms {
			total, correct, silent, execErr := 0, 0, 0, 0
			for _, tr := range arm {
				if tr.Family != f {
					continue
				}
				total++
				switch tr.Final.Category {
				case sqCatCorrect:
					correct++
				case sqCatSilentWrong:
					silent++
				default:
					execErr++
				}
			}
			row += fmt.Sprintf(" %-18s", fmt.Sprintf("%d/%d S%d E%d", correct, total, silent, execErr))
		}
		t.Logf("%s", row)
	}
}

// sqInvisibilityFinding is the central H1 measurement: how much of the weak
// model's error budget an execution signal can even see.
//
// The structural claim is that a silent_wrong first attempt passes the judge, so
// escalation never fires and arms 2 and 3 return the same wrong answer arm 1 did.
// This does not assert it — a harness that asserted its own hypothesis would be
// useless — it counts it, including any case where the ladder changed a
// silent_wrong outcome anyway (which would mean the judge rejected a query that
// executes, i.e. a harness bug worth knowing about).
func sqInvisibilityFinding(t *testing.T, tr1, tr2, tr3 []sqTrial) {
	t.Helper()
	byIndex := func(trials []sqTrial) map[int]sqTrial {
		m := make(map[int]sqTrial, len(trials))
		for _, tr := range trials {
			m[tr.Index] = tr
		}
		return m
	}
	a2, a3 := byIndex(tr2), byIndex(tr3)

	silent, execErr := 0, 0
	silentEsc2, silentEsc3, silentFixed2, silentFixed3 := 0, 0, 0, 0
	execFixed2, execFixed3 := 0, 0
	for _, tr := range tr1 {
		switch tr.Final.Category {
		case sqCatSilentWrong:
			silent++
			if t2, ok := a2[tr.Index]; ok {
				if t2.Escalations > 0 {
					silentEsc2++
				}
				if t2.Final.Correct {
					silentFixed2++
				}
			}
			if t3, ok := a3[tr.Index]; ok {
				if t3.Escalations > 0 {
					silentEsc3++
				}
				if t3.Final.Correct {
					silentFixed3++
				}
			}
		case sqCatExecError:
			execErr++
			if t2, ok := a2[tr.Index]; ok && t2.Final.Correct {
				execFixed2++
			}
			if t3, ok := a3[tr.Index]; ok && t3.Final.Correct {
				execFixed3++
			}
		}
	}

	total := silent + execErr
	t.Logf("")
	t.Logf("── H1: what an execution signal can see (arm 1's error budget) ──")
	t.Logf("arm 1 failures: %d of %d questions", total, len(tr1))
	t.Logf("  exec_error   (VISIBLE to the judge, escalatable):   %d  (%s of failures)",
		execErr, rzPct(execErr, total))
	t.Logf("  silent_wrong (INVISIBLE to the judge, structural):  %d  (%s of failures)",
		silent, rzPct(silent, total))
	t.Logf("of arm 1's exec_error questions, arm 2 (retries only) ended correct: %d/%d (%s)",
		execFixed2, execErr, rzPct(execFixed2, execErr))
	t.Logf("of arm 1's exec_error questions, arm 3 (ladder to r1) ended correct: %d/%d (%s)",
		execFixed3, execErr, rzPct(execFixed3, execErr))
	t.Logf("of arm 1's silent_wrong questions, arm 2 ended correct: %d, arm 3 ended correct: %d",
		silentFixed2, silentFixed3)
	t.Logf("escalations fired on one of arm 1's silent_wrong questions (expected 0 — the judge "+
		"passes an executable query): arm 2 = %d, arm 3 = %d", silentEsc2, silentEsc3)
	t.Logf("ceiling on any execution-signal ladder: %s accuracy is unreachable because "+
		"%d silent_wrong questions never trigger an escalation",
		rzPct(len(tr1)-silent, len(tr1)), silent)
}

// sqLadderBreakdown reports where a leveled arm's work actually went: how many
// trials reached each rung, and what each rung repaired. This is what separates
// "the ladder helped" from "the ladder ran".
func sqLadderBreakdown(t *testing.T, name string, trials []sqTrial) {
	t.Helper()
	reached := map[int]int{}
	fixedAt := map[int]int{}
	rung0Bad, endedCorrect := 0, 0
	for _, tr := range trials {
		var first *sqAttempt
		for i := range tr.Attempts {
			at := &tr.Attempts[i]
			reached[at.Rung]++
			if at.Rung == 0 {
				first = at
			}
		}
		if first == nil || first.Correct {
			continue
		}
		rung0Bad++
		if !tr.Final.Correct {
			continue
		}
		endedCorrect++
		// Attribute the fix to the last rung that ran, which is the rung whose
		// output the executor returned.
		if len(tr.Attempts) > 0 {
			fixedAt[tr.Attempts[len(tr.Attempts)-1].Rung]++
		}
	}

	rungs := make([]int, 0, len(reached))
	for k := range reached {
		rungs = append(rungs, k)
	}
	sort.Ints(rungs)

	t.Logf("")
	t.Logf("── [%s] ladder breakdown ──", name)
	for _, r := range rungs {
		t.Logf("rung %d ran on %d trials, and was the rung that produced a correct final answer %d times",
			r, reached[r], fixedAt[r])
	}
	t.Logf("wrong at rung 0: %d, of which the ladder ended correct: %d (%s)",
		rung0Bad, endedCorrect, rzPct(endedCorrect, rung0Bad))
}

// sqJudgeCost reports what the execution judge cost and, more importantly, how
// often it waved a wrong answer through. Unlike an LLM judge this one is nearly
// free, so the interesting number is not its price but its blindness.
func sqJudgeCost(t *testing.T, arm *sqArm, trials []sqTrial) {
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
	t.Logf("── [%s] judge accounting ──", arm.name)
	t.Logf("judge invocations: %d (on already-correct output: %d, on wrong output: %d)",
		arm.judge.calls, arm.judge.callsOnCorrect, arm.judge.callsOnWrong)
	t.Logf("false PASS (wrong query judged acceptable because it executes): %d of %d wrong outputs (%s)",
		arm.judge.passesOnWrong, arm.judge.callsOnWrong,
		rzPct(arm.judge.passesOnWrong, arm.judge.callsOnWrong))
	t.Logf("judge cost: %.2fs of sqlite execution over %d invocations (%.0fms each), zero LLM calls, zero USD",
		arm.judge.seconds, arm.judge.calls,
		1000*arm.judge.seconds/float64(arm.judge.calls))
	t.Logf("arm wall-clock: %.1fs over %d questions", modelTime, len(trials))
}

// sqCeilingGap puts the three numbers that matter side by side: the floor, what
// the ladder actually reached, and the ceiling it could not reach because the
// signal is blind to silent_wrong.
func sqCeilingGap(t *testing.T, tr1, tr3, tr4 []sqTrial) {
	t.Helper()
	count := func(trials []sqTrial) int {
		n := 0
		for _, tr := range trials {
			if tr.Final.Correct {
				n++
			}
		}
		return n
	}
	floor, ladder, ceiling := count(tr1), count(tr3), count(tr4)

	// How many questions the strong model got right that the ladder got wrong —
	// accuracy the ladder had available at rung 3 and never asked for.
	byIndex := make(map[int]sqTrial, len(tr3))
	for _, tr := range tr3 {
		byIndex[tr.Index] = tr
	}
	leftOnTable, wouldHaveNeededEsc := 0, 0
	for _, tr := range tr4 {
		l, ok := byIndex[tr.Index]
		if !ok || !tr.Final.Correct || l.Final.Correct {
			continue
		}
		leftOnTable++
		if l.StrongCalls == 0 {
			wouldHaveNeededEsc++
		}
	}

	t.Logf("")
	t.Logf("── accuracy: floor, ladder, ceiling (N=%d) ──", len(tr1))
	t.Logf("arm 1 llama3.2 alone:      %d/%d (%s)", floor, len(tr1), rzPct(floor, len(tr1)))
	t.Logf("arm 3 ladder to r1:        %d/%d (%s)", ladder, len(tr3), rzPct(ladder, len(tr3)))
	t.Logf("arm 4 r1 alone (ceiling):  %d/%d (%s)", ceiling, len(tr4), rzPct(ceiling, len(tr4)))
	t.Logf("questions r1 got right that the ladder got wrong: %d, of which %d never reached the r1 rung "+
		"at all (the judge had already passed the weak model's query)", leftOnTable, wouldHaveNeededEsc)
}

// ─────────────────────────── per-trial artifacts ───────────────────────────

// sqWriteResults writes per-trial JSONL when LOOM_LEVELING_RESULTS_DIR is set. A
// write failure is logged, never fatal: the measurement already happened and
// t.Log carries every row.
func sqWriteResults(t *testing.T, trials []sqTrial, questions []sqQuestion, refs []sqResult) {
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
	write("sql_arms.jsonl", rows)

	// The question set goes out too: the reference SQL and its result are what any
	// re-analysis has to score against, and they are derived from the generated
	// database rather than hardcoded.
	qrows := make([]any, 0, len(questions))
	for i, q := range questions {
		qrows = append(qrows, struct {
			sqQuestion
			Reference string `json:"reference_result"`
			Rows      int    `json:"reference_rows"`
		}{sqQuestion: q, Reference: sqRenderResult(refs[i]), Rows: len(refs[i].Rows)})
	}
	write("sql_questions.jsonl", qrows)
}
