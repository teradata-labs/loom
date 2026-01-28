# Tool Configuration Examples

This directory contains YAML configuration examples for registering custom tools with Loom agents.

## Tool Configuration Format

```yaml
apiVersion: loom/v1
kind: Tool
metadata:
  name: tool-name
  description: What this tool does
  version: "1.0"

spec:
  input_schema:
    type: object
    properties:
      param1:
        type: string
        description: Parameter description
    required:
      - param1

  implementation:
    type: mcp | http | grpc | python | shell
    config:
      # Implementation-specific configuration
```

## Available Examples

1. **calculator.yaml** - Simple math calculator tool
2. **web-search.yaml** - Web search via Tavily API
3. **code-executor.yaml** - Safe code execution sandbox
4. **database-query.yaml** - SQL query execution
5. **file-operations.yaml** - File system operations

## Usage

### Register with Agent

```yaml
# In agent configuration
spec:
  tools:
    custom:
      - config_file: examples/tools/calculator.yaml
      - config_file: examples/tools/web-search.yaml
```

### Programmatic Registration

```go
// Load tool config
toolConfig, err := loadToolConfig("config/tools/calculator.yaml")

// Register with agent
agent.RegisterTool(toolConfig.ToTool())
```

## See Also

- [Tool Development Guide](../../../website/content/en/docs/guides/tools.md)
- [MCP Tool Integration](../../../website/content/en/docs/guides/integration/mcp.md)
