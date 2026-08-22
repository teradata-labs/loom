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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsThrottlingErrorExported(t *testing.T) {
	assert.True(t, IsThrottlingError(errors.New("API error (status 429): rate_limit_exceeded")))
	assert.True(t, IsThrottlingError(errors.New("ThrottlingException: slow down")))
	assert.False(t, IsThrottlingError(errors.New("API error (status 400): bad request")))
	assert.False(t, IsThrottlingError(nil))
}

func TestRetryAfterHint(t *testing.T) {
	// Azure's message body wording.
	assert.Equal(t, 60*time.Second, RetryAfterHint(errors.New(
		`API error (status 429): {"error":{"message":"Please retry after 60 seconds."}}`)))
	// The Retry-After header echoed into error text by the client.
	assert.Equal(t, 7*time.Second, RetryAfterHint(errors.New(
		"API error (status 429, retry after 7s): throttled")))
	// Pathological windows are capped.
	assert.Equal(t, 300*time.Second, RetryAfterHint(errors.New(
		"please retry after 86400 seconds")))
	// No stated window.
	assert.Equal(t, time.Duration(0), RetryAfterHint(errors.New("API error (status 429): nope")))
	assert.Equal(t, time.Duration(0), RetryAfterHint(nil))
}

// A 429 inside the rate limiter's execution loop is retried, and the retry
// succeeds without surfacing the throttle to the caller.
func TestRateLimiterRetriesThrottle(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		BurstCapacity:     10,
		MinDelay:          time.Millisecond,
		MaxRetries:        2,
		RetryBackoff:      time.Millisecond,
		QueueTimeout:      5 * time.Second,
	})
	defer func() { _ = rl.Close() }()

	calls := 0
	result, err := rl.Do(context.Background(), func(ctx context.Context) (interface{}, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("API error (status 429): rate_limit_exceeded")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, calls)
}
