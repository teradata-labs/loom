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
package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetModelContextLimits(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		expectedMax      int
		expectedReserved int
		shouldFind       bool
	}{
		{
			name:             "Claude Sonnet 4 exact match",
			model:            "claude-sonnet-4",
			expectedMax:      200000,
			expectedReserved: 20000,
			shouldFind:       true,
		},
		{
			name:             "Claude Sonnet 4 with version suffix",
			model:            "claude-sonnet-4-20250514",
			expectedMax:      200000,
			expectedReserved: 20000,
			shouldFind:       true,
		},
		{
			name:             "Llama 3.1 exact match",
			model:            "llama3.1",
			expectedMax:      128000,
			expectedReserved: 12800,
			shouldFind:       true,
		},
		{
			name:             "Llama 3.1 with size suffix",
			model:            "llama3.1:8b",
			expectedMax:      128000,
			expectedReserved: 12800,
			shouldFind:       true,
		},
		{
			name:             "Mistral exact match",
			model:            "mistral",
			expectedMax:      32000,
			expectedReserved: 3200,
			shouldFind:       true,
		},
		{
			name:             "Qwen 2.5 Coder 32B",
			model:            "qwen2.5-coder:32b",
			expectedMax:      131072,
			expectedReserved: 13107,
			shouldFind:       true,
		},
		{
			name:             "DeepSeek R1 7B",
			model:            "deepseek-r1:7b",
			expectedMax:      64000,
			expectedReserved: 6400,
			shouldFind:       true,
		},
		{
			name:             "Phi-4",
			model:            "phi4",
			expectedMax:      16000,
			expectedReserved: 1600,
			shouldFind:       true,
		},
		{
			name:             "Gemini 1.5 Pro (huge context)",
			model:            "gemini-1.5-pro",
			expectedMax:      1000000,
			expectedReserved: 100000,
			shouldFind:       true,
		},
		{
			name:       "Unknown model",
			model:      "some-unknown-model",
			shouldFind: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := GetModelContextLimits(tt.model)
			if tt.shouldFind {
				assert.NotNil(t, limits, "Expected to find limits for %s", tt.model)
				assert.Equal(t, tt.expectedMax, limits.MaxContextTokens, "MaxContextTokens mismatch")
				assert.Equal(t, tt.expectedReserved, limits.ReservedOutputTokens, "ReservedOutputTokens mismatch")
			} else {
				assert.Nil(t, limits, "Expected not to find limits for %s", tt.model)
			}
		})
	}
}

func TestGetProviderDefaultLimits(t *testing.T) {
	tests := []struct {
		name             string
		provider         string
		expectedMax      int
		expectedReserved int
	}{
		{
			name:             "Anthropic",
			provider:         "anthropic",
			expectedMax:      200000,
			expectedReserved: 20000,
		},
		{
			name:             "Bedrock (Claude)",
			provider:         "bedrock",
			expectedMax:      200000,
			expectedReserved: 20000,
		},
		{
			name:             "Ollama (conservative)",
			provider:         "ollama",
			expectedMax:      32000,
			expectedReserved: 3200,
		},
		{
			name:             "OpenAI",
			provider:         "openai",
			expectedMax:      128000,
			expectedReserved: 12800,
		},
		{
			name:             "Gemini (huge)",
			provider:         "gemini",
			expectedMax:      1000000,
			expectedReserved: 100000,
		},
		{
			name:             "Unknown provider (conservative fallback)",
			provider:         "unknown",
			expectedMax:      8192,
			expectedReserved: 819,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := GetProviderDefaultLimits(tt.provider)
			assert.Equal(t, tt.expectedMax, limits.MaxContextTokens, "MaxContextTokens mismatch")
			assert.Equal(t, tt.expectedReserved, limits.ReservedOutputTokens, "ReservedOutputTokens mismatch")
		})
	}
}

func TestResolveContextLimits(t *testing.T) {
	tests := []struct {
		name               string
		provider           string
		model              string
		configuredMax      int32
		configuredReserved int32
		expectedMax        int
		expectedReserved   int
		description        string
	}{
		{
			name:               "Both explicitly configured",
			provider:           "ollama",
			model:              "llama3.1",
			configuredMax:      100000,
			configuredReserved: 5000,
			expectedMax:        100000,
			expectedReserved:   5000,
			description:        "Explicit config takes precedence",
		},
		{
			name:               "Only max configured (auto-calculate reserved)",
			provider:           "ollama",
			model:              "llama3.1",
			configuredMax:      100000,
			configuredReserved: 0,
			expectedMax:        100000,
			expectedReserved:   10000, // 10% of 100000
			description:        "Reserved auto-calculated as 10% of max",
		},
		{
			name:               "Model lookup with no config",
			provider:           "ollama",
			model:              "llama3.1",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        128000,
			expectedReserved:   12800,
			description:        "Use model-specific limits from lookup table",
		},
		{
			name:               "Model lookup with custom reserved",
			provider:           "ollama",
			model:              "llama3.1",
			configuredMax:      0,
			configuredReserved: 20000,
			expectedMax:        128000,
			expectedReserved:   20000,
			description:        "Use model max from lookup, custom reserved",
		},
		{
			name:               "Unknown model - provider default",
			provider:           "ollama",
			model:              "unknown-model",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        32000,
			expectedReserved:   3200,
			description:        "Fall back to provider defaults",
		},
		{
			name:               "Unknown provider - system default",
			provider:           "unknown",
			model:              "unknown-model",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        8192,
			expectedReserved:   819,
			description:        "Fall back to conservative system default",
		},
		{
			name:               "Versioned model (prefix match)",
			provider:           "ollama",
			model:              "llama3.1:70b-instruct",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        128000,
			expectedReserved:   12800,
			description:        "Prefix matching works for versioned models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := ResolveContextLimits(tt.provider, tt.model, tt.configuredMax, tt.configuredReserved)
			assert.Equal(t, tt.expectedMax, limits.MaxContextTokens,
				"%s: MaxContextTokens mismatch", tt.description)
			assert.Equal(t, tt.expectedReserved, limits.ReservedOutputTokens,
				"%s: ReservedOutputTokens mismatch", tt.description)
		})
	}
}

func TestModelPrefixMatching(t *testing.T) {
	// Test that prefix matching works for various model name formats
	tests := []struct {
		model       string
		shouldMatch bool
		baseModel   string
	}{
		{"llama3.1", true, "llama3.1"},
		{"llama3.1:8b", true, "llama3.1"},
		{"llama3.1:70b-instruct", true, "llama3.1"},
		{"llama3.1-custom", true, "llama3.1"},
		{"mistral", true, "mistral"},
		{"mistral:7b", true, "mistral"},
		{"mistral-7b-instruct", true, "mistral"},
		{"claude-3-5-sonnet", true, "claude-3-5-sonnet"},
		{"claude-3-5-sonnet-20241022", true, "claude-3-5-sonnet"},
		{"claude-3-5-sonnet-20241022-v2:0", true, "claude-3-5-sonnet"},
		{"totally-unknown", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			limits := GetModelContextLimits(tt.model)
			if tt.shouldMatch {
				assert.NotNil(t, limits, "Expected to find limits for %s (should match %s)", tt.model, tt.baseModel)
			} else {
				assert.Nil(t, limits, "Expected not to find limits for %s", tt.model)
			}
		})
	}
}
