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
package main

import (
	"fmt"
	"os"

	workflowviz "github.com/teradata-labs/loom/internal/workflow-viz"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <workflow.yaml> <output.html>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nGenerates an interactive ECharts visualization of a Loom workflow.\n")
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  %s workflow.yaml output.html\n", os.Args[0])
		os.Exit(1)
	}

	workflowPath := os.Args[1]
	outputPath := os.Args[2]

	// Parse workflow YAML
	workflow, err := workflowviz.ParseWorkflow(workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing workflow: %v\n", err)
		os.Exit(1)
	}

	// Generate visualization data
	data, err := workflowviz.GenerateVisualization(workflow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating visualization: %v\n", err)
		os.Exit(1)
	}

	// Generate HTML file
	if err := workflowviz.GenerateHTML(data, outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating HTML: %v\n", err)
		os.Exit(1)
	}

	// Print success message
	fmt.Printf("✅ Workflow visualization generated: %s\n", outputPath)
	fmt.Printf("   Workflow: %s v%s\n", workflow.Metadata.Name, workflow.Metadata.Version)
	fmt.Printf("   Stages: %d\n", len(workflow.Spec.Pipeline.Stages))
	fmt.Printf("   Type: %s\n", workflow.Spec.Type)
	if workflow.Spec.MaxIterations > 0 {
		fmt.Printf("   Max Iterations: %d\n", workflow.Spec.MaxIterations)
	}
}
