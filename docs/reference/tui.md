
# Terminal UI Reference

**Version**: v1.0.0-beta.1

Complete technical reference for Loom's interactive terminal user interface - a Bubbletea-based chat client for real-time agent conversations.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Features](#features)
- [Installation](#installation)
- [Configuration](#configuration)
- [Keybindings](#keybindings)
- [UI Components](#ui-components)
- [Session Management](#session-management)
- [Streaming Mode](#streaming-mode)
- [Cost Tracking](#cost-tracking)
- [Human-in-the-Loop (HITL)](#human-in-the-loop-hitl)
- [TLS Support](#tls-support)
- [Error Handling](#error-handling)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)
- [Customization](#customization)
- [See Also](#see-also)


## Quick Reference

### Launch Commands

```bash
# Auto-connect to agent (if only one exists)
loom

# Connect to specific agent/thread
loom --thread sql-optimizer

# Connect to specific server
loom --server prod-server.example.com:60051

# Resume existing session
loom --thread sql-optimizer --session sess_abc123

# TLS connection
loom --tls --tls-ca-file /path/to/ca.crt
```

### Keybindings Summary

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Quit |
| `Ctrl+D` | Quit |
| `Ctrl+L` | Clear screen |
| `?` | Show help |
| `↑` / `↓` | Scroll chat history |
| `PgUp` / `PgDn` | Scroll page up/down |
| `Home` / `End` | Scroll to top/bottom |

### Configuration Parameters

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | `string` | `localhost:60051` | Loom server address (host:port) |
| `--thread`, `-t` | `string` | Auto-select | Agent/thread ID to connect to |
| `--session` | `string` | Generate new | Resume existing session ID |
| `--tls` | `bool` | `false` | Enable TLS connection |
| `--tls-insecure` | `bool` | `false` | Skip TLS certificate verification |
| `--tls-ca-file` | `string` | System CAs | Path to CA certificate file |
| `--tls-server-name` | `string` | From address | Override TLS server name |


## Overview

The Loom Terminal UI (TUI) provides an **interactive chat interface** for conversing with Loom agents via gRPC. Built with [Bubbletea](https://github.com/charmbracelet/bubbletea), it offers a modern, responsive terminal experience with real-time streaming, cost tracking, and session management.

**Implementation**: `cmd/loom/` (main, chat, memory, thread components)
**Framework**: Charmbracelet Bubbletea + Bubbles + Lipgloss
**Protocol**: gRPC (Loom service)
**Available Since**: v0.1.0

**Key Features**:
- Interactive chat with streaming responses
- Real-time cost tracking
- Session persistence and resume
- Human-in-the-loop (HITL) interaction
- Thread/agent selection
- TLS support
- Viewport for scrolling chat history
- Syntax-highlighted messages
- Progress indicators


## Prerequisites

### System Requirements

- **Operating System**: Linux, macOS, Windows (with WSL2)
- **Terminal**: Any ANSI-compatible terminal (iTerm2, Terminal.app, Alacritty, etc.)
- **Go Version**: 1.21+ (for building from source)
- **Network**: Access to Loom server (gRPC port, default 60051)

### Server Requirements

The TUI requires a running Loom server:

```bash
# Start server (separate terminal)
looms serve

# Verify server is running
looms status
```

**Expected Output**:
```
✅ Server running on localhost:60051
✅ 3 agents registered: sql-optimizer, code-reviewer, data-analyst
```


## Features

### Implemented ✅

- **Interactive Chat**: Send queries and receive responses in real-time
- **Streaming Responses**: Real-time token-by-token streaming display
- **Session Management**: Create, resume, and persist conversation sessions
- **Agent Selection**: Auto-select single agent or choose from list
- **Cost Tracking**: Real-time display of token usage and costs
- **Progress Indicators**: Visual feedback for long-running operations
- **Scrollable History**: Navigate conversation with keyboard shortcuts
- **HITL Support**: Interactive approval for sensitive operations
- **TLS Support**: Secure connections with certificate validation
- **Error Recovery**: Graceful error handling with reconnection
- **Syntax Highlighting**: Colored output for user/agent messages
- **Help System**: Built-in help with keybinding reference
- **Timestamps**: Message timestamps for all interactions

### Partial ⚠️

- **Multi-Agent Switching**: Cannot switch agents mid-session (planned v1.1.0)
- **Message Editing**: Cannot edit sent messages (planned v1.1.0)
- **Export History**: No built-in export (use server-side session queries)

### Planned 📋

- **Themes**: Custom color schemes (v1.1.0)
- **Plugins**: Extension system for custom components (v1.2.0)
- **Shortcuts**: User-configurable keybindings (v1.1.0)
- **Markdown Rendering**: Rich text display (v1.2.0)


## Installation

### Using Pre-built Binary

```bash
# Download latest release
curl -LO https://github.com/teradata-labs/loom/releases/latest/download/loom-$(uname -s)-$(uname -m)

# Make executable
chmod +x loom-*

# Move to PATH
sudo mv loom-* /usr/local/bin/loom

# Verify installation
loom --version
```

**Expected Output**:
```
Loom TUI v1.0.0-beta.1
```


### Building from Source

```bash
# Clone repository
git clone https://github.com/teradata-labs/loom.git
cd loom

# Build TUI binary
go build -o loom ./cmd/loom

# Verify build
./loom --version
```


## Configuration

### Connection Configuration

The TUI connects to the Loom server via gRPC. Configure connection using flags or environment variables.

**Command-Line Flags**:
```bash
loom --server myserver.example.com:60051 --thread sql-optimizer
```

**Environment Variables**:
```bash
# Set server address
export LOOM_SERVER=myserver.example.com:60051

# Launch TUI (uses environment)
loom --thread sql-optimizer
```


### Server Address

**Flag**: `--server`, `-s`
**Type**: `string`
**Default**: `localhost:60051`
**Format**: `host:port`

**Examples**:
```bash
# Local server (default)
loom

# Custom port
loom --server localhost:8080

# Remote server
loom --server prod.example.com:60051

# IP address
loom --server 192.168.1.100:60051
```


### Agent/Thread Selection

**Flag**: `--thread`, `-t`
**Type**: `string`
**Default**: Auto-select (if only one agent exists)

**Behavior**:
- If no `--thread` specified and only 1 agent exists → Auto-connect
- If no `--thread` specified and multiple agents exist → Show selection menu
- If `--thread` specified → Connect directly to that agent

**Examples**:
```bash
# Auto-select agent (if only one)
loom

# Connect to specific agent
loom --thread sql-optimizer

# Agent with full identifier
loom --thread sql-optimizer-abc123
```

**Agent Selection Menu**:
```
Select an agent thread:

  1. sql-optimizer (SQL query optimization and analysis)
  2. code-reviewer (Code review and best practices)
  3. data-analyst (Data analysis and visualization)

Enter number (1-3): 2

✅ Connected to code-reviewer
```


### Session Management

**Flag**: `--session`
**Type**: `string`
**Default**: Generate new session ID

**Behavior**:
- Without `--session`: New session created, ID displayed at startup
- With `--session`: Resume existing session, load conversation history

**Examples**:
```bash
# New session (default)
loom --thread sql-optimizer

# Resume existing session
loom --thread sql-optimizer --session sess_abc123def456

# Session ID displayed at startup
# Session ID: sess_abc123def456 (save this to resume later)
```

**Session ID Format**: `sess_` + 12 alphanumeric characters


## Keybindings

### Global Keybindings

| Key | Action | Description |
|-----|--------|-------------|
| `Ctrl+C` | Quit | Exit TUI immediately |
| `Ctrl+D` | Quit | Exit TUI (alternative) |
| `?` | Toggle Help | Show/hide help panel |
| `Ctrl+L` | Clear Screen | Clear viewport (not history) |


### Input Keybindings

| Key | Action | Description |
|-----|--------|-------------|
| `Enter` | Send Message | Submit query to agent |
| `Ctrl+A` | Move to Start | Move cursor to start of line |
| `Ctrl+E` | Move to End | Move cursor to end of line |
| `Ctrl+U` | Clear Input | Delete all text before cursor |
| `Ctrl+K` | Delete to End | Delete all text after cursor |
| `Ctrl+W` | Delete Word | Delete word before cursor |
| `Alt+Backspace` | Delete Word | Delete word before cursor (alternative) |

**Note**: Multi-line input not supported (newline disabled)


### Viewport Navigation

| Key | Action | Description |
|-----|--------|-------------|
| `↑` | Scroll Up | Scroll chat history up by 1 line |
| `↓` | Scroll Down | Scroll chat history down by 1 line |
| `PgUp` | Page Up | Scroll up by viewport height |
| `PgDn` | Page Down | Scroll down by viewport height |
| `Home` | Scroll to Top | Jump to start of conversation |
| `End` | Scroll to Bottom | Jump to end of conversation |
| `g` | Scroll to Top | Vi-style scroll to top |
| `G` | Scroll to Bottom | Vi-style scroll to bottom |


### Human-in-the-Loop (HITL) Keybindings

When HITL request active:

| Key | Action | Description |
|-----|--------|-------------|
| `y` | Approve | Approve pending action |
| `n` | Reject | Reject pending action |
| `Enter` | Default Action | Approve if input empty |


## UI Components

### Main Interface Layout

```
┌─────────────────────────────────────────────────────────────────┐
│ Loom TUI                    Session: sess_abc123    Cost: $0.42 │
│═════════════════════════════════════════════════════════════════│
│                                                                  │
│ ┌─────────────────────────────────────────────────────────────┐ │
│ │                    Chat Viewport (Scrollable)               │ │
│ │                                                             │ │
│ │  You: Show sales by region                                 │ │
│ │  09:15:23                                                   │ │
│ │                                                             │ │
│ │  Agent: Here are the sales by region:                      │ │
│ │  West: $2.4M, East: $2.1M...                              │ │
│ │  09:15:25 | Tokens: 1,234 | Cost: $0.012                   │ │
│ │                                                             │ │
│ │  You: Compare with last quarter                            │ │
│ │  09:16:10                                                   │ │
│ │                                                             │ │
│ │  Agent: ⠋ Querying database...                             │ │
│ │  (streaming in progress)                                    │ │
│ └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│ ┌─────────────────────────────────────────────────────────────┐ │
│ │ ▸ Ask me anything...                                        │ │
│ │                                                             │ │
│ └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│ Press ? for help | Ctrl+C to quit | ↑↓ to scroll               │
└─────────────────────────────────────────────────────────────────┘
```


### Header Bar

**Location**: Top of screen
**Content**:
- Application title: "Loom TUI"
- Session ID: Current session identifier
- Total cost: Cumulative conversation cost

**Example**:
```
Loom TUI                    Session: sess_abc123def456    Cost: $0.42
```


### Chat Viewport

**Location**: Center of screen (scrollable)
**Content**: Conversation history with messages

**Message Format**:

**User Message**:
```
You: Show sales by region
09:15:23
```

**Agent Message**:
```
Agent: Here are the sales by region:
West: $2.4M, East: $2.1M, South: $1.5M
09:15:25 | Tokens: 1,234 (input: 45, output: 1,189) | Cost: $0.012
```

**Streaming Message** (in progress):
```
Agent: ⠋ Analyzing data...
Comparing sales trends across regions. West shows...
(streaming, 234 tokens so far)
```

**Color Coding**:
- User messages: Cyan text
- Agent messages: Green text
- Timestamps: Gray text
- Cost info: Yellow text
- Errors: Red text
- HITL requests: Magenta text


### Input Area

**Location**: Bottom of screen (above status bar)
**Component**: Textarea (Bubbles component)

**Configuration**:
- Placeholder: "Ask me anything..."
- Prompt: "▸ "
- Character limit: 2000 characters
- Height: 3 lines
- Auto-focus: Yes
- Multiline: No (Enter submits)


### Status Bar

**Location**: Bottom of screen
**Content**: Context-sensitive help text

**States**:

**Normal Mode**:
```
Press ? for help | Ctrl+C to quit | ↑↓ to scroll
```

**Loading**:
```
⠋ Waiting for response... | Press Ctrl+C to cancel
```

**HITL Active**:
```
Action requires approval: [SQL Query] | Press y to approve, n to reject
```

**Error**:
```
⚠ Error: Connection lost. Reconnecting... | Ctrl+C to quit
```


### Help Panel

**Trigger**: Press `?` to toggle
**Location**: Overlay on viewport

**Content**:
```
╔═══════════════════════════════════════╗
║          Loom TUI Help                ║
╠═══════════════════════════════════════╣
║ Input                                 ║
║   Enter       Send message            ║
║   Ctrl+C      Quit                    ║
║   Ctrl+L      Clear screen            ║
║                                       ║
║ Navigation                            ║
║   ↑ ↓         Scroll chat             ║
║   PgUp PgDn   Page up/down            ║
║   Home End    Scroll to start/end     ║
║                                       ║
║ HITL (when active)                    ║
║   y           Approve action          ║
║   n           Reject action           ║
║                                       ║
║ Press ? again to close                ║
╚═══════════════════════════════════════╝
```


## Session Management

### Session Lifecycle

```
┌──────────────────────────────────────────────────────────┐
│                   Session Lifecycle                       │
│                                                           │
│  Launch TUI                                               │
│      │                                                    │
│      ├── No --session flag                               │
│      │       │                                            │
│      │       ▼                                            │
│      │  Generate New Session ID                          │
│      │  (sess_abc123def456)                              │
│      │                                                    │
│      └── With --session flag                             │
│              │                                            │
│              ▼                                            │
│         Load Existing Session                            │
│         (fetch history from server)                      │
│                                                           │
│  ┌──────────────────────────────────────────┐            │
│  │       Active Conversation                │            │
│  │  - Send queries                          │            │
│  │  - Receive responses                     │            │
│  │  - Messages stored on server             │            │
│  └──────────────────────────────────────────┘            │
│      │                                                    │
│      ▼                                                    │
│  User Quits (Ctrl+C or Ctrl+D)                          │
│      │                                                    │
│      ▼                                                    │
│  Session Persists on Server                              │
│  (can resume later with --session)                       │
│                                                           │
└──────────────────────────────────────────────────────────┘
```


### New Session

**Command**:
```bash
loom --thread sql-optimizer
```

**Behavior**:
1. Generate session ID: `sess_` + 12 random alphanumeric chars
2. Display session ID at startup
3. Send queries, build conversation history
4. Session persisted on server

**Startup Message**:
```
✅ Connected to sql-optimizer
📝 Session ID: sess_abc123def456
💡 Save this ID to resume later: loom --thread sql-optimizer --session sess_abc123def456
```


### Resume Session

**Command**:
```bash
loom --thread sql-optimizer --session sess_abc123def456
```

**Behavior**:
1. Connect to server
2. Fetch session history from server
3. Display historical messages in viewport
4. Continue conversation from where you left off

**Startup Message**:
```
✅ Connected to sql-optimizer
📝 Resuming session: sess_abc123def456
📜 Loaded 12 previous messages
```

**Viewport Content** (historical messages):
```
You: Show sales by region
09:15:23 (yesterday)

Agent: Here are the sales...
09:15:25 (yesterday)

────────────────────────────────────
Session resumed at 10:32:15 (today)
────────────────────────────────────

You: Update the analysis for this quarter
10:32:20
```


### Session Persistence

**Storage**: Server-side (not TUI-side)
**Lifetime**: Until server restart or explicit deletion
**Location**: Server session store (SQLite or in-memory)

**Query Session Info**:
```bash
# List all sessions
looms sessions list

# Get session details
looms sessions get sess_abc123def456

# Delete session
looms sessions delete sess_abc123def456
```


## Streaming Mode

### Real-Time Streaming

The TUI uses `StreamWeave` RPC for real-time token-by-token response streaming.

**Protocol**: gRPC server-streaming RPC
**Endpoint**: `loomv1.LoomService/StreamWeave`


### Streaming Behavior

**Visual Indicators**:
1. **Loading Spinner**: Animated spinner during agent processing
2. **Partial Content**: Tokens displayed as received
3. **Token Counter**: Real-time token count
4. **Time to First Token (TTFT)**: Latency until first token received

**Example Streaming Display**:
```
Agent: ⠋ Analyzing data...
Comparing sales trends across regions. West shows strong growth with
$2.4M in Q4, up 15% from Q3. East maintained $2.1M...
(streaming, 234 tokens so far, TTFT: 340ms)
```


### Streaming Stages

**Progress Updates**:
```proto
message WeaveProgress {
  string stage = 1;           // "thinking", "querying", "responding"
  int32 progress_pct = 2;     // 0-100
  string status_message = 3;  // Human-readable status
  StreamChunk chunk = 4;      // Partial response content
  CostInfo cost_info = 5;     // Real-time cost tracking
}
```

**TUI Display**:
```
Stage: thinking → "⠋ Agent is thinking..."
Stage: querying → "⠹ Querying database..."
Stage: responding → "⠸ Generating response..."
```


### Streaming Performance

**Typical Latency**:
- **Time to First Token (TTFT)**: 200-500ms
- **Token Rate**: 20-50 tokens/second (depends on LLM provider)
- **Total Response Time**: 2-10 seconds for typical queries

**Factors Affecting Performance**:
- LLM provider latency
- Network latency (TUI ↔ server ↔ LLM)
- Backend query execution time
- Token generation complexity


## Cost Tracking

### Real-Time Cost Display

The TUI displays token usage and costs for every agent response.

**Display Format**:
```
Agent: [response content]
09:15:25 | Tokens: 1,234 (input: 45, output: 1,189) | Cost: $0.012
```

**Header Summary** (cumulative):
```
Loom TUI                    Session: sess_abc123    Cost: $0.42
```


### Cost Calculation

**Cost Info Structure** (from gRPC):
```proto
message CostInfo {
  int32 input_tokens = 1;
  int32 output_tokens = 2;
  int32 total_tokens = 3;
  double cost_usd = 4;
}
```

**Rendering**:
```go
fmt.Sprintf("Tokens: %d (input: %d, output: %d) | Cost: $%.4f",
    cost.TotalTokens,
    cost.InputTokens,
    cost.OutputTokens,
    cost.CostUsd)
```

**Example**:
```
Tokens: 3,456 (input: 123, output: 3,333) | Cost: $0.0876
```


### Cumulative Cost

**Tracking**: Sum of all message costs in current session
**Display**: Header bar (top-right)
**Precision**: 2 decimal places

**Example**:
```
Cost: $0.42  (after 5 messages)
Cost: $1.23  (after 10 messages)
Cost: $5.67  (after 25 messages)
```


### Cost by Provider

**Varies by LLM provider**:
- **Anthropic Claude**: $3-$15 per 1M tokens
- **AWS Bedrock**: $0.80-$24 per 1M tokens
- **Ollama**: $0.00 (local models)

See [LLM Provider Reference](./llm-providers.md) for detailed pricing.


## Human-in-the-Loop (HITL)

### HITL Overview

Human-in-the-Loop allows agents to request user approval for sensitive operations (e.g., SQL writes, API calls, file deletions).

**Protocol**: `HITLRequestInfo` message in `WeaveProgress`
**TUI Handling**: Display request, wait for user input (y/n), send approval via `SubmitHITLResponse` RPC


### HITL Flow

```
┌──────────────────────────────────────────────────────────┐
│                    HITL Flow                              │
│                                                           │
│  Agent Needs Approval                                     │
│       │                                                   │
│       ▼                                                   │
│  Server Sends HITLRequestInfo                            │
│       │                                                   │
│       ▼                                                   │
│  TUI Displays Request                                     │
│  ┌────────────────────────────────────┐                  │
│  │ Action Requires Approval:          │                  │
│  │                                    │                  │
│  │ [SQL Write]                        │                  │
│  │ DELETE FROM users WHERE age > 90   │                  │
│  │                                    │                  │
│  │ Press y to approve, n to reject    │                  │
│  └────────────────────────────────────┘                  │
│       │                                                   │
│       ├── User presses 'y'                               │
│       │       │                                           │
│       │       ▼                                           │
│       │  Send SubmitHITLResponse(approved=true)          │
│       │                                                   │
│       └── User presses 'n'                               │
│               │                                           │
│               ▼                                           │
│          Send SubmitHITLResponse(approved=false)         │
│                                                           │
│  Agent Continues or Aborts Based on Response             │
│                                                           │
└──────────────────────────────────────────────────────────┘
```


### HITL Display

**Request Message**:
```
⚠ Action requires approval:

Type: SQL Write
Query: DELETE FROM users WHERE inactive_days > 180

Rationale: Removing inactive users to comply with data retention policy.

Press y to approve, n to reject
```

**Approval**:
```
✅ Action approved. Continuing...
```

**Rejection**:
```
❌ Action rejected. Agent will find an alternative approach.
```


### HITL Configuration

**Server-Side**: Agent must be configured with HITL-enabled tools
**TUI-Side**: Automatic (no configuration needed)

**Example Agent Config** (server):
```yaml
agents:
  - id: sql-admin
    tools:
      - name: execute_write_query
        hitl_required: true  # Requires user approval
```


## TLS Support

### TLS Configuration

The TUI supports TLS for secure connections to Loom servers.

**Flags**:
- `--tls`: Enable TLS
- `--tls-insecure`: Skip certificate verification (self-signed certs)
- `--tls-ca-file`: Custom CA certificate
- `--tls-server-name`: Override server name verification


### TLS Examples

**Production Server** (valid certificate):
```bash
loom --server prod.example.com:60051 --tls
```

**Self-Signed Certificate**:
```bash
loom --server dev.example.com:60051 --tls --tls-insecure
```

**Custom CA Certificate**:
```bash
loom --server internal.example.com:60051 \
     --tls \
     --tls-ca-file /path/to/internal-ca.crt
```

**Testing with Name Override**:
```bash
loom --server 192.168.1.100:60051 \
     --tls \
     --tls-server-name myserver.local
```


### TLS Certificate Validation

**Default Behavior**:
1. Use system root CA certificates
2. Verify server certificate chain
3. Check server name matches certificate CN/SAN

**Insecure Mode** (`--tls-insecure`):
- ⚠️ Skips all certificate validation
- ⚠️ Vulnerable to MITM attacks
- ⚠️ Only use for development/testing

**Custom CA**:
- Loads custom CA certificate
- Appends to system root CAs
- Validates server certificate against custom + system CAs


## Error Handling

### Connection Errors

**Symptom**: Cannot connect to server

**Display**:
```
❌ Failed to connect to Loom server at localhost:60051

Make sure the server is running:
  looms serve

Press Ctrl+C to quit
```

**Resolution**:
1. Verify server is running: `looms status`
2. Check server address: `loom --server <correct-address>`
3. Verify firewall allows gRPC port


### Agent Not Found

**Symptom**: Specified agent doesn't exist

**Display**:
```
❌ Agent not found: sql-optimizer-xyz

Available agents:
  - sql-optimizer
  - code-reviewer
  - data-analyst

Press Ctrl+C to quit
```

**Resolution**:
1. List available agents: `looms agents list`
2. Use correct agent ID: `loom --thread <correct-id>`


### Session Not Found

**Symptom**: Resuming non-existent session

**Display**:
```
⚠ Session not found: sess_xyz123
Creating new session instead.

Session ID: sess_abc789def012
```

**Resolution**: No action needed (TUI falls back to new session)


### Stream Interruption

**Symptom**: Network error during streaming

**Display**:
```
⚠ Stream interrupted: connection lost

Attempting to reconnect...
```

**Behavior**:
1. Display partial response received so far
2. Attempt reconnection (3 retries with exponential backoff)
3. If reconnected: Resume conversation
4. If failed: Display error, allow user to retry or quit


## Troubleshooting

### Issue: TUI Not Starting

**Symptoms**:
- Command not found: `loom`
- Permission denied

**Resolution**:
```bash
# Verify binary exists
which loom

# If not found, check PATH
echo $PATH

# Add loom directory to PATH (if needed)
export PATH=$PATH:/path/to/loom

# Verify executable permissions
chmod +x /path/to/loom
```


### Issue: Display Garbled or Colors Missing

**Symptoms**:
- Box-drawing characters not rendering
- Colors not displaying
- Layout broken

**Resolution**:
```bash
# Verify terminal supports ANSI colors
echo $TERM
# Should show: xterm-256color or similar

# Set TERM if needed
export TERM=xterm-256color

# Test with simpler terminal if issues persist
loom --no-color  # (feature not yet implemented, use basic terminal)
```


### Issue: Cannot Resume Session

**Symptoms**:
- Session ID not accepted
- History not loading

**Resolution**:
```bash
# Verify session exists on server
looms sessions list | grep sess_abc123

# Check session format (must be sess_ + 12 chars)
loom --session sess_abc123def456  # Correct
loom --session abc123             # Incorrect

# If session truly lost, start new session
loom --thread sql-optimizer
```


### Issue: High Latency During Streaming

**Symptoms**:
- Slow token delivery
- Long wait for first token

**Resolution**:
1. Check network latency: `ping <server-address>`
2. Verify server performance: `looms status --verbose`
3. Try different LLM provider (some faster than others)
4. Check server logs for backend query delays


## Best Practices

### 1. Save Session IDs

```bash
# Save session ID at startup
loom --thread sql-optimizer > ~/loom-session.txt

# Extract session ID
SESSION_ID=$(grep "Session ID:" ~/loom-session.txt | awk '{print $3}')

# Resume later
loom --thread sql-optimizer --session $SESSION_ID
```


### 2. Use TLS in Production

```bash
# Always use TLS for production servers
loom --server prod.example.com:60051 --tls

# Never use --tls-insecure in production
# (only for development/testing)
```


### 3. Scroll to Review History

```bash
# Before asking follow-up questions, scroll up to review context
# Press ↑ or PgUp to review previous messages
# Press Home to jump to start of conversation
```


### 4. Monitor Costs

```bash
# Keep an eye on cumulative cost (top-right)
# If cost exceeds budget, quit and review queries

# Check cost breakdown per message
# Optimize queries to reduce token usage
```


### 5. Use HITL Wisely

```bash
# Always review HITL requests carefully
# For SQL writes: verify query doesn't delete too much
# For API calls: check endpoint and payload
# When in doubt, reject and refine query
```


## Customization

### Custom Server Configuration

**Environment Variable**:
```bash
# Set default server address
export LOOM_SERVER=prod.example.com:60051

# Launch TUI (uses environment)
loom
```

**Config File** (planned v1.1.0):
```yaml
# ~/.config/loom/tui.yaml
server:
  address: prod.example.com:60051
  tls:
    enabled: true
    ca_file: /path/to/ca.crt

defaults:
  thread: sql-optimizer
```


### Theme Customization

**Implementation**: `pkg/tui/styles/theme.go`
**Status**: ⚠️ Partial (theme exists but not user-configurable yet)

**Planned** (v1.1.0):
```yaml
# ~/.config/loom/theme.yaml
colors:
  user_message: "#00FFFF"    # Cyan
  agent_message: "#00FF00"   # Green
  error: "#FF0000"           # Red
  timestamp: "#808080"       # Gray
```


## See Also

### Reference Documentation
- [CLI Reference](./cli.md) - `looms` server commands
- [gRPC API Reference](./grpc-api.md) - Protocol details
- [Session Management](./sessions.md) - Session persistence

### Guides
- [Getting Started with TUI](../guides/getting-started-tui.md) - Quick tutorial
- [Human-in-the-Loop Guide](../guides/hitl.md) - HITL best practices

### Architecture Documentation
- [Communication Architecture](../architecture/communication-system.md) - Streaming protocol design

### External Resources
- [Bubbletea Documentation](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Bubbles Components](https://github.com/charmbracelet/bubbles) - Textarea, Viewport
- [Lipgloss Styling](https://github.com/charmbracelet/lipgloss) - Terminal styling
