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
)

// Every scalar in the decision YAML block reaches the proto; a field that
// silently stayed at its zero value would look like "configured but inert".
func TestConvertDecisionConfigYAMLCarriesEveryField(t *testing.T) {
	cfg, err := convertDecisionConfigYAMLToProto(&DecisionConfigYAML{
		Provider:             "jev",
		Model:                "typesafe-ai/jev",
		AllowAlias:           true,
		TimeoutMs:            1500,
		BaseURL:              "https://example.test",
		MaxPerSession:        40,
		MaxCostUSDPerSession: 0.5,
		LLMRole:              "classifier",
		RequestsPerMinute:    30,
		ExposeTool:           true,
	})
	require.NoError(t, err)
	assert.Equal(t, "jev", cfg.Provider)
	assert.Equal(t, "typesafe-ai/jev", cfg.Model)
	assert.True(t, cfg.AllowAlias)
	assert.Equal(t, int64(1500), cfg.TimeoutMs)
	assert.Equal(t, "https://example.test", cfg.BaseUrl)
	assert.Equal(t, int64(40), cfg.MaxPerSession)
	assert.InDelta(t, 0.5, cfg.MaxCostUsdPerSession, 1e-9)
	assert.Equal(t, "classifier", cfg.LlmRole)
	assert.Equal(t, int64(30), cfg.RequestsPerMinute)
	assert.True(t, cfg.ExposeTool)

	_, err = convertDecisionConfigYAMLToProto(&DecisionConfigYAML{Provider: "jev", RequestsPerMinute: -1})
	require.Error(t, err)
}
