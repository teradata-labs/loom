# Server Configuration Examples

This directory contains example server configurations for Loom server (looms).

## Configuration Files

### looms.yaml - Full Multi-Agent Server

Complete production-style server configuration demonstrating:
- Multiple agent configurations
- Backend integrations (Teradata, PostgreSQL, etc.)
- MCP server configurations
- Pattern libraries
- Observability settings
- Security configurations

**Usage:**
```bash
cd examples
looms serve --config config/looms.yaml
```

**Use case:** Reference implementation for production deployments

---

### looms-tls-dev.yaml - TLS Development Configuration

Server configuration with TLS enabled for development/testing.

**Features:**
- TLS certificate configuration
- Development certificate paths
- Secure communication testing

**Usage:**
```bash
looms serve --config config/looms-tls-dev.yaml
```

**Use case:** Testing secure connections in development

---

### looms-tls-manual.yaml - Manual TLS Configuration

Server configuration with manual TLS certificate management.

**Features:**
- Custom certificate paths
- Manual certificate rotation
- Production-like TLS setup

**Usage:**
```bash
looms serve --config config/looms-tls-manual.yaml
```

**Use case:** Production TLS setup with custom certificates

---

### looms-production-cors.yaml - Production CORS Configuration

Production-ready CORS (Cross-Origin Resource Sharing) configuration.

**Features:**
- Secure CORS settings for production
- Allowed origins whitelist (no wildcards)
- Restricted HTTP methods and headers
- Credential support configuration
- Preflight caching

**Usage:**
```bash
looms serve --config config/looms-production-cors.yaml
```

**Use case:** Production deployment with web frontend requiring CORS

---

## Configuration Structure

All server configurations follow this structure:

```yaml
server:
  port: 9090              # gRPC server port
  host: 0.0.0.0           # Bind address
  enable_reflection: true # gRPC reflection for tools

llm:
  provider: anthropic     # anthropic|bedrock|ollama
  anthropic_model: claude-sonnet-4-5-20250929
  temperature: 1.0
  max_tokens: 4096

database:
  path: $LOOM_DATA_DIR/loom.db   # SQLite database for sessions
  driver: sqlite

observability:
  enabled: true
  hawk:
    endpoint: http://localhost:9099

logging:
  level: info
  format: json

agents:
  agents:
    agent-name:
      name: Agent Name
      backend_path: ./backends/backend.yaml
      # ... agent configuration

mcp:
  servers:
    - name: server-name
      command: server-binary
      args: [serve, --mode=stdio]
```

## Environment-Specific Configurations

Create environment-specific configurations by copying and modifying these examples:

```bash
# Development
cp config/looms.yaml $LOOM_DATA_DIR/looms-dev.yaml
# Edit for local development settings

# Staging
cp config/looms.yaml $LOOM_DATA_DIR/looms-staging.yaml
# Edit for staging environment

# Production
cp config/looms.yaml $LOOM_DATA_DIR/looms-prod.yaml
# Edit for production settings (observability, TLS, etc.)
```

## Configuration Best Practices

### Security
- **Use TLS in production**: See `looms-tls-manual.yaml`
- **Store secrets in environment variables**: Don't hardcode API keys
- **Restrict server binding**: Use `127.0.0.1` for local-only access
- **Enable authentication**: Configure API key authentication for production

### Observability
- **Enable Hawk tracing**: Essential for debugging multi-agent interactions
- **Use JSON logging**: Easier parsing and analysis
- **Set appropriate log levels**: `info` for production, `debug` for development
- **Monitor database size**: SQLite sessions can grow large

### Performance
- **Adjust connection pools**: Based on expected load
- **Configure timeouts**: Prevent long-running operations from blocking
- **Enable compression**: Reduce network overhead
- **Optimize database**: Regular VACUUM for SQLite

### Agent Configuration
- **Use backend paths**: Reference external backend configs for reusability
- **Enable tracing per agent**: Debug individual agent behavior
- **Set appropriate max_turns**: Prevent infinite loops
- **Configure memory compression**: Balance memory vs quality

## Testing Configuration

To test a configuration without starting the server:

```bash
# Dry run - checks configuration validity
looms serve --config config/looms.yaml --dry-run

# Validate configuration structure
looms validate file config/looms.yaml
```

**Note:** `looms validate` checks YAML structure for Project, Backend, PatternLibrary, and EvalSuite kinds. Full server configurations are validated during startup.

## Related Documentation

- **Test configurations**: See `tests/config/` for minimal test configs
- **Agent configurations**: See `reference/agents/` for agent examples
- **Backend configurations**: See `../backends/` for backend examples
- **Pattern libraries**: See `../patterns/` for pattern examples

## Troubleshooting

### Configuration Loading Errors

**Error**: `failed to load config file`
- Check file path is correct
- Verify YAML syntax is valid
- Ensure file permissions allow reading

**Error**: `unsupported field in config`
- Update to latest Loom version
- Check field names match documentation
- Remove deprecated configuration fields

**Error**: `failed to connect to database`
- Verify database path exists and is writable
- Check database driver is correct (sqlite|postgres)
- Ensure database file has correct permissions

### Agent Loading Errors

**Error**: `agent backend not found`
- Check backend_path points to existing file
- Verify backend configuration is valid
- Ensure backend file has correct permissions

**Error**: `MCP server failed to start`
- Verify MCP binary is in PATH
- Check MCP server arguments are correct
- Review MCP server logs for errors

## See Also

- [Examples README](../README.md) - Overview of all examples
- [Agent Configuration Reference](../reference/agents/agent-all-fields-reference.yaml) - Complete agent YAML spec
- [Backend Examples](../backends/) - Backend configuration examples
