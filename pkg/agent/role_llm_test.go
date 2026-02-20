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
package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// roleMockLLM is a configurable mock LLM provider for role-specific testing.
// Unlike the simple mockLLMProvider in registry_test.go, this mock supports
// configurable name/model to distinguish different role-specific providers.
type roleMockLLM struct {
	providerName string
	modelName    string
}

func (m *roleMockLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{
		Content:    "Mock response from " + m.providerName,
		StopReason: "end_turn",
	}, nil
}

func (m *roleMockLLM) Name() string  { return m.providerName }
func (m *roleMockLLM) Model() string { return m.modelName }

// newRoleMockLLM creates a mock LLM provider with the given name and model.
func newRoleMockLLM(name, model string) *roleMockLLM {
	return &roleMockLLM{providerName: name, modelName: model}
}

// createRoleTestAgent creates a minimal Agent for role LLM testing.
// It uses NewAgent to ensure all internal state (tools, memory, config) is
// properly initialized, then applies optional WithXxxLLM options.
func createRoleTestAgent(mainLLM LLMProvider, opts ...Option) *Agent {
	return NewAgent(nil, mainLLM, opts...)
}

// ---------------------------------------------------------------------------
// TestGetLLMForRole_FallbackChain
// ---------------------------------------------------------------------------

func TestGetLLMForRole_FallbackChain(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")
	orchestratorLLM := newRoleMockLLM("orchestrator", "orchestrator-model")
	classifierLLM := newRoleMockLLM("classifier", "classifier-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	tests := []struct {
		name     string
		opts     []Option
		role     loomv1.LLMRole
		wantName string
		wantDesc string
	}{
		// --- No role LLMs set: every role falls back to main ---
		{
			name:     "no role LLMs / AGENT returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_AGENT,
			wantName: "main",
			wantDesc: "AGENT role with no overrides should return main LLM",
		},
		{
			name:     "no role LLMs / UNSPECIFIED returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_UNSPECIFIED,
			wantName: "main",
			wantDesc: "UNSPECIFIED role with no overrides should return main LLM",
		},
		{
			name:     "no role LLMs / JUDGE returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantName: "main",
			wantDesc: "JUDGE role with no overrides should fall back to main LLM",
		},
		{
			name:     "no role LLMs / ORCHESTRATOR returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			wantName: "main",
			wantDesc: "ORCHESTRATOR role with no overrides should fall back to main LLM",
		},
		{
			name:     "no role LLMs / CLASSIFIER returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantName: "main",
			wantDesc: "CLASSIFIER role with no overrides should fall back to main LLM",
		},
		{
			name:     "no role LLMs / COMPRESSOR returns main",
			opts:     nil,
			role:     loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			wantName: "main",
			wantDesc: "COMPRESSOR role with no overrides should fall back to main LLM",
		},

		// --- Single role LLM set: that role returns specific, others still main ---
		{
			name:     "only judge set / JUDGE returns judge",
			opts:     []Option{WithJudgeLLM(judgeLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantName: "judge",
			wantDesc: "JUDGE role with judge override should return judge LLM",
		},
		{
			name:     "only judge set / ORCHESTRATOR returns main",
			opts:     []Option{WithJudgeLLM(judgeLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			wantName: "main",
			wantDesc: "ORCHESTRATOR role with only judge override should fall back to main",
		},
		{
			name:     "only judge set / CLASSIFIER returns main",
			opts:     []Option{WithJudgeLLM(judgeLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantName: "main",
			wantDesc: "CLASSIFIER role with only judge override should fall back to main",
		},
		{
			name:     "only judge set / COMPRESSOR returns main",
			opts:     []Option{WithJudgeLLM(judgeLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			wantName: "main",
			wantDesc: "COMPRESSOR role with only judge override should fall back to main",
		},
		{
			name:     "only judge set / AGENT returns main",
			opts:     []Option{WithJudgeLLM(judgeLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_AGENT,
			wantName: "main",
			wantDesc: "AGENT role with only judge override should return main",
		},
		{
			name:     "only orchestrator set / ORCHESTRATOR returns orchestrator",
			opts:     []Option{WithOrchestratorLLM(orchestratorLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			wantName: "orchestrator",
			wantDesc: "ORCHESTRATOR role with orchestrator override should return orchestrator LLM",
		},
		{
			name:     "only classifier set / CLASSIFIER returns classifier",
			opts:     []Option{WithClassifierLLM(classifierLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantName: "classifier",
			wantDesc: "CLASSIFIER role with classifier override should return classifier LLM",
		},
		{
			name:     "only compressor set / COMPRESSOR returns compressor",
			opts:     []Option{WithCompressorLLM(compressorLLM)},
			role:     loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			wantName: "compressor",
			wantDesc: "COMPRESSOR role with compressor override should return compressor LLM",
		},

		// --- All role LLMs set: each returns its specific provider ---
		{
			name: "all set / JUDGE returns judge",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantName: "judge",
			wantDesc: "With all roles set, JUDGE should return judge-specific LLM",
		},
		{
			name: "all set / ORCHESTRATOR returns orchestrator",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			wantName: "orchestrator",
			wantDesc: "With all roles set, ORCHESTRATOR should return orchestrator-specific LLM",
		},
		{
			name: "all set / CLASSIFIER returns classifier",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantName: "classifier",
			wantDesc: "With all roles set, CLASSIFIER should return classifier-specific LLM",
		},
		{
			name: "all set / COMPRESSOR returns compressor",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			wantName: "compressor",
			wantDesc: "With all roles set, COMPRESSOR should return compressor-specific LLM",
		},
		{
			name: "all set / AGENT still returns main",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_AGENT,
			wantName: "main",
			wantDesc: "With all roles set, AGENT should still return main LLM",
		},
		{
			name: "all set / UNSPECIFIED still returns main",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			role:     loomv1.LLMRole_LLM_ROLE_UNSPECIFIED,
			wantName: "main",
			wantDesc: "With all roles set, UNSPECIFIED should still return main LLM",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM, tt.opts...)
			got := ag.GetLLMForRole(tt.role)
			require.NotNil(t, got, tt.wantDesc)
			assert.Equal(t, tt.wantName, got.Name(), tt.wantDesc)
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetLLMModelForRole / TestGetLLMProviderNameForRole
// ---------------------------------------------------------------------------

func TestGetLLMModelForRole(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "gpt-4")
	judgeLLM := newRoleMockLLM("judge", "claude-3-haiku")

	tests := []struct {
		name      string
		opts      []Option
		role      loomv1.LLMRole
		wantModel string
	}{
		{
			name:      "AGENT returns main model",
			opts:      nil,
			role:      loomv1.LLMRole_LLM_ROLE_AGENT,
			wantModel: "gpt-4",
		},
		{
			name:      "JUDGE with override returns judge model",
			opts:      []Option{WithJudgeLLM(judgeLLM)},
			role:      loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantModel: "claude-3-haiku",
		},
		{
			name:      "JUDGE without override returns main model",
			opts:      nil,
			role:      loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantModel: "gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM, tt.opts...)
			assert.Equal(t, tt.wantModel, ag.GetLLMModelForRole(tt.role))
		})
	}
}

func TestGetLLMProviderNameForRole(t *testing.T) {
	mainLLM := newRoleMockLLM("anthropic", "claude-3-opus")
	classifierLLM := newRoleMockLLM("ollama", "llama-3")

	tests := []struct {
		name         string
		opts         []Option
		role         loomv1.LLMRole
		wantProvider string
	}{
		{
			name:         "AGENT returns main provider name",
			opts:         nil,
			role:         loomv1.LLMRole_LLM_ROLE_AGENT,
			wantProvider: "anthropic",
		},
		{
			name:         "CLASSIFIER with override returns classifier provider",
			opts:         []Option{WithClassifierLLM(classifierLLM)},
			role:         loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantProvider: "ollama",
		},
		{
			name:         "CLASSIFIER without override falls back to main",
			opts:         nil,
			role:         loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantProvider: "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM, tt.opts...)
			assert.Equal(t, tt.wantProvider, ag.GetLLMProviderNameForRole(tt.role))
		})
	}
}

// ---------------------------------------------------------------------------
// TestGetLLMModelForRole_NilLLM
// ---------------------------------------------------------------------------

func TestGetLLMModelForRole_NilLLM(t *testing.T) {
	// Agent with nil main LLM -- GetLLMModelForRole should return "" not panic
	ag := NewAgent(nil, nil)
	assert.Equal(t, "", ag.GetLLMModelForRole(loomv1.LLMRole_LLM_ROLE_AGENT))
	assert.Equal(t, "", ag.GetLLMProviderNameForRole(loomv1.LLMRole_LLM_ROLE_JUDGE))
}

// ---------------------------------------------------------------------------
// TestSetLLMProviderForRole
// ---------------------------------------------------------------------------

func TestSetLLMProviderForRole(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")
	orchestratorLLM := newRoleMockLLM("orchestrator", "orchestrator-model")
	classifierLLM := newRoleMockLLM("classifier", "classifier-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	tests := []struct {
		name     string
		role     loomv1.LLMRole
		provider LLMProvider
		// After setting, which role should we query to verify?
		queryRole loomv1.LLMRole
		wantName  string
	}{
		{
			name:      "set JUDGE provider",
			role:      loomv1.LLMRole_LLM_ROLE_JUDGE,
			provider:  judgeLLM,
			queryRole: loomv1.LLMRole_LLM_ROLE_JUDGE,
			wantName:  "judge",
		},
		{
			name:      "set ORCHESTRATOR provider",
			role:      loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			provider:  orchestratorLLM,
			queryRole: loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
			wantName:  "orchestrator",
		},
		{
			name:      "set CLASSIFIER provider",
			role:      loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			provider:  classifierLLM,
			queryRole: loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			wantName:  "classifier",
		},
		{
			name:      "set COMPRESSOR provider",
			role:      loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			provider:  compressorLLM,
			queryRole: loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			wantName:  "compressor",
		},
		{
			name:      "set AGENT replaces main LLM",
			role:      loomv1.LLMRole_LLM_ROLE_AGENT,
			provider:  newRoleMockLLM("new-main", "new-main-model"),
			queryRole: loomv1.LLMRole_LLM_ROLE_AGENT,
			wantName:  "new-main",
		},
		{
			name:      "set UNSPECIFIED replaces main LLM",
			role:      loomv1.LLMRole_LLM_ROLE_UNSPECIFIED,
			provider:  newRoleMockLLM("via-unspecified", "via-unspecified-model"),
			queryRole: loomv1.LLMRole_LLM_ROLE_AGENT,
			wantName:  "via-unspecified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM)

			ag.SetLLMProviderForRole(tt.role, tt.provider)

			got := ag.GetLLMForRole(tt.queryRole)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantName, got.Name())
		})
	}
}

// TestSetLLMProviderForRole_DoesNotAffectOtherRoles verifies that setting one
// role does not modify any other role's provider.
func TestSetLLMProviderForRole_DoesNotAffectOtherRoles(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")

	ag := createRoleTestAgent(mainLLM)
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, judgeLLM)

	// Judge should be updated
	assert.Equal(t, "judge", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())

	// All other roles should still fall back to main
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR).Name())
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_CLASSIFIER).Name())
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR).Name())
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_AGENT).Name())
}

// TestSetLLMProviderForRole_CompressorUpdatesMemory verifies that setting the
// COMPRESSOR role also calls Memory.SetLLMProvider so that the memory subsystem
// uses the dedicated compressor for conversation compression.
func TestSetLLMProviderForRole_CompressorUpdatesMemory(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	ag := createRoleTestAgent(mainLLM)

	// Memory should exist (created by NewAgent -> NewMemory)
	require.NotNil(t, ag.memory, "Agent memory should be initialized by NewAgent")

	// NewAgent sets memory.llmProvider to the main LLM during construction
	// (see agent.go: a.memory.SetLLMProvider(a.llm) when no compressorLLM is set)
	require.NotNil(t, ag.memory.llmProvider,
		"Memory LLM provider should be set to main LLM after NewAgent")
	assert.Equal(t, "main", ag.memory.llmProvider.Name(),
		"Memory should initially use main LLM when no compressor is configured")

	// Set compressor role -- this should update memory to use the compressor
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR, compressorLLM)

	// Memory should now have the compressor LLM
	require.NotNil(t, ag.memory.llmProvider, "Memory LLM provider should be set after COMPRESSOR role update")
	assert.Equal(t, "compressor", ag.memory.llmProvider.Name(),
		"Memory should use the compressor-specific LLM, not main")
}

// TestSetLLMProviderForRole_AgentUpdatesMemoryConditionally verifies that
// setting AGENT/UNSPECIFIED role only updates memory if no dedicated compressor
// has been set.
func TestSetLLMProviderForRole_AgentUpdatesMemoryConditionally(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")
	newMainLLM := newRoleMockLLM("new-main", "new-main-model")

	t.Run("no compressor set, AGENT updates memory", func(t *testing.T) {
		ag := createRoleTestAgent(mainLLM)
		require.NotNil(t, ag.memory)

		// No compressor set, so setting AGENT should update memory
		ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_AGENT, newMainLLM)

		require.NotNil(t, ag.memory.llmProvider)
		assert.Equal(t, "new-main", ag.memory.llmProvider.Name(),
			"With no compressor, AGENT role change should update memory LLM")
	})

	t.Run("compressor set, AGENT does NOT update memory", func(t *testing.T) {
		ag := createRoleTestAgent(mainLLM, WithCompressorLLM(compressorLLM))
		require.NotNil(t, ag.memory)

		// Memory was updated by WithCompressorLLM during construction
		// Note: WithCompressorLLM sets a.compressorLLM but does NOT call
		// memory.SetLLMProvider (that happens in SetLLMProviderForRole).
		// So we need to call SetLLMProviderForRole first to set up memory.
		ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR, compressorLLM)
		require.NotNil(t, ag.memory.llmProvider)
		assert.Equal(t, "compressor", ag.memory.llmProvider.Name())

		// Now set AGENT -- memory should NOT change because compressor is set
		ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_AGENT, newMainLLM)

		assert.Equal(t, "compressor", ag.memory.llmProvider.Name(),
			"With dedicated compressor, AGENT role change should NOT update memory LLM")
	})

	t.Run("UNSPECIFIED follows same logic as AGENT", func(t *testing.T) {
		ag := createRoleTestAgent(mainLLM)
		require.NotNil(t, ag.memory)

		ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, newMainLLM)
		require.NotNil(t, ag.memory.llmProvider)
		assert.Equal(t, "new-main", ag.memory.llmProvider.Name(),
			"UNSPECIFIED should update memory when no compressor is set")
	})
}

// ---------------------------------------------------------------------------
// TestGetAllRoleLLMs
// ---------------------------------------------------------------------------

func TestGetAllRoleLLMs(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")
	orchestratorLLM := newRoleMockLLM("orchestrator", "orchestrator-model")
	classifierLLM := newRoleMockLLM("classifier", "classifier-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	tests := []struct {
		name      string
		opts      []Option
		wantRoles []loomv1.LLMRole
		wantCount int
	}{
		{
			name:      "no role LLMs -> only AGENT in map",
			opts:      nil,
			wantRoles: []loomv1.LLMRole{loomv1.LLMRole_LLM_ROLE_AGENT},
			wantCount: 1,
		},
		{
			name:      "judge set -> AGENT + JUDGE",
			opts:      []Option{WithJudgeLLM(judgeLLM)},
			wantRoles: []loomv1.LLMRole{loomv1.LLMRole_LLM_ROLE_AGENT, loomv1.LLMRole_LLM_ROLE_JUDGE},
			wantCount: 2,
		},
		{
			name: "judge + classifier -> AGENT + JUDGE + CLASSIFIER",
			opts: []Option{WithJudgeLLM(judgeLLM), WithClassifierLLM(classifierLLM)},
			wantRoles: []loomv1.LLMRole{
				loomv1.LLMRole_LLM_ROLE_AGENT,
				loomv1.LLMRole_LLM_ROLE_JUDGE,
				loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
			},
			wantCount: 3,
		},
		{
			name: "all roles set -> 5 entries",
			opts: []Option{
				WithJudgeLLM(judgeLLM),
				WithOrchestratorLLM(orchestratorLLM),
				WithClassifierLLM(classifierLLM),
				WithCompressorLLM(compressorLLM),
			},
			wantRoles: []loomv1.LLMRole{
				loomv1.LLMRole_LLM_ROLE_AGENT,
				loomv1.LLMRole_LLM_ROLE_JUDGE,
				loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
				loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
				loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
			},
			wantCount: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM, tt.opts...)
			result := ag.GetAllRoleLLMs()

			assert.Len(t, result, tt.wantCount, "Should have correct number of role LLMs")

			for _, role := range tt.wantRoles {
				_, exists := result[role]
				assert.True(t, exists, "Expected role %s to be present in map", role.String())
			}
		})
	}
}

// TestGetAllRoleLLMs_NilMainLLM verifies that GetAllRoleLLMs does not include
// the AGENT key when the main LLM is nil.
func TestGetAllRoleLLMs_NilMainLLM(t *testing.T) {
	ag := NewAgent(nil, nil)
	result := ag.GetAllRoleLLMs()

	_, hasAgent := result[loomv1.LLMRole_LLM_ROLE_AGENT]
	assert.False(t, hasAgent, "Nil main LLM should not be included in GetAllRoleLLMs")
	assert.Len(t, result, 0)
}

// TestGetAllRoleLLMs_VerifiesCorrectProviders checks that the returned map
// contains the exact providers that were configured.
func TestGetAllRoleLLMs_VerifiesCorrectProviders(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	ag := createRoleTestAgent(mainLLM, WithJudgeLLM(judgeLLM), WithCompressorLLM(compressorLLM))
	result := ag.GetAllRoleLLMs()

	assert.Equal(t, "main", result[loomv1.LLMRole_LLM_ROLE_AGENT].Name())
	assert.Equal(t, "judge", result[loomv1.LLMRole_LLM_ROLE_JUDGE].Name())
	assert.Equal(t, "compressor", result[loomv1.LLMRole_LLM_ROLE_COMPRESSOR].Name())

	// Orchestrator and classifier should NOT be present
	_, hasOrch := result[loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR]
	assert.False(t, hasOrch, "Orchestrator should not be in map when not set")
	_, hasCls := result[loomv1.LLMRole_LLM_ROLE_CLASSIFIER]
	assert.False(t, hasCls, "Classifier should not be in map when not set")
}

// ---------------------------------------------------------------------------
// TestWithRoleLLMOptions
// ---------------------------------------------------------------------------

func TestWithRoleLLMOptions(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")

	tests := []struct {
		name        string
		optionFn    func(LLMProvider) Option
		provider    LLMProvider
		checkField  func(*Agent) LLMProvider
		description string
	}{
		{
			name:     "WithJudgeLLM sets judgeLLM field",
			optionFn: WithJudgeLLM,
			provider: newRoleMockLLM("judge", "judge-model"),
			checkField: func(a *Agent) LLMProvider {
				return a.judgeLLM
			},
			description: "WithJudgeLLM option should set the agent's judgeLLM field",
		},
		{
			name:     "WithOrchestratorLLM sets orchestratorLLM field",
			optionFn: WithOrchestratorLLM,
			provider: newRoleMockLLM("orchestrator", "orchestrator-model"),
			checkField: func(a *Agent) LLMProvider {
				return a.orchestratorLLM
			},
			description: "WithOrchestratorLLM option should set the agent's orchestratorLLM field",
		},
		{
			name:     "WithClassifierLLM sets classifierLLM field",
			optionFn: WithClassifierLLM,
			provider: newRoleMockLLM("classifier", "classifier-model"),
			checkField: func(a *Agent) LLMProvider {
				return a.classifierLLM
			},
			description: "WithClassifierLLM option should set the agent's classifierLLM field",
		},
		{
			name:     "WithCompressorLLM sets compressorLLM field",
			optionFn: WithCompressorLLM,
			provider: newRoleMockLLM("compressor", "compressor-model"),
			checkField: func(a *Agent) LLMProvider {
				return a.compressorLLM
			},
			description: "WithCompressorLLM option should set the agent's compressorLLM field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := createRoleTestAgent(mainLLM, tt.optionFn(tt.provider))

			got := tt.checkField(ag)
			require.NotNil(t, got, tt.description)
			assert.Equal(t, tt.provider.Name(), got.Name(), tt.description)
			assert.Equal(t, tt.provider.Model(), got.Model(), tt.description)
		})
	}
}

// TestWithRoleLLMOptions_DoNotAffectMainLLM verifies that With*LLM options
// do not modify the main agent LLM.
func TestWithRoleLLMOptions_DoNotAffectMainLLM(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")
	orchestratorLLM := newRoleMockLLM("orchestrator", "orchestrator-model")
	classifierLLM := newRoleMockLLM("classifier", "classifier-model")
	compressorLLM := newRoleMockLLM("compressor", "compressor-model")

	ag := createRoleTestAgent(mainLLM,
		WithJudgeLLM(judgeLLM),
		WithOrchestratorLLM(orchestratorLLM),
		WithClassifierLLM(classifierLLM),
		WithCompressorLLM(compressorLLM),
	)

	assert.Equal(t, "main", ag.llm.Name(), "Main LLM should not be modified by WithXxxLLM options")
	assert.Equal(t, "main-model", ag.llm.Model(), "Main LLM model should not be modified")
}

// ---------------------------------------------------------------------------
// TestRoleLLM_ConcurrentAccess (race detector test)
// ---------------------------------------------------------------------------

// TestRoleLLM_ConcurrentAccess exercises concurrent GetLLMForRole and
// SetLLMProviderForRole from multiple goroutines. This test is designed to
// catch data races and must always be run with -race.
func TestRoleLLM_ConcurrentAccess(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	ag := createRoleTestAgent(mainLLM)

	roles := []loomv1.LLMRole{
		loomv1.LLMRole_LLM_ROLE_JUDGE,
		loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
		loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
		loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
		loomv1.LLMRole_LLM_ROLE_AGENT,
		loomv1.LLMRole_LLM_ROLE_UNSPECIFIED,
	}

	const goroutines = 20
	const iterations = 100

	var wg sync.WaitGroup

	// Concurrent writers: set role-specific LLMs
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				role := roles[j%len(roles)]
				provider := newRoleMockLLM(
					"writer-"+string(rune('A'+id)),
					"model-"+string(rune('A'+id)),
				)
				ag.SetLLMProviderForRole(role, provider)
			}
		}(i)
	}

	// Concurrent readers: get role-specific LLMs
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				role := roles[j%len(roles)]
				llm := ag.GetLLMForRole(role)
				// LLM should never be nil since main LLM is always set
				// (might change during concurrent writes to AGENT/UNSPECIFIED,
				// but GetLLMForRole returns main which was initially set)
				if llm != nil {
					_ = llm.Name()
					_ = llm.Model()
				}
			}
		}()
	}

	// Concurrent GetAllRoleLLMs calls
	for i := 0; i < goroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				result := ag.GetAllRoleLLMs()
				// Should always have at least 0 entries (main can be swapped)
				_ = len(result)
			}
		}()
	}

	// Concurrent model/provider name lookups
	for i := 0; i < goroutines/4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				role := roles[j%len(roles)]
				_ = ag.GetLLMModelForRole(role)
				_ = ag.GetLLMProviderNameForRole(role)
			}
		}()
	}

	wg.Wait()

	// If we get here without a data race, the test passes.
	// Verify the agent is still functional after all concurrent access.
	llm := ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_AGENT)
	assert.NotNil(t, llm, "Agent LLM should still be accessible after concurrent operations")
}

// TestRoleLLM_ConcurrentSetAndGetSameRole exercises the specific scenario of
// concurrent reads and writes to the same role, which is the most likely path
// for a data race.
func TestRoleLLM_ConcurrentSetAndGetSameRole(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	ag := createRoleTestAgent(mainLLM)

	const goroutines = 10
	const iterations = 200

	var wg sync.WaitGroup

	// All goroutines operate on JUDGE role to maximize contention
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		if i%2 == 0 {
			// Writer
			go func(id int) {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					provider := newRoleMockLLM("judge-writer", "model-v"+string(rune('0'+id)))
					ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, provider)
				}
			}(i)
		} else {
			// Reader
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					llm := ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE)
					if llm != nil {
						_ = llm.Name()
					}
				}
			}()
		}
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestSetLLMProviderForRole_OverwriteExisting
// ---------------------------------------------------------------------------

// TestSetLLMProviderForRole_OverwriteExisting verifies that calling
// SetLLMProviderForRole twice overwrites the previous value.
func TestSetLLMProviderForRole_OverwriteExisting(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	ag := createRoleTestAgent(mainLLM)

	first := newRoleMockLLM("first-judge", "model-v1")
	second := newRoleMockLLM("second-judge", "model-v2")

	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, first)
	assert.Equal(t, "first-judge", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())

	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, second)
	assert.Equal(t, "second-judge", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())
}

// ---------------------------------------------------------------------------
// TestSetLLMProviderForRole_NilProvider
// ---------------------------------------------------------------------------

// TestSetLLMProviderForRole_NilProvider verifies that setting a role to nil
// causes that role to fall back to the main LLM again.
func TestSetLLMProviderForRole_NilProvider(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	judgeLLM := newRoleMockLLM("judge", "judge-model")

	ag := createRoleTestAgent(mainLLM, WithJudgeLLM(judgeLLM))

	// Judge should currently return the judge-specific LLM
	assert.Equal(t, "judge", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())

	// Setting to nil should cause fallback
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, nil)
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name(),
		"After setting JUDGE to nil, should fall back to main LLM")
}

// ---------------------------------------------------------------------------
// TestGetLLMForRole_UnknownRoleValue
// ---------------------------------------------------------------------------

// TestGetLLMForRole_UnknownRoleValue verifies behavior with an undefined
// proto enum value. It should fall through to the main LLM.
func TestGetLLMForRole_UnknownRoleValue(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	ag := createRoleTestAgent(mainLLM)

	// Use a role value not in the switch statement (e.g., 99)
	unknownRole := loomv1.LLMRole(99)
	got := ag.GetLLMForRole(unknownRole)
	require.NotNil(t, got, "Unknown role should fall back to main LLM")
	assert.Equal(t, "main", got.Name(), "Unknown role should return main LLM")
}

// ---------------------------------------------------------------------------
// TestRoleLLM_FullLifecycle
// ---------------------------------------------------------------------------

// TestRoleLLM_FullLifecycle exercises the complete lifecycle:
// create agent -> set roles -> verify -> overwrite -> verify -> clear -> verify.
func TestRoleLLM_FullLifecycle(t *testing.T) {
	mainLLM := newRoleMockLLM("main", "main-model")
	ag := createRoleTestAgent(mainLLM)

	// Step 1: Initially all roles return main
	for _, role := range []loomv1.LLMRole{
		loomv1.LLMRole_LLM_ROLE_JUDGE,
		loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR,
		loomv1.LLMRole_LLM_ROLE_CLASSIFIER,
		loomv1.LLMRole_LLM_ROLE_COMPRESSOR,
	} {
		assert.Equal(t, "main", ag.GetLLMForRole(role).Name(),
			"Step 1: Role %s should return main initially", role.String())
	}

	// Step 2: Set all role-specific providers
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, newRoleMockLLM("judge-v1", "j-v1"))
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR, newRoleMockLLM("orch-v1", "o-v1"))
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_CLASSIFIER, newRoleMockLLM("cls-v1", "c-v1"))
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR, newRoleMockLLM("comp-v1", "co-v1"))

	assert.Equal(t, "judge-v1", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())
	assert.Equal(t, "orch-v1", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR).Name())
	assert.Equal(t, "cls-v1", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_CLASSIFIER).Name())
	assert.Equal(t, "comp-v1", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_COMPRESSOR).Name())
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_AGENT).Name())

	allLLMs := ag.GetAllRoleLLMs()
	assert.Len(t, allLLMs, 5, "Step 2: Should have all 5 role LLMs")

	// Step 3: Overwrite judge
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_JUDGE, newRoleMockLLM("judge-v2", "j-v2"))
	assert.Equal(t, "judge-v2", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_JUDGE).Name())
	assert.Equal(t, "j-v2", ag.GetLLMModelForRole(loomv1.LLMRole_LLM_ROLE_JUDGE))

	// Step 4: Clear orchestrator by setting to nil
	ag.SetLLMProviderForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR, nil)
	assert.Equal(t, "main", ag.GetLLMForRole(loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR).Name(),
		"Step 4: Cleared orchestrator should fall back to main")

	allLLMs = ag.GetAllRoleLLMs()
	assert.Len(t, allLLMs, 4, "Step 4: Should have 4 role LLMs (orchestrator cleared)")
	_, hasOrch := allLLMs[loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR]
	assert.False(t, hasOrch, "Orchestrator should not be in map after clearing")
}
