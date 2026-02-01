# 5-Minute Demo Quick Reference

## Pre-Demo Setup

```bash
# 1. Clean install
rm -rf ~/.loom
just build

# 2. Verify environment
aws sso login --profile bedrock

# 3. Start recording
asciinema rec loomdemo-5min-workflow.cast
```

---

## Demo Script (5 Minutes)

### Act 1: Launch (0:00 - 0:15)
```bash
./bin/loom
```
**Wait for:** TUI to load, Guide agent greeting

- Ask the guide a question

---

### Act 2: Create Agent (0:15 - 1:00)

**Step 1:** Click "Weaver" in sidebar

**Step 2:** Type:
```
I need an agent that knows teradata sql and can give me insights about my data
```

**Step 3:** Press Enter, wait for Weaver to complete

**Expected:**
- "✓ Created teradata-sql-expert agent"
- Agent appears in sidebar

---

### Act 3: Analyze Data (1:00 - 2:30)

**Step 1:** Click "teradata-sql-expert" in sidebar

**Step 2:** First query:
```
tell me about acc_ted_con_vw.dbp_featusg_agg_dly
```

**Expected:**
- Tool search for teradata_execute_sql
- Error: debugMode type mismatch
- Self-correction
- Table analysis with 42 columns categorized

**Step 3:** Second query:
```
give me usage of the 3d geospatial feature for 2025
```

**Expected:**
- 5 SQL queries
- Error 3707: reserved word "month"
- Self-correction
- Full analysis with recommendations

---

### Act 4: Capture Knowledge (2:30 - 3:30)

**Type:**
```
can you document the process you used to analyze the 3D geospatial feature? Save it as an artifact and give me the path so we can reuse this methodology.
```

**Expected:**
- workspace tool called (action: write, scope: artifact)
- Artifact saved: geospatial-analysis-methodology.md
- Confirmation message with path

---

### Act 5: Automate Workflow (3:30 - 5:00)

**Step 1:** Click "Weaver" in sidebar

**Step 2:** Type:
```
create a scheduled workflow that runs geospatial-analysis-methodology.md every Friday morning at 9 AM Eastern Time and save the analysis to a file. 
```

**Expected:**
- workspace tool (action: read) - reads artifact
- agent_management tool - creates workflow
- Workflow saved: friday-geospatial-report.yaml
- Schedule confirmed: "Every Friday at 9:00 AM EST"
- Next run date displayed

---

### Closing (5:00)

**Press:** Ctrl+Q to quit

**Confirmation:** "Are you sure?" → y

**Stop recording:** Ctrl+D

---

## Expected Outputs

### Files Created
```
~/.loom/
├── agents/
│   └── teradata-sql-expert.yaml
├── workflows/
│   └── friday-geospatial-report.yaml
└── artifacts/
    └── sessions/
        └── <session-id>/
            └── agent/
                └── geospatial-analysis-methodology.md
```

### Database Records
```sql
-- Sessions created
SELECT * FROM sessions ORDER BY created_at DESC LIMIT 4;
-- Expected: 4 sessions (Guide, Weaver, TD agent x2, Weaver)

-- Artifacts
SELECT * FROM artifacts;
-- Expected: 1 artifact (methodology)

-- Workflows scheduled
SELECT * FROM scheduled_workflows;
-- Expected: 1 workflow (friday-geospatial-report)
```

---

## Verification Commands

```bash
# Verify agent created
ls ~/.loom/agents/teradata-sql-expert.yaml

# Verify workflow created
ls ~/.loom/workflows/friday-geospatial-report.yaml

# Verify artifact
loom artifacts search "geospatial methodology"

# Verify workflow scheduled
loom workflows list --scheduled

# Check next execution
loom workflows show friday-geospatial-report | grep "Next execution"
```

---

## Troubleshooting

### Agent creation fails
- Check: Weaver has access to examples/ directory
- Check: vantage-mcp.yaml exists in examples/
- Check: Anthropic API key is valid

### SQL queries fail
- Check: vantage-mcp server is running
- Check: Table acc_ted_con_vw.dbp_featusg_agg_dly exists
- Check: Table has 2025 data

### Artifact not created
- Check: workspace tool is available to teradata-sql-expert
- Check: Session directory created: ~/.loom/artifacts/sessions/
- Check: Permissions on ~/.loom/artifacts/

### Workflow not scheduled
- Check: scheduler.enabled: true in looms.yaml
- Check: Workflow YAML syntax is valid
- Check: Scheduler is running (server logs)

---

## Post-Demo Verification

```bash
# View all sessions
loom sessions list

# View artifacts
loom artifacts list

# View scheduled workflows
loom workflows list --scheduled

# View next execution time
grpcurl -d '{}' localhost:50051 loom.v1.LoomService/ListScheduledWorkflows

# Manually trigger workflow (test)
loom workflows trigger friday-geospatial-report

# View execution history (after trigger)
loom workflows history friday-geospatial-report
```

---

## Statistics to Capture

During demo, note:
- [ ] Weaver session duration
- [ ] Number of tool calls in each session
- [ ] Cost per session (from logs)
- [ ] SQL query count
- [ ] Error count and types
- [ ] Artifact file size
- [ ] Workflow schedule confirmation

---

## Recording Post-Processing

```bash
# Convert to GIF
./convert-to-gif.sh loomdemo-5min-workflow.cast

# Compress (speed up thinking pauses)
./speed-up-thinking.py loomdemo-5min-workflow.cast loomdemo-5min-compressed.cast

# Add narration
./generate-tts-narration.sh DEMO_5MIN_WORKFLOW_NARRATION.md

# Create video with voiceover
./create-video-with-vo.sh loomdemo-5min-compressed.cast narration.mp3
```

---

## Backup Plan

If demo fails at any step:

1. **Agent creation fails:**
   - Fall back to pre-created agent
   - Skip to Act 3

2. **SQL analysis fails:**
   - Use cached results
   - Show pre-recorded artifact

3. **Workflow creation fails:**
   - Show pre-created workflow YAML
   - Explain what Weaver would generate

---

## Key Talking Points (If Demoing Live)

1. **Zero Configuration:**
   "Notice: No config files edited. No YAML written. Just natural language."

2. **Self-Correction:**
   "The agent made a mistake and fixed it autonomously. No specialized debugging tools."

3. **Artifacts:**
   "This knowledge is now searchable, reusable, persistent. Self-documenting AI."

4. **Workflows:**
   "From agent work to automated pipeline in 90 seconds. Natural language to production."

5. **Observability:**
   "Every SQL query, every error, every decision—traced to SQLite. Complete audit trail."

---

## Timeline Checkpoints

| Time | Checkpoint | Duration |
|------|------------|----------|
| 0:15 | TUI launched | 15s |
| 1:00 | Agent created | 45s |
| 1:30 | First query complete | 30s |
| 2:30 | Second query complete | 60s |
| 3:30 | Artifact created | 60s |
| 5:00 | Workflow scheduled | 90s |

**Total:** 5:00

If running behind: Skip detailed table structure explanation in Act 3.
If running ahead: Add artifact search demonstration.

---

## Emergency Contacts

- **API Issues:** Anthropic support
- **Database Issues:** Teradata/MCP team
- **Recording Issues:** asciinema documentation
- **Loom Issues:** Check looms serve logs

---

**Quick Start Command:**
```bash
# One command to verify everything
just verify && ./bin/loom
```

**Quick Stop Command:**
```bash
# Ctrl+Q in TUI, or:
pkill loom
```

---

**Demo Date:** _______________
**Demo By:** _______________
**Recording File:** _______________
**Notes:** _________________________________
