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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// Issue #348: the custom-LLM path built clients with no rate limiter and its
// azure branch read only serverConfig (no env fallback), so WHERE the API key
// lived silently decided whether an agent was throttled at all.

func TestCreateLLMProviderFromProtoConfigAzureEnvFallback(t *testing.T) {
	// Key present only in the environment must not fail client creation.
	t.Setenv("AZURE_OPENAI_API_KEY", "test-key-not-real")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://unit-test.openai.azure.com/")

	serverConfig := &Config{} // no azure fields set — env is the only source
	protoConfig := &loomv1.LLMConfig{Provider: "azure-openai", Model: "gpt-4o"}

	provider, err := createLLMProviderFromProtoConfig(protoConfig, serverConfig, zap.NewNop())
	require.NoError(t, err, "env-only azure credentials must work on the custom-LLM path")
	require.NotNil(t, provider)
	assert.Equal(t, "azure-openai", provider.Name())
}

func TestCreateLLMProviderFromProtoConfigAzureYAMLKeyStillWorks(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "")

	serverConfig := &Config{}
	serverConfig.LLM.AzureOpenAIEndpoint = "https://unit-test.openai.azure.com/"
	serverConfig.LLM.AzureOpenAIAPIKey = "yaml-key-not-real"
	protoConfig := &loomv1.LLMConfig{Provider: "azure-openai", Model: "gpt-4o"}

	provider, err := createLLMProviderFromProtoConfig(protoConfig, serverConfig, zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, "azure-openai", provider.Name())
}

func TestBuildRateLimiterConfigDefaults(t *testing.T) {
	// Nil proto → enabled with zero numerics (backfilled by NewRateLimiter).
	cfg := agent.BuildRateLimiterConfig(nil, zap.NewNop())
	assert.True(t, cfg.Enabled, "rate limiting must default to ON for every construction path")

	// Explicit disable is honored.
	off := agent.BuildRateLimiterConfig(&loomv1.LLMRateLimitConfig{Disabled: true}, zap.NewNop())
	assert.False(t, off.Enabled)

	// Agent-specified numbers pass through.
	custom := agent.BuildRateLimiterConfig(&loomv1.LLMRateLimitConfig{
		RequestsPerSecond: 100,
		TokensPerMinute:   1_200_000,
	}, zap.NewNop())
	assert.Equal(t, 100.0, custom.RequestsPerSecond)
	assert.Equal(t, int64(1_200_000), custom.TokensPerMinute)
}
