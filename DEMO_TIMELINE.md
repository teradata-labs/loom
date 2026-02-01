# Loom Demo Timeline

**Duration:** 1:40 (compressed from 6:24)
**Date:** January 29, 2026
**Version:** Loom v1.0.2
**Context:** Fresh installation, no pre-configuration

---

## What Happened

### 0:00-0:03 | Launch
- Ran `./bin/loom`
- TUI started with Guide agent

### 0:03-0:10 | Switch to Weaver
- Selected Weaver agent from sidebar

### 0:10-0:20 | User Request
**Input:** "I need an agent that knows teradata sql and can give me insights about my data"

### 0:20-0:35 | Agent Creation
Weaver executed:
- 16 shell commands (explored examples directory)
- 4 tool searches (found `teradata_execute_sql`)
- Read `vantage-mcp.yaml`, `database-query.yaml`, pattern files
- Created agent: `teradata-sql-expert`
- Wrote documentation file

Time: 15 seconds (original: 2:21)

### 0:35-0:55 | First Query
**Input:** "tell me about acc_ted_con_vw.dbp_featusg_agg_dly"

Agent executed:
- 3 tool searches (located `teradata_execute_sql` tool)
- 1st SQL attempt: Failed (parameter type error: `debugMode: "false"` vs `false`)
- 2nd SQL attempt: Succeeded (queried `DBC.ColumnsV` for 42 columns)
- 3rd SQL: Queried `DBC.TablesV` (table metadata)
- 4th SQL: `SELECT TOP 5` (sample data)

Output: Table structure with 42 columns categorized

### 0:55-1:35 | Second Query
**Input:** "give me usage of the 3d geospatial feature for 2025"

Agent executed 5 SQL queries:
1. Feature discovery query
2. Account distribution query (8 accounts, 11 sites, 42,704 requests)
3. Temporal analysis: Failed (Error 3707: reserved word `month` used as alias)
4. Temporal analysis: Succeeded (used `month_num` and `ORDER BY 1`)
5. Performance metrics query
6. Comparative analysis query

Output: Analysis showing:
- 8 accounts using feature
- Proximus SA: 2 high-usage sites
- November-December: 3-4x usage increase
- A1 Telekom: High AMP CPU usage (289-493 avg)
- 0.05% of standard geospatial volume
- 4 recommendations

### 1:35-1:40 | End
Display of final recommendations

---

## Statistics

**Weaver Session (agent creation):**
- Time: 2:21 → 0:15 (compressed)
- Tool calls: 23
- Cost: $0.53

**Teradata SQL Expert Session:**
- Time: 3:28 → 1:05 (compressed)
- Messages: 29
- Tool calls: 13
- SQL queries: 11 attempted, 9 successful, 2 errors
- Errors: Type mismatch (self-corrected), SQL syntax error 3707 (self-corrected)
- Cost: $0.47

**Total:**
- Time: 6:24 → 1:40 (3.8x compression)
- Cost: $1.00
- Agents created: 1
- Tool executions: 36
- Human interventions: 0

---

## Data Sources

All data extracted by Claude from:
- `~/.loom/loom.db` (sessions, messages, tool_executions, agents tables)
- `loomdemo` (asciinema recording, 543 lines)

Compression method: Pauses > 0.8s reduced by 4x
