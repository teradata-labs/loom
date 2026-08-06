// Copyright 2026 Teradata
//
// Deterministic unit test for prompt-cache wiring on the streaming path: proves
// (1) the client emits cache_control on exactly the CacheBreakpoint-flagged
// messages and requests stream_options.include_usage, and (2) it parses the
// streamed usage chunk's cache tokens back into llmtypes.Usage. No gateway, no
// cloud — an httptest mock returns a canned SSE stream.
package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func TestChatStream_CacheControlAndUsage(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A token chunk, then the final chunk carrying usage with cache tokens.
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"index\":0}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],"+
			"\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105,"+
			"\"cache_read_input_tokens\":80,\"cache_creation_input_tokens\":20}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(Config{Model: "coding-agent/claude-sonnet-4-6", Endpoint: srv.URL, MaxTokens: 100})

	// ROM, summary, and a later assistant message are marked as breakpoints; the
	// user message is NOT — it must stay a plain string with no cache_control.
	messages := []llmtypes.Message{
		{Role: "system", Content: "ROM operating guide", CacheBreakpoint: true},
		{Role: "system", Content: "covers msg:1-5 …summary…", CacheBreakpoint: true},
		{Role: "user", Content: "hello there"},
		{Role: "assistant", Content: "prior turn text", CacheBreakpoint: true},
	}

	resp, err := c.ChatStream(context.Background(), messages, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	// (1a) exactly three cache_control markers in the request body.
	if n := strings.Count(gotBody, "cache_control"); n != 3 {
		t.Errorf("want 3 cache_control markers in request, got %d\nbody: %s", n, gotBody)
	}
	if !strings.Contains(gotBody, "\"type\":\"ephemeral\"") {
		t.Errorf("cache_control marker is not ephemeral\nbody: %s", gotBody)
	}
	// (1b) streaming asks for the usage chunk.
	if !strings.Contains(gotBody, "\"include_usage\":true") {
		t.Errorf("stream_options.include_usage not requested\nbody: %s", gotBody)
	}
	// (1c) the unflagged user message stays a plain string (not a block list).
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			if _, isString := m.Content.(string); !isString {
				t.Errorf("unflagged user message should stay a plain string, got %T", m.Content)
			}
		}
	}

	// (2) the streamed usage chunk's cache tokens are parsed back.
	if resp.Usage.InputTokens != 100 {
		t.Errorf("input tokens: got %d want 100", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 80 {
		t.Errorf("cache_read: got %d want 80", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheCreationInputTokens != 20 {
		t.Errorf("cache_creation: got %d want 20", resp.Usage.CacheCreationInputTokens)
	}
}

// TestConvertMessages_CacheControlGatedToClaude proves cache_control is emitted
// only for Anthropic-backed models: a non-claude model keeps plain string
// content on marked messages — no foreign field, no block reshaping.
func TestConvertMessages_CacheControlGatedToClaude(t *testing.T) {
	c := NewClient(Config{Model: "gpt-4o", APIKey: "test"})
	msgs := c.convertMessages([]llmtypes.Message{
		{Role: "system", Content: "ROM", CacheBreakpoint: true},
		{Role: "user", Content: "hello"},
	})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if _, ok := msgs[0].Content.(string); !ok {
		t.Fatalf("non-claude model must keep plain string content, got %T", msgs[0].Content)
	}
}
