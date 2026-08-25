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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// lvlErrLLM is an LLMProvider whose every Chat fails. It exists to drive the
// error paths that only a failing transport can reach.
type lvlErrLLM struct {
	provider string
	model    string
	err      error
}

func newLvlErrLLM(provider, model string, err error) *lvlErrLLM {
	return &lvlErrLLM{provider: provider, model: model, err: err}
}

func (m *lvlErrLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return nil, m.err
}

func (m *lvlErrLLM) Name() string  { return m.provider }
func (m *lvlErrLLM) Model() string { return m.model }

// TestValidateLevelingLadderShape covers the agent-free shape check the config
// loaders run. A nil rung is reported by its 1-based position because that is
// how the operator counts the YAML list, and the message must match
// resolveLevelingLadder's wording so a load-time and an execution-time failure
// read identically.
func TestValidateLevelingLadderShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rungs   []*loomv1.LevelingRung
		wantErr string
	}{
		{name: "nil ladder is valid", rungs: nil},
		{name: "empty ladder is valid", rungs: []*loomv1.LevelingRung{}},
		{
			name:  "provider only is valid",
			rungs: []*loomv1.LevelingRung{{Provider: "ollama"}},
		},
		{
			name:  "role only is valid",
			rungs: []*loomv1.LevelingRung{{Role: loomv1.LLMRole_LLM_ROLE_JUDGE}},
		},
		{
			name:    "nil rung is rejected",
			rungs:   []*loomv1.LevelingRung{nil},
			wantErr: "leveling ladder: rung 1 is nil",
		},
		{
			name:    "nil rung is reported by its position",
			rungs:   []*loomv1.LevelingRung{{Provider: "ollama"}, nil},
			wantErr: "leveling ladder: rung 2 is nil",
		},
		{
			name:    "model without role or provider is rejected",
			rungs:   []*loomv1.LevelingRung{{Model: "deepseek-r1:latest"}},
			wantErr: "leveling ladder: rung 1 needs role or provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateLevelingLadderShape(tt.rungs)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

// TestResolveLevelingLadderNilRung pins that resolveLevelingLadder re-checks the
// nil-rung condition rather than trusting a config loader to have done it — it
// also serves callers that build rungs in Go and never went through one.
func TestResolveLevelingLadderNilRung(t *testing.T) {
	t.Parallel()

	main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
	strong := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("nil-rung-agent"))
	require.NoError(t, ag.SetProviderPool(map[string]agent.LLMProvider{lvlFrontierProvider: strong}, "", nil))
	primary := LevelingRung{Provider: lvlLowProvider, Model: lvlLowModel}

	ladder, err := resolveLevelingLadder(ag, "nil-rung-agent", primary,
		[]*loomv1.LevelingRung{{Provider: lvlFrontierProvider}, nil})

	require.Error(t, err)
	assert.Nil(t, ladder)
	assert.Contains(t, err.Error(), "rung 2 is nil",
		"a resolvable rung 1 must not mask the nil rung behind it")
	assert.Equal(t, 0, strong.count(), "no rung is called during resolution")
}

// TestResolveLevelingLadderRoleWithNoLLM covers the role-lookup failure: a role
// with no LLM of its own must fail the resolve with the role and the agent
// named, rather than silently becoming a rung that escalates to the primary's
// own model — a paid call that cannot improve anything.
func TestResolveLevelingLadderRoleWithNoLLM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent func() *agent.Agent
	}{
		{
			// The case the strict lookup exists for: a perfectly normal agent
			// whose JUDGE role was never configured. GetLLMForRole answers this
			// with the main LLM, which is why the ladder cannot use it.
			name: "role unset on an agent that has a main LLM",
			agent: func() *agent.Agent {
				main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")
				return agent.NewAgent(&mockBackend{}, main, agent.WithName("llm-less-agent"))
			},
		},
		{
			name: "agent with no LLM at all",
			agent: func() *agent.Agent {
				return agent.NewAgent(&mockBackend{}, nil, agent.WithName("llm-less-agent"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ag := tt.agent()
			_, ok := ag.GetLLMForRoleStrict(loomv1.LLMRole_LLM_ROLE_JUDGE)
			require.False(t, ok, "precondition: no LLM is configured for the judge role")

			primary := LevelingRung{Provider: lvlLowProvider, Model: lvlLowModel}
			ladder, err := resolveLevelingLadder(ag, "llm-less-agent", primary,
				[]*loomv1.LevelingRung{{Role: loomv1.LLMRole_LLM_ROLE_JUDGE}})

			require.Error(t, err)
			assert.Nil(t, ladder)
			assert.Contains(t, err.Error(), "rung 1 role LLM_ROLE_JUDGE has no LLM configured")
			assert.Contains(t, err.Error(), "llm-less-agent")
		})
	}
}

// TestLevelingRungExecuteSurfacesLLMError pins the rung ExecuteFunc's error
// wrapping: the provider/model is named so a failing escalation can be attributed
// without reading the ladder, and the underlying error is wrapped rather than
// replaced.
func TestLevelingRungExecuteSurfacesLLMError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("ollama refused the connection")
	failing := newLvlErrLLM(lvlLowProvider, "deepseek-r1:latest", wantErr)
	main := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, "primary out")

	ag := agent.NewAgent(&mockBackend{}, main, agent.WithName("failing-rung-agent"))
	require.NoError(t, ag.SetProviderPool(map[string]agent.LLMProvider{"strong": failing}, "", nil))

	primary := LevelingRung{Provider: lvlLowProvider, Model: lvlLowModel}
	ladder, err := resolveLevelingLadder(ag, "failing-rung-agent", primary,
		[]*loomv1.LevelingRung{{Provider: "strong", Model: "deepseek-r1:latest"}})
	require.NoError(t, err, "resolution succeeds; only the call fails")
	require.Len(t, ladder, 2)

	result, execErr := ladder[1].Execute(context.Background(), "ignored-session", "escalate this")
	require.Error(t, execErr)
	assert.Nil(t, result)
	assert.ErrorIs(t, execErr, wantErr, "the provider error is wrapped, not swallowed")
	assert.Contains(t, execErr.Error(), "leveling rung strong/deepseek-r1:latest failed")
	assert.Equal(t, 0, main.count(), "a failing rung must not fall back to the main LLM")
}

// TestBackfillLevelingResultMetadata covers the metadata merge that keeps
// downstream consumers of the executor's own keys working no matter which rung
// produced the winning result.
func TestBackfillLevelingResultMetadata(t *testing.T) {
	t.Parallel()

	t.Run("nil result is a no-op", func(t *testing.T) {
		t.Parallel()
		// The point is that this does not panic: the executor calls this on a
		// result it did not necessarily obtain.
		backfillLevelingResultMetadata(nil, map[string]string{"stage": "1"})
	})

	t.Run("empty base is a no-op", func(t *testing.T) {
		t.Parallel()
		result := &loomv1.AgentResult{Output: "x"}
		backfillLevelingResultMetadata(result, nil)
		assert.Nil(t, result.Metadata, "no base keys means no map is allocated")

		backfillLevelingResultMetadata(result, map[string]string{})
		assert.Nil(t, result.Metadata)
	})

	t.Run("nil metadata map is created", func(t *testing.T) {
		t.Parallel()
		result := &loomv1.AgentResult{Output: "x"}
		backfillLevelingResultMetadata(result, map[string]string{"stage": "1", "agent_name": "worker"})
		require.NotNil(t, result.Metadata)
		assert.Equal(t, "1", result.Metadata["stage"])
		assert.Equal(t, "worker", result.Metadata["agent_name"])
	})

	t.Run("existing keys are never overwritten", func(t *testing.T) {
		t.Parallel()
		result := &loomv1.AgentResult{
			Metadata: map[string]string{
				levelingRungProviderKey: lvlFrontierProvider,
				"stage":                 "already-set",
			},
		}
		backfillLevelingResultMetadata(result, map[string]string{
			"stage":      "1",
			"agent_name": "worker",
		})
		assert.Equal(t, "already-set", result.Metadata["stage"],
			"the rung's own labeling wins over the backfill")
		assert.Equal(t, "worker", result.Metadata["agent_name"])
		assert.Equal(t, lvlFrontierProvider, result.Metadata[levelingRungProviderKey])
	})
}
