# 5-Minute Loom Demo: Documentation Summary

## What Was Created

I've created a new 5-minute demo that extends the existing Loom demo with two powerful new capabilities:

1. **Artifact Creation** - The TD agent documents its analysis methodology
2. **Workflow Automation** - Weaver creates a scheduled workflow that runs every Friday

## Documents Created

### 1. DEMO_5MIN_WORKFLOW.md
**Complete demo script with full technical details**

**Contents:**
- Complete demo timeline (5 acts, 5 minutes)
- Detailed narration for each section
- Technical specifications (SQL queries, tool calls, errors)
- Expected outputs and responses
- Statistics and cost breakdown
- Setup requirements
- Comparison with original demo
- File structure created by demo

**Use for:**
- Understanding the complete demo flow
- Reference during recording
- Technical documentation
- Blog post/announcement content

### 2. DEMO_5MIN_WORKFLOW_NARRATION.md
**Speaker-focused narration script**

**Contents:**
- Voice-over script with timing marks
- Pause indicators and emphasis notes
- Camera work suggestions (wide/medium/close-up shots)
- Callout overlays to highlight
- Pacing notes (fast-forward, slow-motion, freeze frames)
- Audio cue suggestions
- Production notes for video editing

**Use for:**
- Recording voice-over narration
- Video production planning
- Presentation delivery
- Demo walkthrough guidance

### 3. DEMO_5MIN_QUICKREF.md
**Quick reference for running the demo**

**Contents:**
- Pre-demo setup checklist
- Exact commands to type at each step
- Expected outputs at each checkpoint
- Verification commands
- Troubleshooting guide
- Timeline checkpoints
- Backup plan if something fails

**Use for:**
- Live demo execution
- Recording asciinema sessions
- Quick verification
- On-the-spot troubleshooting

---

## Demo Flow Overview

### What the Demo Shows

**Act 1: Launch (0:00 - 0:15)**
- Fresh Loom installation starts
- Guide agent greets user

**Act 2: Create Agent (0:15 - 1:00)**
- User asks Weaver to create Teradata SQL expert
- Weaver discovers tools, reads examples, generates agent
- 45 seconds → fully functional agent

**Act 3: Analyze Data (1:00 - 2:30)**
- TD agent analyzes real production table
- First query: Type error → self-correction
- Second query: 5 analytical queries, SQL Error 3707 → self-correction
- Delivers comprehensive analysis with business recommendations

**Act 4: Capture Knowledge (2:30 - 3:30)** ✨ **NEW**
- User asks agent to document its methodology
- Agent creates artifact with reusable SQL patterns
- Artifact includes error handling strategies
- Saved as searchable, persistent knowledge

**Act 5: Automate Workflow (3:30 - 5:00)** ✨ **NEW**
- User asks Weaver to create scheduled workflow
- Weaver reads the methodology artifact
- Creates workflow with cron schedule (every Friday 9 AM)
- Pipeline uses the documented methodology
- Workflow automatically scheduled and ready to run

---

## Key Differentiators from Original Demo

| Feature | Original Demo | New 5-Min Demo |
|---------|---------------|----------------|
| **Duration** | 1:40 (compressed from 6:24) | 5:00 |
| **Agent Creation** | ✅ Yes | ✅ Yes |
| **Real Analysis** | ✅ Yes | ✅ Yes |
| **Self-Correction** | ✅ Yes (2 errors) | ✅ Yes (2 errors) |
| **Artifact Creation** | ❌ No | ✅ **NEW: Methodology doc** |
| **Workflow Generation** | ❌ No | ✅ **NEW: Scheduled workflow** |
| **Automation** | ❌ No | ✅ **NEW: Runs every Friday** |
| **Knowledge Capture** | ❌ No | ✅ **NEW: Reusable patterns** |

---

## Technical Requirements

### Environment Setup
```bash
# Clean install
rm -rf ~/.loom
just build

# Configure scheduler
cat > ~/.loom/looms.yaml <<EOF
scheduler:
  enabled: true
  workflow_dir: "$HOME/.loom/workflows"
  hot_reload: true
EOF

# Set API key
export ANTHROPIC_API_KEY="your-key"
```

### Data Requirements
- **Table:** `acc_ted_con_vw.dbp_featusg_agg_dly`
- **MCP Server:** `vantage-mcp` running and configured
- **Data Range:** Must include 2025 records with 3D Geospatial feature usage

### Recording Setup
```bash
# Terminal size
asciinema rec --cols 169 --rows 41 loomdemo-5min-workflow.cast

# Then run demo
./bin/loom
```

---

## Files Created by Demo

After running the demo, these files will exist:

```
$LOOM_DATA_DIR/
├── agents/
│   └── teradata-sql-expert.yaml              # Created by Weaver
├── workflows/
│   └── friday-geospatial-report.yaml         # Created by Weaver
└── artifacts/
    └── sessions/
        └── <session-id>/
            └── agent/
                └── geospatial-analysis-methodology.md  # Created by TD agent
```

**Database Records:**
- 4 sessions (Guide interactions, Weaver x2, TD agent x2)
- 1 artifact (methodology)
- 1 scheduled workflow
- ~40 tool executions
- Complete trace of all SQL queries and errors

---

## Verification After Demo

```bash
# Check agent exists
ls ~/.loom/agents/teradata-sql-expert.yaml

# Check workflow exists
ls ~/.loom/workflows/friday-geospatial-report.yaml

# Search for artifact
loom artifacts search "methodology"

# View scheduled workflow
loom workflows list --scheduled

# Check next execution
loom workflows show friday-geospatial-report
```

---

## Demo Statistics

**Expected Metrics:**

| Metric | Value |
|--------|-------|
| **Total Duration** | 5:00 |
| **Total Cost** | ~$1.20 USD |
| **Agents Created** | 1 (teradata-sql-expert) |
| **Workflows Created** | 1 (friday-geospatial-report) |
| **Artifacts Created** | 1 (methodology doc) |
| **SQL Queries** | 11 attempted, 9 first-try, 2 self-corrected |
| **Errors** | 2 (both autonomously fixed) |
| **Human YAML Edits** | 0 |
| **Sessions** | 4 total |

---

## Key Messages

### For Technical Audience
1. **Zero Configuration**: No YAML editing required
2. **Self-Correction**: Autonomous error recovery via LLM reasoning
3. **Observability**: Complete SQLite trace of all actions
4. **Artifact System**: FTS5-indexed searchable knowledge base
5. **Workflow Automation**: Cron-based scheduling with hot-reload

### For Business Audience
1. **Natural Language**: Create agents and workflows by describing what you need
2. **Self-Documenting**: Agents capture their methodologies as reusable artifacts
3. **Production Ready**: From idea to scheduled automation in 5 minutes
4. **Cost Efficient**: $1.20 to create agent, analyze data, document process, automate workflow
5. **Zero Maintenance**: Workflows run automatically, no infrastructure to manage

### For Data Analysts
1. **Reusable Patterns**: SQL templates with error handling built-in
2. **Teradata Expertise**: Understands TD-specific syntax and optimizations
3. **Multi-Dimensional Analysis**: Account distribution, temporal trends, performance metrics
4. **Business Context**: Not just queries—insights and recommendations
5. **Scheduled Reports**: Automated weekly analysis without manual effort

---

## Next Steps

### For Recording the Demo

1. **Review:** Read through DEMO_5MIN_WORKFLOW.md completely
2. **Practice:** Run through demo 2-3 times to ensure smooth flow
3. **Setup:** Follow DEMO_5MIN_QUICKREF.md setup checklist
4. **Record:** Use asciinema with specified terminal size
5. **Narrate:** Use DEMO_5MIN_WORKFLOW_NARRATION.md for voice-over
6. **Post-Process:** Compress thinking pauses, add overlays, sync audio

### For Presenting Live

1. **Print:** DEMO_5MIN_QUICKREF.md for quick reference
2. **Backup:** Have pre-created artifacts ready if demo fails
3. **Test:** Run complete demo at least once in presentation environment
4. **Timing:** Use checkpoints to stay on schedule
5. **Flexibility:** Know which sections can be compressed if running long

### For Blog Posts / Announcements

1. **Source:** Use DEMO_5MIN_WORKFLOW.md for technical content
2. **Screenshots:** Capture key moments (error corrections, artifact creation, workflow schedule)
3. **Statistics:** Include cost, time, and capability metrics
4. **Comparison:** Show before/after (manual process vs. automated workflow)
5. **Call to Action:** Link to documentation, GitHub, download page

---

## Troubleshooting

### Demo Won't Start
- Check: Loom built with `just build`
- Check: API key set correctly
- Check: No conflicting processes on port 50051

### Agent Creation Fails
- Check: Examples directory exists with vantage-mcp.yaml
- Check: Weaver has access to examples/
- Check: Network access to Anthropic API

### SQL Queries Fail
- Check: vantage-mcp server running
- Check: Database credentials configured
- Check: Table exists and has data
- Check: 2025 data available

### Artifact Not Created
- Check: Workspace tool available to agent
- Check: Permissions on ~/.loom/artifacts/
- Check: Session ID valid and active

### Workflow Not Scheduled
- Check: Scheduler enabled in looms.yaml
- Check: Workflow directory configured
- Check: YAML syntax valid
- Check: Server logs for scheduler startup

---

## Additional Resources

### Related Documentation
- `loomdemo-narration.md` - Original demo narration
- `DEMO_TIMELINE.md` - Original demo timeline
- `docs/guides/artifacts-usage.md` - Artifact system guide
- `docs/guides/weaver-usage.md` - Weaver agent guide
- `examples/reference/workflows/orchestration-patterns/scheduled-workflows/` - Workflow examples

### Video Production
- `convert-to-gif.sh` - Convert asciinema to GIF
- `speed-up-thinking.py` - Compress LLM pauses
- `generate-tts-narration.sh` - Generate voice-over
- `create-video-with-vo.sh` - Combine video + audio

---

## Questions & Support

### Common Questions

**Q: Can I use a different database instead of Teradata?**
A: Yes! Change the agent creation request to reference PostgreSQL, MySQL, MongoDB, etc. Weaver will find appropriate tools and patterns.

**Q: Can workflows run at different schedules?**
A: Yes! Use any standard cron expression. Examples in `examples/reference/workflows/orchestration-patterns/scheduled-workflows/README.md`

**Q: What if I want the workflow to run daily instead of weekly?**
A: Change the request to Weaver: "...runs every day at 9 AM" → cron: "0 9 * * *"

**Q: Can I manually trigger the workflow for testing?**
A: Yes! `loom workflows trigger friday-geospatial-report`

**Q: Where are execution logs stored?**
A: SQLite database at `~/.loom/loom.db` in `scheduled_workflow_executions` table

---

## Success Criteria

Demo is successful if:
- [ ] Agent created in under 60 seconds
- [ ] Both SQL errors self-corrected
- [ ] Artifact created and searchable
- [ ] Workflow scheduled with correct cron expression
- [ ] Total time ≤ 5 minutes
- [ ] No human YAML editing
- [ ] All files created in expected locations

---

**Created:** January 30, 2026
**Version:** 1.0
**For:** Loom v1.0.2+
**Author:** Claude Code
**Status:** Ready for recording/presentation
