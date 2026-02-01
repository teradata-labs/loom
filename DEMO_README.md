# Loom Demo Documentation Index

## Overview

This directory contains documentation for Loom demonstrations, showing the complete agent lifecycle from creation to automation.

---

## 5-Minute Demo: Zero to Scheduled Workflow ✨ **NEW**

**Full Title:** "From Zero to Scheduled Workflow: Fresh Install → Custom Agent → Artifacts → Automated Workflows"

### What It Shows
1. Weaver creates a Teradata SQL expert agent (45 seconds)
2. Agent analyzes production data with self-correction (90 seconds)
3. Agent documents its methodology as an artifact (60 seconds)
4. Weaver creates a scheduled workflow that runs every Friday (90 seconds)

**Total:** 5 minutes | **Cost:** ~$1.20 | **Human YAML edits:** 0

### Documentation Files

| File | Purpose | Use When |
|------|---------|----------|
| **DEMO_5MIN_SUMMARY.md** | Overview & quick start | Planning or first-time review |
| **DEMO_5MIN_WORKFLOW.md** | Complete technical script | Detailed reference, blog posts |
| **DEMO_5MIN_WORKFLOW_NARRATION.md** | Voice-over script | Recording narration, presenting |
| **DEMO_5MIN_QUICKREF.md** | Command cheat sheet | Running the demo live |

---

## Original Demo: Zero to Insights (1:40)

**Full Title:** "Zero to Insights in 90 Seconds: Creating Custom Teradata Agents"

### What It Shows
1. Weaver creates a Teradata SQL expert agent
2. Agent analyzes production data with self-correction
3. Delivers business insights and recommendations

**Total:** 1:40 (compressed from 6:24) | **Cost:** ~$1.00 | **Human YAML edits:** 0

### Documentation Files

| File | Purpose | Use When |
|------|---------|----------|
| **loomdemo-narration.md** | Complete narration with technical details | Reference, documentation |
| **DEMO_TIMELINE.md** | Timeline summary | Quick overview |
| **loomdemo-final.cast** | Asciinema recording | Playback, conversion |
| **loomdemo.mp4** | Video recording | Presentations, website |

---

## Quick Start Guide

### To Run 5-Minute Demo

```bash
# 1. Setup
rm -rf ~/.loom && just build
export ANTHROPIC_API_KEY="your-key"

# Enable scheduler
cat > ~/.loom/looms.yaml <<EOF
scheduler:
  enabled: true
  workflow_dir: "$HOME/.loom/workflows"
  hot_reload: true
EOF

# 2. Start recording
asciinema rec loomdemo-5min.cast --cols 169 --rows 41

# 3. Run demo (follow DEMO_5MIN_QUICKREF.md)
./bin/loom

# 4. Stop recording
# Ctrl+D after exiting Loom
```

### To Run Original Demo

```bash
# 1. Setup
rm -rf ~/.loom && just build
export ANTHROPIC_API_KEY="your-key"

# 2. Run
./bin/loom
# Follow loomdemo-narration.md
```

---

## File Organization

```
loom-services-demo/
├── DEMO_README.md                          ← You are here
│
├── 5-Minute Demo Files:
│   ├── DEMO_5MIN_SUMMARY.md                ← Start here
│   ├── DEMO_5MIN_WORKFLOW.md               ← Complete script
│   ├── DEMO_5MIN_WORKFLOW_NARRATION.md     ← Voice-over
│   └── DEMO_5MIN_QUICKREF.md               ← Quick reference
│
├── Original Demo Files:
│   ├── loomdemo-narration.md               ← Narration script
│   ├── DEMO_TIMELINE.md                    ← Timeline
│   ├── loomdemo-final.cast                 ← Recording
│   └── loomdemo.mp4                        ← Video
│
└── Utility Scripts:
    ├── convert-to-gif.sh                   ← Asciinema → GIF
    ├── speed-up-thinking.py                ← Compress pauses
    ├── generate-tts-narration.sh           ← Generate voice
    ├── create-video-with-vo.sh             ← Combine video + audio
    ├── CONVERTING_ASCIINEMA.md             ← Conversion guide
    ├── EDITING_GIFS.md                     ← GIF editing guide
    └── ADD_VOICEOVER.md                    ← Voiceover guide
```

---

## Comparison: Original vs. 5-Minute Demo

| Aspect | Original (1:40) | 5-Minute Demo |
|--------|-----------------|---------------|
| **Focus** | Agent creation + analysis | Full lifecycle + automation |
| **Duration** | 1:40 (compressed) | 5:00 |
| **Acts** | 5 acts | 5 acts |
| **Agent Creation** | ✅ Teradata SQL expert | ✅ Teradata SQL expert |
| **Data Analysis** | ✅ 2 queries, 2 errors | ✅ 2 queries, 2 errors |
| **Self-Correction** | ✅ Type error + SQL error | ✅ Type error + SQL error |
| **Artifact Creation** | ❌ Not included | ✅ **Methodology doc** |
| **Workflow Automation** | ❌ Not included | ✅ **Friday scheduler** |
| **Cost** | ~$1.00 | ~$1.20 |
| **Best For** | Quick proof of concept | Complete platform demo |

---

## Use Cases by Audience

### Technical Developers
- **Show:** Original demo (quick, focused on agent creation)
- **Emphasize:** Proto-first API, gRPC, SQLite observability
- **Next:** Documentation in `docs/architecture/`

### Business Decision Makers
- **Show:** 5-minute demo (full automation story)
- **Emphasize:** Natural language, zero config, cost efficiency
- **Next:** ROI discussion, use case workshops

### Data Analysts
- **Show:** 5-minute demo (methodology artifacts, scheduled reports)
- **Emphasize:** Reusable patterns, Teradata expertise, automation
- **Next:** Custom agent creation for their use cases

### Product Managers
- **Show:** Both demos back-to-back
- **Emphasize:** Iteration speed, knowledge capture, workflow automation
- **Next:** Roadmap discussion, integration planning

---

## Production Workflow

### Video Production Pipeline

```
1. Record asciinema
   ↓
2. Compress thinking pauses (speed-up-thinking.py)
   ↓
3. Convert to MP4 (convert-to-gif.sh or asciinema-to-video.sh)
   ↓
4. Generate narration (generate-tts-narration.sh)
   ↓
5. Combine video + audio (create-video-with-vo.sh)
   ↓
6. Add overlays, callouts (video editor)
   ↓
7. Export final video
```

### Presentation Workflow

```
1. Review DEMO_5MIN_SUMMARY.md
   ↓
2. Practice with DEMO_5MIN_QUICKREF.md
   ↓
3. Print quick reference for backup
   ↓
4. Test environment (Loom, database, API key)
   ↓
5. Present live
   ↓
6. Handle Q&A (refer to DEMO_5MIN_WORKFLOW.md)
```

---

## Demo Variations

### Shorter Version (3 minutes)
- Skip temporal analysis details
- Compress artifact creation to highlights
- Show workflow creation without YAML walkthrough
- **Files:** Use DEMO_5MIN_QUICKREF.md, skip some steps

### Longer Version (7 minutes)
- Add artifact search demonstration
- Manually trigger workflow to show execution
- View workflow execution history
- Show hot-reload by editing artifact
- **Files:** Extend DEMO_5MIN_WORKFLOW.md with extra acts

### Different Domain
- PostgreSQL instead of Teradata
- Security audit instead of feature analysis
- Daily schedule instead of weekly
- **Files:** Modify prompts in DEMO_5MIN_QUICKREF.md

---

## Troubleshooting

### Demo Won't Start
**Check:**
- [ ] Loom built successfully (`just build`)
- [ ] API key set (`echo $ANTHROPIC_API_KEY`)
- [ ] No port conflicts (`lsof -i :50051`)
- [ ] Clean install (`rm -rf ~/.loom`)

**Fix:** See DEMO_5MIN_QUICKREF.md troubleshooting section

### Recording Issues
**Check:**
- [ ] Terminal size correct (169x41)
- [ ] Asciinema installed (`asciinema --version`)
- [ ] Disk space available

**Fix:** See CONVERTING_ASCIINEMA.md

### Data Not Available
**Check:**
- [ ] MCP server running (`ps aux | grep vantage-mcp`)
- [ ] Database accessible
- [ ] Table has 2025 data

**Fix:** Use alternative table or skip to pre-recorded section

---

## Statistics & Metrics

### 5-Minute Demo Metrics

| Metric | Value |
|--------|-------|
| Total Duration | 5:00 |
| Agent Creation | 0:45 |
| Data Analysis | 1:30 |
| Artifact Creation | 1:00 |
| Workflow Automation | 1:45 |
| Total Cost (LLM) | ~$1.20 |
| SQL Queries | 11 total |
| Errors (self-corrected) | 2 |
| Human YAML Edits | 0 |
| Files Created | 3 |

### Original Demo Metrics

| Metric | Value |
|--------|-------|
| Total Duration | 1:40 (compressed from 6:24) |
| Agent Creation | 0:15 |
| Data Analysis | 1:25 |
| Total Cost (LLM) | ~$1.00 |
| SQL Queries | 9 total |
| Errors (self-corrected) | 2 |
| Human YAML Edits | 0 |
| Files Created | 1 |

---

## Additional Resources

### Documentation
- Main README: `README.md`
- Architecture docs: `docs/architecture/`
- User guides: `docs/guides/`
- Reference: `docs/reference/`

### Examples
- Agent examples: `examples/reference/agents/`
- Workflow examples: `examples/reference/workflows/`
- Pattern examples: `patterns/`

### Utilities
- Build tools: `Justfile`
- CI/CD: `.github/workflows/`
- Packaging: `packaging/`

---

## Contact & Support

### Questions
- GitHub Issues: https://github.com/teradata-labs/loom/issues
- Documentation: `docs/`
- Examples: `examples/`

### Contributing
- Guidelines: `CONTRIBUTING.md`
- Code of Conduct: `CODE_OF_CONDUCT.md`
- Security: `SECURITY.md`

---

## Quick Links

### To Get Started
1. Read: `DEMO_5MIN_SUMMARY.md`
2. Setup: Follow setup section
3. Practice: Use `DEMO_5MIN_QUICKREF.md`
4. Record: Use asciinema
5. Share: Convert to video/GIF

### To Present
1. Print: `DEMO_5MIN_QUICKREF.md`
2. Practice: 2-3 run-throughs
3. Backup: Pre-record artifacts
4. Present: Follow quick reference
5. Q&A: Refer to `DEMO_5MIN_WORKFLOW.md`

### To Customize
1. Read: `DEMO_5MIN_WORKFLOW.md` (understand flow)
2. Modify: Prompts in quick reference
3. Test: Run through modified demo
4. Document: Update narration if needed
5. Record: New version

---

**Last Updated:** January 30, 2026
**Loom Version:** v1.0.2
**Status:** Ready for recording and presentation

---

**Next Steps:**
1. ☐ Review DEMO_5MIN_SUMMARY.md
2. ☐ Setup environment (clean install, API key, scheduler)
3. ☐ Practice demo 2-3 times
4. ☐ Record with asciinema
5. ☐ Add narration and produce video
6. ☐ Share with team/community
