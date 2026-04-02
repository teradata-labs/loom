package loadtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestLoadTest_Weave_Quick runs a short load test against the Weave RPC.
// Use `go test -tags fts5 -race -v -run TestLoadTest_Weave_Quick ./test/loadtest/`
func TestLoadTest_Weave_Quick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 5
	cfg.TotalRequests = 50
	cfg.LLMConfig.BaseLatency = 10 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 5 * time.Millisecond
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	addr, err := h.Setup()
	require.NoError(t, err, "harness setup failed")
	defer h.Teardown()

	t.Logf("gRPC server listening on %s", addr)

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Equal(t, int64(50), report.RequestsCompleted)
	assert.Equal(t, int64(0), report.Errors)
	assert.Greater(t, report.RequestsPerSecond, 0.0)
	assert.Greater(t, report.P50, time.Duration(0))
	assert.GreaterOrEqual(t, report.P99, report.P50)
}

// TestLoadTest_Weave_Sustained runs a sustained load test for a fixed duration.
func TestLoadTest_Weave_Sustained(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 10
	cfg.TotalRequests = 0 // duration mode
	cfg.Duration = 5 * time.Second
	cfg.LLMConfig.BaseLatency = 20 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 30 * time.Millisecond
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	addr, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	t.Logf("gRPC server listening on %s", addr)

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Greater(t, report.RequestsCompleted, int64(0))
	assert.Greater(t, report.RequestsPerSecond, 0.0)
	// With 10 concurrent workers and ~35ms avg LLM latency, expect > 50 req/s
	t.Logf("Sustained throughput: %.1f req/s over %s", report.RequestsPerSecond, report.WallTime.Round(time.Millisecond))
}

// TestLoadTest_Weave_WithErrors tests behavior under error injection.
func TestLoadTest_Weave_WithErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 5
	cfg.TotalRequests = 100
	cfg.LLMConfig.BaseLatency = 5 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 0
	cfg.LLMConfig.ErrorRate = 0.3 // 30% of LLM calls fail

	h := NewHarness(cfg)
	_, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Equal(t, int64(100), report.RequestsCompleted)
	// The gRPC layer should return errors for failed LLM calls
	t.Logf("Error rate: %.1f%% (%d/%d)", report.ErrorRate*100, report.Errors, report.RequestsCompleted)
	t.Logf("LLM provider errors: %d/%d", report.LLMMetrics.ErrorCount, report.LLMMetrics.TotalCalls)
}

// TestLoadTest_Weave_HighConcurrency tests under high concurrency to surface contention.
func TestLoadTest_Weave_HighConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 50
	cfg.TotalRequests = 500
	cfg.LLMConfig.BaseLatency = 5 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 10 * time.Millisecond
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	_, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Equal(t, int64(500), report.RequestsCompleted)
	assert.Equal(t, int64(0), report.Errors)
	t.Logf("High concurrency (50 workers): %.1f req/s, p99=%s", report.RequestsPerSecond, report.P99.Round(time.Microsecond))
}

// TestLoadTest_Weave_RampUp tests gradual ramp-up behavior.
func TestLoadTest_Weave_RampUp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 20
	cfg.TotalRequests = 200
	cfg.RampUp = 2 * time.Second
	cfg.LLMConfig.BaseLatency = 10 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 5 * time.Millisecond
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	_, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Equal(t, int64(200), report.RequestsCompleted)
	// Wall time should be at least RampUp duration
	assert.GreaterOrEqual(t, report.WallTime, 2*time.Second)
	t.Logf("Ramp-up test: %.1f req/s with %s ramp, wall=%s", report.RequestsPerSecond, cfg.RampUp, report.WallTime.Round(time.Millisecond))
}

// TestLoadTest_StreamWeave runs a load test against the StreamWeave RPC.
func TestLoadTest_StreamWeave(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.Concurrency = 5
	cfg.TotalRequests = 50
	cfg.UseStreaming = true
	cfg.LLMConfig.BaseLatency = 10 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 5 * time.Millisecond
	cfg.LLMConfig.ErrorRate = 0
	cfg.LLMConfig.StreamChunkSize = 10
	cfg.LLMConfig.StreamChunkDelay = 1 * time.Millisecond

	h := NewHarness(cfg)
	_, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	report, err := h.Run(context.Background())
	require.NoError(t, err)

	t.Log(report.String())

	assert.Equal(t, int64(50), report.RequestsCompleted)
	assert.Greater(t, report.RequestsPerSecond, 0.0)
	t.Logf("StreamWeave: %.1f req/s, p50=%s, p99=%s", report.RequestsPerSecond, report.P50.Round(time.Microsecond), report.P99.Round(time.Microsecond))
}

// TestLoadTest_CompareLatencyProfiles runs the same load at different simulated
// LLM latencies and compares the overhead introduced by the Loom stack.
func TestLoadTest_CompareLatencyProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}

	profiles := []struct {
		name        string
		baseLatency time.Duration
		jitter      time.Duration
	}{
		{"fast (1ms)", 1 * time.Millisecond, 0},
		{"moderate (50ms)", 50 * time.Millisecond, 25 * time.Millisecond},
		{"slow (200ms)", 200 * time.Millisecond, 100 * time.Millisecond},
	}

	fmt.Println("\n=== Latency Profile Comparison ===")
	fmt.Printf("%-20s %10s %10s %10s %10s %10s\n", "Profile", "Req/s", "P50", "P90", "P99", "Overhead")
	fmt.Println("-----------------------------------------------------------------------")

	for _, p := range profiles {
		t.Run(p.name, func(t *testing.T) {
			cfg := DefaultHarnessConfig()
			cfg.Concurrency = 10
			cfg.TotalRequests = 100
			cfg.LLMConfig.BaseLatency = p.baseLatency
			cfg.LLMConfig.LatencyJitter = p.jitter
			cfg.LLMConfig.ErrorRate = 0

			h := NewHarness(cfg)
			_, err := h.Setup()
			require.NoError(t, err)
			defer h.Teardown()

			report, err := h.Run(context.Background())
			require.NoError(t, err)

			// Overhead = measured P50 - expected LLM latency (base + jitter/2)
			expectedLLM := p.baseLatency + p.jitter/2
			overhead := report.P50 - expectedLLM
			if overhead < 0 {
				overhead = 0
			}

			fmt.Printf("%-20s %10.1f %10s %10s %10s %10s\n",
				p.name,
				report.RequestsPerSecond,
				report.P50.Round(time.Microsecond),
				report.P90.Round(time.Microsecond),
				report.P99.Round(time.Microsecond),
				overhead.Round(time.Microsecond),
			)

			t.Logf("%s: %.1f req/s, overhead ~%s", p.name, report.RequestsPerSecond, overhead.Round(time.Microsecond))
		})
	}
}

// TestLoadTest_Weave_ConcurrencyScaling increases concurrent workers until
// throughput plateaus or errors appear. This finds the practical concurrency
// ceiling for the current stack.
func TestLoadTest_Weave_ConcurrencyScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency scaling test in -short mode")
	}

	// Fixed request count per level so each level runs long enough to be meaningful
	requestsPerLevel := 200
	// Start at 5 workers, double each step
	levels := []int{5, 10, 20, 40, 80, 160}

	type levelResult struct {
		concurrency int
		reqPerSec   float64
		p50         time.Duration
		p99         time.Duration
		errorRate   float64
		wallTime    time.Duration
		llmErrors   int64
	}

	var results []levelResult
	var prevRPS float64

	fmt.Println("\n=== Concurrency Scaling Test ===")
	fmt.Printf("%-12s %10s %10s %10s %10s %10s %10s\n",
		"Workers", "Req/s", "Delta", "P50", "P99", "Err%", "Wall")
	fmt.Println("--------------------------------------------------------------------------")

	for _, concurrency := range levels {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = concurrency
		cfg.TotalRequests = requestsPerLevel
		cfg.RequestTimeout = 30 * time.Second
		cfg.LLMConcurrencyLimit = 10000 // Disable the server-side LLM semaphore
		cfg.LLMConfig.BaseLatency = 10 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 5 * time.Millisecond
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		lr := levelResult{
			concurrency: concurrency,
			reqPerSec:   report.RequestsPerSecond,
			p50:         report.P50,
			p99:         report.P99,
			errorRate:   report.ErrorRate,
			wallTime:    report.WallTime,
			llmErrors:   report.LLMMetrics.ErrorCount,
		}
		results = append(results, lr)

		delta := ""
		if prevRPS > 0 {
			pctChange := (lr.reqPerSec - prevRPS) / prevRPS * 100
			delta = fmt.Sprintf("%+.1f%%", pctChange)
		}
		prevRPS = lr.reqPerSec

		fmt.Printf("%-12d %10.1f %10s %10s %10s %9.1f%% %10s\n",
			lr.concurrency,
			lr.reqPerSec,
			delta,
			lr.p50.Round(time.Microsecond),
			lr.p99.Round(time.Microsecond),
			lr.errorRate*100,
			lr.wallTime.Round(time.Millisecond),
		)

		t.Logf("concurrency=%d: %.1f req/s, p50=%s, p99=%s, err=%.1f%%",
			concurrency, lr.reqPerSec, lr.p50.Round(time.Microsecond), lr.p99.Round(time.Microsecond), lr.errorRate*100)

		// Detect plateau: if throughput gain < 5% for two consecutive levels
		if len(results) >= 3 {
			prev := results[len(results)-2]
			prevPrev := results[len(results)-3]
			gain1 := (prev.reqPerSec - prevPrev.reqPerSec) / prevPrev.reqPerSec
			gain2 := (lr.reqPerSec - prev.reqPerSec) / prev.reqPerSec
			if gain1 < 0.05 && gain2 < 0.05 {
				fmt.Printf("\n>>> Plateau detected at %d workers (%.1f req/s). Throughput gain < 5%% for two consecutive levels.\n",
					prev.concurrency, prev.reqPerSec)
				t.Logf("Plateau detected at %d workers", prev.concurrency)
				break
			}
		}

		// Detect degradation: if error rate > 10%
		if lr.errorRate > 0.10 {
			fmt.Printf("\n>>> Error threshold exceeded at %d workers (%.1f%% errors). Stopping.\n",
				concurrency, lr.errorRate*100)
			t.Logf("Error threshold at %d workers", concurrency)
			break
		}
	}

	fmt.Println("==========================================================================")

	// Summary
	if len(results) > 0 {
		best := results[0]
		for _, r := range results[1:] {
			if r.reqPerSec > best.reqPerSec {
				best = r
			}
		}
		fmt.Printf("Peak throughput: %.1f req/s at %d workers\n", best.reqPerSec, best.concurrency)
		t.Logf("Peak: %.1f req/s at %d workers", best.reqPerSec, best.concurrency)
	}
}

// BenchmarkWeave_Throughput measures raw throughput via Go benchmarks.
func BenchmarkWeave_Throughput(b *testing.B) {
	cfg := DefaultHarnessConfig()
	cfg.LLMConfig.BaseLatency = 0
	cfg.LLMConfig.LatencyJitter = 0
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	_, err := h.Setup()
	if err != nil {
		b.Fatal(err)
	}
	defer h.Teardown()

	ctx := context.Background()
	req := &loomv1.WeaveRequest{Query: "benchmark query"}

	b.ResetTimer()
	for b.Loop() {
		_, _ = h.client.Weave(ctx, req)
	}
}

// BenchmarkWeave_Throughput_Parallel measures concurrent throughput.
func BenchmarkWeave_Throughput_Parallel(b *testing.B) {
	cfg := DefaultHarnessConfig()
	cfg.LLMConfig.BaseLatency = 0
	cfg.LLMConfig.LatencyJitter = 0
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	_, err := h.Setup()
	if err != nil {
		b.Fatal(err)
	}
	defer h.Teardown()

	ctx := context.Background()
	req := &loomv1.WeaveRequest{Query: "benchmark query"}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = h.client.Weave(ctx, req)
		}
	})
}
