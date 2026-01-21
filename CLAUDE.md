# Loom Project Context

## Overview
Loom is an LLM agent framework that provides **autonomous agent creation with pattern-guided learning, self-correction, self-improvement, and complete observability**.

**Version**: v1.0.2
**Status**: Beta - Feature Complete, API Stabilizing
**Quality**: 2252+ test functions across 244 test files, 0 race conditions


## Core Principles
1. **FTS5 is REQUIRED**: All builds and tests MUST use `-tags fts5`. Use `just test` or `go test -tags fts5 -race ./...`
2. **Proto is law**: All APIs defined in proto first, then implementation follows
3. **Race detector always**: Run `go test -tags fts5 -race ./...` before every commit
4. **Observability by design**: Every operation traced to hawk
5. **Patterns not prompts**: Domain knowledge in YAML files
6. **Pluggable backends**: SQL databases, REST APIs, documents - all via ExecutionBackend interface
7. **Honesty in documentation**: Verify features exist before documenting them. No marketing speak.
8. **No role prompting**: Never use "You are a [role]..." or "As a [role]..." in prompts. Be direct and task-oriented. Bad: "You are a SQL expert. Analyze this query." Good: "Analyze this SQL query for performance issues."
9. **Dont take shortcuts**: No hardcodes, and do NOT add tech debt!

## Documentation Standards (CRITICAL - READ THIS!)

### Zero Tolerance for Marketing Speak

**This project is v1.0.0. Documentation must reflect reality, not aspirations.**

**BANNED WORDS/PHRASES:**
- ❌ "production-ready" (unless explicitly approved and verified)
- ❌ "comprehensive" (use "basic" or specific details)
- ❌ "seamless" / "seamlessly" (use "integrates" or "works with")
- ❌ "enterprise-grade" / "battle-tested"
- ❌ "fully-featured" / "complete"
- ❌ "robust" / "powerful" (show, don't tell)
- ❌ "cutting-edge" / "revolutionary"
- ❌ "effortless" / "simple" (unless truly 1-2 steps)

**REQUIRED PRACTICES:**
1. **Verify before claiming**: Use Grep/Read to verify feature exists before documenting
2. **Use status indicators**:
   - ✅ Implemented (feature fully working with tests)
   - ⚠️ Partial (infrastructure exists, content/features incomplete)
   - 🚧 In Development (actively being worked on)
   - 📋 Planned (not yet started)
3. **Separate Implemented vs Planned**: Never mix completed and planned features in same section
4. **Include version context**: README has "Version: v0.1.0 Alpha" badge
5. **Note dependencies**: Document internal dependencies (Hawk) clearly
6. **Specify limitations**: "File backend supported; SQL/API coming soon" not "all backends supported"

**GOOD EXAMPLES:**
- ✅ "Agent config hot-reload works; pattern hot-reload coming soon"
- ✅ "Pattern infrastructure implemented; example patterns being added"
- ✅ "File backend scaffolding works (`loom create --backend=file`)"
- ✅ "Framework for building agents" (not "production-ready framework")

**BAD EXAMPLES:**
- ❌ "Hot reload - patterns and prompts reload automatically" (only agent config works)
- ❌ "One-command scaffolding for all backends" (only file backend works)
- ❌ "Comprehensive pattern library" (library is empty)
- ❌ "Zero external dependencies" (Hawk is optional)

### README.md Structure Requirements

**The README.md MUST maintain:**
1. Version badge at top: `**Version:** v0.2.0`
2. Dependency note about Hawk (optional dependency)
3. Features split into "✅ Implemented" and "📋 Planned" sections
4. Accurate LLM provider info (Anthropic, Bedrock, Ollama implemented; Azure/Vertex AI planned)
5. Accurate feature claims verified against codebase

**Before updating README:**
1. Check current Features section structure (Implemented/In Development split)
2. Verify any claimed feature actually exists in codebase
3. Update version badge if releasing
4. Add new planned features to "🚧 In Development" section, not "✅ Implemented"

### Go Packages
- **buf.build/googleapis/googleapis**: For gRPC-gateway annotations
- **google.golang.org/grpc**: gRPC framework
- **github.com/grpc-ecosystem/grpc-gateway/v2**: HTTP/JSON gateway

## Development Workflow

### Before Starting Work
```bash
just verify          # Check all tools installed
buf generate        # Generate Go code from proto
go test ./...       # Run tests
```

### Making Changes
1. **Proto first**: If adding/changing APIs, update `proto/loom/v1/loom.proto`
2. **Generate**: Run `buf generate` to update Go code
3. **Implement**: Write implementation in `pkg/` or `internal/`
4. **Test**: Write tests with `-race` detector
5. **Commit**: Use `just git-commit "message"` for safe commits

### Critical Commands
```bash
just proto          # Generate proto code
just proto-lint     # Lint proto files
just test           # Run tests with race detector
just race-check     # Extensive race detection (50 runs)
just build          # Build all binaries
just check          # Lint + test + build
```

## Version Management

Loom uses semantic versioning (MAJOR.MINOR.PATCH). The VERSION file is the single source of truth.

### Releasing a New Version

```bash
# Patch release (bug fixes)
just bump-patch

# Minor release (new features)
just bump-minor

# Major release (breaking changes)
just bump-major
```

All bump commands automatically:
1. Update 20+ files (VERSION, version.go, packaging, docs)
2. Create commit: "Bump version to vX.Y.Z"
3. Create git tag: vX.Y.Z

Push to trigger release: `git push origin main --tags`

### Checking Version Consistency

```bash
# Show current version
just version-show

# Verify all files have consistent versions
just version-verify

# Fix any drift detected
just version-sync
```

### Files Automatically Updated

The version manager updates 20+ files across the codebase:
- VERSION (canonical)
- internal/version/version.go
- Homebrew formulas (loom.rb, loom-server.rb)
- Chocolatey package (loom.nuspec)
- Scoop manifests (loom.json, loom-server.json)
- Winget manifests (installer.yaml, locale.yaml)
- Documentation (README.md, CLAUDE.md)

### CI/CD Integration

Version consistency is automatically checked in:
- **PR checks**: Validates no version drift before merge
- **Release workflow**: Verifies tag matches VERSION file

## Testing Requirements

### Race Detection (Zero Tolerance!)
```bash
go test -race ./pkg/agent        # Agent conversation loops
go test -race ./pkg/observability # Tracer implementations
go test -race ./pkg/prompts      # Prompt cache
```

### Coverage Targets
- Agent core: >30% (complex system)
- Observability: >80% (critical path)
- Prompts: >80% (critical path)

### Test Patterns
- Table-driven tests for multiple scenarios
- Mock external dependencies (LLMs, databases)
- Golden files for SQL generation (when applicable)

## Proto Workflow

### Buf Commands
```bash
buf mod update      # Update dependencies
buf generate        # Generate Go code
buf lint            # Lint proto files
buf breaking        # Check for breaking changes
buf format -w       # Format proto files
```

## Common Mistakes to Avoid

### Code
1. ❌ **Don't skip race detector** - Always use `-race` flag
2. ❌ **Don't hardcode prompts** - Use FileRegistry for prompt management
3. ❌ **Don't skip tracing** - Instrument all LLM calls and tool executions
4. ❌ **Don't change proto without buf lint** - Keep it clean
5. ❌ **Don't break backwards compatibility** - Use `buf breaking`

### Documentation
8. ❌ **Don't use marketing speak** - See "Documentation Standards" section
9. ❌ **Don't claim unimplemented features** - Verify with Grep/Read first
10. ❌ **Don't mix implemented and planned features** - Use separate sections
11. ❌ **Don't remove version badge or status indicators** - Keep README honest
12. ⚠️ **Use "production-ready" carefully** - Only for verified features with tests
13. ❌ **Don't hide limitations** - Document what doesn't work yet

## Code Style

### Package Organization
```
pkg/                 # Framework (importable)
internal/            # Private implementation
examples/            # Reference implementations (teradata, postgres, etc.)
cmd/                 # Binaries (loom, loom-server, loom-mcp)
```

### Naming Conventions
- Interfaces: `ExecutionBackend`, `Tracer`, `PromptRegistry`
- Implementations: `HawkTracer`, `FileRegistry`, `TeradataBackend`
- Tests: `*_test.go` with table-driven patterns

### Imports
```go
import (
    "context"

    "github.com/teradata-labs/loom/pkg/agent"
    "github.com/teradata-labs/loom/pkg/observability"
    loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)
```

## Quick Reference

### Start Development
```bash
cd ~/Projects/loom
just verify
just proto
just test
```

### Add New RPC
1. Edit `proto/loom/v1/loom.proto`
2. Run `buf generate`
3. Implement in appropriate package
4. Add tests
5. Run `just check`

### Fix Proto Issues
```bash
buf format -w              # Format
buf lint                   # Check style
buf breaking --against .git#branch=main  # Check compatibility
```

### Debug Race Conditions
```bash
go test -race -run TestName ./pkg/agent -v
go test -race -count=50 ./pkg/agent  # Extensive testing
```

### Key Documentation
- `docs/_index.md` - Documentation home
- `docs/architecture/` - System design and architecture documentation
- `docs/guides/` - User guides and tutorials
- `docs/reference/` - API and CLI reference documentation

**IMPORTANT**: Always edit documentation in `docs/`

## Questions to Ask Before Implementing
1. Does this need a proto definition first?
2. Will this code run concurrently? (If yes, use -race)
3. Should this be traced to hawk?
4. Should this use a prompt from FileRegistry?
5. Is this backend-specific or framework-generic?

---

**Remember**:
- Proto is law. Race detector is mandatory. Observability is not optional.
- **Documentation honesty is non-negotiable.** Verify features exist before claiming them.
- **Documentation lives in `docs/`** (top-level docs directory)
- Commit often, use checklists, write honest docs in the docs directory (YOLO mode)
- run just check enough to not build up tech debt.
