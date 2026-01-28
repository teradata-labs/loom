
# Observability Guide

**Version**: v1.0.2

## Table of Contents

- [Overview](#overview)
- [Observability Modes](#observability-modes)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Use Embedded Storage](#use-embedded-storage)
  - [Send Traces to Hawk Service](#send-traces-to-hawk-service)
  - [Track LLM Costs](#track-llm-costs)
  - [Enable Privacy Redaction](#enable-privacy-redaction)
  - [Use No-Op Tracer for Development](#use-no-op-tracer-for-development)
- [Examples](#examples)
  - [Example 1: Embedded SQLite Storage](#example-1-embedded-sqlite-storage)
  - [Example 2: Hawk Service Export](#example-2-hawk-service-export)
  - [Example 3: Production Configuration](#example-3-production-configuration)
- [Query Examples](#query-examples)
- [Troubleshooting](#troubleshooting)


## Overview

Loom provides comprehensive observability with distributed tracing and metrics. Choose from three modes based on your needs:

1. **Embedded Mode**: In-process storage (memory or SQLite) - no external dependencies
2. **Service Mode**: HTTP export to Hawk or other observability services
3. **None Mode**: Zero overhead for testing

## Observability Modes

### Embedded Mode (Recommended)

**Use when**: You want local trace storage without external services

**Features**:
- ✅ Zero external dependencies
- ✅ Memory or SQLite storage options
- ✅ Query traces with SQL
- ✅ Offline capability
- ❌ Single-server only (no centralized aggregation)

### Service Mode

**Use when**: You need centralized observability across multiple servers

**Features**:
- ✅ Centralized trace aggregation
- ✅ Hawk UI for visualization
- ✅ Multi-server support
- ❌ Requires external Hawk service
- ❌ Requires `-tags hawk` build flag

### None Mode

**Use when**: Testing or when observability is not needed

**Features**:
- ✅ Zero overhead
- ✅ Always available
- ❌ No trace collection

## Prerequisites

**For all modes**:
- Loom v1.0.2+
- Build with FTS5 tag: `go build -tags fts5`

**For service mode only**:
- Hawk service running
- Build with hawk tag: `go build -tags fts5,hawk`

**For embedded mode**:
- No additional prerequisites (always available)

## Quick Start

### Embedded Mode (Recommended for Getting Started)

```yaml
# looms.yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: ./traces.db
```

Start server:
```bash
looms serve --config looms.yaml
```

Query traces:
```bash
sqlite3 ./traces.db "SELECT * FROM eval_metrics;"
```

### Service Mode (For Production with Hawk)

```yaml
# looms.yaml
observability:
  enabled: true
  mode: service
  hawk_endpoint: http://localhost:9090/v1/traces
  hawk_api_key: ${HAWK_API_KEY}
```

Start server (requires `-tags hawk` build):
```bash
looms serve --config looms.yaml
```

### None Mode (For Testing)

```yaml
# looms.yaml
observability:
  enabled: false
```

Or explicitly:
```yaml
observability:
  enabled: true
  mode: none
```

## Configuration

### HawkConfig Options

```go
type HawkConfig struct {
    Endpoint      string        // Hawk API endpoint (required)
    APIKey        string        // Bearer token (optional)
    BatchSize     int           // Spans per batch (default: 100)
    FlushInterval time.Duration // Auto-flush interval (default: 10s)
    MaxRetries    int           // Max retry attempts (default: 3)
    RetryBackoff  time.Duration // Initial backoff (default: 1s)
    Privacy       PrivacyConfig // Privacy settings
}

type PrivacyConfig struct {
    RedactCredentials bool     // Remove passwords, API keys
    RedactPII         bool     // Redact emails, phones, SSNs
    AllowedAttributes []string // Keys that bypass redaction
}
```

### YAML Configuration

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  version: "1.0.0"
spec:
  observability:
    enabled: true
    hawk_endpoint: http://localhost:9090
```

## Common Tasks

### Use Embedded Storage

Store traces locally without external services:

**Configuration**:
```yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite  # or "memory"
  sqlite_path: ./traces.db
  flush_interval: 30s
```

**Query traces**:
```bash
# View sessions
sqlite3 ./traces.db "
SELECT id, name, status, datetime(created_at, 'unixepoch') as created
FROM evals ORDER BY created_at DESC LIMIT 10;
"

# View metrics
sqlite3 ./traces.db "
SELECT eval_id, total_runs, successful_runs,
       ROUND(success_rate, 4) as success_rate,
       ROUND(avg_execution_time_ms, 2) as avg_ms
FROM eval_metrics;
"
```

Embedded storage includes:
- Session tracking
- Span storage with timing
- Aggregated metrics (success rate, avg execution time)
- Cost tracking (token usage)

### Send Traces to Hawk Service

Export traces to centralized Hawk service:

**Configuration**:
```yaml
observability:
  enabled: true
  mode: service
  hawk_endpoint: http://localhost:9090/v1/traces
  hawk_api_key: ${HAWK_API_KEY}
```

**Build requirement**: Requires `-tags fts5,hawk`:
```bash
go build -tags fts5,hawk -o bin/looms ./cmd/looms
```

Traces include:
- LLM calls with token counts
- Tool executions with timing
- Conversation history
- Error patterns

### Track LLM Costs

Costs are tracked automatically. Access them in responses:

```go
response, _ := agent.Chat(ctx, sessionID, query)

fmt.Printf("Cost: $%.4f\n", response.Usage.CostUSD)
fmt.Printf("Tokens: %d\n", response.Usage.TotalTokens)
```

Query costs in Hawk:

```bash
hawk query --metric llm.cost --group-by session.id --timerange 24h
```

### Enable Privacy Redaction

Redact sensitive data before export:

```go
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    Endpoint: "http://localhost:9090/v1/traces",
    Privacy: observability.PrivacyConfig{
        RedactCredentials: true,
        RedactPII:         true,
        AllowedAttributes: []string{
            "session.id",
            "llm.model",
            "tool.name",
        },
    },
})
```

Redaction patterns:
- Emails: `user@example.com` -> `[EMAIL_REDACTED]`
- Phones: `555-123-4567` -> `[PHONE_REDACTED]`
- SSNs: `123-45-6789` -> `[SSN_REDACTED]`
- Credit cards: `1234-5678-9012-3456` -> `[CARD_REDACTED]`

### Use No-Op Tracer for Development

Disable tracing without code changes:

```go
tracer := observability.NewNoOpTracer()
agent := loom.NewInstrumentedAgent(backend, llmProvider, tracer)
```

## Examples

### Example 1: Instrumented Agent

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/teradata-labs/loom"
    "github.com/teradata-labs/loom/pkg/llm/anthropic"
    "github.com/teradata-labs/loom/pkg/observability"
)

func main() {
    ctx := context.Background()

    // Create tracer
    tracer, err := observability.NewHawkTracer(observability.HawkConfig{
        Endpoint:      "http://localhost:9090/v1/traces",
        BatchSize:     100,
        FlushInterval: 10 * time.Second,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer tracer.Close()

    // Create LLM provider
    llm := anthropic.NewClient(anthropic.Config{
        APIKey: os.Getenv("ANTHROPIC_API_KEY"),
        Model:  "claude-sonnet-4-5-20250929",
    })

    // Create instrumented agent
    agent := loom.NewInstrumentedAgent(backend, llm, tracer)

    // Use agent
    response, err := agent.Chat(ctx, "session-123", "Hello!")
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("Response: %s", response.Content)
    log.Printf("Cost: $%.4f", response.Usage.CostUSD)

    // Flush before exit
    tracer.Flush(ctx)
}
```

### Example 2: Production Configuration

```go
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    Endpoint:      os.Getenv("HAWK_ENDPOINT"),
    APIKey:        os.Getenv("HAWK_API_KEY"),
    BatchSize:     100,
    FlushInterval: 10 * time.Second,
    MaxRetries:    3,
    RetryBackoff:  1 * time.Second,
    Privacy: observability.PrivacyConfig{
        RedactCredentials: true,
        RedactPII:         true,
        AllowedAttributes: []string{
            "session.id",
            "llm.provider",
            "llm.model",
            "tool.name",
        },
    },
    HTTPClient: &http.Client{
        Timeout: 30 * time.Second,
        Transport: &http.Transport{
            MaxIdleConns:        100,
            MaxIdleConnsPerHost: 10,
            IdleConnTimeout:     90 * time.Second,
        },
    },
})
```

## Hawk Query Examples

### Cost Analysis

```bash
# Total cost by session
hawk query --metric llm.cost --group-by session.id --timerange 24h

# Cost by LLM provider
hawk query --metric llm.cost --group-by llm.provider --timerange 7d

# Most expensive sessions
hawk query --metric llm.cost --sort desc --limit 10
```

### Performance Analysis

```bash
# LLM latency by model
hawk query --metric llm.latency --group-by llm.model --timerange 24h

# Slow tool executions
hawk query --span tool.execute --where "duration_ms > 5000"
```

### Error Tracking

```bash
# LLM error rate
hawk query --metric llm.errors.total --group-by error.type --timerange 24h

# Failed tool executions
hawk query --span tool.execute --where "status = error" --timerange 24h
```

## Troubleshooting

### Traces Not Appearing

1. Check endpoint reachability:
   ```bash
   curl -X POST http://localhost:9090/v1/traces
   ```

2. Verify API key (if required):
   ```bash
   export HAWK_API_KEY=your-key-here
   ```

3. Force flush to see immediate results:
   ```go
   tracer.Flush(context.Background())
   ```

### High Memory Usage

Reduce buffer size or flush more frequently:

```go
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    BatchSize:     50,
    FlushInterval: 5 * time.Second,
})
```

### Export Failures

Increase retry attempts:

```go
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    MaxRetries:   5,
    RetryBackoff: 2 * time.Second,
})
```

Check network connectivity:

```bash
ping your-hawk-endpoint
```
