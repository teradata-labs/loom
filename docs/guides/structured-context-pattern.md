
# Structured Context Pattern

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Initialize Context](#initialize-context)
  - [Add Stage Outputs](#add-stage-outputs)
  - [Validate References](#validate-references)
  - [Inject into Prompts](#inject-into-prompts)
- [Examples](#examples)
  - [Example 1: Database Discovery Workflow](#example-1-database-discovery-workflow)
  - [Example 2: Workflow YAML Integration](#example-2-workflow-yaml-integration)
- [Troubleshooting](#troubleshooting)


## Overview

Pass structured JSON between workflow stages with mandatory validation to prevent LLM hallucinations. When Stage 6 tries to reference a table that Stage 2 never discovered, the validation fails before any query executes.

## Prerequisites

- Loom v1.0.0-beta.1+
- Multi-stage workflow defined
- Understanding of workflow orchestration

## Quick Start

```go
import "github.com/teradata-labs/loom/pkg/orchestration"

// Create context
ctx := orchestration.NewStructuredContext("workflow-123", "npath-discovery")

// Stage 2 outputs recommended table
ctx.AddStageOutput("stage-2", orchestration.StageOutput{
    StageID: "td-expert-stage-2",
    Status:  "completed",
    Outputs: map[string]interface{}{
        "recommended_table": map[string]interface{}{
            "database": "demo",
            "table":    "bank_events",
        },
    },
})

// Stage 6 validates before querying
err := ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-2")
if err != nil {
    // Validation failed - agent tried to use wrong table
}
```


## Configuration

Enable structured context in workflow YAML:

```yaml
spec:
  type: iterative

  structured_context:
    enabled: true
    schema_version: "1.0"
    strict_validation: true  # Reject invalid outputs
```


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
```

### Validate References

Prevent hallucinated table references:

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

### Inject into Prompts

```go
// Serialize context to JSON
jsonStr, err := ctx.ToJSON()
if err != nil {
    return err
}

// Replace template variable in prompt
prompt := strings.Replace(promptTemplate, "{{structured_context}}", jsonStr, -1)
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
    ctx.AddStageOutput("stage-1", stage1)

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
    ctx.AddStageOutput("stage-2", stage2)

    // Stage 6: Validate before profiling
    // This PASSES - demo.bank_events was recommended by stage-2
    err := ctx.ValidateTableReference("stage-6", "demo", "bank_events", "stage-2")
    if err != nil {
        return err
    }

    // This FAILS - val_telco_churn was never recommended
    err = ctx.ValidateTableReference("stage-6", "val_telco_churn", "customers", "stage-2")
    // Error: database mismatch

    return nil
}
```

### Example 2: Workflow YAML Integration

```yaml
spec:
  type: iterative
  structured_context:
    enabled: true
    strict_validation: true

  pipeline:
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
          "stage_outputs": {
            "stage-2": {
              "stage_id": "td-expert-stage-2",
              "status": "completed",
              "outputs": {
                "recommended_table": {
                  "database": "demo",
                  "table": "bank_events"
                }
              }
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


## Troubleshooting

### Agent Outputs Invalid JSON

**Problem**: Agent produces prose instead of JSON

**Solution**: Add strict format requirements to prompt:

```yaml
prompt_template: |
  **CRITICAL**: You MUST output ONLY valid JSON. No prose before or after.

  **BAD** (will fail validation):
  "I found 3 databases. Here's the JSON: {...}"

  **GOOD** (will pass validation):
  ```json
  {"stage_outputs": {...}}
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

**Solution**: Use selective stage outputs:

```yaml
spec:
  pass_full_history: false  # Only pass {{previous}}

  # Stage 9 uses {{history}} to create summary
  # Stage 10 uses {{previous}} to get summary only
```

### Database Mismatch Error

**Error:**
```
database mismatch: current stage references 'val_telco_churn'
but source stage 'stage-2' recommended 'demo'
```

**Cause**: Agent hallucinated a database name from training data

**Solution**: This error is working as intended. The validation caught the hallucination. Review the agent prompt to ensure it extracts the exact database/table from the structured context JSON.
