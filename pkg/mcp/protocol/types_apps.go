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

package protocol

import "encoding/json"

// MCP Apps extension constants
const (
	// ExtensionID is the well-known extension identifier for MCP Apps (UI support)
	ExtensionID = "io.modelcontextprotocol/ui"

	// ResourceMIME is the MIME type for MCP App HTML resources
	ResourceMIME = "text/html;profile=mcp-app"

	// UIScheme is the URI scheme prefix for MCP App resources
	UIScheme = "ui://"
)

// UIToolMeta contains UI-related metadata attached to a tool's _meta field.
// When present, it indicates the tool has an associated interactive UI resource.
type UIToolMeta struct {
	ResourceURI string   `json:"resourceUri,omitempty"` // URI of associated UI resource (e.g. "ui://loom/conversation-viewer")
	Visibility  []string `json:"visibility,omitempty"`  // Who sees this tool: "model", "app"
}

// UIResourceMeta contains metadata for a UI resource's _meta field.
// It controls security policies, permissions, and display preferences.
type UIResourceMeta struct {
	CSP           *UIResourceCSP         `json:"csp,omitempty"`
	Permissions   *UIResourcePermissions `json:"permissions,omitempty"`
	Domain        string                 `json:"domain,omitempty"`
	PrefersBorder *bool                  `json:"prefersBorder,omitempty"`
}

// UIResourceCSP defines Content Security Policy settings for a UI resource.
type UIResourceCSP struct {
	ConnectDomains  []string `json:"connectDomains,omitempty"`
	ResourceDomains []string `json:"resourceDomains,omitempty"`
	FrameDomains    []string `json:"frameDomains,omitempty"`
	BaseURIDomains  []string `json:"baseUriDomains,omitempty"`
}

// UIResourcePermissions declares what browser APIs a UI resource may use.
// Each permission is an opt-in marker (presence means requested).
type UIResourcePermissions struct {
	Camera         *struct{} `json:"camera,omitempty"`
	Microphone     *struct{} `json:"microphone,omitempty"`
	Geolocation    *struct{} `json:"geolocation,omitempty"`
	ClipboardWrite *struct{} `json:"clipboardWrite,omitempty"`
}

// GetUIToolMeta extracts UI metadata from a Tool's _meta field.
// Returns nil if the tool has no UI metadata.
func GetUIToolMeta(tool Tool) *UIToolMeta {
	if tool.Meta == nil {
		return nil
	}

	uiRaw, ok := tool.Meta["ui"]
	if !ok {
		return nil
	}

	// Marshal and unmarshal to convert map[string]interface{} to UIToolMeta
	data, err := json.Marshal(uiRaw)
	if err != nil {
		return nil
	}

	var meta UIToolMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}

	return &meta
}

// SetUIToolMeta attaches UI metadata to a Tool's _meta field.
// Initializes the Meta map if nil.
func SetUIToolMeta(tool *Tool, meta *UIToolMeta) {
	if tool.Meta == nil {
		tool.Meta = make(map[string]interface{})
	}
	tool.Meta["ui"] = meta
}

// ClientSupportsApps checks whether a client's extensions indicate MCP Apps support.
// This is determined by the presence of the ExtensionID key in the extensions map.
func ClientSupportsApps(extensions map[string]interface{}) bool {
	if extensions == nil {
		return false
	}
	_, ok := extensions[ExtensionID]
	return ok
}

// ServerAppsExtension builds the extensions map a server should return
// to advertise MCP Apps support during initialization.
func ServerAppsExtension() map[string]interface{} {
	return map[string]interface{}{
		ExtensionID: map[string]interface{}{},
	}
}
