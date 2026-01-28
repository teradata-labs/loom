
# Judge Streaming Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Stream Evaluation Progress](#stream-evaluation-progress)
  - [Handle Progress Events](#handle-progress-events)
  - [Cancel Long Evaluations](#cancel-long-evaluations)
- [Examples](#examples)
  - [Example 1: Basic Streaming](#example-1-basic-streaming)
  - [Example 2: Progress Bar UI](#example-2-progress-bar-ui)
- [Troubleshooting](#troubleshooting)


## Overview

Get real-time progress updates during long-running judge evaluations, useful for MIPRO and BootstrapFewShot compilations.

## Prerequisites

- Loom v1.0.0-beta.1+
- At least one judge registered
- Loom server running: `looms serve`

## Quick Start

Stream evaluation progress:

```go
stream := make(chan *loomv1.EvaluateProgress, 10)

go func() {
    resp, err := orchestrator.EvaluateStream(ctx, req, stream)
    close(stream)
}()

for progress := range stream {
    switch p := progress.Progress.(type) {
    case *loomv1.EvaluateProgress_JudgeStarted:
        fmt.Printf("Started: %s\n", p.JudgeStarted.JudgeId)
    case *loomv1.EvaluateProgress_JudgeCompleted:
        fmt.Printf("Completed: %s (%.0fms)\n",
            p.JudgeCompleted.JudgeId,
            float64(p.JudgeCompleted.DurationMs))
    }
}
```

## Configuration

### Progress Message Types

| Message | When Sent | Contains |
|---------|-----------|----------|
| `JudgeStarted` | Before evaluation | Judge ID, example number |
| `JudgeCompleted` | After evaluation | Judge ID, result, duration |
| `ExampleCompleted` | After all judges for example | Cumulative score, pass/fail |
| `EvaluationCompleted` | At end | Final result, total duration |

### Channel Buffer Size

Use buffered channels to avoid blocking:

```go
stream := make(chan *loomv1.EvaluateProgress, 100)  // Buffer for 100 messages
```

## Common Tasks

### Stream Evaluation Progress

```go
import "github.com/teradata-labs/loom/pkg/evals/judges"

orch := judges.NewOrchestrator(&judges.Config{
    Registry:   registry,
    Aggregator: aggregator,
})

stream := make(chan *loomv1.EvaluateProgress, 100)

go func() {
    resp, err := orch.EvaluateStream(ctx, req, stream)
    if err != nil {
        log.Printf("Evaluation failed: %v", err)
    }
    close(stream)
}()

for progress := range stream {
    handleProgress(progress)
}
```

### Handle Progress Events

```go
func handleProgress(progress *loomv1.EvaluateProgress) {
    switch p := progress.Progress.(type) {
    case *loomv1.EvaluateProgress_JudgeStarted:
        fmt.Printf("[START] Judge %s (example %d)\n",
            p.JudgeStarted.JudgeId,
            p.JudgeStarted.ExampleNumber)

    case *loomv1.EvaluateProgress_JudgeCompleted:
        fmt.Printf("[DONE] Judge %s: score=%.1f (%dms)\n",
            p.JudgeCompleted.JudgeId,
            p.JudgeCompleted.Result.Score,
            p.JudgeCompleted.DurationMs)

    case *loomv1.EvaluateProgress_ExampleCompleted:
        fmt.Printf("[EXAMPLE] %d/%d complete (score: %.1f)\n",
            p.ExampleCompleted.ExampleNumber+1,
            p.ExampleCompleted.TotalExamples,
            p.ExampleCompleted.CurrentScore)

    case *loomv1.EvaluateProgress_EvaluationCompleted:
        fmt.Printf("[FINISHED] Total time: %dms\n",
            p.EvaluationCompleted.TotalDurationMs)
    }
}
```

### Cancel Long Evaluations

Use context cancellation:

```go
ctx, cancel := context.WithCancel(context.Background())

// Cancel after 30 seconds
go func() {
    time.Sleep(30 * time.Second)
    cancel()
}()

stream := make(chan *loomv1.EvaluateProgress, 10)
resp, err := orch.EvaluateStream(ctx, req, stream)
// Evaluation stops when context is cancelled
```

Or with timeout:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

resp, err := orch.EvaluateStream(ctx, req, stream)
```

## Examples

### Example 1: Basic Streaming

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/evals/judges"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func main() {
    ctx := context.Background()

    // Create orchestrator
    orch := judges.NewOrchestrator(&judges.Config{
        Registry:   registry,
        Aggregator: aggregator,
    })

    // Create request
    req := &loomv1.EvaluateRequest{
        AgentId:  "sql-agent",
        Prompt:   "Generate a query",
        Response: "SELECT * FROM users",
        JudgeIds: []string{"quality-judge", "safety-judge"},
    }

    // Stream evaluation
    stream := make(chan *loomv1.EvaluateProgress, 100)

    go func() {
        resp, err := orch.EvaluateStream(ctx, req, stream)
        if err != nil {
            log.Printf("Error: %v", err)
        } else {
            log.Printf("Final score: %.1f", resp.AggregatedScore)
        }
        close(stream)
    }()

    // Process progress
    for progress := range stream {
        switch p := progress.Progress.(type) {
        case *loomv1.EvaluateProgress_JudgeStarted:
            fmt.Printf(">> Judge %s started\n", p.JudgeStarted.JudgeId)
        case *loomv1.EvaluateProgress_JudgeCompleted:
            fmt.Printf("<< Judge %s: %.0f/100\n",
                p.JudgeCompleted.JudgeId,
                p.JudgeCompleted.Result.Score)
        case *loomv1.EvaluateProgress_EvaluationCompleted:
            fmt.Printf("Done! (%.2fs)\n",
                float64(p.EvaluationCompleted.TotalDurationMs)/1000)
        }
    }
}
```

### Example 2: Progress Bar UI

```go
package main

import (
    "context"
    "fmt"

    "github.com/teradata-labs/loom/pkg/evals/judges"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func evaluateWithProgress(ctx context.Context, orch *judges.Orchestrator, req *loomv1.EvaluateRequest) error {
    stream := make(chan *loomv1.EvaluateProgress, 100)

    var totalExamples, completedExamples int

    go func() {
        _, _ = orch.EvaluateStream(ctx, req, stream)
        close(stream)
    }()

    for progress := range stream {
        switch p := progress.Progress.(type) {
        case *loomv1.EvaluateProgress_JudgeStarted:
            fmt.Printf("\r[%d/%d] Evaluating %s...",
                completedExamples, totalExamples, p.JudgeStarted.JudgeId)

        case *loomv1.EvaluateProgress_ExampleCompleted:
            totalExamples = int(p.ExampleCompleted.TotalExamples)
            completedExamples = int(p.ExampleCompleted.ExampleNumber) + 1

            pct := float64(completedExamples) / float64(totalExamples) * 100
            bar := progressBar(pct, 40)
            fmt.Printf("\r%s %.0f%% (%d/%d)",
                bar, pct, completedExamples, totalExamples)

        case *loomv1.EvaluateProgress_EvaluationCompleted:
            fmt.Printf("\nComplete! Score: %.1f\n",
                p.EvaluationCompleted.FinalResult.AggregatedScore)
        }
    }

    return nil
}

func progressBar(pct float64, width int) string {
    filled := int(pct / 100 * float64(width))
    bar := ""
    for i := 0; i < width; i++ {
        if i < filled {
            bar += "="
        } else {
            bar += " "
        }
    }
    return "[" + bar + "]"
}
```

## Troubleshooting

### Progress Not Showing

Ensure channel is being read:

```go
// Wrong: channel blocks because nobody reads
stream := make(chan *loomv1.EvaluateProgress)
orch.EvaluateStream(ctx, req, stream)  // Blocks!

// Correct: read in separate goroutine or use buffered channel
stream := make(chan *loomv1.EvaluateProgress, 100)
go func() {
    orch.EvaluateStream(ctx, req, stream)
    close(stream)
}()
for p := range stream { ... }
```

### Channel Deadlock

Use buffered channels and proper closure:

```go
stream := make(chan *loomv1.EvaluateProgress, 100)

go func() {
    resp, err := orch.EvaluateStream(ctx, req, stream)
    close(stream)  // Always close when done
}()
```

### Missing Final Result

The `EvaluationCompleted` message contains the final result:

```go
case *loomv1.EvaluateProgress_EvaluationCompleted:
    result := p.EvaluationCompleted.FinalResult
    fmt.Printf("Score: %.1f, Verdict: %s\n",
        result.AggregatedScore,
        result.Verdict)
```

### Slow Progress Updates

Check buffer size and processing speed:

```go
// Larger buffer for high-throughput evaluations
stream := make(chan *loomv1.EvaluateProgress, 1000)
```
