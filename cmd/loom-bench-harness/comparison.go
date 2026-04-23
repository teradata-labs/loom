package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/teradata-labs/loom/deploy/comparison/langgraph/benchpb"
	loadtest "github.com/teradata-labs/loom/test/loadtest"
)

func init() {
	scenarioRegistry["comparison"] = scenarioComparison
	scenarioRegistry["comparison_concurrency"] = scenarioComparisonConcurrency
	scenarioRegistry["comparison_langgraph_only"] = scenarioLangGraphOnly
}

// langgraphAddr is set from the --langgraph-addr flag in main.go.
var langgraphAddr string

// scenarioComparison runs the head-to-head comparison per spec Section 4.
//
// The comparison runs sequentially: Loom first, then LangGraph. Both use
// the same server node with identical resource limits (31 CPU, 112Gi,
// Guaranteed QoS). The harness tears down the Loom server and deploys
// LangGraph on the same node between the two benchmarks.
//
// Since the harness binary cannot manage K8s deployments directly in this
// scenario (that's the orchestration script's job), this implementation
// assumes both servers are already reachable at their respective addresses.
// The orchestration script (or manual steps) is responsible for:
//  1. Deploy Loom server on server node → run harness with --scenario=comparison_loom
//  2. Tear down Loom, deploy LangGraph on same node → run harness with --scenario=comparison_langgraph
//  3. Combine results
//
// When --langgraph-addr is provided, it runs both in one shot (assuming
// both are reachable, with the caveat that they may not have equal resources).
func scenarioComparison(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if langgraphAddr == "" {
		return nil, fmt.Errorf("--langgraph-addr is required for the comparison scenario")
	}

	var reports []*loadtest.BenchmarkReport

	// --- Benchmark Loom ---
	log.Printf("[comparison] Benchmarking Loom at %s", serverAddr)
	if err := reconfigureServer(httpAddr, map[string]interface{}{
		"llm_latency_ms": 1, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
	}); err != nil {
		return nil, fmt.Errorf("reconfigure loom: %w", err)
	}
	_ = resetServer(httpAddr)

	loomCfg := baseCfg(serverAddr, runs, warmupRuns, "comparison_loom")
	loomCfg.Concurrency = 20
	loomCfg.TotalRequests = 500

	loomReport, err := runSingleScenario(ctx, loomCfg)
	if err != nil {
		return nil, fmt.Errorf("loom benchmark: %w", err)
	}
	reports = append(reports, loomReport)
	log.Printf("[comparison] Loom: median=%.1f req/s", loomReport.Aggregate.ThroughputRPS.Median)

	// --- Benchmark LangGraph ---
	log.Printf("[comparison] Benchmarking LangGraph at %s", langgraphAddr)

	lgReport, err := runLangGraphBenchmark(ctx, langgraphAddr, runs, warmupRuns)
	if err != nil {
		return nil, fmt.Errorf("langgraph benchmark: %w", err)
	}
	reports = append(reports, lgReport)

	// Print side-by-side summary
	loomTPut := loomReport.Aggregate.ThroughputRPS.Median
	lgTPut := lgReport.Aggregate.ThroughputRPS.Median
	log.Printf("[comparison] === RESULTS ===")
	log.Printf("[comparison] Loom:      %.1f req/s (p50=%.0fµs, p99=%.0fµs)",
		loomTPut, loomReport.Aggregate.LatencyP50Us.Median, loomReport.Aggregate.LatencyP99Us.Median)
	log.Printf("[comparison] LangGraph: %.1f req/s (p50=%.0fµs, p99=%.0fµs)",
		lgTPut, lgReport.Aggregate.LatencyP50Us.Median, lgReport.Aggregate.LatencyP99Us.Median)

	return reports, nil
}

// --- Comparison Scenario: Concurrency Scaling ---

// scenarioComparisonConcurrency sweeps worker counts on both frameworks to
// show how throughput scales with concurrency. This is a fair comparison
// because both frameworks handle concurrency at the framework level.
func scenarioComparisonConcurrency(ctx context.Context, serverAddr, httpAddr string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if langgraphAddr == "" {
		return nil, fmt.Errorf("--langgraph-addr is required for comparison_concurrency")
	}

	levels := []int{1, 5, 10, 20, 50, 100}
	var reports []*loadtest.BenchmarkReport

	for _, workers := range levels {
		log.Printf("[comparison_concurrency] workers=%d", workers)

		// --- Loom ---
		if err := reconfigureServer(httpAddr, map[string]interface{}{
			"llm_latency_ms": 1, "llm_jitter_ms": 0, "llm_concurrency_limit": 100000,
		}); err != nil {
			return nil, err
		}
		_ = resetServer(httpAddr)

		loomCfg := baseCfg(serverAddr, runs, 0, fmt.Sprintf("comparison_concurrency_loom_%dw", workers))
		loomCfg.Concurrency = workers
		loomCfg.TotalRequests = 500
		loomCfg.CollectHistogram = false
		loomCfg.CollectTimeSeries = false

		loomReport, err := runSingleScenario(ctx, loomCfg)
		if err != nil {
			return nil, fmt.Errorf("loom concurrency %d: %w", workers, err)
		}
		reports = append(reports, loomReport)

		// --- LangGraph ---
		lgReport, err := runLangGraphConcurrency(ctx, langgraphAddr, workers, 500, runs)
		if err != nil {
			return nil, fmt.Errorf("langgraph concurrency %d: %w", workers, err)
		}
		reports = append(reports, lgReport)

		log.Printf("[comparison_concurrency] workers=%d: loom=%.1f req/s, langgraph=%.1f req/s",
			workers, loomReport.Aggregate.ThroughputRPS.Median, lgReport.Aggregate.ThroughputRPS.Median)
	}

	return reports, nil
}

// runLangGraphConcurrency benchmarks LangGraph at a specific concurrency level.
func runLangGraphConcurrency(ctx context.Context, addr string, concurrency, totalRequests, runs int) (*loadtest.BenchmarkReport, error) {
	env := loadtest.CaptureEnvironment()
	report := &loadtest.BenchmarkReport{
		Environment:  env,
		ScenarioName: fmt.Sprintf("comparison_concurrency_langgraph_%dw", concurrency),
		HarnessConfig: loadtest.HarnessConfig{
			Concurrency:   concurrency,
			TotalRequests: totalRequests,
		},
	}

	for i := range runs {
		results, wallTime, err := driveLangGraph(ctx, addr, concurrency, totalRequests)
		if err != nil {
			return nil, fmt.Errorf("run %d: %w", i+1, err)
		}
		report.Runs = append(report.Runs, buildLGRunResult(results, wallTime, concurrency, totalRequests))
	}
	report.Aggregate = computeLGAggregate(report.Runs)
	return report, nil
}

// scenarioLangGraphOnly benchmarks just LangGraph (for when Loom isn't deployed).
func scenarioLangGraphOnly(ctx context.Context, _, _ string, runs, warmupRuns int) ([]*loadtest.BenchmarkReport, error) {
	if langgraphAddr == "" {
		return nil, fmt.Errorf("--langgraph-addr is required")
	}
	log.Printf("[langgraph_only] Benchmarking LangGraph at %s (20 workers, 500 requests, 1ms mock LLM)", langgraphAddr)

	report, err := runLangGraphBenchmark(ctx, langgraphAddr, runs, warmupRuns)
	if err != nil {
		return nil, err
	}
	log.Printf("[langgraph_only] median=%.1f req/s, p50=%.0fµs, p99=%.0fµs",
		report.Aggregate.ThroughputRPS.Median,
		report.Aggregate.LatencyP50Us.Median,
		report.Aggregate.LatencyP99Us.Median)

	return []*loadtest.BenchmarkReport{report}, nil
}

// runLangGraphBenchmark drives the LangGraph BenchService.Process RPC
// using the same concurrency and request count as the Loom benchmark.
func runLangGraphBenchmark(ctx context.Context, addr string, runs, warmupRuns int) (*loadtest.BenchmarkReport, error) {
	env := loadtest.CaptureEnvironment()

	report := &loadtest.BenchmarkReport{
		Environment:  env,
		ScenarioName: "comparison_langgraph",
		HarnessConfig: loadtest.HarnessConfig{
			Concurrency:   20,
			TotalRequests: 500,
		},
		WarmupRuns: warmupRuns,
	}

	// Warmup
	for i := range warmupRuns {
		log.Printf("[langgraph warmup %d/%d]", i+1, warmupRuns)
		_, _, err := driveLangGraph(ctx, addr, 20, 500)
		if err != nil {
			return nil, fmt.Errorf("warmup %d: %w", i+1, err)
		}
	}

	// Measured runs
	for i := range runs {
		log.Printf("[langgraph run %d/%d]", i+1, runs)
		results, wallTime, err := driveLangGraph(ctx, addr, 20, 500)
		if err != nil {
			return nil, fmt.Errorf("run %d: %w", i+1, err)
		}

		runResult := buildLGRunResult(results, wallTime, 20, 500)
		report.Runs = append(report.Runs, runResult)

		log.Printf("[langgraph run %d/%d] %.1f req/s, p50=%s, p99=%s, errors=%d",
			i+1, runs, runResult.Report.RequestsPerSecond,
			runResult.Report.P50.Round(time.Microsecond),
			runResult.Report.P99.Round(time.Microsecond),
			runResult.Report.Errors)
	}

	report.Aggregate = computeLGAggregate(report.Runs)
	return report, nil
}

// --- Helpers ---

type lgResult struct {
	latency time.Duration
	err     error
}

// buildLGRunResult converts raw LangGraph results into a RunResult.
func buildLGRunResult(results []lgResult, wallTime time.Duration, concurrency, totalRequests int) loadtest.RunResult {
	latencies := make([]time.Duration, len(results))
	var errCount int64
	var totalNs int64
	for j, r := range results {
		latencies[j] = r.latency
		totalNs += r.latency.Nanoseconds()
		if r.err != nil {
			errCount++
		}
	}
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })

	rps := float64(len(results)) / wallTime.Seconds()
	avgNs := totalNs / int64(len(latencies))

	return loadtest.RunResult{
		Report: &loadtest.Report{
			Concurrency:       concurrency,
			TotalRequests:     totalRequests,
			WallTime:          wallTime,
			RequestsCompleted: int64(len(results)),
			RequestsPerSecond: rps,
			P50:               loadtest.PercentileExported(latencies, 0.50),
			P90:               loadtest.PercentileExported(latencies, 0.90),
			P95:               loadtest.PercentileExported(latencies, 0.95),
			P99:               loadtest.PercentileExported(latencies, 0.99),
			Min:               latencies[0],
			Max:               latencies[len(latencies)-1],
			Avg:               time.Duration(avgNs),
			Errors:            errCount,
			ErrorRate:         float64(errCount) / float64(len(results)),
		},
	}
}

// computeLGAggregate computes aggregate stats across LangGraph runs.
func computeLGAggregate(runs []loadtest.RunResult) loadtest.JSONAggregate {
	n := len(runs)
	throughputs := make([]float64, n)
	p50s := make([]float64, n)
	p90s := make([]float64, n)
	p95s := make([]float64, n)
	p99s := make([]float64, n)
	for i, r := range runs {
		throughputs[i] = r.Report.RequestsPerSecond
		p50s[i] = float64(r.Report.P50.Microseconds())
		p90s[i] = float64(r.Report.P90.Microseconds())
		p95s[i] = float64(r.Report.P95.Microseconds())
		p99s[i] = float64(r.Report.P99.Microseconds())
	}
	return loadtest.JSONAggregate{
		ThroughputRPS: loadtest.ComputeStats(throughputs),
		LatencyP50Us:  loadtest.ComputeStats(p50s),
		LatencyP90Us:  loadtest.ComputeStats(p90s),
		LatencyP95Us:  loadtest.ComputeStats(p95s),
		LatencyP99Us:  loadtest.ComputeStats(p99s),
	}
}

// driveLangGraph sends concurrent requests to the LangGraph BenchService.
func driveLangGraph(ctx context.Context, addr string, concurrency, totalRequests int) ([]lgResult, time.Duration, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("dial langgraph: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := benchpb.NewBenchServiceClient(conn)

	requestCh := make(chan struct{}, totalRequests)
	for range totalRequests {
		requestCh <- struct{}{}
	}
	close(requestCh)

	var (
		results   []lgResult
		resultsMu sync.Mutex
	)
	results = make([]lgResult, 0, totalRequests)

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for range concurrency {
		go func() {
			defer wg.Done()
			for range requestCh {
				reqStart := time.Now()
				_, reqErr := client.Process(ctx, &benchpb.ProcessRequest{
					Query: "SELECT * FROM test_table WHERE id = 1",
				})
				r := lgResult{latency: time.Since(reqStart), err: reqErr}
				resultsMu.Lock()
				results = append(results, r)
				resultsMu.Unlock()
			}
		}()
	}

	wg.Wait()
	wallTime := time.Since(start)

	return results, wallTime, nil
}
