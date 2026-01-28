# Loom TUI Guide

**Version**: v1.0.2
**Last Updated**: 2026-01-27

## Overview

The Loom Terminal UI (`loom`) provides an interactive, guide-driven interface for working with LLM agents. Built with [Bubbletea](https://github.com/charmbracelet/bubbletea) and inspired by [Crush](https://github.com/charmbracelet/crush)'s visual design, it offers a modern, keyboard-first experience for agent interaction.

## Key Concepts

### Guide-Driven Discovery

When you launch `loom`, you start with the **Guide** - a built-in agent designed to help you discover and select the right agent for your task:

```bash
# Launch TUI with Guide
loom

# Or directly to a specific agent
loom --thread weaver
loom --thread sql-optimizer
```

The Guide:
- Understands natural language queries about available agents
- Suggests suitable agents based on your needs
- Lists agents with descriptions using the `agent_management` tool
- Works like a regular Loom agent (conversation is persisted)

### Agent Selection

There are three ways to select an agent in the TUI:

1. **Ask the Guide**: Natural language query → Guide suggests agents via clarification dialog
2. **Keyboard Shortcut** (`ctrl+e`): Opens agent selection modal with fuzzy search
3. **Sidebar Navigation**: Click "Weaver" in the sidebar to switch to weaver agent

### Workflow Selection

Workflows (multi-agent orchestrations) can be selected via:

1. **Keyboard Shortcut** (`ctrl+w`): Opens workflow selection modal
2. **Agent Modal** (`ctrl+e`): Workflow coordinators appear in the agent list

Workflow format: `workflow-name:sub-agent-name` (e.g., `dnd-campaign-workflow:session-planner`)

## Keyboard Shortcuts

### Global Shortcuts (Available Everywhere)

| Shortcut | Action | Description |
|----------|--------|-------------|
| `ctrl+c` or `ctrl+q` | Quit | Exit the TUI |
| `ctrl+e` | Agents | Open agent selection modal with fuzzy search |
| `ctrl+w` | Workflows | Open workflow selection modal |
| `ctrl+k` or `ctrl+p` | Commands | Open command palette |
| `ctrl+o` or `ctrl+s` | Sessions | Open session switcher |
| `ctrl+g` or `ctrl+/` | Help | Show more keyboard shortcuts |
| `ctrl+z` | Suspend | Suspend TUI (Unix systems) |

### Chat View

| Shortcut | Action | Description |
|----------|--------|-------------|
| `tab` | Focus Editor | Switch focus to message input |
| `shift+tab` | Focus Chat | Switch focus to message list |
| `ctrl+u` | Page Up | Scroll up half a page |
| `ctrl+d` | Page Down | Scroll down half a page |
| `u` | Half Page Up | Scroll up (when chat focused) |
| `d` | Half Page Down | Scroll down (when chat focused) |
| `g` or `home` | Go to Top | Jump to first message |
| `G` or `end` | Go to Bottom | Jump to last message |
| `ctrl+l` | Clear | Clear chat messages |
| `ctrl+n` | New Session | Start a new conversation session |

### Editor (Message Input)

| Shortcut | Action | Description |
|----------|--------|-------------|
| `enter` | Send | Send message to agent |
| `shift+enter` or `ctrl+j` | New Line | Insert line break in message |
| `ctrl+f` | Add Image | Attach an image file |
| `@` | Mention File | Reference a file in message |
| `ctrl+o` | External Editor | Open system editor for longer messages |
| `/` | Commands | Open command palette (when editor is empty) |

### Agent/Workflow Modals

| Shortcut | Action | Description |
|----------|--------|-------------|
| `enter` | Select | Switch to selected agent/workflow |
| `esc` | Cancel | Close modal without changing |
| `up` / `down` | Navigate | Move selection |
| Type text | Search | Fuzzy filter by name/description |

### Sidebar Navigation

| Shortcut | Action | Description |
|----------|--------|-------------|
| `up` / `down` | Navigate | Cycle through sections |
| `enter` | Select | Activate selected item |
| Mouse click | Select | Click to activate (Weaver, MCP server, pattern) |

## UI Layout

### Non-Compact Mode (default)

```
┌─────────────────────────────────────────────────────────┬──────────────┐
│ Header: Agent Name | Model | Session                    │   Sidebar    │
├─────────────────────────────────────────────────────────┤              │
│                                                          │  Weaver      │
│                                                          │  MCP Servers │
│                     Chat Messages                        │  Patterns    │
│                                                          │              │
│                                                          │              │
├─────────────────────────────────────────────────────────┤              │
│ Message Input Editor                                    │              │
└─────────────────────────────────────────────────────────┴──────────────┘
```

### Compact Mode (narrow/short terminals)

```
┌─────────────────────────────────────────────────────────┐
│ Header: Agent Name | Model | Session                    │
├─────────────────────────────────────────────────────────┤
│                                                          │
│                     Chat Messages                        │
│                                                          │
├─────────────────────────────────────────────────────────┤
│ Message Input Editor                                    │
└─────────────────────────────────────────────────────────┘
```

**Compact Mode Triggers:**
- Terminal width < 120 columns
- Terminal height < 30 rows

## Sidebar Sections

The sidebar shows three main sections:

### 1. Weaver
- Click to switch to the weaver meta-agent
- Weaver creates new agents from natural language descriptions
- After creating an agent, TUI automatically switches to it

### 2. MCP Servers
- Lists configured Model Context Protocol servers
- Click to expand and see available tools
- Click on a tool to view details (name, description, input schema)
- Use `ctrl+e` to add a new MCP server

### 3. Patterns
- Shows pattern categories (SQL, Teradata, Postgres, Code, Text, etc.)
- Click category to expand/collapse pattern files
- Click pattern file to view/edit in dialog
- Patterns are automatically selected and injected by agents based on task intent

## Weaver Auto-Switch

When weaver creates a new agent, the TUI automatically switches to it:

**Workflow:**
1. Switch to weaver: `loom --thread weaver` or click "Weaver" in sidebar
2. Ask weaver to create an agent: "Create a SQL performance analyzer"
3. Weaver uses `agent_management` tool to create agent YAML
4. TUI detects agent creation in tool result
5. Waits 200ms for hot-reload to detect new agent file
6. Retries up to 3 times (100ms delays) if agent not found
7. Automatically switches to new agent when ready

**Total wait time**: 200ms initial + up to 300ms retries = max 500ms

**If auto-switch fails**: Warning message suggests using `ctrl+e` to manually select the agent.

## Session Persistence

Each agent maintains its own conversation session:

- **Switching agents**: Conversations are saved per agent
- **Return to agent**: Previous messages are restored
- **New agents**: Start with empty conversation history
- **Session storage**: Managed by the server, persisted to SQLite

**Example:**
```bash
# Talk to guide
loom
> "Show me SQL agents"

# Switch to weaver (ctrl+e or sidebar)
# Previous guide conversation is saved

# Switch back to guide
# Guide conversation is restored exactly where you left off
```

## Agent Selection Modal (ctrl+e)

**Features:**
- Fuzzy search by agent name or description
- Filters out: weaver, guide, workflow coordinators, sub-agents
- Shows only regular agents
- Keyboard navigation (up/down/enter/esc)
- Toggle: Press `ctrl+e` again to close

**Display Format:**
```
┌─────────────────────────────────────┐
│ Select Agent                        │
│ Search: [sql_______________]        │
│                                     │
│ > sql-optimizer                     │
│   sql-debugger                      │
│   sql-performance-analyzer          │
│                                     │
│ esc: cancel  enter: select          │
└─────────────────────────────────────┘
```

## Workflow Selection Modal (ctrl+w)

**Features:**
- Shows workflow coordinators with sub-agent counts
- Format: `workflow-name (N agents)`
- Fuzzy search by workflow name
- Keyboard navigation

**Display Format:**
```
┌─────────────────────────────────────┐
│ Select Workflow                     │
│ Search: [_______________]           │
│                                     │
│ > dnd-campaign-workflow (3 agents)  │
│   data-pipeline-workflow (5 agents) │
│                                     │
│ esc: cancel  enter: select          │
└─────────────────────────────────────┘
```

## Visual Design

### Color Scheme (Crush-Inspired)

- **Primary**: Orange (`#ff7043`) for highlights and active elements
- **Success**: Green (`#81c784`) for completed states
- **Warning**: Yellow (`#ffd54f`) for warnings
- **Error**: Red (`#e57373`) for errors
- **Text**: Light gray (`#e0e0e0`) for readable text
- **Borders**: Gray (`#757575`) for UI structure

### Agent Colors

Each agent gets a distinct color for message identification:

**Predefined Colors** (first 6 agents):
1. Orange `#ff7043`
2. Blue `#64b5f6`
3. Green `#81c784`
4. Purple `#ba68c8`
5. Teal `#4db6ac`
6. Pink `#f06292`

**Generated Colors** (agents 7+):
- Golden ratio-based color generation
- Ensures visual distinction between agents
- Supports 50+ agents without repetition

### Message Format

```
┌─────────────────────────────────────────────────────────┐
│ Assistant [agent-name] • 10:30 AM • $0.0045            │
│ ──────────────────────────────────────────────────────  │
│ Here's the analysis you requested...                    │
│                                                          │
│ ▸ Used Tool: query_database                            │
│   Status: ✓ Success • 245ms                            │
└─────────────────────────────────────────────────────────┘
```

## Status Indicators

### Tool Execution

- **Pending**: Gray spinner icon
- **Success**: Green checkmark `✓`
- **Error**: Red X `✗`
- **Duration**: Shown in milliseconds

### Streaming Progress

- **Stage names**: Pattern Selection, Schema Discovery, LLM Generation, Tool Execution
- **Animated spinner**: Indicates active processing
- **Percentage**: Shows completion for long-running stages

### Message Metadata

- **Timestamp**: Hour:minute format (e.g., "10:30 AM")
- **Cost**: Shown per message in dollars (e.g., "$0.0045")
- **Token counts**: Available in message details

## Model Switching

Change LLM model mid-conversation without losing context:

**Available Models** (17+):
- **Anthropic**: Claude Sonnet 4.5/3.5, Opus 3.5
- **OpenAI**: GPT-5, GPT-4o, GPT-4o-mini
- **Anthropic Bedrock**: Claude models via AWS
- **Ollama** (local/free): Llama 3.1/3.2, Qwen 2.5, Gemma 2
- **Google**: Gemini 2.0 Flash, Gemini 1.5 Pro
- **Mistral**: Mistral Large, Mistral Small
- **HuggingFace**: Various open models

**Features:**
- Context preservation: Full conversation history maintained
- Cost transparency: Shows $/1M tokens for each model
- Provider diversity: Mix and match providers in same conversation

## Command Palette (ctrl+k or ctrl+p)

Quick access to all TUI actions:

**Available Commands:**
- Clear messages
- New session
- Change model
- Add MCP server
- View agent info
- Export conversation
- Toggle compact mode
- Open external editor

## Tips & Best Practices

### 1. Start with Guide

Don't remember agent names? Just launch `loom` and ask the Guide:
- "Show me all SQL-related agents"
- "I need help with data analysis"
- "What agents are available for Python code?"

### 2. Use Keyboard Shortcuts

Faster workflow with keyboard-first navigation:
- `ctrl+e` to quickly switch agents
- `ctrl+w` to browse workflows
- `tab` / `shift+tab` to move between chat and editor

### 3. Leverage Fuzzy Search

Agent/workflow modals support fuzzy matching:
- Type "sql opt" to find "sql-optimizer"
- Type "data ana" to find "data-analyzer"

### 4. Session Persistence is Automatic

No need to save or manage sessions manually:
- Switch between agents freely
- Each agent remembers your conversation
- Sessions persist across TUI restarts (stored server-side)

### 5. Let Weaver Auto-Switch

When creating agents with weaver, no manual action needed:
- Weaver creates agent → TUI switches automatically
- If it doesn't switch, wait a moment or use `ctrl+e` to select manually

### 6. Compact Mode for Small Terminals

Working on a laptop or SSH session?
- TUI automatically switches to compact mode
- Sidebar hidden, more space for conversation
- All keyboard shortcuts still work

### 7. Use Patterns for Complex Tasks

Patterns provide domain expertise to agents:
- View available patterns in sidebar
- Click to see pattern content
- Agents automatically select relevant patterns based on your query

## Troubleshooting

### "No Server Running" Splash Screen

**Problem**: TUI shows "No Server Running" message.

**Solution**:
```bash
# In another terminal, start the server:
looms serve

# TUI will auto-connect within 2 seconds
```

### Agent Not Found After Creation

**Problem**: Weaver creates an agent, but auto-switch doesn't happen.

**Solution**: Wait a few seconds for hot-reload, or manually select with `ctrl+e`.

**Why**: File system watcher may have a delay. Auto-switch retries 3 times (500ms total).

### Keyboard Shortcuts Not Working

**Problem**: `ctrl+e`, `ctrl+w`, or other shortcuts don't respond.

**Possible Causes:**
1. Terminal doesn't support keyboard enhancements (older terminals)
2. Focus is in a modal dialog (shortcuts disabled)
3. Agent is busy streaming (some shortcuts disabled during processing)

**Solution**:
- Update terminal emulator
- Close any open modals first
- Wait for agent to finish streaming

### Modal Search Not Finding Agent

**Problem**: Fuzzy search in `ctrl+e` modal doesn't show expected agent.

**Check:**
1. Agent file exists in `$LOOM_DATA_DIR/agents/`
2. Agent YAML is valid (check server logs)
3. Agent is not a workflow coordinator (use `ctrl+w` instead)
4. File watcher has detected the agent (restart server if needed)

### Sidebar Navigation Stuck

**Problem**: Can't navigate past certain sidebar section.

**Solution**: Use mouse to click desired section, or press `ctrl+e` for direct agent selection.

## Advanced Usage

### Multiple Weaver Sessions

You can have multiple conversations with different agents simultaneously:

```bash
# Terminal 1: Work with weaver
loom --thread weaver

# Terminal 2: Use created agent
loom --thread sql-optimizer

# Terminal 3: Guide for discovery
loom
```

Each TUI instance maintains its own session.

### Custom Agent Workflows

Create specialized agents via weaver, then use workflows to orchestrate them:

1. Create agents: "Create a schema analyzer" → "Create a query optimizer"
2. Create workflow: "Create a workflow that uses schema-analyzer and query-optimizer"
3. Select workflow: `ctrl+w` → Select your new workflow

### Pattern Development Workflow

1. Switch to weaver: Click "Weaver" in sidebar
2. Create pattern-aware agent: "Create an agent that uses SQL optimization patterns"
3. View patterns: Expand "Patterns" in sidebar
4. Test agent: Switch to new agent, verify pattern selection in responses

## Keyboard Shortcut Reference Card

Print this for quick reference:

```
┌─────────────────────────────────────────────────────────────┐
│                    LOOM TUI SHORTCUTS                       │
├─────────────────────────────────────────────────────────────┤
│ GLOBAL                                                      │
│  ctrl+e       Agents       │  ctrl+w       Workflows        │
│  ctrl+k       Commands     │  ctrl+o       Sessions         │
│  ctrl+g       Help         │  ctrl+c       Quit             │
├─────────────────────────────────────────────────────────────┤
│ CHAT                                                        │
│  tab          Focus Editor │  shift+tab    Focus Chat      │
│  ctrl+u       Page Up      │  ctrl+d       Page Down        │
│  ctrl+l       Clear        │  ctrl+n       New Session     │
├─────────────────────────────────────────────────────────────┤
│ EDITOR                                                      │
│  enter        Send         │  shift+enter  New Line         │
│  ctrl+f       Add Image    │  @            Mention File     │
│  /            Commands     │  ctrl+o       External Editor  │
└─────────────────────────────────────────────────────────────┘
```

---

## Related Documentation

- [Pattern Library Guide](./pattern-library-guide.md) - Learn about pattern-guided learning
- [Zero-Code Implementation Guide](./zero-code-implementation-guide.md) - Create agents without coding
- [Architecture](../architecture/ARCHITECTURE.md) - Understand Loom's design

## Feedback

Found an issue or have a suggestion for the TUI? Please report it at:
https://github.com/teradata-labs/loom/issues
