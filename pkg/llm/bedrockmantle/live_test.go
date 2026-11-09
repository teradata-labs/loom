// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package bedrockmantle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// Live tests against a real bedrock-mantle endpoint. They run only when
// BEDROCK_MANTLE_LIVE_REGION is set, and need either BEDROCK_MANTLE_API_KEY
// or an AWS credential chain (AWS_PROFILE / SSO / IAM role) that is allowed
// to call bedrock-mantle:CreateInference. The models used were available and
// subscribed in the Teradata Labs account on 2026-11-04; adjust via
// BEDROCK_MANTLE_LIVE_CLAUDE / BEDROCK_MANTLE_LIVE_OPENAI if yours differ.
//
//	BEDROCK_MANTLE_LIVE_REGION=us-west-2 go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/bedrockmantle/ -v

func liveConfig(t *testing.T, model string) Config {
	t.Helper()
	region := os.Getenv("BEDROCK_MANTLE_LIVE_REGION")
	if region == "" {
		t.Skip("BEDROCK_MANTLE_LIVE_REGION not set")
	}
	return Config{
		Region:  region,
		APIKey:  os.Getenv("BEDROCK_MANTLE_API_KEY"),
		Model:   model,
		Timeout: 2 * time.Minute,
	}
}

func liveClaudeModel() string {
	if m := os.Getenv("BEDROCK_MANTLE_LIVE_CLAUDE"); m != "" {
		return m
	}
	return DefaultModel
}

func liveOpenAIModel() string {
	if m := os.Getenv("BEDROCK_MANTLE_LIVE_OPENAI"); m != "" {
		return m
	}
	return "openai.gpt-oss-120b"
}

func TestLive_HealthCheck(t *testing.T) {
	c, err := NewClientForModel(liveConfig(t, liveClaudeModel()))
	require.NoError(t, err)
	require.NoError(t, c.HealthCheck(context.Background()))
}

func TestLive_Claude_Chat(t *testing.T) {
	c, err := NewClientForModel(liveConfig(t, liveClaudeModel()))
	require.NoError(t, err)

	resp, err := c.Chat(context.Background(), userMessage("Reply with the single word OK."), nil)
	require.NoError(t, err)
	assert.Contains(t, strings.ToUpper(resp.Content), "OK")
	assert.Greater(t, resp.Usage.InputTokens, 0)
	assert.Greater(t, resp.Usage.OutputTokens, 0)
	assert.Greater(t, resp.Usage.CostUSD, 0.0, "pricing resolves through the bedrock-mantle catalog namespace")
}

func TestLive_Claude_ChatStream(t *testing.T) {
	c, err := NewClientForModel(liveConfig(t, liveClaudeModel()))
	require.NoError(t, err)

	var tokens int
	resp, err := c.ChatStream(context.Background(), userMessage("Count from 1 to 5, separated by spaces."), nil, func(string) { tokens++ })
	require.NoError(t, err)
	assert.Greater(t, tokens, 0, "token callback must fire")
	assert.Contains(t, resp.Content, "5")
}

func TestLive_Claude_ToolCall(t *testing.T) {
	c, err := NewClientForModel(liveConfig(t, liveClaudeModel()))
	require.NoError(t, err)

	weather := &shuttle.MockTool{
		MockName:        "get_weather",
		MockDescription: "Get the current weather for a city",
		MockSchema: &shuttle.JSONSchema{
			Type:       "object",
			Properties: map[string]*shuttle.JSONSchema{"city": {Type: "string", Description: "City name"}},
			Required:   []string{"city"},
		},
	}
	resp, err := c.Chat(context.Background(), userMessage("What is the weather in Paris right now? Use the get_weather tool."), []shuttle.Tool{weather})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ToolCalls, "Claude on Mantle must emit a tool_use block")
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Paris", resp.ToolCalls[0].Input["city"])
}

func TestLive_Claude_PromptCache(t *testing.T) {
	c, err := NewClientForModel(liveConfig(t, liveClaudeModel()))
	require.NoError(t, err)

	// Haiku 4.5's minimum cacheable prefix is 4096 tokens; ~8k keeps clear of it.
	var sb strings.Builder
	for i := 0; i < 420; i++ {
		sb.WriteString("Clause ")
		sb.WriteString(strings.Repeat("x", i%7+1))
		sb.WriteString(": the quick brown fox jumps over the lazy dog. ")
	}
	msgs := []types.Message{
		{Role: "system", Content: sb.String(), CacheBreakpoint: true},
		{Role: "user", Content: "Reply with the single word OK."},
	}

	first, err := c.Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	second, err := c.Chat(context.Background(), msgs, nil)
	require.NoError(t, err)

	// Without the anthropic-beta header Mantle still honors cache_control:
	// the first call writes the cache, the second reads it.
	assert.Greater(t, first.Usage.CacheCreationInputTokens+first.Usage.CacheReadInputTokens, 0, "first call: %+v", first.Usage)
	assert.Greater(t, second.Usage.CacheReadInputTokens, 0, "second call: %+v", second.Usage)
}

func TestLive_OpenAICompatible_Chat(t *testing.T) {
	cfg := liveConfig(t, liveOpenAIModel())
	cfg.MaxTokens = 400 // gpt-oss spends tokens on reasoning before the answer
	c, err := NewClientForModel(cfg)
	require.NoError(t, err)
	assert.Equal(t, RouteV1, c.Route())

	resp, err := c.Chat(context.Background(), userMessage("Reply with the single word OK."), nil)
	require.NoError(t, err)
	assert.Contains(t, strings.ToUpper(resp.Content), "OK")
	assert.NotEmpty(t, resp.Thinking, `gpt-oss returns its trace in "reasoning"`)
	assert.Greater(t, resp.Usage.InputTokens, 0)
}

func TestLive_OpenAICompatible_ChatStream(t *testing.T) {
	cfg := liveConfig(t, liveOpenAIModel())
	cfg.MaxTokens = 400
	c, err := NewClientForModel(cfg)
	require.NoError(t, err)

	var got strings.Builder
	resp, err := c.ChatStream(context.Background(), userMessage("Reply with the single word OK."), nil, func(tok string) { got.WriteString(tok) })
	require.NoError(t, err, "stream must end cleanly without a [DONE] sentinel")
	assert.Contains(t, strings.ToUpper(got.String()), "OK")
	assert.Greater(t, resp.Usage.OutputTokens, 0, "usage chunk is honored")
}

func TestLive_OpenAICompatible_ToolCall(t *testing.T) {
	cfg := liveConfig(t, liveOpenAIModel())
	cfg.MaxTokens = 400
	c, err := NewClientForModel(cfg)
	require.NoError(t, err)

	weather := &shuttle.MockTool{
		MockName:        "get_weather",
		MockDescription: "Get the current weather for a city",
		MockSchema: &shuttle.JSONSchema{
			Type:       "object",
			Properties: map[string]*shuttle.JSONSchema{"city": {Type: "string"}},
			Required:   []string{"city"},
		},
	}
	resp, err := c.Chat(context.Background(), userMessage("What is the weather in Paris? Use the get_weather tool."), []shuttle.Tool{weather})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ToolCalls)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
}

func TestLive_Gemma4_OpenAIV1Route(t *testing.T) {
	cfg := liveConfig(t, "google.gemma-4-e2b")
	cfg.MaxTokens = 64
	c, err := NewClientForModel(cfg)
	require.NoError(t, err)
	assert.Equal(t, RouteOpenAIV1, c.Route())

	resp, err := c.Chat(context.Background(), userMessage("Reply with the single word OK."), nil)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Content)
	assert.Equal(t, RouteOpenAIV1, c.Route(), "no fallback should have been needed")
}

func TestLive_UnknownVendorFallsOver(t *testing.T) {
	// Pin the fallback path against the real gateway by starting a Grok model
	// on the wrong route on purpose: construct the client, then force /v1.
	cfg := liveConfig(t, "xai.grok-4.6")
	cfg.MaxTokens = 400 // Grok reasons before answering; a tiny budget yields empty content
	c, err := NewClientForModel(cfg)
	require.NoError(t, err)
	c.mu.Lock()
	c.route = RouteV1
	c.inner = c.newOpenAIClient(RouteV1)
	c.mu.Unlock()

	resp, err := c.Chat(context.Background(), userMessage("Reply with the single word OK."), nil)
	if err != nil && strings.Contains(err.Error(), "subscription") {
		t.Skipf("account is not subscribed to xai.grok-4.6: %v", err)
	}
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Content)
	assert.Equal(t, RouteOpenAIV1, c.Route(), "the route error must have moved the client to /openai/v1")
}
