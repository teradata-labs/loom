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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestRateLimiter_RetriesReenterAdmission is the regression for the retry
// admission bypass: retries used to re-invoke the call without re-acquiring
// an admission token, so under throttling the aggregate outbound rate was
// admitted RPS plus all concurrent retry waves. With rps=10 and burst=1,
// EVERY outbound attempt — first tries and retries alike — must be spaced by
// the bucket, so no two attempts may start closer than ~100ms apart.
func TestRateLimiter_RetriesReenterAdmission(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 10,
		BurstCapacity:     1,
		MinDelay:          time.Millisecond,
		MaxRetries:        2,
		RetryBackoff:      time.Millisecond, // tiny: pacing must come from admission, not backoff
		Logger:            zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	const numRequests = 4
	var mu sync.Mutex
	var attemptTimes []time.Time

	var wg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			calls := 0
			_, err := rl.Do(context.Background(), func(context.Context) (interface{}, error) {
				mu.Lock()
				attemptTimes = append(attemptTimes, time.Now())
				mu.Unlock()
				calls++
				if calls == 1 {
					return nil, errors.New("HTTP 429: throttle storm")
				}
				return "ok", nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, attemptTimes, numRequests*2, "each request must attempt twice (429 then success)")

	sort.Slice(attemptTimes, func(i, j int) bool { return attemptTimes[i].Before(attemptTimes[j]) })
	for i := 1; i < len(attemptTimes); i++ {
		gap := attemptTimes[i].Sub(attemptTimes[i-1])
		// Nominal spacing at 10 rps / burst 1 is 100ms; allow half for
		// goroutine scheduling skew. Any retry that bypassed admission would
		// start ~1ms (the backoff) after its failed attempt and fail this.
		assert.GreaterOrEqual(t, gap, 50*time.Millisecond,
			"attempts %d and %d started %v apart: a retry bypassed admission", i-1, i, gap)
	}
}

// TestRateLimiter_RetryDelayJitter verifies the backoff jitter: for each
// attempt the delay must stay within [0.5, 1.5]x of that attempt's
// exponential base, and repeated computations must not all collapse to one
// value (the old no-jitter backoff synchronized every admitted-burst request
// into retry waves at the same ~1s/2s/4s marks).
func TestRateLimiter_RetryDelayJitter(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:      true,
		RetryBackoff: 100 * time.Millisecond,
		Logger:       zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	throttle := errors.New("HTTP 429: slow down")

	tests := []struct {
		name    string
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{"attempt 0: base 100ms", 0, 50 * time.Millisecond, 150 * time.Millisecond},
		{"attempt 1: base 200ms", 1, 100 * time.Millisecond, 300 * time.Millisecond},
		{"attempt 2: base 400ms", 2, 200 * time.Millisecond, 600 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const samples = 32
			seen := make(map[time.Duration]struct{}, samples)
			for i := 0; i < samples; i++ {
				d := rl.retryDelay(tt.attempt, throttle)
				assert.GreaterOrEqual(t, d, tt.min)
				assert.LessOrEqual(t, d, tt.max)
				seen[d] = struct{}{}
			}
			assert.Greater(t, len(seen), 1, "32 jittered delays must not all be identical")
		})
	}
}

// TestRateLimiter_RetryDelayCapped verifies pathological attempt counts
// cannot overflow or exceed the cap.
func TestRateLimiter_RetryDelayCapped(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:      true,
		RetryBackoff: time.Second,
		Logger:       zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	d := rl.retryDelay(63, errors.New("HTTP 429"))
	assert.Greater(t, d, time.Duration(0))
	assert.LessOrEqual(t, d, maxRetryDelay+maxRetryDelay/2)
}

// TestRateLimiter_RetryDelayHonorsRetryAfter verifies the server-specified
// wait floors the computed backoff: with a tiny configured retry_backoff_ms,
// all retries used to burn inside one throttle window and a recoverable
// throttle became a hard failure.
func TestRateLimiter_RetryDelayHonorsRetryAfter(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:      true,
		RetryBackoff: time.Millisecond, // tiny on purpose
		Logger:       zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	err := NewThrottleError(errors.New("API error (status 429): busy"), 300*time.Millisecond)
	for i := 0; i < 8; i++ {
		d := rl.retryDelay(0, err)
		assert.GreaterOrEqual(t, d, 300*time.Millisecond,
			"retry delay must never undercut the server's Retry-After")
	}

	// Backoff larger than Retry-After wins (max of the two).
	big := NewThrottleError(errors.New("API error (status 429): busy"), time.Microsecond)
	d := rl.retryDelay(0, big)
	assert.GreaterOrEqual(t, d, 500*time.Microsecond)
}

// TestRateLimiter_Do_WaitsRetryAfter is the end-to-end check: one throttled
// attempt carrying Retry-After=300ms with retry_backoff_ms=1 must delay the
// second attempt by at least the server's wait.
func TestRateLimiter_Do_WaitsRetryAfter(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		BurstCapacity:     8,
		MinDelay:          time.Millisecond,
		MaxRetries:        2,
		RetryBackoff:      time.Millisecond, // tiny: the wait must come from Retry-After
		Logger:            zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	var times []time.Time
	result, err := rl.Do(context.Background(), func(context.Context) (interface{}, error) {
		times = append(times, time.Now())
		if len(times) == 1 {
			return nil, NewThrottleError(errors.New("API error (status 429): busy"), 300*time.Millisecond)
		}
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	require.Len(t, times, 2)
	assert.GreaterOrEqual(t, times[1].Sub(times[0]), 300*time.Millisecond,
		"second attempt must wait at least the server's Retry-After")
}

// TestRateLimiter_AbandonedRequestSkipsAdmission is the regression for the
// fast-path context check: a request whose context expired while it sat in
// the queue must not consume a bucket token or stall the dispatcher for
// MinDelay — that stall lands on every live request queued behind it.
func TestRateLimiter_AbandonedRequestSkipsAdmission(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000, // token refill is instant; MinDelay is the only stall
		BurstCapacity:     2,
		MinDelay:          200 * time.Millisecond,
		Logger:            zap.NewNop(),
	})
	defer func() { _ = rl.Close() }()

	// req1 occupies the dispatcher (its MinDelay sleep) while req2 and req3
	// are queued behind it.
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = rl.Do(context.Background(), func(context.Context) (interface{}, error) {
			<-release
			return "ok", nil
		})
	}()
	time.Sleep(20 * time.Millisecond)

	// req2: queued live, then abandoned while waiting. Its Do returns the
	// context error; the queued entry stays behind for the dispatcher.
	ctx2, cancel2 := context.WithCancel(context.Background())
	executed := make(chan struct{}, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := rl.Do(ctx2, func(context.Context) (interface{}, error) {
			executed <- struct{}{}
			return nil, nil
		})
		assert.ErrorIs(t, err, context.Canceled)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel2()

	// req3: live, queued after the abandoned req2.
	start := time.Now()
	result, err := rl.Do(context.Background(), func(context.Context) (interface{}, error) {
		return "live", nil
	})
	elapsed := time.Since(start)
	close(release)
	wg.Wait()

	require.NoError(t, err)
	assert.Equal(t, "live", result)
	select {
	case <-executed:
		t.Fatal("abandoned request must never execute")
	default:
	}
	// Expected stall: the tail of req1's MinDelay (~160ms) plus req3's own
	// MinDelay (200ms) ≈ 360ms. The bug adds a full MinDelay burned on the
	// abandoned req2 (~560ms total). Assert well under the buggy floor.
	assert.Less(t, elapsed, 450*time.Millisecond,
		"live request stalled %v: dispatcher spent admission on an abandoned request", elapsed)
}
