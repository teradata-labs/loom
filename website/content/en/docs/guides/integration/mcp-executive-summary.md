---
title: "MCP Concepts"
weight: 2
---

# MCP Concepts

**Version**: v1.0.0-beta.1

## Table of Contents

- [What is MCP](#what-is-mcp)
- [MCP vs Custom Tools](#mcp-vs-custom-tools)
- [Usage Modes](#usage-modes)
- [When to Use MCP](#when-to-use-mcp)
- [Available MCP Servers](#available-mcp-servers)

---

## What is MCP

MCP (Model Context Protocol) is a standard protocol for connecting AI agents to external tools and data sources.

**Without MCP:**
- Write custom code for every integration
- Maintain all tool implementations yourself
- Tools fixed at compile time

**With MCP:**
- Connect to 100+ existing MCP servers
- Community-maintained tool ecosystem
- Dynamic tool loading at runtime

---

## MCP vs Custom Tools

| Aspect | Custom Tools | MCP Tools |
|--------|--------------|-----------|
| Development | Write Go code | Install existing server |
| Control | Full control | Standard protocol |
| Performance | Direct calls | JSON-RPC overhead |
| Ecosystem | Build everything | 100+ servers available |
| Best for | Proprietary logic | Standard integrations |

---

## Usage Modes

### Mode 1: Pure Custom (No MCP)

```go
agent := agent.NewAgent(myBackend, llm)
agent.RegisterTool(myCustomTool)
// Works exactly as before - no MCP code runs
```

**Use when:**
- Proprietary systems
- Security-critical operations
- Maximum control needed

### Mode 2: Pure MCP

```yaml
mcp:
  servers:
    filesystem: {command: npx, args: [...]}
    github: {command: npx, args: [...]}
```

```go
agent := agent.NewAgent(backend, llm)
agent.RegisterMCPTools(ctx, mcpMgr)
// All tools from MCP servers
```

**Use when:**
- Rapid prototyping
- Standard use cases
- Minimize custom code

### Mode 3: Hybrid (Recommended)

```go
agent := agent.NewAgent(teradataBackend, llm)

// YOUR proprietary tools
agent.RegisterTool(NewOptimizeSQLTool(backend))

// MCP for commodity features
agent.RegisterMCPTools(ctx, mcpMgr)
// Adds: filesystem, GitHub, etc.
```

**Use when:**
- Production systems
- Mix proprietary + standard tools
- Best performance + development speed

---

## When to Use MCP

### Use Custom Tools When

- Proprietary business logic
- Competitive advantage code
- Security-critical operations
- Performance-critical paths
- Complex domain requirements

### Use MCP When

- Standard operations (file I/O, databases)
- Third-party integrations (GitHub, Slack)
- Rapid prototyping
- Well-solved problems
- Community-maintained preferred

---

## Available MCP Servers

Common MCP servers you can use:

| Server | Package | What It Does |
|--------|---------|--------------|
| Filesystem | `@modelcontextprotocol/server-filesystem` | Read/write files |
| GitHub | `@modelcontextprotocol/server-github` | GitHub API access |
| Postgres | `@modelcontextprotocol/server-postgres` | PostgreSQL queries |
| Slack | `@modelcontextprotocol/server-slack` | Slack messaging |
| Memory | `@modelcontextprotocol/server-memory` | Key-value storage |

See [MCP Server Registry](https://github.com/modelcontextprotocol/servers) for the full list.

---

## Key Points

1. **MCP is optional** - Existing Loom code works unchanged
2. **MCP is additive** - Adds capabilities, doesn't replace existing tools
3. **Mix and match** - Use custom tools alongside MCP tools
4. **Standard protocol** - Works with any MCP-compatible server
