> **Status: COMPLETED** — All enhancements implemented. This plan is archived for historical reference.

# Weaver Enhancements Implementation Plan

**Branch**: `weaver-agent-plan-mode`
**Version**: v1.2.0
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

#### Tools Section
- Extended to cover agent/workflow/skill YAMLs (agent_management tool description)

#### New: Skill Creation Section
```yaml
## Skill Creation
- Use agent_management with action="create_skill" / action="update_skill"
- Skill structure: apiVersion: loom/v1, kind: Skill, metadata (name, domain), prompt (instructions)
- Activation modes: MANUAL (/command), AUTO (keyword-based), HYBRID (both), ALWAYS (every turn)
- **ALWAYS ask before creating a new skill. Never create skills without user consent.**
```

> Note: The consent requirement is embedded within the Skill Creation section, not as a separate "Skill Recommendation Guidelines" section. Detailed skill recommendation logic lives in `prompts/metaagent/skills_catalog.yaml` and `prompts/metaagent/agent_plan_mode.yaml`, not in the weaver system prompt itself.

#### Updated Workflow Steps (matches `embedded/weaver.yaml.tmpl`)
1. On first interaction: Offer /agent-plan mode vs quick start
2. Assess intent and recommend relevant skills (SQL/Database, Code quality, Data validation, Multi-agent). If needed skill doesn't exist: ASK before creating
3. If multi-agent, determine workflow type (Coordination vs Orchestration)
4. Use shell_execute to find relevant example agents as inspiration
5. Use tool_search to discover tools for agents
6. Create agents with agent_management (action="create_agent") - one call per agent
7. **Configure skills** in agent's spec.skills section (enabled: true)
8. Create workflow with agent_management (action="create_workflow") referencing agents
9. Fix validation errors using returned error messages, then retry
10. Create user docs in workspace showing how to use what you built
11. Verify with agent_management (action="list")
12. Use shell_execute to run `loom --help` for exact run commands
13. Let user know they can return for changes

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

**File**: `pkg/shuttle/builtin/agent_management_skill.go` (246 lines, NEW)

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
- Writes file with 0600 permissions (owner read/write only)
- Returns result with file path and success message

**File Modified**: `pkg/shuttle/builtin/agent_management.go`
- Updated InputSchema WithEnum: added "create_skill", "update_skill" (line 89)
- Added routing cases for skill actions in Execute switch (lines 145-152)
- Updated type validation to accept "agent", "workflow", or "skill" (line 168)

> ⚠️ Known gap: The `read`, `list`, `validate`, and `delete` handlers only branch on "agent" vs everything-else (→workflows). Passing `type: "skill"` is accepted by validation but incorrectly routes to the workflows directory.

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
- `pkg/shuttle/builtin/agent_management_skill.go` - Skill CRUD implementation (246 lines)
- `docs/plans/weaver-enhancements.md` - This technical plan document

**Total New Lines**: 719 lines across 3 new source files (excluding documentation and modifications to existing files)

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
- ✅ agent_management.go: Access control, create/update/read/list/validate/delete for agents and workflows (agent_management_test.go, agent_management_structured_test.go)
- ⚠️ Skill creation functions follow same validation patterns but have no dedicated tests
- 🚧 Skill CRUD logic in agent_management_skill.go (246 lines) needs unit tests (create/update/validation paths)

## Commits

```bash
git log --oneline -7

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
- Skills write to $LOOM_DATA_DIR/skills/ (via `create_skill` and `update_skill` actions only)
- Validation uses existing validation.ValidateYAMLContent
- Same error handling as agent/workflow operations

> ⚠️ The default case error message in `Execute()` (line 195) does not list `create_skill`/`update_skill` as valid actions. This is cosmetic -- the actions work, but an invalid action error message is incomplete.

## Configuration Changes

### Weaver Agent Config

**New Workflow Steps (13 total, see `embedded/weaver.yaml.tmpl`):**
- Step 1: Offer /agent-plan mode vs quick start
- Step 2 (UPDATED): Assess intent + recommend skills + ask before creating missing skills
- Step 7 (NEW): Configure skills in agent spec.skills section (enabled: true)
- Step 10: Create user docs in workspace showing how to use what you built

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
3. **Skill read/list/delete routes to wrong directory** - Type validation accepts "skill" for read/list/validate/delete actions, but the handlers only branch on "agent" vs else (→workflows). Passing `type: "skill"` silently looks in the workflows directory instead of skills. Only `create_skill` and `update_skill` (dedicated actions) correctly use `$LOOM_DATA_DIR/skills/`.
4. **Pattern injection timing** - Skills use existing pattern system, limited to configured injection points
5. **No unit tests for skill CRUD** - `agent_management_skill.go` has no dedicated test file

## Future Enhancements

- [ ] Fix read/list/validate/delete handlers to properly route `type: "skill"` to `$LOOM_DATA_DIR/skills/`
- [ ] Add unit tests for skill CRUD in agent_management_skill.go
- [ ] Skill discovery from $LOOM_DATA_DIR/skills/ directory
- [ ] Skill testing framework
- [ ] Skill versioning
- [ ] Skill templates library
- [ ] Metrics on skill usage and effectiveness
- [ ] Skill marketplace/sharing

## References

- **User Guide**: [docs/guides/weaver-usage.md](../guides/weaver-usage.md)
- **Meta-Agent Architecture**: [docs/guides/meta-agent-usage.md](../guides/meta-agent-usage.md)
- **Skills System**: See skills-compatibility branch (merged)
