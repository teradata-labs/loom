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

//go:embed html/data-chart.html
var dataChartHTML []byte

//go:embed html/data-quality-dashboard.html
var dataQualityDashboardHTML []byte

//go:embed html/explain-plan-visualizer.html
var explainPlanVisualizerHTML []byte

// RegisterEmbeddedApps registers all built-in MCP App HTML resources.
// Returns an error if any registration fails (e.g., duplicate URI on second call).
func RegisterEmbeddedApps(registry *UIResourceRegistry) error {
	if err := registry.Register(&UIResource{
		URI:         "ui://loom/conversation-viewer",
		Name:        "Conversation Viewer",
		Description: "Interactive viewer for Loom agent conversations, sessions, and tool call history",
		MIMEType:    protocol.ResourceMIME,
		HTML:        conversationViewerHTML,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtr(true),
		},
	}); err != nil {
		return err
	}

	if err := registry.Register(&UIResource{
		URI:         "ui://loom/data-chart",
		Name:        "Data Chart",
		Description: "Interactive chart for visualizing time-series data from Loom agent queries",
		MIMEType:    protocol.ResourceMIME,
		HTML:        dataChartHTML,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtr(true),
		},
	}); err != nil {
		return err
	}

	if err := registry.Register(&UIResource{
		URI:         "ui://loom/data-quality-dashboard",
		Name:        "Data Quality Dashboard",
		Description: "Interactive dashboard for visualizing data quality metrics across Teradata database tables",
		MIMEType:    protocol.ResourceMIME,
		HTML:        dataQualityDashboardHTML,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtr(true),
		},
	}); err != nil {
		return err
	}

	return registry.Register(&UIResource{
		URI:         "ui://loom/explain-plan-visualizer",
		Name:        "EXPLAIN Plan Visualizer",
		Description: "Interactive DAG visualization of Teradata EXPLAIN plan output with cost analysis",
		MIMEType:    protocol.ResourceMIME,
		HTML:        explainPlanVisualizerHTML,
		Meta: &protocol.UIResourceMeta{
			PrefersBorder: boolPtr(true),
		},
	})
}

func boolPtr(b bool) *bool {
	return &b
}
