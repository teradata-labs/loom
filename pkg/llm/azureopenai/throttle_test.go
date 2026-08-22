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
package azureopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
)

// A 429 must come back as an error (so retry layers see it), carrying the
// Retry-After header in classifier-parsable form — not as a raw response.
func TestDoWithRateLimit_ThrottleConvertedToError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o",
		APIKey:       "test-key-not-real",
	})
	require.NoError(t, err)

	_, err = client.doWithRateLimit(context.Background(), server.URL, []byte(`{"x":1}`))
	require.Error(t, err)
	assert.True(t, llm.IsThrottlingError(err), "429 must classify as throttling: %v", err)
	assert.Equal(t, 3*time.Second, llm.RetryAfterHint(err), "Retry-After header must survive into the error")
}

// The request is rebuilt per attempt: after a 429 the retried request must
// carry the FULL body again (a reused request's body reader is already
// consumed and silently sends nothing).
func TestDoWithRateLimit_RebuildsBodyPerAttempt(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o",
		APIKey:       "test-key-not-real",
	})
	require.NoError(t, err)
	// Wire a private (non-global) rate limiter so the retry loop runs.
	client.rateLimiter = llm.NewRateLimiter(llm.RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		BurstCapacity:     10,
		MinDelay:          time.Millisecond,
		MaxRetries:        2,
		RetryBackoff:      time.Millisecond,
		QueueTimeout:      5 * time.Second,
	})
	defer func() { _ = client.rateLimiter.Close() }()

	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	resp, err := client.doWithRateLimit(context.Background(), server.URL, []byte(payload))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, bodies, 2, "one throttled attempt, one retry")
	assert.Equal(t, payload, bodies[0])
	assert.Equal(t, payload, bodies[1], "retry must resend the full body, not an empty one")
}
