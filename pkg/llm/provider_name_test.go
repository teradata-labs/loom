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

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The provider-neutral form keeps the exact behavior the gateway package had
// (pkg/llm/vercelgateway TestNormalizeProviderName pins the wrapper).
func TestNormalizeProviderName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"canonical", "vercel-ai-gateway", ProviderVercelAIGateway},
		{"alias", "vercel", ProviderVercelAIGateway},
		{"alias with case and space", " Vercel ", ProviderVercelAIGateway},
		{"canonical upper case", "VERCEL-AI-GATEWAY", ProviderVercelAIGateway},
		{"other provider unchanged", "anthropic", "anthropic"},
		{"other provider keeps its case", " Anthropic ", " Anthropic "},
		{"near miss unchanged", "vercel-gateway", "vercel-gateway"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeProviderName(tt.in), "normalized name of %q", tt.in)
			assert.Equal(t, tt.want == ProviderVercelAIGateway, IsVercelAIGateway(tt.in), "IsVercelAIGateway(%q)", tt.in)
		})
	}
}
