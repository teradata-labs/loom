
# MCP Troubleshooting

**Version**: v1.2.0
**Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Diagnostic Steps](#diagnostic-steps)
  - [Step 1: Verify MCP Server Starts](#step-1-verify-mcp-server-starts)
  - [Step 2: Check Tool Registration](#step-2-check-tool-registration)
  - [Step 3: Verify FTS5 Support](#step-3-verify-fts5-support)
- [Common Issues](#common-issues)
  - [Agent Has 0 Tools](#agent-has-0-tools)
  - [MCP Server Not Enabled](#mcp-server-not-enabled)
  - [macOS Kills Copied Binaries](#macos-kills-copied-binaries)
  - [FTS5 Module Missing](#fts5-module-missing)
- [Configuration Examples](#configuration-examples)
- [Getting Help](#getting-help)


## Overview

Debug MCP tool registration issues when agents report 0 tools despite MCP servers being available.


## Diagnostic Steps

### Step 1: Verify MCP Server Starts

```bash
looms serve > /tmp/looms.log 2>&1 &
grep "MCP server started" /tmp/looms.log
```

**Expected output** (structured zap log with fields from logger context):
```
MCP server started: command="/path/to/vantage-mcp", args=[], pid=12345, env_vars=["TD_USER","TD_DEFAULT_HOST"], server="vantage"
```

**If missing:**
- Check `$LOOM_DATA_DIR/looms.yaml` has `mcp.servers` configured
- Verify MCP binary exists: `ls /path/to/vantage-mcp`
- Check permissions: `chmod +x /path/to/vantage-mcp`

### Step 2: Check Tool Registration

```bash
grep -E "tools_added|MCP server registered|Agent loaded" /tmp/looms.log
```

**Expected output:**
```
MCP server registered: server="vantage", tools="all", tools_added=17, total_tools=17
Agent loaded successfully: name="my-agent", tool_count=17
```

**If tools_added=0:**
- ToolFilter.All may be false (see "Agent Has 0 Tools" below)
- Check agent YAML has `tools.mcp` section

### Step 3: Verify FTS5 Support

```bash
strings bin/looms | grep -i fts5
```

If no output, rebuild with FTS5:
```bash
go build -tags fts5 -o bin/looms ./cmd/looms
```


## Common Issues

### Agent Has 0 Tools

**Symptom:**
```
MCP server registered: server="vantage", tools="all", tools_added=17, total_tools=17
Agent loaded successfully: name="my-agent", tool_count=0
```

**Causes:**

1. **ToolFilter.All=false** - Default in `manager.ToolFilter` rejects all tools (YAML config auto-sets `All: true`; this mainly affects programmatic/gRPC usage)
2. **Missing MCP tool registration** - Agent YAML missing `tools.mcp` section
3. **MCP server disabled** - `enabled: false` explicitly set in config

**Solution 1: Set ToolFilter.All=true**

When creating ServerConfig programmatically:
```go
mcpConfig.Servers[serverName] = manager.ServerConfig{
    Command:   serverConfig.Command,
    Transport: "stdio",
    Enabled:   true,
    ToolFilter: manager.ToolFilter{
        All: true,  // Required!
    },
}
```

**Solution 2: Add MCP tools to agent config**

```yaml
# $LOOM_DATA_DIR/agents/my-agent.yaml (legacy format, under agent: key)
agent:
  # ...other fields...
  tools:
    mcp:
      - server: "vantage"
        tools: ["*"]  # Or empty list [] to register all tools
```

### MCP Server Not Enabled

**Symptom:**
```
Skipping disabled server: server="vantage"
```

**Note:** This log is emitted at **debug** level. Set `logging.level: debug`
in looms.yaml to see it. Servers listed in `mcp.servers` default to
`enabled: true` unless explicitly set to `enabled: false`. This message means
the server was explicitly disabled.

**Solution:**

Remove `enabled: false` or set it to `true` in YAML config:
```yaml
mcp:
  servers:
    vantage:
      command: /path/to/mcp-server
      transport: stdio
      enabled: true  # Or simply remove the line (defaults to true)
```

### macOS Kills Copied Binaries

**Symptom:**
```
MCP server started: pid=12345
MCP server exited with error: signal: killed
Failed to initialize: write |1: broken pipe
```

**Cause:** macOS security features kill binaries installed via `cp`.

**Solution:** Use symlinks:
```bash
# Wrong
cp /path/to/vantage-mcp ~/.local/bin/vantage-mcp

# Correct
ln -s /path/to/vantage-mcp ~/.local/bin/vantage-mcp
```

**Verify:**
```bash
ls -la ~/.local/bin/vantage-mcp
# Should show: lrwxr-xr-x ... -> /path/to/source
```

### FTS5 Module Missing

**Error:**
```
failed to initialize schema: no such module: fts5
```

**Solution:** Rebuild with FTS5 tag:
```bash
go build -tags fts5 -o bin/looms ./cmd/looms
go build -tags fts5 -o bin/loom ./cmd/loom
```

Or use Justfile (already configured with fts5):
```bash
just build
```


## Configuration Examples

### Minimal Working Config

```yaml
# $LOOM_DATA_DIR/looms.yaml
server:
  host: "0.0.0.0"
  port: 60051  # Default gRPC port

llm:
  provider: anthropic

mcp:
  servers:
    vantage:
      command: ~/.local/bin/vantage-mcp
      transport: stdio
      # enabled defaults to true for servers listed in config
      env:
        TD_USER: your_username
        TD_DEFAULT_HOST: your_host
```

### Agent with All MCP Tools (Legacy Format)

```yaml
# $LOOM_DATA_DIR/agents/my-agent.yaml (legacy format with agent: top-level key)
agent:
  name: "my-agent"
  description: "Teradata SQL agent"

  llm:
    provider: "anthropic"
    model: "claude-sonnet-4-5-20250929"

  tools:
    mcp:
      - server: "vantage"
        tools: ["*"]  # All tools (empty list also registers all)

  behavior:
    max_iterations: 25
    timeout_seconds: 300
```

> Both legacy (`agent:`) and k8s-style (`apiVersion`/`kind`/`metadata`/`spec`)
> YAML formats are supported. See `examples/reference/agents/` for k8s-style examples.


## Getting Help

**Debugging checklist:**
1. Check logs: `looms serve > /tmp/looms.log 2>&1`
2. Verify FTS5: `strings bin/looms | grep -i fts5`
3. Test tool count: `grep "tool_count" /tmp/looms.log`
4. Validate MCP server: `vantage-mcp --version`

**Common mistakes:**
- Forgetting `-tags fts5` when building
- Not setting `ToolFilter.All=true` when adding MCP servers via gRPC API (YAML configs default to `All: true`)
- Using relative paths for MCP server commands
- Using `cp` to install binaries on macOS (use `ln -s`)
- Missing `tools.mcp` section in agent YAML (legacy format)
