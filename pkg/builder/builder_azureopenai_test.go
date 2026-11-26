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

package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/fabric"
)

// stubBackend satisfies fabric.ExecutionBackend so Build gets past its
// backend check. Build fails before it calls any method, so none is
// implemented.
type stubBackend struct{ fabric.ExecutionBackend }

// An Azure OpenAI construction failure must surface from Build, not be
// swallowed into the generic "LLM provider is required" message.
func TestAgentBuilder_WithAzureOpenAI_ErrorSurfacesInBuild(t *testing.T) {
	tests := []struct {
		name    string
		apply   func(*AgentBuilder) *AgentBuilder
		wantErr string
	}{
		{
			name:    "api key, empty config",
			apply:   func(b *AgentBuilder) *AgentBuilder { return b.WithAzureOpenAILLM("", "", "") },
			wantErr: "endpoint is required",
		},
		{
			name: "api key, no credential",
			apply: func(b *AgentBuilder) *AgentBuilder {
				return b.WithAzureOpenAILLM("https://example.openai.azure.com", "gpt-4o", "")
			},
			wantErr: "either APIKey or EntraToken must be provided",
		},
		{
			name:    "entra, empty config",
			apply:   func(b *AgentBuilder) *AgentBuilder { return b.WithAzureOpenAIEntraAuth("", "", "") },
			wantErr: "endpoint is required",
		},
		{
			name: "entra, no deployment",
			apply: func(b *AgentBuilder) *AgentBuilder {
				return b.WithAzureOpenAIEntraAuth("https://example.openai.azure.com", "", "token")
			},
			wantErr: "deployment ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.apply(NewAgentBuilder().WithBackend(stubBackend{}))
			assert.Nil(t, b.llm)

			_, err := b.Build()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "azure-openai: "+tt.wantErr)
			assert.NotContains(t, err.Error(), "LLM provider is required")
		})
	}
}
