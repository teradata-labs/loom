// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		modelID        string
		wantFound      bool
		wantContextWin int32
		wantMaxOutput  int32
	}{
		{
			name:           "anthropic claude opus 4.6",
			provider:       "anthropic",
			modelID:        "claude-opus-4-6",
			wantFound:      true,
			wantContextWin: 1_000_000,
			wantMaxOutput:  128_000,
		},
		{
			name:           "anthropic claude sonnet 4.5 (dated ID)",
			provider:       "anthropic",
			modelID:        "claude-sonnet-4-5-20250929",
			wantFound:      true,
			wantContextWin: 200_000,
			wantMaxOutput:  64_000,
		},
		{
			name:           "openai gpt-5",
			provider:       "openai",
			modelID:        "gpt-5",
			wantFound:      true,
			wantContextWin: 400_000,
			wantMaxOutput:  128_000,
		},
		{
			name:           "azureopenai alias resolves",
			provider:       "azureopenai",
			modelID:        "gpt-5",
			wantFound:      true,
			wantContextWin: 400_000,
			wantMaxOutput:  128_000,
		},
		{
			name:           "anthropic claude opus 4.7 (added 2026-04)",
			provider:       "anthropic",
			modelID:        "claude-opus-4-7",
			wantFound:      true,
			wantContextWin: 1_000_000,
			wantMaxOutput:  128_000,
		},
		{
			name:      "unknown model returns nil",
			provider:  "anthropic",
			modelID:   "claude-9000",
			wantFound: false,
		},
		{
			name:      "unknown provider returns nil",
			provider:  "not-a-provider",
			modelID:   "anything",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lookup(tt.provider, tt.modelID)
			if !tt.wantFound {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, tt.wantContextWin, got.ContextWindow)
				assert.Equal(t, tt.wantMaxOutput, got.MaxOutputTokens)
			}
		})
	}
}

func TestBuildCatalogCompleteness(t *testing.T) {
	c := BuildCatalog()
	for provider, models := range c {
		assert.NotEmpty(t, models, "provider %q has no models", provider)
		for _, m := range models {
			assert.NotEmpty(t, m.Id, "provider %q has a model with empty ID", provider)
			assert.Greater(t, m.ContextWindow, int32(0),
				"model %s (%s) needs ContextWindow > 0", m.Id, provider)
			assert.Greater(t, m.MaxOutputTokens, int32(0),
				"model %s (%s) needs MaxOutputTokens > 0", m.Id, provider)
		}
	}
}
