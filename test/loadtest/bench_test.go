package loadtest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBenchmarkRunner_LocalSmoke runs the benchmark framework against an
// in-process server with minimal settings to verify the full pipeline:
// warmup → measured runs → aggregate stats → JSON output.
func TestBenchmarkRunner_LocalSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark smoke test in -short mode")
	}

	cfg := BenchmarkConfig{
		HarnessConfig: HarnessConfig{
			Concurrency:         5,
			TotalRequests:       50,
			RequestTimeout:      10 * time.Second,
			Query:               "SELECT 1",
			LLMConcurrencyLimit: 10000,
			LLMConfig:           DefaultHarnessConfig().LLMConfig,
		},
		Runs:              2,
		WarmupRuns:        1,
		ScenarioName:      "smoke_test",
		CollectTimeSeries: true,
		CollectHistogram:  true,
		CollectResources:  true,
	}
	cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 0

	runner := NewBenchmarkRunner(cfg)
	report, err := runner.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify environment
	assert.NotEmpty(t, report.Environment.GoVersion)
	assert.NotEmpty(t, report.Environment.GOARCH)
	assert.Greater(t, report.Environment.NumCPU, 0)
	assert.Greater(t, report.Environment.GOMAXPROCS, 0)

	// Verify runs
	assert.Equal(t, 2, len(report.Runs))
	assert.Equal(t, 1, report.WarmupRuns)

	for i, run := range report.Runs {
		assert.Greater(t, run.Report.RequestsPerSecond, 0.0, "run %d throughput", i)
		assert.Greater(t, run.Report.RequestsCompleted, int64(0), "run %d requests", i)
		assert.Equal(t, int64(0), run.Report.Errors, "run %d errors", i)
		assert.Greater(t, run.Report.P50, time.Duration(0), "run %d p50", i)
		assert.Greater(t, run.Report.P99, time.Duration(0), "run %d p99", i)
	}

	// Verify aggregate
	agg := report.Aggregate
	assert.Greater(t, agg.ThroughputRPS.Median, 0.0)
	assert.Greater(t, agg.ThroughputRPS.Mean, 0.0)
	assert.Greater(t, agg.LatencyP50Us.Median, 0.0)
	assert.Greater(t, agg.LatencyP99Us.Median, 0.0)
	// With 2 runs, CI should be computed
	assert.Greater(t, agg.ThroughputRPS.CI95Hi, agg.ThroughputRPS.CI95Low)

	// Verify JSON serialization
	jr := report.ToJSON()
	data, err := json.MarshalIndent(jr, "", "  ")
	require.NoError(t, err)
	assert.Greater(t, len(data), 100, "JSON should be non-trivial")

	// Verify JSON can be parsed back
	var parsed JSONReport
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.Equal(t, "smoke_test", parsed.Scenario)
	assert.Equal(t, 2, parsed.MeasuredRuns)
	assert.Equal(t, 1, parsed.WarmupRuns)
	assert.Equal(t, 2, len(parsed.Runs))
	assert.Equal(t, 5, parsed.Config.Concurrency)
	assert.Greater(t, parsed.Aggregate.ThroughputRPS.Median, 0.0)
}

// TestBenchmarkRunner_JSONOutput verifies WriteJSON produces a valid file.
func TestBenchmarkRunner_JSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping JSON output test in -short mode")
	}

	cfg := BenchmarkConfig{
		HarnessConfig: HarnessConfig{
			Concurrency:         2,
			TotalRequests:       20,
			RequestTimeout:      10 * time.Second,
			Query:               "SELECT 1",
			LLMConcurrencyLimit: 10000,
			LLMConfig:           DefaultHarnessConfig().LLMConfig,
		},
		Runs:              1,
		WarmupRuns:        0,
		ScenarioName:      "json_output_test",
		CollectTimeSeries: true,
		CollectHistogram:  true,
		CollectResources:  true,
	}
	cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 0

	runner := NewBenchmarkRunner(cfg)
	report, err := runner.Run(context.Background())
	require.NoError(t, err)

	// Write to temp file
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test-output.json")
	err = WriteJSON(outPath, report)
	require.NoError(t, err)

	// Read it back
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var parsed JSONReport
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "json_output_test", parsed.Scenario)
	assert.Equal(t, "1.0.0", parsed.BenchmarkVersion)
	assert.Equal(t, 1, len(parsed.Runs))
	assert.Greater(t, parsed.Runs[0].ThroughputRPS, 0.0)
}

// TestComputeStats verifies the statistical computations.
func TestComputeStats(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		check  func(t *testing.T, s Stats)
	}{
		{
			name:   "empty",
			values: nil,
			check: func(t *testing.T, s Stats) {
				assert.Equal(t, 0.0, s.Median)
				assert.Equal(t, 0.0, s.Mean)
			},
		},
		{
			name:   "single value",
			values: []float64{42.0},
			check: func(t *testing.T, s Stats) {
				assert.Equal(t, 42.0, s.Median)
				assert.Equal(t, 42.0, s.Mean)
				assert.Equal(t, 0.0, s.StdDev)
				assert.Equal(t, 42.0, s.Min)
				assert.Equal(t, 42.0, s.Max)
			},
		},
		{
			name:   "known values",
			values: []float64{10, 20, 30, 40, 50},
			check: func(t *testing.T, s Stats) {
				assert.Equal(t, 30.0, s.Median)
				assert.Equal(t, 30.0, s.Mean)
				assert.Equal(t, 10.0, s.Min)
				assert.Equal(t, 50.0, s.Max)
				assert.InDelta(t, 15.81, s.StdDev, 0.01)
				assert.Greater(t, s.CI95Hi, s.Mean)
				assert.Less(t, s.CI95Low, s.Mean)
				assert.InDelta(t, 52.7, s.CV, 0.1)
			},
		},
		{
			name:   "even count median",
			values: []float64{10, 20, 30, 40},
			check: func(t *testing.T, s Stats) {
				assert.Equal(t, 25.0, s.Median)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := ComputeStats(tc.values)
			tc.check(t, s)
		})
	}
}

// TestTimeSeries verifies throughput bucketing.
func TestTimeSeries(t *testing.T) {
	now := time.Now()
	results := []result{
		{latency: 1 * time.Millisecond, startedAt: now},
		{latency: 2 * time.Millisecond, startedAt: now.Add(100 * time.Millisecond)},
		{latency: 3 * time.Millisecond, startedAt: now.Add(500 * time.Millisecond)},
		{latency: 1 * time.Millisecond, startedAt: now.Add(1100 * time.Millisecond)},
		{latency: 2 * time.Millisecond, startedAt: now.Add(1200 * time.Millisecond)},
	}

	buckets := BuildTimeSeries(results, now)
	require.Equal(t, 2, len(buckets))
	assert.Equal(t, 0, buckets[0].Second)
	assert.Equal(t, 3, buckets[0].Requests) // 3 requests in second 0
	assert.Equal(t, 1, buckets[1].Second)
	assert.Equal(t, 2, buckets[1].Requests) // 2 requests in second 1
}

// TestResourceSampler verifies the sampler collects data.
func TestResourceSampler(t *testing.T) {
	sampler := NewResourceSampler()
	sampler.Start()
	time.Sleep(2500 * time.Millisecond) // Collect ~2-3 samples
	samples := sampler.Stop()

	require.GreaterOrEqual(t, len(samples), 2, "should have at least 2 samples")
	for _, s := range samples {
		assert.Greater(t, s.Goroutines, 0)
		assert.Greater(t, s.HeapAllocMB, 0.0)
	}
}

// TestEnvironment verifies environment capture.
func TestEnvironment(t *testing.T) {
	env := CaptureEnvironment()
	assert.NotEmpty(t, env.GoVersion)
	assert.NotEmpty(t, env.GOOS)
	assert.NotEmpty(t, env.GOARCH)
	assert.Greater(t, env.NumCPU, 0)
	assert.Greater(t, env.GOMAXPROCS, 0)
	assert.NotEmpty(t, env.GRPCVersion)
	assert.NotEmpty(t, env.TimestampUTC)
}
