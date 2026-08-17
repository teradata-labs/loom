// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// lvlMockLLM is an LLMProvider whose Name()/Model() are configurable, so the
// real catalog resolves it to a chosen tier, and whose Chat calls are counted.
type lvlMockLLM struct {
	provider string
	model    string
	cost     float64

	mu      sync.Mutex
	calls   int
	prompts []string
	outputs []string // per-call outputs; the last entry repeats
}

func newLvlMockLLM(provider, model string, cost float64, outputs ...string) *lvlMockLLM {
	return &lvlMockLLM{provider: provider, model: model, cost: cost, outputs: outputs}
}

func (m *lvlMockLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	if len(messages) > 0 {
		m.prompts = append(m.prompts, messages[len(messages)-1].Content)
	}
	m.mu.Unlock()

	out := ""
	if len(m.outputs) > 0 {
		idx := n - 1
		if idx >= len(m.outputs) {
			idx = len(m.outputs) - 1
		}
		out = m.outputs[idx]
	}
	return &llmtypes.LLMResponse{
		Content:    out,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			CostUSD:      m.cost,
		},
	}, nil
}

func (m *lvlMockLLM) Name() string  { return m.provider }
func (m *lvlMockLLM) Model() string { return m.model }

func (m *lvlMockLLM) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *lvlMockLLM) promptAt(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.prompts) {
		return ""
	}
	return m.prompts[i]
}

func TestLevelingPolicyFromProto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     *loomv1.LevelingPolicy
		verify func(t *testing.T, got *LevelingPolicy)
	}{
		{
			name: "nil proto means no policy",
			in:   nil,
			verify: func(t *testing.T, got *LevelingPolicy) {
				assert.Nil(t, got)
			},
		},
		{
			name: "enabled false yields a disabled policy",
			in: &loomv1.LevelingPolicy{
				Enabled:        false,
				MaxEscalations: proto.Int32(-5), // never validated on this path
				MaxCostUsd:     -1,
				TierPolicies:   map[string]*loomv1.LevelingTierPolicy{"bogus-tier": {}},
			},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				assert.False(t, got.Enabled)
				assert.Zero(t, got.MaxEscalations)
				assert.Zero(t, got.MaxCostUSD)
				assert.Nil(t, got.TierPolicies)
				assert.Nil(t, got.Judge)
			},
		},
		{
			name: "full policy round trip",
			in: &loomv1.LevelingPolicy{
				Enabled:                  true,
				ShortCircuitMid:          proto.Bool(false),
				MaxEscalations:           proto.Int32(3),
				MaxCostUsd:               1.25,
				FrontierMinOutputCostUsd: 25,
				MidMinOutputCostUsd:      2.5,
				TierPolicies: map[string]*loomv1.LevelingTierPolicy{
					"local": {RetryBudget: 4, AggressiveCoercion: true},
					"mid":   {RetryBudget: 0, AggressiveCoercion: false},
				},
			},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				assert.True(t, got.Enabled)
				assert.False(t, got.ShortCircuitMid)
				assert.Equal(t, 3, got.MaxEscalations)
				assert.InDelta(t, 1.25, got.MaxCostUSD, 1e-9)
				assert.InDelta(t, 25.0, got.Thresholds.FrontierMinOutputCostUSD, 1e-9)
				assert.InDelta(t, 2.5, got.Thresholds.MidMinOutputCostUSD, 1e-9)
				require.Len(t, got.TierPolicies, 2)
				assert.Equal(t,
					TierPolicy{RetryBudget: 4, AggressiveCoercion: true},
					got.TierPolicies[catalog.TierLocal])
				assert.Equal(t, TierPolicy{}, got.TierPolicies[catalog.TierMid])
				assert.Nil(t, got.Judge, "the proto surface carries no judge")
			},
		},
		{
			name: "absent optionals take the executor defaults",
			in:   &loomv1.LevelingPolicy{Enabled: true},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				assert.True(t, got.ShortCircuitMid, "absent short_circuit_mid defaults to true")
				assert.Equal(t, 1, got.MaxEscalations, "absent max_escalations defaults to 1")
				assert.Zero(t, got.MaxCostUSD)
				assert.Equal(t, catalog.TierThresholds{}, got.Thresholds,
					"absent thresholds stay zero and resolve to catalog defaults downstream")
			},
		},
		{
			name: "explicit zero max_escalations disables escalation",
			in: &loomv1.LevelingPolicy{
				Enabled:        true,
				MaxEscalations: proto.Int32(0),
			},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				assert.Equal(t, 0, got.MaxEscalations)
			},
		},
		{
			name: "explicit true short_circuit_mid",
			in: &loomv1.LevelingPolicy{
				Enabled:         true,
				ShortCircuitMid: proto.Bool(true),
			},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				assert.True(t, got.ShortCircuitMid)
			},
		},
		{
			name: "every tier name parses",
			in: &loomv1.LevelingPolicy{
				Enabled: true,
				TierPolicies: map[string]*loomv1.LevelingTierPolicy{
					"unknown":    {RetryBudget: 1},
					"local":      {RetryBudget: 2},
					"small-open": {RetryBudget: 3},
					"mid":        {RetryBudget: 4},
					"frontier":   {RetryBudget: 5},
				},
			},
			verify: func(t *testing.T, got *LevelingPolicy) {
				require.NotNil(t, got)
				require.Len(t, got.TierPolicies, 5)
				assert.Equal(t, 1, got.TierPolicies[catalog.TierUnknown].RetryBudget)
				assert.Equal(t, 2, got.TierPolicies[catalog.TierLocal].RetryBudget)
				assert.Equal(t, 3, got.TierPolicies[catalog.TierSmallOpen].RetryBudget)
				assert.Equal(t, 4, got.TierPolicies[catalog.TierMid].RetryBudget)
				assert.Equal(t, 5, got.TierPolicies[catalog.TierFrontier].RetryBudget)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := LevelingPolicyFromProto(tt.in)
			require.NoError(t, err)
			tt.verify(t, got)
		})
	}
}

func TestLevelingPolicyFromProtoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      *loomv1.LevelingPolicy
		wantMsg []string
	}{
		{
			name: "unknown tier name",
			in: &loomv1.LevelingPolicy{
				Enabled:      true,
				TierPolicies: map[string]*loomv1.LevelingTierPolicy{"Frontier": {}},
			},
			wantMsg: []string{"Frontier", "unknown", "local", "small-open", "mid", "frontier"},
		},
		{
			name: "negative max_escalations",
			in: &loomv1.LevelingPolicy{
				Enabled:        true,
				MaxEscalations: proto.Int32(-1),
			},
			wantMsg: []string{"max_escalations"},
		},
		{
			name: "negative max_cost_usd",
			in: &loomv1.LevelingPolicy{
				Enabled:    true,
				MaxCostUsd: -0.01,
			},
			wantMsg: []string{"max_cost_usd"},
		},
		{
			name: "negative retry_budget",
			in: &loomv1.LevelingPolicy{
				Enabled:      true,
				TierPolicies: map[string]*loomv1.LevelingTierPolicy{"local": {RetryBudget: -2}},
			},
			wantMsg: []string{"retry_budget", "local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := LevelingPolicyFromProto(tt.in)
			require.Error(t, err)
			assert.Nil(t, got)
			for _, want := range tt.wantMsg {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestResolveLevelingLadder(t *testing.T) {
	t.Parallel()

	primary := LevelingRung{
		Provider: lvlLowProvider,
		Model:    lvlLowModel,
		Execute: func(context.Context, string, string) (*loomv1.AgentResult, error) {
			return &loomv1.AgentResult{Output: "primary"}, nil
		},
	}

	t.Run("role based rung resolves through GetLLMForRole", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		judge := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.5, lvlValidJSON)
		ag := agent.NewAgent(&mockBackend{}, main,
			agent.WithName("role-agent"),
			agent.WithJudgeLLM(judge))

		ladder, err := resolveLevelingLadder(ag, "role-agent", primary, []*loomv1.LevelingRung{
			{Role: loomv1.LLMRole_LLM_ROLE_JUDGE},
		})
		require.NoError(t, err)
		require.Len(t, ladder, 2)
		assert.Equal(t, lvlFrontierProvider, ladder[1].Provider, "provider falls back to the resolved LLM")
		assert.Equal(t, lvlFrontierModel, ladder[1].Model, "model falls back to the resolved LLM")
		require.NotNil(t, ladder[1].Execute)
		assert.Nil(t, ladder[1].Feedback, "escalation rungs have no session to continue")

		result, err := ladder[1].Execute(context.Background(), "ignored-session", "escalate this")
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, lvlValidJSON, result.Output)
		assert.Equal(t, "role-agent", result.AgentId, "escalated spend attributes to the same agent")
		assert.InDelta(t, 0.5, result.Cost.GetCostUsd(), 1e-9, "provider cost flows into the result")
		assert.Equal(t, lvlFrontierProvider, result.Metadata[levelingRungProviderKey])
		assert.Equal(t, lvlFrontierModel, result.Metadata[levelingRungModelKey])
		assert.Equal(t, 1, judge.count())
		assert.Equal(t, "escalate this", judge.promptAt(0))
		assert.Equal(t, 0, main.count(), "resolving a rung must not touch the main LLM")
	})

	t.Run("provider based rung resolves through the pool", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		strong := newLvlMockLLM("pool-name", "pool-model", 0.4, lvlValidJSON)
		ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("pool-agent"))
		require.NoError(t, ag.SetProviderPool(map[string]agent.LLMProvider{lvlFrontierProvider: strong}, "", nil))

		ladder, err := resolveLevelingLadder(ag, "pool-agent", primary, []*loomv1.LevelingRung{
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel},
		})
		require.NoError(t, err)
		require.Len(t, ladder, 2)
		assert.Equal(t, lvlFrontierProvider, ladder[1].Provider, "explicit proto provider wins")
		assert.Equal(t, lvlFrontierModel, ladder[1].Model, "explicit proto model wins")

		_, err = ladder[1].Execute(context.Background(), "ignored-session", "escalate this")
		require.NoError(t, err)
		assert.Equal(t, 1, strong.count())
	})

	t.Run("provider missing from the pool errors", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("pool-agent"))
		require.NoError(t, ag.SetProviderPool(map[string]agent.LLMProvider{"other": main}, "", nil))

		ladder, err := resolveLevelingLadder(ag, "pool-agent", primary, []*loomv1.LevelingRung{
			{Provider: "absent-provider"},
		})
		require.Error(t, err)
		assert.Nil(t, ladder)
		assert.Contains(t, err.Error(), "absent-provider")
		assert.Contains(t, err.Error(), "provider pool")
	})

	t.Run("no provider pool at all errors", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("bare-agent"))

		ladder, err := resolveLevelingLadder(ag, "bare-agent", primary, []*loomv1.LevelingRung{
			{Provider: lvlFrontierProvider},
		})
		require.Error(t, err)
		assert.Nil(t, ladder)
		assert.Contains(t, err.Error(), "provider pool")
	})

	t.Run("rung with neither role nor provider errors", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("bare-agent"))

		ladder, err := resolveLevelingLadder(ag, "bare-agent", primary, []*loomv1.LevelingRung{{}})
		require.Error(t, err)
		assert.Nil(t, ladder)
		assert.Contains(t, err.Error(), "needs role or provider")
	})

	t.Run("empty proto ladder yields the primary alone", func(t *testing.T) {
		t.Parallel()

		main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
		ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("bare-agent"))

		ladder, err := resolveLevelingLadder(ag, "bare-agent", primary, nil)
		require.NoError(t, err)
		require.Len(t, ladder, 1)
		assert.Equal(t, lvlLowProvider, ladder[0].Provider)
	})

	t.Run("nil agent errors", func(t *testing.T) {
		t.Parallel()

		ladder, err := resolveLevelingLadder(nil, "x", primary, nil)
		require.Error(t, err)
		assert.Nil(t, ladder)
	})
}

func TestEffectiveLevelingOutputPolicy(t *testing.T) {
	t.Parallel()

	unified := &loomv1.OutputPolicy{OutputSchema: lvlSchema}

	tests := []struct {
		name   string
		stage  *loomv1.PipelineStage
		verify func(t *testing.T, got *loomv1.OutputPolicy)
	}{
		{
			name:  "unified policy wins",
			stage: &loomv1.PipelineStage{OutputPolicy: unified, OutputSchema: `{"type":"array"}`},
			verify: func(t *testing.T, got *loomv1.OutputPolicy) {
				assert.Same(t, unified, got)
			},
		},
		{
			name:  "legacy schema is synthesized",
			stage: &loomv1.PipelineStage{OutputSchema: lvlSchema},
			verify: func(t *testing.T, got *loomv1.OutputPolicy) {
				require.NotNil(t, got)
				assert.Equal(t, lvlSchema, got.OutputSchema)
				assert.Nil(t, got.RetryPolicy)
			},
		},
		{
			name:  "legacy retry policy is synthesized",
			stage: &loomv1.PipelineStage{RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 2}},
			verify: func(t *testing.T, got *loomv1.OutputPolicy) {
				require.NotNil(t, got)
				assert.Empty(t, got.OutputSchema)
				require.NotNil(t, got.RetryPolicy)
				assert.Equal(t, int32(2), got.RetryPolicy.MaxRetries)
			},
		},
		{
			name:  "no contract at all",
			stage: &loomv1.PipelineStage{AgentId: "a"},
			verify: func(t *testing.T, got *loomv1.OutputPolicy) {
				assert.Nil(t, got)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.verify(t, effectiveLevelingOutputPolicy(tt.stage))
		})
	}
}
