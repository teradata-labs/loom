# Teradata ROM (Read-Only Memory)

This directory contains foundational Teradata knowledge that agents should understand before performing analysis or query generation tasks.

## What is ROM?

ROM patterns are **fundamental domain knowledge** - core concepts, database object types, system behaviors, and architectural patterns that form the foundation for all Teradata work.

Think of ROM as:
- **Permanent knowledge** that doesn't change frequently
- **Prerequisites** for understanding Teradata systems
- **Reference material** for agent decision-making
- **Foundational concepts** that prevent common mistakes

## ROM Contents

### Database Object Types
- **join-indexes.md** - What join indexes are and why they're not directly selectable
- **tablekind-reference.md** - Complete list of all TableKind values in DBC.TablesV

### System Behaviors
- Coming soon: Volatile tables, session management, query limits

### SQL Semantics
- Coming soon: Teradata-specific SQL differences, function behaviors

## When to Use ROM

Agents should consult ROM when:
1. **Discovering tables** - Check TableKind to filter out non-selectable objects
2. **Encountering errors** - Understand why certain operations fail
3. **Making recommendations** - Ensure suggestions align with Teradata architecture
4. **Validating assumptions** - Verify expected behaviors against documented facts

## Integration with Agents

ROM documentation should be:
- Loaded into agent system prompts for foundational tasks
- Referenced when encountering unexpected database objects
- Used to validate table discovery queries
- Consulted when troubleshooting connectivity issues

---

**Principle**: Know the system's fundamentals before attempting complex operations.
