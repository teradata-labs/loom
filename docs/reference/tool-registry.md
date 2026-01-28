
# Tool Registry Reference

Complete specification for Loom's tool indexing and search system. The tool registry maintains an FTS5 index of all available tools (builtin, MCP, custom) and supports LLM-assisted search for high-accuracy tool discovery.

**Version**: v1.0.0-beta.2
**Package**: `pkg/tools/registry`
**Status**: Implemented with FTS5 indexing, LLM-assisted search


## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Registry API](#registry-api)
  - [New](#new)
  - [IndexAll](#indexall)
  - [Search](#search)
  - [GetTool](#gettool)
  - [GetToolsByCapability](#gettoolsbycapability)
  - [ListSources](#listsources)
- [Indexers](#indexers)
  - [BuiltinIndexer](#builtinindexer)
  - [MCPIndexer](#mcpindexer)
  - [CustomIndexer](#customindexer)
- [Search Modes](#search-modes)
- [SearchTool (Builtin)](#searchtool-builtin)
- [Data Structures](#data-structures)
- [Configuration](#configuration)
- [Performance](#performance)
- [Examples](#examples)


## Overview

The Tool Registry provides:

1. **FTS5 Full-Text Search**: SQLite FTS5 virtual table with BM25 ranking and Porter stemmer
2. **LLM-Assisted Search**: Query expansion and result re-ranking using LLM
3. **Multi-Source Indexing**: Builtin tools, MCP servers, and custom YAML definitions
4. **Capability Tagging**: Automatic capability extraction for filtering
5. **Agent Integration**: `tool_search` builtin tool for agent use

**Key Features**:
- BM25 ranking with weighted columns (name: 10x, description: 5x, capabilities: 3x, keywords: 2x)
- Three search modes: FAST, BALANCED, ACCURATE
- Automatic capability detection from tool names and descriptions
- Relevance signals with confidence scores and match reasons


## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Tool Registry                                    │
│                                                                         │
│  ┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐  │
│  │ BuiltinIndexer   │    │   MCPIndexer     │    │  CustomIndexer   │  │
│  │ (shuttle.Tool)   │    │ (MCP Manager)    │    │ (YAML configs)   │  │
│  └────────┬─────────┘    └────────┬─────────┘    └────────┬─────────┘  │
│           │                       │                       │            │
│           └───────────────────────┼───────────────────────┘            │
│                                   ▼                                     │
│                    ┌──────────────────────────────┐                    │
│                    │        SQLite FTS5           │                    │
│                    │  ┌──────────┐  ┌──────────┐  │                    │
│                    │  │  tools   │  │ tools_fts│  │                    │
│                    │  │ (main)   │──│ (FTS5)   │  │                    │
│                    │  └──────────┘  └──────────┘  │                    │
│                    └──────────────────────────────┘                    │
│                                   │                                     │
│           ┌───────────────────────┼───────────────────────┐            │
│           ▼                       ▼                       ▼            │
│  ┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐  │
│  │   FAST Search    │    │ BALANCED Search  │    │ ACCURATE Search  │  │
│  │ (FTS5 only)      │    │ (FTS5 + Rerank)  │    │ (Expand + Rerank)│  │
│  └──────────────────┘    └──────────────────┘    └──────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```


## Registry API

### New

**Function**: `New(cfg Config) (*Registry, error)`

Creates a new tool registry with FTS5-enabled SQLite database.

**Config Parameters**:
```go
type Config struct {
    DBPath   string            // Path to SQLite database file
    LLM      types.LLMProvider // LLM provider for search assistance
    Tracer   observability.Tracer
    Indexers []Indexer         // Tool source indexers
}
```

**Example**:
```go
registry, err := registry.New(registry.Config{
    DBPath: "$LOOM_DATA_DIR/tools.db",
    LLM:    llmProvider,
    Tracer: tracer,
    Indexers: []registry.Indexer{
        registry.NewBuiltinIndexer(tracer),
        registry.NewMCPIndexer(mcpManager, tracer),
    },
})
```

**Schema Created**:
- `tools` - Main tools table with all metadata
- `tools_fts` - FTS5 virtual table for full-text search
- `tool_sources` - Tracking table for indexer status


### IndexAll

**Function**: `IndexAll(ctx context.Context) (*loomv1.IndexToolsResponse, error)`

Indexes tools from all registered indexers.

**Response**:
```go
type IndexToolsResponse struct {
    TotalCount   int32          // Total tools indexed
    BuiltinCount int32          // Builtin tools count
    McpCount     int32          // MCP tools count
    CustomCount  int32          // Custom tools count
    DurationMs   int64          // Indexing duration
    Errors       []*IndexError  // Any indexing errors
}
```

**Example**:
```go
resp, err := registry.IndexAll(ctx)
// resp.TotalCount = 45
// resp.BuiltinCount = 10
// resp.McpCount = 35
// resp.DurationMs = 234
```


### Search

**Function**: `Search(ctx context.Context, req *loomv1.SearchToolsRequest) (*loomv1.SearchToolsResponse, error)`

Performs LLM-assisted tool search.

**Request Parameters**:
```go
type SearchToolsRequest struct {
    Query             string           // Natural language query
    Mode              SearchMode       // FAST, BALANCED, ACCURATE
    CapabilityFilters []string         // Filter by capabilities
    SourceFilters     []ToolSource     // Filter by source type
    MaxResults        int32            // Max results (default: 5)
    IncludeSchema     bool             // Include input/output schemas
    TaskContext       string           // Optional task context for ranking
}
```

**Response**:
```go
type SearchToolsResponse struct {
    Results  []*ToolSearchResult
    Metadata *SearchMetadata
}

type ToolSearchResult struct {
    Tool        *IndexedTool
    Confidence  float64          // 0.0-1.0 relevance score
    MatchReason string           // Why this matched
    Signals     []*RelevanceSignal
}

type SearchMetadata struct {
    ModeUsed              SearchMode
    TotalIndexed          int32
    CandidatesRetrieved   int32
    QueryUnderstandingMs  int64   // Time for query expansion
    FtsRetrievalMs        int64   // Time for FTS search
    LlmRerankingMs        int64   // Time for LLM re-ranking
    TotalMs               int64   // Total search time
    ExpandedTerms         []string // Terms added by query expansion
}
```

**Example**:
```go
resp, err := registry.Search(ctx, &loomv1.SearchToolsRequest{
    Query:      "send notification to Slack",
    Mode:       loomv1.SearchMode_SEARCH_MODE_BALANCED,
    MaxResults: 5,
})
// resp.Results[0].Tool.Name = "slack_send"
// resp.Results[0].Confidence = 0.95
// resp.Results[0].MatchReason = "Exact match for Slack notification"
```


### GetTool

**Function**: `GetTool(ctx context.Context, toolID string) (*loomv1.IndexedTool, error)`

Retrieves a specific tool by ID.

**Tool ID Format**:
- Builtin: `builtin:tool_name`
- MCP: `mcp:server_name:tool_name`
- Custom: `custom:tool_name`

**Example**:
```go
tool, err := registry.GetTool(ctx, "builtin:http_request")
// tool.Name = "http_request"
// tool.Description = "Make HTTP requests to APIs"
// tool.Capabilities = ["http", "search"]
```


### GetToolsByCapability

**Function**: `GetToolsByCapability(ctx context.Context, capability string, sourceFilters []loomv1.ToolSource, maxResults int) ([]*loomv1.IndexedTool, error)`

Returns tools with a specific capability tag.

**Available Capabilities**:
- `file_io` - File read/write operations
- `http` - HTTP/API requests
- `database` - Database queries
- `notification` - Notifications/alerts
- `shell` - Shell command execution
- `search` - Search/lookup operations
- `transform` - Data transformation
- `validate` - Validation/linting
- `generate` - Content generation
- `analyze` - Analysis/review
- `web_search` - Web search
- `code` - Code operations
- `git` - Git operations
- `kubernetes` - Kubernetes operations
- `aws` - AWS services
- `visualization` - Charts/graphs

**Example**:
```go
tools, err := registry.GetToolsByCapability(ctx, "notification", nil, 10)
// Returns all notification-capable tools
```


### ListSources

**Function**: `ListSources(ctx context.Context) ([]*loomv1.ToolSourceInfo, error)`

Returns all registered tool sources with status.

**Response**:
```go
type ToolSourceInfo struct {
    Name          string
    Type          ToolSource   // BUILTIN, MCP, CUSTOM
    Description   string
    ToolCount     int32
    LastIndexed   string       // RFC3339 timestamp
    Available     bool
    StatusMessage string
}
```


## Indexers

### BuiltinIndexer

**Purpose**: Indexes builtin tools from `pkg/shuttle/builtin`.

**Implementation** (`pkg/tools/registry/indexers.go`):
```go
type BuiltinIndexer struct {
    tools  []shuttle.Tool
    tracer observability.Tracer
}

func NewBuiltinIndexer(tracer observability.Tracer, tools ...shuttle.Tool) *BuiltinIndexer
```

**Indexed Fields**:
- `id`: `builtin:{tool_name}`
- `name`: Tool name
- `description`: Tool description
- `input_schema`: JSON schema from `InputSchema()`
- `capabilities`: Auto-extracted from name/description
- `keywords`: Auto-extracted for FTS
- `requires_approval`: True for bash, exec, write tools

**Example**:
```go
indexer := registry.NewBuiltinIndexer(tracer)
tools, err := indexer.Index(ctx)
// tools[0].Id = "builtin:http_request"
// tools[0].Capabilities = ["http"]
```


### MCPIndexer

**Purpose**: Indexes tools from connected MCP servers.

**Implementation** (`pkg/tools/registry/indexers.go`):
```go
type MCPIndexer struct {
    manager *manager.Manager
    tracer  observability.Tracer
}

func NewMCPIndexer(mgr *manager.Manager, tracer observability.Tracer) *MCPIndexer
```

**Indexed Fields**:
- `id`: `mcp:{server_name}:{tool_name}`
- `name`: MCP tool name
- `description`: MCP tool description
- `mcp_server`: Server name
- `input_schema`: MCP tool input schema
- `capabilities`: Auto-extracted
- `keywords`: Auto-extracted

**Example**:
```go
indexer := registry.NewMCPIndexer(mcpManager, tracer)
tools, err := indexer.Index(ctx)
// tools[0].Id = "mcp:filesystem:read_file"
// tools[0].McpServer = "filesystem"
```


### CustomIndexer

**Purpose**: Indexes custom tool definitions from YAML files.

**Status**: Scaffolded, implementation pending.

**Planned Features**:
- Load tool definitions from `$LOOM_DATA_DIR/tools/*.yaml`
- Support custom capability tagging
- Support rate limiting configuration


## Search Modes

### FAST Mode

**Pipeline**: FTS5 only (no LLM)

**Characteristics**:
- Latency: 5-20ms
- Accuracy: Good for exact keyword matches
- Cost: Zero LLM tokens

**Use When**: Known tool names, simple queries, latency-sensitive


### BALANCED Mode (Default)

**Pipeline**: FTS5 retrieval + LLM re-ranking

**Characteristics**:
- Latency: 200-500ms
- Accuracy: High (LLM re-ranks FTS candidates)
- Cost: ~100-200 tokens per search

**Use When**: Most searches, balance of speed and accuracy


### ACCURATE Mode

**Pipeline**: LLM query expansion + FTS5 + LLM re-ranking

**Characteristics**:
- Latency: 500-1000ms
- Accuracy: Highest (query expansion finds synonyms)
- Cost: ~300-500 tokens per search

**Use When**: Complex natural language queries, unfamiliar tool names


## SearchTool (Builtin)

The `tool_search` builtin tool allows agents to search for tools.

**Tool Specification** (`pkg/tools/registry/search_tool.go`):
```go
type SearchTool struct {
    registry *Registry
}

func (t *SearchTool) Name() string { return "tool_search" }
```

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural language description of what you want to do"
    },
    "mode": {
      "type": "string",
      "enum": ["fast", "balanced", "accurate"],
      "description": "Search accuracy mode"
    },
    "capabilities": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Capability filters (e.g., ['notification', 'database'])"
    },
    "max_results": {
      "type": "integer",
      "description": "Maximum results (default: 5, max: 10)"
    },
    "task_context": {
      "type": "string",
      "description": "Optional context for ranking"
    }
  },
  "required": ["query"]
}
```

**Output Format**:
```json
{
  "query": "send notification to Slack",
  "results_count": 3,
  "results": [
    {
      "name": "slack_send",
      "description": "Send messages to Slack channels",
      "confidence": "95%",
      "source": "MCP",
      "mcp_server": "slack",
      "match_reason": "Exact match for Slack notification",
      "capabilities": ["notification"],
      "parameters": {
        "channel": "Slack channel ID (string)",
        "message": "Message content (string)"
      }
    }
  ],
  "search_mode": "BALANCED",
  "search_time_ms": 234,
  "total_tools_indexed": 45
}
```


## Data Structures

### IndexedTool

```go
type IndexedTool struct {
    Id               string          // Unique tool ID
    Name             string          // Tool name
    Description      string          // Tool description
    Source           ToolSource      // BUILTIN, MCP, CUSTOM
    McpServer        string          // MCP server name (if MCP)
    InputSchema      string          // JSON schema
    OutputSchema     string          // JSON schema
    Capabilities     []string        // Capability tags
    Keywords         []string        // Search keywords
    Examples         []*ToolExample  // Usage examples
    IndexedAt        string          // RFC3339 timestamp
    Version          string          // Tool version
    RequiresApproval bool            // Needs human approval
    RateLimit        *RateLimitInfo  // Rate limiting config
}
```

### RelevanceSignal

```go
type RelevanceSignal struct {
    SignalType  string  // "bm25_score", "llm_rerank"
    Description string  // Human-readable explanation
    Weight      float64 // Signal weight/score
}
```


## Configuration

### Database Schema

**Main Table (`tools`)**:
```sql
CREATE TABLE tools (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    source INTEGER NOT NULL,
    mcp_server TEXT,
    input_schema TEXT,
    output_schema TEXT,
    capabilities TEXT,   -- JSON array
    keywords TEXT,       -- JSON array
    examples TEXT,       -- JSON array
    indexed_at TEXT NOT NULL,
    version TEXT,
    requires_approval INTEGER DEFAULT 0,
    rate_limit TEXT      -- JSON object
);
```

**FTS5 Table (`tools_fts`)**:
```sql
CREATE VIRTUAL TABLE tools_fts USING fts5(
    name,
    description,
    capabilities,
    keywords,
    content='tools',
    content_rowid='rowid',
    tokenize='porter unicode61'
);
```

**BM25 Weights**:
- `name`: 10.0 (highest)
- `description`: 5.0
- `capabilities`: 3.0
- `keywords`: 2.0


## Performance

### Latency Benchmarks

| Mode | P50 | P99 | Notes |
|------|-----|-----|-------|
| FAST | 8ms | 25ms | FTS5 only, no LLM |
| BALANCED | 250ms | 500ms | FTS5 + LLM re-ranking |
| ACCURATE | 600ms | 1200ms | Query expansion + FTS5 + re-ranking |

### Throughput

- Indexing: ~500 tools/second
- Search (FAST): ~100 searches/second
- Search (BALANCED): ~4 searches/second (LLM-bound)

### Resource Usage

- Database size: ~50KB per 100 tools
- Memory: ~10MB for registry instance
- FTS5 index: ~30% of data size


## Examples

### Example 1: Basic Search

```go
registry, _ := registry.New(registry.Config{
    DBPath:   "/tmp/tools.db",
    Indexers: []registry.Indexer{registry.NewBuiltinIndexer(nil)},
})
defer registry.Close()

// Index all tools
registry.IndexAll(ctx)

// Search for tools
resp, _ := registry.Search(ctx, &loomv1.SearchToolsRequest{
    Query:      "make HTTP request",
    Mode:       loomv1.SearchMode_SEARCH_MODE_FAST,
    MaxResults: 3,
})

for _, r := range resp.Results {
    fmt.Printf("%s (%.0f%%): %s\n", r.Tool.Name, r.Confidence*100, r.Tool.Description)
}
// Output:
// http_request (95%): Make HTTP requests to REST APIs
// web_search (72%): Search the web using Tavily
```

### Example 2: Agent Using SearchTool

```yaml
# Agent configuration
agent:
  name: research-assistant
  tools:
    builtin:
      - tool_search    # Enable tool discovery
      - http_request   # Use discovered tools
      - file_write
```

Agent conversation:
```
User: I need to send a notification to Slack about our deployment.

Agent: Let me search for available notification tools.
[Calls tool_search with query="send notification to Slack"]

Search results:
1. slack_send (95%): Send messages to Slack channels
2. webhook_post (78%): POST to webhook endpoints

I found a Slack-specific tool. Let me use slack_send...
[Calls slack_send with channel="#deployments", message="Deployment complete!"]
```

### Example 3: Capability Filtering

```go
// Find all database-capable tools
tools, _ := registry.GetToolsByCapability(ctx, "database", nil, 10)

// Find only MCP database tools
tools, _ := registry.GetToolsByCapability(ctx, "database",
    []loomv1.ToolSource{loomv1.ToolSource_TOOL_SOURCE_MCP}, 10)
```


## See Also

- [Shuttle (Tool System) Reference](./shuttle.md) - Tool execution system
- [MCP Integration Guide](../guides/integration/mcp.md) - MCP server setup
- [Agent Configuration Reference](./agent-configuration.md) - Tool configuration in agents
