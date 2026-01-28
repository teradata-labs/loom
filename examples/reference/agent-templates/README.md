# Agent Templates

This directory contains reusable agent configuration templates for Loom. Templates support inheritance, variable substitution, and parameter validation, making it easy to create specialized agents from common base configurations.

## Available Templates

### 1. Base Expert (`base-expert.yaml`)

Foundation template with sensible defaults for expert agents.

**Features:**
- Claude 3.5 Sonnet model
- Balanced temperature (0.7)
- SQLite memory with 100 message history
- 10 iteration limit, 10-minute timeout

**Use as:** Parent template for specialized agents

### 2. SQL Expert (`sql-expert.yaml`)

Database-specific SQL query and optimization expert.

**Extends:** `base-expert`

**Parameters:**
- `database` (required): Database type (postgres, mysql, teradata, oracle, etc.)
- `schema` (optional, default: "public"): Default schema
- `max_tokens` (optional, default: "8192"): Token limit for complex queries

**Example Usage:**
```go
registry := orchestration.NewTemplateRegistry()
err := registry.LoadTemplate("examples/agent-templates/base-expert.yaml")
err = registry.LoadTemplate("examples/agent-templates/sql-expert.yaml")

// Create Postgres SQL expert
config, err := registry.ApplyTemplate("sql-expert", map[string]string{
    "database": "postgres",
    "schema":   "analytics",
})

// Create Teradata SQL expert
config, err = registry.ApplyTemplate("sql-expert", map[string]string{
    "database": "teradata",
    "schema":   "prod_db",
    "max_tokens": "16384",
})
```

**Features:**
- Lower temperature (0.3) for precise SQL generation
- Database-specific best practices
- Query optimization focus
- Code interpreter tool for SQL validation

### 3. Security Analyst (`security-analyst.yaml`)

Cybersecurity vulnerability detection and analysis expert.

**Extends:** `base-expert`

**Parameters:**
- `focus_area` (optional, default: "general"): Security specialization (web, api, infrastructure, code)
- `severity_threshold` (optional, default: "medium"): Minimum severity to report

**Example Usage:**
```go
// Create web security analyst
config, err := registry.ApplyTemplate("security-analyst", map[string]string{
    "focus_area": "web",
    "severity_threshold": "high",
})

// Create API security analyst
config, err = registry.ApplyTemplate("security-analyst", map[string]string{
    "focus_area": "api",
})
```

**Features:**
- OWASP compliance
- CVE reference lookups
- Severity-based filtering
- Extended iteration limit (15) for thorough analysis
- Web search for security intelligence

### 4. Code Reviewer (`code-reviewer.yaml`)

Programming language-specific code review expert.

**Extends:** `base-expert`

**Parameters:**
- `language` (required): Programming language (go, python, typescript, java, etc.)
- `style_guide` (optional, default: "standard"): Code style guide
- `review_depth` (optional, default: "standard"): Thoroughness level (quick, standard, deep)

**Example Usage:**
```go
// Create Go code reviewer with standard depth
config, err := registry.ApplyTemplate("code-reviewer", map[string]string{
    "language": "go",
    "style_guide": "effective-go",
})

// Create Python code reviewer with deep analysis
config, err = registry.ApplyTemplate("code-reviewer", map[string]string{
    "language": "python",
    "style_guide": "pep8",
    "review_depth": "deep",
})
```

**Features:**
- Language-specific best practices
- Comprehensive review checklist (correctness, style, performance, security)
- Severity ratings (BLOCKER, MAJOR, MINOR, SUGGESTION)
- Extended timeout (20 minutes) for thorough reviews
- Code interpreter for syntax validation

## Template Features

### Variable Substitution

Templates support two variable syntaxes:

1. **Curly braces**: `{{variable}}` - Recommended for YAML values
2. **Dollar braces**: `${variable}` - Supports environment variables

**Example:**
```yaml
spec:
  name: "{{database}}-expert"
  system_prompt: "API Key: ${API_KEY}"
  memory:
    path: "./sessions/{{database}}-{{schema}}.db"
```

### Template Inheritance

Child templates inherit all configuration from parent templates. Child values override parent values.

**Example:**
```yaml
# Parent: base-expert.yaml
llm:
  provider: anthropic
  temperature: 0.7
  max_tokens: 4096

# Child: sql-expert.yaml
extends: base-expert
llm:
  temperature: 0.3  # Override temperature
  max_tokens: "{{max_tokens}}"  # Override with variable

# Result: Inherits provider=anthropic, overrides temperature and max_tokens
```

**Multi-level Inheritance:**
```yaml
# Level 1: base-expert
# Level 2: sql-expert extends base-expert
# Level 3: teradata-expert extends sql-expert
```

### Parameter Validation

Templates can define required and optional parameters:

```yaml
parameters:
  - name: database
    type: string
    required: true
    description: Database type

  - name: schema
    type: string
    required: false
    default: "public"
    description: Default schema
```

- **Required parameters**: Must be provided or an error is returned
- **Optional parameters**: Use default value if not provided
- **Type validation**: Supported types: string, int, bool

## Creating New Templates

### 1. Start with Base Template

```yaml
apiVersion: loom/v1
kind: AgentTemplate
metadata:
  name: my-template
  description: My custom template
  version: "1.0"
  labels:
    category: custom

spec:
  name: my-agent
  system_prompt: "Your agent prompt"
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20250131
```

### 2. Add Parameters

```yaml
parameters:
  - name: domain
    type: string
    required: true
  - name: max_tokens
    type: int
    default: "4096"
```

### 3. Use Variables

```yaml
spec:
  name: "{{domain}}-expert"
  system_prompt: "Expert in {{domain}}"
  memory:
    path: "./sessions/{{domain}}.db"
```

### 4. Extend Existing Template

```yaml
extends: base-expert

spec:
  system_prompt: |
    {{parent_prompt}}  # Inherit parent prompt if needed
    Additional instructions...
```

## Testing Templates

Verify your template loads correctly:

```go
registry := orchestration.NewTemplateRegistry()

// Load template
err := registry.LoadTemplate("path/to/template.yaml")
if err != nil {
    log.Fatal(err)
}

// Apply with variables
vars := map[string]string{
    "database": "postgres",
    "schema": "analytics",
}

config, err := registry.ApplyTemplate("sql-expert", vars)
if err != nil {
    log.Fatal(err)
}

// Use config to create agent
agent := agent.NewAgent(backend, llmProvider, agent.WithConfig(config))
```

## Best Practices

1. **Use inheritance** to avoid duplication - Create base templates for common patterns
2. **Parameterize** specialized values - Use variables for database names, languages, etc.
3. **Provide defaults** for optional parameters - Make templates easy to use
4. **Document parameters** in descriptions - Help users understand what each does
5. **Test templates** with different variable combinations - Ensure they work correctly
6. **Version templates** using metadata.version - Track breaking changes
7. **Use labels** for categorization - Make templates discoverable
8. **Keep prompts focused** - Each template should have a clear purpose

## Environment Variables

Templates automatically have access to environment variables using `${VAR}` syntax:

```yaml
spec:
  system_prompt: "Database connection: ${DATABASE_URL}"
  metadata:
    api_key: "${ANTHROPIC_API_KEY}"
```

**Note:** Template parameters take precedence over environment variables.

## Error Handling

Common template errors:

- **Template not found**: Check file path and template name
- **Missing required parameter**: Provide all required parameters in `vars`
- **Circular reference**: Template inheritance cannot form cycles (A → B → A)
- **Invalid YAML**: Check YAML syntax and indentation
- **Invalid apiVersion**: Use `loom/v1`
- **Invalid kind**: Use `AgentTemplate` or `Agent`

## Integration with Workflows

Templates work seamlessly with workflow orchestration:

```go
// Create specialized agents from templates
sqlAgent, _ := registry.ApplyTemplate("sql-expert", map[string]string{
    "database": "postgres",
})

securityAgent, _ := registry.ApplyTemplate("security-analyst", map[string]string{
    "focus_area": "api",
})

// Use in workflow
orchestrator.Debate().
    WithTopic("API security for Postgres backend").
    WithAgents(sqlAgent, securityAgent).
    WithRounds(3).
    Execute(ctx)
```

## See Also

- [Workflow Orchestration Guide](../workflows/README.md)
- [Agent Configuration Proto](../../proto/loom/v1/agent_config.proto)
- [Template Implementation](../../pkg/orchestration/agent_template.go)
