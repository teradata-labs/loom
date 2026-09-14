// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestLookupPricing_MatchesCatalog asserts the lazily-built price index returns
// exactly what BuildCatalog() holds for every cataloged model, keyed by provider.
func TestLookupPricing_MatchesCatalog(t *testing.T) {
	cat := BuildCatalog()
	if len(cat) == 0 {
		t.Fatal("catalog is empty")
	}
	for provider, models := range cat {
		for _, m := range models {
			in, out, found := LookupPricing(provider, m.Id)
			if !found {
				t.Errorf("LookupPricing(%q, %q): found=false, want true", provider, m.Id)
				continue
			}
			if in != m.CostPer_1MInputUsd || out != m.CostPer_1MOutputUsd {
				t.Errorf("LookupPricing(%q, %q) = (%v, %v), want (%v, %v)",
					provider, m.Id, in, out, m.CostPer_1MInputUsd, m.CostPer_1MOutputUsd)
			}
		}
	}
}

// TestLookupPricing_RegisteredSourceWins asserts the registered default Source
// is consulted before the static table — the mechanism that lets an embedder
// price gateway aliases at the rates it actually pays. Previously this read
// the static table only, so a registered catalog could never influence cost.
func TestLookupPricing_RegisteredSourceWins(t *testing.T) {
	original := DefaultSource()
	t.Cleanup(func() { Register(original) })

	Register(MultiSource{
		newFakeSource(
			// A gateway alias the static table has never heard of.
			&loomv1.ModelInfo{Id: "deepseek.v3.2", Provider: "litellm", CostPer_1MInputUsd: 0.27, CostPer_1MOutputUsd: 1.10},
			// An override of a static id: the registered rate wins.
			&loomv1.ModelInfo{Id: "claude-opus-4-6", Provider: "anthropic", CostPer_1MInputUsd: 1, CostPer_1MOutputUsd: 2},
			// A metadata-only row (no rates) must NOT zero out the static price.
			&loomv1.ModelInfo{Id: "gpt-4.1", Provider: "openai", ContextWindow: 1_047_576},
		),
		StaticSource(),
	})

	in, out, found := LookupPricing("litellm", "deepseek.v3.2")
	require.True(t, found, "gateway alias resolves through the registered source")
	assert.Equal(t, 0.27, in)
	assert.Equal(t, 1.10, out)

	in, out, found = LookupPricing("anthropic", "claude-opus-4-6")
	require.True(t, found)
	assert.Equal(t, 1.0, in, "registered override outranks the static rate")
	assert.Equal(t, 2.0, out)

	staticIn, staticOut, ok := staticPricing("openai", "gpt-4.1")
	require.True(t, ok)
	in, out, found = LookupPricing("openai", "gpt-4.1")
	require.True(t, found)
	assert.Equal(t, staticIn, in, "an unpriced registered row falls through to the static rate")
	assert.Equal(t, staticOut, out)

	// Provider aliases normalize before the lookup, as Lookup already does.
	_, _, found = LookupPricing("azureopenai", "gpt-4.1")
	assert.True(t, found)
}

// staticPricing reads the static table directly, bypassing the registered
// source, for the fall-through assertion above.
func staticPricing(provider, modelID string) (float64, float64, bool) {
	priceOnce.Do(buildPriceIndex)
	p, ok := priceIndex[provider][modelID]
	return p[0], p[1], ok
}

func TestLookupPricing_NotFound(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"unknown provider", "no-such-provider", "gpt-4.1"},
		{"unknown model", "anthropic", "claude-does-not-exist"},
		// A bedrock-only id must not resolve under the "anthropic" provider:
		// pricing is keyed by (provider, model), not model alone.
		{"cross-provider id", "anthropic", "us.anthropic.claude-opus-4-7-v1:0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, found := LookupPricing(tc.provider, tc.model); found {
				t.Errorf("LookupPricing(%q, %q): found=true, want false", tc.provider, tc.model)
			}
		})
	}
}
