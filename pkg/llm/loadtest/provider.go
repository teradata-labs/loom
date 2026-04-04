// Package loadtest provides a configurable mock LLM provider for load testing.
//
// The provider simulates realistic LLM behavior — configurable latency,
// error injection, token streaming, and tool call responses — without
// consuming any real LLM tokens. This allows the entire loom stack
// (gRPC server, agent orchestration, session management, observability)
// to be hammered under concurrent load at zero cost.
package loadtest

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// ProviderConfig controls the mock provider's behavior.
type ProviderConfig struct {
	// Name is the provider name returned by Name().
	Name string

	// Model is the model identifier returned by Model().
	Model string

	// BaseLatency is the minimum latency per Chat call.
	// Simulates LLM time-to-first-token.
	BaseLatency time.Duration

	// LatencyJitter adds random jitter up to this duration on top of BaseLatency.
	// Total latency per call = BaseLatency + rand(0, LatencyJitter).
	LatencyJitter time.Duration

	// ErrorRate is the probability [0.0, 1.0] that a Chat call returns an error.
	// Simulates 429s, timeouts, and transient failures.
	ErrorRate float64

	// ErrorMessage is the error returned when error injection fires.
	// Defaults to "loadtest: simulated provider error" if empty.
	ErrorMessage string

	// Response is the canned response returned on success.
	// If nil, a default response is generated.
	Response *types.LLMResponse

	// ResponseFunc generates a dynamic response based on the input messages.
	// Takes precedence over Response if set.
	ResponseFunc func(messages []types.Message, tools []shuttle.Tool) *types.LLMResponse

	// StreamChunkSize is the number of characters per streaming chunk.
	// Only used when the provider is accessed via ChatStream.
	// Defaults to 10.
	StreamChunkSize int

	// StreamChunkDelay is the delay between streaming chunks.
	// Defaults to 5ms.
	StreamChunkDelay time.Duration

	// InputTokensPerMessage is the simulated input token count per message.
	// Defaults to 50.
	InputTokensPerMessage int

	// OutputTokens is the simulated output token count.
	// Defaults to 100.
	OutputTokens int

	// CostPerCall is the simulated cost in USD per call.
	// Defaults to 0.001.
	CostPerCall float64
}

// DefaultConfig returns a ProviderConfig with realistic defaults.
func DefaultConfig() ProviderConfig {
	return ProviderConfig{
		Name:                  "loadtest",
		Model:                 "loadtest-mock-v1",
		BaseLatency:           200 * time.Millisecond,
		LatencyJitter:         300 * time.Millisecond,
		ErrorRate:             0.0,
		StreamChunkSize:       10,
		StreamChunkDelay:      5 * time.Millisecond,
		InputTokensPerMessage: 50,
		OutputTokens:          100,
		CostPerCall:           0.001,
	}
}

// Metrics tracks provider usage during a load test.
type Metrics struct {
	TotalCalls     atomic.Int64
	SuccessCount   atomic.Int64
	ErrorCount     atomic.Int64
	TotalLatencyNs atomic.Int64 // sum of all call latencies in nanoseconds
	MinLatencyNs   atomic.Int64
	MaxLatencyNs   atomic.Int64
}

// Snapshot returns a copy of the current metrics as a plain struct.
type MetricsSnapshot struct {
	TotalCalls   int64
	SuccessCount int64
	ErrorCount   int64
	AvgLatency   time.Duration
	MinLatency   time.Duration
	MaxLatency   time.Duration
}

// Snapshot returns a point-in-time snapshot of the metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	total := m.TotalCalls.Load()
	var avg time.Duration
	if total > 0 {
		avg = time.Duration(m.TotalLatencyNs.Load() / total)
	}

	minNs := m.MinLatencyNs.Load()
	if minNs == int64(^uint64(0)>>1) { // initial sentinel
		minNs = 0
	}

	return MetricsSnapshot{
		TotalCalls:   total,
		SuccessCount: m.SuccessCount.Load(),
		ErrorCount:   m.ErrorCount.Load(),
		AvgLatency:   avg,
		MinLatency:   time.Duration(minNs),
		MaxLatency:   time.Duration(m.MaxLatencyNs.Load()),
	}
}

// Provider is a mock LLM provider for load testing.
// It implements types.LLMProvider and types.StreamingLLMProvider.
// All methods are safe for concurrent use.
type Provider struct {
	config  ProviderConfig
	metrics Metrics
	rng     *rand.Rand
	mu      sync.Mutex // protects rng
}

// NewProvider creates a new load test provider.
func NewProvider(config ProviderConfig) *Provider {
	p := &Provider{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())), // #nosec G404 -- load test mock, not crypto
	}
	// Initialize min latency to max int64 sentinel
	p.metrics.MinLatencyNs.Store(int64(^uint64(0) >> 1))
	return p
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return p.config.Name
}

// Model returns the model identifier.
func (p *Provider) Model() string {
	return p.config.Model
}

// GetMetrics returns the provider's usage metrics.
func (p *Provider) GetMetrics() *Metrics {
	return &p.metrics
}

// Chat implements types.LLMProvider. It simulates an LLM call with
// configurable latency and error injection.
func (p *Provider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	start := time.Now()

	// Simulate latency
	delay := p.simulateLatency()
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		p.recordCall(time.Since(start), false)
		return nil, ctx.Err()
	}

	// Check error injection
	if p.shouldError() {
		p.recordCall(time.Since(start), false)
		errMsg := p.config.ErrorMessage
		if errMsg == "" {
			errMsg = "loadtest: simulated provider error"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	resp := p.buildResponse(messages, tools)
	p.recordCall(time.Since(start), true)
	return resp, nil
}

// ChatStream implements types.StreamingLLMProvider. It streams the response
// content in chunks with configurable delay between chunks.
func (p *Provider) ChatStream(ctx context.Context, messages []types.Message, tools []shuttle.Tool, callback types.TokenCallback) (*types.LLMResponse, error) {
	start := time.Now()

	// Simulate initial latency (time-to-first-token)
	delay := p.simulateLatency()
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		p.recordCall(time.Since(start), false)
		return nil, ctx.Err()
	}

	// Check error injection
	if p.shouldError() {
		p.recordCall(time.Since(start), false)
		errMsg := p.config.ErrorMessage
		if errMsg == "" {
			errMsg = "loadtest: simulated provider error"
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	resp := p.buildResponse(messages, tools)

	// Stream the content in chunks
	if callback != nil && resp.Content != "" {
		chunkSize := p.config.StreamChunkSize
		if chunkSize <= 0 {
			chunkSize = 10
		}
		chunkDelay := p.config.StreamChunkDelay
		if chunkDelay <= 0 {
			chunkDelay = 5 * time.Millisecond
		}

		content := resp.Content
		for i := 0; i < len(content); i += chunkSize {
			end := min(i+chunkSize, len(content))

			select {
			case <-ctx.Done():
				p.recordCall(time.Since(start), false)
				return nil, ctx.Err()
			default:
			}

			callback(content[i:end])

			if end < len(content) {
				select {
				case <-time.After(chunkDelay):
				case <-ctx.Done():
					p.recordCall(time.Since(start), false)
					return nil, ctx.Err()
				}
			}
		}
	}

	p.recordCall(time.Since(start), true)
	return resp, nil
}

// simulateLatency returns the delay for this call.
func (p *Provider) simulateLatency() time.Duration {
	base := p.config.BaseLatency
	jitter := p.config.LatencyJitter
	if jitter <= 0 {
		return base
	}
	p.mu.Lock()
	j := time.Duration(p.rng.Int63n(int64(jitter)))
	p.mu.Unlock()
	return base + j
}

// shouldError returns true if this call should fail.
func (p *Provider) shouldError() bool {
	if p.config.ErrorRate <= 0 {
		return false
	}
	if p.config.ErrorRate >= 1.0 {
		return true
	}
	p.mu.Lock()
	r := p.rng.Float64()
	p.mu.Unlock()
	return r < p.config.ErrorRate
}

// buildResponse constructs the LLM response.
func (p *Provider) buildResponse(messages []types.Message, tools []shuttle.Tool) *types.LLMResponse {
	if p.config.ResponseFunc != nil {
		return p.config.ResponseFunc(messages, tools)
	}
	if p.config.Response != nil {
		// Return a copy to avoid mutation across goroutines
		resp := *p.config.Response
		return &resp
	}

	// Default: echo back something useful
	lastMsg := ""
	if len(messages) > 0 {
		lastMsg = messages[len(messages)-1].Content
	}

	inputTokens := len(messages) * p.config.InputTokensPerMessage
	if inputTokens == 0 {
		inputTokens = p.config.InputTokensPerMessage
	}

	return &types.LLMResponse{
		Content:    fmt.Sprintf("Load test response for: %s", lastMsg),
		StopReason: "end_turn",
		Usage: types.Usage{
			InputTokens:  inputTokens,
			OutputTokens: p.config.OutputTokens,
			TotalTokens:  inputTokens + p.config.OutputTokens,
			CostUSD:      p.config.CostPerCall,
		},
	}
}

// recordCall updates metrics atomically.
func (p *Provider) recordCall(elapsed time.Duration, success bool) {
	ns := elapsed.Nanoseconds()
	p.metrics.TotalCalls.Add(1)
	p.metrics.TotalLatencyNs.Add(ns)

	if success {
		p.metrics.SuccessCount.Add(1)
	} else {
		p.metrics.ErrorCount.Add(1)
	}

	// CAS loop for min latency
	for {
		old := p.metrics.MinLatencyNs.Load()
		if ns >= old {
			break
		}
		if p.metrics.MinLatencyNs.CompareAndSwap(old, ns) {
			break
		}
	}

	// CAS loop for max latency
	for {
		old := p.metrics.MaxLatencyNs.Load()
		if ns <= old {
			break
		}
		if p.metrics.MaxLatencyNs.CompareAndSwap(old, ns) {
			break
		}
	}
}

// Compile-time interface checks
var (
	_ types.LLMProvider          = (*Provider)(nil)
	_ types.StreamingLLMProvider = (*Provider)(nil)
)
