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

	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

func TestAgentBuilder_WithVercelGatewayLLM(t *testing.T) {
	t.Setenv(vercelgateway.EnvLoomAPIKey, "")
	t.Setenv(vercelgateway.EnvAPIKey, "vck_test_not_a_real_key")
	t.Setenv("VERCEL_AI_GATEWAY_ENDPOINT", "https://us.ai-gateway.vercel.sh")

	b := NewAgentBuilder().WithVercelGatewayLLM("openai/gpt-5")
	require.NoError(t, b.err)
	c, ok := b.llm.(*vercelgateway.Client)
	require.True(t, ok, "got %T", b.llm)
	assert.Equal(t, "openai/gpt-5", c.Model())
	assert.Equal(t, "https://us.ai-gateway.vercel.sh", c.BaseURL())
}

func TestAgentBuilder_WithVercelGatewayLLM_ErrorsSurface(t *testing.T) {
	t.Setenv(vercelgateway.EnvLoomAPIKey, "")
	t.Setenv(vercelgateway.EnvAPIKey, "vck_test_not_a_real_key")
	b := NewAgentBuilder().WithVercelGatewayLLM("")
	require.Error(t, b.err)
	assert.Contains(t, b.err.Error(), "model is required")

	t.Setenv(vercelgateway.EnvAPIKey, "")
	b = NewAgentBuilder().WithVercelGatewayLLM("openai/gpt-5")
	require.ErrorContains(t, b.err, "no credential")
}
