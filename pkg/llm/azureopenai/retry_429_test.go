package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// HTTP 429 must surface as an error INSIDE the rate limiter so
// executeWithRetry retries it — httpClient.Do returns nil error for any
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
