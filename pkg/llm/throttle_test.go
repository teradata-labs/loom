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

func TestRetryAfterFromHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name:    "nil headers",
			headers: nil,
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "no relevant headers",
			headers: map[string]string{"Content-Type": "application/json"},
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "Retry-After delta-seconds",
			headers: map[string]string{"Retry-After": "7"},
			wantMin: 7 * time.Second, wantMax: 7 * time.Second,
		},
		{
			name:    "Retry-After decimal seconds",
			headers: map[string]string{"Retry-After": "1.5"},
			wantMin: 1500 * time.Millisecond, wantMax: 1500 * time.Millisecond,
		},
		{
			name:    "Retry-After HTTP-date in the future",
			headers: map[string]string{"Retry-After": time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)},
			wantMin: 25 * time.Second, wantMax: 30 * time.Second,
		},
		{
			name:    "Retry-After HTTP-date in the past yields zero",
			headers: map[string]string{"Retry-After": time.Now().Add(-30 * time.Second).UTC().Format(http.TimeFormat)},
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "azure retry-after-ms wins over Retry-After",
			headers: map[string]string{"retry-after-ms": "250", "Retry-After": "9"},
			wantMin: 250 * time.Millisecond, wantMax: 250 * time.Millisecond,
		},
		{
			name:    "openai x-ratelimit-reset-requests duration string",
			headers: map[string]string{"x-ratelimit-reset-requests": "1s"},
			wantMin: time.Second, wantMax: time.Second,
		},
		{
			name:    "openai x-ratelimit-reset-requests compound duration",
			headers: map[string]string{"x-ratelimit-reset-requests": "6m0s"},
			wantMin: 6 * time.Minute, wantMax: 6 * time.Minute,
		},
		{
			name:    "x-ratelimit-reset-tokens bare seconds",
			headers: map[string]string{"x-ratelimit-reset-tokens": "3"},
			wantMin: 3 * time.Second, wantMax: 3 * time.Second,
		},
		{
			name:    "garbage values yield zero",
			headers: map[string]string{"Retry-After": "soon", "retry-after-ms": "many"},
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "negative values yield zero",
			headers: map[string]string{"Retry-After": "-5"},
			wantMin: 0, wantMax: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h http.Header
			if tt.headers != nil {
				h = http.Header{}
				for k, v := range tt.headers {
					h.Set(k, v)
				}
			}
			got := RetryAfterFromHeaders(h)
			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestThrottleError(t *testing.T) {
	inner := errors.New("API error (status 429): busy")
	te := NewThrottleError(inner, 2*time.Second)

	assert.Equal(t, inner.Error(), te.Error())
	assert.ErrorIs(t, te, inner)

	// RetryAfter must survive wrapping.
	wrapped := fmt.Errorf("HTTP request failed: %w", te)
	assert.Equal(t, 2*time.Second, RetryAfter(wrapped))

	// Errors without a ThrottleError in the chain carry no wait.
	assert.Equal(t, time.Duration(0), RetryAfter(nil))
	assert.Equal(t, time.Duration(0), RetryAfter(errors.New("HTTP 429")))
	assert.Equal(t, time.Duration(0), RetryAfter(NewThrottleError(inner, 0)))

	// Nil inner error still renders a message.
	assert.NotEmpty(t, (&ThrottleError{}).Error())
}
