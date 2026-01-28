
# MCP Examples

**Version**: v1.0.0-beta.1

## Table of Contents

- [Basic Agent with MCP](#basic-agent-with-mcp)
- [Multi-Server Setup](#multi-server-setup)
- [Custom MCP Server](#custom-mcp-server)
- [Agent with Mixed Tools](#agent-with-mixed-tools)


## Basic Agent with MCP

Minimal example using filesystem MCP server:

```go
package main

import (
    "context"
    "log"

    "github.com/teradata-labs/loom/pkg/agent"
    "github.com/teradata-labs/loom/pkg/llm"
    "github.com/teradata-labs/loom/pkg/mcp/manager"
    "go.uber.org/zap"
)

func main() {
    ctx := context.Background()
    logger, _ := zap.NewDevelopment()

    // Create MCP manager
    mcpCfg := &manager.Config{
        Servers: map[string]manager.ServerConfig{
            "filesystem": {
                Command:    "npx",
                Args:       []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
                Enabled:    true,
                Transport:  "stdio",
                ToolFilter: manager.ToolFilter{All: true},
            },
        },
    }
    mcpMgr := manager.NewManager(mcpCfg, logger)

    // Start MCP servers
    if err := mcpMgr.Start(ctx); err != nil {
        log.Fatalf("Failed to start MCP: %v", err)
    }
    defer mcpMgr.Stop()

    // Create LLM provider
    llmProvider, _ := llm.NewAnthropicProvider(llm.AnthropicConfig{
        APIKey: os.Getenv("ANTHROPIC_API_KEY"),
        Model:  "claude-sonnet-4-5-20250929",
    })

    // Create agent
    ag := agent.NewAgent(nil, llmProvider)

    // Register MCP tools
    if err := ag.RegisterMCPServer(ctx, mcpMgr, "filesystem"); err != nil {
        log.Fatalf("Failed to register MCP tools: %v", err)
    }

    // Use agent
    response, _ := ag.Chat(ctx, "session-1", "List files in /data")
    log.Printf("Response: %s", response)
}
```


## Multi-Server Setup

Using multiple MCP servers together:

```go
mcpCfg := &manager.Config{
    Servers: map[string]manager.ServerConfig{
        "filesystem": {
            Command:    "npx",
            Args:       []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
            Enabled:    true,
            Transport:  "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
        "github": {
            Command:    "npx",
            Args:       []string{"-y", "@modelcontextprotocol/server-github"},
            Env:        map[string]string{"GITHUB_TOKEN": os.Getenv("GITHUB_TOKEN")},
            Enabled:    true,
            Transport:  "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
        "postgres": {
            Command:    "npx",
            Args:       []string{"-y", "@modelcontextprotocol/server-postgres"},
            Env:        map[string]string{"DATABASE_URL": os.Getenv("DATABASE_URL")},
            Enabled:    true,
            Transport:  "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
    },
}

mcpMgr := manager.NewManager(mcpCfg, logger)
mcpMgr.Start(ctx)

// Register all servers with agent
for serverName := range mcpCfg.Servers {
    ag.RegisterMCPServer(ctx, mcpMgr, serverName)
}

// Agent now has tools from all three servers
response, _ := ag.Chat(ctx, "session-1", `
    Read the config.yaml file,
    then create a GitHub issue with its contents,
    and log the issue URL to the database
`)
```


## Custom MCP Server

Create your own MCP server for custom tools:

```go
// cmd/my-mcp-server/main.go
package main

import (
    "context"
    "os"

    "github.com/teradata-labs/loom/pkg/mcp/server"
)

func main() {
    srv := server.New(server.Config{
        Name:    "my-custom-server",
        Version: "1.0.0",
    })

    // Register tools
    srv.RegisterTool(server.Tool{
        Name:        "calculate_metrics",
        Description: "Calculate custom business metrics",
        InputSchema: map[string]interface{}{
            "type": "object",
            "properties": map[string]interface{}{
                "metric_name": map[string]interface{}{"type": "string"},
                "start_date":  map[string]interface{}{"type": "string"},
                "end_date":    map[string]interface{}{"type": "string"},
            },
            "required": []string{"metric_name"},
        },
        Handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
            metricName := args["metric_name"].(string)
            // Your custom logic here
            return map[string]interface{}{
                "metric":  metricName,
                "value":   42.5,
                "status":  "success",
            }, nil
        },
    })

    // Start server (stdio transport)
    srv.ServeStdio(os.Stdin, os.Stdout)
}
```

Use your custom server:

```yaml
mcp:
  servers:
    custom:
      command: /path/to/my-mcp-server
      transport: stdio
```


## Agent with Mixed Tools

Combine custom tools with MCP tools:

```go
// Create agent with custom backend
backend := NewTeradataBackend(connectionString)
ag := agent.NewAgent(backend, llmProvider)

// Register custom tools (your proprietary logic)
ag.RegisterTool(NewOptimizeSQLTool(backend))
ag.RegisterTool(NewAnalyzeQueryTool(backend))

// Register MCP tools (commodity features)
ag.RegisterMCPServer(ctx, mcpMgr, "filesystem")
ag.RegisterMCPServer(ctx, mcpMgr, "github")

// Agent has both:
// - Your custom SQL optimization tools
// - File I/O from filesystem MCP
// - GitHub integration from GitHub MCP

response, _ := ag.Chat(ctx, "session-1", `
    Analyze the slow queries from query_log.sql,
    optimize them,
    and create a GitHub issue with the recommendations
`)
```


## YAML Configuration

Equivalent YAML configuration:

```yaml
# $LOOM_DATA_DIR/looms.yaml
mcp:
  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      transport: stdio

    github:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}
      transport: stdio

    custom:
      command: /path/to/my-mcp-server
      transport: stdio
```

```yaml
# $LOOM_DATA_DIR/agents/my-agent.yaml
tools:
  mcp:
    - server: "filesystem"
      tools: ["*"]
    - server: "github"
      tools: ["create_issue", "list_repos"]
    - server: "custom"
      tools: ["*"]
```
