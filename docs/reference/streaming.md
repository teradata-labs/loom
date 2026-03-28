
# Streaming Reference

Technical specification for real-time streaming in Loom via StreamWeave and SubscribeToSession RPCs.

**Version**: v1.2.0
**Protocol**: gRPC server streaming, HTTP/SSE
**API**: `StreamWeave` RPC, `SubscribeToSession` RPC


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
- [SubscribeToSession RPC](#subscribetosession-rpc)
- [Comparison: Weave vs StreamWeave vs SubscribeToSession](#comparison-weave-vs-streamweave-vs-subscribetosession)
- [Comparison: Weave vs StreamWeave](#comparison-weave-vs-streamweave)
- [Testing](#testing)


## Quick Reference

| Feature | Details |
|---------|---------|
| **RPC Name** | `StreamWeave` |
| **Protocol** | gRPC streaming, HTTP/SSE |
| **Request** | `WeaveRequest` |
| **Response** | Stream of `WeaveProgress` |
| **Stages** | 9 execution stages |
| **Progress Range** | 0-100% |
| **First Event Latency** | <1ms |
| **Cancellation** | Context-based |


## Protocol Support

### gRPC Streaming

**RPC Definition**:
```proto
rpc StreamWeave(WeaveRequest) returns (stream WeaveProgress)
```

**Endpoint**: `localhost:60051` (default gRPC port)
**Transport**: gRPC server streaming
**Message Format**: Protocol Buffers


### HTTP/SSE

**Available since**: v0.8.2
**Endpoint**: `POST /v1/weave:stream`
**Transport**: Server-Sent Events (SSE)
**Message Format**: JSON (newline-delimited)

**Configuration**:
```yaml
server:
  http_port: 5006  # default: 5006, 0 = disabled
```

**Enable HTTP/SSE**:
```bash
looms config set server.http_port 5006
looms serve
# Log output includes: {"level":"info","msg":"HTTP/REST+SSE endpoints available","sse_endpoint":"http://0.0.0.0:5006/v1/weave:stream"}
```


## Progress Events

### WeaveProgress Message

```proto
message WeaveProgress {
  ExecutionStage stage = 1;            // Current execution stage
  int32 progress = 2;                  // Progress percentage (0-100)
  string message = 3;                  // Human-readable status message
  string tool_name = 4;               // Tool being executed (optional)
  int64 timestamp = 5;                // Unix timestamp
  ExecutionResult partial_result = 6;  // Final result (COMPLETED stage only)
  HITLRequestInfo hitl_request = 7;    // HITL request info (HUMAN_IN_THE_LOOP stage only)
  string partial_content = 8;          // Accumulated content so far (token streaming)
  bool is_token_stream = 9;           // True if this is a token streaming update
  int32 token_count = 10;             // Running token count (token streaming)
  int64 ttft_ms = 11;                 // Time to first token in milliseconds
  CostInfo cost = 12;                  // Cost information (included in completion event)
  bool is_tool_started = 13;          // True if this is a tool-execution-started event
  bool is_tool_completed = 14;        // True if this is a tool-execution-completed event
  google.protobuf.Struct tool_input = 15;  // Tool input parameters (when is_tool_started)
  google.protobuf.Value tool_result = 16;  // Tool output data (when is_tool_completed)
  string tool_error = 17;             // Error message if tool failed (when is_tool_completed)
  bool tool_success = 18;             // Whether tool succeeded (when is_tool_completed)
  int64 tool_duration_ms = 19;        // Tool execution duration in ms (when is_tool_completed)
  string tool_call_id = 20;           // Unique ID correlating started/completed events
  ContextState context_state = 21;    // Context window state (included in completion event)
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
- `type` (string): Result type (e.g., `"query"`, `"operation"`, `"document"`)
- `data_json` (string): Result data as JSON string
- `row_count` (int32): Row count for query results
- `columns` (repeated string): Column names for tabular results


#### hitl_request

**Type**: `HITLRequestInfo`
**Required**: No (only in `HUMAN_IN_THE_LOOP` stage)
**Available since**: v1.1.0

Carries information about a human-in-the-loop request when the agent needs human input to proceed.

**Fields**:
- `request_id` (string): Request ID (generated by contact_human tool or clarification system)
- `question` (string): Question being asked to the human
- `request_type` (string): Request type (`approval`, `decision`, `input`, `review`, `clarification`)
- `priority` (string): Priority (`low`, `normal`, `high`, `critical`)
- `timeout_seconds` (int32): Timeout in seconds
- `context_json` (string): Additional context as JSON
- `question_id` (string): Question ID for clarification questions (used with `AnswerClarificationQuestion` RPC)
- `options` (repeated string): Suggested options for the user (if applicable)


#### partial_content

**Type**: `string`
**Required**: No (only during token streaming)
**Available since**: v1.1.0

Accumulated content so far during streaming responses. Updated incrementally as the LLM generates tokens.


#### is_token_stream

**Type**: `bool`
**Required**: No
**Available since**: v1.1.0

When `true`, this progress event is a token streaming update. Clients can use this flag to distinguish between stage progress events and incremental token delivery.


#### token_count

**Type**: `int32`
**Required**: No (only during token streaming)
**Available since**: v1.1.0

Running token count during streaming responses. Increases with each token streaming update.


#### ttft_ms

**Type**: `int64`
**Required**: No
**Available since**: v1.2.0

Time to first token in milliseconds. Populated on the first token streaming event.


#### cost

**Type**: `CostInfo`
**Required**: No (included in completion event)
**Available since**: v1.2.0

Cost information for the execution. Included in the final `COMPLETED` event.

**Fields**:
- `total_cost_usd` (double): Total cost in USD
- `llm_cost` (LLMCost): LLM cost breakdown
- `backend_cost_usd` (double): Backend execution cost (if applicable)


#### is_tool_started

**Type**: `bool`
**Required**: No
**Available since**: v1.2.0

When `true`, this event signals the start of a tool execution. Pair with `tool_call_id` to correlate with the corresponding `is_tool_completed` event.


#### is_tool_completed

**Type**: `bool`
**Required**: No
**Available since**: v1.2.0

When `true`, this event signals that a tool execution has finished. Check `tool_success` to determine the outcome.


#### tool_input

**Type**: `google.protobuf.Struct`
**Required**: No (only when `is_tool_started` is true)
**Available since**: v1.2.0

Tool input parameters as a structured object. Populated on tool-started events.


#### tool_result

**Type**: `google.protobuf.Value`
**Required**: No (only when `is_tool_completed` is true)
**Available since**: v1.2.0

Tool output data. Populated on tool-completed events when the tool succeeds.


#### tool_error

**Type**: `string`
**Required**: No (only when `is_tool_completed` is true and tool failed)
**Available since**: v1.2.0

Error message if the tool execution failed.


#### tool_success

**Type**: `bool`
**Required**: No (only when `is_tool_completed` is true)
**Available since**: v1.2.0

Whether the tool execution succeeded.


#### tool_duration_ms

**Type**: `int64`
**Required**: No (only when `is_tool_completed` is true)
**Available since**: v1.2.0

Tool execution duration in milliseconds.


#### tool_call_id

**Type**: `string`
**Required**: No (only on tool lifecycle events)
**Available since**: v1.2.0

Unique identifier that correlates `is_tool_started` and `is_tool_completed` events for the same tool call. Clients can use this to track individual tool executions.


#### context_state

**Type**: `ContextState`
**Required**: No (included in completion event)
**Available since**: v1.2.0

Context window state after the response. Reports how full the context window is.

**Fields**:
- `active_pattern` (string): Active pattern name (empty if none)
- `context_tokens_used` (int64): Context tokens currently used
- `context_tokens_max` (int64): Context token budget (max context window)
- `rom` (string): ROM identifier loaded (empty if none)
- `tools_loaded` (repeated string): Tool names currently registered on the agent


## Execution Stages

### ExecutionStage Enum

```proto
enum ExecutionStage {
  EXECUTION_STAGE_UNSPECIFIED = 0;
  EXECUTION_STAGE_PATTERN_SELECTION = 1;
  EXECUTION_STAGE_SCHEMA_DISCOVERY = 2;
  EXECUTION_STAGE_LLM_GENERATION = 3;
  EXECUTION_STAGE_TOOL_EXECUTION = 4;
  EXECUTION_STAGE_HUMAN_IN_THE_LOOP = 9;
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

**Progress**: `20%` + (turn x 10%)
**Description**: Calling LLM to generate response
**Duration**: 500ms-5s (provider-dependent)

**Message examples**:
- `"Generating response (turn 1)"`
- `"Generating response (turn 2)"`

**Progress calculation**: Increases by 10% per conversation turn

**Token streaming**: When `is_token_stream` is `true`, the event carries `partial_content` and `token_count` fields with incremental token delivery.


#### TOOL_EXECUTION

**Progress**: `50%` + (execution_count x 5%)
**Description**: Executing tools (SQL queries, API calls, etc.)
**Duration**: Variable (query-dependent)

**Message examples**:
- `"Executing tool: execute_query"`
- `"Executing tool: list_tables"`

**Progress calculation**: Increases by 5% per tool execution

**Fields**:
- `tool_name` field populated with executing tool name
- `is_tool_started` / `is_tool_completed` for lifecycle tracking
- `tool_call_id` correlates started/completed events
- `tool_input` (on start), `tool_result` / `tool_error` (on completion)


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
- `cost` field populated with cost information
- `context_state` field populated with context window state


#### FAILED

**Progress**: `0%`
**Description**: Execution failed with error
**Duration**: N/A (terminal event)

**Message examples**:
- `"LLM call failed: API rate limit exceeded"`
- `"Tool execution failed: connection refused"`

**Behavior**: Stream terminates with gRPC error after FAILED event


#### HUMAN_IN_THE_LOOP

**Progress**: Variable (depends on when HITL is triggered)
**Description**: Agent is waiting for human input to proceed
**Duration**: Variable (depends on human response time)
**Available since**: v1.1.0

**Message examples**:
- `"Waiting for human approval"`
- `"Clarification needed: Which database should I query?"`

**Fields**:
- `hitl_request` field populated with `HITLRequestInfo` containing the question, request type, priority, timeout, and options

**Behavior**: Stream pauses until human responds via `AnswerClarificationQuestion` RPC or the request times out


## gRPC Implementation

### Request Format

```proto
message WeaveRequest {
  string query = 1;                    // Required: User query
  string session_id = 2;              // Optional: Session identifier (auto-generated if empty)
  map<string, string> backend_config = 3; // Optional: Backend configuration overrides
  int32 max_rounds = 4;               // Optional: Maximum execution rounds (default: 3)
  int32 timeout_seconds = 5;          // Optional: Timeout in seconds (default: 300)
  map<string, string> context = 6;    // Optional: Context variables for prompt interpolation
  string force_pattern = 7;           // Optional: Force specific pattern (bypass selection)
  bool enable_trace = 8;              // Optional: Enable tracing for this request
  string agent_id = 9;               // Optional: Agent ID to route request to (uses default if empty)
  bool reset_context = 10;            // Optional: Clear context window before processing
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
    conn, err := grpc.NewClient("localhost:60051",
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

        // Handle token streaming
        if progress.IsTokenStream {
            log.Printf("  Tokens: %d, Content: %s\n",
                progress.TokenCount, progress.PartialContent)
        }

        // Handle HITL requests
        if progress.HitlRequest != nil {
            log.Printf("  HITL: %s (type=%s, priority=%s)\n",
                progress.HitlRequest.Question,
                progress.HitlRequest.RequestType,
                progress.HitlRequest.Priority)
        }

        // Handle tool lifecycle events
        if progress.IsToolStarted {
            log.Printf("  Tool started: %s (call_id=%s)\n",
                progress.ToolName, progress.ToolCallId)
        }
        if progress.IsToolCompleted {
            log.Printf("  Tool completed: %s (success=%v, duration=%dms)\n",
                progress.ToolName, progress.ToolSuccess, progress.ToolDurationMs)
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
Host: localhost:5006
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

data: {"stage":"EXECUTION_STAGE_LLM_GENERATION","progress":20,"message":"Generating response (turn 1)","timestamp":1700000001,"is_token_stream":true,"partial_content":"Based on","token_count":2}

data: {"stage":"EXECUTION_STAGE_TOOL_EXECUTION","progress":55,"message":"Executing tool: execute_query","tool_name":"execute_query","timestamp":1700000002,"is_tool_started":true,"tool_call_id":"tc_001"}

data: {"stage":"EXECUTION_STAGE_TOOL_EXECUTION","progress":60,"message":"Tool completed: execute_query","tool_name":"execute_query","timestamp":1700000003,"is_tool_completed":true,"tool_call_id":"tc_001","tool_success":true,"tool_duration_ms":245}

data: {"stage":"EXECUTION_STAGE_COMPLETED","progress":100,"message":"Query completed successfully","timestamp":1700000004,"partial_result":{"type":"text","data_json":"..."}}
```

### curl Example

```bash
# Stream agent execution
curl -N -X POST http://localhost:5006/v1/weave:stream \
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
  const response = await fetch('http://localhost:5006/v1/weave:stream', {
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

        if (progress.is_token_stream) {
          console.log(`  Tokens: ${progress.token_count}, Content: ${progress.partial_content}`);
        }

        if (progress.hitl_request) {
          console.log(`  HITL: ${progress.hitl_request.question}`);
        }

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
| HUMAN_IN_THE_LOOP | Variable | 0 | Variable |

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

fetch('http://localhost:5006/v1/weave:stream', {
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

        // Handle HITL - pause spinner and prompt user
        if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP {
            sp.Stop()
            fmt.Printf("Agent needs input: %s\n", progress.HitlRequest.Question)
            // Prompt user for answer, then call AnswerClarificationQuestion RPC
        }
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
  const [hitlQuestion, setHitlQuestion] = useState(null);
  const [streamContent, setStreamContent] = useState('');

  useEffect(() => {
    streamWeave(query, sessionId, (event) => {
      setProgress(event.progress);
      setMessage(event.message);
      setToolName(event.tool_name || '');

      if (event.is_token_stream) {
        setStreamContent(event.partial_content);
      }

      if (event.hitl_request) {
        setHitlQuestion(event.hitl_request);
      }
    });
  }, [query, sessionId]);

  return (
    <div>
      <progress value={progress} max={100} />
      <div>{message}</div>
      {toolName && <div>Executing: {toolName}</div>}
      {streamContent && <div>{streamContent}</div>}
      {hitlQuestion && <div>Input needed: {hitlQuestion.question}</div>}
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
            zap.Bool("is_token_stream", progress.IsTokenStream),
            zap.Int32("token_count", progress.TokenCount),
        )

        if progress.HitlRequest != nil {
            logger.Info("HITL request",
                zap.String("request_id", progress.HitlRequest.RequestId),
                zap.String("question", progress.HitlRequest.Question),
                zap.String("request_type", progress.HitlRequest.RequestType),
                zap.String("priority", progress.HitlRequest.Priority),
            )
        }
    }
}
```


## SubscribeToSession RPC

**Available since**: v1.0.0

### Overview

Updates for async workflow sessions. Streams `SessionUpdate` events when new messages arrive from sub-agents. ⚠️ Currently only `new_message` updates are emitted; `status_change` updates are proto-defined but not yet implemented server-side.

**Use case**: Async multi-agent workflows where coordinator delegates to sub-agents that complete independently. User receives notifications as sub-agents send results (polled every 500ms).

### RPC Definition

```proto
rpc SubscribeToSession(SubscribeToSessionRequest) returns (stream SessionUpdate) {
    option (google.api.http) = {
        get: "/v1/sessions/{session_id}:subscribe"
    };
}
```

**Endpoint**: `localhost:60051` (gRPC), `http://localhost:5006/v1/sessions/{session_id}:subscribe` (HTTP/SSE)
**Transport**: gRPC server streaming
**Message Format**: Protocol Buffers (gRPC), JSON (HTTP/SSE)

### Request

```proto
message SubscribeToSessionRequest {
    string session_id = 1; // Required: Session to monitor for updates
    string agent_id = 2;   // Optional: Agent ID to filter updates (empty = all agents in session)
}
```

**Parameters**:
- `session_id` (string) - **Required**. Session identifier to subscribe to.
- `agent_id` (string) - **Optional**. 🚧 Agent ID filter is defined in the proto but not yet applied by the server implementation (all messages in the session are delivered regardless of this field).

**Constraints**:
- `session_id` must be a valid session ID
- Session must exist when subscribing (returns `NOT_FOUND` if session does not exist)
- Multiple clients can subscribe to same session

### Response

```proto
message SessionUpdate {
    string session_id = 1;     // Session that generated update
    string agent_id = 2;       // Agent ID that produced this update
    int64 timestamp = 3;       // Timestamp of the update (Unix timestamp in seconds)

    oneof update_type {
        NewMessageUpdate new_message = 4;      // New message added to conversation
        SessionStatusUpdate status_change = 5; // Session status changed
    }
}

message NewMessageUpdate {
    string role = 1;               // Message role (user, assistant, tool, system)
    string content = 2;            // Message content
    int64 message_timestamp = 3;   // Message timestamp (Unix timestamp in seconds)
    string tool_name = 4;          // Optional: Tool name if this is a tool message
    CostInfo cost = 5;             // Optional: Cost information for LLM responses
}

message SessionStatusUpdate {
    string status = 1;    // New status (active, waiting, completed, failed)
    string message = 2;   // Optional status message
}
```

### Update Types

#### new_message

Sent when a new message is added to the session conversation (e.g., from a sub-agent response, tool result, or user input).

**Fields**:
- `role`: Message role - `user`, `assistant`, or `tool` (only these roles are sent)
- `content`: The message text
- `message_timestamp`: When the message was created (Unix timestamp)
- `tool_name`: 🚧 Defined in proto but not yet populated by the server implementation
- `cost`: 🚧 Defined in proto but not yet populated by the server implementation

**Typical flow**:
1. Client receives `SessionUpdate` with `new_message` set
2. Client reads `new_message.role` and `new_message.content`
3. Client displays message to user

#### status_change

🚧 **Status**: Defined in proto but not yet emitted by the server implementation. The current implementation only sends `new_message` updates.

Intended to be sent when the session status transitions (e.g., active to completed, active to failed).

**Fields**:
- `status`: New status - `active`, `waiting`, `completed`, `failed`
- `message`: Human-readable explanation (e.g., "All sub-agents completed", "Workflow timeout exceeded")

**Planned flow**:
1. Session starts: `status_change` with `status="active"`
2. Work proceeds...
3. Workflow completes: `status_change` with `status="completed"`, `message="All sub-agents finished"`

### Errors

| gRPC Code | Error | Condition |
|-----------|-------|-----------|
| `INVALID_ARGUMENT` | `session_id is required` | When `session_id` not provided |
| `NOT_FOUND` | `session not found` | When session does not exist |
| `CANCELLED` | `context cancelled` | When client cancels subscription |
| `UNAVAILABLE` | `server shutting down` | When server terminating |

### Latency

| Metric | Value |
|--------|-------|
| **Subscription setup** | <5ms (session lookup + ticker start) |
| **Message stored to client receives update** | Up to 500ms (polling interval) |
| **Polling interval** | 500ms (ticker-based, loads messages from session store) |

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
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    conn, _ := grpc.NewClient("localhost:60051",
        grpc.WithTransportCredentials(insecure.NewCredentials()))
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

        fmt.Printf("[%s agent=%s] ", update.SessionId, update.AgentId)

        switch v := update.UpdateType.(type) {
        case *loomv1.SessionUpdate_NewMessage:
            fmt.Printf("New message (role=%s): %s\n",
                v.NewMessage.Role, v.NewMessage.Content)
            if v.NewMessage.Cost != nil {
                fmt.Printf("  Cost: $%.6f\n", v.NewMessage.Cost.TotalCostUsd)
            }

        case *loomv1.SessionUpdate_StatusChange:
            fmt.Printf("Status changed: %s (%s)\n",
                v.StatusChange.Status, v.StatusChange.Message)

            if v.StatusChange.Status == "completed" {
                fmt.Println("Workflow completed, closing subscription")
                return
            }
        }
    }
}

// Output:
// Subscribed to session sess_abc123, waiting for updates...
// [sess_abc123 agent=weather-analyst] New message (role=assistant): Weather forecast for Rome: Sunny, 22C...
// [sess_abc123 agent=vacation-planner] New message (role=assistant): Recommended itinerary for Rome...
// [sess_abc123 agent=coordinator] Status changed: completed (All sub-agents finished)
// Workflow completed, closing subscription
```

### Example (HTTP/SSE)

```bash
# Subscribe via HTTP Server-Sent Events
curl -N http://localhost:5006/v1/sessions/sess_abc123:subscribe

# Output (streaming):
# data: {"session_id":"sess_abc123","agent_id":"weather-analyst","timestamp":1700000001,"new_message":{"role":"assistant","content":"Weather forecast for Rome: Sunny, 22C...","message_timestamp":1700000001}}
#
# data: {"session_id":"sess_abc123","agent_id":"vacation-planner","timestamp":1700000002,"new_message":{"role":"assistant","content":"Recommended itinerary for Rome...","message_timestamp":1700000002}}
#
# data: {"session_id":"sess_abc123","agent_id":"coordinator","timestamp":1700000003,"status_change":{"status":"completed","message":"All sub-agents finished"}}
```

### Example (Filtered by Agent)

🚧 **Note**: Agent-level filtering is defined in the proto but not yet implemented server-side. All session messages are delivered regardless of `agent_id`.

```go
// Subscribe to updates from a specific agent only (filtering not yet implemented)
stream, err := client.SubscribeToSession(ctx, &loomv1.SubscribeToSessionRequest{
    SessionId: "sess_abc123",
    AgentId:   "weather-analyst", // Intended: only receive updates from this agent
})
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
| **Goroutine** | 1 (stream handler with ticker) |
| **Memory** | ~2KB (ticker + message tracking state) |
| **Database queries** | 1 per 500ms (loads messages from session store) |

**Scaling**:
- Each subscription polls the session store every 500ms
- High subscription counts increase database read load proportionally

### Performance Characteristics

**Throughput**:
- Updates per session: Up to 2/sec per polling interval (500ms)
- Concurrent subscriptions: Supported (limited by database load from polling)

**Implementation note**:
- Current implementation uses polling (500ms `time.Ticker`), not event-driven channels
- Each tick loads all messages from the session store and sends new ones to the client
- No backpressure mechanism needed since updates are sent synchronously per tick

### Integration with Sub-Agents

Sub-agents receive messages via event-driven auto-delivery through the `MessageQueue`. `SubscribeToSession` provides client visibility into these messages using a different mechanism:

```
Sub-Agent Flow:                      Client Flow (SubscribeToSession):
MessageQueue.Enqueue()               Ticker (every 500ms)
  |-> NotifyChannel                    |-> sessionStore.LoadMessages()
  |   (agent receives via               |-> Compare with lastMessageCount
  |    auto-delivery)                   |-> stream.Send(new updates)
```

Sub-agents use event-driven `MessageQueue` delivery. `SubscribeToSession` uses polling-based message detection from the session store.

### Use Cases

#### 1. TUI Client with Async Workflows

User submits "Plan Rome vacation" then coordinator delegates to weather-analyst and vacation-planner, TUI subscribes to session, receives updates as sub-agents complete, and displays results incrementally.

#### 2. Web Dashboard

Browser subscribes via HTTP/SSE, coordinator spawns 10 sub-agents, dashboard shows live progress as each completes, updates workflow visualization in real-time.

#### 3. Long-Running Workflows

Coordinator kicks off 2-hour data analysis workflow. User closes CLI, opens new CLI session later, subscribes to same session, receives updates for new messages going forward (messages sent while disconnected are not replayed, but can be retrieved via `GetConversationHistory`).

### Constraints

- **Session must exist**: Returns `NOT_FOUND` if the session does not exist at subscription time
- **No history replay**: Subscription starts from "now" (messages stored before subscription are not replayed)
- **Single session**: Each subscription monitors one session (not multiple sessions)
- **Polling-based**: Current implementation polls the session store every 500ms (not event-driven)

### Comparison with Client-Side Polling

| Approach | Latency | Bandwidth | Server Load |
|----------|---------|-----------|-------------|
| **SubscribeToSession (server-side polling, gRPC stream)** | Up to 500ms | Low (gRPC stream, only new messages sent) | Moderate (database query every 500ms per subscription) |
| **Client-side polling (GetConversationHistory every 1s)** | 500-1000ms | High (full request/response per poll) | High (database queries + gRPC overhead per poll) |

`SubscribeToSession` is preferred over client-side polling because it reduces gRPC overhead and only sends new messages over the stream. Note: the server-side implementation currently uses 500ms polling internally.

### See Also

- [Multi-Agent Architecture](../architecture/multi-agent.md) - Async workflow design
- [Tool Registry Reference](./tool-registry.md) - Tool registration and discovery
- [Agent Configuration Reference](./agent-configuration.md) - Session and agent setup


## Comparison: Weave vs StreamWeave vs SubscribeToSession

| Feature | Weave | StreamWeave | SubscribeToSession |
|---------|-------|-------------|--------------------|
| **Response Type** | Single response (blocks) | Stream of progress events | Stream of session updates |
| **Use Case** | Blocking queries | Interactive progress | Async workflows with sub-agents |
| **User Feedback** | None until complete | Real-time stage updates | Near-real-time message arrivals (500ms polling) |
| **Cancellation** | Not supported | Context cancellation | Context cancellation |
| **Observability** | Final result only | Stage-by-stage visibility | Sub-agent message visibility |
| **First Response** | Blocks until done | <1ms (progress start) | <5ms (subscription setup) |
| **Event Content** | Single result | Stage, progress %, tool name, HITL | Message role/content, status |
| **Session State** | Not tracked | Not tracked | 🚧 Proto-defined but status_change events not yet emitted |
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
| **Tool Visibility** | No | Yes (tool_name, tool lifecycle events) |
| **Progress Percentage** | No | Yes (0-100%) |
| **Token Streaming** | No | Yes (partial_content, token_count) |
| **HITL Support** | No | Yes (hitl_request field) |
| **Error Handling** | Single error at end | FAILED event + error |
| **Use Case** | Scripts, batch jobs | Interactive UIs, long queries |


## Testing

### Integration Tests

**File**: `pkg/server/integration_test.go`

**Test functions** (8 total, all passing):
- `TestServer_StreamWeave_Success`
- `TestServer_StreamWeave_ProgressStages`
- `TestServer_StreamWeave_EmptyQuery`
- `TestServer_StreamWeave_AgentError`
- `TestServer_StreamWeave_ContextCancellation`
- `TestServer_StreamWeave_GeneratesSessionID`
- `TestServer_StreamWeave_MultipleClients`
- `TestServer_StreamWeave_RealProgressEvents`

### Running Tests

```bash
# All StreamWeave tests
go test -tags fts5 ./pkg/server -run TestServer_StreamWeave -v

# With race detector (required)
go test -tags fts5 ./pkg/server -run TestServer_StreamWeave -race -v

# Extensive race detection
go test -tags fts5 ./pkg/server -run TestServer_StreamWeave_MultipleClients -race -count=50
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent setup
- [CLI Reference](./cli.md) - Command-line usage
- [LLM Providers](./llm-providers.md) - Provider configuration
