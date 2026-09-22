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
    base_url: https://gateway.example/typesafe
    bands:
      - site: recall.rerank
        act_min: 0.9
      - site: tool.failure_kind
        act_min: 0.8
        mode: tighten_only
        shadow: true
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
	assert.Equal(t, "https://gateway.example/typesafe", d.BaseUrl)
	require.Len(t, d.Bands, 2)
	assert.Equal(t, "recall.rerank", d.Bands[0].Site)
	assert.InDelta(t, 0.9, d.Bands[0].ActMin, 1e-9)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE, d.Bands[0].Mode, "mode defaults to replace")
	assert.False(t, d.Bands[0].Shadow)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY, d.Bands[1].Mode)
	assert.True(t, d.Bands[1].Shadow)
}

func TestLoadConfig_DecisionAbsentIsNil(t *testing.T) {
	t.Parallel()
	cfg, err := LoadConfigFromString(decisionYAMLHead)
	require.NoError(t, err)
	assert.Nil(t, cfg.GetDecision())
}

func TestLoadConfig_DecisionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{name: "empty provider means off", block: "  decision: {}\n"},
		{name: "unknown provider", block: "  decision:\n    provider: gemini\n", wantErr: "decision.provider"},
		{name: "alias rejected", block: "  decision:\n    provider: jev\n    model: jev-latest\n", wantErr: "floating alias"},
		{name: "alias allowed", block: "  decision:\n    provider: jev\n    model: jev-latest\n    allow_alias: true\n"},
		{name: "negative timeout", block: "  decision:\n    provider: llm\n    timeout_ms: -1\n", wantErr: "timeout_ms"},
		{name: "negative budget", block: "  decision:\n    provider: llm\n    max_per_session: -5\n", wantErr: "max_per_session"},
		{name: "negative cost", block: "  decision:\n    provider: llm\n    max_cost_usd_per_session: -0.1\n", wantErr: "max_cost_usd_per_session"},
		{name: "band missing site", block: "  decision:\n    provider: llm\n    bands:\n      - act_min: 0.5\n", wantErr: "site is required"},
		{name: "band duplicate site", block: "  decision:\n    provider: llm\n    bands:\n      - site: a\n      - site: a\n", wantErr: "duplicate site"},
		{name: "band act_min out of range", block: "  decision:\n    provider: llm\n    bands:\n      - site: a\n        act_min: 1.5\n", wantErr: "act_min"},
		{name: "band bad mode", block: "  decision:\n    provider: llm\n    bands:\n      - site: a\n        mode: maybe\n", wantErr: "mode"},
		{name: "mode spellings", block: "  decision:\n    provider: llm\n    bands:\n      - site: a\n        mode: tighten-only\n      - site: b\n        mode: REPLACE\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := LoadConfigFromString(decisionYAMLHead + tt.block)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg.GetDecision())
			if tt.name == "empty provider means off" {
				assert.Equal(t, "off", cfg.GetDecision().Provider)
			}
		})
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
