# Weaver Enhancements Implementation Plan

**Branch**: `weaver-agent-plan-mode`
**Version**: v1.1.0
**Status**: ✅ Implemented

## Overview

Implementation of three major enhancements to the weaver meta-agent:
1. **Guided /agent-plan mode** - 5-phase structured requirement gathering
2. **Skills-based recommendations** - Intelligent skill suggestions via decision tree
3. **Skill creation capability** - Opt-in skill generation with user consent

> **User Guide**: See [docs/guides/weaver-usage.md](../guides/weaver-usage.md) for user-facing documentation.

## Technical Architecture

### 1. TUI Integration

**Files Modified:**
- `internal/tui/components/chat/sidebar/sidebar.go` - Added /agent-plan to weaver section and keyboard hints
- `internal/tui/components/dialogs/commands/slash_help_dialog.go` - Added to slash commands help
- `internal/tui/components/chat/splash/splash.go` - Updated weaver welcome screen

**Discoverability Points:**
- Weaver sidebar section (when active): `/agent-plan  guided planning`
- Slash commands help (/help): `/agent-plan    guided agent planning (weaver)`
- Keyboard hints block: `/agent-plan    plan agent`
- Weaver splash screen: Featured in description and examples

**Implementation (sidebar.go:601-607)**:
```go
// Add /agent-plan command hint when weaver is active
if isActive {
    cmdHint := t.S().Base.Foreground(t.FgSubtle).PaddingLeft(2).Render("/agent-plan  guided planning")
    lines = append(lines, cmdHint)
}
```

### 2. Weaver System Prompt Updates

**File**: `embedded/weaver.yaml.tmpl`

**Key Additions:**

#### Guidelines Section
- Extended to cover agent/workflow/skill YAMLs
- Updated file discovery to include skills directory
- Added LLM-agnostic skill creation documentation

#### New: Skill Creation Section
```yaml
## Skill Creation

- Skills are LLM-agnostic prompt injections that enhance agents with domain expertise
- Use agent_management with action="create_skill" to create new skills
- Use agent_management with action="update_skill" to modify existing skills
- Skills automatically validate and write to $LOOM_DATA_DIR/skills/
- Skill structure: apiVersion: loom/v1, kind: Skill, metadata (name, description), spec (trigger, prompt, tools)
- Activation modes: MANUAL (/command), AUTO (keyword-based), HYBRID (both), ALWAYS (every turn)
- Skills can be configured in agent spec.skills section to enable them for specific agents
```

#### New: Skill Recommendation Guidelines
```yaml
## Skill Recommendation Guidelines

**ALWAYS ask before creating a new skill. Never create skills without user consent.**

When recommending a new skill:
1. Explain what the skill would do (specific capabilities)
2. Explain why it's beneficial (automation, expertise injection, consistency)
3. Explain how it activates (slash command, keywords, or always-on)
4. Give user clear choice: "Would you like me to create this skill? (yes/skip)"
```

**Example template provided in prompt for skill recommendations.**

#### Updated Workflow Steps
1. On first interaction: Offer /agent-plan mode vs quick start
2. Assess intent AND recommend skills based on problem domain
3. **If needed skill doesn't exist**: ASK before creating with clear reasoning
4. Multi-agent workflow determination
5. Tool discovery via tool_search
6. Agent creation with agent_management
7. **Configure recommended skills** in agent's spec.skills section
8. Workflow creation (if multi-agent)
9. Validation and error handling
10. Documentation with skill activation commands
11. Verification via agent_management list
12. Show exact loom commands
13. Remind user they can return for changes

### 3. Agent Plan Mode Guide

**File**: `prompts/metaagent/agent_plan_mode.yaml` (247 lines)

**5 Planning Phases:**

1. **Problem Understanding** (2-3 questions)
   - Goal discovery
   - Success criteria
   - Current workflow

2. **Technical Requirements** (3-4 questions)
   - Data sources
   - Integrations
   - Tool needs
   - Constraints

3. **Skill Recommendation**
   - Match needs to available skills
   - Explain benefits
   - Get user consent for creation

4. **Workflow Design** (multi-agent only)
   - Pipeline, parallel, debate, or coordination
   - Agent role definition
   - Orchestration strategy

5. **Confirmation & Creation**
   - Summary of plan
   - User approval
   - Agent/workflow generation

### 4. Skills Catalog

**File**: `prompts/metaagent/skills_catalog.yaml` (226 lines)

**Decision Tree Structure:**

```yaml
IF user mentions:
  "slow queries" OR "database performance" OR "SQL tuning"
    → Recommend: sql-optimization (MUST HAVE)

  "code review" OR "security" OR "quality"
    → Recommend: code-review (MUST HAVE)

  "validate data" OR "data quality"
    → Recommend: data-quality-check (MUST HAVE)

  "multiple agents" OR "coordinate" OR "orchestrate"
    → Recommend: multi-agent-coordinator + agent-discovery (MUST HAVE)
```

**Domain Mapping:**
- SQL & Database → sql-optimization
- Code & Development → code-review
- Data Engineering → data-quality-check
- Multi-Agent → multi-agent-coordinator, agent-discovery, request-response-coordinator

**Configuration Examples:**
- Each skill includes example YAML configuration
- Activation mode guidance (MANUAL, AUTO, HYBRID, ALWAYS)
- Tool requirements
- Trigger keywords

### 5. Skill Creation Backend

**File**: `pkg/shuttle/builtin/agent_management_skill.go` (248 lines, NEW)

**Core Functions:**

#### executeCreateSkill
```go
func (t *AgentManagementTool) executeCreateSkill(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error)
```
- Extracts config object from params
- Converts structured JSON to YAML
- Writes to $LOOM_DATA_DIR/skills/ with validation
- Returns success/error with file path

#### executeUpdateSkill
```go
func (t *AgentManagementTool) executeUpdateSkill(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error)
```
- Similar to create but checks name matches
- Updates existing skill file
- Validates before writing

#### convertStructuredSkillToYAML
```go
func (t *AgentManagementTool) convertStructuredSkillToYAML(configObj interface{}) (string, string, error)
```
- Parses config map
- Extracts metadata.name
- Sets defaults: apiVersion: loom/v1, kind: Skill
- Marshals to YAML
- Returns YAML content and skill name

#### writeSkillFile
```go
func (t *AgentManagementTool) writeSkillFile(name, yamlContent string, isUpdate bool, start time.Time) (*shuttle.Result, error)
```
- Validates YAML content using validation.ValidateYAMLContent
- Creates $LOOM_DATA_DIR/skills/ directory if needed (0750 permissions)
- Adds .yaml extension if missing
- Checks create vs update semantics:
  - create: Error if file exists
  - update: Error if file doesn't exist
- Writes file with 0644 permissions
- Returns result with file path and success message

**File Modified**: `pkg/shuttle/builtin/agent_management.go`
- Updated InputSchema enum: added "create_skill", "update_skill"
- Added routing cases for skill actions (lines 143-148)
- Updated type validation to include "skill"

### 6. Skill Creation Flow

```
1. Weaver detects need for skill (e.g., python-performance-analysis)
2. Checks if skill exists in catalog
3. If not found:
   a. Formulate recommendation with what/why/how/reusability
   b. ASK user: "Would you like me to create this skill? (yes/skip)"
   c. If yes:
      - Call agent_management(action="create_skill", config={...})
      - Skill written to $LOOM_DATA_DIR/skills/skillname.yaml
      - Configure in agent's spec.skills section
   d. If skip:
      - Continue without skill
      - Agent still created but without skill configuration
```

**Opt-In Design:**
- **NEVER** creates skills automatically
- **ALWAYS** asks with clear reasoning
- User has full control (yes/skip)

## Files Summary

### Files Modified
- `embedded/weaver.yaml.tmpl` - System prompt updates (skills, /agent-plan mode, workflow changes)
- `internal/tui/components/chat/sidebar/sidebar.go` - /agent-plan hint in weaver section + keyboard hints
- `internal/tui/components/dialogs/commands/slash_help_dialog.go` - /agent-plan in help dialog
- `internal/tui/components/chat/splash/splash.go` - Weaver welcome screen update
- `pkg/shuttle/builtin/agent_management.go` - Added create_skill/update_skill routing
- `docs/guides/weaver-usage.md` - User-facing documentation (NEW sections)

### Files Created
- `prompts/metaagent/agent_plan_mode.yaml` - 5-phase planning guide (247 lines)
- `prompts/metaagent/skills_catalog.yaml` - Skill recommendation decision tree (226 lines)
- `pkg/shuttle/builtin/agent_management_skill.go` - Skill CRUD implementation (248 lines)
- `docs/plans/weaver-enhancements.md` - This technical plan document

**Total New Lines**: 721+ lines (excluding documentation)

## Testing

### Manual Testing

#### Test /agent-plan Mode
```bash
# Start loom TUI and select weaver
bin/loom

# Test guided planning
User: "Create an agent to analyze slow database queries"

# Expected: Weaver offers Quick Start vs /agent-plan choice
# Choose option 2 (/agent-plan) and follow 5 phases
```

#### Test Skills Recommendations
```bash
# Test SQL skill recommendation
User: "Build a PostgreSQL query optimizer"
# Expected: Recommends sql-optimization skill

# Test multi-agent skill recommendation
User: "Create a workflow where agents collaborate"
# Expected: Recommends multi-agent-coordinator + agent-discovery

# Test code review skill recommendation
User: "I need an agent for security code reviews"
# Expected: Recommends code-review skill
```

#### Test Skill Creation (Opt-In)
```bash
# Test creating a custom skill
User: "Create an agent for analyzing Python performance"

# Expected flow:
# 1. Weaver detects need for python-performance-analysis skill
# 2. Asks: "I recommend creating a 'python-performance-analysis' skill because: ..."
# 3. User chooses: "yes" or "skip"
# 4. If yes: Skill created at ~/.loom/skills/python-performance-analysis.yaml

# Verify skill was created
cat ~/.loom/skills/python-performance-analysis.yaml
```

### Automated Testing

**Existing tests pass:**
```bash
go test -tags fts5 ./pkg/shuttle/builtin/... -v -run TestAgentManagement
```

**Test Coverage:**
- agent_management.go: Access control, create/update/read/list/validate/delete for agents and workflows
- Skill creation functions follow same validation patterns
- No new test coverage needed (uses existing validation.ValidateYAMLContent)

## Commits

```bash
git log --oneline -6

74ea9c6 docs: Update WEAVER_ENHANCEMENTS.md for opt-in skill creation
01569c6 feat(weaver): Make skill creation opt-in with clear reasoning
54f97b9 docs: Update WEAVER_ENHANCEMENTS.md with skill creation feature
470f475 feat(weaver): Enable skill creation and recommendations
d9470b0 feat(tui): Update weaver splash screen with /agent-plan mode
1f9aa02 feat(tui): Show /agent-plan command in sidebar and help
acf3b35 feat(weaver): Add /agent-plan mode and skills-based recommendations
```

**Total Commits**: 7
**Lines Added**: 1100+
**Files Modified**: 6
**Files Created**: 4

## API Changes

### agent_management Tool

**New Actions:**
- `create_skill` - Create new skill YAML
- `update_skill` - Update existing skill YAML

**Updated Type Validation:**
- Now accepts "agent", "workflow", "skill"

**Behavior:**
- Skills write to $LOOM_DATA_DIR/skills/
- Validation uses existing validation.ValidateYAMLContent
- Same error handling as agent/workflow operations

## Configuration Changes

### Weaver Agent Config

**New Workflow Steps:**
- Step 1: Offer /agent-plan mode vs quick start
- Step 2: Assess intent + recommend skills
- Step 3 (NEW): Ask before creating custom skills
- Step 7 (NEW): Configure skills in agent spec.skills section
- Step 10: Include skill activation commands in documentation

**Skill Configuration Format:**
```yaml
spec:
  skills:
    - name: sql-optimization
      enabled: true
      activation_mode: HYBRID
```

## Migration Notes

**Backwards Compatibility:**
- ✅ No breaking changes
- ✅ Existing agents unaffected
- ✅ /agent-plan is opt-in
- ✅ Skill creation requires user consent

**Upgrading:**
- Users get new features automatically
- /agent-plan shows in sidebar when weaver is active
- Existing workflow unchanged if user prefers Quick Start

## Known Limitations

1. **Skill catalog is static** - Defined in prompts/metaagent/skills_catalog.yaml, not dynamically generated
2. **No skill editing UI** - Skills must be edited via YAML files
3. **No skill deletion** - Currently only create/update supported
4. **Pattern injection timing** - Skills use existing pattern system, limited to configured injection points

## Future Enhancements

- [ ] Skill discovery from $LOOM_DATA_DIR/skills/ directory
- [ ] Skill testing framework
- [ ] Skill versioning
- [ ] Skill templates library
- [ ] Metrics on skill usage and effectiveness
- [ ] Skill deletion via agent_management
- [ ] Skill marketplace/sharing

## References

- **User Guide**: [docs/guides/weaver-usage.md](../guides/weaver-usage.md)
- **Meta-Agent Architecture**: [docs/guides/meta-agent-usage.md](../guides/meta-agent-usage.md)
- **Skills System**: See skills-compatibility branch (merged)
