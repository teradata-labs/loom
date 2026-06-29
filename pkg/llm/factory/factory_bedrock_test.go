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

import "testing"

// TestIsAnthropicBedrockModel guards the routing decision in createBedrockProvider:
// Anthropic Claude IDs (including us./eu./global. cross-region inference profiles)
// must use the streaming+caching SDK client, while every other Bedrock model must
// stay on the AWS Converse client (the Anthropic SDK cannot serve them).
func TestIsAnthropicBedrockModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"global inference profile", "global.anthropic.claude-opus-4-6-v1", true},
		{"us inference profile", "us.anthropic.claude-opus-4-7", true},
		{"us sonnet versioned", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"bare anthropic prefix", "anthropic.claude-3-5-sonnet-20241022-v2:0", true},
		{"bare claude id", "claude-opus-4-6", true},
		{"uppercase display form", "Claude-Opus-4-6-Bedrock", true},
		{"deepseek", "us.deepseek.r1-v1:0", false},
		{"qwen", "qwen.qwen3-235b-a22b-v1:0", false},
		{"meta llama", "us.meta.llama3-3-70b-instruct-v1:0", false},
		{"amazon titan", "amazon.titan-text-premier-v1:0", false},
		{"mistral", "mistral.mistral-large-2407-v1:0", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnthropicBedrockModel(tt.model); got != tt.want {
				t.Errorf("isAnthropicBedrockModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
