
# MCP Examples

**Version**: v1.2.0 | **Status**: ✅ Implemented

## Table of Contents

- [Basic Agent with MCP](#basic-agent-with-mcp)
- [Multi-Server Setup](#multi-server-setup)
- [Custom MCP Server](#custom-mcp-server)
- [Agent with Mixed Tools](#agent-with-mixed-tools)


## Basic Agent with MCP

✅ Minimal example using filesystem MCP server:

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/teradata-labs/loom/pkg/agent"
    "github.com/teradata-labs/loom/pkg/llm/anthropic"
    "github.com/teradata-labs/loom/pkg/mcp/manager"
    "go.uber.org/zap"
)

func main() {
    ctx := context.Background()
    logger, _ := zap.NewDevelopment()

    // Create MCP manager
    mcpCfg := manager.Config{
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
    mcpMgr, err := manager.NewManager(mcpCfg, logger)
    if err != nil {
        log.Fatalf("Invalid MCP config: %v", err)
    }

    // Start MCP servers
    if err := mcpMgr.Start(ctx); err != nil {
        log.Fatalf("Failed to start MCP: %v", err)
    }
    defer mcpMgr.Stop()

    // Create LLM provider
    llmProvider := anthropic.NewClient(anthropic.Config{
        APIKey: os.Getenv("ANTHROPIC_API_KEY"),
        Model:  "claude-sonnet-4-5-20250929",
    })

    // Create agent
    ag := agent.NewAgent(nil, llmProvider)

    // Register MCP tools
    if err := ag.RegisterMCPServer(ctx, mcpMgr, "filesystem"); err != nil {
        log.Fatalf("Failed to register MCP tools: %v", err)
    }

    // Use agent — Chat returns (*agent.Response, error)
    response, err := ag.Chat(ctx, "session-1", "List files in /data")
    if err != nil {
        log.Fatalf("Chat failed: %v", err)
    }
    log.Printf("Response: %s", response.Content)
}
```


## Multi-Server Setup

✅ Using multiple MCP servers together:

```go
mcpCfg := manager.Config{
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

mcpMgr, err := manager.NewManager(mcpCfg, logger)
if err != nil {
    log.Fatalf("Invalid config: %v", err)
}
if err := mcpMgr.Start(ctx); err != nil {
    log.Printf("Some servers failed: %v", err)
}

// Register all servers with agent
for serverName := range mcpCfg.Servers {
    ag.RegisterMCPServer(ctx, mcpMgr, serverName)
}

// Agent now has tools from all three servers
// Chat returns (*agent.Response, error)
response, _ := ag.Chat(ctx, "session-1", `
    Read the config.yaml file,
    then create a GitHub issue with its contents,
    and log the issue URL to the database
`)
log.Printf("Response: %s", response.Content)
```


## Custom MCP Server

✅ Create your own MCP server for custom tools:

```go
// cmd/my-mcp-server/main.go
package main

import (
    "context"
    "os"

    "github.com/teradata-labs/loom/pkg/mcp/protocol"
    "github.com/teradata-labs/loom/pkg/mcp/server"
    "github.com/teradata-labs/loom/pkg/mcp/transport"
    "go.uber.org/zap"
)

// metricsProvider implements server.ToolProvider
type metricsProvider struct{}

func (p *metricsProvider) ListTools(ctx context.Context) ([]protocol.Tool, error) {
    return []protocol.Tool{
        {
            Name:        "calculate_metrics",
            Description: "Calculate custom business metrics",
            // InputSchema is map[string]interface{} (JSON Schema)
            InputSchema: map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "metric_name": map[string]interface{}{"type": "string", "description": "Name of the metric"},
                    "start_date":  map[string]interface{}{"type": "string", "description": "Start date"},
                    "end_date":    map[string]interface{}{"type": "string", "description": "End date"},
                },
                "required": []string{"metric_name"},
            },
        },
    }, nil
}

func (p *metricsProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
    metricName := args["metric_name"].(string)
    // Your custom logic here
    return &protocol.CallToolResult{
        Content: []protocol.Content{
            {Type: "text", Text: metricName + ": 42.5"},
        },
    }, nil
}

func main() {
    logger, _ := zap.NewDevelopment()

    // Create server with a ToolProvider option
    srv := server.NewMCPServer("my-custom-server", "1.0.0", logger,
        server.WithToolProvider(&metricsProvider{}),
    )

    // Create stdio transport and serve
    t := transport.NewStdioServerTransport(os.Stdin, os.Stdout)
    if err := srv.Serve(context.Background(), t); err != nil {
        logger.Fatal("server error", zap.Error(err))
    }
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

✅ Combine custom tools with MCP tools:

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

// Chat returns (*agent.Response, error)
response, _ := ag.Chat(ctx, "session-1", `
    Analyze the slow queries from query_log.sql,
    optimize them,
    and create a GitHub issue with the recommendations
`)
log.Printf("Response: %s", response.Content)
```


## YAML Configuration

✅ Equivalent YAML configuration:

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
