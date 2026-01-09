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
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"
)

var judgeCmd = &cobra.Command{
	Use:   "judge",
	Short: "Manage judge evaluation operations",
	Long: `Manage the Judge system - multi-dimensional LLM evaluation.

The judge system evaluates agent outputs across 6 dimensions:
- Quality: Accuracy, completeness, correctness
- Safety: Security, compliance, risk mitigation
- Cost: Token efficiency, API costs
- Domain: Domain-specific rules (SQL, legal, medical)
- Performance: Latency, throughput
- Usability: User experience, clarity

Use these commands to evaluate outputs, register judges, and view history.`,
}

var judgeEvaluateCmd = &cobra.Command{
	Use:   "evaluate",
	Short: "Evaluate agent output with judges",
	Long: `Evaluate an agent output using multiple judges.

Runs multi-judge evaluation with configurable aggregation strategies.
Useful for testing judge behavior, debugging criteria, and manual evaluation.

Examples:
  # Evaluate with quality and safety judges
  looms judge evaluate \
    --agent=sql-agent \
    --prompt="Generate a SELECT query" \
    --response="SELECT * FROM users" \
    --judges=quality-judge,safety-judge

  # With specific aggregation strategy
  looms judge evaluate \
    --agent=sql-agent \
    --prompt="Query for admins" \
    --response="SELECT * FROM users WHERE role='admin'" \
    --judges=quality-judge,safety-judge,cost-judge \
    --aggregation=weighted-average \
    --export-to-hawk

  # Read input/output from files
  looms judge evaluate \
    --agent=sql-agent \
    --prompt-file=input.txt \
    --response-file=output.txt \
    --judges=quality-judge,safety-judge`,
	Run: runJudgeEvaluate,
}

var judgeRegisterCmd = &cobra.Command{
	Use:   "register [config-file]",
	Short: "Register a new judge from YAML config",
	Long: `Register a new judge configuration.

Reads a YAML judge config file and registers it with the judge orchestrator.
Judges can then be used in evaluations, teleprompter optimization, and learning agent tuning.

Config Format:
  name: quality-judge
  criteria: "Evaluate SQL query accuracy and completeness"
  dimensions:
    - JUDGE_DIMENSION_QUALITY
  weight: 2.0
  min_passing_score: 80
  criticality: JUDGE_CRITICALITY_CRITICAL
  type: JUDGE_TYPE_HAWK
  model: claude-sonnet-4-5

Examples:
  # Register from YAML file
  looms judge register config/judges/quality-judge.yaml

  # Register and verify
  looms judge register config/judges/safety-judge.yaml
  looms judge history --judge=safety-judge`,
	Args: cobra.ExactArgs(1),
	Run:  runJudgeRegister,
}

var judgeEvaluateStreamCmd = &cobra.Command{
	Use:   "evaluate-stream",
	Short: "Evaluate agent output with judges (streaming)",
	Long: `Evaluate an agent output using multiple judges with real-time streaming progress.

Runs multi-judge evaluation with configurable aggregation strategies, streaming
progress updates for long-running evaluations (Phase 11).

Examples:
  # Stream evaluation progress with quality and safety judges
  looms judge evaluate-stream \
    --agent=sql-agent \
    --prompt="Generate a SELECT query" \
    --response="SELECT * FROM users" \
    --judges=quality-judge,safety-judge

  # With specific aggregation strategy
  looms judge evaluate-stream \
    --agent=sql-agent \
    --prompt="Query for admins" \
    --response="SELECT * FROM users WHERE role='admin'" \
    --judges=quality-judge,safety-judge,cost-judge \
    --aggregation=weighted-average \
    --export-to-hawk

  # Read input/output from files
  looms judge evaluate-stream \
    --agent=sql-agent \
    --prompt-file=input.txt \
    --response-file=output.txt \
    --judges=quality-judge,safety-judge`,
	Run: runJudgeEvaluateStream,
}

var judgeHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Get judge evaluation history",
	Long: `Retrieve historical judge evaluations with filters.

Shows past evaluations with dimension scores, verdicts, and reasoning.
Useful for debugging judge behavior and tracking evaluation trends.

Examples:
  # All evaluations
  looms judge history

  # Filter by agent
  looms judge history --agent=sql-agent

  # Filter by judge
  looms judge history --judge=quality-judge

  # Filter by pattern
  looms judge history --pattern=query_optimization

  # Time range filter
  looms judge history \
    --agent=sql-agent \
    --start-time=2025-12-01T00:00:00Z \
    --end-time=2025-12-10T23:59:59Z \
    --limit=50`,
	Run: runJudgeHistory,
}

var (
	judgeServer       string
	judgeTimeout      int
	judgeAgent        string
	judgePrompt       string
	judgePromptFile   string
	judgeResponse     string
	judgeResponseFile string
	judgeJudges       []string
	judgeAggregation  string
	judgeExportHawk   bool
	judgeFailFast     bool
	judgePattern      string
	judgeStartTime    string
	judgeEndTime      string
	judgeLimit        int32
	judgeOffset       int32

	// Phase 7: Retry config flags for judge register
	judgeRetryMaxAttempts               int
	judgeRetryInitialBackoffMs          int
	judgeRetryMaxBackoffMs              int
	judgeRetryBackoffMultiplier         float64
	judgeCircuitBreakerEnabled          bool
	judgeCircuitBreakerFailureThreshold int
	judgeCircuitBreakerResetTimeoutMs   int
	judgeCircuitBreakerSuccessThreshold int
)

func init() {
	rootCmd.AddCommand(judgeCmd)
	judgeCmd.AddCommand(judgeEvaluateCmd)
	judgeCmd.AddCommand(judgeEvaluateStreamCmd)
	judgeCmd.AddCommand(judgeRegisterCmd)
	judgeCmd.AddCommand(judgeHistoryCmd)

	// Global judge flags
	for _, cmd := range []*cobra.Command{judgeEvaluateCmd, judgeEvaluateStreamCmd, judgeRegisterCmd, judgeHistoryCmd} {
		cmd.Flags().StringVar(&judgeServer, "server", "localhost:60051", "Loom server address")
		cmd.Flags().IntVar(&judgeTimeout, "timeout", 60, "Request timeout in seconds")
	}

	// Evaluate command flags
	judgeEvaluateCmd.Flags().StringVar(&judgeAgent, "agent", "", "Agent ID (required)")
	judgeEvaluateCmd.Flags().StringVar(&judgePrompt, "prompt", "", "User prompt/input")
	judgeEvaluateCmd.Flags().StringVar(&judgePromptFile, "prompt-file", "", "Read prompt from file")
	judgeEvaluateCmd.Flags().StringVar(&judgeResponse, "response", "", "Agent response/output")
	judgeEvaluateCmd.Flags().StringVar(&judgeResponseFile, "response-file", "", "Read response from file")
	judgeEvaluateCmd.Flags().StringSliceVar(&judgeJudges, "judges", []string{}, "Judge IDs (comma-separated, required)")
	judgeEvaluateCmd.Flags().StringVar(&judgeAggregation, "aggregation", "weighted-average", "Aggregation strategy (weighted-average, all-must-pass, majority-pass)")
	judgeEvaluateCmd.Flags().BoolVar(&judgeExportHawk, "export-to-hawk", false, "Export results to Hawk")
	judgeEvaluateCmd.Flags().BoolVar(&judgeFailFast, "fail-fast", false, "Abort if any critical judge fails")
	judgeEvaluateCmd.Flags().StringVar(&judgePattern, "pattern", "", "Pattern used (optional)")

	_ = judgeEvaluateCmd.MarkFlagRequired("agent")
	_ = judgeEvaluateCmd.MarkFlagRequired("judges")

	// Evaluate-stream command flags (same as evaluate)
	judgeEvaluateStreamCmd.Flags().StringVar(&judgeAgent, "agent", "", "Agent ID (required)")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgePrompt, "prompt", "", "User prompt/input")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgePromptFile, "prompt-file", "", "Read prompt from file")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgeResponse, "response", "", "Agent response/output")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgeResponseFile, "response-file", "", "Read response from file")
	judgeEvaluateStreamCmd.Flags().StringSliceVar(&judgeJudges, "judges", []string{}, "Judge IDs (comma-separated, required)")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgeAggregation, "aggregation", "weighted-average", "Aggregation strategy (weighted-average, all-must-pass, majority-pass)")
	judgeEvaluateStreamCmd.Flags().BoolVar(&judgeExportHawk, "export-to-hawk", false, "Export results to Hawk")
	judgeEvaluateStreamCmd.Flags().BoolVar(&judgeFailFast, "fail-fast", false, "Abort if any critical judge fails")
	judgeEvaluateStreamCmd.Flags().StringVar(&judgePattern, "pattern", "", "Pattern used (optional)")

	_ = judgeEvaluateStreamCmd.MarkFlagRequired("agent")
	_ = judgeEvaluateStreamCmd.MarkFlagRequired("judges")

	// History command flags
	judgeHistoryCmd.Flags().StringVar(&judgeAgent, "agent", "", "Filter by agent ID")
	judgeHistoryCmd.Flags().StringVar(&judgePattern, "pattern", "", "Filter by pattern name")
	judgeHistoryCmd.Flags().StringSliceVar(&judgeJudges, "judges", []string{}, "Filter by judge ID(s)")
	judgeHistoryCmd.Flags().StringVar(&judgeStartTime, "start-time", "", "Start time (RFC3339 format)")
	judgeHistoryCmd.Flags().StringVar(&judgeEndTime, "end-time", "", "End time (RFC3339 format)")
	judgeHistoryCmd.Flags().Int32Var(&judgeLimit, "limit", 50, "Maximum number of results")
	judgeHistoryCmd.Flags().Int32Var(&judgeOffset, "offset", 0, "Offset for pagination")

	// Phase 7: Register command retry config flags
	judgeRegisterCmd.Flags().IntVar(&judgeRetryMaxAttempts, "max-attempts", 3, "Maximum retry attempts")
	judgeRegisterCmd.Flags().IntVar(&judgeRetryInitialBackoffMs, "initial-backoff-ms", 1000, "Initial backoff in milliseconds")
	judgeRegisterCmd.Flags().IntVar(&judgeRetryMaxBackoffMs, "max-backoff-ms", 8000, "Maximum backoff in milliseconds")
	judgeRegisterCmd.Flags().Float64Var(&judgeRetryBackoffMultiplier, "backoff-multiplier", 2.0, "Backoff multiplier")
	judgeRegisterCmd.Flags().BoolVar(&judgeCircuitBreakerEnabled, "circuit-breaker", true, "Enable circuit breaker")
	judgeRegisterCmd.Flags().IntVar(&judgeCircuitBreakerFailureThreshold, "circuit-breaker-failure-threshold", 5, "Circuit breaker failure threshold")
	judgeRegisterCmd.Flags().IntVar(&judgeCircuitBreakerResetTimeoutMs, "circuit-breaker-reset-timeout-ms", 60000, "Circuit breaker reset timeout in milliseconds")
	judgeRegisterCmd.Flags().IntVar(&judgeCircuitBreakerSuccessThreshold, "circuit-breaker-success-threshold", 2, "Circuit breaker success threshold")
}

// createJudgeClient creates a gRPC client for the JudgeService
func createJudgeClient(serverAddr string) (loomv1.JudgeServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to %s: %w", serverAddr, err)
	}

	client := loomv1.NewJudgeServiceClient(conn)
	return client, conn, nil
}

func runJudgeEvaluate(cmd *cobra.Command, args []string) {
	// Create client
	client, conn, err := createJudgeClient(judgeServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Read prompt
	prompt := judgePrompt
	if judgePromptFile != "" {
		data, err := os.ReadFile(judgePromptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading prompt file: %v\n", err)
			os.Exit(1)
		}
		prompt = string(data)
	}

	// Read response
	response := judgeResponse
	if judgeResponseFile != "" {
		data, err := os.ReadFile(judgeResponseFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading response file: %v\n", err)
			os.Exit(1)
		}
		response = string(data)
	}

	// Validate
	if prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: --prompt or --prompt-file is required\n")
		os.Exit(1)
	}
	if response == "" {
		fmt.Fprintf(os.Stderr, "Error: --response or --response-file is required\n")
		os.Exit(1)
	}

	// Parse aggregation strategy
	var aggregation loomv1.AggregationStrategy
	switch strings.ToLower(judgeAggregation) {
	case "weighted-average", "weighted":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	case "all-must-pass", "all":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS
	case "majority-pass", "majority":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS
	case "any-pass", "any":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS
	case "min-score", "min":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE
	case "max-score", "max":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE
	default:
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(judgeTimeout)*time.Second)
	defer cancel()

	// Build request
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:     judgeAgent,
			Prompt:      prompt,
			Response:    response,
			PatternUsed: judgePattern,
		},
		JudgeIds:       judgeJudges,
		Aggregation:    aggregation,
		ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		ExportToHawk:   judgeExportHawk,
		TimeoutSeconds: int32(judgeTimeout),
		FailFast:       judgeFailFast,
	}

	// Print header
	fmt.Printf("🔍 Evaluating with %d judges...\n", len(judgeJudges))
	fmt.Printf("   Agent: %s\n", judgeAgent)
	fmt.Printf("   Judges: %s\n", strings.Join(judgeJudges, ", "))
	fmt.Printf("   Aggregation: %s\n", judgeAggregation)
	if judgePattern != "" {
		fmt.Printf("   Pattern: %s\n", judgePattern)
	}
	fmt.Println()

	// Call evaluate
	resp, err := client.EvaluateWithJudges(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error evaluating: %v\n", err)
		os.Exit(1)
	}

	// Display results
	displayEvaluationResults(resp)
}

func displayEvaluationResults(resp *loomv1.EvaluateResponse) {
	// Overall verdict
	verdictIcon := "✅"
	if !resp.Passed {
		verdictIcon = "❌"
	}

	fmt.Printf("%s Overall Verdict: ", verdictIcon)
	if resp.Passed {
		fmt.Printf("PASS (score: %.1f/100)\n", resp.FinalScore)
	} else {
		fmt.Printf("FAIL (score: %.1f/100)\n", resp.FinalScore)
	}
	fmt.Println(strings.Repeat("─", 80))

	// Individual judge results
	fmt.Printf("\n📊 Judge Results (%d judges)\n\n", len(resp.Verdicts))
	for i, verdict := range resp.Verdicts {
		// Judge header
		icon := "✅"
		if verdict.Verdict == "FAIL" {
			icon = "❌"
		} else if verdict.Verdict == "PARTIAL" {
			icon = "⚠️"
		}

		fmt.Printf("[%d] %s %s (%s)\n", i+1, icon, verdict.JudgeName, verdict.JudgeModel)
		fmt.Printf("    Verdict: %s (score: %.1f/100)\n", verdict.Verdict, verdict.OverallScore)

		// Dimension scores
		if len(verdict.DimensionScores) > 0 {
			fmt.Printf("    Dimensions:\n")
			for dim, score := range verdict.DimensionScores {
				fmt.Printf("      - %s: %.1f/100\n", dim, score)
			}
		}

		// Reasoning
		if verdict.Reasoning != "" {
			fmt.Printf("    Reasoning: %s\n", verdict.Reasoning)
		}

		// Issues
		if len(verdict.Issues) > 0 {
			fmt.Printf("    Issues:\n")
			for _, issue := range verdict.Issues {
				fmt.Printf("      - %s\n", issue)
			}
		}

		// Suggestions
		if len(verdict.Suggestions) > 0 {
			fmt.Printf("    Suggestions:\n")
			for _, suggestion := range verdict.Suggestions {
				fmt.Printf("      - %s\n", suggestion)
			}
		}

		// Metrics
		fmt.Printf("    Cost: $%.4f | Latency: %dms\n", verdict.CostUsd, verdict.ExecutionTimeMs)
		fmt.Println()
	}

	// Dimension scores (aggregated)
	if len(resp.DimensionScores) > 0 {
		fmt.Println(strings.Repeat("─", 80))
		fmt.Printf("\n📈 Aggregated Dimension Scores\n\n")
		for dim, score := range resp.DimensionScores {
			fmt.Printf("   %s: %.1f/100\n", dim, score)
		}
		fmt.Println()
	}

	// Aggregated metrics
	if resp.Aggregated != nil {
		fmt.Println(strings.Repeat("─", 80))
		fmt.Printf("\n💰 Metrics\n\n")
		fmt.Printf("   Pass Rate: %.1f%% (%s)\n",
			resp.Aggregated.PassRate*100,
			resp.Aggregated.Strategy.String())
		fmt.Printf("   Score Range: %.1f - %.1f (avg: %.1f, σ: %.1f)\n",
			resp.Aggregated.MinScore,
			resp.Aggregated.MaxScore,
			resp.Aggregated.WeightedAverageScore,
			resp.Aggregated.ScoreStddev)
		fmt.Printf("   Total Cost: $%.4f\n", resp.Aggregated.TotalCostUsd)
		fmt.Printf("   Total Time: %dms\n", resp.Aggregated.TotalExecutionTimeMs)
		fmt.Println()
	}

	// Summary suggestions
	if len(resp.Suggestions) > 0 {
		fmt.Println(strings.Repeat("─", 80))
		fmt.Printf("\n💡 Suggestions for Improvement\n\n")
		for i, suggestion := range resp.Suggestions {
			fmt.Printf("   %d. %s\n", i+1, suggestion)
		}
		fmt.Println()
	}

	// Export status
	if resp.Metadata != nil && resp.Metadata.ExportedToHawk {
		fmt.Printf("✅ Results exported to Hawk\n")
	}
}

func runJudgeEvaluateStream(cmd *cobra.Command, args []string) {
	// Create client
	client, conn, err := createJudgeClient(judgeServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Read prompt
	prompt := judgePrompt
	if judgePromptFile != "" {
		data, err := os.ReadFile(judgePromptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading prompt file: %v\n", err)
			os.Exit(1)
		}
		prompt = string(data)
	}

	// Read response
	response := judgeResponse
	if judgeResponseFile != "" {
		data, err := os.ReadFile(judgeResponseFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading response file: %v\n", err)
			os.Exit(1)
		}
		response = string(data)
	}

	// Validate
	if prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: --prompt or --prompt-file is required\n")
		os.Exit(1)
	}
	if response == "" {
		fmt.Fprintf(os.Stderr, "Error: --response or --response-file is required\n")
		os.Exit(1)
	}

	// Parse aggregation strategy
	var aggregation loomv1.AggregationStrategy
	switch strings.ToLower(judgeAggregation) {
	case "weighted-average", "weighted":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	case "all-must-pass", "all":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS
	case "majority-pass", "majority":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS
	case "any-pass", "any":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS
	case "min-score", "min":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE
	case "max-score", "max":
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE
	default:
		aggregation = loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(judgeTimeout)*time.Second)
	defer cancel()

	// Build request
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:     judgeAgent,
			Prompt:      prompt,
			Response:    response,
			PatternUsed: judgePattern,
		},
		JudgeIds:       judgeJudges,
		Aggregation:    aggregation,
		ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		ExportToHawk:   judgeExportHawk,
		TimeoutSeconds: int32(judgeTimeout),
		FailFast:       judgeFailFast,
	}

	// Print header
	fmt.Printf("🔍 Streaming evaluation with %d judges...\n", len(judgeJudges))
	fmt.Printf("   Agent: %s\n", judgeAgent)
	fmt.Printf("   Judges: %s\n", strings.Join(judgeJudges, ", "))
	fmt.Printf("   Aggregation: %s\n", judgeAggregation)
	if judgePattern != "" {
		fmt.Printf("   Pattern: %s\n", judgePattern)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 80))

	// Call streaming evaluate
	stream, err := client.EvaluateWithJudgesStream(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error starting stream: %v\n", err)
		os.Exit(1)
	}

	// Receive and display progress
	var finalResult *loomv1.EvaluateResponse
	for {
		progress, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			fmt.Fprintf(os.Stderr, "\n❌ Error receiving progress: %v\n", err)
			os.Exit(1)
		}

		switch p := progress.Progress.(type) {
		case *loomv1.EvaluateProgress_JudgeStarted:
			fmt.Printf("⏳ Judge %s started (example %d)\n",
				p.JudgeStarted.JudgeId,
				p.JudgeStarted.ExampleNumber+1)

		case *loomv1.EvaluateProgress_JudgeCompleted:
			icon := "✅"
			if p.JudgeCompleted.Result != nil && p.JudgeCompleted.Result.Verdict != "PASS" {
				icon = "❌"
			}
			fmt.Printf("%s Judge %s completed (%.0fms, score: %.0f/100)\n",
				icon,
				p.JudgeCompleted.JudgeId,
				float64(p.JudgeCompleted.DurationMs),
				p.JudgeCompleted.Result.OverallScore)

		case *loomv1.EvaluateProgress_ExampleCompleted:
			icon := "✅"
			if !p.ExampleCompleted.Passed {
				icon = "❌"
			}
			fmt.Printf("%s Example %d/%d completed (score: %.0f/100)\n",
				icon,
				p.ExampleCompleted.ExampleNumber+1,
				p.ExampleCompleted.TotalExamples,
				p.ExampleCompleted.CurrentScore)
			fmt.Println()

		case *loomv1.EvaluateProgress_EvaluationCompleted:
			finalResult = p.EvaluationCompleted.FinalResult
			fmt.Println(strings.Repeat("─", 80))
			fmt.Printf("\n🎉 Evaluation completed! (%.0fms total)\n",
				float64(p.EvaluationCompleted.TotalDurationMs))
		}
	}

	if finalResult != nil {
		fmt.Println()
		displayEvaluationResults(finalResult)
	}
}

func runJudgeRegister(cmd *cobra.Command, args []string) {
	configPath := args[0]

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		os.Exit(1)
	}

	// Parse YAML
	var config loomv1.JudgeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing YAML: %v\n", err)
		os.Exit(1)
	}

	// Phase 7: Add retry config from CLI flags if provided
	if judgeRetryMaxAttempts > 0 {
		config.RetryConfig = &loomv1.RetryConfig{
			MaxAttempts:       int32(judgeRetryMaxAttempts),
			InitialBackoffMs:  int32(judgeRetryInitialBackoffMs),
			MaxBackoffMs:      int32(judgeRetryMaxBackoffMs),
			BackoffMultiplier: judgeRetryBackoffMultiplier,
			RetryOnStatus:     []int32{429, 500, 502, 503}, // Standard transient errors
		}

		// Add circuit breaker config if enabled
		if judgeCircuitBreakerEnabled {
			config.RetryConfig.CircuitBreaker = &loomv1.CircuitBreakerConfig{
				FailureThreshold: int32(judgeCircuitBreakerFailureThreshold),
				ResetTimeoutMs:   int32(judgeCircuitBreakerResetTimeoutMs),
				SuccessThreshold: int32(judgeCircuitBreakerSuccessThreshold),
				Enabled:          true,
			}
		}
	}

	// Create client
	client, conn, err := createJudgeClient(judgeServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(judgeTimeout)*time.Second)
	defer cancel()

	// Build request
	req := &loomv1.RegisterJudgeRequest{
		Config: &config,
	}

	// Print header
	fmt.Printf("📝 Registering judge: %s\n", config.Name)
	fmt.Printf("   Criteria: %s\n", config.Criteria)
	if len(config.Dimensions) > 0 {
		dims := []string{}
		for _, d := range config.Dimensions {
			dims = append(dims, d.String())
		}
		fmt.Printf("   Dimensions: %s\n", strings.Join(dims, ", "))
	}
	fmt.Printf("   Type: %s\n", config.Type.String())
	if config.Model != "" {
		fmt.Printf("   Model: %s\n", config.Model)
	}

	// Phase 7: Display retry config if present
	if config.RetryConfig != nil {
		fmt.Printf("   Retry Config:\n")
		fmt.Printf("     Max Attempts: %d\n", config.RetryConfig.MaxAttempts)
		fmt.Printf("     Initial Backoff: %dms\n", config.RetryConfig.InitialBackoffMs)
		fmt.Printf("     Max Backoff: %dms\n", config.RetryConfig.MaxBackoffMs)
		fmt.Printf("     Backoff Multiplier: %.1fx\n", config.RetryConfig.BackoffMultiplier)
		if config.RetryConfig.CircuitBreaker != nil && config.RetryConfig.CircuitBreaker.Enabled {
			fmt.Printf("     Circuit Breaker: Enabled (threshold: %d, reset: %dms)\n",
				config.RetryConfig.CircuitBreaker.FailureThreshold,
				config.RetryConfig.CircuitBreaker.ResetTimeoutMs)
		}
	}

	fmt.Println()

	// Call register
	resp, err := client.RegisterJudge(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error registering judge: %v\n", err)
		os.Exit(1)
	}

	// Display result
	fmt.Printf("✅ Judge registered successfully!\n\n")
	fmt.Printf("   ID: %s\n", resp.JudgeId)
	fmt.Printf("   Message: %s\n", resp.Message)
	fmt.Printf("\n💡 Test it: looms judge evaluate --agent=<agent-id> --judges=%s --prompt=\"...\" --response=\"...\"\n", config.Name)
}

func runJudgeHistory(cmd *cobra.Command, args []string) {
	// Create client
	client, conn, err := createJudgeClient(judgeServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(judgeTimeout)*time.Second)
	defer cancel()

	// Parse time filters
	var startTime, endTime *timestamppb.Timestamp
	if judgeStartTime != "" {
		t, err := time.Parse(time.RFC3339, judgeStartTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing start-time (use RFC3339 format): %v\n", err)
			os.Exit(1)
		}
		startTime = timestamppb.New(t)
	}
	if judgeEndTime != "" {
		t, err := time.Parse(time.RFC3339, judgeEndTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing end-time (use RFC3339 format): %v\n", err)
			os.Exit(1)
		}
		endTime = timestamppb.New(t)
	}

	// Build request
	req := &loomv1.GetJudgeHistoryRequest{
		AgentId:     judgeAgent,
		PatternName: judgePattern,
		StartTime:   startTime,
		EndTime:     endTime,
		Limit:       judgeLimit,
		Offset:      judgeOffset,
	}

	// Use first judge ID if provided (proto only supports single judge_id)
	if len(judgeJudges) > 0 {
		req.JudgeId = judgeJudges[0]
	}

	// Print header
	fmt.Printf("📜 Judge Evaluation History\n")
	if judgeAgent != "" {
		fmt.Printf("   Agent: %s\n", judgeAgent)
	}
	if judgePattern != "" {
		fmt.Printf("   Pattern: %s\n", judgePattern)
	}
	if len(judgeJudges) > 0 {
		fmt.Printf("   Judge: %s\n", judgeJudges[0])
	}
	if judgeStartTime != "" || judgeEndTime != "" {
		fmt.Printf("   Time Range: %s to %s\n", judgeStartTime, judgeEndTime)
	}
	fmt.Printf("   Limit: %d (offset: %d)\n\n", judgeLimit, judgeOffset)

	// Call history
	resp, err := client.GetJudgeHistory(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting history: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Evaluations) == 0 {
		fmt.Println("No evaluations found.")
		return
	}

	// Display history
	fmt.Printf("📊 History (%d of %d total)\n", len(resp.Evaluations), resp.TotalCount)
	fmt.Println(strings.Repeat("─", 80))

	for i, eval := range resp.Evaluations {
		// Evaluation header
		timestamp := eval.EvaluatedAt.AsTime().Format("2006-01-02 15:04:05")
		icon := "✅"
		if eval.Result != nil && !eval.Result.Passed {
			icon = "❌"
		}

		fmt.Printf("\n[%d] %s %s - %s\n", i+1, icon, eval.AgentId, timestamp)
		if eval.PatternName != "" {
			fmt.Printf("    Pattern: %s\n", eval.PatternName)
		}

		if eval.Result != nil {
			fmt.Printf("    Verdict: ")
			if eval.Result.Passed {
				fmt.Printf("PASS")
			} else {
				fmt.Printf("FAIL")
			}
			fmt.Printf(" (score: %.1f/100)\n", eval.Result.FinalScore)

			// Show judge count
			fmt.Printf("    Judges: %d", len(eval.Result.Verdicts))
			if eval.Result.Aggregated != nil {
				fmt.Printf(" (pass rate: %.1f%%)", eval.Result.Aggregated.PassRate*100)
			}
			fmt.Println()

			// Show dimension scores
			if len(eval.Result.DimensionScores) > 0 {
				fmt.Printf("    Dimensions: ")
				dims := []string{}
				for dim, score := range eval.Result.DimensionScores {
					dims = append(dims, fmt.Sprintf("%s=%.0f", dim, score))
				}
				fmt.Printf("%s\n", strings.Join(dims, ", "))
			}

			// Show cost/time
			if eval.Result.Aggregated != nil {
				fmt.Printf("    Cost: $%.4f | Time: %dms\n",
					eval.Result.Aggregated.TotalCostUsd,
					eval.Result.Aggregated.TotalExecutionTimeMs)
			}
		}
	}

	fmt.Println(strings.Repeat("─", 80))

	// Pagination hint
	if resp.TotalCount > int32(len(resp.Evaluations)) {
		remaining := resp.TotalCount - judgeOffset - int32(len(resp.Evaluations))
		fmt.Printf("\n💡 %d more evaluations available. Use --offset=%d to see more.\n",
			remaining, judgeOffset+int32(len(resp.Evaluations)))
	}
}
