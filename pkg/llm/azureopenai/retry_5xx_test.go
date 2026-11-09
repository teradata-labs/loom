package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

const okCompletion = `{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func retryingClient(t *testing.T, url string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		Endpoint:     url,
		DeploymentID: "gpt-4o-test-5xx",
		APIKey:       "test-key-not-real",
		RateLimiterConfig: llm.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: 1000,
			BurstCapacity:     8,
			MinDelay:          time.Millisecond,
			RetryBackoff:      5 * time.Millisecond,
			MaxRetries:        3,
		},
	})
	require.NoError(t, err)
	return client
}

// An Azure OpenAI 500 ("The server had an error while processing your
// request") ended an agent's conversation on the az512 rig because nothing
// retried it: the limiter only saw 429s. A 5xx whose status is known before
// any content streams is now retried under the same budget as a 429.
func TestCallAPI5xxIsRetriedThroughRateLimiter(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504, 529} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) <= 2 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"The server had an error while processing your request. Sorry about that!","type":"server_error"}}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(okCompletion))
			}))
			defer srv.Close()

			resp, err := retryingClient(t, srv.URL).Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
			require.NoError(t, err, "two %d then success must be absorbed by retry", status)
			assert.Equal(t, int32(3), calls.Load())
			assert.Contains(t, resp.Content, "ok")
		})
	}
}

// The transient budget is finite and shared with throttling: MaxRetries+1
// attempts, then the typed error surfaces with the provider's message intact.
func TestCallAPI5xxExhaustsBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable","type":"server_error"}}`))
	}))
	defer srv.Close()

	_, err := retryingClient(t, srv.URL).Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)
	assert.Equal(t, int32(4), calls.Load(), "MaxRetries=3 means 4 attempts")
	assert.True(t, llm.IsTransient(err))
	assert.Contains(t, err.Error(), "transient server error")
	assert.Contains(t, err.Error(), "status 503")
}

// A 4xx is the caller's problem and is never retried — one call, error out.
func TestCallAPI4xxIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"nope","type":"invalid_request_error"}}`))
			}))
			defer srv.Close()

			_, err := retryingClient(t, srv.URL).Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
			require.Error(t, err)
			assert.Equal(t, int32(1), calls.Load(), "a %d must not be retried", status)
			assert.False(t, llm.IsTransient(err))
		})
	}
}

// Without a rate limiter there is no retry machinery, so a 5xx surfaces on the
// first attempt exactly as a 429 does — the retry lives in one place.
func TestCallAPI5xxWithoutLimiterIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{Endpoint: srv.URL, DeploymentID: "gpt-4o-test-nolimiter", APIKey: "test-key-not-real"})
	require.NoError(t, err)
	_, err = client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
}

// The streaming path classifies the status before any SSE frame is consumed,
// so a 5xx there is retried too.
func TestChatStream5xxIsRetriedThroughRateLimiter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"server_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client := retryingClient(t, srv.URL)
	var streamed string
	resp, err := client.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil,
		func(token string) { streamed += token })
	require.NoError(t, err, "one 502 then a stream must be absorbed by retry")
	assert.Equal(t, int32(2), calls.Load())
	assert.Contains(t, resp.Content+streamed, "ok")
}
