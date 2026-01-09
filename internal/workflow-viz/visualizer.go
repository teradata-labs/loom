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
package workflowviz

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"strings"
)

// ChartNode represents an ECharts graph node
type ChartNode struct {
	Name       string         `json:"name"`
	Category   int            `json:"category"`
	Value      int            `json:"value"`
	SymbolSize int            `json:"symbolSize"`
	Tooltip    NodeTooltip    `json:"tooltip"`
	Label      NodeLabel      `json:"label"`
	ItemStyle  *NodeItemStyle `json:"itemStyle,omitempty"`
}

// NodeTooltip represents node tooltip configuration
type NodeTooltip struct {
	Formatter string `json:"formatter"`
}

// NodeLabel represents node label configuration
type NodeLabel struct {
	Show       bool   `json:"show"`
	FontSize   int    `json:"fontSize"`
	FontWeight string `json:"fontWeight,omitempty"`
}

// NodeItemStyle represents node visual style
type NodeItemStyle struct {
	BorderColor string `json:"borderColor,omitempty"`
	BorderWidth int    `json:"borderWidth,omitempty"`
}

// ChartLink represents an ECharts graph link
type ChartLink struct {
	Source    string     `json:"source"`
	Target    string     `json:"target"`
	LineStyle *LineStyle `json:"lineStyle,omitempty"`
	Label     *EdgeLabel `json:"label,omitempty"`
}

// LineStyle represents edge line style
type LineStyle struct {
	Type  string `json:"type"`
	Color string `json:"color"`
	Width int    `json:"width"`
}

// EdgeLabel represents edge label
type EdgeLabel struct {
	Show      bool   `json:"show"`
	Formatter string `json:"formatter"`
}

// Category represents a node category
type Category struct {
	Name      string     `json:"name"`
	ItemStyle *ItemStyle `json:"itemStyle"`
}

// ItemStyle represents node item style
type ItemStyle struct {
	Color string `json:"color"`
}

// VisualizationData holds all data needed for HTML template
type VisualizationData struct {
	Title          string
	Subtitle       string
	ChartTitle     string
	ChartSubtitle  string
	NodesJSON      template.JS
	LinksJSON      template.JS
	CategoriesJSON template.JS
	Categories     []struct{ Name, Color string }
}

// GenerateVisualization creates nodes, links, and categories from workflow
func GenerateVisualization(workflow *Workflow) (*VisualizationData, error) {
	nodes, categoryMap := generateNodes(workflow.Spec.Pipeline.Stages)
	links := generateLinks(nodes, workflow.Spec.Pipeline.Stages)
	categories := generateCategories(categoryMap)

	// Marshal to JSON
	nodesJSON, err := json.MarshalIndent(nodes, "        ", "    ")
	if err != nil {
		return nil, fmt.Errorf("marshaling nodes: %w", err)
	}

	linksJSON, err := json.MarshalIndent(links, "        ", "    ")
	if err != nil {
		return nil, fmt.Errorf("marshaling links: %w", err)
	}

	categoriesJSON, err := json.MarshalIndent(categories, "        ", "    ")
	if err != nil {
		return nil, fmt.Errorf("marshaling categories: %w", err)
	}

	// Build visualization data
	// Note: template.JS is used to safely embed JSON in JavaScript context.
	// The JSON is generated from trusted workflow YAML config files via json.MarshalIndent()
	// which properly escapes special characters.
	data := &VisualizationData{
		Title:          fmt.Sprintf("%s - v%s", workflow.Metadata.Name, workflow.Metadata.Version),
		Subtitle:       fmt.Sprintf("%d Stages | %s", len(workflow.Spec.Pipeline.Stages), workflow.Metadata.Description),
		ChartTitle:     workflow.Metadata.Name,
		ChartSubtitle:  fmt.Sprintf("v%s | %d stages | %s", workflow.Metadata.Version, len(workflow.Spec.Pipeline.Stages), workflow.Spec.Type),
		NodesJSON:      template.JS(nodesJSON),      // #nosec G203 -- JSON from trusted workflow config
		LinksJSON:      template.JS(linksJSON),      // #nosec G203 -- JSON from trusted workflow config
		CategoriesJSON: template.JS(categoriesJSON), // #nosec G203 -- JSON from trusted workflow config
		Categories:     []struct{ Name, Color string }{},
	}

	// Add category legend
	colorMap := getCategoryColors()
	for cat, name := range categoryMap {
		data.Categories = append(data.Categories, struct{ Name, Color string }{
			Name:  name,
			Color: colorMap[cat],
		})
	}

	return data, nil
}

func generateNodes(stages []WorkflowStage) ([]ChartNode, map[int]string) {
	nodes := []ChartNode{}
	categoryMap := make(map[int]string)

	for i, stage := range stages {
		stageNum := i + 1
		category, _, categoryName := CategorizeAgent(stage.AgentID)
		categoryMap[category] = categoryName

		stageTitle := ExtractStageTitle(stage.PromptTemplate)
		markers := ExtractKeyMarkers(stage.PromptTemplate)
		instructions := ExtractKeyInstructions(stage.PromptTemplate)

		// Build tooltip
		tooltipParts := []string{
			fmt.Sprintf("<b>Stage %d: %s</b>", stageNum, stageTitle),
			fmt.Sprintf("Agent: %s", stage.AgentID),
		}
		if len(markers) > 0 {
			tooltipParts = append(tooltipParts, fmt.Sprintf("Markers: %s", strings.Join(markers, ", ")))
		}
		for _, instr := range instructions {
			tooltipParts = append(tooltipParts, strings.TrimSpace(instr))
		}
		tooltip := strings.Join(tooltipParts, "<br/>")

		// Determine node size and style based on markers
		symbolSize := 70
		fontWeight := "normal"
		itemStyle := (*NodeItemStyle)(nil)

		if containsMarker(markers, "CRITICAL") || containsMarker(markers, "TOKEN_BUDGET") {
			symbolSize = 90
			fontWeight = "bold"
			itemStyle = &NodeItemStyle{BorderColor: "#f37021", BorderWidth: 3}
		}
		if containsMarker(markers, "MERGED") || containsMarker(markers, "FULL_HISTORY") {
			symbolSize = 100
			fontWeight = "bold"
			itemStyle = &NodeItemStyle{BorderColor: "#f37021", BorderWidth: 4}
		}

		nodeName := fmt.Sprintf("Stage %d\n%s", stageNum, stageTitle)

		nodes = append(nodes, ChartNode{
			Name:       nodeName,
			Category:   category,
			Value:      stageNum,
			SymbolSize: symbolSize,
			Tooltip: NodeTooltip{
				Formatter: tooltip,
			},
			Label: NodeLabel{
				Show:       true,
				FontSize:   12,
				FontWeight: fontWeight,
			},
			ItemStyle: itemStyle,
		})
	}

	return nodes, categoryMap
}

func generateLinks(nodes []ChartNode, stages []WorkflowStage) []ChartLink {
	links := []ChartLink{}

	// Sequential flow links
	for i := 0; i < len(nodes)-1; i++ {
		links = append(links, ChartLink{
			Source: nodes[i].Name,
			Target: nodes[i+1].Name,
		})
	}

	// Shared memory connections
	memoryConnections := FindSharedMemoryConnections(stages)
	for _, conn := range memoryConnections {
		if conn.From < len(nodes) && conn.To < len(nodes) {
			links = append(links, ChartLink{
				Source: nodes[conn.From].Name,
				Target: nodes[conn.To].Name,
				LineStyle: &LineStyle{
					Type:  "dashed",
					Color: "#f37021",
					Width: 3,
				},
				Label: &EdgeLabel{
					Show:      true,
					Formatter: "shared_memory",
				},
			})
		}
	}

	return links
}

func generateCategories(categoryMap map[int]string) []Category {
	categories := []Category{}
	colorMap := getCategoryColors()

	for cat, name := range categoryMap {
		categories = append(categories, Category{
			Name: name,
			ItemStyle: &ItemStyle{
				Color: colorMap[cat],
			},
		})
	}

	return categories
}

func getCategoryColors() map[int]string {
	return map[int]string{
		0: "#4CAF50", // Analytics
		1: "#2196F3", // Quality
		2: "#FF9800", // Performance
		3: "#9C27B0", // Insights
		4: "#E91E63", // Architecture
		5: "#00BCD4", // Transcend
		6: "#757575", // Other
	}
}

func containsMarker(markers []string, target string) bool {
	for _, m := range markers {
		if m == target {
			return true
		}
	}
	return false
}

// GenerateHTML creates the final HTML file
func GenerateHTML(data *VisualizationData, outputPath string) error {
	tmpl, err := template.New("workflow").Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	if err := tmpl.Execute(outFile, data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	return nil
}

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>{{.Title}}</title>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5.4.0/dist/echarts.min.js"></script>
    <style>
        body {
            font-family: 'IBM Plex Mono', 'Courier New', monospace;
            background: #0a0a0a;
            color: #e0e0e0;
            margin: 0;
            padding: 20px;
        }
        #chart {
            width: 100%;
            height: 800px;
            background: #1a1a1a;
            border-radius: 8px;
            box-shadow: 0 4px 20px rgba(243, 112, 33, 0.2);
        }
        h1 {
            color: #f37021;
            text-align: center;
            margin-bottom: 10px;
            font-weight: bold;
        }
        .info {
            text-align: center;
            color: #888;
            margin-bottom: 20px;
            font-size: 14px;
        }
        .legend {
            background: #2a2a2a;
            padding: 15px;
            border-radius: 8px;
            margin-top: 20px;
            border: 1px solid #333;
        }
        .legend h3 {
            margin-top: 0;
            color: #f37021;
        }
        .legend-item {
            display: inline-block;
            margin-right: 20px;
            margin-bottom: 10px;
        }
        .color-box {
            display: inline-block;
            width: 20px;
            height: 20px;
            margin-right: 8px;
            vertical-align: middle;
            border-radius: 3px;
        }
        .footer {
            text-align: center;
            color: #666;
            margin-top: 20px;
            font-size: 12px;
        }
    </style>
</head>
<body>
    <h1>{{.Title}}</h1>
    <div class="info">{{.Subtitle}}</div>
    <div id="chart"></div>
    <div class="legend">
        <h3>Agent Categories</h3>
        {{range .Categories}}
        <div class="legend-item">
            <span class="color-box" style="background: {{.Color}};"></span>
            <span>{{.Name}}</span>
        </div>
        {{end}}
    </div>
    <div class="footer">
        Generated by Loom Workflow Visualizer | Hover nodes for details | Drag to explore
    </div>

    <script>
        const chart = echarts.init(document.getElementById('chart'));

        const nodes = {{.NodesJSON}};
        const links = {{.LinksJSON}};
        const categories = {{.CategoriesJSON}};

        const option = {
            backgroundColor: '#1a1a1a',
            title: {
                text: '{{.ChartTitle}}',
                subtext: '{{.ChartSubtitle}}',
                top: '2%',
                left: 'center',
                textStyle: {
                    color: '#f37021',
                    fontSize: 20,
                    fontWeight: 'bold',
                    fontFamily: 'IBM Plex Mono, monospace'
                },
                subtextStyle: {
                    color: '#888',
                    fontSize: 14,
                    fontFamily: 'IBM Plex Mono, monospace'
                }
            },
            tooltip: {
                trigger: 'item',
                backgroundColor: 'rgba(20, 20, 20, 0.95)',
                borderColor: '#f37021',
                borderWidth: 2,
                textStyle: {
                    color: '#e0e0e0',
                    fontFamily: 'IBM Plex Mono, monospace',
                    fontSize: 12
                },
                padding: 12
            },
            legend: {
                show: false
            },
            series: [{
                type: 'graph',
                layout: 'force',
                data: nodes,
                links: links,
                categories: categories,
                roam: true,
                draggable: true,
                label: {
                    show: true,
                    position: 'inside',
                    color: '#fff',
                    fontFamily: 'IBM Plex Mono, monospace',
                    fontSize: 11
                },
                force: {
                    repulsion: 400,
                    edgeLength: [100, 200],
                    gravity: 0.1,
                    friction: 0.3
                },
                lineStyle: {
                    color: '#555',
                    width: 2,
                    curveness: 0.1
                },
                emphasis: {
                    focus: 'adjacency',
                    lineStyle: {
                        width: 4,
                        color: '#f37021'
                    },
                    itemStyle: {
                        shadowBlur: 15,
                        shadowColor: '#f37021'
                    }
                },
                edgeSymbol: ['none', 'arrow'],
                edgeSymbolSize: 8
            }]
        };

        chart.setOption(option);

        // Responsive resize
        window.addEventListener('resize', () => {
            chart.resize();
        });

        // Highlight on click
        chart.on('click', function (params) {
            if (params.dataType === 'node') {
                console.log('Node clicked:', params.name);
            }
        });
    </script>
</body>
</html>
`
