package llm

import (
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// sharedLimiters holds one RateLimiter per (scope, effective config) pair.
// Provider clients that share an upstream quota share a limiter; clients with
// a different scope or a different effective config get their own. This
// replaces the per-package first-wins singletons, where the first client's
// config silently applied to every later client for the process lifetime.
var sharedLimiters = struct {
	mu sync.Mutex
	m  map[string]*RateLimiter
	// scopes tracks how many distinct limiters exist per scope so a second
	// config on the same quota boundary can be surfaced to the operator.
	scopes map[string]int
}{
	m:      map[string]*RateLimiter{},
	scopes: map[string]int{},
}

// SharedRateLimiter returns the process-wide rate limiter for the given scope
// and config, creating it on first use.
//
// scope names the upstream quota boundary the limiter protects — e.g.
// "azure-openai|<endpoint>|<deployment>" or "bedrock|<region>|<model>" — so
// clients that draw on the same quota share one token bucket, and clients on
// independent quotas do not throttle each other.
//
// The config is normalized (zero fields backfilled with defaults) before it
// keys the map, so an explicit value equal to the default shares the
// default's limiter. Distinct effective configs on the same scope each get
// their own limiter and a warning is logged: their combined rate can exceed
// the quota the scope represents, which is the operator's explicit choice
// but worth seeing.
func SharedRateLimiter(scope string, config RateLimiterConfig) *RateLimiter {
	eff := normalizeRateLimiterConfig(config)
	key := fmt.Sprintf("%s|rps=%g|tpm=%d|burst=%d|min=%s|retries=%d|backoff=%s|queue=%s",
		scope, eff.RequestsPerSecond, eff.TokensPerMinute, eff.BurstCapacity,
		eff.MinDelay, eff.MaxRetries, eff.RetryBackoff, eff.QueueTimeout)

	sharedLimiters.mu.Lock()
	defer sharedLimiters.mu.Unlock()

	if rl, ok := sharedLimiters.m[key]; ok {
		return rl
	}

	rl := NewRateLimiter(eff)
	sharedLimiters.m[key] = rl
	sharedLimiters.scopes[scope]++

	eff.Logger.Info("LLM rate limiter created",
		zap.String("scope", scope),
		zap.Float64("requests_per_second", eff.RequestsPerSecond),
		zap.Int64("tokens_per_minute", eff.TokensPerMinute),
		zap.Int("burst_capacity", eff.BurstCapacity),
		zap.Duration("queue_timeout", eff.QueueTimeout))
	if n := sharedLimiters.scopes[scope]; n > 1 {
		eff.Logger.Warn("multiple rate limiter configs share one upstream quota; their combined rate can exceed it",
			zap.String("scope", scope),
			zap.Int("limiters_for_scope", n))
	}
	return rl
}
