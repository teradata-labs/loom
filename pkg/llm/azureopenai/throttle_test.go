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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// A retried request must carry the FULL body again.
//
// http.Request's body is a reader consumed by the first send, so a client
// that builds one request and re-sends it through the rate limiter's retry
// silently ships an EMPTY body on every attempt after the first — the
// provider then rejects or mis-answers the retry, and a recoverable throttle
// looks like a model failure. sendOnce builds a fresh request per attempt;
// this pins that, which a stub server that ignores the body cannot catch
// (TestCallAPI429IsRetriedThroughRateLimiter passes either way).
func TestThrottleRetryResendsFullBody(t *testing.T) {
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()

		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		Endpoint:     srv.URL,
		DeploymentID: "gpt-4o-body-resend",
		APIKey:       "test-key-not-real",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      time.Millisecond,
			MaxRetries:        3,
		},
	})
	require.NoError(t, err)

	resp, err := client.Chat(context.Background(),
		[]llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err, "one 429 then success must be absorbed by retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "one throttled attempt, one retry")
	assert.NotEmpty(t, bodies[0], "first attempt must carry a body")
	assert.Equal(t, bodies[0], bodies[1],
		"the retry must resend the full body, not an empty one")
}
