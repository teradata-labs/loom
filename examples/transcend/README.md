# Customer Health Analysis Workflow

A multi-agent workflow for comprehensive Teradata VantageCloud customer health analysis, quality validation, alerting, and presentation using event-driven coordination.

## Overview

This workflow orchestrates five specialized agents to provide complete customer health analytics:

1. **Health Coordinator** - User-facing agent that gathers analysis requirements and orchestrates the workflow (event-driven, no polling)
2. **Data Analyst** - Executes Teradata SQL queries and provides data insights
3. **Quality Checker** - Validates health score data quality and identifies anomalies
4. **Alert Generator** - Detects significant health changes and generates CSM alerts
5. **Presentation Agent** - Creates executive dashboards, reports, and visualizations

## Architecture

```
┌─────────────────────────┐
│   Health Coordinator    │ ← User interaction (event-driven)
│     (Entrypoint)        │
└───────┬─────────────────┘
        │
        ├────────────┬────────────┬────────────┐
        │            │            │            │
        ▼            ▼            ▼            ▼
┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐
│   Data   │  │ Quality  │  │  Alert   │  │Presentation  │
│ Analyst  │  │ Checker  │  │Generator │  │    Agent     │
└──────────┘  └──────────┘  └──────────┘  └──────────────┘
```

**Communication Pattern**: Hub-and-spoke (coordinator is the hub)

### How Hub-and-Spoke Works

This workflow demonstrates an **emergent communication pattern** rather than an enforced workflow pattern. Unlike Loom's formal workflow patterns (`debate`, `pipeline`, `swarm`, etc.), hub-and-spoke is not implemented as runtime enforcement. Instead, it emerges naturally through:

**Loom's Tri-Modal Communication System:**
- **Message Queue**: Direct agent-to-agent messaging via `send_message`/`receive_message` tools
- **Broadcast Bus**: Topic-based pub/sub via `publish`/`subscribe` tools (not used in this workflow)
- **Shared Memory**: Shared state via `shared_memory_write`/`shared_memory_read` tools (not used in this workflow)

**Why the Coordinator Acts as Hub:**
1. **Entrypoint designation**: Users interact with the coordinator first
2. **Event-driven architecture**: Coordinator uses event-driven messaging (responses automatically injected, no polling)
3. **System prompt instructions**: Coordinator's prompt tells it to delegate to specialists via `send_message`
4. **Specialist agent prompts**: All specialists call `receive_message` once, then respond via `send_message`

**Key Architectural Detail - Event-Driven Coordinator:**
The coordinator is fully event-driven. It does NOT call `receive_message` - instead, specialist responses are automatically injected into its conversation. This eliminates polling and ensures instant notification when sub-agents complete their work.

The `communication` field in the workflow YAML is **advisory documentation only** - it communicates intent to humans but is not parsed or enforced by the runtime. This flexibility allows you to implement various communication topologies (hub-and-spoke, peer-to-peer, hierarchical) simply by configuring agent prompts and tool usage.

## Usage

### Prerequisites

1. **Teradata VantageCloud Access**:
   - Access to DataMart_CTO schema
   - customer_health_score view
   - sites_daily_* views
   - vantage_sites dimension

2. **vantage-mcp Tool**:
   - Teradata MCP server configured and accessible
   - See vantage-mcp documentation for setup

### Starting the Workflow

```bash
# Start the looms server (if not already running)
looms serve

# In another terminal, connect to the coordinator
loom --thread health-coordinator
```

### Example Interactions

#### Health Score Analysis
```
User: Show me customers with declining health scores this week

Coordinator: I'll analyze customer health changes for you. Let me coordinate with my data analyst to query the latest scores...

[Coordinator sends request to customer-health-workflow:data-analyst]
[Coordinator tells user it's gathering data]
[System automatically injects data-analyst response when ready]

Coordinator: Based on the analysis:

             📊 Health Score Changes (Past Week):
             - 23 customers with declining scores
             - 8 customers dropped >20 points (CRITICAL)
             - 15 customers dropped 10-19 points (WARNING)

             🔴 Top At-Risk Customers:
             1. Customer A: 82 → 58 (-24 points)
             2. Customer B: 75 → 51 (-24 points)
             3. Customer C: 88 → 65 (-23 points)

             [Analysis saved to ~/.loom/artifacts/health-data-{timestamp}.md]
```

#### Quality Validation
```
User: Run quality checks on today's health scores

Coordinator: I'll validate the health score data quality with my quality checker...

[Coordinator sends request to customer-health-workflow:quality-checker]
[System automatically injects quality-checker response when ready]

Coordinator: Quality Validation Results:

             Overall Quality Score: 92/100 ✅

             ✅ Score Range Validity: PASS (100% in range 0-100)
             ✅ Component Sum Consistency: PASS (99.8% within tolerance)
             ✅ Customer Coverage: PASS (all customers scored)
             ⚠️ Data Freshness: WARNING (3 customers last updated 26 hours ago)
             ✅ Anomaly Detection: PASS (no unusual patterns detected)

             [Quality report saved to ~/.loom/artifacts/quality-check-{timestamp}.md]
```

#### Alert Generation
```
User: Generate alerts for health score changes

Coordinator: I'll have the alert generator identify customers requiring CSM attention...

[Coordinator sends request to customer-health-workflow:alert-generator]
[System automatically injects alert-generator response when ready]

Coordinator: CSM Alert Summary:

             🔴 CRITICAL ALERTS (2):
             - Customer A: Score 58 (dropped 24 points)
               Action: Schedule immediate CSM call within 24 hours

             - Customer B: Score 51 (dropped 24 points)
               Action: Schedule immediate CSM call within 24 hours

             🟡 WARNING ALERTS (5):
             - Customer D: Score 68 (dropped 15 points)
               Action: Send check-in email within 3 days

             [Complete alerts saved to ~/.loom/artifacts/alerts-{timestamp}.md]
```

#### Executive Dashboard
```
User: Create an executive dashboard for this month

Coordinator: I'll have the presentation agent generate an executive dashboard...

[Coordinator sends request to customer-health-workflow:presentation-agent]
[System automatically injects presentation-agent response when ready]

Coordinator: Executive Dashboard - Customer Health (December 2025)

             📈 KEY METRICS:
             - Average Health Score: 76.3
             - Customers at Risk (<60): 47
             - Quality Score: 94/100
             - Churn Risk: 3.8%

             🟢 TOP IMPROVERS:
             1. Customer X: +32 points (48→80)
             2. Customer Y: +28 points (52→80)

             🔴 ATTENTION REQUIRED:
             1. Customer A: Score 58 (CRITICAL)
             2. Customer B: Score 51 (URGENT)

             [Dashboard saved to ~/.loom/artifacts/dashboard-{timestamp}.md]
```

## Features

### Health Coordinator Agent
- **Communication Model**: Event-driven (responses automatically injected, no polling)
- Understands user analysis requirements (queries, risk analysis, trends, reports)
- Delegates work to appropriate specialist agents via `send_message`
- Synthesizes multi-agent results into comprehensive insights
- Saves final analysis reports to `~/.loom/artifacts/customer-health-{timestamp}.md`
- **Memory**: SQLite with conversational profile (max_history: 1000)
- **Config**: max_turns: 100, max_tool_executions: 200, timeout: 900s
- **Tools**: shell_execute, tool_search, send_message (NO receive_message - fully event-driven)

### Data Analyst Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Executes Teradata SQL queries against DataMart_CTO schema
- Provides customer health scoring and root cause analysis
- Accesses: customer_health_score view, sites_daily_* views, vantage_sites dimension
- Saves data reports to `~/.loom/artifacts/health-data-{timestamp}.md`
- **Memory**: SQLite with data_intensive profile (max_history: 800)
- **Config**: max_turns: 50, max_tool_executions: 150, timeout: 600s
- **Patterns**: teradata_sql_best_practices, customer_health_query_template, customer_health_root_cause_analysis
- **Tools**: shell_execute, tool_search, send_message, receive_message, vantage-mcp (discovered)

### Quality Checker Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Validates health score data quality (5 core checks)
- Checks: Score range validity, component sum consistency, customer coverage, data freshness, anomaly detection
- Calculates overall quality score (0-100)
- Provides actionable recommendations for data issues
- Saves quality reports to `~/.loom/artifacts/quality-check-{timestamp}.md`
- **Memory**: SQLite with balanced profile (max_history: 800)
- **Config**: max_turns: 50, max_tool_executions: 150, timeout: 600s
- **Patterns**: health_score_validation, teradata_sql_best_practices
- **Tools**: shell_execute, tool_search, send_message, receive_message, vantage-mcp (discovered)

### Alert Generator Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Detects significant health score changes (4 alert types)
- Generates alerts: CRITICAL (>20 pt drop), WARNING (10-19 pt drop), SUCCESS (>20 pt improvement), CHURN RISK (<40 score)
- Provides recommended actions with timelines for CSM teams
- Includes root cause analysis and engagement context
- Saves alert reports to `~/.loom/artifacts/alerts-{timestamp}.md`
- **Memory**: SQLite with balanced profile (max_history: 1000)
- **Config**: max_turns: 50, max_tool_executions: 150, timeout: 600s
- **Patterns**: customer_health_root_cause_analysis, teradata_sql_best_practices, customer_health_query_template
- **Tools**: shell_execute, tool_search, send_message, receive_message, vantage-mcp (discovered)

### Presentation Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Creates 4 presentation formats: Executive Dashboard, CSM Weekly Report, Data Quality Dashboard, Trend Analysis Report
- Uses ASCII charts, markdown tables, and color-coded indicators
- Queries aggregated data for visualizations
- Discovers and uses presentation tools (top_n_query, group_by_query if available)
- Saves presentation artifacts to `~/.loom/artifacts/dashboard-{timestamp}.md`
- **Memory**: SQLite with balanced profile (max_history: 1000)
- **Config**: max_turns: 60, max_tool_executions: 200, timeout: 600s
- **Patterns**: weekly_csm_report, teradata_sql_best_practices, customer_health_query_template
- **Tools**: shell_execute, tool_search, send_message, receive_message, vantage-mcp + presentation tools (discovered)

## Configuration

### Memory Profiles

Each agent uses a different memory compression profile optimized for its workload:

- **Coordinator** (`conversational`): Optimized for back-and-forth conversation with user (1000 messages)
- **Data Analyst** (`data_intensive`): Handles large query results and data analysis (800 messages)
- **Quality Checker** (`balanced`): Mix of queries and validation logic (800 messages)
- **Alert Generator** (`balanced`): Mix of detection queries and alert formatting (1000 messages)
- **Presentation Agent** (`balanced`): Mix of data queries and visualization generation (1000 messages)

### Pattern Libraries

The workflow uses 4 pattern libraries located in `patterns/`:

- **sql/**: Teradata SQL best practices and query templates
  - `teradata_sql_best_practices.yml`
  - `customer_health_query_template.yml`

- **analysis/**: Customer health analysis patterns
  - `customer_health_root_cause.yml`

- **quality/**: Data quality validation patterns
  - `health_score_validation.yml`

- **reports/**: Report generation patterns
  - `weekly_csm_report.yml`

### Tool Discovery

All agents use dynamic tool discovery via `tool_search`:
- Coordinator discovers `send_message` tool
- Data Analyst discovers `vantage-mcp` tool for Teradata queries
- Quality Checker discovers `vantage-mcp` tool for validation queries
- Alert Generator discovers `vantage-mcp` tool for change detection queries
- Presentation Agent discovers `vantage-mcp` + presentation tools (top_n_query, group_by_query if available)

### Self-Correction and Observability

All agents have:
- **Self-correction**: Enabled for automatic error recovery
- **Observability**: Full tracing and metrics export to Hawk
- **Workflow tags**: All agents tagged with `workflow: customer-health`, `domain: customer-analytics`

## Output Artifacts

The workflow saves detailed artifacts to `~/.loom/artifacts/`:

- `customer-health-{timestamp}.md` - Complete analysis report (coordinator)
- `health-data-{timestamp}.md` - Data analysis and insights (data-analyst)
- `quality-check-{timestamp}.md` - Quality validation results (quality-checker)
- `alerts-{timestamp}.md` - CSM alerts and recommended actions (alert-generator)
- `dashboard-{timestamp}.md` - Executive dashboards and reports (presentation-agent)

## Troubleshooting

### Cannot connect to Teradata
- Ensure vantage-mcp tool is configured and accessible
- Verify credentials and network access to Teradata VantageCloud
- Check that DataMart_CTO schema is accessible

### Specialist agents not responding
- Check that all agents are running in the workflow
- Verify agent IDs in send_message calls: must be `customer-health-workflow:data-analyst` format
- Check looms server logs for message delivery issues

### Quality checks failing
- Review the 5 core quality checks in quality-checker agent
- Check data freshness (scores should be updated daily)
- Verify upstream data pipeline health (vantage_sites, sites_daily_* views)

### Dashboard generation slow
- Presentation agent may need to query large aggregations
- Consider adding data views for common dashboard queries
- Adjust timeout in workflow config if needed (default: 1800s / 30 minutes)

## Dependencies

### Required Tools (built into Loom)

All agents have access to:
- `shell_execute` - Execute shell commands
- `tool_search` - Discover available tools dynamically
- `get_error_detail` - Get detailed error information
- `search_conversation` - Search conversation history
- `recall_conversation` - Recall specific conversation segments
- `clear_recalled_context` - Clear recalled context

### Communication Tools

- **Coordinator**: `send_message` only (event-driven, no receive)
- **Specialists**: `send_message` and `receive_message`
- **Note**: This workflow does NOT use broadcast bus or shared_memory tools

### Backend Tools (must be configured)

All specialist agents discover:
- **vantage-mcp**: Teradata SQL execution tool
  - Accesses DataMart_CTO schema
  - Queries customer_health_score, sites_daily_*, vantage_sites views
  - Must be configured externally (see vantage-mcp documentation)

Presentation agent additionally discovers (if available):
- **top_n_query**: Ranks customers by score, change, or other metrics
- **group_by_query**: Aggregates data by CSM, region, product, etc.

## Development

### Testing Individual Agents

```bash
# Test coordinator (event-driven)
loom --thread health-coordinator

# Test data analyst (request-response)
loom --thread data-analyst

# Test quality checker (request-response)
loom --thread quality-checker

# Test alert generator (request-response)
loom --thread alert-generator

# Test presentation agent (request-response)
loom --thread presentation-agent
```

### Understanding Event-Driven Coordinator

The coordinator is unique - it does NOT poll for messages. When you send it requests:
1. It calls `send_message` to delegate work to specialists
2. It tells you it's working on it
3. Responses from specialists are automatically injected into its conversation
4. It sees the responses and synthesizes them for you

This eliminates polling delays and ensures instant coordination.

## Architecture Notes

### Why Event-Driven Coordinator?

Traditional hub-and-spoke patterns require the hub to poll for responses:
```
send_message → poll receive_message → timeout/retry logic
```

This workflow's coordinator is event-driven:
```
send_message → system injects responses automatically → coordinator sees responses
```

Benefits:
- **Zero polling overhead**: No wasted API calls checking for messages
- **Instant notification**: Coordinator sees responses immediately when ready
- **Simpler logic**: No timeout/retry management needed
- **Better UX**: User sees progress updates instead of waiting periods

### Specialist Agent Pattern

All specialist agents follow a simple request-response pattern:
1. Wait for notification of pending message
2. Call `receive_message` ONCE to get the request
3. Process the request (execute queries, run checks, generate alerts/reports)
4. Call `send_message` to send complete response
5. Wait for next notification

This pattern ensures specialists don't poll and waste resources.

## License

Part of the Loom agent framework examples. Internal Teradata use - TRANSCEND proprietary patterns included.
