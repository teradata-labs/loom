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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

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

// isolateAWSEnv points the AWS SDK at empty config files and disables IMDS so a
// test never reads the developer's real AWS credentials or region.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "empty-aws")
	require.NoError(t, os.WriteFile(empty, nil, 0o600), "create empty AWS config file")
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_PROFILE", "")
}

// Review F4 on PR #555: `looms serve` builds a startup agent's LLM through
// createLLMProviderFromProtoConfig. Without a bedrock-mantle case, an agent
// YAML with `provider: bedrock-mantle` passed validation but ran on the
// server's default LLM. The switch must build a real bedrock-mantle client.
func TestCreateLLMProviderFromProtoConfigBedrockMantle(t *testing.T) {
	tests := []struct {
		name      string
		region    string
		awsRegion string
		endpoint  string
		model     string
		wantModel string
	}{
		{
			name:      "BEDROCK_MANTLE_REGION",
			region:    "us-west-2",
			model:     "anthropic.claude-haiku-4-5",
			wantModel: "anthropic.claude-haiku-4-5",
		},
		{
			name:      "AWS_REGION fallback",
			awsRegion: "us-east-1",
			model:     "openai.gpt-oss-120b",
			wantModel: "openai.gpt-oss-120b",
		},
		{
			name:      "explicit endpoint, no region",
			endpoint:  "https://bedrock-mantle.us-west-2.api.aws/v1",
			model:     "anthropic.claude-haiku-4-5",
			wantModel: "anthropic.claude-haiku-4-5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWSEnv(t)
			t.Setenv("BEDROCK_MANTLE_REGION", tt.region)
			t.Setenv("AWS_REGION", tt.awsRegion)
			t.Setenv("BEDROCK_MANTLE_ENDPOINT", tt.endpoint)
			t.Setenv("BEDROCK_MANTLE_API_KEY", "ABSKtest")

			protoConfig := &loomv1.LLMConfig{Provider: "bedrock-mantle", Model: tt.model, MaxTokens: 2048}
			provider, err := createLLMProviderFromProtoConfig(protoConfig, &Config{}, zap.NewNop())
			require.NoError(t, err, "bedrock-mantle must be buildable on the looms serve path")
			require.NotNil(t, provider, "provider must not be nil")
			assert.Equal(t, "bedrock-mantle", provider.Name(), "agent must run on bedrock-mantle, not another provider")
			assert.Equal(t, tt.wantModel, provider.Model(), "agent model must come from the agent YAML")
		})
	}
}

func TestCreateLLMProviderFromProtoConfigBedrockMantleMissingRegion(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("BEDROCK_MANTLE_REGION", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("BEDROCK_MANTLE_ENDPOINT", "")
	t.Setenv("BEDROCK_MANTLE_API_KEY", "ABSKtest")

	protoConfig := &loomv1.LLMConfig{Provider: "bedrock-mantle", Model: "anthropic.claude-haiku-4-5"}
	_, err := createLLMProviderFromProtoConfig(protoConfig, &Config{}, zap.NewNop())
	require.Error(t, err, "a bedrock-mantle config with no region and no endpoint must not build")
	assert.Contains(t, err.Error(), "region is required", "error must name the missing region")
}

// An agent role that declares bedrock-mantle must never run on a substitute
// LLM: an operator picks Mantle to keep traffic in their AWS account. Other
// providers keep the existing fall-back-to-default behaviour.
func TestCustomLLMFallbackAllowed(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{provider: "bedrock-mantle", want: false},
		{provider: "anthropic", want: true},
		{provider: "bedrock", want: true},
		{provider: "azure-openai", want: true},
		{provider: "litellm", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			assert.Equal(t, tt.want, customLLMFallbackAllowed(tt.provider),
				"fallback policy for provider %q", tt.provider)
		})
	}
}

// closeCountingBackend records Close calls so a test can prove that a skipped
// agent's backend is released.
type closeCountingBackend struct {
	mockBackend
	closes int
}

func (b *closeCountingBackend) Close() error {
	b.closes++
	return nil
}

func TestSkipAgentForUnbuildableLLM(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	backend := &closeCountingBackend{}
	buildErr := errors.New("bedrock-mantle: region is required (or set an explicit endpoint)")

	skipAgentForUnbuildableLLM(zap.New(core), "mantle_agent", "llm",
		&loomv1.LLMConfig{Provider: "bedrock-mantle", Model: "anthropic.claude-haiku-4-5"}, backend, buildErr)

	assert.Equal(t, 1, backend.closes, "the skipped agent's backend must be closed exactly once")
	errLogs := logs.FilterLevelExact(zapcore.ErrorLevel).All()
	require.Len(t, errLogs, 1, "the skip must be logged once at error level, not as a warning")
	fields := errLogs[0].ContextMap()
	assert.Equal(t, "mantle_agent", fields["name"], "log must name the agent")
	assert.Equal(t, "llm", fields["role"], "log must name the LLM role")
	assert.Equal(t, "bedrock-mantle", fields["provider"], "log must name the provider")
	assert.Equal(t, buildErr.Error(), fields["error"], "log must carry the build error")

	// A nil backend must not panic.
	assert.NotPanics(t, func() {
		skipAgentForUnbuildableLLM(zap.NewNop(), "a", "classifier_llm", nil, nil, buildErr)
	}, "nil backend and nil LLM config must be tolerated")
}
