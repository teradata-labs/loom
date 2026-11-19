// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openairesponses

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func retryLimiterConfig() llm.RateLimiterConfig {
	return llm.RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		BurstCapacity:     8,
		MinDelay:          time.Millisecond,
		RetryBackoff:      5 * time.Millisecond,
		MaxRetries:        4,
	}
}

const textStreamOK = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n"

// The first attempt fails with failStatus; the limiter retries and the
// second attempt must carry the identical full body.
func TestRetryThroughLimiter(t *testing.T) {
	t.Parallel()
	for _, failStatus := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway} {
		for _, stream := range []bool{false, true} {
			name := http.StatusText(failStatus)
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request, n int) {
					if n == 1 {
						w.WriteHeader(failStatus)
						_, _ = io.WriteString(w, `{"error":{"message":"try later","type":"rate_limit"}}`)
						return
					}
					if stream {
						sseHandler(textStreamOK)(w, r, n)
						return
					}
					jsonHandler(http.StatusOK, minimalCompleted)(w, r, n)
				})
				// A unique key per subtest keeps the shared limiters apart.
				c := NewClient(Config{APIKey: testAPIKey + name, Endpoint: rec.srv.URL, RateLimiterConfig: retryLimiterConfig()})
				msgs := []llmtypes.Message{{Role: "user", Content: "hello there"}}
				var resp *llmtypes.LLMResponse
				var err error
				if stream {
					resp, err = c.ChatStream(context.Background(), msgs, nil, nil)
				} else {
					resp, err = c.Chat(context.Background(), msgs, nil)
				}
				require.NoError(t, err, "one failure then success is absorbed by the limiter")
				assert.Equal(t, "ok", resp.Content, "content from the retry")
				require.Equal(t, 2, rec.count(), "failed attempt plus retry")
				rec.mu.Lock()
				defer rec.mu.Unlock()
				assert.NotEmpty(t, rec.bodies[1], "retry carries a body")
				assert.Equal(t, rec.bodies[0], rec.bodies[1], "retry re-sends the identical body")
			})
		}
	}
}

func TestNoLimiter429Surfaces(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, jsonHandler(http.StatusTooManyRequests, `{"error":{"message":"rate limit","type":"rate_limit_error"}}`))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL})
	_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
	require.Error(t, err, "429 without a limiter is returned")
	var se *llm.HTTPStatusError
	require.True(t, errors.As(err, &se), "status attached")
	assert.Equal(t, http.StatusTooManyRequests, se.StatusCode, "status")
	assert.Equal(t, 1, rec.count(), "no retry without a limiter")
}

func TestLimiterExhaustedReturnsThrottle(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, jsonHandler(http.StatusTooManyRequests, `{"error":{"message":"rate limit"}}`))
	cfg := retryLimiterConfig()
	cfg.MaxRetries = 1
	c := NewClient(Config{APIKey: testAPIKey + "exhaust", Endpoint: rec.srv.URL, RateLimiterConfig: cfg})
	_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
	require.Error(t, err, "persistent 429 fails")
	var te *llm.ThrottleError
	assert.True(t, errors.As(err, &te), "throttle error reaches the caller: %v", err)
}

// failingTransport fails the first n round trips with err, then delegates.
type failingTransport struct {
	failures atomic.Int32
	err      error
	next     http.RoundTripper
}

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, f.err
	}
	return f.next.RoundTrip(r)
}

func TestTransportErrorRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		failures  int32
		limiter   bool
		wantOK    bool
		wantCalls int
	}{
		{"EOF retried once", io.EOF, 1, false, true, 1},
		{"reset retried once", syscall.ECONNRESET, 1, false, true, 1},
		{"EOF twice fails", io.EOF, 2, false, false, 0},
		{"other error not retried", errors.New("dial refused"), 1, false, false, 0},
		{"EOF retried once with limiter", net.ErrClosed, 1, true, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
			ft := &failingTransport{err: tt.err}
			ft.failures.Store(tt.failures)
			cfg := Config{APIKey: testAPIKey + tt.name, Endpoint: rec.srv.URL,
				WrapTransport: func(rt http.RoundTripper) http.RoundTripper { ft.next = rt; return ft }}
			if tt.limiter {
				cfg.RateLimiterConfig = retryLimiterConfig()
				cfg.RateLimiterConfig.MaxRetries = 0
			}
			_, err := NewClient(cfg).Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
			if tt.wantOK {
				require.NoError(t, err, "recovered")
			} else {
				require.Error(t, err, "not recovered")
			}
			assert.Equal(t, tt.wantCalls, rec.count(), "requests that reached the server")
		})
	}
}

func TestIsRetryableTransportError(t *testing.T) {
	t.Parallel()
	assert.False(t, isRetryableTransportError(nil), "nil")
	assert.True(t, isRetryableTransportError(io.ErrUnexpectedEOF), "unexpected EOF")
	assert.False(t, isRetryableTransportError(context.Canceled), "cancel")
}
