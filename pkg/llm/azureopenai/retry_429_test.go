package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// HTTP 429 must surface as an error INSIDE the rate limiter so its retry
// machinery fires — httpClient.Do returns nil error for any
// status, so before this the retry machinery never saw throttling and 429s
// went straight to the caller (issue #348 field evidence: 503/512 agents
// dead on first-attempt 429s with zero retry log lines).
func TestCallAPI429IsRetriedThroughRateLimiter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"exceeded rate limit","type":"too_many_requests"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		Endpoint:     srv.URL,
		DeploymentID: "gpt-4o-test-retry",
		APIKey:       "test-key-not-real",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      5 * time.Millisecond,
			MaxRetries:        4,
		},
	})
	require.NoError(t, err)

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err, "two 429s then success must be absorbed by retry")
	assert.Equal(t, int32(3), calls.Load(), "server must have been called three times (2x429 + success)")
	assert.Contains(t, resp.Content, "ok")
}

// The server's Retry-After must floor the retry wait: with a tiny configured
// retry_backoff_ms (5ms here) all retries used to burn inside one throttle
// window, turning a recoverable throttle into a hard failure. The second
// attempt must arrive no sooner than the header's delta-seconds.
func TestRetryAfterHeaderHonored(t *testing.T) {
	var mu sync.Mutex
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		n := len(arrivals)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1") // delta-seconds
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"exceeded rate limit","type":"too_many_requests"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{
		Endpoint:     srv.URL,
		DeploymentID: "gpt-4o-test-retry-after",
		APIKey:       "test-key-not-real",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      5 * time.Millisecond, // tiny: the wait must come from Retry-After
			MaxRetries:        2,
		},
	})
	require.NoError(t, err)

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err, "429 with Retry-After then success must be absorbed by retry")
	assert.Contains(t, resp.Content, "ok")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, arrivals, 2)
	gap := arrivals[1].Sub(arrivals[0])
	assert.GreaterOrEqual(t, gap, 900*time.Millisecond,
		"retry arrived %v after the 429: Retry-After: 1 was not honored over the 5ms backoff", gap)
}
