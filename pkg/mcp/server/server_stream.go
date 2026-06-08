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

package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// HandleMessageStream processes a single JSON-RPC message and, for a
// stream-capable tools/call, emits progress notifications via w before
// returning the final response bytes. Any other message — or any tool when no
// StreamingToolProvider is registered — falls back to the synchronous
// HandleMessage path, whose single result the transport emits as one SSE event.
//
// It implements transport.StreamingMCPHandler so cmd/loom-mcp can wire it as the
// StreamableHTTPServer's StreamHandler.
func (s *MCPServer) HandleMessageStream(ctx context.Context, msg []byte, w transport.SSEWriter) ([]byte, error) {
	var req protocol.Request
	if err := json.Unmarshal(msg, &req); err != nil {
		return marshalResponse(nil, nil, protocol.NewError(protocol.ParseError, "invalid JSON", nil))
	}

	// Only requests (with an id) for a streamable tool take the streaming path.
	sp, _ := s.toolProvider.(StreamingToolProvider)
	if req.Method != "tools/call" || req.ID == nil || sp == nil {
		return s.HandleMessage(ctx, msg)
	}

	var callParams protocol.CallToolParams
	if err := json.Unmarshal(req.Params, &callParams); err != nil {
		return marshalResponse(req.ID, nil, protocol.NewError(protocol.InvalidParams, "invalid tool call params", nil))
	}
	if callParams.Name == "" || !sp.SupportsStreaming(callParams.Name) {
		return s.HandleMessage(ctx, msg)
	}

	progressToken := extractProgressToken(req.Params)
	emit := &sseProgressEmitter{w: w, token: progressToken}

	result, err := sp.CallToolStream(ctx, callParams.Name, callParams.Arguments, progressToken, emit)
	if err != nil {
		// Mirror newToolsCallHandler: surface tool failures as an isError result,
		// not a transport error, so the client still receives a well-formed event.
		result = &protocol.CallToolResult{
			Content: []protocol.Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		}
	}
	return marshalResponse(req.ID, result, nil)
}

// extractProgressToken reads params._meta.progressToken (MCP 2025-03-26). The
// token may be a JSON string or number; it is normalized to a string because
// protocol.ProgressNotification.ProgressToken is string-typed. An empty result
// means the client did not request progress.
func extractProgressToken(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var p struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.Meta.ProgressToken) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(p.Meta.ProgressToken, &s); err == nil {
		return s
	}
	return strings.Trim(string(p.Meta.ProgressToken), `"`)
}

// sseProgressEmitter adapts an SSEWriter into a ProgressEmitter, framing each
// update as a JSON-RPC notifications/progress event. When no progress token was
// supplied it drops updates (progress is opt-in).
type sseProgressEmitter struct {
	w     transport.SSEWriter
	token string
}

func (e *sseProgressEmitter) EmitProgress(progress, total float64) error {
	if e.token == "" {
		return nil
	}
	notif, err := marshalNotification("notifications/progress", protocol.ProgressNotification{
		ProgressToken: e.token,
		Progress:      progress,
		Total:         total,
	})
	if err != nil {
		return err
	}
	return e.w.WriteEvent(notif)
}
