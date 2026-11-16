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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Live tests against the real Vercel AI Gateway. They run only when
// VERCEL_GATEWAY_LIVE=1 and a key is available (AI_GATEWAY_API_KEY or
// LOOM_LLM_VERCEL_GATEWAY_API_KEY). Models default to ones the public catalog
// listed on 2026-11-13; override with VERCEL_GATEWAY_LIVE_CLAUDE /
// VERCEL_GATEWAY_LIVE_OTHER. VERCEL_GATEWAY_LIVE_ENDPOINT selects a regional
// host.
//
// Set VERCEL_GATEWAY_LIVE_CAPTURE_DIR to write every request/response pair
// (credentials redacted) for docs/research/vercel-ai-gateway-spike.md.
//
//	VERCEL_GATEWAY_LIVE=1 go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/vercelgateway/ -v

func liveConfig(t *testing.T, model string) Config {
	t.Helper()
	if os.Getenv("VERCEL_GATEWAY_LIVE") != "1" {
		t.Skip("VERCEL_GATEWAY_LIVE not set")
	}
	key := ResolveAPIKey("")
	if key == "" {
		t.Skip("no gateway API key in the environment")
	}
	return Config{
		Endpoint:      os.Getenv("VERCEL_GATEWAY_LIVE_ENDPOINT"),
		APIKey:        key,
		Model:         model,
		Timeout:       3 * time.Minute,
		MaxTokens:     2048,
		WrapTransport: captureTransport(t),
	}
}

func liveClaude() string {
	if m := os.Getenv("VERCEL_GATEWAY_LIVE_CLAUDE"); m != "" {
		return m
	}
	return "anthropic/claude-haiku-4.5"
}

func liveOther() string {
	if m := os.Getenv("VERCEL_GATEWAY_LIVE_OTHER"); m != "" {
		return m
	}
	return "openai/gpt-5-mini"
}

func liveModels() []string { return []string{liveClaude(), liveOther()} }

func say(text string) []types.Message { return []types.Message{{Role: "user", Content: text, Turn: 1}} }

func logFacts(t *testing.T, resp *types.LLMResponse) {
	t.Helper()
	t.Logf("tokens in=%d out=%d cache_read=%d cache_write=%d cost_usd=%.6f stop=%s",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadInputTokens,
		resp.Usage.CacheCreationInputTokens, resp.Usage.CostUSD, resp.StopReason)
	for k, v := range resp.Metadata {
		if strings.HasPrefix(k, "gateway.") || k == llm.MetadataCostUnpriced {
			t.Logf("  %s = %v", k, v)
		}
	}
}

// assertGatewayFacts checks what the gateway reports on every call (observed
// 2026-11-16): input tokens, its own cost (not an estimate), the generation
// id and the upstream that served the call.
func assertGatewayFacts(t *testing.T, resp *types.LLMResponse) {
	t.Helper()
	assert.Positive(t, resp.Usage.InputTokens+resp.Usage.CacheReadInputTokens+resp.Usage.CacheCreationInputTokens, "input tokens")
	assert.Positive(t, resp.Usage.CostUSD, "gateway-reported cost")
	_, unpriced := resp.Metadata[llm.MetadataCostUnpriced]
	assert.False(t, unpriced, "the gateway reported a cost")
	assert.NotEmpty(t, resp.Metadata[AttrGenerationID])
	assert.NotEmpty(t, resp.Metadata[AttrFinalProvider])
	assert.Equal(t, "false", resp.Metadata[AttrFallbackUsed], "no fallback was configured")
}

func TestLive_HealthCheckAndCredits(t *testing.T) {
	c, err := NewClient(liveConfig(t, liveOther()))
	require.NoError(t, err)
	require.NoError(t, c.HealthCheck(context.Background()))
	cr, err := c.Credits(context.Background())
	require.NoError(t, err)
	t.Logf("credits: balance=%s total_used=%s", cr.Balance, cr.TotalUsed)
}

func TestLive_BadKeyFailsHealthCheck(t *testing.T) {
	cfg := liveConfig(t, liveOther())
	cfg.APIKey = "vck_definitely_invalid_key"
	c, err := NewClient(cfg)
	require.NoError(t, err)
	err = c.HealthCheck(context.Background())
	require.Error(t, err)
	t.Logf("bad key: %v", err)
	assert.True(t, llm.IsDeterministicClientError(err))
}

func TestLive_Catalog(t *testing.T) {
	_ = liveConfig(t, liveOther())
	src, err := NewCatalogSource(os.Getenv("VERCEL_GATEWAY_LIVE_ENDPOINT"), nil)
	require.NoError(t, err)
	n, err := src.Refresh(context.Background())
	require.NoError(t, err)
	assert.Greater(t, n, 10)
	for _, id := range liveModels() {
		info := src.Lookup(context.Background(), CatalogProvider, id)
		require.NotNil(t, info, "%s must be in the live catalog", id)
		t.Logf("%s: ctx=%d max_out=%d in=$%.4f/M out=$%.4f/M cache_read=$%.4f/M cache_write=$%.4f/M reasoning=%v",
			id, info.ContextWindow, info.MaxOutputTokens, info.CostPer_1MInputUsd, info.CostPer_1MOutputUsd,
			info.CostPer_1MCacheReadUsd, info.CostPer_1MCacheWriteUsd, info.IsReasoning)
		assert.Positive(t, info.CostPer_1MInputUsd, "pricing unit check: per-token × 1e6 should be a sane per-1M rate")
		assert.Less(t, info.CostPer_1MInputUsd, 100.0, "a per-1M rate above $100 means the unit is not USD per token")
	}
}

func TestLive_ChatAndStream(t *testing.T) {
	for _, model := range liveModels() {
		t.Run(model, func(t *testing.T) {
			c, err := NewClient(liveConfig(t, model))
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), say("Reply with the single word OK."), nil)
			require.NoError(t, err)
			assert.Contains(t, strings.ToUpper(resp.Content), "OK")
			logFacts(t, resp)
			assertGatewayFacts(t, resp)

			var streamed strings.Builder
			resp, err = c.ChatStream(context.Background(), say("Count from 1 to 5, comma separated."), nil,
				func(tok string) { streamed.WriteString(tok) })
			require.NoError(t, err)
			assert.Equal(t, resp.Content, streamed.String())
			assert.Contains(t, resp.Content, "3")
			assert.Positive(t, resp.Usage.OutputTokens, "stream reports usage")
			logFacts(t, resp)
			assertGatewayFacts(t, resp)
		})
	}
}

// A tool loop of three model turns with thinking on: Claude's signed thinking
// blocks must replay on the Messages surface; the non-Claude reasoning model
// exercises reasoning_details on the Chat surface.
func TestLive_ToolLoopWithThinking(t *testing.T) {
	calc := &liveTool{name: "add", desc: "Add two integers and return the sum."}
	for _, model := range liveModels() {
		t.Run(model, func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.ThinkingLevel = "low"
			cfg.MaxTokens = 4096
			c, err := NewClient(cfg)
			require.NoError(t, err)

			history := say("Use the add tool to compute 2+3, then use it again to add 10 to that result. Then state the final number.")
			for turn := 0; turn < 4; turn++ {
				resp, err := c.ChatStream(context.Background(), history, []shuttle.Tool{calc}, nil)
				require.NoError(t, err, "turn %d", turn)
				logFacts(t, resp)
				t.Logf("turn %d: %d thinking blocks, %d tool calls", turn, len(resp.ThinkingBlocks), len(resp.ToolCalls))
				history = append(history, types.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls,
					ThinkingBlocks: resp.ThinkingBlocks, Turn: 1})
				if len(resp.ToolCalls) == 0 {
					assert.Contains(t, resp.Content, "15")
					return
				}
				for _, tc := range resp.ToolCalls {
					history = append(history, types.Message{Role: "tool", ToolUseID: tc.ID,
						Content: calc.run(tc.Input), Turn: 1})
				}
			}
			t.Fatal("model did not finish within 4 turns")
		})
	}
}

func TestLive_RoutingOptions(t *testing.T) {
	for _, model := range liveModels() {
		t.Run(model+"/unsatisfiable-only", func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.Routing = &Routing{Only: []string{"no-such-provider"}}
			c, err := NewClient(cfg)
			require.NoError(t, err)
			_, err = c.Chat(context.Background(), say("hi"), nil)
			require.Error(t, err)
			t.Logf("unsatisfiable only: %v", err)
			assert.True(t, llm.IsDeterministicClientError(err), "a failed only filter is terminal")
		})
		t.Run(model+"/zero-data-retention", func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.Routing = &Routing{ZeroDataRetention: true}
			c, err := NewClient(cfg)
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
			if err != nil {
				t.Logf("zeroDataRetention: true rejected: %v", err)
			}
			require.NoError(t, err, "the gateway must accept zeroDataRetention: true")
			logFacts(t, resp)
		})
		t.Run(model+"/sort-and-caching", func(t *testing.T) {
			cfg := liveConfig(t, model)
			cfg.Routing = &Routing{Sort: SortTTFT, Caching: CachingAuto}
			c, err := NewClient(cfg)
			require.NoError(t, err)
			resp, err := c.Chat(context.Background(), say("Reply OK."), nil)
			require.NoError(t, err)
			logFacts(t, resp)
		})
	}
}

// A cached Messages stream must not count cached tokens as input too: the
// prompt (input + cache read + cache write) is the same size on both calls.
func TestLive_CachedStreamTokenAccounting(t *testing.T) {
	cfg := liveConfig(t, liveClaude())
	// The gateway places the cache breakpoint itself.
	cfg.Routing = &Routing{Caching: CachingAuto}
	c, err := NewClient(cfg)
	require.NoError(t, err)
	// Long enough to clear Haiku's minimum cacheable prefix.
	sys := strings.Repeat("Reference text for the cache test. Each sentence adds tokens to the prefix. ", 400)
	msgs := []types.Message{{Role: "system", Content: sys}, {Role: "user", Content: "Reply OK.", Turn: 1}}
	var total [2]int
	for i := 0; i < 2; i++ {
		resp, err := c.ChatStream(context.Background(), msgs, nil, nil)
		require.NoError(t, err)
		logFacts(t, resp)
		u := resp.Usage
		total[i] = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		if i == 1 {
			t.Logf("call 2: cache_read=%d (0 means the upstream did not cache)", u.CacheReadInputTokens)
		}
	}
	assert.InDelta(t, total[0], total[1], float64(total[0])*0.05, "prompt size must not change between identical calls (no double count)")
}

func TestLive_UnknownModel(t *testing.T) {
	c, err := NewClient(liveConfig(t, "acme/does-not-exist-model"))
	require.NoError(t, err)
	_, err = c.Chat(context.Background(), say("hi"), nil)
	require.Error(t, err)
	t.Logf("unknown model: %v", err)
	assert.True(t, llm.IsDeterministicClientError(err))
	assert.False(t, errors.Is(err, ErrCreditsExhausted))
}

// liveTool is a deterministic add tool.
type liveTool struct{ name, desc string }

func (l *liveTool) Name() string        { return l.name }
func (l *liveTool) Description() string { return l.desc }
func (l *liveTool) Backend() string     { return "" }
func (l *liveTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema("two integers", map[string]*shuttle.JSONSchema{
		"a": shuttle.NewNumberSchema("first"),
		"b": shuttle.NewNumberSchema("second"),
	}, []string{"a", "b"})
}
func (l *liveTool) Execute(_ context.Context, in map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: l.run(in)}, nil
}

func (l *liveTool) run(in map[string]interface{}) string {
	a, _ := in["a"].(float64)
	b, _ := in["b"].(float64)
	return fmt.Sprintf("%g", a+b)
}

// captureTransport writes each exchange to VERCEL_GATEWAY_LIVE_CAPTURE_DIR
// with Authorization and x-api-key redacted. Nil when the variable is unset.
func captureTransport(t *testing.T) func(http.RoundTripper) http.RoundTripper {
	dir := os.Getenv("VERCEL_GATEWAY_LIVE_CAPTURE_DIR")
	if dir == "" {
		return nil
	}
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var seq atomic.Int64
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return func(base http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var reqBody []byte
			if r.Body != nil {
				reqBody, _ = io.ReadAll(r.Body)
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			resp, err := base.RoundTrip(r)
			n := seq.Add(1)
			var buf bytes.Buffer
			fmt.Fprintf(&buf, "%s %s\n", r.Method, r.URL.Path)
			for k, v := range r.Header {
				if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "x-api-key") {
					v = []string{"[REDACTED]"}
				}
				fmt.Fprintf(&buf, "%s: %s\n", k, strings.Join(v, ","))
			}
			fmt.Fprintf(&buf, "\n%s\n\n--- response ---\n", reqBody)
			if err != nil {
				fmt.Fprintf(&buf, "transport error: %v\n", err)
			} else {
				respBody, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
				fmt.Fprintf(&buf, "%d\n", resp.StatusCode)
				for k, v := range resp.Header {
					fmt.Fprintf(&buf, "%s: %s\n", k, strings.Join(v, ","))
				}
				fmt.Fprintf(&buf, "\n%s\n", respBody)
			}
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%03d.txt", name, n)), buf.Bytes(), 0o600)
			return resp, err
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
