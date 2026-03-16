# Weaver Accuracy Test Plan

**Branch**: `weaver-accuracy`
**Prerequisite**: Build from branch (`just build`) to get ROM, patterns, and skill deployed
**Goal**: Verify that the weaver produces correct YAML without hallucinated tools, wrong formats, or deprecated references

---

## Prerequisites

```bash
# 1. Build fresh binaries from weaver-accuracy branch
just build

# 2. Delete old weaver + skills so server deploys the new versions
rm ~/.loom/agents/weaver.yaml
rm -rf ~/.loom/skills/

# 3. Copy updated examples to loom config directory
#    (server copies examples/ on startup via copyDir, but only from CWD)
#    Run from the loom project root so server finds the source:
cd /path/to/loom

# 4. Copy patterns to loom config directory
#    Patterns are NOT auto-deployed — must be copied manually
cp -R patterns/ ~/.loom/patterns/

# 5. Start the server from project root (separate terminal)
#    Must run from project root so examples/ source dir is found
./bin/looms serve --yolo --log-level info

# 6. Verify deployment in server logs — look for:
#    - "Weaver agent installed"
#    - "Weaver creation skill installed"
#    - "Examples copied"

# 7. Verify the new weaver has ROM and skills
grep "rom:" ~/.loom/agents/weaver.yaml        # should show: rom: "weaver"
grep "skills:" ~/.loom/agents/weaver.yaml      # should show: skills:
ls ~/.loom/skills/weaver-creation.yaml         # should exist
ls ~/.loom/patterns/weaver/                    # should show 9 .yaml files
```

---

## Test Group 1: Validator (offline, no server needed)

These tests verify the foundation — the validator catches mistakes before the LLM ever sees them.

### T1.1 — Skill kind recognized

```bash
# Should validate cleanly (no errors)
./bin/looms validate file embedded/skills/weaver-creation.yaml
```

**Expected**: Valid, kind detected as "Skill"

### T1.2 — Deprecated tools produce warnings

Create `test-fixtures/test-deprecated-tools.yaml`:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-deprecated
spec:
  system_prompt: "Test agent"
  tools:
    - shell_execute
    - search_conversation
    - recall_conversation
    - clear_recalled_context
```

```bash
./bin/looms validate file test-fixtures/test-deprecated-tools.yaml
```

**Expected**: Unknown tool errors with suggestions → "Did you mean 'conversation_memory'?"

### T1.3 — Auto-registered tools produce warnings

Create `test-fixtures/test-auto-registered.yaml`:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-autotools
spec:
  system_prompt: "Test agent"
  tools:
    - shell_execute
    - workspace
    - conversation_memory
    - get_error_details
```

```bash
./bin/looms validate file test-fixtures/test-auto-registered.yaml
```

**Expected**: Warnings that workspace, conversation_memory, get_error_details are auto-registered and should not be listed

### T1.4 — Workflow-injected tools produce warnings

Create `test-fixtures/test-workflow-tools.yaml`:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-workflow-tools
spec:
  system_prompt: "Test agent"
  tools:
    - shell_execute
    - send_message
    - publish
    - shared_memory_read
```

```bash
./bin/looms validate file test-fixtures/test-workflow-tools.yaml
```

**Expected**: Warnings that send_message, publish, shared_memory_read are workflow-injected

### T1.5 — Nested tools format rejected

Create `test-fixtures/test-nested-tools.yaml`:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: test-nested
spec:
  system_prompt: "Test agent"
  tools:
    builtin:
      - shell_execute
    mcp:
      - some-server
```

```bash
./bin/looms validate file test-fixtures/test-nested-tools.yaml
```

**Expected**: Error — "Invalid tools format", expects flat array

### T1.6 — Reference examples are clean

```bash
# Validate ALL reference agent configs — should produce zero deprecated tool errors
./bin/looms validate dir examples/reference/agents/
./bin/looms validate dir examples/reference/agent-templates/
```

**Expected**: No errors about unknown tools (deprecated tools removed in decontamination)

### T1.7 — Weaver patterns loadable

```bash
# These should parse as valid YAML (not validated via `validate file` since they're Pattern kind)
for f in patterns/weaver/*.yaml; do
  echo "--- $f ---"
  python3 -c "import yaml; yaml.safe_load(open('$f'))" && echo "OK" || echo "FAIL"
done
```

**Expected**: All 9 pattern files parse as valid YAML

---

## Test Group 2: Simple Agent Creation (server required)

Connect to weaver and ask it to create simple agents. Verify outputs are correct.

### T2.1 — Teradata explorer agent with MCP tools

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a Teradata agent that can explore and tell me about my Teradata instance. It should use tools from vantage-mcp."
```

**Verify in output YAML**:
- [ ] `apiVersion: loom/v1`
- [ ] `kind: Agent`
- [ ] `metadata.name` is kebab-case
- [ ] `spec.tools` is a flat array (NOT nested `builtin:`/`mcp:`)
- [ ] Weaver used `tool_search` to discover vantage-mcp tools (check tool call log)
- [ ] Tools include relevant vantage-mcp tools discovered via tool_search
- [ ] NO `llm:` section present (user didn't request specific provider)
- [ ] NO auto-registered tools listed (`workspace`, `get_error_details`, `conversation_memory`, `session_memory`, `query_tool_result`)
- [ ] System prompt does NOT start with "You are a..."
- [ ] System prompt is task-oriented (e.g., "Explore and describe the Teradata instance...")

**Post-check**:
```bash
./bin/looms validate file ~/.loom/agents/<created-agent>.yaml
```

### T2.2 — Agent with explicit LLM

```bash
loom chat --thread weaver --stream \
  "Quick start. Create an agent for API testing using Bedrock with Claude Sonnet."
```

**Verify**:
- [ ] `llm:` section IS present (user explicitly asked for Bedrock)
- [ ] `llm.provider: bedrock` (or similar)
- [ ] Tools include `http_request`
- [ ] Flat tool array

### T2.3 — Agent with tool_search fallback

```bash
loom chat --thread weaver --stream \
  "Quick start. Create an agent for quantum computing research."
```

**Verify**:
- [ ] Weaver uses `tool_search` to find relevant tools (check in tool call log)
- [ ] If no domain tools found, gives agent `tool_search` as a tool
- [ ] Does NOT hallucinate tool names like `quantum_simulate` or `arxiv_search`

---

## Test Group 3: Orchestration Workflows

### T3.1 — Debate workflow

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a debate workflow about whether microservices are better than monoliths. Use 3 agents."
```

**Verify**:
- [ ] Creates 3 agent YAMLs first, then workflow
- [ ] Workflow has `spec.type: debate` (NOT `spec.entrypoint`)
- [ ] Has `topic:` field with the debate topic
- [ ] Has `agent_ids:` array with 3 entries matching created agent filenames
- [ ] Has `rounds:` field
- [ ] Has `merge_strategy:` field
- [ ] NO deprecated fields (`workflow_type`, `aggregation`)

**Post-check**:
```bash
./bin/looms workflow validate ~/.loom/workflows/<created-workflow>.yaml
```

### T3.2 — Pipeline workflow

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a 3-stage pipeline for document processing: extract, transform, summarize."
```

**Verify**:
- [ ] Workflow has `spec.type: pipeline`
- [ ] Has `initial_prompt:` field
- [ ] Has `stages:` as an array (NOT a map)
- [ ] Each stage has `agent_id:` and `prompt_template:`
- [ ] `prompt_template` values contain `{{previous}}` (except optionally first stage)
- [ ] 3 agent YAMLs created before workflow

### T3.3 — Fork-join workflow

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a fork-join workflow that sends the same code review prompt to a security expert, performance expert, and maintainability expert."
```

**Verify**:
- [ ] Workflow has `spec.type: fork-join`
- [ ] Has `prompt:` field (shared prompt)
- [ ] Has `agent_ids:` array
- [ ] Has `merge_strategy:` field
- [ ] NOT confused with parallel (fork-join = same prompt; parallel = different prompts)

### T3.4 — Parallel workflow

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a parallel workflow: one agent analyzes logs, another analyzes metrics, another analyzes traces."
```

**Verify**:
- [ ] Workflow has `spec.type: parallel`
- [ ] Has `tasks:` array (NOT `agent_ids` + shared prompt)
- [ ] Each task has `agent_id:` AND unique `prompt:`
- [ ] NOT confused with fork-join

### T3.5 — Swarm voting workflow

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a swarm vote with 5 agents to decide whether to deploy the release."
```

**Verify**:
- [ ] Workflow has `spec.type: swarm`
- [ ] Has `question:` field
- [ ] Has `agent_ids:` array with 5 entries
- [ ] Has `strategy:` (majority, supermajority, etc.)
- [ ] Optional: `confidence_threshold`

---

## Test Group 4: Event-Driven Coordination Workflows

### T4.1 — Hub-and-spoke coordination

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a multi-agent system where a project coordinator delegates research tasks to 3 specialist agents."
```

**Verify**:
- [ ] Creates agent YAMLs FIRST, then workflow
- [ ] Workflow has `spec.entrypoint:` (NOT `spec.type`)
- [ ] Has `spec.agents:` array with name+agent pairs
- [ ] Has `communication.pattern: hub-and-spoke`
- [ ] Has `communication.hub:` pointing to coordinator
- [ ] Agent configs do NOT list `send_message`, `publish`, `shared_memory_read/write` in `spec.tools`
- [ ] Agent system prompts mention `{workflow-name}:{agent-name}` addressing format
- [ ] NO `receive_message`, `receive_broadcast`, or `subscribe` tools anywhere

### T4.2 — Peer-to-peer pub-sub coordination

```bash
loom chat --thread weaver --stream \
  "Quick start. Create a brainstorming workflow where 4 agents freely discuss and build on each other's ideas."
```

**Verify**:
- [ ] Workflow has `spec.entrypoint:`
- [ ] Has `communication.pattern: peer-to-peer-pub-sub`
- [ ] Has `communication.topic:` field
- [ ] Agent configs do NOT list communication tools
- [ ] System prompts mention `publish` for broadcasting (not `receive_broadcast`)
- [ ] NO `manage_ephemeral_agents` references

---

## Test Group 5: Skill Creation

### T5.1 — Weaver recommends skill

```bash
loom chat --thread weaver --stream \
  "/agent-plan. I want to create a SQL optimization agent for PostgreSQL."
```

**Verify**:
- [ ] Weaver asks user before creating any skill (never auto-creates)
- [ ] Recommends relevant skill (e.g., sql-optimization)
- [ ] Explains what the skill does, activation mode, and benefits
- [ ] Waits for user confirmation before proceeding

### T5.2 — Skill YAML is valid

After approving skill creation in T5.1:

**Verify**:
- [ ] Created file has `kind: Skill`
- [ ] Has `apiVersion: loom/v1`
- [ ] `metadata.name` is kebab-case
- [ ] `metadata.domain` is valid (sql, code, data, etc.)
- [ ] `prompt.instructions` is non-empty
- [ ] File validates: `./bin/looms validate file ~/.loom/skills/<skill>.yaml`

---

## Test Group 6: Negative Tests (What Should NOT Happen)

### T6.1 — No hallucinated tools

Across ALL outputs from T2-T5, verify NONE of these appear:
- [ ] `generate_agent` (was in deleted prompts/metaagent/weaver.yaml)
- [ ] `list_agents` (same)
- [ ] `get_agent` (same)
- [ ] `search_conversation` (deprecated)
- [ ] `recall_conversation` (deprecated)
- [ ] `clear_recalled_context` (deprecated)
- [ ] `receive_message` (doesn't exist as a tool)
- [ ] `receive_broadcast` (doesn't exist)
- [ ] `subscribe` (doesn't exist as a tool)
- [ ] `manage_ephemeral_agents` (not needed for coordination)

### T6.2 — No nested tools format

Across ALL agent YAMLs created in T2-T5:
- [ ] No `tools: { builtin: [...] }` format
- [ ] No `tools: { mcp: [...] }` format
- [ ] Only `tools: [tool1, tool2]` flat array format

### T6.3 — No role prompting

Across ALL agent YAMLs created in T2-T5:
- [ ] No system prompts starting with "You are a..." or "As a..."
- [ ] Direct, task-oriented instructions

### T6.4 — Correct action names

Observe the weaver's tool calls during agent/workflow creation:
- [ ] Uses `action="create_agent"` (NOT `action="create"` with `type="agent"`)
- [ ] Uses `action="create_workflow"` (NOT `action="create"` with `type="workflow"`)

---

## Test Group 7: ROM Verification

### T7.1 — ROM is loaded

```bash
# In server logs when weaver thread starts, look for ROM loading:
# Should see "Composed ROM" or domain ROM loading for weaver
grep -i "rom\|ROM\|weaver" <server-log-output>
```

### T7.2 — ROM content in context

During any T2-T5 test, if the weaver produces correct output, that's indirect evidence the ROM is working. For direct verification:

```bash
# Ask the weaver about its knowledge
loom chat --thread weaver --stream \
  "What tool categories exist? List configurable, auto-registered, and workflow-injected tools."
```

**Expected**: Response should match the ROM's Section 2 tool matrix — listing the correct tools in each category without hallucination.

---

## Test Group 8: Pattern Injection

### T8.1 — Debate pattern injected

In server logs during T3.1, look for pattern injection:
```bash
grep -i "pattern.*inject\|pattern.*match\|debate_workflow" <server-log-output>
```

**Expected**: `debate_workflow` pattern matched and injected into context

### T8.2 — Pipeline pattern injected

In server logs during T3.2:
```bash
grep -i "pattern.*inject\|pipeline_workflow" <server-log-output>
```

### T8.3 — Hub-spoke pattern injected

In server logs during T4.1:
```bash
grep -i "pattern.*inject\|hub_spoke" <server-log-output>
```

---

## Test Group 9: End-to-End Workflow Execution

After weaver creates agents + workflow, actually run them.

### T9.1 — Execute created debate workflow

```bash
# From T3.1 output
./bin/looms workflow run ~/.loom/workflows/<debate-workflow>.yaml
```

**Expected**: Executes without crash. Agents load, debate rounds execute, merge produces output.

### T9.2 — Execute created pipeline workflow

```bash
./bin/looms workflow run ~/.loom/workflows/<pipeline-workflow>.yaml
```

**Expected**: Pipeline stages execute sequentially. `{{previous}}` interpolation works.

---

## Test Group 10: Regression — Decontamination

### T10.1 — No deprecated tools in reference examples

```bash
grep -r "search_conversation\|recall_conversation\|clear_recalled_context" \
  examples/reference/agents/ \
  examples/reference/agent-templates/ \
  examples/reference/workflows/
```

**Expected**: Zero matches

### T10.2 — No receive_message/receive_broadcast as tools in examples

```bash
grep -r "receive_message\|receive_broadcast" \
  examples/reference/agents/ \
  examples/reference/agent-templates/ \
  examples/reference/workflows/ \
  | grep -v "NOT\|not\|never\|auto-deliver\|don't\|do not\|No.*tool"
```

**Expected**: Only matches are warnings AGAINST using these (not endorsing them)

### T10.3 — Patterns use conversation_memory

```bash
grep -r "recall_conversation" patterns/
```

**Expected**: Zero matches (all replaced with `conversation_memory`)

---

## Scoring

| Group | Tests | Weight | Pass Criteria |
|-------|-------|--------|---------------|
| T1: Validator | 7 | 15% | All 7 pass |
| T2: Simple Agent | 3 | 10% | All 3 correct YAML |
| T3: Orchestration | 5 | 20% | All 5 correct workflow types |
| T4: Coordination | 2 | 15% | Both correct (entrypoint, no receive tools) |
| T5: Skills | 2 | 5% | Skill created, validates |
| T6: Negative | 4 | 15% | Zero hallucinations |
| T7: ROM | 2 | 5% | ROM loaded, correct knowledge |
| T8: Patterns | 3 | 5% | Patterns injected per intent |
| T9: E2E | 2 | 5% | Workflows actually execute |
| T10: Regression | 3 | 5% | Zero contamination |

**Ship criteria**: T1 (100%), T6 (100%), T10 (100%), and T2-T5 (>80% — LLM output may vary slightly)
