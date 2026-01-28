# Docker Backend Examples

This directory contains examples of using the Docker backend for isolated, secure execution.

## Overview

The Docker backend enables:
- **Isolated execution**: Python, Node.js, or custom runtimes in containers
- **MCP server support**: Run MCP servers inside containers
- **Resource limits**: CPU, memory, execution time constraints
- **Security**: Non-root users, sandboxed execution
- **Observability**: Trace propagation (Phase 3)

## Examples

### 1. Teradata MCP Server (`teradata-mcp.yaml`)

Run a Teradata MCP server inside a Docker container for secure, isolated database access.

**Features:**
- Python 3.11 runtime
- Pre-installed `teradatasql` and `vantage-mcp-server`
- Resource limits (2 CPU cores, 2GB memory)
- Container rotation (daily or after 10,000 executions)
- Health checks every 60 seconds

**Usage:**
```bash
# Start MCP server
looms mcp start teradata --config examples/docker/teradata-mcp.yaml

# List available tools
looms mcp tools teradata

# Invoke query tool
looms mcp call teradata query '{"sql": "SELECT * FROM DBC.Tables LIMIT 10"}'

# Check health
looms mcp health teradata

# Stop server
looms mcp stop teradata
```

**Programmatic Usage (Go):**
```go
import (
    "github.com/teradata-labs/loom/pkg/docker"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Create MCP manager
manager, err := docker.NewMCPServerManager(docker.MCPManagerConfig{
    Executor: executor,
    Logger:   logger,
})

// Start Teradata MCP server
mcpConfig := &loomv1.MCPServerConfig{
    Enabled:   true,
    Transport: "stdio",
    Command:   "python3",
    Args:      []string{"-m", "vantage_mcp.server"},
    Env: map[string]string{
        "TD_HOST": "vantage.teradata.com",
        "TD_USER": "dbc",
    },
}

dockerConfig := &loomv1.DockerBackendConfig{
    RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
    BaseImage:   "python:3.11-slim",
    Python: &loomv1.PythonRuntimeConfig{
        PreinstallPackages: []string{"teradatasql", "vantage-mcp-server"},
    },
}

err = manager.StartMCPServer(ctx, "teradata", mcpConfig,
    loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)

// Invoke tool
result, err := manager.InvokeTool(ctx, "teradata", "query", map[string]interface{}{
    "sql": "SELECT * FROM DBC.Tables LIMIT 10",
})
```

## Security Best Practices

### 1. Credential Management
❌ **Never hardcode credentials in YAML:**
```yaml
env:
  TD_USER: dbc
  TD_PASSWORD: mysecretpassword  # DON'T DO THIS!
```

✅ **Use environment variables or secret management:**
```bash
# Option 1: Environment variables
export TD_PASSWORD=mysecretpassword
looms mcp start teradata --config teradata-mcp.yaml

# Option 2: Loom keyring
looms config set-key td_password
# Prompts securely for password, stores in OS keyring

# Option 3: Secret management
# Use Vault, AWS Secrets Manager, etc.
```

### 2. Resource Limits
Always set resource limits to prevent DoS:
```yaml
resource_limits:
  cpu_cores: 2.0          # Max CPU
  memory_mb: 2048         # Max memory
  execution_timeout_seconds: 300  # Max execution time
```

### 3. Non-Root User
Run containers as non-root user:
```yaml
build_config:
  run_commands:
    - useradd -m -u 1000 mcpuser
  # Container runs as UID 1000 (automatic in loom)
```

### 4. Container Rotation
Regularly rotate containers to prevent state accumulation:
```yaml
lifecycle:
  rotation_interval_hours: 24    # Rotate daily
  max_executions: 10000          # Or after 10k executions
```

### 5. Audit Logging
All MCP operations are logged to Hawk (when observability is enabled):
- Tool invocations
- Query execution
- Errors and failures
- Resource usage

## Configuration Options

### Runtime Types

**Python:**
```yaml
runtime_type: RUNTIME_TYPE_PYTHON
python:
  python_version: "3.11"
  preinstall_packages:
    - teradatasql
    - pandas
```

**Node.js:**
```yaml
runtime_type: RUNTIME_TYPE_NODE
node:
  node_version: "20"
  preinstall_packages:
    - express
    - pg
```

**Custom:**
```yaml
runtime_type: RUNTIME_TYPE_CUSTOM
dockerfile_path: ./Dockerfile.custom
```

### MCP Server Configuration

```yaml
mcp_server:
  enabled: true
  transport: stdio        # Only stdio supported for Docker
  command: python3
  args:
    - -m
    - vantage_mcp.server
  timeout_seconds: 30
  env:
    TD_HOST: vantage.teradata.com
    TD_USER: dbc
  tools:
    # Option 1: Enable all tools
    all: true

    # Option 2: Include specific tools
    include:
      - query
      - schema

    # Option 3: Exclude specific tools
    exclude:
      - dangerous_tool
```

## Monitoring and Debugging

### Health Checks
```bash
# Check MCP server health
looms mcp health teradata

# Or programmatically
err := manager.HealthCheck(ctx, "teradata")
if err != nil {
    log.Printf("Server unhealthy: %v", err)
}
```

### Container Inspection
```bash
# Get server info
looms mcp info teradata

# Programmatically
info, err := manager.GetServerInfo("teradata")
fmt.Printf("Container: %s\n", info.ContainerID)
fmt.Printf("Healthy: %v\n", info.Healthy)
fmt.Printf("Created: %v\n", info.CreatedAt)
fmt.Printf("Restarts: %d\n", info.RestartCount)
```

### Logs
```bash
# View container logs
docker logs <container_id>

# Follow logs in real-time
docker logs -f <container_id>
```

### Traces (Phase 3 - Coming Soon)
```bash
# View traces in Hawk
hawk traces --trace-id=<trace_id>

# All MCP tool invocations will appear with:
# - Parent span: agent.conversation
# - Child span: docker.mcp.invoke
# - Grandchild spans: mcp.tool.query, teradata.execute_query
```

## Troubleshooting

### MCP Server Won't Start

**Problem:** Server fails to initialize
```
Error: failed to initialize MCP server: context deadline exceeded
```

**Solutions:**
1. Increase timeout: `timeout_seconds: 60`
2. Check base image exists: `docker pull python:3.11-slim`
3. Check network connectivity if pulling images
4. Check Docker daemon is running: `docker ps`

### Container Creation Fails

**Problem:** Cannot create container
```
Error: failed to create MCP server container: image not found
```

**Solutions:**
1. Pull base image manually: `docker pull python:3.11-slim`
2. Check Dockerfile path if using custom image
3. Verify preinstall_packages are valid PyPI packages

### Tool Invocation Fails

**Problem:** Tool returns error
```
Error: tool error: connection refused
```

**Solutions:**
1. Check credentials in env vars
2. Verify database host is reachable from container
3. Check resource limits aren't too restrictive
4. Review container logs: `docker logs <container_id>`

### Health Check Fails

**Problem:** Server marked unhealthy
```
Server unhealthy: ping timeout
```

**Solutions:**
1. Check server is still running: `docker ps`
2. Increase health_check_interval_seconds
3. Check container resource usage: `docker stats <container_id>`
4. Review container logs for errors

## Future Enhancements (Phase 3)

### Trace Propagation
- All MCP tool invocations traced to Hawk
- Parent-child span relationships
- Query execution visibility
- Performance profiling

### Multi-Tenancy
- Tenant-isolated containers
- Tenant-specific resource quotas
- Tenant audit trails

### Distributed Scheduling
- Multi-node container orchestration
- Load balancing across nodes
- Cost-aware scheduling

## Additional Examples

More examples coming soon:
- `postgres-mcp.yaml` - PostgreSQL MCP server
- `github-mcp.yaml` - GitHub MCP server
- `filesystem-mcp.yaml` - Filesystem MCP server
- `custom-tool-mcp.yaml` - Custom tool development

## Related Documentation

- [Docker Backend Architecture](../../website/content/en/docs/concepts/docker-backend.md)
- [MCP Integration Guide](../../website/content/en/docs/guides/integration/mcp.md)
- [Security Best Practices](../../website/content/en/docs/guides/security.md)
- [Observability Guide](../../website/content/en/docs/guides/integration/observability.md)
