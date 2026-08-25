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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/litellm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
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

// TestCreateLiteLLMProvider_ExpandsEnvPlaceholder verifies that ${VAR}
// placeholders in config values are expanded by NewProviderFactory so the
// factory code path produces working providers. This is the pattern used by
// avmo-tera-cloud: looms.yaml contains litellm_endpoint: ${LITELLM_BASE_URL}
// and the real value is injected as a pod env var.
func TestCreateLiteLLMProvider_ExpandsEnvPlaceholder(t *testing.T) {
	t.Setenv("LITELLM_BASE_URL", "http://real-litellm:4000")
	t.Setenv("LITELLM_API_KEY", "")
	t.Setenv("LITELLM_MODEL", "expanded-model")
	t.Setenv("LITELLM_TENANT", "tenant-123")

	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint:     "${LITELLM_BASE_URL}",
		LiteLLMAPIKey:       "${LITELLM_API_KEY}", // unset → expands to ""
		LiteLLMModel:        "${LITELLM_MODEL}",
		LiteLLMExtraHeaders: map[string]string{"X-Tenant": "${LITELLM_TENANT}"},
	})

	// After expansion, the endpoint should be the real URL, not the placeholder.
	assert.Equal(t, "http://real-litellm:4000", f.config.LiteLLMEndpoint)
	assert.Equal(t, "", f.config.LiteLLMAPIKey) // unset env var → empty
	assert.Equal(t, "expanded-model", f.config.LiteLLMModel)
	assert.Equal(t, "tenant-123", f.config.LiteLLMExtraHeaders["X-Tenant"])

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)
	require.NotNil(t, raw)
}

// TestCreateLiteLLMProvider_UnsetPlaceholderFallsBack verifies that when an
// env-var placeholder references a variable set to empty string, it expands to ""
// and the factory falls back to the direct env lookup (LITELLM_ENDPOINT / LITELLM_BASE_URL).
func TestCreateLiteLLMProvider_UnsetPlaceholderFallsBack(t *testing.T) {
	// LITELLM_BASE_URL is NOT set in the config placeholder, but IS set as a
	// direct env var for the fallback lookup.
	t.Setenv("MY_CUSTOM_ENDPOINT", "") // placeholder target set to empty string, triggers fallback
	requestPath := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath <- r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()
	t.Setenv("LITELLM_BASE_URL", server.URL)

	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "${MY_CUSTOM_ENDPOINT}", // expands to ""
	})

	// Endpoint placeholder expanded to "" so the factory falls back to env.
	assert.Equal(t, "", f.config.LiteLLMEndpoint)

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)
	client, ok := raw.(*litellm.Client)
	require.True(t, ok)
	response, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "ping"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", response.Content)
	assert.Equal(t, "/v1/chat/completions", <-requestPath)
}

// TestCreateLiteLLMProvider_GenuinelyUnsetPlaceholderFallsBack verifies that
// when an env-var placeholder references a variable that is genuinely absent
// from the environment (LookupEnv returns false), ExpandEnvPlaceholders leaves
// the literal "${VAR}" in place. The factory detects the unresolved placeholder
// via UnresolvedEnvPlaceholders, logs a warning, and falls back to
// LITELLM_ENDPOINT / LITELLM_BASE_URL — rather than using the literal string
// as the endpoint URL and producing "no Host in request URL".
func TestCreateLiteLLMProvider_GenuinelyUnsetPlaceholderFallsBack(t *testing.T) {
	const varName = "MY_GENUINELY_UNSET_ENDPOINT"
	t.Setenv(varName, "") // ensure it's unset for this test via Unsetenv
	os.Unsetenv(varName)  //nolint:tenv // deliberately unset after Setenv cleanup registration

	requestPath := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath <- r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()
	t.Setenv("LITELLM_BASE_URL", server.URL)

	f := NewProviderFactory(FactoryConfig{
		LiteLLMEndpoint: "${" + varName + "}", // expands to literal "${MY_GENUINELY_UNSET_ENDPOINT}"
	})

	// ExpandEnvPlaceholders leaves the placeholder literal when the var is unset.
	assert.Equal(t, "${"+varName+"}", f.config.LiteLLMEndpoint)

	raw, err := f.createLiteLLMProvider("")
	require.NoError(t, err)
	client, ok := raw.(*litellm.Client)
	require.True(t, ok)
	response, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "ping"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", response.Content)
	assert.Equal(t, "/v1/chat/completions", <-requestPath)
}
