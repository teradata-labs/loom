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

package bedrockmantle

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAnthropicModel(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"anthropic.claude-haiku-4-5", true},
		{"anthropic.claude-opus-4-7", true},
		{"us.anthropic.claude-haiku-4-5", false}, // Mantle rejects inference-profile prefixes with 404
		{"global.anthropic.claude-opus-4-7-v1:0", false},
		{"openai.gpt-oss-120b", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAnthropicModel(tt.id))
		})
	}
}

// TestRoutePrefix pins the route table to what the live sweep observed on
// 2026-11-04 (docs/research/bedrock-mantle-spike.md).
func TestRoutePrefix(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"google.gemma-4-e2b", RouteOpenAIV1},
		{"google.gemma-4-31b", RouteOpenAIV1},
		{"google.gemma-3-12b-it", RouteV1},
		{"xai.grok-4.3", RouteOpenAIV1},
		{"xai.grok-4.6", RouteOpenAIV1},
		{"openai.gpt-5.4", RouteOpenAIV1},
		{"openai.gpt-5.6-sol", RouteOpenAIV1},
		{"openai.gpt-6-astra", RouteOpenAIV1},
		{"openai.gpt-6.1-sol", RouteOpenAIV1},
		{"openai.gpt-oss-120b", RouteV1},
		{"openai.gpt-oss-safeguard-20b", RouteV1},
		{"mistral.devstral-2-123b", RouteV1},
		{"qwen.qwen3-32b", RouteV1},
		{"deepseek.v3.2", RouteV1},
		{"minimax.minimax-m2.5", RouteV1},
		{"moonshotai.kimi-k2.5", RouteV1},
		{"nvidia.nemotron-super-3-120b", RouteV1},
		{"zai.glm-5", RouteV1},
		{"writer.palmyra-vision-7b", RouteV1},
		{"vendor.unknown-model", RouteV1}, // unknown ids start on /v1 and may fall over
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.want, RoutePrefix(tt.id))
		})
	}
}

func TestOtherRoute(t *testing.T) {
	assert.Equal(t, RouteOpenAIV1, otherRoute(RouteV1))
	assert.Equal(t, RouteV1, otherRoute(RouteOpenAIV1))
	assert.Equal(t, RouteOpenAIV1, otherRoute(""), "anything that is not /openai/v1 flips to it")
}

func TestIsRouteError(t *testing.T) {
	assert.True(t, IsRouteError(errors.New("API error (status 400): {\"error\":{\"message\":\"model `google.gemma-4-e2b` isn't supported on this route\"}}")))
	assert.False(t, IsRouteError(errors.New("API error (status 400): {\"error\":{\"message\":\"The model 'anthropic.claude-haiku-4-5' does not support the '/v1/chat/completions' API\"}}")))
	assert.False(t, IsRouteError(errors.New("API error (status 401): Your subscription to the model could not be established")))
	assert.False(t, IsRouteError(nil))
}

func TestChatCompletionsURL(t *testing.T) {
	assert.Equal(t, "https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions",
		chatCompletionsURL(EndpointForRegion("us-west-2"), RouteV1))
	assert.Equal(t, "https://bedrock-mantle.us-east-1.api.aws/openai/v1/chat/completions",
		chatCompletionsURL(EndpointForRegion("us-east-1"), RouteOpenAIV1))
}
