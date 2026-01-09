// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"context"
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/evals/judges"
)

// Agent represents a generic agent interface for running evals
// This will be replaced with the actual agent interface once extracted
type Agent interface {
	// Execute runs the agent with the given input and returns the output
	Execute(ctx context.Context, input string) (*AgentResponse, error)
}

// AgentResponse represents the response from an agent execution
type AgentResponse struct {
	Output     string
	ToolsUsed  []string
	CostUsd    float64
	LatencyMs  int64
	TraceID    string
	Successful bool
	Error      string
}

// Runner executes eval suites against agents
type Runner struct {
	suite             *loomv1.EvalSuite
	agent             Agent
	store             *Store
	calculator        *MetricsCalculator
	judgeOrchestrator *judges.Orchestrator
	patternTracker    PatternTracker // Optional: for recording pattern effectiveness with judge metrics
}

// PatternTracker interface for recording pattern usage (optional dependency)
type PatternTracker interface {
	RecordUsage(
		ctx context.Context,
		patternName string,
		variant string,
		domain string,
		agentID string,
		success bool,
		costUSD float64,
		latency time.Duration,
		errorType string,
		llmProvider string,
		llmModel string,
		judgeResult *loomv1.EvaluateResponse,
	)
}

// NewRunner creates a new eval runner
func NewRunner(suite *loomv1.EvalSuite, agent Agent, store *Store, judgeOrchestrator *judges.Orchestrator) *Runner {
	return &Runner{
		suite:             suite,
		agent:             agent,
		store:             store,
		calculator:        NewMetricsCalculator(suite),
		judgeOrchestrator: judgeOrchestrator,
		patternTracker:    nil, // Optional, set via WithPatternTracker
	}
}

// WithPatternTracker adds a pattern tracker to the runner
func (r *Runner) WithPatternTracker(tracker PatternTracker) *Runner {
	r.patternTracker = tracker
	return r
}

// Run executes all test cases in the eval suite
func (r *Runner) Run(ctx context.Context) (*loomv1.EvalResult, error) {
	// Prepare test results slice
	testResults := make([]*loomv1.TestCaseResult, 0, len(r.suite.Spec.TestCases))

	// Run each test case
	for _, testCase := range r.suite.Spec.TestCases {
		result, err := r.runTestCase(ctx, testCase)
		if err != nil {
			return nil, fmt.Errorf("failed to run test case %s: %w", testCase.Name, err)
		}
		testResults = append(testResults, result)
	}

	// Create eval result
	evalResult := r.calculator.CreateEvalResult(
		r.suite.Metadata.Name,
		r.suite.Spec.AgentId,
		testResults,
	)

	// Save to store if provided
	if r.store != nil {
		id, err := r.store.Save(ctx, evalResult)
		if err != nil {
			return nil, fmt.Errorf("failed to save eval result: %w", err)
		}
		evalResult.Id = fmt.Sprintf("%d", id)
	}

	// Export to hawk if configured
	if r.suite.Spec.HawkExport {
		if err := ExportToHawk(ctx, evalResult, nil); err != nil {
			// Log error but don't fail the eval run
			// Hawk export is best-effort
			fmt.Printf("⚠️  Warning: Failed to export to Hawk: %v\n", err)
		}
	}

	return evalResult, nil
}

// runTestCase executes a single test case
func (r *Runner) runTestCase(ctx context.Context, testCase *loomv1.TestCase) (*loomv1.TestCaseResult, error) {
	// Apply timeout if specified
	if testCase.MaxLatencyMs > 0 {
		var cancel context.CancelFunc
		timeout := time.Duration(testCase.MaxLatencyMs) * time.Millisecond
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Execute agent
	response, err := r.agent.Execute(ctx, testCase.Input)

	// Handle execution errors
	if err != nil {
		return &loomv1.TestCaseResult{
			TestName:      testCase.Name,
			Passed:        false,
			FailureReason: fmt.Sprintf("agent execution failed: %v", err),
			ActualOutput:  "",
			ToolsUsed:     []string{},
			CostUsd:       0,
			LatencyMs:     0,
			TraceId:       "",
		}, nil
	}

	// If agent execution failed internally
	if !response.Successful {
		return &loomv1.TestCaseResult{
			TestName:      testCase.Name,
			Passed:        false,
			FailureReason: response.Error,
			ActualOutput:  response.Output,
			ToolsUsed:     response.ToolsUsed,
			CostUsd:       response.CostUsd,
			LatencyMs:     response.LatencyMs,
			TraceId:       response.TraceID,
		}, nil
	}

	// Validate result against expectations
	result := ValidateTestResult(
		testCase,
		response.Output,
		response.ToolsUsed,
		response.CostUsd,
		response.LatencyMs,
	)

	// Add trace ID
	result.TraceId = response.TraceID

	// Compare with golden file if specified
	if testCase.GoldenFile != "" && result.Passed {
		goldenResult, err := CompareWithGoldenFile(
			testCase.GoldenFile,
			response.Output,
			r.getGoldenThreshold(testCase),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to compare with golden file: %w", err)
		}

		result.GoldenResult = goldenResult
		if !goldenResult.Matched {
			result.Passed = false
			result.FailureReason = fmt.Sprintf("golden file mismatch (similarity: %.2f%%)", goldenResult.SimilarityScore*100)
		}
	}

	// Add multi-judge evaluation if configured
	if r.suite.Spec.MultiJudge != nil && r.judgeOrchestrator != nil {
		multiJudgeResult, err := r.evaluateWithJudges(ctx, testCase, response)
		if err != nil {
			// Log error but don't fail the test (best-effort)
			fmt.Printf("⚠️  Multi-judge evaluation failed: %v\n", err)
		} else {
			result.MultiJudgeResult = multiJudgeResult

			// Update pass/fail based on judge verdict
			if !multiJudgeResult.Passed {
				result.Passed = false
				result.FailureReason = fmt.Sprintf("Failed multi-judge evaluation: %s",
					multiJudgeResult.Explanation)
			}

			// Record judge metrics in pattern tracker if available
			if r.patternTracker != nil {
				r.recordJudgeMetrics(ctx, testCase, response, result, multiJudgeResult)
			}
		}
	}

	return result, nil
}

// recordJudgeMetrics records judge evaluation metrics to the pattern tracker
func (r *Runner) recordJudgeMetrics(
	ctx context.Context,
	testCase *loomv1.TestCase,
	response *AgentResponse,
	result *loomv1.TestCaseResult,
	judgeResult *loomv1.EvaluateResponse,
) {
	// Extract pattern name from test case context, or use agent_id as fallback
	patternName := r.suite.Spec.AgentId
	if pattern, ok := testCase.Context["pattern"]; ok {
		patternName = pattern
	}

	// Extract variant from context
	variant := "eval-test"
	if v, ok := testCase.Context["variant"]; ok {
		variant = v
	}

	// Extract domain from context or suite metadata
	domain := "evaluation"
	if d, ok := testCase.Context["domain"]; ok {
		domain = d
	} else if len(r.suite.Metadata.Labels) > 0 {
		if d, ok := r.suite.Metadata.Labels["domain"]; ok {
			domain = d
		}
	}

	// Extract LLM provider/model from context if available
	llmProvider := "unknown"
	llmModel := "unknown"
	if p, ok := testCase.Context["llm_provider"]; ok {
		llmProvider = p
	}
	if m, ok := testCase.Context["llm_model"]; ok {
		llmModel = m
	}

	// Record usage with judge metrics
	r.patternTracker.RecordUsage(
		ctx,
		patternName,
		variant,
		domain,
		r.suite.Spec.AgentId,
		result.Passed,
		response.CostUsd,
		time.Duration(response.LatencyMs)*time.Millisecond,
		result.FailureReason,
		llmProvider,
		llmModel,
		judgeResult,
	)
}

// evaluateWithJudges runs multi-judge evaluation on a test case result
func (r *Runner) evaluateWithJudges(ctx context.Context, testCase *loomv1.TestCase, response *AgentResponse) (*loomv1.EvaluateResponse, error) {
	// Build evaluation context
	evalCtx := &loomv1.EvaluationContext{
		AgentId:     r.suite.Spec.AgentId,
		SessionId:   "", // TODO: add session tracking
		Prompt:      testCase.Input,
		Response:    response.Output,
		PatternUsed: "", // TODO: extract from response if available
		ToolsUsed:   response.ToolsUsed,
		CostUsd:     response.CostUsd,
		LatencyMs:   response.LatencyMs,
		TraceId:     response.TraceID,
		Metadata:    testCase.Context,
	}

	// Build judge IDs from config
	judgeIds := make([]string, 0, len(r.suite.Spec.MultiJudge.Judges))
	for _, judgeConfig := range r.suite.Spec.MultiJudge.Judges {
		judgeIds = append(judgeIds, judgeConfig.Id)
	}

	// Build evaluate request
	evalReq := &loomv1.EvaluateRequest{
		Context:        evalCtx,
		JudgeIds:       judgeIds,
		Aggregation:    r.suite.Spec.MultiJudge.Aggregation,
		ExecutionMode:  r.suite.Spec.MultiJudge.ExecutionMode,
		ExportToHawk:   r.suite.Spec.MultiJudge.ExportToHawk,
		TimeoutSeconds: r.suite.Spec.MultiJudge.TimeoutSeconds,
		FailFast:       r.suite.Spec.MultiJudge.FailFast,
	}

	// Run multi-judge evaluation
	return r.judgeOrchestrator.Evaluate(ctx, evalReq)
}

// getGoldenThreshold returns the similarity threshold for golden file comparison
func (r *Runner) getGoldenThreshold(testCase *loomv1.TestCase) float64 {
	// Test case specific threshold takes precedence
	if testCase.GoldenSimilarityThreshold > 0 {
		return testCase.GoldenSimilarityThreshold
	}

	// Suite-level threshold
	if r.suite.Spec.GoldenFiles != nil && r.suite.Spec.GoldenFiles.SimilarityThreshold > 0 {
		return r.suite.Spec.GoldenFiles.SimilarityThreshold
	}

	// Default to 90% similarity
	return 0.90
}

// MockAgent is a simple mock agent for testing
type MockAgent struct {
	Response *AgentResponse
	Error    error
}

// Execute implements the Agent interface
func (m *MockAgent) Execute(ctx context.Context, input string) (*AgentResponse, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Response, nil
}

// NewMockAgent creates a new mock agent with a predefined response
func NewMockAgent(output string, toolsUsed []string) *MockAgent {
	return &MockAgent{
		Response: &AgentResponse{
			Output:     output,
			ToolsUsed:  toolsUsed,
			CostUsd:    0.10,
			LatencyMs:  1000,
			TraceID:    "mock-trace-123",
			Successful: true,
		},
	}
}
