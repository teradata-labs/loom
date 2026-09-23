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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsTransientStatus(t *testing.T) {
	retried := []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529}
	for _, s := range retried {
		assert.True(t, IsTransientStatus(s), "status %d must be transient", s)
	}
	// Everything else is the caller's problem or permanent — never retried as transient.
	notRetried := []int{200, 201, 400, 401, 403, 404, 408, 409, 413, 422, 429, 501, 505, 507, 511}
	for _, s := range notRetried {
		assert.False(t, IsTransientStatus(s), "status %d must not be transient", s)
	}
}

func TestTransientError(t *testing.T) {
	inner := fmt.Errorf("API error (status 503): upstream unavailable")
	te := NewTransientError(inner, http.StatusServiceUnavailable, 2*time.Second)

	assert.Equal(t, inner.Error(), te.Error())
	assert.ErrorIs(t, te, inner, "Unwrap must expose the cause")
	assert.True(t, IsTransient(te))
	assert.True(t, IsTransient(fmt.Errorf("HTTP request failed: %w", te)), "wrapped anywhere in the chain")
	assert.False(t, IsThrottle(te), "a transient failure is not throttling — it must not feed the AIMD throttle signal")
	assert.Equal(t, 2*time.Second, RetryAfter(te), "Retry-After rides on the transient error into retryDelay")
	assert.Equal(t, time.Duration(0), RetryAfter(NewTransientError(inner, 500, 0)))

	assert.False(t, IsTransient(errors.New("API error (status 500): plain text")),
		"an untyped 5xx message is NOT transient: only a provider that classified the status opts in")
	assert.Equal(t, "transient server error (HTTP 502)", (&TransientError{StatusCode: 502}).Error())
}
