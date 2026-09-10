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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ThrottleError is a throttling (HTTP 429) error that optionally carries the
// server-specified wait before the next attempt (Retry-After and related
// headers). Provider clients construct it in their sendOnce paths so the rate
// limiter's retry can wait max(RetryAfter, computed backoff): with a small
// configured retry_backoff_ms, retrying inside the server's throttle window
// just burns attempts and turns a recoverable throttle into a hard failure.
type ThrottleError struct {
	Err        error
	RetryAfter time.Duration // 0 when the server did not specify a wait
}

// Error returns the underlying error message.
func (e *ThrottleError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "throttled (HTTP 429)"
}

// Unwrap exposes the underlying error for errors.Is/As chains.
func (e *ThrottleError) Unwrap() error { return e.Err }

// NewThrottleError wraps err as a ThrottleError carrying retryAfter.
func NewThrottleError(err error, retryAfter time.Duration) *ThrottleError {
	return &ThrottleError{Err: err, RetryAfter: retryAfter}
}

// IsThrottle reports whether err is a throttling error: a typed
// *ThrottleError anywhere in its chain (every HTTP provider client wraps a
// 429 in one), or a provider message that identifies throttling (AWS SDK
// ThrottlingException et al., which carry no HTTP response to type). The
// scheduler's provider-agnostic AIMD seam classifies call outcomes with it.
func IsThrottle(err error) bool {
	return isThrottlingError(err)
}

// RetryAfter extracts the server-specified wait carried on err (via a
// ThrottleError anywhere in its chain), or 0 when none was specified.
func RetryAfter(err error) time.Duration {
	var te *ThrottleError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		return te.RetryAfter
	}
	return 0
}

// RetryAfterFromHeaders parses the server-specified wait from a throttled
// response's headers. It understands, in priority order:
//   - retry-after-ms (Azure): integer/decimal milliseconds
//   - Retry-After: delta-seconds or an HTTP-date (RFC 9110 §10.2.3)
//   - x-ratelimit-reset-requests / x-ratelimit-reset-tokens (OpenAI/Azure):
//     duration strings ("1s", "6m0s", "120ms") or bare seconds
//
// Returns 0 when no header yields a positive wait.
func RetryAfterFromHeaders(h http.Header) time.Duration {
	if h == nil {
		return 0
	}

	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}

	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}

	for _, name := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}

	return 0
}
