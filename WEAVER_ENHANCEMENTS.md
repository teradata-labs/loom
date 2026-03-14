# Weaver Enhancements: /agent-plan Mode & Skills Recommendations

## Overview

The weaver meta-agent has been enhanced with two major features:
1. **Guided /agent-plan mode** - Structured requirement gathering through conversation
2. **Skills-based recommendations** - Intelligent skill suggestions based on user needs

## Branch

Created on: `weaver-agent-plan-mode`

## What Changed

### 1. Updated Weaver System Prompt (`embedded/weaver.yaml`)

The weaver now:
- **Offers /agent-plan mode** on first interaction
- **Recommends relevant skills** based on user's problem domain
- **Provides skill activation commands** and usage examples
- **Configures skills in agent YAML** when creating agents

#### Example First Interaction

```
User: "Create an agent to optimize SQL queries"

Weaver: "I can help you create that! Would you like me to:

1. **Quick Start** - I'll create the agent/workflow based on your description right away
2. **/agent-plan Mode** - I'll guide you through a structured planning process first

Which approach would you prefer?"
```

### 2. New: Agent Plan Mode Guide (`prompts/metaagent/agent_plan_mode.yaml`)

A comprehensive conversation flow guide for /agent-plan mode with:

**5 Planning Phases:**
1. **Problem Understanding** - Goal discovery, success criteria, current workflow
2. **Technical Requirements** - Data sources, integrations, tool needs
3. **Skill Recommendation** - Match user needs to available skills
4. **Workflow Design** - For multi-agent scenarios (pipeline, parallel, debate, coordination)
5. **Confirmation & Creation** - Summary and user approval

**Example Planning Conversation:**
```
Phase 1: "What specific problem are you solving?"
Phase 2: "What database are you using?"
Phase 3: "I recommend sql-optimization skill (/optimize) for query analysis"
Phase 5: "Here's the plan... Ready to create?"
```

### 3. New: Skills Catalog (`prompts/metaagent/skills_catalog.yaml`)

A decision tree and catalog for skill recommendations:

**Skill Matching Logic:**
- Keywords → Skills mapping (e.g., "slow query" → `sql-optimization`)
- Domain → Skills mapping (e.g., Database → `sql-optimization`, `data-quality-check`)
- Use case → Skill combinations (e.g., Multi-agent → `multi-agent-coordinator` + `agent-discovery`)

**Available Skills:**
- `sql-optimization` - Database performance analysis
- `code-review` - Security and quality checks
- `data-quality-check` - Data validation
- `multi-agent-coordinator` - Task delegation
- `agent-discovery` - Find specialist agents
- `request-response-coordinator` - Synchronous agent communication

## How It Works

### Quick Start Flow (Option 1)
```
User: "Create SQL agent"
Weaver: "Quick Start or /agent-plan?"
User: "Quick Start"
Weaver: [Creates agent immediately]
        "I've configured sql-optimization skill (/optimize) for query analysis"
```

### /agent-plan Mode Flow (Option 2)
```
User: "Create SQL agent"
Weaver: "Quick Start or /agent-plan?"
User: "/agent-plan"
Weaver: "What specific problem are you solving?"
User: "Slow queries on PostgreSQL"
Weaver: "How will you know it's working?"
User: "If it finds bottlenecks"
Weaver: "What database?"
User: "PostgreSQL"
Weaver: "I recommend sql-optimization skill - include it?"
User: "Yes"
Weaver: "Here's the plan: [summary]... Create this?"
User: "Yes!"
Weaver: [Creates agent with skill configured]
```

### Skills Recommendation Examples

#### SQL Performance
```yaml
# Weaver detects: "slow query", "database performance"
# Recommends:
spec:
  skills:
    - name: sql-optimization
      enabled: true
      activation_mode: HYBRID
```

#### Code Security
```yaml
# Weaver detects: "code review", "security"
# Recommends:
spec:
  skills:
    - name: code-review
      enabled: true
  tools:
    - file_read  # Required by code-review
```

#### Multi-Agent Orchestration
```yaml
# Weaver detects: "multiple agents", "coordinate"
# Recommends:
spec:
  skills:
    - name: multi-agent-coordinator
      enabled: true
    - name: agent-discovery
      enabled: true
  tools:
    - send_message  # Required for coordination
```

## Implementation Details

### Files Modified
- `embedded/weaver.yaml` - Added /agent-plan mode prompt and skills recommendations to system_prompt

### Files Created
- `prompts/metaagent/agent_plan_mode.yaml` - Conversation flow guide (247 lines)
- `prompts/metaagent/skills_catalog.yaml` - Skill matching decision tree (226 lines)

### Weaver Workflow Updates

The weaver's YOUR Workflow section now includes:
1. **On first interaction**: Offer /agent-plan mode vs quick start
2. **Assess intent**: AND recommend relevant skills from catalog
3. **If multi-agent**: Determine workflow type
4. **Find tools**: Using tool_search
5. **Create agents**: With agent_management
6. **Configure skills**: In agent's spec.skills section (NEW)
7. **Create workflow**: If multi-agent
8. **Document**: Including skill activation commands (UPDATED)
9. **Verify**: With agent_management list
10. **Show commands**: Via loom --help

## Benefits

### For Users
- ✅ Structured guidance through /agent-plan mode
- ✅ Discover skills they didn't know existed
- ✅ Get skill activation commands upfront (/optimize, /review, etc.)
- ✅ Better agent configurations with recommended skills

### For Weaver
- ✅ Systematic requirement gathering process
- ✅ Clear skill recommendation criteria
- ✅ Consistent conversation flow
- ✅ Reduced back-and-forth guessing

## Testing the Changes

### Test /agent-plan Mode

```bash
# Start loom TUI and select weaver
loom

# In TUI, send:
"Create an agent to analyze slow database queries"

# Weaver should offer:
# 1. Quick Start
# 2. /agent-plan Mode

# Choose option 2 and follow the conversation
```

### Test Skills Recommendations

```bash
# Test SQL skill recommendation
"Build a PostgreSQL query optimizer"
# Should recommend: sql-optimization

# Test multi-agent skill recommendation
"Create a workflow where agents collaborate"
# Should recommend: multi-agent-coordinator + agent-discovery

# Test code review skill recommendation
"I need an agent for security code reviews"
# Should recommend: code-review
```

### Verify Created Agent Config

After weaver creates an agent, check the YAML:

```bash
cat ~/.loom/agents/[agent-name].yaml
```

Should include:
```yaml
spec:
  skills:
    - name: sql-optimization
      enabled: true
      activation_mode: HYBRID
```

## Skills by Problem Domain

| User Problem | Recommended Skill(s) |
|-------------|---------------------|
| "Slow queries" | sql-optimization |
| "Code review" | code-review |
| "Validate data" | data-quality-check |
| "Multiple agents" | multi-agent-coordinator, agent-discovery |
| "Agent collaboration" | request-response-coordinator |

## Next Steps

1. **Test with real users** - Gather feedback on /agent-plan mode flow
2. **Expand skill catalog** - Add more skills as they're created
3. **A/B test** - Compare quick start vs /agent-plan mode usage
4. **Metrics** - Track which skills are most recommended/used
5. **Refinement** - Adjust conversation flow based on user feedback

## Example: Full /agent-plan Session

```
User: "I need help with slow SQL queries"

Weaver: "I can help you create that! Would you like:
        1. Quick Start - Create it right away
        2. /agent-plan Mode - Guided planning
        Which approach?"

User: "/agent-plan"

Weaver: "Let's plan this out. What specific problem are you solving?"

User: "Our PostgreSQL queries are getting slower as data grows"

Weaver: "How will you know the agent is working well?"

User: "If it can identify why queries are slow and suggest fixes"

Weaver: "What database are you using?"

User: "PostgreSQL"

Weaver: "Do you need read-only access or write access too?"

User: "Read-only"

Weaver: "Based on your needs, I recommend:

        🎯 sql-optimization (/optimize) - RECOMMENDED
           Analyzes query execution plans and suggests improvements
           Includes index recommendations and query rewrites

        Would you like me to include this skill?"

User: "Yes"

Weaver: "Great! Here's what I'm creating:

        Agent Name: postgresql-query-optimizer
        Purpose: Analyze slow PostgreSQL queries and recommend optimizations
        Database: PostgreSQL (read-only)
        Tools: execute_sql, tool_search
        Skill: sql-optimization (/optimize)
        Success: Identify bottlenecks and suggest fixes

        Ready to create?"

User: "Yes!"

Weaver: [Creates agent with agent_management]

        "✅ Created postgresql-query-optimizer!

        To use it:
        loom postgresql-query-optimizer

        The sql-optimization skill activates when you:
        - Use /optimize command
        - Mention 'slow query' or 'optimize'

        Example: 'This query is slow: SELECT * FROM users WHERE...'

        Come back anytime if you need changes!"
```

## Files Summary

```
embedded/weaver.yaml                       - Updated system prompt (existing file)
prompts/metaagent/agent_plan_mode.yaml    - Planning conversation guide (new)
prompts/metaagent/skills_catalog.yaml     - Skill recommendation catalog (new)
```

Total: 473 lines of guidance added

## Commit

```bash
git log -1 --oneline
# faaeceb feat(weaver): Add /agent-plan mode and skills-based recommendations
```
