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

package azureopenai

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

// Azure OpenAI refuses a prompt over the model's window with status 400 and
// error.code "context_length_exceeded". The client reports it as the
// context-too-long refusal the agent recovers from; another 400 stays a
// plain error.
func TestClient_ReportsTheContextTooLongRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		tooLong    bool
	}{
		{"over the window", `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 140210 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`, true},
		{"another bad request", `{"error":{"message":"Unrecognized request argument supplied: foo","type":"invalid_request_error","param":null,"code":null}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := NewClient(Config{Endpoint: server.URL, DeploymentID: "test-deployment", APIKey: "test-key"})
			require.NoError(t, err)

			msgs := []types.Message{{Role: "user", Content: "hi"}}
			_, err = client.Chat(context.Background(), msgs, nil)
			require.Error(t, err)
			require.Equal(t, tc.tooLong, errors.Is(err, llm.ErrContextTooLong), err.Error())

			_, err = client.ChatStream(context.Background(), msgs, nil, func(string) {})
			require.Error(t, err)
			require.Equal(t, tc.tooLong, errors.Is(err, llm.ErrContextTooLong), "stream: "+err.Error())
		})
	}
}
