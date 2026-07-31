// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestApplyBehaviorConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		behavior *loomv1.BehaviorConfig
		check    func(t *testing.T, cfg *Config)
		wantErr  string
	}{
		{
			name:     "nil behavior applies legacy defaults",
			behavior: nil,
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 25, cfg.MaxTurns)
				assert.Equal(t, 50, cfg.MaxToolExecutions)
				assert.Equal(t, 8, cfg.OutputTokenCBThreshold)
				assert.Nil(t, cfg.OutputVerification)
			},
		},
		{
			name: "explicit values applied",
			behavior: &loomv1.BehaviorConfig{
				MaxTurns:               40,
				MaxToolExecutions:      99,
				MaxIterations:          7,
				OutputTokenCbThreshold: 4,
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 40, cfg.MaxTurns)
				assert.Equal(t, 99, cfg.MaxToolExecutions)
				assert.Equal(t, 7, cfg.MaxIterations)
				assert.Equal(t, 4, cfg.OutputTokenCBThreshold)
			},
		},
		{
			name:     "zero values fall back to legacy defaults (parity with pre-consolidation sites)",
			behavior: &loomv1.BehaviorConfig{},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 25, cfg.MaxTurns)
				assert.Equal(t, 50, cfg.MaxToolExecutions)
				assert.Equal(t, 8, cfg.OutputTokenCBThreshold)
			},
		},
		{
			name:     "negative CB threshold (disabled) is preserved, not defaulted",
			behavior: &loomv1.BehaviorConfig{OutputTokenCbThreshold: -1},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, -1, cfg.OutputTokenCBThreshold)
			},
		},
		{
			name: "pattern config transferred with defaults overlay",
			behavior: &loomv1.BehaviorConfig{
				Patterns: &loomv1.PatternConfig{
					Enabled:        true,
					EnableTracking: true,
					MinConfidence:  0.9,
				},
			},
			check: func(t *testing.T, cfg *Config) {
				require.NotNil(t, cfg.PatternConfig)
				assert.True(t, cfg.PatternConfig.Enabled)
				assert.InDelta(t, 0.9, cfg.PatternConfig.MinConfidence, 1e-6)
				assert.Equal(t, 1, cfg.PatternConfig.MaxPatternsPerTurn, "zero proto value overlays Go default")
			},
		},
		{
			name: "output policy mapped",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema:       `{"type":"object"}`,
					AcceptanceCriteria: "cites a table",
					RetryPolicy: &loomv1.OutputRetryPolicy{
						MaxRetries:         3,
						IncludeValidValues: true,
						SessionMode:        loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE,
						FeedbackTemplate:   "t {{error}}",
						CooldownMs:         250,
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				ov := cfg.OutputVerification
				require.NotNil(t, ov)
				assert.Equal(t, `{"type":"object"}`, ov.Schema)
				assert.Equal(t, "cites a table", ov.AcceptanceCriteria)
				assert.Equal(t, 3, ov.MaxRetries)
				assert.True(t, ov.IncludeValidValues)
				assert.Equal(t, "t {{error}}", ov.FeedbackTemplate)
				assert.Equal(t, 250, ov.CooldownMs)
			},
		},
		{
			name: "output policy retries capped at 10",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: `{"type":"object"}`,
					RetryPolicy:  &loomv1.OutputRetryPolicy{MaxRetries: 500},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 10, cfg.OutputVerification.MaxRetries)
			},
		},
		{
			name: "output policy without retry_policy verifies once",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{OutputSchema: `{"type":"object"}`},
			},
			check: func(t *testing.T, cfg *Config) {
				require.NotNil(t, cfg.OutputVerification)
				assert.Equal(t, 0, cfg.OutputVerification.MaxRetries)
			},
		},
		{
			name: "unspecified session mode maps to CONTINUE semantics",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: `{"type":"object"}`,
					RetryPolicy: &loomv1.OutputRetryPolicy{
						MaxRetries:  1,
						SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED,
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				require.NotNil(t, cfg.OutputVerification)
			},
		},
		{
			name: "validator_agent_id rejected",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema:     `{"type":"object"}`,
					ValidatorAgentId: "some-agent",
				},
			},
			wantErr: "validator_agent_id is not supported in the agent loop",
		},
		{
			name: "judge_config_id rejected",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema:  `{"type":"object"}`,
					JudgeConfigId: "some-judge",
				},
			},
			wantErr: "judge_config_id is not supported in the agent loop",
		},
		{
			name: "FRESH session mode rejected",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: `{"type":"object"}`,
					RetryPolicy:  &loomv1.OutputRetryPolicy{SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH},
				},
			},
			wantErr: "session_mode",
		},
		{
			name: "ESCALATE session mode rejected",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{
					OutputSchema: `{"type":"object"}`,
					RetryPolicy:  &loomv1.OutputRetryPolicy{SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_ESCALATE},
				},
			},
			wantErr: "session_mode",
		},
		{
			name: "empty output policy rejected",
			behavior: &loomv1.BehaviorConfig{
				OutputPolicy: &loomv1.OutputPolicy{},
			},
			wantErr: "requires output_schema and/or acceptance_criteria",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			err := ApplyBehaviorConfig(cfg, tt.behavior)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}

func TestLoadConfig_OutputPolicyYAML(t *testing.T) {
	t.Parallel()

	t.Run("full round trip", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadConfigFromString(`
agent:
  name: verified-agent
  behavior:
    max_turns: 10
    output_policy:
      output_schema: '{"type":"object"}'
      acceptance_criteria: "cites a table"
      retry_policy:
        max_retries: 2
        session_mode: continue
        feedback_template: "fix it: {{error}}"
        cooldown_ms: 100
`)
		require.NoError(t, err)
		require.NotNil(t, cfg.Behavior.OutputPolicy)
		assert.Equal(t, `{"type":"object"}`, cfg.Behavior.OutputPolicy.OutputSchema)
		assert.Equal(t, "cites a table", cfg.Behavior.OutputPolicy.AcceptanceCriteria)
		rp := cfg.Behavior.OutputPolicy.RetryPolicy
		require.NotNil(t, rp)
		assert.Equal(t, int32(2), rp.MaxRetries)
		assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE, rp.SessionMode)
		assert.True(t, rp.IncludeValidValues, "include_valid_values defaults to true in YAML")
		assert.Equal(t, "fix it: {{error}}", rp.FeedbackTemplate)
		assert.Equal(t, int32(100), rp.CooldownMs)
	})

	t.Run("include_valid_values explicit false honored", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadConfigFromString(`
agent:
  name: a
  behavior:
    output_policy:
      output_schema: '{"type":"object"}'
      retry_policy:
        max_retries: 1
        include_valid_values: false
`)
		require.NoError(t, err)
		assert.False(t, cfg.Behavior.OutputPolicy.RetryPolicy.IncludeValidValues)
	})

	t.Run("unknown session_mode rejected at load", func(t *testing.T) {
		t.Parallel()
		_, err := LoadConfigFromString(`
agent:
  name: a
  behavior:
    output_policy:
      output_schema: '{"type":"object"}'
      retry_policy:
        session_mode: sideways
`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "session_mode")
	})

	t.Run("omitted output_policy stays nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadConfigFromString(`
agent:
  name: a
  behavior:
    max_turns: 5
`)
		require.NoError(t, err)
		assert.Nil(t, cfg.Behavior.OutputPolicy)
	})
}
