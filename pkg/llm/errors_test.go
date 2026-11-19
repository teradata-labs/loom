// Copyright 2026 Teradata
//
// Matcher tests — the context-too-long identification is the ONLY relief
// trigger (HLD §5.2); a miss fails silently, so both provider message shapes
// must be pinned.

package llm

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestIsAnthropicContextTooLong(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"prompt alone too long", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, true},
		{"prompt + max_tokens exceed (the common case)", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"input length and max_tokens exceed context limit: 150000 + 64000 > 200000, decrease input length or max_tokens and try again"}}`, true},
		{"unrelated 400 is NOT context-too-long", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`, false},
		{"a 429 is not this", 429,
			`{"type":"error","error":{"type":"rate_limit_error","message":"exceed context limit"}}`, false},
	}
	for _, c := range cases {
		if got := IsAnthropicContextTooLong(c.code, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsOpenAIContextTooLong(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"litellm context_length_exceeded", 400,
			`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 200000 tokens"}}`, true},
		{"litellm passthrough of anthropic-via-vertex (the company gateway)", 400,
			`{"error":{"message":"litellm.BadRequestError: Vertex_aiException BadRequestError - b'{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"prompt is too long: 258934 tokens > 200000 maximum\"}}'. Received Model Group=coding-agent/claude-haiku-4-5","type":null,"param":null,"code":"400"}}`, true},
		{"litellm passthrough exceed context limit", 400,
			`{"error":{"message":"litellm.BadRequestError: input length and max_tokens exceed context limit","code":"400"}}`, true},
		{"other 400 code", 400,
			`{"error":{"code":"invalid_api_key","message":"bad key"}}`, false},
	}
	for _, c := range cases {
		if got := IsOpenAIContextTooLong(c.code, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// vLLM's own refusals, as its OpenAI server words them (vllm-project/vllm,
// read at main 18d8c2ea and the release tags named). A prompt that is too long
// is context-too-long: a fold can shorten it. A request whose max_tokens alone
// is at or over the window is not: no prompt fits, and a fold would rewrite
// the stored results before the turn fails anyway. Older releases put the
// phrase "maximum context length" in that refusal too.
func TestIsOpenAIContextTooLong_VLLM(t *testing.T) {
	t.Parallel()
	body := func(msg string) string {
		b, err := json.Marshal(map[string]interface{}{"error": map[string]interface{}{
			"message": msg, "type": "BadRequestError", "param": "input_tokens", "code": 400}})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		// main and v0.32+ (vllm/renderers/params.py, _token_len_check).
		{"main: the prompt alone is over the window",
			"This model's maximum context length is 8192 tokens. However, your prompt contains at least 8193 input tokens, which exceeds this limit even without any output tokens. Please reduce the length of the input prompt. (parameter=input_tokens, value=8193)", true},
		// main and v0.32+: max_tokens over the window is refused in other words.
		{"main: max_tokens alone is over the window",
			"max_tokens=10000 cannot be greater than max_model_len=8192. Please request fewer output tokens. (parameter=max_tokens, value=10000)", false},
		// v0.17-v0.31 (params.py): one message for both cases.
		{"v0.31: the prompt and the reply together are over the window",
			"This model's maximum context length is 8192 tokens. However, you requested 1000 output tokens and your prompt contains 7500 input tokens, for a total of 8500 tokens. Please reduce the length of the input prompt or the number of requested output tokens. (parameter=input_tokens, value=7500)", true},
		{"v0.31: the reply alone fills the window",
			"This model's maximum context length is 8192 tokens. However, you requested 9000 output tokens and your prompt contains 20 input tokens, for a total of 9020 tokens. Please reduce the length of the input prompt or the number of requested output tokens. (parameter=input_tokens, value=20)", false},
		// v0.10-v0.14 (vllm/entrypoints/openai/serving_engine.py).
		{"v0.14: the prompt alone is over the window",
			"This model's maximum context length is 8192 tokens. However, your request has 9000 input tokens. Please reduce the length of the input messages.", true},
		{"v0.14: max_tokens is too large for this prompt",
			"'max_tokens' or 'max_completion_tokens' is too large: 1000. This model's maximum context length is 8192 tokens and your request has 7500 input tokens (1000 > 8192 - 7500).", true},
		{"v0.14: max_tokens alone fills the window",
			"'max_tokens' or 'max_completion_tokens' is too large: 9000. This model's maximum context length is 8192 tokens and your request has 20 input tokens (9000 > 8192 - 20).", false},
		// v0.6-v0.8 (serving_engine.py), the same shape as OpenAI's own text.
		{"v0.8: the messages and the completion together are over the window",
			"This model's maximum context length is 8192 tokens. However, you requested 8500 tokens (7500 in the messages, 1000 in the completion). Please reduce the length of the messages or completion.", true},
		{"v0.8: the completion alone fills the window",
			"This model's maximum context length is 8192 tokens. However, you requested 9020 tokens (20 in the messages, 9000 in the completion). Please reduce the length of the messages or completion.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := IsOpenAIContextTooLong(400, []byte(body(c.msg))); got != c.want {
				t.Errorf("IsOpenAIContextTooLong = %v, want %v for %q", got, c.want, c.msg)
			}
		})
	}
}

// The typed error is what the relief trigger checks with errors.Is.
func TestErrContextTooLong_IsWrappable(t *testing.T) {
	wrapped := errors.Join(errors.New("API error (status 400): body"), ErrContextTooLong)
	if !errors.Is(wrapped, ErrContextTooLong) {
		t.Fatal("wrapped ErrContextTooLong must be identifiable with errors.Is")
	}
}

func TestIsBedrockContextTooLong(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"anthropic wording via ValidationException",
			errors.New(`operation error Bedrock Runtime: InvokeModel, ValidationException: prompt is too long: 210000 tokens > 200000 maximum`), true},
		{"anthropic reserve wording",
			errors.New(`ValidationException: input length and max_tokens exceed context limit: 195000 + 8000 > 200000`), true},
		{"bedrock own wording",
			errors.New(`ValidationException: Input is too long for requested model.`), true},
		{"unrelated validation error",
			errors.New(`ValidationException: The provided model identifier is invalid.`), false},
		{"throttling", errors.New(`ThrottlingException: Too many requests`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBedrockContextTooLong(tc.err); got != tc.want {
				t.Errorf("IsBedrockContextTooLong(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Context-overflow refusals as the Vercel AI Gateway returns them (captured
// live 2026-11-16, bodies trimmed): its own error type, the upstream message
// passed through, no code. Relief must recognise both surfaces.
func TestContextTooLong_VercelGatewayBodies(t *testing.T) {
	messages := []byte(`{"error":{"message":"prompt is too long: 300024 tokens > 200000 maximum","type":"AI_APICallError","param":{"statusCode":400,"isRetryable":false}},"providerMetadata":{"gateway":{}}}`)
	chat := []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"AI_APICallError","param":{"statusCode":400,"isRetryable":false}},"providerMetadata":{"gateway":{}}}`)
	if !IsAnthropicContextTooLong(400, messages) {
		t.Error("Messages-surface gateway refusal must be context-too-long")
	}
	if !IsOpenAIContextTooLong(400, chat) {
		t.Error("Chat-surface gateway refusal must be context-too-long")
	}
	if IsAnthropicContextTooLong(400, []byte(`{"error":{"message":"Invalid model","type":"AI_APICallError"}}`)) {
		t.Error("an unrelated gateway 400 is not context-too-long")
	}
	if IsAnthropicContextTooLong(500, messages) {
		t.Error("a 500 is not this")
	}
	if IsOpenAIContextTooLong(400, []byte(`{"error":{"message":"No available providers match the 'only' filter","type":"invalid_request_error"}}`)) {
		t.Error("an unsatisfiable only filter is not context-too-long")
	}
}
