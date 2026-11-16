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

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func TestInstrumentedProvider_GatewayAttributesOnSpan(t *testing.T) {
	mock := &mockLLMProvider{
		name:  "vercel-ai-gateway",
		model: "vendor/model",
		response: &llmtypes.LLMResponse{
			Content: "ok",
			Metadata: map[string]interface{}{
				"gateway.final_provider": "bedrock",
				"gateway.generation_id":  "gen_01",
				"gateway.fallback_used":  true,
				MetadataCostUnpriced:     true,
				"gateway.ignored_number": 42, // only strings and bools are copied
				"model":                  "vendor/model",
			},
		},
	}
	tracer := newMockTracer()
	_, err := NewInstrumentedProvider(mock, tracer).Chat(context.Background(),
		[]llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)

	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	require.NotEmpty(t, tracer.spans)
	attrs := tracer.spans[0].Attributes
	assert.Equal(t, "bedrock", attrs["gateway.final_provider"])
	assert.Equal(t, "gen_01", attrs["gateway.generation_id"])
	assert.Equal(t, true, attrs["gateway.fallback_used"])
	assert.Equal(t, true, attrs[MetadataCostUnpriced])
	_, hasNumber := attrs["gateway.ignored_number"]
	assert.False(t, hasNumber)
	_, hasModel := attrs["model"]
	assert.False(t, hasModel, "non-gateway metadata is not copied")
}
