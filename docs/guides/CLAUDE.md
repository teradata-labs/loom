# User Guide Documentation Standards

## Purpose
This directory contains **user-facing guides** for Loom features. Guides teach users HOW to accomplish specific tasks, not how the system works internally.

## Critical Rules

### 1. Always Use the docs-verifier Agent
Before writing or updating any guide, ALWAYS use the docs-verifier agent to:
- Verify features actually exist in the codebase
- Check test coverage for claimed functionality
- Ensure status indicators are accurate
- Validate code examples compile and run

**Never document a feature without verification!**

### 2. No Implementation Details
Guides are for USERS, not developers. Avoid:
- ❌ Code architecture explanations
- ❌ Internal API details
- ❌ Package structure discussions
- ❌ Implementation trade-offs
- ❌ Function signatures (unless showing example usage)

Implementation details belong in `/docs/concepts/` or `/docs/reference/`.

### 3. Task-Oriented Structure
Every guide should follow this pattern:

```markdown
# [Feature Name] Guide

## Overview
Brief 1-2 sentence description of what this feature does.

## Prerequisites
- Required setup
- Dependencies
- Configuration needed

## Quick Start
Minimal working example to get started immediately.

## Common Tasks
### Task 1: [Specific Goal]
Step-by-step instructions with examples.

### Task 2: [Another Goal]
More examples.

## Examples
### Example 1: [Real-World Scenario]
Complete, runnable example with explanation.

### Example 2: [Another Scenario]
Another complete example.

## Troubleshooting
Common issues and solutions.

## Next Steps
Links to related guides.
```

### 4. Example Requirements
Every guide MUST include:
- ✅ At least 2 complete, runnable examples
- ✅ Real-world scenarios (not toy examples)
- ✅ Expected output shown
- ✅ Common variations demonstrated
- ✅ Error handling examples where relevant

**Example Quality Standards:**
```markdown
<!-- BAD: Incomplete, no context -->
Run: `looms serve`

<!-- GOOD: Complete, contextual, shows output -->
Start the Loom server with your agent configuration:

\`\`\`bash
looms serve --config $LOOM_DATA_DIR/config.yaml
\`\`\`

Expected output:
\`\`\`
🚀 Loom server starting on :50051
✅ Loaded 3 agents: sql-agent, code-reviewer, data-analyst
✅ MCP servers initialized: vantage, github
🎯 Server ready
\`\`\`
```

### 5. Status Indicators (Required)
Use these consistently:
- ✅ **Available** - Feature fully working with tests
- ⚠️ **Partial** - Core works, some features incomplete
- 🚧 **In Development** - Actively being built
- 📋 **Planned** - Not yet started
- 🔬 **Experimental** - Working but API may change

### 6. Version Context
Include version info when features were added:
```markdown
> **Note:** This feature requires Loom v1.0.0-beta.1 or later.
```

### 7. Banned Marketing Speak
Never use these words without evidence:
- ❌ "seamless" / "seamlessly"
- ❌ "comprehensive"
- ❌ "robust" / "powerful"
- ❌ "production-ready" (unless verified)
- ❌ "enterprise-grade"
- ❌ "cutting-edge"

Use specific, measurable claims instead:
- ✅ "Supports 5 LLM providers"
- ✅ "Hot-reloads in 89-143ms"
- ✅ "73% test coverage, 1439+ tests"

### 8. Code Examples Must Be Tested
All code examples must:
1. Actually work (verified by docs-verifier agent)
2. Use real file paths and configurations
3. Show complete commands (not fragments)
4. Include expected output
5. Handle errors appropriately

### 9. External Dependencies
Clearly document external requirements:
```markdown
## Prerequisites

This guide requires:
- Loom v1.0.0-beta.1+
- Anthropic API key (get from https://console.anthropic.com)
- Optional: Hawk service running for observability
```

### 10. Cross-References
Link to related documentation:
- Concepts: `/docs/concepts/` - What things are and how they work
- Reference: `/docs/reference/` - API details, CLI commands
- Other Guides: `/docs/guides/` - Other how-to guides

## Guide Categories

### Getting Started Guides
For new users, assume no prior knowledge:
- `quickstart.md` - 5-minute introduction
- `zero-code-implementation-guide.md` - No-code agent setup

### Feature Guides
Deep dive into specific features:
- `pattern-library-guide.md`
- `learning-agent-guide.md`
- `multi-judge-evaluation.md`
- etc.

### Integration Guides
How to connect with external systems:
- `judge-dspy-integration.md`
- Future: `hawk-integration.md`, `promptio-integration.md`

### Advanced Guides
Complex use cases:
- `meta-agent-usage.md`
- `structured-context-pattern.md`
- `human-in-the-loop.md`

## Verification Checklist

Before committing any guide, verify:

- [ ] Starts with Table of Contents
- [ ] Used docs-verifier agent to validate claims
- [ ] All features mentioned actually exist in codebase
- [ ] At least 2 complete, runnable examples included
- [ ] No implementation details (architecture, internals)
- [ ] Status indicators present and accurate
- [ ] Code examples tested and working
- [ ] Expected output shown for commands
- [ ] Prerequisites clearly listed
- [ ] No marketing speak or unverified claims
- [ ] Cross-references to related docs included
- [ ] Troubleshooting section included
- [ ] Version requirements specified if relevant

## Common Mistakes

### ❌ Mixing Concepts and Tasks
```markdown
<!-- BAD: This is a concept explanation, not a guide -->
# Agent Configuration Guide

Agents in Loom use a layered memory system with ROM, Kernel, L1, and L2 caches...
```

```markdown
<!-- GOOD: Task-focused -->
# Agent Configuration Guide

## Configure an Agent for SQL Analysis

1. Create a configuration file:
\`\`\`yaml
name: sql-analyzer
llm:
  provider: anthropic
  model: claude-sonnet-4
\`\`\`
```

### ❌ Missing Examples
```markdown
<!-- BAD: No examples -->
You can configure judges with different aggregation strategies.
```

```markdown
<!-- GOOD: Complete examples -->
Configure judges with weighted-average aggregation:

\`\`\`yaml
aggregation: weighted-average
judges:
  - id: quality
    weight: 0.6
  - id: safety
    weight: 0.4
\`\`\`

Or use all-must-pass for critical checks:

\`\`\`yaml
aggregation: all-must-pass
judges:
  - id: security-scan
  - id: compliance-check
\`\`\`
```

### ❌ Incomplete Commands
```markdown
<!-- BAD: Fragment -->
Run `looms judge evaluate`
```

```markdown
<!-- GOOD: Complete with context -->
Evaluate an agent response with multiple judges:

\`\`\`bash
looms judge evaluate \
  --agent=sql-agent \
  --judges=quality-judge,safety-judge \
  --prompt="Generate a query" \
  --response="SELECT * FROM users"
\`\`\`
```

## Documentation Workflow

1. **Plan**: Identify what users need to accomplish
2. **Verify**: Use docs-verifier agent to check feature exists
3. **Draft**: Write task-oriented guide with examples
4. **Test**: Run all code examples to ensure they work
5. **Review**: Check against this CLAUDE.md
6. **Validate**: Run docs-verifier again on final version
7. **Commit**: Submit with accurate, verified content

## Questions to Ask Before Writing

1. **What task does this guide help users accomplish?**
   - If you can't answer clearly, it might be a concept doc, not a guide

2. **Can I show a complete working example?**
   - If no, the feature might not be ready to document

3. **Have I verified this feature exists and works?**
   - If no, use the docs-verifier agent first

4. **Would a new user understand this without reading code?**
   - If no, remove implementation details

5. **Does this duplicate existing documentation?**
   - Check `/docs/concepts/`, `/docs/reference/`, and other guides

## Agent Usage

Always use the docs-verifier agent when:
- Creating a new guide
- Updating an existing guide
- Adding new examples
- Claiming a feature exists
- Documenting API behavior

Example:
```
Use the Task tool with subagent_type='docs-verifier' and prompt:
"Verify the multi-judge evaluation feature exists, check test coverage,
and validate these code examples work: [examples]"
```

---

**Remember**: Guides are for USERS. Keep them practical, verified, and example-rich.
