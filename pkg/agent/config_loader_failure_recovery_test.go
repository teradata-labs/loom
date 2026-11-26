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
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestFailureRecoveryConfigFromProto_Nil(t *testing.T) {
	assert.Nil(t, FailureRecoveryConfigFromProto(nil))
}

func TestLoadAgentConfig_FailureRecovery(t *testing.T) {
	tests := []struct {
		name     string
		behavior string
		check    func(t *testing.T, fr *loomv1.FailureRecoveryConfig)
	}{
		{
			name:     "unset leaves feature off",
			behavior: "  behavior:\n    max_iterations: 5\n",
			check: func(t *testing.T, fr *loomv1.FailureRecoveryConfig) {
				assert.Nil(t, fr)
				assert.Nil(t, FailureRecoveryConfigFromProto(fr))
			},
		},
		{
			name: "all fields round-trip",
			behavior: `  behavior:
    max_iterations: 5
    failure_recovery:
      enabled: true
      similarity_threshold: 0.75
      max_hints: 3
      classes: [tool_exec]
      initial_salience: 0.5
      user_scoped: true
      preserve_details: true
      semantic_only: true
      proactive:
        enabled: true
        threshold: 0.4
        llm_intent: true
      llm_abstraction:
        enabled: true
        timeout_sec: 12
`,
			check: func(t *testing.T, fr *loomv1.FailureRecoveryConfig) {
				require.NotNil(t, fr)
				cfg := FailureRecoveryConfigFromProto(fr)
				require.NotNil(t, cfg)
				assert.True(t, cfg.Enabled)
				assert.InDelta(t, 0.75, cfg.SimilarityThreshold, 1e-6)
				assert.Equal(t, 3, cfg.MaxHints)
				assert.Equal(t, []string{"tool_exec"}, cfg.Classes)
				assert.InDelta(t, 0.5, cfg.InitialSalience, 1e-6)
				assert.True(t, cfg.UserScoped)
				assert.True(t, cfg.PreserveDetails)
				assert.True(t, cfg.SemanticOnly)
				assert.True(t, cfg.Proactive.Enabled)
				assert.InDelta(t, 0.4, cfg.Proactive.Threshold, 1e-6)
				assert.True(t, cfg.Proactive.LLMIntent)
				assert.True(t, cfg.LLMAbstraction.Enabled)
				assert.Equal(t, 12, cfg.LLMAbstraction.TimeoutSec)
			},
		},
		{
			name:     "enabled only keeps other fields zero for defaults",
			behavior: "  behavior:\n    failure_recovery:\n      enabled: true\n",
			check: func(t *testing.T, fr *loomv1.FailureRecoveryConfig) {
				cfg := FailureRecoveryConfigFromProto(fr)
				require.NotNil(t, cfg)
				assert.True(t, cfg.Enabled)
				assert.Zero(t, cfg.MaxHints)
				assert.True(t, cfg.Proactive.Enabled, "proactive defaults on under failure_recovery")
				assert.True(t, cfg.Proactive.LLMIntent, "llm_intent defaults on")
				assert.False(t, cfg.LLMAbstraction.Enabled)
			},
		},
		{
			name:     "explicit false opts out of proactive and llm_intent",
			behavior: "  behavior:\n    failure_recovery:\n      enabled: true\n      proactive:\n        enabled: false\n        llm_intent: false\n",
			check: func(t *testing.T, fr *loomv1.FailureRecoveryConfig) {
				cfg := FailureRecoveryConfigFromProto(fr)
				require.NotNil(t, cfg)
				assert.False(t, cfg.Proactive.Enabled)
				assert.False(t, cfg.Proactive.LLMIntent)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			content := "agent:\n  name: fr-test\n  description: test\n  llm:\n    provider: anthropic\n    model: test\n" + tt.behavior
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := LoadAgentConfig(path)
			require.NoError(t, err)
			tt.check(t, cfg.GetBehavior().GetFailureRecovery())

			// proto -> YAML round-trip preserves the block.
			back := failureRecoveryProtoToYAML(cfg.GetBehavior().GetFailureRecovery())
			if cfg.GetBehavior().GetFailureRecovery() == nil {
				assert.Nil(t, back)
			} else {
				require.NotNil(t, back)
				again, err := failureRecoveryYAMLToProto(back)
				require.NoError(t, err)
				assert.Equal(t, FailureRecoveryConfigFromProto(cfg.GetBehavior().GetFailureRecovery()),
					FailureRecoveryConfigFromProto(again))
			}
		})
	}
}
