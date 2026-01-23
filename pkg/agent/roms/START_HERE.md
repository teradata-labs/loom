# START HERE - Agent Instructions

## Agent management

IF YOU ARE NOT THE weaver, skip this section.  If you ARE the weaver, always make sure that you use shell_execute ONLY to explore examples and documentation for clues on how to be the best weaver ever.  In order to create agents, NEVER use shell_execute or the workspace tool.  Instead, use the agent_management tool.

---

## The `workspace` tool

Your `workspace` tool is a great way of managing files. **Artifacts** can be used for results or information that you want to share with other agents in a workflow, or the user. Artifacts are indexed, searchable, and persistent. **Scratchpad** is for fast, ephemeral note taking or temporary work.

---

## Other tool discovery

**Always use `tool_search` first when you need a tool that's not in your registry.**

tool_search(query="shell")     # Find shell tools
tool_search(query="awesome")   # Find awesome-related tools

**Critical:**
- Call discovered tools directly by name (e.g., `awesome_tool_call`)
- Don't invoke via shell_execute (no `mcp-server ...` commands)
- Tool names include full namespace so follow what the tool_search tool returns.

---

**After every successful query_tool_result execution, YOU MUST NARRATE what you found (explanatory text)** **Never chain tool calls silently** - Always provide explanatory text between tool executions
**Always validate schema** - Verify column names exist before querying

---

## ⚠️ Common Mistakes

1. **Chaining tool calls silently** → Narrate findings before continuing
2. **Not discovering tools** → Use tool_search first if you don't have the proper tool
3. **Fabricating data in reports** → Reference verified findings from memory, tool results, scratchpad, and artifacts.
4. **Trying to retrieve all data** → Use filtering/pagination
5. **Wrong agent IDs in workflows** → Use full `workflow:agent` format
6. **Polling for messages from other agents** → Message receipt is automatic.  Chill, you will be notified.
7. **Using scratchpad for sharing** → Use artifacts instead