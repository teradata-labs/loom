// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"gopkg.in/yaml.v3"
)

// EvalSuiteYAML represents the YAML structure for eval suite configuration
type EvalSuiteYAML struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   EvalMetadataYAML `yaml:"metadata"`
	Spec       EvalSpecYAML     `yaml:"spec"`
}

type EvalMetadataYAML struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

type EvalSpecYAML struct {
	AgentID        string               `yaml:"agent_id"`
	TestCases      []TestCaseYAML       `yaml:"test_cases"`
	Metrics        []string             `yaml:"metrics"`
	HawkExport     bool                 `yaml:"hawk_export"`
	GoldenFiles    GoldenFileConfigYAML `yaml:"golden_files"`
	TimeoutSeconds int                  `yaml:"timeout_seconds"`
	Comparison     ComparisonConfigYAML `yaml:"comparison"`
	MultiJudge     MultiJudgeConfigYAML `yaml:"multi_judge"`
}

type TestCaseYAML struct {
	Name                      string            `yaml:"name"`
	Input                     string            `yaml:"input"`
	ExpectedOutputContains    []string          `yaml:"expected_output_contains"`
	ExpectedOutputNotContains []string          `yaml:"expected_output_not_contains"`
	ExpectedOutputRegex       string            `yaml:"expected_output_regex"`
	ExpectedTools             []string          `yaml:"expected_tools"`
	MaxCostUSD                float64           `yaml:"max_cost_usd"`
	MaxLatencyMS              int               `yaml:"max_latency_ms"`
	Context                   map[string]string `yaml:"context"`
	GoldenFile                string            `yaml:"golden_file"`
}

type GoldenFileConfigYAML struct {
	Directory           string  `yaml:"directory"`
	UpdateOnMismatch    bool    `yaml:"update_on_mismatch"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
}

type ComparisonConfigYAML struct {
	BaselineAgentID   string   `yaml:"baseline_agent_id"`
	ComparisonMetrics []string `yaml:"comparison_metrics"`
}

type MultiJudgeConfigYAML struct {
	Parallel       bool              `yaml:"parallel"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	FailFast       bool              `yaml:"fail_fast"`
	Aggregation    string            `yaml:"aggregation"`
	ExecutionMode  string            `yaml:"execution_mode"`
	ExportToHawk   bool              `yaml:"export_to_hawk"`
	Judges         []JudgeConfigYAML `yaml:"judges"`
}

type JudgeConfigYAML struct {
	Name            string   `yaml:"name"`
	Criteria        string   `yaml:"criteria"`
	Weight          float64  `yaml:"weight"`
	MinPassingScore float64  `yaml:"min_passing_score"`
	Criticality     string   `yaml:"criticality"`
	Dimensions      []string `yaml:"dimensions"`
}

// LoadEvalSuite loads an eval suite from a YAML file
func LoadEvalSuite(path string) (*loomv1.EvalSuite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read eval suite file %s: %w", path, err)
	}

	// Expand environment variables
	dataStr := expandEnvVars(string(data))

	var yamlConfig EvalSuiteYAML
	if err := yaml.Unmarshal([]byte(dataStr), &yamlConfig); err != nil {
		return nil, fmt.Errorf("failed to parse eval suite YAML: %w", err)
	}

	// Validate structure
	if err := validateEvalSuiteYAML(&yamlConfig); err != nil {
		return nil, fmt.Errorf("invalid eval suite config: %w", err)
	}

	// Convert to proto
	suite := yamlToProtoEvalSuite(&yamlConfig)

	// Resolve file paths (make them absolute based on eval file location)
	evalDir := filepath.Dir(path)
	if err := resolveEvalFilePaths(suite, evalDir); err != nil {
		return nil, fmt.Errorf("failed to resolve file paths: %w", err)
	}

	return suite, nil
}

// validateEvalSuiteYAML validates the YAML structure
func validateEvalSuiteYAML(yaml *EvalSuiteYAML) error {
	if yaml.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if yaml.APIVersion != "loom/v1" {
		return fmt.Errorf("unsupported apiVersion: %s (expected: loom/v1)", yaml.APIVersion)
	}
	if yaml.Kind != "EvalSuite" {
		return fmt.Errorf("kind must be 'EvalSuite', got: %s", yaml.Kind)
	}
	if yaml.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if yaml.Spec.AgentID == "" {
		return fmt.Errorf("spec.agent_id is required")
	}
	if len(yaml.Spec.TestCases) == 0 {
		return fmt.Errorf("spec.test_cases must contain at least one test case")
	}

	// Validate test cases
	for i, tc := range yaml.Spec.TestCases {
		if tc.Name == "" {
			return fmt.Errorf("test_cases[%d].name is required", i)
		}
		if tc.Input == "" {
			return fmt.Errorf("test_cases[%d].input is required", i)
		}
		if tc.MaxCostUSD < 0 {
			return fmt.Errorf("test_cases[%d].max_cost_usd must be non-negative", i)
		}
		if tc.MaxLatencyMS < 0 {
			return fmt.Errorf("test_cases[%d].max_latency_ms must be non-negative", i)
		}
	}

	// Validate golden file config
	if yaml.Spec.GoldenFiles.SimilarityThreshold < 0 || yaml.Spec.GoldenFiles.SimilarityThreshold > 1 {
		return fmt.Errorf("golden_files.similarity_threshold must be between 0 and 1")
	}

	return nil
}

// yamlToProtoEvalSuite converts YAML to proto
func yamlToProtoEvalSuite(yaml *EvalSuiteYAML) *loomv1.EvalSuite {
	suite := &loomv1.EvalSuite{
		Metadata: &loomv1.EvalMetadata{
			Name:        yaml.Metadata.Name,
			Version:     yaml.Metadata.Version,
			Description: yaml.Metadata.Description,
			Labels:      yaml.Metadata.Labels,
		},
		Spec: &loomv1.EvalSpec{
			AgentId:        yaml.Spec.AgentID,
			Metrics:        yaml.Spec.Metrics,
			HawkExport:     yaml.Spec.HawkExport,
			TimeoutSeconds: int32(yaml.Spec.TimeoutSeconds),
		},
	}

	// Convert test cases
	for _, tc := range yaml.Spec.TestCases {
		suite.Spec.TestCases = append(suite.Spec.TestCases, &loomv1.TestCase{
			Name:                      tc.Name,
			Input:                     tc.Input,
			ExpectedOutputContains:    tc.ExpectedOutputContains,
			ExpectedOutputNotContains: tc.ExpectedOutputNotContains,
			ExpectedOutputRegex:       tc.ExpectedOutputRegex,
			ExpectedTools:             tc.ExpectedTools,
			MaxCostUsd:                tc.MaxCostUSD,
			MaxLatencyMs:              int32(tc.MaxLatencyMS),
			Context:                   tc.Context,
			GoldenFile:                tc.GoldenFile,
		})
	}

	// Convert golden file config
	suite.Spec.GoldenFiles = &loomv1.GoldenFileConfig{
		Directory:           yaml.Spec.GoldenFiles.Directory,
		UpdateOnMismatch:    yaml.Spec.GoldenFiles.UpdateOnMismatch,
		SimilarityThreshold: yaml.Spec.GoldenFiles.SimilarityThreshold,
	}

	// Set default similarity threshold
	if suite.Spec.GoldenFiles.SimilarityThreshold == 0 {
		suite.Spec.GoldenFiles.SimilarityThreshold = 0.9 // 90% similarity by default
	}

	// Convert comparison config
	if yaml.Spec.Comparison.BaselineAgentID != "" {
		suite.Spec.Comparison = &loomv1.ComparisonConfig{
			BaselineAgentId:   yaml.Spec.Comparison.BaselineAgentID,
			ComparisonMetrics: yaml.Spec.Comparison.ComparisonMetrics,
		}
	}

	// Convert multi-judge config
	if len(yaml.Spec.MultiJudge.Judges) > 0 {
		suite.Spec.MultiJudge = &loomv1.MultiJudgeConfig{
			Parallel:       yaml.Spec.MultiJudge.Parallel,
			TimeoutSeconds: int32(yaml.Spec.MultiJudge.TimeoutSeconds),
			FailFast:       yaml.Spec.MultiJudge.FailFast,
			Aggregation:    parseAggregationStrategy(yaml.Spec.MultiJudge.Aggregation),
			ExecutionMode:  parseExecutionMode(yaml.Spec.MultiJudge.ExecutionMode),
			ExportToHawk:   yaml.Spec.MultiJudge.ExportToHawk,
		}

		// Convert judges
		for _, judgeYAML := range yaml.Spec.MultiJudge.Judges {
			judge := &loomv1.JudgeConfig{
				Id:              judgeYAML.Name, // Use name as ID
				Name:            judgeYAML.Name,
				Criteria:        judgeYAML.Criteria,
				Weight:          judgeYAML.Weight,
				MinPassingScore: int32(judgeYAML.MinPassingScore),
				Criticality:     parseJudgeCriticality(judgeYAML.Criticality),
			}

			// Convert dimensions
			for _, dimStr := range judgeYAML.Dimensions {
				judge.Dimensions = append(judge.Dimensions, parseJudgeDimension(dimStr))
			}

			suite.Spec.MultiJudge.Judges = append(suite.Spec.MultiJudge.Judges, judge)
		}
	}

	// Set defaults
	if suite.Spec.TimeoutSeconds == 0 {
		suite.Spec.TimeoutSeconds = 600 // 10 minutes default
	}
	if len(suite.Spec.Metrics) == 0 {
		suite.Spec.Metrics = []string{"accuracy", "cost_efficiency", "latency"}
	}

	return suite
}

// parseAggregationStrategy converts string to enum
func parseAggregationStrategy(s string) loomv1.AggregationStrategy {
	switch s {
	case "AGGREGATION_STRATEGY_WEIGHTED_AVERAGE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	case "AGGREGATION_STRATEGY_ALL_MUST_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS
	case "AGGREGATION_STRATEGY_MAJORITY_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS
	case "AGGREGATION_STRATEGY_ANY_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS
	case "AGGREGATION_STRATEGY_MIN_SCORE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE
	case "AGGREGATION_STRATEGY_MAX_SCORE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE
	default:
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	}
}

// parseExecutionMode converts string to enum
func parseExecutionMode(s string) loomv1.ExecutionMode {
	switch s {
	case "EXECUTION_MODE_SYNCHRONOUS":
		return loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS
	case "EXECUTION_MODE_ASYNCHRONOUS":
		return loomv1.ExecutionMode_EXECUTION_MODE_ASYNCHRONOUS
	case "EXECUTION_MODE_HYBRID":
		return loomv1.ExecutionMode_EXECUTION_MODE_HYBRID
	default:
		return loomv1.ExecutionMode_EXECUTION_MODE_HYBRID
	}
}

// parseJudgeCriticality converts string to enum
func parseJudgeCriticality(s string) loomv1.JudgeCriticality {
	switch s {
	case "JUDGE_CRITICALITY_SAFETY_CRITICAL":
		return loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL
	case "JUDGE_CRITICALITY_CRITICAL":
		return loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL
	case "JUDGE_CRITICALITY_NON_CRITICAL":
		return loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL
	default:
		return loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL
	}
}

// parseJudgeDimension converts string to enum
func parseJudgeDimension(s string) loomv1.JudgeDimension {
	switch s {
	case "JUDGE_DIMENSION_QUALITY", "JUDGE_DIMENSION_CORRECTNESS":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY
	case "JUDGE_DIMENSION_COST":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_COST
	case "JUDGE_DIMENSION_SAFETY":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY
	case "JUDGE_DIMENSION_DOMAIN", "JUDGE_DIMENSION_DOMAIN_SPECIFIC":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_DOMAIN
	case "JUDGE_DIMENSION_PERFORMANCE":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_PERFORMANCE
	case "JUDGE_DIMENSION_USABILITY":
		return loomv1.JudgeDimension_JUDGE_DIMENSION_USABILITY
	default:
		return loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY
	}
}

// resolveEvalFilePaths makes all file paths absolute relative to eval directory
func resolveEvalFilePaths(suite *loomv1.EvalSuite, evalDir string) error {
	// Resolve golden file directory
	if suite.Spec.GoldenFiles != nil && suite.Spec.GoldenFiles.Directory != "" {
		suite.Spec.GoldenFiles.Directory = resolveRelativePath(evalDir, suite.Spec.GoldenFiles.Directory)
	}

	// Resolve golden file references in test cases
	for _, tc := range suite.Spec.TestCases {
		if tc.GoldenFile != "" {
			// If golden file is relative and golden files directory is set, resolve relative to that
			if suite.Spec.GoldenFiles != nil && suite.Spec.GoldenFiles.Directory != "" && !filepath.IsAbs(tc.GoldenFile) {
				tc.GoldenFile = filepath.Join(suite.Spec.GoldenFiles.Directory, tc.GoldenFile)
			} else {
				tc.GoldenFile = resolveRelativePath(evalDir, tc.GoldenFile)
			}
		}
	}

	return nil
}

// resolveRelativePath resolves a relative path to absolute
func resolveRelativePath(baseDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// expandEnvVars expands environment variables in YAML content
func expandEnvVars(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}

// ValidateEvalSuite validates an eval suite configuration
func ValidateEvalSuite(suite *loomv1.EvalSuite) error {
	if suite.Metadata == nil {
		return fmt.Errorf("eval suite metadata is required")
	}
	if suite.Metadata.Name == "" {
		return fmt.Errorf("eval suite name is required")
	}
	if suite.Spec == nil {
		return fmt.Errorf("eval suite spec is required")
	}
	if suite.Spec.AgentId == "" {
		return fmt.Errorf("agent_id is required")
	}
	if len(suite.Spec.TestCases) == 0 {
		return fmt.Errorf("at least one test case is required")
	}

	// Validate metrics
	validMetrics := map[string]bool{
		"accuracy":        true,
		"cost_efficiency": true,
		"latency":         true,
		"tool_usage":      true,
	}
	for _, metric := range suite.Spec.Metrics {
		if !validMetrics[strings.ToLower(metric)] {
			return fmt.Errorf("invalid metric: %s (must be: accuracy, cost_efficiency, latency, tool_usage)", metric)
		}
	}

	// Validate golden files if referenced
	for i, tc := range suite.Spec.TestCases {
		if tc.GoldenFile != "" {
			if _, err := os.Stat(tc.GoldenFile); os.IsNotExist(err) {
				// Golden file doesn't exist - only error if we're not in update mode
				if suite.Spec.GoldenFiles != nil && !suite.Spec.GoldenFiles.UpdateOnMismatch {
					return fmt.Errorf("test_cases[%d].golden_file not found: %s", i, tc.GoldenFile)
				}
			}
		}
	}

	return nil
}
