// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package llm

import "time"

// CapacityObserver receives provider capacity telemetry harvested from
// responses: ratelimit headers on successes, Retry-After on throttles. The
// LLM slot scheduler implements it; provider clients call it when
// configured. A nil observer is always legal.
type CapacityObserver interface {
	// UpdateFromHeaders reports the provider's stated token budget:
	// x-ratelimit-limit-tokens, x-ratelimit-remaining-tokens, and the token
	// window reset. limitTokens <= 0 means the header was absent.
	UpdateFromHeaders(limitTokens, remainingTokens int64, reset time.Duration)
	// ObserveThrottle reports a throttle response (HTTP 429 or provider
	// equivalent) and the provider-suggested retry delay (0 if absent).
	ObserveThrottle(retryAfter time.Duration)
	// ObserveSuccess reports a clean response that carried NO usable
	// ratelimit telemetry. Providers with no headers at all (Bedrock,
	// Ollama, some proxies) drive the scheduler's AIMD fallback through
	// this: additive growth on clean traffic, halving on throttles.
	ObserveSuccess()
}
