
# Data Flow Architecture

End-to-end data flows showing how information moves through Loom's multi-layered system, from client request through agent execution, memory management, tool invocation, and observability.

**Target Audience**: Architects, academics, and advanced developers

**Version**: v1.0.0-beta.1


## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Data Flow Categories](#data-flow-categories)
- [Core Flows](#core-flows)
  - [Agent Conversation Flow](#agent-conversation-flow)
  - [Tool Execution Flow](#tool-execution-flow)
  - [Memory Management Flow](#memory-management-flow)
  - [Pattern Matching Flow](#pattern-matching-flow)
- [Advanced Flows](#advanced-flows)
  - [Reference-Based Data Passing](#reference-based-data-passing)
  - [Multi-Agent Workflow Flow](#multi-agent-workflow-flow)
  - [Session Recovery Flow](#session-recovery-flow)
  - [Pattern Hot-Reload Flow](#pattern-hot-reload-flow)
- [Cross-Cutting Flows](#cross-cutting-flows)
  - [Observability Trace Flow](#observability-trace-flow)
  - [Cost Attribution Flow](#cost-attribution-flow)
- [Data Structures](#data-structures)
- [Flow Properties](#flow-properties)
- [Performance Characteristics](#performance-characteristics)
- [Related Work](#related-work)
- [References](#references)
- [Further Reading](#further-reading)


## Overview

Loom's architecture involves complex data flows across 10+ subsystems. This document traces data movement through:

1. **Agent Conversation Flow**: User message → LLM invocation → tool execution → response
2. **Tool Execution Flow**: Tool call → backend query → result processing → memory storage
3. **Memory Management Flow**: Message addition → L1 cache → L2 compression → SQLite persistence
4. **Pattern Matching Flow**: Query → TF-IDF search → pattern selection → prompt injection
5. **Reference-Based Data Passing**: Large result → compression → shared memory → reference ID
6. **Multi-Agent Workflow Flow**: YAML load → pattern routing → stage execution → result merge
7. **Session Recovery Flow**: Crash → session load → memory reconstruction → resume
8. **Pattern Hot-Reload Flow**: File change → fsnotify → YAML parse → atomic swap
9. **Observability Trace Flow**: Span creation → attribute collection → batch export → Hawk
10. **Cost Attribution Flow**: Token counting → pricing calculation → aggregation → trace export

**Key Innovation**: Data flows show complete lifecycle including error paths, retry logic, and observability integration.


## Design Goals

1. **End-to-End Visibility**: Trace data from client request to Hawk export
2. **Error Path Documentation**: Show not just happy paths but failure handling
3. **Performance Bottlenecks**: Identify critical paths and latency sources
4. **Memory Efficiency**: Document where data is compressed, cached, or evicted
5. **Concurrency Patterns**: Show parallel execution and synchronization points

**Non-goals**:
- Code-level implementation details (see architecture docs for that)
- API specifications (see reference docs)
- Configuration examples (see guides)


## System Context

```mermaid
flowchart TB
    subgraph External["External Environment"]
        Client[Client]
        LLM[LLM]
        Backend[Backend]
        Hawk[Hawk]
        Promptio[Promptio]
    end

    subgraph LoomSystem["Loom System"]
        ClientReq[Client Request] --> AgentRuntime[Agent Runtime]
        AgentRuntime --> MemoryManager[Memory Manager]
        MemoryManager --> LLMProvider[LLM Provider]
        LLMProvider --> ToolsExecutor[Tools Executor]
        ToolsExecutor --> BackendQuery[Backend Query]
        BackendQuery --> ToolResults[Tool Results]
        ToolResults --> LLMResponse[LLM Response]
        LLMResponse --> MemoryUpdate[Memory Update]
        MemoryUpdate --> ClientResponse[Client Response]
        ClientResponse --> ObsTrace[Observability Trace]
    end

    Client --> LoomSystem
    LLM --> LoomSystem
    Backend --> LoomSystem
    Hawk --> LoomSystem
    Promptio --> LoomSystem
```

**Data Flow Direction**:
- **→**: Synchronous data flow (blocking)
- **- - →**: Asynchronous data flow (non-blocking)
- **◀**: Response/return data flow


## Data Flow Categories

### 1. Request-Response Flows
- Agent conversation (client → agent → LLM → client)
- Tool execution (LLM → executor → backend → executor → LLM)
- Pattern matching (query → matcher → patterns → prompt)

### 2. State Management Flows
- Memory management (messages → L1 → L2 → SQLite)
- Session persistence (session → SQLite → recovery)
- Shared memory (large data → compression → cache → disk)

### 3. Configuration Flows
- Pattern hot-reload (file change → fsnotify → parser → agent)
- Agent config reload (YAML edit → watcher → validation → swap)
- Prompt versioning (promptio → cache → memory ROM)

### 4. Observability Flows
- Trace export (span creation → buffering → batch → Hawk)
- Metrics collection (counters → aggregation → export)
- Cost tracking (token counting → pricing → attribution → trace)


## Core Flows

### Agent Conversation Flow

**Description**: Complete lifecycle of a user message through the agent conversation loop.

**Sequence Diagram**:
```
Client         Agent        Memory       Pattern      LLM         Executor     Backend      Tracer
  │              │            │          Matcher    Provider       │            
  ├─ POST ──────▶│            │            │           │           │            
  │  /chat       │            │            │           │           │            
  │  sessionID   ├─ StartSpan ┼────────────┼───────────┼───────────┼────────────
  │  message     │            │            │           │           │            
  │              ├─ Get ──────▶│            │           │           │           
  │              │  Session    │            │           │           │           
  │              │◀─ Session ──┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Add ───────▶│            │           │           │          
  │              │  Message    │            │           │           │           
  │              │  (user)     │            │           │           │           
  │              │◀─ OK ───────┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Match ─────┼───────────▶│           │           │           
  │              │  Pattern    │            │           │           │           
  │              │◀─ Pattern ──┼────────────┤           │           │           
  │              │             │            │           │           │           
  │              ├─ Build ─────▶│            │           │           │          
  │              │  Context    │            │           │           │           
  │              │  ROM+Kernel+L1+L2        │           │           │           
  │              │◀─ Messages ─┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Invoke ────┼────────────┼──────────▶│           │           
  │              │  LLM        │            │           │           │           
  │              │             │            │           ├─ API ─────┼───────────
  │              │             │            │           │  Call     │           
  │              │             │            │           │◀─ Stream ─┼───────────
  │              │             │            │           │  Response │           
  │              │             │            │           │           │           
  │              │             │            │           ├─ Stop ────┤           
  │              │             │            │           │  Reason:  │           
  │              │             │            │           │  tool_use │           
  │              │             │            │           │           │           
  │              │◀─ Tool ─────┼────────────┼───────────┤           │           
  │              │  Calls      │            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Execute ───┼────────────┼───────────┼──────────▶│           
  │              │  Tools      │            │           │           │           
  │              │  [get_schema, execute_sql]           │            │          
  │              │             │            │           │           │           
  │              │             │            │           │           ├─ Query ───
  │              │             │            │           │           │           
  │              │             │            │           │           │◀─ Result ─
  │              │             │            │           │           │           
  │              │◀─ Results ──┼────────────┼───────────┼───────────┤           
  │              │             │            │           │           │           
  │              ├─ Store ─────▶│            │           │           │          
  │              │  Results    │            │           │           │           
  │              │  in L1      │            │           │           │           
  │              │◀─ OK ───────┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Check ─────▶│            │           │           │          
  │              │  L1 Full?   │            │           │           │           
  │              │◀─ Yes ──────┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Compress ──▶│            │           │           │          
  │              │  Oldest 5   │            │           │           │           
  │              │  to L2      ├─ LLM ──────┼───────────┼──────────▶│           
  │              │             │  Summary   │           │  Call     │           
  │              │             │◀─ Summary ─┼───────────┼───────────┤           
  │              │◀─ Compressed┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Invoke ────┼────────────┼───────────┼──────────▶│           
  │              │  LLM #2     │            │           │  (with tool results)  
  │              │             │            │           │           │           
  │              │◀─ Final ────┼────────────┼───────────┤           │           
  │              │  Response   │            │           │           │           
  │              │  (end_turn) │            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Add ───────▶│            │           │           │          
  │              │  Message    │            │           │           │           
  │              │  (assistant)│            │           │           │           
  │              │◀─ OK ───────┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ Persist ───▶│            │           │           │          
  │              │  Session    ├─ SQLite ───┤           │           │           
  │              │◀─ OK ───────┤            │           │           │           
  │              │             │            │           │           │           
  │              ├─ EndSpan ───┼────────────┼───────────┼───────────┼───────────
  │              │  (metrics)  │            │           │           │           
  │              │             │            │           │           │           
  │◀─ Response ──┤             │            │           │           │           
  │  200 OK      │             │            │           │           │           
  │  content     │             │            │           │           │           
  │  usage       │             │            │           │           │           
  │  cost        │             │            │           │           │           
  │              │             │            │           │           │           
```

**Data Transformations**:
1. **User message** → String (UTF-8, 1-10K chars typical)
2. **Pattern match** → Matched pattern + score (TF-IDF cosine similarity)
3. **Context build** → Concatenated messages (ROM 5K + Kernel 2K + L1 10K + L2 3K ≈ 20K tokens)
4. **LLM request** → JSON with messages array + tools array
5. **LLM response** → Streaming chunks → final Response proto
6. **Tool calls** → Parallel goroutine execution → aggregated results
7. **L1 overflow** → LLM summarization → compressed L2 entry
8. **Session persist** → SQLite row (session_id, messages JSONB, metadata)

**Performance Metrics**:
- **P50 latency**: 1.2s (LLM call dominates)
- **P99 latency**: 3.5s (tool execution + LLM)
- **Token usage**: 20K input, 500 output (typical)
- **Cost**: $0.015-0.045 per turn (Claude Sonnet 4.5)
- **Memory**: 50KB session state (10 messages)


### Tool Execution Flow

**Description**: Concurrent tool execution with error handling and result processing.

**Sequence Diagram**:
```
LLM Response    Executor     Tool A       Tool B       Backend A    Backend B    ErrorStore   SharedMem
  │               │            │            │             │            │        
  ├─ tool_use ───▶│            │            │             │            │        
  │  [toolA, toolB]            │            │             │            │        
  │               │            │            │             │            │        
  │               ├─ Spawn ────▶│            │             │            │       
  │               │  Goroutine 1            │             │            │        
  │               │            │            │             │            │        
  │               ├─ Spawn ────┼───────────▶│             │            │        
  │               │  Goroutine 2            │             │            │        
  │               │            │            │             │            │        
  │               │            ├─ Validate ─┤             │            │        
  │               │            │  Input     │             │            │        
  │               │            │            │             │            │        
  │               │            ├─ Execute ──┼────────────▶│            │        
  │               │            │            │             │            │        
  │               │            │            ├─ Validate ─┤            │         
  │               │            │            │  Input     │            │         
  │               │            │            │            │            │         
  │               │            │            ├─ Execute ──┼────────────┼─────────
  │               │            │            │            │            │         
  │               │            │            │◀─ Result ──┼────────────┤         
  │               │            │            │  (10K rows)│            │         
  │               │            │            │  1.3MB JSON│            │         
  │               │            │            │            │            │         
  │               │            │            ├─ Check ────┼────────────┼─────────
  │               │            │            │  Size      │            │         
  │               │            │            │  >100KB?   │            │         
  │               │            │            │            │            │         
  │               │            │            ├─ Store ────┼────────────┼─────────
  │               │            │            │  Data      │            │         
  │               │            │            │  (compress)│            │         
  │               │            │            │◀─ Ref ID ──┼────────────┼─────────
  │               │            │            │  ref_abc123│            │         
  │               │            │            │            │            │         
  │               │            │◀─ Result ──┤            │            │         
  │               │            │  (reference)            │            │         
  │               │            │            │            │            │         
  │               │            │◀─ ERROR ───┼────────────┤            │         
  │               │            │  (3K chars)│            │            │         
  │               │            │            │            │            │         
  │               │            ├─ Store ────┼────────────┼────────────┼─────────
  │               │            │  Error     │            │            │         
  │               │            │◀─ Error ID ┼────────────┼────────────┼─────────
  │               │            │  err_xyz789│            │            │         
  │               │            │            │            │            │         
  │               │◀─ Results ─┤            │            │            │         
  │               │  [success(ref), error(id)]          │            │          
  │               │            │            │            │            │         
  │◀─ Tool ───────┤            │            │            │            │         
  │  Results      │            │            │            │            │         
  │  [{content: "ref_abc123"}, {error: "err_xyz789"}]   │            │          
  │               │            │            │            │            │         
```

**Data Transformations**:
1. **Tool calls** → Parallel goroutine spawning (N goroutines for N tools)
2. **Input validation** → JSON schema validation against tool definition
3. **Backend execution** → Backend-specific query/API call
4. **Large result** → Size check (>100KB) → compression (gzip) → SharedMemory → Reference ID
5. **Tool error** → Stack trace (3K chars) → ErrorStore → Summary (100 chars) + Error ID
6. **Result aggregation** → Channel collection → ordered by completion
7. **Timeout handling** → Context cancellation after 30s → partial results returned

**Error Handling**:
- **Validation error**: Return immediately with error, no backend call
- **Execution error**: Store full error, return summary + ID
- **Timeout**: Cancel goroutine, return timeout error
- **Circuit breaker open**: Return cached error, no backend call

**Performance Metrics**:
- **P50 latency**: 500ms (single tool)
- **P99 latency**: 1.6s (parallel 3 tools)
- **Throughput**: 100+ tools/s (goroutine-based)
- **Memory**: 10KB per tool execution (goroutine stack + buffers)
- **Max concurrency**: 20 tools (configurable)


### Memory Management Flow

**Description**: Segmented memory lifecycle from message addition to L2 compression and SQLite persistence.

**Sequence Diagram**:
```
Agent         Memory       L1 Cache     L2 Archive    LLM          SQLite       Tracer
  │             │            │            │          Provider       │           
  ├─ Add ──────▶│            │            │            │            │           
  │  Message    │            │            │            │            │           
  │  (assistant)│            │            │            │            │           
  │             │            │            │            │            │           
  │             ├─ Append ───▶│            │            │            │          
  │             │  to L1      │            │            │            │          
  │             │◀─ OK ───────┤            │            │            │          
  │             │             │            │            │            │          
  │             ├─ Check ─────▶│            │            │            │         
  │             │  L1.Size()  │            │            │            │          
  │             │◀─ 11 msgs ──┤            │            │            │          
  │             │  (limit: 10)│            │            │            │          
  │             │             │            │            │            │          
  │             ├─ Calculate ─▶│            │            │            │         
  │             │  Tokens     │            │            │            │          
  │             │◀─ 8500 ─────┤            │            │            │          
  │             │  (budget: 8000)          │            │            │          
  │             │             │            │            │            │          
  │             ├─ Evict ─────▶│            │            │            │         
  │             │  Oldest     │            │            │            │          
  │             │◀─ 5 msgs ───┤            │            │            │          
  │             │  to compress│            │            │            │          
  │             │             │            │            │            │          
  │             ├─ Build ─────┤            │            │            │          
  │             │  Summary    │            │            │            │          
  │             │  Prompt     │            │            │            │          
  │             │             │            │            │            │          
  │             ├─ Invoke ────┼────────────┼────────────┼───────────▶│          
  │             │  LLM        │            │            │            │          
  │             │  (summarize)│            │            │            │          
  │             │◀─ Summary ──┼────────────┼────────────┼────────────┤          
  │             │  "User asked about sales, agent queried DB..."     │          
  │             │             │            │            │            │          
  │             ├─ Store ─────┼────────────▶│            │            │         
  │             │  Summary    │            │            │            │          
  │             │  in L2      │            │            │            │          
  │             │◀─ OK ───────┼────────────┤            │            │          
  │             │             │            │            │            │          
  │             ├─ Persist ───┼────────────┼────────────┼────────────┼──────────
  │             │  Session    │            │            │            │          
  │             │  (L1+L2)    │            │            │            │          
  │             │◀─ OK ───────┼────────────┼────────────┼────────────┼──────────
  │             │             │            │            │            │          
  │             ├─ Record ────┼────────────┼────────────┼────────────┼──────────
  │             │  Event      │            │            │            │          
  │             │  memory.l2_compression_triggered     │            │           
  │             │             │            │            │            │          
  │◀─ OK ───────┤             │            │            │            │          
  │             │             │            │            │            │          
```

**Memory Budget Calculation**:
```
Total Context: 200,000 tokens (Claude Sonnet 4.5)
Reserved Output: 8,000 tokens
Available Input: 192,000 tokens

Memory Allocation:
├─ ROM (30%): 57,600 tokens                                                     
│  ├─ System prompt: 2,000 tokens                                              │
│  ├─ Tool definitions: 3,000 tokens (30 tools)                                │
│  └─ Pattern library: 52,600 tokens (cached)                                  │
│                                                                              │
├─ Kernel (10%): 19,200 tokens                                                  
│  ├─ Task context: 500 tokens                                                 │
│  ├─ User preferences: 200 tokens                                             │
│  └─ Session metadata: 18,500 tokens                                          │
│                                                                              │
├─ L1 Cache (50%): 96,000 tokens                                                
│  └─ Recent messages: 10-15 messages (6K-9K tokens each)                      │
│                                                                              │
└─ L2 Archive (10%): 19,200 tokens                                              
   └─ Compressed summaries: 5-10 summaries (2K-4K tokens each)                  
```

**Eviction Policy**:
1. **Trigger**: L1.TokenCount() > L1.Budget OR L1.MessageCount() > 10
2. **Selection**: Oldest 5 messages by timestamp (FIFO)
3. **Compression**: LLM summarization (10:1 ratio typical)
4. **Storage**: L2 append + SQLite persist
5. **Cleanup**: Remove evicted messages from L1

**Performance Metrics**:
- **Compression latency**: 800ms-1.5s (LLM call)
- **Compression ratio**: 10:1 (5 messages → 1 summary)
- **SQLite write**: 5-15ms (single transaction)
- **Memory overhead**: 50KB per session (L1 + L2 in RAM)


### Pattern Matching Flow

**Description**: TF-IDF-based pattern selection from library with confidence scoring.

**Sequence Diagram**:
```
Agent       Orchestrator   PatternLib    TF-IDF      Patterns     Scorer      Memory
  │             │              │          Index        YAML         │           
  ├─ Match ────▶│              │            │           │           │           
  │  Pattern    │              │            │           │           │           
  │  query="show sales"        │            │           │           │           
  │             │              │            │           │           │           
  │             ├─ Search ─────▶│            │           │           │          
  │             │              │            │           │           │           
  │             │              ├─ Tokenize ─▶│           │           │          
  │             │              │  ["show", "sales"]      │           │          
  │             │              │◀─ Tokens ───┤           │           │          
  │             │              │            │           │           │           
  │             │              ├─ Compute ───▶│           │           │         
  │             │              │  TF-IDF     │           │           │          
  │             │              │◀─ Vector ────┤           │           │         
  │             │              │  [0.3, 0.7, ...]         │           │         
  │             │              │            │           │           │           
  │             │              ├─ Cosine ────▶│           │           │         
  │             │              │  Similarity │           │           │          
  │             │              │◀─ Scores ────┤           │           │         
  │             │              │  [0.85, 0.72, 0.31, ...]│           │          
  │             │              │            │           │           │           
  │             │              ├─ Top-K ──────┤           │           │         
  │             │              │  (k=3)      │           │           │          
  │             │              │◀─ Patterns ──┤           │           │         
  │             │              │  [{id, score}, ...]      │           │         
  │             │              │            │           │           │           
  │             │              ├─ Load ───────┼───────────▶│           │        
  │             │              │  Pattern    │           │           │          
  │             │              │  YAML       │           │           │          
  │             │              │◀─ Content ───┼───────────┤           │         
  │             │              │            │           │           │           
  │             │              ├─ Evaluate ───┼───────────┼──────────▶│         
  │             │              │  Confidence │           │           │          
  │             │              │◀─ Score ─────┼───────────┼───────────┤         
  │             │              │  0.92       │           │           │          
  │             │              │            │           │           │           
  │             │◀─ Pattern ───┤            │           │           │           
  │             │  (best match)│            │           │           │           
  │             │            │           │           │           │           │  
  │             ├─ Inject ─────┼────────────┼───────────┼───────────┼──────────▶
  │             │  Pattern    │            │           │           │           │
  │             │  into ROM   │            │           │           │           │
  │◀─ Pattern ──┤            │           │           │           │           │  
  │  Matched    │            │           │           │           │           │  
  │             │            │           │           │           │           │  
```

**TF-IDF Calculation**:
```
Query: "show sales by region"
Tokenize: ["show", "sales", "by", "region"]

Pattern 1: "Display sales metrics grouped by geographic region"
  TF-IDF: [0.3, 0.8, 0.2, 0.9]
  Cosine Similarity: 0.85

Pattern 2: "Analyze revenue data per territory"
  TF-IDF: [0.1, 0.7, 0.1, 0.6]
  Cosine Similarity: 0.72

Pattern 3: "Create user account with role assignment"
  TF-IDF: [0.0, 0.0, 0.1, 0.0]
  Cosine Similarity: 0.12

Result: Pattern 1 selected (highest score: 0.85)
```

**Performance Metrics**:
- **Index build**: 89-143ms (59 patterns, 11 libraries)
- **Search latency**: 5-15ms (TF-IDF lookup + scoring)
- **Memory overhead**: 2MB (TF-IDF vectors + pattern cache)
- **Match accuracy**: 92% (validated via Judge system)


## Advanced Flows

### Reference-Based Data Passing

**Description**: Large dataset storage with reference semantics to prevent token overflow.

**Sequence Diagram**:
```
Backend     Executor    SharedMem    Policy       Compression   Agent        LLM
  │            │           │         Manager         Engine       │           │ 
  ├─ Result ──▶│           │           │               │          │           │ 
  │  (10K rows)│           │           │               │          │           │ 
  │  1.3MB JSON│           │           │               │          │           │ 
  │            │           │           │               │          │           │ 
  │            ├─ Check ───┼──────────▶│               │          │           │ 
  │            │  Policy   │           │               │          │           │ 
  │            │  (tool_result, size=1.3MB)            │          │           │ 
  │            │◀─ Decision ┼───────────┤               │          │           │
  │            │  REFERENCE │           │               │          │           │
  │            │  (>10KB)   │           │               │          │           │
  │            │           │           │               │          │           │ 
  │            ├─ Compress ─┼───────────┼───────────────▶│          │           
  │            │  Data      │           │               │          │           │
  │            │  (zstd)    │           │               │          │           │
  │            │◀─ Compressed┼───────────┼───────────────┤          │           
  │            │  120KB     │           │               │          │           │
  │            │  (91% savings)         │               │          │           │
  │            │           │           │               │          │           │ 
  │            ├─ Store ────▶│           │               │          │           
  │            │  Data      │           │               │          │           │
  │            │  Memory Tier│           │               │          │           
  │            │◀─ Ref ID ───┤           │               │          │           
  │            │  ref_abc123│           │               │          │           │
  │            │  checksum  │           │               │          │           │
  │            │  size      │           │               │          │           │
  │            │           │           │               │          │           │ 
  │            ├─ Format ───┤           │               │          │           │
  │            │  Result    │           │               │          │           │
  │            │  "[Large dataset stored: ref_abc123]" │          │           │ 
  │            │           │           │               │          │           │ 
  │            ├─ Return ───┼───────────┼───────────────┼─────────▶│           │
  │            │  Reference │           │               │          │           │
  │            │           │           │               │          │           │ 
  │            │           │           │               │          ├─ Context ─▶│
  │            │           │           │               │          │  (50 tokens 
  │            │           │           │               │          │           │ 
  │            │           │           │               │          │◀─ Action ──┤
  │            │           │           │               │          │  get_tool_re
  │            │           │           │               │          │           │ 
  │            │           │           │               │          ├─ Resolve ─▶│
  │            │           │           │               │          │  Reference │
  │            │◀─ Data ────┤           │               │          │◀─ Full ────
  │            │  (decompressed)        │               │          │  Data      
  │            │           │           │               │          │  1.3MB     │
  │            │           │           │               │          │           │ 
```

**Token Savings**:
```
Before (Value Semantics):
├─ Result Size: 1.3MB JSON                                                      
├─ Token Count: ~15,000 tokens                                                  
├─ Context Budget: Exceeded (max 8K for results)                                
└─ Outcome: Truncation or failure                                               

After (Reference Semantics):
├─ Reference ID: "ref_abc123"
├─ Token Count: ~50 tokens
├─ Savings: 99.67%
└─ Outcome: Full data available on-demand via query_tool_result()                 
```

**Storage Tiers**:
1. **Memory Tier** (1GB LRU cache):
   - Hot data: <1ms retrieval
   - Compressed: >1MB auto-compress with zstd
   - Checksum: SHA-256 for integrity
   - Reference counting: Prevent premature eviction

2. **Disk Tier** (10GB filesystem):
   - Cold data: 5-15ms retrieval
   - Auto-promotion: Disk → memory on access
   - TTL cleanup: 1 hour default
   - Atomic writes: temp + rename

**Policy Decision**:
```
message_type: "tool_result"
policy: AUTO_PROMOTE
threshold: 10KB

if size > 10KB:
    semantics = REFERENCE
    → Store in SharedMemory
    → Return reference ID
else:
    semantics = VALUE
    → Pass inline in message
```


### Multi-Agent Workflow Flow

**Description**: Kubernetes-style YAML workflow execution with stage-level orchestration.

**Sequence Diagram**:
```
User         Server       YAML Loader  Validator   Orchestrator  Agent A   Agent B   Agent C
  │             │              │           │            │           │         │ 
  ├─ Run ───────▶│              │           │            │           │         │
  │  workflow   │              │           │            │           │         │ 
  │  pipeline.yaml             │           │            │           │         │ 
  │             │              │           │            │           │         │ 
  │             ├─ Load ───────▶│           │            │           │         │
  │             │  YAML        │           │            │           │         │ 
  │             │◀─ Config ────┤           │            │           │         │ 
  │             │  (parsed)    │           │            │           │         │ 
  │             │              │           │            │           │         │ 
  │             ├─ Validate ───┼──────────▶│            │           │         │ 
  │             │  Structure   │           │            │           │         │ 
  │             │  - apiVersion: loom/v1   │            │           │         │ 
  │             │  - kind: Workflow        │            │           │         │ 
  │             │  - spec.type: pipeline   │            │           │         │ 
  │             │◀─ Valid ─────┼───────────┤            │           │         │ 
  │             │              │           │            │           │         │ 
  │             ├─ Convert ────┼───────────┤            │           │         │ 
  │             │  to Proto    │           │            │           │         │ 
  │             │◀─ Pattern ───┼───────────┤            │           │         │ 
  │             │  WorkflowPattern          │            │           │         │
  │             │              │           │            │           │         │ 
  │             ├─ Execute ────┼───────────┼───────────▶│           │         │ 
  │             │  Pattern     │           │            │           │         │ 
  │             │              │           │            ├─ Validate ─┤         │
  │             │              │           │            │  Agents    │         │
  │             │              │           │            │  Exist     │         │
  │             │              │           │            │◀─ OK ──────┤         │
  │             │              │           │            │           │         │ 
  │             │              │           │            ├─ Stage 1 ──▶│         
  │             │              │           │            │  initial_prompt       
  │             │              │           │            │◀─ Output ───┤         
  │             │              │           │            │  "Schema: ..."        
  │             │              │           │            │           │         │ 
  │             │              │           │            ├─ Build ────┤         │
  │             │              │           │            │  Prompt    │         │
  │             │              │           │            │  {{.previous}}        
  │             │              │           │            │           │         │ 
  │             │              │           │            ├─ Stage 2 ──┼────────▶│
  │             │              │           │            │  "Optimize: {{.previou
  │             │              │           │            │◀─ Output ───┼─────────
  │             │              │           │            │  "Query: SELECT..."   
  │             │              │           │            │           │         │ 
  │             │              │           │            ├─ Stage 3 ──┼─────────┼
  │             │              │           │            │  "Execute: {{.previous
  │             │              │           │            │◀─ Output ───┼─────────
  │             │              │           │            │  "Results: 10K rows"  
  │             │              │           │            │           │         │ 
  │             │              │           │            ├─ Merge ────┤         │
  │             │              │           │            │  Results   │         │
  │             │              │           │            │  (final stage output) 
  │             │◀─ Result ────┼───────────┼────────────┤           │         │ 
  │             │  WorkflowResult           │            │           │         │
  │             │  - pattern_type: pipeline │            │           │         │
  │             │  - agent_results: [3]     │            │           │         │
  │             │  - merged_output          │            │           │         │
  │             │  - duration_ms: 5300      │            │           │         │
  │             │  - cost: $0.045           │            │           │         │
  │◀─ Response ─┤              │           │            │           │         │ 
  │  200 OK     │              │           │            │           │         │ 
  │  result     │              │           │            │           │         │ 
  │             │              │           │            │           │         │ 
```

**YAML Structure**:
```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: analytics-pipeline
  version: v1
spec:
  type: pipeline
  initial_prompt: "Analyze sales data"
  stages:
    - agent_id: schema-agent
      prompt_template: "Discover schema: {{.initial}}"
    - agent_id: optimizer-agent
      prompt_template: "Optimize query: {{.previous}}"
    - agent_id: executor-agent
      prompt_template: "Execute: {{.previous}}"
  pass_full_history: false
```

**Variable Substitution**:
- `{{.initial}}`: Initial prompt (first stage only)
- `{{.previous}}`: Previous stage output
- `{{.history}}`: All previous outputs (if pass_full_history: true)

**Performance Metrics**:
- **YAML load**: 15-35ms (parse + validate + convert)
- **Pipeline latency**: N × stage_latency (sequential)
- **Fork-join latency**: max(stage_latency) (parallel)
- **Cost attribution**: Per-agent, per-stage tracking


### Session Recovery Flow

**Description**: Crash recovery via SQLite session persistence and memory reconstruction.

**Sequence Diagram**:
```
Server Crash   Server Start  SessionStore  Memory       Agent        Client
  │               │              │           │            │            │        
  ├─ CRASH ───────┤              │           │            │            │        
  │  (power loss) │              │           │            │            │        
  │               │              │           │            │            │        
  │               ├─ Start ──────┤           │            │            │        
  │               │  Up          │           │            │            │        
  │               │              │           │            │            │        
  │               ├─ Load ───────▶│           │            │            │       
  │               │  Sessions    │           │            │            │        
  │               │◀─ List ──────┤           │            │            │        
  │               │  [sess-123, sess-456, ...]            │            │        
  │               │              │           │            │            │        
  │               │              │           │            │            ├─ POST ─
  │               │              │           │            │            │  /chat 
  │               │              │           │            │            │  sess-1
  │               │              │           │            │◀───────────┤        
  │               │              │           │            │            │        
  │               │              │           │            ├─ Get ──────▶│       
  │               │              │           │            │  Session   │        
  │               │              │           │            │  sess-123  │        
  │               │              │           │◀─ Load ────┤            │        
  │               │              │           │  SQLite    │            │        
  │               │              │◀─ Session ┤            │            │        
  │               │              │  {id, messages, metadata}           │        
  │               │              │           │            │            │        
  │               │              │           ├─ Reconstruct┤            │       
  │               │              │           │  Memory    │            │        
  │               │              │           │  L1: messages[-10:]     │        
  │               │              │           │  L2: summaries          │        
  │               │              │           │◀─ Memory ───┤            │       
  │               │              │           │  Restored  │            │        
  │               │              │           │            │            │        
  │               │              │           │            ├─ Continue ─┤        
  │               │              │           │            │  Conversation       
  │               │              │           │            │◀───────────┤        
  │               │              │           │            │  Response  │        
  │               │              │           │            ├───────────▶│        
  │               │              │           │            │            │        
```

**SQLite Schema**:
```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    messages TEXT NOT NULL,  -- JSON array
    l2_summaries TEXT,       -- JSON array
    metadata TEXT            -- JSON object
);

CREATE INDEX idx_sessions_updated ON sessions(updated_at);
```

**Recovery Algorithm**:
1. **Server start**: Load all session IDs from SQLite
2. **Client request**: Extract sessionID from request
3. **Session load**: Query SQLite by sessionID
4. **Memory reconstruction**:
   - Parse messages JSON → Message[] array
   - Last 10 messages → L1 cache
   - Parse l2_summaries JSON → L2 archive
   - Rebuild ROM (system prompt, tools, patterns)
5. **Resume conversation**: Agent continues from last message

**Performance Metrics**:
- **Load latency**: 5-15ms (SQLite query + JSON parse)
- **Memory reconstruction**: 10-30ms (array copy + token counting)
- **Total recovery**: 15-45ms per session
- **Storage overhead**: 50-200KB per session (10-50 messages)


### Pattern Hot-Reload Flow

**Description**: Zero-downtime pattern library updates via fsnotify and atomic swap.

**Sequence Diagram**:
```
User       Filesystem    fsnotify     Server      YAML Parser  Validator   Agent
  │             │            │           │             │           │          │ 
  ├─ Add ───────▶│            │           │             │           │          │
  │  Pattern    │            │           │             │           │          │ 
  │  YAML       │            │           │             │           │          │ 
  │             │            │           │             │           │          │ 
  │             ├─ Notify ───▶│           │             │           │          │
  │             │  CREATE     │           │             │           │          │
  │             │  teradata-analytics.yaml│             │           │          │
  │             │            │           │             │           │          │ 
  │             │            ├─ Debounce ─┤             │           │          │
  │             │            │  (100ms)   │             │           │          │
  │             │            │           │             │           │          │ 
  │             │            ├─ Parse ────┼────────────▶│           │          │
  │             │            │  YAML      │             │           │          │
  │             │            │◀─ Patterns ┼─────────────┤           │          │
  │             │            │  [{name, prompt, ...}]   │           │          │
  │             │            │           │             │           │          │ 
  │             │            ├─ Validate ─┼─────────────┼──────────▶│          │
  │             │            │  Schema    │             │           │          │
  │             │            │◀─ Valid ───┼─────────────┼───────────┤          │
  │             │            │           │             │           │          │ 
  │             │            ├─ Build ────┤             │           │          │
  │             │            │  TF-IDF    │             │           │          │
  │             │            │  Index     │             │           │          │
  │             │            │◀─ Index ───┤             │           │          │
  │             │            │  (89-143ms)│             │           │          │
  │             │            │           │             │           │          │ 
  │             │            ├─ Swap ─────┼─────────────┼───────────┼─────────▶│
  │             │            │  Atomic    │             │           │          │
  │             │            │  (CAS)     │             │           │          │
  │             │            │◀─ OK ──────┼─────────────┼───────────┼──────────┤
  │             │            │           │             │           │          │ 
  │             │            ├─ Broadcast ┤             │           │          │
  │             │            │  Event     │             │           │          │
  │             │            │  PatternUpdateEvent       │           │          
  │             │            │           │             │           │          │ 
  │◀─ Response ──┼────────────┤           │             │           │          │
  │  "Pattern library reloaded (59 patterns, 89ms)"     │           │          │
  │             │            │           │             │           │          │ 
```

**Atomic Swap**:
```go
// Old approach (not atomic)
library.patterns = newPatterns  // ❌ Race condition

// Atomic approach (CAS)
old := atomic.LoadPointer(&library.patterns)
new := &PatternLibrary{patterns: newPatterns, index: newIndex}
atomic.CompareAndSwapPointer(&library.patterns, old, unsafe.Pointer(new))
// ✅ Thread-safe, zero downtime
```

**Performance Metrics**:
- **File watch notification**: 10-15ms
- **YAML parse**: 45-60ms (59 patterns)
- **TF-IDF index rebuild**: 20-40ms
- **Atomic swap**: <1ms (pointer update)
- **Total reload**: 89-143ms (P50-P99)
- **Frequency**: <1 reload/minute typical


## Cross-Cutting Flows

### Observability Trace Flow

**Description**: End-to-end trace export from span creation to Hawk service.

**Sequence Diagram**:
```
Agent       Tracer      Buffer      Flusher     HTTP Client   Hawk
  │            │           │           │             │          │               
  ├─ Start ───▶│           │           │             │          │               
  │  Span      │           │           │             │          │               
  │  "agent.chat"          │           │             │          │               
  │◀─ (ctx, span)          │           │             │          │               
  │            │           │           │             │          │               
  ├─ Operation ┤           │           │             │          │               
  │  ...       │           │           │             │          │               
  │            │           │           │             │          │               
  ├─ End ──────▶│           │           │             │          │              
  │  Span      │           │           │             │          │               
  │            ├─ Redact ──┤           │             │          │               
  │            │  PII      │           │             │          │               
  │            ├─ Add ─────▶│           │             │          │              
  │            │  to Buffer│           │             │          │               
  │            │◀─ Count ───┤           │             │          │              
  │            │  (100/100)│           │             │          │               
  │            │           │           │             │          │               
  │            │           ├─ Trigger ─▶│             │          │              
  │            │           │  Flush    │             │          │               
  │            │           │           │             │          │               
  │            │           │           ├─ Drain ─────▶│          │              
  │            │           │           │  Buffer     │          │               
  │            │           │◀─ 100 spans┤             │          │              
  │            │           │           │             │          │               
  │            │           │           ├─ Marshal ───┤          │               
  │            │           │           │  JSON       │          │               
  │            │           │           │             │          │               
  │            │           │           ├─ POST ──────┼─────────▶│               
  │            │           │           │  /v1/traces │          │               
  │            │           │           │  (batch)    │          │               
  │            │           │           │◀─ 200 OK ───┼──────────┤               
  │            │           │           │             │          │               
  │            │           │           ├─ Record ────┤          │               
  │            │           │           │  exported   │          │               
  │            │           │           │             │          │               
  │            │           │           │  (10s later)│          │               
  │            │           │           ├─ Ticker ────┤          │               
  │            │           │           │  Flush      │          │               
  │            │           │◀─ Partial ─┤             │          │              
  │            │           │  (20 spans)│             │          │              
  │            │           │           ├─ POST ──────┼─────────▶│               
  │            │           │           │             │          │               
  │            │           │           │◀─ 503 ──────┼──────────┤               
  │            │           │           │  Unavailable│          │               
  │            │           │           │             │          │               
  │            │           │           ├─ Wait 1s ───┤          │               
  │            │           │           │  (retry 1)  │          │               
  │            │           │           │             │          │               
  │            │           │           ├─ POST ──────┼─────────▶│               
  │            │           │           │◀─ 200 OK ───┼──────────┤               
  │            │           │           │             │          │               
```

**Batch Export Logic**:
```
Trigger Conditions:
1. Buffer full (len >= 100)
2. Timer tick (every 10s)
3. Explicit Flush()
4. Server shutdown

Export Flow:
1. Drain buffer (atomic swap)
2. Redact PII (email, phone, SSN, card)
3. Remove credentials (api_key, password, token)
4. Marshal to JSON
5. HTTP POST to Hawk
6. Retry on failure (3 attempts, exponential backoff)
```

**Performance Metrics**:
- **Span creation**: <1µs (UUID + context)
- **EndSpan**: 10µs (PII redaction) or <1µs (no redaction)
- **Buffer add**: <1µs (mutex + append)
- **Flush latency**: 15-50ms (100 spans → JSON → HTTP)
- **Retry schedule**: 0s, 1s, 2s, 4s (exponential)


### Cost Attribution Flow

**Description**: Token counting and cost calculation throughout conversation lifecycle.

**Sequence Diagram**:
```
LLM Provider  TokenCounter  CostCalc   Agent      Span        Hawk
  │              │             │          │          │          │               
  ├─ Response ──▶│             │          │          │          │               
  │  usage:      │             │          │          │          │               
  │  input_tokens: 1200        │          │          │          │               
  │  output_tokens: 350        │          │          │          │               
  │              │             │          │          │          │               
  │              ├─ Count ─────┤          │          │          │               
  │              │  Tokens     │          │          │          │               
  │              │◀─ Verified ─┤          │          │          │               
  │              │  1200 + 350 = 1550     │          │          │               
  │              │             │          │          │          │               
  │              ├─ Calculate ─┼─────────▶│          │          │               
  │              │  Cost       │          │          │          │               
  │              │  Model: claude-sonnet-4.5        │          │                
  │              │  Input: 1200 × $3/1M = $0.0036   │          │                
  │              │  Output: 350 × $15/1M = $0.0053  │          │                
  │              │◀─ Cost ─────┼──────────┤          │          │               
  │              │  $0.0089    │          │          │          │               
  │              │             │          │          │          │               
  │              ├─ Accumulate ┼──────────┼─────────▶│          │               
  │              │  Turn       │          │          │          │               
  │              │  Total      │          │          │          │               
  │              │◀─ OK ───────┼──────────┼──────────┤          │               
  │              │             │          │          │          │               
  │              │  (end of conversation)│          │          │                
  │              │             │          │          │          │               
  │              ├─ Export ────┼──────────┼──────────┼─────────▶│               
  │              │  Trace      │          │          │          │               
  │              │  Attributes:│          │          │          │               
  │              │  - conversation.turns: 3          │          │               
  │              │  - conversation.tokens.total: 4650│          │               
  │              │  - conversation.cost.usd: 0.0267  │          │               
  │              │  - llm.model: claude-sonnet-4.5   │          │               
  │              │             │          │          │          │               
```

**Pricing Table** (as of v1.0.0-beta.1):
```
Model                     Input $/1M    Output $/1M
───────────────────────────────────────────────────                             
claude-sonnet-4.5         $3.00         $15.00
claude-opus-4.5           $15.00        $75.00
claude-haiku-4.0          $0.25         $1.25
bedrock-sonnet-4.5        $3.00         $15.00
ollama-llama3             $0.00         $0.00 (local)
```

**Cost Calculation**:
```go
func CalculateCost(inputTokens, outputTokens int, model string) float64 {
    pricing := GetPricing(model)
    inputCost := float64(inputTokens) * pricing.InputPer1M / 1_000_000
    outputCost := float64(outputTokens) * pricing.OutputPer1M / 1_000_000
    return inputCost + outputCost
}

// Example: Claude Sonnet 4.5
inputTokens = 1200
outputTokens = 350
inputCost = 1200 * 3.00 / 1_000_000 = $0.0036
outputCost = 350 * 15.00 / 1_000_000 = $0.0053
totalCost = $0.0089
```

**Attribution Levels**:
1. **Turn-level**: Cost per user message → response cycle
2. **Session-level**: Cumulative cost across all turns
3. **Agent-level**: Cost per agent (multi-agent workflows)
4. **Workflow-level**: Cost per workflow execution
5. **Organization-level**: Aggregate across all sessions (via Hawk)


## Data Structures

### Flow Message

**Definition**:
```protobuf
message FlowMessage {
  string id = 1;                    // Unique message ID
  string session_id = 2;            // Session context
  string role = 3;                  // "user" | "assistant" | "system"
  string content = 4;               // Message text
  repeated ToolCall tool_calls = 5; // LLM-requested tool invocations
  repeated ToolResult tool_results = 6; // Tool execution results
  TokenUsage usage = 7;             // Token counts and cost
  int64 timestamp = 8;              // Unix timestamp
}
```

### ToolResult

**Definition**:
```protobuf
message ToolResult {
  string tool_id = 1;               // Tool call ID
  string tool_name = 2;             // Tool identifier

  oneof result {
    string content = 3;             // Inline result (value semantics)
    Reference reference = 4;         // Stored result (reference semantics)
    ToolError error = 5;            // Execution error
  }

  int64 duration_ms = 6;            // Execution time
  map<string, string> metadata = 7;
}
```

### SessionState

**Definition**:
```protobuf
message SessionState {
  string id = 1;
  repeated Message messages = 2;   // Full message history
  Memory memory = 3;                // Segmented memory state
  map<string, string> metadata = 4;
  int64 created_at = 5;
  int64 updated_at = 6;
  TokenUsage cumulative_usage = 7; // Session-wide token/cost tracking
}
```

### TraceSpan

**Definition**:
```protobuf
message TraceSpan {
  string trace_id = 1;              // Shared across trace tree
  string span_id = 2;               // Unique per span
  string parent_id = 3;             // Links to parent
  string name = 4;                  // Operation name
  int64 start_time = 5;
  int64 end_time = 6;
  int64 duration_ms = 7;
  map<string, interface{}> attributes = 8; // Key-value metadata
  repeated Event events = 9;        // Timestamped log entries
  Status status = 10;               // OK | Error | Unset
}
```


## Flow Properties

### Throughput

| Flow | Throughput | Bottleneck |
|------|-----------|------------|
| Agent conversation | 1 conversation/1.2s | LLM API latency |
| Tool execution | 100+ tools/s | Goroutine spawning |
| Pattern matching | 1000+ queries/s | TF-IDF index lookup |
| Memory persistence | 200 writes/s | SQLite transaction |
| Trace export | 100 spans/request | HTTP batch size |
| Pattern hot-reload | <1 reload/min | Filesystem watch |

### Latency (End-to-End)

| Flow | P50 | P99 | Critical Path |
|------|-----|-----|---------------|
| Agent conversation | 1.2s | 3.5s | LLM API call (800ms) + tool execution (400ms) |
| Tool execution | 500ms | 1.6s | Backend query |
| Memory management | 15ms | 45ms | SQLite write |
| Pattern matching | 5ms | 15ms | TF-IDF search |
| Reference storage | 10ms | 35ms | Compression (zstd) |
| Workflow execution | 5.3s | 12s | N × stage_latency (sequential) |
| Session recovery | 15ms | 45ms | SQLite load + JSON parse |
| Pattern hot-reload | 89ms | 143ms | TF-IDF index rebuild |
| Trace export | 15ms | 50ms | HTTP POST |

### Data Volume

| Flow | Typical Volume | Max Volume | Storage |
|------|---------------|------------|---------|
| Agent conversation | 20KB/turn | 100KB/turn | SQLite |
| Tool result | 10KB | 10MB | SharedMemory |
| Session state | 50KB | 500KB | SQLite |
| Pattern library | 80KB | 5MB | Filesystem |
| Trace span | 2KB | 10KB | Hawk |
| L2 summary | 2KB | 10KB | SQLite |


## Performance Characteristics

### Memory Usage

| Component | Typical | Max | Notes |
|-----------|---------|-----|-------|
| Agent runtime | 5MB | 20MB | Includes L1 + tools + patterns |
| Session state (RAM) | 50KB | 500KB | 10-50 messages |
| SharedMemory (memory tier) | 100MB | 1GB | LRU cache |
| SharedMemory (disk tier) | 1GB | 10GB | Filesystem |
| Pattern library (RAM) | 2MB | 10MB | TF-IDF index + patterns |
| Trace buffer | 200KB | 1MB | 100 spans |

### CPU Usage

| Operation | CPU | Parallelism |
|-----------|-----|-------------|
| LLM invocation | 1% | Network-bound (waiting) |
| Tool execution | 5-20% | Goroutine-based (parallel) |
| Pattern matching | 2-5% | TF-IDF vectorization |
| Memory compression | 10-30% | LLM API call |
| Trace export | 1-3% | JSON serialization |
| Pattern hot-reload | 15-40% | TF-IDF index rebuild |


## Related Work

### Flow-Based Systems

1. **Apache Kafka Streams**: Stream processing with stateful flows
   - **Similar**: Data flow through stages with state management
   - **Loom differs**: LLM-centric, pattern-guided, observability-first

2. **Apache Airflow**: Workflow orchestration with DAGs
   - **Similar**: Stage-based execution, dependency management
   - **Loom differs**: Real-time agent conversations, not batch ETL

3. **Temporal**: Durable workflow execution
   - **Similar**: Crash recovery, workflow state persistence
   - **Loom differs**: Ephemeral workflows, LLM agent focus

### Observability Systems

1. **OpenTelemetry**: Distributed tracing standard
   - **Similar**: Span model, context propagation, trace export
   - **Loom differs**: Privacy-aware PII redaction, cost attribution

2. **Jaeger**: Distributed tracing backend
   - **Similar**: Trace visualization, span relationships
   - **Loom differs**: Hawk integration, LLM-specific attributes


## References

1. OpenTelemetry Specification. Tracing API. https://opentelemetry.io/docs/specs/otel/trace/api/

2. Martin Fowler. (2004). *Patterns of Enterprise Application Architecture*. Addison-Wesley. (Data flow patterns)

3. Gregor Hohpe & Bobby Woolf. (2003). *Enterprise Integration Patterns*. Addison-Wesley. (Message routing)


## Further Reading

### Architecture Deep Dives

- [Agent System Architecture](agent-system-design.md) - Agent conversation loop
- [Memory Systems Architecture](memory-systems.md) - Segmented memory design
- [Multi-Agent Orchestration](multi-agent.md) - Workflow patterns
- [Communication System](communication-system-design.md) - Tri-modal messaging
- [Observability Architecture](observability.md) - Hawk integration
- [Pattern System](pattern-system.md) - TF-IDF matching

### Reference Documentation

- [Agent API Reference](/docs/reference/agent-api.md) - Agent RPC definitions
- [Workflow API Reference](/docs/reference/workflow-api.md) - Orchestration RPCs
- [CLI Reference](/docs/reference/cli.md) - Command-line tools

### Guides

- [Getting Started](/docs/guides/quickstart.md) - Quick start guide
- [Building Workflows](/docs/guides/multi-agent-workflows.md) - Workflow creation
