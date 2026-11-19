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
	return sse("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "gen_test", "type": "message", "role": "assistant", "model": "m", "content": []any{},
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}}) +
		sse("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}}) +
		sse("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text}}) +
		sse("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) +
		sse("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 1}}) +
		sse("message_stop", map[string]any{"type": "message_stop"})
}

// ChatSSE is a complete Chat Completions stream that answers text.
func ChatSSE(text string) string {
	return sse("", map[string]any{"id": "gen_test", "object": "chat.completion.chunk", "created": 1, "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}}) +
		sse("", map[string]any{"id": "gen_test", "object": "chat.completion.chunk", "created": 1, "model": "m", "choices": []any{},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}) +
		"data: [DONE]\n\n"
}

// ResponsesSSE is a complete Responses API stream that answers text.
func ResponsesSSE(text string) string {
	item := map[string]any{"type": "message", "id": "msg_test", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	return sse("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": text}) +
		sse("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}) +
		sse("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "gen_test", "object": "response", "status": "completed", "model": "m", "output": []any{item},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
				"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}})
}

// sse renders one server-sent event; an empty event name writes only data.
func sse(event string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		panic(err) // fixtures are static maps; a marshal error is a bug in this file
	}
	if event == "" {
		return "data: " + string(b) + "\n\n"
	}
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
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
