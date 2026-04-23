// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResolveMaxOutput exercises the precedence rules for the output token cap
// used when constructing per-provider LLM clients. Before this helper existed,
// the factory silently pinned every provider at 4096 output tokens regardless
// of which model was selected — truncating large outputs on models like Claude
// Opus 4.6 (128K) without the caller ever knowing.
func TestResolveMaxOutput(t *testing.T) {
	tests := []struct {
		name             string
		configuredMax    int
		provider         string
		model            string
		wantMaxOutputCap int
		description      string
	}{
		{
			name:             "explicit user config wins",
			configuredMax:    2048,
			provider:         "anthropic",
			model:            "claude-opus-4-6",
			wantMaxOutputCap: 2048,
			description:      "explicit FactoryConfig.MaxTokens must override catalog",
		},
		{
			name:             "anthropic opus 4.6 reads catalog",
			configuredMax:    0,
			provider:         "anthropic",
			model:            "claude-opus-4-6",
			wantMaxOutputCap: 128_000,
			description:      "catalog MaxOutputTokens flows through when user did not configure",
		},
		{
			name:             "anthropic sonnet 4.6 reads catalog",
			configuredMax:    0,
			provider:         "anthropic",
			model:            "claude-sonnet-4-6",
			wantMaxOutputCap: 64_000,
			description:      "catalog MaxOutputTokens differs per model",
		},
		{
			name:             "openai gpt-5 reads catalog",
			configuredMax:    0,
			provider:         "openai",
			model:            "gpt-5",
			wantMaxOutputCap: 128_000,
			description:      "catalog entries work for openai provider",
		},
		{
			name:             "gemini 2.5 pro reads catalog",
			configuredMax:    0,
			provider:         "gemini",
			model:            "gemini-2.5-pro",
			wantMaxOutputCap: 65_536,
			description:      "catalog entries work for gemini provider",
		},
		{
			name:             "bedrock opus 4.6 reads catalog",
			configuredMax:    0,
			provider:         "bedrock",
			model:            "us.anthropic.claude-opus-4-6-v1",
			wantMaxOutputCap: 128_000,
			description:      "catalog entries work for bedrock deployment IDs",
		},
		{
			name:             "azureopenai alias normalizes",
			configuredMax:    0,
			provider:         "azureopenai",
			model:            "gpt-5",
			wantMaxOutputCap: 128_000,
			description:      "azureopenai alias must resolve the same as azure-openai",
		},
		{
			name:             "unknown model falls back to safe default",
			configuredMax:    0,
			provider:         "anthropic",
			model:            "some-unreleased-claude-7",
			wantMaxOutputCap: fallbackMaxOutputTokens,
			description:      "unknown models get the conservative fallback",
		},
		{
			name:             "unknown provider falls back to safe default",
			configuredMax:    0,
			provider:         "not-a-provider",
			model:            "anything",
			wantMaxOutputCap: fallbackMaxOutputTokens,
			description:      "unknown providers get the conservative fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewProviderFactory(FactoryConfig{MaxTokens: tt.configuredMax})
			got := f.resolveMaxOutput(tt.provider, tt.model)
			assert.Equal(t, tt.wantMaxOutputCap, got, tt.description)
		})
	}
}
