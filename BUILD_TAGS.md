# Build Tags for Optional Dependencies

Loom can be built with or without optional dependencies (Hawk) using Go build tags.

## Quick Start

**Build without any optional dependencies (minimal build - default)**:
```bash
just build              # or: just build-minimal
go build ./cmd/looms    # Direct go build
```

**Build with all features**:
```bash
just build-full
go build -tags "hawk" ./cmd/looms
```

**Build with Hawk**:
```bash
just build-hawk        # Hawk observability
```

---

## Available Build Tags

### `hawk` - Hawk Observability Support

**What it enables**:
- `observability.HawkTracer` - Export traces to Hawk service
- `observability.EmbeddedHawkTracer` - Embedded trace storage (memory/SQLite)
- `observability.HawkJudgeExporter` - Export judge verdicts to Hawk for observability
- `pkg/evals.ExportToHawk()` - Eval result export

**When to use**:
- You want to export observability traces to Hawk
- You need embedded trace storage for debugging
- You want to export judge evaluation results to Hawk for analysis

**Without this tag**:
- All Hawk functions return errors: "hawk support not compiled in"
- Falls back to `observability.NoOpTracer` (no overhead)
- **Judge evaluation works fully** - judges are built-in, only export functionality requires this tag
- Full agent and evaluation functionality works, just without observability export

**Dependencies required**:
- `github.com/Teradata-TIO/hawk` - Hawk SDK

---

## Build Target Reference

| Command | Tags | Description | Use Case |
|---------|------|-------------|----------|
| `just build` | `fts5` | Minimal build (default) | Production deployment, no external services |
| `just build-minimal` | `fts5` | Same as `build` | Explicit minimal build |
| `just build-hawk` | `fts5,hawk` | With Hawk | Observability enabled |
| `just build-full` | `fts5,hawk` | All features | Full development environment |

**Note**: The `fts5` tag is always included for SQLite FTS5 support (required for session storage).

---

## Direct Go Build Commands

If you're not using `just`:

```bash
# Minimal (no optional dependencies)
go build -tags fts5 -o bin/looms ./cmd/looms

# With Hawk
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

### Without Hawk Tag

```go
tracer, err := observability.NewHawkTracer(config)
// Returns: nil, "hawk support not compiled in (rebuild with -tags hawk)"

// Server automatically falls back to NoOpTracer:
tracer = observability.NewNoOpTracer()
```

---

## Dependency Management

### go.mod Entries

The optional dependencies remain in `go.mod` for convenience:

```go
require (
    // Optional: Only needed when building with -tags hawk
    github.com/Teradata-TIO/hawk v0.0.0-00010101000000-000000000000
)
```

### Building Without Dependencies

If you don't have the dependencies locally and only want the minimal build:

1. **Remove the dependencies** (they're not needed for minimal build):
   ```bash
   go mod edit -dropreplace github.com/Teradata-TIO/hawk
   go mod edit -droprequire github.com/Teradata-TIO/hawk
   go mod tidy
   ```

2. **Build normally**:
   ```bash
   go build -tags fts5 ./cmd/looms
   ```

3. **To restore full features later**:
   ```bash
   go get github.com/Teradata-TIO/hawk@latest
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

### Build fails with "undefined: HawkConfig"

**Cause**: You're building with `-tags hawk` but `hawk_types.go` is excluded.

**Fix**: This shouldn't happen with the current setup. If it does, check that `hawk_types.go` has the build constraint `//go:build !hawk`.

### Runtime error: "hawk support not compiled in"

**Cause**: Binary was built without `-tags hawk` but code tried to use Hawk.

**Fix**: Either:
1. Rebuild with `-tags hawk`
2. Configure observability to use `mode: none` or `mode: embedded`
3. Don't set `HAWK_ENDPOINT` (server will use NoOpTracer automatically)

### Import cycle with hawk

**Cause**: Trying to import Hawk packages directly.

**Fix**: Don't import these packages directly in application code. Use the interfaces:
- `observability.Tracer` (not `hawk.Tracer`)

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

- `pkg/observability/hawk.go` - Real Hawk implementation (`//go:build hawk`)
- `pkg/observability/hawk_stub.go` - Stub implementation (`//go:build !hawk`)
- `pkg/observability/hawk_types.go` - Shared types (`//go:build !hawk`)

---

## Questions?

See also:
- `CLAUDE.md` - Development workflow
- `CONTRIBUTING.md` - Contribution guidelines
- `website/content/en/docs/` - Full documentation
