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

package client

import (
	"strings"
	"testing"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestDetectEmbeddedError(t *testing.T) {
	tests := []struct {
		name        string
		result      *protocol.CallToolResult
		wantOK      bool
		wantMsgPart string
	}{
		{
			name:   "nil result",
			result: nil,
			wantOK: false,
		},
		{
			name: "teradata ok false with error string",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"ok":false,"error":"[Error 3706] Syntax error: expected something between the 'SELECT' keyword and the 'order' keyword."}`,
				}},
			},
			wantOK:      true,
			wantMsgPart: "Error 3706",
		},
		{
			name: "ok true is success",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"ok":true,"rows":[{"id":1}]}`,
				}},
			},
			wantOK: false,
		},
		{
			name: "success false",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"success":false,"error":"boom"}`,
				}},
			},
			wantOK:      true,
			wantMsgPart: "boom",
		},
		{
			name: "status error",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"status":"error","message":"nope"}`,
				}},
			},
			wantOK: true,
		},
		{
			name: "non-empty error field alone",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"error":"table not found"}`,
				}},
			},
			wantOK:      true,
			wantMsgPart: "table not found",
		},
		{
			name: "empty error field is not a failure",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"error":"","data":42}`,
				}},
			},
			wantOK: false,
		},
		{
			name: "errors array populated",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"errors":["a","b"]}`,
				}},
			},
			wantOK: true,
		},
		{
			name: "empty errors array is not a failure",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `{"errors":[],"ok":true}`,
				}},
			},
			wantOK: false,
		},
		{
			name: "plain text is never an error",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: "operation completed with 3 rows",
				}},
			},
			wantOK: false,
		},
		{
			name: "json array payload ignored",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{
					Type: "text",
					Text: `[{"ok":false}]`,
				}},
			},
			wantOK: false,
		},
		{
			name: "structured content ok false",
			result: &protocol.CallToolResult{
				Content: []protocol.Content{{Type: "text", Text: "see structured"}},
				StructuredContent: map[string]interface{}{
					"ok":    false,
					"error": "structured failure",
				},
			},
			wantOK:      true,
			wantMsgPart: "structured failure",
		},
		{
			name: "structured content ok true",
			result: &protocol.CallToolResult{
				StructuredContent: map[string]interface{}{
					"ok":   true,
					"rows": 5,
				},
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := detectEmbeddedError(tt.result)
			if ok != tt.wantOK {
				t.Fatalf("detectEmbeddedError() ok = %v, want %v (msg=%q)", ok, tt.wantOK, msg)
			}
			if tt.wantMsgPart != "" && !strings.Contains(msg, tt.wantMsgPart) {
				t.Fatalf("detectEmbeddedError() msg = %q, want to contain %q", msg, tt.wantMsgPart)
			}
		})
	}
}

func TestJSONHasContent(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{``, false},
		{`null`, false},
		{`""`, false},
		{`[]`, false},
		{`{}`, false},
		{`0`, false},
		{`false`, false},
		{`"msg"`, true},
		{`["a"]`, true},
		{`{"k":1}`, true},
		{`true`, true},
	}
	for _, tt := range tests {
		if got := jsonHasContent([]byte(tt.raw)); got != tt.want {
			t.Errorf("jsonHasContent(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
