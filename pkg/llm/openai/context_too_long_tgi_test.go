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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

// TGI has two refusals for a prompt that is too long (router/src/validation.rs).
// MaxTotalTokens is checked first: prompt plus max_new_tokens over
// --max-total-tokens. InputLength is the other: the prompt alone over
// --max-input-tokens, which is the one a server gives when its input limit is
// below its total limit less the request's max_tokens. Both are the
// context-too-long refusal. MaxNewTokens is about the reply size alone and is
// not: a shorter prompt cannot answer it.
func TestClient_ReportsBothTGIPromptRefusals(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		tooLong bool
	}{
		{
			name:    "InputLength: the prompt alone is over max-input-tokens",
			body:    "{\"error\":\"Input validation error: `inputs` must have less than 3000 tokens. Given: 3200\",\"error_type\":\"validation\"}",
			tooLong: true,
		},
		{
			name:    "MaxTotalTokens: prompt plus reply is over max-total-tokens",
			body:    "{\"error\":\"Input validation error: `inputs` tokens + `max_new_tokens` must be <= 4096. Given: 4000 `inputs` tokens and 400 `max_new_tokens`\",\"error_type\":\"validation\"}",
			tooLong: true,
		},
		{
			name:    "MaxNewTokens: the reply size alone is over the server's limit",
			body:    "{\"error\":\"Input validation error: `max_new_tokens` must be <= 512. Given: 4096\",\"error_type\":\"validation\"}",
			tooLong: false,
		},
		{
			name:    "EmptyInput: not a length refusal",
			body:    "{\"error\":\"Input validation error: `inputs` cannot be empty\",\"error_type\":\"validation\"}",
			tooLong: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			client := NewClient(Config{APIKey: "test-key", Model: "served-model", Endpoint: server.URL})
			msgs := []types.Message{{Role: "user", Content: "hi"}}

			_, err := client.Chat(context.Background(), msgs, nil)
			require.Error(t, err, "a 422 must be an error on the chat path")
			assert.Equal(t, tt.tooLong, errors.Is(err, llm.ErrContextTooLong), "chat: %v", err)

			_, err = client.ChatStream(context.Background(), msgs, nil, func(string) {})
			require.Error(t, err, "a 422 must be an error on the streaming path")
			assert.Equal(t, tt.tooLong, errors.Is(err, llm.ErrContextTooLong), "stream: %v", err)
		})
	}
}
