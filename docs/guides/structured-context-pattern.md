
# Structured Context Pattern

**Version**: v1.2.0 | **Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [How It Works](#how-it-works)
- [Common Tasks](#common-tasks)
  - [Initialize Context](#initialize-context)
  - [Add Stage Outputs](#add-stage-outputs)
  - [Validate References](#validate-references)
  - [Validate Database Lists](#validate-database-lists)
  - [Extract Target Tables](#extract-target-tables)
  - [Validate Tool Executions](#validate-tool-executions)
  - [Validate File Creation](#validate-file-creation)
  - [Inject into Prompts](#inject-into-prompts)
- [Examples](#examples)
  - [Example 1: Database Discovery Workflow](#example-1-database-discovery-workflow)
  - [Example 2: Workflow YAML Integration](#example-2-workflow-yaml-integration)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)


## Overview

Pass structured JSON between workflow stages with mandatory validation to prevent LLM hallucinations. When Stage 6 tries to reference a table that Stage 2 never discovered, the validation fails before any query executes.

## Prerequisites

- Loom v1.2.0+
- Multi-stage workflow defined (iterative or pipeline pattern)
- Understanding of workflow orchestration

## Quick Start

```go
import "github.com/teradata-labs/loom/pkg/orchestration"

// Create context
ctx := orchestration.NewStructuredContext("workflow-123", "npath-discovery")

// Stage 2 outputs recommended table
err := ctx.AddStageOutput("stage-2", orchestration.StageOutput{
    StageID: "td-expert-stage-2",
    Status:  "completed",
    Outputs: map[string]interface{}{
        "recommended_table": map[string]interface{}{
            "database": "demo",
            "table":    "bank_events",
        },
    },
})
if err != nil {
    // Handle error (StageID and Status are required fields)
}

// Stage 6 validates before querying
err = ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-2")
if err != nil {
    // Validation failed - agent tried to use wrong table
}
```


## How It Works

Structured context is **automatically created** by the `IterativePipelineExecutor` when running iterative workflows. It initializes a `StructuredContext` at the start of pipeline execution and uses the `{{structured_context}}` template variable to inject context JSON into stage prompts.

There is no YAML configuration required to enable it -- it is built into the iterative pipeline execution path. To use structured context in prompts, include the `{{structured_context}}` placeholder in your stage `prompt_template` values.

For programmatic use outside the executor, create a `StructuredContext` directly via the Go API as shown in the Quick Start above.


## Common Tasks

### Initialize Context

```go
import "github.com/teradata-labs/loom/pkg/orchestration"

ctx := orchestration.NewStructuredContext(
    "npath-v3.7-run-12345",  // workflow_id
    "npath-discovery",       // workflow_type
)
```

### Add Stage Outputs

```go
stage1Output := orchestration.StageOutput{
    StageID:     "td-expert-analytics-stage-1",
    Status:      "completed",
    StartedAt:   time.Now(),
    CompletedAt: time.Now().Add(5 * time.Second),
    Inputs: map[string]interface{}{
        "databases_from": "stage-1",
    },
    Outputs: map[string]interface{}{
        "discovered_databases": []interface{}{"demo", "data_scientist", "financial"},
        "database_count":       3,
    },
    Evidence: orchestration.Evidence{
        ToolCalls: []orchestration.ToolCall{
            {
                ToolName:      "teradata_query",
                Parameters:    map[string]interface{}{"query": "SELECT DatabaseName FROM DBC.Databases"},
                ResultSummary: "3 databases returned",
            },
        },
    },
}

err := ctx.AddStageOutput("stage-1", stage1Output)
if err != nil {
    // StageID and Status are required; returns error if missing
    return err
}

// Retrieve a stage output later
output, exists := ctx.GetStageOutput("stage-1")
if !exists {
    // Stage key not found
}
```

### Validate References

Prevent hallucinated table references. Checks that the database and table match what a source stage recommended:

```go
// Stage 6 wants to profile a table
// Validate against Stage 2's recommendation
err := ctx.ValidateTableReference(
    "stage-6",           // current stage
    "demo",              // database to validate
    "bank_events",       // table to validate
    "stage-2",           // source stage
)

if err != nil {
    // Error: "database mismatch: current stage references 'val_telco_churn'
    //         but source stage 'stage-2' recommended 'demo'"
}
```

### Validate Database Lists

Check if a database name was actually discovered by a previous stage:

```go
err := ctx.ValidateDatabaseList("demo", "stage-1")
if err != nil {
    // Error: "database 'unknown_db' not found in source stage 'stage-1' discovered_databases list"
}
```

### Extract Target Tables

Extract the recommended table from a stage without manual map traversal:

```go
database, table, err := ctx.GetTargetTable("stage-2")
if err != nil {
    return err
}
// database = "demo", table = "bank_events"
```

### Validate Tool Executions

Detect action hallucination -- when an agent claims to have done something but executed zero tools:

```go
err := ctx.ValidateToolExecutions("stage-10", []string{"generate_visualization", "group_by_query"})
if err != nil {
    // Error: "stage 'stage-10' executed zero tools - possible action hallucination"
    // Or: "stage 'stage-10' missing required tool executions: [top_n_query]"
}
```

### Validate File Creation

Verify that a file an agent claims to have created actually exists on disk:

```go
err := ctx.ValidateFileCreation("stage-10", "report_path")
if err != nil {
    // Error: "stage 'stage-10' claimed to create '/tmp/report.html' but file does not exist - action hallucination detected"
}
```

### Inject into Prompts

In workflow YAML, use the `{{structured_context}}` placeholder in `prompt_template` -- the pipeline executor replaces it automatically.

For programmatic use:

```go
// Serialize context to JSON
jsonStr, err := ctx.ToJSON()
if err != nil {
    return err
}

// Replace template variable in prompt
prompt := strings.ReplaceAll(promptTemplate, "{{structured_context}}", jsonStr)

// Deserialize context from JSON (e.g., when parsing agent outputs)
ctx2 := &orchestration.StructuredContext{}
err = ctx2.FromJSON(jsonStr)
```

### Validate Output Structure

Validate that an agent's output conforms to the expected structured format (supports both JSON and XML):

```go
err := orchestration.ValidateOutputStructure(agentOutput)
if err != nil {
    // Error: "no JSON object found in output"
    // Or: "missing required field: 'stage_id'"
}
```


## Examples

### Example 1: Database Discovery Workflow

```go
func runDiscoveryWorkflow() error {
    ctx := orchestration.NewStructuredContext("workflow-123", "discovery")

    // Stage 1: Discover databases
    stage1 := orchestration.StageOutput{
        StageID: "stage-1",
        Status:  "completed",
        Outputs: map[string]interface{}{
            "discovered_databases": []interface{}{"demo", "finance"},
        },
    }
    if err := ctx.AddStageOutput("stage-1", stage1); err != nil {
        return err
    }

    // Stage 2: Recommend table from discovered databases
    stage2 := orchestration.StageOutput{
        StageID: "stage-2",
        Status:  "completed",
        Outputs: map[string]interface{}{
            "recommended_table": map[string]interface{}{
                "database": "demo",
                "table":    "bank_events",
            },
        },
    }
    if err := ctx.AddStageOutput("stage-2", stage2); err != nil {
        return err
    }

    // Stage 6: Validate before profiling
    // This PASSES - demo.bank_events was recommended by stage-2
    err := ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-2")
    if err != nil {
        return err
    }

    // This FAILS - val_telco_churn was never recommended
    err = ctx.ValidateTableReference("stage-6", "val_telco_churn", "customers", "stage-2")
    // Error: "database mismatch: current stage references 'val_telco_churn'
    //         but source stage 'stage-2' recommended 'demo'"

    return nil
}
```

### Example 2: Workflow YAML Integration

Structured context is automatically available in iterative workflows. Use the `{{structured_context}}` placeholder in `prompt_template` to inject the accumulated context JSON.

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: npath-discovery
spec:
  type: iterative
  max_iterations: 3
  restart_policy:
    enabled: true
    max_validation_retries: 2

  pipeline:
    initial_prompt: "Discover databases for nPath analysis"
    pass_full_history: false
    stages:
    - agent_id: td-expert-stage-2
      prompt_template: |
        ## STAGE 2: FIND CANDIDATE TABLES

        **CONTEXT VALIDATION:**
        Extract databases from Stage 1:
        ```json
        {{structured_context}}
        ```

        **You MUST use ONLY databases from:**
        stage_outputs.stage-1.outputs.discovered_databases

        **MANDATORY OUTPUT FORMAT** (valid JSON required):
        ```json
        {
          "stage_id": "td-expert-stage-2",
          "status": "completed",
          "outputs": {
            "recommended_table": {
              "database": "demo",
              "table": "bank_events"
            }
          }
        }
        ```

    - agent_id: td-expert-stage-6
      prompt_template: |
        **CONTEXT VALIDATION:**
        Extract target table from:
        ```json
        {{structured_context}}
        ```

        Target: stage_outputs.stage-2.outputs.recommended_table

        **You MUST use ONLY this table - do NOT invent others**
```

**Template variables available in stage prompts:**

| Variable | Description |
|---|---|
| `{{previous}}` | Output from the immediately preceding stage |
| `{{history}}` | All previous stage outputs concatenated |
| `{{structured_context}}` | Full structured context JSON with metadata and evidence |


## Troubleshooting

### Agent Outputs Invalid JSON

**Problem**: Agent produces prose instead of JSON

**Solution**: Add strict format requirements to prompt. The `ValidateOutputStructure` function accepts two formats:

- **Flat format** (preferred): `{"stage_id": "...", "outputs": {...}}`
- **Nested format**: `{"stage_outputs": {"stage-N": {"stage_id": "...", "status": "...", "outputs": {...}}}}`

```yaml
prompt_template: |
  **CRITICAL**: You MUST output ONLY valid JSON. No prose before or after.

  **BAD** (will fail validation):
  "I found 3 databases. Here's the JSON: {...}"

  **GOOD** (will pass validation):
  ```json
  {"stage_id": "td-expert-stage-1", "outputs": {"databases": ["demo"]}}
  ```
```

### Validation Too Strict

**Problem**: Valid references rejected

**Solution**: Check source stage key matches exactly:

```go
// Correct
err := ctx.ValidateTableReference("stage-6", db, table, "stage-2")

// Wrong (not hyphenated)
err := ctx.ValidateTableReference("stage-6", db, table, "Stage 2")
```

### Context Too Large

**Problem**: JSON context exceeds token limits

**Solution**: Use selective stage outputs. The `pass_full_history` field controls whether all previous outputs are appended when no `{{previous}}` or `{{history}}` placeholder is present:

```yaml
spec:
  type: iterative
  pipeline:
    initial_prompt: "Start workflow"
    pass_full_history: false  # Only pass {{previous}}, not all history
    stages:
      # Stage 9 uses {{history}} to create summary
      - agent_id: summarizer
        prompt_template: "Summarize: {{history}}"
      # Stage 10 uses {{previous}} to get summary only
      - agent_id: presenter
        prompt_template: "Present: {{previous}}"
```

### Database Mismatch Error

**Error:**
```
database mismatch: current stage references 'val_telco_churn'
but source stage 'stage-2' recommended 'demo'
```

**Cause**: Agent hallucinated a database name from training data

**Solution**: This error is working as intended. The validation caught the hallucination. Review the agent prompt to ensure it extracts the exact database/table from the structured context JSON.


## Next Steps

- [Iterative Workflow Reference](/docs/reference/workflow-iterative.md) - Full reference for iterative pipeline configuration
- [Multi-Agent Architecture](/docs/architecture/multi-agent.md) - How multi-agent workflows are orchestrated
- [Data Flows](/docs/architecture/data-flows.md) - How data moves between workflow stages
