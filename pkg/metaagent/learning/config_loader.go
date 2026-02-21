// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// LearningAgentConfigYAML represents the YAML structure for learning agent configuration.
// This mirrors the proto LearningAgentConfig but uses YAML-friendly types.
type LearningAgentConfigYAML struct {
	APIVersion string                      `yaml:"apiVersion"`
	Kind       string                      `yaml:"kind"`
	Metadata   LearningAgentMetadataYAML   `yaml:"metadata"`
	Spec       LearningAgentConfigSpecYAML `yaml:"spec"`
}

// LearningAgentMetadataYAML contains metadata for the learning agent config
type LearningAgentMetadataYAML struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

// LearningAgentConfigSpecYAML contains the learning agent specification
type LearningAgentConfigSpecYAML struct {
	Enabled           bool                     `yaml:"enabled"`
	AutonomyLevel     string                   `yaml:"autonomy_level"`
	AnalysisInterval  string                   `yaml:"analysis_interval"`
	WatchEvalSuites   []string                 `yaml:"watch_eval_suites"`
	Domains           []string                 `yaml:"domains"`
	CircuitBreaker    CircuitBreakerConfigYAML `yaml:"circuit_breaker"`
	ImprovementPolicy ImprovementPolicyYAML    `yaml:"improvement_policy"`
	Notifications     NotificationConfigYAML   `yaml:"notifications"`
}

// CircuitBreakerConfigYAML configures the circuit breaker
type CircuitBreakerConfigYAML struct {
	Enabled          bool   `yaml:"enabled"`
	FailureThreshold int32  `yaml:"failure_threshold"`
	CooldownPeriod   string `yaml:"cooldown_period"`
	SuccessThreshold int32  `yaml:"success_threshold"`
}

// ImprovementPolicyYAML defines improvement application rules
type ImprovementPolicyYAML struct {
	AutoApplyMinConfidence float64  `yaml:"auto_apply_min_confidence"`
	MaxDailyChanges        int32    `yaml:"max_daily_changes"`
	ProtectedAgents        []string `yaml:"protected_agents"`
	AllowedChangeTypes     []string `yaml:"allowed_change_types"`
	MaxAutoApplyImpact     string   `yaml:"max_auto_apply_impact"`
}

// NotificationConfigYAML defines notification settings
type NotificationConfigYAML struct {
	SlackWebhook   string   `yaml:"slack_webhook"`
	EmailAddresses []string `yaml:"email_addresses"`
	NotifyOn       []string `yaml:"notify_on"`
	IncludeDetails bool     `yaml:"include_details"`
}

// LoadLearningAgentConfig loads a learning agent configuration from a YAML file
func LoadLearningAgentConfig(path string) (*loomv1.LearningAgentConfig, []string, error) {
	path = filepath.Clean(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read learning agent config file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := expandEnvVars(string(data))

	var yamlConfig LearningAgentConfigYAML
	if err := yaml.Unmarshal([]byte(dataStr), &yamlConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to parse learning agent config YAML: %w", err)
	}

	// Validate structure
	warnings, err := validateLearningAgentConfigYAML(&yamlConfig)
	if err != nil {
		return nil, warnings, fmt.Errorf("invalid learning agent config: %w", err)
	}

	// Convert to proto
	config := yamlToProtoLearningAgentConfig(&yamlConfig)

	// Resolve relative paths for eval suites
	configDir := filepath.Dir(path)
	for i, suitePath := range config.WatchEvalSuites {
		if !filepath.IsAbs(suitePath) {
			config.WatchEvalSuites[i] = filepath.Join(configDir, suitePath)
		}
	}

	return config, warnings, nil
}

// LoadLearningAgentConfigs loads all learning agent configurations from a directory
func LoadLearningAgentConfigs(dir string) ([]*loomv1.LearningAgentConfig, error) {
	pattern := filepath.Join(dir, "*.yaml")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob learning agent configs: %w", err)
	}

	var configs []*loomv1.LearningAgentConfig
	for _, file := range files {
		config, _, err := LoadLearningAgentConfig(file)
		if err != nil {
			// Log warning but continue loading other configs
			continue
		}
		// Warnings are non-fatal, config is still usable
		configs = append(configs, config)
	}

	return configs, nil
}

// validateLearningAgentConfigYAML validates the YAML structure
func validateLearningAgentConfigYAML(yaml *LearningAgentConfigYAML) ([]string, error) {
	var warnings []string

	if yaml.APIVersion == "" {
		return warnings, fmt.Errorf("apiVersion is required")
	}
	if yaml.APIVersion != "loom/v1" {
		return warnings, fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", yaml.APIVersion)
	}
	if yaml.Kind != "LearningAgentConfig" {
		return warnings, fmt.Errorf("kind must be 'LearningAgentConfig', got: %s", yaml.Kind)
	}
	if yaml.Metadata.Name == "" {
		return warnings, fmt.Errorf("metadata.name is required")
	}

	// Validate autonomy level
	if yaml.Spec.AutonomyLevel != "" {
		validLevels := map[string]bool{
			"manual":                  true,
			"human_approval":          true,
			"full":                    true,
			"AUTONOMY_MANUAL":         true,
			"AUTONOMY_HUMAN_APPROVAL": true,
			"AUTONOMY_FULL":           true,
		}
		if !validLevels[yaml.Spec.AutonomyLevel] {
			return warnings, fmt.Errorf("invalid autonomy_level: %s (expected: manual, human_approval, or full)", yaml.Spec.AutonomyLevel)
		}
	}

	// Validate analysis interval
	if yaml.Spec.AnalysisInterval != "" {
		if _, err := parseDuration(yaml.Spec.AnalysisInterval); err != nil {
			return warnings, fmt.Errorf("invalid analysis_interval: %s (%w)", yaml.Spec.AnalysisInterval, err)
		}
	}

	// Validate circuit breaker cooldown period
	if yaml.Spec.CircuitBreaker.CooldownPeriod != "" {
		if _, err := parseDuration(yaml.Spec.CircuitBreaker.CooldownPeriod); err != nil {
			return warnings, fmt.Errorf("invalid circuit_breaker.cooldown_period: %s (%w)", yaml.Spec.CircuitBreaker.CooldownPeriod, err)
		}
	}

	// Validate improvement policy
	if yaml.Spec.ImprovementPolicy.AutoApplyMinConfidence < 0 || yaml.Spec.ImprovementPolicy.AutoApplyMinConfidence > 1 {
		warnings = append(warnings, fmt.Sprintf("auto_apply_min_confidence %.2f is outside 0-1 range, will be clamped", yaml.Spec.ImprovementPolicy.AutoApplyMinConfidence))
	}

	// Validate max_auto_apply_impact
	if yaml.Spec.ImprovementPolicy.MaxAutoApplyImpact != "" {
		validImpacts := map[string]bool{
			"low":             true,
			"medium":          true,
			"high":            true,
			"critical":        true,
			"IMPACT_LOW":      true,
			"IMPACT_MEDIUM":   true,
			"IMPACT_HIGH":     true,
			"IMPACT_CRITICAL": true,
		}
		if !validImpacts[yaml.Spec.ImprovementPolicy.MaxAutoApplyImpact] {
			warnings = append(warnings, fmt.Sprintf("unknown max_auto_apply_impact: %s, defaulting to IMPACT_MEDIUM", yaml.Spec.ImprovementPolicy.MaxAutoApplyImpact))
		}
	}

	// Warn about full autonomy without circuit breaker
	if (yaml.Spec.AutonomyLevel == "full" || yaml.Spec.AutonomyLevel == "AUTONOMY_FULL") && !yaml.Spec.CircuitBreaker.Enabled {
		warnings = append(warnings, "autonomy_level is 'full' but circuit_breaker is disabled - this is risky")
	}

	// Warn about empty watch_eval_suites
	if len(yaml.Spec.WatchEvalSuites) == 0 {
		warnings = append(warnings, "no watch_eval_suites specified - learning agent will not receive judge feedback")
	}

	return warnings, nil
}

// yamlToProtoLearningAgentConfig converts YAML config to proto
func yamlToProtoLearningAgentConfig(yaml *LearningAgentConfigYAML) *loomv1.LearningAgentConfig {
	config := &loomv1.LearningAgentConfig{
		Name:             yaml.Metadata.Name,
		Description:      yaml.Metadata.Description,
		Enabled:          yaml.Spec.Enabled,
		AutonomyLevel:    parseAutonomyLevel(yaml.Spec.AutonomyLevel),
		AnalysisInterval: yaml.Spec.AnalysisInterval,
		WatchEvalSuites:  yaml.Spec.WatchEvalSuites,
		Domains:          yaml.Spec.Domains,
		Metadata:         yaml.Metadata.Labels,
	}

	// Set defaults for enabled if not explicitly set
	// YAML bool defaults to false, but we want enabled=true by default
	// We check if the YAML explicitly set enabled: false vs not setting it
	if !yaml.Spec.Enabled && yaml.Spec.AnalysisInterval == "" && len(yaml.Spec.WatchEvalSuites) == 0 {
		// Likely a minimal config, don't override
	} else if yaml.Spec.AnalysisInterval != "" || len(yaml.Spec.WatchEvalSuites) > 0 {
		// Config has substance, default enabled to true if not set
		config.Enabled = true
	}
	// Explicit enabled: false in YAML will be respected

	// Circuit breaker config
	config.CircuitBreaker = &loomv1.LearningCircuitBreakerConfig{
		Enabled:          yaml.Spec.CircuitBreaker.Enabled,
		FailureThreshold: yaml.Spec.CircuitBreaker.FailureThreshold,
		CooldownPeriod:   yaml.Spec.CircuitBreaker.CooldownPeriod,
		SuccessThreshold: yaml.Spec.CircuitBreaker.SuccessThreshold,
	}

	// Apply circuit breaker defaults
	if config.CircuitBreaker.FailureThreshold == 0 {
		config.CircuitBreaker.FailureThreshold = 5
	}
	if config.CircuitBreaker.CooldownPeriod == "" {
		config.CircuitBreaker.CooldownPeriod = "30m"
	}
	if config.CircuitBreaker.SuccessThreshold == 0 {
		config.CircuitBreaker.SuccessThreshold = 3
	}

	// Improvement policy
	config.ImprovementPolicy = &loomv1.ImprovementPolicy{
		AutoApplyMinConfidence: yaml.Spec.ImprovementPolicy.AutoApplyMinConfidence,
		MaxDailyChanges:        yaml.Spec.ImprovementPolicy.MaxDailyChanges,
		ProtectedAgents:        yaml.Spec.ImprovementPolicy.ProtectedAgents,
		AllowedChangeTypes:     yaml.Spec.ImprovementPolicy.AllowedChangeTypes,
		MaxAutoApplyImpact:     parseImpactLevel(yaml.Spec.ImprovementPolicy.MaxAutoApplyImpact),
	}

	// Apply improvement policy defaults
	if config.ImprovementPolicy.AutoApplyMinConfidence == 0 {
		config.ImprovementPolicy.AutoApplyMinConfidence = 0.8
	}
	if config.ImprovementPolicy.MaxDailyChanges == 0 {
		config.ImprovementPolicy.MaxDailyChanges = 10
	}
	if config.ImprovementPolicy.MaxAutoApplyImpact == loomv1.ImpactLevel_IMPACT_LEVEL_UNSPECIFIED {
		config.ImprovementPolicy.MaxAutoApplyImpact = loomv1.ImpactLevel_IMPACT_MEDIUM
	}

	// Notification config
	config.Notifications = &loomv1.NotificationConfig{
		SlackWebhook:   yaml.Spec.Notifications.SlackWebhook,
		EmailAddresses: yaml.Spec.Notifications.EmailAddresses,
		NotifyOn:       yaml.Spec.Notifications.NotifyOn,
		IncludeDetails: yaml.Spec.Notifications.IncludeDetails,
	}

	// Default to include details
	if !yaml.Spec.Notifications.IncludeDetails && len(yaml.Spec.Notifications.NotifyOn) > 0 {
		config.Notifications.IncludeDetails = true
	}

	// Apply analysis interval default
	if config.AnalysisInterval == "" {
		config.AnalysisInterval = "1h"
	}

	return config
}

// parseAutonomyLevel converts string to proto enum
func parseAutonomyLevel(s string) loomv1.AutonomyLevel {
	switch strings.ToLower(s) {
	case "manual", "autonomy_manual":
		return loomv1.AutonomyLevel_AUTONOMY_MANUAL
	case "human_approval", "autonomy_human_approval":
		return loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL
	case "full", "autonomy_full":
		return loomv1.AutonomyLevel_AUTONOMY_FULL
	default:
		return loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL // Safe default
	}
}

// parseImpactLevel converts string to proto enum
func parseImpactLevel(s string) loomv1.ImpactLevel {
	switch strings.ToLower(s) {
	case "low", "impact_low":
		return loomv1.ImpactLevel_IMPACT_LOW
	case "medium", "impact_medium":
		return loomv1.ImpactLevel_IMPACT_MEDIUM
	case "high", "impact_high":
		return loomv1.ImpactLevel_IMPACT_HIGH
	case "critical", "impact_critical":
		return loomv1.ImpactLevel_IMPACT_CRITICAL
	default:
		return loomv1.ImpactLevel_IMPACT_MEDIUM // Safe default
	}
}

// parseDuration parses a duration string like "1h", "30m", "15m"
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// expandEnvVars expands environment variables in the format ${VAR} or $VAR
func expandEnvVars(s string) string {
	return os.ExpandEnv(s)
}

// ParseAnalysisInterval parses the analysis interval from config to time.Duration
func ParseAnalysisInterval(config *loomv1.LearningAgentConfig) (time.Duration, error) {
	if config.AnalysisInterval == "" {
		return time.Hour, nil // Default
	}
	return parseDuration(config.AnalysisInterval)
}

// ParseCooldownPeriod parses the circuit breaker cooldown from config to time.Duration
func ParseCooldownPeriod(config *loomv1.LearningAgentConfig) (time.Duration, error) {
	if config.CircuitBreaker == nil || config.CircuitBreaker.CooldownPeriod == "" {
		return 30 * time.Minute, nil // Default
	}
	return parseDuration(config.CircuitBreaker.CooldownPeriod)
}

// ToLearningAgentOptions converts proto config to LearningAgent constructor options
func ToLearningAgentOptions(config *loomv1.LearningAgentConfig) (AutonomyLevel, time.Duration, *CircuitBreaker, error) {
	// Parse autonomy level
	var autonomy AutonomyLevel
	switch config.AutonomyLevel {
	case loomv1.AutonomyLevel_AUTONOMY_MANUAL:
		autonomy = AutonomyManual
	case loomv1.AutonomyLevel_AUTONOMY_HUMAN_APPROVAL:
		autonomy = AutonomyHumanApproval
	case loomv1.AutonomyLevel_AUTONOMY_FULL:
		autonomy = AutonomyFull
	default:
		autonomy = AutonomyHumanApproval
	}

	// Parse analysis interval
	interval, err := ParseAnalysisInterval(config)
	if err != nil {
		return autonomy, 0, nil, fmt.Errorf("invalid analysis_interval: %w", err)
	}

	// Parse circuit breaker config
	cooldown, err := ParseCooldownPeriod(config)
	if err != nil {
		return autonomy, 0, nil, fmt.Errorf("invalid cooldown_period: %w", err)
	}

	cb := &CircuitBreaker{
		threshold:      5,
		cooldownPeriod: cooldown,
		state:          "closed",
	}

	if config.CircuitBreaker != nil {
		if config.CircuitBreaker.FailureThreshold > 0 {
			cb.threshold = int(config.CircuitBreaker.FailureThreshold)
		}
	}

	return autonomy, interval, cb, nil
}
