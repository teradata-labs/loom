# CLI Sessions and MCP Commands Implementation Plan

**Created**: 2026-01-16
**Status**: Planning Phase
**Branch**: `feature/cli-sessions-mcp-commands`

## Executive Summary

The proto definitions and server-side implementations for session and MCP management are **already complete**. We only need to create CLI commands that call the existing client methods.

## Current State Analysis

### Already Implemented ✅

**Proto Definitions** (`proto/loom/v1/loom.proto`):
- `rpc CreateSession(CreateSessionRequest) returns (Session)`
- `rpc GetSession(GetSessionRequest) returns (Session)`
- `rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse)`
- `rpc DeleteSession(DeleteSessionRequest) returns (DeleteSessionResponse)`
- `rpc GetConversationHistory(GetConversationHistoryRequest) returns (ConversationHistory)`
- `rpc ListMCPServers(ListMCPServersRequest) returns (ListMCPServersResponse)`
- `rpc GetMCPServer(GetMCPServerRequest) returns (MCPServerInfo)`
- `rpc TestMCPServerConnection(TestMCPServerConnectionRequest) returns (TestMCPServerConnectionResponse)`
- `rpc ListMCPServerTools(ListMCPServerToolsRequest) returns (ListMCPServerToolsResponse)`
- `rpc AddMCPServer(AddMCPServerRequest) returns (AddMCPServerResponse)`
- `rpc UpdateMCPServer(UpdateMCPServerRequest) returns (MCPServerInfo)`
- `rpc DeleteMCPServer(DeleteMCPServerRequest) returns (DeleteMCPServerResponse)`
- `rpc RestartMCPServer(RestartMCPServerRequest) returns (MCPServerInfo)`
- `rpc HealthCheckMCPServers(HealthCheckMCPServersRequest) returns (HealthCheckMCPServersResponse)`

**Server Implementation** (`pkg/server/`):
- `pkg/server/server.go` - Session RPCs implemented
- `pkg/server/multi_agent.go` - Session RPCs for multi-agent server
- `pkg/server/mcp_management.go` - All MCP RPCs implemented

**Client Library** (`pkg/tui/client/client.go`):
- `GetSession(ctx, sessionID) (*Session, error)`
- `ListSessions(ctx, limit, offset) ([]*Session, error)`
- `DeleteSession(ctx, sessionID) error`
- `ListMCPServers(ctx, req) (*ListMCPServersResponse, error)`
- `TestMCPServerConnection(ctx, req) (*TestMCPServerConnectionResponse, error)`
- `ListMCPServerTools(ctx, serverName) ([]*ToolDefinition, error)`

### What's Missing ❌

**CLI Commands** (`cmd/loom/`):
- No `sessions.go` file
- No `mcp.go` file

---

## Phase 1: Essential User Commands (Week 1)

### Priority: CRITICAL
**Goal**: Solve immediate pain points for users.

### Commands to Implement

#### 1.1 `loom sessions list`

**Usage:**
```bash
loom sessions list [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--thread`, `-t` | string | `""` | Filter by thread ID |
| `--limit` | int | `20` | Max results to return |
| `--offset` | int | `0` | Pagination offset |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client method: `client.ListSessions(ctx, limit, offset)`
- Output format: Table with columns: SESSION_ID, THREAD, MESSAGES, CREATED, LAST_UPDATED

**Example Output:**
```
Available sessions (showing 20 of 45):

SESSION_ID           THREAD              MESSAGES  CREATED           LAST_UPDATED
sess_abc123def456    sql-expert          15        2 hours ago       1 hour ago
sess_xyz789ghi012    weaver              8         1 day ago         1 day ago
sess_jkl345mno678    teradata-explorer   23        3 days ago        2 days ago

To resume a session:
  loom --thread <thread-id> --session <session-id>
  loom chat --thread <thread-id> --session <session-id> "message"
```

**Exit Codes:**
- 0: Success
- 1: Invalid arguments
- 4: Cannot connect to server

---

#### 1.2 `loom sessions show <session-id>`

**Usage:**
```bash
loom sessions show <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--with-history` | bool | `false` | Include full conversation history |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client methods:
  - `client.GetSession(ctx, sessionID)`
  - `client.GetConversationHistory(ctx, sessionID)` (if `--with-history`)
- Output format: Session metadata + optional conversation history

**Example Output:**
```
Session: sess_abc123def456

Details:
  Thread:       sql-expert
  Created:      2026-01-16 09:15:23
  Last Updated: 2026-01-16 10:42:15
  Messages:     15
  Tokens:       12,345 (input: 2,345 | output: 10,000)
  Cost:         $0.123456

To resume:
  loom --thread sql-expert --session sess_abc123def456
```

With `--with-history`:
```
Session: sess_abc123def456
...

Conversation History:

[2026-01-16 09:15:23] User:
show me all tables

[2026-01-16 09:15:25] Agent:
I'll query the database to list all tables...
[Tool: get_schema]
Found 42 tables in the database...

[2026-01-16 09:16:10] User:
show sales table schema
...
```

**Exit Codes:**
- 0: Success
- 1: Session ID required
- 4: Cannot connect to server
- 7: Session not found

---

#### 1.3 `loom sessions delete <session-id>`

**Usage:**
```bash
loom sessions delete <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--force`, `-f` | bool | `false` | Skip confirmation prompt |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client method: `client.DeleteSession(ctx, sessionID)`
- Behavior: Prompt for confirmation unless `--force`

**Example Output:**
```
⚠️  This will permanently delete session sess_abc123def456 and all conversation history.

Continue? (y/N): y

✅ Session sess_abc123def456 deleted successfully.
```

With `--force`:
```bash
loom sessions delete sess_abc123def456 --force
# Output: ✅ Session sess_abc123def456 deleted successfully.
```

**Exit Codes:**
- 0: Success
- 1: Session ID required or user cancelled
- 4: Cannot connect to server
- 7: Session not found

---

#### 1.4 `loom mcp list`

**Usage:**
```bash
loom mcp list [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client method: `client.ListMCPServers(ctx, req)`
- Output format: Table with server status

**Example Output:**
```
Configured MCP servers (3):

NAME              STATUS   TOOLS  TRANSPORT  UPTIME
vantage           running  45     stdio      2h 15m
github            running  12     stdio      2h 15m
filesystem        stopped  0      stdio      -

To test a server:
  loom mcp test <server-name>

To list tools:
  loom mcp tools <server-name>
```

**Exit Codes:**
- 0: Success
- 4: Cannot connect to server

---

#### 1.5 `loom mcp test <server-name>`

**Usage:**
```bash
loom mcp test <server-name> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--timeout` | duration | `30s` | Connection timeout |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client method: `client.TestMCPServerConnection(ctx, req)`
- Output format: Connection test results

**Example Output:**

Success:
```
Testing MCP server: vantage

Connection Test:
  ✅ Server process started
  ✅ Handshake completed (234ms)
  ✅ Protocol version: 1.0.0

Tool Discovery:
  ✅ 45 tools discovered

Sample Tools:
  - execute_query: Execute Teradata SQL query
  - get_schema: Get table schema information
  - list_tables: List available tables
  - list_databases: List accessible databases
  ... (41 more tools)

Status: healthy
Response Time: 234ms

To list all tools:
  loom mcp tools vantage
```

Failure:
```
Testing MCP server: broken-server

Connection Test:
  ❌ Failed to start server process
  Error: command not found: /usr/bin/nonexistent-mcp

Troubleshooting:
  1. Verify the command exists: which /usr/bin/nonexistent-mcp
  2. Check server configuration: looms config get mcp.servers.broken-server
  3. Review server logs: looms logs mcp --server broken-server

Status: failed
```

**Exit Codes:**
- 0: Success (server healthy)
- 1: Server name required
- 4: Cannot connect to loom server
- 7: MCP server not found in config
- 8: MCP server test failed

---

## Phase 2: Power User Commands (Week 2)

### Priority: HIGH
**Goal**: Enable advanced workflows and debugging.

### Commands to Implement

#### 2.1 `loom sessions export <session-id>`

**Usage:**
```bash
loom sessions export <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `json` | Output format: `json`, `csv`, `markdown` |
| `--output`, `-o` | string | stdout | Output file path |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client methods:
  - `client.GetSession(ctx, sessionID)`
  - `client.GetConversationHistory(ctx, sessionID)`
- Formats:
  - JSON: Full session data with conversation array
  - CSV: Flattened messages (timestamp, role, content, tokens, cost)
  - Markdown: Human-readable conversation transcript

**Example Output:**

JSON format:
```bash
loom sessions export sess_abc123 --format json --output session.json
```
```json
{
  "session_id": "sess_abc123def456",
  "thread_id": "sql-expert",
  "created_at": "2026-01-16T09:15:23Z",
  "updated_at": "2026-01-16T10:42:15Z",
  "total_messages": 15,
  "total_tokens": 12345,
  "total_cost_usd": 0.123456,
  "messages": [
    {
      "timestamp": "2026-01-16T09:15:23Z",
      "role": "user",
      "content": "show me all tables",
      "tokens": 5
    },
    {
      "timestamp": "2026-01-16T09:15:25Z",
      "role": "assistant",
      "content": "I'll query the database...",
      "tokens": 234,
      "cost_usd": 0.00234,
      "tool_calls": ["get_schema"]
    }
  ]
}
```

Markdown format:
```bash
loom sessions export sess_abc123 --format markdown --output session.md
```
```markdown
# Session: sess_abc123def456

**Thread**: sql-expert
**Created**: 2026-01-16 09:15:23
**Messages**: 15
**Cost**: $0.123456

---

## Conversation

### 2026-01-16 09:15:23 - User
show me all tables

### 2026-01-16 09:15:25 - Agent
I'll query the database to list all tables...

[Tool: get_schema]

Found 42 tables in the database...
```

**Exit Codes:**
- 0: Success
- 1: Session ID required or invalid format
- 4: Cannot connect to server
- 7: Session not found
- 9: Cannot write to output file

---

#### 2.2 `loom mcp tools <server-name>`

**Usage:**
```bash
loom mcp tools <server-name> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--filter` | string | `""` | Filter tools by name pattern |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client method: `client.ListMCPServerTools(ctx, serverName)`
- Output format: Detailed tool list with schemas

**Example Output:**
```
MCP Server: vantage
Tools: 45

execute_query
  Description: Execute Teradata SQL query and return results
  Input Schema:
    - query (string, required): SQL query to execute
    - max_rows (integer, optional): Maximum rows to return (default: 1000)
  Returns: Query results as JSON array

get_schema
  Description: Get table schema information
  Input Schema:
    - table_name (string, required): Table name
    - database (string, optional): Database name
  Returns: Schema definition with column details

list_tables
  Description: List all accessible tables
  Input Schema:
    - database (string, optional): Filter by database
  Returns: Array of table names

... (42 more tools)

To test a tool:
  loom mcp call vantage execute_query --arg query="SELECT 1"
```

With filter:
```bash
loom mcp tools vantage --filter "*query*"
```
```
MCP Server: vantage
Filtered Tools: 3 (of 45 total)

execute_query
  Description: Execute Teradata SQL query and return results
  ...

explain_query
  Description: Get query execution plan
  ...

validate_query
  Description: Validate SQL query syntax
  ...
```

**Exit Codes:**
- 0: Success
- 1: Server name required
- 4: Cannot connect to loom server
- 7: MCP server not found
- 8: MCP server not running

---

#### 2.3 `loom mcp call <server-name> <tool-name> [flags]`

**Usage:**
```bash
loom mcp call <server-name> <tool-name> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--arg` | stringArray | `[]` | Tool arguments (key=value, repeatable) |
| `--input` | string | `""` | JSON file with arguments |
| `--timeout` | duration | `30s` | Execution timeout |
| `--server`, `-s` | string | `localhost:60051` | Server address |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Requires: New `CallMCPTool` RPC (see Phase 3)
- Output format: Tool execution result

**Example Usage:**
```bash
# Simple argument
loom mcp call vantage execute_query --arg query="SELECT 1"

# Multiple arguments
loom mcp call vantage execute_query \
  --arg query="SELECT * FROM sales LIMIT 10" \
  --arg max_rows=10

# From JSON file
echo '{"query": "SELECT * FROM sales", "max_rows": 10}' > args.json
loom mcp call vantage execute_query --input args.json
```

**Example Output:**
```
Calling MCP tool: vantage.execute_query

Arguments:
  query: "SELECT * FROM sales LIMIT 10"
  max_rows: 10

Result:
{
  "columns": ["id", "product", "amount", "date"],
  "rows": [
    [1, "Widget A", 299.99, "2026-01-15"],
    [2, "Widget B", 499.99, "2026-01-15"],
    ...
  ],
  "row_count": 10,
  "execution_time_ms": 145
}

Status: success
Execution Time: 145ms
```

**Exit Codes:**
- 0: Success
- 1: Server/tool name required or invalid arguments
- 4: Cannot connect to loom server
- 7: MCP server or tool not found
- 8: MCP server not running
- 10: Tool execution failed

---

## Phase 3: Admin Commands (Week 3)

### Priority: MEDIUM
**Goal**: Server administration and automation.

### Commands to Implement

#### 3.1 `looms sessions prune`

**Usage:**
```bash
looms sessions prune [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--older-than` | duration | `30d` | Delete sessions older than duration |
| `--thread` | string | `""` | Only prune sessions for this thread |
| `--dry-run` | bool | `false` | Show what would be deleted without deleting |
| `--force`, `-f` | bool | `false` | Skip confirmation prompt |

**Implementation:**
- File: `cmd/looms/sessions.go`
- Requires: New `PruneSessions` RPC
- Behavior: Batch delete old sessions

**Example Output:**
```bash
looms sessions prune --older-than 30d --dry-run
```
```
Session Pruning (DRY RUN)

Criteria:
  - Older than: 30 days (before 2025-12-17)
  - Thread filter: (all threads)

Would delete 127 sessions:

THREAD              COUNT  OLDEST              NEWEST
sql-expert          45     90 days ago         31 days ago
weaver              38     85 days ago         32 days ago
teradata-explorer   32     120 days ago        30 days ago
web-search-agent    12     45 days ago         31 days ago

Total sessions: 127
Total storage: ~45 MB

To actually delete:
  looms sessions prune --older-than 30d
```

Without `--dry-run`:
```bash
looms sessions prune --older-than 30d
```
```
⚠️  This will permanently delete 127 sessions older than 30 days.

Continue? (y/N): y

Pruning sessions...
✅ Deleted 127 sessions
✅ Freed 45 MB of storage
```

**Exit Codes:**
- 0: Success
- 1: User cancelled or invalid duration
- 4: Database error

---

#### 3.2 `looms sessions stats`

**Usage:**
```bash
looms sessions stats [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--by-thread` | bool | `false` | Group by thread |
| `--by-date` | bool | `false` | Group by date (daily) |
| `--since` | duration | `7d` | Stats window (e.g., 7d, 30d, 90d) |
| `--format` | string | `table` | Output format: `table`, `json`, `csv` |

**Implementation:**
- File: `cmd/looms/sessions.go`
- Requires: New `GetSessionStats` RPC
- Output format: Aggregate statistics

**Example Output:**
```bash
looms sessions stats --by-thread --since 30d
```
```
Session Statistics (Last 30 days)

Overall:
  Total Sessions:    342
  Active Sessions:   45
  Avg Messages/Session: 12.3
  Total Tokens:      1,234,567
  Total Cost:        $123.45

By Thread:

THREAD              SESSIONS  AVG_MSGS  TOTAL_TOKENS  TOTAL_COST
sql-expert          156       15.2      567,890       $56.78
weaver              98        8.5       234,567       $23.45
teradata-explorer   54        18.9      321,234       $32.12
web-search-agent    34        6.2       110,876       $11.08

Most Active Days:
  2026-01-15: 23 sessions
  2026-01-14: 19 sessions
  2026-01-13: 18 sessions
```

**Exit Codes:**
- 0: Success
- 1: Invalid duration or format

---

#### 3.3 `looms mcp reload [<server-name>]`

**Usage:**
```bash
looms mcp reload [server-name] [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--all` | bool | `false` | Reload all MCP servers |
| `--force`, `-f` | bool | `false` | Force restart even if healthy |

**Implementation:**
- File: `cmd/looms/mcp.go`
- Uses: Existing `RestartMCPServer` RPC
- Behavior: Graceful restart with config reload

**Example Output:**
```bash
looms mcp reload vantage
```
```
Reloading MCP server: vantage

Steps:
  1. Stopping server...        ✅ Stopped (graceful shutdown)
  2. Reloading configuration... ✅ Config loaded
  3. Starting server...         ✅ Started
  4. Testing connection...      ✅ Healthy (45 tools available)

Status: running
Downtime: 1.2 seconds
```

Reload all:
```bash
looms mcp reload --all
```
```
Reloading all MCP servers (3)...

vantage:        ✅ Reloaded (1.2s downtime)
github:         ✅ Reloaded (0.8s downtime)
filesystem:     ⚠️  Already stopped, skipping

Completed: 2 reloaded, 1 skipped
```

**Exit Codes:**
- 0: Success
- 1: No server specified and --all not set
- 7: Server not found
- 8: Reload failed

---

#### 3.4 `looms mcp start <server-name>` / `stop` / `restart`

**Usage:**
```bash
looms mcp start <server-name>
looms mcp stop <server-name>
looms mcp restart <server-name>
```

**Implementation:**
- File: `cmd/looms/mcp.go`
- Requires: New `StartMCPServer`, `StopMCPServer` RPCs
- Uses existing: `RestartMCPServer` RPC

**Example Output:**
```bash
looms mcp start vantage
```
```
Starting MCP server: vantage

Configuration:
  Command: /usr/local/bin/vantage-mcp
  Transport: stdio
  Environment: 3 variables set

Starting...          ✅ Process started (PID: 12345)
Testing connection... ✅ Connected
Discovering tools...  ✅ 45 tools available

Status: running
Startup time: 892ms
```

```bash
looms mcp stop vantage
```
```
Stopping MCP server: vantage

Active sessions: 3

⚠️  This will interrupt 3 active agent sessions using this server.

Continue? (y/N): y

Stopping server...    ✅ Gracefully stopped
Cleaning up...        ✅ Resources released

Status: stopped
```

**Exit Codes:**
- 0: Success
- 1: Server name required or user cancelled
- 7: Server not found
- 8: Operation failed

---

## Implementation Requirements

### New Proto Messages/RPCs Needed

#### Phase 2 Requirements

```protobuf
// Already exists - no new RPCs needed for Phase 2!
// We can use GetConversationHistory for export
```

#### Phase 3 Requirements

```protobuf
// Admin session management
rpc PruneSessions(PruneSessionsRequest) returns (PruneSessionsResponse);
rpc GetSessionStats(GetSessionStatsRequest) returns (SessionStats);

// MCP server lifecycle (start/stop already exist via RestartMCPServer)
rpc StartMCPServer(StartMCPServerRequest) returns (MCPServerInfo);
rpc StopMCPServer(StopMCPServerRequest) returns (StopMCPServerResponse);

// Direct MCP tool execution
rpc CallMCPTool(CallMCPToolRequest) returns (CallMCPToolResponse);
```

### File Structure

```
cmd/loom/
├── main.go           # Register session and mcp commands
├── agents.go         # Existing
├── chat.go           # Existing
├── sessions.go       # NEW - Phase 1 & 2
└── mcp.go            # NEW - Phase 1 & 2

cmd/looms/
├── main.go           # Register admin commands
├── serve.go          # Existing
├── sessions.go       # NEW - Phase 3 (admin commands)
└── mcp.go            # NEW - Phase 3 (admin commands)
```

---

## Implementation Timeline

### Week 1: Phase 1 (Essential Commands)
**Days 1-2:**
- Create `cmd/loom/sessions.go`
- Implement: `list`, `show`, `delete`
- Write tests
- Update documentation

**Days 3-4:**
- Create `cmd/loom/mcp.go`
- Implement: `list`, `test`
- Write tests
- Update documentation

**Day 5:**
- Integration testing
- Documentation review
- Create PR

**Deliverables:**
- 5 new CLI commands
- Full documentation
- Comprehensive tests
- User-facing pain points solved

---

### Week 2: Phase 2 (Power User Commands)
**Days 1-2:**
- Implement `loom sessions export` (JSON, CSV, Markdown)
- Write tests for all export formats

**Days 3-4:**
- Implement `loom mcp tools`
- Implement `loom mcp call` (requires new RPC)
- Write proto for `CallMCPTool`
- Implement server-side handler

**Day 5:**
- Integration testing
- Documentation
- Create PR

**Deliverables:**
- 3 new CLI commands
- New CallMCPTool RPC
- Export formats (JSON, CSV, Markdown)
- Power user workflows enabled

---

### Week 3: Phase 3 (Admin Commands)
**Days 1-2:**
- Create `cmd/looms/sessions.go`
- Implement `prune` (with new RPC)
- Implement `stats` (with new RPC)

**Days 3-4:**
- Create `cmd/looms/mcp.go`
- Implement `reload`, `start`, `stop`
- Write proto for new RPCs
- Implement server-side handlers

**Day 5:**
- Integration testing
- Documentation
- Create PR

**Deliverables:**
- 5 new admin commands
- 3-4 new RPCs
- Server automation capabilities

---

## Success Criteria

### Phase 1
- ✅ Users can list and find session IDs
- ✅ Users can view session details
- ✅ Users can delete old sessions
- ✅ Users can verify MCP server status
- ✅ Users can test MCP connectivity

### Phase 2
- ✅ Users can export sessions for documentation/analysis
- ✅ Users can discover available MCP tools
- ✅ Users can test individual MCP tools directly

### Phase 3
- ✅ Admins can prune old sessions automatically
- ✅ Admins can monitor session usage
- ✅ Admins can manage MCP server lifecycle

---

## Testing Strategy

### Unit Tests
- Test each command with mock client
- Test argument parsing
- Test output formatting
- Test error handling

### Integration Tests
- Test against real loom server
- Test session lifecycle (create, list, show, delete)
- Test MCP operations with real MCP servers
- Test export formats produce valid output

### E2E Tests
- Test complete workflows:
  - Create session → Chat → List → Resume → Export → Delete
  - Add MCP server → Test → List tools → Call tool → Restart

---

## Documentation Updates Required

### Reference Documentation
- `docs/reference/cli.md` - Add all new commands
- `docs/reference/sessions.md` - NEW - Session management guide
- `docs/reference/mcp-cli.md` - NEW - MCP CLI reference

### Guides
- `docs/guides/session-management.md` - NEW - Session workflows
- `docs/guides/mcp-debugging.md` - NEW - Debugging MCP servers
- `docs/guides/automation.md` - UPDATE - Add session pruning

### README
- Update Quick Start with session examples
- Add MCP management examples

---

## Risk Assessment

### Low Risk ✅
- Phase 1: Only uses existing RPCs and client methods
- Well-defined interfaces already tested

### Medium Risk ⚠️
- Phase 2: `CallMCPTool` RPC requires new server implementation
- Export formats need validation

### High Risk ⚡
- Phase 3: Admin commands modify server state
- Session pruning is destructive (but has --dry-run and confirmation)

### Mitigation
- Comprehensive tests at each phase
- `--dry-run` flags for destructive operations
- Confirmation prompts (unless `--force`)
- Detailed error messages with troubleshooting

---

## Open Questions

1. **Session export to Hawk**: Should we add `--hawk` flag to export directly to Hawk?
2. **MCP tool schemas**: Should we generate JSON schemas for tool inputs?
3. **Batch operations**: Should we support batch delete (e.g., `loom sessions delete sess_*`)?
4. **Session search**: Should we add full-text search across session histories?

---

## Next Steps

1. **Get approval** on this plan
2. **Create branch**: `feature/cli-sessions-mcp-commands`
3. **Start Phase 1** implementation
4. **Daily check-ins** on progress
5. **PR reviews** after each phase

---

**Questions? Concerns? Adjustments?** Let me know before we proceed!
