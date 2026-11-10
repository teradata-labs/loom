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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

const decisionYAMLHead = `
agent:
  name: decision-test
  llm:
    provider: anthropic
    model: claude-sonnet-5
`

func TestLoadConfig_DecisionBlock(t *testing.T) {
	t.Parallel()
	cfg, err := LoadConfigFromString(decisionYAMLHead + `
  decision:
    provider: llm
    llm_role: classifier
    model: jev-1.13.0
    timeout_ms: 2000
    max_per_session: 200
    max_cost_usd_per_session: 0.05
    bands:
      - site: recall.rerank
        act_min: 0.9
      - site: tool.failure_kind
        act_min: 0.8
        mode: tighten_only
        shadow: true
        aggregate: per_question
`)
	require.NoError(t, err)
	d := cfg.GetDecision()
	require.NotNil(t, d)
	assert.Equal(t, "llm", d.Provider)
	assert.Equal(t, "classifier", d.LlmRole)
	assert.Equal(t, "jev-1.13.0", d.Model)
	assert.Equal(t, int64(2000), d.TimeoutMs)
	assert.Equal(t, int64(200), d.MaxPerSession)
	assert.InDelta(t, 0.05, d.MaxCostUsdPerSession, 1e-12)
	assert.Empty(t, d.BaseUrl)
	require.Len(t, d.Bands, 2)
	assert.Equal(t, "recall.rerank", d.Bands[0].Site)
	assert.InDelta(t, 0.9, d.Bands[0].ActMin, 1e-9)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_UNSPECIFIED, d.Bands[0].Mode,
		"an unset mode stays unset, so the site's own default applies (tighten-only at the gate sites)")
	assert.False(t, d.Bands[0].Shadow)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY, d.Bands[1].Mode)
	assert.True(t, d.Bands[1].Shadow)
	assert.Equal(t, loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_MIN, d.Bands[0].Aggregate, "aggregate defaults to min")
	assert.Equal(t, loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION, d.Bands[1].Aggregate)
}

// Every scalar in the decision YAML block reaches the proto; a field that
// silently stayed at its zero value would look like "configured but inert".
func TestConvertDecisionConfigYAMLCarriesEveryField(t *testing.T) {
	cfg, err := convertDecisionConfigYAMLToProto(&DecisionConfigYAML{
		Provider:               "jev",
		Model:                  "typesafe-ai/jev",
		AllowAlias:             true,
		TimeoutMs:              1500,
		MaxPerSession:          40,
		MaxCostUSDPerSession:   0.5,
		LLMRole:                "classifier",
		RequestsPerMinute:      30,
		ExposeTool:             true,
		MaxQuestionsPerRequest: 12,
	})
	require.NoError(t, err)
	assert.Equal(t, "jev", cfg.Provider)
	assert.Equal(t, "typesafe-ai/jev", cfg.Model)
	assert.True(t, cfg.AllowAlias)
	assert.Equal(t, int64(1500), cfg.TimeoutMs)
	assert.Empty(t, cfg.BaseUrl)
	assert.Equal(t, int64(40), cfg.MaxPerSession)
	assert.InDelta(t, 0.5, cfg.MaxCostUsdPerSession, 1e-9)
	assert.Equal(t, "classifier", cfg.LlmRole)
	assert.Equal(t, int64(30), cfg.RequestsPerMinute)
	assert.True(t, cfg.ExposeTool)
	assert.Equal(t, int64(12), cfg.MaxQuestionsPerRequest)

	_, err = convertDecisionConfigYAMLToProto(&DecisionConfigYAML{Provider: "jev", RequestsPerMinute: -1})
	require.Error(t, err)

	// The endpoint is server-level only: a YAML base_url is refused.
	_, err = convertDecisionConfigYAMLToProto(&DecisionConfigYAML{Provider: "jev", BaseURL: "https://example.test"})
	require.ErrorIs(t, err, decision.ErrEndpointNotServerLevel)
	_, err = convertDecisionConfigYAMLToProto(&DecisionConfigYAML{Provider: "jev", MaxQuestionsPerRequest: -1})
	require.Error(t, err)
}

// true_min is per band and is not act_min: the two answer different
// questions and a config that confuses them inverts the site's behaviour.
func TestConvertDecisionBandYAMLCarriesTrueMin(t *testing.T) {
	cfg, err := convertDecisionConfigYAMLToProto(&DecisionConfigYAML{
		Provider: "jev",
		Bands: []DecisionBandConfigYAML{
			{Site: "recall.rerank", ActMin: 0.5, TrueMin: 0.3, Aggregate: "per_question"},
			{Site: "stage.validation", ActMin: 0.6},
		},
	})
	require.NoError(t, err)
	require.Len(t, cfg.Bands, 2)
	assert.InDelta(t, 0.5, cfg.Bands[0].ActMin, 1e-9)
	assert.InDelta(t, 0.3, cfg.Bands[0].TrueMin, 1e-9)
	assert.InDelta(t, 0, cfg.Bands[1].TrueMin, 1e-9, "unset means the site default")

	for _, bad := range []float64{-0.1, 1.5} {
		_, err = convertDecisionConfigYAMLToProto(&DecisionConfigYAML{
			Provider: "jev",
			Bands:    []DecisionBandConfigYAML{{Site: "recall.rerank", TrueMin: bad}},
		})
		require.Error(t, err, "true_min %v", bad)
	}
}

func TestLoadConfig_DecisionK8sFormat(t *testing.T) {
	t.Parallel()
	cfg, err := LoadConfigFromString(`
apiVersion: loom/v1
kind: Agent
metadata:
  name: k8s-decision
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-5
  decision:
    provider: mock
    bands:
      - site: recall.rerank
        shadow: true
`)
	require.NoError(t, err)
	require.NotNil(t, cfg.GetDecision(), "k8s-style spec.decision maps to the legacy block")
	assert.Equal(t, "mock", cfg.GetDecision().Provider)
	require.Len(t, cfg.GetDecision().Bands, 1)
	assert.True(t, cfg.GetDecision().Bands[0].Shadow)
}

// Load → SaveAgentConfig → load keeps the decision block proto-equal (#482).
func TestSaveAgentConfig_DecisionRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		block string
	}{
		{
			name: "full block with several bands and modes",
			block: `  decision:
    provider: jev
    llm_role: classifier
    model: typesafe-ai/jev
    allow_alias: true
    timeout_ms: 2000
    max_per_session: 200
    max_cost_usd_per_session: 0.05
    requests_per_minute: 30
    expose_tool: true
    max_questions_per_request: 12
    bands:
      - site: recall.rerank
        act_min: 0.9
        true_min: 0.4
        aggregate: per_question
        mode: replace
      - site: tool.failure_kind
        act_min: 0.8
        mode: tighten_only
        shadow: true
      - site: tool.search
        act_min: 0.75
        mode: tighten-only
`,
		},
		{
			name: "band with no mode",
			block: `  decision:
    provider: llm
    bands:
      - site: recall.rerank
        act_min: 0.6
`,
		},
		{
			name: "shadow band and provider case normalised",
			block: `  decision:
    provider: MOCK
    bands:
      - site: recall.rerank
        shadow: true
`,
		},
		{name: "empty block means off", block: "  decision: {}\n"},
		{name: "no decision block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first, err := LoadConfigFromString(decisionYAMLHead + tt.block)
			require.NoError(t, err)

			path := filepath.Join(t.TempDir(), "agent.yaml")
			require.NoError(t, SaveAgentConfig(first, path))
			second, err := LoadAgentConfig(path)
			require.NoError(t, err)

			if tt.block == "" {
				assert.Nil(t, second.GetDecision(), "an agent without a decision block must not gain one")
				return
			}
			require.NotNil(t, second.GetDecision(), "SaveAgentConfig dropped the decision block")
			assert.True(t, proto.Equal(first.GetDecision(), second.GetDecision()),
				"decision changed across save:\nfirst:  %v\nsecond: %v", first.GetDecision(), second.GetDecision())
		})
	}
}

// A band built in code with no mode is saved without one, so the file stays
// unset and reloads unset: the site supplies its default (tighten-only at the
// gate sites, replace elsewhere; decision.Band.ModeOr).
func TestSaveAgentConfig_DecisionUnspecifiedModeStaysUnset(t *testing.T) {
	t.Parallel()
	cfg := &loomv1.AgentConfig{
		Name: "decision-unset",
		Decision: &loomv1.DecisionConfig{
			Provider: DecisionProviderLLM,
			Bands:    []*loomv1.DecisionBand{{Site: "recall.rerank", ActMin: 0.5}},
		},
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	require.NoError(t, SaveAgentConfig(cfg, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "mode:", "an unset mode must not be written")
	assert.NotContains(t, string(data), "base_url", "the server-level endpoint is never written to agent YAML")

	got, err := LoadAgentConfig(path)
	require.NoError(t, err)
	require.Len(t, got.GetDecision().GetBands(), 1)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_UNSPECIFIED, got.GetDecision().GetBands()[0].GetMode())
	assert.InDelta(t, 0.5, got.GetDecision().GetBands()[0].GetActMin(), 1e-12)
}
