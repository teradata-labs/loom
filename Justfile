# Loom Justfile - Task automation for the agentic framework

# Default recipe (show available commands)
default:
    @just --list

# Initialize buf module (run once)
proto-init:
    buf mod init

# Update buf dependencies
proto-update:
    buf mod update

# Generate proto files using buf
proto:
    buf generate

# Lint proto files
proto-lint:
    buf lint

# Breaking change detection
proto-breaking:
    buf breaking --against '.git#branch=main'

# Format proto files
proto-format:
    buf format -w

# Check proto formatting (fails if unformatted)
proto-format-check:
    buf format --diff --exit-code

# Verify proto generation is up to date
proto-gen-check: proto
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git diff --exit-code gen/ >/dev/null 2>&1; then
        echo "::error::Generated proto files are out of date. Run 'buf generate' and commit."
        git diff gen/
        exit 1
    fi
    echo "✅ Proto generation is up to date"

# Run tests with race detector (skip expensive integration tests)
test:
    GOWORK=off go test -tags fts5 -race -short -v ./...

# Run tests with coverage (skip expensive integration tests)
test-coverage:
    GOWORK=off go test -tags fts5 -race -short -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# Run integration tests (with real Bedrock LLM - expensive!)
test-integration:
    @echo "Running integration tests with real Bedrock LLM..."
    @echo "This may take 10+ minutes and will incur AWS costs."
    GOWORK=off go test -tags fts5 -race -v ./test/...

# Run tests for specific package
test-pkg pkg:
    GOWORK=off go test -tags fts5 -race -v ./{{pkg}}/...

# Run benchmarks
bench:
    GOWORK=off go test -tags fts5 -bench=. -benchmem ./...

# Run benchmarks for specific package
bench-pkg pkg:
    GOWORK=off go test -tags fts5 -bench=. -benchmem ./{{pkg}}/...

# Generate embedded weaver.yaml from template (injects CLI help)
generate-weaver:
    @echo "Generating embedded/weaver.yaml from template..."
    @go run ./cmd/generate-weaver

# Build all binaries
build: generate-weaver build-server build-tui
    @echo "✅ All binaries built successfully!"

# Build server only
build-server: proto generate-weaver
    @echo "Building Loom server (looms)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/looms ./cmd/looms
    @echo "✅ Server binary: bin/looms"

# Build TUI client only
build-tui:
    @echo "Building Loom TUI client (loom)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/loom ./cmd/loom
    @echo "✅ TUI binary: bin/loom"

# Build standalone binary (embedded server + TUI)
build-standalone: proto generate-weaver
    @echo "Building standalone Loom (loom-standalone)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5,standalone -o bin/loom-standalone ./cmd/loom-standalone
    @echo "✅ Standalone binary: bin/loom-standalone"

# Build with Hawk support (observability)
build-hawk: proto
    @echo "Building Loom with Hawk support..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5,hawk -o bin/looms-hawk ./cmd/looms
    GOWORK=off go build -tags fts5,hawk -o bin/loom-hawk ./cmd/loom
    @echo "✅ Hawk-enabled binaries: bin/looms-hawk, bin/loom-hawk"

# Build with Promptio support (prompt management)
build-promptio: proto
    @echo "Building Loom with Promptio support..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5,promptio -o bin/looms-promptio ./cmd/looms
    GOWORK=off go build -tags fts5,promptio -o bin/loom-promptio ./cmd/loom
    @echo "✅ Promptio-enabled binaries: bin/looms-promptio, bin/loom-promptio"

# Build with all features (Promptio - judge is now built-in)
build-full: proto
    @echo "Building Loom with all features (built-in judge + Promptio)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5,promptio -o bin/looms-full ./cmd/looms
    GOWORK=off go build -tags fts5,promptio -o bin/loom-full ./cmd/loom
    @echo "✅ Full-featured binaries: bin/looms-full, bin/loom-full"
    @echo "Note: Judge functionality is now built-in (no longer requires Hawk)"

# Build minimal (no Promptio) - default - judge still included
build-minimal: proto
    @echo "Building Loom (built-in judge, no Promptio)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/looms-minimal ./cmd/looms
    GOWORK=off go build -tags fts5 -o bin/loom-minimal ./cmd/loom
    @echo "✅ Minimal binaries: bin/looms-minimal, bin/loom-minimal"
    @echo "Note: Judge functionality is built-in (uses hardcoded prompts without Promptio)"
    @echo "This binary includes both server and TUI - no separate server needed!"

# Install patterns to $LOOM_DATA_DIR/patterns (defaults to ~/.loom/patterns)
install-patterns:
    #!/usr/bin/env bash
    set -euo pipefail
    LOOM_DIR="${LOOM_DATA_DIR:-~/.loom}"
    echo "Installing patterns to $LOOM_DIR/patterns..."
    mkdir -p "$LOOM_DIR/patterns"
    # Copy all pattern directories and their contents
    rsync -av --delete patterns/ "$LOOM_DIR/patterns/"
    echo "✅ Patterns installed to $LOOM_DIR/patterns"
    echo "   Found $(find "$LOOM_DIR/patterns" -name '*.yaml' | wc -l) pattern files"

# Install documentation to $LOOM_DATA_DIR/documentation (defaults to ~/.loom/documentation)
install-docs:
    #!/usr/bin/env bash
    set -euo pipefail
    LOOM_DIR="${LOOM_DATA_DIR:-~/.loom}"
    echo "Installing documentation to $LOOM_DIR/documentation..."
    mkdir -p "$LOOM_DIR/documentation"
    # Copy entire docs directory (architecture, guides, reference, etc.)
    if [ -d "docs" ]; then
        rsync -av --delete docs/ "$LOOM_DIR/documentation/"
        echo "   ✅ All documentation copied"
    fi
    echo "✅ Documentation installed to $LOOM_DIR/documentation"
    echo "   Total files: $(find "$LOOM_DIR/documentation" -name '*.md' 2>/dev/null | wc -l) markdown files"
    echo "   Architecture: $(find "$LOOM_DIR/documentation/architecture" -name '*.md' 2>/dev/null | wc -l) files"
    echo "   Guides: $(find "$LOOM_DIR/documentation/guides" -name '*.md' 2>/dev/null | wc -l) files"
    echo "   Reference: $(find "$LOOM_DIR/documentation/reference" -name '*.md' 2>/dev/null | wc -l) files"

# Install binaries, patterns, and documentation to user directory
# Set LOOM_BIN_DIR to customize installation directory (defaults to ~/.local/bin)
install: build install-patterns install-docs
    #!/usr/bin/env bash
    set -euo pipefail
    BIN_DIR="${LOOM_BIN_DIR:-~/.local/bin}"
    echo "Installing Loom binaries to $BIN_DIR..."
    mkdir -p "$BIN_DIR"
    cp bin/looms "$BIN_DIR/"
    cp bin/loom "$BIN_DIR/"
    chmod +x "$BIN_DIR/looms" "$BIN_DIR/loom"
    echo "✅ Binaries installed to $BIN_DIR"
    echo ""
    echo "Make sure $BIN_DIR is in your PATH:"
    echo "  export PATH=\"$BIN_DIR:\$PATH\""
    echo ""
    echo "Start the server:"
    echo "  looms serve"
    echo ""
    echo "Connect with the TUI:"
    echo "  loom"

# Build all variants (server, tui, standalone)
build-all: build-server build-tui build-standalone
    @echo "✅ All build variants complete!"
    @ls -lh bin/

build-examples:
    @echo "Building example agents..."
    @mkdir -p bin
    GOWORK=off go build -o bin/file-agent ./examples/file-agent
    GOWORK=off go build -o bin/postgres-agent ./examples/postgres-agent
    GOWORK=off go build -o bin/rest-api-agent ./examples/rest-api-agent
    @echo "✅ Example agents built: bin/file-agent, bin/postgres-agent, bin/rest-api-agent"

# Build for distribution (all platforms)
build-dist: proto
    @echo "Building distribution binaries..."
    @mkdir -p dist
    # Server
    GOWORK=off GOOS=darwin GOARCH=amd64 go build -tags fts5 -o dist/looms-darwin-amd64 ./cmd/looms
    GOWORK=off GOOS=darwin GOARCH=arm64 go build -tags fts5 -o dist/looms-darwin-arm64 ./cmd/looms
    GOWORK=off GOOS=linux GOARCH=amd64 go build -tags fts5 -o dist/looms-linux-amd64 ./cmd/looms
    GOWORK=off GOOS=linux GOARCH=arm64 go build -tags fts5 -o dist/looms-linux-arm64 ./cmd/looms
    GOWORK=off GOOS=windows GOARCH=amd64 go build -tags fts5 -o dist/looms-windows-amd64.exe ./cmd/looms
    # TUI
    GOWORK=off GOOS=darwin GOARCH=amd64 go build -tags fts5 -o dist/loom-darwin-amd64 ./cmd/loom
    GOWORK=off GOOS=darwin GOARCH=arm64 go build -tags fts5 -o dist/loom-darwin-arm64 ./cmd/loom
    GOWORK=off GOOS=linux GOARCH=amd64 go build -tags fts5 -o dist/loom-linux-amd64 ./cmd/loom
    GOWORK=off GOOS=linux GOARCH=arm64 go build -tags fts5 -o dist/loom-linux-arm64 ./cmd/loom
    GOWORK=off GOOS=windows GOARCH=amd64 go build -tags fts5 -o dist/loom-windows-amd64.exe ./cmd/loom
    @echo "✅ Distribution binaries in dist/"
    @ls -lh dist/

# Build specific binary
build-bin name:
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/{{name}} ./cmd/{{name}}

# Run go vet
vet:
    go vet ./...

# Run linter
lint:
    golangci-lint run ./...

# Format code
fmt:
    gofmt -s -w .
    goimports -w .

# Check code formatting (fails if unformatted files exist)
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(find . -name '*.go' -not -path './vendor/*' -not -path './gen/*' -exec gofmt -l {} +)
    if [ -n "$unformatted" ]; then
        echo "The following files need formatting:"
        echo "$unformatted"
        echo ""
        echo "Run 'just fmt' to fix formatting"
        exit 1
    fi

# Install development dependencies
deps:
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
    go install golang.org/x/tools/cmd/goimports@latest
    go install github.com/securego/gosec/v2/cmd/gosec@latest
    go mod download
    go mod tidy

# Run security scanner
security:
    @echo "Running security scan..."
    @gosec -no-fail -fmt=text ./... || echo "⚠️  Security issues found (non-blocking)"

# Clean build artifacts
clean:
    #!/usr/bin/env bash
    set -euo pipefail
    # Stop any running looms server processes
    pkill -9 -f looms 2>/dev/null && echo "🧹 Stopped running looms server" || true
    # Clean build artifacts
    rm -rf bin/
    rm -rf gen/
    rm -f coverage.out coverage.html
    rm -rf dist/
    find . -name "*.test" -delete
    find . -name "*.out" -delete
    # Clean installed binaries (use LOOM_BIN_DIR if set, otherwise ~/.local/bin)
    BIN_DIR="${LOOM_BIN_DIR:-~/.local/bin}"
    rm -rf "$BIN_DIR/loom" "$BIN_DIR/looms" "$BIN_DIR/hawk"
    # Clean app data directories (use LOOM_DATA_DIR if set, otherwise ~/.loom)
    LOOM_DIR="${LOOM_DATA_DIR:-~/.loom}"
    rm -rf "$LOOM_DIR" ~/.hawk
    # Clean any databases in project root
    rm -f *.db *.sqlite *.sqlite3
    # Clean any stray binaries in project root
    rm -f loom looms loom-standalone
    echo "✅ Clean complete"

# Clean and regenerate everything
clean-all: clean proto
    @echo "Clean rebuild complete!"

# Run TUI client (development)
run *args:
    go run -tags fts5 ./cmd/loom {{args}}

# Run server (development)
run-server *args:
    go run -tags fts5 ./cmd/looms {{args}}

# Run standalone (development)
run-standalone *args:
    go run -tags fts5,standalone ./cmd/loom-standalone {{args}}

# Start server in background, then run TUI
dev-full: build-server
    @echo "Starting server in background..."
    @bin/looms serve > /tmp/loom-server.log 2>&1 & echo $$! > /tmp/loom-server.pid
    @sleep 2
    @echo "Server started (PID: $$(cat /tmp/loom-server.pid))"
    @echo "Logs: /tmp/loom-server.log"
    @echo ""
    @echo "Starting TUI..."
    go run -tags fts5 ./cmd/loom
    @echo ""
    @echo "Stopping server..."
    @kill $$(cat /tmp/loom-server.pid) 2>/dev/null || true
    @rm -f /tmp/loom-server.pid

# Run all checks (matches GitHub CI exactly)
check: proto-lint proto-format-check proto-gen-check fmt-check vet lint test build security
    @echo "✅ All checks passed! (matches GitHub CI)"

# Watch for changes and run tests
watch:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Watching for changes..."
    while true; do
        find . -name "*.go" -o -name "*.proto" | entr -d just test
    done

# Generate mock interfaces
mocks:
    @echo "Generating mocks..."
    go generate ./...

# Run performance profiling
profile-cpu pkg:
    GOWORK=off go test -tags fts5 -cpuprofile=cpu.prof -bench=. ./{{pkg}}
    go tool pprof -http=:8080 cpu.prof

profile-mem pkg:
    GOWORK=off go test -tags fts5 -memprofile=mem.prof -bench=. ./{{pkg}}
    go tool pprof -http=:8080 mem.prof

# Check for race conditions extensively (critical for agent code!)
race-check:
    @echo "Running extensive race detection tests..."
    GOWORK=off go test -tags fts5 -race -count=50 ./pkg/agent
    GOWORK=off go test -tags fts5 -race -count=50 ./pkg/observability
    GOWORK=off go test -tags fts5 -race -count=50 ./pkg/prompts

# Run specific tests matching pattern
test-run pattern:
    GOWORK=off go test -tags fts5 -race -v -run {{pattern}} ./...

# Update go dependencies
update-deps:
    go get -u ./...
    go mod tidy

# Show project statistics
stats:
    @echo "Lines of code:"
    @find . -name "*.go" -not -path "*/vendor/*" -not -name "*.pb.go" -not -path "*/gen/*" | xargs wc -l | tail -1
    @echo ""
    @echo "Proto files:"
    @find ./proto -name "*.proto" 2>/dev/null | wc -l || echo "0"
    @echo ""
    @echo "Test files:"
    @find . -name "*_test.go" | wc -l
    @echo ""
    @echo "Packages:"
    @go list ./... | wc -l

# Create a new release
release version:
    @echo "Creating release {{version}}..."
    git tag -a {{version}} -m "Release {{version}}"
    git push origin {{version}}
    @echo "Release {{version}} created!"

# Verify installation
verify:
    @echo "Verifying installation..."
    @command -v buf >/dev/null 2>&1 && echo "✅ buf installed" || echo "❌ buf not installed"
    @command -v go >/dev/null 2>&1 && echo "✅ go installed" || echo "❌ go not installed"
    @command -v protoc-gen-go >/dev/null 2>&1 && echo "✅ protoc-gen-go installed" || echo "❌ protoc-gen-go not installed"
    @command -v protoc-gen-go-grpc >/dev/null 2>&1 && echo "✅ protoc-gen-go-grpc installed" || echo "❌ protoc-gen-go-grpc not installed"

# Bootstrap project (first-time setup)
bootstrap: deps proto-init proto-update proto
    @echo "✅ Loom project bootstrapped!"
    @echo "Run 'just test' to verify everything works."

# Self-test: Use loom to test loom (dogfooding!)
self-test: build
    @echo "Running self-test (dogfooding)..."
    @echo "TODO: Implement self-test once agent is functional"

# Git workflow helpers
git-status:
    git status --short

git-commit msg: proto test
    git add .
    git commit -m "{{msg}}"

# Development workflow: make changes and verify
dev: proto test
    @echo "✅ Development cycle complete!"

# CI/CD simulation
ci: proto-lint lint test build
    @echo "✅ CI checks passed!"

# Update version across all files
set-version VERSION:
    @echo "Updating version to {{VERSION}}"
    echo "{{VERSION}}" > VERSION
    sed -i.bak 's/var Version = ".*"/var Version = "{{VERSION}}"/' internal/version/version.go
    sed -i.bak 's/\*\*Version\*\*: v.*/\*\*Version\*\*: v{{VERSION}}/' README.md CLAUDE.md
    sed -i.bak 's/Version: v.*/Version: v{{VERSION}}/' website/content/en/_index.md
    @echo "✅ Version updated. Review changes with 'git diff'"
