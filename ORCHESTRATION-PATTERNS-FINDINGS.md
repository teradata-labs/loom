# Orchestration Patterns Testing - Findings (Jan 22, 2026)

## Summary

Orchestration patterns are **functionally working** but require **pre-existing agents** in the agent registry. Inline agent definitions documented in `workflow-all-fields-example.yaml` are **not yet implemented** in the CLI.

## Test Results

### ✅ Fork-Join Pattern - WORKING

**Test File**: `test-fork-join-simple.yaml`
**Status**: ✅ Passed
**Duration**: 6.36s
**Cost**: $0.036
**Agents Used**: `creative`, `analyst` (existing agents from registry)

**Output**:
```
✅ WORKFLOW COMPLETED
Duration: 6.36s
Total cost: $0.0360
Total tokens: 10263
LLM calls: 2
```

Both agents executed in parallel and results were merged successfully using the `concatenate` strategy.

## Key Findings

### 1. CLI Doesn't Support Inline Agent Definitions Yet

**Expected** (per `workflow-all-fields-example.yaml` lines 372-399):
```yaml
spec:
  type: fork-join
  agents:
    - id: my-agent
      name: My Agent
      system_prompt: |
        Agent instructions...
```

**Reality**: CLI code (`cmd_workflow.go:343`) calls `registry.CreateAgent(ctx, agentID)` which **only** looks up agents from `~/.loom/agents/` directory, not from inline definitions in the workflow YAML.

**Error When Using Inline Definitions**:
```
❌ Failed to create agent api-architect: agent configuration not found: api-architect
```

### 2. Pattern Type Names Use Hyphens, Not Underscores

**Correct**:
- `type: fork-join` ✅
- `type: pipeline` ✅

**Incorrect**:
- `type: fork_join` ❌ → Error: "unsupported workflow pattern type: 'fork_join'"

**Valid Pattern Types**:
- `debate`
- `fork-join`
- `pipeline`
- `parallel`
- `conditional`
- `iterative`
- `swarm`

### 3. Orchestration Pattern Infrastructure Works

The execution infrastructure for orchestration patterns is fully functional:
- Pattern parsing ✅
- Agent registration ✅
- Parallel execution ✅
- Result merging ✅
- Timeout handling ✅
- Cost tracking ✅

### 4. Reference Documentation vs Implementation Gap

**`workflow-all-fields-example.yaml`** (lines 372-399) documents 4 ways to define agents:
1. By ID (agent must exist in registry) - ✅ **WORKS**
2. By inline configuration - ❌ **NOT IMPLEMENTED**
3. By path reference (load from file) - ❓ **UNTESTED**
4. Mixed (some inline, some referenced) - ❓ **UNTESTED**

## Implications for Example Workflows

All 6 orchestration pattern examples in `examples/reference/workflows/` have inline agent definitions but reference non-existent agent IDs:

| Pattern | File | Agent IDs Referenced | Status |
|---------|------|----------------------|--------|
| Pipeline | feature-pipeline.yaml | api-architect, backend-developer, test-engineer | ❌ Agents don't exist |
| Fork-Join | code-review.yaml | quality, security, performance | ❌ Agents don't exist |
| Parallel | doc-generation.yaml | api-documenter, technical-writer, example-creator | ❌ Agents don't exist |
| Parallel | security-analysis.yaml | sast-analyzer, dast-analyzer, threat-modeler, etc | ❌ Agents don't exist |
| Debate | architecture-debate.yaml | architect-advocate, pragmatist-engineer, senior-architect | ❌ Agents don't exist |
| Conditional | complexity-routing.yaml | complexity-classifier | ❌ Agent doesn't exist |
| Swarm | technology-swarm.yaml | database-expert, performance-engineer, etc | ❌ Agents don't exist |

## Recommendations

### Short-Term Fix Options

1. **Option A: Create Agent Configs in Registry**
   - Create YAML files in `~/.loom/agents/` for each referenced agent
   - Allows examples to work immediately
   - Downside: Duplicates inline definitions

2. **Option B: Implement Inline Agent Support in CLI**
   - Modify `cmd_workflow.go` to check for inline agents before calling `registry.CreateAgent()`
   - Parse `spec.agents` array and create agents dynamically
   - Aligns with documented behavior

3. **Option C: Reference Existing Agents**
   - Update examples to use existing agents (like `creative`, `analyst`, `dm`, etc.)
   - Simplifies examples
   - Downside: Less clear what each pattern does

### Recommended Approach: Option B + Documentation Update

**Implement inline agent support** AND **update documentation** to clarify current limitations:

**Code Changes Needed** (`cmd_workflow.go`):
```go
// Before calling registry.CreateAgent(), check if agent is defined inline
if inlineAgent := findInlineAgent(workflowSpec, agentID); inlineAgent != nil {
    // Create agent from inline definition
    ag, err := createAgentFromInline(ctx, inlineAgent)
} else {
    // Fall back to registry lookup
    ag, err := registry.CreateAgent(ctx, agentID)
}
```

**Documentation Updates**:
- Add **"⚠️ Current Limitation"** sections to example workflows
- Note that inline agents require creating agent configs in `~/.loom/agents/` first
- Update `WORKFLOW-PATTERNS-STATUS.md` with these findings

## Testing Checklist

- [x] Fork-join with existing agents
- [ ] Pipeline with existing agents
- [ ] Parallel with existing agents
- [ ] Debate with existing agents
- [ ] Conditional with existing agents
- [ ] Swarm with existing agents
- [ ] Test after inline agent support implemented

## Code References

- **CLI workflow execution**: `cmd_workflow.go:325-407`
- **Agent ID extraction**: `cmd_workflow.go:extractAgentIDs()`
- **Agent creation**: `cmd_workflow.go:343` → `registry.CreateAgent()`
- **Agent registry**: `pkg/agent/registry.go:288-307`
- **Documentation**: `examples/workflow-all-fields-example.yaml:372-399`

## Next Steps

1. ✅ Document findings (this file)
2. ⏭️ Implement inline agent support in CLI (if prioritized)
3. ⏭️ OR create agent configs for example workflows
4. ⏭️ Test remaining orchestration patterns with existing agents
5. ⏭️ Update WORKFLOW-PATTERNS-STATUS.md with test results
