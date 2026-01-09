# Workflow Visualization Tool

A production-ready Go tool that converts Loom workflow YAML files into interactive ECharts graph visualizations.

## Features

- Parses Loom workflow YAML files (apiVersion: loom/v1, kind: Workflow)
- Extracts pipeline stages with agent IDs and prompt templates
- Color-codes nodes by agent category (Analytics, Quality, Performance, Insights, Architecture, Transcend)
- Sizes nodes by importance (detects CRITICAL, TOKEN_BUDGET, MERGED, FULL_HISTORY markers)
- Creates interactive HTML with embedded ECharts
- Hawk StyleGuide aesthetic (Teradata Orange #f37021, IBM Plex Mono, dark theme)
- Detects shared_memory connections between stages (displayed as dashed orange links)
- Supports iterative workflows with max_iterations metadata
- Zero external dependencies at runtime (self-contained HTML)

## Installation

```bash
# Build the binary
go build ./cmd/workflow-viz

# Or run directly
go run ./cmd/workflow-viz/main.go <workflow.yaml> <output.html>
```

## Usage

```bash
# Basic usage
./workflow-viz workflow.yaml output.html

# Example with actual workflow
./workflow-viz examples/workflows/workflow-npath-autonomous-v3.6-streamlined.yaml workflow-viz.html

# Open in browser
open workflow-viz.html
```

## Output Example

The tool generates an interactive HTML file with:

- **Title**: Workflow name and version
- **Subtitle**: Stage count and description
- **Interactive Graph**:
  - Hover nodes for details (agent ID, markers, key instructions)
  - Drag to explore
  - Zoom/pan with mouse wheel
  - Click highlights adjacent nodes
  - Sequential flow shown as solid arrows
  - Shared memory connections shown as dashed orange lines
- **Legend**: Agent categories with color coding

## Agent Categories

| Category | Color | Hex Code |
|----------|-------|----------|
| Analytics | Green | #4CAF50 |
| Quality | Blue | #2196F3 |
| Performance | Orange | #FF9800 |
| Insights | Purple | #9C27B0 |
| Architecture | Pink | #E91E63 |
| Transcend | Cyan | #00BCD4 |
| Other | Gray | #757575 |

## Node Sizing Logic

Nodes are sized based on detected markers in the prompt template:

- **Base size (70px)**: Standard stages
- **Critical size (90px)**: Contains `⚠️ CRITICAL` or `TOKEN BUDGET` markers
- **Large size (100px)**: Contains `✅ MERGED` or `{{history}}` (full context) markers

Nodes with critical/merged markers also get an orange border (#f37021, 3-4px width).

## Shared Memory Detection

The tool detects shared memory connections by looking for:

1. Stages that write to shared_memory (`shared_memory_write`)
2. Later stages that read from the same key (`shared_memory_read` or explicit `stage-N` references)

These connections are displayed as **dashed orange lines** with "shared_memory" labels.

## Development

### Project Structure

```
cmd/workflow-viz/
├── main.go           # CLI entry point
└── README.md         # This file

internal/workflow-viz/
├── parser.go         # YAML parsing and stage extraction
├── parser_test.go    # Parser unit tests
├── visualizer.go     # ECharts graph generation
└── visualizer_test.go # Visualizer unit tests
```

### Running Tests

```bash
# Run tests with race detector (required for all commits)
go test -race ./internal/workflow-viz/... -v

# Run with coverage
go test -race -cover ./internal/workflow-viz/...

# Test output:
# - 16 test functions
# - Tests for: parsing, stage extraction, agent categorization, node generation, link creation, HTML generation
# - No race conditions detected
```

### Code Quality

Before committing:

```bash
# Format code
gofmt -s -w ./internal/workflow-viz/ ./cmd/workflow-viz/

# Vet code
go vet ./internal/workflow-viz/...

# Build binary
go build ./cmd/workflow-viz

# Run tests with race detector
go test -race ./internal/workflow-viz/...
```

## Technical Details

### YAML Structure Expected

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: workflow-name
  version: "1.0.0"
  description: Workflow description
  labels:
    category: analysis
spec:
  type: pipeline  # or iterative
  max_iterations: 5  # for iterative workflows
  pipeline:
    initial_prompt: "Initial prompt"
    stages:
      - agent_id: agent-name-stage-1
        prompt_template: |
          ## STAGE 1: STAGE TITLE
          Prompt content with markers
```

### Stage Title Extraction

The tool looks for the pattern `## STAGE N: TITLE` in the prompt template:

```
## STAGE 1: DISCOVER ACCESSIBLE DATABASES
```

Extracts: "DISCOVER ACCESSIBLE DATABASES"

### Key Markers Detected

- `⚠️ CRITICAL` - Critical operation
- `TOKEN BUDGET` or `🔴 TOKEN BUDGET` - Token budget constraints
- `✅ MERGED` - Merged stage (combines multiple operations)
- `{{history}}` - Uses full conversation history
- `shared_memory` - Reads/writes shared memory
- `VOLATILE TABLE` - Uses Teradata volatile tables

## Example Output

For the nPath autonomous workflow v3.6.1:

```
✅ Workflow visualization generated: workflow-v3.6-visualization.html
   Workflow: npath-autonomous-v3.6-iterative v3.6.1
   Stages: 10
   Type: iterative
   Max Iterations: 5
```

The generated HTML includes:
- 10 nodes representing pipeline stages
- Sequential flow arrows connecting stages
- Shared memory connections (e.g., Stage 8 → Stage 10)
- Color-coded by agent type (Analytics, Quality, Performance, Insights)
- Interactive tooltips with stage details

## Error Handling

The tool provides clear error messages for:

- Missing or invalid workflow files
- YAML parsing errors
- Invalid workflow structure
- File system errors

All errors are reported to stderr with context.

## Dependencies

Runtime (embedded in HTML):
- ECharts 5.4.0 (CDN)

Build time:
- gopkg.in/yaml.v3 (YAML parsing)
- html/template (HTML generation)
- Standard library only

## License

Part of the Loom project. See main project LICENSE file.

## Maintainer

Loom development team (Teradata Labs)
