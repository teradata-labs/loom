# Changelog

All notable changes to Loom will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-02-02

### Internal API Improvements

#### agent_management Tool API Restructure (PR #63)
The `agent_management` tool now uses structured JSON for better type safety and validation.

**Key Point**: Weaver prompt was updated in the same PR - the primary use case (weaver creating agents) works seamlessly with the new API.

**What Changed**:
- Old string-based actions (`"create"`, `"update"`) → new structured JSON actions
- New actions: `create_agent`, `create_workflow`, `update_agent`, `update_workflow`
- JSONSchema validation prevents runtime errors from invalid field names

**Who's Affected**:
- ✅ Weaver users: No impact (weaver prompt updated simultaneously)
- ✅ Standard workflows: No impact (built-in tools updated)
- ⚠️ Custom integrations: Need migration (if directly calling agent_management)

**Migration for Custom Tools**:
- Old actions return `INVALID_ACTION` error with clear migration instructions
- See `prompts/tools/agent_management.yaml` for new structured format
- JSONSchema available at `pkg/shuttle/builtin/agent_management_schema.go`

### Added

#### Memory & Communication
- **Unified conversation_memory tool** - Progressive disclosure with recall/search/clear actions
- **Workflow agent communication** (PR #61) - Auto-healing agent IDs, message delivery auto-injection
- **Session-memory tool** - List/summary/compact actions with FTS5 search
- **Broadcast bus auto-injection** - Workflow coordinators get automatic message buses

#### Observability & Instrumentation
- **Self-contained observability** (PR #47) - Removed Hawk dependency
  - MemoryStorage: Ring buffer with FIFO eviction (10k traces)
  - SQLiteStorage: Persistent storage with FTS5
  - HTTP export mode still available with `-tags hawk`
- **LLM instrumentation** (PR #57) - Comprehensive token/cost tracking for all LLM calls
- **GetWorkflowExecution & ListWorkflowExecutions RPCs** - Full workflow observability

#### Multi-Agent & Orchestration
- **spawn_agent tool** - Dynamic sub-agent creation with lifecycle management
- **Auto-workflow initialization** - Peer-to-peer pub-sub workflows
- **StreamWeave integration** - CLI and workflow examples
- **Agent ID migration** (PR #59) - Backward-compatible message tracking

#### CLI, TUI & UX
- **Phase 1 CLI commands** - Sessions and MCP management
- **Built-in Operator agent** - Agent discovery with keyword matching
- **Session search** (PR #60) - Improved session select UX with metadata
- **Pattern viewer dialog** - Full pattern editor in TUI sidebar
- **Rich session metadata** - Title, time ago, token count, cost display

#### Tool System
- **Structured JSON API** (PR #63) - agent_management with JSONSchema validation
- **Tool documentation system** (PR #53) - YAML descriptions work for all tools
- **Universal executor-level solution** - Oversized tool parameter handling
- **Progressive disclosure** - Character-based limits for tool output

#### Token-Based Memory (PR #58)
- **Dynamic L1 memory allocation** - Adapts to LLM context sizes (8K-200K)
- **Token-based limits** - `MaxL1Tokens` replaces `MaxL1Messages`
- **Profile multipliers** - data_intensive (0.6x), balanced (1.0x), conversational (1.5x)
- **Backward compatible** - Proto accepts `max_l1_messages` (converts: messages × 800 → tokens)

#### Release Infrastructure
- **GPG signing** - All checksums cryptographically signed starting with v1.1.0
- **SLSA provenance attestations** - Supply chain security with build provenance
- **Enhanced version-manager** - Now tracks 13 files (added docs/README.md, chocolateyinstall.ps1)

### Fixed

#### Critical Fixes
- **Database migration failure** (PR #51) - Fixed index creation order for v1.0.0/v1.0.1 upgrade
- **MCP server startup hang** (PR #50) - Persist enabled field fix
- **Workflow agent communication** (PR #61) - Auto-inject queue messages, persist dequeue_count
- **shell_execute contradictory sandbox checks** (PR #55) - Resolved permission conflicts
- **Message duplication in TUI** (PR #52) - Fixed for all agents
- **Session modal crash** (PR #60) - Handle zero-token sessions gracefully

#### Security & Quality
- **gosec security warnings** - Fixed G302, G104, G115 across codebase
- **File permissions** - Hardened to 0600/0644/0755
- **Integer overflow protection** - Proto int32 conversions safeguarded
- **CodeQL permissions** - Security workflow improvements

#### Tool & Agent Fixes
- **Agent name lookup** (PR #44) - Support both names and GUIDs
- **Tool documentation system** (PR #53) - YAML descriptions now enabled
- **Multi-agent broadcast** (PR #48) - Test failures fixed
- **Pattern selection in sidebar** (PR #52) - Selection state preserved

#### Package Management
- **Chocolatey antivirus false positives** - Package format fixes
- **winget file format** - Preservation improvements
- **Scoop formatjson** - Pre-commit formatting
- **Homebrew GPG signing** - Added for tap commits

#### Proto & Build
- **buf generate** - Downgraded openapiv2 plugin to v2.22.0 for stability
- **Proto generation** - Up-to-date checks
- **Linter issues** - artifacts CLI, version-manager fixed

### Changed

#### Dependency Updates
- anthropic-sdk-go: 1.19.0 → 1.20.0
- lib/pq: 1.10.9 → 1.11.1
- grpc-gateway/v2: 2.27.4 → 2.27.7
- AWS SDK v2/bedrockruntime: 1.47.2 → 1.48.0
- actions/setup-go: 5 → 6
- Total: 30+ dependency updates across 3 batch PRs (#64, #46, and others)

### Documentation

#### Major Restructuring (PR #54, #56)
- Reorganized examples/ directory:
  - `examples/config/` - Server configurations
  - `examples/backends/` - Backend integrations
  - `examples/patterns/` - Pattern libraries
  - `examples/reference/` - Reference examples
- Complete rewrite of examples/README.md
- Organization rename: "Teradata-TIO" → "teradata-labs" (110 files)

#### Pattern Libraries
- agent-standard.yaml - Standard agent creation
- workflow-multi-agent.yaml - Multi-agent coordination
- workflow-debate.yaml - Debate orchestration
- workflow-pipeline.yaml - Pipeline orchestration
- workflow-parallel.yaml - Parallel orchestration

#### Reference Documentation
- TD.rom v2 improvements (PR #56) - Focus on 5 critical error patterns
- Agent templates: code-reviewer.yaml (NEW)
- Workflow namespacing documentation
- Comprehensive tool descriptions (prompts/tools/*.yaml)

### Testing

- **2252+ test functions** across 244 test files
- **Zero race conditions** (all tests run with `-race` detector)
- **50+ new test functions** for new features
- **Security scan**: gosec passes with no critical issues
- **Linting**: golangci-lint passes on all modified code

---

## [1.0.2] - 2026-01-15

### Fixed
- Various bug fixes and improvements
- Package distribution fixes

## [1.0.1] - 2026-01-14

### Fixed
- Database migration issues
- Initial release stability improvements

## [1.0.0] - 2026-01-09

### Added
- Initial release of Loom
- Complete LLM agent runtime with tool system
- Multi-agent orchestration patterns
- Pattern-guided learning system
- Agent templates and workflow management
- TUI client and gRPC server
- Support for Anthropic, AWS Bedrock, Ollama, OpenAI backends

[1.1.0]: https://github.com/teradata-labs/loom/compare/v1.0.2...v1.1.0
[1.0.2]: https://github.com/teradata-labs/loom/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/teradata-labs/loom/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/teradata-labs/loom/releases/tag/v1.0.0
