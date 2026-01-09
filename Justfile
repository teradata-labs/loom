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

# Build all binaries
build: build-server build-tui
    @echo "✅ All binaries built successfully!"

# Build server only
build-server: proto
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
build-standalone: proto
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

# Build with all features (Hawk + Promptio)
build-full: proto
    @echo "Building Loom with all features (Hawk + Promptio)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5,hawk,promptio -o bin/looms-full ./cmd/looms
    GOWORK=off go build -tags fts5,hawk,promptio -o bin/loom-full ./cmd/loom
    @echo "✅ Full-featured binaries: bin/looms-full, bin/loom-full"

# Build minimal (no Hawk, no Promptio) - default
build-minimal: proto
    @echo "Building minimal Loom (no Hawk, no Promptio)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/looms-minimal ./cmd/looms
    GOWORK=off go build -tags fts5 -o bin/loom-minimal ./cmd/loom
    @echo "✅ Minimal binaries: bin/looms-minimal, bin/loom-minimal"
    @echo "Note: Default build targets (build-server, build-tui) are now minimal builds"
    @echo "This binary includes both server and TUI - no separate server needed!"

# Install patterns to ~/.loom/patterns
install-patterns:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Installing patterns to ~/.loom/patterns..."
    mkdir -p ~/.loom/patterns
    # Copy all pattern directories and their contents
    rsync -av --delete patterns/ ~/.loom/patterns/
    echo "✅ Patterns installed to ~/.loom/patterns"
    echo "   Found $(find ~/.loom/patterns -name '*.yaml' | wc -l) pattern files"

# Install documentation to ~/.loom/documentation
install-docs:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Installing documentation to ~/.loom/documentation..."
    mkdir -p ~/.loom/documentation
    # Copy entire docs directory (architecture, guides, reference, etc.)
    if [ -d "website/content/en/docs" ]; then
        rsync -av --delete website/content/en/docs/ ~/.loom/documentation/
        echo "   ✅ All documentation copied"
    fi
    # Copy styleguide files if they exist
    if [ -f "pkg/visualization/styleguide_client.go" ]; then
        cp pkg/visualization/styleguide_client.go ~/.loom/documentation/
        echo "   ✅ Styleguide client copied"
    fi
    if [ -f "website/StyleGuide.tsx" ]; then
        cp website/StyleGuide.tsx ~/.loom/documentation/
        echo "   ✅ StyleGuide.tsx copied"
    fi
    echo "✅ Documentation installed to ~/.loom/documentation"
    echo "   Total files: $(find ~/.loom/documentation -name '*.md' 2>/dev/null | wc -l) markdown files"
    echo "   Architecture: $(find ~/.loom/documentation/architecture -name '*.md' 2>/dev/null | wc -l) files"
    echo "   Guides: $(find ~/.loom/documentation/guides -name '*.md' 2>/dev/null | wc -l) files"
    echo "   Reference: $(find ~/.loom/documentation/reference -name '*.md' 2>/dev/null | wc -l) files"

# Install binaries, patterns, and documentation to user directory
install: build install-patterns install-docs
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Installing Loom binaries..."
    mkdir -p ~/.local/bin
    cp bin/looms ~/.local/bin/
    cp bin/loom ~/.local/bin/
    chmod +x ~/.local/bin/looms ~/.local/bin/loom
    echo "✅ Binaries installed to ~/.local/bin"
    echo ""
    echo "Make sure ~/.local/bin is in your PATH:"
    echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
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

# Build Hugo documentation site
docs-build:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "📚 Building Hugo documentation..."
    if ! command -v hugo &> /dev/null; then
        echo "❌ Hugo not found. Install with: brew install hugo"
        exit 1
    fi
    # Use absolute path for destination
    DEST="$(pwd)/cmd/looms/docs/public"
    hugo -s website --minify --destination="$DEST"
    if [ -d "$DEST" ]; then
        SIZE=$(du -sh "$DEST" 2>/dev/null | cut -f1 || echo "unknown")
        echo "✅ Docs built to cmd/looms/docs/public ($SIZE)"
    else
        echo "❌ Error: Hugo build succeeded but output directory not found"
        exit 1
    fi

# Serve docs locally with live reload (development)
docs-serve:
    #!/usr/bin/env bash
    if ! command -v hugo &> /dev/null; then
        echo "❌ Hugo not found. Install with: brew install hugo"
        exit 1
    fi
    cd website && hugo server -D --bind 0.0.0.0

# Build server with embedded documentation
build-server-with-docs: proto docs-build
    @echo "Building Loom server with embedded docs (looms)..."
    @mkdir -p bin
    GOWORK=off go build -tags fts5 -o bin/looms ./cmd/looms
    @rm -rf cmd/looms/docs/public
    @echo "✅ Server binary with embedded docs: bin/looms"
    @echo "   Run 'bin/looms docs' to view documentation"

# Clean built docs
docs-clean:
    @rm -rf cmd/looms/docs/public website/public website/resources
    @echo "✅ Cleaned built documentation"

# Development workflow: Build docs, build binary with edit mode, and serve
docs-dev:
    #!/usr/bin/env bash
    set -euo pipefail

    # Kill any running looms-dev instances
    pkill -9 -f "looms-dev docs" 2>/dev/null || true
    echo "🧹 Killed existing looms-dev instances"

    # Build docs
    echo "📚 Building Hugo documentation..."
    if ! command -v hugo &> /dev/null; then
        echo "❌ Hugo not found. Install with: brew install hugo"
        exit 1
    fi
    DEST="$(pwd)/cmd/looms/docs/public"
    hugo -s website --minify --destination="$DEST" > /dev/null
    SIZE=$(du -sh "$DEST" 2>/dev/null | cut -f1)
    echo "✅ Docs built ($SIZE)"

    # Build binary with embedded docs
    echo "🔨 Building looms-dev binary..."
    GOWORK=off go build -tags fts5 -o bin/looms-dev ./cmd/looms
    echo "✅ Binary built ($(ls -lh bin/looms-dev | awk '{print $5}'))"

    # Start docs server with dev mode
    echo ""
    echo "🚀 Starting docs server with edit mode..."
    echo "   URL: http://localhost:6060"
    echo "   Press Ctrl+C to stop"
    echo ""
    ./bin/looms-dev docs --dev-mode

# Build example agents
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
    # Clean installed binaries
    rm -rf ~/.local/bin/loom ~/.local/bin/looms ~/.local/bin/hawk
    # Clean app data directories
    rm -rf ~/.loom ~/.hawk
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
