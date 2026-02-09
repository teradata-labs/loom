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

package apps

import (
	_ "embed"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

//go:embed html/conversation-viewer.html
var conversationViewerHTML []byte

// RegisterEmbeddedApps registers all built-in MCP App HTML resources.
// Returns an error if any registration fails (e.g., duplicate URI on second call).
func RegisterEmbeddedApps(registry *UIResourceRegistry) error {
	return registry.Register(&UIResource{
		URI:         "ui://loom/conversation-viewer",
		Name:        "Conversation Viewer",
		Description: "Interactive viewer for Loom agent conversations, sessions, and tool call history",
		MIMEType:    protocol.ResourceMIME,
		HTML:        conversationViewerHTML,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtr(true),
		},
	})
}

func boolPtr(b bool) *bool {
	return &b
}
