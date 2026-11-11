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
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrContextTooLong marks a provider refusal positively identified as
// "context too long" (HLD §5.2 step 12). It is the ONLY relief trigger:
// identification is positive, per provider — anthropic: status 400,
// error.type="invalid_request_error", message containing "prompt is too long"
// OR "exceed context limit";
// OpenAI-shaped (LiteLLM): status 400, error.code="context_length_exceeded".
// Gemini: status 400, "exceeds the maximum number of tokens allowed". Bedrock
// (SDK and InvokeModel): the ValidationException wording. OpenAI-shaped
// servers that set no code (Mistral, vLLM, TGI): their own wording.
// An error not positively identified is NOT context-too-long and propagates as
// today.
var ErrContextTooLong = errors.New("context too long")

// AsContextTooLong marks err as the provider's context-too-long refusal,
// keeping its text and chain.
func AsContextTooLong(err error) error {
	return fmt.Errorf("%w: %w", err, ErrContextTooLong)
}

// IsGeminiContextTooLong positively identifies Gemini's refusal of a prompt
// over the model's input limit from the status and the error text (the raw
// body or the error message): status 400, "The input token count (N) exceeds
// the maximum number of tokens allowed (M)".
func IsGeminiContextTooLong(statusCode int, text string) bool {
	return statusCode == http.StatusBadRequest &&
		strings.Contains(text, "exceeds the maximum number of tokens allowed")
}

// ErrStreamTimeout marks a provider response stream that stopped producing
// bytes while the caller's context remained active.
var ErrStreamTimeout = errors.New("provider stream timeout")

// IsAnthropicContextTooLong positively identifies anthropic's prompt-too-long
// refusal from the HTTP status and raw response body.
func IsAnthropicContextTooLong(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	var resp struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	// Anthropic emits two distinct messages for this refusal; both must match.
	// "prompt is too long" — prompt alone over the window.
	// "exceed context limit" — prompt + max_tokens over it (the common case with
	// any real output reservation: it fires first, while the prompt is still
	// under the window). A matcher carrying only the first never triggers relief
	// in normal operation, and fails silently (releasePressure never runs).
	return resp.Error.Type == "invalid_request_error" &&
		(strings.Contains(resp.Error.Message, "prompt is too long") ||
			strings.Contains(resp.Error.Message, "exceed context limit"))
}

// IsBedrockContextTooLong positively identifies the context refusal surfaced
// by the anthropic SDK's bedrock backend from the SDK error text. Bedrock
// wraps anthropic's refusal in a ValidationException whose message carries the
// same wording anthropic emits directly ("prompt is too long", "exceed
// context limit") or bedrock's own variant ("Input is too long for requested
// model"). Anything else is NOT context-too-long and propagates as today.
func IsBedrockContextTooLong(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "exceed context limit") ||
		strings.Contains(msg, "Input is too long for requested model")
}

// IsOpenAIContextTooLong positively identifies the OpenAI-shaped (LiteLLM)
// context-length refusal from the HTTP status and raw response body.
func IsOpenAIContextTooLong(statusCode int, body []byte) bool {
	// 422 is TGI's status for a request over its token limits.
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	// The error is an object with a code and a message (OpenAI, Azure, vLLM,
	// LiteLLM), a string (TGI), or absent with the message at the top level
	// (Mistral).
	var resp struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	var detail struct {
		Code    interface{} `json:"code"`
		Message string      `json:"message"`
	}
	msg := resp.Message
	if json.Unmarshal(resp.Error, &detail) == nil {
		if code, _ := detail.Code.(string); code == "context_length_exceeded" {
			return true
		}
		if detail.Message != "" {
			msg = detail.Message
		}
	} else {
		_ = json.Unmarshal(resp.Error, &msg)
	}
	// LiteLLM proxying a non-OpenAI upstream (Anthropic via Vertex on the company
	// gateway) does NOT normalise to context_length_exceeded: it returns a generic
	// 400 with the upstream provider's own message passed through verbatim
	// ("litellm.BadRequestError: ... prompt is too long: N tokens > M maximum").
	// Matching only the OpenAI code leaves relief blind to that gateway — the
	// refusal fires, the matcher misses it, and the turn hard-fails instead of
	// shedding. Match the passed-through wording too.
	//
	// Servers that set no code state the refusal in their own words: OpenAI and
	// vLLM "This model's maximum context length is N tokens", Mistral "too large
	// for model with N maximum context length", TGI "`inputs` tokens +
	// `max_new_tokens` must be <= N".
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "exceed context limit") ||
		strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "`inputs` tokens + `max_new_tokens` must be <=")
}
