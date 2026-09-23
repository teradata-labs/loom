// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// newTracedLevelingExecutor builds an executor whose spans are captured, so a
// test can assert on what the instrumentation actually recorded.
func newTracedLevelingExecutor(
	t *testing.T,
	policy *LevelingPolicy,
) (*LevelingExecutor, *observability.MockTracer) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	tracer := observability.NewMockTracer()
	return NewLevelingExecutor(NewOutputValidator(tracer, logger), policy, tracer, logger), tracer
}

// TestLevelingJudgeCallIsTraced pins a span on the judge. The judge is an LLM
// call, and an LLM call without a span is invisible to the trace that is
// supposed to account for a workflow's spend — the judge ran with a bare
// context and reported nothing but a cost delta on the parent.
func TestLevelingJudgeCallIsTraced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		judge      *mockJudge
		rungCount  int
		wantSpans  int
		wantErrMsg string
	}{
		{
			name:      "one span for the primary's verdict",
			judge:     newMockJudge(0.01, true),
			rungCount: 1,
			wantSpans: 1,
		},
		{
			// The primary is rejected, so the rung's output is judged too: one
			// span per judge call, not one per Execute.
			name:      "one span per rung evaluated",
			judge:     newMockJudge(0.01, false, true),
			rungCount: 2,
			wantSpans: 2,
		},
		{
			name:       "a judge error lands on its span",
			judge:      &mockJudge{err: errors.New("judge backend unreachable")},
			rungCount:  1,
			wantSpans:  1,
			wantErrMsg: "judge backend unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exec, tracer := newTracedLevelingExecutor(t, &LevelingPolicy{
				Enabled:        true,
				MaxEscalations: 1,
				Judge:          tt.judge.judge,
			})

			ladder := []LevelingRung{
				{Provider: lvlLowProvider, Model: lvlLowModel,
					Execute: newMockRung(0.02, "a prose answer").execute},
			}
			if tt.rungCount > 1 {
				ladder = append(ladder, LevelingRung{
					Provider: lvlFrontierProvider, Model: lvlFrontierModel,
					Execute: newMockRung(0.90, "a better answer").execute,
				})
			}

			// No schema: the judge is the only signal, so it is consulted.
			_, report, err := exec.Execute(
				context.Background(),
				&loomv1.OutputPolicy{AcceptanceCriteria: "answers the question"},
				ladder, "do the thing", "wf-judge-span")

			require.NoError(t, err)
			require.NotNil(t, report)
			assert.Equal(t, tt.wantSpans, report.JudgeCalls, "judge calls")

			spans := tracer.GetSpansByName("leveling.judge")
			require.Len(t, spans, tt.wantSpans, "one span per judge call")

			parent := tracer.GetSpanByName("leveling.execute")
			require.NotNil(t, parent)

			for i, span := range spans {
				assert.Equal(t, parent.SpanID, span.ParentID,
					"judge span %d nests under leveling.execute", i)
				assert.Contains(t, span.Attributes, "leveling.judge.pass", "span %d", i)
				assert.Contains(t, span.Attributes, "leveling.judge.cost_usd", "span %d", i)
				assert.Contains(t, span.Attributes, "leveling.judge.reason", "span %d", i)
				if tt.wantErrMsg == "" {
					assert.NotContains(t, span.Attributes, "leveling.judge.error", "span %d", i)
					continue
				}
				assert.Equal(t, tt.wantErrMsg, span.Attributes["leveling.judge.error"], "span %d", i)
			}
		})
	}
}

// TestLevelingRungTierDiagnostics pins per-rung tier resolution. Only the
// primary's tier was ever computed, so a ladder wired to escalate into a weaker
// model looked identical in the trace to one wired correctly.
//
// A rung below the primary is reported, not rejected: the tiers come from
// catalog pricing, which is the wrong authority to fail a run on, and the
// ladder's shape is validated at the config surface instead. Equal tiers are
// deliberately silent — a same-tier rung carrying a different signal (a
// critique pass, a different prompt) is a working pattern, and the SQL
// experiment's measured gain came from exactly that.
func TestLevelingRungTierDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rungProvider string
		rungModel    string
		wantRungTier catalog.ModelTier
		wantWarning  bool
	}{
		{
			name:         "rung below the primary warns",
			rungProvider: lvlLowProvider,
			rungModel:    lvlLowModel,
			wantRungTier: catalog.TierLocal,
			wantWarning:  true,
		},
		{
			name:         "rung at the primary's tier stays silent",
			rungProvider: lvlMidProvider,
			rungModel:    lvlMidModel,
			wantRungTier: catalog.TierMid,
		},
		{
			name:         "rung above the primary stays silent",
			rungProvider: lvlFrontierProvider,
			rungModel:    lvlFrontierModel,
			wantRungTier: catalog.TierFrontier,
		},
		{
			name:         "unclassified rung stays silent",
			rungProvider: lvlUnknownProvider,
			rungModel:    lvlUnknownModel,
			wantRungTier: catalog.TierUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primary := newMockRung(0.05, lvlInvalidJSON)
			rung := newMockRung(0.30, lvlInvalidJSON)

			// A mid primary with mid short-circuiting off is the active path
			// with a tier above local, so a rung can sit below it. The zero
			// retry budget keeps the primary to one attempt.
			exec, tracer := newTracedLevelingExecutor(t, &LevelingPolicy{
				Enabled:         true,
				ShortCircuitMid: false,
				MaxEscalations:  1,
				TierPolicies:    map[catalog.ModelTier]TierPolicy{catalog.TierMid: {RetryBudget: 0}},
			})

			_, report, err := exec.Execute(
				context.Background(), schemaPolicy(0, false),
				[]LevelingRung{
					{Provider: lvlMidProvider, Model: lvlMidModel, Execute: primary.execute},
					{Provider: tt.rungProvider, Model: tt.rungModel, Execute: rung.execute},
				},
				"do the thing", "wf-rung-tier")

			require.NoError(t, err)
			require.NotNil(t, report)
			assert.Equal(t, catalog.TierMid, report.Tier)
			assert.Equal(t, 1, primary.count())
			assert.Equal(t, 1, rung.count(), "the rung is attempted, so its tier is resolved")
			assert.Equal(t, 1, report.Escalations)

			span := tracer.GetSpanByName("leveling.execute")
			require.NotNil(t, span)
			assert.Equal(t, tt.wantRungTier.String(), span.Attributes["leveling.rung.1.tier"])

			const downward = "the ladder escalates downward"
			assert.Equal(t, tt.wantWarning, lvlAnyWarningContains(report.Warnings, downward),
				"downward-ladder warning: %v", report.Warnings)
			if tt.wantWarning {
				assert.True(t, lvlAnyWarningContains(report.Warnings,
					"escalation rung 1 (ollama/llama3.2) is tier local, below the primary's tier mid"),
					"warning names both tiers: %v", report.Warnings)
			}
		})
	}
}

// TestLevelingRungCallIsTraced pins a span on every escalation rung. Rung calls
// are the one part of a leveled stage that spends money on a second model, and
// they ran with a bare context: every primary attempt produced a
// pipeline.agent.* span while the rung that replaced it produced nothing. The
// span carries what a trace reader needs to attribute the spend — provider,
// model, agent, session, cost and tokens — and the error when the call fails.
func TestLevelingRungCallIsTraced(t *testing.T) {
	t.Parallel()

	const sessionID = "wf-rung-span-lvl1"

	tests := []struct {
		name    string
		rungLLM agent.LLMProvider
		wantErr string
	}{
		{
			name:    "successful rung records identity, cost and tokens",
			rungLLM: newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.75, lvlValidJSON),
		},
		{
			name:    "failing rung records the error",
			rungLLM: newLvlErrLLM(lvlFrontierProvider, lvlFrontierModel, errors.New("frontier unreachable")),
			wantErr: "frontier unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tracer := observability.NewMockTracer()
			main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
			ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("traced-agent"))
			require.NoError(t, ag.SetProviderPool(
				map[string]agent.LLMProvider{lvlFrontierProvider: tt.rungLLM}, "", nil))

			ladder, err := resolveLevelingLadder(ag, "traced-agent",
				LevelingRung{Provider: lvlLowProvider, Model: lvlLowModel},
				[]*loomv1.LevelingRung{{Provider: lvlFrontierProvider, Model: lvlFrontierModel}},
				tracer)
			require.NoError(t, err)
			require.Len(t, ladder, 2)

			parentCtx, parent := tracer.StartSpan(context.Background(), "test.parent")
			result, execErr := ladder[1].Execute(parentCtx, sessionID, "escalate this")
			tracer.EndSpan(parent)

			spans := tracer.GetSpansByName("leveling.rung")
			require.Len(t, spans, 1, "exactly one span per rung call")
			span := spans[0]
			assert.Equal(t, parent.SpanID, span.ParentID, "the rung span nests under the caller's span")
			assert.Equal(t, lvlFrontierProvider, span.Attributes["leveling.rung.provider"])
			assert.Equal(t, lvlFrontierModel, span.Attributes["leveling.rung.model"])
			assert.Equal(t, "traced-agent", span.Attributes["agent.id"])
			assert.Equal(t, sessionID, span.Attributes["agent.session_id"],
				"the validator's session id is what joins the rung to the rest of the stage")
			assert.False(t, span.EndTime.IsZero(), "the span is ended even when the call fails")

			if tt.wantErr != "" {
				require.Error(t, execErr)
				assert.Nil(t, result)
				assert.Equal(t, tt.wantErr, span.Attributes["error"])
				assert.NotContains(t, span.Attributes, "llm.cost_usd", "no cost is recorded for a call that returned nothing")
				return
			}

			require.NoError(t, execErr)
			require.NotNil(t, result)
			assert.Equal(t, "0.750000", span.Attributes["llm.cost_usd"])
			assert.Equal(t, "10", span.Attributes["llm.input_tokens"])
			assert.Equal(t, "20", span.Attributes["llm.output_tokens"])
			assert.Contains(t, span.Attributes, "llm.duration_ms")
			assert.NotContains(t, span.Attributes, "error")
		})
	}
}

// TestLevelingRungNilTracerIsNoOp pins that a rung resolved without a tracer
// still executes: the nil is replaced by a no-op tracer rather than
// dereferenced, so Go callers that never wired observability keep working.
func TestLevelingRungNilTracerIsNoOp(t *testing.T) {
	t.Parallel()

	main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
	strong := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.5, lvlValidJSON)
	ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("untraced-agent"))
	require.NoError(t, ag.SetProviderPool(map[string]agent.LLMProvider{lvlFrontierProvider: strong}, "", nil))

	ladder, err := resolveLevelingLadder(ag, "untraced-agent",
		LevelingRung{Provider: lvlLowProvider, Model: lvlLowModel},
		[]*loomv1.LevelingRung{{Provider: lvlFrontierProvider, Model: lvlFrontierModel}},
		nil)
	require.NoError(t, err)

	result, execErr := ladder[1].Execute(context.Background(), "s", "escalate this")
	require.NoError(t, execErr)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output)
	assert.Equal(t, 1, strong.count())
}
