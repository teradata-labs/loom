# CLI Sessions and MCP Commands Implementation Plan

**Created**: 2026-01-16
**Status**: Phase 1 Complete (✅), Phase 2 Partially Complete (⚠️), Phase 3 Planned (📋)
**Branch**: Phase 1 merged to `main`

## Executive Summary

The proto definitions and server-side implementations for session and MCP management are **already complete**. Phase 1 CLI commands (`sessions list/show/delete`, `mcp list/test/tools`) have been implemented in `cmd/loom/sessions.go` and `cmd/loom/mcp.go`.

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
- `pkg/server/server.go` - Session RPCs on `Server` struct
- `pkg/server/multi_agent.go` - Session RPCs on `MultiAgentServer` struct
- `pkg/server/mcp_management.go` - All MCP RPCs on `MultiAgentServer` struct

**Client Library** (`pkg/tui/client/client.go`):
- `CreateSession(ctx, name, agentID) (*Session, error)`
- `GetSession(ctx, sessionID) (*Session, error)`
- `ListSessions(ctx, limit, offset) ([]*Session, error)`
- `DeleteSession(ctx, sessionID) error`
- `GetConversationHistory(ctx, sessionID) ([]*Message, error)`
- `ListMCPServers(ctx, req) (*ListMCPServersResponse, error)`
- `GetMCPServer(ctx, req) (*MCPServerInfo, error)`
- `TestMCPServerConnection(ctx, req) (*TestMCPServerConnectionResponse, error)`
- `ListMCPServerTools(ctx, serverName) ([]*ToolDefinition, error)`
- `AddMCPServer(ctx, req) (*AddMCPServerResponse, error)`
- `UpdateMCPServer(ctx, req) (*MCPServerInfo, error)`
- `DeleteMCPServer(ctx, req) (*DeleteMCPServerResponse, error)`
- `RestartMCPServer(ctx, req) (*MCPServerInfo, error)`
- `HealthCheckMCPServers(ctx, req) (*HealthCheckMCPServersResponse, error)`

### Phase 1 CLI Commands ✅ Implemented

**CLI Commands** (`cmd/loom/`):
- `cmd/loom/sessions.go` - `list`, `show`, `delete` commands
- `cmd/loom/mcp.go` - `list`, `test`, `tools` commands

### What's Still Missing ❌

**Phase 2 CLI Commands** (`cmd/loom/`):
- No `sessions export` subcommand
- No `mcp call` subcommand (requires `CallMCPTool` RPC which does not exist)

**Phase 3 Admin Commands** (`cmd/looms/`):
- No `sessions.go` file (prune, stats)
- No `mcp.go` file (reload, start, stop, restart)

---

## Phase 1: Essential User Commands ✅ IMPLEMENTED

### Priority: CRITICAL
**Goal**: Solve immediate pain points for users.

### Implemented Commands

#### 1.1 `loom sessions list` ✅

**Usage:**
```bash
loom sessions list [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--limit`, `-n` | int32 | `20` | Max results to return |
| `--offset` | int32 | `0` | Pagination offset |
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

> **Note**: The `ListSessionsRequest` proto also supports `state` and `backend` filters, but these are not yet exposed as CLI flags.

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client method: `client.ListSessions(ctx, limit, offset)`
- Output format: Table with columns: SESSION ID, STATE, BACKEND, MESSAGES, CREATED

**Example Output:**
```
SESSION ID                STATE           BACKEND         MESSAGES     CREATED
-------------------------------------------------------------------------------------
sess_abc123def456         active          file            15           2 hours ago
sess_xyz789ghi012         idle            sqlite          8            1 day ago
sess_jkl345mno678         active          file            23           3 days ago

Showing 3 session(s)
```

**Exit Codes:**
- 0: Success
- 1: Connection error or listing error

---

#### 1.2 `loom sessions show <session-id>` ✅

**Usage:**
```bash
loom sessions show <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

> **Note**: A `--with-history` flag to include conversation history via `client.GetConversationHistory()` is not yet implemented.

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client method: `client.GetSession(ctx, sessionID)`
- Output format: Session metadata (id, name, backend, state, messages, cost, timestamps, metadata map)

**Example Output:**
```
Session: sess_abc123def456
Name: my-session
Backend: file
State: active
Messages: 15
Total Cost: $0.123456
Created: 2026-01-16T09:15:23Z (2 hours ago)
Updated: 2026-01-16T10:42:15Z (1 hour ago)

Metadata:
  key1: value1
```

**Exit Codes:**
- 0: Success
- 1: Connection or retrieval error

---

#### 1.3 `loom sessions delete <session-id>` ✅

**Usage:**
```bash
loom sessions delete <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

> **Note**: No `--force` flag or confirmation prompt is implemented. Deletion is immediate.

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client method: `client.DeleteSession(ctx, sessionID)`
- Behavior: Deletes immediately with no confirmation

**Example Output:**
```
Deleted session: sess_abc123def456
```

**Exit Codes:**
- 0: Success
- 1: Connection or deletion error

---

#### 1.4 `loom mcp list` ✅

**Usage:**
```bash
loom mcp list [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client method: `client.ListMCPServers(ctx, req)`
- Output format: Table with columns: NAME, STATUS, TOOLS, COMMAND

**Example Output:**
```
NAME                 STATUS          TOOLS      COMMAND
--------------------------------------------------------------------------------
vantage              running         45         /usr/local/bin/vantage-mcp
github               running         12         npx github-mcp
filesystem           stopped         0          /usr/bin/fs-mcp

Total: 3 MCP server(s)
```

**Exit Codes:**
- 0: Success
- 1: Connection or listing error

---

#### 1.5 `loom mcp test <server-name>` ✅

**Usage:**
```bash
loom mcp test <server-name> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

> **Note**: No `--timeout` flag is implemented. Uses a hardcoded 10-second context timeout.

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client methods: `client.GetMCPServer(ctx, req)` then `client.ListMCPServerTools(ctx, serverName)`
- Output format: Server info, connection status, and tool summary (first 10 tools shown)

**Example Output:**

Success:
```
Checking MCP server: vantage

Server Info:
  Name: vantage
  Transport: stdio
  Command: /usr/local/bin/vantage-mcp
  Args: [-m mcp_server.main]

Status:
  ✅ Connected
  State: running
  ✅ Enabled
  Uptime: 2h 15m

Tools:
  45 tools available
    - execute_query
    - get_schema
    - list_tables
    ... and 42 more
```

Failure:
```
Checking MCP server: broken-server

Server Info:
  Name: broken-server
  Transport: stdio
  Command: /usr/bin/nonexistent-mcp

Status:
  ❌ Not connected
  State: error
  Error: command not found: /usr/bin/nonexistent-mcp
```

**Exit Codes:**
- 0: Success (server info retrieved)
- 1: Connection or retrieval error

---

## Phase 2: Power User Commands (Week 2)

### Priority: HIGH
**Goal**: Enable advanced workflows and debugging.

### Commands

#### 2.0 `loom mcp tools <server-name>` ✅ (Moved up from Phase 2, already implemented)

**Usage:**
```bash
loom mcp tools <server-name> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Client method: `client.ListMCPServerTools(ctx, serverName)`
- Output format: Tool name and truncated description (50 chars)

**Example Output:**
```
Tools from MCP server: vantage

execute_query                  - Execute Teradata SQL query and return resu...
get_schema                     - Get table schema information
list_tables                    - List all accessible tables

Total: 45 tool(s)
```

> **Note**: No `--filter` flag is implemented.

---

#### 2.1 `loom sessions export <session-id>` 📋 Planned

**Usage:**
```bash
loom sessions export <session-id> [flags]
```

**Flags:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `json` | Output format: `json`, `csv`, `markdown` |
| `--output`, `-o` | string | stdout | Output file path |
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

**Implementation:**
- File: `cmd/loom/sessions.go`
- Client methods:
  - `client.GetSession(ctx, sessionID)`
  - `client.GetConversationHistory(ctx, sessionID)`
- Formats:
  - JSON: Full session data with conversation array
  - CSV: Flattened messages (timestamp, role, content, tokens, cost)
  - Markdown: Human-readable conversation transcript

> **Note**: The `Session` proto message has fields: `id`, `name`, `backend`, `state`, `total_cost_usd`, `conversation_count`, `created_at`, `updated_at`, `metadata`. There is no `thread_id` field.

**Example Output:**

JSON format:
```bash
loom sessions export sess_abc123 --format json --output session.json
```
```json
{
  "session_id": "sess_abc123def456",
  "name": "my-session",
  "backend": "file",
  "state": "active",
  "created_at": "2026-01-16T09:15:23Z",
  "updated_at": "2026-01-16T10:42:15Z",
  "conversation_count": 15,
  "total_cost_usd": 0.123456,
  "messages": [
    {
      "timestamp": "2026-01-16T09:15:23Z",
      "role": "user",
      "content": "show me all tables"
    },
    {
      "timestamp": "2026-01-16T09:15:25Z",
      "role": "assistant",
      "content": "I'll query the database...",
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

**Name**: my-session
**Backend**: file
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

#### 2.2 `loom mcp tools <server-name>` -- ✅ Already implemented (see 2.0 above)

---

#### 2.3 `loom mcp call <server-name> <tool-name> [flags]` 📋 Planned

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
| `--server`, `-s` | string | `127.0.0.1:60051` | Server address (inherited) |

**Implementation:**
- File: `cmd/loom/mcp.go`
- Requires: New `CallMCPTool` RPC (see Phase 2 Requirements above)
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

## Phase 3: Admin Commands (Week 3) 📋 Planned

### Priority: MEDIUM
**Goal**: Server administration and automation.

### Commands to Implement

#### 3.1 `looms sessions prune` 📋

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

#### 3.2 `looms sessions stats` 📋

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

#### 3.3 `looms mcp reload [<server-name>]` 📋

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

#### 3.4 `looms mcp start <server-name>` / `stop` / `restart` 📋

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
// GetConversationHistory already exists for export
// New RPC needed for direct tool execution:
rpc CallMCPTool(CallMCPToolRequest) returns (CallMCPToolResponse);
```

#### Phase 3 Requirements

```protobuf
// Admin session management
rpc PruneSessions(PruneSessionsRequest) returns (PruneSessionsResponse);
rpc GetSessionStats(GetSessionStatsRequest) returns (SessionStats);

// MCP server lifecycle (RestartMCPServer already exists)
rpc StartMCPServer(StartMCPServerRequest) returns (MCPServerInfo);
rpc StopMCPServer(StopMCPServerRequest) returns (StopMCPServerResponse);
```

> **Note**: None of the Phase 2 or Phase 3 RPCs above exist in the proto yet.

### File Structure

```
cmd/loom/
├── main.go           # Registers session, mcp, and other commands
├── agents.go         # ✅ Existing
├── artifacts.go      # ✅ Existing
├── chat.go           # ✅ Existing
├── providers.go      # ✅ Existing
├── sessions.go       # ✅ Implemented (list, show, delete)
└── mcp.go            # ✅ Implemented (list, test, tools)

cmd/looms/
├── main.go           # Register admin commands
├── root.go           # ✅ Existing (root command definition)
├── cmd_serve.go      # ✅ Existing (server start)
├── cmd_config.go     # ✅ Existing (config management)
├── sessions.go       # 📋 Phase 3 (admin commands: prune, stats)
└── mcp.go            # 📋 Phase 3 (admin commands: reload, start, stop)
```

---

## Implementation Timeline

### Week 1: Phase 1 (Essential Commands) ✅ COMPLETE
**Days 1-2:**
- ✅ Created `cmd/loom/sessions.go`
- ✅ Implemented: `list`, `show`, `delete`
- ⚠️ Tests not yet written (`cmd/loom/` has no `*_test.go` files)
- ⚠️ Documentation not yet updated

**Days 3-4:**
- ✅ Created `cmd/loom/mcp.go`
- ✅ Implemented: `list`, `test`, `tools` (tools was moved up from Phase 2)
- ⚠️ Tests not yet written
- ⚠️ Documentation not yet updated

**Day 5:**
- 📋 Integration testing (not yet done)
- 📋 Documentation review (not yet done)

**Deliverables:**
- ✅ 6 new CLI commands implemented (sessions: list, show, delete; mcp: list, test, tools)
- ⚠️ Documentation pending
- ⚠️ Tests pending
- ✅ User-facing pain points solved

---

### Week 2: Phase 2 (Power User Commands) ⚠️ Partially Complete
**Days 1-2:**
- 📋 Implement `loom sessions export` (JSON, CSV, Markdown)
- 📋 Write tests for all export formats

**Days 3-4:**
- ✅ `loom mcp tools` already implemented (moved up to Phase 1)
- 📋 Implement `loom mcp call` (requires new `CallMCPTool` RPC)
- 📋 Write proto for `CallMCPTool`
- 📋 Implement server-side handler

**Day 5:**
- 📋 Integration testing
- 📋 Documentation
- 📋 Create PR

**Deliverables:**
- 📋 2 remaining CLI commands (`sessions export`, `mcp call`)
- 📋 New `CallMCPTool` RPC
- 📋 Export formats (JSON, CSV, Markdown)
- 📋 Power user workflows enabled

---

### Week 3: Phase 3 (Admin Commands) 📋 Not Started
**Days 1-2:**
- 📋 Create `cmd/looms/sessions.go`
- 📋 Implement `prune` (requires new `PruneSessions` RPC)
- 📋 Implement `stats` (requires new `GetSessionStats` RPC)

**Days 3-4:**
- 📋 Create `cmd/looms/mcp.go`
- 📋 Implement `reload`, `start`, `stop`
- 📋 Write proto for new RPCs (`PruneSessions`, `GetSessionStats`, `StartMCPServer`, `StopMCPServer`)
- 📋 Implement server-side handlers

**Day 5:**
- 📋 Integration testing
- 📋 Documentation
- 📋 Create PR

**Deliverables:**
- 📋 5 new admin commands
- 📋 4 new RPCs
- 📋 Server automation capabilities

---

## Success Criteria

### Phase 1 ✅ Complete
- ✅ Users can list and find session IDs (`loom sessions list`)
- ✅ Users can view session details (`loom sessions show`)
- ✅ Users can delete old sessions (`loom sessions delete`)
- ✅ Users can verify MCP server status (`loom mcp list`, `loom mcp test`)
- ✅ Users can discover MCP tools (`loom mcp tools`)

### Phase 2 ⚠️ Partial
- 📋 Users can export sessions for documentation/analysis (`sessions export` not yet implemented)
- ✅ Users can discover available MCP tools (`mcp tools` already implemented)
- 📋 Users can test individual MCP tools directly (`mcp call` not yet implemented, requires `CallMCPTool` RPC)

### Phase 3 📋 Planned
- 📋 Admins can prune old sessions automatically (requires `PruneSessions` RPC)
- 📋 Admins can monitor session usage (requires `GetSessionStats` RPC)
- 📋 Admins can manage MCP server lifecycle (requires `StartMCPServer`/`StopMCPServer` RPCs)

---

## Testing Strategy

> **Status**: ⚠️ No tests exist yet for `cmd/loom/sessions.go` or `cmd/loom/mcp.go`. No `*_test.go` files in `cmd/loom/`.

### Unit Tests 📋
- Test each command with mock client
- Test argument parsing
- Test output formatting
- Test error handling

### Integration Tests 📋
- Test against real loom server
- Test session lifecycle (create, list, show, delete)
- Test MCP operations with real MCP servers
- Test export formats produce valid output

### E2E Tests 📋
- Test complete workflows:
  - Create session -> Chat -> List -> Resume -> Export -> Delete
  - Add MCP server -> Test -> List tools -> Call tool -> Restart

---

## Documentation Updates Required 📋

### Reference Documentation
- `docs/reference/cli.md` - 📋 Add all new commands (file exists but needs updating)
- `docs/reference/sessions.md` - 📋 NEW - Session management guide (does not exist yet)
- `docs/reference/mcp-cli.md` - 📋 NEW - MCP CLI reference (does not exist yet)

### Guides
- `docs/guides/session-management.md` - 📋 NEW - Session workflows (does not exist yet)
- `docs/guides/mcp-debugging.md` - 📋 NEW - Debugging MCP servers (does not exist yet)
- `docs/guides/automation.md` - 📋 NEW - Add session pruning (does not exist yet)

### README
- 📋 Update Quick Start with session examples
- 📋 Add MCP management examples

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
- Tests at each phase covering critical paths
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

1. ~~**Get approval** on this plan~~ ✅ Done
2. ~~**Create branch**: `feature/cli-sessions-mcp-commands`~~ ✅ Phase 1 merged to `main`
3. ~~**Start Phase 1** implementation~~ ✅ Complete
4. **Implement Phase 2**: `sessions export` and `mcp call` (requires `CallMCPTool` RPC)
5. **Implement Phase 3**: Admin commands (requires new proto RPCs)
6. **Write documentation** for implemented commands
