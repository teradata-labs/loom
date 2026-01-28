
# Meta-Agent Tools Reference

Complete specification for meta-agent factory tools. These builtin tools enable the meta-agent to analyze requirements, generate agent configurations, validate settings, test connectivity, and spawn ephemeral agents.

**Version**: v1.0.0-beta.2
**Package**: `pkg/metaagent/tools`
**Tool Count**: 18 tools across 6 categories


## Table of Contents

- [Quick Reference](#quick-reference)
- [Analysis Tools](#analysis-tools)
  - [classify_domain](#classify_domain)
  - [extract_capabilities](#extract_capabilities)
  - [identify_data_sources](#identify_data_sources)
  - [infer_workflow_needs](#infer_workflow_needs)
- [Generation Tools](#generation-tools)
  - [select_template](#select_template)
  - [select_patterns](#select_patterns)
  - [compose_workflow](#compose_workflow)
  - [generate_yaml](#generate_yaml)
- [Validation Tools](#validation-tools)
  - [validate_config](#validate_config)
  - [check_pattern_dependencies](#check_pattern_dependencies)
- [Testing Tools](#testing-tools)
  - [test_backend_connection](#test_backend_connection)
  - [test_pattern](#test_pattern)
  - [test_end_to_end](#test_end_to_end)
- [Spawning Tools](#spawning-tools)
  - [spawn_ephemeral_agent](#spawn_ephemeral_agent)
  - [attach_session](#attach_session)
  - [monitor_spawned_agent](#monitor_spawned_agent)
- [Learning Tools](#learning-tools)
  - [record_success](#record_success)
  - [suggest_improvements](#suggest_improvements)
- [Tool Registration](#tool-registration)
- [Error Codes](#error-codes)
- [Examples](#examples)
- [Testing](#testing)
- [See Also](#see-also)


## Quick Reference

### Tool Categories

| Category | Tools | Purpose |
|----------|-------|---------|
| **Analysis** | 4 | Requirements analysis and domain classification |
| **Generation** | 4 | YAML generation and pattern selection |
| **Validation** | 2 | Configuration validation and dependency checking |
| **Testing** | 3 | Backend connectivity and end-to-end testing |
| **Spawning** | 3 | Ephemeral agent lifecycle management |
| **Learning** | 2 | Success tracking and optimization |

### All Tools Summary

| Tool | Category | Purpose | Timeout |
|------|----------|---------|---------|
| `classify_domain` | Analysis | Determine primary domain from requirements | 10s |
| `extract_capabilities` | Analysis | Identify required capabilities | 10s |
| `identify_data_sources` | Analysis | Detect data sources (DB, API, files) | 10s |
| `infer_workflow_needs` | Analysis | Determine orchestration needs | 5s |
| `select_template` | Generation | Choose agent template | 5s |
| `select_patterns` | Generation | Select relevant patterns | 10s |
| `compose_workflow` | Generation | Generate workflow config | 10s |
| `generate_yaml` | Generation | Generate complete YAML files | 15s |
| `validate_config` | Validation | Validate agent configuration | 10s |
| `check_pattern_dependencies` | Validation | Verify pattern dependencies | 5s |
| `test_backend_connection` | Testing | Test backend connectivity | 30s |
| `test_pattern` | Testing | Validate pattern execution | 30s |
| `test_end_to_end` | Testing | Full agent simulation test | 120s |
| `spawn_ephemeral_agent` | Spawning | Create ephemeral agent instance | 30s |
| `attach_session` | Spawning | Create/retrieve session | 5s |
| `monitor_spawned_agent` | Spawning | Track agent health | 5s |
| `record_success` | Learning | Record successful deployment | 5s |
| `suggest_improvements` | Learning | AI-powered suggestions | 15s |


## Analysis Tools

### classify_domain

**Implementation**: `builtin://meta-agent/classify_domain`
**Timeout**: 10 seconds

**Purpose**: Determine the primary domain from user requirements.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "requirements": {
      "type": "string",
      "description": "User's natural language requirements"
    }
  },
  "required": ["requirements"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "domain": {
      "type": "string",
      "enum": ["sql", "rest", "graphql", "grpc", "file", "document", "mcp", "hybrid"]
    },
    "confidence": {
      "type": "number",
      "minimum": 0,
      "maximum": 1
    },
    "specific_type": {
      "type": "string",
      "description": "postgres, mysql, sqlite, etc."
    },
    "rationale": {
      "type": "string",
      "description": "Why this domain was chosen"
    }
  }
}
```

**Example**:
```json
// Input:
{
  "requirements": "I need an agent that analyzes PostgreSQL slow queries and suggests indexes"
}

// Output:
{
  "domain": "sql",
  "confidence": 0.95,
  "specific_type": "postgres",
  "rationale": "Requirements mention PostgreSQL database operations (slow query analysis, index suggestions)"
}
```

**Classification algorithm**:
1. Keyword matching on domain-specific terms
2. Database name detection (postgres, mysql, sqlite, etc.)
3. Operation type detection (query, API call, file processing)
4. Confidence scoring based on keyword density

**Common domains**:
- `sql` - PostgreSQL, MySQL, SQLite, SQL Server, Teradata
- `rest` - REST API integration
- `graphql` - GraphQL API integration
- `grpc` - gRPC service integration
- `file` - File system operations
- `document` - Document search, embeddings
- `mcp` - Model Context Protocol servers
- `hybrid` - Multiple domain types

**Thread safety**: Safe for concurrent use


### extract_capabilities

**Implementation**: `builtin://meta-agent/extract_capabilities`
**Timeout**: 10 seconds

**Purpose**: Identify required agent capabilities from requirements.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "requirements": {
      "type": "string"
    },
    "domain": {
      "type": "string"
    }
  },
  "required": ["requirements"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "capabilities": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "description": {"type": "string"},
          "category": {"type": "string"},
          "priority": {"type": "string", "enum": ["high", "medium", "low"]}
        }
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "requirements": "analyze slow queries, suggest indexes, generate migration scripts",
  "domain": "sql"
}

// Output:
{
  "capabilities": [
    {
      "name": "sql_performance_analysis",
      "description": "Analyze query performance using EXPLAIN plans",
      "category": "performance",
      "priority": "high"
    },
    {
      "name": "index_optimization",
      "description": "Suggest index improvements",
      "category": "optimization",
      "priority": "high"
    },
    {
      "name": "schema_migration",
      "description": "Generate safe migration scripts",
      "category": "migration",
      "priority": "medium"
    }
  ]
}
```

**Capability categories**:
- `performance` - Query optimization, profiling
- `optimization` - Index suggestions, schema improvements
- `migration` - Schema changes, data migration
- `analytics` - Metrics, reporting
- `validation` - Data quality, constraints
- `transformation` - ETL, data processing

**Thread safety**: Safe for concurrent use


### identify_data_sources

**Implementation**: `builtin://meta-agent/identify_data_sources`
**Timeout**: 10 seconds

**Purpose**: Detect databases, APIs, files, or services mentioned in requirements.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "requirements": {"type": "string"}
  },
  "required": ["requirements"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "data_sources": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "type": {"type": "string"},
          "connection_info": {"type": "object"},
          "authentication": {"type": "string"}
        }
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "requirements": "Connect to production PostgreSQL at db.example.com:5432 and staging API at api.staging.example.com"
}

// Output:
{
  "data_sources": [
    {
      "name": "production_postgres",
      "type": "postgres",
      "connection_info": {
        "host": "db.example.com",
        "port": 5432
      },
      "authentication": "password"
    },
    {
      "name": "staging_api",
      "type": "rest_api",
      "connection_info": {
        "base_url": "https://api.staging.example.com"
      },
      "authentication": "bearer_token"
    }
  ]
}
```

**Detection patterns**:
- Database URLs: `postgres://`, `mysql://`, `sqlite://`
- API endpoints: `https://`, `http://`, `api.`
- File paths: `/path/to/`, `./`, `~/`
- Connection strings: `host:port`, `server=`, `database=`

**Thread safety**: Safe for concurrent use


### infer_workflow_needs

**Implementation**: `builtin://meta-agent/infer_workflow_needs`
**Timeout**: 5 seconds

**Purpose**: Determine if orchestration is needed and what type.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "capabilities": {"type": "array"},
    "data_sources": {"type": "array"}
  },
  "required": ["capabilities"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "workflow_type": {
      "type": "string",
      "enum": ["simple", "pipeline", "parallel", "fork_join", "debate", "conditional"]
    },
    "complexity": {
      "type": "string",
      "enum": ["low", "medium", "high"]
    },
    "rationale": {"type": "string"}
  }
}
```

**Example**:
```json
// Input:
{
  "capabilities": [
    {"name": "sql_performance_analysis", "priority": "high"},
    {"name": "index_optimization", "priority": "high"},
    {"name": "schema_migration", "priority": "medium"}
  ],
  "data_sources": [
    {"name": "production_db", "type": "postgres"}
  ]
}

// Output:
{
  "workflow_type": "pipeline",
  "complexity": "medium",
  "rationale": "Sequential workflow needed: analyze → optimize → migrate"
}
```

**Workflow types**:
- `simple` - Single agent, no orchestration
- `pipeline` - Sequential multi-step workflow
- `parallel` - Independent parallel tasks
- `fork_join` - Parallel tasks with join point
- `debate` - Multi-agent debate/consensus
- `conditional` - Conditional branching

**Complexity factors**:
- Number of capabilities: >5 = high complexity
- Number of data sources: >2 = medium complexity
- Capability dependencies: Deep = high complexity

**Thread safety**: Safe for concurrent use


## Generation Tools

### select_template

**Implementation**: `builtin://meta-agent/select_template`
**Timeout**: 5 seconds

**Purpose**: Choose appropriate agent template based on requirements.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "domain": {"type": "string"},
    "capabilities": {"type": "array"},
    "data_sources": {"type": "array"}
  },
  "required": ["domain", "capabilities"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "template_name": {"type": "string"},
    "template": {
      "type": "object",
      "properties": {
        "base_prompt": {"type": "string"},
        "required_patterns": {"type": "array"},
        "default_tools": {"type": "array"},
        "constraints": {"type": "object"}
      }
    },
    "match_score": {"type": "number"},
    "customizations_needed": {"type": "array"}
  }
}
```

**Example**:
```json
// Input:
{
  "domain": "sql",
  "capabilities": [
    {"name": "sql_performance_analysis", "priority": "high"}
  ],
  "data_sources": [
    {"name": "prod_db", "type": "postgres"}
  ]
}

// Output:
{
  "template_name": "postgres_performance_analyst",
  "template": {
    "base_prompt": "Analyze PostgreSQL query performance and suggest optimizations.",
    "required_patterns": ["query_analysis", "index_suggestions"],
    "default_tools": ["execute_query", "explain_analyze", "list_indexes"],
    "constraints": {
      "max_turns": 10,
      "max_tool_executions": 20
    }
  },
  "match_score": 0.92,
  "customizations_needed": ["Add specific slow query patterns"]
}
```

**Available templates**:
- `postgres_performance_analyst` - PostgreSQL optimization
- `mysql_dba_assistant` - MySQL administration
- `rest_api_integrator` - REST API integration
- `document_search_agent` - Vector search/embeddings
- `data_quality_auditor` - Data validation

**Template matching algorithm**:
1. Domain exact match (+40 points)
2. Capability overlap (+10 points per match)
3. Data source compatibility (+20 points)
4. Score normalized to 0.0-1.0

**Thread safety**: Safe for concurrent use


### select_patterns

**Implementation**: `builtin://meta-agent/select_patterns`
**Timeout**: 10 seconds

**Purpose**: Select relevant patterns from the pattern library.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "capabilities": {"type": "array"},
    "domain": {"type": "string"},
    "max_patterns": {"type": "integer", "default": 10}
  },
  "required": ["capabilities"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "patterns": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "category": {"type": "string"},
          "relevance_score": {"type": "number"},
          "rationale": {"type": "string"}
        }
      }
    },
    "dependencies": {
      "type": "array",
      "description": "Additional patterns required as dependencies"
    }
  }
}
```

**Example**:
```json
// Input:
{
  "capabilities": [
    {"name": "sql_performance_analysis", "category": "performance"}
  ],
  "domain": "sql",
  "max_patterns": 5
}

// Output:
{
  "patterns": [
    {
      "name": "query_performance_analysis",
      "category": "analytics",
      "relevance_score": 0.95,
      "rationale": "Directly matches sql_performance_analysis capability"
    },
    {
      "name": "index_optimization",
      "category": "analytics",
      "relevance_score": 0.88,
      "rationale": "Commonly needed with performance analysis"
    }
  ],
  "dependencies": [
    "schema_discovery"
  ]
}
```

**Pattern selection algorithm**:
1. Match capability names to pattern names/tags (exact = 1.0, partial = 0.5)
2. Match domain to pattern backend_type (exact = +0.3)
3. Apply pattern dependencies (required patterns = +0.2)
4. Sort by relevance_score descending
5. Return top N patterns (max_patterns)

**Thread safety**: Safe for concurrent use


### compose_workflow

**Implementation**: `builtin://meta-agent/compose_workflow`
**Timeout**: 10 seconds

**Purpose**: Generate workflow orchestration configuration.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "workflow_type": {"type": "string"},
    "patterns": {"type": "array"},
    "data_sources": {"type": "array"}
  },
  "required": ["workflow_type", "patterns"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "workflow": {
      "type": "object",
      "properties": {
        "type": {"type": "string"},
        "stages": {"type": "array"},
        "error_handling": {"type": "string"}
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "workflow_type": "pipeline",
  "patterns": [
    {"name": "query_analysis"},
    {"name": "index_optimization"}
  ],
  "data_sources": [
    {"name": "prod_db", "type": "postgres"}
  ]
}

// Output:
{
  "workflow": {
    "type": "pipeline",
    "stages": [
      {
        "name": "analyze",
        "agent": "performance_analyst",
        "pattern": "query_analysis"
      },
      {
        "name": "optimize",
        "agent": "index_optimizer",
        "pattern": "index_optimization"
      }
    ],
    "error_handling": "stop_on_error"
  }
}
```

**Workflow composition rules**:
- `pipeline` - Sequential stages with output chaining
- `parallel` - Independent stages executed concurrently
- `fork_join` - Parallel execution with synchronization point
- `debate` - Multiple agents with voting/consensus
- `conditional` - Branching based on stage outputs

**Error handling strategies**:
- `stop_on_error` - Halt workflow on first error
- `continue_on_error` - Log error, continue to next stage
- `retry_with_backoff` - Exponential backoff retry

**Thread safety**: Safe for concurrent use


### generate_yaml

**Implementation**: `builtin://meta-agent/generate_yaml`
**Timeout**: 15 seconds

**Purpose**: Generate complete YAML configuration files.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "spec": {
      "type": "object",
      "description": "Agent specification"
    },
    "output_format": {
      "type": "string",
      "enum": ["single_file", "multi_file"],
      "default": "multi_file"
    }
  },
  "required": ["spec"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "files": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path": {"type": "string"},
          "content": {"type": "string"},
          "description": {"type": "string"}
        }
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "spec": {
    "name": "postgres-optimizer",
    "domain": "sql",
    "backend_type": "postgres",
    "capabilities": ["performance_analysis"],
    "patterns": ["query_analysis"]
  },
  "output_format": "multi_file"
}

// Output:
{
  "files": [
    {
      "path": "agents/postgres-optimizer.yaml",
      "content": "apiVersion: loom/v1\nkind: Agent\nmetadata:\n  name: postgres-optimizer\n...",
      "description": "Main agent configuration"
    },
    {
      "path": "backends/postgres-prod.yaml",
      "content": "apiVersion: loom/v1\nkind: Backend\nmetadata:\n  name: postgres-prod\n...",
      "description": "PostgreSQL backend configuration"
    }
  ]
}
```

**Generated file structure**:
```
agents/
  {agent_name}.yaml          # Main agent config
backends/
  {backend_name}.yaml        # Backend config
patterns/
  {pattern_name}.yaml        # Custom patterns (optional)
workflows/
  {workflow_name}.yaml       # Workflow config (if orchestration)
```

**YAML generation rules**:
- No role prompting (task-oriented instructions only)
- All required fields populated
- Sensible defaults for optional fields
- Validation-ready output

**Thread safety**: Safe for concurrent use


## Validation Tools

### validate_config

**Implementation**: `builtin://meta-agent/validate_config`
**Timeout**: 10 seconds

**Purpose**: Validate generated configuration against all rules.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "config": {
      "type": "object",
      "description": "Agent configuration to validate"
    },
    "strict": {
      "type": "boolean",
      "default": true,
      "description": "Fail on warnings"
    }
  },
  "required": ["config"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "valid": {"type": "boolean"},
    "errors": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "code": {"type": "string"},
          "message": {"type": "string"},
          "field": {"type": "string"},
          "suggestion": {"type": "string"}
        }
      }
    },
    "warnings": {"type": "array"},
    "score": {
      "type": "number",
      "description": "Quality score 0-100"
    }
  }
}
```

**Example**:
```json
// Input:
{
  "config": {
    "apiVersion": "loom/v1",
    "kind": "Agent",
    "metadata": {"name": "test-agent"},
    "spec": {
      "system_prompt": "You are a SQL expert who analyzes queries.",
      "llm": {"provider": "anthropic", "model": "claude-sonnet-4-5-20250929"}
    }
  },
  "strict": true
}

// Output:
{
  "valid": false,
  "errors": [
    {
      "code": "ROLE_PROMPTING_DETECTED",
      "message": "System prompt contains role-playing language",
      "field": "spec.system_prompt",
      "suggestion": "Use task-oriented prompts: 'Analyze SQL queries' instead of 'You are a SQL expert who analyzes'"
    }
  ],
  "warnings": [
    {
      "code": "MISSING_SSL",
      "message": "Production deployment without SSL",
      "field": "spec.backend.enable_ssl"
    }
  ],
  "score": 75
}
```

**Validation rules**:
1. **Schema validation** - All required fields present, correct types
2. **Role prompting detection** - No "You are..." or "As a..." phrases
3. **Security checks** - SSL enabled for production, no hardcoded secrets
4. **Resource limits** - Sensible timeouts, token limits
5. **Pattern dependencies** - All referenced patterns exist

**Quality scoring**:
- 100: Perfect configuration
- 90-99: Minor warnings
- 75-89: Some warnings or non-critical errors
- <75: Critical errors or many warnings

**Thread safety**: Safe for concurrent use


### check_pattern_dependencies

**Implementation**: `builtin://meta-agent/check_pattern_dependencies`
**Timeout**: 5 seconds

**Purpose**: Verify all pattern dependencies are satisfied.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "patterns": {"type": "array"}
  },
  "required": ["patterns"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "satisfied": {"type": "boolean"},
    "missing_dependencies": {"type": "array"},
    "suggestions": {"type": "array"}
  }
}
```

**Example**:
```json
// Input:
{
  "patterns": ["query_optimization", "index_suggestions"]
}

// Output:
{
  "satisfied": false,
  "missing_dependencies": ["schema_discovery"],
  "suggestions": [
    "Add 'schema_discovery' pattern (required by 'query_optimization')"
  ]
}
```

**Dependency resolution**:
1. Load pattern definitions
2. Extract dependencies from each pattern
3. Check if all dependencies present in pattern list
4. Return missing dependencies with suggestions

**Thread safety**: Safe for concurrent use


## Testing Tools

### test_backend_connection

**Implementation**: `builtin://meta-agent/test_backend_connection`
**Timeout**: 30 seconds

**Purpose**: Test backend connectivity before deployment.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "backend_config": {"type": "object"}
  },
  "required": ["backend_config"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "success": {"type": "boolean"},
    "message": {"type": "string"},
    "latency_ms": {"type": "number"},
    "details": {"type": "object"}
  }
}
```

**Example**:
```json
// Input:
{
  "backend_config": {
    "type": "postgres",
    "host": "localhost",
    "port": 5432,
    "database": "test_db"
  }
}

// Output:
{
  "success": true,
  "message": "Connection successful",
  "latency_ms": 45.3,
  "details": {
    "server_version": "16.1",
    "ssl_enabled": true,
    "max_connections": 100
  }
}
```

**Connection tests performed**:
- TCP connectivity (host:port reachable)
- Authentication (credentials valid)
- Database exists (if applicable)
- SSL/TLS negotiation (if configured)
- Query execution (simple SELECT 1)
- Latency measurement (round-trip time)

**Thread safety**: Safe for concurrent use


### test_pattern

**Implementation**: `builtin://meta-agent/test_pattern`
**Timeout**: 30 seconds

**Purpose**: Validate pattern execution with test data.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "pattern_name": {"type": "string"},
    "test_data": {"type": "object"}
  },
  "required": ["pattern_name"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "success": {"type": "boolean"},
    "output": {"type": "string"},
    "execution_time_ms": {"type": "number"},
    "errors": {"type": "array"}
  }
}
```

**Example**:
```json
// Input:
{
  "pattern_name": "revenue_aggregation",
  "test_data": {
    "dimension": "region",
    "metric": "revenue",
    "table": "sales"
  }
}

// Output:
{
  "success": true,
  "output": "SELECT region, SUM(revenue) as total FROM sales GROUP BY region ORDER BY total DESC",
  "execution_time_ms": 5.2,
  "errors": []
}
```

**Pattern testing steps**:
1. Load pattern from library
2. Validate YAML structure
3. Execute template with test_data
4. Validate generated output
5. Measure execution time

**Thread safety**: Safe for concurrent use


### test_end_to_end

**Implementation**: `builtin://meta-agent/test_end_to_end`
**Timeout**: 120 seconds (2 minutes)

**Purpose**: Full agent simulation test with multiple scenarios.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "config": {"type": "object"},
    "test_scenarios": {"type": "array"}
  },
  "required": ["config"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "success": {"type": "boolean"},
    "scenarios_passed": {"type": "integer"},
    "scenarios_failed": {"type": "integer"},
    "results": {"type": "array"},
    "metrics": {
      "type": "object",
      "properties": {
        "avg_response_time_ms": {"type": "number"},
        "tool_execution_count": {"type": "integer"},
        "error_rate": {"type": "number"}
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "config": {
    "agent_name": "postgres-optimizer",
    "backend_type": "postgres"
  },
  "test_scenarios": [
    {
      "name": "Simple query",
      "input": "Show me all tables",
      "expected_tool": "list_tables"
    },
    {
      "name": "Performance analysis",
      "input": "Analyze slow queries",
      "expected_tool": "explain_analyze"
    }
  ]
}

// Output:
{
  "success": true,
  "scenarios_passed": 2,
  "scenarios_failed": 0,
  "results": [
    {
      "scenario": "Simple query",
      "passed": true,
      "response_time_ms": 234,
      "tools_used": ["list_tables"]
    },
    {
      "scenario": "Performance analysis",
      "passed": true,
      "response_time_ms": 1523,
      "tools_used": ["get_schema", "explain_analyze"]
    }
  ],
  "metrics": {
    "avg_response_time_ms": 878.5,
    "tool_execution_count": 3,
    "error_rate": 0.0
  }
}
```

**End-to-end test steps**:
1. Create agent instance from config
2. For each test scenario:
   - Send input query
   - Monitor tool executions
   - Validate expected behavior
   - Measure response time
3. Aggregate metrics across all scenarios

**Thread safety**: Not safe for concurrent use (creates agent instances)


## Spawning Tools

### spawn_ephemeral_agent

**Implementation**: `builtin://ephemeral/spawn`
**Timeout**: 30 seconds

**Purpose**: Create ephemeral agent instance immediately for user interaction.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "config": {
      "type": "object",
      "description": "Agent configuration"
    },
    "auto_attach": {
      "type": "boolean",
      "default": true,
      "description": "Automatically create session"
    },
    "ttl_seconds": {
      "type": "integer",
      "default": 3600,
      "description": "Time-to-live in seconds"
    }
  },
  "required": ["config"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"},
    "session_id": {"type": "string"},
    "endpoint": {"type": "string"},
    "status": {"type": "string"},
    "created_at": {"type": "string", "format": "date-time"},
    "expires_at": {"type": "string", "format": "date-time"}
  }
}
```

**Example**:
```json
// Input:
{
  "config": {
    "name": "postgres-opt",
    "backend_type": "postgres",
    "llm": {"provider": "anthropic", "model": "claude-sonnet-4-5-20250929"}
  },
  "auto_attach": true,
  "ttl_seconds": 1800
}

// Output:
{
  "agent_id": "agent-postgres-opt-abc123",
  "session_id": "session-xyz789",
  "endpoint": "looms://agents/agent-postgres-opt-abc123",
  "status": "ready",
  "created_at": "2025-12-11T10:30:00Z",
  "expires_at": "2025-12-11T11:00:00Z"
}
```

**Ephemeral agent lifecycle**:
1. **Create** - Agent spawned with generated ID
2. **Ready** - Agent initialized, ready for queries
3. **Active** - Agent processing queries
4. **Idle** - No activity for configured timeout
5. **Expired** - TTL reached, agent terminated
6. **Failed** - Agent encountered fatal error

**Resource management**:
- Memory: Cleaned up on expiration
- Sessions: Persisted if configured
- Logs: Retained for debugging

**Thread safety**: Safe for concurrent use


### attach_session

**Implementation**: `builtin://ephemeral/attach`
**Timeout**: 5 seconds

**Purpose**: Create or retrieve session for user interaction with agent.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"},
    "user_id": {"type": "string"}
  },
  "required": ["agent_id"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "session_id": {"type": "string"},
    "agent_id": {"type": "string"},
    "status": {"type": "string"},
    "message_count": {"type": "integer"}
  }
}
```

**Example**:
```json
// Input:
{
  "agent_id": "agent-postgres-opt-abc123",
  "user_id": "user-john"
}

// Output:
{
  "session_id": "session-xyz789",
  "agent_id": "agent-postgres-opt-abc123",
  "status": "active",
  "message_count": 5
}
```

**Session behavior**:
- **New session**: Created if none exists for user+agent
- **Existing session**: Returned if active session found
- **Expired session**: New session created, old one archived

**Thread safety**: Safe for concurrent use


### monitor_spawned_agent

**Implementation**: `builtin://ephemeral/monitor`
**Timeout**: 5 seconds

**Purpose**: Track spawned agent performance and health.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"}
  },
  "required": ["agent_id"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"},
    "status": {"type": "string"},
    "uptime_seconds": {"type": "integer"},
    "resource_usage": {
      "type": "object",
      "properties": {
        "memory_mb": {"type": "integer"},
        "cpu_percent": {"type": "number"}
      }
    },
    "metrics": {
      "type": "object",
      "properties": {
        "message_count": {"type": "integer"},
        "tool_execution_count": {"type": "integer"},
        "error_count": {"type": "integer"}
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "agent_id": "agent-postgres-opt-abc123"
}

// Output:
{
  "agent_id": "agent-postgres-opt-abc123",
  "status": "active",
  "uptime_seconds": 1243,
  "resource_usage": {
    "memory_mb": 128,
    "cpu_percent": 2.3
  },
  "metrics": {
    "message_count": 15,
    "tool_execution_count": 42,
    "error_count": 1
  }
}
```

**Monitored metrics**:
- **Status**: Current agent state
- **Uptime**: Time since spawn
- **Memory**: Current memory usage
- **CPU**: Average CPU utilization
- **Messages**: Total messages processed
- **Tools**: Total tool executions
- **Errors**: Error count

**Thread safety**: Safe for concurrent use


## Learning Tools

### record_success

**Implementation**: `builtin://meta-agent/record_success`
**Timeout**: 5 seconds

**Purpose**: Record successful agent deployment for future learning.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "agent_id": {"type": "string"},
    "config": {"type": "object"},
    "metrics": {
      "type": "object",
      "properties": {
        "success_rate": {"type": "number"},
        "user_satisfaction": {"type": "number"},
        "execution_time": {"type": "number"}
      }
    }
  },
  "required": ["agent_id", "config", "metrics"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "recorded": {"type": "boolean"},
    "learning_updates": {"type": "array"}
  }
}
```

**Example**:
```json
// Input:
{
  "agent_id": "agent-postgres-opt-abc123",
  "config": {
    "domain": "sql",
    "patterns": ["query_optimization"],
    "capabilities": ["performance_analysis"]
  },
  "metrics": {
    "success_rate": 0.95,
    "user_satisfaction": 4.5,
    "execution_time": 234.5
  }
}

// Output:
{
  "recorded": true,
  "learning_updates": [
    "Updated template 'postgres_performance_analyst' with success metrics",
    "Reinforced pattern 'query_optimization' for domain 'sql'"
  ]
}
```

**Learning mechanisms**:
1. **Template ranking** - Successful templates ranked higher
2. **Pattern reinforcement** - Successful patterns weighted more
3. **Capability mapping** - Link capabilities to patterns
4. **Performance tracking** - Store execution time baselines

**Thread safety**: Safe for concurrent use


### suggest_improvements

**Implementation**: `builtin://meta-agent/suggest_improvements`
**Timeout**: 15 seconds

**Purpose**: AI-powered suggestions for agent improvement based on usage patterns.

**Input Schema**:
```json
{
  "type": "object",
  "properties": {
    "config": {"type": "object"},
    "performance_data": {"type": "object"}
  },
  "required": ["config"]
}
```

**Output Schema**:
```json
{
  "type": "object",
  "properties": {
    "suggestions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "type": {"type": "string"},
          "message": {"type": "string"},
          "confidence": {"type": "number"},
          "impact": {"type": "string", "enum": ["low", "medium", "high"]}
        }
      }
    }
  }
}
```

**Example**:
```json
// Input:
{
  "config": {
    "name": "postgres-opt",
    "patterns": ["query_optimization"],
    "guardrails": {"max_turns": 10}
  },
  "performance_data": {
    "avg_turns": 8.5,
    "avg_tool_executions": 15,
    "error_rate": 0.05
  }
}

// Output:
{
  "suggestions": [
    {
      "type": "pattern_addition",
      "message": "Add 'index_suggestions' pattern (commonly used after query_optimization)",
      "confidence": 0.85,
      "impact": "medium"
    },
    {
      "type": "guardrail_adjustment",
      "message": "Increase max_turns to 15 (current avg 8.5 approaching limit)",
      "confidence": 0.72,
      "impact": "low"
    },
    {
      "type": "tool_optimization",
      "message": "Enable tool result caching (15 tool executions per conversation)",
      "confidence": 0.90,
      "impact": "high"
    }
  ]
}
```

**Suggestion types**:
- `pattern_addition` - Recommend additional patterns
- `pattern_removal` - Remove unused patterns
- `guardrail_adjustment` - Adjust limits based on usage
- `tool_optimization` - Optimize tool configuration
- `performance_tuning` - Improve response time

**Confidence scoring**:
- 0.9-1.0: High confidence (data-driven)
- 0.7-0.89: Medium confidence (heuristic-based)
- 0.5-0.69: Low confidence (speculative)

**Thread safety**: Safe for concurrent use


## Tool Registration

### Agent Configuration

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: agent-factory

spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.7

  tools:
    # Analysis Tools
    - name: classify_domain
      implementation: builtin://meta-agent/classify_domain
      timeout_seconds: 10

    - name: extract_capabilities
      implementation: builtin://meta-agent/extract_capabilities
      timeout_seconds: 10

    - name: identify_data_sources
      implementation: builtin://meta-agent/identify_data_sources
      timeout_seconds: 10

    - name: infer_workflow_needs
      implementation: builtin://meta-agent/infer_workflow_needs
      timeout_seconds: 5

    # Generation Tools
    - name: select_template
      implementation: builtin://meta-agent/select_template
      timeout_seconds: 5

    - name: select_patterns
      implementation: builtin://meta-agent/select_patterns
      timeout_seconds: 10

    - name: compose_workflow
      implementation: builtin://meta-agent/compose_workflow
      timeout_seconds: 10

    - name: generate_yaml
      implementation: builtin://meta-agent/generate_yaml
      timeout_seconds: 15

    # Validation Tools
    - name: validate_config
      implementation: builtin://meta-agent/validate_config
      timeout_seconds: 10

    - name: check_pattern_dependencies
      implementation: builtin://meta-agent/check_pattern_dependencies
      timeout_seconds: 5

    # Testing Tools
    - name: test_backend_connection
      implementation: builtin://meta-agent/test_backend_connection
      timeout_seconds: 30

    - name: test_pattern
      implementation: builtin://meta-agent/test_pattern
      timeout_seconds: 30

    - name: test_end_to_end
      implementation: builtin://meta-agent/test_end_to_end
      timeout_seconds: 120

    # Spawning Tools
    - name: spawn_ephemeral_agent
      implementation: builtin://ephemeral/spawn
      timeout_seconds: 30

    - name: attach_session
      implementation: builtin://ephemeral/attach
      timeout_seconds: 5

    - name: monitor_spawned_agent
      implementation: builtin://ephemeral/monitor
      timeout_seconds: 5

    # Learning Tools
    - name: record_success
      implementation: builtin://meta-agent/record_success
      timeout_seconds: 5

    - name: suggest_improvements
      implementation: builtin://meta-agent/suggest_improvements
      timeout_seconds: 15
```


## Error Codes

### ErrInvalidDomain

**Code**: `invalid_domain`
**Cause**: Domain classification returned unknown or unsupported domain type

**Example**:
```
Error: invalid_domain: domain "unknown_db_type" is not supported
```

**Resolution**:
1. Check supported domains: sql, rest, graphql, grpc, file, document, mcp, hybrid
2. Provide more specific requirements text
3. Use `hybrid` domain for multi-type agents


### ErrTemplateNotFound

**Code**: `template_not_found`
**Cause**: No suitable template found for domain/capability combination

**Example**:
```
Error: template_not_found: no template found for domain "graphql" with capabilities ["real_time_subscriptions"]
```

**Resolution**:
1. Check available templates with select_template
2. Use generic template and customize manually
3. Create custom template for specialized use cases


### ErrPatternMissing

**Code**: `pattern_missing`
**Cause**: Referenced pattern doesn't exist in library

**Example**:
```
Error: pattern_missing: pattern "advanced_query_optimization" not found in library
```

**Resolution**:
1. List available patterns with select_patterns
2. Check pattern name spelling
3. Load pattern library from correct path


### ErrValidationFailed

**Code**: `validation_failed`
**Cause**: Generated configuration failed validation checks

**Example**:
```
Error: validation_failed: configuration has 3 errors:
- ROLE_PROMPTING_DETECTED in system_prompt
- MISSING_REQUIRED_FIELD: backend.host
- INVALID_VALUE: guardrails.max_turns must be > 0
```

**Resolution**:
1. Fix each validation error listed
2. Use validate_config with strict=false for warnings-only
3. Check CLAUDE.md for validation rules


### ErrConnectionFailed

**Code**: `connection_failed`
**Cause**: test_backend_connection failed to connect to backend

**Example**:
```
Error: connection_failed: failed to connect to postgres://localhost:5432/test_db: connection refused
```

**Resolution**:
1. Verify backend service is running
2. Check host/port configuration
3. Verify network connectivity
4. Check credentials and permissions


### ErrSpawnFailed

**Code**: `spawn_failed`
**Cause**: spawn_ephemeral_agent failed to create agent instance

**Example**:
```
Error: spawn_failed: failed to spawn agent: invalid configuration: missing llm.provider
```

**Resolution**:
1. Validate agent configuration with validate_config first
2. Check server capacity (memory, CPU)
3. Verify all required fields present in config


### ErrAgentNotFound

**Code**: `agent_not_found`
**Cause**: Referenced agent_id doesn't exist (attach_session, monitor_spawned_agent)

**Example**:
```
Error: agent_not_found: agent "agent-invalid-id" not found
```

**Resolution**:
1. Verify agent_id spelling
2. Check if agent has expired (TTL reached)
3. Use spawn_ephemeral_agent to create new agent


## Examples

### Example 1: Complete Agent Generation Flow

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/metaagent/tools"
)

func main() {
    ctx := context.Background()

    // Step 1: Classify domain
    classifyInput := map[string]interface{}{
        "requirements": "I need an agent that analyzes PostgreSQL slow queries and suggests indexes",
    }

    classifyResult, err := tools.ExecuteTool(ctx, "classify_domain", classifyInput)
    if err != nil {
        log.Fatalf("classify_domain failed: %v", err)
    }

    domain := classifyResult["domain"].(string)
    fmt.Printf("Classified domain: %s (%.2f confidence)\n",
        domain, classifyResult["confidence"])

    // Step 2: Extract capabilities
    extractInput := map[string]interface{}{
        "requirements": "analyze slow queries, suggest indexes, generate migration scripts",
        "domain":       domain,
    }

    capabilities, err := tools.ExecuteTool(ctx, "extract_capabilities", extractInput)
    if err != nil {
        log.Fatalf("extract_capabilities failed: %v", err)
    }

    fmt.Printf("Extracted %d capabilities\n", len(capabilities["capabilities"].([]interface{})))

    // Step 3: Select patterns
    selectInput := map[string]interface{}{
        "capabilities": capabilities["capabilities"],
        "domain":       domain,
        "max_patterns": 5,
    }

    patterns, err := tools.ExecuteTool(ctx, "select_patterns", selectInput)
    if err != nil {
        log.Fatalf("select_patterns failed: %v", err)
    }

    fmt.Printf("Selected %d patterns\n", len(patterns["patterns"].([]interface{})))

    // Step 4: Generate YAML
    generateInput := map[string]interface{}{
        "spec": map[string]interface{}{
            "name":         "postgres-optimizer",
            "domain":       domain,
            "capabilities": capabilities["capabilities"],
            "patterns":     patterns["patterns"],
        },
        "output_format": "multi_file",
    }

    files, err := tools.ExecuteTool(ctx, "generate_yaml", generateInput)
    if err != nil {
        log.Fatalf("generate_yaml failed: %v", err)
    }

    // Step 5: Validate configuration
    for _, file := range files["files"].([]interface{}) {
        fileMap := file.(map[string]interface{})
        fmt.Printf("Generated: %s\n", fileMap["path"])

        // Validate each file
        validateInput := map[string]interface{}{
            "config": fileMap["content"],
            "strict": true,
        }

        validation, err := tools.ExecuteTool(ctx, "validate_config", validateInput)
        if err != nil {
            log.Printf("Validation failed for %s: %v", fileMap["path"], err)
            continue
        }

        if validation["valid"].(bool) {
            fmt.Printf("✓ %s validated (score: %.0f)\n",
                fileMap["path"], validation["score"])
        } else {
            fmt.Printf("✗ %s has errors\n", fileMap["path"])
            for _, err := range validation["errors"].([]interface{}) {
                errMap := err.(map[string]interface{})
                fmt.Printf("  - %s: %s\n", errMap["code"], errMap["message"])
            }
        }
    }
}

// Output:
// Classified domain: sql (0.95 confidence)
// Extracted 3 capabilities
// Selected 5 patterns
// Generated: agents/postgres-optimizer.yaml
// ✓ agents/postgres-optimizer.yaml validated (score: 95)
// Generated: backends/postgres-prod.yaml
// ✓ backends/postgres-prod.yaml validated (score: 98)
```


### Example 2: Test and Spawn Agent

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/teradata-labs/loom/pkg/metaagent/tools"
)

func main() {
    ctx := context.Background()

    // Load agent configuration
    config := loadAgentConfig("postgres-optimizer.yaml")

    // Step 1: Test backend connection
    testInput := map[string]interface{}{
        "backend_config": config["backend"],
    }

    connResult, err := tools.ExecuteTool(ctx, "test_backend_connection", testInput)
    if err != nil {
        log.Fatalf("Connection test failed: %v", err)
    }

    if !connResult["success"].(bool) {
        log.Fatalf("Backend connection failed: %s", connResult["message"])
    }

    fmt.Printf("✓ Backend connection OK (%.1fms latency)\n",
        connResult["latency_ms"])

    // Step 2: Run end-to-end tests
    e2eInput := map[string]interface{}{
        "config": config,
        "test_scenarios": []map[string]interface{}{
            {
                "name":          "List tables",
                "input":         "Show me all tables",
                "expected_tool": "list_tables",
            },
            {
                "name":          "Analyze query",
                "input":         "Analyze this query: SELECT * FROM large_table",
                "expected_tool": "explain_analyze",
            },
        },
    }

    e2eResult, err := tools.ExecuteTool(ctx, "test_end_to_end", e2eInput)
    if err != nil {
        log.Fatalf("E2E test failed: %v", err)
    }

    fmt.Printf("✓ Tests passed: %d/%d\n",
        e2eResult["scenarios_passed"],
        e2eResult["scenarios_passed"].(int)+e2eResult["scenarios_failed"].(int))

    // Step 3: Spawn ephemeral agent
    spawnInput := map[string]interface{}{
        "config":      config,
        "auto_attach": true,
        "ttl_seconds": 1800,
    }

    spawnResult, err := tools.ExecuteTool(ctx, "spawn_ephemeral_agent", spawnInput)
    if err != nil {
        log.Fatalf("Spawn failed: %v", err)
    }

    agentID := spawnResult["agent_id"].(string)
    sessionID := spawnResult["session_id"].(string)

    fmt.Printf("✓ Agent spawned: %s\n", agentID)
    fmt.Printf("  Session: %s\n", sessionID)
    fmt.Printf("  Endpoint: %s\n", spawnResult["endpoint"])
    fmt.Printf("  Expires: %s\n", spawnResult["expires_at"])

    // Step 4: Monitor agent
    monitorInput := map[string]interface{}{
        "agent_id": agentID,
    }

    monitorResult, err := tools.ExecuteTool(ctx, "monitor_spawned_agent", monitorInput)
    if err != nil {
        log.Fatalf("Monitor failed: %v", err)
    }

    fmt.Printf("✓ Agent status: %s\n", monitorResult["status"])
    fmt.Printf("  Memory: %dMB\n", monitorResult["resource_usage"].(map[string]interface{})["memory_mb"])
    fmt.Printf("  Messages: %d\n", monitorResult["metrics"].(map[string]interface{})["message_count"])
}

func loadAgentConfig(path string) map[string]interface{} {
    // Load configuration from file
    return map[string]interface{}{
        "name":    "postgres-optimizer",
        "backend": map[string]interface{}{"type": "postgres", "host": "localhost", "port": 5432},
        "llm":     map[string]interface{}{"provider": "anthropic", "model": "claude-sonnet-4-5-20250929"},
    }
}

// Output:
// ✓ Backend connection OK (45.3ms latency)
// ✓ Tests passed: 2/2
// ✓ Agent spawned: agent-postgres-opt-abc123
//   Session: session-xyz789
//   Endpoint: looms://agents/agent-postgres-opt-abc123
//   Expires: 2025-12-11T11:00:00Z
// ✓ Agent status: ready
//   Memory: 128MB
//   Messages: 0
```


## Testing

### Test Functions

Meta-agent tools package includes 32 test functions covering:

- Domain classification accuracy
- Capability extraction completeness
- Template selection logic
- YAML generation validation
- Configuration validation rules
- Backend connection testing
- Pattern testing
- Ephemeral agent spawning
- Error handling

**Example test**:
```go
func TestClassifyDomain(t *testing.T) {
    input := map[string]interface{}{
        "requirements": "I need a PostgreSQL performance agent",
    }

    result, err := tools.ExecuteTool(context.Background(), "classify_domain", input)
    require.NoError(t, err)

    assert.Equal(t, "sql", result["domain"])
    assert.Equal(t, "postgres", result["specific_type"])
    assert.Greater(t, result["confidence"].(float64), 0.8)
}
```

**Run tests**:
```bash
# All tests
go test ./pkg/metaagent/tools -v

# With race detector
go test ./pkg/metaagent/tools -race -v

# Specific test
go test ./pkg/metaagent/tools -run TestClassifyDomain -v
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML configuration
- [Pattern Reference](./patterns.md) - Pattern library system
- [CLI Reference](./cli.md) - Meta-agent CLI commands
- [Backend Reference](./backend.md) - Backend types and configuration
