# Agents vs Agent-Templates - Complete Guide

## Quick Answer

| Feature | `config/agents/` | `config/agent-templates/` |
|---------|------------------|---------------------------|
| **Purpose** | Ready-to-run configurations | Reusable templates with variables |
| **Format** | `kind: Agent` | `kind: AgentTemplate` |
| **Variables** | ❌ No | ✅ Yes (`{{variable}}`) |
| **Inheritance** | ❌ No | ✅ Yes (`extends: base`) |
| **Parameters** | ❌ No | ✅ Yes (required/optional) |
| **Usage** | `looms agent create -f agents/foo.yaml` | `registry.ApplyTemplate("foo", vars)` |
| **Use Case** | Load and run immediately | Create specialized variants programmatically |

---

## config/agents/ - Complete Agent Configurations

**Purpose:** Production-ready agent definitions that can be loaded and run immediately.

**Characteristics:**
- All values are concrete (no variables or placeholders)
- No template inheritance
- No parameter definitions
- Can be used directly with `looms` CLI
- Configuration-only deployment

**File Format:**
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  version: 1.0.0
  description: "What this agent does"
  labels:
    backend: file
    maturity: stable

spec:
  backend:
    name: file
    config_file: examples/backends/file.yaml

  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.7
    max_tokens: 4096

  system_prompt: |
    Concrete instructions for the agent...

  config:
    max_turns: 15
    max_tool_executions: 30
    enable_tracing: true
```

**Examples in `config/agents/`:**

1. **file-analysis-agent.yaml** - File system operations
   - Backend: Local file system
   - Use: Read, write, analyze local files
   - Format: `kind: Agent` ✅

2. **github-agent.yaml** - GitHub API operations
   - Backend: REST API
   - Use: Repository analysis, issues, PRs
   - Format: `kind: Agent` ✅

3. **teradata-agent-with-patterns.yaml** - Teradata SQL with patterns
   - Backend: MCP (Teradata)
   - Use: SQL analytics with pattern libraries
   - Format: `kind: Agent` ✅

4. **web-search-agent.yaml** - Web search via Tavily API
   - Backend: REST API (Tavily)
   - Use: Web search and information retrieval
   - Format: `kind: Agent` ✅

5. **sql_expert.yaml** - SQL query expert
   - Backend: Database (via MCP)
   - Use: SQL generation and optimization
   - Format: Old `agent:` format ⚠️

6. **code_reviewer.yaml** - Code review agent
   - Backend: File system
   - Use: Code review and analysis
   - Format: Old `agent:` format ⚠️

7. **security_analyst.yaml** - Security analysis
   - Backend: File system
   - Use: Security vulnerability detection
   - Format: Old `agent:` format ⚠️

8. **presentation_agent.yaml** - Data presentation
   - Backend: Various
   - Use: Create visualizations and presentations
   - Format: Old `agent:` format ⚠️

9. **swarm-coordinator.yaml** - Multi-agent coordinator
   - Backend: N/A (meta-agent)
   - Use: Coordinate other agents
   - Format: `kind: Agent` (loom/v1) ✅

10. **llama-3.1-8b-optimized.yaml** - Optimized for Llama
    - Backend: Various
    - Use: Local LLM optimized config
    - Format: Custom ⚠️

**Usage:**
```bash
# Load and create agent from config
looms agent create -f config/agents/file-analysis-agent.yaml

# Chat with the agent
loom chat --agent file-analysis

# Or start server with agent preloaded
looms serve --config looms.yaml  # References agents in config
```

---

## config/agent-templates/ - Reusable Templates

**Purpose:** Base templates with variable substitution and inheritance for creating specialized agent variants programmatically.

**Characteristics:**
- Uses variables: `{{variable_name}}`
- Supports inheritance: `extends: parent-template`
- Defines parameters with validation (required/optional)
- Used programmatically via registry API
- Creates multiple specialized variants from one template

**File Format:**
```yaml
apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: sql-expert
  description: SQL query expert for specific database types
  version: "1.0"
  labels:
    category: database

extends: base-expert  # Inherit from parent

parameters:
  - name: database
    type: string
    required: true
    description: Database type (postgres, mysql, teradata)
  - name: schema
    type: string
    required: false
    default: "public"

spec:
  name: "{{database}}-sql-expert"
  system_prompt: |
    You are an expert in {{database}} databases.
    Use schema: {{schema}} as the default.

  llm:
    temperature: 0.3  # Override parent
    max_tokens: "{{max_tokens}}"

  memory:
    path: "./sessions/{{database}}.db"
```

**Examples in `config/agent-templates/`:**

1. **base-expert.yaml** - Foundation template
   - No parameters
   - No inheritance
   - Base configuration for all expert agents
   - Defines: LLM settings, memory, behavior

2. **sql-expert.yaml** - Database-specific SQL expert
   - Extends: `base-expert`
   - Parameters: `database` (required), `schema`, `max_tokens`
   - Creates: `postgres-sql-expert`, `mysql-sql-expert`, `teradata-sql-expert`

3. **security-analyst.yaml** - Cybersecurity expert
   - Extends: `base-expert`
   - Parameters: `focus_area`, `severity_threshold`
   - Creates: `web-security-analyst`, `api-security-analyst`, `infrastructure-security-analyst`

4. **code-reviewer.yaml** - Language-specific code reviewer
   - Extends: `base-expert`
   - Parameters: `language` (required), `style_guide`, `review_depth`
   - Creates: `go-code-reviewer`, `python-code-reviewer`, `typescript-code-reviewer`

**Usage (Programmatic):**
```go
// Load templates
registry := orchestration.NewTemplateRegistry()
registry.LoadTemplate("config/agent-templates/base-expert.yaml")
registry.LoadTemplate("config/agent-templates/sql-expert.yaml")

// Create specialized Postgres SQL expert
postgresConfig, err := registry.ApplyTemplate("sql-expert", map[string]string{
    "database":   "postgres",
    "schema":     "analytics",
    "max_tokens": "8192",
})

// Create specialized Teradata SQL expert
teradataConfig, err := registry.ApplyTemplate("sql-expert", map[string]string{
    "database":   "teradata",
    "schema":     "prod_db",
    "max_tokens": "16384",
})

// Use in agent creation
agent1 := agent.NewAgent(backend1, llm, agent.WithConfig(postgresConfig))
agent2 := agent.NewAgent(backend2, llm, agent.WithConfig(teradataConfig))
```

---

## Key Differences Explained

### 1. Variable Substitution

**agent-templates:**
```yaml
spec:
  name: "{{database}}-sql-expert"  # Variable
  system_prompt: "Expert in {{database}} SQL"  # Variable
  memory:
    path: "./sessions/{{database}}.db"  # Variable
```

**agents:**
```yaml
spec:
  name: "postgres-sql-expert"  # Concrete value
  system_prompt: "Expert in PostgreSQL SQL"  # Concrete value
  memory:
    path: "./sessions/postgres.db"  # Concrete value
```

### 2. Inheritance

**agent-templates:** ✅ Supported
```yaml
extends: base-expert  # Inherit all fields from base-expert

spec:
  llm:
    temperature: 0.3  # Override parent's temperature
    # model, provider, etc. inherited from base-expert
```

**agents:** ❌ Not supported
```yaml
# No inheritance - all values must be explicitly defined
spec:
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
    temperature: 0.7
    max_tokens: 4096
```

### 3. Parameters with Validation

**agent-templates:** ✅ Supported
```yaml
parameters:
  - name: database
    type: string
    required: true  # Must be provided
    description: Database type

  - name: schema
    type: string
    required: false  # Optional
    default: "public"  # Default if not provided
```

**agents:** ❌ Not supported
```yaml
# No parameter definitions - this is the final, concrete configuration
```

### 4. Usage Pattern

**agent-templates:**
```go
// Programmatic - create many variants from one template
for _, db := range []string{"postgres", "mysql", "teradata"} {
    config, _ := registry.ApplyTemplate("sql-expert", map[string]string{
        "database": db,
    })
    agents = append(agents, agent.NewAgent(backend, llm, agent.WithConfig(config)))
}
// Result: 3 specialized agents from 1 template
```

**agents:**
```bash
# CLI-based - load and run immediately
looms agent create -f config/agents/postgres-sql-expert.yaml
loom chat --agent postgres-sql-expert
# Result: 1 agent, ready to use
```

---

## When to Use Which?

### Use `config/agents/` when:
- ✅ You want configuration-only deployment (no code)
- ✅ You need to quickly load and run an agent
- ✅ You have a specific, fixed configuration
- ✅ You're deploying via `looms serve` with YAML config
- ✅ You want version-controlled agent definitions

**Example:**
"I need a GitHub API agent to analyze repositories - just load the config and go!"

### Use `config/agent-templates/` when:
- ✅ You need to create multiple similar agents programmatically
- ✅ You want to parameterize certain values
- ✅ You want to share common configuration via inheritance
- ✅ You're building a multi-agent system in code
- ✅ You need different variants of the same agent type

**Example:**
"I need SQL experts for Postgres, MySQL, and Teradata - create all 3 from one template!"

---

## Format Migration

**Current State:**
- 4 agents use old `agent:` format (code_reviewer, presentation_agent, security_analyst, sql_expert)
- 6 agents use new `kind: Agent` format
- All agent-templates use `kind: AgentTemplate` format

**Recommendation:**
Migrate old format agents to new format for consistency:

```yaml
# Old format ❌
agent:
  name: sql_expert
  llm:
    provider: anthropic

# New format ✅
apiVersion: loom/v1
kind: Agent
metadata:
  name: sql_expert
spec:
  llm:
    provider: anthropic
```

---

## Summary

| Aspect | Agents | Agent-Templates |
|--------|--------|-----------------|
| **Format** | `kind: Agent` | `kind: AgentTemplate` |
| **Ready to run** | ✅ Yes (CLI) | ❌ No (API only) |
| **Variables** | ❌ No | ✅ Yes (`{{var}}`) |
| **Inheritance** | ❌ No | ✅ Yes (`extends`) |
| **Parameters** | ❌ No | ✅ Yes (validated) |
| **Reusability** | One config = one agent | One template = many agents |
| **Use Case** | Fixed configurations | Parameterized variants |
| **Deployment** | Configuration-only | Code-based |

**Think of it this way:**
- **Agents** = Concrete instances (like Docker containers)
- **Agent-Templates** = Blueprints (like Dockerfiles)

You run agents directly, but you build agents from templates!
