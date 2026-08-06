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
	"net/http"
	"strings"
)

// ErrContextTooLong marks a provider refusal positively identified as
// "context too long" (HLD §5.2 step 12). It is the ONLY relief trigger:
// identification is positive, per provider — anthropic: status 400,
// error.type="invalid_request_error", message containing "prompt is too long"
// OR "exceed context limit";
// OpenAI-shaped (LiteLLM): status 400, error.code="context_length_exceeded".
// An error not positively identified is NOT context-too-long and propagates as
// today.
var ErrContextTooLong = errors.New("context too long")

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
	if statusCode != http.StatusBadRequest {
		return false
	}
	var resp struct {
		Error struct {
			Code    interface{} `json:"code"`
			Message string      `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	if code, _ := resp.Error.Code.(string); code == "context_length_exceeded" {
		return true
	}
	// LiteLLM proxying a non-OpenAI upstream (Anthropic via Vertex on the company
	// gateway) does NOT normalise to context_length_exceeded: it returns a generic
	// 400 with the upstream provider's own message passed through verbatim
	// ("litellm.BadRequestError: ... prompt is too long: N tokens > M maximum").
	// Matching only the OpenAI code leaves relief blind to that gateway — the
	// refusal fires, the matcher misses it, and the turn hard-fails instead of
	// shedding. Match the passed-through wording too.
	msg := resp.Error.Message
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "exceed context limit") ||
		strings.Contains(msg, "context_length_exceeded")
}
