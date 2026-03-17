# Loom CLI Demo Script

**Date**: Wednesday
**Duration**: ~21 minutes
**Setup**: Two terminals side by side. T1 = server, T2 = commands.
**Status**: All commands tested 2026-03-16.

---

## Pre-Demo (do this before the audience arrives)

```bash
# Build all binaries
just build

# Start server
./bin/looms serve --llm-provider anthropic 2>/dev/null &
# Wait for banner + "Server ready on :60051"

# Seed pattern metrics (knowledge questions — no hallucination risk)
./bin/loom chat --thread pattern-test-agent "What are the best methods for detecting duplicate records in SQL databases?"
./bin/loom chat --thread pattern-test-agent "Explain the IQR method for outlier detection and when to use it vs z-score"
./bin/loom chat --thread pattern-test-agent "What data profiling checks should I run before loading data into a warehouse?"
./bin/loom chat --thread pattern-test-agent "How do I validate referential integrity across tables in a data pipeline?"
./bin/loom chat --thread pattern-test-agent "What strategies handle missing values in time-series data?"

# Stop server gracefully (flushes buffered metrics to DB)
kill -INT $(pgrep -f "bin/looms serve")
sleep 3
```

Verify metrics seeded:
```bash
sqlite3 ~/.loom/loom.db "SELECT pattern_name, total_usages FROM pattern_effectiveness;"
```

Expected: 4-5 patterns with 1 usage each (duplicate_detection, outlier_detection, data_profiling, data_validation, missing_value_analysis).

---

## Demo Start

### 1. Validate (2 min)

> "Before we start any server, let's validate our configs."

```bash
./bin/looms validate file ~/.loom/agents/pattern-test-agent.yaml
```

Expected:
```
/Users/you/.loom/agents/pattern-test-agent.yaml is valid
```

```bash
./bin/looms validate dir examples/reference/agents/
```

Expected: 15 valid, 0 invalid, warning on llama agent's low max_turns.

```bash
./bin/looms validate file examples/reference/workflows/event-driven/dungeon-crawler/workflows/dungeon-crawl.yaml
```

Expected: Fails — catches 4 missing agent file references (dm, fighter, wizard, rogue).

> "Shift-left. Catches missing agent references, flags auto-injected tools, warns on low max_turns. All before the server starts."

---

### 2. Start the Server (1 min)

> "One command to start."

**Terminal 1:**
```bash
./bin/looms serve --llm-provider anthropic
```

Expected: ASCII banner, 56 agents loaded, gRPC on 60051, HTTP on 5006.

> "gRPC on 60051, HTTP on 5006. 56 agents loaded from YAML. Zero code. Hot-reload is on — drop a YAML file and the agent appears."

---

### 3. Discover Agents (1 min)

**Terminal 2:**
```bash
./bin/loom agents
```

Expected: List of 56 agents with GUIDs, names, statuses.

> "56 agents, each with a stable GUID, name, and status. All from YAML files in the data directory."

---

### 4. Ask the Guide (2 min)

> "Instead of scrolling through 56 agents, let's ask the guide."

```bash
./bin/loom chat --thread guide "What agents do you have for security analysis?"
```

Expected: Lists security-agent, security-expert, security-voter, compliance-agent with recommendations.

> "The guide is a meta-agent. It discovers and recommends other agents based on what you need."

---

### 5. Research Agent with Streaming (3 min)

> "Now let's see a real agent work. Streaming mode shows every step."

```bash
./bin/loom chat --thread research-agent --stream --timeout 3m "What are the top 3 LLM agent frameworks in 2026?"
```

Expected output shows the pipeline:
```
[Pattern Selection: Analyzing query and selecting patterns]
[LLM: Generating response (turn 1)]
[Executing Tool: web_search]
[Executing Tool: web_search]
[LLM: Generating response (turn 2)]
[Executing Tool: web_search] ... (5-7 total searches)
[LLM: Generating response (turn 3-4)]
Based on my research... LangGraph, CrewAI, LangChain...
```

> "Watch the pipeline: pattern selection, LLM generation, web searches — 7 of them — then synthesis across 4 turns. All streamed in real-time."

---

### 6. Pattern-Guided Chat (2 min)

> "Patterns are domain knowledge as YAML. Watch the agent pick one automatically."

```bash
./bin/loom chat --thread pattern-test-agent --stream "Explain the IQR method for outlier detection and when to use it vs z-score"
```

Expected:
```
[Pattern Selection: Analyzing query and selecting patterns]
[Pattern Selection: Selected pattern: Outlier Detection and Analysis (59% confidence)]
[LLM: Generating response (turn 1)]
# IQR Method for Outlier Detection ...
```

> "It selected the outlier detection pattern at 59% confidence and injected it into the prompt. The pattern guided the agent to cover IQR formulas, when to use IQR vs z-score, and practical examples — domain knowledge encoded as YAML, not hardcoded in the prompt."

---

### 7. Pattern Effectiveness Tracking (3 min)

> "Every pattern-guided conversation records metrics. Let's see them."

```bash
./bin/looms learning analyze
```

Expected:
```
Pattern Analysis (5 patterns analyzed)

  data_validation (variant: default)
    Usage: 1 total (1 success, 0 failures)
    Success Rate: 100.0%
    Avg Cost: $0.0263 | Avg Latency: 27363ms
    Recommendation: PATTERN_INVESTIGATE (confidence: 8.3%)

  duplicate_detection (variant: default)
    Usage: 1 total (1 success, 0 failures)
    Success Rate: 100.0%
    ...
```

> "Five patterns tracked — duplicate_detection, outlier_detection, data_validation, data_profiling, missing_value_analysis. Each with success rate, cost, and latency. The system says INVESTIGATE — it's cautious, needs about 25 usages per pattern before recommending PROMOTE or DEMOTE. This is the self-improvement feedback loop."

```bash
./bin/looms learning proposals
```

Expected: No proposals yet (not enough data for confident recommendations).

> "No proposals yet — not enough data. Once the system has confidence, it generates actionable proposals: promote high-performing patterns, demote underperformers, remove failures. All with rollback."

---

### 8. Workflow Orchestration (5 min)

> "Now multi-agent workflows. Six orchestration patterns, all defined in YAML."

```bash
./bin/looms workflow list examples/reference/workflows/
```

Expected: 4 workflows — architecture-debate (debate), doc-generation (parallel), feature-pipeline (pipeline), security-analysis (parallel).

```bash
./bin/looms workflow run --dry-run examples/reference/workflows/orchestration-patterns/architecture-debate.yaml
```

Expected:
```
Loaded workflow: architecture-debate.yaml
  Pattern: debate
  Agents: 3 (pragmatist-engineer, senior-architect, architect-advocate)
Dry-run successful (workflow not executed)
```

> "Dry-run validated: debate pattern, 3 agents, all refs resolved. Zero LLM calls."

```bash
./bin/looms workflow run examples/reference/workflows/orchestration-patterns/architecture-debate.yaml
```

> "Two agents debate microservices vs monolith, a senior architect moderates. Three rounds. The debate pattern structures the conversation — opening arguments, rebuttals, final synthesis."

---

### 9. Config & Security (1 min)

```bash
./bin/looms config show
```

```bash
./bin/looms config list-keys
```

Expected: 18 secret types (anthropic, bedrock, openai, azure, gemini, mistral, github, postgres, brave, tavily, etc.)

> "18 secret types managed via system keyring. Not env vars, not .env files."

---

## If You Have Extra Time

### Live Pattern Watch (needs Terminal 3)

**Terminal 3:**
```bash
./bin/looms pattern watch
```

**Terminal 2:**
```bash
./bin/looms pattern create my-pattern --thread pattern-test-agent --file examples/patterns/sql-optimization.yaml
```

> "Hot-reload in action. Drop a pattern, it appears in the watch stream. The agent picks it up on the next conversation."

### Database Upgrade

```bash
./bin/looms upgrade --db ./loom.db
```

> "Schema migrations built in. No Flyway, no Alembic."

### HITL (Human-in-the-Loop)

```bash
# Start server with approval required
./bin/looms serve --require-approval

# In another terminal, while agent is running:
./bin/looms hitl list
./bin/looms hitl show <request-id>
./bin/looms hitl respond <request-id> --status approved --message "Looks good, proceed"
```

> "Agents can pause and ask for human approval. Full audit trail."

---

## Talking Points by Section

| Section | Key Message |
|---------|-------------|
| Validate | "Shift-left. Catch errors before runtime." |
| Serve | "One command. 56 agents. Zero code." |
| Guide | "Meta-agent that discovers other agents." |
| Research | "Real web search, streamed pipeline, multi-turn synthesis." |
| Patterns | "Domain knowledge as YAML. Auto-selected, auto-injected." |
| Learning | "Self-improving. Tracks success, cost, latency. Recommends promote/demote." |
| Workflow | "Debate, pipeline, fork-join, parallel, conditional, swarm. All YAML." |
| Config | "Keyring-managed secrets. Not .env files." |

---

## Troubleshooting

| Problem | Fix |
|---------|-----|
| Server won't start | `lsof -iTCP:60051` — kill stale process |
| `learning analyze` empty | Server wasn't stopped gracefully. Metrics flush on SIGINT. Re-seed and `kill -INT`. |
| research-agent timeout | Add `--timeout 3m` flag |
| deep-researcher fails | Needs external API — use `research-agent` instead |
| Workflow agents not found | Copy agent YAMLs to `~/.loom/agents/` first |
| Pattern selection not showing | Use `--stream` flag |
| LLM timeout on seed chat | Normal if LLM is slow. Skip that seed, others still work. |
