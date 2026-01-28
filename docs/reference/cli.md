
# Loom CLI Reference

Complete command reference for `loom` (client) and `looms` (server).

**Version**: v1.0.0-beta.2


## Table of Contents

### Server Commands (`looms`)
- [looms serve](#looms-serve) - Start multi-agent server
- [looms config](#looms-config) - Manage server configuration
- [looms agent](#looms-agent) - Manage agent lifecycle
- [looms judge evaluate](#looms-judge-evaluate) - Evaluate agent responses
- [looms judge stream](#looms-judge-stream) - Stream judge evaluation
- [looms teleprompter compile](#looms-teleprompter-compile) - Optimize prompts
- [looms learning stats](#looms-learning-stats) - View pattern statistics
- [looms learning export](#looms-learning-export) - Export learning data
- [looms learning sync](#looms-learning-sync) - Sync with external systems
- [looms pattern list](#looms-pattern-list) - List patterns
- [looms pattern validate](#looms-pattern-validate) - Validate pattern YAML
- [looms pattern reload](#looms-pattern-reload) - Hot reload patterns
- [looms workflow run](#looms-workflow-run) - Execute workflows
- [looms workflow validate](#looms-workflow-validate) - Validate workflow YAML

### Client Commands (`loom`)
- [loom chat](#loom-chat) - Interactive chat session
- [loom thread](#loom-thread) - Manage conversation threads
- [loom config set](#loom-config-set) - Set configuration values
- [loom config get](#loom-config-get) - Get configuration values
- [loom config set-key](#loom-config-set-key) - Store secrets in keyring
- [loom session list](#loom-session-list) - List sessions
- [loom session resume](#loom-session-resume) - Resume previous session
- [loom session export](#loom-session-export) - Export session to Hawk
- [loom mcp list](#loom-mcp-list) - List MCP servers
- [loom mcp test](#loom-mcp-test) - Test MCP server connection

### Reference
- [Environment Variables](#environment-variables) - Environment configuration
- [Configuration Files](#configuration-files) - YAML configuration examples
- [Exit Codes](#exit-codes) - Exit code meanings
- [Error Codes](#error-codes) - Complete error reference
- [Common Workflows](#common-workflows) - Standard patterns
- [Troubleshooting](#troubleshooting) - Common issues


## Quick Reference

### Server Commands Summary

| Command | Purpose | Key Flags |
|---------|---------|-----------|
| `looms serve` | Start multi-agent server | `--port`, `--http-port`, `--hot-reload`, `--agents` |
| `looms config` | Manage server config | `set`, `get`, `list`, `reset` |
| `looms agent` | Manage agents | `list`, `start`, `stop`, `reload`, `status` |
| `looms judge evaluate` | Evaluate responses | `--agent`, `--judges`, `--aggregation` |
| `looms judge stream` | Stream evaluation | `--agent`, `--judge`, `--prompt` |
| `looms teleprompter compile` | Optimize prompts | `--optimizer`, `--training-data`, `--iterations` |
| `looms learning stats` | View pattern stats | `--domain`, `--window`, `--sort` |
| `looms learning export` | Export learning data | `--domain`, `--format`, `--output` |
| `looms learning sync` | Sync learning data | `--direction`, `--endpoint` |
| `looms pattern list` | List patterns | `--domain`, `--category`, `--backend` |
| `looms pattern validate` | Validate pattern | `<file>`, `--strict` |
| `looms pattern reload` | Hot reload patterns | `--pattern`, `--domain` |
| `looms workflow run` | Execute workflow | `<file>`, `--input`, `--stream` |
| `looms workflow validate` | Validate workflow | `<file>`, `--strict` |

### Client Commands Summary

| Command | Purpose | Key Flags |
|---------|---------|-----------|
| `loom chat` | Interactive chat | `--agent`, `--session`, `--stream` |
| `loom thread` | Manage threads | `create`, `resume`, `list`, `delete` |
| `loom config set` | Set config value | `<key> <value>` |
| `loom config get` | Get config value | `<key>` |
| `loom config set-key` | Store secret | `<key-name>` (interactive) |
| `loom session list` | List sessions | `--agent`, `--limit`, `--format` |
| `loom session resume` | Resume session | `<session-id>` |
| `loom session export` | Export to Hawk | `<session-id>`, `--format` |
| `loom mcp list` | List MCP servers | `--format` |
| `loom mcp test` | Test MCP server | `<server-name>` |

### Common Flag Patterns

**Output Formats:**
- `--format table` (default, human-readable)
- `--format json` (machine-readable, programmatic)
- `--format yaml` (configuration export)
- `--format csv` (data export)

**Filtering:**
- `--domain <string>` (filter by domain: analytics, ml, etc.)
- `--agent <string>` (filter by agent ID)
- `--window <duration>` (time window: 1h, 24h, 7d, 30d, all)

**Validation:**
- `--strict` (fail on warnings, not just errors)


## Server Commands (`looms`)

### looms serve

Start the multi-agent server with gRPC and HTTP gateways.

**Usage:**
```bash
looms serve [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--config` | string | `$LOOM_DATA_DIR/server.yaml` | Path to server config file |
| `--port` | int | `50051` | gRPC port |
| `--http-port` | int | `8080` | HTTP gateway port |
| `--agents` | stringArray | `[]` | Agent config files to load (repeatable) |
| `--hot-reload` | bool | `false` | Enable pattern hot reload |
| `--tls-cert` | string | `""` | Path to TLS certificate |
| `--tls-key` | string | `""` | Path to TLS private key |

**Examples:**

Start server with default settings:
```bash
looms serve
```

Start with custom port and agents:
```bash
looms serve \
  --port 9090 \
  --http-port 8888 \
  --agents $LOOM_DATA_DIR/agents/sql-expert.yaml \
  --agents $LOOM_DATA_DIR/agents/data-analyst.yaml
```

Enable hot reload for development:
```bash
looms serve --hot-reload
```

Start with TLS:
```bash
looms serve \
  --tls-cert /path/to/cert.pem \
  --tls-key /path/to/key.pem
```

**Expected Output:**
```
🚀 Loom server starting...
✅ Loaded 2 agents: sql-expert, data-analyst
✅ MCP servers initialized: vantage, github
🎯 gRPC server listening on :50051
🌐 HTTP gateway listening on :8080
✅ Server ready
```

**When to Use:**
- Starting the server for the first time
- Running multiple agents simultaneously
- Deploying in production environments
- Development with hot reload enabled

**Configuration File Example:**
```yaml
# $LOOM_DATA_DIR/server.yaml
server:
  grpc_port: 50051
  http_port: 8080
  hot_reload: false

agents_dir: $LOOM_DATA_DIR/agents
patterns_dir: $LOOM_DATA_DIR/patterns

observability:
  enabled: true
  hawk_endpoint: http://localhost:9090
```

**Errors:**
- Exit code 1: Port already in use
- Exit code 3: Invalid configuration file
- Exit code 4: Failed to load agents
- Exit code 4: Failed to initialize MCP servers

**See Also:**
- [looms config](#looms-config) - Configure server settings
- [looms agent](#looms-agent) - Manage running agents


### looms config

Manage server-wide configuration settings.

**Usage:**
```bash
looms config <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `set` | Set configuration value |
| `get` | Get configuration value |
| `list` | List all configuration |
| `reset` | Reset to defaults |

**Examples:**

Set MCP server configuration:
```bash
looms config set mcp.servers.vantage.command ~/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
```

Set observability endpoint:
```bash
looms config set observability.hawk_endpoint http://localhost:9090
looms config set observability.enabled true
```

List all configuration:
```bash
looms config list
```

Get specific value:
```bash
looms config get mcp.servers.vantage.command
```

**When to Use:**
- Initial server setup
- Configuring MCP servers without editing YAML
- Updating observability settings
- Managing environment-specific configuration

**Configuration Hierarchy:**
1. Command-line flags (highest priority)
2. `$LOOM_DATA_DIR/server.yaml`
3. `/etc/loom/server.yaml`
4. Default values (lowest priority)

**Errors:**
- Exit code 2: Invalid key format
- Exit code 3: Invalid value for key type
- Exit code 3: Required field cannot be unset

**See Also:**
- [Configuration Files](#configuration-files) - YAML structure


### looms agent

Manage agent lifecycle (start, stop, reload).

**Usage:**
```bash
looms agent <subcommand> [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List running agents |
| `start` | Start an agent |
| `stop` | Stop an agent |
| `reload` | Reload agent configuration |
| `status` | Get agent status |

**Examples:**

List all running agents:
```bash
looms agent list
```

Output:
```
NAME           STATUS    UPTIME     REQUESTS
sql-expert     running   2h 15m     1,234
data-analyst   running   2h 15m     456
code-reviewer  stopped   -          -
```

Start a specific agent:
```bash
looms agent start code-reviewer
```

Reload agent configuration without restart:
```bash
looms agent reload sql-expert
```

Get detailed agent status:
```bash
looms agent status sql-expert
```

Output:
```
Agent: sql-expert
Status: running
Uptime: 2h 15m 30s
Requests: 1,234
Avg Response Time: 2.3s
Memory: 45 MB
MCP servers: vantage, github
Last Error: none
```

**When to Use:**
- Checking agent health
- Starting agents on-demand
- Reloading configuration after changes
- Debugging agent issues

**Errors:**
- Exit code 7: Agent not found
- Exit code 3: Agent configuration invalid
- Exit code 4: Agent failed to start

**See Also:**
- [Agent Configuration Reference](./agent-configuration.md) - Agent YAML spec


### looms judge evaluate

Evaluate agent responses using one or more judges.

**Usage:**
```bash
looms judge evaluate [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | required | Agent to evaluate |
| `--judges` | stringArray | required | Judge IDs to use (repeatable) |
| `--prompt` | string | required | Input prompt |
| `--response` | string | required | Agent response to evaluate |
| `--aggregation` | string | `weighted-average` | Aggregation strategy |
| `--export-hawk` | bool | `true` | Export results to Hawk |

**Aggregation Strategies:**

| Strategy | Description | Use Case |
|----------|-------------|----------|
| `weighted-average` | Combine scores using judge weights | Balanced evaluation with priority judges |
| `all-must-pass` | All judges must approve (AND logic) | Strict quality gates (security, compliance) |
| `any-must-pass` | At least one judge must approve (OR logic) | Exploratory or lenient evaluation |
| `majority` | Majority of judges must approve | Democratic consensus |

**Examples:**

Evaluate with single judge:
```bash
looms judge evaluate \
  --agent sql-expert \
  --judges quality-judge \
  --prompt "Generate a sales report query" \
  --response "SELECT * FROM sales WHERE date > '2024-01-01'"
```

Output:
```
Judge: quality-judge
Score: 7.5/10
Verdict: PASS

Feedback:
✅ Query syntax is correct
✅ Date filter properly formatted
⚠️  SELECT * not recommended (specify columns)
⚠️  Missing ORDER BY clause
```

Evaluate with multiple judges and weighted aggregation:
```bash
looms judge evaluate \
  --agent sql-expert \
  --judges quality-judge,safety-judge,performance-judge \
  --aggregation weighted-average \
  --prompt "Delete old customer records" \
  --response "DELETE FROM customers WHERE last_login < '2020-01-01'"
```

Output:
```
Judge Results:
  quality-judge:      8.0/10 (weight: 0.4) = 3.2
  safety-judge:       4.0/10 (weight: 0.4) = 1.6
  performance-judge:  7.0/10 (weight: 0.2) = 1.4

Final Score: 6.2/10
Verdict: CONDITIONAL

Safety Issues:
❌ No WHERE clause validation
❌ No row limit specified
⚠️  Recommend adding LIMIT clause
```

All-must-pass aggregation (strict):
```bash
looms judge evaluate \
  --agent code-reviewer \
  --judges security-judge,style-judge,test-coverage-judge \
  --aggregation all-must-pass \
  --prompt "Review this authentication function" \
  --response "def login(user, pass): ..."
```

**When to Use:**
- Validating agent outputs before production use
- A/B testing different agent configurations
- Automated quality assurance in CI/CD
- Collecting evaluation metrics for training data

**Errors:**
- Exit code 2: Missing required flags
- Exit code 7: Judge not found
- Exit code 7: Agent not found
- Exit code 4: Failed to connect to Hawk (when `--export-hawk=true`)

**See Also:**
- [looms judge stream](#looms-judge-stream) - Streaming variant


### looms judge stream

Stream judge evaluation progress for long-running assessments.

**Usage:**
```bash
looms judge stream [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | required | Agent to evaluate |
| `--judge` | string | required | Judge ID to use |
| `--prompt` | string | required | Input prompt |
| `--response` | string | required | Agent response to evaluate |

**Example:**

Stream evaluation progress:
```bash
looms judge stream \
  --agent sql-expert \
  --judge quality-judge \
  --prompt "Analyze quarterly sales trends" \
  --response "SELECT region, quarter, SUM(revenue) FROM sales..."
```

Output (streaming):
```
[setup] 0% - Initializing judge
[setup] 10% - Loading evaluation criteria
[analysis] 25% - Analyzing query structure
[analysis] 50% - Checking for performance issues
[analysis] 75% - Validating business logic
[scoring] 90% - Calculating final score
[complete] 100% - Evaluation complete

Final Score: 8.5/10
Verdict: PASS
```

**When to Use:**
- Long-running judge evaluations (>5 seconds)
- Real-time feedback during evaluation
- Debugging judge logic
- User-facing evaluation with progress indicators

**Benefits:**
- Immediate feedback without waiting for completion
- Early termination if critical issues detected
- Better UX for complex evaluations
- Easier debugging of slow judges

**Errors:**
- Exit code 2: Missing required flags
- Exit code 7: Judge not found
- Exit code 7: Agent not found
- Exit code 4: Stream connection lost

**See Also:**
- [looms judge evaluate](#looms-judge-evaluate) - Single-shot evaluation


### looms teleprompter compile

Optimize prompts using DSPy-compatible compilation with training data.

**Usage:**
```bash
looms teleprompter compile [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--optimizer` | string | required | Optimizer: `mipro`, `copro`, `textgrad`, `bootstrap-fewshot` |
| `--training-data` | string | required | Path to training data JSONL file |
| `--validation-data` | string | `""` | Path to validation data JSONL file |
| `--metric` | string | `accuracy` | Evaluation metric: `accuracy`, `f1`, `precision`, `recall` |
| `--iterations` | int | `10` | Number of optimization iterations |
| `--output` | string | stdout | Output file for optimized prompt |
| `--base-prompt` | string | required | Starting prompt to optimize |

**Optimizer Comparison:**

| Optimizer | Best For | Training Data Required | Speed |
|-----------|----------|------------------------|-------|
| `mipro` | General optimization | 100-1000 examples | Slow (minutes-hours) |
| `copro` | Constraint satisfaction (safety, compliance) | 50-500 examples | Medium |
| `textgrad` | Fine-grained optimization | 200-1000 examples | Slow |
| `bootstrap-fewshot` | Few-shot example generation | 20-100 examples | Fast (seconds) |

**Examples:**

Optimize with MIPRO (multi-prompt instruction proposal):
```bash
looms teleprompter compile \
  --optimizer mipro \
  --base-prompt "Analyze this SQL query for performance issues" \
  --training-data ./data/sql-analysis-train.jsonl \
  --validation-data ./data/sql-analysis-val.jsonl \
  --metric accuracy \
  --iterations 20 \
  --output ./prompts/sql-analysis-optimized.txt
```

Output:
```
🔄 Starting MIPRO optimization...
📊 Training examples: 500
📊 Validation examples: 100

Iteration 1/20: accuracy=0.65 (baseline)
Iteration 2/20: accuracy=0.68 (+0.03)
Iteration 3/20: accuracy=0.71 (+0.03)
...
Iteration 20/20: accuracy=0.84 (+0.13)

✅ Optimization complete!
📈 Improvement: +29% (0.65 → 0.84)
💾 Saved to: ./prompts/sql-analysis-optimized.txt
```

Bootstrap few-shot examples:
```bash
looms teleprompter compile \
  --optimizer bootstrap-fewshot \
  --base-prompt "Classify customer sentiment" \
  --training-data ./data/sentiment-train.jsonl \
  --iterations 5
```

ConstraintGrad (CoPro) with safety constraints:
```bash
looms teleprompter compile \
  --optimizer copro \
  --base-prompt "Generate SQL query from natural language" \
  --training-data ./data/text2sql-train.jsonl \
  --metric accuracy \
  --iterations 15
```

**Training Data Format (JSONL):**
```jsonl
{"input": "Find customers in California", "output": "SELECT * FROM customers WHERE state = 'CA'", "label": "correct"}
{"input": "Count total orders", "output": "SELECT COUNT(*) FROM orders", "label": "correct"}
{"input": "Show revenue by region", "output": "SELECT region, SUM(revenue) FROM sales GROUP BY region", "label": "correct"}
```

**When to Use:**
- Initial prompt engineering with systematic optimization
- Improving agent accuracy on specific tasks
- Creating few-shot examples automatically
- A/B testing prompt variations with data-driven approach

**Errors:**
- Exit code 2: Missing required flags
- Exit code 6: Invalid training data format
- Exit code 6: Training data file not found
- Exit code 1: Optimization failed to converge

**See Also:**
- [Judge Integration Guide](../guides/judge-dspy-integration.md) - DSPy details


### looms learning stats

View pattern effectiveness statistics and learning metrics.

**Usage:**
```bash
looms learning stats [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain (e.g., analytics, ml, data-quality) |
| `--agent` | string | `""` | Filter by agent ID |
| `--pattern` | string | `""` | Filter by pattern name |
| `--window` | string | `24h` | Time window: `1h`, `24h`, `7d`, `30d`, `all` |
| `--format` | string | `table` | Output format: `table`, `json`, `csv` |
| `--sort` | string | `usage` | Sort by: `usage`, `success_rate`, `avg_tokens`, `avg_time` |

**Examples:**

View all pattern statistics (last 24h):
```bash
looms learning stats
```

Output:
```
PATTERN                    USAGE  SUCCESS  FAIL  SUCCESS_RATE  AVG_TOKENS  AVG_TIME
revenue_aggregation        1,234  1,180    54    95.6%         1,245       2.3s
join_optimization          456    402      54    88.2%         2,134       3.1s
funnel_analysis            234    198      36    84.6%         3,456       4.2s
missing_index_analysis     123    115      8     93.5%         1,876       2.8s
```

Filter by domain and sort by success rate:
```bash
looms learning stats \
  --domain analytics \
  --window 7d \
  --sort success_rate
```

View specific pattern details with JSON output:
```bash
looms learning stats \
  --pattern revenue_aggregation \
  --window 30d \
  --format json
```

Output:
```json
{
  "pattern": "revenue_aggregation",
  "domain": "analytics",
  "window": "30d",
  "total_usage": 5432,
  "success_count": 5187,
  "failure_count": 245,
  "success_rate": 0.955,
  "avg_tokens": 1245,
  "avg_execution_time_ms": 2300,
  "agents_using": ["sql-expert", "data-analyst"],
  "variants": {
    "control": {"usage": 2716, "success_rate": 0.948},
    "treatment-a": {"usage": 2716, "success_rate": 0.962}
  }
}
```

Export to CSV for analysis:
```bash
looms learning stats --window 30d --format csv > pattern-stats.csv
```

**When to Use:**
- Identifying underperforming patterns
- Monitoring pattern adoption across agents
- Tracking pattern effectiveness over time
- Preparing data for A/B test analysis

**Errors:**
- Exit code 2: Invalid window format
- Exit code 2: Invalid sort field
- Exit code 7: Pattern not found
- Exit code 7: Domain not found

**See Also:**
- [looms learning export](#looms-learning-export) - Export raw data


### looms learning export

Export learning data for external analysis or synchronization.

**Usage:**
```bash
looms learning export [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |
| `--patterns` | stringArray | `[]` | Filter by pattern names (repeatable) |
| `--window` | string | `all` | Time window: `1h`, `24h`, `7d`, `30d`, `all` |
| `--format` | string | `jsonl` | Output format: `json`, `jsonl`, `csv` |
| `--output` | string | stdout | Output file |

**Examples:**

Export all learning data:
```bash
looms learning export --output learning-data.jsonl
```

Export specific domain for last 7 days:
```bash
looms learning export \
  --domain analytics \
  --window 7d \
  --format json \
  --output analytics-7d.json
```

Export specific patterns as CSV:
```bash
looms learning export \
  --patterns revenue_aggregation,join_optimization \
  --window 30d \
  --format csv \
  --output patterns-30d.csv
```

**Output Format (JSONL):**
```jsonl
{"pattern":"revenue_aggregation","domain":"analytics","agent":"sql-expert","timestamp":"2025-01-23T10:15:30Z","success":true,"tokens":1245,"time_ms":2300}
{"pattern":"join_optimization","domain":"analytics","agent":"sql-expert","timestamp":"2025-01-23T10:18:45Z","success":true,"tokens":2134,"time_ms":3100}
```

**When to Use:**
- Backing up learning data
- Analyzing patterns in external tools (pandas, Excel)
- Syncing data to centralized learning repository
- Creating training datasets for teleprompter optimization

**Errors:**
- Exit code 2: Invalid format
- Exit code 7: No data found matching filters
- Exit code 1: Failed to write output file

**See Also:**
- [looms learning stats](#looms-learning-stats) - View statistics


### looms learning sync

Synchronize learning data with external systems.

**Usage:**
```bash
looms learning sync [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--direction` | string | required | Sync direction: `push`, `pull`, `bidirectional` |
| `--domain` | string | `""` | Filter by domain |
| `--agent` | string | `""` | Filter by agent ID |
| `--patterns` | stringArray | `[]` | Filter by pattern names (repeatable) |
| `--endpoint` | string | required | External system endpoint (for push/pull) |
| `--window` | string | `24h` | Time window for sync: `1h`, `24h`, `7d`, `30d`, `all` |

**Examples:**

Push local learning data to external system:
```bash
looms learning sync \
  --direction push \
  --domain analytics \
  --endpoint https://learning-hub.example.com/api/v1/sync \
  --window 24h
```

Output:
```
🔄 Syncing learning data...
📤 Direction: push
🎯 Domain: analytics
⏱️  Window: 24h

Analyzing local data...
  Found 1,234 learning records
  Patterns: revenue_aggregation, join_optimization, funnel_analysis

Pushing to https://learning-hub.example.com/api/v1/sync...
  ✅ Uploaded 1,234 records
  ✅ Sync complete

Summary:
  Records pushed: 1,234
  Patterns synced: 3
  Duration: 2.3s
```

Pull external improvements:
```bash
looms learning sync \
  --direction pull \
  --domain analytics \
  --endpoint https://learning-hub.example.com/api/v1/sync
```

Bidirectional sync:
```bash
looms learning sync \
  --direction bidirectional \
  --endpoint https://learning-hub.example.com/api/v1/sync
```

**When to Use:**
- Centralized learning across multiple Loom deployments
- Sharing pattern improvements across teams
- Importing curated patterns from external sources
- Backing up learning data to remote storage

**Note:** Pull sync currently has infrastructure but requires external system integration. Push sync is fully functional.

**Errors:**
- Exit code 2: Missing required flags
- Exit code 4: Failed to connect to endpoint
- Exit code 4: Authentication failed
- Exit code 1: Sync operation failed

**See Also:**
- [looms learning export](#looms-learning-export) - Export for manual sync


### looms pattern list

List all available patterns with filtering options.

**Usage:**
```bash
looms pattern list [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--domain` | string | `""` | Filter by domain (e.g., analytics, ml) |
| `--category` | string | `""` | Filter by category (e.g., performance, security) |
| `--backend` | string | `""` | Filter by backend type (e.g., postgres, teradata) |
| `--format` | string | `table` | Output format: `table`, `json`, `yaml` |

**Examples:**

List all patterns:
```bash
looms pattern list
```

Output:
```
NAME                        DOMAIN      CATEGORY      BACKEND    DESCRIPTION
revenue_aggregation         analytics   reporting     sql        Aggregate revenue metrics by dimension
join_optimization           analytics   performance   sql        Optimize JOIN operations
funnel_analysis             analytics   reporting     sql        Multi-step funnel conversion analysis
linear_regression           ml          supervised    sql        Linear regression model training
data_profiling              quality     validation    sql        Profile data distributions and quality
```

Filter by domain:
```bash
looms pattern list --domain analytics
```

Output as JSON for programmatic use:
```bash
looms pattern list --domain analytics --format json
```

Filter by multiple criteria:
```bash
looms pattern list \
  --domain analytics \
  --category performance \
  --backend postgres
```

**When to Use:**
- Discovering available patterns
- Finding patterns for specific tasks
- Programmatic pattern discovery
- Documentation generation

**Errors:**
- Exit code 7: No patterns found matching filters
- Exit code 2: Invalid format specified

**See Also:**
- [Pattern Reference](./patterns.md) - Pattern specification


### looms pattern validate

Validate pattern YAML files against the schema.

**Usage:**
```bash
looms pattern validate <pattern-file> [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--strict` | bool | `false` | Enable strict validation (fail on warnings) |

**Examples:**

Validate a pattern file:
```bash
looms pattern validate patterns/analytics/revenue-agg.yaml
```

Output (success):
```
✅ Pattern is valid: revenue_aggregation

Validation Results:
  ✅ Schema structure valid
  ✅ Required fields present
  ✅ Template syntax valid
  ✅ Example parameters valid
  ✅ Backend compatibility confirmed
```

Output (errors):
```
❌ Pattern validation failed: join_optimization

Errors:
  ❌ Line 15: Missing required field 'description'
  ❌ Line 23: Invalid template syntax: {{.table_name}
  ❌ Line 45: Example missing required parameter 'left_table'

Warnings:
  ⚠️  Line 30: Template uses deprecated variable {{.backend}}
```

Validate with strict mode:
```bash
looms pattern validate patterns/analytics/revenue-agg.yaml --strict
```

Validate all patterns in directory:
```bash
for f in patterns/**/*.yaml; do
  looms pattern validate "$f"
done
```

**When to Use:**
- Before committing new patterns
- CI/CD pipeline validation
- Debugging pattern syntax errors
- Ensuring pattern quality standards

**Errors:**
- Exit code 2: File not found
- Exit code 6: Validation failed
- Exit code 6: Strict mode: warnings present

**See Also:**
- [Pattern Reference](./patterns.md) - Schema specification


### looms pattern reload

Hot reload patterns without server restart.

**Usage:**
```bash
looms pattern reload [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--pattern` | string | `""` | Reload specific pattern (optional) |
| `--domain` | string | `""` | Reload all patterns in domain (optional) |

**Examples:**

Reload all patterns:
```bash
looms pattern reload
```

Output:
```
🔄 Reloading patterns...
  ✅ Loaded 59 patterns
  ⏱️  Reload time: 89ms
  ✅ All agents updated
```

Reload specific pattern:
```bash
looms pattern reload --pattern revenue_aggregation
```

Reload all patterns in domain:
```bash
looms pattern reload --domain analytics
```

**When to Use:**
- After editing pattern files during development
- Testing pattern changes without server restart
- Hot-fixing pattern issues in production
- Updating patterns from version control

**Requirements:**
- Server must be started with `--hot-reload` flag
- Pattern files must pass validation
- No syntax errors in YAML

**Performance:**
- Typical reload time: 89-143ms
- Zero downtime during reload
- Existing sessions unaffected

**Errors:**
- Exit code 1: Server not started with `--hot-reload`
- Exit code 7: Pattern not found
- Exit code 6: Pattern validation failed
- Exit code 4: Server connection failed

**See Also:**
- [looms serve](#looms-serve) - Start with `--hot-reload`


### looms workflow run

Execute multi-stage workflows with orchestration.

**Usage:**
```bash
looms workflow run <workflow-file> [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--input` | string | `{}` | Input data (JSON string or `@file`) |
| `--session` | string | `""` | Session ID for conversation context |
| `--stream` | bool | `false` | Stream execution progress |
| `--export-hawk` | bool | `true` | Export workflow trace to Hawk |

**Examples:**

Run workflow with inline input:
```bash
looms workflow run workflows/npath-analysis.yaml \
  --input '{"user_query": "Analyze customer journeys", "database": "demo"}'
```

Run with input from file:
```bash
looms workflow run workflows/data-pipeline.yaml --input @data/input.json
```

Stream workflow progress:
```bash
looms workflow run workflows/multi-stage-report.yaml \
  --input '{"report_type": "quarterly", "year": 2024}' \
  --stream
```

Output (streaming):
```
🚀 Starting workflow: multi-stage-report

[Stage 1/5] database_discovery (0%)
  ✅ Discovered 3 databases: demo, analytics, staging

[Stage 2/5] table_selection (20%)
  ✅ Selected table: analytics.sales_facts

[Stage 3/5] data_profiling (40%)
  ✅ Profiled 1.2M rows, 15 columns

[Stage 4/5] aggregation (60%)
  ✅ Generated quarterly aggregations

[Stage 5/5] report_generation (80%)
  ✅ Generated report with 4 sections

✅ Workflow complete (12.3s)
📊 Results exported to Hawk
```

**When to Use:**
- Complex multi-stage analyses
- Data pipelines with dependencies between stages
- Automated reporting workflows
- Orchestrated agent interactions

**Workflow File Example:**
```yaml
# workflows/multi-stage-report.yaml
name: quarterly-report
type: pipeline

stages:
  - id: discovery
    agent: sql-expert
    pattern: database_discovery

  - id: selection
    agent: sql-expert
    pattern: table_selection
    depends_on: [discovery]

  - id: aggregation
    agent: sql-expert
    pattern: aggregation
    depends_on: [selection]

  - id: report
    agent: data-analyst
    pattern: report_generation
    depends_on: [aggregation]
```

**Errors:**
- Exit code 2: Workflow file not found
- Exit code 6: Invalid workflow YAML
- Exit code 7: Referenced agent not found
- Exit code 7: Referenced pattern not found
- Exit code 1: Stage execution failed

**See Also:**
- [looms workflow validate](#looms-workflow-validate) - Validate workflow


### looms workflow validate

Validate workflow YAML files.

**Usage:**
```bash
looms workflow validate <workflow-file> [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--strict` | bool | `false` | Enable strict validation |

**Examples:**

Validate workflow:
```bash
looms workflow validate workflows/npath-analysis.yaml
```

Output (success):
```
✅ Workflow is valid: npath-analysis

Validation Results:
  ✅ Schema structure valid
  ✅ All stages have valid agents
  ✅ All patterns exist
  ✅ Dependencies are acyclic
  ✅ Input/output contracts match
```

Output (errors):
```
❌ Workflow validation failed: data-pipeline

Errors:
  ❌ Stage 'aggregation' references non-existent agent 'analytics-bot'
  ❌ Stage 'report' has circular dependency: report -> analysis -> report
  ❌ Stage 'selection' pattern 'table_picker' not found

Warnings:
  ⚠️  Stage 'discovery' has no dependencies (expected for first stage)
```

**When to Use:**
- Before running new workflows
- CI/CD pipeline validation
- Debugging workflow execution errors
- Ensuring workflow quality standards

**Errors:**
- Exit code 2: File not found
- Exit code 6: Validation failed
- Exit code 6: Strict mode: warnings present
- Exit code 7: Referenced agent/pattern not found

**See Also:**
- [Workflow Orchestration Guide](../guides/workflow-orchestration.md) - Workflow patterns


## Client Commands (`loom`)

### loom chat

Start an interactive chat session with an agent.

**Usage:**
```bash
loom chat [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | from config | Agent to chat with |
| `--server` | string | `localhost:50051` | Server address |
| `--session` | string | `""` | Resume existing session ID |
| `--pattern` | string | `""` | Use specific pattern |
| `--stream` | bool | `true` | Stream responses |

**Examples:**

Start interactive chat:
```bash
loom chat
```

Chat with specific agent:
```bash
loom chat --agent sql-expert
```

Resume previous session:
```bash
loom chat --session sess_abc123
```

Chat using specific pattern:
```bash
loom chat --agent sql-expert --pattern aggregation
```

**Interactive Session:**
```
🤖 Connected to sql-expert

You: Analyze sales by region for Q4 2024

Agent: I'll help you analyze Q4 2024 sales by region. Let me query the database.

[Executing: get_schema sales]
[Executing: execute_query SELECT region, SUM...]

Based on the analysis:

1. **West Region**: $2.4M (35% of total)
2. **East Region**: $2.1M (30% of total)
3. **South Region**: $1.5M (22% of total)
4. **Central Region**: $0.9M (13% of total)

Total Q4 Revenue: $6.9M

Would you like me to break this down further by month?

You: Yes, show monthly breakdown

Agent: Here's the monthly breakdown for Q4 2024...


Commands:
  /exit, /quit   - Exit chat
  /session       - Show current session ID
  /clear         - Clear conversation history
  /export        - Export session to Hawk
  /help          - Show help
```

**When to Use:**
- Interactive exploration and analysis
- Testing agent behavior
- Ad-hoc queries and investigations
- Learning agent capabilities

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 7: Agent not found
- Exit code 7: Session not found
- Exit code 5: Authentication failed

**See Also:**
- [loom thread](#loom-thread) - Persistent threads


### loom thread

Create or resume conversation threads with persistent context.

**Usage:**
```bash
loom thread [subcommand] [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `create` | Start new thread |
| `resume` | Continue existing thread |
| `list` | List your threads |
| `delete` | Delete a thread |

**Examples:**

Create new thread:
```bash
loom thread create --agent sql-expert --title "Q4 Sales Analysis"
```

Output:
```
✅ Created thread: thread_xyz789
📝 Title: Q4 Sales Analysis
🤖 Agent: sql-expert
💬 Messages: 0
```

Resume thread:
```bash
loom thread resume thread_xyz789
```

List threads:
```bash
loom thread list
```

Output:
```
THREAD_ID       TITLE                  AGENT        MESSAGES  UPDATED
thread_xyz789   Q4 Sales Analysis      sql-expert   12        2 hours ago
thread_abc123   Customer Segmentation  data-analyst 8         1 day ago
thread_def456   Performance Tuning     sql-expert   5         3 days ago
```

Delete thread:
```bash
loom thread delete thread_xyz789
```

**When to Use:**
- Long-running investigations with persistent context
- Organizing conversations by topic
- Collaborating on analysis with teammates
- Maintaining conversation history

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 7: Thread not found
- Exit code 7: Agent not found

**See Also:**
- [loom chat](#loom-chat) - Simple chat sessions


### loom config set

Set configuration values.

**Usage:**
```bash
loom config set <key> <value> [flags]
```

**Examples:**

Set default server:
```bash
loom config set server.address localhost:50051
```

Configure default agent:
```bash
loom config set agent.default sql-expert
```

Enable streaming responses:
```bash
loom config set client.stream true
```

Set MCP server configuration:
```bash
loom config set mcp.servers.vantage.command ~/bin/vantage-mcp
loom config set mcp.servers.vantage.args.0 --database demo
```

Set MCP environment variables:
```bash
loom config set mcp.servers.vantage.env.TD_USER myuser
```

**When to Use:**
- Initial client setup
- Switching between servers/environments
- Configuring MCP servers
- Setting default preferences

**Configuration File:** `$LOOM_DATA_DIR/config.yaml`

**Errors:**
- Exit code 2: Invalid key format
- Exit code 3: Invalid value for key type

**See Also:**
- [loom config get](#loom-config-get) - View configuration


### loom config get

Get configuration values.

**Usage:**
```bash
loom config get <key> [flags]
```

**Examples:**

Get server address:
```bash
loom config get server.address
```

Output:
```
localhost:50051
```

Get all configuration:
```bash
loom config get
```

Output:
```yaml
server:
  address: localhost:50051
agent:
  default: sql-expert
client:
  stream: true
mcp:
  servers:
    vantage:
      command: ~/bin/vantage-mcp
      env:
        TD_USER: myuser
```

**When to Use:**
- Checking current configuration
- Debugging connection issues
- Verifying MCP server setup

**Errors:**
- Exit code 7: Key not found

**See Also:**
- [loom config set](#loom-config-set) - Set configuration


### loom config set-key

Store secrets securely in system keyring.

**Usage:**
```bash
loom config set-key <key-name> [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--value` | string | `""` | Provide value directly (not recommended) |

**Examples:**

Store password interactively (secure - password hidden):
```bash
loom config set-key td_password
```

Prompt:
```
Enter value for 'td_password': [hidden input]
✅ Stored in system keyring
```

Use stored key in MCP configuration:
```bash
loom config set mcp.servers.vantage.env.TD_PASSWORD "{{keyring:td_password}}"
```

Store API key:
```bash
loom config set-key anthropic_api_key
```

**Supported Keyrings:**
- **macOS**: Keychain
- **Linux**: Secret Service (gnome-keyring, kwallet)
- **Windows**: Windows Credential Manager

**When to Use:**
- Storing database passwords
- Storing API keys
- Storing any sensitive credentials
- Avoiding plaintext secrets in configuration files

**Security:**
- Never use `--value` flag for secrets (visible in shell history)
- Always use interactive prompt for sensitive data
- Keys are encrypted by OS keyring service

**Errors:**
- Exit code 1: Keyring service unavailable
- Exit code 1: Failed to store key

**See Also:**
- [loom config set](#loom-config-set) - Reference keys in config


### loom session list

List conversation sessions.

**Usage:**
```bash
loom session list [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--agent` | string | `""` | Filter by agent |
| `--limit` | int | `20` | Limit results |
| `--format` | string | `table` | Output format: `table`, `json` |

**Examples:**

List recent sessions:
```bash
loom session list
```

Output:
```
SESSION_ID       AGENT         MESSAGES  TOKENS   CREATED
sess_abc123      sql-expert    15        12,345   2 hours ago
sess_def456      data-analyst  8         5,678    1 day ago
sess_ghi789      sql-expert    23        18,900   3 days ago
```

Filter by agent:
```bash
loom session list --agent sql-expert
```

Get more sessions:
```bash
loom session list --limit 50
```

JSON output for programmatic use:
```bash
loom session list --format json
```

**When to Use:**
- Finding previous conversations
- Reviewing session history
- Monitoring session usage
- Preparing for session export

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 2: Invalid limit value

**See Also:**
- [loom session resume](#loom-session-resume) - Resume session


### loom session resume

Resume a previous conversation session.

**Usage:**
```bash
loom session resume <session-id> [flags]
```

**Examples:**

Resume session:
```bash
loom session resume sess_abc123
```

Output:
```
📂 Resuming session: sess_abc123
🤖 Agent: sql-expert
💬 Messages: 15
🔄 Loading conversation history...

[Previous conversation displayed]

You: [continue conversation]
```

**When to Use:**
- Continuing previous analysis
- Reviewing past conversations
- Building on prior context
- Following up on recommendations

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 7: Session not found

**See Also:**
- [loom session list](#loom-session-list) - Find session IDs


### loom session export

Export session to Hawk for analysis.

**Usage:**
```bash
loom session export <session-id> [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--hawk-endpoint` | string | from config | Hawk server endpoint |
| `--format` | string | `hawk` | Export format: `hawk`, `json` |
| `--output` | string | send to Hawk | Output file |

**Examples:**

Export to Hawk:
```bash
loom session export sess_abc123
```

Output:
```
📤 Exporting session to Hawk...
  Session: sess_abc123
  Messages: 15
  Tokens: 12,345
  Endpoint: http://localhost:9090

✅ Export complete
🔗 View in Hawk: http://localhost:9090/sessions/sess_abc123
```

Export to JSON file:
```bash
loom session export sess_abc123 \
  --format json \
  --output session-abc123.json
```

**When to Use:**
- Analyzing conversation patterns
- Debugging agent behavior
- Tracking token usage and costs
- Sharing sessions with team for review

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 7: Session not found
- Exit code 4: Cannot connect to Hawk

**See Also:**
- [Observability Guide](../guides/integration/observability.md) - Hawk integration


### loom mcp list

List configured MCP servers and their status.

**Usage:**
```bash
loom mcp list [flags]
```

**Flags:**

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `table` | Output format: `table`, `json` |

**Examples:**

List MCP servers:
```bash
loom mcp list
```

Output:
```
NAME         STATUS    TOOLS  COMMAND
vantage      running   45     ~/bin/vantage-mcp
github       running   12     npx @modelcontextprotocol/server-github
filesystem   stopped   8      npx @modelcontextprotocol/server-filesystem
```

JSON output:
```bash
loom mcp list --format json
```

**When to Use:**
- Checking MCP server health
- Discovering available tools
- Debugging MCP configuration
- Verifying server startup

**Errors:**
- Exit code 4: Cannot connect to server

**See Also:**
- [loom mcp test](#loom-mcp-test) - Test MCP server


### loom mcp test

Test MCP server connection and list available tools.

**Usage:**
```bash
loom mcp test <server-name> [flags]
```

**Examples:**

Test MCP server:
```bash
loom mcp test vantage
```

Output:
```
🔌 Testing MCP server: vantage

Connection:
  ✅ Server started successfully
  ✅ Handshake complete
  ✅ Protocol version: 1.0

Available Tools:
  ✅ execute_query - Execute Teradata SQL query
  ✅ get_schema - Get table schema information
  ✅ list_tables - List available tables
  ✅ list_databases - List accessible databases
  ... (41 more tools)

Summary:
  Status: healthy
  Tools: 45
  Response time: 234ms
```

Test with errors:
```bash
loom mcp test filesystem
```

Output:
```
🔌 Testing MCP server: filesystem

Connection:
  ❌ Failed to start server
  Error: command not found: npx

Troubleshooting:
  1. Install Node.js and npm
  2. Install server: npm install -g @modelcontextprotocol/server-filesystem
  3. Verify command: npx @modelcontextprotocol/server-filesystem --help
```

**When to Use:**
- After configuring new MCP server
- Debugging MCP connection issues
- Verifying tool availability
- Testing after MCP server updates

**Errors:**
- Exit code 4: Cannot connect to server
- Exit code 7: MCP server not configured
- Exit code 4: MCP server failed to start

**See Also:**
- [MCP Integration Guide](../guides/integration/mcp.md) - MCP setup


## Environment Variables

### Server Environment

```bash
# Server configuration
LOOM_SERVER_PORT=50051
LOOM_HTTP_PORT=8080
LOOM_CONFIG_PATH=$LOOM_DATA_DIR/server.yaml

# Observability
LOOM_HAWK_ENDPOINT=http://localhost:9090
LOOM_HAWK_ENABLED=true

# LLM providers
ANTHROPIC_API_KEY=sk-ant-...
AWS_REGION=us-east-1
OLLAMA_BASE_URL=http://localhost:11434

# Database credentials (via keyring recommended)
TD_USER=myuser
TD_PASSWORD={{keyring:td_password}}
```

### Client Environment

```bash
# Client configuration
LOOM_SERVER=localhost:50051
LOOM_AGENT=sql-expert
LOOM_CONFIG_PATH=$LOOM_DATA_DIR/config.yaml

# MCP servers
MCP_VANTAGE_CMD=~/bin/vantage-mcp
MCP_GITHUB_TOKEN={{keyring:github_token}}
```


## Configuration Files

### Server Configuration (`$LOOM_DATA_DIR/server.yaml`)

```yaml
server:
  grpc_port: 50051
  http_port: 8080
  hot_reload: false
  tls:
    enabled: false
    cert_file: /path/to/cert.pem
    key_file: /path/to/key.pem

agents_dir: $LOOM_DATA_DIR/agents
patterns_dir: $LOOM_DATA_DIR/patterns

observability:
  enabled: true
  hawk_endpoint: http://localhost:9090
  trace_sampling: 1.0

llm:
  default_provider: anthropic
  providers:
    anthropic:
      api_key: ${ANTHROPIC_API_KEY}
      model: claude-sonnet-4-5-20250929
    bedrock:
      region: us-east-1
      model_id: anthropic.claude-3-5-sonnet-20241022-v2:0

mcp:
  servers:
    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: ${TD_USER}
        TD_PASSWORD: "{{keyring:td_password}}"
```

### Client Configuration (`$LOOM_DATA_DIR/config.yaml`)

```yaml
server:
  address: localhost:50051
  tls: false

agent:
  default: sql-expert

client:
  stream: true
  timeout: 300s

mcp:
  servers:
    vantage:
      command: ~/bin/vantage-mcp
      env:
        TD_USER: myuser
        TD_PASSWORD: "{{keyring:td_password}}"
    github:
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-github"
      env:
        GITHUB_TOKEN: "{{keyring:github_token}}"
```

### Agent Configuration (`$LOOM_DATA_DIR/agents/sql-expert.yaml`)

```yaml
name: sql-expert
description: SQL query generation and optimization expert

llm:
  provider: anthropic
  model: claude-sonnet-4-5-20250929
  temperature: 0.0

backend:
  type: sql
  connection_string: "{{keyring:db_connection}}"

tools:
  - name: execute_query
    enabled: true
  - name: get_schema
    enabled: true
  - name: list_tables
    enabled: true

patterns:
  - aggregation
  - join_optimization
  - funnel_analysis

memory:
  type: sqlite
  path: $LOOM_DATA_DIR/memory/sql-expert.db
  max_history: 50

limits:
  max_turns: 25
  max_tool_executions: 50
  timeout_seconds: 300

observability:
  enabled: true
```


## Exit Codes

All commands use standard exit codes:

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid arguments |
| 3 | Configuration error |
| 4 | Connection error |
| 5 | Authentication error |
| 6 | Validation error |
| 7 | Not found (session, pattern, agent) |

**Example:**
```bash
looms judge evaluate --agent sql-expert --judges quality-judge --prompt "test"
if [ $? -eq 0 ]; then
  echo "Evaluation passed"
else
  echo "Evaluation failed with code: $?"
fi
```


## Error Codes

### ERR_PORT_IN_USE

**Exit Code**: 1
**Command**: `looms serve`

**Cause**: The specified gRPC or HTTP port is already in use by another process.

**Example**:
```
Error: listen tcp :50051: bind: address already in use
```

**Resolution**:
1. Check what's using the port:
   ```bash
   lsof -i :50051
   ```
2. Kill the process or use a different port:
   ```bash
   looms serve --port 9090
   ```


### ERR_CONFIG_NOT_FOUND

**Exit Code**: 3
**Command**: All commands using config files

**Cause**: Configuration file specified does not exist.

**Example**:
```
Error: config file not found: $LOOM_DATA_DIR/server.yaml
```

**Resolution**:
1. Create the default config file:
   ```bash
   mkdir -p $LOOM_DATA_DIR
   looms config set server.grpc_port 50051
   ```
2. Or specify a different config:
   ```bash
   looms serve --config /path/to/config.yaml
   ```


### ERR_INVALID_CONFIG

**Exit Code**: 3
**Command**: All commands using config files

**Cause**: Configuration file has syntax errors or invalid values.

**Example**:
```
Error: invalid configuration: field 'agent.timeout' must be positive duration, got: -5s
```

**Resolution**:
1. Validate YAML syntax:
   ```bash
   yamllint $LOOM_DATA_DIR/server.yaml
   ```
2. Check field types match schema requirements
3. Verify all required fields are present

**See Also**: [Agent Configuration Reference](./agent-configuration.md)


### ERR_AGENT_NOT_FOUND

**Exit Code**: 7
**Command**: `looms agent`, `loom chat`, `looms judge evaluate`

**Cause**: Referenced agent does not exist or is not loaded.

**Example**:
```
Error: agent not found: sql-expert
```

**Resolution**:
1. List available agents:
   ```bash
   looms agent list
   ```
2. Check agent configuration file exists:
   ```bash
   ls -la $LOOM_DATA_DIR/agents/sql-expert.yaml
   ```
3. Load agent at server start:
   ```bash
   looms serve --agents $LOOM_DATA_DIR/agents/sql-expert.yaml
   ```


### ERR_PATTERN_NOT_FOUND

**Exit Code**: 7
**Command**: `looms pattern validate`, `looms pattern reload`, `looms workflow run`

**Cause**: Referenced pattern does not exist in the pattern library.

**Example**:
```
Error: pattern not found: revenue_aggregation
```

**Resolution**:
1. List available patterns:
   ```bash
   looms pattern list
   ```
2. Check pattern file exists:
   ```bash
   ls -la $LOOM_DATA_DIR/patterns/analytics/revenue-aggregation.yaml
   ```
3. Validate pattern YAML:
   ```bash
   looms pattern validate $LOOM_DATA_DIR/patterns/analytics/revenue-aggregation.yaml
   ```
4. Reload patterns:
   ```bash
   looms pattern reload
   ```


### ERR_SESSION_NOT_FOUND

**Exit Code**: 7
**Command**: `loom session resume`, `loom session export`

**Cause**: Referenced session ID does not exist or has been deleted.

**Example**:
```
Error: session not found: sess_abc123
```

**Resolution**:
1. List available sessions:
   ```bash
   loom session list
   ```
2. Verify session ID format: `sess_[a-z0-9]+`


### ERR_VALIDATION_FAILED

**Exit Code**: 6
**Command**: `looms pattern validate`, `looms workflow validate`

**Cause**: Pattern or workflow YAML file has validation errors.

**Example**:
```
Error: validation failed: line 15: missing required field 'description'
```

**Resolution**:
1. Review validation error messages
2. Fix YAML syntax errors
3. Add missing required fields
4. Check template variable syntax: `{{.variable}}`
5. Ensure backend compatibility

**See Also**: [Pattern Reference](./patterns.md)


### ERR_CONNECTION_FAILED

**Exit Code**: 4
**Command**: All client commands, `looms serve` (for backends)

**Cause**: Cannot connect to server, backend, or external service.

**Example (client):**
```
Error: failed to connect to server: dial tcp 127.0.0.1:50051: connect: connection refused
```

**Example (server):**
```
Error: failed to connect to database: postgres://localhost:5432: connection refused
```

**Resolution (client):**
1. Verify server is running:
   ```bash
   lsof -i :50051
   ```
2. Check server address:
   ```bash
   loom config get server.address
   ```
3. Test network connectivity:
   ```bash
   telnet localhost 50051
   ```

**Resolution (server):**
1. Verify backend service is running (database, MCP server, etc.)
2. Check firewall/network settings
3. Verify credentials in configuration
4. Check service logs for errors


### ERR_AUTH_FAILED

**Exit Code**: 5
**Command**: All commands connecting to external services

**Cause**: Authentication failed due to invalid credentials or expired tokens.

**Example**:
```
Error: authentication failed: invalid credentials for vantage MCP server
```

**Resolution**:
1. Verify credentials in keyring:
   ```bash
   loom config set-key td_password
   ```
2. Check keyring reference in config:
   ```bash
   loom config get mcp.servers.vantage.env.TD_PASSWORD
   ```
3. Test MCP server connection:
   ```bash
   loom mcp test vantage
   ```


### ERR_MCP_SERVER_FAILED

**Exit Code**: 4
**Command**: `looms serve`, `loom mcp test`

**Cause**: MCP server failed to start or crashed during initialization.

**Example**:
```
Error: failed to start MCP server 'vantage': command not found: ~/bin/vantage-mcp
```

**Resolution**:
1. Verify MCP server command exists:
   ```bash
   ls -la ~/bin/vantage-mcp
   ```
2. Test command manually:
   ```bash
   ~/bin/vantage-mcp --help
   ```
3. Check MCP server configuration:
   ```bash
   loom config get mcp.servers.vantage
   ```
4. Review MCP server logs (if available)

**See Also**: [MCP Integration Guide](../guides/integration/mcp.md)


### ERR_HOT_RELOAD_NOT_ENABLED

**Exit Code**: 1
**Command**: `looms pattern reload`

**Cause**: Server was not started with `--hot-reload` flag.

**Example**:
```
Error: hot reload not enabled on server
```

**Resolution**:
1. Restart server with hot reload enabled:
   ```bash
   looms serve --hot-reload
   ```

**Note**: Hot reload is disabled by default in production for stability.


### ERR_INVALID_TRAINING_DATA

**Exit Code**: 6
**Command**: `looms teleprompter compile`

**Cause**: Training data JSONL file has invalid format or missing required fields.

**Example**:
```
Error: invalid training data at line 42: missing 'output' field
```

**Resolution**:
1. Verify JSONL format (one JSON object per line)
2. Check required fields: `input`, `output`, `label`
3. Validate JSON syntax:
   ```bash
   jq empty training-data.jsonl
   ```

**Expected format**:
```jsonl
{"input": "query", "output": "result", "label": "correct"}
```


### ERR_JUDGE_NOT_FOUND

**Exit Code**: 7
**Command**: `looms judge evaluate`, `looms judge stream`

**Cause**: Referenced judge ID does not exist or is not configured.

**Example**:
```
Error: judge not found: quality-judge
```

**Resolution**:
1. Verify judge configuration exists
2. Check judge is registered with agent
3. Review available judges in agent configuration

**See Also**: [Judge Integration Guide](../guides/judge-dspy-integration.md)


### ERR_MAX_TURNS_EXCEEDED

**Exit Code**: 1
**Command**: `loom chat`, `looms workflow run`

**Cause**: Conversation exceeded configured `max_turns` limit.

**Example**:
```
Error: max turns exceeded: limit 25, current 26
```

**Resolution**:
1. Increase max_turns in agent configuration:
   ```yaml
   limits:
     max_turns: 50
   ```
2. Or start new session:
   ```bash
   loom chat --agent sql-expert
   ```


### ERR_TIMEOUT

**Exit Code**: 1
**Command**: All commands with operations taking longer than timeout

**Cause**: Operation exceeded configured timeout duration.

**Example**:
```
Error: operation timed out after 300s
```

**Resolution**:
1. Increase timeout in configuration:
   ```yaml
   client:
     timeout: 600s
   ```
2. Or increase agent-level timeout:
   ```yaml
   limits:
     timeout_seconds: 600
   ```


### ERR_HAWK_UNAVAILABLE

**Exit Code**: 4
**Command**: `loom session export`, `looms serve` (when observability enabled)

**Cause**: Cannot connect to Hawk observability platform.

**Example**:
```
Error: failed to export to Hawk: dial tcp 127.0.0.1:9090: connect: connection refused
```

**Resolution**:
1. Verify Hawk is running:
   ```bash
   curl http://localhost:9090/health
   ```
2. Check Hawk endpoint configuration:
   ```bash
   looms config get observability.hawk_endpoint
   ```
3. Disable observability if Hawk not available:
   ```bash
   looms config set observability.enabled false
   ```

**See Also**: [Observability Guide](../guides/integration/observability.md)


### ERR_WORKFLOW_CIRCULAR_DEPENDENCY

**Exit Code**: 6
**Command**: `looms workflow validate`, `looms workflow run`

**Cause**: Workflow has circular dependency between stages.

**Example**:
```
Error: circular dependency detected: stage-a -> stage-b -> stage-a
```

**Resolution**:
1. Review workflow `depends_on` relationships
2. Ensure dependency graph is acyclic (DAG)
3. Visualize workflow to identify cycles

**Example fix**:
```yaml
# BAD (circular)
stages:
  - id: stage-a
    depends_on: [stage-b]
  - id: stage-b
    depends_on: [stage-a]

# GOOD (acyclic)
stages:
  - id: stage-a
  - id: stage-b
    depends_on: [stage-a]
```


## Common Workflows

### Initial Setup

```bash
# 1. Start server
looms serve --hot-reload

# 2. In another terminal, configure client
loom config set server.address localhost:50051
loom config set agent.default sql-expert

# 3. Store credentials securely
loom config set-key td_password
loom config set-key anthropic_api_key

# 4. Configure MCP servers
loom config set mcp.servers.vantage.command ~/bin/vantage-mcp
loom config set mcp.servers.vantage.env.TD_PASSWORD "{{keyring:td_password}}"

# 5. Test connection
loom mcp test vantage

# 6. Start chatting
loom chat
```


### Pattern Development

```bash
# 1. Create pattern file
vim patterns/analytics/custom-pattern.yaml

# 2. Validate pattern
looms pattern validate patterns/analytics/custom-pattern.yaml

# 3. Reload patterns (server must be running with --hot-reload)
looms pattern reload --pattern custom-pattern

# 4. Test pattern
loom chat --pattern custom-pattern

# 5. Monitor effectiveness
looms learning stats --pattern custom-pattern --window 1h
```


### A/B Testing Patterns

```bash
# 1. Create pattern variants
# patterns/analytics/revenue-agg-control.yaml (variant: control)
# patterns/analytics/revenue-agg-treatment-a.yaml (variant: treatment-a)

# 2. Use patterns in production
# (agents automatically use variants)

# 3. View stats
looms learning stats --pattern revenue_aggregation --window 7d

# 4. Analyze A/B test results
# (trigger via interrupt channel - see learning agent guide)

# 5. Export results
looms learning export \
  --patterns revenue_aggregation \
  --window 7d \
  --format csv \
  --output ab-test-results.csv
```


### Judge-Based CI/CD

```bash
#!/bin/bash
# ci-evaluate.sh

AGENT="sql-expert"
PROMPT="Generate a quarterly sales report query"
RESPONSE=$(loom chat --agent $AGENT --message "$PROMPT" --non-interactive)

# Evaluate with multiple judges
looms judge evaluate \
  --agent $AGENT \
  --judges quality-judge,safety-judge,performance-judge \
  --aggregation all-must-pass \
  --prompt "$PROMPT" \
  --response "$RESPONSE"

if [ $? -eq 0 ]; then
  echo "✅ All judges passed - deploying"
  exit 0
else
  echo "❌ Judge evaluation failed - blocking deployment"
  exit 1
fi
```


### Prompt Optimization

```bash
# 1. Prepare training data
# Create training.jsonl with input/output pairs

# 2. Run optimization
looms teleprompter compile \
  --optimizer mipro \
  --base-prompt "Analyze SQL query performance" \
  --training-data ./data/sql-perf-train.jsonl \
  --validation-data ./data/sql-perf-val.jsonl \
  --iterations 20 \
  --output ./prompts/sql-perf-optimized.txt

# 3. Update agent configuration with optimized prompt
vim $LOOM_DATA_DIR/agents/sql-expert.yaml
# (update system_prompt with optimized content)

# 4. Reload agent
looms agent reload sql-expert

# 5. Compare before/after
looms learning stats --agent sql-expert --window 24h
```


## Troubleshooting

### Server Won't Start

**Symptom:** `looms serve` fails with "address already in use"

**Solution:**
```bash
# Check what's using the port
lsof -i :50051

# Kill the process or use different port
looms serve --port 9090
```


### MCP Server Connection Failed

**Symptom:** `Error: failed to start MCP server`

**Solution:**
```bash
# Test MCP server manually
npx @modelcontextprotocol/server-filesystem /data

# Check configuration
loom config get mcp.servers.filesystem

# Test with loom
loom mcp test filesystem
```


### Pattern Not Found

**Symptom:** `Error: pattern 'revenue_aggregation' not found`

**Solution:**
```bash
# List available patterns
looms pattern list

# Check pattern file exists
ls -la $LOOM_DATA_DIR/patterns/analytics/revenue-aggregation.yaml

# Validate pattern
looms pattern validate $LOOM_DATA_DIR/patterns/analytics/revenue-aggregation.yaml

# Reload patterns
looms pattern reload
```


### Authentication Failed

**Symptom:** `Error: authentication failed`

**Solution:**
```bash
# Check credentials in keyring
loom config set-key td_password

# Verify configuration
loom config get mcp.servers.vantage.env

# Test connection
loom mcp test vantage
```


### Session Not Persisting

**Symptom:** Sessions don't appear in `loom session list`

**Solution:**
```bash
# Check agent memory configuration
cat $LOOM_DATA_DIR/agents/sql-expert.yaml | grep -A 3 memory:

# Verify database exists
ls -la $LOOM_DATA_DIR/memory/sql-expert.db

# Check permissions
chmod 644 $LOOM_DATA_DIR/memory/sql-expert.db
```


## See Also

- [Agent Configuration Reference](./agent-configuration.md) - Agent configuration details
- [Backend Reference](./backend.md) - Backend configuration
- [Pattern System Reference](./patterns.md) - Pattern authoring guide
- [LLM Providers Reference](./llm-providers.md) - LLM configuration
- [Features Guide](../guides/features.md) - Feature overview
