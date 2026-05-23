
# Terminal UI Reference

**Version**: v1.2.0

Technical reference for Loom's interactive terminal user interface - a Bubbletea-based chat client for real-time agent conversations.


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
# Launch TUI (defaults to guide agent)
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
| `Shift+Enter` / `Ctrl+J` | Insert newline |
| `Ctrl+C` / `Ctrl+Q` | Quit |
| `Ctrl+D` | Toggle details panel |
| `Ctrl+N` | New session |
| `Ctrl+E` | Open agents dialog |
| `Ctrl+W` | Open workflows dialog |
| `Ctrl+F` | Add file attachment |
| `Ctrl+G` / `Ctrl+/` | Toggle help |
| `Ctrl+K` / `Ctrl+P` | Open command palette |
| `↑` / `↓` | Scroll chat history |
| `PgUp` / `PgDn` | Scroll page up/down |
| `Home` / `End` | Scroll to top/bottom |

### Configuration Parameters

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--server`, `-s` | `string` | `127.0.0.1:60051` | Loom server address (host:port) |
| `--thread`, `-t` | `string` | Auto-select | Agent/thread ID to connect to |
| `--session` | `string` | Generate new | Resume existing session ID |
| `--tls` | `bool` | `false` | Enable TLS connection |
| `--tls-insecure` | `bool` | `false` | Skip TLS certificate verification |
| `--tls-ca-file` | `string` | System CAs | Path to CA certificate file |
| `--tls-server-name` | `string` | From address | Override TLS server name |


## Overview

The Loom Terminal UI (TUI) provides an **interactive chat interface** for conversing with Loom agents via gRPC. Built with [Bubbletea v2](https://charm.land/bubbletea), it offers a modern, responsive terminal experience with real-time streaming, cost tracking, and session management.

**Implementation**: `cmd/loom/` (main, chat, agents, sessions, artifacts, mcp, providers) + `internal/tui/` (UI components, pages, adapters)
**Framework**: Bubbletea v2 (`charm.land/bubbletea/v2`) + Bubbles v2 + Lipgloss v2
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
- **Go Version**: 1.25+ (for building from source)
- **Network**: Access to Loom server (gRPC port, default 60051)

### Server Requirements

The TUI requires a running Loom server:

```bash
# Start server (separate terminal)
looms serve

# Verify server is reachable (from another terminal)
loom agents
```

**Expected Output**:
```
Available agents (3):
  sql-optimizer, code-reviewer, data-analyst
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
- **Agent Switching**: Switch agents via dialog (`Ctrl+E`) or `/agents` command
- **Workflow Support**: Open workflows dialog via `Ctrl+W` or `/workflows` command
- **Model Switching**: Switch LLM model/provider via command palette or `/model` command
- **File Attachments**: Add file attachments via `Ctrl+F` or file picker
- **Multiline Input**: Insert newlines with `Shift+Enter` / `Ctrl+J`

### Partial ⚠️

- **Message Editing**: Cannot edit sent messages
- **Export History**: No built-in export (use server-side session queries)

### Planned 📋

- **Themes**: Custom user-configurable color schemes
- **Plugins**: Extension system for custom components
- **Shortcuts**: User-configurable keybindings


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
loom version 1.2.0
```


### Building from Source

```bash
# Clone repository
git clone https://github.com/teradata-labs/loom.git
cd loom

# Build TUI binary
go build -tags fts5 -o loom ./cmd/loom

# Verify build
./loom --version
```


## Configuration

### Connection Configuration

The TUI connects to the Loom server via gRPC. Configure connection using command-line flags.

**Command-Line Flags**:
```bash
loom --server myserver.example.com:60051 --thread sql-optimizer
```

**Note**: The TUI does not currently read environment variables for the server address. Use the `--server` flag to specify the server address.


### Server Address

**Flag**: `--server`, `-s`
**Type**: `string`
**Default**: `127.0.0.1:60051`
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
- If no `--thread` specified → Defaults to built-in `guide` agent (helps discover and select agents)
- If `--thread` specified → Connect directly to that agent
- If server not available → Shows `no-server` splash with connection instructions

**Examples**:
```bash
# Auto-select agent (if only one)
loom

# Connect to specific agent
loom --thread sql-optimizer

# Agent with full identifier
loom --thread sql-optimizer-abc123
```

**Agent Switching**: Use `Ctrl+E` or `/agents` command to open the agents dialog at any time.


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

**Session ID Format**: Server-generated (format depends on server session store)


## Keybindings

### Global Keybindings

| Key | Action | Description |
|-----|--------|-------------|
| `Ctrl+C` / `Ctrl+Q` | Quit | Exit TUI (opens quit dialog) |
| `Ctrl+G` / `Ctrl+/` | Toggle Help | Show/hide help bar |
| `Ctrl+K` / `Ctrl+P` | Commands | Open command palette |
| `Ctrl+D` | Toggle Details | Show/hide detail panel (chat page) |
| `Ctrl+N` | New Session | Clear session and start fresh |
| `Ctrl+O` / `Ctrl+S` | Switch Sessions | Open session switcher |
| `Ctrl+E` | Agents | Open agents dialog |
| `Ctrl+W` | Workflows | Open workflows dialog |
| `Ctrl+F` | Add Attachment | Open file picker for attachments |
| `Ctrl+Z` | Suspend | Suspend TUI process |


### Input Keybindings

| Key | Action | Description |
|-----|--------|-------------|
| `Enter` | Send Message | Submit query to agent |
| `Shift+Enter` / `Ctrl+J` | Insert Newline | Add a new line (multiline input) |
| `Ctrl+O` | Open Editor | Open external editor ($EDITOR) for long messages |
| `Ctrl+R` | Delete Attachment | Enter attachment delete mode (then press index or `r` for all) |
| `Esc` | Cancel | Cancel current operation or delete mode |
| `@` | File Completions | Start file path completion |

**Note**: Multiline input is supported via `Shift+Enter` / `Ctrl+J`. The `\` character at end of line also inserts a newline. No character limit on input.


### Viewport Navigation

| Key | Action | Description |
|-----|--------|-------------|
| `↑` | Scroll Up | Scroll chat history up by 1 line |
| `↓` | Scroll Down | Scroll chat history down by 1 line |
| `PgUp` | Page Up | Scroll up by viewport height |
| `PgDn` | Page Down | Scroll down by viewport height |
| `Home` | Scroll to Top | Jump to start of conversation |
| `End` | Scroll to Bottom | Jump to end of conversation |
| `Tab` | Change Focus | Switch focus between editor and chat pane |


### Permission Dialog Keybindings (HITL)

When a permission request is active:

| Key | Action | Description |
|-----|--------|-------------|
| `a` / `A` / `Ctrl+A` | Allow | Allow the action once |
| `s` / `S` / `Ctrl+S` | Allow Session | Allow for entire session |
| `d` / `D` / `Esc` | Deny | Deny the action |
| `Enter` / `Ctrl+Y` | Confirm | Confirm selected option |
| `Tab` | Switch | Switch between options |
| `t` | Toggle Diff | Toggle diff mode view |

### Clarification Dialog Keybindings

When an agent asks a clarification question:

| Key | Action | Description |
|-----|--------|-------------|
| `Enter` / `Ctrl+S` | Submit | Submit answer |
| `Esc` | Cancel | Cancel clarification |


## UI Components

### Main Interface Layout

```
┌─────────────────────────────────────────────────────────────────┐
│ Teradata™ LOOM ╱╱╱╱╱╱╱╱╱╱╱╱ ~/Projects • 42% • ctrl+d open   │
│                                                                  │
│ ┌──────────────────────────────────────────────────┬──────────┐ │
│ │            Chat Viewport (Scrollable)            │ Sidebar  │ │
│ │                                                  │ (opt.)   │ │
│ │  User: Show sales by region                      │          │ │
│ │                                                  │ Agent    │ │
│ │  Assistant: Here are the sales by region:        │ Model    │ │
│ │  West: $2.4M, East: $2.1M...                    │ Tools    │ │
│ │                                                  │          │ │
│ │  User: Compare with last quarter                 │          │ │
│ │                                                  │          │ │
│ │  Assistant: (streaming in progress)              │          │ │
│ └──────────────────────────────────────────────────┴──────────┘ │
│                                                                  │
│ ┌─────────────────────────────────────────────────────────────┐ │
│ │ > Ready!                                                     │ │
│ └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│ ctrl+c quit • ctrl+n new session • ctrl+d page down • pgup/pgdn│
└─────────────────────────────────────────────────────────────────┘
```


### Header Bar

**Location**: Top of screen
**Content**:
- Branding: "Teradata LOOM" with gradient styling
- Working directory path
- Context usage percentage (tokens used / context window)
- Details toggle indicator (`ctrl+d` open/close)

**Example**:
```
Teradata™ LOOM ╱╱╱╱╱╱╱╱╱╱╱╱╱╱ ~/Projects/loom • 42% • ctrl+d open
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

**Color Coding** (default theme):
- User messages: Blue text (bold)
- Agent/assistant messages: Pink text
- Tool messages: Orange text (italic)
- System messages: Gray text (italic)
- Errors: Red text (bold)
- Cost info: Orange/Warning color
- Session info: Blue/Info color


### Input Area

**Location**: Bottom of screen (above status bar)
**Component**: Textarea (Bubbles v2 component)

**Configuration**:
- Placeholder: Randomized from pool ("Ready!", "Ready...", "Ready?", "Ready for instructions")
- Working placeholder: Randomized ("Working!", "Thinking...", "Brrrrr...", etc.)
- Yolo mode placeholder: "Yolo mode!"
- Prompt: `> ` (focused) or `:::` (unfocused)
- Character limit: None (unlimited)
- Height: Dynamic (adjusts to layout)
- Auto-focus: Yes
- Multiline: Yes (`Shift+Enter` / `Ctrl+J` inserts newline)
- File completions: Type `@` to trigger file path completions
- Max attachments: 5


### Status Bar

**Location**: Bottom of screen
**Content**: Context-sensitive help text showing available keybindings

**States**:

**Normal Mode** (shows available key bindings):
```
ctrl+c quit • ctrl+n new session • ctrl+u page up • ctrl+d page down • pgup/pgdn scroll
```

**Full Help** (toggled with `Ctrl+G`):
Expands to show all available keybindings for the current context.


### Help Bar

**Trigger**: Press `Ctrl+G` or `Ctrl+/` to toggle between short and full help
**Location**: Bottom status bar (expands when toggled)

The help display is inline in the status bar, not a separate overlay. It shows context-sensitive keybindings relevant to the current state.

### Slash Commands

Type in the editor to access quick commands:

| Command | Action |
|---------|--------|
| `/clear`, `/new`, `/reset` | Clear current session |
| `/quit`, `/exit` | Exit TUI |
| `/sessions` | Open session switcher |
| `/model` | Switch LLM model/provider |
| `/agents` | Open agents dialog |
| `/workflows` | Open workflows dialog |
| `/apps` | Open MCP apps browser |
| `/mcp` | Add MCP server |
| `/patterns` | Open pattern browser |
| `/sidebar` | Toggle sidebar |
| `/help` | Show slash command help |


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
│  User Quits (Ctrl+C opens quit dialog)                   │
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
1. Connect to server via gRPC
2. If no `--thread` specified, defaults to built-in `guide` agent
3. If server not available, shows `no-server` splash with connection instructions
4. Session created on first message send
5. Session persisted on server


### Resume Session

**Command**:
```bash
loom --thread sql-optimizer --session <session-id>
```

**Behavior**:
1. Connect to server
2. Load existing session by ID
3. Continue conversation from where you left off


### Session Persistence

**Storage**: Server-side (not TUI-side)
**Lifetime**: Until server restart or explicit deletion
**Location**: Server session store (SQLite or in-memory)

**Query Session Info via CLI**:
```bash
# List all sessions
loom sessions list

# Show session details
loom sessions show <session-id>

# Delete session
loom sessions delete <session-id>
```

**Flags for `loom sessions list`**:
- `--limit`, `-n`: Maximum sessions to return (default: 20)
- `--offset`: Number of sessions to skip (default: 0)


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

**Progress Updates** (from proto `WeaveProgress`):
```proto
message WeaveProgress {
  ExecutionStage stage = 1;       // Enum: execution stage
  int32 progress = 2;             // 0-100
  string message = 3;             // Human-readable status
  string tool_name = 4;           // Tool being executed (if applicable)
  int64 timestamp = 5;            // Timestamp
  ExecutionResult partial_result = 6; // Partial result
  HITLRequestInfo hitl_request = 7;   // HITL request (when stage == HUMAN_IN_THE_LOOP)
  string partial_content = 8;     // Accumulated content (streaming)
  bool is_token_stream = 9;       // True if token streaming update
  int32 token_count = 10;         // Running token count
  int64 ttft_ms = 11;             // Time to first token (ms)
  CostInfo cost = 12;             // Cost information
}
```

**Execution Stages** (enum `ExecutionStage`):
```
PATTERN_SELECTION  → Pattern matching
SCHEMA_DISCOVERY   → Schema discovery
LLM_GENERATION     → LLM token generation (streaming)
TOOL_EXECUTION     → Tool being executed
HUMAN_IN_THE_LOOP  → Waiting for human input
GUARDRAIL_CHECK    → Guardrail validation
SELF_CORRECTION    → Self-correction
COMPLETED          → Done
FAILED             → Error
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

**Header**: Shows context usage percentage (not cumulative cost in header).


### Cost Calculation

**Cost Info Structure** (from gRPC):
```proto
message CostInfo {
  double total_cost_usd = 1;    // Total cost (USD)
  LLMCost llm_cost = 2;         // LLM cost breakdown
  double backend_cost_usd = 3;  // Backend execution cost
}

message LLMCost {
  int32 total_tokens = 1;   // Total tokens used
  int32 input_tokens = 2;   // Input tokens
  int32 output_tokens = 3;  // Output tokens
}
```

**CLI Chat Display** (from `cmd/loom/chat.go`):
```
[Cost: $0.001234 | Tokens: 456]
```


### Cumulative Cost

**Tracking**: Cost information is reported per-response in the `CostInfo` field of `WeaveProgress`
**CLI Chat Display**: Shown at end of response in stderr
**TUI Display**: Cost tracked per session via server-side `total_cost_usd` field on the `Session` proto

**Session Cost Query**:
```bash
loom sessions show <session-id>
# Shows: Total Cost: $0.001234
```


### Cost by Provider

**Varies by LLM provider**:
- **Anthropic Claude**: $3-$15 per 1M tokens
- **AWS Bedrock**: $0.80-$24 per 1M tokens
- **Ollama**: $0.00 (local models)

See [LLM Provider Reference](./llm-providers.md) for detailed pricing.


## Human-in-the-Loop (HITL)

### HITL Overview

Human-in-the-Loop allows agents to request user approval for sensitive operations (e.g., SQL writes, API calls, file deletions) and ask clarification questions.

**Protocol**: `HITLRequestInfo` message in `WeaveProgress` (when stage == `EXECUTION_STAGE_HUMAN_IN_THE_LOOP`)
**TUI Handling**: Two mechanisms:
1. **Permission Dialog**: For tool execution approval (allow/allow session/deny via `a`/`s`/`d` keys)
2. **Clarification Dialog**: For agent questions (text input, answered via `AnswerClarificationQuestion` RPC)


### HITL Flow

```
┌──────────────────────────────────────────────────────────┐
│                    HITL Flow                              │
│                                                           │
│  Agent Needs Approval or Clarification                    │
│       │                                                   │
│       ├── Permission Request (tool execution)            │
│       │       │                                           │
│       │       ▼                                           │
│       │  TUI Opens Permission Dialog                     │
│       │  ┌────────────────────────────────────┐          │
│       │  │ [Tool details with diff view]      │          │
│       │  │                                    │          │
│       │  │ a: allow  s: allow session  d: deny│          │
│       │  └────────────────────────────────────┘          │
│       │       │                                           │
│       │       ├── 'a' → Allow once                       │
│       │       ├── 's' → Allow for session                │
│       │       └── 'd' / Esc → Deny                       │
│       │                                                   │
│       └── Clarification Question                         │
│               │                                           │
│               ▼                                           │
│          TUI Opens Clarification Dialog                   │
│          ┌────────────────────────────────────┐          │
│          │ Agent question displayed           │          │
│          │ [text input for answer]            │          │
│          │                                    │          │
│          │ Enter: submit  Esc: cancel         │          │
│          └────────────────────────────────────┘          │
│               │                                           │
│               ▼                                           │
│          Send AnswerClarificationQuestion RPC             │
│                                                           │
│  Agent Continues Based on Response                       │
│                                                           │
└──────────────────────────────────────────────────────────┘
```


### HITL Display

**Permission Request**: Opens a modal dialog showing the tool details, with diff view toggle (`t` key). User selects allow (`a`), allow for session (`s`), or deny (`d`/`Esc`).

**Clarification Question**: Opens a modal dialog showing the agent's question with a text input field. User types an answer and submits with `Enter`, or cancels with `Esc`.


### HITL Configuration

**Server-Side**: Agent must be configured with tools that require approval
**TUI-Side**: Automatic (permission dialog shown when server sends permission request)

**Yolo Mode**: Toggle via command palette "Toggle Yolo Mode" to skip permission requests (auto-approve all). The editor prompt changes to indicate yolo mode is active.


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
1. Verify server is running: `looms serve` (in another terminal)
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
1. List available agents: `loom agents`
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
2. Display error in status bar / error detail dialog
3. User can send a new message to retry


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
loom sessions list

# Try resuming with session ID
loom --thread sql-optimizer --session <session-id>

# If session truly lost, start new session
loom --thread sql-optimizer
```


### Issue: High Latency During Streaming

**Symptoms**:
- Slow token delivery
- Long wait for first token

**Resolution**:
1. Check network latency: `ping <server-address>`
2. Check server logs for backend query delays
3. Try different LLM provider (some faster than others)
4. Switch model via `/model` command or command palette


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

Use the `--server` flag to specify the server address:
```bash
loom --server prod.example.com:60051 --thread sql-optimizer
```

**Note**: Environment variable support for default server address is not currently implemented. Use command-line flags.


### Theme Customization

**Implementation**: `internal/tui/styles/theme.go` (internal theme) and `pkg/tui/styles/theme.go` (public API)
**Status**: ⚠️ Partial (theme system exists with dark/light detection, but not user-configurable via config file)

The TUI automatically detects terminal color scheme (dark/light) and adjusts colors. There are multiple built-in themes:
- Default theme (primary: cyan, secondary: pink, accent: purple)
- Crush theme (`internal/tui/styles/crush_theme.go`)
- Enhanced theme (`pkg/tui/styles/enhanced_theme.go`)

Custom user-configurable themes via YAML config are planned.


## See Also

### Reference Documentation
- [CLI Reference](./cli.md) - `looms` server commands and `loom` client commands
- [Streaming Reference](./streaming.md) - Streaming protocol details
- [TLS Reference](./tls.md) - TLS configuration details
- [LLM Providers Reference](./llm-providers.md) - Provider configuration and pricing

### Guides
- [TUI Guide](../guides/tui-guide.md) - TUI usage guide
- [Human-in-the-Loop Guide](../guides/human-in-the-loop.md) - HITL best practices

### Architecture Documentation
- [Communication Architecture](../architecture/communication-system-design.md) - Streaming protocol design

### External Resources
- [Bubbletea v2 Documentation](https://charm.land/bubbletea) - TUI framework
- [Bubbles v2 Components](https://charm.land/bubbles) - Textarea, Viewport
- [Lipgloss v2 Styling](https://charm.land/lipgloss) - Terminal styling
