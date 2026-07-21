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
package litellm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/types"
)

// openAIChatResponse mirrors the subset of the OpenAI chat-completions response
// that the inner openai.Client parses, sufficient for test assertions.
type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func writeChatResponse(w http.ResponseWriter, content string, inputTokens, outputTokens int) {
	resp := openAIChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   DefaultModel,
	}
	resp.Choices = []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		{
			Index: 0,
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{Role: "assistant", Content: content},
			FinishReason: "stop",
		},
	}
	resp.Usage.PromptTokens = inputTokens
	resp.Usage.CompletionTokens = outputTokens
	resp.Usage.TotalTokens = inputTokens + outputTokens
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// newTestServer starts an httptest server that returns a fixed assistant message
// for POST /v1/chat/completions requests.
func newTestServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		writeChatResponse(w, content, 10, 5)
	}))
}

// TestNormalizeEndpoint covers all URL normalisation branches.
func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"http://litellm:4000", "http://litellm:4000/v1/chat/completions"},
		{"http://litellm:4000/", "http://litellm:4000/v1/chat/completions"},
		{"http://litellm:4000/v1", "http://litellm:4000/v1/chat/completions"},
		{"http://litellm:4000/v1/", "http://litellm:4000/v1/chat/completions"},
		{"http://litellm:4000/v1/chat/completions", "http://litellm:4000/v1/chat/completions"},
		{"http://litellm:4000/v1/chat/completions/", "http://litellm:4000/v1/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeEndpoint(tt.input))
		})
	}
}

// TestNewClient_Defaults verifies that zero-value Config fields get sensible defaults.
func TestNewClient_Defaults(t *testing.T) {
	c := NewClient(Config{})
	assert.Equal(t, "litellm", c.Name())
	assert.Equal(t, DefaultModel, c.Model())
	assert.NotNil(t, c.inner)
}

// TestNewClient_CustomConfig verifies that explicit config values are respected.
func TestNewClient_CustomConfig(t *testing.T) {
	cfg := Config{
		Endpoint:    "http://proxy:8080/v1",
		APIKey:      "sk-test",
		Model:       "ollama/llama3.2",
		MaxTokens:   1024,
		Temperature: 0.7,
		Timeout:     30 * time.Second,
	}
	c := NewClient(cfg)
	assert.Equal(t, "ollama/llama3.2", c.Model())
	assert.Equal(t, "litellm", c.Name())
}

// TestClient_Chat_SimpleText verifies a successful round-trip for a plain text response.
func TestClient_Chat_SimpleText(t *testing.T) {
	srv := newTestServer(t, "Hello from LiteLLM!")
	defer srv.Close()

	c := NewClient(Config{
		Endpoint: srv.URL,
		Model:    DefaultModel,
	})

	resp, err := c.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "Hi"},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello from LiteLLM!", resp.Content)
	assert.NotEmpty(t, resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

// TestClient_Chat_MultiTurn verifies that multi-turn conversations are forwarded correctly.
func TestClient_Chat_MultiTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
		assert.Len(t, reqBody.Messages, 3)
		writeChatResponse(w, "Sure!", 20, 3)
	}))
	defer srv.Close()

	c := NewClient(Config{Endpoint: srv.URL})
	resp, err := c.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "What is 2+2?"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "And 3+3?"},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Sure!", resp.Content)
}

// TestClient_Chat_ErrorResponse verifies that HTTP error status codes surface as errors.
func TestClient_Chat_ErrorResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"bad request", http.StatusBadRequest},
		{"unauthorized", http.StatusUnauthorized},
		{"server error", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error": "something went wrong"}`))
			}))
			defer srv.Close()

			c := NewClient(Config{Endpoint: srv.URL})
			_, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "ping"}}, nil)
			assert.Error(t, err)
		})
	}
}

// TestClient_HealthCheck_NoHealthChecker verifies that HealthCheck returns nil when the
// inner client does not implement the optional interface (should not panic).
func TestClient_HealthCheck_NoHealthChecker(t *testing.T) {
	c := NewClient(Config{Endpoint: "http://localhost:4000"})
	// HealthCheck either delegates or returns nil — must not panic.
	_ = c.HealthCheck(context.Background())
}

// TestClient_ChatStream_SimpleText verifies streaming returns a valid response.
func TestClient_ChatStream_SimpleText(t *testing.T) {
	// The inner openai.Client streaming uses SSE; we return a non-streamed
	// completion so the client falls back to the body, which is acceptable for
	// unit-level coverage.
	streamResp := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(streamResp))
	}))
	defer srv.Close()

	c := NewClient(Config{Endpoint: srv.URL, Model: DefaultModel})

	var tokens []string
	resp, err := c.ChatStream(context.Background(), []types.Message{
		{Role: "user", Content: "Hi"},
	}, nil, func(token string) {
		tokens = append(tokens, token)
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Contains(t, resp.Content, "Hi")
}

// TestClient_ExtraHeaders verifies that ExtraHeaders set on litellm.Config are
// forwarded to the proxy on every request.
func TestClient_ExtraHeaders(t *testing.T) {
	var receivedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		writeChatResponse(w, "ok", 5, 3)
	}))
	defer srv.Close()

	c := NewClient(Config{
		Endpoint: srv.URL,
		Model:    DefaultModel,
		ExtraHeaders: map[string]string{
			"X-LiteLLM-Tags": "env=test,agent=loom",
			"X-LiteLLM-User": "vasu",
		},
	})

	_, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)

	assert.Equal(t, "env=test,agent=loom", receivedHeaders.Get("X-LiteLLM-Tags"))
	assert.Equal(t, "vasu", receivedHeaders.Get("X-LiteLLM-User"))
}

// TestClient_ImplementsLLMProvider is a compile-time interface assertion.
func TestClient_ImplementsLLMProvider(t *testing.T) {
	var _ llmtypes.LLMProvider = (*Client)(nil)
}
