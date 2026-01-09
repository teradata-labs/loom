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
	"fmt"
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

	// TokensPerMinute is the maximum tokens allowed per minute (for token-based rate limiting).
	// Default: 100000 (AWS Bedrock typical limit)
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

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(config RateLimiterConfig) *RateLimiter {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	rl := &RateLimiter{
		config:      config,
		tokens:      float64(config.BurstCapacity),
		maxTokens:   float64(config.BurstCapacity),
		refillRate:  config.RequestsPerSecond,
		lastRefill:  time.Now(),
		tokenWindow: make([]tokenUsage, 0, 100),
		queue:       make(chan *rateLimitedRequest, config.BurstCapacity*2),
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
			rl.processRequest(req)
		case <-rl.stopCh:
			return
		}
	}
}

// processRequest processes a single request with token bucket rate limiting.
func (rl *RateLimiter) processRequest(req *rateLimitedRequest) {
	// Wait for token availability
	for {
		if rl.acquireToken() {
			break
		}

		select {
		case <-time.After(50 * time.Millisecond):
			// Continue waiting
		case <-req.ctx.Done():
			req.resultCh <- &rateLimitedResult{err: req.ctx.Err()}
			return
		case <-rl.stopCh:
			req.resultCh <- &rateLimitedResult{err: fmt.Errorf("rate limiter stopped")}
			return
		}
	}

	// Enforce minimum delay
	if rl.config.MinDelay > 0 {
		time.Sleep(rl.config.MinDelay)
	}

	// Execute call with retry on throttling
	result, err := rl.executeWithRetry(req.ctx, req.call)

	// Send result
	select {
	case req.resultCh <- &rateLimitedResult{result: result, err: err}:
	case <-req.ctx.Done():
	case <-rl.stopCh:
	}
}

// executeWithRetry executes a call with exponential backoff retry on throttling.
func (rl *RateLimiter) executeWithRetry(ctx context.Context, call func(context.Context) (interface{}, error)) (interface{}, error) {
	backoff := rl.config.RetryBackoff

	for attempt := 0; attempt <= rl.config.MaxRetries; attempt++ {
		// Execute call
		result, err := call(ctx)
		rl.recordMetric("request", 0)

		// Check for throttling error
		if err != nil && isThrottlingError(err) {
			rl.recordMetric("throttled", 0)
			rl.config.Logger.Warn("LLM request throttled, retrying",
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", rl.config.MaxRetries),
				zap.Duration("backoff", backoff),
				zap.Error(err),
			)

			// Exponential backoff before retry (except on last attempt)
			if attempt < rl.config.MaxRetries {
				select {
				case <-time.After(backoff):
					backoff *= 2 // Double the backoff
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-rl.stopCh:
					return nil, fmt.Errorf("rate limiter stopped during retry")
				}
				continue
			}
			// Last attempt failed with throttling - continue to return formatted error
			continue
		}

		// Success or non-retryable error
		return result, err
	}

	// All retries exhausted
	return nil, fmt.Errorf("LLM request failed after %d retries due to throttling", rl.config.MaxRetries+1)
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

			rl.config.Logger.Info("Rate limiter metrics",
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
