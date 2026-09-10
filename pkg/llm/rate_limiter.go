// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// RateLimiterConfig configures the LLM rate limiter.
type RateLimiterConfig struct {
	// Enabled enables rate limiting (default: true for production)
	Enabled bool

	// RequestsPerSecond is the maximum requests allowed per second across all agents.
	// Default: 5 (conservative for AWS Bedrock)
	RequestsPerSecond float64

	// TokensPerMinute is OBSERVATIONAL ONLY today: consumption is tracked
	// (RecordTokenUsage / GetTokenUsageLastMinute) and exported in metrics, but
	// the limiter never enforces it — only the request bucket
	// (RequestsPerSecond/BurstCapacity/MinDelay) gates admission.
	// Default: 40000 (metrics baseline).
	TokensPerMinute int64

	// BurstCapacity is the maximum burst of requests allowed.
	// Default: 10 (allows brief bursts)
	BurstCapacity int

	// MinDelay is the minimum delay between requests (overrides RequestsPerSecond if larger).
	// Default: 200ms
	MinDelay time.Duration

	// MaxRetries is the maximum number of retries for 429 throttling errors.
	// Default: 5
	MaxRetries int

	// RetryBackoff is the initial backoff duration for retries (doubles each retry).
	// Default: 1s
	RetryBackoff time.Duration

	// QueueTimeout is the maximum time a request can wait in the queue.
	// Default: 5 minutes
	QueueTimeout time.Duration

	// Logger for rate limiter events
	Logger *zap.Logger
}

// DefaultRateLimiterConfig returns conservative defaults for AWS Bedrock.
func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 2.0,                    // Moderate for regional on-demand models
		TokensPerMinute:   40000,                  // Higher quota for regional models
		BurstCapacity:     5,                      // Reasonable burst allowance
		MinDelay:          300 * time.Millisecond, // Moderate spacing
		MaxRetries:        5,
		RetryBackoff:      1 * time.Second,
		QueueTimeout:      5 * time.Minute,
		Logger:            zap.NewNop(),
	}
}

// RateLimiter implements token bucket rate limiting for LLM requests.
type RateLimiter struct {
	config RateLimiterConfig

	// Token bucket for request rate limiting
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex

	// Token consumption tracking (sliding window)
	tokenWindow   []tokenUsage
	tokenWindowMu sync.Mutex

	// Request queue and processing
	queue      chan *rateLimitedRequest
	queueDepth int64
	queueMu    sync.Mutex

	// Metrics
	metrics   RateLimiterMetrics
	metricsMu sync.RWMutex

	// Lifecycle
	stopCh chan struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
}

type tokenUsage struct {
	timestamp time.Time
	tokens    int64
}

type rateLimitedRequest struct {
	ctx      context.Context
	call     func(context.Context) (interface{}, error)
	resultCh chan *rateLimitedResult
	// attempt is the 0-based attempt number. A value > 0 means this request
	// re-entered the queue as a throttling retry: it is paced through
	// admission exactly like fresh work (no queue jumping), so the configured
	// request rate holds even when the upstream is throttling.
	attempt int
}

type rateLimitedResult struct {
	result interface{}
	err    error
}

// RateLimiterMetrics tracks rate limiter performance.
type RateLimiterMetrics struct {
	TotalRequests      int64
	ThrottledRequests  int64
	QueuedRequests     int64
	DroppedRequests    int64
	AverageQueueTimeMs int64
	CurrentQueueDepth  int64
	TokensConsumed     int64
	LastThrottleTime   time.Time
}

// normalizeRateLimiterConfig backfills zero-value fields from
// DefaultRateLimiterConfig(). SharedRateLimiter keys its map on the
// normalized form, so an explicit value equal to the default shares the
// default's limiter.
func normalizeRateLimiterConfig(config RateLimiterConfig) RateLimiterConfig {
	defaults := DefaultRateLimiterConfig()

	if config.Logger == nil {
		config.Logger = defaults.Logger
	}
	if config.RequestsPerSecond == 0 {
		config.RequestsPerSecond = defaults.RequestsPerSecond
	}
	if config.TokensPerMinute == 0 {
		config.TokensPerMinute = defaults.TokensPerMinute
	}
	if config.BurstCapacity == 0 {
		config.BurstCapacity = defaults.BurstCapacity
	}
	if config.MinDelay == 0 {
		config.MinDelay = defaults.MinDelay
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = defaults.MaxRetries
	}
	if config.RetryBackoff == 0 {
		config.RetryBackoff = defaults.RetryBackoff
	}
	if config.QueueTimeout == 0 {
		config.QueueTimeout = defaults.QueueTimeout
	}
	return config
}

// NewRateLimiter creates a new rate limiter.
// Zero-value fields in config are backfilled from DefaultRateLimiterConfig().
func NewRateLimiter(config RateLimiterConfig) *RateLimiter {
	config = normalizeRateLimiterConfig(config)

	// Compute queue capacity with overflow-safe bounds check.
	// We compute using int64 and clamp to a sane maximum to avoid overflow
	// when converting back to int for channel allocation on any platform.
	const maxQueueCap int64 = 1_000_000 // hard upper bound to prevent abuse
	burst64 := int64(config.BurstCapacity)
	if burst64 < 0 {
		burst64 = 0
	}
	queueCap64 := burst64 * 2
	if queueCap64 > maxQueueCap {
		queueCap64 = maxQueueCap
		config.Logger.Warn("RateLimiter BurstCapacity too large; clamping queue capacity",
			zap.Int("original_burst", config.BurstCapacity),
			zap.Int64("max_queue_cap", maxQueueCap))
	}
	queueCap := int(queueCap64)

	rl := &RateLimiter{
		config:      config,
		tokens:      float64(config.BurstCapacity),
		maxTokens:   float64(config.BurstCapacity),
		refillRate:  config.RequestsPerSecond,
		lastRefill:  time.Now(),
		tokenWindow: make([]tokenUsage, 0, 100),
		queue:       make(chan *rateLimitedRequest, queueCap),
		stopCh:      make(chan struct{}),
	}

	// Start request processor
	rl.wg.Add(1)
	go rl.processQueue()

	// Start metrics reporter
	rl.wg.Add(1)
	go rl.reportMetrics()

	return rl
}

// Do executes a function call with rate limiting and automatic retry on throttling.
func (rl *RateLimiter) Do(ctx context.Context, call func(context.Context) (interface{}, error)) (interface{}, error) {
	if !rl.config.Enabled {
		// Rate limiting disabled - call directly
		return call(ctx)
	}

	// Check if limiter is closed
	if rl.closed.Load() {
		return nil, fmt.Errorf("rate limiter stopped")
	}

	// Create request
	req := &rateLimitedRequest{
		ctx:      ctx,
		call:     call,
		resultCh: make(chan *rateLimitedResult, 1),
	}

	// Check if context is already canceled before queuing
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Queue request with timeout
	queueCtx, cancel := context.WithTimeout(ctx, rl.config.QueueTimeout)
	defer cancel()

	rl.incrementQueueDepth()
	defer rl.decrementQueueDepth()

	queueStart := time.Now()
	select {
	case <-rl.stopCh:
		return nil, fmt.Errorf("rate limiter stopped")
	case <-ctx.Done():
		rl.recordMetric("dropped", 0)
		return nil, ctx.Err()
	case <-queueCtx.Done():
		rl.recordMetric("dropped", 0)
		return nil, fmt.Errorf("rate limiter queue timeout after %v", rl.config.QueueTimeout)
	case rl.queue <- req:
		rl.recordMetric("queued", 0)
	}

	// Wait for result
	select {
	case result := <-req.resultCh:
		queueTime := time.Since(queueStart)
		rl.updateAverageQueueTime(queueTime)
		return result.result, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-rl.stopCh:
		return nil, fmt.Errorf("rate limiter stopped")
	}
}

// processQueue processes queued requests with rate limiting.
func (rl *RateLimiter) processQueue() {
	defer rl.wg.Done()

	for {
		select {
		case req := <-rl.queue:
			// Pace ADMISSION here (request-token bucket + MinDelay spacing),
			// then run the call concurrently. Executing calls synchronously in
			// this loop serialized the whole fleet behind one LLM round-trip
			// per request — ~1 call/s regardless of configuration (issue #349).
			if !rl.waitForAdmission(req) {
				continue // waitForAdmission already delivered the error result
			}
			// wg.Add here is safe: processQueue itself holds a wg count, so
			// the counter cannot reach zero while new work is being added.
			rl.wg.Add(1)
			go func(r *rateLimitedRequest) {
				defer rl.wg.Done()
				rl.execute(r)
			}(req)
		case <-rl.stopCh:
			return
		}
	}
}

// waitForAdmission blocks until the request may start: a request token from
// the bucket (RequestsPerSecond), then MinDelay as inter-admission spacing.
// Returns false — after delivering the error result — if the request's
// context expires or the limiter stops while waiting.
func (rl *RateLimiter) waitForAdmission(req *rateLimitedRequest) bool {
	// An already-abandoned request must not consume a bucket token or stall
	// the dispatcher for MinDelay — that would slow every request queued
	// behind it for work nobody is waiting on.
	if err := req.ctx.Err(); err != nil {
		req.resultCh <- &rateLimitedResult{err: err}
		return false
	}

	for !rl.acquireToken() {
		select {
		case <-time.After(50 * time.Millisecond):
			// Continue waiting
		case <-req.ctx.Done():
			req.resultCh <- &rateLimitedResult{err: req.ctx.Err()}
			return false
		case <-rl.stopCh:
			req.resultCh <- &rateLimitedResult{err: fmt.Errorf("rate limiter stopped")}
			return false
		}
	}

	// Enforce minimum delay between request STARTS. Sleeping in the dispatch
	// loop preserves the documented spacing semantics without serializing the
	// calls themselves.
	if rl.config.MinDelay > 0 {
		select {
		case <-time.After(rl.config.MinDelay):
		case <-rl.stopCh:
			req.resultCh <- &rateLimitedResult{err: fmt.Errorf("rate limiter stopped")}
			return false
		}
	}
	return true
}

// execute runs ONE admitted attempt and delivers the result. A throttled
// attempt with retries remaining sleeps its backoff and then re-enters the
// admission path via the queue — it never re-sends directly. Every outbound
// attempt therefore consumed an admission token, so the configured request
// rate holds exactly even when the upstream is throttling; without this,
// aggregate outbound rate was admitted RPS plus all concurrent retry waves.
func (rl *RateLimiter) execute(req *rateLimitedRequest) {
	result, err := req.call(req.ctx)
	rl.recordMetric("request", 0)

	// Success or non-retryable error: deliver as-is.
	if err == nil || !isThrottlingError(err) {
		rl.deliver(req, &rateLimitedResult{result: result, err: err})
		return
	}

	rl.recordMetric("throttled", 0)

	if req.attempt >= rl.config.MaxRetries {
		// All attempts exhausted.
		rl.deliver(req, &rateLimitedResult{err: fmt.Errorf(
			"LLM request failed after %d retries due to throttling: %w",
			rl.config.MaxRetries+1, err)})
		return
	}

	delay := rl.retryDelay(req.attempt, err)
	rl.config.Logger.Warn("LLM request throttled, retrying",
		zap.Int("attempt", req.attempt+1),
		zap.Int("max_retries", rl.config.MaxRetries),
		zap.Duration("backoff", delay),
		zap.Error(err),
	)

	select {
	case <-time.After(delay):
	case <-req.ctx.Done():
		rl.deliver(req, &rateLimitedResult{err: req.ctx.Err()})
		return
	case <-rl.stopCh:
		rl.deliver(req, &rateLimitedResult{err: fmt.Errorf("rate limiter stopped during retry")})
		return
	}

	// Re-enter the admission path: the retry queues behind fresh work and is
	// paced exactly like a new request. The queue send cannot deadlock: the
	// dispatcher keeps draining, and ctx/stop provide an escape while full.
	req.attempt++
	select {
	case rl.queue <- req:
	case <-req.ctx.Done():
		rl.deliver(req, &rateLimitedResult{err: req.ctx.Err()})
	case <-rl.stopCh:
		rl.deliver(req, &rateLimitedResult{err: fmt.Errorf("rate limiter stopped during retry")})
	}
}

// deliver hands the final result to the caller waiting in Do. Each request is
// delivered exactly once onto its buffered (capacity 1) channel; the ctx/stop
// cases cover a caller that has already left.
func (rl *RateLimiter) deliver(req *rateLimitedRequest, res *rateLimitedResult) {
	select {
	case req.resultCh <- res:
	case <-req.ctx.Done():
	case <-rl.stopCh:
	}
}

// maxRetryDelay caps the exponential backoff so pathological attempt counts
// cannot produce multi-hour waits.
const maxRetryDelay = 5 * time.Minute

// retryDelay computes the wait before retry attempt attempt+1: exponential
// backoff (RetryBackoff doubled per attempt) with uniform ±50% jitter so
// concurrent throttled requests do not retry in synchronized waves, floored
// by any server-specified Retry-After carried on the error — retrying sooner
// than the server's throttle window just burns attempts inside it.
func (rl *RateLimiter) retryDelay(attempt int, err error) time.Duration {
	shift := attempt
	if shift > 30 {
		shift = 30 // cap the shift; maxRetryDelay clamps below anyway
	}
	backoff := rl.config.RetryBackoff << uint(shift)
	if backoff <= 0 || backoff > maxRetryDelay {
		backoff = maxRetryDelay
	}

	// Uniform jitter in [0.5*backoff, 1.5*backoff].
	// #nosec G404 -- retry-wave desynchronization jitter, not security-sensitive
	delay := backoff/2 + time.Duration(rand.Int64N(int64(backoff)+1))

	// Honor the server-specified wait when it is longer than the jittered
	// backoff (delta-seconds, HTTP-date, and x-ratelimit reset forms all
	// arrive here via ThrottleError.RetryAfter).
	if ra := RetryAfter(err); ra > delay {
		delay = ra
	}
	return delay
}

// acquireToken attempts to acquire a token from the bucket.
func (rl *RateLimiter) acquireToken() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens = min(rl.maxTokens, rl.tokens+elapsed*rl.refillRate)
	rl.lastRefill = now

	// Try to acquire token
	if rl.tokens >= 1.0 {
		rl.tokens -= 1.0
		return true
	}

	return false
}

// isThrottlingError checks if an error is a throttling error (HTTP 429).
func isThrottlingError(err error) bool {
	if err == nil {
		return false
	}
	// Typed throttle errors (carrying Retry-After) are always throttling,
	// regardless of message wording.
	var te *ThrottleError
	if errors.As(err, &te) {
		return true
	}
	errStr := err.Error()
	return contains(errStr, "429") ||
		contains(errStr, "ThrottlingException") ||
		contains(errStr, "TooManyRequests") ||
		contains(errStr, "rate limit") ||
		contains(errStr, "throttle")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// RecordTokenUsage records token consumption for rate limiting.
func (rl *RateLimiter) RecordTokenUsage(tokens int64) {
	rl.tokenWindowMu.Lock()
	defer rl.tokenWindowMu.Unlock()

	now := time.Now()
	rl.tokenWindow = append(rl.tokenWindow, tokenUsage{
		timestamp: now,
		tokens:    tokens,
	})

	// Remove entries older than 1 minute
	cutoff := now.Add(-1 * time.Minute)
	for i, usage := range rl.tokenWindow {
		if usage.timestamp.After(cutoff) {
			rl.tokenWindow = rl.tokenWindow[i:]
			break
		}
	}

	// Update metrics
	rl.recordMetric("tokens", tokens)
}

// GetTokenUsageLastMinute returns token consumption in the last minute.
func (rl *RateLimiter) GetTokenUsageLastMinute() int64 {
	rl.tokenWindowMu.Lock()
	defer rl.tokenWindowMu.Unlock()

	var total int64
	cutoff := time.Now().Add(-1 * time.Minute)

	for _, usage := range rl.tokenWindow {
		if usage.timestamp.After(cutoff) {
			total += usage.tokens
		}
	}

	return total
}

// recordMetric records a metric event.
func (rl *RateLimiter) recordMetric(event string, value int64) {
	rl.metricsMu.Lock()
	defer rl.metricsMu.Unlock()

	switch event {
	case "request":
		rl.metrics.TotalRequests++
	case "throttled":
		rl.metrics.ThrottledRequests++
		rl.metrics.LastThrottleTime = time.Now()
	case "queued":
		rl.metrics.QueuedRequests++
	case "dropped":
		rl.metrics.DroppedRequests++
	case "tokens":
		rl.metrics.TokensConsumed += value
	}
}

// incrementQueueDepth increments the queue depth counter.
func (rl *RateLimiter) incrementQueueDepth() {
	rl.queueMu.Lock()
	defer rl.queueMu.Unlock()
	rl.queueDepth++

	rl.metricsMu.Lock()
	rl.metrics.CurrentQueueDepth = rl.queueDepth
	rl.metricsMu.Unlock()
}

// decrementQueueDepth decrements the queue depth counter.
func (rl *RateLimiter) decrementQueueDepth() {
	rl.queueMu.Lock()
	defer rl.queueMu.Unlock()
	rl.queueDepth--

	rl.metricsMu.Lock()
	rl.metrics.CurrentQueueDepth = rl.queueDepth
	rl.metricsMu.Unlock()
}

// updateAverageQueueTime updates the average queue time metric.
func (rl *RateLimiter) updateAverageQueueTime(queueTime time.Duration) {
	rl.metricsMu.Lock()
	defer rl.metricsMu.Unlock()

	// Simple moving average (could be improved with exponential moving average)
	currentAvg := time.Duration(rl.metrics.AverageQueueTimeMs) * time.Millisecond
	newAvg := (currentAvg + queueTime) / 2
	rl.metrics.AverageQueueTimeMs = newAvg.Milliseconds()
}

// GetMetrics returns current rate limiter metrics.
func (rl *RateLimiter) GetMetrics() RateLimiterMetrics {
	rl.metricsMu.RLock()
	defer rl.metricsMu.RUnlock()
	return rl.metrics
}

// reportMetrics periodically logs rate limiter metrics.
func (rl *RateLimiter) reportMetrics() {
	defer rl.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			metrics := rl.GetMetrics()
			tokenUsage := rl.GetTokenUsageLastMinute()

			rl.config.Logger.Debug("Rate limiter metrics",
				zap.Int64("total_requests", metrics.TotalRequests),
				zap.Int64("throttled_requests", metrics.ThrottledRequests),
				zap.Int64("queued_requests", metrics.QueuedRequests),
				zap.Int64("dropped_requests", metrics.DroppedRequests),
				zap.Int64("current_queue_depth", metrics.CurrentQueueDepth),
				zap.Int64("avg_queue_time_ms", metrics.AverageQueueTimeMs),
				zap.Int64("tokens_consumed", metrics.TokensConsumed),
				zap.Int64("tokens_last_minute", tokenUsage),
			)
		case <-rl.stopCh:
			return
		}
	}
}

// Close stops the rate limiter and waits for pending requests.
func (rl *RateLimiter) Close() error {
	// Check if already closed (idempotent)
	if !rl.closed.CompareAndSwap(false, true) {
		return nil // Already closed
	}

	// Close stopCh to signal goroutines to stop
	close(rl.stopCh)

	// Wait for all goroutines to finish
	rl.wg.Wait()

	// Now safe to close queue
	close(rl.queue)

	return nil
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
