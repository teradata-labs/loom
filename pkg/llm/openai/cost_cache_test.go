package openai

import (
	"math"
	"net/http"
	"testing"
)

// The OpenAI-compatible shape reports prompt_tokens INCLUSIVE of cached tokens,
// so a cache-blind total over-charges a cache-heavy call several fold. These are
// the real counts from a 20-turn ContextCompilation benchmark run.
func TestCalculateCost_CacheTiersAreBilledSeparately(t *testing.T) {
	c := &Client{model: "coding-agent/claude-sonnet-4-6"}
	const (
		promptTokens = 950830 // includes the two cache buckets below
		cacheRead    = 872023
		cacheWrite   = 78708
		output       = 11140
	)
	got := c.calculateCost(promptTokens, output, cacheRead, cacheWrite)

	// 99 uncached@1.0x + 78,708 write@1.25x + 872,023 read@0.10x + 11,140 out.
	// The id is not in the catalog, but it is recognisably Claude Sonnet, so it
	// must price at $3/$15 — not the GPT-4o rates this client used to fall back to.
	want := (99*3.00 + 78708*3.00*1.25 + 872023*3.00*0.10 + 11140*15.00) / 1e6
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("cache-aware cost = %.6f, want %.6f", got, want)
	}
	// A cache-blind total would bill every input token at the full rate.
	if blind := (promptTokens*3.00 + output*15.00) / 1e6; got >= blind {
		t.Fatalf("cache tiers not applied: got %.6f, cache-blind would be %.6f", got, blind)
	}
}

// A gateway-proxied Claude must not be priced with the GPT rate card.
func TestGatewayProxiedClaudeIsNotPricedAsGPT(t *testing.T) {
	for _, tc := range []struct {
		id      string
		in, out float64
	}{
		{"coding-agent/claude-sonnet-4-6", 3.0, 15.0},
		{"coding-agent/claude-haiku-4-5", 1.0, 5.0},
		{"claude-opus-4-1", 15.0, 75.0},
		{"anything/claude-opus-4-6", 5.0, 25.0},
	} {
		in, out, matched := anthropicFallbackPricing(tc.id)
		if !matched || in != tc.in || out != tc.out {
			t.Fatalf("%s: got (%v,%v,%v), want (%v,%v,true)", tc.id, in, out, matched, tc.in, tc.out)
		}
	}
	if _, _, matched := anthropicFallbackPricing("gpt-4o"); matched {
		t.Fatal("gpt-4o must not match the Anthropic family")
	}
}

// The gateway computes its own cache-aware cost; it is authoritative.
func TestProviderCostHeaderWins(t *testing.T) {
	h := http.Header{}
	h.Set(providerCostHeader, "0.01810725")
	if got := parseProviderCost(h); got != 0.01810725 {
		t.Fatalf("parseProviderCost = %v", got)
	}
	if got := costOrEstimate(parseProviderCost(h), func() float64 { return 99.0 }); got != 0.01810725 {
		t.Fatalf("provider cost must win, got %v", got)
	}
	// Absent or unusable header falls back to the estimate.
	for _, bad := range []string{"", "not-a-number", "-1"} {
		hh := http.Header{}
		hh.Set(providerCostHeader, bad)
		if got := costOrEstimate(parseProviderCost(hh), func() float64 { return 42.0 }); got != 42.0 {
			t.Fatalf("header %q: expected fallback, got %v", bad, got)
		}
	}
}

// Genuine-OpenAI cached reads bill at OpenAI's tiers, not Anthropic's: 0.5x on
// cached input and NO write premium. The regression this pins: a single
// Anthropic-shaped multiplier (0.10x read) under-charged real gpt-4o cached
// reads 5x.
func TestCalculateCost_OpenAIFamilyUsesOpenAICacheTiers(t *testing.T) {
	c := &Client{model: "gpt-4o"}
	const (
		promptTokens = 100_000 // includes the cached bucket below
		cacheRead    = 80_000
		output       = 1_000
	)
	got := c.calculateCost(promptTokens, output, cacheRead, 0)

	// 20k uncached @ $2.50/M + 80k cached @ 0.5x ($1.25/M) + 1k out @ $10/M.
	want := (20_000*2.50 + 80_000*2.50*0.5 + 1_000*10.00) / 1e6
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("openai cache cost = %.6f, want %.6f (0.5x read, no write premium)", got, want)
	}
	// The Anthropic-shaped 0.10x read would be visibly cheaper — pin the gap.
	if wrong := (20_000*2.50 + 80_000*2.50*0.10 + 1_000*10.00) / 1e6; got <= wrong {
		t.Fatalf("cost %.6f still at the Anthropic 0.10x read tier (%.6f)", got, wrong)
	}
}

// A garbage litellm cost header ("Inf"/"NaN") parses in ParseFloat and +Inf is
// not < 0 — both must fall back to the estimate, never poison CostUSD.
func TestParseProviderCost_RejectsInfAndNaN(t *testing.T) {
	for _, v := range []string{"Inf", "+Inf", "-Inf", "NaN"} {
		h := http.Header{}
		h.Set(providerCostHeader, v)
		if got := parseProviderCost(h); got != 0 {
			t.Fatalf("header %q parsed to %v, want 0 (fallback)", v, got)
		}
	}
}
