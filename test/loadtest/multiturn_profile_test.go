package loadtest

import (
	"context"
	"fmt"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestProfile_MultiTurn_Latency sends sequential requests to a single session
// and measures per-turn latency, writing a CPU profile for the hot phase.
// Run with: go test -tags fts5 -v -run TestProfile_MultiTurn_Latency ./test/loadtest/
// Then: go tool pprof -http=:8080 /tmp/loom_multiturn_cpu.prof
func TestProfile_MultiTurn_Latency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping profiling test in -short mode")
	}

	cfg := DefaultHarnessConfig()
	cfg.LLMConcurrencyLimit = 10000
	cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
	cfg.LLMConfig.LatencyJitter = 0
	cfg.LLMConfig.ErrorRate = 0

	h := NewHarness(cfg)
	_, err := h.Setup()
	require.NoError(t, err)
	defer h.Teardown()

	sessionID := "profile-multiturn-session"
	totalTurns := 200

	// Warmup: 5 turns to JIT and fill caches
	for range 5 {
		_, err := h.client.Weave(context.Background(), &loomv1.WeaveRequest{
			Query:     "warmup query",
			SessionId: sessionID,
		})
		require.NoError(t, err)
	}

	// Measure per-turn latency in buckets
	type bucket struct {
		turnStart int
		turnEnd   int
		latencies []time.Duration
	}
	buckets := []bucket{
		{5, 20, nil},
		{20, 50, nil},
		{50, 80, nil},
		{80, 120, nil},
		{120, 160, nil},
		{160, 200, nil},
	}

	// Start CPU profile at turn 80 (where the cliff happens)
	var cpuFile *os.File

	fmt.Println("\n=== Per-Turn Latency Profile ===")
	fmt.Printf("%-12s %10s %10s %10s\n", "Turn", "Latency", "Avg(bucket)", "Delta")
	fmt.Println("---------------------------------------------")

	prevAvg := time.Duration(0)
	bucketIdx := 0

	for turn := 5; turn < totalTurns; turn++ {
		// Start profiling at turn 80
		if turn == 80 && cpuFile == nil {
			cpuFile, err = os.Create("/tmp/loom_multiturn_cpu.prof")
			require.NoError(t, err)
			require.NoError(t, pprof.StartCPUProfile(cpuFile))
			t.Log("CPU profiling started at turn 80")
		}

		start := time.Now()
		_, err := h.client.Weave(context.Background(), &loomv1.WeaveRequest{
			Query:     fmt.Sprintf("Turn %d: What is the status of table_%d?", turn, turn%10),
			SessionId: sessionID,
		})
		elapsed := time.Since(start)
		require.NoError(t, err)

		// Record into current bucket
		if bucketIdx < len(buckets) && turn >= buckets[bucketIdx].turnStart && turn < buckets[bucketIdx].turnEnd {
			buckets[bucketIdx].latencies = append(buckets[bucketIdx].latencies, elapsed)
		}

		// Print bucket summary when we cross a boundary
		if bucketIdx < len(buckets) && turn == buckets[bucketIdx].turnEnd-1 {
			b := buckets[bucketIdx]
			var total time.Duration
			for _, l := range b.latencies {
				total += l
			}
			avg := total / time.Duration(len(b.latencies))
			delta := ""
			if prevAvg > 0 {
				delta = fmt.Sprintf("+%s", (avg - prevAvg).Round(time.Microsecond))
			}
			fmt.Printf("%-12s %10s %10s %10s\n",
				fmt.Sprintf("%d-%d", b.turnStart, b.turnEnd),
				b.latencies[len(b.latencies)-1].Round(time.Microsecond),
				avg.Round(time.Microsecond),
				delta,
			)
			t.Logf("turns %d-%d: avg=%s, last=%s",
				b.turnStart, b.turnEnd,
				avg.Round(time.Microsecond),
				b.latencies[len(b.latencies)-1].Round(time.Microsecond))
			prevAvg = avg
			bucketIdx++
		}
	}

	// Stop CPU profile
	if cpuFile != nil {
		pprof.StopCPUProfile()
		require.NoError(t, cpuFile.Close())
		t.Logf("CPU profile written to /tmp/loom_multiturn_cpu.prof")
	}

	// Write memory profile
	memFile, err := os.Create("/tmp/loom_multiturn_mem.prof")
	require.NoError(t, err)
	require.NoError(t, pprof.WriteHeapProfile(memFile))
	require.NoError(t, memFile.Close())
	t.Logf("Memory profile written to /tmp/loom_multiturn_mem.prof")
}
