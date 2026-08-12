// Copyright 2026 Teradata
//
// Extended thinking on the Bedrock (Anthropic SDK) client — convert-function
// unit routes. The block grammar these pin was validated live on the native
// Anthropic wire (2026-08-12 probes); Bedrock speaks the same grammar through
// SDK types, so these test our mapping, with live Bedrock validation deferred
// until an AWS environment exists.
package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func bedrockThinkingClient(model, level string) *SDKClient {
	return &SDKClient{modelID: model, thinkingLevel: level, maxTokens: 64, temperature: 1.0}
}

// TestBedrockThinking_ConfigMapping — adaptive (summarized) on 4.6+/5 ids,
// budget tiers on older ones, omitted when off.
func TestBedrockThinking_ConfigMapping(t *testing.T) {
	cases := []struct {
		name, model, level, want string
	}{
		{"adaptive sonnet-5", "global.anthropic.claude-sonnet-5-v1:0", "auto", `"type":"adaptive"`},
		{"adaptive display", "global.anthropic.claude-sonnet-5-v1:0", "high", `"display":"summarized"`},
		{"budget high older", "us.anthropic.claude-3-5-sonnet-20241022-v2:0", "high", `"budget_tokens":32768`},
		{"budget low older", "us.anthropic.claude-3-5-sonnet-20241022-v2:0", "low", `"budget_tokens":4096`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := bedrockThinkingClient(tc.model, tc.level)
			b, err := json.Marshal(c.thinkingConfig())
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Errorf("want %s in %s", tc.want, b)
			}
		})
	}
	// Off: the zero union must marshal to nothing meaningful.
	c := bedrockThinkingClient("global.anthropic.claude-sonnet-5-v1:0", "")
	if b, _ := json.Marshal(c.thinkingConfig()); strings.Contains(string(b), "adaptive") ||
		strings.Contains(string(b), "budget") {
		t.Errorf("off level produced a thinking config: %s", b)
	}
}

// TestBedrockThinking_ReplayOrder — blocks replay first and verbatim in the
// assistant SDK message; redacted blocks carry data.
func TestBedrockThinking_ReplayOrder(t *testing.T) {
	c := bedrockThinkingClient("global.anthropic.claude-sonnet-5-v1:0", "auto")
	_, sdkMessages := c.convertMessagesToSDK([]llmtypes.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "working",
			ThinkingBlocks: []llmtypes.ThinkingBlock{
				{Type: "thinking", Thinking: "step one", Signature: "SIGX"},
				{Type: "redacted_thinking", Thinking: "OPAQUE"},
			},
			ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "shell", Input: map[string]interface{}{}}}},
	})
	b, err := json.Marshal(sdkMessages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	for _, want := range []string{`"signature":"SIGX"`, `"thinking":"step one"`, `"data":"OPAQUE"`} {
		if !strings.Contains(body, want) {
			t.Errorf("want %s in wire messages\nbody: %s", want, body)
		}
	}
	if ti, wi := strings.Index(body, `"signature":"SIGX"`), strings.Index(body, `"text":"working"`); ti > wi {
		t.Errorf("thinking block must precede assistant text\nbody: %s", body)
	}
}

// TestBedrockThinking_ResponseParse — SDK message blocks land on LLMResponse.
func TestBedrockThinking_ResponseParse(t *testing.T) {
	raw := `{"id":"m1","type":"message","role":"assistant","model":"x","content":[` +
		`{"type":"thinking","thinking":"the plan","signature":"SIG1"},` +
		`{"type":"redacted_thinking","data":"OPAQUE"},` +
		`{"type":"text","text":"ok"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal SDK message: %v", err)
	}
	c := bedrockThinkingClient("global.anthropic.claude-sonnet-5-v1:0", "auto")
	r := c.convertResponseFromSDK(&msg)
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
