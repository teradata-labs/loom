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

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// ollamaChatCapture records the /api/chat request body the constructed client
// sends. Access is mutex-guarded because the httptest handler runs on its own
// goroutine.
type ollamaChatCapture struct {
	mu   sync.Mutex
	body []byte
}

func (c *ollamaChatCapture) record(body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = body
}

func (c *ollamaChatCapture) options(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.body, "the client never called /api/chat")

	var decoded struct {
		Options map[string]json.RawMessage `json:"options"`
	}
	require.NoError(t, json.Unmarshal(c.body, &decoded))
	return decoded.Options
}

// newOllamaCaptureServer serves the two endpoints the Ollama client touches:
// /api/show (native-tool probing) and /api/chat, whose request body it records.
func newOllamaCaptureServer(t *testing.T, capture *ollamaChatCapture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]string{"template": "{{ .Prompt }}"})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		capture.record(body)

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"model":             "llama3.2",
			"created_at":        "2026-01-01T00:00:00Z",
			"message":           map[string]string{"role": "assistant", "content": "ok"},
			"done":              true,
			"prompt_eval_count": 1,
			"eval_count":        1,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCreateLLMProviderOllamaCarriesSeed pins the registry's ollama branch: a
// seed set on the agent's LLMConfig must reach the wire, and an unset one must
// not appear at all. Ollama reads seed 0 as a real seed, so a registry that
// materialized an absent seed as 0 would silently pin every generation.
func TestCreateLLMProviderOllamaCarriesSeed(t *testing.T) {
	tests := []struct {
		name string
		seed *int64
		// wantSeedJSON is the exact raw JSON expected for options.seed; empty
		// means the key must be absent entirely.
		wantSeedJSON string
	}{
		{name: "no seed omits the key", seed: nil, wantSeedJSON: ""},
		{name: "seed is forwarded", seed: int64Ptr(4242), wantSeedJSON: "4242"},
		{name: "explicit zero seed is forwarded as 0", seed: int64Ptr(0), wantSeedJSON: "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &ollamaChatCapture{}
			srv := newOllamaCaptureServer(t, capture)
			t.Setenv("OLLAMA_ENDPOINT", srv.URL)

			reg, err := NewRegistry(RegistryConfig{
				ConfigDir: t.TempDir(),
				Logger:    zaptest.NewLogger(t),
			})
			require.NoError(t, err)

			provider, err := reg.createLLMProvider(&loomv1.LLMConfig{
				Provider:    "ollama",
				Model:       "llama3.2",
				MaxTokens:   256,
				Temperature: 0.1,
				Seed:        tt.seed,
			})
			require.NoError(t, err)
			require.NotNil(t, provider)
			assert.Equal(t, "llama3.2", provider.Model())

			_, err = provider.Chat(context.Background(), []llmtypes.Message{
				{Role: "user", Content: "hello"},
			}, nil)
			require.NoError(t, err)

			options := capture.options(t)
			raw, present := options["seed"]
			if tt.wantSeedJSON == "" {
				assert.False(t, present, "an unset seed must not be sent at all")
				return
			}
			require.True(t, present, "a configured seed must be sent")
			assert.JSONEq(t, tt.wantSeedJSON, string(raw))
		})
	}
}
