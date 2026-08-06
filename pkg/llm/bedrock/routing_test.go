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

import "testing"

// TestIsAnthropicModel guards the single routing decision shared by the factory
// and the server provider pool: Anthropic Claude IDs (incl. us./eu./global.
// cross-region inference profiles) use the streaming+caching SDK client; every
// other Bedrock model uses the AWS Converse client.
func TestIsAnthropicModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"global.anthropic.claude-opus-4-6-v1", true},
		{"us.anthropic.claude-opus-4-7", true},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"anthropic.claude-3-5-sonnet-20241022-v2:0", true},
		{"claude-opus-4-6", true},
		{"Claude-Opus-4-6-Bedrock", true},
		{"us.deepseek.r1-v1:0", false},
		{"qwen.qwen3-235b-a22b-v1:0", false},
		{"us.meta.llama3-3-70b-instruct-v1:0", false},
		{"amazon.titan-text-premier-v1:0", false},
		{"mistral.mistral-large-2407-v1:0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAnthropicModel(tt.model); got != tt.want {
			t.Errorf("IsAnthropicModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

// TestNewClientForModel_RoutesToCorrectClient verifies the constructor picks
// the SDK client for Anthropic models and the Converse client otherwise. (Uses
// a region but no credentials; construction must not require live AWS access.)
func TestNewClientForModel_RoutesToCorrectClient(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	anth, err := NewClientForModel(Config{ModelID: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if _, ok := anth.(*SDKClient); !ok {
		t.Errorf("anthropic model routed to %T, want *SDKClient", anth)
	}
	open, err := NewClientForModel(Config{ModelID: "us.deepseek.r1-v1:0", Region: "us-east-1"})
	if err != nil {
		t.Fatalf("deepseek: %v", err)
	}
	if _, ok := open.(*Client); !ok {
		t.Errorf("non-anthropic model routed to %T, want *Client", open)
	}
	def, err := NewClientForModel(Config{Region: "us-east-1"}) // empty -> Anthropic default
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, ok := def.(*SDKClient); !ok {
		t.Errorf("empty model routed to %T, want *SDKClient (default is Anthropic)", def)
	}
}
