// Copyright 2026 Teradata
//
// Extended thinking on the native Anthropic wire. The contract these routes
// pin was observed live (2026-08-12 native probes, api.anthropic.com,
// claude-sonnet-5): adaptive takes display as a PLAIN STRING; the default
// (omitted) display returns thinking blocks with EMPTY text + signature; a
// replayed thinking block is valid with the thinking field present-but-empty
// and a 400 when the field is absent; streaming emits content_block_start
// (thinking) then thinking_delta / signature_delta fragments.
package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func thinkingSrv(t *testing.T, gotBody *string, responseJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseJSON)
	}))
}

const nativePlainResponse = `{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-5",` +
	`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":10,"output_tokens":2}}`

// TestNativeThinking_RequestMapping — adaptive+summarized on 4.6+/5 models,
// budget tiers on older ones, absent when off.
func TestNativeThinking_RequestMapping(t *testing.T) {
	cases := []struct {
		name, model, level, want string
	}{
		{"adaptive on sonnet-5", "claude-sonnet-5", "auto", `"thinking":{"display":"summarized","type":"adaptive"}`},
		{"adaptive on 4-6", "claude-sonnet-4-6", "high", `"type":"adaptive"`},
		{"budget high on older", "claude-3-5-sonnet-20241022", "high", `"budget_tokens":32768`},
		{"budget low on older", "claude-3-5-sonnet-20241022", "low", `"budget_tokens":4096`},
		{"off", "claude-sonnet-5", "", ""},
		{"none", "claude-sonnet-5", "none", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := thinkingSrv(t, &gotBody, nativePlainResponse)
			defer srv.Close()
			c := NewClient(Config{APIKey: "k", Model: tc.model, Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: tc.level})
			if _, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if tc.want == "" {
				if strings.Contains(gotBody, `"thinking"`) {
					t.Errorf("thinking field present when off\nbody: %s", gotBody)
				}
				return
			}
			if !strings.Contains(gotBody, tc.want) {
				t.Errorf("want %s in body\nbody: %s", tc.want, gotBody)
			}
		})
	}
}

// TestNativeThinking_PresentButEmptyReplay — the probe-caught law: an
// empty-text thinking block replays with the thinking field PRESENT
// ("thinking":""), never absent; redacted blocks carry data.
func TestNativeThinking_PresentButEmptyReplay(t *testing.T) {
	var gotBody string
	srv := thinkingSrv(t, &gotBody, nativePlainResponse)
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})

	messages := []llmtypes.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "working",
			ThinkingBlocks: []llmtypes.ThinkingBlock{
				{Type: "thinking", Thinking: "", Signature: "EMPTYSIG"},
				{Type: "redacted_thinking", Thinking: "REDACTEDDATA"},
			},
			ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "shell", Input: map[string]interface{}{}}}},
		{Role: "tool", ToolUseID: "c1", Content: "result"},
	}
	if _, err := c.Chat(context.Background(), messages, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(gotBody, `"thinking":""`) {
		t.Errorf("empty-text block must marshal the thinking field present-but-empty\nbody: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"signature":"EMPTYSIG"`) {
		t.Errorf("signature not replayed\nbody: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"data":"REDACTEDDATA"`) {
		t.Errorf("redacted block data not replayed\nbody: %s", gotBody)
	}
	// Thinking blocks come FIRST in the assistant content (response order):
	// the signature block precedes the assistant's own text.
	if ti, wi := strings.Index(gotBody, `"signature":"EMPTYSIG"`), strings.Index(gotBody, `"text":"working"`); ti > wi {
		t.Errorf("thinking block must precede text in assistant content\nbody: %s", gotBody)
	}
}

// TestNativeThinking_ResponseParse — thinking + redacted blocks land on the
// LLMResponse; the omitted-display shape (empty text + signature) parses too.
func TestNativeThinking_ResponseParse(t *testing.T) {
	resp := `{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-5","content":[` +
		`{"type":"thinking","thinking":"the plan","signature":"SIG1"},` +
		`{"type":"redacted_thinking","data":"OPAQUE"},` +
		`{"type":"text","text":"ok"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`
	var gotBody string
	srv := thinkingSrv(t, &gotBody, resp)
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})
	r, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if r.Thinking != "the plan" {
		t.Errorf("Thinking = %q", r.Thinking)
	}
	if len(r.ThinkingBlocks) != 2 || r.ThinkingBlocks[0].Signature != "SIG1" ||
		r.ThinkingBlocks[1].Type != "redacted_thinking" || r.ThinkingBlocks[1].Thinking != "OPAQUE" {
		t.Errorf("blocks not parsed: %+v", r.ThinkingBlocks)
	}
	if r.Content != "ok" {
		t.Errorf("Content = %q", r.Content)
	}
}

// TestNativeThinking_StreamAssembly — start/delta/signature events assemble
// per index; thinking never reaches the token callback.
func TestNativeThinking_StreamAssembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":10}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hard"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"STREAMSIG"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, ev := range events {
			_, _ = io.WriteString(w, "data: "+ev+"\n\n")
		}
	}))
	defer srv.Close()

	c := NewClient(Config{APIKey: "k", Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})
	var streamed strings.Builder
	r, err := c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil,
		func(token string) { streamed.WriteString(token) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if r.Thinking != "think hard" {
		t.Errorf("assembled Thinking = %q", r.Thinking)
	}
	if len(r.ThinkingBlocks) != 1 || r.ThinkingBlocks[0].Signature != "STREAMSIG" {
		t.Errorf("streamed block not assembled: %+v", r.ThinkingBlocks)
	}
	if streamed.String() != "answer" {
		t.Errorf("token callback saw %q — thinking must never reach it", streamed.String())
	}
}
