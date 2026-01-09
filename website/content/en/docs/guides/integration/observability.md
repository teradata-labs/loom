---
title: "Observability (Hawk)"
weight: 10
---

# Observability Guide

**Version**: v1.0.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Common Tasks](#common-tasks)
  - [Send Traces to Hawk](#send-traces-to-hawk)
  - [Track LLM Costs](#track-llm-costs)
  - [Enable Privacy Redaction](#enable-privacy-redaction)
  - [Use No-Op Tracer for Development](#use-no-op-tracer-for-development)
- [Examples](#examples)
  - [Example 1: Instrumented Agent](#example-1-instrumented-agent)
  - [Example 2: Production Configuration](#example-2-production-configuration)
- [Hawk Query Examples](#hawk-query-examples)
- [Troubleshooting](#troubleshooting)

---

## Overview

Instrument Loom agents to send traces and metrics to Hawk for monitoring, cost tracking, and debugging.

## Prerequisites

- Loom v1.0.0-beta.1+
- Hawk service running (optional - use no-op tracer for development)
- Anthropic/Bedrock/Ollama API key configured

## Quick Start

Add observability to your agent:

```go
import (
    "github.com/teradata-labs/loom/pkg/observability"
    "github.com/teradata-labs/loom/pkg/llm/anthropic"
)

// Create Hawk tracer
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    Endpoint: "http://localhost:9090/v1/traces",
})
defer tracer.Close()

// Create instrumented agent
agent := loom.NewInstrumentedAgent(backend, llmProvider, tracer)

// Use normally - traces sent automatically
response, _ := agent.Chat(ctx, "session-123", "Hello!")
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

### Send Traces to Hawk

Create a tracer and inject it into your agent:

```go
tracer, _ := observability.NewHawkTracer(observability.HawkConfig{
    Endpoint: "http://localhost:9090/v1/traces",
    APIKey:   os.Getenv("HAWK_API_KEY"),
})
defer tracer.Close()

agent := agent.NewAgent(backend, llm, agent.WithTracer(tracer))
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
