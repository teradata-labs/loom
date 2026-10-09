// Copyright 2026 Teradata
//
// Extended thinking on the OpenAI-shaped (litellm) wire. The contract these
// routes pin was observed live against litellm 1.88.1 → Anthropic (2026-08-12
// probes): request field thinking:{type:"adaptive"}; response thinking text in
// reasoning_content and/or inside thinking_blocks (both shapes real); replay
// sends thinking_blocks back verbatim; streaming delivers reasoning_content
// deltas and thinking_blocks fragments (signature in a fragment), merged by
// index.
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

func thinkingTestServer(t *testing.T, gotBody *string, responseJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseJSON)
	}))
}

const plainResponse = `{"model":"claude-sonnet-5","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`

// TestThinking_RequestGate — the thinking field is sent exactly when a level
// is set AND the model is Claude-family; every other combination produces a
// request without the field (dark for the whole installed base).
func TestThinking_RequestGate(t *testing.T) {
	cases := []struct {
		name  string
		model string
		level string
		want  bool
	}{
		{"level+claude", "claude-sonnet-5", "auto", true},
		{"high+claude", "claude-sonnet-5", "high", true},
		{"empty level", "claude-sonnet-5", "", false},
		{"none level", "claude-sonnet-5", "none", false},
		{"level+gpt", "gpt-4o", "auto", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := thinkingTestServer(t, &gotBody, plainResponse)
			defer srv.Close()
			c := NewClient(Config{Model: tc.model, Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: tc.level})
			if _, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			has := strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`)
			if has != tc.want {
				t.Errorf("thinking field present=%v, want %v\nbody: %s", has, tc.want, gotBody)
			}
		})
	}
}

// TestThinking_ResponseParse — both observed response shapes land on the
// LLMResponse: text-in-blocks (empty reasoning_content) and text-as-
// reasoning_content (signature-only block).
func TestThinking_ResponseParse(t *testing.T) {
	blockShape := `{"model":"claude-sonnet-5","choices":[{"message":{"role":"assistant","content":"ok",` +
		`"reasoning_content":"","thinking_blocks":[{"type":"thinking","thinking":"the plan","signature":"SIG1"}]},` +
		`"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
	textShape := `{"model":"claude-sonnet-5","choices":[{"message":{"role":"assistant","content":"ok",` +
		`"reasoning_content":"the plan","thinking_blocks":[{"type":"thinking","thinking":"","signature":"SIG2"}]},` +
		`"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`

	for name, body := range map[string]string{"text-in-blocks": blockShape, "text-as-reasoning": textShape} {
		t.Run(name, func(t *testing.T) {
			var gotBody string
			srv := thinkingTestServer(t, &gotBody, body)
			defer srv.Close()
			c := NewClient(Config{Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})
			resp, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if resp.Thinking != "the plan" {
				t.Errorf("Thinking = %q, want %q", resp.Thinking, "the plan")
			}
			if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Signature == "" {
				t.Errorf("ThinkingBlocks not carried with signature: %+v", resp.ThinkingBlocks)
			}
			// The split-shape reassembly law: blocks are COMPLETE at rest —
			// a signature-only skeleton gets its relocated text paired back
			// in, because Anthropic 400s a replayed block with no thinking
			// field (observed live: messages.N.content.0.thinking.thinking
			// "Field required", T16 validation run).
			if resp.ThinkingBlocks[0].Thinking != "the plan" {
				t.Errorf("block not completed with relocated text: %+v", resp.ThinkingBlocks[0])
			}
		})
	}
}

// TestThinking_ReplayVerbatim — an assistant message carrying blocks replays
// them on the wire byte-for-byte (type, thinking, signature), and messages
// without blocks add nothing.
func TestThinking_ReplayVerbatim(t *testing.T) {
	var gotBody string
	srv := thinkingTestServer(t, &gotBody, plainResponse)
	defer srv.Close()
	c := NewClient(Config{Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})

	messages := []llmtypes.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "working", ThinkingBlocks: []llmtypes.ThinkingBlock{
			{Type: "thinking", Thinking: "step one", Signature: "SIGX"},
			// An incomplete skeleton (should not exist post-capture, but must
			// never reach the wire — Anthropic 400s on a text-less block).
			{Type: "thinking", Thinking: "", Signature: "ORPHAN"},
		}, ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "shell", Input: map[string]interface{}{}}}},
		{Role: "tool", ToolUseID: "c1", Content: "result"},
	}
	if _, err := c.Chat(context.Background(), messages, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	var found bool
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			if len(m.ThinkingBlocks) != 0 {
				t.Errorf("non-assistant message carries thinking blocks: %+v", m)
			}
			continue
		}
		for _, b := range m.ThinkingBlocks {
			if b.Type == "thinking" && b.Thinking == "step one" && b.Signature == "SIGX" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("assistant thinking block not replayed verbatim\nbody: %s", gotBody)
	}
	if strings.Contains(gotBody, "ORPHAN") {
		t.Errorf("text-less block skeleton reached the wire — replay guard failed\nbody: %s", gotBody)
	}
}

// TestThinking_StreamAssembly — reasoning_content deltas and thinking_blocks
// fragments (signature arriving in its own fragment) assemble into the final
// response; the token callback never sees thinking text.
func TestThinking_StreamAssembly(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunks := []string{
			`{"choices":[{"delta":{"role":"assistant","reasoning_content":"think "},"index":0}]}`,
			`{"choices":[{"delta":{"reasoning_content":"hard","thinking_blocks":[{"type":"thinking","index":0}]},"index":0}]}`,
			`{"choices":[{"delta":{"thinking_blocks":[{"signature":"STREAMSIG","index":0}]},"index":0}]}`,
			`{"choices":[{"delta":{"content":"answer"},"index":0}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		}
		for _, ch := range chunks {
			_, _ = io.WriteString(w, "data: "+ch+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(Config{Model: "claude-sonnet-5", Endpoint: srv.URL, MaxTokens: 64, ThinkingLevel: "auto"})
	var streamed strings.Builder
	resp, err := c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, nil,
		func(token string) { streamed.WriteString(token) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"adaptive"}`) {
		t.Errorf("stream request missing thinking field\nbody: %s", gotBody)
	}
	if resp.Thinking != "think hard" {
		t.Errorf("assembled Thinking = %q, want %q", resp.Thinking, "think hard")
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Signature != "STREAMSIG" {
		t.Errorf("streamed block not assembled with signature: %+v", resp.ThinkingBlocks)
	}
	if resp.ThinkingBlocks[0].Thinking != "think hard" {
		t.Errorf("streamed signature-only block not completed with delta text: %+v", resp.ThinkingBlocks[0])
	}
	if streamed.String() != "answer" {
		t.Errorf("token callback saw %q — thinking must never reach it", streamed.String())
	}
}

// The gateway path sends the form the model accepts, from the shared list:
// adaptive where the model takes it, enabled with a clamped budget otherwise.
// A model that takes only one shape rejects the other outright.
func TestGatewayThinking_FormFollowsTheModel(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		model, want string
	}{
		{"claude-opus-5-5", "adaptive"},
		{"claude-sonnet-5", "adaptive"},
		{"claude-haiku-5-5", "adaptive"},
		{"claude-opus-4-6", "adaptive"},
		{"claude-sonnet-4-5-20250929", "enabled"},
		{"claude-haiku-4-5-20251001", "enabled"},
		{"claude-sonnet-4-20250514", "enabled"},
	} {
		c := NewClient(Config{APIKey: "k", Model: tc.model, MaxTokens: 64000, ThinkingLevel: "high"})
		p := c.thinkingParam(ctx)
		if p == nil {
			t.Errorf("%s: thinking omitted entirely", tc.model)
			continue
		}
		if p["type"] != tc.want {
			t.Errorf("%s: type = %v, want %s", tc.model, p["type"], tc.want)
		}
		if tc.want == "enabled" {
			b, ok := p["budget_tokens"].(int)
			if !ok || b >= 64000 {
				t.Errorf("%s: budget %v is not below max_tokens", tc.model, p["budget_tokens"])
			}
		} else if _, carries := p["budget_tokens"]; carries {
			t.Errorf("%s: adaptive must not carry budget_tokens", tc.model)
		}
	}

	// A non-Claude model takes no thinking field at all.
	if p := NewClient(Config{APIKey: "k", Model: "gpt-5", MaxTokens: 64000, ThinkingLevel: "high"}).thinkingParam(ctx); p != nil {
		t.Errorf("non-Claude model got a thinking field: %v", p)
	}

	// The budget yields to a small ceiling, and is omitted when none fits.
	c := NewClient(Config{APIKey: "k", Model: "claude-sonnet-4-20250514", MaxTokens: 4096, ThinkingLevel: "low"})
	if p := c.thinkingParam(ctx); p == nil || p["budget_tokens"].(int) >= 4096 {
		t.Errorf("budget did not yield to max_tokens: %v", p)
	}
	c = NewClient(Config{APIKey: "k", Model: "claude-sonnet-4-20250514", MaxTokens: 1500, ThinkingLevel: "low"})
	if p := c.thinkingParam(ctx); p != nil {
		t.Errorf("no valid budget fits under 1500; thinking should be omitted: %v", p)
	}
}
