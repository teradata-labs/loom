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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestToolResultError(t *testing.T) {
	tests := []struct {
		name    string
		content []protocol.Content
		wantMsg string
		wantURI string
	}{
		{
			name:    "text only renders historical message, no retry URI",
			content: []protocol.Content{{Type: "text", Text: "budget full"}},
			wantMsg: "tool error: budget full",
			wantURI: "",
		},
		{
			name: "resource_link marks the retry resource",
			content: []protocol.Content{
				{Type: "text", Text: "budget full"},
				{Type: "resource_link", URI: "teradata://session-handles", Name: "session-handles"},
			},
			wantMsg: "tool error: budget full",
			wantURI: "teradata://session-handles",
		},
		{
			name: "embedded plain resource is payload, not a retry condition",
			content: []protocol.Content{
				{Type: "text", Text: "busy"},
				{Type: "resource", Resource: &protocol.ResourceRef{URI: "x://slots"}},
			},
			wantMsg: "tool error: busy",
			wantURI: "",
		},
		{
			name: "resource_link without uri is ignored",
			content: []protocol.Content{
				{Type: "text", Text: "busy"},
				{Type: "resource_link", Name: "nameless"},
			},
			wantMsg: "tool error: busy",
			wantURI: "",
		},
		{
			name:    "no content",
			content: nil,
			wantMsg: "tool returned error",
			wantURI: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &ToolResultError{Result: &protocol.CallToolResult{IsError: true, Content: tt.content}}
			assert.Equal(t, tt.wantMsg, e.Error())
			assert.Equal(t, tt.wantURI, e.RetryResourceURI())
		})
	}

	t.Run("nil result", func(t *testing.T) {
		e := &ToolResultError{}
		assert.Equal(t, "tool returned error", e.Error())
		assert.Equal(t, "", e.RetryResourceURI())
	})
}
