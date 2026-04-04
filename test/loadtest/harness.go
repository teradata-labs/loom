// Package loadtest provides a gRPC load testing harness for the Loom server.
//
// It spins up a real gRPC server backed by a mock LLM provider, then drives
// concurrent Weave and StreamWeave calls to measure throughput, latency
// percentiles, error rates, and resource usage — all without consuming
// real LLM tokens.
package loadtest

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/llm/loadtest"
	"github.com/teradata-labs/loom/pkg/server"
)

// HarnessConfig controls the load test parameters.
type HarnessConfig struct {
	// Concurrency is the number of concurrent goroutines making gRPC calls.
	Concurrency int

	// TotalRequests is the total number of requests to make.
	// If 0, the harness runs for Duration instead.
	TotalRequests int

	// Duration is how long to run the load test (if TotalRequests is 0).
	Duration time.Duration

	// RequestTimeout is the per-request context timeout.
	RequestTimeout time.Duration

	// LLMConfig controls the mock LLM provider behavior.
	LLMConfig loadtest.ProviderConfig

	// UseStreaming uses StreamWeave instead of Weave.
	UseStreaming bool

	// Query is the query string sent in each request.
	Query string

	// RampUp is the duration over which to gradually ramp up to full concurrency.
	// Zero means all goroutines start immediately.
	RampUp time.Duration

	// LLMConcurrencyLimit overrides the server's LLM concurrency semaphore.
	// 0 means use the server default (5). Set high (e.g., 10000) to effectively
	// disable the limiter for load testing.
	LLMConcurrencyLimit int

	// SessionID, if set, is used for all requests (session reuse/contention testing).
	// Empty means each request creates a new session.
	SessionID string

	// NumAgents controls how many agents to register in the multi-agent server.
	// 0 or 1 means a single default agent. >1 creates N agents, and requests
	// are round-robined across them via agent_id.
	NumAgents int

	// ServerAddr, if set, connects to a remote gRPC server instead of starting
	// an in-process one. Setup() will only establish the client connection.
	// The mock LLM provider will be nil in this mode.
	ServerAddr string
}

// DefaultHarnessConfig returns sensible defaults for a quick load test.
func DefaultHarnessConfig() HarnessConfig {
	return HarnessConfig{
		Concurrency:    10,
		TotalRequests:  1000,
		RequestTimeout: 30 * time.Second,
		LLMConfig:      loadtest.DefaultConfig(),
		UseStreaming:   false,
		Query:          "SELECT * FROM test_table WHERE id = 1",
	}
}

// Report contains the load test results.
type Report struct {
	// Config is the configuration used for the test.
	Concurrency   int
	TotalRequests int
	UseStreaming  bool
	LLMLatency    string // description of LLM config

	// Timing
	WallTime time.Duration

	// Throughput
	RequestsCompleted int64
	RequestsPerSecond float64

	// Latency percentiles (request-level, including LLM simulation)
	P50 time.Duration
	P90 time.Duration
	P95 time.Duration
	P99 time.Duration
	Min time.Duration
	Max time.Duration
	Avg time.Duration

	// Error rates
	Errors    int64
	ErrorRate float64

	// LLM provider metrics
	LLMMetrics loadtest.MetricsSnapshot
}

// String returns a human-readable summary of the load test report.
func (r *Report) String() string {
	return fmt.Sprintf(`
=== Loom Load Test Report ===
Mode:           %s
Concurrency:    %d
LLM Latency:    %s

Requests:       %d completed, %d errors (%.2f%% error rate)
Wall time:      %s
Throughput:     %.1f req/s

Latency:
  min:   %s
  p50:   %s
  p90:   %s
  p95:   %s
  p99:   %s
  max:   %s
  avg:   %s

LLM Provider:
  calls:    %d
  success:  %d
  errors:   %d
  avg lat:  %s
================================`,
		modeStr(r.UseStreaming),
		r.Concurrency,
		r.LLMLatency,
		r.RequestsCompleted, r.Errors, r.ErrorRate*100,
		r.WallTime.Round(time.Millisecond),
		r.RequestsPerSecond,
		r.Min.Round(time.Microsecond),
		r.P50.Round(time.Microsecond),
		r.P90.Round(time.Microsecond),
		r.P95.Round(time.Microsecond),
		r.P99.Round(time.Microsecond),
		r.Max.Round(time.Microsecond),
		r.Avg.Round(time.Microsecond),
		r.LLMMetrics.TotalCalls,
		r.LLMMetrics.SuccessCount,
		r.LLMMetrics.ErrorCount,
		r.LLMMetrics.AvgLatency.Round(time.Microsecond),
	)
}

func modeStr(streaming bool) string {
	if streaming {
		return "StreamWeave (server streaming)"
	}
	return "Weave (unary)"
}

// Harness manages the lifecycle of a load test.
type Harness struct {
	config   HarnessConfig
	provider *loadtest.Provider
	srv      *grpc.Server
	listener net.Listener
	conn     *grpc.ClientConn
	client   loomv1.LoomServiceClient
	agentIDs []string      // GUIDs of registered agents (for round-robin)
	reqCount atomic.Uint64 // monotonic counter for round-robin
}

// NewHarness creates a new load test harness. Call Setup() to start the server.
func NewHarness(config HarnessConfig) *Harness {
	return &Harness{
		config: config,
	}
}

// Setup starts the gRPC server with a mock LLM provider.
// Returns the server address for external clients (e.g., ghz).
// If HarnessConfig.ServerAddr is set, connects to a remote server instead.
func (h *Harness) Setup() (string, error) {
	// Remote mode: connect to existing server, no local server startup
	if h.config.ServerAddr != "" {
		var err error
		h.conn, err = grpc.NewClient(h.config.ServerAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return "", fmt.Errorf("dial remote server: %w", err)
		}
		h.client = loomv1.NewLoomServiceClient(h.conn)
		return h.config.ServerAddr, nil
	}

	// Create mock LLM provider
	h.provider = loadtest.NewProvider(h.config.LLMConfig)

	// Create mock backend
	backend := &noopBackend{}

	// Create agents
	numAgents := h.config.NumAgents
	if numAgents < 1 {
		numAgents = 1
	}
	agents := make(map[string]*agent.Agent, numAgents)
	h.agentIDs = make([]string, 0, numAgents)
	for i := range numAgents {
		name := fmt.Sprintf("loadtest-agent-%d", i)
		if i == 0 {
			name = "default"
		}
		ag := agent.NewAgent(backend, h.provider, agent.WithConfig(&agent.Config{
			Name:        name,
			Description: fmt.Sprintf("Load test agent %d", i),
		}))
		agents[name] = ag
		h.agentIDs = append(h.agentIDs, ag.GetID())
	}

	multiSrv := server.NewMultiAgentServer(agents, nil)
	multiSrv.SetLogger(zap.NewNop())

	// Override LLM concurrency limit if configured
	if h.config.LLMConcurrencyLimit > 0 {
		multiSrv.SetLLMConcurrencyLimit(h.config.LLMConcurrencyLimit)
	}

	// Start gRPC server on a random port
	var err error
	h.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}

	h.srv = grpc.NewServer()
	loomv1.RegisterLoomServiceServer(h.srv, multiSrv)

	go func() {
		_ = h.srv.Serve(h.listener)
	}()

	// Create client connection
	addr := h.listener.Addr().String()
	h.conn, err = grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		h.srv.Stop()
		return "", fmt.Errorf("dial: %w", err)
	}

	h.client = loomv1.NewLoomServiceClient(h.conn)
	return addr, nil
}

// Run executes the load test and returns a report.
func (h *Harness) Run(ctx context.Context) (*Report, error) {
	results, wallTime, err := h.RunRaw(ctx)
	if err != nil {
		return nil, err
	}
	return h.buildReport(results, wallTime), nil
}

// RunRaw executes the load test and returns the raw results and wall time.
// Use this when you need access to per-request data for time-series analysis.
func (h *Harness) RunRaw(ctx context.Context) ([]result, time.Duration, error) {
	if h.client == nil {
		return nil, 0, fmt.Errorf("harness not set up; call Setup() first")
	}

	cfg := h.config

	var (
		results   []result
		resultsMu sync.Mutex
		completed atomic.Int64
	)

	// Pre-allocate results slice
	estimatedResults := cfg.TotalRequests
	if estimatedResults == 0 {
		// Estimate based on duration and concurrency
		estimatedResults = cfg.Concurrency * int(cfg.Duration.Seconds()) * 10
	}
	results = make([]result, 0, estimatedResults)

	start := time.Now()
	var wg sync.WaitGroup

	if cfg.TotalRequests > 0 {
		// Fixed request count mode: distribute across goroutines
		requestCh := make(chan struct{}, cfg.TotalRequests)
		for range cfg.TotalRequests {
			requestCh <- struct{}{}
		}
		close(requestCh)

		wg.Add(cfg.Concurrency)
		for i := range cfg.Concurrency {
			// Ramp up delay
			var rampDelay time.Duration
			if cfg.RampUp > 0 {
				rampDelay = cfg.RampUp * time.Duration(i) / time.Duration(cfg.Concurrency)
			}

			go func() {
				defer wg.Done()
				if rampDelay > 0 {
					select {
					case <-time.After(rampDelay):
					case <-ctx.Done():
						return
					}
				}

				for range requestCh {
					r := h.doRequest(ctx)
					completed.Add(1)
					resultsMu.Lock()
					results = append(results, r)
					resultsMu.Unlock()
				}
			}()
		}
	} else {
		// Duration mode: run until context or duration expires
		deadline := start.Add(cfg.Duration)
		runCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()

		wg.Add(cfg.Concurrency)
		for i := range cfg.Concurrency {
			var rampDelay time.Duration
			if cfg.RampUp > 0 {
				rampDelay = cfg.RampUp * time.Duration(i) / time.Duration(cfg.Concurrency)
			}

			go func() {
				defer wg.Done()
				if rampDelay > 0 {
					select {
					case <-time.After(rampDelay):
					case <-runCtx.Done():
						return
					}
				}

				for runCtx.Err() == nil {
					r := h.doRequest(runCtx)
					completed.Add(1)
					resultsMu.Lock()
					results = append(results, r)
					resultsMu.Unlock()
				}
			}()
		}
	}

	wg.Wait()
	wallTime := time.Since(start)

	return results, wallTime, nil
}

// doRequest makes a single Weave or StreamWeave request.
func (h *Harness) doRequest(ctx context.Context) result {
	reqCtx := ctx
	if h.config.RequestTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, h.config.RequestTimeout)
		defer cancel()
	}

	req := &loomv1.WeaveRequest{
		Query: h.config.Query,
	}

	// Session reuse: use fixed session ID if configured
	if h.config.SessionID != "" {
		req.SessionId = h.config.SessionID
	}

	// Multi-agent: round-robin across agents
	if len(h.agentIDs) > 1 {
		idx := h.reqCount.Add(1) - 1
		req.AgentId = h.agentIDs[idx%uint64(len(h.agentIDs))]
	}

	start := time.Now()

	if h.config.UseStreaming {
		stream, err := h.client.StreamWeave(reqCtx, req)
		if err != nil {
			return result{latency: time.Since(start), err: err, startedAt: start}
		}
		// Drain the stream
		for {
			_, err := stream.Recv()
			if err != nil {
				break
			}
		}
		return result{latency: time.Since(start), startedAt: start}
	}

	_, err := h.client.Weave(reqCtx, req)
	return result{latency: time.Since(start), err: err, startedAt: start}
}

type result struct {
	latency   time.Duration
	err       error
	startedAt time.Time // when the request started (for time-series bucketing)
}

// buildReport computes statistics from the results.
func (h *Harness) buildReport(results []result, wallTime time.Duration) *Report {
	report := &Report{
		Concurrency:   h.config.Concurrency,
		TotalRequests: h.config.TotalRequests,
		UseStreaming:  h.config.UseStreaming,
		LLMLatency: fmt.Sprintf("base=%s jitter=%s",
			h.config.LLMConfig.BaseLatency, h.config.LLMConfig.LatencyJitter),
		WallTime:          wallTime,
		RequestsCompleted: int64(len(results)),
	}

	// LLM metrics are only available in local mode (provider is nil in remote mode)
	if h.provider != nil {
		report.LLMMetrics = h.provider.GetMetrics().Snapshot()
	}

	if len(results) == 0 {
		return report
	}

	// Separate successes from errors
	var latencies []time.Duration
	for _, r := range results {
		if r.err != nil {
			report.Errors++
		}
		latencies = append(latencies, r.latency)
	}

	report.ErrorRate = float64(report.Errors) / float64(len(results))
	report.RequestsPerSecond = float64(len(results)) / wallTime.Seconds()

	// Sort for percentiles
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	report.Min = latencies[0]
	report.Max = latencies[len(latencies)-1]

	var totalNs int64
	for _, l := range latencies {
		totalNs += l.Nanoseconds()
	}
	report.Avg = time.Duration(totalNs / int64(len(latencies)))

	report.P50 = percentile(latencies, 0.50)
	report.P90 = percentile(latencies, 0.90)
	report.P95 = percentile(latencies, 0.95)
	report.P99 = percentile(latencies, 0.99)

	return report
}

// PercentileExported computes the p-th percentile from an unsorted slice.
// It sorts the input in place.
func PercentileExported(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return percentile(values, p)
}

// percentile returns the p-th percentile from a sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Teardown stops the server and closes connections.
func (h *Harness) Teardown() {
	if h.conn != nil {
		_ = h.conn.Close()
	}
	if h.srv != nil {
		h.srv.GracefulStop()
	}
}

// ServerAddr returns the address the gRPC server is listening on.
// Useful for pointing external load testing tools (e.g., ghz) at the server.
func (h *Harness) ServerAddr() string {
	if h.listener == nil {
		return ""
	}
	return h.listener.Addr().String()
}

// noopBackend is a minimal ExecutionBackend for load testing.
type noopBackend struct{}

func (b *noopBackend) Name() string { return "loadtest-noop" }
func (b *noopBackend) ExecuteQuery(_ context.Context, _ string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "rows", RowCount: 0}, nil
}
func (b *noopBackend) GetSchema(_ context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: resource}, nil
}
func (b *noopBackend) GetMetadata(_ context.Context, _ string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}
func (b *noopBackend) ListResources(_ context.Context, _ map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}
func (b *noopBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}
func (b *noopBackend) Close() error                 { return nil }
func (b *noopBackend) Ping(_ context.Context) error { return nil }
func (b *noopBackend) ExecuteCustomOperation(_ context.Context, _ string, _ map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not supported")
}
