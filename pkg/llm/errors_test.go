// Copyright 2026 Teradata
//
// Matcher tests — the context-too-long identification is the ONLY relief
// trigger (HLD §5.2); a miss fails silently, so both provider message shapes
// must be pinned.

package llm

import (
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
