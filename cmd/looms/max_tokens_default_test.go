// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/teradata-labs/loom/pkg/llm/factory"
)

// An unset llm.max_tokens resolves through the model looms will actually use,
// so the per-agent path (agent YAML naming only a provider) and the server
// default path agree on the model's catalog output limit.
func TestDefaultModelForLLM_DrivesOutputLimit(t *testing.T) {
	tests := []struct {
		name     string
		cfg      LLMConfig
		provider string
		want     int
	}{
		{
			name:     "bedrock opus 4.5 from server config",
			cfg:      LLMConfig{Provider: "bedrock", BedrockModelID: "us.anthropic.claude-opus-4-5-20251101-v1:0"},
			provider: "bedrock",
			want:     64_000,
		},
		{
			name:     "explicit max_tokens wins",
			cfg:      LLMConfig{Provider: "bedrock", BedrockModelID: "us.anthropic.claude-opus-4-5-20251101-v1:0", MaxTokens: 8192},
			provider: "bedrock",
			want:     8192,
		},
		{
			name:     "azure deployment id is not a catalog model",
			cfg:      LLMConfig{Provider: "azure-openai", AzureOpenAIDeploymentID: "my-deployment"},
			provider: "azure-openai",
			want:     4096,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := defaultModelForLLM(tt.cfg, tt.provider)
			assert.Equal(t, tt.want, factory.ResolveMaxOutput(tt.cfg.MaxTokens, tt.provider, model))
		})
	}

	// getDefaultModelForProvider is now a thin wrapper; it must keep returning
	// the provider's configured model.
	cfg := &Config{LLM: LLMConfig{Provider: "bedrock", BedrockModelID: "us.anthropic.claude-opus-4-5-20251101-v1:0"}}
	assert.Equal(t, "us.anthropic.claude-opus-4-5-20251101-v1:0", getDefaultModelForProvider(cfg))
}
