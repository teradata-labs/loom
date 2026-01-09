// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestLoadLearningAgentConfig_Valid(t *testing.T) {
	// Create temp file with valid config
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig

metadata:
  name: test-learner
  description: Test learning agent
  labels:
    domain: test

spec:
  enabled: true
  autonomy_level: human_approval
  analysis_interval: "30m"
  watch_eval_suites:
    - eval-suites/test.yaml
  domains:
    - sql
    - rest
  circuit_breaker:
    enabled: true
    failure_threshold: 3
    cooldown_period: "15m"
    success_threshold: 2
  improvement_policy:
    auto_apply_min_confidence: 0.9
    max_daily_changes: 5
    protected_agents:
      - critical-agent
    allowed_change_types:
      - prompt_append
      - parameter_tune
    max_auto_apply_impact: low
  notifications:
    slack_webhook: https://hooks.slack.com/test
    email_addresses:
      - test@example.com
    notify_on:
      - improvement_generated
      - improvement_applied
    include_details: true
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	// Load config
	config, warnings, err := LoadLearningAgentConfig(tmpFile.Name())
	require.NoError(t, err)
	assert.Empty(t, warnings)

	// Verify config
	assert.Equal(t, "test-learner", config.Name)
	assert.Equal(t, "Test learning agent", config.Description)
	assert.True(t, config.Enabled)
	assert.Equal(t, loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL, config.AutonomyLevel)
	assert.Equal(t, "30m", config.AnalysisInterval)
	assert.Contains(t, config.WatchEvalSuites[0], "eval-suites/test.yaml")
	assert.Equal(t, []string{"sql", "rest"}, config.Domains)

	// Circuit breaker
	assert.True(t, config.CircuitBreaker.Enabled)
	assert.Equal(t, int32(3), config.CircuitBreaker.FailureThreshold)
	assert.Equal(t, "15m", config.CircuitBreaker.CooldownPeriod)
	assert.Equal(t, int32(2), config.CircuitBreaker.SuccessThreshold)

	// Improvement policy
	assert.Equal(t, 0.9, config.ImprovementPolicy.AutoApplyMinConfidence)
	assert.Equal(t, int32(5), config.ImprovementPolicy.MaxDailyChanges)
	assert.Equal(t, []string{"critical-agent"}, config.ImprovementPolicy.ProtectedAgents)
	assert.Equal(t, []string{"prompt_append", "parameter_tune"}, config.ImprovementPolicy.AllowedChangeTypes)
	assert.Equal(t, loomv1.ImpactLevel_IMPACT_LOW, config.ImprovementPolicy.MaxAutoApplyImpact)

	// Notifications
	assert.Equal(t, "https://hooks.slack.com/test", config.Notifications.SlackWebhook)
	assert.Equal(t, []string{"test@example.com"}, config.Notifications.EmailAddresses)
	assert.Equal(t, []string{"improvement_generated", "improvement_applied"}, config.Notifications.NotifyOn)
	assert.True(t, config.Notifications.IncludeDetails)
}

func TestLoadLearningAgentConfig_Defaults(t *testing.T) {
	// Minimal config should use defaults
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig

metadata:
  name: minimal-learner

spec:
  analysis_interval: "1h"
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	config, warnings, err := LoadLearningAgentConfig(tmpFile.Name())
	require.NoError(t, err)

	// Should have warning about empty watch_eval_suites
	assert.Contains(t, warnings, "no watch_eval_suites specified - learning agent will not receive judge feedback")

	// Defaults should be applied
	assert.Equal(t, "minimal-learner", config.Name)
	assert.Equal(t, loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL, config.AutonomyLevel) // Safe default

	// Circuit breaker defaults
	assert.Equal(t, int32(5), config.CircuitBreaker.FailureThreshold)
	assert.Equal(t, "30m", config.CircuitBreaker.CooldownPeriod)
	assert.Equal(t, int32(3), config.CircuitBreaker.SuccessThreshold)

	// Improvement policy defaults
	assert.Equal(t, 0.8, config.ImprovementPolicy.AutoApplyMinConfidence)
	assert.Equal(t, int32(10), config.ImprovementPolicy.MaxDailyChanges)
	assert.Equal(t, loomv1.ImpactLevel_IMPACT_MEDIUM, config.ImprovementPolicy.MaxAutoApplyImpact)
}

func TestLoadLearningAgentConfig_InvalidAPIVersion(t *testing.T) {
	content := `
apiVersion: loom/v2
kind: LearningAgentConfig
metadata:
  name: test
spec: {}
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	_, _, err = LoadLearningAgentConfig(tmpFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
}

func TestLoadLearningAgentConfig_InvalidKind(t *testing.T) {
	content := `
apiVersion: loom/v1
kind: WrongKind
metadata:
  name: test
spec: {}
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	_, _, err = LoadLearningAgentConfig(tmpFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kind must be 'LearningAgentConfig'")
}

func TestLoadLearningAgentConfig_MissingName(t *testing.T) {
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata: {}
spec: {}
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	_, _, err = LoadLearningAgentConfig(tmpFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name is required")
}

func TestLoadLearningAgentConfig_InvalidAutonomyLevel(t *testing.T) {
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: test
spec:
  autonomy_level: invalid_level
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	_, _, err = LoadLearningAgentConfig(tmpFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid autonomy_level")
}

func TestLoadLearningAgentConfig_InvalidAnalysisInterval(t *testing.T) {
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: test
spec:
  analysis_interval: "invalid"
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	_, _, err = LoadLearningAgentConfig(tmpFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid analysis_interval")
}

func TestLoadLearningAgentConfig_WarningFullAutonomyNoCircuitBreaker(t *testing.T) {
	content := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: risky-learner
spec:
  autonomy_level: full
  analysis_interval: "1h"
  circuit_breaker:
    enabled: false
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	config, warnings, err := LoadLearningAgentConfig(tmpFile.Name())
	require.NoError(t, err)
	require.NotNil(t, config)

	// Should have warning about full autonomy without circuit breaker
	found := false
	for _, w := range warnings {
		if w == "autonomy_level is 'full' but circuit_breaker is disabled - this is risky" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected warning about full autonomy without circuit breaker")
}

func TestLoadLearningAgentConfig_EnvVarExpansion(t *testing.T) {
	// Set env var
	os.Setenv("TEST_SLACK_WEBHOOK", "https://hooks.slack.com/env-test")
	defer os.Unsetenv("TEST_SLACK_WEBHOOK")

	content := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: env-test
spec:
  analysis_interval: "1h"
  notifications:
    slack_webhook: ${TEST_SLACK_WEBHOOK}
`

	tmpFile, err := os.CreateTemp("", "learning-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(content)
	require.NoError(t, err)
	tmpFile.Close()

	config, _, err := LoadLearningAgentConfig(tmpFile.Name())
	require.NoError(t, err)

	assert.Equal(t, "https://hooks.slack.com/env-test", config.Notifications.SlackWebhook)
}

func TestParseAutonomyLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected loomv1.AutonomyLevel
	}{
		{"manual", loomv1.AutonomyLevel_AUTONOMY_MANUAL},
		{"MANUAL", loomv1.AutonomyLevel_AUTONOMY_MANUAL},
		{"AUTONOMY_MANUAL", loomv1.AutonomyLevel_AUTONOMY_MANUAL},
		{"human_approval", loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL},
		{"HUMAN_APPROVAL", loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL},
		{"AUTONOMY_HUMAN_APPROVAL", loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL},
		{"full", loomv1.AutonomyLevel_AUTONOMY_FULL},
		{"FULL", loomv1.AutonomyLevel_AUTONOMY_FULL},
		{"AUTONOMY_FULL", loomv1.AutonomyLevel_AUTONOMY_FULL},
		{"unknown", loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL}, // Default
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := parseAutonomyLevel(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseImpactLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected loomv1.ImpactLevel
	}{
		{"low", loomv1.ImpactLevel_IMPACT_LOW},
		{"LOW", loomv1.ImpactLevel_IMPACT_LOW},
		{"IMPACT_LOW", loomv1.ImpactLevel_IMPACT_LOW},
		{"medium", loomv1.ImpactLevel_IMPACT_MEDIUM},
		{"high", loomv1.ImpactLevel_IMPACT_HIGH},
		{"critical", loomv1.ImpactLevel_IMPACT_CRITICAL},
		{"unknown", loomv1.ImpactLevel_IMPACT_MEDIUM}, // Default
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := parseImpactLevel(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseAnalysisInterval(t *testing.T) {
	tests := []struct {
		interval string
		expected time.Duration
	}{
		{"1h", time.Hour},
		{"30m", 30 * time.Minute},
		{"15m", 15 * time.Minute},
		{"", time.Hour}, // Default
	}

	for _, tc := range tests {
		t.Run(tc.interval, func(t *testing.T) {
			config := &loomv1.LearningAgentConfig{
				AnalysisInterval: tc.interval,
			}
			result, err := ParseAnalysisInterval(config)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestParseCooldownPeriod(t *testing.T) {
	tests := []struct {
		name     string
		config   *loomv1.LearningAgentConfig
		expected time.Duration
	}{
		{
			name:     "no circuit breaker",
			config:   &loomv1.LearningAgentConfig{},
			expected: 30 * time.Minute, // Default
		},
		{
			name: "empty cooldown",
			config: &loomv1.LearningAgentConfig{
				CircuitBreaker: &loomv1.LearningCircuitBreakerConfig{},
			},
			expected: 30 * time.Minute, // Default
		},
		{
			name: "custom cooldown",
			config: &loomv1.LearningAgentConfig{
				CircuitBreaker: &loomv1.LearningCircuitBreakerConfig{
					CooldownPeriod: "15m",
				},
			},
			expected: 15 * time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParseCooldownPeriod(tc.config)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestToLearningAgentOptions(t *testing.T) {
	config := &loomv1.LearningAgentConfig{
		AutonomyLevel:    loomv1.AutonomyLevel_AUTONOMY_FULL,
		AnalysisInterval: "45m",
		CircuitBreaker: &loomv1.LearningCircuitBreakerConfig{
			FailureThreshold: 3,
			CooldownPeriod:   "20m",
		},
	}

	autonomy, interval, cb, err := ToLearningAgentOptions(config)
	require.NoError(t, err)

	assert.Equal(t, AutonomyFull, autonomy)
	assert.Equal(t, 45*time.Minute, interval)
	assert.Equal(t, 3, cb.threshold)
	assert.Equal(t, 20*time.Minute, cb.cooldownPeriod)
	assert.Equal(t, "closed", cb.state)
}

func TestLoadLearningAgentConfigs_Directory(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "learning-configs-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create two config files
	config1 := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: learner-1
spec:
  analysis_interval: "1h"
`
	config2 := `
apiVersion: loom/v1
kind: LearningAgentConfig
metadata:
  name: learner-2
spec:
  analysis_interval: "30m"
`

	err = os.WriteFile(filepath.Join(tmpDir, "learner-1.yaml"), []byte(config1), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "learner-2.yaml"), []byte(config2), 0644)
	require.NoError(t, err)

	// Load configs
	configs, err := LoadLearningAgentConfigs(tmpDir)
	require.NoError(t, err)
	assert.Len(t, configs, 2)

	// Verify configs loaded
	names := make([]string, len(configs))
	for i, c := range configs {
		names[i] = c.Name
	}
	assert.Contains(t, names, "learner-1")
	assert.Contains(t, names, "learner-2")
}
