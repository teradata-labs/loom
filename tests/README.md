# Loom Tests Directory

This directory contains test data, test configurations, and integration tests for the Loom project.

## Directory Structure

```
tests/
├── config/           # Server configuration test files
│   ├── looms-test.yaml
│   └── README.md
└── workflows/        # Workflow validation tests and test data
    ├── test-data/
    ├── validate_test.go
    └── README.md
```

## Subdirectories

### config/

Server configuration files and test data for looms (Loom server) configuration validation.

**Contains**:
- `looms-test.yaml` - Minimal test configuration for config loading tests
- Test configurations with non-standard ports, test databases, disabled observability

**Tests using this data**: `cmd/looms/config_test.go`

See [config/README.md](config/README.md) for details.

### workflows/

Workflow validation tests and test workflow YAML files.

**Contains**:
- `test-data/` - Test workflow YAML files (valid and invalid)
- `validate_test.go` - Workflow validation tests
- Test agent configurations for workflow testing

**Tests**: Validates workflow YAML parsing, agent loading, and workflow structure

See [workflows/README.md](workflows/README.md) for details.

## Running Tests

### All Tests
```bash
# Run all tests with required tags
go test -tags fts5 ./...
```

### Configuration Tests
```bash
cd cmd/looms
go test -tags fts5 -run TestLoadConfig
```

### Workflow Tests
```bash
cd tests/workflows
go test -tags fts5 -v ./...
```

## Test Organization Principles

### Test Data Placement

**Test data belongs in `/tests` if**:
- Used exclusively by tests
- Not part of user-facing examples
- Contains test-specific values (test ports, test databases)
- Used across multiple test files

**Test data belongs in package directory if**:
- Testing specific package functionality
- Test data is simple or generated in-memory
- Package-specific test fixtures

**Examples belong in `/examples` if**:
- User-facing reference implementations
- Documentation examples
- Production-like configurations
- Tutorial or guide content

### File Naming

- Test configs: `<component>-test.yaml` (e.g., `looms-test.yaml`)
- Test data: Descriptive names indicating what's being tested
- Invalid test cases: Prefix with `invalid-` (e.g., `invalid-config-workflow.yaml`)

### Configuration Values for Tests

Test configurations should use:
- Non-standard ports (9091, 9092, not 9090)
- Test database paths (`./test-loom.db`, not `./loom.db`)
- Text logging format (not JSON)
- Disabled observability (faster tests)
- Localhost binding only (127.0.0.1)

## Adding New Test Data

When adding new test data or configurations:

1. **Choose correct subdirectory** based on what's being tested
2. **Create descriptive filename** indicating test purpose
3. **Add README section** documenting the new file
4. **Reference in test code** with clear comments
5. **Update .gitignore** for generated test artifacts

## CI/CD Integration

Tests in this directory are run by CI:
- Go tests: `go test -tags fts5 ./...`
- Validation: Workflow and config validation
- Integration: Full server startup tests

## Cleaning Test Artifacts

Test runs may create temporary files:
```bash
# Clean test databases
rm -f tests/config/*.db tests/**/*.db

# Clean test artifacts
find tests -name "*.log" -delete
find tests -name "*.tmp" -delete
```

**Note**: These files are in `.gitignore` and won't be committed.
