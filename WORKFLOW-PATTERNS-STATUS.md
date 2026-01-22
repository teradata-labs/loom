# Loom Workflow Patterns - Status and Testing

## ⚠️ Key Finding: Orchestration Pattern Format Mismatch

**Issue:** Examples in `examples/reference/workflows/` use `spec.pattern` (old format), but the implementation expects `spec.type` (new format).

**Fix Required:** Update all orchestration pattern examples to use `spec.type` instead of `spec.pattern`.

**Reference:** See `examples/workflow-all-fields-example.yaml` lines 200-370 for correct orchestration pattern format.

---

## Overview

Loom supports two categories of workflow patterns:
1. **Communication Patterns** - Real-time multi-agent coordination (uses `spec.entrypoint`)
2. **Orchestration Patterns** - Static execution flows (uses `spec.type`)

## 1. Communication Patterns (✅ Fully Working)

These workflows enable real-time agent communication using Loom's tri-modal communication system.

### Pattern A: Pub-Sub (Peer-to-Peer)

**Status:** ✅ Fully implemented and tested

**Examples:**
- `dungeon-crawl-workflow` - D&D party adventure
- `brainstorm-session` - Creative brainstorming with facilitator, creative, and analyst

**How It Works:**
- All agents subscribe to shared topic (e.g., "party-chat", "brainstorm-chat")
- Agents publish messages using `publish(topic="...", message="...")`
- Messages auto-injected via event-driven broadcast bus
- No manual polling required - responses appear automatically

**YAML Structure:**
```yaml
spec:
  entrypoint: coordinator-agent-name
  agents:
    - name: coordinator
      agent: coordinator-agent-id
    - name: participant1
      agent: participant1-agent-id
  communication:
    pattern: "peer-to-peer-pub-sub"
    topic: "shared-topic-name"
```

**Testing:**
```bash
./bin/loom chat --thread dungeon-crawl-workflow "DM, start an adventure"
./bin/loom chat --thread brainstorm-session "Brainstorm AI app names"
```

**Test Results:** ✅ All passing (tested Jan 22, 2026)

---

### Pattern B: Hub-and-Spoke (Message Queue)

**Status:** ✅ Fully implemented and tested

**Examples:**
- `dnd-campaign-workflow` - Campaign creation with coordinator + specialists
- `vacation-planning-workflow` - Vacation planning with coordinator + analysts

**How It Works:**
- Coordinator agent orchestrates specialist sub-agents
- Uses `send_message(to_agent="...", message="...")` for direct communication
- Responses auto-injected into coordinator conversation
- Event-driven - no manual polling

**YAML Structure:**
```yaml
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      agent: coordinator-agent-id
      metadata:
        role: coordinator
        workflow: workflow-name
    - name: specialist1
      agent: specialist1-agent-id
    - name: specialist2
      agent: specialist2-agent-id
```

**Testing:**
```bash
./bin/loom chat --thread dnd-campaign-workflow "Create a pirate campaign"
./bin/loom chat --thread vacation-planning-workflow "Plan a week in Japan"
```

**Test Results:** ✅ All passing (tested Jan 22, 2026)

---

## 2. Orchestration Patterns (YAML Format Issue Found)

These workflows use static execution patterns for structured agent coordination.

**⚠️ IMPORTANT:** The examples in `examples/reference/workflows/` use `spec.pattern` (old format), but the implementation expects `spec.type` (new format).

**Reference:** See `examples/workflow-all-fields-example.yaml` for correct format.

### Pattern 1: Pipeline

**Status:** ⚠️ Examples need updating from `pattern:` to `type:`

**Example:** `examples/reference/workflows/feature-pipeline.yaml` (uses old format)

**Description:** Sequential execution where each stage's output becomes next stage's input

**Current YAML (OLD - uses `pattern:`):**
```yaml
spec:
  pattern: pipeline  # ❌ OLD FORMAT
  agents:
    - id: stage1
      name: API Architect
      system_prompt: |
        Design APIs...
```

**Correct YAML (NEW - uses `type:`):**
```yaml
spec:
  type: pipeline  # ✅ CORRECT FORMAT
  initial_prompt: "Design auth system"
  stages:
    - agent_id: spec-writer
      prompt_template: "Write spec: {{previous}}"
    - agent_id: implementer
      prompt_template: "Implement: {{previous}}"
  pass_full_history: true
```

**Current Issue:** ❌ Examples use `spec.pattern` but implementation expects `spec.type`

---

### Pattern 2: Fork-Join

**Status:** 📋 YAML parsing implemented, CLI execution unclear

**Example:** `examples/reference/workflows/code-review.yaml`

**Description:** Agents execute in parallel on same prompt, results merged

**YAML Structure:**
```yaml
spec:
  pattern: fork_join
  agents:
    - id: quality-reviewer
      name: Code Quality Reviewer
      system_prompt: |
        Review code quality...
    - id: security-reviewer
      name: Security Reviewer
      system_prompt: |
        Review security...
  config:
    merge_strategy: concatenate
```

---

### Pattern 3: Parallel

**Status:** 📋 YAML parsing implemented, CLI execution unclear

**Examples:**
- `doc-generation.yaml`
- `security-analysis.yaml`

**Description:** Independent tasks execute in parallel with agent-specific prompts

**YAML Structure:**
```yaml
spec:
  pattern: parallel
  agents:
    - id: task1
      name: Task 1 Agent
      system_prompt: |
        Do task 1...
      prompt_template: "Task 1: {{user_query}}"
    - id: task2
      name: Task 2 Agent
      system_prompt: |
        Do task 2...
      prompt_template: "Task 2: {{user_query}}"
```

---

### Pattern 4: Debate

**Status:** 📋 YAML parsing implemented, CLI execution unclear

**Example:** `architecture-debate.yaml`

**Description:** Multiple agents debate and reach consensus through structured rounds

**YAML Structure:**
```yaml
spec:
  pattern: debate
  agents:
    - id: advocate
      name: Architect Advocate
      role: debater
      system_prompt: |
        Advocate for best practices...
    - id: pragmatist
      name: Pragmatist
      role: debater
      system_prompt: |
        Advocate for pragmatism...
    - id: moderator
      name: Senior Architect
      role: moderator
      system_prompt: |
        Moderate and synthesize...
  config:
    rounds: 3
```

---

### Pattern 5: Conditional

**Status:** 📋 YAML parsing implemented, CLI execution unclear

**Example:** `complexity-routing.yaml`

**Description:** Routes execution based on classifier agent's decision

**YAML Structure:**
```yaml
spec:
  pattern: conditional
  agents:
    - id: classifier
      name: Complexity Classifier
      role: classifier
      system_prompt: |
        Classify as: simple, medium, complex
      prompt_template: "Classify: {{user_query}}"
```

**Limitation:** Nested workflows not yet supported

---

### Pattern 6: Swarm

**Status:** 📋 YAML parsing implemented, CLI execution unclear

**Example:** `technology-swarm.yaml`

**Description:** Collective decision-making through voting

**YAML Structure:**
```yaml
spec:
  pattern: swarm
  agents:
    - id: expert1
      name: Database Expert
      system_prompt: |
        Evaluate database options...
    - id: expert2
      name: Performance Engineer
      system_prompt: |
        Analyze performance...
    - id: judge
      name: Senior Architect
      role: judge
      system_prompt: |
        Break ties and synthesize...
  config:
    strategy: majority  # Options: majority, supermajority, unanimous
    confidence_threshold: 0.7
```

---

## Summary

| Pattern | Type | Status | Example | Tested |
|---------|------|--------|---------|--------|
| Pub-Sub | Communication | ✅ Working | dungeon-crawl, brainstorm-session | ✅ Yes |
| Hub-and-Spoke | Communication | ✅ Working | dnd-campaign, vacation-planner | ✅ Yes |
| Pipeline | Orchestration | 📋 YAML Only | feature-pipeline.yaml | ❌ No |
| Fork-Join | Orchestration | 📋 YAML Only | code-review.yaml | ❌ No |
| Parallel | Orchestration | 📋 YAML Only | doc-generation.yaml | ❌ No |
| Debate | Orchestration | 📋 YAML Only | architecture-debate.yaml | ❌ No |
| Conditional | Orchestration | 📋 YAML Only | complexity-routing.yaml | ❌ No |
| Swarm | Orchestration | 📋 YAML Only | technology-swarm.yaml | ❌ No |

## Testing Status

### ✅ Tested and Working (3/8)
1. Pub-Sub pattern
2. Hub-and-Spoke pattern
3. (Both tested successfully Jan 22, 2026)

### ❌ Needs Testing/Implementation (6/8)
1. Pipeline pattern
2. Fork-Join pattern
3. Parallel pattern
4. Debate pattern
5. Conditional pattern
6. Swarm pattern

**Issue:** `looms workflow run` command returns "missing spec.type" error for orchestration patterns

## Next Steps

1. ✅ **Communication patterns** - Fully working and tested

2. ⚠️ **Orchestration patterns** - Format issue identified:
   - **Problem:** Examples use `spec.pattern` but implementation expects `spec.type`
   - **Solution:** Update all 6 orchestration pattern examples in `examples/reference/workflows/` to use correct format
   - **Template:** Use `examples/workflow-all-fields-example.yaml` as reference (lines 200-370)
   - **After fix:** Test each pattern with `looms workflow run <file>.yaml`

3. **Quick Fix Example:**
   ```yaml
   # OLD (current examples):
   spec:
     pattern: pipeline
     agents: [...]

   # NEW (correct format):
   spec:
     type: pipeline
     initial_prompt: "..."
     stages: [...]
   ```

4. **Files to Update:**
   - `examples/reference/workflows/feature-pipeline.yaml` → use `type: pipeline`
   - `examples/reference/workflows/code-review.yaml` → use `type: fork-join`
   - `examples/reference/workflows/doc-generation.yaml` → use `type: parallel`
   - `examples/reference/workflows/security-analysis.yaml` → use `type: parallel`
   - `examples/reference/workflows/architecture-debate.yaml` → use `type: debate`
   - `examples/reference/workflows/complexity-routing.yaml` → use `type: conditional`
   - `examples/reference/workflows/technology-swarm.yaml` → use `type: swarm`

## Recent Changes

**Jan 22, 2026:**
- Simplified all workflow agent prompts (50-70% reduction)
- Implemented dynamic communication injection
- All communication patterns tested and working
- Committed to `weaver-updates` branch (commit d5f3ec7)

---

**Documentation:** See `examples/reference/workflows/README.md` for full workflow pattern documentation.
