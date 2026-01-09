// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"syscall"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
)

// HTTPError represents an HTTP error with status code
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// RetryableJudge wraps a Judge with retry and circuit breaker logic
type RetryableJudge struct {
	judge          Judge
	retryConfig    *loomv1.RetryConfig
	circuitBreaker *CircuitBreaker
	logger         *zap.Logger
}

// NewRetryableJudge creates a new retryable judge wrapper
func NewRetryableJudge(judge Judge, config *loomv1.JudgeConfig, logger *zap.Logger) *RetryableJudge {
	if logger == nil {
		logger = zap.NewNop()
	}

	retryConfig := config.RetryConfig
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig()
	}

	// Apply defaults
	if retryConfig.MaxAttempts == 0 {
		retryConfig.MaxAttempts = 3
	}
	if retryConfig.InitialBackoffMs == 0 {
		retryConfig.InitialBackoffMs = 1000
	}
	if retryConfig.MaxBackoffMs == 0 {
		retryConfig.MaxBackoffMs = 8000
	}
	if retryConfig.BackoffMultiplier == 0 {
		retryConfig.BackoffMultiplier = 2.0
	}
	if len(retryConfig.RetryOnStatus) == 0 {
		retryConfig.RetryOnStatus = []int32{429, 500, 502, 503}
	}

	// Create circuit breaker
	var circuitBreaker *CircuitBreaker
	if retryConfig.CircuitBreaker != nil {
		circuitBreaker = NewCircuitBreaker(retryConfig.CircuitBreaker)
	} else {
		// Default circuit breaker enabled
		circuitBreaker = NewCircuitBreaker(DefaultCircuitBreakerConfig())
	}

	return &RetryableJudge{
		judge:          judge,
		retryConfig:    retryConfig,
		circuitBreaker: circuitBreaker,
		logger:         logger,
	}
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() *loomv1.RetryConfig {
	return &loomv1.RetryConfig{
		MaxAttempts:       3,
		InitialBackoffMs:  1000,
		MaxBackoffMs:      8000,
		BackoffMultiplier: 2.0,
		RetryOnStatus:     []int32{429, 500, 502, 503},
		CircuitBreaker:    DefaultCircuitBreakerConfig(),
	}
}

// Evaluate implements the Judge interface with retry and circuit breaker logic
func (rj *RetryableJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	// Check circuit breaker
	if !rj.circuitBreaker.AllowRequest() {
		stats := rj.circuitBreaker.GetStats()
		return nil, fmt.Errorf("circuit breaker open for judge %s (state: %s, failures: %d, time since last change: %s)",
			rj.judge.Name(),
			stats.State.String(),
			stats.FailureCount,
			stats.TimeSinceLastChange,
		)
	}

	var lastErr error
	maxAttempts := rj.retryConfig.MaxAttempts

	for attempt := int32(0); attempt <= maxAttempts; attempt++ {
		// Log attempt
		if attempt > 0 {
			rj.logger.Info("Retrying judge evaluation",
				zap.String("judge", rj.judge.Name()),
				zap.Int32("attempt", attempt+1),
				zap.Int32("max_attempts", maxAttempts+1),
			)
		}

		// Attempt evaluation
		result, err := rj.judge.Evaluate(ctx, evalCtx)

		// Success
		if err == nil {
			rj.circuitBreaker.RecordSuccess()
			if attempt > 0 {
				rj.logger.Info("Judge evaluation succeeded after retry",
					zap.String("judge", rj.judge.Name()),
					zap.Int32("attempt", attempt+1),
				)
			}
			return result, nil
		}

		// Record error
		lastErr = err

		// Check if error is retryable
		if !rj.isRetryable(err) {
			rj.logger.Debug("Non-retryable error, not retrying",
				zap.String("judge", rj.judge.Name()),
				zap.Error(err),
				zap.Int32("attempt", attempt+1),
			)
			rj.circuitBreaker.RecordFailure()
			return nil, fmt.Errorf("judge %s failed with non-retryable error: %w", rj.judge.Name(), err)
		}

		// Last attempt? Don't sleep, record failure and return
		if attempt == maxAttempts {
			rj.logger.Warn("Judge evaluation failed after all retries",
				zap.String("judge", rj.judge.Name()),
				zap.Int32("attempts", attempt+1),
				zap.Error(err),
			)
			rj.circuitBreaker.RecordFailure()
			break
		}

		// Calculate backoff
		backoff := rj.calculateBackoff(attempt)

		rj.logger.Info("Judge evaluation failed, backing off before retry",
			zap.String("judge", rj.judge.Name()),
			zap.Error(err),
			zap.Int32("attempt", attempt+1),
			zap.Duration("backoff", backoff),
		)

		// Wait for backoff or context cancellation
		select {
		case <-time.After(backoff):
			// Continue to next attempt
		case <-ctx.Done():
			rj.circuitBreaker.RecordFailure()
			return nil, fmt.Errorf("judge %s evaluation cancelled during retry: %w", rj.judge.Name(), ctx.Err())
		}
	}

	// All attempts failed
	return nil, fmt.Errorf("judge %s failed after %d attempts: %w",
		rj.judge.Name(), maxAttempts+1, lastErr)
}

// Name implements the Judge interface
func (rj *RetryableJudge) Name() string {
	return rj.judge.Name()
}

// Config implements the Judge interface
func (rj *RetryableJudge) Config() *loomv1.JudgeConfig {
	return rj.judge.Config()
}

// ID implements the Judge interface
func (rj *RetryableJudge) ID() string {
	return rj.judge.ID()
}

// Criteria implements the Judge interface
func (rj *RetryableJudge) Criteria() []string {
	return rj.judge.Criteria()
}

// Weight implements the Judge interface
func (rj *RetryableJudge) Weight() float64 {
	return rj.judge.Weight()
}

// Criticality implements the Judge interface
func (rj *RetryableJudge) Criticality() loomv1.JudgeCriticality {
	return rj.judge.Criticality()
}

// Dimensions implements the Judge interface
func (rj *RetryableJudge) Dimensions() []loomv1.JudgeDimension {
	return rj.judge.Dimensions()
}

// isRetryable determines if an error should trigger a retry
func (rj *RetryableJudge) isRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for HTTP errors with retryable status codes
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		for _, status := range rj.retryConfig.RetryOnStatus {
			if httpErr.StatusCode == int(status) {
				return true
			}
		}
	}

	// Check for context deadline exceeded (timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check for network errors
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	// Check error message for common retryable errors
	errMsg := err.Error()
	retryablePatterns := []string{
		"timeout",
		"temporary failure",
		"connection reset",
		"connection refused",
		"no such host",
		"i/o timeout",
		"rate limit",
		"too many requests",
	}

	for _, pattern := range retryablePatterns {
		if contains(errMsg, pattern) {
			return true
		}
	}

	return false
}

// calculateBackoff calculates the backoff duration for the given attempt using exponential backoff
func (rj *RetryableJudge) calculateBackoff(attempt int32) time.Duration {
	// Exponential backoff: initialBackoff * (multiplier ^ attempt)
	backoffMs := float64(rj.retryConfig.InitialBackoffMs) * math.Pow(rj.retryConfig.BackoffMultiplier, float64(attempt))

	// Cap at max backoff
	if backoffMs > float64(rj.retryConfig.MaxBackoffMs) {
		backoffMs = float64(rj.retryConfig.MaxBackoffMs)
	}

	return time.Duration(backoffMs) * time.Millisecond
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || indexAny(s, substr) >= 0)
}

// indexAny returns the index of any substring match (simple implementation)
func indexAny(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// GetCircuitBreakerStats returns current circuit breaker statistics
func (rj *RetryableJudge) GetCircuitBreakerStats() CircuitBreakerStats {
	return rj.circuitBreaker.GetStats()
}

// ResetCircuitBreaker resets the circuit breaker to closed state
func (rj *RetryableJudge) ResetCircuitBreaker() {
	rj.circuitBreaker.Reset()
}

// NewHTTPError creates a new HTTP error from an HTTP response
func NewHTTPError(resp *http.Response) error {
	return &HTTPError{
		StatusCode: resp.StatusCode,
		Message:    resp.Status,
	}
}
