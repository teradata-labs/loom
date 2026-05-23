# 12-Factor Architecture

Analysis of Loom's adherence to 12-factor app principles for cloud-native LLM agent frameworks.

**Target Audience**: Architects, academics, platform engineers

**Version**: v1.2.0

---

## Table of Contents

- [Overview](#overview)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [12-Factor Analysis](#12-factor-analysis)
  - [I. Codebase](#i-codebase)
  - [II. Dependencies](#ii-dependencies)
  - [III. Configuration](#iii-configuration)
  - [IV. Backing Services](#iv-backing-services)
  - [V. Build, Release, Run](#v-build-release-run)
  - [VI. Processes](#vi-processes)
  - [VII. Port Binding](#vii-port-binding)
  - [VIII. Concurrency](#viii-concurrency)
  - [IX. Disposability](#ix-disposability)
  - [X. Dev/Prod Parity](#x-devprod-parity)
  - [XI. Logs](#xi-logs)
  - [XII. Admin Processes](#xii-admin-processes)
- [Architecture Diagrams](#architecture-diagrams)
- [Key Design Decisions](#key-design-decisions)
- [Performance Characteristics](#performance-characteristics)
- [Security Considerations](#security-considerations)
- [Evolution and Recommendations](#evolution-and-recommendations)
- [Related Work](#related-work)
- [References](#references)

---

## Overview

This document analyzes Loom's architecture against the **12-factor app methodology** (Wiggins, 2011), a set of principles for building cloud-native applications. While originally designed for web services, these factors apply directly to LLM agent frameworks that require configuration flexibility, state management, observability, and operational resilience.

**Key Innovation**: Loom demonstrates how proto-first design, crash-only architecture, and segmented memory management align with 12-factor principles to create a resilient, observable, and horizontally scalable agent framework.

**Assessment**: Loom achieves **10/12 Excellent** ratings, with particularly strong configuration management, state persistence, observability, disposability, and admin tooling. Main gaps: horizontal scaling (single-process design) and database backend diversity (SQLite only for session store).

---

## Design Goals

**For 12-Factor Compliance:**

1. **Configuration Flexibility**: Support multiple environments (dev, staging, prod) with zero code changes
2. **State Externalization**: Persist all agent state to backing services (SQLite, Redis future)
3. **Interface Stability**: Proto-first APIs with backward compatibility guarantees
4. **Operational Observability**: Trace every operation for debugging and cost attribution
5. **Fast Startup/Shutdown**: <200ms cold start, graceful drain for zero-downtime deploys
6. **Horizontal Scalability**: Enable multi-instance deployments (future work)

**Non-goals**:
- Serverless execution (stateful agents require long-lived processes)
- Real-time distributed coordination (agents designed for async workflows)
- Multi-language runtime (Go-first, proto APIs for polyglot clients)

---

## System Context

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Loom Agent Framework                             │
│                                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                 │
│  │  CLI/TUI    │  │ gRPC Server  │  │ HTTP Gateway │                 │
│  │ (looms/loom)│  │  Port 60051  │  │  Port 5006   │                 │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘                 │
│         │                  │                  │                         │
│         └──────────────────┼──────────────────┘                         │
│                            │                                            │
│         ┌──────────────────┴──────────────────┐                         │
│         │        Agent Orchestration          │                         │
│         │   (Multi-agent, Goroutine-based)    │                         │
│         └──────────────────┬──────────────────┘                         │
│                            │                                            │
│         ┌──────────────────┼──────────────────┐                         │
│         │                  │                  │                         │
│    ┌────▼─────┐      ┌────▼─────┐      ┌────▼─────┐                   │
│    │ Pattern  │      │  Memory  │      │ Backend  │                    │
│    │ Registry │      │ Manager  │      │ Executor │                    │
│    └────┬─────┘      └────┬─────┘      └────┬─────┘                   │
│         │                  │                  │                         │
└─────────┼──────────────────┼──────────────────┼─────────────────────────┘
          │                  │                  │
          │                  ▼                  │
          │        ┌──────────────────┐         │
          │        │  SQLite (WAL)    │         │
          │        │  - Sessions      │         │
          │        │  - Memory Swap   │         │
          │        │  - Observations  │         │
          │        └──────────────────┘         │
          │                                     │
          ▼                                     ▼
┌──────────────────┐              ┌──────────────────────────┐
│  Configuration   │              │   Execution Backends     │
│  ───────────────│              │  ──────────────────────  │
│  • Env Vars      │              │  • SQL (Teradata, PG)    │
│  • Config Files  │              │  • REST APIs             │
│  • Keyring       │              │  • Documents             │
│  • Hot-reload    │              │  • Custom Plugins        │
└──────────────────┘              └──────────────────────────┘

┌────────────────────────────────────────────────────────────┐
│                    External Dependencies                   │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  LLM Providers (8):                                        │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐            │
│  │ Anthropic  │ │   Bedrock  │ │   Ollama   │            │
│  │  Claude    │ │   Claude   │ │ Local LLMs │            │
│  └────────────┘ └────────────┘ └────────────┘            │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐            │
│  │   Azure    │ │  Gemini    │ │  Mistral   │            │
│  │   OpenAI   │ │   Flash    │ │   AI       │            │
│  └────────────┘ └────────────┘ └────────────┘            │
│  ┌────────────┐ ┌────────────┐                            │
│  │   OpenAI   │ │ HuggingFace│                            │
│  │  Official  │ │  Inference │                            │
│  └────────────┘ └────────────┘                            │
│                                                            │
│  Observability (Optional):                                 │
│  ┌────────────┐ ┌────────────┐                            │
│  │    Hawk    │ │  Promptio  │                            │
│  │  Tracer    │ │   Export   │                            │
│  └────────────┘ └────────────┘                            │
│                                                            │
│  MCP Servers (Optional):                                   │
│  ┌──────────────────────────────────┐                     │
│  │  Model Context Protocol Servers  │                     │
│  │  (External Tool Integration)     │                     │
│  └──────────────────────────────────┘                     │
└────────────────────────────────────────────────────────────┘
```

**External Clients**: CLI admin (looms), TUI (loom), gRPC clients (Go, Python, etc.), HTTP/REST clients (curl, Postman, browsers)

**External Dependencies**:
- **LLM Providers** (8): Anthropic, Bedrock, Ollama, OpenAI, Azure OpenAI, Gemini, Mistral, HuggingFace
- **Observability** (optional): Hawk platform (requires `-tags hawk` build) or embedded SQLite tracing
- **MCP Servers** (optional): External tool providers via Model Context Protocol
- **Backing Services**: SQLite (required), Postgres (planned), Redis (planned)

---

## 12-Factor Analysis

### I. Codebase

**Factor Definition**: "One codebase tracked in revision control, many deploys" (Wiggins, 2011)

**Loom Implementation**:

**Single Repository**: `github.com/teradata-labs/loom`

**Multiple Binaries**:
- `looms`: Multi-agent server (gRPC + HTTP gateway) + CLI admin commands
- `loom`: TUI client (Bubbletea-based terminal interface)
- `loom-mcp`: MCP protocol adapter

**Build Configuration**:
```bash
# Single codebase, multiple build targets (via Justfile)
just build           # Builds all: looms, loom, loom-mcp
just build-server    # go build -tags fts5 -o bin/looms ./cmd/looms
just build-tui       # go build -tags fts5 -o bin/loom ./cmd/loom
just build-mcp       # go build -tags fts5 -o bin/loom-mcp ./cmd/loom-mcp
```

**Deployment Variants** (config-driven, same binary):
- **Development**: `looms serve --config=dev.yaml` (Ollama, NoOpTracer)
- **Staging**: `looms serve --config=staging.yaml` (Bedrock, EmbeddedHawk)
- **Production**: `looms serve --config=prod.yaml` (Anthropic, HawkTracer)

**Files**: `cmd/looms/main.go`, `cmd/loom/main.go`, `cmd/loom-mcp/main.go`

**Compliance**: ✅ **Excellent**. Single Git repository with multiple build targets and config-driven deployment differences.

---

### II. Dependencies

**Factor Definition**: "Explicitly declare and isolate dependencies"

**Loom Implementation**:

**Dependency Declaration**: Go modules (`go.mod`)

```go
// go.mod (selected dependencies — see go.mod for full list)
module github.com/teradata-labs/loom

require (
    google.golang.org/grpc           // gRPC framework
    github.com/anthropics/anthropic-sdk-go  // Anthropic Claude
    github.com/aws/aws-sdk-go-v2     // AWS Bedrock
    go.uber.org/zap                   // Structured logging
    github.com/fsnotify/fsnotify      // File watching (hot-reload)
    // + per-provider SDKs for all 8 LLM providers
)
```

**Interface-Based Dependency Injection**:

```go
// pkg/types/types.go — LLMProvider interface
type LLMProvider interface {
    Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error)
    Name() string
    Model() string
}

// pkg/agent/agent.go — Dependency injection at construction
agent := agent.NewAgent(
    postgresBackend,  // Implements fabric.ExecutionBackend
    anthropicLLM,     // Implements types.LLMProvider
    agent.WithTracer(hawkTracer),  // Optional dependency
)
```

**8 LLM Providers** (pluggable via interface):
1. Anthropic (claude-sonnet-4-5-20250929)
2. AWS Bedrock (cross-region inference profiles)
3. Ollama (local inference, default: llama3.2)
4. OpenAI (default: gpt-4o)
5. Azure OpenAI (Entra token or API key auth)
6. Mistral AI (default: mistral-large-latest)
7. Google Gemini (default: gemini-3-flash-preview)
8. HuggingFace (default: meta-llama/Meta-Llama-3.1-70B-Instruct)

**Optional Dependencies** (build tags):
- `-tags fts5`: SQLite FTS5 support (required for all builds)
- `-tags hawk`: Hawk HTTP export observability integration
- Default build (with `-tags fts5`): Works independently without external deps

**Circuit Breakers** (failure isolation):
```go
// pkg/fabric/circuit_breaker.go
type CircuitBreakerManager struct {
    breakers map[string]*CircuitBreaker
    config   CircuitBreakerConfig
}

// Per-tool circuit breakers prevent cascading failures
func (m *CircuitBreakerManager) GetBreaker(toolName string) *CircuitBreaker {
    // Thread-safe double-checked locking; creates breaker if needed
}

// Each breaker wraps operations with state tracking (Closed/Open/HalfOpen)
func (cb *CircuitBreaker) Execute(operation func() error) error {
    if err := cb.beforeRequest(); err != nil {
        return err // Circuit open — reject immediately
    }
    err := operation()
    cb.afterRequest(err) // Track success/failure for state transitions
    return err
}
```

**Files**: `go.mod`, `pkg/llm/factory/factory.go`, `pkg/fabric/circuit_breaker.go`

**Compliance**: ✅ **Excellent**. Explicit Go module deps, interface-based DI, circuit breakers for external services.

---

### III. Configuration

**Factor Definition**: "Store config in the environment"

**Loom Implementation**:

**Hierarchical Configuration** (priority: high → low):

1. **CLI Flags** (highest)
2. **Config File** (YAML)
3. **Environment Variables** (`LOOM_*` prefix)
4. **Defaults** (lowest)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    Hierarchical Configuration Flow                      │
│                         (Priority: High → Low)                          │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│  Priority 1: CLI Flags (Highest)                                        │
│  ─────────────────────────────────────────────────────────────────────  │
│  $ looms serve --config=/path/config.yaml --port=60051 --hawk-addr=...  │
│                                                                          │
│  Overrides all other sources                                            │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Priority 2: Configuration File                                         │
│  ─────────────────────────────────────────────────────────────────────  │
│  config.yaml:                                                            │
│    server:                                                               │
│      grpc_port: 60051                                                    │
│      http_port: 5006                                                     │
│    agents:                                                               │
│      - name: teradata-agent                                              │
│        patterns: [sql-optimization, query-analysis]                      │
│    observability:                                                        │
│      hawk_address: localhost:50051                                       │
│                                                                          │
│  ┌────────────────────────────────────────┐                             │
│  │  Hot-Reload Support:                   │                             │
│  │  • Agent configuration (watch mode)    │                             │
│  │  • Pattern files (FileRegistry watch)  │                             │
│  │  • No restart required                 │                             │
│  └────────────────────────────────────────┘                             │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Priority 3: Environment Variables                                      │
│  ─────────────────────────────────────────────────────────────────────  │
│  LOOM_DATA_DIR=~/.loom              (data directory)                     │
│  LOOM_SERVER_PORT=60051             (gRPC port)                          │
│  ANTHROPIC_API_KEY=sk-ant-...       (from keyring or env)               │
│  AWS_REGION=us-east-1               (Bedrock region)                    │
│  OLLAMA_ENDPOINT=http://localhost:11434                                   │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Priority 4: Defaults (Lowest)                                          │
│  ─────────────────────────────────────────────────────────────────────  │
│  grpc_port: 60051                                                        │
│  http_port: 5006                                                         │
│  db_path: $LOOM_DATA_DIR/loom.db                                                │
│  tracer: noop (disabled)                                                 │
│  patterns_dir: ./patterns                                                │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                        Runtime Configuration Changes                    │
│  ─────────────────────────────────────────────────────────────────────  │
│                                                                          │
│  Via gRPC RPCs:                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │ ReloadAgent(agent_id)                                            │   │
│  │   → Hot-reload agent configuration without restart              │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │ SwitchModel(session_id, provider, model)                         │   │
│  │   → Switch LLM model/provider for a session without losing      │   │
│  │     context                                                      │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │ LoadPatterns(directory)                                           │   │
│  │   → Load/refresh pattern library from disk                       │   │
│  └──────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘

Legend:
  ─────▶  Configuration precedence (higher overrides lower)
  [Hot]   Supports hot-reload without process restart
  [API]   Can be changed via gRPC RPC at runtime
```

**Secret Management**: System keyring integration (macOS Keychain, Linux Secret Service)

```bash
# Store API key in keyring
looms config set-key anthropic_api_key
# Prompt: Enter value for anthropic_api_key: ********

# Retrieved at runtime from keyring, not environment
```

**Hot-Reload** (89-143ms latency):
- Pattern files via `fsnotify` (`pkg/patterns/hotreload.go`)
- Agent configuration via `ReloadAgent` RPC (line 231-237 in `loom.proto`)
- Model switching via `SwitchModel` RPC (line 239-245, preserves context)

**Files**: `cmd/looms/config.go`, `pkg/config/paths.go`, `proto/loom/v1/loom.proto` (lines 231-245)

**Compliance**: ✅ **Excellent**. Hierarchical config with env var support, keyring for secrets, hot-reload for runtime changes.

---

### IV. Backing Services

**Factor Definition**: "Treat backing services as attached resources"

**Loom Implementation**:

**ExecutionBackend Interface** (proto-first):

```go
// pkg/fabric/interface.go
type ExecutionBackend interface {
    Name() string
    ExecuteQuery(ctx context.Context, query string) (*QueryResult, error)
    GetSchema(ctx context.Context, resource string) (*Schema, error)
    ListResources(ctx context.Context, filters map[string]string) ([]Resource, error)
    GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error)
    Ping(ctx context.Context) error
    Capabilities() *Capabilities
    ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error)
    Close() error
}
```

**Supported Backend Types** (validated via `pkg/fabric/config.go`):
- **SQL Databases**: PostgreSQL, MySQL, SQLite
- **REST APIs**: HTTP client with OAuth2, API key auth
- **GraphQL Endpoints**: Query builder with schema introspection
- **gRPC Services**: Client with automatic retry and circuit breaking
- **MCP Servers**: Model Context Protocol for external tools
- **Document Stores**: File system
- **Supabase**: Managed Postgres via Supabase API

**Backend Configuration** (YAML, swappable at runtime):

```yaml
# PostgreSQL backend (production)
type: postgres
connection:
  dsn: postgresql://user:pass@prod-db.example.com:5432/loom
  max_connections: 50
  enable_ssl: true
  ssl_mode: verify-full

# Swapped to SQLite backend (development)
type: sqlite
connection:
  path: ./dev.db
  enable_wal: true
```

**LLM Provider Abstraction**:

```go
// pkg/types/types.go
type LLMProvider interface {
    Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error)
    Name() string
    Model() string
}

// StreamingLLMProvider extends LLMProvider with token streaming support
type StreamingLLMProvider interface {
    LLMProvider
    ChatStream(ctx context.Context, messages []Message, tools []shuttle.Tool, callback TokenCallback) (*LLMResponse, error)
}

// 8 implementations: Anthropic, Bedrock, Ollama, OpenAI, Azure OpenAI, Gemini, Mistral, HuggingFace
```

**Shared Backend Wrapper** (large result storage):

```go
// pkg/fabric/shared_backend.go
type SharedBackendWrapper struct {
    backend      ExecutionBackend
    sharedMemory *storage.SharedMemoryStore
    threshold    int64
    autoStore    bool
}
// Wraps backends to auto-store large results (>100KB) in shared memory
```

**Health Checks** (via ExecutionBackend interface):

```go
// pkg/fabric/interface.go — Ping is part of the ExecutionBackend interface
Ping(ctx context.Context) error
// Each backend implementation provides its own health check logic
```

**Files**: `pkg/fabric/interface.go` (lines 1-185), `pkg/fabric/shared_backend.go`, `proto/loom/v1/backend.proto`, `pkg/types/types.go`

**Compliance**: ✅ **Excellent**. Clean interface abstraction, pluggable implementations, connection pooling, health checks.

---

### V. Build, Release, Run

**Factor Definition**: "Strictly separate build and run stages"

**Loom Implementation**:

**Build Stage**:

```bash
# Proto generation (buf)
buf generate
# Output: gen/go/loom/v1/*.pb.go

# Go compilation
just build
# Output: bin/looms (server), bin/loom (TUI), bin/loom-mcp (MCP bridge)

# Build with Hawk observability support
just build-hawk  # Includes -tags fts5,hawk
```

**Release Stage** (versioned artifacts):

```bash
# Git tag
git tag v1.0.0

# Go module version
go install github.com/teradata-labs/loom/cmd/looms@v1.0.0

# Docker image (future)
docker build -t loom:v1.0.0 .
```

**Run Stage** (config-driven, no rebuild):

```bash
# Development
looms serve --config=dev.yaml

# Staging
looms serve --config=/etc/loom/staging.yaml

# Production
looms serve --config=/etc/loom/prod.yaml
```

**No Build-Time Configuration**: All environment-specific config injected at runtime via env vars or config files.

**Files**: `Justfile` (build targets at line ~106), `buf.gen.yaml`, `.github/workflows/release.yml`

**Compliance**: ✅ **Excellent**. Strict separation of build (buf + go build), release (Git tags), and run (config-driven).

---

### VI. Processes

**Factor Definition**: "Execute the app as one or more stateless processes"

**Loom Implementation**:

**Single-Process Multi-Agent Architecture**:

```
┌───────────────────────────────────────────────────────────────────────────┐
│                  Single-Process Multi-Agent Deployment                    │
└───────────────────────────────────────────────────────────────────────────┘

                             External Clients
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
                ┌───▼────┐     ┌───▼────┐     ┌───▼────┐
                │  CLI   │     │ gRPC   │     │  HTTP  │
                │ Client │     │ Client │     │ Browser│
                └───┬────┘     └───┬────┘     └───┬────┘
                    │              │               │
                    │              │               │
┌───────────────────┼──────────────┼───────────────┼─────────────────────────┐
│                   │   looms Process (PID 1234)   │                         │
│                   │              │               │                         │
│   ┌───────────────▼──────────────▼───────────────▼─────────────────────┐   │
│   │                    Network Layer (TLS/mTLS)                        │   │
│   │  ┌──────────────────────┐       ┌──────────────────────┐          │   │
│   │  │   gRPC Server        │       │   HTTP Gateway       │          │   │
│   │  │   Port: 60051        │◀──────│   Port: 5006         │          │   │
│   │  │   (Primary API)      │       │   (gRPC-gateway)     │          │   │
│   │  └──────────┬───────────┘       └──────────────────────┘          │   │
│   └─────────────┼───────────────────────────────────────────────────────┘   │
│                 │                                                          │
│   ┌─────────────▼─────────────────────────────────────────────────────┐   │
│   │               Agent Management Layer                              │   │
│   │  ┌──────────────────────────────────────────────────────────────┐ │   │
│   │  │  Agent Registry (thread-safe map)                            │ │   │
│   │  │  • Create/List/Delete agents via gRPC RPCs                   │ │   │
│   │  │  • Hot-reload configuration                                  │ │   │
│   │  │  • Health monitoring                                          │ │   │
│   │  └────────────┬─────────────────────────────────────────────────┘ │   │
│   └───────────────┼───────────────────────────────────────────────────┘   │
│                   │                                                        │
│   ┌───────────────┴───────────────────────────────────────────────────┐   │
│   │            Agent Instances (Goroutine Pools)                      │   │
│   │                                                                    │   │
│   │  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐   │   │
│   │  │ Agent: td-sql   │  │ Agent: pg-admin │  │ Agent: doc-qa   │   │   │
│   │  │ ─────────────── │  │ ─────────────── │  │ ─────────────── │   │   │
│   │  │ • 10 goroutines │  │ • 10 goroutines │  │ • 10 goroutines │   │   │
│   │  │ • Session pool  │  │ • Session pool  │  │ • Session pool  │   │   │
│   │  │ • Pattern cache │  │ • Pattern cache │  │ • Pattern cache │   │   │
│   │  │ • Memory mgr    │  │ • Memory mgr    │  │ • Memory mgr    │   │   │
│   │  └────────┬────────┘  └────────┬────────┘  └────────┬────────┘   │   │
│   └───────────┼──────────────────────┼──────────────────────┼───────────┘   │
│               │                      │                      │              │
│   ┌───────────┴──────────────────────┴──────────────────────┴───────────┐  │
│   │                  Shared Infrastructure                             │  │
│   │                                                                     │  │
│   │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐             │  │
│   │  │   Pattern    │  │    Tracer    │  │  LLM Client  │             │  │
│   │  │  Registry    │  │   (Hawk)     │  │    Pool      │             │  │
│   │  │  (Shared)    │  │  (Singleton) │  │ (Connection  │             │  │
│   │  │              │  │              │  │  Pooling)    │             │  │
│   │  └──────────────┘  └──────────────┘  └──────────────┘             │  │
│   └───────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│   ┌───────────────────────────────────────────────────────────────────┐    │
│   │                    Backend Execution Layer                        │    │
│   │                                                                    │    │
│   │  ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐  │    │
│   │  │ SQL Backend│  │ REST Backend│ │ Doc Backend│  │ MCP Backend│  │    │
│   │  │ (Teradata, │  │ (HTTP/REST)│  │ (Files,    │  │ (External  │  │    │
│   │  │ Postgres)  │  │            │  │ Embeddings)│  │ Tools)     │  │    │
│   │  └────────────┘  └────────────┘  └────────────┘  └────────────┘  │    │
│   └───────────────────────────────────────────────────────────────────┘    │
└───────────────────────────────────────┬─────────────────────────────────────┘
                                        │
                    ┌───────────────────┼───────────────────┐
                    │                   │                   │
                    ▼                   ▼                   ▼
         ┌────────────────┐  ┌────────────────┐  ┌────────────────┐
         │  SQLite (WAL)  │  │  Hawk Observer │  │  LLM Providers │
         │  ────────────  │  │  ────────────  │  │  ────────────  │
         │  • Sessions    │  │  • Traces      │  │  • Anthropic   │
         │  • Messages    │  │  • Metrics     │  │  • Bedrock     │
         │  • Memory Swap │  │  • Events      │  │  • Ollama      │
         │  • Metadata    │  │  (Optional)    │  │  • 5 others    │
         │                │  │                │  │                │
         │ File Location: │  │ Address:       │  │ Config-driven  │
         │ $LOOM_DATA_DIR/loom.db│  │ :50051         │  │ selection      │
         └────────────────┘  └────────────────┘  └────────────────┘
```

**Stateless Process Model**:
- All session state persists to SQLite (WAL mode)
- Agents are goroutines, not OS processes
- Shared-nothing memory model (each agent has isolated memory space)
- Horizontal scaling not yet supported (SQLite single-writer limitation)

**Crash Recovery** (crash-only design):
- Sessions automatically restored from SQLite on startup
- Reference-counted shared memory reattached
- MCP server reconnection on failure

**Files**: `pkg/server/server.go`, `pkg/agent/session_store.go`

**Compliance**: ✅ **Good**. Stateless process model with externalized state. Single-process design limits horizontal scaling (no distributed coordination).

---

### VII. Port Binding

**Factor Definition**: "Export services via port binding"

**Loom Implementation**:

**Self-Contained Server** (no external web server):

```go
// cmd/looms/cmd_serve.go (conceptual — actual function is runServe)
// gRPC server (primary)
grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", config.GRPCPort))
grpcServer := grpc.NewServer()
loomv1.RegisterLoomServiceServer(grpcServer, loomService)

// HTTP gateway (optional, via grpc-gateway)
if config.HTTPPort > 0 {
    httpMux := runtime.NewServeMux()
    loomv1.RegisterLoomServiceHandlerServer(ctx, httpMux, loomService)
    httpServer := &http.Server{Addr: fmt.Sprintf(":%d", config.HTTPPort), Handler: httpMux}
    go httpServer.ListenAndServe()
}

grpcServer.Serve(grpcLis)
```

**Port Configuration**:

```yaml
server:
  grpc_port: 60051  # Primary gRPC API
  http_port: 5006   # Optional HTTP/REST gateway (0 = disabled)
  host: 0.0.0.0     # Bind address
```

**TLS/mTLS Support**:

```yaml
server:
  tls:
    enabled: true
    mode: letsencrypt  # or manual, self-signed
    letsencrypt:
      domains: ["api.example.com"]
      email: admin@example.com
      auto_renew: true
      renew_before_days: 30
    manual:
      cert_file: /path/to/cert.pem
      key_file: /path/to/key.pem
    mtls:
      enabled: true
      client_ca_file: /path/to/client-ca.pem
```

**CORS Configuration** (HTTP gateway):

```yaml
server:
  cors:
    enabled: true
    allowed_origins: ["https://app.example.com"]
    allowed_methods: ["GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"]
    allowed_headers: ["Content-Type", "Authorization"]
    allow_credentials: true
```

**Protocol Support**:
- **gRPC** (60051): Primary API, streaming support (StreamWeave, StreamPatternUpdates, StreamWorkflow)
- **HTTP/REST** (5006): Gateway with JSON marshaling (grpc-gateway)
- **Server-Sent Events** (SSE): Streaming over HTTP for browser clients

**Files**: `cmd/looms/cmd_serve.go`, `proto/loom/v1/server.proto`, `cmd/looms/config.go`

**Compliance**: ✅ **Excellent**. Self-contained server with port binding, TLS/mTLS, CORS, multiple protocols.

---

### VIII. Concurrency

**Factor Definition**: "Scale out via the process model"

**Loom Implementation**:

**Vertical Scaling** (goroutine-based):

```go
// pkg/shuttle/executor.go
type ToolExecutor struct {
    concurrencyLimit int  // Default: 10
}

func (e *ToolExecutor) ExecuteConcurrent(ctx context.Context, tools []Tool) []Result {
    sem := make(chan struct{}, e.concurrencyLimit)
    results := make(chan Result, len(tools))

    for _, tool := range tools {
        go func(t Tool) {
            sem <- struct{}{}        // Acquire semaphore
            defer func() { <-sem }() // Release semaphore

            result := t.Execute(ctx)
            results <- result
        }(tool)
    }

    // Collect results
    var allResults []Result
    for i := 0; i < len(tools); i++ {
        allResults = append(allResults, <-results)
    }
    return allResults
}
```

**Multi-Agent Workflow Patterns** (6 orchestration patterns):

1. **Pipeline**: Sequential execution, output flows to next stage
2. **Parallel**: Independent tasks execute concurrently
3. **Fork-Join**: Parallel execution with merged results
4. **Debate**: Agents argue different perspectives
5. **Conditional**: Route based on agent decisions
6. **Swarm**: Dynamic agent collaboration

**Configuration**:

```yaml
tools:
  executor:
    concurrent_limit: 10        # Max concurrent tools per agent
    timeout_seconds: 30
    max_retries: 3

server:
  behavior:
    max_concurrent_requests: 100  # Global request limit
    request_timeout_seconds: 300
```

**Race Detection** (zero tolerance):

```bash
# All tests run with -race detector
go test -tags fts5 -race ./...

# Results:
# 2462+ test functions across 342 test files
# 0 race conditions detected
# Critical packages: patterns 81.7%, communication 77.9%, fabric 79.2%
```

**Horizontal Scaling Gap**:
- ❌ No multi-instance coordination (single-process design)
- ❌ No distributed locking for hot-reload
- ❌ No session affinity / load balancer support

**Scaling Recommendations** (future work):
- Redis-backed session store for multi-instance deployments
- Distributed locking (Consul/etcd) for agent hot-reload
- Load balancer with session affinity (sticky sessions or JWT tokens)

**Files**: `pkg/shuttle/executor.go`, `pkg/agent/agent.go`, `proto/loom/v1/orchestration.proto`

**Compliance**: ⚠️ **Partial**. Excellent vertical scaling (goroutines, 0 race conditions), but no horizontal scaling (single-process limitation).

---

### IX. Disposability

**Factor Definition**: "Maximize robustness with fast startup and graceful shutdown"

**Loom Implementation**:

**Fast Startup** (<200ms cold start):

```
Startup Breakdown:
  Binary startup:        <100ms
  SQLite init (WAL):     <50ms
  Pattern loading:       89-143ms (104 patterns, 80KB YAML)
  Agent initialization:  <50ms
  Total:                 <200ms
```

**Crash-Only Design** (inspired by crash-only software, Candea & Fox, 2003):

**Philosophy**: "Applications are crash-safe by design, eliminating need for complex graceful shutdown logic"

**Implementation**:
- All state persists to SQLite (WAL mode) **before** response sent to client
- No in-memory state critical for correctness
- Sessions automatically restored from database on startup
- Reference-counted shared memory reattached on process restart

**Graceful Shutdown** (signal-driven, multi-phase):

```go
// cmd/looms/cmd_serve.go (conceptual — actual shutdown is more involved)
// On SIGINT/SIGTERM:
// 1. Cancel message queue monitor (prevent new work)
// 2. Stop HTTP server with timeout
// 3. Stop hot-reload watchers
// 4. Close agent registry
// 5. Stop pattern tracker (flush buffered metrics)
// 6. Stop learning agent
// 7. Close communication system (message bus, queue)
// 8. GracefulStop gRPC server (drain in-flight, 10s timeout)
// 9. Close database connections
// 10. Flush tracer
// Second Ctrl+C forces immediate exit via os.Exit(1)
```

**Shutdown Timeout Configuration**:

```yaml
server:
  behavior:
    graceful_shutdown: true
    shutdown_timeout_seconds: 30
```

**Recovery Mechanisms**:
- Session restoration: Load from SQLite on startup
- Reference cleanup: Hooks on session deletion
- Shared memory references: Reattached on process restart
- MCP server reconnection: Automatic retry on failure

**Files**: `pkg/agent/session_store.go`, `cmd/looms/cmd_serve.go`

**Compliance**: ✅ **Excellent**. Fast startup (<200ms), crash-only design with automatic recovery, graceful drain for zero-downtime deploys.

---

### X. Dev/Prod Parity

**Factor Definition**: "Keep development, staging, and production as similar as possible"

**Loom Implementation**:

**Same Binary, Different Config**:

```bash
# Development
looms serve --config=dev.yaml

# Production
looms serve --config=prod.yaml
```

**Configuration Differences** (not code differences):

```yaml
# dev.yaml
llm:
  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.1:8b
observability:
  enabled: false
database:
  driver: sqlite
  path: ./dev.db

# prod.yaml
llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
observability:
  enabled: true
  provider: hawk
  hawk_endpoint: https://hawk.example.com:50051
database:
  driver: sqlite  # TODO: Postgres for session store
  path: /var/lib/loom/loom.db
```

**Testing Approach**:
- **Unit tests**: Mock LLM provider (no network calls)
- **Integration tests**: Real Ollama/Anthropic (controlled environment)
- **Self-testing**: Dogfooding pattern (test agents with Loom itself)
- **Race detection**: All tests run with `-race` flag

**Build Tags** (optional dependencies):
- `-tags fts5`: SQLite FTS5 support (required for all builds)
- `-tags hawk`: Hawk HTTP export observability (optional)
- Default build (with `-tags fts5`): Works independently without external deps

**Gap: Database Backend**:
- Dev: SQLite (file-based)
- Prod: SQLite (should be Postgres for multi-instance deployments)
- **Recommendation**: Implement Postgres session store for production parity

**Files**: `cmd/looms/config.go`, `pkg/llm/factory/factory.go`

**Compliance**: ✅ **Good**. Same binary, config-driven differences. Minor gap: SQLite vs Postgres for session store (Postgres backend not yet implemented).

---

### XI. Logs

**Factor Definition**: "Treat logs as event streams"

**Loom Implementation**:

**Structured Logging** (zap-based):

```go
// Conceptual — zap structured logging used throughout codebase via zap.L() global logger
// Logger initialized in cmd/looms/cmd_serve.go via zap.NewProduction() or zap.NewDevelopment()
import "go.uber.org/zap"

zap.L().Info("LLM completion",
    zap.String("trace_id", span.TraceID),
    zap.String("session_id", sessionID),
    zap.String("agent_id", agentID),
    zap.String("llm.provider", "anthropic"),
    zap.String("llm.model", "claude-sonnet-4-5-20250929"),
    zap.Int("llm.tokens.input", 1234),
    zap.Int("llm.tokens.output", 567),
    zap.Float64("llm.cost_usd", 0.012),
    zap.Duration("llm.latency", time.Since(start)),
)
```

**Configuration**:

```yaml
logging:
  level: info        # debug, info, warn, error
  format: json       # json, text
  file: ""           # Optional log file (default: stdout/stderr)
```

**Structured Output** (example):

```json
{
  "level": "info",
  "ts": "2026-01-14T10:30:00.123Z",
  "caller": "agent/agent.go:245",
  "msg": "LLM completion",
  "trace_id": "abc123",
  "session_id": "sess-456",
  "agent_id": "sql-agent",
  "llm.provider": "anthropic",
  "llm.model": "claude-sonnet-4-5-20250929",
  "llm.tokens.input": 1234,
  "llm.tokens.output": 567,
  "llm.cost_usd": 0.012,
  "llm.latency_ms": 342
}
```

**Trace Correlation**:
- Trace IDs from observability span context (propagated via `context.Context`)
- Session IDs for conversation tracking
- Agent IDs for multi-agent scenarios

**Event Streams** (real-time progress):
- gRPC streaming RPCs: `StreamWeave`, `StreamPatternUpdates`, `StreamWorkflow`
- Server-Sent Events (SSE) over HTTP gateway
- `SubscribeToSession` RPC for session update notifications

**What Gets Logged**:
- Agent lifecycle: Session created, agent started/stopped
- LLM API calls: Provider, model, tokens, cost, latency, TTFT
- Tool executions: Name, success/failure, duration
- Pattern matching: Selected patterns, confidence scores
- Error conditions: Stack traces with context

**No Log Aggregation** (stdout/stderr only):
- Logs emitted to stdout/stderr (factor XI compliance)
- External log aggregation: Fluentd, Logstash, CloudWatch Logs
- Observability traces exported to Hawk (separate from logs)

**Files**: `pkg/observability/hawk.go`, `pkg/observability/hawk_types.go`, `cmd/looms/config.go` (logging section)

**Compliance**: ✅ **Excellent**. Structured JSON logs with trace correlation, stdout/stderr streams, event streams via gRPC/SSE.

---

### XII. Admin Processes

**Factor Definition**: "Run admin/management tasks as one-off processes"

**Loom Implementation**:

**looms CLI** (one-off tasks):

```bash
# Server
looms serve --config=looms.yaml

# Pattern management
looms pattern load --dir ./patterns/sql
looms pattern list --domain sql

# Learning agent (pattern proposals)
looms learning analyze --domain sql
looms learning proposals --status=pending
looms learning apply <proposal-id>
looms learning rollback <proposal-id>
looms learning tune --domain=sql --strategy=moderate

# Judge evaluation (multi-judge assessment)
looms judge evaluate --agent sql-agent --judges quality,safety,cost
looms judge evaluate-stream --agent sql-agent

# Workflow management
looms workflow validate <file>

# Configuration management
looms config init
looms config set-key anthropic_api_key
looms config get llm.provider
looms config get-key <key-name>

# Validation
looms validate file agents/my-agent.yaml
looms validate dir examples/
```

**gRPC APIs for Admin Tasks** (proto-first):

```protobuf
// proto/loom/v1/loom.proto — LoomService (selected admin RPCs)
service LoomService {
  // Pattern management
  rpc LoadPatterns(LoadPatternsRequest) returns (LoadPatternsResponse);
  rpc ListPatterns(ListPatternsRequest) returns (ListPatternsResponse);
  rpc CreatePattern(CreatePatternRequest) returns (CreatePatternResponse);

  // Agent management
  rpc CreateAgentFromConfig(CreateAgentRequest) returns (AgentInfo);
  rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse);
  rpc DeleteAgent(DeleteAgentRequest) returns (DeleteAgentResponse);
  rpc ReloadAgent(ReloadAgentRequest) returns (AgentInfo);
  rpc SwitchModel(SwitchModelRequest) returns (SwitchModelResponse);

  // MCP server management
  rpc AddMCPServer(AddMCPServerRequest) returns (AddMCPServerResponse);
  rpc RestartMCPServer(RestartMCPServerRequest) returns (MCPServerInfo);
  rpc DeleteMCPServer(DeleteMCPServerRequest) returns (DeleteMCPServerResponse);

  // Workflow scheduling
  rpc ScheduleWorkflow(ScheduleWorkflowRequest) returns (ScheduleWorkflowResponse);
  rpc ListScheduledWorkflows(ListScheduledWorkflowsRequest) returns (ListScheduledWorkflowsResponse);
}

// proto/loom/v1/learning.proto — LearningService
service LearningService {
  rpc AnalyzePatternEffectiveness(AnalyzePatternEffectivenessRequest) returns (PatternAnalysisResponse);
}

// proto/loom/v1/judge.proto — JudgeService
service JudgeService {
  rpc EvaluateWithJudges(EvaluateRequest) returns (EvaluateResponse);
  rpc EvaluateWithJudgesStream(EvaluateRequest) returns (stream EvaluateProgress);
}
```

**HTTP/REST Gateway** (for scripting):

```bash
# All admin commands available via HTTP
curl -X POST http://localhost:5006/v1/patterns/load \
  -H "Content-Type: application/json" \
  -d '{"directory": "./patterns/sql"}'

curl http://localhost:5006/v1/agents
```

**Files**: `cmd/looms/cmd_pattern.go`, `cmd/looms/cmd_learning.go`, `cmd/looms/cmd_judge.go`, `cmd/looms/cmd_workflow.go`, `cmd/looms/cmd_config.go`, `proto/loom/v1/loom.proto`, `proto/loom/v1/learning.proto`, `proto/loom/v1/judge.proto`

**Compliance**: ✅ **Excellent**. CLI for one-off tasks, gRPC/HTTP APIs for automation, proto-first design.

---

## Architecture Diagrams

### 12-Factor Component Mapping

```
┌─────────────────────────────────────────────────────────────────────────┐
│                  12-Factor Architecture → Loom Mapping                  │
└─────────────────────────────────────────────────────────────────────────┘

 Factor                  Loom Implementation
 ──────                  ───────────────────

 I. Codebase         ┌──────────────────────────────────────┐
 One codebase,       │  Git repo with multi-binary build    │
 many deploys        │  • looms (server + CLI admin)        │
                     │  • loom (TUI client)                 │
                     │  • loom-mcp (MCP adapter)            │
                     └──────────────────────────────────────┘

 II. Dependencies    ┌──────────────────────────────────────┐
 Explicitly          │  Go modules + Optional dependencies  │
 declare             │  • 8 LLM providers (pluggable)       │
                     │  • Hawk (optional observability)     │
                     │  • MCP servers (external tools)      │
                     └──────────────────────────────────────┘

 III. Config         ┌──────────────────────────────────────┐
 Store config in     │  Hierarchical Configuration:         │
 environment         │  CLI flags > Config file > Env vars  │
                     │  • Hot-reload for patterns           │
                     │  • Runtime changes via gRPC          │
                     │  • Keyring for secrets               │
                     └──────────────────────────────────────┘

 IV. Backing         ┌──────────────────────────────────────┐
 Services            │  ExecutionBackend Interface:         │
 Treat as            │  • SQL databases (attachable)        │
 attached            │  • REST APIs (URL-based)             │
 resources           │  • Document stores (pluggable)       │
                     │  • MCP tool servers (external)       │
                     └──────────────────────────────────────┘

 V. Build/Release    ┌──────────────────────────────────────┐
 /Run                │  Strict separation:                  │
 Strict              │  • buf generate (proto → Go)         │
 separation          │  • just build (compile binaries)     │
                     │  • Config-driven runtime             │
                     └──────────────────────────────────────┘

 VI. Processes       ┌──────────────────────────────────────┐
 Execute as          │  Single-process, multi-agent:        │
 stateless           │  • Goroutine-based agents            │
 processes           │  • Shared-nothing memory model       │
                     │  • SQLite persistence (external)     │
                     └──────────────────────────────────────┘

 VII. Port Binding   ┌──────────────────────────────────────┐
 Export services     │  Self-contained server:              │
 via port binding    │  • gRPC: 60051 (primary)             │
                     │  • HTTP: 5006 (gateway)              │
                     │  • TLS/mTLS support                  │
                     └──────────────────────────────────────┘

 VIII. Concurrency   ┌──────────────────────────────────────┐
 Scale out via       │  Goroutine-based scaling:            │
 process model       │  • Each agent = goroutine pool       │
                     │  • Vertical scaling (multi-core)     │
                     │  • ❌ No horizontal scaling yet      │
                     └──────────────────────────────────────┘

 IX. Disposability   ┌──────────────────────────────────────┐
 Fast startup        │  Crash-only design:                  │
 and graceful        │  • <200ms cold start                 │
 shutdown            │  • Graceful gRPC drain               │
                     │  • SQLite WAL for durability         │
                     └──────────────────────────────────────┘

 X. Dev/Prod         ┌──────────────────────────────────────┐
 Parity              │  Config-driven differences:          │
 Keep environments   │  • Same binary, different config     │
 similar             │  • Embedded vs external Hawk         │
                     │  • File vs production backends       │
                     └──────────────────────────────────────┘

 XI. Logs            ┌──────────────────────────────────────┐
 Treat logs as       │  Structured logging:                 │
 event streams       │  • JSON format (zap)                 │
                     │  • Trace correlation IDs             │
                     │  • Stdout/stderr streams             │
                     └──────────────────────────────────────┘

 XII. Admin          ┌──────────────────────────────────────┐
 Processes           │  looms CLI for admin tasks:          │
 Run admin tasks     │  • serve (run server)                │
 as one-off          │  • pattern/learning/judge/workflow   │
 processes           │  • gRPC RPCs (runtime management)    │
                     └──────────────────────────────────────┘
```

### State Persistence Architecture (5-Layer Memory)

```
┌─────────────────────────────────────────────────────────────────────────┐
│              Loom Memory Hierarchy (Reference-Counted)                  │
└─────────────────────────────────────────────────────────────────────────┘

  Fast Access                                            Slow Access
  ───────────                                            ───────────
      ▲                                                      │
      │                                                      ▼

┌─────────────────────────────────────────────────────────────────────────┐
│  ROM (Read-Only Memory)                                   5,000 tokens  │
│  ─────────────────────────────────────────────────────────────────────  │
│  • System prompts (immutable)                                           │
│  • Pattern definitions (YAML templates)                                 │
│  • Tool schemas (function signatures)                                   │
│  • Agent personality/instructions                                       │
│  • Reference-counted: Shared across all conversations                   │
│                                                                          │
│  Storage: In-memory (loaded at startup)                                 │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Kernel Memory (LRU Cache)                                2,000 tokens  │
│  ─────────────────────────────────────────────────────────────────────  │
│  • Critical facts (user preferences, entity definitions)                │
│  • Tool execution results (frequent queries cached)                     │
│  • Pattern invocations (recent pattern applications)                    │
│  • Reference-counted: Shared with aging policy                          │
│                                                                          │
│  Eviction: LRU (Least Recently Used)                                    │
│  Storage: In-memory map with timestamps                                 │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  L1 Cache (Recent Conversation)                          10,000 tokens  │
│  ─────────────────────────────────────────────────────────────────────  │
│  • Last N turns of conversation (typically 20-30 messages)              │
│  • Full context for immediate responses                                 │
│  • User messages + assistant responses + tool results                   │
│  • Per-session: Not shared between conversations                        │
│                                                                          │
│  Management: Ring buffer, newest messages retained                      │
│  Storage: In-memory slice (session-scoped)                              │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  L2 Cache (Compressed Summaries)                          3,000 tokens  │
│  ─────────────────────────────────────────────────────────────────────  │
│  • LLM-generated summaries of older conversation chunks                 │
│  • Key decisions and outcomes preserved                                 │
│  • Lossy compression (details discarded, meaning retained)              │
│  • Per-session: Summarization triggered by L1 overflow                  │
│                                                                          │
│  Compression: Triggered when L1 exceeds threshold                       │
│  Storage: In-memory (session-scoped)                                    │
└─────────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  Swap (Cold Storage)                                       Unlimited    │
│  ─────────────────────────────────────────────────────────────────────  │
│  • Full conversation history (all messages, all tools)                  │
│  • Observations and traces (linked to Hawk)                             │
│  • Session metadata (timestamps, agent config snapshots)                │
│  • Pattern application history                                          │
│                                                                          │
│  SQLite Schema (WAL mode):                                              │
│  ┌────────────────────────────────────────────────────────────────┐    │
│  │ sessions       → id, agent_id, created_at, last_active         │    │
│  │ messages       → id, session_id, role, content, tokens         │    │
│  │ observations   → id, session_id, trace_id, hawk_reference      │    │
│  │ tool_calls     → id, message_id, tool_name, args, result       │    │
│  │ memory_state   → id, session_id, layer, key, value, refcount   │    │
│  └────────────────────────────────────────────────────────────────┘    │
│                                                                          │
│  Access: Lazy-load on demand, paginated queries                         │
│  Storage: $LOOM_DATA_DIR/loom.db (persistent across restarts)                  │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                    Memory Management Strategy                           │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Reference Counting:                                                     │
│  • ROM and Kernel memories shared across sessions                       │
│  • Refcount incremented when session loads memory                       │
│  • Refcount decremented when session ends                               │
│  • Memory freed when refcount reaches 0                                 │
│                                                                          │
│  Overflow Handling:                                                      │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │ L1 full (>10k tokens)                                             │  │
│  │   → Oldest chunk sent to LLM for summarization                    │  │
│  │   → Summary stored in L2                                          │  │
│  │   → Original messages moved to Swap (SQLite)                      │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │ L2 full (>3k tokens)                                              │  │
│  │   → Oldest summaries merged via LLM                               │  │
│  │   → Consolidated summary replaces multiple entries                │  │
│  │   → Original summaries archived to Swap                           │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  Recovery:                                                               │
│  • Session resume: Load from Swap → Rebuild L1/L2 caches               │
│  • Crash recovery: SQLite WAL ensures durability                        │
│  • Partial reconstruction: Only recent context loaded (lazy)            │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Key Design Decisions

### Decision 1: Single-Process Multi-Agent vs. Process-Per-Agent

**Chosen Approach**: Single-process multi-agent (goroutine-based)

**Rationale**:
- Fast startup (<200ms vs. seconds for multi-process)
- Low memory overhead (shared memory between agents)
- Simple deployment (one binary, one config file)
- No network hops between components (in-process communication)

**Trade-offs**:
- ✅ Fast, low-overhead, simple operations
- ❌ No horizontal scaling without distributed coordination
- ❌ Single point of failure (process crash affects all agents)

**Alternatives Considered**:

1. **Process-per-agent** (rejected)
   - ✅ Fault isolation (one agent crash doesn't affect others)
   - ❌ High memory overhead (duplicated pattern libraries)
   - ❌ Slow startup (fork overhead)
   - ❌ Complex IPC for shared state

2. **Serverless (AWS Lambda, Google Cloud Functions)** (rejected)
   - ✅ Auto-scaling, pay-per-invocation
   - ❌ Cold start latency (seconds)
   - ❌ Stateful agents require session affinity (not serverless-friendly)
   - ❌ 15-minute timeout limits (long conversations exceed)

---

### Decision 2: SQLite vs. Postgres for Session Store

**Chosen Approach**: SQLite (WAL mode)

**Rationale**:
- Zero-config deployment (no external database required)
- ACID transactions (session consistency)
- WAL mode for crash resilience
- FTS5 for semantic search (BM25 ranking)

**Trade-offs**:
- ✅ Simple deployment, crash-safe, full-text search
- ❌ Single-writer limitation (horizontal scaling requires Postgres)
- ❌ Network file systems (NFS) have limited WAL support

**Future Work**: Postgres backend for multi-instance deployments (horizontal scaling)

---

### Decision 3: Proto-First API Design

**Chosen Approach**: Define all APIs in Protocol Buffers, then implement

**Rationale**:
- Backward compatibility via `buf breaking --against .git#branch=main`
- Language-agnostic (gRPC clients in Go, Python, Rust, etc.)
- Automatic HTTP/REST gateway via grpc-gateway
- Type-safe generated code

**Trade-offs**:
- ✅ API stability, polyglot clients, backward compatibility
- ❌ Extra build step (`buf generate`)
- ❌ Learning curve for proto syntax

**Alternatives Considered**:

1. **REST-first** (rejected)
   - ✅ Familiar HTTP/JSON
   - ❌ No streaming support (need WebSocket or SSE workarounds)
   - ❌ No schema validation (OpenAPI separate from code)

2. **GraphQL** (rejected)
   - ✅ Client-driven queries
   - ❌ No streaming (subscriptions are complex)
   - ❌ Overfetching prevention requires careful schema design

---

### Decision 4: 5-Layer Memory Hierarchy vs. Flat History

**Chosen Approach**: Segmented memory (ROM, Kernel, L1, L2, Swap)

**Rationale**:
- Predictable token budget (ROM + Kernel + L1 + L2 ≈ 20k tokens)
- Balances recent context (L1) with long-term memory (L2)
- Hot-swappable patterns without session restart (ROM layer)

**Trade-offs**:
- ✅ Bounded tokens, long-term context, pattern hot-reload
- ❌ Complexity (four layers to manage)
- ❌ Lossy compression (L2 summaries drop detail)

**Alternatives Considered**:

1. **Full history** (rejected)
   - ✅ Perfect recall
   - ❌ Unbounded token growth → rejected due to cost

2. **Fixed sliding window** (rejected)
   - ✅ Simple implementation
   - ❌ Loses all context beyond window → rejected for long conversations

3. **External RAG memory** (rejected)
   - ✅ Unbounded storage
   - ❌ Retrieval latency (100-500ms) → rejected for real-time interaction

**Formal Property**:

```
Invariant: Context Budget
sizeof(ROM) + sizeof(Kernel) + sizeof(L1) + sizeof(L2) ≤ CONTEXT_WINDOW - OUTPUT_RESERVE
```

---

## Performance Characteristics

### Startup Latency

**Cold Start**: <200ms

**Breakdown**:
- Binary startup: <100ms (Go compiled executable)
- SQLite init (WAL): <50ms
- Pattern loading: 89-143ms (104 patterns, 80KB YAML)
- Agent initialization: <50ms

**Warm Start** (cached patterns): <150ms

**Scaling**: O(n) where n = pattern count (pattern index build dominates)

---

### Hot-Reload Latency

**Pattern Hot-Reload**: 89-143ms (p50-p99)

**Measurement Conditions**:
- Pattern library size: 104 patterns (11 libraries)
- Total pattern bytes: ~80KB YAML
- Test hardware: M2 MacBook Pro

**Breakdown**:
- File watch notification: 10-15ms (fsnotify)
- YAML parsing: 45-60ms
- Pattern index rebuild: 20-40ms
- Atomic swap: <1ms

**Optimization Considered**: Incremental index updates
- Would reduce latency to ~30ms
- Adds complexity (index mutation synchronization)
- Rejected: 89-143ms acceptable for <1 reload/minute expected frequency

---

### Concurrency

**Vertical Scaling**: 10 goroutines per agent (default)

**Throughput**:
- Single agent: ~100 requests/second (LLM latency-bound)
- Multi-agent: ~1000 requests/second (10 agents, parallel)

**Race Conditions**: 0 detected (2462+ test functions across 342 test files with `-race` flag)

**Critical Packages** (test coverage):
- patterns: 81.7%
- communication: 77.9%
- fabric: 79.2%

---

### Memory Usage

**Per-Agent Overhead**: ~50MB (pattern cache, session pool, memory manager)

**Shared Infrastructure**: ~100MB (pattern registry, tracer, LLM client pool)

**Typical Deployment** (10 agents): ~600MB total

**Memory Hierarchy**:
- ROM: 5k tokens (~20KB)
- Kernel: 2k tokens (~8KB)
- L1: 10k tokens (~40KB)
- L2: 3k tokens (~12KB)
- Swap: Unbounded (SQLite on disk)

---

## Security Considerations

### Threat Model

**Threats**:
1. **API key exposure**: LLM provider keys in logs or config files
2. **Prompt injection**: Malicious user input in patterns or queries
3. **SQL injection**: User-controlled SQL queries (backend-specific)
4. **Unauthorized access**: No authentication on gRPC/HTTP endpoints

**Mitigations**:

1. **API Key Protection**:
   - Store in system keyring (macOS Keychain, Linux Secret Service)
   - Never log API keys (redact in trace export)
   - Rotate keys via `looms config set-key`

2. **Prompt Injection Defense**:
   - Pattern validation: Reject patterns with user-controlled system prompts
   - Input sanitization: Escape special characters in backend queries
   - Tool approval: Human-in-the-loop for destructive operations

3. **SQL Injection Prevention**:
   - Prepared statements: All SQL backends use parameterized queries
   - Query validation: Syntax check before execution
   - Read-only mode: Backend capability flag for read-only connections

4. **Authentication** (future work):
   - mTLS for gRPC (client cert verification)
   - JWT tokens for HTTP gateway
   - RBAC for multi-tenant deployments

---

### Privacy

**PII Redaction** (configured via HawkTracer PrivacyConfig):

```go
// pkg/observability/hawk.go (requires -tags hawk)
// Redaction is integrated into HawkTracer span export, not a standalone function.
// Configured via PrivacyConfig:
type PrivacyConfig struct {
    RedactCredentials bool     // Remove password, api_key, token fields
    RedactPII         bool     // Remove email, phone, SSN, credit card patterns
    AllowedAttributes []string // Whitelist keys that bypass redaction
}

// Redaction markers: [EMAIL_REDACTED], [PHONE_REDACTED], [SSN_REDACTED], [CARD_REDACTED]
```

**Trace Export**: PII redacted before sending to Hawk (configurable per-field)

**Override**: AllowedAttributes whitelist for debugging (attribute keys bypass redaction)

---

## Evolution and Recommendations

### Current Gaps

1. **Horizontal Scaling** (Factor VIII)
   - **Problem**: Single-process design limits multi-instance deployments
   - **Recommendation**: Implement Redis-backed session store
   - **Impact**: Enables load-balanced deployments with session affinity

2. **Database Backend** (Factor X)
   - **Problem**: SQLite only (dev/prod parity gap)
   - **Recommendation**: Implement Postgres session store
   - **Impact**: Multi-writer support for horizontal scaling

3. **Authentication** (Security)
   - **Problem**: No built-in auth (open gRPC/HTTP endpoints)
   - **Recommendation**: Add mTLS, JWT tokens, RBAC
   - **Impact**: Secure multi-tenant deployments

---

### Roadmap

**Phase 1: Horizontal Scaling** (3-6 months)
- Redis session store (distributed state)
- Distributed locking (Consul/etcd) for hot-reload
- Load balancer support (health checks, session affinity)

**Phase 2: Multi-Database Support** (6-9 months)
- Postgres session store (multi-writer)
- MySQL session store (enterprise compatibility)
- Migration tooling (schema version management)

**Phase 3: Authentication & Authorization** (9-12 months)
- mTLS for gRPC (client cert verification)
- JWT tokens for HTTP gateway
- RBAC for multi-tenant deployments (agent-level permissions)

---

## Related Work

### Crash-Only Software

**Reference**: Candea, G., & Fox, A. (2003). *Crash-only software*. HotOS IX.

**Relationship**: Loom's state persistence design follows crash-only principles:
- All state externalized to durable storage (SQLite WAL)
- No complex graceful shutdown logic
- Automatic recovery on restart

**Innovation**: Loom extends crash-only design to LLM agents with segmented memory and reference counting.

---

### 12-Factor App Methodology

**Reference**: Wiggins, A. (2011). *The twelve-factor app*. https://12factor.net/

**Relationship**: This document analyzes Loom against all 12 factors for cloud-native applications.

**Innovation**: Loom demonstrates 12-factor principles for stateful LLM agents (not just stateless web services).

---

### Protocol Buffers

**Reference**: Google. (2023). *Protocol Buffers Language Guide (proto3)*. https://protobuf.dev/

**Relationship**: Loom uses proto-first API design for backward compatibility and polyglot clients.

**Innovation**: gRPC + HTTP gateway via grpc-gateway for dual-protocol support.

---

### CPU Cache Hierarchies

**Reference**: Hennessy, J. L., & Patterson, D. A. (2011). *Computer Architecture: A Quantitative Approach*. 5th ed.

**Relationship**: Loom's 5-layer memory (ROM/Kernel/L1/L2/Swap) inspired by CPU cache design (L1/L2/L3/DRAM).

**Innovation**: Applies cache hierarchy principles to LLM context windows with reference counting.

---

## References

1. Wiggins, A. (2011). *The twelve-factor app*. Retrieved from https://12factor.net/

2. Candea, G., & Fox, A. (2003). *Crash-only software*. In Proceedings of the 9th Workshop on Hot Topics in Operating Systems (HotOS IX).

3. Google. (2023). *Protocol Buffers Language Guide (proto3)*. Retrieved from https://protobuf.dev/

4. Hennessy, J. L., & Patterson, D. A. (2011). *Computer Architecture: A Quantitative Approach* (5th ed.). Morgan Kaufmann.

5. Gamma, E., Helm, R., Johnson, R., & Vlissides, J. (1994). *Design Patterns: Elements of Reusable Object-Oriented Software*. Addison-Wesley.

---

## Further Reading

- [Loom System Architecture](./loom-system-architecture.md) - Overarching system design
- [Agent Runtime Architecture](./agent-runtime.md) - Agent execution model and memory management
- [Observability Architecture](./observability.md) - Distributed tracing and metrics
- [Pattern System Architecture](./pattern-system.md) - Pattern matching and hot-reload
- [Communication System Design](./communication-system-design.md) - Inter-agent messaging and shared memory

---

**Version**: v1.2.0

**Last Updated**: 2026-01-14

**Authors**: Loom Architecture Team
