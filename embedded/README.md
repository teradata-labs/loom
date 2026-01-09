# Embedded Assets

This package contains files that are embedded into the loom binaries using Go's `//go:embed` directive. This ensures critical configuration files are always available, even when the binary is distributed separately from the source tree.

## Files

- **weaver.yaml**: Default meta-agent configuration that orchestrates other agents and manages complex workflows

## Usage

```go
import "github.com/teradata-labs/loom/embedded"

// Get the embedded weaver.yaml content
weaverConfig := embedded.GetWeaver()
```

## Adding New Embedded Files

1. Copy the file to this directory
2. Add an embed directive in `agents.go`:
   ```go
   //go:embed myfile.yaml
   var MyFileYAML []byte
   ```
3. Add a getter function
4. Add tests in `agents_test.go`

## Why Embed?

When `looms serve` runs on a fresh install (or a packaged binary), it needs access to default agent configurations. By embedding these files:

- Users get a working setup immediately after binary installation
- No need to distribute the entire source tree with the binary
- Consistent experience across development and production environments

## Note on Paths

Go's `//go:embed` directive requires paths relative to the source file and cannot use `..` to navigate up. That's why files are copied into this directory at the module root rather than referenced from `examples/`.
