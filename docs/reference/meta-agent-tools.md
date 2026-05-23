
# Meta-Agent Reference

Reference documentation for the meta-agent subsystem. The meta-agent analyzes natural language requirements and generates agent configurations, selecting appropriate templates, patterns, and backends.

**Version**: v1.2.0
**Package**: `pkg/metaagent/` (with sub-packages `learning/`, `templates/`, `teleprompter/`)
**Status**: The meta-agent functionality is implemented as Go packages with Go functions and interfaces. There are no shuttle tools (the 18-tool design described in a previous version of this document was never implemented).


## Table of Contents

- [Quick Reference](#quick-reference)
- [Implemented Components](#implemented-components)
  - [Analyzer](#analyzer)
  - [PatternSelector](#pattern-selector)
  - [AgentValidator](#agent-validator)
  - [ROMBuilder](#rom-builder)
  - [Template Registry](#template-registry)
  - [Learning Engine](#learning-engine)
  - [Teleprompter](#teleprompter)
- [Ephemeral Agent Management](#ephemeral-agent-management)
- [Data Types](#data-types)
- [Domain Types](#domain-types)
- [Validation Rules](#validation-rules)
- [Templates](#templates)
- [Testing](#testing)
- [See Also](#see-also)


## Quick Reference

### Component Summary

| Component | Package | Purpose | Status |
|-----------|---------|---------|--------|
| `Analyzer` | `pkg/metaagent` | LLM-powered requirement analysis | ✅ Implemented |
| `PatternSelector` | `pkg/metaagent` | Map capabilities to patterns | ✅ Implemented |
| `AgentValidator` | `pkg/metaagent` | YAML config validation with anti-pattern detection | ✅ Implemented |
| `ROMBuilder` | `pkg/metaagent` | Build Read-Only Memory for agents | ✅ Implemented |
| `Template Registry` | `pkg/metaagent/templates` | Embedded YAML template management | ✅ Implemented |
| `LearningEngine` | `pkg/metaagent/learning` | Deployment metrics and improvement suggestions | ✅ Implemented |
| `MetricsCollector` | `pkg/metaagent/learning` | SQLite-backed metrics storage | ✅ Implemented |
| `Teleprompter` | `pkg/metaagent/teleprompter` | DSPy-style prompt optimization | ✅ Implemented |
| `ManageEphemeralAgentsTool` | `pkg/shuttle/builtin` | Shuttle tool for spawn/despawn sub-agents | ✅ Implemented |

### Key Interfaces

| Interface | Package | Method | Description |
|-----------|---------|--------|-------------|
| `RequirementAnalyzer` | `pkg/metaagent` | `Analyze(ctx, requirements) (*Analysis, error)` | ✅ Extract structured info from text |
| `ConfigGenerator` | `pkg/metaagent` | `GenerateAgentConfig(ctx, analysis) (string, error)` | 📋 Interface defined, no implementation yet |
| `Validator` | `pkg/metaagent` | `Validate(ctx, config) (*ValidationResult, error)` | ✅ Validate agent config YAML |


## Implemented Components

### Analyzer

**Package**: `pkg/metaagent`
**File**: `analyzer.go`

**Purpose**: Uses an LLM to extract structured information from natural language requirements.

```go
func NewAnalyzer(llm agent.LLMProvider, tracer observability.Tracer) *Analyzer
```

```go
func (a *Analyzer) Analyze(ctx context.Context, requirements string) (*Analysis, error)
```

**Parameters**:
- `ctx` (`context.Context`) - Context for cancellation and deadlines
- `requirements` (`string`) - Natural language requirements text

**Returns**:
- `*Analysis` - Structured analysis containing domain, capabilities, data sources, complexity, and suggested agent name
- `error` - LLM call failure or JSON parse error

**Behavior**:
1. Constructs a task-oriented prompt (no role-prompting) with the requirements
2. Calls the LLM to generate a JSON analysis
3. Extracts JSON from the response (handles mixed text/JSON responses)
4. Parses into `Analysis` struct
5. All operations are traced via the observability tracer

**Observability spans**: `metaagent.analyzer.analyze`

**Thread safety**: Safe for concurrent use (stateless; LLM provider handles its own concurrency)

**Example**:
```go
analyzer := metaagent.NewAnalyzer(llmProvider, tracer)
analysis, err := analyzer.Analyze(ctx, "I need an agent that analyzes PostgreSQL slow queries")
if err != nil {
    log.Fatalf("Analysis failed: %v", err)
}
fmt.Printf("Domain: %s, Complexity: %s\n", analysis.Domain, analysis.Complexity)
// Domain: sql, Complexity: medium
```


### Pattern Selector

**Package**: `pkg/metaagent`
**File**: `pattern_selector.go`

**Purpose**: Maps agent capabilities to pattern names from the pattern library, with dependency resolution.

```go
func NewPatternSelector(tracer observability.Tracer) *PatternSelector
```

```go
func (ps *PatternSelector) SelectPatterns(ctx context.Context, analysis *Analysis) ([]string, error)
```

**Parameters**:
- `ctx` (`context.Context`) - Context for cancellation
- `analysis` (`*Analysis`) - Structured analysis from Analyzer (must not be nil)

**Returns**:
- `[]string` - Pattern names (e.g., `"postgres/analytics/sequential_scan_detection"`)
- `error` - Nil analysis error

**Selection algorithm**:
1. Match capability names to pattern names/tags (direct match, fuzzy match, or category match)
2. Filter patterns by domain
3. Score patterns: base 0.5, +0.3 for domain match, +0.2 for high priority capability
4. If no matches, fall back to domain defaults
5. Resolve dependencies (e.g., `missing_index_analysis` requires `sequential_scan_detection`)
6. Return deduplicated list

**Domain defaults** (when no capability matches):
- `sql`: `sql/data_quality/data_profiling`, `sql/data_quality/data_validation`
- `rest`: `rest_api/health_check`
- `file`: `document/file_parser`
- `document`: `document/document_analyzer`

**Observability spans**: `metaagent.pattern_selector.select_patterns`

**Thread safety**: Safe for concurrent use (read-only capability map)


### Agent Validator

**Package**: `pkg/metaagent`
**File**: `validator.go`

**Purpose**: Validates agent configuration YAML against schema rules, anti-patterns, and security checks.

```go
func NewValidator(tracer observability.Tracer) *AgentValidator
```

```go
func (v *AgentValidator) Validate(ctx context.Context, config string) (*ValidationResult, error)
```

**Parameters**:
- `ctx` (`context.Context`) - Context for cancellation
- `config` (`string`) - Raw YAML agent configuration string

**Returns**:
- `*ValidationResult` - Contains `Valid` (bool), `Errors` ([]ValidationError), `Warnings` ([]ValidationWarning)
- `error` - Only for internal errors (validation failures are returned in the result, not as errors)

**Validation checks performed**:
1. **YAML syntax** - Parseable YAML (splits at `...` document terminator)
2. **Required fields** - `agent.name`, `agent.llm.provider`, `agent.llm.model`
3. **System prompt** - Minimum 50 chars, anti-pattern detection (role-prompting), hardcoded credentials
4. **LLM config** - Valid provider (`anthropic`, `openai`, `bedrock`, `ollama`), temperature 0.0-2.0, positive max_tokens
5. **Backends/Tools** - MCP server URL validation, custom tool name/implementation required
6. **Patterns** - Check if referenced patterns exist in library
7. **Memory** - Valid type (`memory`, `sqlite`, `postgres`), DSN/path required for sqlite/postgres

**Anti-patterns detected** (role-prompting):
- `You are a/an ...`
- `As a/an ...`
- `Act as ...`
- `Pretend to be ...`
- `Imagine you are ...`
- `Your role is ...`
- `You will be a/an ...`
- `You're a/an ...`

**ValidationError fields**:
- `Field` (string) - Config field path (e.g., `"agent.llm.provider"`)
- `Message` (string) - Error description
- `Type` (string) - Error category: `syntax_error`, `required_field`, `invalid_value`, `anti_pattern`, `format_error`, `security_risk`
- `Line` (int) - Line number (0 if not applicable)
- `Suggestion` (string) - How to fix

**Observability spans**: `metaagent.validator.validate`

**Thread safety**: Safe for concurrent use


### ROM Builder

**Package**: `pkg/metaagent`
**File**: `rom_builder.go`

**Purpose**: Builds Read-Only Memory (ROM) content for agents. ROM is static documentation embedded in agent configs that never changes during an agent session.

```go
func NewROMBuilder(tracer observability.Tracer) *ROMBuilder
```

```go
func (rb *ROMBuilder) BuildMetaAgentROM() (string, error)
func (rb *ROMBuilder) BuildAgentROM(analysis *Analysis, selectedPatterns []string, backendPath string) (string, error)
func (rb *ROMBuilder) LoadBackendROM(backendPath string) string
```

**BuildMetaAgentROM**: Returns YAML-formatted ROM with guidelines for the meta-agent itself (requirement analysis, template selection, tool selection, backend selection, pattern integration, validation).

**BuildAgentROM**: Loads backend-specific ROM content (currently only Teradata ROM is embedded via `roms/TD.rom`).

**LoadBackendROM**: Returns backend-specific ROM by checking if the backend path contains "teradata".

**Supported backends with ROM**:
- ✅ Teradata (`roms/TD.rom` embedded)
- 📋 PostgreSQL (not yet created)
- 📋 Others (not yet created)

**Thread safety**: Safe for concurrent use


### Template Registry

**Package**: `pkg/metaagent/templates`
**File**: `registry.go`

**Purpose**: Manages embedded agent configuration templates. Templates are YAML files compiled into the binary via Go `embed`.

```go
func NewRegistry() (*Registry, error)
```

```go
func (r *Registry) Get(name string) (*Template, error)
func (r *Registry) List() []string
func (r *Registry) ListByDomain(domain string) []*Template
func (r *Registry) GetAll() []*Template
```

**Available templates** (9 templates):

| Template Name | Domain | Description |
|---------------|--------|-------------|
| `sql_postgres_analyst` | sql | PostgreSQL analyst |
| `sql_postgres_expert` | sql | PostgreSQL expert |
| `sql_postgres_helper` | sql | PostgreSQL helper |
| `sql_teradata_analyst` | sql | Teradata analyst |
| `sql_teradata_expert` | sql | Teradata expert |
| `sql_teradata_helper` | sql | Teradata helper |
| `api_monitor` | rest | API monitoring |
| `etl_processor` | etl | ETL processing |
| `file_analyzer` | file | File analysis |

**Template struct fields**:
- `Name` (string) - Derived from filename (e.g., `sql_postgres_analyst`)
- `Content` (string) - Raw YAML content
- `Agent` (AgentTemplate) - Parsed agent config
- `Variables` ([]string) - Extracted `{{variable}}` placeholders
- `Domain` (string) - From `agent.metadata.domain`
- `Capabilities` ([]string) - From `agent.metadata.capabilities`
- `Patterns` ([]string) - From `agent.metadata.patterns`

**Thread safety**: Safe for concurrent use (RWMutex protected)


### Learning Engine

**Package**: `pkg/metaagent/learning`
**Files**: `engine.go`, `collector.go`, `schema.go`

**Purpose**: Tracks deployment metrics in SQLite and provides data-driven improvement suggestions.

#### MetricsCollector

```go
func NewMetricsCollector(dbPath string, tracer observability.Tracer) (*MetricsCollector, error)
```

```go
func (mc *MetricsCollector) RecordDeployment(ctx context.Context, metric *DeploymentMetric) error
func (mc *MetricsCollector) GetSuccessRate(ctx context.Context, domain DomainType) (float64, error)
func (mc *MetricsCollector) GetPatternPerformance(ctx context.Context, domain DomainType) (map[string]*PatternMetrics, error)
func (mc *MetricsCollector) GetTemplatePerformance(ctx context.Context, domain DomainType) (map[string]*TemplateMetrics, error)
func (mc *MetricsCollector) GetRecentFailures(ctx context.Context, domain DomainType, limit int) ([]*DeploymentMetric, error)
func (mc *MetricsCollector) UpdateDeploymentFeedback(ctx context.Context, agentID string, feedback interface{}) error
func (mc *MetricsCollector) Close() error
```

**Storage**: SQLite with WAL mode. Tables: `metaagent_deployments`, `pattern_effectiveness`, `improvement_history`, `config_snapshots`.

#### LearningEngine

```go
func NewLearningEngine(collector *MetricsCollector, tracer observability.Tracer) *LearningEngine
```

```go
func (le *LearningEngine) GetBestPatterns(ctx context.Context, domain DomainType) ([]PatternScore, error)
func (le *LearningEngine) SuggestImprovements(ctx context.Context, domain DomainType) ([]Improvement, error)
func (le *LearningEngine) GetDomainInsights(ctx context.Context, domain DomainType) (*DomainInsights, error)
```

**Improvement types**:
- `pattern_add` - Recommend adding a high-performing pattern
- `pattern_remove` - Recommend removing a low-performing pattern
- `template_adjust` - Recommend reviewing a failing or expensive template

**Confidence scoring**: Uses sigmoid function. Reaches ~0.9 at 50 uses, ~0.99 at 100 uses. Capped at 0.3 for fewer than 3 samples.

**Thread safety**: Safe for concurrent use (RWMutex on database operations)


### Teleprompter

**Package**: `pkg/metaagent/teleprompter`
**Files**: `teleprompter.go`, `copro.go`, `textgrad.go`, `bootstrap_few_shot.go`, `mipro.go`

**Purpose**: DSPy-style prompt optimization framework with multiple teleprompter implementations.

**Available teleprompters**:
- COPRO (Coordinate Prompt Optimization)
- TextGrad (Text-based gradient optimization)
- BootstrapFewShot (Few-shot learning with bootstrapping)
- MIPRO (Multi-step Instruction Prompt Optimization)

See `pkg/metaagent/teleprompter/EXTENDING.md` for details on creating custom teleprompters.


## Ephemeral Agent Management

**Package**: `pkg/shuttle/builtin`
**File**: `manage_ephemeral_agents.go`

Ephemeral agent management is implemented as a shuttle tool (`manage_ephemeral_agents`), not as the `builtin://ephemeral/*` tools described in prior design documents.

```go
func NewManageEphemeralAgentsTool(handler EphemeralAgentHandler, parentSessionID, parentAgentID string) *ManageEphemeralAgentsTool
```

**Tool name**: `manage_ephemeral_agents`

**Commands**:
- `spawn` - Create a new sub-agent as a child of the current session
- `despawn` - Terminate a spawned sub-agent

**Spawn parameters**:
- `command` (string, required) - `"spawn"`
- `agent_id` (string, required) - Agent config to spawn (e.g., `"fighter-spawnable"`)
- `workflow_id` (string, optional) - Workflow namespace
- `initial_message` (string, optional) - First message for spawned agent
- `auto_subscribe` ([]string, optional) - Topics to auto-subscribe

**Despawn parameters**:
- `command` (string, required) - `"despawn"`
- `sub_agent_id` (string, required) - Full ID of sub-agent to despawn
- `reason` (string, optional) - Reason for despawn

**Error codes**: `MISSING_COMMAND`, `INVALID_COMMAND`, `MISSING_AGENT_ID`, `SPAWN_FAILED`, `MISSING_SUB_AGENT_ID`, `DESPAWN_FAILED`


## Data Types

### Analysis

```go
type Analysis struct {
    Domain        DomainType      `json:"domain"`
    Capabilities  []Capability    `json:"capabilities"`
    DataSources   []DataSource    `json:"data_sources"`
    Complexity    ComplexityLevel `json:"complexity"`
    SuggestedName string          `json:"suggested_name"`
}
```

### Capability

```go
type Capability struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Category    string `json:"category"`
    Priority    int    `json:"priority"`   // Integer (lower = higher priority)
}
```

### DataSource

```go
type DataSource struct {
    Type           string `json:"type"`
    ConnectionHint string `json:"connection_hint"`
}
```

### ValidationResult

```go
type ValidationResult struct {
    Valid    bool
    Errors   []ValidationError
    Warnings []ValidationWarning
}
```

### ValidationError

```go
type ValidationError struct {
    Field      string  // Config field path
    Message    string  // Error description
    Type       string  // "syntax_error", "required_field", "invalid_value", "anti_pattern", "format_error", "security_risk"
    Line       int     // Line number (0 if N/A)
    Suggestion string  // Fix recommendation
}
```

### ValidationWarning

```go
type ValidationWarning struct {
    Field      string
    Message    string
    Suggestion string  // Optional
}
```

### DeploymentMetric (learning package)

```go
type DeploymentMetric struct {
    AgentID          string
    Domain           DomainType
    Templates        []string
    SelectedTemplate string
    Patterns         []string
    Success          bool
    ErrorMessage     string
    CostUSD          float64
    TurnsUsed        int
    CreatedAt        time.Time
    Metadata         map[string]string
}
```

### Improvement (learning package)

```go
type Improvement struct {
    Type        string  // "pattern_add", "pattern_remove", "template_adjust"
    Description string
    Confidence  float64
    Impact      string  // "high", "medium", "low"
    Details     map[string]interface{}
}
```


## Domain Types

Defined in `pkg/metaagent/interfaces.go`:

| Constant | Value | Description |
|----------|-------|-------------|
| `DomainSQL` | `"sql"` | SQL databases (PostgreSQL, MySQL, SQLite, Teradata) |
| `DomainREST` | `"rest"` | REST API integration |
| `DomainGraphQL` | `"graphql"` | GraphQL API integration |
| `DomainFile` | `"file"` | File system operations |
| `DomainDocument` | `"document"` | Document search and processing |
| `DomainMCP` | `"mcp"` | Model Context Protocol servers |
| `DomainHybrid` | `"hybrid"` | Multiple domain types |

Note: The `learning` sub-package defines its own `DomainType` with additional values `DomainETL` (`"etl"`) and `DomainUnknown` (`"unknown"`).


## Validation Rules

### Required Fields
- `agent.name` - Must be non-empty
- `agent.llm.provider` - Must be one of: `anthropic`, `openai`, `bedrock`, `ollama`
- `agent.llm.model` - Must be non-empty

### Value Constraints
- `agent.llm.temperature` - Must be between 0.0 and 2.0
- `agent.llm.max_tokens` - Must be non-negative (warning if >100,000)
- `agent.system_prompt` - Minimum 50 characters (warning if >2,000)
- `agent.memory.type` - Must be one of: `memory`, `sqlite`, `postgres`
- `agent.memory.max_history` - Must be non-negative (warning if >1,000)

### Security Checks
- No hardcoded passwords, API keys, secrets, or tokens in system prompts
- MCP server URLs must be valid URL format

### Anti-Pattern Detection
- 8 regex patterns detect role-prompting language (see [Agent Validator](#agent-validator) section)


## Templates

9 embedded templates in `pkg/metaagent/templates/*.yaml`:

- `sql_postgres_analyst` - PostgreSQL analysis agent
- `sql_postgres_expert` - PostgreSQL expert agent
- `sql_postgres_helper` - PostgreSQL helper agent
- `sql_teradata_analyst` - Teradata analysis agent
- `sql_teradata_expert` - Teradata expert agent
- `sql_teradata_helper` - Teradata helper agent
- `api_monitor` - API monitoring agent
- `etl_processor` - ETL processing agent
- `file_analyzer` - File analysis agent

Templates support `{{variable}}` placeholder substitution. Variables are automatically extracted during template loading.


## Testing

### Test Coverage

The `pkg/metaagent/` package tree contains approximately 200+ test functions across 28 test files covering:

- Requirement analysis (analyzer_test.go)
- Pattern selection and dependency resolution (pattern_selector_test.go)
- YAML validation and anti-pattern detection (validator_test.go, 34 test functions)
- ROM building (rom_builder_test.go)
- Progress tracking (progress_test.go)
- Template registry (templates/registry_test.go)
- Learning engine and metrics (learning/engine_test.go, learning/collector_test.go)
- Tuning and config loading (learning/tuning_test.go, learning/config_loader_test.go)
- Pattern tracking (learning/pattern_tracker_test.go)
- Teleprompter optimization (teleprompter/*_test.go)

### Running Tests

```bash
# All metaagent tests
go test -tags fts5 -race ./pkg/metaagent/...

# Specific component
go test -tags fts5 -race ./pkg/metaagent/ -run TestValidat -v

# Learning subsystem
go test -tags fts5 -race ./pkg/metaagent/learning/... -v

# Teleprompter subsystem
go test -tags fts5 -race ./pkg/metaagent/teleprompter/... -v
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML configuration
- [Pattern Reference](./patterns.md) - Pattern library system
- [CLI Reference](./cli.md) - Meta-agent CLI commands
- [Backend Reference](./backend.md) - Backend types and configuration
- [Meta-Agent Usage Guide](../guides/meta-agent-usage.md) - How-to guide
- [Meta-Agent Examples](../guides/meta-agent-examples.md) - Practical examples
- [Learning Agent Guide](../guides/learning-agent-guide.md) - Learning subsystem guide
