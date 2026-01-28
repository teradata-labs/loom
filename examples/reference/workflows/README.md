# Loom Workflow Examples

This directory contains workflow examples demonstrating the two main approaches to multi-agent coordination in Loom.

## Directory Structure

### [orchestration-patterns/](orchestration-patterns/)
Structured workflows using predefined coordination patterns.

**Examples:**
- **debate** - Structured debate with rounds and moderator (`architecture-debate.yaml`)
- **pipeline** - Sequential processing stages (`feature-pipeline.yaml`)
- **parallel** - Independent parallel tasks (`doc-generation.yaml`, `security-analysis.yaml`)
- **swarm** - Collective voting and consensus (`technology-swarm.yaml`)
- **conditional** - Dynamic routing based on classification (`complexity-routing.yaml`)
- **fork-join** - Parallel execution with merge (see test-data)
- **iterative** - Self-correcting pipelines with restarts (see workflow-all-fields-reference.yaml)

**When to use:** Structured collaboration scenarios where workflow follows a clear pattern.

---

### [event-driven/](event-driven/)
Dynamic workflows where agents coordinate autonomously via messaging.

**Examples:**
- **dnd-campaign-builder/** - Hub-and-spoke pattern with coordinator
- **dungeon-crawler/** - Peer-to-peer pub-sub for party chat
- **brainstorm-session/** - Peer-to-peer pub-sub for collaboration

**When to use:** Conversational, dynamic interactions where agents decide how to communicate.

## Two Approaches to Multi-Agent Workflows

### 1. Orchestration Patterns

Use predefined patterns with `spec.type:` field.

```yaml
apiVersion: loom/v1
kind: Workflow
spec:
  type: pipeline  # or debate, parallel, swarm, etc.
  stages:
    - agent_id: architect
      prompt_template: "Design API: {{previous}}"
    - agent_id: developer
      prompt_template: "Implement: {{previous}}"
```

**Characteristics:**
- Framework coordinates agent interactions
- Predefined execution patterns
- Guaranteed execution order (for sequential patterns)
- Results merged according to pattern strategy

**Use cases:**
- Feature implementation pipelines
- Multi-perspective analysis
- Consensus-based decisions
- Structured reviews

---

### 2. Event-Driven Workflows

Use `spec.entrypoint` + `spec.agents` structure (NO `type:` field).

```yaml
apiVersion: loom/v1
kind: Workflow
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      agent: coordinator-id
    - name: specialist
      agent: specialist-id
  communication:
    pattern: "hub-and-spoke"  # Documentation only
```

**Characteristics:**
- Agents coordinate via message queue, pub-sub, or shared memory
- Dynamic, conversational interactions
- Agents decide when and how to communicate
- Flexible, emergent behavior

**Use cases:**
- Multi-agent applications
- Conversational workflows
- Gaming and simulations
- Autonomous agent coordination

## Quick Comparison

| Feature | Orchestration Patterns | Event-Driven Workflows |
|---------|----------------------|------------------------|
| **Structure** | Predefined patterns | Dynamic, agent-driven |
| **YAML Field** | `spec.type:` | `spec.entrypoint` + `agents` |
| **Coordination** | Framework-controlled | Agents coordinate via tools |
| **Communication** | Pattern-specific | Tri-modal (queue/pub-sub/memory) |
| **Flexibility** | Structured, predictable | Flexible, conversational |
| **Execution Order** | Defined by pattern | Emerges from interactions |
| **Best For** | Structured tasks | Dynamic conversations |

## YAML Structure

### Orchestration Pattern Workflow

```yaml
apiVersion: loom/v1      # Always loom/v1
kind: Workflow           # Always Workflow
metadata:
  name: my-workflow      # Required
  description: "..."     # Required

spec:
  type: pipeline         # Required: Pattern type
  # Pattern-specific fields (stages, tasks, rounds, etc.)

  config:                # Optional
    timeout_seconds: 300

  orchestration:         # Optional
    pass_full_history: false
```

**Valid types:**
- `debate` - Structured debate with rounds
- `fork-join` - Parallel execution with merge
- `pipeline` - Sequential stages
- `parallel` - Independent parallel tasks
- `conditional` - Dynamic routing
- `iterative` - Self-correcting pipeline
- `swarm` - Collective voting

---

### Event-Driven Workflow

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: my-workflow
  description: "..."

spec:
  entrypoint: coordinator  # Required: Starting agent

  agents:                  # Required: Agent definitions
    - name: coordinator
      agent: coordinator-agent-id
      description: "..."

  communication:           # Optional: Documentation
    pattern: "hub-and-spoke"  # or "peer-to-peer-pub-sub"
    hub: coordinator       # for hub-and-spoke
    # topic: "chat"        # for pub-sub

  config:                  # Optional
    timeout_seconds: 600
    max_workflow_turns: 200
```

**Communication patterns** (documentation only):
- `hub-and-spoke` - Coordinator manages specialists
- `peer-to-peer-pub-sub` - All agents are equal peers

## Agent Specification

Both workflow types support multiple ways to specify agents:

### 1. By ID (Registry Reference)
```yaml
agents:
  - id: existing-agent-id
    prompt_template: "Analyze: {{user_query}}"
```

### 2. By Path (Load from File)
```yaml
agents:
  - id: my-agent
    path: ../agents/code_reviewer.yaml
```

### 3. Inline Definition
```yaml
agents:
  - id: my-agent
    name: My Agent
    system_prompt: |
      You are a specialized agent...
    tools: [tool_1, tool_2]
```

## Variable Interpolation

Both workflow types support variable interpolation:

- `{{user_query}}` - Initial prompt provided at runtime
- `{{previous}}` - Output from previous stage (pipeline pattern)
- `{{history}}` - Full execution history (if enabled)
- Custom variables via ExecuteWorkflow RPC

## Getting Started

### 1. Explore Orchestration Patterns
```bash
cd orchestration-patterns/
cat README.md
loom workflow run feature-pipeline.yaml --prompt "Implement user auth"
```

### 2. Explore Event-Driven Workflows
```bash
cd event-driven/
cat README.md

# Run examples
cd dnd-campaign-builder/
loom workflow run workflows/dnd-campaign-workflow.yaml
```

### 3. Choose Your Approach

**Start with Orchestration Patterns if:**
- You have a clear, structured workflow
- You want guaranteed execution order
- You need standardized collaboration patterns

**Start with Event-Driven if:**
- Agents need conversational interactions
- Workflow is dynamic and unpredictable
- Building multi-agent applications
- Agents should coordinate autonomously

## Testing

Workflow validation tests are located in the project tests directory:

```bash
# Run workflow validation tests
cd tests/workflows
go test -v -tags fts5 ./...
```

See `tests/workflows/README.md` for more information about test data and test structure.

## Further Reading

- **Orchestration Patterns:** See `orchestration-patterns/README.md`
- **Event-Driven Workflows:** See `event-driven/README.md`
- **Proto Definitions:** See `proto/loom/v1/orchestration.proto`
- **Example Applications:**
  - `event-driven/dnd-campaign-builder/` (hub-and-spoke)
  - `event-driven/dungeon-crawler/` (peer-to-peer pub-sub)
  - `event-driven/brainstorm-session/` (peer-to-peer pub-sub)
