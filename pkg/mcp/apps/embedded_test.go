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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestConversationViewerHTMLEmbed(t *testing.T) {
	require.NotEmpty(t, conversationViewerHTML, "embedded conversation viewer HTML should not be empty")
	htmlStr := string(conversationViewerHTML)

	// Verify it's valid HTML5
	assert.True(t, strings.HasPrefix(htmlStr, "<!DOCTYPE html>"), "should start with DOCTYPE")
	assert.Contains(t, htmlStr, "<html")
	assert.Contains(t, htmlStr, "</html>")
	assert.Contains(t, htmlStr, "<head>")
	assert.Contains(t, htmlStr, "<body>")

	// Verify MCP Apps protocol integration
	assert.Contains(t, htmlStr, "postMessage")
	assert.Contains(t, htmlStr, "jsonrpc")
	assert.Contains(t, htmlStr, "tools/call")
	assert.Contains(t, htmlStr, "loom_list_sessions")
	assert.Contains(t, htmlStr, "loom_get_conversation_history")

	// Verify UI features
	assert.Contains(t, htmlStr, "ui/initialize")
	assert.Contains(t, htmlStr, "ui/notifications/tool-result")
	assert.Contains(t, htmlStr, "ui/notifications/host-context-changed")
	assert.Contains(t, htmlStr, "Conversation Viewer")
}

func TestExplainPlanVisualizerHTMLEmbed(t *testing.T) {
	require.NotEmpty(t, explainPlanVisualizerHTML, "embedded explain plan visualizer HTML should not be empty")
	htmlStr := string(explainPlanVisualizerHTML)

	// Verify it's valid HTML5
	assert.True(t, strings.HasPrefix(htmlStr, "<!DOCTYPE html>"), "should start with DOCTYPE")
	assert.Contains(t, htmlStr, "<html")
	assert.Contains(t, htmlStr, "</html>")
	assert.Contains(t, htmlStr, "<head>")
	assert.Contains(t, htmlStr, "<body>")

	// Verify MCP Apps protocol integration
	assert.Contains(t, htmlStr, "postMessage")
	assert.Contains(t, htmlStr, "jsonrpc")
	assert.Contains(t, htmlStr, "ui/initialize")
	assert.Contains(t, htmlStr, "ui://loom/explain-plan-visualizer")

	// Verify EXPLAIN plan features
	assert.Contains(t, htmlStr, "EXPLAIN Plan Visualizer")
	assert.Contains(t, htmlStr, "explain-data") // postMessage data type
	assert.Contains(t, htmlStr, "svg")          // SVG DAG rendering
	assert.Contains(t, htmlStr, "dependsOn")    // DAG dependency references
}

func TestDataQualityDashboardHTMLEmbed(t *testing.T) {
	require.NotEmpty(t, dataQualityDashboardHTML, "embedded data quality dashboard HTML should not be empty")
	htmlStr := string(dataQualityDashboardHTML)

	// Verify it's valid HTML5
	assert.True(t, strings.HasPrefix(htmlStr, "<!DOCTYPE html>"), "should start with DOCTYPE")
	assert.Contains(t, htmlStr, "<html")
	assert.Contains(t, htmlStr, "</html>")
	assert.Contains(t, htmlStr, "<head>")
	assert.Contains(t, htmlStr, "<body>")

	// Verify MCP Apps protocol integration
	assert.Contains(t, htmlStr, "postMessage")
	assert.Contains(t, htmlStr, "jsonrpc")
	assert.Contains(t, htmlStr, "ui/initialize")

	// Verify data quality features
	assert.Contains(t, htmlStr, "Data Quality Dashboard")
	assert.Contains(t, htmlStr, "quality-data") // postMessage data type
	assert.Contains(t, htmlStr, "chart.js")     // Chart.js CDN
	assert.Contains(t, htmlStr, "completeness") // quality metric
	assert.Contains(t, htmlStr, "nullCount")    // column quality field
}

func TestRegisterEmbeddedApps(t *testing.T) {
	registry := NewUIResourceRegistry()
	err := RegisterEmbeddedApps(registry)
	require.NoError(t, err)

	assert.Equal(t, 4, registry.Count())

	// Verify all apps are registered
	resources := registry.List()
	require.Len(t, resources, 4)

	// Resources are sorted by URI
	assert.Equal(t, "ui://loom/conversation-viewer", resources[0].URI)
	assert.Equal(t, "Conversation Viewer", resources[0].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[0].MimeType)

	assert.Equal(t, "ui://loom/data-chart", resources[1].URI)
	assert.Equal(t, "Data Chart", resources[1].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[1].MimeType)

	assert.Equal(t, "ui://loom/data-quality-dashboard", resources[2].URI)
	assert.Equal(t, "Data Quality Dashboard", resources[2].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[2].MimeType)

	assert.Equal(t, "ui://loom/explain-plan-visualizer", resources[3].URI)
	assert.Equal(t, "EXPLAIN Plan Visualizer", resources[3].Name)
	assert.Equal(t, protocol.ResourceMIME, resources[3].MimeType)

	// Verify each app can be read
	for _, uri := range []string{
		"ui://loom/conversation-viewer",
		"ui://loom/data-chart",
		"ui://loom/data-quality-dashboard",
		"ui://loom/explain-plan-visualizer",
	} {
		result, err := registry.Read(uri)
		require.NoError(t, err, "failed to read %s", uri)
		require.Len(t, result.Contents, 1, "expected 1 content for %s", uri)
		assert.Contains(t, result.Contents[0].Text, "<!DOCTYPE html>", "%s should contain DOCTYPE", uri)
		assert.Equal(t, protocol.ResourceMIME, result.Contents[0].MimeType, "%s MIME type mismatch", uri)
	}
}

func TestRegisterEmbeddedApps_Idempotent(t *testing.T) {
	registry := NewUIResourceRegistry()
	err := RegisterEmbeddedApps(registry)
	require.NoError(t, err)

	// Second call returns a duplicate error (Register rejects duplicate URIs)
	err = RegisterEmbeddedApps(registry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")

	// Should still have exactly 4 resources (first call succeeded)
	assert.Equal(t, 4, registry.Count())
}
