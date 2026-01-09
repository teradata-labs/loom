---
title: "Hawk Embedded Integration"
weight: 11
---

# Hawk Embedded Integration Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Memory Storage](#memory-storage)
  - [SQLite Storage](#sqlite-storage)
  - [Auto-Selection Mode](#auto-selection-mode)
- [Examples](#examples)
  - [Example 1: Development Setup](#example-1-development-setup)
  - [Example 2: Production Setup](#example-2-production-setup)
- [Troubleshooting](#troubleshooting)

---

## Overview

Use Hawk's evaluation and storage capabilities in-process instead of as a remote service. Embedded mode provides zero-setup tracing with no network latency.

**Benefits:**
- Zero network latency for tracing
- Single binary deployment
- Offline capability
- No external dependencies

## Prerequisites

- Loom v1.0.0-beta.1+
- Build with FTS5 tag: `go build -tags fts5`

## Quick Start

```go
import "github.com/Teradata-TIO/loom/pkg/builder"

// Create agent with embedded Hawk (memory storage)
agent, err := builder.NewEmbeddedAgent(backend, llm, "")
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

// Or with persistent SQLite storage
agent, err := builder.NewEmbeddedAgent(backend, llm, "./loom.db")
```

---

## Configuration

### EmbeddedHawkConfig Options

```go
import "github.com/Teradata-TIO/loom/pkg/observability"

config := observability.EmbeddedHawkConfig{
    StorageType:   "sqlite",          // "memory" or "sqlite"
    DBPath:        "./loom.db",       // Required for sqlite
    BatchSize:     100,               // Spans to buffer before flush
    FlushInterval: 5 * time.Second,   // Flush frequency
    Privacy: observability.PrivacyConfig{
        RedactCredentials: true,
        RedactPII:         true,
    },
}

tracer, err := observability.NewEmbeddedHawkTracer(config)
```

### Environment Variables

```bash
export LOOM_TRACER_MODE=embedded   # Force embedded mode
export LOOM_ENV=development        # Auto-selects embedded
export HAWK_ENDPOINT=""            # Empty = use embedded
```

---

## Common Tasks

### Memory Storage

For ephemeral traces (development):

```go
agent, err := builder.NewEmbeddedAgent(backend, llm, "")
// Traces stored in memory, lost on restart
```

### SQLite Storage

For persistent traces:

```go
agent, err := builder.NewEmbeddedAgent(backend, llm, "./loom.db")
// Traces persisted to SQLite file
```

### Auto-Selection Mode

Let Loom choose the best mode:

```go
agent, err := builder.NewAutoAgent(backend, llm)
// Auto-selects based on environment:
// - Development: embedded (memory)
// - HAWK_ENDPOINT set: service mode
// - Default: embedded (memory)
```

---

## Examples

### Example 1: Development Setup

```go
package main

import (
    "context"
    "log"

    "github.com/Teradata-TIO/loom/pkg/builder"
)

func main() {
    backend := NewMyBackend()
    llm := NewAnthropicLLM()

    // Memory storage - fast, ephemeral
    agent, err := builder.NewEmbeddedAgent(backend, llm, "")
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    ctx := context.Background()
    response, _ := agent.Chat(ctx, "session-1", "What tables exist?")
    log.Printf("Response: %s", response.Content)
}
```

### Example 2: Production Setup

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/Teradata-TIO/loom/pkg/builder"
)

func main() {
    backend := NewMyBackend()
    llm := NewAnthropicLLM()

    // SQLite storage - persistent traces
    dbPath := os.Getenv("LOOM_TRACE_DB")
    if dbPath == "" {
        dbPath = "/var/lib/loom/traces.db"
    }

    agent, err := builder.NewEmbeddedAgent(backend, llm, dbPath)
    if err != nil {
        log.Fatal(err)
    }
    defer agent.Close()

    ctx := context.Background()
    response, _ := agent.Chat(ctx, "session-prod", "Analyze sales trends")
}
```

### Builder Pattern

```go
agent, err := builder.NewAgentBuilder().
    WithBackend(myBackend).
    WithAnthropicLLM("api-key").
    WithEmbeddedHawk("sqlite", "./loom.db").
    WithGuardrails().
    Build()
```

---

## Troubleshooting

### FTS5 Build Error

**Error:** `undefined: sqlite3.FTS5`

**Solution:** Build with FTS5 tag:
```bash
go build -tags fts5 -o bin/looms ./cmd/looms
```

### Database Locked

**Error:** `database is locked`

**Solution:** Reduce batch size or use memory storage:
```go
config := EmbeddedHawkConfig{
    StorageType: "sqlite",
    BatchSize:   10,  // Smaller batches
}
```

### Missing Traces

**Problem:** Traces not appearing in storage

**Solution:** Ensure flush before close:
```go
defer func() {
    tracer.Flush(context.Background())
    tracer.Close()
}()
```

### High Memory Usage

**Solution:** Reduce buffer size and increase flush frequency:
```go
config := EmbeddedHawkConfig{
    BatchSize:     50,
    FlushInterval: 2 * time.Second,
}
```

---

## Storage Comparison

| Storage | Best For | Persistence | Speed |
|---------|----------|-------------|-------|
| Memory | Development | None | Fastest |
| SQLite | Production | File-based | Fast |
| Service | Multi-node | Centralized | Network dependent |
