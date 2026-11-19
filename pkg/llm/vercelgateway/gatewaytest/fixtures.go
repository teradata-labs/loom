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

// Package gatewaytest holds response fixtures for tests that put a fake
// Vercel AI Gateway behind a vercelgateway.Client. The client streams every
// model call (Chat included), so a fake gateway answers with server-sent
// events; these build complete, minimal streams for each API surface.
package gatewaytest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// MessagesSSE is a complete Anthropic Messages stream that answers text.
func MessagesSSE(text string) string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"gen_test","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + quote(text) + `}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
}

// ChatSSE is a complete Chat Completions stream that answers text.
func ChatSSE(text string) string {
	return `data: {"id":"gen_test","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":` + quote(text) + `},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"gen_test","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
}

// ResponsesSSE is a complete Responses API stream that answers text.
func ResponsesSSE(text string) string {
	item := `{"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + quote(text) + `,"annotations":[]}]}`
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":` + quote(text) + `}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":` + item + `}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"gen_test","object":"response","status":"completed","model":"m","output":[` + item + `],` +
		`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}` + "\n\n"
}

// WriteSSE answers a model request on any surface with text, choosing the
// stream shape from the request path.
func WriteSSE(w http.ResponseWriter, r *http.Request, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	var body string
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/messages"):
		body = MessagesSSE(text)
	case strings.HasSuffix(r.URL.Path, "/v1/responses"):
		body = ResponsesSSE(text)
	default:
		body = ChatSSE(text)
	}
	_, _ = io.WriteString(w, body)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
