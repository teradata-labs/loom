# ROM Files

This directory contains Read-Only Memory (ROM) files that provide domain-specific knowledge to agents.

**This is the single source of truth for all ROM files.**

## Files

- **START_HERE.md** (5KB): Base ROM with operational guidance for all agents
  - Tool discovery patterns
  - Progressive disclosure (tool result management)
  - Agent communication (send_message, receive_message, pub-sub)
  - Artifacts directory usage
  - Scratchpad patterns
  - **This file is the source of truth** - embedded into binary and deployed to ~/.loom/

- **TD.rom** (31KB): Teradata SQL development guide
  - Teradata-specific SQL syntax
  - Pattern library integration
  - Schema discovery workflows
  - Best practices for SQL generation

## Editing ROM Files

**To update operational guidance:**
1. Edit `pkg/agent/roms/START_HERE.md` directly (this file)
2. Rebuild: `just build`
3. The updated ROM is automatically:
   - Embedded into the binary (via go:embed in rom_loader.go)
   - Deployed to ~/.loom/START_HERE.md (via embedded.GetStartHere())

**No copying required** - there is only one source file.

## ROM Composition

All agents automatically receive:
```
Base ROM (START_HERE.md)
+
Optional Domain ROM (e.g., TD.rom)
```

See `rom_loader.go` for implementation details.
