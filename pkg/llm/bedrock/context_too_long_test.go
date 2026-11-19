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
package bedrock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

// The Converse client (every non-Anthropic model on Bedrock) and the
// InvokeModel path report Bedrock's refusal of a prompt over the model's
// window as llm.ErrContextTooLong, which is what the agent's recovery waits
// for. Another validation error stays a plain error.
func TestClient_ReportsTheContextTooLongRefusal(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	tests := []struct {
		name        string
		useConverse bool
		message     string
		wantTooLong bool
	}{
		{name: "converse: input too long", useConverse: true, message: "Input is too long for requested model.", wantTooLong: true},
		{name: "converse: the model's own wording passed through", useConverse: true, message: "The model returned the following errors: prompt is too long: 250000 tokens > 200000 maximum", wantTooLong: true},
		{name: "converse: another validation error", useConverse: true, message: "The provided model identifier is invalid.", wantTooLong: false},
		{name: "invoke: input too long", useConverse: false, message: "Input is too long for requested model.", wantTooLong: true},
		{name: "invoke: another validation error", useConverse: false, message: "Malformed input request: #: extraneous key [foo] is not permitted", wantTooLong: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Amzn-ErrorType", "ValidationException")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"` + tt.message + `"}`))
			}))
			t.Cleanup(server.Close)
			client := &Client{
				client: bedrockruntime.New(bedrockruntime.Options{
					Region:       "us-east-1",
					BaseEndpoint: aws.String(server.URL),
					Credentials:  credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
					HTTPClient:   server.Client(),
					Retryer:      retry.AddWithMaxAttempts(retry.NewStandard(), 1),
				}),
				modelID:     "deepseek.v3-v1:0",
				maxTokens:   DefaultBedrockMaxTokens,
				useConverse: tt.useConverse,
			}

			_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
			require.Error(t, err, "a 400 from Bedrock must be an error")
			assert.Contains(t, err.Error(), tt.message, "the provider's own message must stay in the error")
			assert.Equal(t, tt.wantTooLong, errors.Is(err, llm.ErrContextTooLong),
				"only the context-too-long refusal is reported as llm.ErrContextTooLong")
		})
	}
}
