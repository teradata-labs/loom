# Loom Workflow Examples

This directory contains example workflow YAML files demonstrating the orchestration patterns in Loom.

## Overview

Loom workflows use a Kubernetes-style YAML structure with `apiVersion`, `kind`, `metadata`, and `spec` fields. Each workflow defines an orchestration pattern for coordinating multiple AI agents with well-defined roles and system prompts.

## YAML Structure

```yaml
apiVersion: loom/v1      # Always loom/v1
kind: Workflow           # Always Workflow
metadata:
  name: my-workflow      # Required: Unique workflow name
  description: "..."     # Required: Human-readable description
  labels:                # Optional: Key-value labels
    category: design
    complexity: medium
spec:
  pattern: <pattern>     # Required: Pattern type (pipeline, fork_join, parallel, debate, conditional)
  agents:                # Required: Agent definitions (inline, by ID, or by path)
    - id: agent-id
      # Option 1: Inline definition
      name: Agent Name
      system_prompt: |
        Agent's role and instructions...
      # Option 2: Load from file
      # path: ../agents/agent-config.yaml
      # Option 3: Registry reference (minimal config, loaded at runtime)
      prompt_template: |  # Optional: Template for agent's input
        Process {{previous}} or {{user_query}}
      role: debater      # Optional: For debate/conditional patterns
  config:                # Optional: Pattern-specific configuration
    merge_strategy: concatenate
    rounds: 3
orchestration:           # Optional: Orchestration settings
  timeout_seconds: 300
  pass_full_history: false
```

## Supported Patterns

### ✅ Implemented Patterns

#### 1. Pipeline Pattern
**File**: `feature-pipeline.yaml`

Sequential execution where each stage's output becomes the next stage's input.

```yaml
spec:
  pattern: pipeline
  agents:
    - id: architect
      name: API Architect
      system_prompt: "Design APIs with best practices..."
      prompt_template: "Based on: {{previous}}\n\nDesign API specification..."
    - id: developer
      name: Backend Developer
      system_prompt: "Implement robust systems..."
      prompt_template: "Based on: {{previous}}\n\nImplement backend code..."
orchestration:
  pass_full_history: false  # Each stage only sees previous output
```

**Use Cases**:
- Feature implementation (design → code → test)
- Content refinement (draft → edit → review)
- Sequential transformations

#### 2. Fork-Join Pattern
**File**: `code-review.yaml`

Agents execute in parallel on the same prompt, results are merged.

```yaml
spec:
  pattern: fork_join
  agents:
    - id: quality-reviewer
      name: Code Quality Reviewer
      system_prompt: "Review code for maintainability..."
    - id: security-reviewer
      name: Security Reviewer
      system_prompt: "Identify security vulnerabilities..."
  config:
    merge_strategy: concatenate
orchestration:
  timeout_seconds: 300
```

**Use Cases**:
- Multi-perspective code reviews
- Parallel analysis (quality, security, performance)
- Risk assessment

#### 3. Parallel Pattern
**File**: `doc-generation.yaml`, `security-analysis.yaml`

Independent tasks execute in parallel with agent-specific prompts.

```yaml
spec:
  pattern: parallel
  agents:
    - id: api-documenter
      name: API Documenter
      system_prompt: "Create API documentation..."
      prompt_template: "Generate API docs:\n\n{{user_query}}"
    - id: technical-writer
      name: Technical Writer
      system_prompt: "Write user guides..."
      prompt_template: "Write user guide:\n\n{{user_query}}"
  config:
    merge_strategy: concatenate
```

**Use Cases**:
- Documentation generation (API docs, user guide, examples)
- Security analysis (SAST, DAST, threat modeling)
- Independent parallel tasks

#### 4. Debate Pattern
**File**: `architecture-debate.yaml`

Multiple agents debate a topic and reach consensus through structured rounds.

```yaml
spec:
  pattern: debate
  agents:
    - id: architect-advocate
      name: Architect Advocate
      role: debater
      system_prompt: "Advocate for best practices..."
    - id: pragmatist-engineer
      name: Pragmatist Engineer
      role: debater
      system_prompt: "Advocate for practical solutions..."
    - id: senior-architect
      name: Senior Architect
      role: moderator
      system_prompt: "Moderate the debate and synthesize..."
  config:
    rounds: 3
```

**Use Cases**:
- Architecture decisions
- Design trade-offs
- Strategic planning

#### 5. Conditional Pattern
**File**: `complexity-routing.yaml`

Routes execution based on a classifier agent's decision.

```yaml
spec:
  pattern: conditional
  agents:
    - id: complexity-classifier
      name: Complexity Classifier
      role: classifier
      system_prompt: "Classify feature complexity as: simple, medium, or complex"
      prompt_template: "Classify this request:\n\n{{user_query}}"
```

**Limitations**:
- Nested workflows (branches with sub-workflows) are not yet supported
- Use for simple classification/routing decisions
- For complex routing, use separate workflow files

**Use Cases**:
- Feature complexity routing
- Request type classification
- Binary decision-making

#### 6. Swarm Pattern
**File**: `technology-swarm.yaml`

Collective decision-making through voting with multiple strategies.

```yaml
spec:
  pattern: swarm
  agents:
    - id: database-expert
      name: Database Expert
      system_prompt: "Evaluate database options..."
    - id: performance-engineer
      name: Performance Engineer
      system_prompt: "Analyze performance characteristics..."
    # ... more voting agents
    - id: senior-architect
      name: Senior Architect
      role: judge
      system_prompt: "Break ties and synthesize final recommendation..."
  config:
    strategy: majority  # Options: majority, supermajority, unanimous
    confidence_threshold: 0.7
    share_votes: false  # If true, agents see previous votes (sequential voting)
```

**Voting Strategies**:
- `majority`: > 50% agreement required
- `supermajority`: ≥ 66.7% agreement required
- `unanimous`: 100% agreement required

**Use Cases**:
- Technology selection decisions
- Architecture choices
- Policy decisions requiring consensus

## Variable Interpolation

Workflows support variable interpolation using `{{variable_name}}` syntax:

- `{{user_query}}`: The initial prompt/question provided at runtime
- `{{previous}}`: Output from the previous stage (pipeline pattern)
- Custom variables: Pass via `variables` parameter in ExecuteWorkflow RPC

Example:
```yaml
agents:
  - id: analyzer
    prompt_template: |
      Analyze {{language}} code for {{issue_type}}:

      {{user_query}}
```

## Agent Specification

Workflows support three ways to specify agents, allowing you to choose the approach that best fits your needs:

### 1. Inline Definition (Self-Contained)

Define agents directly in the workflow file with full configuration:

```yaml
spec:
  agents:
    - id: my-agent
      name: My Agent
      system_prompt: |
        You are a specialized agent with these capabilities...
      tools:
        - tool_1
        - tool_2
      prompt_template: "Process: {{user_query}}"
```

**Pros**: Self-contained, easy to understand, no external dependencies
**Cons**: Verbose for complex agents, not reusable across workflows

### 2. By ID (Registry Reference)

Reference agents by ID that will be loaded from the agent registry at runtime:

```yaml
spec:
  agents:
    - id: existing-agent-id
      prompt_template: "Analyze: {{user_query}}"
      # Agent name and system_prompt loaded from registry
```

**Pros**: Reuses existing agents, minimal configuration
**Cons**: Requires agent to exist in registry, less explicit

### 3. By Path (Load from File)

Load agent configuration from an external YAML file:

```yaml
spec:
  agents:
    - id: my-agent
      path: ../agents/code_reviewer.yaml
      # All configuration loaded from file

    - id: another-agent
      path: agents/sql_expert.yaml
      # Can override specific fields
      name: Custom SQL Reviewer
      prompt_template: "Review SQL: {{user_query}}"
```

**Agent config file structure** (`agents/code_reviewer.yaml`):
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: Code Reviewer
  version: 1.0.0
  description: Reviews code for quality and maintainability
spec:
  system_prompt: |
    You are an expert code reviewer...
  tools:
    - code_analysis
    - lint_check
```

**Path resolution**:
- Relative paths resolved relative to workflow file directory
- Absolute paths used as-is
- Example: If workflow is at `workflows/my-workflow.yaml` and references `../agents/agent.yaml`, resolved to `agents/agent.yaml`

**Field overriding**:
- Workflow can override: `name`, `system_prompt`, `role`, `prompt_template`
- Agent config provides base: `name`, `system_prompt`, `tools`
- Overrides allow customization without modifying shared agent configs

**Pros**: Reusable configs, separation of concerns, easy to maintain
**Cons**: Requires managing separate files, path dependencies

### Mixed Approach (Recommended)

Combine all three approaches in a single workflow:

```yaml
spec:
  pattern: fork_join
  agents:
    # Path-referenced agent with override
    - id: quality-reviewer
      path: ../agents/code_reviewer.yaml
      role: quality

    # Registry reference
    - id: security-expert
      prompt_template: "Security review: {{user_query}}"

    # Inline agent
    - id: performance-reviewer
      name: Performance Reviewer
      system_prompt: |
        You specialize in performance analysis...
```

**Example**: See `code-review-with-paths.yaml` for a real-world example mixing inline and path-referenced agents.

## Executing Workflows

### Via gRPC

```go
import loomv1 "github.com/Teradata-TIO/loom/gen/go/loom/v1"

// Parse workflow from YAML
pattern, metadata, err := orchestration.ParseWorkflowFromYAML("workflow.yaml")

// Execute with variables
result, err := client.ExecuteWorkflow(ctx, &loomv1.ExecuteWorkflowRequest{
    Pattern: pattern,
    Variables: map[string]string{
        "language": "Go",
        "issue_type": "security vulnerabilities",
    },
})
```

### Via CLI

```bash
# Execute workflow from YAML file
looms workflow run workflow.yaml

# Dry-run (validate without executing)
looms workflow run --dry-run feature-pipeline.yaml

# With timeout (default: 3600 seconds)
looms workflow run --timeout=1800 code-review.yaml
```

## Best Practices

### 1. Agent System Prompts
- Be specific about the agent's role and expertise
- Include clear instructions on what to focus on
- Specify output format expectations
- Add reasoning guidance

### 2. Prompt Templates
- Use `{{user_query}}` to reference the input
- Use `{{previous}}` for pipeline stages
- Keep templates focused and actionable
- Avoid redundancy with system_prompt

### 3. Merge Strategies
- `concatenate`: Simple concatenation of all outputs
- `summary`: LLM-generated summary of all outputs
- `consensus`: Extract common agreement (debate pattern)
- `first`: Take first agent's output (early exit)
- `best`: LLM selects best output based on criteria

### 4. Pattern Selection
- **Pipeline**: Sequential dependencies, each stage builds on previous
- **Fork-Join**: Same task, multiple perspectives, merge results
- **Parallel**: Independent tasks, different prompts, combine outputs
- **Debate**: Opposing viewpoints, need consensus through discussion
- **Conditional**: Simple routing based on classification
- **Swarm**: Collective voting for decision-making with confidence thresholds

## Testing Workflows

Run the validation tests to ensure workflows parse correctly:

```bash
cd examples/reference/workflows
go test -tags fts5 -v .
```

All example workflows in this directory must pass validation tests.

## Pattern Implementation Status

| Pattern | YAML Parsing | Execution | Example | Status |
|---------|--------------|-----------|---------|--------|
| Pipeline | ✅ | ✅ | feature-pipeline.yaml | Complete |
| Fork-Join | ✅ | ✅ | code-review.yaml | Complete |
| Parallel | ✅ | ✅ | doc-generation.yaml | Complete |
| Debate | ✅ | ✅ | architecture-debate.yaml | Complete |
| Conditional | ✅ | ✅ | complexity-routing.yaml | Partial (no nested workflows) |
| Swarm | ✅ | ✅ | technology-swarm.yaml | Complete |
| Iterative | ❌ | ✅ | - | YAML parsing TODO |
| Pair Programming | ❌ | ✅ | - | YAML parsing TODO |
| Teacher-Student | ❌ | ✅ | - | YAML parsing TODO |

## Contributing

When adding new workflow examples:

1. Follow the YAML structure defined in this README
2. Provide comprehensive system prompts for each agent
3. Add validation test in `validate_test.go`
4. Document the use case and pattern rationale
5. Include example usage in comments
6. Run `go test -tags fts5 -v .` to validate

## Questions?

- See `pkg/orchestration/yaml_parser.go` for implementation details
- See `proto/loom/v1/` for pattern definitions
- See test files for parsing examples
- See `pkg/orchestration/orchestrator.go` for execution logic
