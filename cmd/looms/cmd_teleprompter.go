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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"
)

var teleprompterCmd = &cobra.Command{
	Use:   "teleprompter",
	Short: "DSPy-style prompt optimization",
	Long: `Run DSPy-style teleprompters for prompt optimization.

Teleprompters use multi-judge evaluation to optimize prompts:
- Bootstrap: Select high-quality few-shot demonstrations
- MIPRO: Find optimal instructions via search
- TextGrad: Iteratively improve prompts with gradient feedback

All teleprompters support multi-dimensional optimization using
judge dimension scores (quality, cost, safety, domain, etc.).`,
}

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap few-shot demonstrations",
	Long: `Select high-quality few-shot demonstrations using multi-judge evaluation.

BootstrapFewShot filters examples based on judge scores, selecting only
demonstrations that meet quality, safety, and cost thresholds.

Examples:
  # Basic bootstrap with quality and safety judges
  looms teleprompter bootstrap \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --judges=quality-judge,safety-judge \
    --max-demos=5 \
    --min-confidence=0.8 \
    --output=demos.yaml

  # With dimension weights (quality > cost)
  looms teleprompter bootstrap \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --judges=quality-judge,safety-judge,cost-judge \
    --dimension-weights='{"quality":0.6,"safety":0.3,"cost":0.1}' \
    --max-demos=10 \
    --output=demos.yaml

  # Strict safety filtering (all must pass)
  looms teleprompter bootstrap \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --judges=quality-judge,safety-judge \
    --aggregation=ALL_MUST_PASS \
    --min-confidence=0.9 \
    --output=demos.yaml`,
	Run: runBootstrap,
}

var miproCmd = &cobra.Command{
	Use:   "mipro",
	Short: "Optimize instructions with MIPRO",
	Long: `Multi-prompt Instruction Proposal Optimizer (MIPRO).

MIPRO searches over instruction candidates to find the best instruction
based on multi-dimensional judge evaluation. Supports dimension priorities
to optimize for specific tradeoffs (e.g., quality vs cost).

Examples:
  # Basic MIPRO with instruction candidates
  looms teleprompter mipro \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --instructions=candidates.txt \
    --judges=quality-judge,cost-judge \
    --output=optimized.yaml

  # With dimension priorities (quality 2x, safety 3x)
  looms teleprompter mipro \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --instructions=candidates.txt \
    --judges=quality-judge,safety-judge,cost-judge \
    --dimension-priorities='{"quality":2.0,"safety":3.0,"cost":1.0}' \
    --output=optimized.yaml

  # Cost-optimized instruction selection
  looms teleprompter mipro \
    --agent=sql-agent \
    --trainset=examples.jsonl \
    --instructions=candidates.txt \
    --dimension-priorities='{"quality":1.0,"cost":3.0}' \
    --min-confidence=0.7 \
    --output=optimized.yaml`,
	Run: runMIPRO,
}

var textgradCmd = &cobra.Command{
	Use:   "textgrad",
	Short: "Iterative improvement with TextGrad",
	Long: `TextGrad-style iterative prompt improvement using judge feedback.

Uses judge dimension scores as "textual gradients" to generate targeted
improvements. Each iteration produces specific suggestions based on
failing dimensions.

Examples:
  # Basic TextGrad with single example
  looms teleprompter textgrad \
    --agent=sql-agent \
    --example=example.json \
    --variables=prompts.yaml \
    --judges=quality-judge,safety-judge \
    --iterations=5 \
    --output=improvements.yaml

  # With custom result from agent execution
  looms teleprompter textgrad \
    --agent=sql-agent \
    --example=example.json \
    --result=agent-output.json \
    --variables=prompts.yaml \
    --judges=quality-judge,safety-judge \
    --output=improvements.yaml`,
	Run: runTextGrad,
}

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Get compilation history",
	Long: `Retrieve compilation history for an agent.

Shows all past compilations with scores, optimization details, and timestamps.
Useful for tracking experimentation and comparing optimization approaches.

Examples:
  # Get all compilations for an agent
  looms teleprompter history --agent=sql-agent

  # Limit results
  looms teleprompter history --agent=sql-agent --limit=10

  # With pagination
  looms teleprompter history --agent=sql-agent --limit=20 --offset=20`,
	Run: runHistory,
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback [compilation-id]",
	Short: "Rollback to a previous compilation",
	Long: `Revert an agent to a previous compilation.

Restores optimized prompts and demonstrations from a previous compilation.
Use 'looms teleprompter history' to find compilation IDs.

Examples:
  # Rollback to specific compilation
  looms teleprompter rollback abc123-def456 --agent=sql-agent

  # With timeout
  looms teleprompter rollback abc123-def456 --agent=sql-agent --timeout=60`,
	Args: cobra.ExactArgs(1),
	Run:  runRollback,
}

var compareCmd = &cobra.Command{
	Use:   "compare [compilation-a] [compilation-b]",
	Short: "Compare two compilations",
	Long: `A/B test two compiled versions on a testset.

Evaluates both compilations and reports which performs better.
Includes statistical significance testing.

Examples:
  # Compare two compilations
  looms teleprompter compare abc123 def456 \
    --agent=sql-agent \
    --testset=test-examples.jsonl \
    --judges=quality-judge,safety-judge

  # With dimension weights
  looms teleprompter compare abc123 def456 \
    --agent=sql-agent \
    --testset=test-examples.jsonl \
    --judges=quality-judge,safety-judge,cost-judge \
    --dimension-weights='{"quality":0.6,"safety":0.3,"cost":0.1}'`,
	Args: cobra.ExactArgs(2),
	Run:  runCompare,
}

var (
	tpAgent            string
	tpTrainset         string
	tpJudges           string
	tpMaxDemos         int
	tpMinConfidence    float64
	tpOutput           string
	tpAggregation      string
	tpDimensionWeights string
	tpInstructions     string
	tpDimensionPrios   string
	tpExample          string
	tpResult           string
	tpVariables        string
	tpIterations       int
	tpServer           string
	tpTimeout          int
	tpExportHawk       bool
	tpLimit            int
	tpOffset           int
	tpTestset          string
)

func init() {
	rootCmd.AddCommand(teleprompterCmd)
	teleprompterCmd.AddCommand(bootstrapCmd)
	teleprompterCmd.AddCommand(miproCmd)
	teleprompterCmd.AddCommand(textgradCmd)
	teleprompterCmd.AddCommand(historyCmd)
	teleprompterCmd.AddCommand(rollbackCmd)
	teleprompterCmd.AddCommand(compareCmd)

	// Common flags
	for _, cmd := range []*cobra.Command{bootstrapCmd, miproCmd, textgradCmd} {
		cmd.Flags().StringVar(&tpAgent, "agent", "", "Agent ID (required)")
		cmd.Flags().StringVar(&tpJudges, "judges", "", "Comma-separated judge IDs (required)")
		cmd.Flags().StringVar(&tpOutput, "output", "", "Output file path (required)")
		cmd.Flags().StringVar(&tpServer, "server", "localhost:60051", "Loom server address")
		cmd.Flags().IntVar(&tpTimeout, "timeout", 300, "Request timeout in seconds")
		cmd.Flags().BoolVar(&tpExportHawk, "export-hawk", false, "Export results to Hawk")

		_ = cmd.MarkFlagRequired("agent")
		_ = cmd.MarkFlagRequired("judges")
		_ = cmd.MarkFlagRequired("output")
	}

	// Bootstrap flags
	bootstrapCmd.Flags().StringVar(&tpTrainset, "trainset", "", "Training examples (JSONL file, required)")
	bootstrapCmd.Flags().IntVar(&tpMaxDemos, "max-demos", 5, "Maximum demonstrations to select")
	bootstrapCmd.Flags().Float64Var(&tpMinConfidence, "min-confidence", 0.8, "Minimum confidence threshold (0.0-1.0)")
	bootstrapCmd.Flags().StringVar(&tpAggregation, "aggregation", "WEIGHTED_AVERAGE", "Aggregation strategy (WEIGHTED_AVERAGE, ALL_MUST_PASS, MAJORITY_PASS, etc.)")
	bootstrapCmd.Flags().StringVar(&tpDimensionWeights, "dimension-weights", "", "JSON map of dimension weights (e.g., '{\"quality\":0.6,\"safety\":0.3,\"cost\":0.1}')")
	_ = bootstrapCmd.MarkFlagRequired("trainset")

	// MIPRO flags
	miproCmd.Flags().StringVar(&tpTrainset, "trainset", "", "Training examples (JSONL file, required)")
	miproCmd.Flags().StringVar(&tpInstructions, "instructions", "", "Instruction candidates (text file, one per line, required)")
	miproCmd.Flags().IntVar(&tpMaxDemos, "max-demos", 3, "Maximum demonstrations to bootstrap")
	miproCmd.Flags().Float64Var(&tpMinConfidence, "min-confidence", 0.7, "Minimum confidence threshold (0.0-1.0)")
	miproCmd.Flags().StringVar(&tpDimensionPrios, "dimension-priorities", "", "JSON map of dimension priorities (e.g., '{\"quality\":2.0,\"safety\":3.0,\"cost\":1.0}')")
	_ = miproCmd.MarkFlagRequired("trainset")
	_ = miproCmd.MarkFlagRequired("instructions")

	// TextGrad flags
	textgradCmd.Flags().StringVar(&tpExample, "example", "", "Single example (JSON file, required)")
	textgradCmd.Flags().StringVar(&tpResult, "result", "", "Agent execution result (JSON file, optional)")
	textgradCmd.Flags().StringVar(&tpVariables, "variables", "", "Variables to optimize (YAML file, required)")
	textgradCmd.Flags().IntVar(&tpIterations, "iterations", 1, "Number of improvement iterations")
	_ = textgradCmd.MarkFlagRequired("example")
	_ = textgradCmd.MarkFlagRequired("variables")

	// History flags
	historyCmd.Flags().StringVar(&tpAgent, "agent", "", "Agent ID (required)")
	historyCmd.Flags().IntVar(&tpLimit, "limit", 20, "Maximum number of results")
	historyCmd.Flags().IntVar(&tpOffset, "offset", 0, "Offset for pagination")
	historyCmd.Flags().StringVar(&tpServer, "server", "localhost:60051", "Loom server address")
	historyCmd.Flags().IntVar(&tpTimeout, "timeout", 30, "Request timeout in seconds")
	_ = historyCmd.MarkFlagRequired("agent")

	// Rollback flags
	rollbackCmd.Flags().StringVar(&tpAgent, "agent", "", "Agent ID (required)")
	rollbackCmd.Flags().StringVar(&tpServer, "server", "localhost:60051", "Loom server address")
	rollbackCmd.Flags().IntVar(&tpTimeout, "timeout", 30, "Request timeout in seconds")
	_ = rollbackCmd.MarkFlagRequired("agent")

	// Compare flags
	compareCmd.Flags().StringVar(&tpAgent, "agent", "", "Agent ID (required)")
	compareCmd.Flags().StringVar(&tpTestset, "testset", "", "Test examples (JSONL file, required)")
	compareCmd.Flags().StringVar(&tpJudges, "judges", "", "Comma-separated judge IDs (required)")
	compareCmd.Flags().StringVar(&tpDimensionWeights, "dimension-weights", "", "JSON map of dimension weights")
	compareCmd.Flags().StringVar(&tpServer, "server", "localhost:60051", "Loom server address")
	compareCmd.Flags().IntVar(&tpTimeout, "timeout", 300, "Request timeout in seconds")
	compareCmd.Flags().BoolVar(&tpExportHawk, "export-hawk", false, "Export results to Hawk")
	_ = compareCmd.MarkFlagRequired("agent")
	_ = compareCmd.MarkFlagRequired("testset")
	_ = compareCmd.MarkFlagRequired("judges")
}

// Helper: Load trainset from JSONL file
func loadTrainset(path string) ([]*loomv1.Example, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open trainset: %w", err)
	}
	defer file.Close()

	var examples []*loomv1.Example
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var ex struct {
			ID      string            `json:"id"`
			Inputs  map[string]string `json:"inputs"`
			Outputs map[string]string `json:"outputs"`
		}

		if err := json.Unmarshal([]byte(line), &ex); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %w", lineNum, err)
		}

		examples = append(examples, &loomv1.Example{
			Id:      ex.ID,
			Inputs:  ex.Inputs,
			Outputs: ex.Outputs,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read trainset: %w", err)
	}

	return examples, nil
}

// Helper: Load instruction candidates from text file
func loadInstructions(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open instructions: %w", err)
	}
	defer file.Close()

	var instructions []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			instructions = append(instructions, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read instructions: %w", err)
	}

	return instructions, nil
}

// Helper: Parse aggregation strategy
func parseAggregation(s string) loomv1.AggregationStrategy {
	switch strings.ToUpper(s) {
	case "WEIGHTED_AVERAGE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	case "ALL_MUST_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS
	case "MAJORITY_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS
	case "ANY_PASS":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS
	case "MIN_SCORE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE
	case "MAX_SCORE":
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE
	default:
		return loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE
	}
}

// Helper: Parse JSON map
func parseJSONMap(s string) (map[string]float64, error) {
	if s == "" {
		return nil, nil
	}

	var m map[string]float64
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("invalid JSON map: %w", err)
	}

	return m, nil
}

// Helper: Create teleprompter client
func createTeleprompterClient(serverAddr string) (loomv1.TeleprompterServiceClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to %s: %w", serverAddr, err)
	}

	client := loomv1.NewTeleprompterServiceClient(conn)
	return client, conn, nil
}

func runBootstrap(cmd *cobra.Command, args []string) {
	fmt.Println("🚀 BootstrapFewShot Starting...")
	fmt.Printf("   Agent: %s\n", tpAgent)
	fmt.Printf("   Trainset: %s\n", tpTrainset)
	fmt.Printf("   Judges: %s\n", tpJudges)
	fmt.Printf("   Max Demos: %d\n", tpMaxDemos)
	fmt.Printf("   Min Confidence: %.2f\n", tpMinConfidence)
	fmt.Printf("   Aggregation: %s\n\n", tpAggregation)

	// Load trainset
	fmt.Print("📂 Loading trainset...")
	trainset, err := loadTrainset(tpTrainset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d examples loaded\n", len(trainset))

	// Parse dimension weights
	var dimensionWeights map[string]float64
	if tpDimensionWeights != "" {
		dimensionWeights, err = parseJSONMap(tpDimensionWeights)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error parsing dimension weights: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("   Dimension Weights: %v\n", dimensionWeights)
	}

	// Parse judge IDs
	judgeIDs := strings.Split(tpJudges, ",")
	for i, id := range judgeIDs {
		judgeIDs[i] = strings.TrimSpace(id)
	}

	// Connect to server
	fmt.Printf("🔌 Connecting to Loom server at %s...\n", tpServer)
	client, conn, err := createTeleprompterClient(tpServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tpTimeout)*time.Second)
	defer cancel()

	// Build compile request
	aggregation := parseAggregation(tpAggregation)
	req := &loomv1.CompileRequest{
		AgentId:      tpAgent,
		Teleprompter: loomv1.TeleprompterType_TELEPROMPTER_BOOTSTRAP_FEW_SHOT,
		Config: &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: int32(tpMaxDemos),
			MinConfidence:        tpMinConfidence,
		},
		Trainset: trainset,
		Metric: &loomv1.MetricConfig{
			Type: loomv1.MetricType_METRIC_MULTI_JUDGE,
			MultiJudge: &loomv1.MultiJudgeMetricConfig{
				JudgeIds:         judgeIDs,
				Aggregation:      aggregation,
				DimensionWeights: dimensionWeights,
				MinThreshold:     tpMinConfidence * 100, // 0-100 scale
				ExportToHawk:     tpExportHawk,
			},
		},
	}

	// Execute compilation
	fmt.Println("⚙️  Running BootstrapFewShot compilation...")
	resp, err := client.Compile(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "❌ Compilation failed: %s\n", resp.Message)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Compilation successful!\n")
	fmt.Printf("   Compilation ID: %s\n", resp.CompilationId)
	fmt.Printf("   Trainset Score: %.4f\n", resp.Result.TrainsetScore)
	fmt.Printf("   Demonstrations: %d\n", len(resp.Result.Demonstrations))
	fmt.Printf("   Examples Used: %d\n", resp.Result.ExamplesUsed)
	fmt.Printf("   Successful Traces: %d\n", resp.Result.SuccessfulTraces)
	fmt.Printf("   Compilation Time: %dms\n", resp.Result.CompilationTimeMs)

	// Save results
	fmt.Printf("\n💾 Saving results to %s...\n", tpOutput)
	outputData, err := yaml.Marshal(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error marshaling output: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(tpOutput, outputData, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Done!")
}

func runMIPRO(cmd *cobra.Command, args []string) {
	fmt.Println("🚀 MIPRO Starting...")
	fmt.Printf("   Agent: %s\n", tpAgent)
	fmt.Printf("   Trainset: %s\n", tpTrainset)
	fmt.Printf("   Instructions: %s\n", tpInstructions)
	fmt.Printf("   Judges: %s\n\n", tpJudges)

	// Load trainset
	fmt.Print("📂 Loading trainset...")
	trainset, err := loadTrainset(tpTrainset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d examples loaded\n", len(trainset))

	// Load instruction candidates
	fmt.Print("📂 Loading instruction candidates...")
	instructions, err := loadInstructions(tpInstructions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d candidates loaded\n", len(instructions))

	// Parse dimension priorities
	var dimensionPrios map[string]float64
	if tpDimensionPrios != "" {
		dimensionPrios, err = parseJSONMap(tpDimensionPrios)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error parsing dimension priorities: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("   Dimension Priorities: %v\n", dimensionPrios)
	}

	// Parse judge IDs
	judgeIDs := strings.Split(tpJudges, ",")
	for i, id := range judgeIDs {
		judgeIDs[i] = strings.TrimSpace(id)
	}

	// Connect to server
	fmt.Printf("🔌 Connecting to Loom server at %s...\n", tpServer)
	client, conn, err := createTeleprompterClient(tpServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tpTimeout)*time.Second)
	defer cancel()

	// Build compile request
	req := &loomv1.CompileRequest{
		AgentId:      tpAgent,
		Teleprompter: loomv1.TeleprompterType_TELEPROMPTER_MIPRO,
		Config: &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: int32(tpMaxDemos),
			MinConfidence:        tpMinConfidence,
			Mipro: &loomv1.MIPROConfig{
				InstructionCandidates:  instructions,
				DimensionPriorities:    dimensionPrios,
				OptimizeInstructions:   true,
				OptimizeDemonstrations: true,
			},
		},
		Trainset: trainset,
		Metric: &loomv1.MetricConfig{
			Type: loomv1.MetricType_METRIC_MULTI_JUDGE,
			MultiJudge: &loomv1.MultiJudgeMetricConfig{
				JudgeIds:     judgeIDs,
				MinThreshold: tpMinConfidence * 100,
				ExportToHawk: tpExportHawk,
			},
		},
	}

	// Execute compilation
	fmt.Println("⚙️  Running MIPRO optimization...")
	resp, err := client.Compile(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "❌ Compilation failed: %s\n", resp.Message)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Optimization successful!\n")
	fmt.Printf("   Compilation ID: %s\n", resp.CompilationId)
	fmt.Printf("   Trainset Score: %.4f\n", resp.Result.TrainsetScore)
	fmt.Printf("   Improvement: %.2f%%\n", resp.Result.ImprovementDelta*100)
	fmt.Printf("   Optimization Rounds: %d\n", resp.Result.OptimizationRounds)
	fmt.Printf("   Compilation Time: %dms\n", resp.Result.CompilationTimeMs)

	// Show optimized prompts
	if len(resp.Result.OptimizedPrompts) > 0 {
		fmt.Println("\n📝 Optimized Prompts:")
		for key, value := range resp.Result.OptimizedPrompts {
			fmt.Printf("   %s: %s\n", key, value)
		}
	}

	// Save results
	fmt.Printf("\n💾 Saving results to %s...\n", tpOutput)
	outputData, err := yaml.Marshal(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error marshaling output: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(tpOutput, outputData, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Done!")
}

func runTextGrad(cmd *cobra.Command, args []string) {
	fmt.Println("🚀 TextGrad Starting...")
	fmt.Printf("   Agent: %s\n", tpAgent)
	fmt.Printf("   Example: %s\n", tpExample)
	fmt.Printf("   Variables: %s\n", tpVariables)
	fmt.Printf("   Iterations: %d\n\n", tpIterations)

	// Load example
	fmt.Print("📂 Loading example...")
	exampleData, err := os.ReadFile(tpExample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}

	var ex struct {
		ID      string            `json:"id"`
		Inputs  map[string]string `json:"inputs"`
		Outputs map[string]string `json:"outputs"`
	}

	if err := json.Unmarshal(exampleData, &ex); err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error parsing example: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(" done")

	// Load variables
	fmt.Print("📂 Loading variables...")
	varData, err := os.ReadFile(tpVariables)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}

	var vars []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	}

	if err := yaml.Unmarshal(varData, &vars); err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error parsing variables: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d variables loaded\n", len(vars))

	fmt.Println("\n⚠️  TextGrad Implementation Note:")
	fmt.Println("   TextGrad is not yet integrated with the TeleprompterService.")
	fmt.Println("   TextGrad provides iterative improvement through judge feedback,")
	fmt.Println("   which requires a different API pattern than one-shot compilation.")
	fmt.Println()
	fmt.Println("   Workaround: Use the Go library directly:")
	fmt.Println("   ")
	fmt.Println("   engine, _ := teleprompter.NewJudgeGradientEngine(config)")
	fmt.Println("   engine.Backward(ctx, example, result, variables)")
	fmt.Println("   improvements, _ := engine.Step(ctx, variables)")
	fmt.Println()
	fmt.Println("   OR use the Learning Agent for automatic improvements:")
	fmt.Println("   looms learning proposals --domain=<domain>")
	fmt.Printf("\n   Output would be saved to: %s\n", tpOutput)

	_ = ex
	_ = vars
}

func runHistory(cmd *cobra.Command, args []string) {
	// Create client
	client, conn, err := createTeleprompterClient(tpServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tpTimeout)*time.Second)
	defer cancel()

	// Build request
	req := &loomv1.GetCompilationHistoryRequest{
		AgentId: tpAgent,
		Limit:   int32(tpLimit),
		Offset:  int32(tpOffset),
	}

	// Print header
	fmt.Printf("📜 Compilation History: %s\n", tpAgent)
	fmt.Printf("   Limit: %d (offset: %d)\n\n", tpLimit, tpOffset)

	// Call history
	resp, err := client.GetCompilationHistory(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting history: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Compilations) == 0 {
		fmt.Println("No compilation history found.")
		fmt.Println("\n💡 Run 'looms teleprompter bootstrap' or 'looms teleprompter mipro' to create compilations.")
		return
	}

	// Display history
	fmt.Printf("📊 Found %d of %d total compilations\n", len(resp.Compilations), resp.TotalCount)
	fmt.Println(strings.Repeat("─", 80))

	for i, comp := range resp.Compilations {
		// Compilation header
		timestamp := time.UnixMilli(comp.CompiledAt).Format("2006-01-02 15:04:05")
		fmt.Printf("\n[%d] ID: %s\n", i+1, comp.CompilationId)
		fmt.Printf("    Teleprompter: %s\n", comp.Teleprompter.String())
		fmt.Printf("    Compiled: %s (%s)\n", timestamp, comp.CompiledVersion)

		// Scores
		fmt.Printf("    Scores: trainset=%.1f%%, devset=%.1f%%",
			comp.TrainsetScore*100, comp.DevsetScore*100)
		if comp.ImprovementDelta != 0 {
			fmt.Printf(" (improvement: %+.1f%%)", comp.ImprovementDelta*100)
		}
		fmt.Println()

		// Content summary
		fmt.Printf("    Content: %d demonstrations, %d prompts\n",
			len(comp.Demonstrations), len(comp.OptimizedPrompts))

		// Optimization details
		if comp.OptimizationRounds > 0 {
			fmt.Printf("    Optimization: %d rounds, %d examples, %d successful traces\n",
				comp.OptimizationRounds, comp.ExamplesUsed, comp.SuccessfulTraces)
		}

		// Time
		fmt.Printf("    Time: %dms\n", comp.CompilationTimeMs)

		// Rollback hint
		fmt.Printf("    💡 Rollback: looms teleprompter rollback %s --agent=%s\n",
			comp.CompilationId, tpAgent)
	}

	fmt.Println(strings.Repeat("─", 80))

	// Pagination hint
	if resp.TotalCount > int32(len(resp.Compilations)) {
		remaining := resp.TotalCount - int32(tpOffset) - int32(len(resp.Compilations))
		fmt.Printf("\n💡 %d more compilations available. Use --offset=%d to see more.\n",
			remaining, tpOffset+len(resp.Compilations))
	}
}

func runRollback(cmd *cobra.Command, args []string) {
	compilationID := args[0]

	// Create client
	client, conn, err := createTeleprompterClient(tpServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tpTimeout)*time.Second)
	defer cancel()

	// Build request
	req := &loomv1.RollbackCompilationRequest{
		AgentId:       tpAgent,
		CompilationId: compilationID,
	}

	// Rollback
	fmt.Printf("⏪ Rolling back agent '%s' to compilation '%s'...\n", tpAgent, compilationID)

	resp, err := client.RollbackCompilation(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error rolling back: %v\n", err)
		os.Exit(1)
	}

	// Display result
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "\n❌ Rollback failed: %s\n", resp.Message)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Rollback successful!\n\n")
	fmt.Printf("   Message: %s\n", resp.Message)

	if resp.Restored != nil {
		comp := resp.Restored
		fmt.Printf("   Restored Version: %s\n", comp.CompiledVersion)
		fmt.Printf("   Teleprompter: %s\n", comp.Teleprompter.String())
		fmt.Printf("   Scores: trainset=%.1f%%, devset=%.1f%%\n",
			comp.TrainsetScore*100, comp.DevsetScore*100)
		fmt.Printf("   Content: %d demonstrations, %d prompts\n",
			len(comp.Demonstrations), len(comp.OptimizedPrompts))
		timestamp := time.UnixMilli(comp.CompiledAt).Format("2006-01-02 15:04:05")
		fmt.Printf("   Originally Compiled: %s\n", timestamp)
	}

	fmt.Printf("\n💡 Verify with: looms teleprompter history --agent=%s\n", tpAgent)
}

func runCompare(cmd *cobra.Command, args []string) {
	compilationA := args[0]
	compilationB := args[1]

	// Create client
	client, conn, err := createTeleprompterClient(tpServer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Load testset
	fmt.Print("📂 Loading testset...")
	testset, err := loadTrainset(tpTestset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(" %d examples loaded\n", len(testset))

	// Parse dimension weights
	var dimensionWeights map[string]float64
	if tpDimensionWeights != "" {
		if err := json.Unmarshal([]byte(tpDimensionWeights), &dimensionWeights); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing dimension weights: %v\n", err)
			os.Exit(1)
		}
	}

	// Parse judges
	judgeIDs := strings.Split(tpJudges, ",")
	for i := range judgeIDs {
		judgeIDs[i] = strings.TrimSpace(judgeIDs[i])
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(tpTimeout)*time.Second)
	defer cancel()

	// Build metric config
	metricConfig := &loomv1.MetricConfig{
		Type: loomv1.MetricType_METRIC_MULTI_JUDGE,
		MultiJudge: &loomv1.MultiJudgeMetricConfig{
			JudgeIds:         judgeIDs,
			Aggregation:      loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			DimensionWeights: dimensionWeights,
			ExportToHawk:     tpExportHawk,
		},
	}

	// Build request
	req := &loomv1.CompareCompilationsRequest{
		AgentId:      tpAgent,
		CompilationA: compilationA,
		CompilationB: compilationB,
		Testset:      testset,
		Metric:       metricConfig,
	}

	// Print header
	fmt.Printf("\n🔬 Comparing Compilations\n")
	fmt.Println(strings.Repeat("─", 80))
	fmt.Printf("   Agent: %s\n", tpAgent)
	fmt.Printf("   Compilation A: %s\n", compilationA)
	fmt.Printf("   Compilation B: %s\n", compilationB)
	fmt.Printf("   Testset: %d examples\n", len(testset))
	fmt.Printf("   Judges: %s\n", strings.Join(judgeIDs, ", "))
	if len(dimensionWeights) > 0 {
		fmt.Printf("   Dimension Weights: ")
		weights := []string{}
		for dim, w := range dimensionWeights {
			weights = append(weights, fmt.Sprintf("%s=%.1f", dim, w))
		}
		fmt.Printf("%s\n", strings.Join(weights, ", "))
	}
	fmt.Println()

	// Run comparison
	fmt.Print("🔄 Evaluating both compilations...")
	resp, err := client.CompareCompilations(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(" done")

	// Display results
	comparison := resp.Comparison
	fmt.Printf("\n📊 Comparison Results\n")
	fmt.Println(strings.Repeat("─", 80))

	// Scores
	fmt.Printf("\n🎯 Scores:\n")
	fmt.Printf("   Compilation A: %.1f%%\n", comparison.ScoreA*100)
	fmt.Printf("   Compilation B: %.1f%%\n", comparison.ScoreB*100)

	delta := comparison.ScoreDelta
	deltaIcon := "→"
	if delta > 0 {
		deltaIcon = "📈"
	} else if delta < 0 {
		deltaIcon = "📉"
	}
	fmt.Printf("   Delta (B - A): %s %+.1f%%\n", deltaIcon, delta*100)

	// Win/loss/tie
	fmt.Printf("\n🏆 Head-to-Head:\n")
	fmt.Printf("   A Wins: %d\n", comparison.AWins)
	fmt.Printf("   B Wins: %d\n", comparison.BWins)
	fmt.Printf("   Ties:   %d\n", comparison.Ties)

	// Statistical significance
	fmt.Printf("\n📈 Statistical Analysis:\n")
	fmt.Printf("   P-value: %.4f\n", comparison.StatisticalSignificance)
	if comparison.IsSignificant {
		fmt.Printf("   Significance: ✅ Significant (p < 0.05)\n")
	} else {
		fmt.Printf("   Significance: ⚠️  Not significant (p >= 0.05)\n")
	}

	// Recommendation
	fmt.Printf("\n💡 Recommendation:\n")
	fmt.Printf("   %s\n", resp.Recommendation)

	fmt.Println(strings.Repeat("─", 80))

	// Winner summary
	if delta > 0 && comparison.IsSignificant {
		fmt.Printf("\n✅ Compilation B performs significantly better (+%.1f%%).\n", delta*100)
		fmt.Printf("   Consider using: looms teleprompter rollback %s --agent=%s\n", compilationB, tpAgent)
	} else if delta < 0 && comparison.IsSignificant {
		fmt.Printf("\n✅ Compilation A performs significantly better (%.1f%%).\n", -delta*100)
		fmt.Printf("   Consider using: looms teleprompter rollback %s --agent=%s\n", compilationA, tpAgent)
	} else {
		fmt.Printf("\n⚠️  No significant performance difference detected.\n")
		fmt.Printf("   Both compilations perform similarly. Choice depends on other factors.\n")
	}
}
