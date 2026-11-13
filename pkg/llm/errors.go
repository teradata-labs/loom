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
	"regexp"
	"strconv"
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

// windowRe and outputRes read a "maximum context length" refusal: the window
// it states, and the output the request asked for. outputRes holds one form
// for each wording that states the output (vLLM v0.10-v0.14, v0.17-v0.31,
// v0.6-v0.8; the last is also OpenAI's own text).
var (
	windowRe  = regexp.MustCompile(`maximum context length is (\d+) tokens`)
	outputRes = []*regexp.Regexp{
		regexp.MustCompile(`is too large: (\d+)\.`),
		regexp.MustCompile(`you requested (\d+) output tokens`),
		regexp.MustCompile(`, (\d+) in the completion\)`),
	}
)

// outputAloneFillsWindow reports whether a refusal says that the output the
// request asked for (max_tokens) is at or over the window by itself. A fold
// cannot answer that refusal: no prompt fits, so it is not context-too-long.
// IsOpenAIContextTooLong calls it. vLLM releases before v0.32.0 word that
// refusal with "maximum context length", as they word the prompt that is too
// long; vLLM main lowers max_tokens instead, and refuses a max_tokens over
// the window as "max_tokens=N cannot be greater than max_model_len=M", which
// no matcher here matches. A message that states no window or no output
// returns false.
func outputAloneFillsWindow(msg string) bool {
	w := windowRe.FindStringSubmatch(msg)
	if w == nil {
		return false
	}
	window, err := strconv.Atoi(w[1])
	if err != nil || window <= 0 {
		return false
	}
	for _, re := range outputRes {
		if o := re.FindStringSubmatch(msg); o != nil {
			output, err := strconv.Atoi(o[1])
			return err == nil && output >= window
		}
	}
	return false
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
		if detail.Message != "" {
			msg = detail.Message
		}
		if code, _ := detail.Code.(string); code == "context_length_exceeded" {
			return !outputAloneFillsWindow(msg)
		}
	} else {
		_ = json.Unmarshal(resp.Error, &msg)
	}
	if outputAloneFillsWindow(msg) {
		return false
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
	// for model with N maximum context length". TGI has two (router
	// validation): "`inputs` tokens + `max_new_tokens` must be <= N" when the
	// prompt and the reply together pass --max-total-tokens, and "`inputs`
	// must have less than N tokens" when the prompt alone passes
	// --max-input-tokens. Its "`max_new_tokens` must be <= N" is about the
	// reply size alone and is not matched: a shorter prompt cannot answer it.
	// For the same reason a "maximum context length" refusal whose requested
	// output alone is at or over the window is not matched
	// (outputAloneFillsWindow, checked above).
	return strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "exceed context limit") ||
		strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "`inputs` tokens + `max_new_tokens` must be <=") ||
		strings.Contains(msg, "`inputs` must have less than")
}
