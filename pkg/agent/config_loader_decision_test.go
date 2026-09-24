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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE, d.Bands[0].Mode, "mode defaults to replace")
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
