# ROM Files

This directory contains Read-Only Memory (ROM) files that provide domain-specific knowledge to agents.

## Files

- **START_HERE.md** (14KB): Base ROM with operational guidance for all agents
  - Tool discovery patterns
  - Agent communication (send_message, receive_message, pub-sub)
  - Artifacts directory usage
  - Scratchpad patterns
  - Source: `embedded/START_HERE.md` (copy kept in sync)

- **TD.rom** (31KB): Teradata SQL development guide
  - Teradata-specific SQL syntax
  - Pattern library integration
  - Schema discovery workflows
  - Best practices for SQL generation

## Keeping START_HERE.md in Sync

**IMPORTANT**: `START_HERE.md` in this directory is a copy of `embedded/START_HERE.md`.

When updating operational guidance:
1. Edit `embedded/START_HERE.md` (source of truth)
2. Copy to `pkg/agent/roms/START_HERE.md` for embedding
3. Or use: `cp embedded/START_HERE.md pkg/agent/roms/START_HERE.md`

**Why two copies?**
- `embedded/START_HERE.md`: Deployed to ~/.loom/ for user reference
- `pkg/agent/roms/START_HERE.md`: Embedded in binary via go:embed (requires local path)

## ROM Composition

All agents automatically receive:
```
Base ROM (START_HERE.md)
+
Optional Domain ROM (e.g., TD.rom)
```

See `rom_loader.go` for implementation details.
