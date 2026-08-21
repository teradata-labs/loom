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

// CredentialScope returns a stable, non-derived identity token for an API
// credential, for use inside a rate-limiter scope string. Providers whose
// quotas attach to the credential (Anthropic, OpenAI, Gemini) include it so
// two clients with different keys — different upstream quotas — never share
// one bucket. The token is an interned sequential id ("key1", "key2", …):
// nothing is ever hashed or otherwise derived from the secret, so no
// fingerprint of it appears in scope strings, log fields, or map keys beyond
// the process-private interning table. Limiters are process-scoped, so
// process-lifetime stability is all the identity needs. Empty credentials
// (ambient/env auth) share the "nokey" scope, preserving sharing for the
// common single-key process.
func CredentialScope(credential string) string {
	if credential == "" {
		return "nokey"
	}
	credentialIDs.mu.Lock()
	defer credentialIDs.mu.Unlock()
	if id, ok := credentialIDs.m[credential]; ok {
		return id
	}
	credentialIDs.seq++
	id := fmt.Sprintf("key%d", credentialIDs.seq)
	credentialIDs.m[credential] = id
	return id
}

// credentialIDs interns credentials to opaque sequential ids. Bounded by the
// number of distinct credentials configured in the process.
var credentialIDs = struct {
	mu  sync.Mutex
	m   map[string]string
	seq int
}{m: map[string]string{}}
