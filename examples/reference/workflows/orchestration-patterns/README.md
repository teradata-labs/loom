# Orchestration Pattern Workflows

Orchestration patterns use predefined workflow structures to coordinate multiple agents. These patterns use the `type:` field to specify the orchestration strategy.

## What Are Orchestration Patterns?

Orchestration patterns define **how** multiple agents collaborate on a task using structured coordination strategies. Each pattern is optimized for specific collaboration scenarios.

### Key Characteristics:
- Use `spec.type:` field to specify pattern type
- Predefined coordination logic
- Agent interactions are orchestrated by the framework
- Results are merged according to the pattern's strategy

## Available Patterns

### 1. **debate** - Structured Debate
**File:** `architecture-debate.yaml`

Multiple agents debate a topic through structured rounds, with optional moderator. The moderator writes the synthesis; without one the result is a one-line confidence summary of the last round. A `join` block bounds the moderator's synthesis prompt and each agent's round 2+ response prompt (full positions go to SharedMemory, inline copies are capped, the prompt is held to the receiving agent's token budget). `merge_strategy` is not a debate field and is rejected.

```yaml
spec:
  type: debate
  topic: "Should we use microservices?"
  agent_ids: [advocate, pragmatist]
  moderator_agent_id: senior-architect
  rounds: 3
```

**Use Cases:**
- Architecture decisions
- Design reviews
- Policy discussions

---

### 2. **pipeline** - Sequential Processing
**Files:** `feature-pipeline.yaml`, `pipeline-history-report.yaml`

Agents execute sequentially, where each stage's output becomes the next stage's input.

```yaml
spec:
  type: pipeline
  initial_prompt: "Design authentication system"
  stages:
    - agent_id: architect
      prompt_template: "Design API: {{previous}}"
    - agent_id: developer
      prompt_template: "Implement: {{previous}}"
```

**Use Cases:**
- Feature implementation (design → code → test)
- Content refinement (draft → edit → review)
- Sequential transformations

A final stage that compiles everything reads `{{history}}`. Each entry is the same 8 KiB copy the next stage gets as `{{previous}}`, with a `stage-N-output` SharedMemory pointer for the rest. Add a `history` block to hold the whole prompt to the stage agent's token budget (`pipeline-history-report.yaml`):

```yaml
spec:
  type: pipeline
  initial_prompt: "Prepare the quarterly review"
  stages:
    - agent_id: finance-analyst
      prompt_template: "Analyze finances: {{previous}}"
    - agent_id: report-writer
      prompt_template: "Write the report from: {{history}}"
  history:
    max_entry_bytes: 6144
    max_input_tokens: 48000
    overflow_strategy: pre_reduce   # pre_reduce | truncate | fail
```

---

### 3. **fork-join** - Parallel Analysis with Merge
**File:** `fork-join-presentation.yaml`

All agents execute in parallel on the same prompt, results are merged.

```yaml
spec:
  type: fork-join
  prompt: "Review this code"
  agent_ids: [quality-reviewer, security-reviewer]
  merge_strategy: concatenate
```

When a final agent has to build something from every branch (a deck, a report), add a `join` block. The join then runs as that agent with bounded input: full branch outputs go to SharedMemory, the inline copies are capped, and the whole prompt is held to a token budget. See `docs/reference/workflow-join.md`.

```yaml
spec:
  type: fork-join
  prompt: "Analyze Q3 results"
  agent_ids: [finance-analyst, sales-analyst, ops-analyst]
  join:
    agent_id: presenter
    prompt_template: "Build a deck for: {{prompt}}\n\n{{branches}}"
    max_branch_bytes: 4096
    max_input_tokens: 40000
    overflow_strategy: pre_reduce
```

**Use Cases:**
- Multi-perspective code reviews
- Parallel analysis (quality, security, performance)
- Parallel analyses joined into one deliverable by a dedicated agent

---

### 4. **parallel** - Independent Parallel Tasks
**Files:** `doc-generation.yaml`, `security-analysis.yaml`

Multiple independent tasks execute in parallel with agent-specific prompts.

```yaml
spec:
  type: parallel
  tasks:
    - agent_id: api-documenter
      prompt: "Generate API docs"
    - agent_id: technical-writer
      prompt: "Write user guide"
  merge_strategy: concatenate
```

**Use Cases:**
- Documentation generation (API docs, guides, examples)
- Security analysis (SAST, DAST, threat modeling)
- Independent parallel tasks

---

### 5. **conditional** - Dynamic Routing
**File:** `complexity-routing.yaml`

Routes tasks based on classification or conditions. The condition agent's
answer selects a branch by key; each branch is a full nested workflow pattern.

```yaml
spec:
  type: conditional
  condition_agent_id: complexity-classifier
  condition_prompt: 'Respond "simple" or "complex": {{input}}'
  branches:
    simple:
      type: pipeline
      # ... nested pipeline spec
    complex:
      type: pipeline
      # ... nested pipeline spec
  default_branch:
    type: fork-join
    # ... used when no branch key matches
```

**Use Cases:**
- Task routing based on complexity
- Dynamic workflow selection
- Classification-based decisions

---

### 6. **swarm** - Collective Voting
**File:** `technology-swarm.yaml`

Multiple agents vote on decisions, reaching consensus through voting strategies.

```yaml
spec:
  type: swarm
  agent_ids: [expert1, expert2, expert3]
  strategy: majority  # or supermajority, unanimous
  confidence_threshold: 0.7
  share_votes: false
```

**Use Cases:**
- Technology selection decisions
- Architecture choices
- Consensus-based decisions

---

### 7. **iterative** - Self-Correcting Pipeline
**File:** (See ../workflow-all-fields-reference.yaml)

Pipeline with autonomous restart capabilities - agents can trigger restarts of earlier stages.

```yaml
spec:
  type: iterative
  pipeline:
    initial_prompt: "Process data"
    stages: [...]
  max_iterations: 3
  restart_policy:
    enabled: true
```

**Use Cases:**
- Self-correcting workflows
- Adaptive processing
- Workflows needing validation and retry

## Usage

### Running a Workflow

```bash
# Execute workflow with runtime prompt
loom workflow run orchestration-patterns/feature-pipeline.yaml \
  --prompt "Implement user registration feature"

# Or using the server
looms workflow execute \
  --file orchestration-patterns/architecture-debate.yaml \
  --prompt "Should we use GraphQL or REST?"
```

### Agent Requirements

Workflows reference agent IDs that must exist in your agent registry (`$LOOM_DATA_DIR/agents/` or `$LOOM_DATA_DIR/agents/`).

Each workflow file includes comments describing the expected agent capabilities.

## Comparison with Event-Driven Workflows

| Feature | Orchestration Patterns | Event-Driven Workflows |
|---------|----------------------|------------------------|
| Structure | Predefined patterns | Custom agent interactions |
| Coordination | Framework-controlled | Agent-driven via messages |
| Field | `spec.type:` | `spec.entrypoint` + `agents` |
| Communication | Pattern-specific | Message queue / pub-sub / shared memory |
| Use Case | Structured collaboration | Dynamic, conversational workflows |

**See also:** `../event-driven/` for examples of event-driven multi-agent workflows.

## Pattern Selection Guide

- **Need consensus?** → Use `debate` or `swarm`
- **Sequential processing?** → Use `pipeline`
- **Multiple perspectives on same input?** → Use `fork-join`
- **Independent parallel tasks?** → Use `parallel`
- **Dynamic routing?** → Use `conditional`
- **Self-correction needed?** → Use `iterative`

## Testing

Workflow validation tests are located in the project tests directory:

```bash
cd tests/workflows
go test -v -tags fts5 ./...
```

See `tests/workflows/README.md` for test data and examples.
