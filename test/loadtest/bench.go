package loadtest

import (
	"context"
	"fmt"
	"log"
	"time"
)

// BenchmarkConfig extends HarnessConfig with multi-run and publication settings.
type BenchmarkConfig struct {
	HarnessConfig

	// Runs is the number of measured runs (default 10).
	Runs int

	// WarmupRuns is the number of warmup runs to discard (default 2).
	WarmupRuns int

	// PerRunWarmup is the duration of warmup to discard at the start of each
	// duration-based run. For request-count-based runs, 10% of requests are
	// discarded instead. Zero disables per-run warmup trimming.
	PerRunWarmup time.Duration

	// ScenarioName is the name of the scenario for the JSON report.
	ScenarioName string

	// OutputPath is where to write the JSON report. Empty means stdout only.
	OutputPath string

	// CollectTimeSeries enables per-second throughput bucketing.
	CollectTimeSeries bool

	// CollectHistogram enables raw latency histogram collection.
	CollectHistogram bool

	// CollectResources enables runtime resource sampling.
	CollectResources bool

	// CollectGCCorrelation enables GC-latency correlation analysis.
	CollectGCCorrelation bool
}

// DefaultBenchmarkConfig returns a config suitable for publication benchmarks.
func DefaultBenchmarkConfig() BenchmarkConfig {
	return BenchmarkConfig{
		HarnessConfig:        DefaultHarnessConfig(),
		Runs:                 10,
		WarmupRuns:           2,
		PerRunWarmup:         5 * time.Second,
		CollectTimeSeries:    true,
		CollectHistogram:     true,
		CollectResources:     true,
		CollectGCCorrelation: true,
	}
}

// BenchmarkRunner orchestrates multi-run benchmarks with warmup and statistics.
type BenchmarkRunner struct {
	config BenchmarkConfig
}

// NewBenchmarkRunner creates a runner with the given config.
func NewBenchmarkRunner(config BenchmarkConfig) *BenchmarkRunner {
	return &BenchmarkRunner{config: config}
}

// Run executes the full benchmark: warmup runs, measured runs, aggregate stats.
func (br *BenchmarkRunner) Run(ctx context.Context) (*BenchmarkReport, error) {
	cfg := br.config
	env := CaptureEnvironment()

	report := &BenchmarkReport{
		Environment:   env,
		ScenarioName:  cfg.ScenarioName,
		HarnessConfig: cfg.HarnessConfig,
		WarmupRuns:    cfg.WarmupRuns,
	}

	// Warmup runs (discarded)
	for i := range cfg.WarmupRuns {
		log.Printf("[warmup %d/%d] running...", i+1, cfg.WarmupRuns)
		if err := br.runOnce(ctx, nil); err != nil {
			return nil, fmt.Errorf("warmup run %d: %w", i+1, err)
		}
		log.Printf("[warmup %d/%d] done (discarded)", i+1, cfg.WarmupRuns)
	}

	// Measured runs
	for i := range cfg.Runs {
		log.Printf("[run %d/%d] running...", i+1, cfg.Runs)

		var runResult RunResult
		if err := br.runOnce(ctx, &runResult); err != nil {
			return nil, fmt.Errorf("run %d: %w", i+1, err)
		}

		// Clear raw results to free memory — aggregate only needs Report summaries
		runResult.RawResults = nil

		report.Runs = append(report.Runs, runResult)
		log.Printf("[run %d/%d] done: %.1f req/s, p50=%s, p99=%s",
			i+1, cfg.Runs,
			runResult.Report.RequestsPerSecond,
			runResult.Report.P50.Round(time.Microsecond),
			runResult.Report.P99.Round(time.Microsecond))
	}

	// Compute aggregate statistics
	report.Aggregate = br.computeAggregate(report.Runs)

	return report, nil
}

// runOnce executes a single benchmark run. If runResult is non-nil, populates it.
func (br *BenchmarkRunner) runOnce(ctx context.Context, runResult *RunResult) error {
	cfg := br.config

	h := NewHarness(cfg.HarnessConfig)
	_, err := h.Setup()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer h.Teardown()

	// Start resource sampler if requested
	var sampler *ResourceSampler
	if runResult != nil && cfg.CollectResources {
		sampler = NewResourceSampler()
		sampler.Start()
	}

	// Run and get raw results
	rawResults, wallTime, err := h.RunRaw(ctx)
	if err != nil {
		if sampler != nil {
			sampler.Stop()
		}
		return fmt.Errorf("run: %w", err)
	}

	// Stop sampler
	var resourceSamples []ResourceSample
	if sampler != nil {
		resourceSamples = sampler.Stop()
	}

	if runResult == nil {
		// Warmup run — discard everything
		return nil
	}

	// Trim per-run warmup window
	trimmedResults := br.trimWarmup(rawResults)

	// Build report from trimmed results
	runResult.Report = h.buildReport(trimmedResults, wallTime)
	runResult.RawResults = trimmedResults
	runResult.RunStart = rawResults[0].startedAt // use first result's timestamp

	// Build time series and histogram from trimmed results
	if cfg.CollectTimeSeries && len(trimmedResults) > 0 {
		runResult.ThroughputTimeline = BuildTimeSeries(trimmedResults, runResult.RunStart)
	}
	if cfg.CollectHistogram {
		runResult.LatencyHistogram = BuildLatencyHistogram(trimmedResults)
	}
	if cfg.CollectResources {
		runResult.ResourceTimeline = resourceSamples
	}
	if cfg.CollectGCCorrelation && len(trimmedResults) > 0 {
		gc := CorrelateGCWithLatency(trimmedResults, runResult.RunStart)
		runResult.GCCorrelation = &gc
	}

	return nil
}

// trimWarmup removes the warmup window from the beginning of results.
func (br *BenchmarkRunner) trimWarmup(results []result) []result {
	if len(results) == 0 {
		return results
	}

	cfg := br.config

	if cfg.HarnessConfig.Duration > 0 && cfg.PerRunWarmup > 0 {
		// Duration-based: discard first N seconds
		cutoff := results[0].startedAt.Add(cfg.PerRunWarmup)
		trimmed := make([]result, 0, len(results))
		for _, r := range results {
			if r.startedAt.After(cutoff) || r.startedAt.Equal(cutoff) {
				trimmed = append(trimmed, r)
			}
		}
		if len(trimmed) > 0 {
			return trimmed
		}
		// If trimming removed everything, return original (shouldn't happen)
		return results
	}

	if cfg.HarnessConfig.TotalRequests > 0 {
		// Request-count-based: discard first 10%
		trimCount := len(results) / 10
		if trimCount > 0 {
			return results[trimCount:]
		}
	}

	return results
}

// computeAggregate computes statistics across all measured runs.
func (br *BenchmarkRunner) computeAggregate(runs []RunResult) JSONAggregate {
	n := len(runs)
	if n == 0 {
		return JSONAggregate{}
	}

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

	return JSONAggregate{
		ThroughputRPS: ComputeStats(throughputs),
		LatencyP50Us:  ComputeStats(p50s),
		LatencyP90Us:  ComputeStats(p90s),
		LatencyP95Us:  ComputeStats(p95s),
		LatencyP99Us:  ComputeStats(p99s),
	}
}
