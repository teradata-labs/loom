# Contributing to Loom

Thank you for your interest in contributing to Loom! This document provides guidelines and instructions for contributing to the project.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [Coding Standards](#coding-standards)
- [Testing Requirements](#testing-requirements)
- [Pull Request Process](#pull-request-process)
- [Commit Message Convention](#commit-message-convention)
- [Project Structure](#project-structure)

## Code of Conduct

- Be respectful and professional
- Focus on technical merit and facts
- Provide constructive feedback
- Help maintain a welcoming environment

## Getting Started

### Prerequisites

- **Go 1.24+** - Required for development
- **Buf CLI** - For proto generation (`brew install bufbuild/buf/buf` or see [buf.build](https://buf.build))
- **Just** - Task runner (`brew install just` or `cargo install just`)
- **Git** - Version control

### Setup Development Environment

```bash
# Clone the repository
git clone https://github.com/teradata-labs/loom.git
cd loom

# Verify all tools are installed
just verify

# Generate proto files
buf generate

# Run tests to verify setup
just test

# Run all checks (lint + test + build)
just check
```

## Development Workflow

### 1. Create a Feature Branch

```bash
git checkout -b feature/your-feature-name
# OR
git checkout -b fix/issue-description
```

Branch naming conventions:
- `feature/` - New features
- `fix/` - Bug fixes
- `docs/` - Documentation updates
- `refactor/` - Code refactoring
- `test/` - Test additions/updates
- `chore/` - Maintenance tasks

### 2. Make Changes

Follow these principles:

1. **Proto First**: If adding/changing APIs, update `.proto` files first
2. **Test as You Go**: Write tests before or alongside implementation (TDD encouraged)
3. **Run Race Detector**: Always test with `-race` flag for concurrent code
4. **Small Commits**: Make atomic commits with clear messages

### 3. Run Quality Checks

Before committing:

```bash
# Generate proto (if proto files changed)
buf generate

# Format proto files
buf format -w

# Lint proto
buf lint

# Check for breaking changes
buf breaking --against .git#branch=main

# Format Go code
gofmt -w .

# Run linters
just lint

# Run tests with race detector
just test

# Build binaries
just build

# Run all checks
just check
```

### 4. Commit Changes

```bash
git add .
git commit -m "feat(scope): add new feature"
```

See [Commit Message Convention](#commit-message-convention) below.

### 5. Push and Create Pull Request

```bash
git push origin feature/your-feature-name
```

Then create a PR on GitHub using the provided template.

## Coding Standards

### Go Code Style

- **Use gofmt**: All code must be formatted with `gofmt`
- **Follow Go conventions**: Exported names, error handling, etc.
- **Package organization**: See [Project Structure](#project-structure)
- **Error handling**: Always check and handle errors appropriately
- **Context propagation**: Pass `context.Context` as first parameter
- **No bare returns**: Use explicit return statements
- **Keep functions small**: Aim for <100 lines per function

### Proto Style

- **Use buf format**: All proto files must be formatted with `buf format`
- **Follow buf.yaml rules**: Lint rules are enforced
- **Breaking changes**: Document and justify any breaking changes
- **Field numbering**: Never reuse field numbers

### Naming Conventions

**Packages:**
- `pkg/` - Framework code (importable by users)
- `internal/` - Private implementation
- `cmd/` - Binary entrypoints
- `examples/` - Reference implementations

**Interfaces:**
```go
type ExecutionBackend interface { ... }
type Tracer interface { ... }
type PromptRegistry interface { ... }
```

**Implementations:**
```go
type HawkTracer struct { ... }
type PromptioRegistry struct { ... }
type TeradataBackend struct { ... }
```

**Tests:**
```go
func TestAgentConversation(t *testing.T) { ... }
func BenchmarkMemoryStore(b *testing.B) { ... }
```

### Documentation

- **Public APIs**: Must have godoc comments
- **Complex logic**: Add inline comments explaining "why", not "what"
- **Examples**: Add examples in `examples/` for new features
- **README updates**: Update README.md for user-facing changes
- **ARCHITECTURE updates**: Update website/content/en/docs/architecture/_index.md for architectural changes

## Testing Requirements

### Unit Tests

**Required for:**
- All new features
- Bug fixes
- Refactored code

**Location:** `*_test.go` files alongside implementation

**Example:**
```go
func TestMemoryStore_Store(t *testing.T) {
    store := NewMemoryStore()
    data := []byte("test data")

    ref, err := store.Store(context.Background(), data, StoreOptions{})
    require.NoError(t, err)
    assert.NotEmpty(t, ref.Id)
}
```

### Race Detection

**Mandatory for concurrent code:**

```bash
go test -race ./pkg/communication -v
go test -race ./pkg/agent -v
```

**Zero tolerance for race conditions.** All PRs must pass race detection.

### Table-Driven Tests

For comprehensive scenario coverage:

```go
func TestPolicy_ShouldReference(t *testing.T) {
    tests := []struct {
        name        string
        messageType string
        size        int
        want        bool
    }{
        {"small value", "control", 1024, false},
        {"large value", "data", 11000, true},
        {"always ref", "session_state", 100, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test implementation
        })
    }
}
```

### Coverage Targets

- **Agent core**: >60%
- **Communication**: >75%
- **Observability**: >80%
- **Overall**: >70% (target for production readiness)

## Pull Request Process

### Before Submitting

1. ✅ All tests pass: `just test`
2. ✅ Zero race conditions: `just race-check`
3. ✅ Build succeeds: `just build`
4. ✅ Linters pass: `just check`
5. ✅ Proto validation (if changed): `buf lint` and `buf breaking`
6. ✅ Documentation updated
7. ✅ Self-review completed

### PR Requirements

**Required checks (must pass):**
- Proto lint & breaking changes
- Go lint (gofmt, go vet)
- Unit tests with race detection
- Multi-platform build (Linux, macOS, Windows)
- Security scan (gosec, CodeQL)
- Coverage check

**PR template:**
- Use the provided template (`.github/PULL_REQUEST_TEMPLATE.md`)
- Complete all required sections
- Check all applicable boxes
- Link to related issues

### Review Process

1. **Automated checks**: All CI jobs must pass
2. **Code review**: At least one approval required
3. **Address feedback**: Make requested changes
4. **Final approval**: Maintainer approval before merge
5. **Squash commits**: PRs will be squashed on merge to keep history clean

### Merge Requirements

- ✅ All CI checks pass
- ✅ At least 1 approval from maintainer
- ✅ No unresolved conversations
- ✅ Branch is up to date with main
- ✅ No merge conflicts

## Commit Message Convention

Format: `<type>(<scope>): <subject>`

### Types

- `feat` - New feature
- `fix` - Bug fix
- `docs` - Documentation only
- `test` - Test additions/updates
- `refactor` - Code refactoring
- `perf` - Performance improvements
- `chore` - Maintenance tasks
- `ci` - CI/CD changes
- `build` - Build system changes

### Scopes

Common scopes:
- `proto` - Protocol buffer changes
- `communication` - Communication layer
- `orchestration` - Workflow orchestration
- `agent` - Agent core
- `server` - Server implementation
- `cli` - CLI commands
- `observability` - Tracing/metrics
- `prompts` - Prompt management
- `fabric` - Backend interface
- `shuttle` - Tool system

### Examples

```bash
feat(communication): add ReferenceStore interface
fix(agent): prevent memory leak in session cleanup
docs(readme): update installation instructions
test(communication): add multi-process Redis tests
refactor(server): extract config validation logic
perf(memory): optimize reference resolution
chore(deps): update grpc to v1.60.0
ci(workflow): add CodeQL security analysis
```

### Subject Guidelines

- Use imperative mood ("add feature" not "added feature")
- No period at the end
- Keep under 72 characters
- Be specific and descriptive

### Body (optional)

For complex changes, add a body:

```
feat(orchestration): implement WorkflowContext

Add reference-backed shared state management for workflows.
This enables zero-copy data sharing between pipeline stages
and reduces memory usage by 90% for large payloads.

- Implement Set/Get/Delete methods
- Add reference counting
- Integrate with ReferenceStore
- Add snapshot/restore support

Closes #123
```

## Project Structure

```
loom/
├── proto/                  # Protocol buffer definitions
│   └── loom/v1/           # Versioned proto files
├── gen/                    # Generated code (DO NOT EDIT)
│   └── go/loom/v1/        # Generated Go code
├── pkg/                    # Framework (importable by users)
│   ├── agent/             # Agent runtime
│   ├── communication/     # Tiered communication (NEW)
│   ├── orchestration/     # Workflow patterns
│   ├── observability/     # Hawk integration
│   ├── prompts/           # Promptio integration
│   ├── fabric/            # Backend interface
│   ├── shuttle/           # Tool system
│   ├── server/            # Multi-agent server
│   └── builder/           # Convenience API
├── internal/              # Private implementation
├── cmd/                   # Binaries
│   ├── loom/             # TUI client
│   ├── looms/            # Multi-agent server
│   └── loom-standalone/  # Standalone agent
├── examples/              # YAML configuration examples
│   ├── config/           # Configuration templates
│   ├── dnd-party/        # Simple multi-agent example
│   ├── scheduled-workflows/
│   ├── 02-production-ready/transcend/
│   └── 03-advanced/dnd-adventure/
├── docs/                  # Documentation
│   ├── guides/           # User guides
│   ├── reference/        # Technical reference
│   ├── integration/      # External services
│   ├── design/           # Design decisions
│   └── development/      # Developer docs
├── .github/              # GitHub configuration
│   ├── workflows/        # CI/CD pipelines
│   ├── PULL_REQUEST_TEMPLATE.md
│   └── dependabot.yml
├── README.md             # Project overview
├── CONTRIBUTING.md       # This file
├── CODE_REVIEW.md        # Review guidelines
├── buf.yaml              # Buf configuration
├── buf.gen.yaml          # Buf generation config
└── Justfile              # Task automation
```

## Key Principles

### Proto is Law

1. Design proto first for all APIs
2. Run `buf generate` to create Go types
3. Implement using generated types
4. Never edit generated code

### Zero Tolerance

- **Race conditions**: All tests must pass `-race` detector
- **Lint errors**: Code must pass all linters
- **Security issues**: Gosec and CodeQL must pass
- **Breaking changes**: Must be justified and documented

### Test as You Go

- Write tests before or alongside implementation
- Run tests frequently during development
- Test concurrent code with `-race` flag
- Aim for >75% coverage on critical paths

### Commit Often

- Small, atomic commits (1 feature = 1 commit)
- Clear, descriptive commit messages
- Push frequently (backup and collaboration)

## Getting Help

- **Documentation**: Start with [docs/](docs/)
- **Examples**: See [examples/](examples/)
- **Issues**: Search existing issues or create a new one
- **Discussions**: Use GitHub Discussions for questions
- **Architecture**: See [website/content/en/docs/architecture/_index.md](website/content/en/docs/architecture/_index.md)

## Additional Resources

- [Proto Style Guide](https://buf.build/docs/best-practices/style-guide/)
- [Effective Go](https://golang.org/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [Buf Documentation](https://buf.build/docs)

---

**Thank you for contributing to Loom!** Your contributions help make agent orchestration better for everyone.
