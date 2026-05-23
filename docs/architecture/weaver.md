# Weaver Architecture

**Version**: v1.2.0
**Status**: ✅ Implemented (standard agent with structured JSON API)
**Last Updated**: 2026-03-28

## Table of Contents

- [Overview](#overview)
- [Design Philosophy](#design-philosophy)
- [System Context](#system-context)
- [Architecture](#architecture)
- [How It Works](#how-it-works)
- [Agent Management Tool](#agent-management-tool)
- [Agent Configuration](#agent-configuration)
- [Design Rationale](#design-rationale)
- [Performance Characteristics](#performance-characteristics)
- [Future Considerations](#future-considerations)


## Overview

The Weaver is Loom's **agent generation system** that creates complete agent and workflow configurations from natural language requirements. Unlike traditional template-based approaches or complex multi-stage pipelines, the Weaver is **just a regular Loom agent** with specialized prompting and tools that enable it to discover, generate, and validate agent configurations.

**Key Capabilities**:
- **Natural Language Generation**: Creates k8s-style agent, workflow, and skill YAMLs from descriptions
- **Structured JSON API**: Uses JSONSchema-validated structured inputs to prevent configuration errors
- **Multi-Layer Validation**: JSONSchema (call-time), semantic (execute-time), YAML (write-time) validation
- **Field Validation**: Validates workflow agent references use `agent_id` field (not `role`)
- **Self-Discovery**: Uses `tool_search` to find relevant examples and tools
- **Hot-Reload Integration**: Saves to `$LOOM_DATA_DIR/agents/`, `$LOOM_DATA_DIR/workflows/`, and `$LOOM_DATA_DIR/skills/` for immediate availability
- **Automatic Namespacing**: Workflow-scoped agents use `<workflow-name>:<role>` pattern to prevent conflicts

**Design Philosophy**: **Minimalism over complexity**. The weaver is a standard agent using standard RPCs (`Weave`/`StreamWeave`). No special services, no conflict resolution system, no multi-stage pipeline.


## Design Philosophy

### Why a Standard Agent?

**Previous Approach (v0.x)**: Complex coordinator with 6-stage pipeline, conflict detection, specialized sub-agents, and dedicated RPCs.

**Current Approach (v1.0+)**: Regular Loom agent with specialized tools.

**Rationale**:
1. **Less is More**: One agent using standard infrastructure vs. custom coordinator
2. **Works Better**: LLMs are good at following examples; let them do their job
3. **Easier to Maintain**: Standard agent configuration vs. complex pipeline code
4. **No Special Cases**: Uses `Weave`/`StreamWeave` like every other agent
5. **Validation Built-in**: `agent_management` tool validates YAML before writing

### Core Principle

> "Give the weaver agent good examples, clear instructions, and specialized tools. Let the LLM do what it's good at."


## System Context

```
┌─────────────────────────────────────────────────────────────────┐
│                         User Interface                          │
│                                                                 │
│    TUI Client: "Create a SQL optimizer agent"                   │
│                                                                 │
└──────────────────────────────────┬──────────────────────────────┘
                                   │
                                   │ Weave RPC
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                       Loom Server                               │
│                                                                 │
│  Standard Weave/StreamWeave RPCs                                │
│  (No special Weaver service)                                    │
│                                                                 │
└──────────────────────────────────┬──────────────────────────────┘
                                   │
                                   │ Execute agent
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Weaver Agent                                │
│                  (embedded/weaver.yaml)                         │
│                                                                 │
│  System Prompt:                                                 │
│    "Create agent and workflow YAML configurations from          │
│     user requirements."                                        │
│                                                                 │
│  Tools:                                                         │
│    - agent_management (validate & write YAML)                   │
│    - shell_execute (read reference examples)                    │
│    - tool_search (discover available tools for agents)          │
│                                                                 │
│  Key Instructions:                                              │
│    - Use agent_management for all YAML operations               │
│    - Tool validates YAML before writing                         │
│    - Automatic placement in $LOOM_DATA_DIR/{agents,workflows,skills}/  │
│    - Give agents standard toolset + tool_search                │
│    - Enable memory compression with appropriate workload        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
                                   │
                                   │ agent_management tool
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────┐
│              Agent Management Tool (Restricted)                 │
│                                                                 │
│  Security Check:                                                │
│    if agentID != "weaver" && agentID != "guide"                │
│      → return UNAUTHORIZED                                      │
│    (guide agent has read-only access)                           │
│                                                                 │
│  Validation Pipeline:                                           │
│    1. Parse YAML syntax                                         │
│    2. Validate schema structure                                 │
│    3. Check semantic consistency                                │
│    4. Return actionable error messages                          │
│                                                                 │
│  File Operations:                                               │
│    - Create: Check exists → validate → write                    │
│    - Update: Check exists → validate → overwrite                │
│    - Read: Direct file read                                     │
│    - List: Directory scan                                       │
│    - Delete: Remove file                                        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
                                   │
          ┌────────────────────────┼────────────────────┐
          │                        │                    │
          ▼                        ▼                    ▼
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ $LOOM_DATA_DIR/  │  │ $LOOM_DATA_DIR/  │  │ $LOOM_DATA_DIR/  │
│ agents/          │  │ workflows/       │  │ skills/          │
│  sql-agent.yaml  │  │  debate-flow.yaml│  │  sql-opt.yaml    │
│  file-agent.yaml │  │  pipeline.yaml   │  │  code-review.yaml│
└──────────────────┘  └──────────────────┘  └──────────────────┘
          │                        │                    │
          └────────────────────────┼────────────────────┘
                                   │
                                   │ Hot-reload / Immediately available
                                   │
                                   ▼
                          ┌─────────────────┐
                          │   User can run  │
                          │  generated agent│
                          │   via Weave RPC │
                          └─────────────────┘
```


## Architecture

### Component: Weaver Agent

**Location**: `embedded/weaver.yaml.tmpl`

**Type**: Standard Loom agent (no special service)

**Configuration** (key fields shown; see `embedded/weaver.yaml.tmpl` for full config):
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: weaver
  version: "1.0.0"
  description: Meta-agent that generates k8s-style agent and workflow configurations from natural language
  labels:
    domain: meta-agent
    category: agent-generation

spec:
  rom: "weaver"

  system_prompt: |
    Create agent and workflow YAML configurations from user requirements.

    ## Tools
    - agent_management: create/update/read/list/validate agent/workflow/skill YAMLs
    - shell_execute: view reference files in $LOOM_DATA_DIR/examples/reference
    - tool_search: discover available tools for agents

    ## Skill Creation
    - Use agent_management with action="create_skill" / action="update_skill"
    - Skill structure: apiVersion: loom/v1, kind: Skill, metadata (name, domain), prompt (instructions)
    - Activation modes: MANUAL (/command), AUTO (keyword-based), HYBRID (both), ALWAYS (every turn)
    ...

  tools:
    - agent_management         # Specialized tool for agent/workflow/skill YAML management
    - shell_execute            # Fallback for reading reference examples
    - tool_search              # Discover available tools for agents

  memory:
    type: sqlite
    max_history: 1000
    memory_compression:
      workload_profile: balanced

  config:
    max_turns: 1000
    max_tool_executions: 50
    enable_self_correction: true
    max_context_tokens: 200000
    reserved_output_tokens: 20000
    skills:
      enabled: true
    patterns:
      enabled: true
      use_llm_classifier: true
```

### Component: Agent Management Tool

**Location**: `pkg/shuttle/builtin/agent_management.go`

**Type**: Restricted tool (weaver and guide agent access; guide is read-only)

**Security Model**:
```go
// SECURITY: Restrict this tool to weaver and guide agents only
agentID := session.AgentIDFromContext(ctx)
if agentID != "weaver" && agentID != "guide" {
    return &shuttle.Result{
        Success: false,
        Error: &shuttle.Error{
            Code:    "UNAUTHORIZED",
            Message: "This tool is restricted to the weaver and guide meta-agents only",
        },
    }, nil
}
```

**Actions Supported** (v1.0+):

**Structured Actions** (JSONSchema-validated):
- `create_agent`: Create new agent with structured JSON config
- `create_workflow`: Create new workflow with structured JSON config
- `create_skill`: Create new skill with structured JSON config
- `update_agent`: Update existing agent with structured JSON config
- `update_workflow`: Update existing workflow with structured JSON config
- `update_skill`: Update existing skill with structured JSON config

**Legacy Actions** (require `type` parameter: `agent`, `workflow`, or `skill`):
- `read`: Read existing configuration
- `list`: List all agents, workflows, or skills
- `validate`: Validate YAML content
- `delete`: Remove configuration

**Unsupported Actions** (return `INVALID_ACTION`):
- `create` (with type/name/content) -- use `create_agent`, `create_workflow`, or `create_skill`
- `update` (with type/name/content) -- use `update_agent`, `update_workflow`, or `update_skill`

### No Special Infrastructure

**What the Weaver DOESN'T have**:
- ❌ Dedicated gRPC service (uses standard `Weave`/`StreamWeave`)
- ❌ Conflict resolution system (good prompting eliminates conflicts)
- ❌ Multi-stage pipeline (LLM does it in one pass)
- ❌ Specialized sub-agents (one agent with good prompts)
- ❌ Custom coordinator (uses standard agent runtime)

**What the Weaver DOES have**:
- ✅ Clear system prompt with instructions
- ✅ Specialized `agent_management` tool with validation
- ✅ Security restriction (weaver and guide access; guide is read-only)
- ✅ Memory management for context
- ✅ Self-correction capabilities


## How It Works

### Generation Flow

```
User Request                           Weaver Agent Processing
     │                                         │
     │ "Create a SQL optimizer agent"          │
     │                                         │
     ▼                                         ▼
┌─────────────┐                      ┌─────────────────────────┐
│ Weave RPC   │─────────────────────▶│ 1. Parse requirements  │
└─────────────┘                      └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 2. Generate agent YAML  │
                                     │    based on patterns    │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 3. Call agent_management│
                                     │  action=create_agent    │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 4. Tool validates YAML  │
                                     │    (3-level validation) │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 5. If valid, write to   │
                                     │    $LOOM_DATA_DIR/agents/      │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 6. Hot-reload picks up  │
                                     │    new agent            │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 7. Return success msg   │
                                     │    to user              │
                                     └─────────────────────────┘
```

### Validation Flow

```
YAML Content                          Validation Pipeline
     │                                         │
     │                                         │
     ▼                                         ▼
┌─────────────┐                      ┌─────────────────────────┐
│ Parse YAML  │─────────────────────▶│ 1. SYNTAX Validation   │
└─────────────┘                      │    - Valid YAML syntax │
                                     │    - Parseable structure│
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 2. STRUCTURE Validation │
                                     │    - Required fields    │
                                     │    - Field types        │
                                     │    - Schema compliance  │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 3. SEMANTIC Validation  │
                                     │    - Tool references    │
                                     │    - Memory config      │
                                     │    - Logical consistency│
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 4. Format Result        │
                                     │    - Group by level     │
                                     │    - Actionable fixes   │
                                     │    - LLM-friendly       │
                                     └─────────────────────────┘
```


## Structured JSON API (v1.0+)

### Problem: Field Name Validation

**Root Cause**: LLMs generated workflows with incorrect field names:
```yaml
# ❌ WRONG - Invalid Field
spec:
  stages:
    - role: coordinator      # Invalid field!
      prompt_template: "..."
```

Should have been:
```yaml
# ✅ CORRECT
spec:
  stages:
    - agent_id: coordinator  # Correct field
      prompt_template: "..."
```

**Impact**: Workflows failed at runtime because `role` field doesn't exist in schema. Error discovered after generation, requiring manual fixes.

### Solution: Structured JSON with JSONSchema Validation

**Design Principle**: Validate structure BEFORE execution, not after.

**Implementation**:
```
User Request → Weaver generates JSON → JSONSchema validates → Tool executes → YAML written
                                              ↑
                                         Catch errors HERE
                                         (not after file write)
```

**Example Structured Call**:
```json
{
  "action": "create_workflow",
  "config": {
    "metadata": {"name": "my-workflow"},
    "spec": {
      "type": "pipeline",
      "stages": [
        {
          "agent_id": "coordinator",  // JSONSchema enforces this field
          "prompt_template": "..."
        }
      ]
    }
  }
}
```

If LLM tries to use `role` instead of `agent_id`, JSONSchema validation fails BEFORE tool execution with clear error:
```json
{
  "error": {
    "code": "INVALID_FIELD",
    "message": "stage 0 has invalid field 'role'...",
    "suggestion": "Replace 'role' with 'agent_id'..."
  }
}
```

### Validation Layers

**Layer 1: JSONSchema (Call-Time)**
- Validates structure and types BEFORE tool execution
- Catches wrong field names (`role` vs `agent_id`)
- Enforces required fields
- Prevents unknown properties

**Layer 2: Semantic (Execute-Time)**
- Validates agent references exist as files
- Checks tool names against registry
- Verifies workflow type validity
- Field validation check (rejects any `role` field in workflows)

**Layer 3: YAML (Write-Time)**
- Ensures final YAML is syntactically correct
- Validates proto field constraints
- Confirms K8s-style structure

### Benefits

1. **Errors caught earlier**: Before execution, not after file write
2. **Clear error messages**: Field-specific with suggestions
3. **LLM-friendly**: Structured format matches LLM training
4. **Type safety**: JSON types map to YAML types
5. **Invalid fields prevented**: Schema doesn't define `role` field, only `agent_id`


## Agent Management Tool

### Design Goals

1. **Security**: Restrict to weaver and guide agents only (guide is read-only; prevent arbitrary YAML writes)
2. **Validation**: Catch errors before writing files (multi-layer validation)
3. **Structured API**: JSONSchema-validated inputs prevent configuration errors
4. **Minimal interface**: Standard tool interface, no special protocols
5. **Clarity**: LLM-friendly error messages with actionable fixes
6. **Atomicity**: Either write valid YAML or fail completely

### Implementation Details

**Tool Registration**: `pkg/shuttle/builtin/registry.go`
```go
func All(promptRegistry prompts.PromptRegistry) []shuttle.Tool {
    tools := []shuttle.Tool{
        // ... other tools ...
        NewAgentManagementTool(),
        // ... other tools ...
    }
    // ...
}
```

**Security Check**: `Execute()` in `agent_management.go` (line ~101)
```go
// SECURITY: Restrict this tool to weaver and guide agents only
agentID := session.AgentIDFromContext(ctx)
if agentID != "weaver" && agentID != "guide" {
    return &shuttle.Result{
        Success: false,
        Error: &shuttle.Error{
            Code:    "UNAUTHORIZED",
            Message: "This tool is restricted to the weaver and guide meta-agents only",
        },
    }, nil
}
```

**Validation Integration**: `writeAgentFile()` in `agent_management.go` (line ~745)
```go
// Validate YAML content
validationResult := validation.ValidateYAMLContent(yamlContent, "")

if !validationResult.Valid {
    return &shuttle.Result{
        Success: false,
        Error: &shuttle.Error{
            Code:    "VALIDATION_ERROR",
            Message: "YAML validation failed",
        },
        Data: map[string]interface{}{
            "validation": validationResult.FormatForWeaver(),
            "errors":     validationResult.Errors,
            "warnings":   validationResult.Warnings,
        },
    }, nil
}
```

**Directory Management**: `writeAgentFile()` in `agent_management.go` (line ~763)
```go
// Determine target directory
dir := config.GetLoomSubDir("agents")

// Ensure directory exists
// nosec G301 - Directory permissions 0750 (owner: rwx, group: r-x, other: none)
if err := os.MkdirAll(dir, 0750); err != nil {
    // ... error handling ...
}
```

### Validation Levels

The tool uses three-level validation from `pkg/validation/`:

1. **SYNTAX Level** (`LevelSyntax`)
   - YAML parsing errors
   - Malformed structure
   - Example: "yaml: line 10: found character that cannot start any token"

2. **STRUCTURE Level** (`LevelStructure`)
   - Missing required fields
   - Wrong field types
   - Schema violations
   - Example: "spec.tools must be a list, got: map"

3. **SEMANTIC Level** (`LevelSemantic`)
   - Invalid tool references
   - Logical inconsistencies
   - Deprecated fields
   - Example: "Tool 'non_existent_tool' does not exist"

### Error Formatting

The tool formats errors for LLM consumption (`FormatForWeaver()` in `pkg/validation/types.go`, line ~75):

```go
func (r *ValidationResult) FormatForWeaver() string {
    if r.Valid {
        return "✅ YAML validation passed - no issues detected"
    }

    output := "\n⚠️  YAML VALIDATION ISSUES DETECTED:\n\n"

    // Group by level for clarity
    byLevel := r.ErrorsByLevel()

    // Syntax errors first (most critical)
    // Then structure errors
    // Then semantic errors
    // Each with actionable fixes
}
```

### Test Coverage

**Test Files**:
- `pkg/shuttle/builtin/agent_management_test.go`
- `pkg/shuttle/builtin/agent_management_structured_test.go`

**Key Test Cases** (`agent_management_test.go`):
- `TestAgentManagementTool_AccessControl`: Verifies weaver/guide-only restriction
- `TestAgentManagementTool_CreateAgent`: Tests agent creation with validation
- `TestAgentManagementTool_UpdateAgent`: Tests update operations
- `TestAgentManagementTool_ReadAgent`: Tests read operations
- `TestAgentManagementTool_ListAgents`: Tests list operations
- `TestAgentManagementTool_ValidateOnly`: Tests validation-only mode
- `TestAgentManagementTool_DeleteAgent`: Tests delete operations
- `TestAgentManagementTool_WorkflowOperations`: Tests workflow create/list
- `TestAgentManagementTool_ConcurrentOperations`: Tests concurrent reads

**Key Test Cases** (`agent_management_structured_test.go`):
- `TestDNDBugPrevention`: Validates `role` vs `agent_id` field enforcement
- `TestCreateAgentStructured`: Tests structured JSON agent creation
- `TestAgentReferenceValidation`: Tests agent reference validation in workflows
- `TestUpdateWorkflowValidation`: Tests workflow update validation

**Coverage Verification**: Tests verify:
- ✅ Security restriction enforced (weaver + guide)
- ✅ Valid YAML accepted and written to correct directory
- ✅ Invalid YAML rejected with actionable errors
- ✅ Correct directory placement (`$LOOM_DATA_DIR/agents/`, `$LOOM_DATA_DIR/workflows/`)
- ✅ Concurrent read safety


## Agent Configuration

### Weaver System Prompt

The weaver's prompt emphasizes:

1. **Tool Usage**: Must use `agent_management` for YAML operations
2. **Validation Handling**: Read error messages and fix issues
3. **Best Practices**: Memory compression, adequate max_turns, tool discovery
4. **Standard Toolset**: Give agents tool_search for discovering available tools

### Critical Rules

The weaver enforces these via the `agent_management` tool:

```
VALIDATION RULES ENFORCED:
- apiVersion MUST be "loom/v1" (not "loom.dev/v1")
- kind MUST be "Agent", "Workflow", or "Skill"
- metadata.name is required
- spec section must exist
- tools MUST be a flat array of strings (not nested under "builtin:")
- config section (NOT "execution:")
- max_tool_executions (NOT "max_tool_calls")
- memory.type MUST be specified if memory section exists
- NO spec.agent.* nesting - all fields go directly under spec:
```


## Design Rationale

### Why `agent_management` Tool?

**Problem**: Previous approach using `shell_execute` for YAML operations had issues:
- No validation before write (discover errors after file created)
- Security risk (any agent could write arbitrary files)
- Complex error recovery (manual file cleanup)
- Poor error messages (generic file I/O errors)

**Solution**: Specialized `agent_management` tool provides:
- ✅ Upfront validation (fail before write)
- ✅ Security restriction (weaver + guide only)
- ✅ Atomic operations (valid or nothing)
- ✅ LLM-friendly errors (actionable fixes)
- ✅ Automatic directory management

### Trade-offs

**Chosen Approach: Specialized Tool**
- ✅ Secure (restricted to weaver + guide agents)
- ✅ Reliable (validation before write)
- ✅ Clear errors (structured validation)
- ✅ LLM-friendly (one tool, clear actions)
- ❌ Additional code to maintain
- ❌ Can only be used by weaver and guide agents

**Alternative: Generic File Operations**
- ✅ Reusable by any agent
- ✅ No special code
- ❌ No validation
- ❌ Security risk
- ❌ Poor error messages
- ❌ Complex error recovery

**Verdict**: Specialized tool is superior for this critical use case.

### Why Restrict to Weaver and Guide?

**Security Rationale**:
1. **Privilege Escalation Risk**: Agents creating agents could escalate privileges
2. **Resource Control**: Prevent unbounded agent proliferation
3. **Quality Control**: Ensure generated agents follow best practices
4. **Audit Trail**: Limited set of agents for monitoring (weaver for write, guide for read-only)

**Implementation**: Session-based agent ID check (not bypassable by LLM). The guide agent can list and read configurations but cannot create, update, or delete.


## Performance Characteristics

### Generation Latency

**Typical**: 5-15 seconds (single agent generation)
- LLM inference: 3-10 seconds
- Validation: 10-50ms
- File I/O: 5-10ms
- Hot-reload detection: 89-143ms

**Multi-Agent Workflow**: 15-45 seconds
- Multiple agent definitions
- Workflow configuration
- Pattern selection

**Cost**: $0.02-0.10 per generation (Claude Sonnet 4.5)

### Validation Performance

**Measured on M2 MacBook Pro**:
- YAML parsing: 1-5ms
- Structure validation: 5-15ms
- Semantic validation: 10-30ms
- Total: 16-50ms for complete validation

**Scaling**: O(n) where n = YAML size in bytes

### Accuracy

**Measured Success Rate**: >98% (generates valid YAML first try with validation)

**Common Failure Modes**:
1. User requests conflicting requirements
2. User asks for non-existent features
3. Complex nested workflows exceeding context

**Self-Correction**: Agent reads validation errors and fixes on retry.


## Future Considerations

### Potential Enhancements

**1. Pattern Library Integration**

Allow agent_management tool to suggest patterns based on agent type.

**Implementation**: Add pattern matching to validation result.

**2. Template Expansion**

Pre-validated templates for common agent types.

**Implementation**: Store in `$LOOM_DATA_DIR/templates/`, reference in tool.

**3. Dependency Resolution**

Automatically add required tools based on agent purpose.

**Implementation**: Tool dependency graph in validation logic.

**4. Version Control**

Track changes to agent configurations over time.

**Implementation**: Git integration in agent_management tool.

### Non-Goals

**Will NOT Add**:
- ❌ Unrestricted access (security boundary stays; weaver + guide only)
- ❌ Multi-stage pipeline (complexity without benefit)
- ❌ Dedicated gRPC service (standard RPCs work fine)
- ❌ Complex conflict resolution (validation prevents conflicts)


## Related Documentation

- **Agent Configuration**: `/reference/agent-configuration.md` - Agent YAML schema
- **Multi-Agent Architecture**: `/architecture/multi-agent.md` - Workflow and multi-agent patterns
- **Tool Registry**: `/reference/tool-registry.md` - Tool discovery and execution


**Key Takeaway**: The weaver uses a specialized `agent_management` tool that provides upfront validation, security restrictions, and atomic operations. This design eliminates entire classes of errors while maintaining the single-agent approach with minimal moving parts.