# Changelog

All notable changes to Loom will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.0] - 2026-03-19

### Added

#### Skills System (#117)
- **LLM-agnostic skills** — Activatable behaviors combining prompt injection, tool preferences, and trigger conditions (slash commands, keyword auto-detection, always-on)
- Proto-first: `skill.proto` defines Skill, SkillsConfig, SkillActivationMode
- `pkg/skills`: Library (discovery+caching), Orchestrator (activation+budget), Loader (YAML parsing), HotReloader (fsnotify), Format (token budgeting)
- Agent integration: skill matching/activation in conversation loop with token budget control
- MCP bridge: 5 skill management tools (list, get, create, activate, deactivate)
- 73+ tests with race detector, concurrent stress tests (33 goroutines)

#### Per-Role LLM Providers (#100)
- **Different LLM providers per operational role** (judge, orchestrator, classifier, compressor) with fallback to main agent LLM
- New proto fields: `judge_llm`, `orchestrator_llm`, `classifier_llm`, `compressor_llm` on AgentConfig
- Role-aware `SwitchModel` RPC (backward compatible when role=UNSPECIFIED)
- Real `GetHealth` with per-role LLM ping checks (replaces hardcoded stub)
- `ValidateProviders` startup preflight wired into server startup

#### Provider Pool & A/B Testing (#100)
- **Provider pool** with round-robin, weighted, and A/B test routing strategies
- **Prompt caching** support across providers
- **Rate limiting** with token bucket algorithm, burst capacity, and queue timeout

#### PostgreSQL Storage Backend (#85)
- **Native PostgreSQL as storage backend** with pgx/v5 driver, connection pooling, RLS, tsvector FTS, JSONB, and embedded SQL migrations
- `StorageBackend` abstraction enabling both SQLite (default) and PostgreSQL with zero breaking changes
- New RPCs: `GetStorageStatus` and `RunMigration`
- All concrete `*SessionStore`/`*SQLResultStore` references refactored to interfaces

#### Multi-Tenancy (#85)
- **User-scoped isolation** with Row Level Security and gRPC interceptors
- Auth via `X-User-ID` gRPC metadata header with configurable strict/lenient modes
- `AdminService` for tenant management and security hardening

#### Supabase Backend (#91)
- **Supabase execution backend** with session/transaction pooler modes and automatic prepared statement handling
- RLS context injection via JWT/role claims
- Loom internal storage can now run on Supabase PostgreSQL (verified with PostgreSQL 17.6)
- Viper env var fix: `SetEnvKeyReplacer(".", "_")` for nested config like `LOOM_STORAGE_POSTGRES_DSN`

#### MCP Server & Apps (#73)
- **MCP server** with stdio transport, app management, and `loom-mcp` bridge binary
- 12 gRPC RPCs implemented on MultiAgentServer: pattern management (LoadPatterns, ListPatterns, GetPattern), agent lifecycle (CreateAgentFromConfig, etc.), and observability
- 71 unit tests, all passing with `-race`

#### Context State & Model Catalog (#118)
- **ContextState** proto message — per-session context window reporting (active_pattern, tokens used/max, ROM, tools loaded)
- `WeaveRequest.reset_context` for atomic context window clearing
- **Model catalog overhaul** — 44 models across 7 providers, replacing outdated GPT-4-era entries
- Removed huggingface provider; added Bedrock Llama/Nova, Azure o3/o4-mini, Mistral Codestral/Pixtral
- New ModelInfo fields: `max_output_tokens`, `is_reasoning`, `show_in_dropdown`

#### Weaver Improvements (#121)
- **/agent-plan mode** — Structured requirement gathering with skills-based recommendations
- Skills catalog (`skills_catalog.yaml`) with decision tree for domain-specific recommendations
- Guided planning vs quick start on first interaction
- `/agent-plan` discoverable in sidebar, help dialog, and splash screen

#### TUI Enhancements (#109, #121)
- **Slash commands** — `/clear`, `/quit`, `/sessions`, `/model`, `/agents`, `/workflows`, `/sidebar`, `/apps`, `/mcp`, `/patterns`, `/help`
- **Pattern browser modal** with keyboard navigation, edit, and view support
- Weaver agent as first selectable item in Ctrl+W workflows dialog
- Agent info block and keyboard hints in sidebar
- `/help` dialog listing all slash commands

#### Dynamic Ollama Discovery (#70)
- **Runtime model discovery** via `/api/tags` replacing hardcoded model list
- Dynamic tool support detection via `/api/show` template inspection
- Updated recommendations: qwen3, mistral-small3.1, command-r families

#### Tool Schema Pruning (#100)
- Pruned tool Description() strings and InputSchema property descriptions
- Schema regression test with baseline+50 token ceilings

#### Tool Lifecycle Streaming (#117)
- **Tool-started/completed events** in WeaveProgress for real-time tool timeline UI
- Correlated start/end via ToolCallID with duration counters and success/failure states

### Fixed

#### LLM Provider Fixes
- **MCP tool schema handling** (#114) — All 6 providers now handle empty/missing `type` fields; infer "object"/"array" from properties/items; added anyOf/oneOf/allOf/not composite keyword support
- **StreamWeave error visibility** (#114) — Sends EXECUTION_STAGE_FAILED event before gRPC error so TUI displays errors instead of silent empty responses
- **Rate limiter starvation** (#89) — `NewRateLimiter()` now backfills zero-value config with defaults; previously caused spurious 429 errors
- **Anthropic model IDs** (#77) — Updated from deprecated claude-3-5-sonnet/claude-3-opus to current model IDs across all providers
- **Anthropic tool_use serialization** (#77) — Custom MarshalJSON ensures `"input": {}` is always present (API requires it)
- **Anthropic streaming tool inputs** (#77) — Fix `input_json_delta` SSE handling; previously caused nil-input tool calls and retry loops
- **Gemini thought_signature** (#78) — Capture from response and echo back verbatim at Part level (was incorrectly placed inside FunctionCall)
- **shell_execute pipe race** (#117) — `wg.Wait()` before `cmd.Wait()` per Go docs

#### Windows Fixes
- **PowerShell 5.1 compatibility** (#74) — UTF-8 BOM added to quickstart.ps1 (byte 0x93 misinterpreted as smart quote)
- **YAML path escapes** (#74) — Convert backslashes in observability `sqlite_path` to forward slashes
- **Auto-install prerequisites** (#74) — Go, Just, and Buf installed via winget when missing
- **Chocolatey package compliance** — Fixed packageSourceUrl 404, removed broken iconUrl, added pattern download checksum

#### Security
- **314 gosec issues resolved** (#84) — G115 integer overflow (SafeInt32/SafeUint helpers), G304 filepath.Clean, G104 error handling, G201 SQL sanitization, G110 decompression bomb protection, G302/G306 tightened permissions
- **30 gosec G118 alerts** (#113) — Moved `#nosec` annotations to correct `go func()` lines for gosec 2.24.7
- **Ollama SSRF prevention** (#109) — Validate endpoint URL before HTTP GET
- **Rate limiter overflow guard** (#109) — `BurstCapacity*2` checked for integer overflow

#### CI/CD
- **Nightly build fixed** (#81) — Missing generate-weaver step caused 40 consecutive failures
- **SARIF upload** — Added to nightly scan; removed duplicate config causing "1 configuration not found" warnings
- **CodeQL** — Workflow permission fixes

### Changed

#### Dependency Updates
- google.golang.org/grpc: 1.79.1 → 1.79.3
- charm.land/bubbletea/v2: upgrade to v2
- charm.land/bubbles/v2: 2.0.0-rc.1 → 2.0.0
- charm.land/lipgloss/v2: upgrade to v2
- github.com/xuri/excelize/v2: updated
- filippo.io/edwards25519: 1.1.0 → 1.1.1
- securego/gosec: 2.23.0 → 2.24.7
- softprops/action-gh-release: 2.5.0 → 2.6.1
- actions/download-artifact: 7.0.0 → 8.0.1
- actions/upload-artifact: 6 → 7
- actions/attest-build-provenance: updated
- github/codeql-action: 4.32.2 → 4.33.0
- 30+ Go module updates across 3 batch PRs (#80, #90, #124)

### Testing

- **3112+ test functions** across 319 test files (up from 2252/244 in v1.1.0)
- **Zero race conditions** — all tests run with `-race` detector
- **Skills system**: 73+ tests with concurrent stress tests (33 goroutines)
- **PostgreSQL storage**: 31+ tests covering config, RPCs, backend factory, migrations
- **Supabase backend**: 560-line unit test suite + 10-function E2E integration suite
- **MCP server**: 71 unit tests for 12 gRPC RPCs
- **Security scan**: gosec passes with 0 issues

---

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

[1.2.0]: https://github.com/teradata-labs/loom/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/teradata-labs/loom/compare/v1.0.2...v1.1.0
[1.0.2]: https://github.com/teradata-labs/loom/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/teradata-labs/loom/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/teradata-labs/loom/releases/tag/v1.0.0
