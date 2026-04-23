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
			// Claude 4.x entries intentionally no longer live in this legacy
			// map — the catalog owns per-model ContextWindow/MaxOutputTokens.
			// An unqualified "claude-sonnet-4" that isn't in the catalog must
			// not match a stale prefix here; callers fall to provider defaults.
			name:       "Claude Sonnet 4 (unqualified) no longer in legacy map",
			model:      "claude-sonnet-4",
			shouldFind: false,
		},
		{
			name:       "Claude Sonnet 4 dated variant no longer in legacy map",
			model:      "claude-sonnet-4-20250514",
			shouldFind: false,
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
			name:             "Gemini 3 Flash Preview",
			model:            "gemini-3-flash-preview",
			expectedMax:      1048576,
			expectedReserved: 65536,
			shouldFind:       true,
		},
		{
			name:             "Gemini 2.5 Flash",
			model:            "gemini-2.5-flash",
			expectedMax:      1048576,
			expectedReserved: 65536,
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
			name:             "Gemini",
			provider:         "gemini",
			expectedMax:      1048576,
			expectedReserved: 65536,
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
			expectedReserved:   8192,
			description:        "Use catalog ContextWindow and MaxOutputTokens for exact match",
		},
		{
			name:               "Model lookup with custom reserved",
			provider:           "ollama",
			model:              "llama3.1",
			configuredMax:      0,
			configuredReserved: 20000,
			expectedMax:        128000,
			expectedReserved:   20000,
			description:        "Use catalog max, explicit configuredReserved overrides catalog MaxOutputTokens",
		},
		{
			name:               "Catalog lookup: Anthropic Claude Opus 4.6",
			provider:           "anthropic",
			model:              "claude-opus-4-6",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        1_000_000,
			expectedReserved:   128_000,
			description:        "Catalog ContextWindow (1M) and MaxOutputTokens (128K) for Claude Opus 4.6",
		},
		{
			name:               "Catalog lookup: Gemini 2.5 Pro",
			provider:           "gemini",
			model:              "gemini-2.5-pro",
			configuredMax:      0,
			configuredReserved: 0,
			expectedMax:        1_048_576,
			expectedReserved:   65_536,
			description:        "Catalog values for Gemini 2.5 Pro",
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
