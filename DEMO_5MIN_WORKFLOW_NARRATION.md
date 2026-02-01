# Loom 5-Minute Demo Narration
## "From Zero to Scheduled Workflow: The Complete Loom Experience"

**Duration:** 5 minutes
**Date:** January 30, 2026
**Version:** Loom v1.0.2
**Speaker Notes:** Pause indicated by [pause]. Emphasis shown in **bold**.

---

## Introduction (0:00 - 0:15)

> "Watch what happens when you start Loom for the very first time. This is a **completely fresh installation**—no configuration, no pre-built agents, nothing. [pause]
>
> In the next five minutes, you'll see Loom create a custom Teradata SQL agent, analyze real production data with autonomous error correction, document its own methodology as a searchable artifact, and then generate a scheduled workflow that runs every Friday morning. [pause]
>
> All from natural language. Zero YAML editing."

**[Screen: `./bin/loom` command, TUI launches]**

---

## Act 1: Agent Creation (0:15 - 1:00)

> "The Loom TUI launches with our **Weaver** agent—Loom's agent that creates other agents. [pause]
>
> Let's ask it to create a Teradata SQL expert:"

**[Screen: Switch to Weaver, type request]**

**Text on screen:**
```
I need an agent that knows teradata sql and can give me insights about my data
```

> "Weaver immediately starts working. It's exploring the examples directory, discovering available tools, reading Teradata pattern files. [pause]
>
> In the background, it found `vantage-mcp.yaml`—the Teradata database connector—and `database-query.yaml` with SQL execution capabilities. [pause]
>
> Now it's generating a complete agent configuration: system prompt with Teradata expertise, appropriate tools, LLM settings. [pause]
>
> **45 seconds later**, we have a fully functional Teradata SQL agent. No YAML editing. No configuration. Just natural language."

**[Screen shows Weaver completion message]**

---

## Act 2: Real Analysis with Self-Correction (1:00 - 2:30)

> "Let's put this generated agent to work. Switching to the **teradata-sql-expert** agent that Weaver just created."

**[Screen: Sidebar shows new agent, click to activate]**

### First Query (1:00 - 1:30)

**Text on screen:**
```
tell me about acc_ted_con_vw.dbp_featusg_agg_dly
```

> "The agent needs to discover how to execute SQL first. It's searching for tools... found the Teradata execution tool. [pause]
>
> Watch closely—the agent just made a mistake on its first query. **Type error**: it passed the debugMode parameter as a string instead of a boolean. [pause]
>
> Does it crash? Does it ask for help? **No.** [pause]
>
> It reads the error message, understands the problem, and **fixes it autonomously**. Second attempt: success. [pause]
>
> Now it's executing three SQL queries: column metadata, table structure, sample data. [pause]
>
> Look at this response. The agent didn't just dump 42 column names—it **categorized them** into logical groups: dimensional attributes, usage metrics, performance metrics. It explained the table's **business purpose**: license compliance and feature adoption tracking. [pause]
>
> This sets up our next query perfectly."

### Second Query (1:30 - 2:30)

**Text on screen:**
```
give me usage of the 3d geospatial feature for 2025
```

> "Now for something more complex: multi-dimensional feature analysis. [pause]
>
> The agent is building an analytical query sequence: feature discovery, account distribution, temporal trends... [pause]
>
> **Another error!** Teradata SQL Error 3707. The agent used the reserved word 'month' as an alias. This is a **Teradata-specific gotcha** that trips up even experienced SQL developers. [pause]
>
> Watch the self-correction: The agent **reads the Teradata error message**, understands that it violated two rules—reserved word as alias AND ordering by an aliased GROUP BY expression—and reformulates the query with a safe alias and ordinal positioning. [pause]
>
> **No specialized debugging tools.** Just reading, reasoning, fixing. [pause]
>
> Five analytical queries attempted. Two errors encountered. Two autonomous corrections. **Zero human intervention.** [pause]
>
> And look at the final output: account distribution, temporal patterns showing a Q4 spike, performance characteristics with a warning flag on high CPU usage, comparative analysis against other geospatial features, and **business recommendations**. [pause]
>
> This isn't just a SQL executor—it's thinking like a data analyst."

---

## Act 3: Knowledge Capture (2:30 - 3:30) **[NEW]**

**Text on screen:**
```
That's excellent analysis. Can you document the process you used to analyze the 3D geospatial feature? Save it as an artifact so we can reuse this methodology.
```

> "Here's where Loom becomes truly powerful. [pause]
>
> The agent is now using the **workspace tool** to create an artifact. Not just 'here's what I did'—but a **reusable framework** with SQL patterns, Teradata-specific error handling, and a five-step analysis methodology. [pause]
>
> Let's look at what it documented: [pause]
>
> - **Step 1**: Feature discovery with SQL pattern template
> - **Step 2**: Account distribution analysis ranked by I/O impact
> - **Step 3**: Temporal pattern analysis with **error handling note**—'Avoid reserved words like month as aliases, use ordinal ORDER BY'
> - **Step 4**: Performance metrics profiling
> - **Step 5**: Comparative analysis framework
>
> [pause]
>
> This artifact is now:
> - **Searchable** via FTS5 full-text index
> - **Reusable** with placeholder variables
> - **Tagged** for discovery: methodology, geospatial, analysis, teradata
> - **Persistent** in session-scoped storage
>
> [pause]
>
> The agent just created institutional knowledge. Any future analysis can reference this methodology. Any other agent can search for and find it. [pause]
>
> This is **self-documenting AI**."

---

## Act 4: Workflow Automation (3:30 - 5:00) **[NEW]**

**Text on screen:**
```
Perfect! Now use Weaver to create a scheduled workflow that runs this analysis every Friday morning at 9 AM Eastern Time. The workflow should use this methodology artifact.
```

> "We're switching back to **Weaver**. Now we're asking it to create a scheduled workflow based on the methodology the Teradata agent just documented. [pause]
>
> Weaver is reading the artifact to understand the methodology structure... [pause]
>
> Now it's generating a **scheduled workflow** YAML. Look at what it's configuring:
>
> - **Cron schedule**: Every Friday at 9:00 AM Eastern Time—'0 9 * * 5' in cron syntax
> - **Pipeline with two stages**:
>   - Stage 1: Execute analysis using the methodology artifact
>   - Stage 2: Generate executive summary with recommendations
> - **Workflow variables**: Methodology artifact path, alert thresholds, recipients
> - **Execution controls**: Skip if previous run is still going, 30-minute timeout
>
> [pause]
>
> The workflow is saved to `$LOOM_DATA_DIR/workflows/friday-geospatial-report.yaml`. [pause]
>
> **First execution**: Friday, February 6, 2026 at 9:00 AM Eastern Time. Automatically scheduled. [pause]
>
> Think about what just happened: [pause]
>
> 1. An **agent analyzed data** and self-corrected errors
> 2. That agent **documented its methodology** as a reusable artifact
> 3. A **meta-agent** read that artifact and created a **scheduled workflow**
> 4. The workflow will now run **every Friday**, using the documented methodology, analyzing fresh data, and generating executive summaries
>
> [pause]
>
> From zero to **fully automated analytics pipeline** in five minutes. [pause]
>
> No cron jobs to configure. No infrastructure to manage. No scheduled tasks to monitor. Just natural language to production automation."

---

## Closing (5:00)

> "Let's see what we accomplished in five minutes: [pause]
>
> **Agents Created**: One—teradata-sql-expert, with Teradata domain expertise [pause]
>
> **Real Work Completed**: 11 SQL queries attempted, 9 succeeded first-try, 2 errors autonomously corrected. Full multi-dimensional analysis delivered with business recommendations. [pause]
>
> **Knowledge Captured**: One methodology artifact—reusable SQL patterns, error handling strategies, Teradata-specific best practices. Searchable and persistent. [pause]
>
> **Workflows Automated**: One scheduled workflow—runs every Friday at 9 AM, applies documented methodology to fresh data, generates executive summaries. [pause]
>
> **Human Interventions**: Zero, after the initial natural language requests. [pause]
>
> **YAML Files Edited**: Zero. [pause]
>
> **Total Cost**: $1.20 in LLM API calls. [pause]
>
> [pause - longer]
>
> This is the power of Loom: [pause]
>
> - **Agents that create agents** from natural language
> - **Autonomous error correction** without specialized tools
> - **Self-documenting systems** that capture knowledge as artifacts
> - **Meta-agents that read artifacts** and generate workflows
> - **Complete observability**—every SQL query, every error, every decision traced to SQLite
>
> [pause]
>
> From zero to scheduled automation. From idea to production. In five minutes. [pause]
>
> **This is Loom v1.0.2.**"

---

## Technical Notes (For Production Team)

### Camera Work
- **Wide shot**: TUI with sidebar visible (show agent switching)
- **Medium shot**: Message area (show typing, responses)
- **Close-up**: Specific outputs (tables, analysis results)
- **Split screen**: Sidebar + main area simultaneously

### Callouts to Highlight
- ⏱️ **Timestamps**: Show elapsed time on major milestones
- 💰 **Cost**: Running total ($0.40 → $0.75 → $0.85 → $1.20)
- 🔧 **Tool Count**: Tool executions in each session
- 📊 **SQL Queries**: Query count and success/error tracking
- ❌ **Error Moments**: Highlight self-correction sequences
- ✨ **Artifact Creation**: When artifact is written
- 📅 **Schedule Confirmation**: Next execution timestamp

### Pacing Notes
- **Fast-forward (2x)**: Weaver tool discovery, reading files
- **Normal speed**: Agent creation, workflow generation
- **Slow-motion (0.5x)**: Error messages and self-correction moments
- **Pause (2 seconds)**: After key insights ("This is self-documenting AI")
- **Freeze frame**: Final statistics screen

### Audio Cues
- **Typing sounds**: User input
- **Soft whoosh**: Agent switching
- **Success chime**: Tool execution complete
- **Error buzz**: When errors occur (14:02:51, 14:04:59)
- **Recovery click**: When self-correction succeeds
- **Completion tone**: Workflow created and scheduled

### Graphics Overlays
- **Act 1-4 Labels**: "Act 1: Agent Creation", etc.
- **Error Callouts**: Red box around error messages with "Self-Correcting..."
- **Artifact Indicator**: Icon when artifact is created
- **Schedule Visualization**: Calendar showing Friday 9 AM marker

---

## Demo Variations

### Shorter Version (3 minutes)
- Skip temporal analysis details (keep self-correction)
- Compress artifact documentation to key points
- Show workflow creation without full YAML walkthrough

### Longer Version (7 minutes)
- Add: Searching for the artifact after creation
- Add: Manually triggering the workflow to test it
- Add: Viewing workflow execution history
- Add: Modifying the artifact and seeing workflow pick up changes

### Different Domain
- Change from Teradata to PostgreSQL or MongoDB
- Use different analysis: security audit, performance optimization
- Different schedule: daily, hourly, monthly

---

## Audience Segments

### For Technical Audience
**Emphasize:**
- gRPC architecture
- SQLite + FTS5 for search
- Cron-based scheduling
- Hot-reload capabilities
- Observability database schema

### For Business Audience
**Emphasize:**
- Zero configuration required
- Natural language interface
- Autonomous error correction
- Knowledge capture (artifacts)
- Production automation (workflows)

### For Data Analysts
**Emphasize:**
- SQL pattern reusability
- Teradata-specific optimizations
- Multi-dimensional analysis framework
- Scheduled reporting
- Business recommendations

---

## Post-Demo Discussion Points

1. **"How does self-correction work?"**
   - Agent reads error messages
   - Uses LLM reasoning to understand problem
   - Reformulates query/parameters
   - No specialized debugging needed

2. **"Can I modify the workflow?"**
   - Yes, edit YAML file directly
   - Hot-reload picks up changes automatically
   - Or use natural language to ask Weaver to modify it

3. **"What if the scheduled workflow fails?"**
   - All executions logged to SQLite
   - Error messages captured
   - Can view execution history
   - Can manually trigger for debugging

4. **"How secure are artifacts?"**
   - Session-scoped by default
   - Filesystem + database storage
   - Searchable via FTS5 index
   - Can set read restrictions

5. **"Can workflows call other workflows?"**
   - Yes, workflows can be stages in pipelines
   - Can create hierarchical workflows
   - Multi-agent collaboration patterns

---

**Recording Checklist:**

- [ ] Fresh Loom install verified
- [ ] Teradata test data available (2025 geospatial usage)
- [ ] MCP server (vantage-mcp) running
- [ ] Scheduler enabled in looms.yaml
- [ ] API key configured
- [ ] Terminal size: 169x41
- [ ] Asciinema ready
- [ ] Narration script printed/displayed
- [ ] Backup demo environment ready
- [ ] Test run completed successfully

---

**End of Narration Script**

**Final Frame Text:**
```
Teradata™ Loom v1.0.2

From Zero to Automation in 5 Minutes

✅ Custom Agent Created
✅ Real Analysis Performed
✅ Knowledge Captured as Artifact
✅ Workflow Scheduled for Every Friday

Zero Configuration. Zero YAML Editing. Zero Human Intervention.

Learn more: github.com/teradata-labs/loom
```
