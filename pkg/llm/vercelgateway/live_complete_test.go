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

//go:build integration

package vercelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Live checks for the features added on feat/vercel-gateway-complete
// (2026-11-19): the Responses API, per-request BYOK, provider timeouts,
// session affinity, the remaining providerOptions keys, thinking replay across
// upstreams, cache usage per upstream, and catalog price tiers. Same switches
// as live_test.go; BYOK tests also need LOOM_LLM_ANTHROPIC_API_KEY.
//
//	VERCEL_GATEWAY_LIVE=1 go test -tags "fts5 integration" -race -run TestLiveComplete ./pkg/llm/vercelgateway/ -v

const (
	liveSonnet55 = "anthropic/claude-sonnet-5.5"
	liveGPT55    = "openai/gpt-5.5"
	envBYOKKey   = "LOOM_LLM_ANTHROPIC_API_KEY" // #nosec G101 -- environment variable name, not a credential
)

// toolLoop runs the add-tool loop used by TestLive_ToolLoopWithThinking and
// returns how many thinking blocks the turns carried (and so were replayed).
// It fails the test unless the model reaches 15.
func toolLoop(t *testing.T, ctx context.Context, c *Client) int {
	t.Helper()
	calc := &liveTool{name: "add", desc: "Add two integers and return the sum."}
	history := say("Use the add tool to compute 2+3, then use it again to add 10 to that result. Then state the final number.")
	blocks := 0
	for turn := 0; turn < 4; turn++ {
		resp, err := c.ChatStream(ctx, history, []shuttle.Tool{calc}, nil)
		require.NoError(t, err, "turn %d", turn)
		blocks += len(resp.ThinkingBlocks)
		logFacts(t, resp)
		t.Logf("turn %d: %d thinking blocks, %d tool calls, cost $%.6f", turn, len(resp.ThinkingBlocks), len(resp.ToolCalls), resp.Usage.CostUSD)
		history = append(history, types.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
			ThinkingBlocks: resp.ThinkingBlocks, Turn: 1})
		if len(resp.ToolCalls) == 0 {
			assert.Contains(t, resp.Content, "15")
			return blocks
		}
		for _, tc := range resp.ToolCalls {
			history = append(history, types.Message{Role: "tool", ToolUseID: tc.ID, Content: calc.run(tc.Input), Turn: 1})
		}
	}
	t.Fatal("model did not finish within 4 turns")
	return blocks
}

func TestLiveComplete_ResponsesAPI(t *testing.T) {
	for _, model := range []string{liveGPT55, liveSonnet55} {
		t.Run(model+"/text", func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.API = APIResponses
			c, err := NewClient(cfg)
			require.NoError(t, err)
			var streamed strings.Builder
			resp, err := c.ChatStream(context.Background(), say("Reply with the single word OK."), nil, func(s string) { streamed.WriteString(s) })
			require.NoError(t, err)
			logFacts(t, resp)
			assert.Contains(t, strings.ToUpper(resp.Content), "OK")
			assert.Equal(t, resp.Content, streamed.String(), "streamed text equals the final text")
			assert.Equal(t, SurfaceResponses, resp.Metadata[AttrEndpointSurface])
			assert.Greater(t, resp.Usage.CostUSD, 0.0, "the gateway reports cost on response.completed")
			assert.Equal(t, "false", resp.Metadata[AttrFallbackUsed])
		})
		t.Run(model+"/tool-loop-with-reasoning", func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.API = APIResponses
			cfg.ThinkingLevel = "medium"
			cfg.MaxTokens = 8192
			c, err := NewClient(cfg)
			require.NoError(t, err)
			blocks := toolLoop(t, context.Background(), c)
			t.Logf("%s: %d reasoning blocks carried and replayed", model, blocks)
		})
		t.Run(model+"/reasoning-replay", func(t *testing.T) {
			// GPT-5.5 did no reasoning on the add loop even at medium effort
			// (2026-11-19, reasoning_tokens 0), so replay is exercised on a
			// problem that needs thought.
			cfg := liveConfig(t, model)
			cfg.API = APIResponses
			cfg.ThinkingLevel = "high"
			cfg.MaxTokens = 16000
			c, err := NewClient(cfg)
			require.NoError(t, err)
			reasoningReplay(t, c, c)
		})
	}
}

func byokConfig(t *testing.T, envVar string) Config {
	t.Helper()
	cfg := liveConfig(t, liveSonnet55)
	if os.Getenv(envBYOKKey) == "" {
		t.Skip(envBYOKKey + " not set")
	}
	cfg.Routing = &Routing{
		Only: []string{"anthropic"},
		BYOK: []BYOKCredential{{Provider: "anthropic", Env: map[string]string{"apiKey": envVar}}},
	}
	return cfg
}

func TestLiveComplete_BYOK(t *testing.T) {
	t.Run("request-scoped key is sent upstream", func(t *testing.T) {
		cfg := byokConfig(t, envBYOKKey)
		cfg.Routing.ProviderTimeoutsMS = map[string]int{"anthropic": 20000}
		c, err := NewClient(cfg)
		require.NoError(t, err)
		resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
		require.NoError(t, err)
		logFacts(t, resp)
		attempts, _ := resp.Metadata[AttrAttempts].(string)
		require.True(t, strings.HasPrefix(attempts, "anthropic/byok/"), "the first attempt used the request's key: %s", attempts)
		if resp.Metadata[AttrCredentialType] != "byok" {
			// 2026-11-19: Anthropic answered 400 "credit balance is too low"
			// for the test key, and the gateway fell back to its own key.
			t.Logf("BYOK key reached the upstream but did not serve: %s", attempts)
		}
	})
	t.Run("a rejected key falls back or fails cleanly", func(t *testing.T) {
		t.Setenv("LOOM_LIVE_BAD_BYOK", "sk-ant-api03-not-a-real-key-for-loom-live-test")
		cfg := byokConfig(t, "LOOM_LIVE_BAD_BYOK")
		c, err := NewClient(cfg)
		require.NoError(t, err)
		resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
		if err != nil {
			// Teams that disable fallback to system credentials get the error.
			t.Logf("bad BYOK key, no fallback: %v", err)
			assert.NotContains(t, err.Error(), "not-a-real-key", "the key is not echoed")
			return
		}
		logFacts(t, resp)
		assert.Equal(t, "system", resp.Metadata[AttrCredentialType], "the gateway fell back to its own key")
	})
}

func TestLiveComplete_SessionAffinity(t *testing.T) {
	for _, model := range []string{liveGPT55, liveSonnet55} {
		t.Run(model, func(t *testing.T) {
			c, err := NewClient(liveConfig(t, model))
			require.NoError(t, err)
			ctx := session.WithSessionID(context.Background(), "loom-live-affinity-"+model)
			resp, err := c.Chat(ctx, say("Reply OK."), nil)
			require.NoError(t, err)
			logFacts(t, resp)
			assert.Equal(t, "explicit", resp.Metadata[AttrSessionSource], "the gateway uses the session id loom sent")

			off := false
			cfg := liveConfig(t, model)
			cfg.Routing = &Routing{SessionAffinity: &off}
			c, err = NewClient(cfg)
			require.NoError(t, err)
			resp, err = c.Chat(ctx, say("Reply OK."), nil)
			require.NoError(t, err, "sessionAffinity:false is accepted")
			logFacts(t, resp)
		})
	}
}

// Every remaining providerOptions.gateway key in the documented shape is
// accepted on each API (an unknown or malformed key would be a 400).
func TestLiveComplete_OtherRoutingKeysAccepted(t *testing.T) {
	routing := &Routing{
		DisallowPromptTraining: true,
		InferenceRegion:        &InferenceRegion{Scope: RegionScopeZone, GeoRegion: "us"},
		Headquarters:           []string{"US"},
		User:                   "loom-live-test",
		Tags:                   []string{"env:loom-live"},
	}
	for _, tc := range []struct{ model, api string }{
		{liveSonnet55, APIAuto}, {liveGPT55, APIAuto}, {liveGPT55, APIResponses},
	} {
		t.Run(tc.model+"/"+tc.api, func(t *testing.T) {
			cfg := liveConfig(t, tc.model)
			cfg.API = tc.api
			cfg.Routing = routing
			c, err := NewClient(cfg)
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
			require.NoError(t, err)
			logFacts(t, resp)
		})
	}
}

// anthropic-beta: loom sends none to the gateway (NoBetaHeaders). This
// records whether the gateway accepts one, for the reference doc.
func TestLiveComplete_AnthropicBetaHeader(t *testing.T) {
	cfg := liveConfig(t, liveSonnet55)
	base, err := NormalizeEndpoint(cfg.Endpoint)
	require.NoError(t, err)
	body := `{"model":"` + liveSonnet55 + `","max_tokens":16,"messages":[{"role":"user","content":"Reply OK."}]}`
	for _, beta := range []string{"interleaved-thinking-2025-05-14", "no-such-beta-2020-01-01"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+MessagesPath, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", beta)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		t.Logf("anthropic-beta %q: status %d: %s", beta, resp.StatusCode, firstLine(raw))
	}
}

// reasoningPrompt needs thought; adaptive thinking skipped a trivial sum.
const reasoningPrompt = "A warehouse holds 1,247 crates. Each day 3/7 of the crates present at the start of the day " +
	"ship, rounded down, and 58 arrive. Work out the count after day 1 carefully, then use the add tool " +
	"to add 1000 to that count, and state the final number."

// reasoningReplay asks first for a turn that both reasons and calls the tool
// (up to three tries), then sends that turn back, reasoning included, to
// second and requires a final answer. It returns the second response.
func reasoningReplay(t *testing.T, first, second *Client) *types.LLMResponse {
	t.Helper()
	calc := &liveTool{name: "add", desc: "Add two integers and return the sum."}
	history := say(reasoningPrompt)
	var resp *types.LLMResponse
	var err error
	for try := 0; try < 3; try++ {
		resp, err = first.Chat(context.Background(), history, []shuttle.Tool{calc})
		require.NoError(t, err)
		logFacts(t, resp)
		if len(resp.ToolCalls) > 0 && len(resp.ThinkingBlocks) > 0 {
			break
		}
	}
	require.NotEmpty(t, resp.ToolCalls, "first turn calls the tool")
	require.NotEmpty(t, resp.ThinkingBlocks, "first turn reasons")
	t.Logf("first turn: %d reasoning blocks, %d tool calls", len(resp.ThinkingBlocks), len(resp.ToolCalls))
	history = append(history, types.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls, ThinkingBlocks: resp.ThinkingBlocks, Turn: 1})
	for _, tc := range resp.ToolCalls {
		history = append(history, types.Message{Role: "tool", ToolUseID: tc.ID, Content: calc.run(tc.Input), Turn: 1})
	}
	resp, err = second.Chat(context.Background(), history, []shuttle.Tool{calc})
	require.NoError(t, err, "the replayed reasoning is accepted")
	logFacts(t, resp)
	return resp
}

// A thinking turn served by one upstream is replayed to another: the first
// turn pinned to anthropic, the second to bedrock (then vertex if bedrock
// does not serve the model).
func TestLiveComplete_ThinkingReplayAcrossUpstreams(t *testing.T) {
	cfg := liveConfig(t, liveSonnet55)
	cfg.ThinkingLevel = "high"
	cfg.MaxTokens = 16000
	cfg.Routing = &Routing{Only: []string{"anthropic"}}
	c1, err := NewClient(cfg)
	require.NoError(t, err)
	for _, upstream := range []string{"bedrock", "vertex"} {
		next := cfg
		next.Routing = &Routing{Only: []string{upstream}}
		c2, err := NewClient(next)
		require.NoError(t, err)
		resp := reasoningReplay(t, c1, c2)
		t.Logf("replay to %s: final provider %v, %d tool calls", upstream, resp.Metadata[AttrFinalProvider], len(resp.ToolCalls))
		final, _ := resp.Metadata[AttrFinalProvider].(string)
		assert.True(t, strings.HasPrefix(final, upstream), "served by %s (vertex reports vertexAnthropic): got %q", upstream, final)
	}
}

// Cache usage fields when each upstream serves a Claude call: the second of
// two identical calls with a long system prompt should read from cache.
func TestLiveComplete_CacheUsageByUpstream(t *testing.T) {
	long := strings.Repeat("Reference material for the assistant. Each paragraph repeats so the prompt passes the cache minimum. ", 220)
	msgs := []types.Message{{Role: "system", Content: long, Turn: 1}, {Role: "user", Content: "Reply OK.", Turn: 1}}
	for _, upstream := range []string{"anthropic", "bedrock", "vertex"} {
		t.Run(upstream, func(t *testing.T) {
			cfg := liveConfig(t, liveSonnet55)
			// The bare client places no cache breakpoints (the agent does);
			// the gateway's automatic caching places them here.
			cfg.Routing = &Routing{Only: []string{upstream}, Caching: CachingAuto}
			c, err := NewClient(cfg)
			require.NoError(t, err)
			var last *types.LLMResponse
			for i := 0; i < 2; i++ {
				resp, err := c.Chat(context.Background(), msgs, nil)
				if err != nil {
					t.Skipf("%s does not serve %s: %v", upstream, liveSonnet55, err)
				}
				t.Logf("%s call %d: input %d, cache read %d, cache write %d, provider %v", upstream, i+1,
					resp.Usage.InputTokens, resp.Usage.CacheReadInputTokens, resp.Usage.CacheCreationInputTokens, resp.Metadata[AttrFinalProvider])
				last = resp
			}
			assert.Greater(t, last.Usage.CacheReadInputTokens+last.Usage.CacheCreationInputTokens, 0,
				"%s reports cache usage", upstream)
		})
	}
}

// Tier semantics: is a tier's rate applied to the whole prompt or only to the
// tokens above its threshold? Priced against the gateway's own cost, on one
// cheap model per family whose catalog row has tiers (alibaba/qwen3-max-thinking,
// first tried, answered 404 "Model not exist" upstream on 2026-11-19).
func TestLiveComplete_PriceTierSemantics(t *testing.T) {
	for _, tc := range []struct {
		model string
		reps  int // ~14 tokens each
	}{
		{"alibaba/qwen3.7-flash", 3000},      // boundary 32,000
		{"anthropic/claude-haiku-5.5", 7600}, // boundary 100,001
	} {
		t.Run(tc.model, func(t *testing.T) {
			cfg := liveConfig(t, tc.model)
			base, err := NormalizeEndpoint(cfg.Endpoint)
			require.NoError(t, err)
			t.Cleanup(func() { catalog.Register(nil) })
			_, err = RegisterCatalog(context.Background(), base, time.Hour, nil)
			require.NoError(t, err)
			info := catalog.Lookup(CatalogProvider, tc.model)
			if info == nil || len(info.GetPriceTiers()) < 2 {
				t.Skipf("%s has no tiers in the catalog now", tc.model)
			}
			filler := strings.Repeat("The quick brown fox jumps over the lazy dog near the riverbank today. ", tc.reps)
			cfg.MaxTokens = 64
			c, err := NewClient(cfg)
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: filler + "\nReply OK.", Turn: 1}}, nil)
			require.NoError(t, err)
			logFacts(t, resp)

			prompt := resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
			out := resp.Usage.OutputTokens
			var whole, marginal float64
			for _, tier := range info.GetPriceTiers() {
				lo, hi := tier.GetMinPromptTokens(), tier.GetMaxPromptTokens()
				if int64(prompt) >= lo && (hi == 0 || int64(prompt) < hi) {
					whole = (float64(prompt)*tier.GetInputPer_1MUsd() + float64(out)*tier.GetOutputPer_1MUsd()) / 1e6
				}
				if hi == 0 || hi > int64(prompt) {
					hi = int64(prompt)
				}
				if hi > lo {
					marginal += float64(hi-lo) * tier.GetInputPer_1MUsd() / 1e6
				}
			}
			flatIn, flatOut := info.GetCostPer_1MInputUsd(), info.GetCostPer_1MOutputUsd()
			flat := (float64(prompt)*flatIn + float64(out)*flatOut) / 1e6
			reported := resp.Usage.CostUSD
			t.Logf("%s prompt %d, output %d: gateway $%.6f | whole-prompt tier $%.6f | marginal input $%.6f | flat $%.6f",
				tc.model, prompt, out, reported, whole, marginal, flat)
			require.Positive(t, reported, "the gateway reports cost")
			assert.InEpsilon(t, reported, whole, 0.05, "the tier containing the prompt prices the whole request")
		})
	}
}

// OIDC: a Vercel project token (from "vercel env pull") authenticates the
// health check and a model call on each API, with no API key. The token is
// read from VERCEL_OIDC_TOKEN, or from the dotenv file named by
// VERCEL_GATEWAY_LIVE_OIDC_ENV. It is never logged.
func TestLiveComplete_OIDC(t *testing.T) {
	if os.Getenv("VERCEL_GATEWAY_LIVE") != "1" {
		t.Skip("VERCEL_GATEWAY_LIVE not set")
	}
	token := os.Getenv("VERCEL_OIDC_TOKEN")
	if f := os.Getenv("VERCEL_GATEWAY_LIVE_OIDC_ENV"); token == "" && f != "" {
		raw, err := os.ReadFile(f) // #nosec G304 -- test-only path from the environment
		require.NoError(t, err)
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERCEL_OIDC_TOKEN="); ok {
				token = strings.Trim(v, `"'`)
			}
		}
	}
	if token == "" {
		t.Skip("no Vercel OIDC token (VERCEL_OIDC_TOKEN or VERCEL_GATEWAY_LIVE_OIDC_ENV)")
	}
	t.Setenv("VERCEL_OIDC_TOKEN", token)
	// No key anywhere: the key would win over OIDC.
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvLoomAPIKey, "")

	for _, tc := range []struct{ model, api string }{
		{liveSonnet55, APIAuto}, {liveGPT55, APIAuto}, {liveGPT55, APIResponses},
	} {
		t.Run(tc.model+"/"+tc.api, func(t *testing.T) {
			c, err := NewClient(Config{
				Endpoint: os.Getenv("VERCEL_GATEWAY_LIVE_ENDPOINT"),
				Model:    tc.model,
				API:      tc.api,
				OIDC:     &OIDCConfig{},
				Timeout:  3 * time.Minute,
			})
			require.NoError(t, err)
			require.NoError(t, c.HealthCheck(context.Background()), "GET /v1/credits with the OIDC token")
			resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
			require.NoError(t, err)
			logFacts(t, resp)
			assert.NotEmpty(t, resp.Content)
		})
	}
}

// A long stream that keeps producing bytes is not cut off: by the gateway's
// own limits or by loom's stream timeouts. Opt-in (VERCEL_GATEWAY_LIVE_LONG=1)
// because it costs about $1.
func TestLiveComplete_LongStream(t *testing.T) {
	if os.Getenv("VERCEL_GATEWAY_LIVE_LONG") != "1" {
		t.Skip("VERCEL_GATEWAY_LIVE_LONG not set")
	}
	cfg := liveConfig(t, liveSonnet55)
	cfg.MaxTokens = 48000
	c, err := NewClient(cfg)
	require.NoError(t, err)
	started := time.Now()
	resp, err := c.ChatStream(context.Background(),
		say("Write a detailed, chapter-by-chapter history of the printing press, at least 30,000 words. Do not stop early."), nil, nil)
	require.NoError(t, err)
	logFacts(t, resp)
	t.Logf("long stream: %s, %d output tokens, stop %s, cost $%.4f", time.Since(started).Round(time.Second), resp.Usage.OutputTokens, resp.StopReason, resp.Usage.CostUSD)
}

func firstLine(b []byte) string {
	b = bytes.TrimSpace(b)
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != nil {
		out, _ := json.Marshal(e.Error)
		return string(out)
	}
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}
