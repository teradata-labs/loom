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
package azureopenai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// TestConvertResponse_EmptyChoices guards against an index-out-of-range panic
// when a 200 response carries no choices (e.g. from a gateway or proxy).
func TestConvertResponse_EmptyChoices(t *testing.T) {
	c := &Client{deploymentID: "gpt-4o-deployment", modelName: "gpt-4o"}
	resp := &openai.ChatCompletionResponse{
		Model:   "gpt-4o",
		Choices: []openai.ChatCompletionChoice{},
	}

	var llmResp *llmtypes.LLMResponse
	require.NotPanics(t, func() {
		llmResp = c.convertResponse(resp, nil)
	})

	require.NotNil(t, llmResp)
	assert.Empty(t, llmResp.StopReason)
	assert.Empty(t, llmResp.Content)
	assert.Empty(t, llmResp.ToolCalls)
	assert.NotContains(t, llmResp.Metadata, "finish_reason")
	assert.Equal(t, "gpt-4o", llmResp.Metadata["model"])
}
