# Build Tags for Optional Dependencies

Loom can be built with or without optional dependencies using Go build tags.

## Quick Start

**Build with SQLite support (recommended - default)**:
```bash
just build              # Includes -tags fts5
go build -tags fts5 ./cmd/looms
```

**Build with Hawk service export (optional)**:
```bash
just build-hawk         # Includes -tags fts5,hawk
go build -tags "fts5,hawk" ./cmd/looms
```

---

## Available Build Tags

### `fts5` - SQLite FTS5 Support (Required)

**What it enables**:
- Session storage with SQLite
- Embedded observability with SQLite persistence
- Full-text search for traces and sessions

**When to use**:
- Always (this is the default for all builds)
- Required for production deployments

**Without this tag**:
- Session storage will fail
- Only in-memory observability available

**Dependencies required**:
- `modernc.org/sqlite` - Pure Go SQLite implementation

### `hawk` - Hawk Service Export (Optional)

**What it enables**:
- `observability.HawkTracer` - Export traces to Hawk service via HTTP
- `observability.HawkJudgeExporter` - Export judge verdicts to Hawk for observability
- `pkg/evals.ExportToHawk()` - Eval result export

**When to use**:
- You want to export observability traces to external Hawk service
- You want to export judge evaluation results to Hawk for analysis

**Without this tag**:
- Hawk HTTP export returns errors: "hawk support not compiled in"
- Embedded observability still works (in-process storage)
- Falls back to `observability.NoOpTracer` for service export
- **Judge evaluation works fully** - judges are built-in, only export functionality requires this tag
- Full agent and evaluation functionality works, just without external export

**Dependencies required**:
- `github.com/Teradata-TIO/hawk` - Hawk SDK (optional)

---

## Build Target Reference

| Command | Tags | Description | Use Case |
|---------|------|-------------|----------|
| `just build` | `fts5` | Standard build (default) | Production deployment with embedded observability |
| `just build-minimal` | `fts5` | Same as `build` | Explicit standard build |
| `just build-hawk` | `fts5,hawk` | With Hawk service export | External observability service |
| `just build-full` | `fts5,hawk` | All features | Full development environment with external export |

**Note**: The `fts5` tag is always included for SQLite FTS5 support (required for session storage and embedded observability).

---

## Direct Go Build Commands

If you're not using `just`:

```bash
# Standard build (embedded observability with SQLite)
go build -tags fts5 -o bin/looms ./cmd/looms

# With Hawk service export
go build -tags fts5,hawk -o bin/looms ./cmd/looms

# With all features (same as hawk)
go build -tags "fts5,hawk" -o bin/looms ./cmd/looms
```

---

## Testing with Build Tags

Run tests for specific builds:

```bash
# Minimal tests (default)
go test -tags fts5 ./...

# Hawk tests
go test -tags "fts5,hawk" ./pkg/observability/...

# All features
go test -tags "fts5,hawk" ./...
```

---

## Runtime Behavior

### Observability Modes

Loom supports three observability modes:

1. **Embedded Mode** (Always available):
```go
// In-process storage (memory or SQLite)
tracer, err := observability.NewEmbeddedTracer(&observability.EmbeddedConfig{
    StorageType: "sqlite",
    SQLitePath:  "./traces.db",
})
// Works without any build tags (requires -tags fts5 for SQLite)
```

2. **Service Mode** (Requires `-tags hawk`):
```go
// HTTP export to Hawk service
tracer, err := observability.NewHawkTracer(observability.HawkConfig{
    Endpoint: "http://localhost:9090/v1/traces",
})
// Without -tags hawk: Returns error "hawk support not compiled in"
```

3. **None Mode** (Always available):
```go
// No observability overhead
tracer := observability.NewNoOpTracer()
```

---

## Dependency Management

### go.mod Entries

Embedded observability has no external dependencies:

```go
require (
    // Always available: Pure Go SQLite
    modernc.org/sqlite v1.29.5

    // Optional: Only needed when building with -tags hawk
    // github.com/Teradata-TIO/hawk v0.0.0 (not required)
)
```

### Building Without Hawk Dependency

Standard builds don't require Hawk:

```bash
# Standard build (embedded observability)
go build -tags fts5 -o bin/looms ./cmd/looms

# No Hawk dependency needed
```

### Adding Hawk Service Export

To enable Hawk service export:

```bash
# Add Hawk dependency (if not present)
go get github.com/Teradata-TIO/hawk@latest

# Build with hawk tag
go build -tags fts5,hawk -o bin/looms ./cmd/looms
```

---

## CI/CD Integration

Example GitHub Actions workflow:

```yaml
name: Build Matrix

on: [push, pull_request]

jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        build: [minimal, hawk, full]
    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.23'

      - name: Build ${{ matrix.build }}
        run: just build-${{ matrix.build }}
```

---

## Binary Size Comparison

Approximate sizes (as of Jan 2026):

| Build | looms | loom | Notes |
|-------|-------|------|-------|
| Minimal | 105 MB | 39 MB | No optional deps |
| +Hawk | 106 MB | 39 MB | +Hawk SDK (~1MB) |
| Full | 106 MB | 39 MB | Same as +Hawk |

---

## Troubleshooting

### Build fails with "undefined: sqlite3"

**Cause**: Missing `-tags fts5` build tag.

**Fix**: Always include fts5 tag:
```bash
go build -tags fts5 -o bin/looms ./cmd/looms
```

### Runtime error: "hawk support not compiled in"

**Cause**: Binary was built without `-tags hawk` but code tried to use Hawk service export.

**Fix**: Either:
1. Use embedded mode instead (always available):
   ```yaml
   observability:
     enabled: true
     mode: embedded
     storage_type: sqlite
   ```
2. Rebuild with `-tags fts5,hawk` for service export
3. Configure observability to use `mode: none`

### Embedded traces not persisting

**Cause**: SQLite path not configured or insufficient permissions.

**Fix**: Check configuration:
```yaml
observability:
  enabled: true
  mode: embedded
  storage_type: sqlite
  sqlite_path: ./traces.db  # Ensure write permissions
```

### Import errors with observability package

**Cause**: Trying to import internal storage packages.

**Fix**: Use the public interfaces:
- `observability.Tracer` (not internal packages)
- `observability.NewEmbeddedTracer()` for embedded mode
- `observability.NewHawkTracer()` for service mode

---

## Best Practices

1. **Default to minimal builds**: Ship minimal binaries by default, let users opt-in to features

2. **Test all combinations**: Use CI matrix to test minimal, hawk, and full builds

3. **Graceful degradation**: Always handle missing features gracefully:
   ```go
   if tracer, err := observability.NewHawkTracer(config); err != nil {
       logger.Warn("Hawk not available, using no-op tracer")
       tracer = observability.NewNoOpTracer()
   }
   ```

4. **Document requirements**: Clearly document which features require which tags

5. **Keep stubs updated**: When adding new Hawk methods, update the stub files

---

## Related Files

**Embedded Observability (Always Available)**:
- `pkg/observability/embedded.go` - Embedded tracer implementation
- `pkg/observability/storage/interface.go` - Storage interface
- `pkg/observability/storage/memory.go` - In-memory storage
- `pkg/observability/storage/sqlite.go` - SQLite storage (`//go:build fts5`)
- `pkg/observability/storage/sqlite_stub.go` - SQLite stub (`//go:build !fts5`)

**Service Export (Requires `-tags hawk`)**:
- `pkg/observability/hawk.go` - HTTP export to Hawk service (`//go:build hawk`)
- `pkg/observability/hawk_stub.go` - Stub implementation (`//go:build !hawk`)

**Always Available**:
- `pkg/observability/noop.go` - No-op tracer
- `pkg/observability/interface.go` - Tracer interface
- `pkg/observability/types.go` - Span and event types

---

## Questions?

See also:
- `CLAUDE.md` - Development workflow
- `CONTRIBUTING.md` - Contribution guidelines
- `website/content/en/docs/` - Full documentation
