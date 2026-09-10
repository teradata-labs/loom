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
package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/types"
)

// The server-specified wait on a 429 must floor the retry backoff: with a
// tiny configured retry_backoff_ms all retries used to burn inside one
// throttle window. Uses retry-after-ms to keep the test wait short while
// still exercising the header-to-limiter path end to end.
func TestRetryAfterHeaderHonored(t *testing.T) {
	var mu sync.Mutex
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		n := len(arrivals)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("retry-after-ms", "400")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	client := NewClient(Config{
		APIKey:   "test-key-not-real-retry-after",
		Endpoint: srv.URL,
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      5 * time.Millisecond, // tiny: the wait must come from retry-after-ms
			MaxRetries:        2,
		},
	})

	resp, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err, "429 with retry-after-ms then success must be absorbed by retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, arrivals, 2)
	gap := arrivals[1].Sub(arrivals[0])
	assert.GreaterOrEqual(t, gap, 350*time.Millisecond,
		"retry arrived %v after the 429: retry-after-ms 400 was not honored over the 5ms backoff", gap)
}
