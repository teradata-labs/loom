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

import "strings"

// Mantle serves three API surfaces on one host. Claude models are served
// only on the Anthropic Messages path; every other model is served on the
// OpenAI Chat Completions path under one of two prefixes.
const (
	// MessagesPath is the Anthropic Messages API path on bedrock-mantle.
	MessagesPath = "/anthropic/v1/messages"
	// RouteV1 is the Chat Completions prefix most models are served under.
	RouteV1 = "/v1"
	// RouteOpenAIV1 is the Chat Completions prefix a few vendors are served under.
	RouteOpenAIV1 = "/openai/v1"
	// ModelsPath lists the models the endpoint knows about (not an entitlement
	// check: unsubscribed models are listed too and fail at inference time).
	ModelsPath = "/v1/models"

	chatCompletionsSuffix = "/chat/completions"
	anthropicPrefix       = "anthropic."
	routeErrorFragment    = "isn't supported on this route"
)

// openAIV1Prefixes lists the model-id prefixes Mantle serves under /openai/v1.
// Observed live on 2026-11-04 by probing every model in us-west-2 and us-east-1
// (docs/research/bedrock-mantle-spike.md): Gemma 4, Grok, GPT-5.x and GPT-6.x.
// Everything else (gpt-oss, Gemma 3, Mistral, Qwen, DeepSeek, MiniMax, Kimi,
// Nemotron, GLM, Palmyra) is served under /v1. An id this table does not know
// starts on /v1 and moves to /openai/v1 once if the endpoint answers with the
// route error (IsRouteError).
var openAIV1Prefixes = []string{"google.gemma-4", "xai.", "openai.gpt-5", "openai.gpt-6"}

// IsAnthropicModel reports whether a Mantle model id is a Claude model, which
// Mantle serves only on MessagesPath. Mantle ids carry no cross-region prefix
// ("us.", "global."); the endpoint rejects such ids with 404, so they are not
// recognised here either.
func IsAnthropicModel(modelID string) bool {
	return strings.HasPrefix(modelID, anthropicPrefix)
}

// RoutePrefix returns the Chat Completions prefix (RouteV1 or RouteOpenAIV1)
// a non-Claude model id is expected to be served under.
func RoutePrefix(modelID string) string {
	for _, p := range openAIV1Prefixes {
		if strings.HasPrefix(modelID, p) {
			return RouteOpenAIV1
		}
	}
	return RouteV1
}

// otherRoute returns the Chat Completions prefix that is not prefix.
func otherRoute(prefix string) string {
	if prefix == RouteOpenAIV1 {
		return RouteV1
	}
	return RouteOpenAIV1
}

// IsRouteError reports whether err is Mantle's wrong-prefix rejection: HTTP 400
// with the body "model `<id>` isn't supported on this route". The openai client
// surfaces the body verbatim in its error string.
func IsRouteError(err error) bool {
	return err != nil && strings.Contains(err.Error(), routeErrorFragment)
}

// chatCompletionsURL joins the base URL, a route prefix and the Chat
// Completions suffix.
func chatCompletionsURL(baseURL, prefix string) string {
	return baseURL + prefix + chatCompletionsSuffix
}
