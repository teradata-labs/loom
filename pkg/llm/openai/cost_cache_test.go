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
		{"coding-agent/claude-fable-5-1", 10.0, 50.0}, // was priced as gpt-4o
		{"coding-agent/claude-opus-5-5", 4.0, 20.0},
		{"coding-agent/claude-opus-5", 5.0, 25.0},
		{"coding-agent/claude-sonnet-5", 2.0, 10.0},
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

// A gateway-proxied Fable takes Anthropic's cache multipliers, not OpenAI's:
// before the shared matcher it was not recognised as Claude at all, so it was
// priced at gpt-4o rates with the 0.5x OpenAI cache-read discount.
func TestGatewayProxiedFableUsesAnthropicRatesAndCacheTiers(t *testing.T) {
	c := &Client{model: "coding-agent/claude-fable-5-1"}
	const promptTokens, cacheRead, output = 1_000_000, 800_000, 10_000
	got := c.calculateCost(promptTokens, output, cacheRead, 0)
	// Fable 5.1 reads cache at 0.025x ($0.25/MTok), not the 0.10x of older models.
	want := (200_000*10.0 + 800_000*10.0*0.025 + output*50.0) / 1e6
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("fable cost = %.6f, want %.6f", got, want)
	}
}

// Cache-read rates follow the model: a million cached tokens cost $0.25 on
// Fable 5.1, $0.20 on Opus 5.5, $0.10 on Sonnet 5.5 and $1.00 on Fable 5.
func TestGatewayProxiedClaude5CacheReadRates(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"coding-agent/claude-fable-5-1", 0.25},
		{"coding-agent/claude-opus-5-5", 0.20},
		{"coding-agent/claude-sonnet-5-5", 0.10},
		{"coding-agent/claude-fable-5", 1.00},
	} {
		c := &Client{model: tc.id}
		got := c.calculateCost(1_000_000, 0, 1_000_000, 0)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("%s: 1M cache reads = $%.4f, want $%.4f", tc.id, got, tc.want)
		}
	}
}

// Haiku 5.5 bills its $0.50/$2.50 card once prompt_tokens (cache buckets
// included) passes 100K, and $0.10/$0.50 at or below it.
func TestGatewayProxiedHaiku55LongContextCard(t *testing.T) {
	c := &Client{model: "coding-agent/claude-haiku-5-5"}
	if got, want := c.calculateCost(100_000, 1_000, 0, 0), (100_000*0.10+1_000*0.50)/1e6; math.Abs(got-want) > 1e-12 {
		t.Fatalf("100K prompt = %.8f, want %.8f", got, want)
	}
	got := c.calculateCost(150_000, 1_000, 100_000, 0)
	want := (50_000*0.50 + 100_000*0.50*0.10 + 1_000*2.50) / 1e6
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("150K prompt = %.8f, want %.8f", got, want)
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

// OpenAI and Anthropic bill cached input differently, so the multipliers follow
// the rate card rather than being fixed. A genuine gpt-4o reporting
// cached_tokens must bill those at 0.5x — charging Anthropic's 0.10x would
// undercharge the cached portion five-fold, and the direct-OpenAI and streaming
// paths have no gateway cost header to mask it.
func TestCalculateCost_CacheMultipliersFollowTheRateCard(t *testing.T) {
	const (
		promptTokens = 100_000 // inclusive of the cache buckets below
		cacheRead    = 80_000
		cacheWrite   = 10_000
		output       = 1_000
	)

	t.Run("openai bills cached reads at 0.5x with no write premium", func(t *testing.T) {
		c := &Client{model: "gpt-4o"}
		got := c.calculateCost(promptTokens, output, cacheRead, cacheWrite)
		want := (10_000*2.50 + 10_000*2.50*1.0 + 80_000*2.50*0.5 + 1_000*10.00) / 1e6
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("gpt-4o cost = %.6f, want %.6f", got, want)
		}
		// Anthropic's read multiplier here would undercharge by 5x on the cached
		// portion — the regression this pins.
		anthropicMult := (10_000*2.50 + 10_000*2.50*1.25 + 80_000*2.50*0.10 + 1_000*10.00) / 1e6
		if math.Abs(got-anthropicMult) < 1e-9 {
			t.Fatal("gpt-4o priced with Anthropic cache multipliers")
		}
	})

	t.Run("claude keeps 1.25x writes and 0.10x reads", func(t *testing.T) {
		c := &Client{model: "coding-agent/claude-sonnet-4-6"}
		got := c.calculateCost(promptTokens, output, cacheRead, cacheWrite)
		want := (10_000*3.00 + 10_000*3.00*1.25 + 80_000*3.00*0.10 + 1_000*15.00) / 1e6
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("claude cost = %.6f, want %.6f", got, want)
		}
	})
}
