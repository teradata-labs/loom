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
package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// rawChatRequest decodes only the options object of an /api/chat request, and
// keeps each option value as raw JSON. This lets tests assert on what actually
// went over the wire — including whether a key is present at all — rather than
// on Go zero values produced by decoding into a typed struct.
type rawChatRequest struct {
	Options map[string]json.RawMessage `json:"options"`
}

// seedCapture records the raw request body of the /api/chat call made by the
// client under test. Access is mutex-guarded because the httptest handler runs
// on its own goroutine.
type seedCapture struct {
	mu     sync.Mutex
	body   []byte
	called bool
}

func (c *seedCapture) record(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = body
	c.called = true
}

func (c *seedCapture) result() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body, c.called
}

// newSeedCaptureServer returns a server that records the /api/chat request body
// and replies with a minimal single-chunk streaming response.
func newSeedCaptureServer(t *testing.T, capture *seedCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mockShowResponse(w, r) {
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		capture.record(body)

		resp := chatResponse{
			Model:     "mistral",
			CreatedAt: "2024-01-01T00:00:00Z",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "ok",
			},
			Done:            true,
			PromptEvalCount: 1,
			EvalCount:       1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func int64Ptr(v int64) *int64 { return &v }

// TestClient_Chat_SeedOption asserts on the raw marshaled request body: an
// unset seed must not appear in the options object at all, while a configured
// seed — including an explicit 0, which Ollama treats as a real seed rather
// than "unset" — must be sent verbatim.
func TestClient_Chat_SeedOption(t *testing.T) {
	tests := []struct {
		name string
		seed *int64
		// wantSeedJSON is the exact raw JSON expected for options.seed.
		// Empty means the "seed" key must be absent entirely.
		wantSeedJSON string
	}{
		{
			name:         "unset seed omits the key",
			seed:         nil,
			wantSeedJSON: "",
		},
		{
			name:         "nonzero seed is sent",
			seed:         int64Ptr(42),
			wantSeedJSON: "42",
		},
		{
			name:         "explicit zero seed is sent as 0",
			seed:         int64Ptr(0),
			wantSeedJSON: "0",
		},
		{
			name:         "negative seed is sent verbatim",
			seed:         int64Ptr(-7),
			wantSeedJSON: "-7",
		},
		{
			name:         "large seed survives without float rounding",
			seed:         int64Ptr(9007199254740993), // 2^53 + 1: unrepresentable as float64
			wantSeedJSON: "9007199254740993",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &seedCapture{}
			server := newSeedCaptureServer(t, capture)
			defer server.Close()

			client := NewClient(Config{
				Endpoint:    server.URL,
				Model:       "mistral",
				Temperature: 0.5,
				MaxTokens:   128,
				Seed:        tt.seed,
			})

			_, err := client.Chat(context.Background(), []llmtypes.Message{
				{Role: "user", Content: "Test"},
			}, nil)
			require.NoError(t, err)

			body, called := capture.result()
			require.True(t, called, "server never received an /api/chat request")

			var req rawChatRequest
			require.NoError(t, json.Unmarshal(body, &req))

			// Baseline options must always be present, so a missing "seed" key
			// cannot be confused with a missing options object.
			require.NotNil(t, req.Options)
			assert.Contains(t, req.Options, "temperature")
			assert.Contains(t, req.Options, "num_predict")

			rawSeed, present := req.Options["seed"]
			if tt.wantSeedJSON == "" {
				assert.False(t, present, "options.seed must be absent when no seed is configured")
				// Stronger raw-bytes check: the token must not appear anywhere.
				assert.NotContains(t, string(body), `"seed"`,
					"raw request body must not mention seed when unset")
				return
			}

			require.True(t, present, "options.seed must be present when a seed is configured")
			assert.Equal(t, tt.wantSeedJSON, string(rawSeed))
		})
	}
}

// TestNewClient_SeedIsCopied verifies the client does not retain the caller's
// pointer, so mutating the original Config value cannot change what is sent.
func TestNewClient_SeedIsCopied(t *testing.T) {
	capture := &seedCapture{}
	server := newSeedCaptureServer(t, capture)
	defer server.Close()

	seed := int64(11)
	client := NewClient(Config{
		Endpoint: server.URL,
		Model:    "mistral",
		Seed:     &seed,
	})

	// Mutate the caller's variable after construction.
	seed = 99

	_, err := client.Chat(context.Background(), []llmtypes.Message{
		{Role: "user", Content: "Test"},
	}, nil)
	require.NoError(t, err)

	body, called := capture.result()
	require.True(t, called)

	var req rawChatRequest
	require.NoError(t, json.Unmarshal(body, &req))
	assert.Equal(t, "11", string(req.Options["seed"]))
}

// TestNewClient_SeedDefaultsToUnset guards the zero-behavior-change contract:
// a Config built without a Seed must leave the client's seed nil.
func TestNewClient_SeedDefaultsToUnset(t *testing.T) {
	client := NewClient(Config{Model: "mistral"})
	assert.Nil(t, client.seed)
}

// TestClient_ChatStream_SeedOption covers the streaming entry point directly,
// since ChatStream is where the options payload is assembled.
func TestClient_ChatStream_SeedOption(t *testing.T) {
	capture := &seedCapture{}
	server := newSeedCaptureServer(t, capture)
	defer server.Close()

	client := NewClient(Config{
		Endpoint: server.URL,
		Model:    "mistral",
		Seed:     int64Ptr(1234),
	})

	var got strings.Builder
	_, err := client.ChatStream(context.Background(), []llmtypes.Message{
		{Role: "user", Content: "Test"},
	}, nil, func(token string) {
		got.WriteString(token)
	})
	require.NoError(t, err)

	body, called := capture.result()
	require.True(t, called)

	var req rawChatRequest
	require.NoError(t, json.Unmarshal(body, &req))
	assert.Equal(t, "1234", string(req.Options["seed"]))
}
