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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

func newStreamTestClient(t *testing.T, url, apiVersion string) *Client {
	t.Helper()
	client, err := NewClient(Config{Endpoint: url, DeploymentID: "gpt-4o", APIKey: "test-key", APIVersion: apiVersion})
	require.NoError(t, err)
	return client
}

// ChatStream types a refused request as the direct path does: a 4xx is a
// deterministic client error (the agent does not retry it), and a
// context-too-long 400 carries llm.ErrContextTooLong (the relief trigger).
func TestClient_ChatStream_TypesHTTPErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		status        int
		body          string
		deterministic bool
		contextTooLng bool
	}{
		{name: "401", status: http.StatusUnauthorized, body: `{"error":{"code":"401","message":"Access denied"}}`, deterministic: true},
		{name: "context too long", status: http.StatusBadRequest,
			body: `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens."}}`, contextTooLng: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			_, err := newStreamTestClient(t, server.URL, "2024-10-21").ChatStream(context.Background(),
				[]types.Message{{Role: "user", Content: "go"}}, nil, nil)
			require.Error(t, err)
			assert.Equal(t, int32(1), calls.Load(), "the client itself must not retry a refused request")
			assert.Equal(t, tc.contextTooLng, errors.Is(err, llm.ErrContextTooLong), "ErrContextTooLong in %v", err)
			if tc.deterministic {
				assert.True(t, llm.IsDeterministicClientError(err), "a 401 must be a deterministic client error: %v", err)
			}
		})
	}
}

// ChatStream asks for the final usage chunk on api-versions that accept
// stream_options, so a streamed turn reports its input tokens. An older
// api-version does not get the field.
func TestClient_ChatStream_RequestsUsageChunk(t *testing.T) {
	t.Parallel()
	cases := []struct {
		apiVersion string
		wantOption bool
	}{
		{apiVersion: "2024-10-21", wantOption: true},
		{apiVersion: "2024-09-01-preview", wantOption: true},
		{apiVersion: "2025-04-01-preview", wantOption: true},
		{apiVersion: "v1", wantOption: true},
		{apiVersion: "2024-06-01", wantOption: false},
		{apiVersion: "2024-02-15-preview", wantOption: false},
	}
	for _, tc := range cases {
		t.Run(tc.apiVersion, func(t *testing.T) {
			t.Parallel()
			var sawOption atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					StreamOptions *struct {
						IncludeUsage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				body, _ := io.ReadAll(r.Body)
				assert.NoError(t, json.Unmarshal(body, &req))
				sawOption.Store(req.StreamOptions != nil && req.StreamOptions.IncludeUsage)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
				if sawOption.Load() {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5000,\"completion_tokens\":2,\"total_tokens\":5002}}\n\n")
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			resp, err := newStreamTestClient(t, server.URL, tc.apiVersion).ChatStream(context.Background(),
				[]types.Message{{Role: "user", Content: "go"}}, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOption, sawOption.Load(), "stream_options.include_usage sent")
			if tc.wantOption {
				assert.Equal(t, 5000, resp.Usage.InputTokens, "input tokens from the usage chunk")
			}
		})
	}
}
