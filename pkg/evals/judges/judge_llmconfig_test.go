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

package judges

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// llmConfigTestProvider is a mock LLM provider for testing LlmConfig wiring.
type llmConfigTestProvider struct {
	name  string
	model string
}

func (p *llmConfigTestProvider) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: "mock response from " + p.name}, nil
}

func (p *llmConfigTestProvider) Name() string  { return p.name }
func (p *llmConfigTestProvider) Model() string { return p.model }

func TestNewLLMJudge_WithProviderFactory_UsesLlmConfig(t *testing.T) {
	// Arrange: fallback provider and a factory that returns a different provider
	fallbackProvider := &llmConfigTestProvider{name: "fallback", model: "fallback-model"}
	configProvider := &llmConfigTestProvider{name: "config-provider", model: "config-model"}

	factoryCalled := false
	factory := func(cfg *loomv1.LLMConfig) (types.LLMProvider, error) {
		factoryCalled = true
		assert.Equal(t, "anthropic", cfg.Provider)
		assert.Equal(t, "claude-sonnet-4-20250514", cfg.Model)
		return configProvider, nil
	}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		LlmConfig: &loomv1.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-20250514",
		},
	}

	// Act
	judge, err := NewLLMJudge(fallbackProvider, config, nil, WithProviderFactory(factory))

	// Assert
	require.NoError(t, err)
	require.NotNil(t, judge)
	assert.True(t, factoryCalled, "factory should have been called")
	assert.Equal(t, "config-provider", judge.llmProvider.Name())
	assert.Equal(t, "config-model", judge.llmProvider.Model())
}

func TestNewLLMJudge_WithoutProviderFactory_UsesLlmConfigIgnored(t *testing.T) {
	// When no factory is provided, LlmConfig is ignored and fallback is used
	fallbackProvider := &llmConfigTestProvider{name: "fallback", model: "fallback-model"}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		LlmConfig: &loomv1.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-20250514",
		},
	}

	// Act
	judge, err := NewLLMJudge(fallbackProvider, config, nil)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, judge)
	assert.Equal(t, "fallback", judge.llmProvider.Name())
}

func TestNewLLMJudge_NoLlmConfig_UsesFallback(t *testing.T) {
	// When LlmConfig is nil, fallback provider is used regardless of factory
	fallbackProvider := &llmConfigTestProvider{name: "fallback", model: "fallback-model"}

	factoryCalled := false
	factory := func(_ *loomv1.LLMConfig) (types.LLMProvider, error) {
		factoryCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		// LlmConfig is nil
	}

	// Act
	judge, err := NewLLMJudge(fallbackProvider, config, nil, WithProviderFactory(factory))

	// Assert
	require.NoError(t, err)
	require.NotNil(t, judge)
	assert.False(t, factoryCalled, "factory should not be called when LlmConfig is nil")
	assert.Equal(t, "fallback", judge.llmProvider.Name())
}

func TestNewLLMJudge_LlmConfigEmptyProvider_UsesFallback(t *testing.T) {
	// When LlmConfig has empty provider, fallback is used
	fallbackProvider := &llmConfigTestProvider{name: "fallback", model: "fallback-model"}

	factoryCalled := false
	factory := func(_ *loomv1.LLMConfig) (types.LLMProvider, error) {
		factoryCalled = true
		return nil, fmt.Errorf("should not be called")
	}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		LlmConfig: &loomv1.LLMConfig{
			Provider: "", // Empty provider
			Model:    "some-model",
		},
	}

	// Act
	judge, err := NewLLMJudge(fallbackProvider, config, nil, WithProviderFactory(factory))

	// Assert
	require.NoError(t, err)
	require.NotNil(t, judge)
	assert.False(t, factoryCalled, "factory should not be called when provider is empty")
	assert.Equal(t, "fallback", judge.llmProvider.Name())
}

func TestNewLLMJudge_FactoryError_ReturnsError(t *testing.T) {
	// When factory returns an error, NewLLMJudge should propagate it
	fallbackProvider := &llmConfigTestProvider{name: "fallback", model: "fallback-model"}

	factory := func(_ *loomv1.LLMConfig) (types.LLMProvider, error) {
		return nil, fmt.Errorf("API key not configured for provider")
	}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		LlmConfig: &loomv1.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-20250514",
		},
	}

	// Act
	judge, err := NewLLMJudge(fallbackProvider, config, nil, WithProviderFactory(factory))

	// Assert
	require.Error(t, err)
	assert.Nil(t, judge)
	assert.Contains(t, err.Error(), "failed to create LLM provider from judge config llm_config")
	assert.Contains(t, err.Error(), "API key not configured")
}

func TestNewLLMJudge_NilFallbackWithLlmConfig_Succeeds(t *testing.T) {
	// When fallback is nil but LlmConfig provides a valid provider, it should succeed
	configProvider := &llmConfigTestProvider{name: "config-provider", model: "config-model"}

	factory := func(_ *loomv1.LLMConfig) (types.LLMProvider, error) {
		return configProvider, nil
	}

	config := &loomv1.JudgeConfig{
		Name: "test-judge",
		LlmConfig: &loomv1.LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-20250514",
		},
	}

	// Act: nil fallback provider, but factory provides one via LlmConfig
	judge, err := NewLLMJudge(nil, config, nil, WithProviderFactory(factory))

	// Assert
	require.NoError(t, err)
	require.NotNil(t, judge)
	assert.Equal(t, "config-provider", judge.llmProvider.Name())
}

func TestNewLLMJudge_NilFallbackNoLlmConfig_ReturnsError(t *testing.T) {
	// When both fallback and LlmConfig are nil/empty, it should error
	config := &loomv1.JudgeConfig{
		Name: "test-judge",
	}

	// Act: nil fallback, no LlmConfig
	judge, err := NewLLMJudge(nil, config, nil)

	// Assert
	require.Error(t, err)
	assert.Nil(t, judge)
	assert.Contains(t, err.Error(), "LLM provider required")
}

func TestNewLLMJudge_ResolutionPriority(t *testing.T) {
	// Table-driven test for the full resolution priority chain
	tests := []struct {
		name             string
		fallbackProvider types.LLMProvider
		llmConfig        *loomv1.LLMConfig
		hasFactory       bool
		factoryProvider  types.LLMProvider
		factoryErr       error
		expectedProvider string
		expectErr        bool
		errContains      string
	}{
		{
			name:             "LlmConfig with factory takes priority over fallback",
			fallbackProvider: &llmConfigTestProvider{name: "fallback", model: "fb"},
			llmConfig:        &loomv1.LLMConfig{Provider: "anthropic", Model: "m"},
			hasFactory:       true,
			factoryProvider:  &llmConfigTestProvider{name: "from-config", model: "fc"},
			expectedProvider: "from-config",
		},
		{
			name:             "fallback used when no LlmConfig",
			fallbackProvider: &llmConfigTestProvider{name: "fallback", model: "fb"},
			llmConfig:        nil,
			hasFactory:       true,
			factoryProvider:  &llmConfigTestProvider{name: "from-config", model: "fc"},
			expectedProvider: "fallback",
		},
		{
			name:             "fallback used when no factory",
			fallbackProvider: &llmConfigTestProvider{name: "fallback", model: "fb"},
			llmConfig:        &loomv1.LLMConfig{Provider: "anthropic", Model: "m"},
			hasFactory:       false,
			expectedProvider: "fallback",
		},
		{
			name:             "error when factory fails",
			fallbackProvider: &llmConfigTestProvider{name: "fallback", model: "fb"},
			llmConfig:        &loomv1.LLMConfig{Provider: "anthropic", Model: "m"},
			hasFactory:       true,
			factoryErr:       fmt.Errorf("creation failed"),
			expectErr:        true,
			errContains:      "creation failed",
		},
		{
			name:             "nil fallback with config succeeds",
			fallbackProvider: nil,
			llmConfig:        &loomv1.LLMConfig{Provider: "anthropic", Model: "m"},
			hasFactory:       true,
			factoryProvider:  &llmConfigTestProvider{name: "from-config", model: "fc"},
			expectedProvider: "from-config",
		},
		{
			name:             "nil fallback without config fails",
			fallbackProvider: nil,
			llmConfig:        nil,
			hasFactory:       false,
			expectErr:        true,
			errContains:      "LLM provider required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &loomv1.JudgeConfig{
				Name:      "test-judge",
				LlmConfig: tt.llmConfig,
			}

			var opts []LLMJudgeOption
			if tt.hasFactory {
				factory := func(_ *loomv1.LLMConfig) (types.LLMProvider, error) {
					if tt.factoryErr != nil {
						return nil, tt.factoryErr
					}
					return tt.factoryProvider, nil
				}
				opts = append(opts, WithProviderFactory(factory))
			}

			judge, err := NewLLMJudge(tt.fallbackProvider, config, nil, opts...)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, judge)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, judge)
				assert.Equal(t, tt.expectedProvider, judge.llmProvider.Name())
			}
		})
	}
}
