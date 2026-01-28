# Server Configuration Tests

This directory contains test configurations and related test data for Loom server (looms) configuration validation.

## Files

### looms-test.yaml

Minimal test configuration for server configuration loading and validation tests.

**Purpose**: Validates that the looms server can correctly load and parse agent configurations from YAML.

**Features**:
- Minimal server configuration (port 9091, localhost)
- Single test agent (sqlite-agent)
- SQLite backend reference
- Observability disabled for testing
- Text logging for test output

**Used by**: `cmd/looms/config_test.go::TestLoadConfig_WithAgents`

**Usage**:
```bash
# Validate configuration
looms serve --config tests/config/looms-test.yaml --dry-run

# Run configuration tests
cd cmd/looms
go test -tags fts5 -run TestLoadConfig_WithAgents
```

## Configuration Structure

Test configurations should follow this pattern:

```yaml
server:
  port: 9091              # Use non-standard port to avoid conflicts
  host: 127.0.0.1
  enable_reflection: true

llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929

database:
  path: ./test-loom.db   # Use test-specific database path
  driver: sqlite

observability:
  enabled: false          # Disable for tests

logging:
  level: info
  format: text            # Use text format for test output

agents:
  agents:
    test-agent:           # Test-specific agent configuration
      name: Test Agent
      # ... agent config
```

## Adding New Test Configurations

When adding new server configuration tests:

1. Create a descriptive YAML file in this directory (e.g., `looms-test-mcp.yaml`)
2. Use test-specific values (non-standard ports, test database paths)
3. Disable observability and use text logging
4. Add corresponding test in `cmd/looms/*_test.go`
5. Document the file in this README

## Related Tests

Configuration tests are located in:
- `cmd/looms/config_test.go` - Basic configuration loading
- `cmd/looms/config_mcp_test.go` - MCP server configuration
- `cmd/looms/cmd_serve_test.go` - Server startup tests

## Test Database Files

Test configurations create temporary database files (e.g., `test-loom.db`). These are typically in `.gitignore` and should be cleaned up after tests.
