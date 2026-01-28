
# Human-in-the-Loop Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Request Approval Before Actions](#request-approval-before-actions)
  - [Request User Input](#request-user-input)
  - [Request Code Review](#request-code-review)
  - [Handle Timeouts](#handle-timeouts)
- [Examples](#examples)
  - [Example 1: Database Deletion Approval](#example-1-database-deletion-approval)
  - [Example 2: Multi-Choice Decision](#example-2-multi-choice-decision)
- [Troubleshooting](#troubleshooting)


## Overview

Request human approval, input, or decision-making during agent execution using the ContactHumanTool.

## Prerequisites

- Loom v1.0.0-beta.1+
- Agent with ContactHumanTool enabled

## Quick Start

The ContactHumanTool is included in the builtin tool registry:

```go
import "github.com/teradata-labs/loom/pkg/shuttle/builtin"

tools := builtin.All()  // Includes contact_human
```

Request human input:

```go
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{})

result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Should I delete table 'users'?",
    "request_type": "approval",
    "priority":     "high",
    "timeout_seconds": 300,
})

if result.Success {
    response := result.Data.(map[string]interface{})
    if response["status"] == "approved" {
        // Proceed with deletion
    }
}
```

## Configuration

### ContactHumanConfig

```go
type ContactHumanConfig struct {
    Store        HumanRequestStore  // Storage backend (default: in-memory)
    Notifier     Notifier           // Notification mechanism (default: no-op)
    Timeout      time.Duration      // Default timeout (default: 5 minutes)
    PollInterval time.Duration      // Check interval (default: 1 second)
}
```

### Request Types

| Type | Description | Use Case |
|------|-------------|----------|
| `approval` | Yes/no decision | "Delete this data?" |
| `decision` | Choose between options | "Which approach: A, B, or C?" |
| `input` | Request information | "What email address?" |
| `review` | Quality check | "Review this code" |

### Priority Levels

| Priority | Suggested Timeout | Description |
|----------|-------------------|-------------|
| `low` | 24+ hours | Non-urgent |
| `normal` | 1-4 hours | Standard |
| `high` | 15-60 minutes | Needs attention |
| `critical` | 5-15 minutes | Urgent |

## Common Tasks

### Request Approval Before Actions

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Delete all test data from the users table?",
    "request_type": "approval",
    "priority":     "high",
    "context": map[string]interface{}{
        "table_name": "users",
        "row_count":  1000000,
    },
    "timeout_seconds": 300,
})

if !result.Success {
    if result.Error.Code == "TIMEOUT" {
        return fmt.Errorf("approval timed out")
    }
    return fmt.Errorf("approval failed: %s", result.Error.Message)
}

response := result.Data.(map[string]interface{})
if response["status"] != "approved" {
    return fmt.Errorf("rejected by %s", response["responded_by"])
}

// Proceed with approved action
```

### Request User Input

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "What is the customer's preferred contact method?",
    "request_type": "input",
    "priority":     "normal",
    "context": map[string]interface{}{
        "customer_id": "CUST-12345",
        "options":     []string{"email", "phone", "SMS"},
    },
})

if result.Success {
    response := result.Data.(map[string]interface{})
    contactMethod := response["response"].(string)
}
```

### Request Code Review

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Review this SQL query before execution",
    "request_type": "review",
    "priority":     "high",
    "context": map[string]interface{}{
        "query":          "DELETE FROM orders WHERE created_at < '2023-01-01'",
        "estimated_rows": 10000,
    },
    "timeout_seconds": 1800,  // 30 minutes for review
})
```

### Handle Timeouts

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":        "Approve this transaction?",
    "request_type":    "approval",
    "timeout_seconds": 300,
})

if !result.Success && result.Error.Code == "TIMEOUT" {
    // Human didn't respond in time
    // Default to safe action (cancel operation)
    log.Warn("Human approval timed out, canceling operation")
    return nil
}
```

## Examples

### Example 1: Database Deletion Approval

```go
func deleteTableWithApproval(ctx context.Context, tableName string, rowCount int) error {
    tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
        Timeout: 10 * time.Minute,
    })

    priority := "normal"
    if rowCount > 1000000 {
        priority = "high"
    }

    result, err := tool.Execute(ctx, map[string]interface{}{
        "question": fmt.Sprintf("Delete table '%s' with %d rows?", tableName, rowCount),
        "request_type": "approval",
        "priority": priority,
        "context": map[string]interface{}{
            "table":     tableName,
            "row_count": rowCount,
            "operation": "DROP TABLE",
        },
    })
    if err != nil {
        return err
    }

    if !result.Success {
        return fmt.Errorf("approval request failed")
    }

    response := result.Data.(map[string]interface{})
    if response["status"] == "approved" {
        return executeDeleteTable(tableName)
    }

    return fmt.Errorf("deletion rejected by %s", response["responded_by"])
}
```

### Example 2: Multi-Choice Decision

```go
func selectDatabaseWithHuman(ctx context.Context, workload string) (string, error) {
    tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{})

    result, err := tool.Execute(ctx, map[string]interface{}{
        "question":     "Which database should I use for this workload?",
        "request_type": "decision",
        "priority":     "normal",
        "context": map[string]interface{}{
            "workload_type":  workload,
            "options":        []string{"PostgreSQL", "Teradata", "BigQuery"},
            "recommendation": "Teradata for OLAP workloads",
        },
        "timeout_seconds": 3600,
    })
    if err != nil {
        return "", err
    }

    if !result.Success {
        return "", fmt.Errorf("decision request failed")
    }

    response := result.Data.(map[string]interface{})
    return response["response"].(string), nil
}
```

## Troubleshooting

### Request Times Out Immediately

Increase timeout:

```go
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
    Timeout: 30 * time.Minute,
})

// Or per-request
result, _ := tool.Execute(ctx, map[string]interface{}{
    "question":        "...",
    "timeout_seconds": 1800,
})
```

### No Notification Received

The default notifier is no-op. Configure a webhook notifier:

```go
notifier := shuttle.NewJSONNotifier("https://myapp.com/webhook/hitl")
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
    Notifier: notifier,
})
```

### Request Not Found After Restart

The default in-memory store loses data on restart. For persistence, wait for SQLite store support or implement a custom `HumanRequestStore`.

### Multiple Responses Rejected

Only the first response is accepted. Check status before responding:

```go
req, _ := store.Get(ctx, requestID)
if req.Status != "pending" {
    return fmt.Errorf("already responded: status=%s", req.Status)
}

store.RespondToRequest(ctx, requestID, "approved", "Yes", "alice", nil)
```
