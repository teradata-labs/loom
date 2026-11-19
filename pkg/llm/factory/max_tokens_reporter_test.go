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
package factory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/bedrockmantle"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/litellm"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// Every client loom ships reports the max_tokens it was built with
// (types.MaxTokensReporter), and the wrappers forward the figure of the
// client they wrap. The agent fits a step in flight against that figure, so a
// client that did not report it would be fitted against the agent's config,
// which is not the client's on a pool entry or after a model switch.
func TestEveryClientReportsTheMaxTokensItSends(t *testing.T) {
	const sends = 3210
	clearMantleEnv(t)
	tests := []struct {
		name  string
		build func(t *testing.T) types.LLMProvider
	}{
		{name: "anthropic", build: func(*testing.T) types.LLMProvider {
			return anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-sonnet-4-5", MaxTokens: sends})
		}},
		{name: "openai", build: func(*testing.T) types.LLMProvider {
			return openai.NewClient(openai.Config{APIKey: "k", Model: "gpt-4o", MaxTokens: sends})
		}},
		{name: "azure-openai", build: func(t *testing.T) types.LLMProvider {
			c, err := azureopenai.NewClient(azureopenai.Config{
				Endpoint: "https://example.openai.azure.com", DeploymentID: "d", APIKey: "k", MaxTokens: sends})
			require.NoError(t, err, "the Azure OpenAI client must build")
			return c
		}},
		{name: "gemini", build: func(*testing.T) types.LLMProvider {
			return gemini.NewClient(gemini.Config{APIKey: "k", Model: "gemini-2.5-flash", MaxTokens: sends})
		}},
		{name: "ollama", build: func(*testing.T) types.LLMProvider {
			return ollama.NewClient(ollama.Config{Model: "llama3.3", MaxTokens: sends})
		}},
		{name: "mistral (wraps the OpenAI-shaped client)", build: func(*testing.T) types.LLMProvider {
			return mistral.NewClient(mistral.Config{APIKey: "k", MaxTokens: sends})
		}},
		{name: "huggingface (wraps the OpenAI-shaped client)", build: func(*testing.T) types.LLMProvider {
			return huggingface.NewClient(huggingface.Config{Token: "k", MaxTokens: sends})
		}},
		{name: "litellm (wraps the OpenAI-shaped client)", build: func(*testing.T) types.LLMProvider {
			return litellm.NewClient(litellm.Config{Endpoint: "http://127.0.0.1:1/v1/chat/completions", APIKey: "k", Model: "m", MaxTokens: sends})
		}},
		{name: "bedrock Anthropic-SDK client", build: func(t *testing.T) types.LLMProvider {
			c, err := bedrock.NewSDKClient(bedrock.Config{
				Region: "us-west-2", AccessKeyID: "AKIATEST", SecretAccessKey: "secret",
				ModelID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", MaxTokens: sends})
			require.NoError(t, err, "the Bedrock SDK client must build")
			return c
		}},
		{name: "bedrock Converse client", build: func(t *testing.T) types.LLMProvider {
			c, err := bedrock.NewClient(bedrock.Config{
				Region: "us-west-2", AccessKeyID: "AKIATEST", SecretAccessKey: "secret",
				ModelID: "deepseek.v3-v1:0", MaxTokens: sends})
			require.NoError(t, err, "the Bedrock Converse client must build")
			return c
		}},
		{name: "bedrock-mantle, Claude route", build: func(t *testing.T) types.LLMProvider {
			c, err := bedrockmantle.NewClientForModel(bedrockmantle.Config{
				Region: "us-west-2", APIKey: "ABSKtest", Model: "anthropic.claude-haiku-4-5", MaxTokens: sends})
			require.NoError(t, err, "the Mantle client must build")
			return c
		}},
		{name: "bedrock-mantle, Chat Completions route", build: func(t *testing.T) types.LLMProvider {
			c, err := bedrockmantle.NewClientForModel(bedrockmantle.Config{
				Region: "us-west-2", APIKey: "ABSKtest", Model: "google.gemma-4-e2b", MaxTokens: sends})
			require.NoError(t, err, "the Mantle client must build")
			return c
		}},
		{name: "instrumented wrapper", build: func(*testing.T) types.LLMProvider {
			return llm.NewInstrumentedProvider(
				anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-sonnet-4-5", MaxTokens: sends}),
				observability.NewNoOpTracer())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.build(t)
			reporter, ok := client.(types.MaxTokensReporter)
			require.True(t, ok, "the client must implement types.MaxTokensReporter")
			assert.Equal(t, sends, reporter.MaxTokens(), "the client must report the max_tokens it was built with")
		})
	}
}

// The reported figure is the one on the wire: read off the request body of
// the Anthropic client and of the OpenAI-shaped client, also when the client
// was built with no max_tokens and sends its own default.
func TestTheReportedMaxTokensIsTheOneOnTheWire(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		build      func(endpoint string, maxTokens int) types.LLMProvider
		reply      string
	}{
		{
			name:       "anthropic, configured",
			configured: 3210,
			build: func(endpoint string, maxTokens int) types.LLMProvider {
				return anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-sonnet-4-5", Endpoint: endpoint, MaxTokens: maxTokens})
			},
			reply: `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		},
		{
			name:       "anthropic, client default",
			configured: 0,
			build: func(endpoint string, maxTokens int) types.LLMProvider {
				return anthropic.NewClient(anthropic.Config{APIKey: "k", Model: "claude-sonnet-4-5", Endpoint: endpoint, MaxTokens: maxTokens})
			},
			reply: `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		},
		{
			name:       "openai-shaped, configured",
			configured: 3210,
			build: func(endpoint string, maxTokens int) types.LLMProvider {
				return openai.NewClient(openai.Config{APIKey: "k", Model: "gpt-4o", Endpoint: endpoint, MaxTokens: maxTokens})
			},
			reply: `{"id":"x","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		},
		{
			name:       "openai-shaped, client default",
			configured: 0,
			build: func(endpoint string, maxTokens int) types.LLMProvider {
				return openai.NewClient(openai.Config{APIKey: "k", Model: "gpt-4o", Endpoint: endpoint, MaxTokens: maxTokens})
			},
			reply: `{"id":"x","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var onWire float64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				n, ok := body["max_tokens"].(float64)
				if !ok {
					n, _ = body["max_completion_tokens"].(float64)
				}
				onWire = n
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.reply))
			}))
			t.Cleanup(srv.Close)

			client := tt.build(srv.URL, tt.configured)
			_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
			require.NoError(t, err, "the request must reach the test server")

			reported := client.(types.MaxTokensReporter).MaxTokens()
			require.Positive(t, onWire, "the request must carry a max_tokens")
			assert.InDelta(t, onWire, float64(reported), 0, "the client must report the max_tokens on the wire")
			if tt.configured > 0 {
				assert.Equal(t, tt.configured, reported, "a configured max_tokens is sent as it is")
			}
		})
	}
}
