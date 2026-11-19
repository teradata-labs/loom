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

package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

// A stream with content but neither finish_reason nor [DONE] is an error
// (llm.ErrStreamTruncated, HTTP 502); one with finish_reason but no [DONE]
// (some OpenAI-compatible servers omit it) is complete.
func TestChatStream_TruncationGuard(t *testing.T) {
	tests := []struct {
		name, body string
		truncated  bool
	}{
		{"content only", `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n", true},
		{"finish_reason without DONE", `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n", false},
		{"DONE", `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\ndata: [DONE]\n\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c := NewClient(Config{APIKey: "test-key-not-real", Endpoint: srv.URL, Model: "gpt-4o"})
			_, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
			if !tt.truncated {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, llm.ErrStreamTruncated)
			assert.Equal(t, llm.ProviderFailureUnavailable, llm.ClassifyProviderFailure(err))
		})
	}
}
