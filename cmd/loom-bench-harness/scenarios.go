package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	loadtest "github.com/teradata-labs/loom/test/loadtest"
)

// ScenarioFunc runs a benchmark scenario and returns one or more reports.
type ScenarioFunc func(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error)

// scenarioRegistry maps scenario names to their implementations.
var scenarioRegistry = map[string]ScenarioFunc{
	"sustained_load":      scenarioSustainedLoad,
	"concurrency_scaling": scenarioConcurrencyScaling,
	"peak_throughput":     scenarioPeakThroughput,
	"memory_pressure":     scenarioMemoryPressure,
	"multi_turn":          scenarioMultiTurn,
	"realistic_llm":       scenarioRealisticLLM,
	"fresh_vs_reused":     scenarioFreshVsReused,
	"session_contention":  scenarioSessionContention,
	"multi_agent":         scenarioMultiAgent,
	"error_resilience":    scenarioErrorResilience,
	"streamweave":         scenarioStreamWeave,
	"cold_start":          scenarioColdStart,
}

// reconfigureServer sends a POST /reconfigure request to the bench server.
func reconfigureServer(httpAddr string, cfg map[string]interface{}) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal reconfigure: %w", err)
	}
	resp, err := http.Post(fmt.Sprintf("http://%s/reconfigure", httpAddr), "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reconfigure request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reconfigure returned %d", resp.StatusCode)
	}
	// Brief pause for in-flight requests to drain
	time.Sleep(500 * time.Millisecond)
	return nil
}

// resetServer sends a POST /reset to clear metrics.
func resetServer(httpAddr string) error {
	resp, err := http.Post(fmt.Sprintf("http://%s/reset", httpAddr), "application/json", nil)
	if err != nil {
		return fmt.Errorf("reset request: %w", err)
	}
	defer resp.Body.Close()
	return nil
}

// runSingleScenario is a helper that runs a single BenchmarkConfig.
func runSingleScenario(ctx context.Context, cfg loadtest.BenchmarkConfig) (*loadtest.BenchmarkReport, error) {
	runner := loadtest.NewBenchmarkRunner(cfg)
	return runner.Run(ctx)
}

// baseCfg creates a BenchmarkConfig with common defaults.
func baseCfg(serverAddr string, runs, warmupRuns int, scenario string) loadtest.BenchmarkConfig {
	return loadtest.BenchmarkConfig{
		HarnessConfig: loadtest.HarnessConfig{
			ServerAddr:     serverAddr,
			RequestTimeout: 60 * time.Second,
			Query:          "SELECT * FROM test_table WHERE id = 1",
		},
		Runs:              runs,
		WarmupRuns:        warmupRuns,
		ScenarioName:      scenario,
		CollectTimeSeries: true,
		CollectHistogram:  true,
		CollectResources:  true,
	}
}

// --- Scenario 1: Sustained Load ---

func scenarioSustainedLoad(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 20, "llm_jitter_ms": 30, "llm_concurrency_limit": 10000,
	}); err != nil {
		return nil, err
	}

	cfg := baseCfg(serverAddr, runs, warmupRuns, "sustained_load")
	cfg.HarnessConfig.Concurrency = 20
	cfg.HarnessConfig.Duration = 120 * time.Second
	cfg.PerRunWarmup = 10 * time.Second

	report, err := runSingleScenario(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return []*loadtest.BenchmarkReport{report}, nil
}

// --- Scenario 2: Concurrency Scaling ---

func scenarioConcurrencyScaling(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 10, "llm_jitter_ms": 5, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	levels := []int{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024}
	var reports []*loadtest.BenchmarkReport
	var consecutiveFails int

	for _, workers := range levels {
		log.Printf("[concurrency_scaling] level=%d workers", workers)
		resetServer(httpAddr)

		cfg := baseCfg(serverAddr, runs, warmupRuns, fmt.Sprintf("concurrency_scaling_%d", workers))
		cfg.HarnessConfig.Concurrency = workers
		cfg.HarnessConfig.Duration = 30 * time.Second
		cfg.PerRunWarmup = 5 * time.Second

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("concurrency level %d: %w", workers, err)
		}
		reports = append(reports, report)

		// Auto-stop: check P99 > 1s or error rate > 10%
		agg := report.Aggregate
		if agg.LatencyP99Us.Median > 1_000_000 || avgErrorRate(report) > 0.10 {
			consecutiveFails++
			log.Printf("[concurrency_scaling] degradation detected at %d workers (consecutive=%d)", workers, consecutiveFails)
			if consecutiveFails >= 2 {
				log.Printf("[concurrency_scaling] auto-stop: 2 consecutive degraded levels")
				break
			}
		} else {
			consecutiveFails = 0
		}
	}

	return reports, nil
}

// --- Scenario 3: Peak Throughput ---

func scenarioPeakThroughput(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 0, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	levels := []int{16, 32, 64, 128, 256, 512, 1024}
	var reports []*loadtest.BenchmarkReport
	var prevThroughput float64
	var consecutiveDeclines int

	for _, workers := range levels {
		log.Printf("[peak_throughput] level=%d workers", workers)
		resetServer(httpAddr)

		cfg := baseCfg(serverAddr, runs, warmupRuns, fmt.Sprintf("peak_throughput_%d", workers))
		cfg.HarnessConfig.Concurrency = workers
		cfg.HarnessConfig.Duration = 30 * time.Second
		cfg.PerRunWarmup = 5 * time.Second

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("peak throughput level %d: %w", workers, err)
		}
		reports = append(reports, report)

		currentThroughput := report.Aggregate.ThroughputRPS.Median
		if prevThroughput > 0 && currentThroughput < prevThroughput {
			consecutiveDeclines++
			if consecutiveDeclines >= 2 {
				log.Printf("[peak_throughput] auto-stop: throughput declining for 2 consecutive levels")
				break
			}
		} else {
			consecutiveDeclines = 0
		}
		prevThroughput = currentThroughput
	}

	return reports, nil
}

// --- Scenario 4: Memory Pressure ---

func scenarioMemoryPressure(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 0, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	sessionLevels := []int{1000, 5000, 10000, 25000, 50000, 100000, 250000, 500000}
	var reports []*loadtest.BenchmarkReport

	for _, sessions := range sessionLevels {
		log.Printf("[memory_pressure] level=%d sessions", sessions)
		resetServer(httpAddr)

		cfg := baseCfg(serverAddr, runs/2, warmupRuns/2, fmt.Sprintf("memory_pressure_%d", sessions))
		if cfg.Runs < 1 {
			cfg.Runs = 1
		}
		cfg.HarnessConfig.Concurrency = 32
		cfg.HarnessConfig.TotalRequests = sessions
		cfg.HarnessConfig.LLMConcurrencyLimit = 100000

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			log.Printf("[memory_pressure] failed at %d sessions: %v", sessions, err)
			break
		}
		reports = append(reports, report)

		// Auto-stop on high error rate (likely OOM)
		if avgErrorRate(report) > 0.50 {
			log.Printf("[memory_pressure] auto-stop: >50%% error rate at %d sessions", sessions)
			break
		}
	}

	return reports, nil
}

// --- Scenario 5: Multi-Turn ---

func scenarioMultiTurn(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 0, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	turnCounts := []int{10, 50, 100, 200, 500, 1000, 2000}
	var reports []*loadtest.BenchmarkReport

	for _, turns := range turnCounts {
		log.Printf("[multi_turn] level=%d turns", turns)
		resetServer(httpAddr)

		cfg := baseCfg(serverAddr, runs/2, 0, fmt.Sprintf("multi_turn_%d", turns))
		if cfg.Runs < 1 {
			cfg.Runs = 1
		}
		cfg.HarnessConfig.Concurrency = 1
		cfg.HarnessConfig.TotalRequests = turns
		cfg.HarnessConfig.SessionID = fmt.Sprintf("multi-turn-%d", turns)
		cfg.PerRunWarmup = 0
		cfg.CollectTimeSeries = false

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("multi-turn %d: %w", turns, err)
		}
		reports = append(reports, report)

		// Auto-stop if per-turn latency > 100ms
		if report.Aggregate.LatencyP50Us.Median > 100_000 {
			log.Printf("[multi_turn] auto-stop: p50 > 100ms at %d turns", turns)
			break
		}
	}

	return reports, nil
}

// --- Scenario 6: Realistic LLM Concurrency ---

func scenarioRealisticLLM(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	latencyProfiles := []int64{100, 500, 1000}
	workerLevels := []int{10, 50, 100, 200, 500, 1000}
	var reports []*loadtest.BenchmarkReport

	for _, latMs := range latencyProfiles {
		if err := reconfigureServer(httpAddr, map[string]interface{}{
			"llm_latency_ms": latMs, "llm_jitter_ms": latMs / 5, "llm_concurrency_limit": 100000,
		}); err != nil {
			return nil, err
		}

		for _, workers := range workerLevels {
			log.Printf("[realistic_llm] latency=%dms, workers=%d", latMs, workers)
			resetServer(httpAddr)

			scenarioRuns := runs / 2
			if scenarioRuns < 1 {
				scenarioRuns = 1
			}
			cfg := baseCfg(serverAddr, scenarioRuns, 0, fmt.Sprintf("realistic_llm_%dms_%dw", latMs, workers))
			cfg.HarnessConfig.Concurrency = workers
			cfg.HarnessConfig.Duration = 30 * time.Second
			cfg.PerRunWarmup = 5 * time.Second

			report, err := runSingleScenario(ctx, cfg)
			if err != nil {
				return nil, fmt.Errorf("realistic LLM %dms/%d workers: %w", latMs, workers, err)
			}
			reports = append(reports, report)
		}
	}

	return reports, nil
}

// --- Scenario 7: Fresh vs Reused ---

func scenarioFreshVsReused(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 1, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	var reports []*loadtest.BenchmarkReport

	// Fresh sessions
	log.Printf("[fresh_vs_reused] mode=fresh")
	resetServer(httpAddr)
	freshCfg := baseCfg(serverAddr, runs, warmupRuns, "fresh_vs_reused_fresh")
	freshCfg.HarnessConfig.Concurrency = 50
	freshCfg.HarnessConfig.TotalRequests = 5000

	freshReport, err := runSingleScenario(ctx, freshCfg)
	if err != nil {
		return nil, err
	}
	reports = append(reports, freshReport)

	// Reused session — lower concurrency because all 50 workers serialize
	// on a single session lock. With N workers queued, the Nth waits for
	// (N-1) × per-request time. At 10 workers this is manageable.
	log.Printf("[fresh_vs_reused] mode=reused")
	resetServer(httpAddr)
	reusedCfg := baseCfg(serverAddr, runs, warmupRuns, "fresh_vs_reused_reused")
	reusedCfg.HarnessConfig.Concurrency = 10
	reusedCfg.HarnessConfig.TotalRequests = 1000
	reusedCfg.HarnessConfig.SessionID = "reused-session-test"

	reusedReport, err := runSingleScenario(ctx, reusedCfg)
	if err != nil {
		return nil, err
	}
	reports = append(reports, reusedReport)

	return reports, nil
}

// --- Scenario 8: Session Contention ---

func scenarioSessionContention(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 0, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, err
	}

	workerLevels := []int{1, 2, 5, 10, 25, 50, 100, 200}
	var reports []*loadtest.BenchmarkReport

	for _, workers := range workerLevels {
		log.Printf("[session_contention] workers=%d", workers)
		resetServer(httpAddr)

		scenarioRuns := runs / 2
		if scenarioRuns < 1 {
			scenarioRuns = 1
		}
		cfg := baseCfg(serverAddr, scenarioRuns, 0, fmt.Sprintf("session_contention_%d", workers))
		cfg.HarnessConfig.Concurrency = workers
		cfg.HarnessConfig.Duration = 30 * time.Second
		cfg.HarnessConfig.SessionID = "contention-test-session"
		cfg.PerRunWarmup = 5 * time.Second

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("session contention %d workers: %w", workers, err)
		}
		reports = append(reports, report)
	}

	return reports, nil
}

// --- Scenario 9: Multi-Agent ---

func scenarioMultiAgent(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	agentCounts := []int{1, 5, 10, 20, 50, 100, 200}
	var reports []*loadtest.BenchmarkReport

	for _, agents := range agentCounts {
		log.Printf("[multi_agent] agents=%d", agents)
		if err := reconfigureServer(httpAddr, map[string]interface{}{
			"num_agents": agents, "llm_latency_ms": 1, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
		}); err != nil {
			return nil, err
		}
		resetServer(httpAddr)

		scenarioRuns := runs / 2
		if scenarioRuns < 1 {
			scenarioRuns = 1
		}
		cfg := baseCfg(serverAddr, scenarioRuns, 0, fmt.Sprintf("multi_agent_%d", agents))
		cfg.HarnessConfig.Concurrency = 32
		cfg.HarnessConfig.TotalRequests = 2000

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("multi-agent %d: %w", agents, err)
		}
		reports = append(reports, report)
	}

	return reports, nil
}

// --- Scenario 10: Error Resilience ---

func scenarioErrorResilience(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	errorRates := []float64{0.10, 0.30, 0.50, 0.70}
	var reports []*loadtest.BenchmarkReport

	for _, rate := range errorRates {
		log.Printf("[error_resilience] error_rate=%.0f%%", rate*100)
		if err := reconfigureServer(httpAddr, map[string]interface{}{
			"llm_latency_ms": 10, "llm_jitter_ms": 0, "llm_error_rate": rate, "llm_concurrency_limit": 100000,
		}); err != nil {
			return nil, err
		}
		resetServer(httpAddr)

		scenarioRuns := runs / 2
		if scenarioRuns < 1 {
			scenarioRuns = 1
		}
		cfg := baseCfg(serverAddr, scenarioRuns, 0, fmt.Sprintf("error_resilience_%dpct", int(rate*100)))
		cfg.HarnessConfig.Concurrency = 20
		cfg.HarnessConfig.Duration = 60 * time.Second
		cfg.PerRunWarmup = 5 * time.Second

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("error resilience %.0f%%: %w", rate*100, err)
		}
		reports = append(reports, report)
	}

	// Reset error rate back to 0
	reconfigureServer(httpAddr, map[string]interface{}{"llm_error_rate": 0.0})
	return reports, nil
}

// --- Scenario 11: StreamWeave Scaling ---

func scenarioStreamWeave(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 10, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
		"stream_chunk_size": 10, "stream_chunk_delay_ms": 1,
	}); err != nil {
		return nil, err
	}

	workerLevels := []int{1, 5, 10, 20, 50, 100}
	var reports []*loadtest.BenchmarkReport

	for _, workers := range workerLevels {
		log.Printf("[streamweave] workers=%d", workers)
		resetServer(httpAddr)

		scenarioRuns := runs / 2
		if scenarioRuns < 1 {
			scenarioRuns = 1
		}
		cfg := baseCfg(serverAddr, scenarioRuns, 0, fmt.Sprintf("streamweave_%d", workers))
		cfg.HarnessConfig.Concurrency = workers
		cfg.HarnessConfig.Duration = 30 * time.Second
		cfg.HarnessConfig.UseStreaming = true
		cfg.PerRunWarmup = 5 * time.Second

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("streamweave %d workers: %w", workers, err)
		}
		reports = append(reports, report)
	}

	return reports, nil
}

// --- Scenario 12: Cold Start ---
// Note: In K8s, this would restart the server pod. For now, it's a simplified
// version that measures the first N requests to an already-running server
// after a reset (simulating the "warm-up" cost without actual pod restarts).

func scenarioColdStart(ctx context.Context, serverAddr, httpAddr string, runs, _ int) ([]*loadtest.BenchmarkReport, error) {
	var reports []*loadtest.BenchmarkReport

	for i := range runs {
		log.Printf("[cold_start] iteration %d/%d", i+1, runs)
		resetServer(httpAddr)

		// Measure first 10 serial requests
		cfg := baseCfg(serverAddr, 1, 0, fmt.Sprintf("cold_start_%d", i+1))
		cfg.HarnessConfig.Concurrency = 1
		cfg.HarnessConfig.TotalRequests = 11
		cfg.PerRunWarmup = 0
		cfg.CollectTimeSeries = false
		cfg.CollectHistogram = true
		cfg.CollectResources = false

		report, err := runSingleScenario(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("cold start iteration %d: %w", i+1, err)
		}
		reports = append(reports, report)
	}

	return reports, nil
}

// --- Helpers ---

// avgErrorRate returns the average error rate across all runs in a report.
func avgErrorRate(report *loadtest.BenchmarkReport) float64 {
	if len(report.Runs) == 0 {
		return 0
	}
	var sum float64
	for _, r := range report.Runs {
		sum += r.Report.ErrorRate
	}
	return sum / float64(len(report.Runs))
}
