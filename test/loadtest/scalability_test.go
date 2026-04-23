package loadtest

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Scalability Test Scenarios
//
// These tests measure how the system behaves as specific dimensions scale:
// sessions, turns, agents, contention, and memory. Each test reports metrics
// so regressions are visible in CI logs.
// =============================================================================

// TestScalability_SessionCount tests throughput as the number of active sessions
// grows. Each request creates a new session, so by the end of the test there are
// N sessions in the Memory map. This reveals whether session map lookup or GC
// pressure from many SegmentedMemory instances degrades throughput.
func TestScalability_SessionCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	sessionCounts := []int{100, 500, 1000, 5000}

	fmt.Println("\n=== Session Count Scaling ===")
	fmt.Printf("%-12s %10s %10s %10s %10s\n", "Sessions", "Req/s", "P50", "P99", "Err%")
	fmt.Println("-----------------------------------------------------")

	for _, count := range sessionCounts {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = 20
		cfg.TotalRequests = count
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-12d %10.1f %10s %10s %9.1f%%\n",
			count,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
			report.ErrorRate*100,
		)

		t.Logf("sessions=%d: %.1f req/s, p50=%s, p99=%s",
			count, report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond))

		assert.Equal(t, int64(0), report.Errors, "no errors expected at %d sessions", count)
	}
}

// TestScalability_MultiTurn tests throughput as conversation depth grows.
// A single session is reused for all requests, simulating a 100+ turn
// conversation. Each AddMessage must remain O(1) with incremental counting,
// and GetMessagesForLLM must handle a growing message list.
func TestScalability_MultiTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	turnCounts := []int{10, 50, 100, 200}

	fmt.Println("\n=== Multi-Turn Scaling (single session) ===")
	fmt.Printf("%-12s %10s %10s %10s %10s\n", "Turns", "Req/s", "P50", "P99", "Err%")
	fmt.Println("-----------------------------------------------------")

	for _, turns := range turnCounts {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = 1 // Serial: one turn at a time on the same session
		cfg.TotalRequests = turns
		cfg.SessionID = "multi-turn-test-session"
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-12d %10.1f %10s %10s %9.1f%%\n",
			turns,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
			report.ErrorRate*100,
		)

		t.Logf("turns=%d: %.1f req/s, p50=%s, p99=%s",
			turns, report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond))
	}
}

// TestScalability_SessionContention tests throughput when many workers hammer
// the same session. This is the SegmentedMemory write lock under maximum
// contention — the worst case for a shared session (e.g., multi-agent
// workflows where sub-agents share a coordinator session).
func TestScalability_SessionContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	workerCounts := []int{1, 5, 10, 20, 50}

	fmt.Println("\n=== Session Contention (all workers → 1 session) ===")
	fmt.Printf("%-12s %10s %10s %10s %10s\n", "Workers", "Req/s", "P50", "P99", "Err%")
	fmt.Println("-----------------------------------------------------")

	for _, workers := range workerCounts {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = workers
		cfg.TotalRequests = 200
		cfg.SessionID = "contention-test-session"
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-12d %10.1f %10s %10s %9.1f%%\n",
			workers,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
			report.ErrorRate*100,
		)

		t.Logf("contention workers=%d: %.1f req/s, p50=%s, p99=%s",
			workers, report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond))
	}
}

// TestScalability_MultiAgent tests throughput with increasing numbers of
// registered agents. Requests are round-robined across agents. This reveals
// whether agent map lookup, per-agent session management, or other per-agent
// overhead degrades throughput.
func TestScalability_MultiAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	agentCounts := []int{1, 5, 10, 20}

	fmt.Println("\n=== Multi-Agent Scaling ===")
	fmt.Printf("%-12s %10s %10s %10s %10s\n", "Agents", "Req/s", "P50", "P99", "Err%")
	fmt.Println("-----------------------------------------------------")

	for _, agents := range agentCounts {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = 20
		cfg.TotalRequests = 200
		cfg.NumAgents = agents
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-12d %10.1f %10s %10s %9.1f%%\n",
			agents,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
			report.ErrorRate*100,
		)

		t.Logf("agents=%d: %.1f req/s, p50=%s, p99=%s",
			agents, report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond))
	}
}

// TestScalability_MemoryPressure creates sessions until heap usage reaches a
// target, then measures throughput and GC impact. This finds the point where
// GC pauses start affecting tail latency.
func TestScalability_MemoryPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	// Measure baseline heap
	runtime.GC()
	var baselineMem runtime.MemStats
	runtime.ReadMemStats(&baselineMem)
	baselineHeap := baselineMem.HeapAlloc

	// Create sessions in batches and measure throughput at each level
	batchSizes := []int{1000, 5000, 10000}

	fmt.Println("\n=== Memory Pressure Test ===")
	fmt.Printf("%-12s %10s %10s %10s %12s %10s\n",
		"Sessions", "Req/s", "P50", "P99", "HeapMB", "GCPauses")
	fmt.Println("-------------------------------------------------------------------")

	for _, totalSessions := range batchSizes {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = 20
		cfg.TotalRequests = totalSessions
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 0 // Minimize LLM time to isolate memory effects
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)
		gcBefore := memBefore.NumGC

		report, err := h.Run(context.Background())

		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		heapMB := float64(memAfter.HeapAlloc-baselineHeap) / (1024 * 1024)
		gcPauses := memAfter.NumGC - gcBefore

		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-12d %10.1f %10s %10s %10.1fMB %10d\n",
			totalSessions,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
			heapMB,
			gcPauses,
		)

		t.Logf("sessions=%d: %.1f req/s, heap=%.1fMB, gc_pauses=%d, p99=%s",
			totalSessions, report.RequestsPerSecond, heapMB, gcPauses,
			report.P99.Round(time.Microsecond))
	}
}

// TestScalability_FreshVsReused compares throughput when every request creates
// a new session versus when all requests reuse one session. This isolates
// the cost of session creation (SegmentedMemory allocation + tiktoken init).
func TestScalability_FreshVsReused(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scalability test in -short mode")
	}

	fmt.Println("\n=== Fresh vs Reused Session ===")
	fmt.Printf("%-20s %10s %10s %10s\n", "Mode", "Req/s", "P50", "P99")
	fmt.Println("-----------------------------------------------------")

	modes := []struct {
		name      string
		sessionID string
	}{
		{"fresh (new each)", ""},
		{"reused (shared)", "reuse-test-session"},
	}

	for _, mode := range modes {
		cfg := DefaultHarnessConfig()
		cfg.Concurrency = 20
		cfg.TotalRequests = 500
		cfg.SessionID = mode.sessionID
		cfg.LLMConcurrencyLimit = 10000
		cfg.LLMConfig.BaseLatency = 1 * time.Millisecond
		cfg.LLMConfig.LatencyJitter = 0
		cfg.LLMConfig.ErrorRate = 0

		h := NewHarness(cfg)
		_, err := h.Setup()
		require.NoError(t, err)

		report, err := h.Run(context.Background())
		h.Teardown()
		require.NoError(t, err)

		fmt.Printf("%-20s %10.1f %10s %10s\n",
			mode.name,
			report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond),
		)

		t.Logf("%s: %.1f req/s, p50=%s, p99=%s",
			mode.name, report.RequestsPerSecond,
			report.P50.Round(time.Microsecond),
			report.P99.Round(time.Microsecond))
	}
}
