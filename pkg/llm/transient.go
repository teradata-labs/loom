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
	"fmt"
	"net/http"
	"time"
)

// MaxErrorBodyBytes bounds how much of a non-2xx response body a provider
// client reads into an error message. Gateway 502/503 pages can be large and
// the message is logged on every retry.
const MaxErrorBodyBytes = 16 << 10

// TransientError is a server-side failure the provider itself reports as
// temporary — HTTP 500, 502, 503, 504 or 529 (overloaded) — on a response
// whose status was known before any content streamed, so the request can be
// re-sent without duplicating effects. Provider clients construct it inside
// their rate-limited send path, exactly where they construct ThrottleError
// for a 429, and the rate limiter retries it under the same rules: the same
// MaxRetries budget, the same exponential backoff with jitter, the same
// Retry-After floor, the same ctx/stop escapes. Nothing string-matches a 5xx
// into a retry — only a provider that classified the status opts in.
type TransientError struct {
	Err        error
	StatusCode int
	RetryAfter time.Duration // 0 when the server did not specify a wait
}

// Error returns the underlying error message.
func (e *TransientError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("transient server error (HTTP %d)", e.StatusCode)
}

// Unwrap exposes the underlying error for errors.Is/As chains.
func (e *TransientError) Unwrap() error { return e.Err }

// NewTransientError wraps err as a TransientError for statusCode carrying
// retryAfter.
func NewTransientError(err error, statusCode int, retryAfter time.Duration) *TransientError {
	return &TransientError{Err: err, StatusCode: statusCode, RetryAfter: retryAfter}
}

// IsTransientStatus reports whether an HTTP status is one the rate limiter
// may retry as a transient server failure. Deliberately narrow: 500 and the
// gateway trio, plus 529 which Anthropic uses for "overloaded". Every other
// non-2xx status — 400, 401, 403, 404, 408, 413, 422, and any 5xx not
// listed — is the caller's problem or a permanent condition and is not
// retried.
func IsTransientStatus(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// IsTransient reports whether err carries a *TransientError anywhere in its
// chain.
func IsTransient(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}

// RetriesExhaustedError is what the rate limiter returns when a retryable
// error — throttling or a transient server failure — survived every attempt
// in its MaxRetries budget. Callers that run their own retry loop around the
// provider (the agent's non-streaming path does) must treat it as final: the
// budget for this class of error lives in the limiter, and re-running it
// outside would multiply the wait without changing the outcome.
type RetriesExhaustedError struct {
	Attempts int    // total attempts made, MaxRetries+1
	Cause    string // "throttling" or "a transient server error"
	Err      error  // the last provider error
}

// Error keeps the historical wording so log greps and tests keep matching.
func (e *RetriesExhaustedError) Error() string {
	return fmt.Sprintf("LLM request failed after %d retries due to %s: %v", e.Attempts, e.Cause, e.Err)
}

// Unwrap exposes the last provider error, so IsThrottle / IsTransient and
// errors.Is on the original cause keep working through the wrap.
func (e *RetriesExhaustedError) Unwrap() error { return e.Err }

// IsRetriesExhausted reports whether err carries a *RetriesExhaustedError.
func IsRetriesExhausted(err error) bool {
	var re *RetriesExhaustedError
	return errors.As(err, &re)
}
