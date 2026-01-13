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

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/evals"
	"github.com/teradata-labs/loom/pkg/evals/judges"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
	_ "modernc.org/sqlite" // SQLite driver
)

var (
	evalStoreDB string
	evalAgentID string
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Run and manage evaluation suites",
	Long: `Run evaluation suites and manage eval results.

Evaluation suites test agent quality with golden datasets.
Results are stored in SQLite for tracking and comparison.

Examples:
  # Run an eval suite
  looms eval run examples/dogfooding/config-loader-eval.yaml

  # Run with custom thread ID
  looms eval run --thread my-thread suite.yaml

  # List recent eval runs
  looms eval list

  # Show specific run details
  looms eval show <run-id>

  # List runs for specific suite
  looms eval list --suite config-loader-quality`,
}

var evalRunCmd = &cobra.Command{
	Use:   "run <suite-file>",
	Short: "Run an evaluation suite",
	Long: `Run an evaluation suite against an agent.

The suite file must be a valid EvalSuite YAML file (kind: EvalSuite).

Flags:
  --thread    Thread ID to use (default: suite's agent_id)
  --store     SQLite database path (default: ./evals.db)

Examples:
  looms eval run examples/dogfooding/config-loader-eval.yaml
  looms eval run --thread custom-thread --store ./my-evals.db suite.yaml`,
	Args: cobra.ExactArgs(1),
	Run:  runEval,
}

var evalListCmd = &cobra.Command{
	Use:   "list",
	Short: "List evaluation runs",
	Long: `List recent evaluation runs from the store.

Flags:
  --suite     Filter by suite name
  --store     SQLite database path (default: ./evals.db)
  --limit     Maximum number of results (default: 10)

Examples:
  looms eval list
  looms eval list --suite config-loader-quality
  looms eval list --limit 20`,
	Run: runEvalList,
}

var evalShowCmd = &cobra.Command{
	Use:   "show <run-id>",
	Short: "Show evaluation run details",
	Long: `Show detailed results for a specific evaluation run.

Flags:
  --store     SQLite database path (default: ./evals.db)

Examples:
  looms eval show abc123`,
	Args: cobra.ExactArgs(1),
	Run:  runEvalShow,
}

func init() {
	rootCmd.AddCommand(evalCmd)
	evalCmd.AddCommand(evalRunCmd)
	evalCmd.AddCommand(evalListCmd)
	evalCmd.AddCommand(evalShowCmd)

	// Flags for run command
	evalRunCmd.Flags().StringVar(&evalAgentID, "thread", "", "Thread ID to use (default: suite's agent_id)")
	evalRunCmd.Flags().StringVar(&evalStoreDB, "store", "./evals.db", "SQLite database path")

	// Flags for list command
	evalListCmd.Flags().StringVar(&evalStoreDB, "store", "./evals.db", "SQLite database path")
	evalListCmd.Flags().String("suite", "", "Filter by suite name")
	evalListCmd.Flags().Int("limit", 10, "Maximum number of results")

	// Flags for show command
	evalShowCmd.Flags().StringVar(&evalStoreDB, "store", "./evals.db", "SQLite database path")
}

func runEval(cmd *cobra.Command, args []string) {
	suitePath := args[0]

	// Load eval suite
	fmt.Printf("📄 Loading eval suite: %s\n", suitePath)
	suite, err := evals.LoadEvalSuite(suitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to load eval suite: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("   Suite: %s\n", suite.Metadata.Name)
	fmt.Printf("   Tests: %d\n", len(suite.Spec.TestCases))
	fmt.Println()

	// Create LLM provider
	llmProvider, providerName := createLLMProviderForEval()
	fmt.Printf("🤖 LLM Provider: %s\n", providerName)
	fmt.Printf("   Model: %s\n", llmProvider.Model())
	fmt.Println()

	// Create agent (using mock backend for CLI)
	agentID := evalAgentID
	if agentID == "" {
		agentID = suite.Spec.AgentId
	}

	fmt.Printf("🔧 Creating agent: %s\n", agentID)
	backend := &mockBackend{}
	ag := agent.NewAgent(backend, llmProvider, agent.WithName(agentID))

	// Wrap agent to implement evals.Agent interface
	evalAgent := &agentWrapper{agent: ag}
	fmt.Println()

	// Create eval store
	store, err := evals.NewStore(evalStoreDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create eval store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Create judge orchestrator if multi-judge config exists
	var judgeOrch *judges.Orchestrator
	if suite.Spec.MultiJudge != nil && len(suite.Spec.MultiJudge.Judges) > 0 {
		fmt.Printf("🎯 Configuring %d judges for multi-dimensional evaluation\n", len(suite.Spec.MultiJudge.Judges))
		fmt.Printf("   Aggregation: %s\n", suite.Spec.MultiJudge.Aggregation)
		fmt.Printf("   Execution: %s\n", suite.Spec.MultiJudge.ExecutionMode)
		fmt.Println()

		// Create tracer for judge orchestration
		tracer := observability.NewNoOpTracer() // TODO: Use real tracer if configured

		// Create logger
		logger, _ := zap.NewDevelopment()

		// Create judge registry
		registry := judges.NewRegistry()

		// Create and register judges
		for _, judgeConfig := range suite.Spec.MultiJudge.Judges {
			judge, err := judges.NewLLMJudge(llmProvider, judgeConfig, tracer)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Failed to create judge %s: %v\n", judgeConfig.Name, err)
				continue
			}
			if err := registry.Register(judge); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Failed to register judge %s: %v\n", judgeConfig.Name, err)
				continue
			}
			fmt.Printf("   ✓ %s (criticality: %s, weight: %.1f)\n",
				judgeConfig.Name, judgeConfig.Criticality, judgeConfig.Weight)
		}
		fmt.Println()

		// Create orchestrator
		judgeOrch = judges.NewOrchestrator(&judges.Config{
			Registry: registry,
			Tracer:   tracer,
			Logger:   logger,
		})
	}

	// Create runner with judge orchestrator
	runner := evals.NewRunner(suite, evalAgent, store, judgeOrch)

	// Create pattern tracker for judge metrics if multi-judge is configured
	var patternTracker *learning.PatternEffectivenessTracker
	if suite.Spec.MultiJudge != nil && judgeOrch != nil {
		// Open database connection for pattern tracking
		db, err := sql.Open("sqlite", evalStoreDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Warning: Failed to open database for pattern tracking: %v\n", err)
			fmt.Fprintf(os.Stderr, "    Judge metrics will not be recorded for learning loop\n")
		} else {
			// Initialize schema for pattern tracking
			ctx := context.Background()
			if err := learning.InitSelfImprovementSchema(ctx, db, observability.NewNoOpTracer()); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Warning: Failed to initialize pattern tracking schema: %v\n", err)
				fmt.Fprintf(os.Stderr, "    Judge metrics will not be recorded for learning loop\n")
			} else {
				// Create pattern tracker (windowSize=1h, flushInterval=5m)
				patternTracker = learning.NewPatternEffectivenessTracker(
					db,
					observability.NewNoOpTracer(),
					nil, // No message bus for CLI
					1*time.Hour,
					5*time.Minute,
				)

				// Start the pattern tracker background goroutine
				if err := patternTracker.Start(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️  Warning: Failed to start pattern tracker: %v\n", err)
					patternTracker = nil
				}

				if patternTracker != nil {
					runner = runner.WithPatternTracker(patternTracker)
				}
				fmt.Printf("✓ Pattern tracker enabled (judge metrics will be recorded)\n")
				fmt.Println()
			}
		}
	}

	// Run evaluation
	fmt.Println("⚡ Running evaluation...")
	fmt.Println()
	startTime := time.Now()

	ctx := context.Background()
	result, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Evaluation failed: %v\n", err)
		os.Exit(1)
	}

	duration := time.Since(startTime)

	// Flush pattern tracker to ensure judge metrics are written to database
	if patternTracker != nil {
		if err := patternTracker.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Warning: Failed to flush pattern tracker: %v\n", err)
		}
	}

	// Print results
	fmt.Println("=" + "==========================================================")
	fmt.Println("✅ EVALUATION COMPLETED")
	fmt.Println("=" + "==========================================================")
	fmt.Println()

	fmt.Printf("Suite:     %s\n", result.SuiteName)
	fmt.Printf("Agent:     %s\n", result.AgentId)
	fmt.Printf("Duration:  %.2fs\n", duration.Seconds())
	fmt.Println()

	fmt.Printf("Results:\n")
	fmt.Printf("  Accuracy:  %.2f%%\n", result.Overall.Accuracy*100)
	fmt.Printf("  Passed:    %d/%d\n", result.Overall.PassedTests, result.Overall.TotalTests)
	fmt.Printf("  Failed:    %d/%d\n", result.Overall.FailedTests, result.Overall.TotalTests)
	fmt.Println()

	fmt.Printf("Performance:\n")
	fmt.Printf("  Total Latency: %.3fs\n", float64(result.Overall.TotalLatencyMs)/1000.0)
	fmt.Printf("  Total Cost:    $%.4f\n", result.Overall.TotalCostUsd)
	fmt.Println()

	// Show test results
	fmt.Println("Test Results:")
	for i, testResult := range result.TestResults {
		status := "✓"
		if !testResult.Passed {
			status = "✗"
		}
		fmt.Printf("  %s Test %d: %s (%.3fs, $%.4f)\n",
			status, i+1, testResult.TestName,
			float64(testResult.LatencyMs)/1000.0,
			testResult.CostUsd)
		if !testResult.Passed {
			fmt.Printf("    Reason: %s\n", testResult.FailureReason)
		}

		// Show multi-judge results if available
		if testResult.MultiJudgeResult != nil {
			mjr := testResult.MultiJudgeResult
			verdict := "PASS"
			if !mjr.Passed {
				verdict = "FAIL"
			}
			fmt.Printf("    Judge Verdict: %s (score: %.1f/100)\n",
				verdict, mjr.FinalScore)
			if mjr.Aggregated != nil {
				fmt.Printf("    Pass Rate: %.1f%% | Weighted Avg: %.1f | Min: %.1f | Max: %.1f\n",
					mjr.Aggregated.PassRate*100,
					mjr.Aggregated.WeightedAverageScore,
					mjr.Aggregated.MinScore,
					mjr.Aggregated.MaxScore)
			}
			// Show individual judge results
			if len(mjr.Verdicts) > 0 {
				fmt.Printf("    Individual Judges:\n")
				for _, jr := range mjr.Verdicts {
					judgeStatus := "✓"
					if jr.Verdict != "PASS" {
						judgeStatus = "✗"
					}
					fmt.Printf("      %s %s: %.1f/100 (%s)\n",
						judgeStatus, jr.JudgeName, jr.OverallScore, jr.Verdict)
				}
			}
		}
	}
	fmt.Println()

	fmt.Printf("Results saved to: %s\n", evalStoreDB)
}

func runEvalList(cmd *cobra.Command, args []string) {
	suiteName, _ := cmd.Flags().GetString("suite")
	limit, _ := cmd.Flags().GetInt("limit")

	// Create eval store
	store, err := evals.NewStore(evalStoreDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to open eval store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()

	var results []*loomv1.EvalResult
	if suiteName != "" {
		fmt.Printf("📊 Recent runs for suite '%s':\n\n", suiteName)
		results, err = store.ListBySuite(ctx, suiteName, limit)
	} else {
		fmt.Printf("📊 Recent evaluation runs:\n\n")
		// For now, we need to implement ListAll in store
		// For MVP, let's use ListBySuite with a known suite name
		fmt.Fprintf(os.Stderr, "⚠️  Please specify --suite flag for now\n")
		fmt.Fprintf(os.Stderr, "    Example: looms eval list --suite config-loader-quality\n")
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list results: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	// Print results
	for i, result := range results {
		fmt.Printf("%d. Suite:    %s\n", i+1, result.SuiteName)
		fmt.Printf("   Agent:    %s\n", result.AgentId)
		fmt.Printf("   Accuracy: %.2f%% (%d/%d passed)\n",
			result.Overall.Accuracy*100,
			result.Overall.PassedTests,
			result.Overall.TotalTests)
		if result.RunAt != nil {
			fmt.Printf("   Time:     %s\n", result.RunAt.AsTime().Format("2006-01-02 15:04:05"))
		}
		fmt.Println()
	}

	fmt.Printf("Showing %d results\n", len(results))
}

func runEvalShow(cmd *cobra.Command, args []string) {
	runIDStr := args[0]

	// Parse run ID
	runID, err := strconv.ParseInt(runIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Invalid run ID: %v\n", err)
		os.Exit(1)
	}

	// Create eval store
	store, err := evals.NewStore(evalStoreDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to open eval store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.Background()

	// Get result
	result, err := store.Get(ctx, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to get result: %v\n", err)
		os.Exit(1)
	}

	// Print detailed results
	fmt.Println("=" + "==========================================================")
	fmt.Printf("EVALUATION RUN: %d\n", runID)
	fmt.Println("=" + "==========================================================")
	fmt.Println()

	fmt.Printf("Suite:     %s\n", result.SuiteName)
	fmt.Printf("Agent:     %s\n", result.AgentId)
	if result.RunAt != nil {
		fmt.Printf("Timestamp: %s\n", result.RunAt.AsTime().Format("2006-01-02 15:04:05"))
	}
	fmt.Println()

	fmt.Printf("Overall Results:\n")
	fmt.Printf("  Accuracy:     %.2f%%\n", result.Overall.Accuracy*100)
	fmt.Printf("  Total Tests:  %d\n", result.Overall.TotalTests)
	fmt.Printf("  Passed:       %d\n", result.Overall.PassedTests)
	fmt.Printf("  Failed:       %d\n", result.Overall.FailedTests)
	fmt.Printf("  Total Latency: %.3fs\n", float64(result.Overall.TotalLatencyMs)/1000.0)
	fmt.Printf("  Total Cost:    $%.4f\n", result.Overall.TotalCostUsd)
	fmt.Println()

	// Show test results
	fmt.Println("Test Results:")
	for i, testResult := range result.TestResults {
		status := "✓"
		if !testResult.Passed {
			status = "✗"
		}
		fmt.Printf("\n%s Test %d: %s\n", status, i+1, testResult.TestName)
		fmt.Printf("  Latency: %.3fs\n", float64(testResult.LatencyMs)/1000.0)
		fmt.Printf("  Cost:    $%.4f\n", testResult.CostUsd)
		if !testResult.Passed {
			fmt.Printf("  Failure: %s\n", testResult.FailureReason)
		}
	}
}

// createLLMProviderForEval creates an LLM provider for eval runs
// Uses the shared createLLMProvider() function to respect config file
func createLLMProviderForEval() (agent.LLMProvider, string) {
	return createLLMProvider()
}

// agentWrapper wraps agent.Agent to implement evals.Agent interface
type agentWrapper struct {
	agent *agent.Agent
}

// Execute implements evals.Agent interface
func (w *agentWrapper) Execute(ctx context.Context, input string) (*evals.AgentResponse, error) {
	// Measure latency
	startTime := time.Now()

	// Use a unique session ID for each eval test
	sessionID := fmt.Sprintf("eval-%d", time.Now().UnixNano())

	// Execute the agent
	response, err := w.agent.Chat(ctx, sessionID, input)
	latencyMs := time.Since(startTime).Milliseconds()

	if err != nil {
		return &evals.AgentResponse{
			Output:     "",
			ToolsUsed:  []string{},
			CostUsd:    0.0,
			LatencyMs:  latencyMs,
			TraceID:    sessionID,
			Successful: false,
			Error:      err.Error(),
		}, nil
	}

	// Extract tool names from executions
	toolsUsed := make([]string, 0, len(response.ToolExecutions))
	for _, exec := range response.ToolExecutions {
		toolsUsed = append(toolsUsed, exec.ToolName)
	}

	// Extract trace ID from metadata if available
	traceID := sessionID
	if tid, ok := response.Metadata["trace_id"].(string); ok && tid != "" {
		traceID = tid
	}

	return &evals.AgentResponse{
		Output:     response.Content,
		ToolsUsed:  toolsUsed,
		CostUsd:    response.Usage.CostUSD,
		LatencyMs:  latencyMs,
		TraceID:    traceID,
		Successful: true,
	}, nil
}
