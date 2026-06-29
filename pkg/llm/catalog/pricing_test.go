// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package catalog

import "testing"

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
