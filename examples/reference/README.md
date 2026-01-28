# Configuration Examples

YAML-based configuration for threads, backends, patterns, and workflows. Supports declarative, configuration-driven development.

## Directories

### agent-templates/
Reusable agent configuration templates.

**Contents:**
- Base agent configurations
- Common thread patterns
- Template variables for customization

**Use case:** Share agent configurations across projects, standardize agent setup.

### agents/
Complete agent definitions ready to load and run.

**Contents:**
- Validated agent configs
- Domain-specific agent definitions
- Agent behavior specifications
- `agent-all-fields-reference.yaml` - Comprehensive reference showing all possible YAML fields

**Use case:** Configuration-driven agent deployment, version-controlled agent definitions.

### backends/
Backend connection configurations.

**Contents:**
- Database connection configs
- API endpoint configurations
- Service connection templates

**Use case:** Environment-specific backend configs, connection management.

### patterns/
Domain-specific pattern libraries (YAML).

**Contents:**
- SQL patterns for databases
- REST API interaction patterns
- Common operation templates
- Domain-specific workflows

**Example patterns:**
- PostgreSQL: 12 production SQL patterns
- Teradata: Advanced analytics and ML patterns
- REST: API interaction patterns

**Use case:** Encode domain knowledge, guide thread behavior, consistent operation execution.

### workflows/
Multi-step workflow definitions.

**Contents:**
- Sequential workflows
- Conditional branching
- Error handling patterns
- Retry strategies
- `workflow-all-fields-reference.yaml` - Comprehensive reference showing all possible workflow YAML fields for both orchestration patterns and event-driven workflows

**Use case:** Complex multi-step operations, orchestration patterns.

## Pattern Library Structure

Patterns are YAML files following this structure:

```yaml
patterns:
  - name: "pattern_name"
    description: "What this pattern does"
    category: "category_name"
    use_cases:
      - "When to use case 1"
      - "When to use case 2"
    examples:
      - input: "Natural language query"
        sql: "Generated SQL"
        explanation: "Why this query"
    best_practices:
      - "Best practice 1"
      - "Best practice 2"
```

## Example: SQL Patterns

See `patterns/sql/postgres.yaml` for PostgreSQL patterns:

- **Data Quality:** NULL checks, duplicate detection, referential integrity
- **Performance:** Index usage, query optimization, slow query identification
- **Operations:** Table creation, data migration, bulk operations

## Using Configurations

### Loading Thread Config

```go
import "github.com/Teradata-TIO/loom/pkg/config"

// Load thread config
threadCfg, err := config.LoadAgentConfig("config/agents/my-thread.yaml")

// Create thread from config
thread := agent.NewAgent(
    backend,
    llm,
    agent.WithConfig(threadCfg),
)
```
### Loading Patterns

```go
import "github.com/Teradata-TIO/loom/pkg/patterns"

// Load pattern library
patternLib, err := patterns.LoadLibrary("config/patterns/sql/postgres.yaml")

// Use in thread
thread := agent.NewAgent(
    backend,
    llm,
    agent.WithPatterns(patternLib),
)
```

### Loading Backend Config

```go
import "github.com/Teradata-TIO/loom/pkg/backends"

// Load backend config
backendCfg, err := backends.LoadConfig("config/backends/postgres.yaml")

// Create backend from config
backend, err := backends.NewFromConfig(backendCfg)
```

## Best Practices

1. **Version Control:** Keep all configurations in version control
2. **Environment Variables:** Use env vars for secrets and environment-specific values
3. **Validation:** Validate configs on load (see `validate_test.go` examples in the ../../test/ directory)
4. **Documentation:** Document patterns with clear use cases and examples
5. **Testing:** Test patterns with real queries (see pattern validation tests)

## Configuration-Driven Development

The config approach enables:

- **Declarative Setup:** Define threads and backends in YAML
- **Reusability:** Share configurations across projects
- **Versioning:** Track thread behavior changes over time
- **Testing:** Validate configurations before deployment
- **Domain Knowledge:** Encode expertise as patterns

## Example Developer Workflow

1. **Define Agent:** Create agent config in `agents/`
2. **Configure Backend:** Set up backend in `backends/`
3. **Add Patterns:** Create domain patterns in `patterns/`
4. **Define Workflow:** Add multi-step workflow in `workflows/`
5. **Load and Run:** Use configs to create production agent

```go
// Load everything from config
threadCfg := config.LoadAgentConfig("config/agents/my-thread.yaml")
backendCfg := backends.LoadConfig("config/backends/my-backend.yaml")
patterns := patterns.LoadLibrary("config/patterns/my-patterns.yaml")

// Create from config
backend := backends.NewFromConfig(backendCfg)
llm := createLLM()
thread := agent.NewAgent(
    backend,
    llm,
    agent.WithConfig(threadCfg),
    agent.WithPatterns(patterns),
)
```

## Validation

Each configuration directory includes `validate_test.go` to ensure YAML files are valid:

```bash
cd config/patterns
go test -v validate_test.go
```

This validates:
- YAML syntax
- Required fields
- Schema compliance
- Example correctness

## Next Steps

- Review existing patterns in `patterns/`
- Create your own domain-specific patterns
- Test configurations with validation tests

---

**Configuration-driven development makes agents reproducible, testable, and maintainable!**
