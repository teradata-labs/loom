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
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// gatewayCostInspector mimics a gateway inspector: it reports the cost found
// at response.provider_metadata.gateway.cost and records every payload.
type gatewayCostInspector struct {
	mu       sync.Mutex
	payloads []string
}

func (g *gatewayCostInspector) inspect(_ http.Header, payload []byte) llm.ResponseFacts {
	g.mu.Lock()
	g.payloads = append(g.payloads, string(payload))
	g.mu.Unlock()
	switch {
	case strings.Contains(string(payload), `"cost":"0.002085"`):
		return llm.ResponseFacts{CostUSD: 0.002085, Attributes: map[string]string{"gateway.final_provider": "openai"}}
	case strings.Contains(string(payload), `"cost":"0.0001"`):
		return llm.ResponseFacts{CostUSD: 0.0001}
	}
	return llm.ResponseFacts{}
}

func streamOnce(t *testing.T, cfg Config, stream string, cb llmtypes.TokenCallback) (*llmtypes.LLMResponse, *recorder, error) {
	t.Helper()
	rec := newRecorder(t, sseHandler(stream))
	cfg.APIKey = testAPIKey
	cfg.Endpoint = rec.srv.URL
	resp, err := NewClient(cfg).ChatStream(context.Background(),
		[]llmtypes.Message{{Role: "user", Content: "x"}}, []shuttle.Tool{weatherTool()}, cb)
	return resp, rec, err
}

func TestStreamCapturedReasoningAndToolCall(t *testing.T) {
	t.Parallel()
	insp := &gatewayCostInspector{}
	var tokens []string
	var mu sync.Mutex
	var activity, progress int
	var lastProgress llmtypes.ToolInputProgress

	rec := newRecorder(t, sseHandler(readFixture(t, "gpt-stream.sse")))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, Model: "openai/gpt-5.5",
		ThinkingLevel: "medium", ResponseInspector: insp.inspect})
	ctx := llmtypes.WithStreamActivity(context.Background(), func() { mu.Lock(); activity++; mu.Unlock() })
	ctx = llmtypes.WithToolInputProgress(ctx, func(p llmtypes.ToolInputProgress) {
		mu.Lock()
		progress++
		lastProgress = p
		mu.Unlock()
	})
	resp, err := c.ChatStream(ctx, []llmtypes.Message{{Role: "user", Content: "x"}}, []shuttle.Tool{weatherTool()},
		func(tok string) { tokens = append(tokens, tok) })
	require.NoError(t, err, "stream")

	body := rec.body(t, 0)
	assert.Equal(t, true, body["stream"], "stream flag sent")
	assert.Equal(t, "text/event-stream", rec.hdrs[0].Get("Accept"), "accept header")

	assert.Empty(t, tokens, "no text deltas in this capture: reasoning never reaches the token callback")
	assert.Equal(t, "tool_use", resp.StopReason, "stop reason")
	require.Len(t, resp.ToolCalls, 1, "tool call assembled")
	assert.Equal(t, "call_FEkVleQgbMUA4y11HnetjbQC", resp.ToolCalls[0].ID, "call id")
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name, "captured name passes through")
	assert.Equal(t, map[string]interface{}{"location": "Paris"}, resp.ToolCalls[0].Input, "arguments")

	assert.True(t, strings.HasPrefix(resp.Thinking, "**Checking if a number is prime"), "summary text in Thinking")
	require.Len(t, resp.ThinkingBlocks, 1, "one reasoning item, one summary part")
	assert.Equal(t, blockTypeReasoning, resp.ThinkingBlocks[0].Type, "block type")
	assert.Equal(t, "rs_0961935d5f3bdf94016aff4cde2c9c87d2916ae610726d4ad8", resp.ThinkingBlocks[0].Format, "item id")
	assert.Equal(t, "enc-placeholder-2", resp.ThinkingBlocks[0].Signature, "final encrypted_content from response.completed")

	assert.Equal(t, 51, resp.Usage.InputTokens, "input tokens")
	assert.Equal(t, 61, resp.Usage.OutputTokens, "output tokens")
	assert.Equal(t, 41, resp.Usage.ThinkingTokens, "reasoning tokens")
	assert.InDelta(t, 0.002085, resp.Usage.CostUSD, 1e-12, "gateway cost from response.completed")
	assert.Equal(t, "openai", resp.Metadata["gateway.final_provider"], "inspector attributes")
	assert.Equal(t, true, resp.Metadata["streaming"], "streaming flag")
	assert.Equal(t, "gpt-5.5-2026-04-23", resp.Metadata["model"], "served model")
	assert.Contains(t, resp.Metadata, "ttft_ms", "latency split")

	insp.mu.Lock()
	n := len(insp.payloads)
	insp.mu.Unlock()
	dataLines := 0
	for _, line := range strings.Split(readFixture(t, "gpt-stream.sse"), "\n") {
		if strings.HasPrefix(line, "data: ") {
			dataLines++
		}
	}
	assert.Equal(t, dataLines, n, "inspector sees every data payload")

	mu.Lock()
	defer mu.Unlock()
	assert.Positive(t, activity, "summary and argument deltas report stream activity")
	assert.Equal(t, 5, progress, "one progress report per argument delta")
	assert.Equal(t, "call_FEkVleQgbMUA4y11HnetjbQC", lastProgress.ToolCallID, "progress carries call id")
	assert.Equal(t, "get_weather", lastProgress.ToolName, "progress carries the tool name")
	assert.Equal(t, len(`{"location":"Paris"}`), lastProgress.Bytes, "progress bytes")
}

func TestStreamTextDeltas(t *testing.T) {
	t.Parallel()
	insp := &gatewayCostInspector{}
	var tokens []string
	resp, _, err := streamOnce(t, Config{Model: "openai/gpt-5.5", ResponseInspector: insp.inspect},
		readFixture(t, "text-stream.sse"), func(tok string) { tokens = append(tokens, tok) })
	require.NoError(t, err, "stream")
	assert.Equal(t, []string{"Hello", ", ", "world"}, tokens, "deltas in order")
	assert.Equal(t, "Hello, world", resp.Content, "content")
	assert.Equal(t, "end_turn", resp.StopReason, "stop reason")
	assert.Equal(t, 200, resp.Usage.InputTokens, "cached tokens subtracted")
	assert.Equal(t, 1000, resp.Usage.CacheReadInputTokens, "cache read")
	assert.Equal(t, 1203, resp.Usage.RateLimitTokens, "raw total")
	assert.InDelta(t, 0.0001, resp.Usage.CostUSD, 1e-12, "gateway cost")
}

func TestStreamFallbackToAccumulatedItems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		sse   string
		check func(t *testing.T, resp *llmtypes.LLMResponse)
	}{
		{
			name: "completed without output uses output_item.done items",
			sse: `data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"c1","name":"weather_get_weather","arguments":"{\"location\":\"Rome\"}"}}
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_x","encrypted_content":"e","summary":[]}}
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}
`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				require.Len(t, resp.ToolCalls, 1, "tool call from accumulated item")
				assert.Equal(t, "Rome", resp.ToolCalls[0].Input["location"], "arguments")
				require.Len(t, resp.ThinkingBlocks, 1, "reasoning from accumulated item")
				assert.Equal(t, "tool_use", resp.StopReason, "stop reason")
				assert.Equal(t, "gpt-4.1", resp.Metadata["model"], "client model when response has none")
			},
		},
		{
			name: "text deltas stand in for a message that never completed",
			sse: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","content":[]}}
data: {"type":"response.output_text.delta","output_index":0,"delta":"par"}
data: {"type":"response.output_text.delta","output_index":0,"delta":"tial"}
data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}
`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "partial", resp.Content, "text from deltas")
				assert.Equal(t, "max_tokens", resp.StopReason, "incomplete stop reason")
			},
		},
		{
			name: "refusal deltas",
			sse: `data: {"type":"response.refusal.delta","output_index":0,"delta":"No."}
data: {"type":"response.completed"}
data: [DONE]
`,
			check: func(t *testing.T, resp *llmtypes.LLMResponse) {
				assert.Equal(t, "refusal", resp.StopReason, "refusal stop reason")
				assert.Equal(t, "No.", resp.Content, "refusal text")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, _, err := streamOnce(t, Config{}, tt.sse, nil)
			require.NoError(t, err, "stream")
			tt.check(t, resp)
		})
	}
}

func TestStreamErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		sse        string
		wantIs     error
		wantText   string
		wantStatus int
	}{
		{
			name:     "truncated after deltas",
			sse:      "data: {\"type\":\"response.output_text.delta\",\"delta\":\"half an ans\"}\n",
			wantIs:   ErrTruncatedStream,
			wantText: "ended before its terminal event",
		},
		{
			name:     "done sentinel without terminal event",
			sse:      "data: {\"type\":\"response.created\"}\ndata: [DONE]\n",
			wantIs:   ErrTruncatedStream,
			wantText: "ended before its terminal event",
		},
		{
			name:       "response.failed",
			sse:        `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}` + "\n",
			wantText:   "boom",
			wantStatus: 500,
		},
		{
			name:     "response.failed without detail",
			sse:      `data: {"type":"response.failed"}` + "\n",
			wantText: "without an error object",
		},
		{
			name:       "error event, flat",
			sse:        `data: {"type":"error","code":"rate_limit_exceeded","message":"slow down"}` + "\n",
			wantText:   "slow down",
			wantStatus: 429,
		},
		{
			name:     "error event, nested, context length",
			sse:      `data: {"type":"error","error":{"code":"context_length_exceeded","message":"too long"}}` + "\n",
			wantIs:   llm.ErrContextTooLong,
			wantText: "too long",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var tokens []string
			resp, _, err := streamOnce(t, Config{}, tt.sse, func(s string) { tokens = append(tokens, s) })
			require.Error(t, err, "stream must fail")
			assert.Nil(t, resp, "no response on failure")
			assert.Contains(t, err.Error(), tt.wantText, "message")
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs, "sentinel")
			}
			if tt.wantStatus != 0 {
				var se *llm.HTTPStatusError
				require.True(t, errors.As(err, &se), "status attached")
				assert.Equal(t, tt.wantStatus, se.StatusCode, "status")
			}
		})
	}
}

func TestStreamLineTooLong(t *testing.T) {
	t.Parallel()
	huge := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("a", maxSSELineBytes) + "\"}\n"
	_, _, err := streamOnce(t, Config{}, huge, nil)
	require.Error(t, err, "line over the cap fails")
	assert.Contains(t, err.Error(), "SSE line exceeded", "message")
}

// slowHandler writes head, flushes, then blocks until the request ends.
func slowHandler(head string, delayHeaders bool) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, _ int) {
		if delayHeaders {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if head != "" {
			_, _ = io.WriteString(w, head)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}
}

func TestStreamTimeouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		handler   func(http.ResponseWriter, *http.Request, int)
		firstByte time.Duration
		idle      time.Duration
		wantPhase string
	}{
		{"first byte: headers never arrive", slowHandler("", true), 150 * time.Millisecond, time.Hour, "first-byte"},
		{"first byte: headers but no body", slowHandler("", false), 150 * time.Millisecond, time.Hour, "first-byte"},
		{"idle after first event", slowHandler("data: {\"type\":\"response.created\"}\n\n", false), time.Hour, 150 * time.Millisecond, "idle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder(t, tt.handler)
			c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL,
				StreamFirstByteTimeout: tt.firstByte, StreamIdleTimeout: tt.idle})
			start := time.Now()
			_, err := c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil, nil)
			require.Error(t, err, "stalled stream fails")
			assert.ErrorIs(t, err, llm.ErrStreamTimeout, "stream timeout sentinel")
			assert.Contains(t, err.Error(), tt.wantPhase, "phase named")
			assert.Less(t, time.Since(start), 5*time.Second, "fails near the limit, not at the server's leisure")
		})
	}
}

func TestStreamCallerCancel(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, slowHandler("data: {\"type\":\"response.created\"}\n\n", false))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := c.ChatStream(ctx, []llmtypes.Message{{Role: "user", Content: "x"}}, nil, nil)
	require.Error(t, err, "canceled stream fails")
	assert.NotErrorIs(t, err, llm.ErrStreamTimeout, "caller cancellation is not a stream timeout")
}

// Concurrent streams on one client share no per-request state.
func TestStreamConcurrent(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, sseHandler(readFixture(t, "text-stream.sse")))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}},
				[]shuttle.Tool{weatherTool()}, func(string) {})
			if err == nil && resp.Content != "Hello, world" {
				err = errors.New("wrong content: " + resp.Content)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err, "every concurrent stream succeeds")
	}
}
