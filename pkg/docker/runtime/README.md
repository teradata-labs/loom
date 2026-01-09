# Docker Runtime Trace Libraries

Container-side trace libraries for Python and Node.js that enable distributed tracing from inside Docker containers to the Loom host.

## Overview

Loom's Docker backend provides **automatic trace propagation** from the host to containers using W3C baggage and environment variables. Container-side code can use lightweight trace libraries to create child spans that are automatically collected and exported to Hawk.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Loom Host                              │
│                                                             │
│  DockerExecutor.executeCommand()                           │
│    │                                                        │
│    ├─ Start span: docker.execute (TraceID, SpanID)        │
│    ├─ Inject env vars: LOOM_TRACE_ID, LOOM_SPAN_ID        │
│    ├─ Execute command in container                         │
│    └─ Collect stderr with TraceCollector                   │
│         │                                                   │
│         └─ Parse __LOOM_TRACE__: lines                     │
│            Forward spans to Hawk                            │
└─────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                  Docker Container                           │
│                                                             │
│  from loom_trace import tracer, trace_span                 │
│                                                             │
│  with trace_span("query_database", query="SELECT..."):     │
│      result = execute_query(...)                           │
│                                                             │
│  # Span exported to stderr:                                │
│  # __LOOM_TRACE__:{"trace_id":"...","span_id":"..."}      │
└─────────────────────────────────────────────────────────────┘
```

## Environment Variables

The host automatically injects these environment variables into every container execution:

- **`LOOM_TRACE_ID`**: Current trace ID (links all spans in request)
- **`LOOM_SPAN_ID`**: Parent span ID (the docker.execute span)
- **`LOOM_TRACE_BAGGAGE`**: W3C baggage format (e.g., `tenant_id=foo,org_id=bar`)

Container-side trace libraries read these on initialization and use them to create properly linked child spans.

## Python: `loom_trace.py`

### Installation

The trace library is automatically copied into Python containers by the runtime strategy. No manual installation needed.

### Quick Start

```python
from loom_trace import tracer, trace_span

# Context manager (recommended)
with trace_span("query_database", query_type="SELECT"):
    result = execute_query("SELECT * FROM users")

# Manual span management
span = tracer.start_span("process_data", input_size=len(data))
try:
    processed = process_data(data)
    tracer.end_span(span, status="ok")
except Exception as e:
    tracer.end_span(span, status="error")
    raise
```

### Features

- **Context Manager**: `trace_span()` for automatic span lifecycle
- **Decorator**: `@trace_function()` for function tracing
- **Manual Control**: `start_span()` / `end_span()` for complex flows
- **Automatic Tenant Context**: Propagates `tenant_id` and `org_id` from baggage
- **Error Handling**: Automatically marks spans as error on exception

### Example

See `python/example_traced.py` for a comprehensive example.

## Node.js: `loom-trace.js`

### Installation

The trace library is automatically copied into Node.js containers by the runtime strategy. No manual installation needed.

### Quick Start

```javascript
const { tracer, traceSpan, traceSpanSync } = require('./loom-trace');

// Async tracing (recommended)
await traceSpan('query_database', { query_type: 'SELECT' }, async () => {
  return await executeQuery('SELECT * FROM users');
});

// Sync tracing
const result = traceSpanSync('parse_data', { format: 'json' }, () => {
  return JSON.parse(data);
});

// Manual span management
const span = tracer.startSpan('process_data', { input_size: data.length });
try {
  const processed = await processData(data);
  tracer.endSpan(span, 'ok');
} catch (e) {
  tracer.endSpan(span, 'error');
  throw e;
}
```

### Features

- **Async/Await Support**: `traceSpan()` for async operations
- **Sync Support**: `traceSpanSync()` for synchronous operations
- **Decorator**: `@traceMethod()` for class methods
- **Manual Control**: `startSpan()` / `endSpan()` for complex flows
- **Automatic Tenant Context**: Propagates `tenant_id` and `org_id` from baggage
- **Error Handling**: Automatically marks spans as error on exception

### Example

See `node/example_traced.js` for a comprehensive example.

## Trace Format

Spans are serialized as JSON and written to stderr with a special prefix:

```
__LOOM_TRACE__:{"trace_id":"abc123","span_id":"def456","parent_id":"ghi789","name":"query_database","start_time":"2025-01-15T10:30:00.123Z","end_time":"2025-01-15T10:30:00.456Z","attributes":{"query_type":"SELECT","tenant_id":"acme"},"status":"ok"}
```

The host's `TraceCollector` parses these lines from stderr and forwards them to Hawk.

### Span Fields

- **`trace_id`**: Trace identifier (inherited from LOOM_TRACE_ID)
- **`span_id`**: Unique span identifier (generated)
- **`parent_id`**: Parent span identifier (inherited from LOOM_SPAN_ID)
- **`name`**: Human-readable span name (e.g., "query_database")
- **`start_time`**: ISO 8601 timestamp (RFC3339Nano format)
- **`end_time`**: ISO 8601 timestamp (RFC3339Nano format)
- **`attributes`**: Key-value metadata (query, rows_returned, etc.)
- **`status`**: "ok" (success), "error" (failure), or "unset" (unknown)

### Container Metadata

The host automatically adds these attributes to all collected spans:

- **`container.id`**: Docker container ID
- **`container.source`**: `true` (marks span as from container)

## Error Handling

### Container-Side Errors

If trace export fails (e.g., JSON serialization error), an error message is written to stderr:

```
__LOOM_TRACE_ERROR__: Failed to export span: invalid JSON
```

The host logs these warnings but continues normal operation.

### Host-Side Errors

If trace parsing fails (e.g., invalid JSON, missing required fields), the host logs a warning but continues collecting traces:

```
2025-01-15T10:30:00.123Z  WARN  Failed to parse trace line  {"container_id": "abc123", "line": 42, "error": "span missing trace_id"}
```

**Important**: Trace failures never cause container execution to fail. Tracing is non-blocking.

## Security

### No Direct Hawk Access

Container code **cannot** directly access Hawk. All traces are proxied through the host, which:

1. Validates span structure (required fields, valid timestamps)
2. Redacts sensitive attributes (configurable)
3. Enforces tenant isolation
4. Rate-limits trace export

### Baggage Sanitization

Both Python and Node.js libraries sanitize baggage values to prevent injection attacks:

```python
# Input: "tenant_id=foo;DROP TABLE users"
# Output: {"tenant_id": "foo"}  (semicolons rejected)
```

### Resource Limits

Container trace libraries have minimal overhead:

- **Memory**: ~10KB per library (single global tracer instance)
- **CPU**: Negligible (JSON serialization only on span end)
- **I/O**: Unbuffered stderr writes (no file I/O)

## Performance

### Overhead

Tracing overhead per span:

- **Python**: ~0.1ms (datetime.utcnow() + JSON serialization)
- **Node.js**: ~0.1ms (new Date().toISOString() + JSON.stringify)
- **Host Collection**: ~0.05ms (bufio.Scanner + JSON unmarshal)

### Best Practices

1. **Trace High-Level Operations**: Database queries, API calls, file I/O
2. **Avoid Over-Instrumentation**: Don't trace every function call
3. **Use Context Managers**: Automatic span lifecycle (less error-prone)
4. **Batch Attributes**: Set all attributes at span start (fewer object mutations)

## Testing

### Unit Tests

Test container-side trace libraries without Docker:

```python
# Python
from loom_trace import LoomTracer
import os

os.environ["LOOM_TRACE_ID"] = "test-trace-123"
os.environ["LOOM_SPAN_ID"] = "test-span-456"
tracer = LoomTracer()
assert tracer.trace_id == "test-trace-123"
```

```javascript
// Node.js
process.env.LOOM_TRACE_ID = 'test-trace-123';
process.env.LOOM_SPAN_ID = 'test-span-456';
const { LoomTracer } = require('./loom-trace');
const tracer = new LoomTracer();
assert(tracer.traceId === 'test-trace-123');
```

### Integration Tests

See `pkg/docker/trace_integration_test.go` for end-to-end trace propagation tests.

## Troubleshooting

### Spans Not Appearing in Hawk

1. **Check Environment Variables**: Verify LOOM_TRACE_ID is set in container
   ```bash
   docker exec <container> env | grep LOOM_
   ```

2. **Check Stderr Output**: Verify traces are being written
   ```bash
   docker logs <container> 2>&1 | grep __LOOM_TRACE__
   ```

3. **Check Host Logs**: Look for trace collector warnings
   ```bash
   grep "Failed to parse trace line" loom.log
   ```

4. **Validate JSON Format**: Ensure spans are valid JSON
   ```python
   import json
   json.loads('{"trace_id":"..."}')  # Should not raise
   ```

### High Parse Error Rate

If `TraceCollector.GetStats()` shows high parse errors:

1. Check for invalid JSON (missing quotes, trailing commas)
2. Verify timestamp format (must be RFC3339: `2006-01-02T15:04:05Z`)
3. Ensure required fields (trace_id, span_id, name) are present
4. Check for newlines in attribute values (not allowed)

## Future Enhancements

- [ ] Ruby runtime support (`loom_trace.rb`)
- [ ] Rust runtime support (`loom_trace.rs`)
- [ ] Buffered span export (batch stderr writes)
- [ ] Trace sampling (reduce overhead for high-volume operations)
- [ ] OpenTelemetry compatibility (export OTLP format)

## See Also

- `pkg/docker/trace_collector.go` - Host-side trace collection
- `pkg/docker/executor.go` - Trace context injection
- `pkg/observability/` - Hawk integration
- `examples/docker/teradata-mcp.yaml` - Production configuration
