// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Catalog identities used to drive real tier resolution.
const (
	lvlLowProvider = "ollama"
	lvlLowModel    = "llama3.2"

	lvlFrontierProvider = "anthropic"
	lvlFrontierModel    = "claude-opus-4-7"

	lvlMidProvider = "anthropic"
	lvlMidModel    = "claude-haiku-4-5-20251001"

	lvlUnknownProvider = "nope"
	lvlUnknownModel    = "nope"
)

const lvlSchema = `{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`

const (
	lvlValidJSON   = `{"answer":"ok"}`
	lvlInvalidJSON = `sorry, I cannot produce that`
	lvlFencedJSON  = "Here is the result:\n```json\n{\"answer\":\"ok\"}\n```\nHope that helps."
)

// mockRung is a scripted ExecuteFunc with call/prompt capture. The counter is
// atomic so -race stays clean regardless of how the executor sequences calls.
type mockRung struct {
	calls atomic.Int32

	mu       sync.Mutex
	prompts  []string
	sessions []string

	outputs []string // per-call outputs; the last entry repeats
	cost    float64
	err     error
}

func newMockRung(cost float64, outputs ...string) *mockRung {
	return &mockRung{outputs: outputs, cost: cost}
}

func newFailingRung(err error) *mockRung {
	return &mockRung{err: err}
}

func (m *mockRung) execute(_ context.Context, sessionID, prompt string) (*loomv1.AgentResult, error) {
	n := int(m.calls.Add(1))

	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.sessions = append(m.sessions, sessionID)
	m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	out := ""
	if len(m.outputs) > 0 {
		idx := n - 1
		if idx >= len(m.outputs) {
			idx = len(m.outputs) - 1
		}
		out = m.outputs[idx]
	}
	return &loomv1.AgentResult{
		Output: out,
		Cost:   &loomv1.AgentExecutionCost{CostUsd: m.cost},
	}, nil
}

func (m *mockRung) count() int {
	return int(m.calls.Load())
}

func (m *mockRung) promptAt(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.prompts) {
		return ""
	}
	return m.prompts[i]
}

func (m *mockRung) sessionAt(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.sessions) {
		return ""
	}
	return m.sessions[i]
}

// mockJudge is a scripted LevelingJudge with a call counter.
type mockJudge struct {
	calls   atomic.Int32
	verdict []bool // per-call pass verdict; the last entry repeats
	reason  string
	cost    float64
	err     error
}

func newMockJudge(cost float64, verdicts ...bool) *mockJudge {
	return &mockJudge{verdict: verdicts, cost: cost, reason: "answer field missing meaning"}
}

func (j *mockJudge) judge(_ context.Context, _, _ string) (bool, string, float64, error) {
	n := int(j.calls.Add(1))
	if j.err != nil {
		return false, "", 0, j.err
	}
	pass := true
	if len(j.verdict) > 0 {
		idx := n - 1
		if idx >= len(j.verdict) {
			idx = len(j.verdict) - 1
		}
		pass = j.verdict[idx]
	}
	reason := ""
	if !pass {
		reason = j.reason
	}
	return pass, reason, j.cost, nil
}

func (j *mockJudge) count() int {
	return int(j.calls.Load())
}

func newTestLevelingExecutor(t *testing.T, policy *LevelingPolicy) *LevelingExecutor {
	t.Helper()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()
	return NewLevelingExecutor(NewOutputValidator(tracer, logger), policy, tracer, logger)
}

func schemaPolicy(maxRetries int32, explicitRetry bool) *loomv1.OutputPolicy {
	p := &loomv1.OutputPolicy{OutputSchema: lvlSchema}
	if explicitRetry {
		p.RetryPolicy = &loomv1.OutputRetryPolicy{MaxRetries: maxRetries}
	}
	return p
}

func TestLevelingDisabledIsInert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy *LevelingPolicy
	}{
		{name: "nil policy", policy: nil},
		{name: "zero value disabled", policy: &LevelingPolicy{}},
		{name: "disabled but otherwise configured", policy: &LevelingPolicy{
			Enabled:        false,
			MaxEscalations: 3,
			MaxCostUSD:     100,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			judge := newMockJudge(0.01, false)
			policy := tt.policy
			if policy != nil {
				policy.Judge = judge.judge
			}

			rung0 := newMockRung(0.02, lvlInvalidJSON)
			rung1 := newMockRung(0.50, lvlValidJSON)
			exec := newTestLevelingExecutor(t, policy)

			result, report, err := exec.Execute(
				context.Background(),
				schemaPolicy(0, false),
				[]LevelingRung{
					{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
					{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
				},
				"do the thing", "wf-inert")

			require.NoError(t, err)
			assert.Nil(t, report, "disabled leveling must report nothing")
			require.NotNil(t, result)
			assert.Equal(t, lvlInvalidJSON, result.Output, "result must pass through untouched")
			assert.Equal(t, 1, rung0.count(), "exactly one execute call")
			assert.Equal(t, 0, rung1.count(), "escalation rung must never run")
			assert.Equal(t, 0, judge.count(), "judge must never run")
		})
	}

	t.Run("matches direct ValidateAndRetry", func(t *testing.T) {
		t.Parallel()

		logger := zaptest.NewLogger(t)
		tracer := observability.NewNoOpTracer()
		validator := NewOutputValidator(tracer, logger)

		// Direct validator call as the reference behavior.
		refRung := newMockRung(0.02, lvlInvalidJSON)
		refResult, refWarnings, refErr := validator.ValidateAndRetry(
			context.Background(), schemaPolicy(1, true), refRung.execute, nil, "do the thing", "wf-ref")
		require.NoError(t, refErr)
		require.NotEmpty(t, refWarnings, "reference call surfaces validation warnings")

		// Same inputs through a disabled leveling executor.
		lvlRung := newMockRung(0.02, lvlInvalidJSON)
		exec := NewLevelingExecutor(validator, &LevelingPolicy{Enabled: false}, tracer, logger)
		lvlResult, report, lvlErr := exec.Execute(
			context.Background(), schemaPolicy(1, true),
			[]LevelingRung{{Provider: lvlLowProvider, Model: lvlLowModel, Execute: lvlRung.execute}},
			"do the thing", "wf-ref")

		require.NoError(t, lvlErr)
		assert.Nil(t, report)
		require.NotNil(t, lvlResult)
		assert.Equal(t, refResult.Output, lvlResult.Output)
		assert.Equal(t, refRung.count(), lvlRung.count(), "identical retry behavior")
		// Prompt sequence must match attempt for attempt.
		for i := 0; i < refRung.count(); i++ {
			assert.Equal(t, refRung.promptAt(i), lvlRung.promptAt(i), "prompt %d", i)
			assert.Equal(t, refRung.sessionAt(i), lvlRung.sessionAt(i), "session %d", i)
		}
	})
}

func TestLevelingFrontierShortCircuits(t *testing.T) {
	t.Parallel()

	judge := newMockJudge(0.01, false)
	rung0 := newMockRung(0.40, lvlInvalidJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
		Judge:          judge.judge,
	})

	result, report, err := exec.Execute(
		context.Background(),
		schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-frontier")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, catalog.TierFrontier, report.Tier)
	assert.True(t, report.ShortCircuited)
	assert.Equal(t, 0, report.Escalations)
	assert.Equal(t, 0, report.JudgeCalls)
	assert.Equal(t, 0, judge.count(), "judge must never run on the short-circuit path")
	assert.Equal(t, 1, rung0.count())
	assert.Equal(t, 0, rung1.count())
	assert.False(t, report.Passed, "free schema re-check still reports the failure")
	assert.InDelta(t, 0.40, report.TotalCostUSD, 1e-9)
	require.NotNil(t, result)
	assert.Equal(t, lvlInvalidJSON, result.Output)
}

func TestLevelingMidShortCircuitConfigurable(t *testing.T) {
	t.Parallel()

	t.Run("short circuit enabled", func(t *testing.T) {
		t.Parallel()

		judge := newMockJudge(0.01, false)
		rung0 := newMockRung(0.05, lvlInvalidJSON)
		rung1 := newMockRung(0.90, lvlValidJSON)

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:         true,
			ShortCircuitMid: true,
			MaxEscalations:  1,
			Judge:           judge.judge,
		})

		_, report, err := exec.Execute(
			context.Background(), schemaPolicy(0, false),
			[]LevelingRung{
				{Provider: lvlMidProvider, Model: lvlMidModel, Execute: rung0.execute},
				{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
			},
			"do the thing", "wf-mid-on")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, catalog.TierMid, report.Tier)
		assert.True(t, report.ShortCircuited)
		assert.Equal(t, 1, rung0.count())
		assert.Equal(t, 0, rung1.count())
		assert.Equal(t, 0, judge.count())
	})

	t.Run("short circuit disabled takes active path", func(t *testing.T) {
		t.Parallel()

		judge := newMockJudge(0.01, false)
		rung0 := newMockRung(0.05, lvlValidJSON)
		rung1 := newMockRung(0.90, lvlValidJSON)

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:         true,
			ShortCircuitMid: false,
			MaxEscalations:  1,
			Judge:           judge.judge,
		})

		result, report, err := exec.Execute(
			context.Background(), schemaPolicy(0, false),
			[]LevelingRung{
				{Provider: lvlMidProvider, Model: lvlMidModel, Execute: rung0.execute},
				{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
			},
			"do the thing", "wf-mid-off")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, catalog.TierMid, report.Tier)
		assert.False(t, report.ShortCircuited)
		assert.True(t, report.Passed)
		assert.Equal(t, 1, rung0.count(), "valid first output needs no retry")
		assert.Equal(t, 0, rung1.count())
		assert.Equal(t, 0, report.Escalations)
		assert.Equal(t, 0, report.JudgeCalls)
		assert.Equal(t, 0, judge.count(), "schema is a free signal, judge stays idle")
		assert.InDelta(t, 0.05, report.TotalCostUSD, 1e-9)
		require.NotNil(t, result)
		assert.Equal(t, lvlValidJSON, result.Output)
	})
}

func TestLevelingUnknownTierShortCircuits(t *testing.T) {
	t.Parallel()

	judge := newMockJudge(0.01, false)
	rung0 := newMockRung(0.03, lvlInvalidJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
		Judge:          judge.judge,
	})

	_, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlUnknownProvider, Model: lvlUnknownModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-unknown")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, catalog.TierUnknown, report.Tier)
	assert.True(t, report.ShortCircuited)
	assert.Equal(t, 1, rung0.count())
	assert.Equal(t, 0, rung1.count())
	assert.Equal(t, 0, judge.count())
}

func TestLevelingLowTierFirstPassFree(t *testing.T) {
	t.Parallel()

	judge := newMockJudge(0.01, false)
	rung0 := newMockRung(0.02, lvlValidJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
		Judge:          judge.judge,
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-low-pass")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, catalog.TierLocal, report.Tier)
	assert.False(t, report.ShortCircuited)
	assert.True(t, report.Passed)
	assert.Equal(t, 1, rung0.count())
	assert.Equal(t, 0, rung1.count())
	assert.Equal(t, 0, report.Escalations)
	assert.Equal(t, 0, report.JudgeCalls)
	assert.Equal(t, 0, judge.count())
	assert.False(t, report.CoercionApplied)
	assert.InDelta(t, 0.02, report.TotalCostUSD, 1e-9)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output)
}

func TestLevelingCoercionBeatsEscalation(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.02, lvlFencedJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	// Explicit zero-retry policy so the count isolates coercion from the
	// tier retry budget.
	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-coerce")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, report.CoercionApplied)
	assert.True(t, report.Passed)
	assert.Equal(t, 0, report.Escalations)
	assert.Equal(t, 1, rung0.count())
	assert.Equal(t, 0, rung1.count(), "free coercion must beat a paid escalation")
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output, "output rewritten to the extracted JSON")
	assert.InDelta(t, 0.02, report.TotalCostUSD, 1e-9)
}

func TestLevelingEscalatesOnlyOnFailure(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.02, lvlInvalidJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	// nil RetryPolicy: the local tier budget (2) applies, so rung0 runs three
	// times before the ladder moves on.
	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"summarize the ledger", "wf-escalate")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 3, rung0.count(), "tier retry budget exhausted on the primary")
	assert.Equal(t, 1, rung1.count(), "escalation rung runs exactly once")
	assert.Equal(t, 1, report.Escalations)
	assert.True(t, report.Passed)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output)
	assert.InDelta(t, 3*0.02+0.90, report.TotalCostUSD, 1e-9)

	escPrompt := rung1.promptAt(0)
	assert.Contains(t, escPrompt, "summarize the ledger", "escalation carries the original prompt")
	assert.Contains(t, escPrompt, "not valid JSON", "escalation carries the failure reason")
	assert.Equal(t, "wf-escalate-lvl1", rung1.sessionAt(0))
}

func TestLevelingJudgeOnlyWhenNoFreeSignal(t *testing.T) {
	t.Parallel()

	t.Run("no schema means judge owns the verdict", func(t *testing.T) {
		t.Parallel()

		judge := newMockJudge(0.01, true)
		rung0 := newMockRung(0.02, "a prose answer")

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:        true,
			MaxEscalations: 1,
			Judge:          judge.judge,
		})

		_, report, err := exec.Execute(
			context.Background(),
			&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
			[]LevelingRung{{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute}},
			"do the thing", "wf-judge-a")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, 1, judge.count())
		assert.Equal(t, 1, report.JudgeCalls)
		assert.True(t, report.Passed)
		assert.Equal(t, 0, report.Escalations)
		assert.InDelta(t, 0.03, report.TotalCostUSD, 1e-9, "judge spend counts toward the total")
	})

	t.Run("schema failure does not consult the judge", func(t *testing.T) {
		t.Parallel()

		judge := newMockJudge(0.01, true)
		rung0 := newMockRung(0.02, lvlInvalidJSON)

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:        true,
			MaxEscalations: 1,
			Judge:          judge.judge,
		})

		_, report, err := exec.Execute(
			context.Background(), schemaPolicy(0, true),
			[]LevelingRung{{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute}},
			"do the thing", "wf-judge-b")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, 0, judge.count(), "free signal owns the verdict")
		assert.Equal(t, 0, report.JudgeCalls)
		assert.False(t, report.Passed)
		assert.Equal(t, 1, rung0.count())
	})

	t.Run("no schema and no judge does nothing extra", func(t *testing.T) {
		t.Parallel()

		rung0 := newMockRung(0.02, "a prose answer")

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:        true,
			MaxEscalations: 1,
		})

		result, report, err := exec.Execute(
			context.Background(),
			&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
			[]LevelingRung{{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute}},
			"do the thing", "wf-judge-c")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, 1, rung0.count())
		assert.Equal(t, 0, report.JudgeCalls)
		assert.Equal(t, 0, report.Escalations)
		assert.True(t, report.Passed)
		assert.InDelta(t, 0.02, report.TotalCostUSD, 1e-9)
		require.NotNil(t, result)
		assert.Equal(t, "a prose answer", result.Output)
	})
}

func TestLevelingJudgeFailTriggersEscalation(t *testing.T) {
	t.Parallel()

	judge := newMockJudge(0.01, false, true)
	rung0 := newMockRung(0.02, "a weak answer")
	rung1 := newMockRung(0.90, "a strong answer")

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
		Judge:          judge.judge,
	})

	result, report, err := exec.Execute(
		context.Background(),
		&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-judge-esc")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 2, judge.count())
	assert.Equal(t, 2, report.JudgeCalls)
	assert.Equal(t, 1, report.Escalations)
	assert.True(t, report.Passed)
	assert.Equal(t, 1, rung1.count())
	require.NotNil(t, result)
	assert.Equal(t, "a strong answer", result.Output)
	assert.Contains(t, rung1.promptAt(0), judge.reason, "judge reason feeds the escalation")
}

func TestLevelingBudgetCapsEscalation(t *testing.T) {
	t.Parallel()

	t.Run("escalation blocked by ceiling", func(t *testing.T) {
		t.Parallel()

		rung0 := newMockRung(0.10, lvlInvalidJSON)
		rung1 := newMockRung(0.10, lvlValidJSON)

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:        true,
			MaxEscalations: 1,
			MaxCostUSD:     0.05,
		})

		result, report, err := exec.Execute(
			context.Background(), schemaPolicy(0, true),
			[]LevelingRung{
				{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
				{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
			},
			"do the thing", "wf-budget-a")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.True(t, report.BudgetExhausted)
		assert.False(t, report.Passed)
		assert.Equal(t, 0, report.Escalations)
		assert.Equal(t, 1, rung0.count())
		assert.Equal(t, 0, rung1.count(), "ceiling reached, no paid rung runs")
		assert.InDelta(t, 0.10, report.TotalCostUSD, 1e-9)
		require.NotNil(t, result)
		assert.Equal(t, lvlInvalidJSON, result.Output, "best result still returned")
	})

	t.Run("judge skipped by ceiling", func(t *testing.T) {
		t.Parallel()

		judge := newMockJudge(0.01, false)
		rung0 := newMockRung(0.10, "a prose answer")
		rung1 := newMockRung(0.10, "a better answer")

		exec := newTestLevelingExecutor(t, &LevelingPolicy{
			Enabled:        true,
			MaxEscalations: 1,
			MaxCostUSD:     0.05,
			Judge:          judge.judge,
		})

		_, report, err := exec.Execute(
			context.Background(),
			&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
			[]LevelingRung{
				{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
				{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
			},
			"do the thing", "wf-budget-b")

		require.NoError(t, err)
		require.NotNil(t, report)
		assert.Equal(t, 0, judge.count(), "judge skipped under an exhausted budget")
		assert.Equal(t, 0, report.JudgeCalls)
		assert.True(t, report.BudgetExhausted)
		assert.Equal(t, 0, report.Escalations)
		assert.Equal(t, 0, rung1.count())
	})
}

func TestLevelingMaxEscalationsRespected(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.01, lvlInvalidJSON)
	rung1 := newMockRung(0.05, lvlInvalidJSON)
	rung2 := newMockRung(0.90, lvlInvalidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	_, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlMidProvider, Model: lvlMidModel, Execute: rung1.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung2.execute},
		},
		"do the thing", "wf-maxesc")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, report.Escalations)
	assert.Equal(t, 3, rung0.count(), "primary uses its tier retry budget")
	assert.Equal(t, 1, rung1.count())
	assert.Equal(t, 0, rung2.count(), "third rung is beyond MaxEscalations")
	assert.Equal(t, 4, rung0.count()+rung1.count()+rung2.count())
	assert.False(t, report.Passed)
	assert.NotEmpty(t, report.Warnings)
}

func TestLevelingRetryBudgetFromTierPolicy(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.01, lvlInvalidJSON)
	callerPolicy := schemaPolicy(0, false)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	_, report, err := exec.Execute(
		context.Background(), callerPolicy,
		[]LevelingRung{{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute}},
		"do the thing", "wf-retrybudget")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 3, rung0.count(), "local RetryBudget=2 means one attempt plus two retries")
	assert.Nil(t, callerPolicy.RetryPolicy, "caller's proto must not be mutated")
	assert.Equal(t, lvlSchema, callerPolicy.OutputSchema)
	assert.False(t, report.Passed)
	assert.Equal(t, 0, report.Escalations, "single-rung ladder has nowhere to escalate")
}

func TestLevelingJudgeErrorFailsOpen(t *testing.T) {
	t.Parallel()

	judge := &mockJudge{err: errors.New("judge backend unreachable")}
	rung0 := newMockRung(0.02, "a prose answer")
	rung1 := newMockRung(0.90, "a better answer")

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
		Judge:          judge.judge,
	})

	result, report, err := exec.Execute(
		context.Background(),
		&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-judge-err")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, judge.count())
	assert.Equal(t, 1, report.JudgeCalls)
	assert.True(t, report.Passed, "a broken judge must not degrade the result")
	assert.Equal(t, 0, report.Escalations)
	assert.Equal(t, 0, rung1.count())
	require.NotNil(t, result)
	assert.Equal(t, "a prose answer", result.Output)

	found := false
	for _, w := range report.Warnings {
		if strings.Contains(w, "judge backend unreachable") {
			found = true
			break
		}
	}
	assert.True(t, found, "judge error recorded in warnings: %v", report.Warnings)
}

func TestLevelingEmptyLadderErrors(t *testing.T) {
	t.Parallel()

	exec := newTestLevelingExecutor(t, &LevelingPolicy{Enabled: true})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, false), nil, "do the thing", "wf-empty")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Nil(t, report)
	assert.Contains(t, err.Error(), "ladder is empty")
}

func TestLevelingRungExecutionErrorContinues(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.01, lvlInvalidJSON)
	rung1 := newFailingRung(errors.New("rung1 upstream 503"))

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 2,
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-rung-err")

	require.NoError(t, err, "a failing rung is a warning, not a hard error")
	require.NotNil(t, report)
	assert.Equal(t, 1, rung1.count())
	assert.False(t, report.Passed)
	require.NotNil(t, result)
	assert.Equal(t, lvlInvalidJSON, result.Output, "best available result is returned")

	found := false
	for _, w := range report.Warnings {
		if strings.Contains(w, "rung1 upstream 503") {
			found = true
			break
		}
	}
	assert.True(t, found, "rung failure recorded: %v", report.Warnings)
}

func TestDefaultLevelingPolicyIsDisabled(t *testing.T) {
	t.Parallel()

	p := DefaultLevelingPolicy()
	require.NotNil(t, p)
	assert.False(t, p.Enabled, "leveling must default to off")
	assert.True(t, p.ShortCircuitMid)
	assert.Equal(t, 1, p.MaxEscalations)
	assert.Nil(t, p.Judge)
	assert.Zero(t, p.MaxCostUSD)
}
