
# Streaming Reference

Complete technical specification for real-time streaming in Loom via StreamWeave RPC.

**Version**: v1.0.0-beta.2
**Protocol**: gRPC bidirectional streaming, HTTP/SSE
**API**: `StreamWeave` RPC


## Table of Contents

- [Quick Reference](#quick-reference)
- [Protocol Support](#protocol-support)
- [Progress Events](#progress-events)
- [Execution Stages](#execution-stages)
- [gRPC Implementation](#grpc-implementation)
- [HTTP/SSE Implementation](#httpsse-implementation)
- [Progress Calculation](#progress-calculation)
- [Error Handling](#error-handling)
- [Cancellation](#cancellation)
- [Performance](#performance)
- [Examples](#examples)


## Quick Reference

| Feature | Details |
|---------|---------|
| **RPC Name** | `StreamWeave` |
| **Protocol** | gRPC streaming, HTTP/SSE |
| **Request** | `WeaveRequest` |
| **Response** | Stream of `WeaveProgress` |
| **Stages** | 8 execution stages |
| **Progress Range** | 0-100% |
| **First Event Latency** | <1ms |
| **Cancellation** | Context-based |


## Protocol Support

### gRPC Streaming

**RPC Definition**:
```proto
rpc StreamWeave(WeaveRequest) returns (stream WeaveProgress)
```

**Endpoint**: `localhost:50051` (default gRPC port)
**Transport**: Bidirectional gRPC streaming
**Message Format**: Protocol Buffers


### HTTP/SSE

**Available since**: v0.8.2
**Endpoint**: `POST /v1/weave:stream`
**Transport**: Server-Sent Events (SSE)
**Message Format**: JSON (newline-delimited)

**Configuration**:
```yaml
server:
  http_port: 9090  # 0 = disabled (default)
```

**Enable HTTP/SSE**:
```bash
looms config set server.http_port 9090
looms serve
# Output: HTTP/REST+SSE endpoints available at http://0.0.0.0:9090/v1/weave:stream
```


## Progress Events

### WeaveProgress Message

```proto
message WeaveProgress {
  ExecutionStage stage = 1;         // Current execution stage
  int32 progress = 2;               // Progress percentage (0-100)
  string message = 3;               // Human-readable status message
  string tool_name = 4;             // Tool being executed (optional)
  int64 timestamp = 5;              // Unix timestamp
  ExecutionResult partial_result = 6; // Final result (COMPLETED stage only)
}
```

#### stage

**Type**: `ExecutionStage` enum
**Required**: Yes

Current execution stage.

**Values**: See [Execution Stages](#execution-stages)


#### progress

**Type**: `int32`
**Required**: Yes
**Range**: `0` - `100`

Progress percentage for current execution.

**Calculation**: See [Progress Calculation](#progress-calculation)


#### message

**Type**: `string`
**Required**: Yes

Human-readable status message.

**Examples**:
- `"Analyzing query and selecting patterns"`
- `"Generating response (turn 1)"`
- `"Executing tool: execute_query"`
- `"Query completed successfully"`


#### tool_name

**Type**: `string`
**Required**: No (only during `TOOL_EXECUTION` stage)

Name of tool being executed.

**Example**: `"execute_query"`, `"list_tables"`, `"get_schema"`


#### timestamp

**Type**: `int64`
**Required**: Yes
**Format**: Unix timestamp (seconds since epoch)

Event timestamp.


#### partial_result

**Type**: `ExecutionResult`
**Required**: No (only in `COMPLETED` stage)

Final execution result.

**Fields**:
- `type` (string): `"text"` | `"structured"`
- `data_json` (string): Result data as JSON string


## Execution Stages

### ExecutionStage Enum

```proto
enum ExecutionStage {
  EXECUTION_STAGE_UNSPECIFIED = 0;
  EXECUTION_STAGE_PATTERN_SELECTION = 1;
  EXECUTION_STAGE_SCHEMA_DISCOVERY = 2;
  EXECUTION_STAGE_LLM_GENERATION = 3;
  EXECUTION_STAGE_TOOL_EXECUTION = 4;
  EXECUTION_STAGE_GUARDRAIL_CHECK = 5;
  EXECUTION_STAGE_SELF_CORRECTION = 6;
  EXECUTION_STAGE_COMPLETED = 7;
  EXECUTION_STAGE_FAILED = 8;
}
```

### Stage Details

#### PATTERN_SELECTION

**Progress**: `10%`
**Description**: Analyzing query and selecting patterns from library
**Duration**: 10-50ms

**Message examples**:
- `"Analyzing query and selecting patterns"`
- `"Selected 3 patterns for execution"`


#### SCHEMA_DISCOVERY

**Progress**: `15%`
**Description**: Discovering database schema (if backend configured)
**Duration**: 50-200ms

**Message examples**:
- `"Discovering database schema"`
- `"Found 12 tables in schema"`


#### LLM_GENERATION

**Progress**: `20%` + (turn × 10%)
**Description**: Calling LLM to generate response
**Duration**: 500ms-5s (provider-dependent)

**Message examples**:
- `"Generating response (turn 1)"`
- `"Generating response (turn 2)"`

**Progress calculation**: Increases by 10% per conversation turn


#### TOOL_EXECUTION

**Progress**: `50%` + (execution_count × 5%)
**Description**: Executing tools (SQL queries, API calls, etc.)
**Duration**: Variable (query-dependent)

**Message examples**:
- `"Executing tool: execute_query"`
- `"Executing tool: list_tables"`

**Progress calculation**: Increases by 5% per tool execution

**Fields**:
- `tool_name` field populated with executing tool name


#### GUARDRAIL_CHECK

**Progress**: `85%`
**Description**: Validating against guardrails (limits, constraints)
**Duration**: <10ms

**Message examples**:
- `"Checking guardrails"`
- `"Guardrails passed"`


#### SELF_CORRECTION

**Progress**: `90%`
**Description**: Auto-correcting errors (if enabled)
**Duration**: Variable

**Message examples**:
- `"Correcting SQL syntax error"`
- `"Retrying with corrected query"`


#### COMPLETED

**Progress**: `100%`
**Description**: Execution completed successfully
**Duration**: N/A (final event)

**Message examples**:
- `"Query completed successfully"`
- `"Execution completed"`

**Fields**:
- `partial_result` field populated with final result


#### FAILED

**Progress**: `0%`
**Description**: Execution failed with error
**Duration**: N/A (terminal event)

**Message examples**:
- `"LLM call failed: API rate limit exceeded"`
- `"Tool execution failed: connection refused"`

**Behavior**: Stream terminates with gRPC error after FAILED event


## gRPC Implementation

### Request Format

```proto
message WeaveRequest {
  string query = 1;          // Required: User query
  string session_id = 2;     // Optional: Session identifier (auto-generated if empty)
  repeated string tools = 3; // Optional: Tool whitelist
}
```

### Client Example

```go
import (
    "context"
    "io"
    "log"

    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func streamExample() error {
    // Connect to server
    conn, err := grpc.Dial("localhost:50051",
        grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        return err
    }
    defer conn.Close()

    client := loomv1.NewLoomServiceClient(conn)

    // Create request
    req := &loomv1.WeaveRequest{
        Query:     "Show me revenue by region",
        SessionId: "sess_abc123",
    }

    // Start stream
    stream, err := client.StreamWeave(context.Background(), req)
    if err != nil {
        return err
    }

    // Receive progress events
    for {
        progress, err := stream.Recv()
        if err == io.EOF {
            break // Stream completed
        }
        if err != nil {
            return err
        }

        // Handle progress event
        log.Printf("[%s] %d%% - %s\n",
            progress.Stage,
            progress.Progress,
            progress.Message)

        if progress.ToolName != "" {
            log.Printf("  Tool: %s\n", progress.ToolName)
        }

        if progress.PartialResult != nil {
            log.Printf("Result: %s\n", progress.PartialResult.DataJson)
        }
    }

    return nil
}
```


## HTTP/SSE Implementation

### Request Format

```http
POST /v1/weave:stream HTTP/1.1
Host: localhost:9090
Content-Type: application/json

{
  "query": "Show me revenue by region",
  "session_id": "sess_abc123"
}
```

### Response Format

```http
HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive

data: {"stage":"EXECUTION_STAGE_PATTERN_SELECTION","progress":10,"message":"Analyzing query and selecting patterns","timestamp":1700000000}

data: {"stage":"EXECUTION_STAGE_LLM_GENERATION","progress":20,"message":"Generating response (turn 1)","timestamp":1700000001}

data: {"stage":"EXECUTION_STAGE_TOOL_EXECUTION","progress":55,"message":"Executing tool: execute_query","tool_name":"execute_query","timestamp":1700000002}

data: {"stage":"EXECUTION_STAGE_COMPLETED","progress":100,"message":"Query completed successfully","timestamp":1700000003,"partial_result":{"type":"text","data_json":"..."}}
```

### curl Example

```bash
# Stream agent execution
curl -N -X POST http://localhost:9090/v1/weave:stream \
  -H "Content-Type: application/json" \
  -d '{
    "query": "What is 2+2?",
    "session_id": "sess_123"
  }'
```

**Output**:
```
data: {"stage":"EXECUTION_STAGE_PATTERN_SELECTION","progress":10,"message":"Analyzing query and selecting patterns","timestamp":1700000000}

data: {"stage":"EXECUTION_STAGE_LLM_GENERATION","progress":20,"message":"Generating response (turn 1)","timestamp":1700000001}

data: {"stage":"EXECUTION_STAGE_COMPLETED","progress":100,"message":"Query completed successfully","timestamp":1700000002,"partial_result":{"type":"text","data_json":"The sum is 4"}}
```

### JavaScript Example

```javascript
async function streamWeave(query, sessionId) {
  const response = await fetch('http://localhost:9090/v1/weave:stream', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({query, session_id: sessionId})
  });

  const reader = response.body.getReader();
  const decoder = new TextDecoder();

  while (true) {
    const {done, value} = await reader.read();
    if (done) break;

    const text = decoder.decode(value);
    const lines = text.split('\n');

    for (const line of lines) {
      if (line.startsWith('data: ')) {
        const progress = JSON.parse(line.substring(6));

        console.log(`[${progress.stage}] ${progress.progress}% - ${progress.message}`);

        if (progress.partial_result) {
          console.log('Result:', progress.partial_result.data_json);
        }
      }
    }
  }
}

// Usage
streamWeave('Show me revenue by region', 'sess_abc123');
```


## Progress Calculation

### Formula

Progress is calculated based on execution stage and iteration count:

```
progress = base_progress + (iteration * increment)
```

### Stage Progress Table

| Stage | Base Progress | Increment | Max |
|-------|---------------|-----------|-----|
| PATTERN_SELECTION | 10% | 0 | 10% |
| SCHEMA_DISCOVERY | 15% | 0 | 15% |
| LLM_GENERATION | 20% | 10% per turn | 40% |
| TOOL_EXECUTION | 50% | 5% per tool | 80% |
| GUARDRAIL_CHECK | 85% | 0 | 85% |
| SELF_CORRECTION | 90% | 0 | 90% |
| COMPLETED | 100% | 0 | 100% |
| FAILED | 0% | 0 | 0% |

### Example Timeline

```
[10%]  PATTERN_SELECTION - Analyzing query
[20%]  LLM_GENERATION - Turn 1
[55%]  TOOL_EXECUTION - execute_query (tool 1)
[30%]  LLM_GENERATION - Turn 2 (20% + 10%)
[60%]  TOOL_EXECUTION - list_tables (tool 2, 50% + 10%)
[85%]  GUARDRAIL_CHECK - Validating
[100%] COMPLETED - Success
```


## Error Handling

### FAILED Stage Event

When execution fails, a `FAILED` stage event is emitted:

```json
{
  "stage": "EXECUTION_STAGE_FAILED",
  "progress": 0,
  "message": "LLM call failed: API rate limit exceeded",
  "timestamp": 1700000000
}
```

### gRPC Error

After FAILED event, stream terminates with gRPC error:

**Error code**: `Internal` (13)
**Error message**: Same as FAILED event message

### HTTP/SSE Error

After FAILED event, SSE stream closes:

**Behavior**: Connection closed, client detects end of stream


### Common Errors

| Error Message | Cause | Resolution |
|---------------|-------|------------|
| `LLM call failed: API rate limit exceeded` | Provider rate limit hit | Retry with exponential backoff |
| `Tool execution failed: connection refused` | Backend service down | Check backend service status |
| `Guardrails exceeded: max turns reached` | Conversation too long | Increase `guardrails.max_turns` |
| `Session not found` | Invalid session_id | Verify session_id or create new session |


## Cancellation

### gRPC Context Cancellation

Clients can cancel streaming at any time:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

stream, _ := client.StreamWeave(ctx, req)

// Later: user presses Ctrl+C
cancel() // Agent stops executing immediately
```

**Behavior**:
- Context cancellation propagates to agent
- Agent stops processing immediately
- Stream terminates with `Canceled` error (1)


### HTTP/SSE Cancellation

Close the HTTP connection:

```javascript
const controller = new AbortController();

fetch('http://localhost:9090/v1/weave:stream', {
  method: 'POST',
  signal: controller.signal,
  body: JSON.stringify({query, session_id})
});

// Later: cancel request
controller.abort();
```

**Behavior**: Server detects connection close and stops agent


## Performance

### Latency

| Metric | Value |
|--------|-------|
| **First event latency** | <1ms |
| **Event throughput** | No buffering delay |
| **Cancellation latency** | <10ms |


### Concurrency

**Concurrent streams**: 10+ clients tested
**Thread safety**: All code race-detector clean
**Memory per stream**: ~10 events buffered (channel size)


### Backpressure

**Buffered channel size**: 10 events
**Behavior**: Agent blocks if client falls behind (prevents memory growth)


## Examples

### Interactive TUI

```go
import "github.com/briandowns/spinner"

func interactiveTUI(client loomv1.LoomServiceClient) {
    sp := spinner.New(spinner.CharSets[9], 100*time.Millisecond)
    sp.Start()

    stream, _ := client.StreamWeave(ctx, req)

    for {
        progress, err := stream.Recv()
        if err == io.EOF {
            sp.Stop()
            break
        }
        if err != nil {
            sp.Stop()
            log.Fatal(err)
        }

        // Update spinner with current message
        sp.Suffix = fmt.Sprintf(" %s (%d%%)", progress.Message, progress.Progress)
    }
}
```


### Web UI Progress Bar

```javascript
// React component
function StreamingQuery({query, sessionId}) {
  const [progress, setProgress] = useState(0);
  const [message, setMessage] = useState('');
  const [toolName, setToolName] = useState('');

  useEffect(() => {
    streamWeave(query, sessionId, (event) => {
      setProgress(event.progress);
      setMessage(event.message);
      setToolName(event.tool_name || '');
    });
  }, [query, sessionId]);

  return (
    <div>
      <progress value={progress} max={100} />
      <div>{message}</div>
      {toolName && <div>Executing: {toolName}</div>}
    </div>
  );
}
```


### Monitoring Dashboard

```go
type ExecutionMetrics struct {
    stages   map[string]int
    latency  time.Duration
    success  int
    failures int
}

func monitorExecution(stream loomv1.LoomService_StreamWeaveClient, metrics *ExecutionMetrics) {
    startTime := time.Now()

    for {
        progress, err := stream.Recv()
        if err != nil {
            if err != io.EOF {
                metrics.failures++
            }
            break
        }

        // Track stage distribution
        metrics.stages[progress.Stage.String()]++

        if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
            metrics.success++
            metrics.latency = time.Since(startTime)
        }
    }
}
```


### Debug Logging

```go
import "go.uber.org/zap"

func debugStream(stream loomv1.LoomService_StreamWeaveClient, logger *zap.Logger) {
    for {
        progress, err := stream.Recv()
        if err == io.EOF {
            logger.Info("Stream completed")
            break
        }
        if err != nil {
            logger.Error("Stream error", zap.Error(err))
            break
        }

        logger.Info("Progress event",
            zap.String("stage", progress.Stage.String()),
            zap.Int32("progress", progress.Progress),
            zap.String("message", progress.Message),
            zap.String("tool", progress.ToolName),
            zap.Int64("timestamp", progress.Timestamp),
        )
    }
}
```


## SubscribeToSession RPC

**Available since**: v1.0.0

### Overview

Real-time updates for async workflow sessions. Streams `SessionUpdate` events when new messages arrive from sub-agents or session state changes.

**Use case**: Async multi-agent workflows where coordinator delegates to sub-agents that complete independently. User receives real-time notifications as sub-agents send results.

### RPC Definition

```proto
rpc SubscribeToSession(SubscribeToSessionRequest) returns (stream SessionUpdate) {
    option (google.api.http) = {
        get: "/v1/sessions/{session_id}:subscribe"
    };
}
```

**Endpoint**: `localhost:50051` (gRPC), `http://localhost:8080/v1/sessions/{session_id}:subscribe` (HTTP/SSE)
**Transport**: gRPC server streaming (bidirectional not required)
**Message Format**: Protocol Buffers (gRPC), JSON (HTTP/SSE)

### Request

```proto
message SubscribeToSessionRequest {
    string session_id = 1; // Required: Session to monitor for updates
}
```

**Parameters**:
- `session_id` (string) - **Required**. Session identifier to subscribe to. Format: `sess_[a-z0-9]+`

**Constraints**:
- Must be valid session ID (matches `^sess_[a-z0-9]+$`)
- Session need not exist when subscribing (subscription waits for session creation)
- Multiple clients can subscribe to same session (pub/sub pattern)

### Response

```proto
message SessionUpdate {
    string session_id = 1;     // Session that generated update
    UpdateType type = 2;       // Type of update (MESSAGE_ENQUEUED, STATE_CHANGED)

    oneof update {
        MessageEnqueuedUpdate message_enqueued = 3;  // New message arrived
        StateChangedUpdate state_changed = 4;        // Session state changed
    }
}

enum UpdateType {
    UPDATE_TYPE_UNSPECIFIED = 0;
    MESSAGE_ENQUEUED = 1;     // Sub-agent sent message via send_message
    STATE_CHANGED = 2;        // Session state transitioned
}

message MessageEnqueuedUpdate {
    string message_id = 1;     // Message identifier for fetching content
    string from_agent = 2;     // Agent that sent message
    string message_type = 3;   // Message type (RESPONSE, QUESTION, DATA)
}

message StateChangedUpdate {
    string new_state = 1;      // New session state (ACTIVE, COMPLETED, FAILED)
    string reason = 2;         // Optional reason for state change
}
```

### Update Types

#### MESSAGE_ENQUEUED

Sent when sub-agent calls `send_message` tool, enqueuing message to coordinator.

**Fields**:
- `message_id`: Unique message identifier (fetch via `GetMessage` RPC)
- `from_agent`: Sub-agent ID that sent message
- `message_type`: `RESPONSE` (answer), `QUESTION` (needs input), `DATA` (artifact)

**Typical flow**:
1. Client receives `MESSAGE_ENQUEUED` update
2. Client calls `GetMessage(message_id)` to fetch content
3. Client displays message to user

#### STATE_CHANGED

Sent when session state transitions (ACTIVE → COMPLETED, ACTIVE → FAILED).

**Fields**:
- `new_state`: `ACTIVE`, `COMPLETED`, `FAILED`
- `reason`: Human-readable explanation (e.g., "All sub-agents completed", "Workflow timeout exceeded")

**Typical flow**:
1. Session starts: `STATE_CHANGED(ACTIVE)`
2. Work proceeds...
3. Workflow completes: `STATE_CHANGED(COMPLETED, "All sub-agents finished")`

### Errors

| gRPC Code | Error | Condition |
|-----------|-------|-----------|
| `INVALID_ARGUMENT` | `session_id empty` | When `session_id` not provided |
| `INVALID_ARGUMENT` | `invalid session_id format` | When `session_id` doesn't match pattern |
| `CANCELLED` | `context cancelled` | When client cancels subscription |
| `UNAVAILABLE` | `server shutting down` | When server terminating |

### Latency

| Metric | Value |
|--------|-------|
| **Subscription setup** | <5ms (listener registration) |
| **Message enqueued → client receives update** | 5-20ms (p50-p99) |
| **Components** | Database insert: 1-3ms, Notification: <1ms, Stream send: 3-15ms |

### Example (Go)

```go
package main

import (
    "context"
    "fmt"
    "io"
    "log"

    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
    "google.golang.org/grpc"
)

func main() {
    conn, _ := grpc.Dial("localhost:50051", grpc.WithInsecure())
    defer conn.Close()

    client := loomv1.NewLoomServiceClient(conn)
    ctx := context.Background()

    // Subscribe to session updates
    stream, err := client.SubscribeToSession(ctx, &loomv1.SubscribeToSessionRequest{
        SessionId: "sess_abc123",
    })
    if err != nil {
        log.Fatalf("Subscription failed: %v", err)
    }

    fmt.Println("Subscribed to session sess_abc123, waiting for updates...")

    // Receive updates
    for {
        update, err := stream.Recv()
        if err == io.EOF {
            fmt.Println("Stream closed by server")
            break
        }
        if err != nil {
            log.Fatalf("Stream error: %v", err)
            break
        }

        switch update.Type {
        case loomv1.UpdateType_MESSAGE_ENQUEUED:
            msgUpdate := update.GetMessageEnqueued()
            fmt.Printf("New message from %s (ID: %s)\n",
                msgUpdate.FromAgent, msgUpdate.MessageId)

            // Fetch and display message
            msg, _ := client.GetMessage(ctx, &loomv1.GetMessageRequest{
                MessageId: msgUpdate.MessageId,
            })
            fmt.Printf("  Content: %s\n", msg.Content)

        case loomv1.UpdateType_STATE_CHANGED:
            stateUpdate := update.GetStateChanged()
            fmt.Printf("Session state: %s (%s)\n",
                stateUpdate.NewState, stateUpdate.Reason)

            if stateUpdate.NewState == "COMPLETED" {
                fmt.Println("Workflow completed, closing subscription")
                return
            }
        }
    }
}

// Output:
// Subscribed to session sess_abc123, waiting for updates...
// New message from weather-analyst (ID: msg_xyz789)
//   Content: Weather forecast for Rome: Sunny, 22°C...
// New message from vacation-planner (ID: msg_def456)
//   Content: Recommended itinerary for Rome...
// Session state: COMPLETED (All sub-agents finished)
// Workflow completed, closing subscription
```

### Example (HTTP/SSE)

```bash
# Subscribe via HTTP Server-Sent Events
curl -N http://localhost:8080/v1/sessions/sess_abc123:subscribe

# Output (streaming):
# data: {"session_id":"sess_abc123","type":"MESSAGE_ENQUEUED","message_enqueued":{"message_id":"msg_xyz789","from_agent":"weather-analyst","message_type":"RESPONSE"}}
#
# data: {"session_id":"sess_abc123","type":"MESSAGE_ENQUEUED","message_enqueued":{"message_id":"msg_def456","from_agent":"vacation-planner","message_type":"RESPONSE"}}
#
# data: {"session_id":"sess_abc123","type":"STATE_CHANGED","state_changed":{"new_state":"COMPLETED","reason":"All sub-agents finished"}}
```

### Cancellation

**Client-initiated**:
```go
ctx, cancel := context.WithCancel(context.Background())
stream, _ := client.SubscribeToSession(ctx, &loomv1.SubscribeToSessionRequest{
    SessionId: "sess_abc123",
})

// Cancel subscription after 30 seconds
time.AfterFunc(30*time.Second, func() {
    cancel() // Stream closes gracefully
})
```

**Server-initiated**:
- Server shutdown: All subscriptions cancelled with `UNAVAILABLE` error
- Session cleanup: Subscription ends when session deleted (after retention period)

### Resource Usage

| Resource | Per Subscription |
|----------|------------------|
| **Goroutine** | 1 (stream handler) |
| **Memory** | ~2KB (channel buffer: 10 updates) |
| **Database connections** | 0 (listener notifications, not polling) |

**Scaling**:
- 10,000 concurrent subscriptions = ~20MB memory
- No polling overhead (event-driven via notification channels)

### Performance Characteristics

**Throughput**:
- Updates per session: ~100/sec (limited by client processing, not server)
- Concurrent subscriptions: 1000+ supported

**Backpressure**:
- Buffered channel (size=10) prevents slow clients from blocking `Enqueue()`
- If buffer full: New updates dropped, warning logged
- Client should process updates promptly to avoid drops

### Integration with Event-Driven Sub-Agents

Sub-agents use `receive_message` tool with notification channels. `SubscribeToSession` extends same pattern to clients:

```
Sub-Agent Flow:                      Client Flow:
MessageQueue.Enqueue()               MessageQueue.Enqueue()
  ├─▶ NotifyChannel                    ├─▶ NotifyChannel
  │   (agent receives via               │   (agent receives)
  │    receive_message)                 │
  └─▶ NotifyListeners                   └─▶ NotifyListeners
      (stream.Send update)                  (stream.Send update)
```

Both use same `Enqueue()` events, ensuring consistent real-time delivery.

### Use Cases

#### 1. TUI Client with Async Workflows

User submits "Plan Rome vacation" → coordinator delegates to weather-analyst and vacation-planner → TUI subscribes to session → receives updates as sub-agents complete → displays results incrementally.

#### 2. Web Dashboard

Browser subscribes via HTTP/SSE → coordinator spawns 10 sub-agents → dashboard shows live progress as each completes → updates workflow visualization in real-time.

#### 3. Long-Running Workflows

Coordinator kicks off 2-hour data analysis workflow → user closes CLI → opens new CLI session later → subscribes to same session → receives all remaining updates → workflow result available when complete.

### Constraints

- **Session lifetime**: Subscriptions automatically closed when session deleted (after retention period)
- **Update buffer**: 10 updates buffered per subscription (older updates dropped if client slow)
- **No history replay**: Subscription starts from "now" (messages enqueued before subscription not replayed)
- **Single session**: Each subscription monitors one session (not multiple sessions)

### Comparison with Polling

| Approach | Latency | Bandwidth | Server Load |
|----------|---------|-----------|-------------|
| **SubscribeToSession (streaming)** | 5-20ms | Low (only on updates) | Low (event-driven) |
| **Polling (GetMessages every 1s)** | 500-1000ms | High (constant requests) | High (database queries every 1s) |

Streaming recommended for all async workflow use cases.

### See Also

- [Multi-Agent Architecture](../architecture/multi-agent.md) - Async workflow design
- [Communication Tools Reference](./tools.md#send_message) - send_message/receive_message tools
- [Session Management](./sessions.md) - Session lifecycle


## Comparison: Weave vs StreamWeave vs SubscribeToSession

| Feature | Weave | StreamWeave | SubscribeToSession |
|---------|-------|-------------|--------------------|
| **Response Type** | Single response (blocks) | Stream of progress events | Stream of session updates |
| **Use Case** | Blocking queries | Interactive progress | Async workflows with sub-agents |
| **User Feedback** | None until complete | Real-time stage updates | Real-time message arrivals |
| **Cancellation** | Not supported | Context cancellation | Context cancellation |
| **Observability** | Final result only | Stage-by-stage visibility | Sub-agent message visibility |
| **First Response** | Blocks until done | <1ms (progress start) | <5ms (subscription setup) |
| **Event Content** | Single result | Stage, progress %, tool name | Message ID, from_agent, state |
| **Session State** | Not tracked | Not tracked | Tracked (ACTIVE, COMPLETED, FAILED) |
| **Multiple Agents** | Single agent | Single agent | Multiple sub-agents (coordinator + workers) |
| **Workflow Pattern** | Request/response | Single-agent execution | Multi-agent delegation |


## Comparison: Weave vs StreamWeave

| Feature | Weave | StreamWeave |
|---------|-------|-------------|
| **Response Type** | Single response (blocks) | Stream of progress events |
| **User Feedback** | None until complete | Real-time updates |
| **Cancellation** | Not supported | Context cancellation |
| **Observability** | Final result only | Stage-by-stage visibility |
| **First Response** | Blocks until done | <1ms |
| **Tool Visibility** | No | Yes (tool_name field) |
| **Progress Percentage** | No | Yes (0-100%) |
| **Error Handling** | Single error at end | FAILED event + error |
| **Use Case** | Scripts, batch jobs | Interactive UIs, long queries |


## Testing

### Integration Tests

**File**: `pkg/server/server_test.go`

**Test functions** (8 total, all passing):
- `TestServer_StreamWeave_BasicStreaming`
- `TestServer_StreamWeave_StageProgression`
- `TestServer_StreamWeave_EmptyQuery`
- `TestServer_StreamWeave_AgentError`
- `TestServer_StreamWeave_ContextCancellation`
- `TestServer_StreamWeave_AutoSessionID`
- `TestServer_StreamWeave_ConcurrentClients`
- `TestServer_StreamWeave_RealProgressEvents`

### Running Tests

```bash
# All StreamWeave tests
go test ./pkg/server -run TestServer_StreamWeave -v

# With race detector (required)
go test ./pkg/server -run TestServer_StreamWeave -race -v

# Extensive race detection
go test ./pkg/server -run TestServer_StreamWeave_ConcurrentClients -race -count=50
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent setup
- [CLI Reference](./cli.md) - Command-line usage
- [LLM Providers](./llm-providers.md) - Provider configuration
