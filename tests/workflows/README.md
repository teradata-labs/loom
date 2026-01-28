# Workflow Validation Tests

This directory contains tests and test data for validating workflow configurations.

## Contents

- **validate_test.go** - Go tests for workflow validation
- **test-data/** - Test workflow YAML files for validation tests

## Test Data Files

The `test-data/` directory contains various workflow YAML files used for testing:

### Valid Workflows (Testing Different Features)
- **path-reference-workflow.yaml** - Tests agent loading from file paths
- **path-override-workflow.yaml** - Tests agent path loading with overrides
- **mixed-agents-workflow.yaml** - Tests mixing inline and path-based agents

### Invalid Workflows (Testing Error Handling)
- **invalid-path-workflow.yaml** - Tests handling of invalid agent paths
- **invalid-config-workflow.yaml** - Tests handling of invalid configurations
- **invalid-agent.yaml** - Tests handling of invalid agent definitions

### Test Agent Configs
- **test-agent-1.yaml** - Sample agent for testing path-based loading
- **test-agent-2.yaml** - Sample agent for testing path-based loading

## Running Tests

```bash
# Run all workflow validation tests
cd tests/workflows
go test -v -tags fts5 ./...

# Run specific test
go test -v -tags fts5 -run TestPathReferenceWorkflow
```

## Test Structure

The tests validate:
1. **Workflow YAML parsing** - Correct structure and required fields
2. **Agent loading** - By ID, by path, and inline definitions
3. **Validation logic** - Required fields and valid configurations
4. **Error handling** - Proper errors for invalid configurations

## Adding New Tests

When adding new test workflows:

1. Create YAML file in `test-data/` directory
2. Add test function in `validate_test.go`
3. Document the test case purpose
4. Run tests to verify

## CI Integration

These tests are run as part of the CI pipeline to ensure workflow validation logic works correctly across all supported patterns and agent loading methods.
