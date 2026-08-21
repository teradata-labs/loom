package llm

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSharedRateLimiterSameScopeAndConfigShareOneInstance(t *testing.T) {
	cfg := RateLimiterConfig{Enabled: true, RequestsPerSecond: 7.5, TokensPerMinute: 123456}
	a := SharedRateLimiter("test-share|ep1|m1", cfg)
	b := SharedRateLimiter("test-share|ep1|m1", cfg)
	assert.Same(t, a, b, "identical scope+config must share one limiter")
}

func TestSharedRateLimiterExplicitDefaultSharesDefaultLimiter(t *testing.T) {
	// rps=0 backfills to the default (2.0); an explicit 2.0 must land on the
	// same limiter, not split the quota into two buckets.
	defaults := DefaultRateLimiterConfig()
	zero := SharedRateLimiter("test-norm|ep|m", RateLimiterConfig{Enabled: true})
	explicit := SharedRateLimiter("test-norm|ep|m", RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: defaults.RequestsPerSecond,
		TokensPerMinute:   defaults.TokensPerMinute,
	})
	assert.Same(t, zero, explicit, "explicit default values must share the default limiter")
}

func TestSharedRateLimiterDifferentConfigGetsOwnInstance(t *testing.T) {
	// The core #346 fix: a later client's differing rate_limit must be
	// honored, not silently replaced by the first client's config.
	first := SharedRateLimiter("test-split|ep|m", RateLimiterConfig{Enabled: true, RequestsPerSecond: 1})
	second := SharedRateLimiter("test-split|ep|m", RateLimiterConfig{Enabled: true, RequestsPerSecond: 100})
	assert.NotSame(t, first, second, "a different effective config must not be silently ignored")
	assert.Equal(t, 100.0, second.config.RequestsPerSecond, "the second config's numbers must be in effect")
}

func TestSharedRateLimiterDifferentScopeGetsOwnInstance(t *testing.T) {
	cfg := RateLimiterConfig{Enabled: true, RequestsPerSecond: 3}
	a := SharedRateLimiter("test-scope|azure|dep-a", cfg)
	b := SharedRateLimiter("test-scope|azure|dep-b", cfg)
	assert.NotSame(t, a, b, "independent quota boundaries must not throttle each other")
}

func TestSharedRateLimiterConcurrentCreation(t *testing.T) {
	// Many goroutines racing on the same key must converge on one instance.
	cfg := RateLimiterConfig{Enabled: true, RequestsPerSecond: 9}
	const n = 32
	results := make([]*RateLimiter, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = SharedRateLimiter("test-race|ep|m", cfg)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		assert.Same(t, results[0], results[i], "goroutine %d got a different instance", i)
	}
}

func TestNormalizeRateLimiterConfigBackfillsZeroFields(t *testing.T) {
	defaults := DefaultRateLimiterConfig()
	got := normalizeRateLimiterConfig(RateLimiterConfig{Enabled: true})
	assert.Equal(t, defaults.RequestsPerSecond, got.RequestsPerSecond)
	assert.Equal(t, defaults.TokensPerMinute, got.TokensPerMinute)
	assert.Equal(t, defaults.BurstCapacity, got.BurstCapacity)
	assert.Equal(t, defaults.MinDelay, got.MinDelay)
	assert.Equal(t, defaults.MaxRetries, got.MaxRetries)
	assert.Equal(t, defaults.RetryBackoff, got.RetryBackoff)
	assert.Equal(t, defaults.QueueTimeout, got.QueueTimeout)
	assert.NotNil(t, got.Logger)

	// Non-zero fields survive untouched.
	custom := normalizeRateLimiterConfig(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 100,
		TokensPerMinute:   1_200_000,
		QueueTimeout:      time.Minute,
	})
	assert.Equal(t, 100.0, custom.RequestsPerSecond)
	assert.Equal(t, int64(1_200_000), custom.TokensPerMinute)
	assert.Equal(t, time.Minute, custom.QueueTimeout)
}
