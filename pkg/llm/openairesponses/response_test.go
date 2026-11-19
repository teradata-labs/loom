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

package openairesponses

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func chatOnce(t *testing.T, cfg Config, status int, body string) (*llmtypes.LLMResponse, error) {
	t.Helper()
	rec := newRecorder(t, jsonHandler(status, body))
	cfg.APIKey = testAPIKey
	cfg.Endpoint = rec.srv.URL
	return NewClient(cfg).Chat(context.Background(),
		[]llmtypes.Message{{Role: "user", Content: "x"}}, []shuttle.Tool{weatherTool()})
}

func TestBlockingParseFixtures(t *testing.T) {
	t.Parallel()
	t.Run("function_call (gpt-tool.json)", func(t *testing.T) {
		t.Parallel()
		resp, err := chatOnce(t, Config{Model: "openai/gpt-5.5"}, http.StatusOK, readFixture(t, "gpt-tool.json"))
		require.NoError(t, err, "chat")
		assert.Equal(t, "tool_use", resp.StopReason, "stop reason")
		require.Len(t, resp.ToolCalls, 1, "one tool call")
		tc := resp.ToolCalls[0]
		assert.Equal(t, "call_97aBo6D1c0RiEEqpybaOlOFT", tc.ID, "call_id is the loom tool call id")
		assert.Equal(t, "get_weather", tc.Name, "a name the request did not sanitize passes through")
		assert.Equal(t, map[string]interface{}{"location": "Paris"}, tc.Input, "arguments parsed")
		assert.Empty(t, resp.Content, "no text")
		assert.Equal(t, 50, resp.Usage.InputTokens, "input tokens")
		assert.Equal(t, 18, resp.Usage.OutputTokens, "output tokens")
		assert.Equal(t, 68, resp.Usage.TotalTokens, "total tokens")
		assert.Equal(t, 68, resp.Usage.RateLimitTokens, "raw total for capacity accounting")
		assert.Equal(t, "gen_01M7TM7ZY56Y7ZSVBZ4QQYWFEK", resp.Metadata["response_id"], "response id in metadata")
		assert.Equal(t, "openai/gpt-5.5", resp.Metadata["model"], "model in metadata")
	})
	t.Run("message (sonnet.json)", func(t *testing.T) {
		t.Parallel()
		resp, err := chatOnce(t, Config{Model: "anthropic/claude-sonnet-5.5"}, http.StatusOK, readFixture(t, "sonnet.json"))
		require.NoError(t, err, "chat")
		assert.Equal(t, "end_turn", resp.StopReason, "stop reason")
		assert.Equal(t, "Hello, lovely human!", resp.Content, "text")
		assert.Empty(t, resp.ToolCalls, "no tool calls")
		assert.Equal(t, 16, resp.Usage.InputTokens, "input tokens")
		assert.Equal(t, 11, resp.Usage.OutputTokens, "output tokens")
	})
}

func TestBlockingParseCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, resp *llmtypes.LLMResponse)
	}{
		{
			name: "reasoning then text",
			body: `{"status":"completed","output":[
				{"type":"reasoning","id":"rs_1","encrypted_content":"enc","summary":[{"type":"summary_text","text":"first"},{"type":"summary_text","text":"second"}]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "answer", resp.Content, "text")
				assert.Equal(t, "first\n\nsecond", resp.Thinking, "summary text goes to Thinking")
				assert.Equal(t, []llmtypes.ThinkingBlock{
					{Type: blockTypeReasoning, Format: "rs_1", Signature: "enc", Thinking: "first"},
					{Type: blockTypeReasoningSummary, Thinking: "second"},
				}, resp.ThinkingBlocks, "reasoning kept for replay")
				assert.Equal(t, "end_turn", resp.StopReason, "stop reason")
			},
		},
		{
			name: "refusal",
			body: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "refusal", resp.StopReason, "pkg/agent isRefusal reads \"refusal\"")
				assert.Equal(t, "I can't help with that.", resp.Content, "refusal text kept as the reply")
			},
		},
		{
			name: "incomplete max_output_tokens",
			body: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "max_tokens", resp.StopReason, "stop reason")
				assert.Equal(t, "partial", resp.Content, "partial text")
				assert.Equal(t, "max_output_tokens", resp.Metadata["incomplete_reason"], "reason in metadata")
			},
		},
		{
			name: "incomplete content_filter",
			body: `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "content_filter", resp.StopReason, "stop reason")
			},
		},
		{
			name: "incomplete other reason passes through",
			body: `{"status":"incomplete","incomplete_details":{"reason":"something_new"},"output":[]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "something_new", resp.StopReason, "unknown reason kept")
			},
		},
		{
			name: "incomplete without details",
			body: `{"status":"incomplete","output":[]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "max_tokens", resp.StopReason, "default incomplete reason")
			},
		},
		{
			name: "bad arguments and incomplete call",
			body: `{"status":"completed","output":[
				{"type":"function_call","call_id":"c1","name":"weather_get_weather","arguments":"not json"},
				{"type":"function_call","call_id":"","name":"weather_get_weather","arguments":"{}"},
				{"type":"function_call","call_id":"c3","name":"other","arguments":""}]}`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				require.Len(t, resp.ToolCalls, 2, "call without call_id dropped")
				assert.Equal(t, map[string]interface{}{"_raw": "not json"}, resp.ToolCalls[0].Input, "raw arguments kept")
				assert.Equal(t, "weather:get_weather", resp.ToolCalls[0].Name, "sanitized name reversed to the original")
				assert.Equal(t, "other", resp.ToolCalls[1].Name, "unknown name passes through")
				assert.Equal(t, map[string]interface{}{}, resp.ToolCalls[1].Input, "empty arguments → empty object")
				assert.Equal(t, "tool_use", resp.StopReason, "stop reason")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := chatOnce(t, Config{Model: "m"}, http.StatusOK, tt.body)
			require.NoError(t, err, "chat")
			tt.check(t, resp)
		})
	}
}

func TestBlockingFailedResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCtx    bool
		wantText   string
	}{
		{"server error", `{"status":"failed","error":{"code":"server_error","message":"upstream broke"}}`, 500, false, "upstream broke"},
		{"context length", `{"status":"failed","error":{"code":"context_length_exceeded","message":"too long"}}`, 400, true, "too long"},
		{"no error object", `{"status":"failed"}`, 0, false, "without an error object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := chatOnce(t, Config{Model: "m"}, http.StatusOK, tt.body)
			require.Error(t, err, "failed status is an error")
			assert.Contains(t, err.Error(), tt.wantText, "message")
			assert.Equal(t, tt.wantCtx, errors.Is(err, llm.ErrContextTooLong), "context-too-long classification")
			var se *llm.HTTPStatusError
			if tt.wantStatus == 0 {
				assert.False(t, errors.As(err, &se), "no status attached")
				return
			}
			require.True(t, errors.As(err, &se), "status attached")
			assert.Equal(t, tt.wantStatus, se.StatusCode, "status")
		})
	}
}

func TestHTTPErrorBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		body     string
		wantText []string
		wantCtx  bool
	}{
		{"nested error object", 400, `{"error":{"type":"invalid_request_error","message":"bad field"}}`, []string{"status 400", "bad field", "invalid_request_error"}, false},
		{"flat gateway shape", 400, `{"error":"model not found","type":"model_not_found","statusCode":400}`, []string{"model not found", "model_not_found"}, false},
		{"top-level message", 404, `{"message":"no route"}`, []string{"status 404", "no route"}, false},
		{"not json", 502, `<html>bad gateway</html>`, []string{"status 502", "bad gateway"}, false},
		{"context too long code", 400, `{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window"}}`, []string{"context window"}, true},
		{"context too long passthrough wording", 400, `{"error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, []string{"prompt is too long"}, true},
		{"unauthorized", 401, `{"error":{"type":"authentication_error","message":"bad key"}}`, []string{"status 401", "bad key"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, stream := range []bool{false, true} {
				rec := newRecorder(t, jsonHandler(tt.status, tt.body))
				c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL})
				msgs := []llmtypes.Message{{Role: "user", Content: "x"}}
				var err error
				if stream {
					_, err = c.ChatStream(context.Background(), msgs, nil, nil)
				} else {
					_, err = c.Chat(context.Background(), msgs, nil)
				}
				require.Error(t, err, "non-2xx is an error (stream=%v)", stream)
				for _, s := range tt.wantText {
					assert.Contains(t, err.Error(), s, "message (stream=%v)", stream)
				}
				assert.NotContains(t, err.Error(), testAPIKey, "no credential in errors")
				var se *llm.HTTPStatusError
				require.True(t, errors.As(err, &se), "status attached (stream=%v)", stream)
				assert.Equal(t, tt.status, se.StatusCode, "status (stream=%v)", stream)
				assert.Equal(t, tt.wantCtx, errors.Is(err, llm.ErrContextTooLong), "context-too-long (stream=%v)", stream)
			}
		})
	}
}

func TestUsageMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		usage string
		want  llmtypes.Usage
	}{
		{
			name:  "cached tokens are inside input_tokens",
			usage: `{"input_tokens":2000,"output_tokens":50,"total_tokens":2050,"input_tokens_details":{"cached_tokens":1536,"cache_write_tokens":100},"output_tokens_details":{"reasoning_tokens":30}}`,
			want:  llmtypes.Usage{InputTokens: 364, OutputTokens: 50, TotalTokens: 414, CacheReadInputTokens: 1536, CacheCreationInputTokens: 100, ThinkingTokens: 30, RateLimitTokens: 2050},
		},
		{
			name:  "no details",
			usage: `{"input_tokens":900,"output_tokens":100}`,
			want:  llmtypes.Usage{InputTokens: 900, OutputTokens: 100, TotalTokens: 1000, RateLimitTokens: 1000},
		},
		{
			name:  "buckets larger than input clamp at zero",
			usage: `{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":500}}`,
			want:  llmtypes.Usage{InputTokens: 0, OutputTokens: 5, TotalTokens: 5, CacheReadInputTokens: 500, RateLimitTokens: 15},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := `{"status":"completed","output":[],"usage":` + tt.usage + `}`
			// An unpriced model keeps CostUSD at 0, so only the buckets differ.
			resp, err := chatOnce(t, Config{Model: "nobody/unpriced-test-model"}, http.StatusOK, body)
			require.NoError(t, err, "chat")
			assert.Equal(t, tt.want, resp.Usage, "usage buckets")
		})
	}
	t.Run("missing usage", func(t *testing.T) {
		t.Parallel()
		resp, err := chatOnce(t, Config{Model: "nobody/unpriced-test-model"}, http.StatusOK, `{"status":"completed","output":[]}`)
		require.NoError(t, err, "chat")
		assert.Zero(t, resp.Usage.TotalTokens, "zero usage")
	})
}

func TestCost(t *testing.T) {
	t.Parallel()
	usage := `{"input_tokens":10000,"output_tokens":1000,"total_tokens":11000,"input_tokens_details":{"cached_tokens":4000}}`
	body := `{"status":"completed","output":[],"usage":` + usage + `}`

	t.Run("catalog estimate", func(t *testing.T) {
		t.Parallel()
		inPerM, outPerM, ok := catalog.RequestPricing("openai", "gpt-4.1", 10000)
		require.True(t, ok, "gpt-4.1 must be priced in the built-in catalog for this test")
		readPerM := inPerM * defaultCacheReadMultiplier
		if r, _ := catalog.CacheRates("openai", "gpt-4.1"); r > 0 {
			readPerM = r
		}
		want := (6000*inPerM + 4000*readPerM + 1000*outPerM) / 1_000_000
		resp, err := chatOnce(t, Config{Model: "gpt-4.1"}, http.StatusOK, body)
		require.NoError(t, err, "chat")
		assert.InDelta(t, want, resp.Usage.CostUSD, 1e-12, "cache-aware estimate")
		assert.NotContains(t, resp.Metadata, llm.MetadataCostUnpriced, "priced call not flagged")
	})
	t.Run("claude family multipliers", func(t *testing.T) {
		t.Parallel()
		model := "anthropic/claude-sonnet-4-5"
		inPerM, outPerM, ok := catalog.RequestPricing("openai", model, 10000)
		require.True(t, ok, "Claude family is always priced")
		want := (6000*inPerM + 4000*inPerM*catalog.ClaudeCacheReadMultiplier(model) + 1000*outPerM) / 1_000_000
		resp, err := chatOnce(t, Config{Model: model}, http.StatusOK, body)
		require.NoError(t, err, "chat")
		assert.InDelta(t, want, resp.Usage.CostUSD, 1e-12, "Claude cache read multiplier")
	})
	t.Run("inspector cost wins and attributes copied", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		inspector := func(_ http.Header, payload []byte) llm.ResponseFacts {
			calls.Add(1)
			if strings.Contains(string(payload), `"cost": "0.00079"`) {
				return llm.ResponseFacts{CostUSD: 0.00079, Attributes: map[string]string{"gateway.final_provider": "openai"}}
			}
			return llm.ResponseFacts{}
		}
		resp, err := chatOnce(t, Config{Model: "nobody/unpriced-test-model", ResponseInspector: inspector},
			http.StatusOK, readFixture(t, "gpt-tool.json"))
		require.NoError(t, err, "chat")
		assert.Equal(t, int32(1), calls.Load(), "inspector sees the blocking body once")
		assert.InDelta(t, 0.00079, resp.Usage.CostUSD, 1e-12, "reported cost")
		assert.Equal(t, "openai", resp.Metadata["gateway.final_provider"], "attributes in metadata")
		assert.NotContains(t, resp.Metadata, llm.MetadataCostUnpriced, "reported cost is not unpriced")
	})
	t.Run("unpriced without ReportUnpriced", func(t *testing.T) {
		t.Parallel()
		resp, err := chatOnce(t, Config{Model: "nobody/unpriced-test-model"}, http.StatusOK, body)
		require.NoError(t, err, "chat")
		assert.Zero(t, resp.Usage.CostUSD, "no guessed rate")
		assert.Equal(t, true, resp.Metadata[llm.MetadataCostUnpriced], "flagged unpriced")
		assert.False(t, resp.Usage.CostEstimated, "not estimated")
	})
	t.Run("unpriced with ReportUnpriced and no catalog", func(t *testing.T) {
		t.Parallel()
		resp, err := chatOnce(t, Config{Model: "nobody/unpriced-test-model", ReportUnpriced: true,
			CatalogProvider: "no-such-catalog-namespace"}, http.StatusOK, body)
		require.NoError(t, err, "chat")
		assert.Zero(t, resp.Usage.CostUSD, "nothing to estimate from")
		assert.Equal(t, true, resp.Metadata[llm.MetadataCostUnpriced], "flagged unpriced")
		assert.NotContains(t, resp.Metadata, llm.MetadataCostEstimated, "no estimate")
	})
	t.Run("unpriced with ReportUnpriced estimates from creator median", func(t *testing.T) {
		t.Parallel()
		inPerM, outPerM, ok := catalog.CreatorMedianPricing("openai", "openai/unpriced-test-model")
		if !ok {
			t.Skip("built-in openai catalog lists no priced model")
		}
		resp, err := chatOnce(t, Config{Model: "openai/unpriced-test-model", ReportUnpriced: true}, http.StatusOK, body)
		require.NoError(t, err, "chat")
		want := (6000*inPerM + 4000*inPerM*defaultCacheReadMultiplier + 1000*outPerM) / 1_000_000
		assert.InDelta(t, want, resp.Usage.CostUSD, 1e-12, "median estimate")
		assert.True(t, resp.Usage.CostEstimated, "estimated")
		assert.Equal(t, true, resp.Metadata[llm.MetadataCostEstimated], "estimated flag")
		assert.Equal(t, true, resp.Metadata[llm.MetadataCostUnpriced], "unpriced flag")
	})
}

func TestBlockingInvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := chatOnce(t, Config{}, http.StatusOK, `not json`)
	require.Error(t, err, "undecodable 200 body is an error")
	assert.Contains(t, err.Error(), "decode response", "message")
}
