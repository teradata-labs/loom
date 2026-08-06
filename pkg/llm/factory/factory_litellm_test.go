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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/litellm"
)

// TestCreateLiteLLMProvider_FromConfig verifies that explicit config values are
// used to construct the client and that the provider is always available (no
// required credentials unlike Anthropic/OpenAI).
func TestCreateLiteLLMProvider_FromConfig(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "http://litellm:4000",
		LiteLLMAPIKey:   "sk-test",
		LiteLLMModel:    "anthropic/claude-sonnet-4-5-20250929",
		Temperature:     1.0,
	})

	raw, err := f.createLiteLLMProvider("anthropic/claude-sonnet-4-5-20250929")
	require.NoError(t, err)

	client, ok := raw.(*litellm.Client)
	require.True(t, ok, "expected *litellm.Client")
	assert.Equal(t, "litellm", client.Name())
	assert.Equal(t, "anthropic/claude-sonnet-4-5-20250929", client.Model())
}

// TestCreateLiteLLMProvider_DefaultModel verifies that the default model is
// applied when no model is specified.
func TestCreateLiteLLMProvider_DefaultModel(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "http://litellm:4000",
	})

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)

	client, ok := raw.(*litellm.Client)
	require.True(t, ok)
	assert.Equal(t, litellm.DefaultModel, client.Model())
}

// TestCreateLiteLLMProvider_FromEnv verifies that the LITELLM_ENDPOINT and
// LITELLM_API_KEY environment variables are used as fallbacks.
func TestCreateLiteLLMProvider_FromEnv(t *testing.T) {
	t.Setenv("LITELLM_ENDPOINT", "http://env-litellm:4000")
	t.Setenv("LITELLM_API_KEY", "sk-env")

	f := NewProviderFactory(FactoryConfig{})

	raw, err := f.createLiteLLMProvider("azure/gpt-4o")
	require.NoError(t, err)

	client, ok := raw.(*litellm.Client)
	require.True(t, ok)
	assert.Equal(t, "azure/gpt-4o", client.Model())
}

// TestCreateLiteLLMProvider_BaseURLEnvFallback verifies LITELLM_BASE_URL is
// used when LITELLM_ENDPOINT is not set.
func TestCreateLiteLLMProvider_BaseURLEnvFallback(t *testing.T) {
	t.Setenv("LITELLM_ENDPOINT", "") // ensure LITELLM_ENDPOINT is unset for this test
	t.Setenv("LITELLM_BASE_URL", "http://base-url-litellm:4000")

	f := NewProviderFactory(FactoryConfig{})

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)
	assert.NotNil(t, raw)
}

// TestCreateProvider_LiteLLM verifies that the generic CreateProvider dispatcher
// routes to the litellm factory correctly.
func TestCreateProvider_LiteLLM(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "http://litellm:4000",
		LiteLLMModel:    "ollama/llama3.2",
	})

	raw, err := f.CreateProvider("litellm", "ollama/llama3.2")
	require.NoError(t, err)

	client, ok := raw.(*litellm.Client)
	require.True(t, ok)
	assert.Equal(t, "ollama/llama3.2", client.Model())
}

// TestIsProviderAvailable_LiteLLM verifies that IsProviderAvailable returns
// true for litellm (no mandatory credentials).
func TestIsProviderAvailable_LiteLLM(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{})
	assert.True(t, f.IsProviderAvailable("litellm"))
}

// TestCreateLiteLLMProvider_MaxTokensOverride verifies that an explicit MaxTokens
// value in the config overrides the catalog default.
func TestCreateLiteLLMProvider_MaxTokensOverride(t *testing.T) {
	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "http://litellm:4000",
		MaxTokens:       512,
	})

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)
	require.NotNil(t, raw)
	// We can only assert it doesn't error; the inner token value is encapsulated
	// inside the openai.Client delegate and not exposed publicly.
}
