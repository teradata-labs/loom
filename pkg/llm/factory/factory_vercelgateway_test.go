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
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/types"
)

func clearGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv(vercelgateway.EnvLoomAPIKey, "")
	t.Setenv(vercelgateway.EnvAPIKey, "")
}

func TestCreateProvider_VercelGateway(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("GW_KEY_FOR_TEST", "vck_test_not_a_real_key")
	f := NewProviderFactory(FactoryConfig{
		DefaultProvider: vercelgateway.ProviderName,
		VercelGateway: vercelgateway.Config{
			APIKey:   "${GW_KEY_FOR_TEST}",
			Model:    "openai/gpt-5",
			Endpoint: "https://eu.ai-gateway.vercel.sh",
		},
	})
	for _, name := range []string{"vercel-ai-gateway", "vercel"} {
		raw, err := f.CreateProvider(name, "")
		require.NoError(t, err, name)
		c, ok := raw.(*vercelgateway.Client)
		require.True(t, ok, "got %T", raw)
		assert.Equal(t, "openai/gpt-5", c.Model())
		assert.Equal(t, "https://eu.ai-gateway.vercel.sh", c.BaseURL())
	}
	raw, err := f.CreateProvider("vercel", "anthropic/claude-haiku-4.5")
	require.NoError(t, err)
	assert.Equal(t, vercelgateway.SurfaceMessages, raw.(*vercelgateway.Client).Surface())
	assert.True(t, f.IsProviderAvailable(vercelgateway.ProviderName))
}

func TestCreateProvider_VercelGatewayErrors(t *testing.T) {
	clearGatewayEnv(t)
	f := NewProviderFactory(FactoryConfig{VercelGateway: vercelgateway.Config{APIKey: "vck_test_not_a_real_key"}})
	_, err := f.CreateProvider(vercelgateway.ProviderName, "")
	require.ErrorContains(t, err, "model is required")

	f = NewProviderFactory(FactoryConfig{VercelGateway: vercelgateway.Config{Model: "openai/gpt-5"}})
	_, err = f.CreateProvider(vercelgateway.ProviderName, "")
	require.ErrorContains(t, err, "no credential")
	assert.False(t, f.IsProviderAvailable(vercelgateway.ProviderName))

	t.Setenv(vercelgateway.EnvAPIKey, "vck_from_env")
	_, err = f.CreateProvider(vercelgateway.ProviderName, "")
	require.NoError(t, err, "AI_GATEWAY_API_KEY satisfies the credential")
}

// FactoryConfig.ThinkingLevel reaches the gateway client, as it does for the
// anthropic and bedrock builders: a Claude id sends Anthropic thinking.
func TestCreateProvider_VercelGatewayThinkingLevel(t *testing.T) {
	clearGatewayEnv(t)
	var body atomic.Value
	body.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body.Store(string(raw))
		// The gateway client streams every call, Chat included.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"gen_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n"+
			"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()
	f := NewProviderFactory(FactoryConfig{ThinkingLevel: "high", MaxTokens: 40000,
		VercelGateway: vercelgateway.Config{APIKey: "vck_test_not_a_real_key", Endpoint: srv.URL}})
	raw, err := f.CreateProvider(vercelgateway.ProviderName, "anthropic/claude-sonnet-4.5")
	require.NoError(t, err)
	_, err = raw.(*vercelgateway.Client).Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, body.Load(), `"thinking":{`)
}
