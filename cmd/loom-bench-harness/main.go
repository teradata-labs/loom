// loom-bench-harness drives benchmark scenarios against a remote Loom gRPC server.
// It connects to the server specified by --server-addr, runs the chosen scenario,
// collects time-series data, and writes a JSON report.
//
// Resume support: if --output-dir contains a result file for a scenario, that
// scenario is skipped. Delete the file to force a re-run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	loadtest "github.com/teradata-labs/loom/test/loadtest"
)

var gitCommit = "unknown"

func main() {
	serverAddr := flag.String("server-addr", "", "gRPC server address (required)")
	httpAddr := flag.String("http-addr", "", "HTTP server address for reconfigure/metrics (default: derived from server-addr)")
	scenario := flag.String("scenario", "sustained_load", "Scenario to run (or 'all' for full suite)")
	outputDir := flag.String("output-dir", "", "Directory to write JSON results (empty = stdout only)")
	runs := flag.Int("runs", 3, "Number of measured runs per scenario level")
	warmupRuns := flag.Int("warmup-runs", 1, "Number of warmup runs to discard")
	resume := flag.Bool("resume", true, "Skip scenarios that already have results in output-dir")
	flag.Parse()

	if *serverAddr == "" {
		log.Fatal("--server-addr is required")
	}

	// Derive HTTP address from gRPC address if not specified
	if *httpAddr == "" {
		host := *serverAddr
		if idx := strings.LastIndex(host, ":"); idx >= 0 {
			host = host[:idx]
		}
		*httpAddr = host + ":8080"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, canceling...")
		cancel()
	}()

	log.Printf("loom-bench-harness (commit=%s)", gitCommit)
	log.Printf("grpc=%s http=%s scenario=%s runs=%d warmup=%d resume=%v",
		*serverAddr, *httpAddr, *scenario, *runs, *warmupRuns, *resume)

	// Determine which scenarios to run
	var scenariosToRun []string
	if *scenario == "all" {
		scenariosToRun = []string{
			"sustained_load", "concurrency_scaling", "peak_throughput",
			"memory_pressure", "multi_turn", "realistic_llm",
			"fresh_vs_reused", "session_contention", "multi_agent",
			"error_resilience", "streamweave", "cold_start",
		}
	} else {
		scenariosToRun = strings.Split(*scenario, ",")
	}

	// Validate scenario names
	for _, name := range scenariosToRun {
		if _, ok := scenarioRegistry[name]; !ok {
			var available []string
			for k := range scenarioRegistry {
				available = append(available, k)
			}
			log.Fatalf("unknown scenario %q (available: %s)", name, strings.Join(available, ", "))
		}
	}

	// Run each scenario
	completed := 0
	skipped := 0
	for _, name := range scenariosToRun {
		if ctx.Err() != nil {
			log.Printf("Context canceled, stopping.")
			break
		}

		// Resume: check if results already exist for this scenario
		if *resume && *outputDir != "" && scenarioHasResults(*outputDir, name) {
			log.Printf("=== Skipping scenario: %s (results exist, use --resume=false to force) ===", name)
			skipped++
			continue
		}

		log.Printf("=== Starting scenario: %s ===", name)
		fn := scenarioRegistry[name]

		reports, err := fn(ctx, *serverAddr, *httpAddr, *runs, *warmupRuns)
		if err != nil {
			log.Printf("ERROR: scenario %s failed: %v", name, err)
			log.Printf("Continuing to next scenario...")
			continue
		}

		// Write each report to disk immediately, then release memory
		for _, report := range reports {
			writeReport(report, *outputDir)
		}
		// Explicitly drop the reference so GC can reclaim the slice before the next scenario.
		reports = nil //nolint:ineffassign,wastedassign // drop reference before forced GC
		runtime.GC()

		completed++
		log.Printf("=== Completed scenario: %s ===", name)
	}

	log.Printf("All done. completed=%d skipped=%d total=%d", completed, skipped, len(scenariosToRun))
}

// writeReport marshals a report to JSON and writes it to disk (or stdout).
func writeReport(report *loadtest.BenchmarkReport, outputDir string) {
	jr := report.ToJSON()
	data, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		log.Printf("ERROR: marshal JSON for %s: %v", report.ScenarioName, err)
		return
	}

	if outputDir != "" {
		filename := fmt.Sprintf("benchmark-%s-%s.json",
			report.ScenarioName,
			time.Now().UTC().Format("20060102-150405"))
		path := filepath.Join(outputDir, filename)
		if err := os.MkdirAll(outputDir, 0o750); err != nil {
			log.Printf("ERROR: create output dir: %v", err)
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Printf("ERROR: write %s: %v", path, err)
			return
		}
		log.Printf("Results written to %s (%d bytes)", path, len(data))
	}

	// Print summary to stderr
	agg := report.Aggregate
	log.Printf("[%s] throughput: median=%.1f mean=%.1f CV=%.2f%% | p50=%.0fus p99=%.0fus",
		report.ScenarioName,
		agg.ThroughputRPS.Median, agg.ThroughputRPS.Mean, agg.ThroughputRPS.CV,
		agg.LatencyP50Us.Median, agg.LatencyP99Us.Median)

	// Only print full JSON if no output dir
	if outputDir == "" {
		fmt.Println(string(data))
	}
}

// scenarioHasResults checks if any result file exists for the given scenario.
func scenarioHasResults(outputDir, scenario string) bool {
	pattern := filepath.Join(outputDir, fmt.Sprintf("benchmark-%s-*.json", scenario))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return false
	}
	return len(matches) > 0
}
