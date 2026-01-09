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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestNewRateLimiter(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)
	require.NotNil(t, rl)
	defer rl.Close()

	assert.Equal(t, config.RequestsPerSecond, rl.refillRate)
	assert.Equal(t, float64(config.BurstCapacity), rl.maxTokens)
	assert.Equal(t, float64(config.BurstCapacity), rl.tokens)
}

func TestRateLimiter_Do_Success(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 10 // Fast for testing

	rl := NewRateLimiter(config)
	defer rl.Close()

	callCount := 0
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callCount++
		return "success", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.Equal(t, 1, callCount)

	metrics := rl.GetMetrics()
	assert.Equal(t, int64(1), metrics.TotalRequests)
	assert.Equal(t, int64(0), metrics.ThrottledRequests)
}

func TestRateLimiter_Do_ThrottlingRetry(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 10
	config.MaxRetries = 3
	config.RetryBackoff = 10 * time.Millisecond // Fast for testing

	rl := NewRateLimiter(config)
	defer rl.Close()

	callCount := 0
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callCount++
		if callCount < 3 {
			return nil, errors.New("ThrottlingException: Too many tokens")
		}
		return "success", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.Equal(t, 3, callCount) // Called 3 times (2 failures + 1 success)

	metrics := rl.GetMetrics()
	assert.Equal(t, int64(3), metrics.TotalRequests)
	assert.Equal(t, int64(2), metrics.ThrottledRequests)
}

func TestRateLimiter_Do_ThrottlingExhausted(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 10
	config.MaxRetries = 2
	config.RetryBackoff = 10 * time.Millisecond

	rl := NewRateLimiter(config)
	defer rl.Close()

	callCount := 0
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callCount++
		return nil, errors.New("HTTP 429: rate limit exceeded")
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed after 3 retries")
	assert.Equal(t, 3, callCount) // MaxRetries=2 means 3 total attempts

	metrics := rl.GetMetrics()
	assert.Equal(t, int64(3), metrics.TotalRequests)
	assert.Equal(t, int64(3), metrics.ThrottledRequests)
}

func TestRateLimiter_Do_Disabled(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Enabled = false
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)
	defer rl.Close()

	callCount := 0
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callCount++
		return "direct", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "direct", result)
	assert.Equal(t, 1, callCount)

	// Metrics should not be updated when disabled
	metrics := rl.GetMetrics()
	assert.Equal(t, int64(0), metrics.TotalRequests)
}

func TestRateLimiter_Do_ContextCancellation(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 1 // Slow to test cancellation

	rl := NewRateLimiter(config)
	defer rl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := rl.Do(ctx, func(ctx context.Context) (interface{}, error) {
		return "should not execute", nil
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, context.Canceled, err)
}

func TestRateLimiter_ConcurrentRequests(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 20 // Fast for testing
	config.BurstCapacity = 20

	rl := NewRateLimiter(config)
	defer rl.Close()

	const numRequests = 50
	var successCount int64
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
				return fmt.Sprintf("request-%d", id), nil
			})

			if err == nil && result != nil {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	assert.Equal(t, int64(numRequests), successCount)

	metrics := rl.GetMetrics()
	assert.Equal(t, int64(numRequests), metrics.TotalRequests)
	assert.Equal(t, int64(0), metrics.DroppedRequests)
}

func TestRateLimiter_TokenBucketRefill(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 10 // 10 requests/sec = 1 token every 100ms
	config.BurstCapacity = 2

	rl := NewRateLimiter(config)
	defer rl.Close()

	// Consume burst capacity (2 tokens)
	for i := 0; i < 2; i++ {
		_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
			return "ok", nil
		})
		require.NoError(t, err)
	}

	// Tokens exhausted - next request should wait
	start := time.Now()
	_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		return "ok", nil
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	// Should have waited at least 100ms for token refill
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
}

func TestRateLimiter_QueueTimeout(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 0.1 // Very slow (1 request per 10 seconds)
	config.BurstCapacity = 1
	config.MinDelay = 0                          // Disable MinDelay for predictable timing
	config.QueueTimeout = 100 * time.Millisecond // Short timeout

	rl := NewRateLimiter(config)

	// Consume burst capacity
	_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	// Fill the queue to force timeout
	// With burst capacity exhausted and very slow refill, new requests should queue up
	// Create enough concurrent requests to fill the queue channel
	var wg sync.WaitGroup
	for i := 0; i < cap(rl.queue)+1; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
				time.Sleep(5 * time.Second) // Long-running to keep queue full
				return "ok", nil
			})
		}()
	}

	// Wait a bit for goroutines to start and fill queue
	time.Sleep(50 * time.Millisecond)

	// This request should timeout because queue is full
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		return "should not execute", nil
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "queue timeout")

	metrics := rl.GetMetrics()
	assert.Greater(t, metrics.DroppedRequests, int64(0))

	// Clean up
	rl.Close()
	wg.Wait()
}

func TestRateLimiter_RecordTokenUsage(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)
	defer rl.Close()

	// Record token usage
	rl.RecordTokenUsage(1000)
	rl.RecordTokenUsage(2000)
	rl.RecordTokenUsage(3000)

	// Check total
	total := rl.GetTokenUsageLastMinute()
	assert.Equal(t, int64(6000), total)

	// Check metrics
	metrics := rl.GetMetrics()
	assert.Equal(t, int64(6000), metrics.TokensConsumed)
}

func TestRateLimiter_TokenUsageWindow(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)
	defer rl.Close()

	// Record token usage with old timestamp (simulate old entry)
	rl.tokenWindowMu.Lock()
	rl.tokenWindow = append(rl.tokenWindow, tokenUsage{
		timestamp: time.Now().Add(-2 * time.Minute), // Older than 1 minute
		tokens:    5000,
	})
	rl.tokenWindowMu.Unlock()

	// Record recent usage
	rl.RecordTokenUsage(1000)

	// Should only count recent usage (old entry pruned)
	total := rl.GetTokenUsageLastMinute()
	assert.Equal(t, int64(1000), total)
}

func TestIsThrottlingError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "HTTP 429 error",
			err:      errors.New("HTTP 429: Too Many Requests"),
			expected: true,
		},
		{
			name:     "ThrottlingException",
			err:      errors.New("ThrottlingException: Too many tokens"),
			expected: true,
		},
		{
			name:     "TooManyRequests",
			err:      errors.New("TooManyRequests: rate limit exceeded"),
			expected: true,
		},
		{
			name:     "rate limit keyword",
			err:      errors.New("API rate limit exceeded"),
			expected: true,
		},
		{
			name:     "throttle keyword",
			err:      errors.New("Request throttled by provider"),
			expected: true,
		},
		{
			name:     "other error",
			err:      errors.New("connection timeout"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isThrottlingError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRateLimiter_MinDelay(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 100           // Very fast
	config.MinDelay = 100 * time.Millisecond // Enforce minimum delay

	rl := NewRateLimiter(config)
	defer rl.Close()

	start := time.Now()

	// Execute 2 requests
	for i := 0; i < 2; i++ {
		_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
			return "ok", nil
		})
		require.NoError(t, err)
	}

	elapsed := time.Since(start)

	// Should have enforced minimum delay between requests
	// 2 requests = 1 delay period (200ms minimum)
	assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
}

func TestRateLimiter_Metrics(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 50 // Fast for testing

	rl := NewRateLimiter(config)
	defer rl.Close()

	// Execute successful request
	_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	// Execute throttled request (retries twice, succeeds on 3rd)
	callCount := 0
	_, err = rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callCount++
		if callCount < 3 {
			return nil, errors.New("429 throttled")
		}
		return "ok", nil
	})
	require.NoError(t, err)

	// Check metrics
	metrics := rl.GetMetrics()
	assert.Equal(t, int64(4), metrics.TotalRequests) // 1 + 3 attempts
	assert.Equal(t, int64(2), metrics.ThrottledRequests)
	assert.Greater(t, metrics.QueuedRequests, int64(0))
}

func TestRateLimiter_ConcurrentThrottling(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 20
	config.BurstCapacity = 10
	config.MaxRetries = 2
	config.RetryBackoff = 10 * time.Millisecond

	rl := NewRateLimiter(config)
	defer rl.Close()

	const numRequests = 20
	var successCount int64
	var wg sync.WaitGroup

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Simulate occasional throttling
			callCount := 0
			result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
				callCount++
				// 30% chance of throttling on first attempt
				if callCount == 1 && id%3 == 0 {
					return nil, errors.New("429 rate limit")
				}
				return fmt.Sprintf("request-%d", id), nil
			})

			if err == nil && result != nil {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// All requests should eventually succeed
	assert.Equal(t, int64(numRequests), successCount)

	metrics := rl.GetMetrics()
	assert.Equal(t, int64(numRequests), metrics.QueuedRequests)
	assert.Greater(t, metrics.ThrottledRequests, int64(0)) // Some throttling occurred
}

func TestRateLimiter_Close(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)

	// Close should complete without error
	err := rl.Close()
	assert.NoError(t, err)

	// Subsequent calls should fail gracefully
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		return "should not execute", nil
	})

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "stopped")
}

func TestRateLimiter_TokenWindowPruning(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)

	rl := NewRateLimiter(config)
	defer rl.Close()

	// Add old entries
	rl.tokenWindowMu.Lock()
	rl.tokenWindow = append(rl.tokenWindow,
		tokenUsage{timestamp: time.Now().Add(-90 * time.Second), tokens: 1000},
		tokenUsage{timestamp: time.Now().Add(-70 * time.Second), tokens: 2000},
		tokenUsage{timestamp: time.Now().Add(-30 * time.Second), tokens: 3000},
	)
	rl.tokenWindowMu.Unlock()

	// Record new usage (should trigger pruning)
	rl.RecordTokenUsage(4000)

	// Only recent entries should remain (within 1 minute)
	total := rl.GetTokenUsageLastMinute()
	assert.Equal(t, int64(7000), total) // 3000 + 4000 (first two pruned)
}

func TestRateLimiter_ExponentialBackoff(t *testing.T) {
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.MaxRetries = 3
	config.RetryBackoff = 50 * time.Millisecond

	rl := NewRateLimiter(config)
	defer rl.Close()

	callTimes := make([]time.Time, 0)
	_, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		callTimes = append(callTimes, time.Now())
		return nil, errors.New("ThrottlingException")
	})

	require.Error(t, err)
	require.Len(t, callTimes, 4) // 1 initial + 3 retries

	// Verify exponential backoff: ~50ms, ~100ms, ~200ms
	delay1 := callTimes[1].Sub(callTimes[0])
	delay2 := callTimes[2].Sub(callTimes[1])
	delay3 := callTimes[3].Sub(callTimes[2])

	assert.GreaterOrEqual(t, delay1, 50*time.Millisecond)
	assert.GreaterOrEqual(t, delay2, 100*time.Millisecond)
	assert.GreaterOrEqual(t, delay3, 200*time.Millisecond)

	// Verify exponential growth
	assert.Greater(t, delay2, delay1)
	assert.Greater(t, delay3, delay2)
}

func TestRateLimiter_RaceConditions(t *testing.T) {
	// This test is designed to catch race conditions with -race detector
	config := DefaultRateLimiterConfig()
	config.Logger = zaptest.NewLogger(t)
	config.RequestsPerSecond = 50

	rl := NewRateLimiter(config)
	defer rl.Close()

	var wg sync.WaitGroup
	const numGoroutines = 20
	const requestsPerGoroutine = 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < requestsPerGoroutine; j++ {
				_, _ = rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
					return "ok", nil
				})

				// Record token usage
				rl.RecordTokenUsage(int64(100 + j))

				// Get metrics
				_ = rl.GetMetrics()

				// Get token usage
				_ = rl.GetTokenUsageLastMinute()
			}
		}()
	}

	wg.Wait()
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{"exact match", "429", "429", true},
		{"at start", "429 error", "429", true},
		{"at end", "error 429", "429", true},
		{"in middle", "error 429 occurred", "429", true},
		{"not found", "error 500", "429", false},
		{"empty substr", "test", "", true},
		{"empty string", "", "test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func BenchmarkRateLimiter_Do(b *testing.B) {
	config := DefaultRateLimiterConfig()
	config.Logger = zap.NewNop()
	config.RequestsPerSecond = 1000 // Very fast for benchmarking

	rl := NewRateLimiter(config)
	defer rl.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rl.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return "ok", nil
		})
	}
}

func BenchmarkRateLimiter_Concurrent(b *testing.B) {
	config := DefaultRateLimiterConfig()
	config.Logger = zap.NewNop()
	config.RequestsPerSecond = 1000
	config.BurstCapacity = 100

	rl := NewRateLimiter(config)
	defer rl.Close()

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = rl.Do(ctx, func(ctx context.Context) (interface{}, error) {
				return "ok", nil
			})
		}
	})
}
