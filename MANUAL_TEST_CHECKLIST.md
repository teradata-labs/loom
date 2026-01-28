# TUI UX Redesign - Manual Test Checklist

## Phase 5: Manual Testing Checklist

### Prerequisites
- [ ] `loom-server` running
- [ ] Clean agent directory (`~/.loom/agents/` only has weaver.yaml and guide.yaml)

### Server Connection Tests
- [ ] **No server running**: Stop `loom-server`, launch `loom`
  - Expected: "No Server Running" splash with instructions
- [ ] **Start server while TUI running**: Start `loom-server` while TUI is on splash
  - Expected: Auto-connects to Guide within 2 seconds
- [ ] **Server running**: Launch `loom` with server already running
  - Expected: Guide splash shows immediately

### Guide Tests
- [ ] **Guide splash**: Verify splash text mentions "Ask me to help you find..."
- [ ] **Type message to guide**: "I need help with SQL queries"
  - Expected: Guide responds with suggestions or lists agents
- [ ] **Guide suggests agents**: If guide suggests agents via clarification dialog
  - Expected: Can select an agent and switch to it
  - Expected: Clarification dialog closes after selection

### Sidebar Tests
- [ ] **Sidebar shows only 3 sections**: Weaver, MCP Servers, Patterns
  - Expected: No "Agents" or "Workflows" sections visible
- [ ] **Navigate sidebar with arrow keys**: Use up/down to cycle through sections
  - Expected: Cycles through Weaver → MCP → Patterns → Weaver (no crashes)
- [ ] **Click Weaver in sidebar**: Click on "Weaver" section
  - Expected: Switches to weaver agent, splash shows weaver info
- [ ] **MCP section**: Expand MCP servers (if any configured)
  - Expected: MCP servers list works as before
- [ ] **Patterns section**: Expand pattern categories (if any exist)
  - Expected: Patterns work as before

### Agent Selection Modal (ctrl+e) Tests
- [ ] **Press ctrl+e**: Press ctrl+e anywhere in TUI
  - Expected: Agent selection modal opens with fuzzy search
- [ ] **Modal shows regular agents**: Check that list shows only regular agents
  - Expected: Excludes weaver, guide, coordinators, and sub-agents
- [ ] **Fuzzy search**: Type "sql" in search box
  - Expected: Filters to agents matching "sql"
- [ ] **Select agent**: Press enter on an agent
  - Expected: Modal closes, switches to selected agent
- [ ] **Press esc**: Press esc to cancel
  - Expected: Modal closes without changing agent
- [ ] **Press ctrl+e again**: Press ctrl+e while modal is open
  - Expected: Modal closes (toggle behavior)

### Workflow Selection Modal (ctrl+w) Tests
- [ ] **Press ctrl+w**: Press ctrl+w anywhere in TUI
  - Expected: Workflow selection modal opens
- [ ] **Modal shows workflows**: Check that list shows workflows with sub-agent counts
  - Expected: Shows "workflow-name (N agents)" format
- [ ] **Fuzzy search**: Type search term
  - Expected: Filters workflows
- [ ] **Select workflow**: Press enter on a workflow coordinator
  - Expected: Modal closes, switches to workflow coordinator agent
- [ ] **Press esc**: Press esc to cancel
  - Expected: Modal closes without changing
- [ ] **No workflows**: If no workflows exist
  - Expected: Shows "No workflows found" or empty list

### Keyboard Shortcuts Tests
- [ ] **Bottom toolbar shows ctrl+e**: Check status bar
  - Expected: Shows "ctrl+e agents" in help bar
- [ ] **Bottom toolbar shows ctrl+w**: Check status bar
  - Expected: Shows "ctrl+w workflows" in help bar
- [ ] **Press ctrl+g**: Open more help
  - Expected: Full help shows ctrl+e and ctrl+w with descriptions

### Weaver Auto-Switch Tests
- [ ] **Switch to weaver**: Use sidebar or ctrl+e to switch to weaver
  - Expected: Weaver agent active
- [ ] **Ask weaver to create agent**: "Create a SQL optimizer agent"
  - Expected: Weaver uses agent_management tool to create agent
- [ ] **Auto-switch happens**: Wait for weaver to finish creating agent
  - Expected: TUI automatically switches to new agent within 500ms
  - Expected: Splash shows new agent name
  - Expected: Session history is empty (fresh agent conversation)
- [ ] **Verify agent exists**: Press ctrl+e
  - Expected: New agent appears in agent list
- [ ] **Multiple agent creations**: Ask weaver to create another agent
  - Expected: Auto-switches to second new agent as well

### Weaver Auto-Switch Edge Cases
- [ ] **Weaver creates workflow (not agent)**: Ask weaver to create a workflow
  - Expected: No auto-switch (stays on weaver)
- [ ] **Weaver updates existing agent**: Ask weaver to update an existing agent
  - Expected: No auto-switch (stays on weaver)
- [ ] **Very slow hot-reload**: Create agent while system is under load
  - Expected: Retries up to 3 times (500ms total), then shows warning if not found
- [ ] **Agent already exists error**: Ask weaver to create agent that already exists
  - Expected: Weaver reports error, no auto-switch attempt

### Session Persistence Tests
- [ ] **Switch between agents**: Talk to guide → switch to weaver → switch back to guide
  - Expected: Previous conversations retained for each agent
- [ ] **New agent has no history**: Auto-switch to newly created agent
  - Expected: Conversation starts fresh (no old messages)
- [ ] **Session IDs persist**: Check that each agent has its own session
  - Expected: agentSessions map maintains separate session IDs per agent

### Backward Compatibility Tests
- [ ] **loom --thread weaver**: Launch with `loom --thread weaver`
  - Expected: Starts directly on weaver (skips guide)
- [ ] **loom --thread <agent-id>**: Launch with specific agent ID
  - Expected: Starts on that agent if it exists
- [ ] **loom --thread invalid**: Launch with non-existent agent
  - Expected: Falls back to guide or shows error

### Compact Mode Tests
- [ ] **Resize terminal to small**: Make terminal narrow (< 120 width) or short (< 30 height)
  - Expected: TUI switches to compact mode
- [ ] **Ctrl+e in compact mode**: Press ctrl+e
  - Expected: Agent modal works correctly
- [ ] **Ctrl+w in compact mode**: Press ctrl+w
  - Expected: Workflow modal works correctly
- [ ] **Sidebar hidden in compact**: Verify sidebar is hidden
  - Expected: More screen space for chat/editor

### Error Handling Tests
- [ ] **Agent coordinator unavailable**: Stop server while TUI running
  - Expected: Graceful error messages, no crashes
- [ ] **Empty agent list**: Delete all agents except guide/weaver, press ctrl+e
  - Expected: Shows empty or minimal list
- [ ] **Keyboard shortcuts while agent busy**: Press ctrl+e while agent is streaming
  - Expected: Modal still opens (or reasonable behavior)

### Visual/UI Tests
- [ ] **No visual glitches**: Navigate through all features
  - Expected: No flickering, overlapping text, or layout issues
- [ ] **Modal centering**: Open modals on different screen sizes
  - Expected: Modals centered properly
- [ ] **Help text formatting**: Check all help bars and descriptions
  - Expected: Text fits, no truncation issues

### Performance Tests
- [ ] **Large agent list**: Create 20+ test agents, press ctrl+e
  - Expected: Modal opens quickly, search is responsive
- [ ] **Fuzzy search speed**: Type rapidly in search box
  - Expected: No lag, filters update smoothly
- [ ] **Auto-switch delay**: Measure time from agent creation to switch
  - Expected: < 500ms in normal conditions

## Test Results Summary

Date tested: ___________
Tester: ___________

Total tests: 50+
Passed: ___________
Failed: ___________

### Issues Found

1. _________________________________________________________
2. _________________________________________________________
3. _________________________________________________________

### Notes

_________________________________________________________________
_________________________________________________________________
_________________________________________________________________
