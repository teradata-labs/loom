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
	"encoding/json"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// ComponentCatalog holds the definitions for all supported component types.
// It is used for validation and for the ListComponentTypes discovery RPC.
type ComponentCatalog struct {
	entries []componentEntry
	byType  map[string]*componentEntry
}

type componentEntry struct {
	Type        string
	Description string
	Category    string // "display", "layout", "complex"
	HasChildren bool   // Whether this component uses the children field
	PropsSchema map[string]interface{}
	ExampleJSON string
}

// NewComponentCatalog creates a catalog with all 14 built-in component types.
func NewComponentCatalog() *ComponentCatalog {
	entries := []componentEntry{
		// Display components
		{
			Type:        "stat-cards",
			Description: "Row of KPI stat cards with label, value, optional color and sublabel",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"label":    map[string]interface{}{"type": "string"},
								"value":    map[string]interface{}{"type": "string"},
								"color":    map[string]interface{}{"type": "string", "description": "Named color (accent, success, warning, error) or hex"},
								"sublabel": map[string]interface{}{"type": "string"},
							},
							"required": []string{"label", "value"},
						},
					},
				},
				"required": []string{"items"},
			},
			ExampleJSON: `{"type":"stat-cards","props":{"items":[{"label":"Total Revenue","value":"$4.54M","color":"success"},{"label":"Active Users","value":"2,100","color":"accent"}]}}`,
		},
		{
			Type:        "chart",
			Description: "Chart.js chart (bar, line, pie, doughnut, radar, polarArea, scatter, bubble)",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"chartType": map[string]interface{}{"type": "string", "enum": []string{"bar", "line", "pie", "doughnut", "radar", "polarArea", "scatter", "bubble"}},
					"title":     map[string]interface{}{"type": "string"},
					"labels":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"datasets": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"label": map[string]interface{}{"type": "string"},
								"data":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}},
								"color": map[string]interface{}{"type": "string"},
							},
							"required": []string{"label", "data"},
						},
					},
					"fill":    map[string]interface{}{"type": "boolean"},
					"stacked": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"chartType", "labels", "datasets"},
			},
			ExampleJSON: `{"type":"chart","props":{"chartType":"bar","title":"Monthly Revenue","labels":["Jan","Feb","Mar"],"datasets":[{"label":"Revenue","data":[320,380,410],"color":"accent"}]}}`,
		},
		{
			Type:        "table",
			Description: "Data table with columns, rows, optional sorting and max height",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":     map[string]interface{}{"type": "string"},
					"columns":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"rows":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}}},
					"sortable":  map[string]interface{}{"type": "boolean"},
					"maxHeight": map[string]interface{}{"type": "string", "description": "CSS max-height value (e.g. '400px')"},
				},
				"required": []string{"columns", "rows"},
			},
			ExampleJSON: `{"type":"table","props":{"title":"Top Customers","columns":["Customer","Revenue"],"rows":[["Acme Corp","$1.2M"],["GlobalTech","$890K"]]}}`,
		},
		{
			Type:        "key-value",
			Description: "Key-value metadata pairs in grid or list layout",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string"},
					"items": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"key":   map[string]interface{}{"type": "string"},
								"value": map[string]interface{}{"type": "string"},
								"color": map[string]interface{}{"type": "string"},
							},
							"required": []string{"key", "value"},
						},
					},
					"layout": map[string]interface{}{"type": "string", "enum": []string{"grid", "list"}},
				},
				"required": []string{"items"},
			},
			ExampleJSON: `{"type":"key-value","props":{"title":"Database Info","items":[{"key":"Host","value":"prod-db-01"},{"key":"Status","value":"Online","color":"success"}]}}`,
		},
		{
			Type:        "text",
			Description: "Text block with optional styling (default, note, warning, error)",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{"type": "string"},
					"style":   map[string]interface{}{"type": "string", "enum": []string{"default", "note", "warning", "error"}},
				},
				"required": []string{"content"},
			},
			ExampleJSON: `{"type":"text","props":{"content":"Analysis complete. 3 anomalies detected.","style":"warning"}}`,
		},
		{
			Type:        "code-block",
			Description: "Monospace code display with optional title and language hint",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":    map[string]interface{}{"type": "string"},
					"language": map[string]interface{}{"type": "string"},
					"code":     map[string]interface{}{"type": "string"},
				},
				"required": []string{"code"},
			},
			ExampleJSON: `{"type":"code-block","props":{"title":"Generated SQL","language":"sql","code":"SELECT customer_id, SUM(amount) FROM orders GROUP BY 1"}}`,
		},
		{
			Type:        "progress-bar",
			Description: "Percentage progress bars with labels and colored fills",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string"},
					"items": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"label": map[string]interface{}{"type": "string"},
								"value": map[string]interface{}{"type": "number", "description": "0-100"},
								"color": map[string]interface{}{"type": "string"},
							},
							"required": []string{"label", "value"},
						},
					},
					"thresholds": map[string]interface{}{
						"type":        "object",
						"description": "Color thresholds: {warning: 60, error: 80}",
					},
				},
				"required": []string{"items"},
			},
			ExampleJSON: `{"type":"progress-bar","props":{"title":"Storage Usage","items":[{"label":"Disk","value":72,"color":"warning"},{"label":"Memory","value":45,"color":"success"}]}}`,
		},
		{
			Type:        "badges",
			Description: "Inline colored status badges/pills",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"text":  map[string]interface{}{"type": "string"},
								"color": map[string]interface{}{"type": "string"},
							},
							"required": []string{"text", "color"},
						},
					},
				},
				"required": []string{"items"},
			},
			ExampleJSON: `{"type":"badges","props":{"items":[{"text":"Production","color":"success"},{"text":"v2.1.0","color":"accent"},{"text":"3 Warnings","color":"warning"}]}}`,
		},
		{
			Type:        "heatmap",
			Description: "Color-coded grid with row labels, column labels, and numeric values",
			Category:    "display",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":        map[string]interface{}{"type": "string"},
					"rowLabels":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"columnLabels": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"values":       map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}}},
					"colorScale":   map[string]interface{}{"type": "string", "description": "Color scale: 'blue', 'green', 'red' (default: 'blue')"},
				},
				"required": []string{"rowLabels", "columnLabels", "values"},
			},
			ExampleJSON: `{"type":"heatmap","props":{"title":"Query Latency (ms)","rowLabels":["Mon","Tue","Wed"],"columnLabels":["Morning","Afternoon","Evening"],"values":[[120,150,90],[200,180,110],[95,130,85]]}}`,
		},

		// Layout components
		{
			Type:        "header",
			Description: "App header with title, optional description and badge",
			Category:    "layout",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string"},
					"description": map[string]interface{}{"type": "string"},
					"badge":       map[string]interface{}{"type": "string"},
				},
				"required": []string{"title"},
			},
			ExampleJSON: `{"type":"header","props":{"title":"Revenue Analysis","description":"Q1 2026 Summary","badge":"Live"}}`,
		},
		{
			Type:        "section",
			Description: "Collapsible section grouping child components",
			Category:    "layout",
			HasChildren: true,
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string"},
					"subtitle":    map[string]interface{}{"type": "string"},
					"collapsible": map[string]interface{}{"type": "boolean"},
				},
				"required": []string{"title"},
			},
			ExampleJSON: `{"type":"section","props":{"title":"Details","collapsible":true},"children":[{"type":"text","props":{"content":"Section content here"}}]}`,
		},
		{
			Type:        "tabs",
			Description: "Tab bar with child components per tab",
			Category:    "layout",
			HasChildren: true,
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tabs": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"label": map[string]interface{}{"type": "string"},
							},
							"required": []string{"label"},
						},
					},
				},
				"required": []string{"tabs"},
			},
			ExampleJSON: `{"type":"tabs","props":{"tabs":[{"label":"Overview"},{"label":"Details"}]},"children":[{"type":"text","props":{"content":"Overview tab"}},{"type":"text","props":{"content":"Details tab"}}]}`,
		},

		// Complex components
		{
			Type:        "dag",
			Description: "Directed acyclic graph (SVG) with nodes and edges",
			Category:    "complex",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string"},
					"nodes": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id":       map[string]interface{}{"type": "string"},
								"label":    map[string]interface{}{"type": "string"},
								"sublabel": map[string]interface{}{"type": "string"},
								"color":    map[string]interface{}{"type": "string"},
							},
							"required": []string{"id", "label"},
						},
					},
					"edges": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"from": map[string]interface{}{"type": "string"},
								"to":   map[string]interface{}{"type": "string"},
							},
							"required": []string{"from", "to"},
						},
					},
				},
				"required": []string{"nodes", "edges"},
			},
			ExampleJSON: `{"type":"dag","props":{"title":"Pipeline","nodes":[{"id":"a","label":"Extract"},{"id":"b","label":"Transform"},{"id":"c","label":"Load"}],"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"}]}}`,
		},
		{
			Type:        "message-list",
			Description: "Conversation message list with role-based styling",
			Category:    "complex",
			PropsSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"messages": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"role":      map[string]interface{}{"type": "string", "enum": []string{"user", "assistant", "system", "tool"}},
								"content":   map[string]interface{}{"type": "string"},
								"timestamp": map[string]interface{}{"type": "string"},
							},
							"required": []string{"role", "content"},
						},
					},
				},
				"required": []string{"messages"},
			},
			ExampleJSON: `{"type":"message-list","props":{"messages":[{"role":"user","content":"Show me revenue trends"},{"role":"assistant","content":"Here's the analysis..."}]}}`,
		},
	}

	byType := make(map[string]*componentEntry, len(entries))
	for i := range entries {
		byType[entries[i].Type] = &entries[i]
	}

	return &ComponentCatalog{
		entries: entries,
		byType:  byType,
	}
}

// IsValidType checks whether a component type exists in the catalog.
func (c *ComponentCatalog) IsValidType(typ string) bool {
	_, ok := c.byType[typ]
	return ok
}

// HasChildren returns true if the component type supports children.
func (c *ComponentCatalog) HasChildren(typ string) bool {
	entry, ok := c.byType[typ]
	if !ok {
		return false
	}
	return entry.HasChildren
}

// ValidTypes returns the sorted list of valid component type names.
func (c *ComponentCatalog) ValidTypes() []string {
	types := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		types = append(types, e.Type)
	}
	return types
}

// ToProto converts the catalog to proto ComponentType messages for the
// ListComponentTypes RPC.
func (c *ComponentCatalog) ToProto() []*loomv1.ComponentType {
	result := make([]*loomv1.ComponentType, 0, len(c.entries))
	for _, e := range c.entries {
		ct := &loomv1.ComponentType{
			Type:        e.Type,
			Description: e.Description,
			Category:    e.Category,
			ExampleJson: e.ExampleJSON,
		}

		// Convert props schema map to protobuf Struct
		if e.PropsSchema != nil {
			schemaJSON, err := json.Marshal(e.PropsSchema)
			if err == nil {
				var s structpb.Struct
				if err := s.UnmarshalJSON(schemaJSON); err == nil {
					ct.PropsSchema = &s
				}
			}
		}

		result = append(result, ct)
	}
	return result
}
