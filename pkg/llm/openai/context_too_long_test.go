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

package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

// The OpenAI-shaped client serves Mistral, HuggingFace, Bedrock Mantle and
// LiteLLM, and any vLLM or TGI server behind them. Those servers refuse a
// prompt over the window in their own words and set no error code. The client
// reports each as the context-too-long refusal the agent recovers from;
// another refusal stays a plain error.
func TestClient_ReportsTheContextTooLongRefusalOfEachServer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		tooLong bool
	}{
		{"vLLM", 400, `{"error":{"message":"This model's maximum context length is 32768 tokens. However, you requested 37000 tokens (36000 in the messages, 1000 in the completion). Please reduce the length of the messages or completion.","type":"BadRequestError","param":null,"code":400}}`, true},
		{"Mistral", 400, `{"object":"error","message":"Prompt contains 65673 tokens, too large for model with 32768 maximum context length","type":"invalid_request_error","param":null,"code":"3051"}`, true},
		{"TGI", 422, "{\"error\":\"Input validation error: `inputs` tokens + `max_new_tokens` must be <= 4096. Given: 4000 `inputs` tokens and 400 `max_new_tokens`\",\"error_type\":\"validation\"}", true},
		{"another validation error", 422, `{"error":"Input validation error: temperature must be strictly positive","error_type":"validation"}`, false},
		{"another bad request", 400, `{"error":{"message":"Unrecognized request argument supplied: foo","type":"invalid_request_error","code":null}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient(Config{APIKey: "test-key", Model: "served-model", Endpoint: server.URL})

			msgs := []types.Message{{Role: "user", Content: "hi"}}
			_, err := client.Chat(context.Background(), msgs, nil)
			require.Error(t, err)
			require.Equal(t, tc.tooLong, errors.Is(err, llm.ErrContextTooLong), err.Error())

			_, err = client.ChatStream(context.Background(), msgs, nil, func(string) {})
			require.Error(t, err)
			require.Equal(t, tc.tooLong, errors.Is(err, llm.ErrContextTooLong), "stream: "+err.Error())
		})
	}
}
