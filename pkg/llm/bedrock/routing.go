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
	"strings"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// IsAnthropicModel reports whether a Bedrock model ID refers to an Anthropic
// Claude model, including cross-region inference profiles such as
// "us.anthropic.claude-opus-4-7" or "global.anthropic.claude-opus-4-6-v1".
//
// Application inference profile ARNs
// (arn:aws:bedrock:…:application-inference-profile/<opaque-id>) carry no model
// hint, so a Claude model behind one is not detected here and falls back to the
// Converse client — those ARNs are not addressable via the Anthropic SDK anyway.
func IsAnthropicModel(modelID string) bool {
	m := strings.ToLower(modelID)
	return strings.Contains(m, "anthropic") || strings.Contains(m, "claude")
}

// NewClientForModel builds the appropriate Bedrock client for cfg.ModelID:
// Anthropic Claude models use the Anthropic SDK client (NewSDKClient), which
// streams tokens incrementally and sets cache_control for prompt caching; all
// other models use the AWS Converse client (NewClient), whose ChatStream is a
// blocking stub and which the Anthropic SDK cannot serve.
//
// This is the single source of truth for Bedrock client selection. Every
// construction site — the provider factory and the server's provider pool —
// must route through it so streaming and caching behave identically regardless
// of how a provider was built.
func NewClientForModel(cfg Config) (llmtypes.LLMProvider, error) {
	model := cfg.ModelID
	if model == "" {
		model = DefaultBedrockModelID // Anthropic by default
	}
	if IsAnthropicModel(model) {
		return NewSDKClient(cfg)
	}
	return NewClient(cfg)
}
