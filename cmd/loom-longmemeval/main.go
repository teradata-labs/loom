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

//go:build fts5

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// HuggingFace URLs for LongMemEval datasets (not in GitHub repo)
	baseURL = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main"
)

// Dataset files available for download.
var datasetFiles = map[string]string{
	"oracle": "longmemeval_oracle.json",
	"small":  "longmemeval_s_cleaned.json",
	"medium": "longmemeval_m_cleaned.json",
}

var (
	// Global flags
	dataDir string
	verbose bool

	// Run flags
	dataset       string
	output        string
	detailed      string
	concurrency   int
	serverAddr    string
	agentID       string
	limit         int
	offset        int
	questionTypes string
	mode          string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "loom-longmemeval",
		Short: "LongMemEval benchmark harness for Loom graph memory",
		Long: `Evaluates Loom's graph memory against the LongMemEval benchmark
(ICLR 2025) which tests long-term memory across 500 questions
covering information extraction, multi-session reasoning,
temporal reasoning, knowledge updates, and abstention.`,
	}

	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "./data/longmemeval", "Directory for dataset files and temp storage")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging")

	rootCmd.AddCommand(downloadCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(scoreCmd())
	rootCmd.AddCommand(infoCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func downloadCmd() *cobra.Command {
	var datasets []string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download LongMemEval dataset files from GitHub",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(datasets) == 0 {
				datasets = []string{"oracle"}
			}

			if err := os.MkdirAll(dataDir, 0o750); err != nil {
				return fmt.Errorf("create data dir: %w", err)
			}

			for _, ds := range datasets {
				filename, ok := datasetFiles[ds]
				if !ok {
					return fmt.Errorf("unknown dataset %q (available: oracle, small, medium)", ds)
				}

				destPath := filepath.Join(dataDir, filename)
				if _, err := os.Stat(destPath); err == nil {
					fmt.Printf("  %s already exists, skipping\n", destPath)
					continue
				}

				url := baseURL + "/" + filename
				fmt.Printf("  Downloading %s -> %s\n", url, destPath)

				if err := downloadFile(url, destPath); err != nil {
					return fmt.Errorf("download %s: %w", ds, err)
				}
				fmt.Printf("  OK\n")
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&datasets, "datasets", []string{"oracle"}, "Datasets to download (oracle, small, medium)")
	return cmd
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the LongMemEval benchmark against a Loom server",
		Long: `Connects to a running Loom server via gRPC and runs the benchmark.
The server's agent configuration determines how memory works —
graph memory, context window, LLM provider, etc.

Two benchmark modes:

  ingest (default):
    Feeds each conversation session through the agent via Weave so it
    builds up memory (graph memory, conversation history, etc.), then
    asks the question in a separate session.

  context-stuffing:
    Puts all session history directly into one Weave call as prompt
    context. Baseline comparison — no memory system involved.`,
		RunE: runBenchmark,
	}

	cmd.Flags().StringVar(&serverAddr, "server", "localhost:60051", "Loom gRPC server address")
	cmd.Flags().StringVar(&agentID, "agent", "", "Target agent ID (empty = default agent)")
	cmd.Flags().StringVar(&dataset, "dataset", "", "Path to dataset JSON (auto-detects from data-dir if empty)")
	cmd.Flags().StringVar(&output, "output", "results.jsonl", "Output JSONL file path (LongMemEval-compatible)")
	cmd.Flags().StringVar(&detailed, "detailed", "", "Optional detailed results JSON path")
	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "Number of concurrent entries to process")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max entries to process (0 = all)")
	cmd.Flags().IntVar(&offset, "offset", 0, "Start from entry N (0-indexed)")
	cmd.Flags().StringVar(&questionTypes, "types", "", "Comma-separated question types to include (empty = all)")
	cmd.Flags().StringVar(&mode, "mode", "ingest", "Run mode: ingest or context-stuffing")

	return cmd
}

func scoreCmd() *cobra.Command {
	var resultsPath string

	cmd := &cobra.Command{
		Use:   "score",
		Short: "Score benchmark results using the Loom judge system",
		Long: `Reads detailed results JSON from a previous run and evaluates
each answer's correctness using the Loom JudgeService. Produces
PASS/PARTIAL/FAIL verdicts with scores and explanations.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(verbose)
			defer func() { _ = logger.Sync() }()

			// Load results
			data, err := os.ReadFile(resultsPath)
			if err != nil {
				return fmt.Errorf("read results: %w", err)
			}
			var results []EntryResult
			if err := json.Unmarshal(data, &results); err != nil {
				return fmt.Errorf("parse results: %w", err)
			}
			logger.Info("loaded results", zap.Int("count", len(results)))

			// Connect to judge service
			scorer, err := NewScorer(serverAddr, logger)
			if err != nil {
				return err
			}
			defer func() { _ = scorer.Close() }()

			// Score
			scored := scorer.ScoreResults(context.Background(), results)

			// Print
			PrintScoredSummary(scored)

			// Write scored results
			scoredPath := resultsPath + ".scored.json"
			f, err := os.Create(scoredPath)
			if err != nil {
				return fmt.Errorf("create scored output: %w", err)
			}
			defer func() { _ = f.Close() }()
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			if err := enc.Encode(scored); err != nil {
				return fmt.Errorf("write scored output: %w", err)
			}
			logger.Info("wrote scored results", zap.String("path", scoredPath))

			return nil
		},
	}

	cmd.Flags().StringVar(&resultsPath, "results", "", "Path to detailed results JSON from a previous run")
	cmd.Flags().StringVar(&serverAddr, "server", "localhost:60051", "Loom gRPC server address")
	_ = cmd.MarkFlagRequired("results")
	return cmd
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info [dataset-path]",
		Short: "Show dataset statistics",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := resolveDatasetPath(args)
			if path == "" {
				return fmt.Errorf("no dataset found; run 'loom-longmemeval download' first or specify --dataset")
			}

			entries, err := LoadDataset(path)
			if err != nil {
				return err
			}

			fmt.Printf("Dataset:  %s\n", path)
			fmt.Printf("Entries:  %d\n", len(entries))
			fmt.Println()

			types := QuestionTypes(entries)
			typeCounts := make(map[string]int)
			for _, e := range entries {
				typeCounts[e.QuestionType]++
			}

			fmt.Println("Question Types:")
			for _, t := range types {
				fmt.Printf("  %-30s %d\n", t, typeCounts[t])
			}

			// Session stats
			var totalSessions, totalTurns int
			for _, e := range entries {
				totalSessions += len(e.HaystackSessions)
				for _, s := range e.HaystackSessions {
					totalTurns += len(s)
				}
			}
			fmt.Printf("\nTotal sessions: %d (avg %.1f/entry)\n",
				totalSessions, float64(totalSessions)/float64(len(entries)))
			fmt.Printf("Total turns:    %d (avg %.1f/session)\n",
				totalTurns, float64(totalTurns)/float64(totalSessions))

			return nil
		},
	}
}

func runBenchmark(cmd *cobra.Command, args []string) error {
	logger := newLogger(verbose)
	defer func() { _ = logger.Sync() }()

	// Resolve dataset path
	path := dataset
	if path == "" {
		path = resolveDatasetPath(nil)
	}
	if path == "" {
		return fmt.Errorf("no dataset found; run 'loom-longmemeval download' first or specify --dataset")
	}

	// Load dataset
	logger.Info("loading dataset", zap.String("path", path))
	entries, err := LoadDataset(path)
	if err != nil {
		return err
	}
	logger.Info("dataset loaded", zap.Int("entries", len(entries)))

	// Filter by question types
	if questionTypes != "" {
		types := strings.Split(questionTypes, ",")
		entries = FilterByType(entries, types)
		logger.Info("filtered by types", zap.Strings("types", types), zap.Int("remaining", len(entries)))
	}

	// Apply offset and limit
	if offset > 0 {
		if offset >= len(entries) {
			return fmt.Errorf("offset %d >= entry count %d", offset, len(entries))
		}
		entries = entries[offset:]
	}
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}

	logger.Info("benchmark configuration",
		zap.String("mode", mode),
		zap.String("server", serverAddr),
		zap.String("agent", agentID),
		zap.Int("entries", len(entries)),
		zap.Int("concurrency", concurrency),
	)

	// Create runner (connects to running Loom server via gRPC)
	runner, err := NewRunner(RunConfig{
		Mode:        RunMode(mode),
		ServerAddr:  serverAddr,
		AgentID:     agentID,
		Concurrency: concurrency,
		Verbose:     verbose,
	}, logger)
	if err != nil {
		return err
	}
	defer func() { _ = runner.Close() }()

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received interrupt, finishing current entries...")
		cancel()
	}()

	// Run benchmark and collect results
	resultCh := make(chan EntryResult, len(entries))
	startTime := time.Now()

	go func() {
		if err := runner.Run(ctx, entries, resultCh); err != nil {
			logger.Error("runner error", zap.Error(err))
		}
		close(resultCh)
	}()

	var results []EntryResult
	for r := range resultCh {
		results = append(results, r)
	}

	elapsed := time.Since(startTime)
	logger.Info("benchmark completed",
		zap.Duration("elapsed", elapsed),
		zap.Int("results", len(results)),
	)

	// Write output
	if err := WriteJSONL(output, results); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	logger.Info("wrote JSONL output", zap.String("path", output))

	if detailed != "" {
		if err := WriteDetailedResults(detailed, results); err != nil {
			return fmt.Errorf("write detailed: %w", err)
		}
		logger.Info("wrote detailed results", zap.String("path", detailed))
	}

	// Print summary
	summary := Summarize(results, mode, serverAddr)
	PrintSummary(summary)

	return nil
}

// resolveDatasetPath finds a dataset file, trying args first, then data-dir.
func resolveDatasetPath(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	// Try oracle first, then small, then medium
	for _, ds := range []string{"oracle", "small", "medium"} {
		path := filepath.Join(dataDir, datasetFiles[ds])
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// downloadFile downloads a URL to a local path.
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url) // #nosec G107 -- URL is constructed from hardcoded base + known dataset names
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer func() { _ = f.Close() }()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("  Downloaded %d bytes\n", written)
	return nil
}

func newLogger(verbose bool) *zap.Logger {
	level := zapcore.InfoLevel
	if verbose {
		level = zapcore.DebugLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	logger, err := cfg.Build()
	if err != nil {
		// Fallback to nop logger if config fails
		return zap.NewNop()
	}
	return logger
}
