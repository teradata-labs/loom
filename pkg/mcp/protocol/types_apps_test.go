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

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetUIToolMeta_NilMeta(t *testing.T) {
	tool := Tool{Name: "test", Meta: nil}
	meta := GetUIToolMeta(tool)
	assert.Nil(t, meta)
}

func TestGetUIToolMeta_NoUIKey(t *testing.T) {
	tool := Tool{
		Name: "test",
		Meta: map[string]interface{}{
			"other": "data",
		},
	}
	meta := GetUIToolMeta(tool)
	assert.Nil(t, meta)
}

func TestGetUIToolMeta_ValidUI(t *testing.T) {
	tool := Tool{
		Name: "test",
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{
				"resourceUri": "ui://loom/conversation-viewer",
				"visibility":  []interface{}{"model", "app"},
			},
		},
	}
	meta := GetUIToolMeta(tool)
	require.NotNil(t, meta)
	assert.Equal(t, "ui://loom/conversation-viewer", meta.ResourceURI)
	assert.Equal(t, []string{"model", "app"}, meta.Visibility)
}

func TestGetUIToolMeta_EmptyUI(t *testing.T) {
	tool := Tool{
		Name: "test",
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{},
		},
	}
	meta := GetUIToolMeta(tool)
	require.NotNil(t, meta)
	assert.Empty(t, meta.ResourceURI)
	assert.Nil(t, meta.Visibility)
}

func TestSetUIToolMeta(t *testing.T) {
	tool := Tool{Name: "test"}
	SetUIToolMeta(&tool, &UIToolMeta{
		ResourceURI: "ui://loom/viewer",
		Visibility:  []string{"model", "app"},
	})

	require.NotNil(t, tool.Meta)
	uiRaw, ok := tool.Meta["ui"]
	require.True(t, ok)

	uiMeta, ok := uiRaw.(*UIToolMeta)
	require.True(t, ok)
	assert.Equal(t, "ui://loom/viewer", uiMeta.ResourceURI)
}

func TestSetUIToolMeta_ExistingMeta(t *testing.T) {
	tool := Tool{
		Name: "test",
		Meta: map[string]interface{}{
			"other": "preserved",
		},
	}
	SetUIToolMeta(&tool, &UIToolMeta{ResourceURI: "ui://loom/viewer"})

	assert.Equal(t, "preserved", tool.Meta["other"])
	assert.NotNil(t, tool.Meta["ui"])
}

func TestClientSupportsApps(t *testing.T) {
	tests := []struct {
		name       string
		extensions map[string]interface{}
		expected   bool
	}{
		{"nil extensions", nil, false},
		{"empty extensions", map[string]interface{}{}, false},
		{"no UI extension", map[string]interface{}{"other": true}, false},
		{"has UI extension", map[string]interface{}{ExtensionID: map[string]interface{}{}}, true},
		{"UI extension with data", map[string]interface{}{ExtensionID: map[string]interface{}{"version": "1"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ClientSupportsApps(tt.extensions))
		})
	}
}

func TestServerAppsExtension(t *testing.T) {
	ext := ServerAppsExtension()
	require.NotNil(t, ext)
	_, ok := ext[ExtensionID]
	assert.True(t, ok)
}

func TestUIToolMeta_JSONRoundTrip(t *testing.T) {
	meta := UIToolMeta{
		ResourceURI: "ui://loom/conversation-viewer",
		Visibility:  []string{"model", "app"},
	}

	data, err := json.Marshal(meta)
	require.NoError(t, err)

	var unmarshaled UIToolMeta
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, meta.ResourceURI, unmarshaled.ResourceURI)
	assert.Equal(t, meta.Visibility, unmarshaled.Visibility)
}

func TestUIResourceMeta_JSONRoundTrip(t *testing.T) {
	prefersBorder := true
	meta := UIResourceMeta{
		CSP: &UIResourceCSP{
			ConnectDomains:  []string{"https://api.example.com"},
			ResourceDomains: []string{"https://cdn.example.com"},
		},
		Permissions: &UIResourcePermissions{
			ClipboardWrite: &struct{}{},
		},
		Domain:        "example.com",
		PrefersBorder: &prefersBorder,
	}

	data, err := json.Marshal(meta)
	require.NoError(t, err)

	var unmarshaled UIResourceMeta
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	require.NotNil(t, unmarshaled.CSP)
	assert.Equal(t, []string{"https://api.example.com"}, unmarshaled.CSP.ConnectDomains)
	assert.Equal(t, []string{"https://cdn.example.com"}, unmarshaled.CSP.ResourceDomains)
	require.NotNil(t, unmarshaled.Permissions)
	assert.NotNil(t, unmarshaled.Permissions.ClipboardWrite)
	assert.Nil(t, unmarshaled.Permissions.Camera)
	assert.Equal(t, "example.com", unmarshaled.Domain)
	require.NotNil(t, unmarshaled.PrefersBorder)
	assert.True(t, *unmarshaled.PrefersBorder)
}

func TestUIResourceCSP_JSONOmitEmpty(t *testing.T) {
	csp := UIResourceCSP{}
	data, err := json.Marshal(csp)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}

func TestUIResourcePermissions_JSONOmitEmpty(t *testing.T) {
	perms := UIResourcePermissions{}
	data, err := json.Marshal(perms)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}

func TestToolAnnotations_JSONRoundTrip(t *testing.T) {
	readOnly := true
	destructive := false
	annotations := ToolAnnotations{
		Title:           "My Tool",
		ReadOnlyHint:    &readOnly,
		DestructiveHint: &destructive,
	}

	data, err := json.Marshal(annotations)
	require.NoError(t, err)

	var unmarshaled ToolAnnotations
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, "My Tool", unmarshaled.Title)
	require.NotNil(t, unmarshaled.ReadOnlyHint)
	assert.True(t, *unmarshaled.ReadOnlyHint)
	require.NotNil(t, unmarshaled.DestructiveHint)
	assert.False(t, *unmarshaled.DestructiveHint)
	assert.Nil(t, unmarshaled.IdempotentHint)
}

func TestTool_WithAnnotationsAndMeta_JSONRoundTrip(t *testing.T) {
	readOnly := true
	tool := Tool{
		Name:        "loom_weave",
		Description: "Execute a weave request",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"prompt": map[string]interface{}{"type": "string"},
			},
		},
		Annotations: &ToolAnnotations{
			ReadOnlyHint: &readOnly,
		},
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{
				"resourceUri": "ui://loom/conversation-viewer",
				"visibility":  []interface{}{"model", "app"},
			},
		},
	}

	data, err := json.Marshal(tool)
	require.NoError(t, err)

	var unmarshaled Tool
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, tool.Name, unmarshaled.Name)
	assert.Equal(t, tool.Description, unmarshaled.Description)
	require.NotNil(t, unmarshaled.Annotations)
	require.NotNil(t, unmarshaled.Annotations.ReadOnlyHint)
	assert.True(t, *unmarshaled.Annotations.ReadOnlyHint)
	require.NotNil(t, unmarshaled.Meta)
	assert.NotNil(t, unmarshaled.Meta["ui"])
}

func TestInitializeParams_WithExtensions_JSONRoundTrip(t *testing.T) {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    ClientCapabilities{},
		ClientInfo: Implementation{
			Name:    "claude-desktop",
			Version: "1.0.0",
		},
		Extensions: map[string]interface{}{
			ExtensionID: map[string]interface{}{},
		},
	}

	data, err := json.Marshal(params)
	require.NoError(t, err)

	var unmarshaled InitializeParams
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.True(t, ClientSupportsApps(unmarshaled.Extensions))
}

func TestInitializeResult_WithExtensions_JSONRoundTrip(t *testing.T) {
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools:     &ToolsCapability{},
			Resources: &ResourcesCapability{},
		},
		ServerInfo: Implementation{
			Name:    "loom-mcp",
			Version: "1.0.0",
		},
		Extensions: ServerAppsExtension(),
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled InitializeResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.True(t, ClientSupportsApps(unmarshaled.Extensions))
}

func TestCallToolResult_WithStructuredContent_JSONRoundTrip(t *testing.T) {
	result := CallToolResult{
		Content: []Content{
			{Type: "text", Text: "result data"},
		},
		StructuredContent: map[string]interface{}{
			"type":    "table",
			"headers": []interface{}{"col1", "col2"},
			"rows":    []interface{}{[]interface{}{"a", "b"}},
		},
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var unmarshaled CallToolResult
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	require.NotNil(t, unmarshaled.StructuredContent)
	assert.Equal(t, "table", unmarshaled.StructuredContent["type"])
}

func TestResourceContents_WithMeta_JSONRoundTrip(t *testing.T) {
	contents := ResourceContents{
		URI:      "ui://loom/conversation-viewer",
		MimeType: ResourceMIME,
		Text:     "<html>...</html>",
		Meta: map[string]interface{}{
			"csp": map[string]interface{}{
				"connectDomains": []interface{}{"https://api.example.com"},
			},
		},
	}

	data, err := json.Marshal(contents)
	require.NoError(t, err)

	var unmarshaled ResourceContents
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, contents.URI, unmarshaled.URI)
	assert.Equal(t, ResourceMIME, unmarshaled.MimeType)
	require.NotNil(t, unmarshaled.Meta)
	assert.NotNil(t, unmarshaled.Meta["csp"])
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "io.modelcontextprotocol/ui", ExtensionID)
	assert.Equal(t, "text/html;profile=mcp-app", ResourceMIME)
	assert.Equal(t, "ui://", UIScheme)
}
