# Weaver Enhancements: /agent-plan Mode & Skills Recommendations

## Overview

The weaver meta-agent has been enhanced with three major features:
1. **Guided /agent-plan mode** - Structured requirement gathering through conversation
2. **Skills-based recommendations** - Intelligent skill suggestions based on user needs
3. **Skill creation capability** - Weaver can now create custom skills for agents

## Branch

Created on: `weaver-agent-plan-mode`

## What Changed

### 1. TUI Integration: /agent-plan Command Visibility

**Files Modified:**
- `internal/tui/components/chat/sidebar/sidebar.go`
- `internal/tui/components/dialogs/commands/slash_help_dialog.go`

The `/agent-plan` command is now discoverable in three places:

#### A. Weaver Sidebar Section (When Active)
When weaver is the active agent, the sidebar shows:
```
Weaver
✨ weaver
  /agent-plan  guided planning
```

#### B. Slash Commands Help (/help)
Added to the global slash commands dialog:
```
/agent-plan    guided agent planning (weaver)
```

#### C. Keyboard Hints Block
Added to the sidebar's slash commands reference at the bottom:
```
/agent-plan    plan agent
```

#### D. Weaver Splash/Welcome Screen
Updated the weaver's welcome screen to prominently feature /agent-plan mode:
```
✨ Weaver

The weaver creates custom agents and workflows from natural language.

Two Ways to Create:
  1. Quick Start - Describe what you need, weaver creates it immediately
  2. /agent-plan - Guided planning with structured questions

Weaver also recommends skills to enhance your agents:
  • sql-optimization for database performance
  • code-review for security and quality
  • multi-agent-coordinator for orchestration
  • And more based on your use case

Examples:
  • "Create a SQL query analyzer for PostgreSQL"
  • "Build a multi-agent workflow for data processing"
  • "/agent-plan" (for guided planning mode)
```

**User Benefit:**
- ✅ Users can discover /agent-plan without needing to know about it beforehand
- ✅ Visible when weaver is active (contextual help in sidebar)
- ✅ Available in global help (/help)
- ✅ Listed with other slash commands in sidebar
- ✅ Prominently featured in weaver's welcome screen (first thing users see!)

### 2. Updated Weaver System Prompt (`embedded/weaver.yaml`)

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

### 3. Skill Creation Capability

Weaver can now create custom skills using the agent_management tool.

#### New agent_management Actions
- `create_skill` - Create a new skill YAML file
- `update_skill` - Update an existing skill YAML file

#### Skill Creation Flow
```
User: "Create an agent for analyzing Python code performance"

Weaver: [Detects need for python-performance-analysis skill]
        [Checks if skill exists]
        [Skill doesn't exist - creates it]

agent_management({
  action: "create_skill",
  config: {
    apiVersion: "loom/v1",
    kind: "Skill",
    metadata: {
      name: "python-performance-analysis",
      description: "Analyzes Python code for performance bottlenecks"
    },
    spec: {
      trigger: {
        activation_mode: "HYBRID",
        slash_command: "/perf",
        keywords: ["slow python", "performance", "bottleneck"]
      },
      prompt: "...",
      tools: ["file_read", "shell_execute"]
    }
  }
})
```

#### Skill File Output
Skills are written to `$LOOM_DATA_DIR/skills/` directory:
```bash
~/.loom/skills/python-performance-analysis.yaml
```

With validation before writing:
- YAML syntax validation
- Required fields check (metadata.name, spec.trigger, spec.prompt)
- Activation mode validation (MANUAL, AUTO, HYBRID, ALWAYS)

## Implementation Details

### Files Modified
- `embedded/weaver.yaml.tmpl` - Added /agent-plan mode, skills recommendations, and skill creation to system_prompt
- `internal/tui/components/chat/sidebar/sidebar.go` - Added /agent-plan hint to weaver section and keyboard hints
- `internal/tui/components/dialogs/commands/slash_help_dialog.go` - Added /agent-plan to help dialog
- `internal/tui/components/chat/splash/splash.go` - Updated weaver welcome screen with /agent-plan info
- `pkg/shuttle/builtin/agent_management.go` - Added create_skill and update_skill action routing

### Files Created
- `prompts/metaagent/agent_plan_mode.yaml` - Conversation flow guide (247 lines)
- `prompts/metaagent/skills_catalog.yaml` - Skill matching decision tree (226 lines)
- `pkg/shuttle/builtin/agent_management_skill.go` - Skill CRUD operations (248 lines)
- `WEAVER_ENHANCEMENTS.md` - Complete documentation (this file)

### Weaver Workflow Updates

The weaver's YOUR Workflow section now includes:
1. **On first interaction**: Offer /agent-plan mode vs quick start
2. **Assess intent**: AND recommend relevant skills from catalog
3. **Create skills if needed**: Use agent_management(action="create_skill") for custom skills (NEW)
4. **If multi-agent**: Determine workflow type
5. **Find tools**: Using tool_search
6. **Create agents**: With agent_management
7. **Configure skills**: In agent's spec.skills section
8. **Create workflow**: If multi-agent
9. **Document**: Including skill activation commands
10. **Verify**: With agent_management list (agents/workflows/skills)
11. **Show commands**: Via loom --help

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

### Test Skill Creation

```bash
# Test creating a custom skill
"Create an agent for analyzing Python performance"
# Weaver should:
# 1. Detect need for python-performance-analysis skill
# 2. Create the skill using agent_management
# 3. Configure the skill in the agent's spec.skills section

# Verify skill was created
cat ~/.loom/skills/python-performance-analysis.yaml
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

## Commits

```bash
git log --oneline -4

# 470f475 feat(weaver): Enable skill creation and recommendations
# d9470b0 feat(tui): Update weaver splash screen with /agent-plan mode
# 1f9aa02 feat(tui): Show /agent-plan command in sidebar and help
# acf3b35 feat(weaver): Add /agent-plan mode and skills-based recommendations
```

**Total changes:**
- 7 files modified
- 3 files created
- 1105+ lines added

## Visual Changes

### Weaver Section (Active Agent)
```
┌─ Sidebar ─────────────────┐
│                           │
│ Weaver                    │
│ ✨ weaver                 │
│   /agent-plan  guided...  │  ← NEW
│                           │
└───────────────────────────┘
```

### Slash Commands Help (/help)
```
┌─ Slash Commands ──────────────────┐
│                                   │
│ /agents         agents    ctrl+e  │
│ /workflows      workflows ctrl+w  │
│ /agent-plan     guided...         │  ← NEW
│ /sidebar        sidebar           │
│                                   │
└───────────────────────────────────┘
```

### Keyboard Hints (Bottom of Sidebar)
```
Slash Commands:
  /clear       clear chat
  /agents      agents
  /workflows   workflows
  /agent-plan  plan agent        ← NEW
  /help        help
```
