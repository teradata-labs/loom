# Loom 5-Minute Demo: From Zero to Scheduled Workflow
## "Fresh Install → Custom Agent → Artifacts → Automated Workflows"

**Duration:** 5 minutes (compressed)
**Date:** January 30, 2026
**Version:** Loom v1.0.2
**Starting Point:** Completely fresh installation

---

## Overview

This demo showcases Loom's complete agent lifecycle:
1. **Fresh Install** - No configuration, no pre-built agents
2. **Agent Creation** - Weaver creates a Teradata SQL expert from natural language
3. **Real Work** - Agent analyzes production data with self-correction
4. **Process Capture** - Agent documents its methodology to an artifact
5. **Workflow Automation** - Weaver creates a scheduled workflow that runs every Friday

**Key Differentiators:**
- Zero YAML editing by humans
- Self-documenting agents (artifacts)
- Workflow generation from agent experience
- Observability built-in

---

## Demo Timeline

### Act 1: Launch & Setup (0:00 - 0:15)

**Action:**
```bash
./bin/loom
```

**What Happens:**
- Loom TUI launches with Teradata branding
- Guide agent greets user with navigation tips
- Sidebar shows: Guide, Weaver, Pattern Library (collapsed), MCP Servers

**Narration:**
> "This is a completely fresh Loom installation. No configuration files, no pre-built agents, no setup. Just the framework and its built-in Weaver—the agent that creates other agents."

**Screen Shows:**
```
Teradata™ Loom v1.0.2

👋 Guide
I'm here to help you find the right agent or workflow for your task.

You can also:
  • Press ctrl+e to browse all agents
  • Press ctrl+w to browse workflows
  • Select Weaver from the sidebar to create a new agent

Examples:
  • "I need help writing SQL queries"
  • "Review my code for security issues"
```

**Duration:** 15 seconds

---

### Act 2: Create Teradata SQL Expert (0:15 - 1:00)

**Action:** Switch to Weaver and request agent

**User Input:**
```
I need an agent that knows teradata sql and can give me insights about my data
```

**What Weaver Does (compressed from ~2 minutes):**

1. **Discovery** (8 shell commands + 4 tool searches)
   - Searches `examples/` directory for Teradata patterns
   - Finds `vantage-mcp.yaml` (Teradata MCP server)
   - Finds `database-query.yaml` (SQL execution tool)
   - Reads Teradata-specific pattern files

2. **Agent Generation** (1 agent_management call)
   - Creates `teradata-sql-expert.yaml`
   - System prompt: Teradata SQL expertise, MPP architecture, BTEQ scripting
   - Tools: `teradata_execute_sql`, `tool_search`, `shared_memory_read`
   - LLM: Claude Sonnet 4.5, temperature 0.7

3. **Documentation** (1 workspace write)
   - Writes user guide to scratchpad

**Weaver Response:**
```
✓ Created teradata-sql-expert agent
✓ Configured with teradata_execute_sql tool
✓ Added SQL optimization patterns
✓ Wrote user guide to scratchpad

Your Teradata SQL expert is ready! Switch to it from the sidebar.
```

**Narration:**
> "In 45 seconds, Weaver has created a fully functional Teradata SQL agent. It discovered available tools, generated appropriate prompts, configured the LLM, and even wrote documentation. No YAML editing, no configuration files—just natural language."

**Duration:** 45 seconds (0:15 - 1:00)

---

### Act 3: Real Data Analysis (1:00 - 2:30)

**Action:** Switch to `teradata-sql-expert` agent

**Sidebar Updates:**
```
- Guide
- Weaver
- ✨ teradata-sql-expert (active)
```

#### Query 1: Table Discovery (1:00 - 1:30)

**User Input:**
```
tell me about acc_ted_con_vw.dbp_featusg_agg_dly
```

**Agent Actions:**
- Tool search: Finds `teradata_execute_sql` tool
- First attempt: ❌ **Type error** (debugMode: "false" vs false)
- Self-correction: ✅ Fixes boolean type
- Executes 3 SQL queries:
  1. Column metadata (DBC.ColumnsV)
  2. Table metadata (DBC.TablesV)
  3. Sample data (TOP 5)

**Agent Response:**
```markdown
## Table: acc_ted_con_vw.dbp_featusg_agg_dly

### Overview:
This is a **VIEW** containing **daily aggregated Teradata feature usage data**.
Tracks how different Teradata features are used across customer accounts.

- **Created:** June 28, 2021
- **Last Modified:** June 7, 2022
- **Type:** View (V)

### Purpose:
Aggregates feature usage metrics by:
- Account (customer)
- Site (specific Teradata system)
- Date (daily log date)
- Feature (specific Teradata capability)

Used for **license compliance**, **feature adoption**, and **performance monitoring**.

### Key Columns (42 total):

#### Dimensional Attributes:
1. **acct_nbr** - Account number
2. **acct_name** - Account name (e.g., "AT&T Inc.")
3. **feat_id** - Feature ID
4. **feat_name** - Feature name (e.g., "Multi-Value Compression")
...

### Sample Data Insights:
Features tracked: External Stored Procedure, Multi-Value Compression,
Trigger, No Primary Index, Geospatial Index Distance Join

Customers include: AT&T Inc., NXP USA Inc., eBay
```

**Narration:**
> "Notice what just happened: The agent made a mistake on its first query—wrong parameter type. It didn't crash or ask for help. It read the error message, reasoned about the problem, and fixed it. Then it categorized 42 columns into logical groups and provided business context. All autonomous."

**Duration:** 30 seconds (1:00 - 1:30)

#### Query 2: 3D Geospatial Analysis (1:30 - 2:30)

**User Input:**
```
give me usage of the 3d geospatial feature for 2025
```

**Agent Actions:**
- Executes 5 analytical SQL queries:
  1. Feature discovery (LIKE '%Geospatial%')
  2. Account distribution analysis
  3. Temporal patterns: ❌ **SQL Error 3707** (reserved word "month")
  4. Self-correction: ✅ Uses "month_num" and ordinal positioning
  5. Performance metrics
  6. Comparative analysis

**Agent Response:**
```markdown
## 3D Geospatial Feature Usage Analysis for 2025

### Account Distribution
- **8 accounts** using the feature across **11 site instances**
- **Dominant user**: Proximus SA (2 high-usage sites)
- **Total requests**: 42,704 requests by 324 users

### Temporal Patterns
| Month | Requests |
|-------|----------|
| January | 3,142 |
| November | 11,223 | ← Q4 spike
| December | 14,557 | ← 3-4x increase

### Performance Characteristics
**Average CPU Usage per Account**:
- **Parser CPU**: Very low (0.01 - 1.71 avg)
- **AMP CPU**: Moderate to high (0.42 - 289 avg)
  - A1 Telekom showing high AMP usage (289-493) ⚠️

### Comparison with Other Geospatial Features
| Feature | Accounts | Sites | Total Requests |
|---------|----------|-------|----------------|
| **3D Geospatial** | 8 | 11 | **42,704** |
| Geospatial (Standard) | 307 | 593 | **93,745,990** |

**Key Insight**: 3D Geospatial is a **niche feature** with only 0.05%
of standard Geospatial volume, serving specialized use cases.

### Recommendations
1. **Engage Proximus SA**: Understand their use cases and success stories
2. **Q4 Pattern Investigation**: Identify why Nov-Dec saw 3-4x usage increase
3. **Adoption Opportunity**: Only 8 accounts using this feature
4. **Performance Monitoring**: A1 Telekom's high AMP CPU warrants review
```

**Narration:**
> "Again, self-correction in action. The agent hit Teradata Error 3707—a reserved word conflict. It didn't need specialized debugging tools. It read the error, understood the Teradata-specific rules, and reformulated the query. Five analytical queries, two errors, two autonomous fixes. Zero human intervention."

**Duration:** 60 seconds (1:30 - 2:30)

---

### Act 4: Process Documentation (2:30 - 3:30) **[NEW]**

**User Input:**
```
That's excellent analysis. Can you document the process you used to analyze the 3D geospatial feature? Save it as an artifact so we can reuse this methodology.
```

**Agent Actions:**
- Uses `workspace` tool to write artifact
- Action: "write"
- Scope: "artifact" (indexed, searchable)
- Filename: "geospatial-analysis-methodology.md"
- Purpose: "Reusable methodology for feature usage analysis"
- Tags: ["methodology", "geospatial", "analysis", "teradata"]

**Artifact Content:**
```markdown
# Teradata Feature Usage Analysis Methodology

## Overview
This document describes a systematic approach to analyzing Teradata feature
usage patterns, discovered through analyzing 3D Geospatial feature usage for 2025.

## Step-by-Step Process

### Step 1: Feature Discovery
**Objective**: Identify which features exist in the usage dataset

**SQL Pattern**:
```sql
SELECT DISTINCT featur_id_txt
FROM acc_ted_con_vw.dbp_featusg_agg_dly
WHERE featur_id_txt LIKE '%[FEATURE_CATEGORY]%'
AND log_dt >= DATE '[START_DATE]';
```

**Output**: List of matching feature IDs

### Step 2: Account Distribution Analysis
**Objective**: Identify which customers use the feature and how heavily

**SQL Pattern**:
```sql
SELECT
  acct_nm,
  site_instance_nm,
  COUNT(DISTINCT log_dt) as usage_days,
  CAST(SUM(CAST(impact_io_qty AS BIGINT)) AS DECIMAL(20,2)) as total_io
FROM acc_ted_con_vw.dbp_featusg_agg_dly
WHERE featur_id_txt = '[FEATURE_NAME]'
AND log_dt >= DATE '[START_DATE]'
GROUP BY 1, 2
ORDER BY total_io DESC;
```

**Output**: Customer usage ranked by I/O impact

### Step 3: Temporal Pattern Analysis
**Objective**: Identify seasonal trends, spikes, or anomalies

**SQL Pattern** (with Teradata error handling):
```sql
SELECT
  CAST(EXTRACT(MONTH FROM log_dt) AS INTEGER) as month_num,
  COUNT(*) as request_count
FROM acc_ted_con_vw.dbp_featusg_agg_dly
WHERE featur_id_txt = '[FEATURE_NAME]'
AND log_dt >= DATE '[START_DATE]'
GROUP BY CAST(EXTRACT(MONTH FROM log_dt) AS INTEGER)
ORDER BY 1;
```

**Common Error**: Using reserved word "month" as alias → Error 3707
**Solution**: Use safe alias like "month_num" and ordinal ORDER BY

### Step 4: Performance Metrics
**Objective**: Understand CPU, I/O, and resource consumption patterns

**SQL Pattern**:
```sql
SELECT
  acct_nm,
  AVG(parser_cpu_qty) as avg_parser_cpu,
  AVG(amp_cpu_qty) as avg_amp_cpu
FROM acc_ted_con_vw.dbp_featusg_agg_dly
WHERE featur_id_txt = '[FEATURE_NAME]'
AND log_dt >= DATE '[START_DATE]'
GROUP BY 1;
```

**Output**: Performance profile per customer

### Step 5: Comparative Analysis
**Objective**: Compare feature against related features

**SQL Pattern**:
```sql
SELECT
  featur_id_txt,
  COUNT(DISTINCT acct_nm) as accounts,
  COUNT(DISTINCT site_instance_nm) as sites,
  SUM(request_qty) as total_requests
FROM acc_ted_con_vw.dbp_featusg_agg_dly
WHERE featur_id_txt LIKE '%[FEATURE_CATEGORY]%'
AND log_dt >= DATE '[START_DATE]'
GROUP BY 1
ORDER BY total_requests DESC;
```

**Output**: Feature comparison table

## Analysis Framework

1. **Discovery** → Identify relevant features
2. **Distribution** → Who uses it and how much?
3. **Temporal** → When do they use it?
4. **Performance** → What's the resource impact?
5. **Comparative** → How does it compare to alternatives?

## Deliverables

- **Account Distribution**: Ranked list of customers
- **Temporal Trends**: Monthly/quarterly usage patterns
- **Performance Profile**: CPU and I/O characteristics
- **Comparative Context**: Feature positioning
- **Recommendations**: Actionable business insights

## Teradata-Specific Considerations

- Use ordinal positions in ORDER BY after GROUP BY with expressions
- Avoid reserved words as aliases (month, year, day, etc.)
- Cast aggregate functions appropriately for large numbers
- Use DISTINCT carefully with large fact tables

## Reusability

This methodology applies to any feature in `dbp_featusg_agg_dly`:
- Storage features (compression, indexing)
- Analytics features (geospatial, temporal, ML)
- Platform features (JSON, XML, stored procedures)

Replace `[FEATURE_NAME]`, `[FEATURE_CATEGORY]`, and `[START_DATE]`
placeholders in SQL patterns.
```

**Agent Response:**
```
Analysis methodology saved to artifact: geospatial-analysis-methodology.md

This artifact is:
- ✅ Indexed (searchable via FTS5)
- ✅ Reusable (SQL patterns with placeholders)
- ✅ Tagged (methodology, geospatial, analysis, teradata)
- ✅ Stored in session: $LOOM_DATA_DIR/artifacts/sessions/<session-id>/agent/

You can now use this methodology for other feature analyses.
```

**Narration:**
> "This is where Loom becomes truly powerful. The agent just documented its own methodology as a searchable artifact. Not just 'here's what I did'—but a reusable framework with SQL patterns, error handling strategies, and Teradata-specific gotchas. This knowledge is now persistent and shareable."

**Duration:** 60 seconds (2:30 - 3:30)

---

### Act 5: Workflow Automation (3:30 - 5:00) **[NEW]**

**User Input:**
```
Perfect! Now use Weaver to create a scheduled workflow that runs this analysis every Friday morning at 9 AM Eastern Time. The workflow should use this methodology artifact.
```

**Action:** Switch back to Weaver

**Weaver Actions:**

1. **Read the Artifact** (uses workspace tool)
   - Action: "read"
   - Filename: "geospatial-analysis-methodology.md"
   - Understands the methodology structure

2. **Design the Workflow** (uses agent_management tool)
   - Creates workflow YAML with scheduled execution
   - Configures pipeline with multiple stages
   - Sets cron: "0 9 * * 5" (every Friday at 9 AM)
   - Timezone: "America/New_York"
   - Passes methodology artifact path as variable

**Generated Workflow:** `friday-geospatial-report.yaml`

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: friday-geospatial-report
  description: Weekly 3D Geospatial feature usage analysis report

spec:
  type: pipeline
  pipeline:
    initial_prompt: "Generate weekly 3D Geospatial feature usage report using standardized methodology"

    stages:
      - agent_id: teradata-sql-expert
        prompt: |
          Read the analysis methodology from artifact: geospatial-analysis-methodology.md

          Apply this methodology to analyze 3D Geospatial feature usage for the
          past 7 days. Focus on:
          1. New accounts adopting the feature
          2. Week-over-week usage trends
          3. Performance anomalies
          4. Comparison with previous weeks

          Save results to artifact: weekly-geospatial-report-{date}.md

      - agent_id: teradata-sql-expert
        prompt: |
          Review the weekly report and identify:
          - Top 3 insights
          - Recommended actions for customer success team
          - Any alerts or anomalies requiring immediate attention

          Format as executive summary.

# Schedule Configuration
schedule:
  # Every Friday at 9:00 AM Eastern Time
  cron: "0 9 * * 5"

  timezone: "America/New_York"

  enabled: true

  # Don't run if previous execution is still going
  skip_if_running: true

  # Allow 30 minutes for analysis
  max_execution_seconds: 1800

  variables:
    methodology_artifact: "geospatial-analysis-methodology.md"
    report_recipient: "customer-success@company.com"
    alert_threshold_pct: "20"
```

**Weaver Response:**
```
✓ Created workflow: friday-geospatial-report
✓ Schedule: Every Friday at 9:00 AM Eastern Time
✓ Pipeline with 2 stages:
  1. Execute analysis using methodology artifact
  2. Generate executive summary
✓ Configured with 30-minute timeout
✓ Skip if previous run still executing
✓ Saved to: $LOOM_DATA_DIR/workflows/friday-geospatial-report.yaml

The workflow will automatically execute every Friday. First run:
Friday, February 6, 2026 at 9:00 AM EST.

Monitor via: loom workflows list
```

**Narration:**
> "And just like that, we've gone from zero to a fully automated analytics pipeline. The Weaver created a scheduled workflow that will run every Friday morning. It references the methodology artifact the TD agent created, executes the analysis, and generates an executive summary. No cron jobs to configure, no infrastructure to manage—just natural language to production automation."

**Duration:** 90 seconds (3:30 - 5:00)

---

## Closing: The Full Picture (5:00)

**Screen Shows:**
```
Loom TUI - Sidebar:
- Guide
- Weaver
- teradata-sql-expert ✨
- friday-geospatial-report 📅 (next run: Fri Feb 6, 9:00 AM EST)

Artifacts Created:
- geospatial-analysis-methodology.md (methodology, 3KB)

Workflows Created:
- friday-geospatial-report.yaml (scheduled, enabled)
```

**Narration:**
> "In 5 minutes, starting from nothing:
>
> 1. ✅ **Agent Created**: Teradata SQL expert with domain knowledge
> 2. ✅ **Real Work**: 11 SQL queries, 2 errors, 2 self-corrections, full analysis
> 3. ✅ **Knowledge Captured**: Reusable methodology documented as artifact
> 4. ✅ **Automation Built**: Scheduled workflow running every Friday
>
> Zero YAML files edited. Zero configuration. Zero human intervention after the initial requests.
>
> This is the power of Loom: Agents that create agents. Agents that document themselves. Agents that build automated workflows. All observable, all traceable, all autonomous."

---

## Statistics

**Session 1: Weaver (Agent Creation)**
- Duration: ~45 seconds
- Messages: ~25
- Tool calls: 14
  - shell_execute: 8 (exploring examples)
  - tool_search: 4 (finding tools)
  - agent_management: 1 (creating agent)
  - workspace: 1 (writing guide)
- Cost: ~$0.40

**Session 2: Teradata SQL Expert (Analysis)**
- Duration: ~90 seconds
- Messages: ~20
- Tool calls: 10
  - teradata_execute_sql: 9 successful (11 attempts, 2 errors)
  - tool_search: 1 (finding tool)
- Errors: 2 (both self-corrected)
- Cost: ~$0.35

**Session 3: Teradata SQL Expert (Documentation)**
- Duration: ~60 seconds
- Messages: ~5
- Tool calls: 1
  - workspace: 1 (write artifact)
- Artifact: geospatial-analysis-methodology.md (3KB, searchable)
- Cost: ~$0.10

**Session 4: Weaver (Workflow Creation)**
- Duration: ~90 seconds
- Messages: ~15
- Tool calls: 3
  - workspace: 1 (read artifact)
  - agent_management: 1 (create workflow)
  - shell_execute: 1 (read examples)
- Workflow: friday-geospatial-report.yaml (scheduled)
- Cost: ~$0.35

**Grand Total:**
- Time: 5 minutes
- Total cost: **$1.20**
- Agents created: **1** (teradata-sql-expert)
- Workflows created: **1** (friday-geospatial-report)
- Artifacts created: **1** (methodology document)
- SQL queries: **11 attempted**, **9 first-try**, **2 self-corrected**
- Human interventions: **0** (after initial requests)
- YAML files edited by human: **0**

---

## Technical Details

### Observability Trail

All actions traced to SQLite:
```sql
-- Session history
SELECT * FROM sessions ORDER BY created_at DESC LIMIT 4;

-- Tool executions
SELECT * FROM tool_executions WHERE error IS NOT NULL;

-- Artifacts created
SELECT * FROM artifacts WHERE session_id IN (SELECT id FROM sessions ORDER BY created_at DESC LIMIT 4);

-- Scheduled workflows
SELECT * FROM scheduled_workflows WHERE workflow_name = 'friday-geospatial-report';

-- Next execution
SELECT next_execution_at FROM scheduled_workflows WHERE enabled = true;
```

### Artifact Management

**Storage:**
- Path: `$LOOM_DATA_DIR/artifacts/sessions/<session-id>/agent/geospatial-analysis-methodology.md`
- Database: SQLite + FTS5 full-text index
- Searchable: Yes (indexed by filename, content, tags)
- Session-scoped: Isolated to creating session

**Search Example:**
```bash
loom artifacts search "geospatial methodology"
# Returns: geospatial-analysis-methodology.md
```

### Workflow Scheduling

**Scheduler Configuration:**
- Source: YAML file (hot-reloadable)
- Cron: "0 9 * * 5" (Fridays at 9 AM)
- Timezone: America/New_York
- Next run: Calculated automatically
- History: All executions logged to SQLite

**Monitor Commands:**
```bash
# List scheduled workflows
loom workflows list --scheduled

# View next execution
loom workflows show friday-geospatial-report

# Trigger manually (for testing)
loom workflows trigger friday-geospatial-report

# View execution history
loom workflows history friday-geospatial-report --limit 10
```

---

## Demo Variations

### Variation 1: Different Feature Analysis
Change user query in Act 3:
```
analyze the Multi-Value Compression feature usage trends
```

### Variation 2: Different Schedule
Change workflow request in Act 5:
```
create a workflow that runs this analysis every Monday at 8 AM and sends results via email
```

### Variation 3: Multi-Feature Comparison
Add complexity in Act 4:
```
expand the methodology to compare multiple features side-by-side
```

---

## Setup Requirements

### Before Demo

1. **Fresh Loom Install:**
   ```bash
   # Clean install
   rm -rf ~/.loom
   just build
   ```

2. **Teradata Data Available:**
   - Table: `acc_ted_con_vw.dbp_featusg_agg_dly`
   - MCP server: `vantage-mcp` configured and running
   - Data range: Includes 2025 data

3. **Environment:**
   ```bash
   export ANTHROPIC_API_KEY="your-key"
   export LOOM_DATA_DIR="$HOME/.loom"
   ```

4. **Scheduler Enabled:**
   ```yaml
   # $LOOM_DATA_DIR/looms.yaml
   scheduler:
     enabled: true
     workflow_dir: "$LOOM_DATA_DIR/workflows"
     hot_reload: true
   ```

### Recording Setup

- **Tool**: Asciinema
- **Terminal**: 169x41 (widescreen)
- **Compression**: 4x speed-up for LLM thinking pauses
- **Output**: loomdemo-5min-workflow.cast

---

## Key Messages

1. **Zero Configuration**: No YAML editing, no setup files
2. **Self-Documenting**: Agents create artifacts describing their work
3. **Autonomous**: Self-correction without specialized tools
4. **Schedulable**: From agent work to automated workflows
5. **Observable**: Complete traceability via SQLite
6. **Reusable**: Knowledge captured in searchable artifacts

---

## Comparison: v1 Demo vs. This Demo

| Feature | Original Demo | This Demo |
|---------|--------------|-----------|
| Duration | 1:40 (from 6:24) | 5:00 |
| Agent Creation | ✅ Yes | ✅ Yes |
| Real Analysis | ✅ Yes | ✅ Yes |
| Self-Correction | ✅ Yes (2 errors) | ✅ Yes (2 errors) |
| Artifacts | ❌ No | ✅ Yes (methodology doc) |
| Workflow Creation | ❌ No | ✅ Yes (scheduled) |
| Automation | ❌ No | ✅ Yes (Friday 9 AM) |
| Knowledge Capture | ❌ No | ✅ Yes (reusable patterns) |

---

## Files Created

```
$LOOM_DATA_DIR/
├── agents/
│   └── teradata-sql-expert.yaml          # Generated by Weaver
├── workflows/
│   └── friday-geospatial-report.yaml     # Generated by Weaver
├── artifacts/
│   └── sessions/
│       └── <session-id>/
│           └── agent/
│               └── geospatial-analysis-methodology.md  # Generated by TD agent
└── loom.db                                # Contains sessions, executions, schedule
```

---

**End of Demo**

**Next Actions:**
- Wait for Friday 9 AM → Workflow executes automatically
- View execution: `loom workflows history friday-geospatial-report`
- Modify methodology artifact → Workflow uses updated version
- Create more workflows from the same methodology
