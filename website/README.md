# Loom Documentation Website

Hugo-based documentation that embeds into the `looms` binary for offline access.

## Quick Start

```bash
# Development (live preview with hot reload)
just docs-serve
# Open http://localhost:1313 in browser

# Build static HTML
just docs-build
# Output: cmd/looms/docs/public/ (4.4MB)

# Build binary with embedded docs
just build-server-with-docs
# Output: bin/looms (48MB with embedded docs)

# Test embedded docs
./bin/looms docs
# Opens browser to http://localhost:6060
```

## Architecture

```
docs/                          # Source markdown files
  ├── guides/
  ├── reference/
  └── integration/

website/                       # Hugo site
  ├── hugo.toml               # Hugo config
  ├── themes/book/            # Hugo Book theme (git submodule)
  └── content/en/docs/        # Migrated docs with frontmatter

cmd/looms/
  ├── cmd_docs.go             # 'looms docs' command
  └── docs/public/            # Built HTML (git-ignored)
      └── [Hugo output]       # Embedded via //go:embed

bin/looms                      # Final binary with embedded docs
```

## Workflow

### 1. Edit Docs

Edit markdown files in `docs/` directory as usual.

### 2. Migrate to Hugo

```bash
./scripts/migrate-docs-to-hugo.sh
```

Converts docs to Hugo format with frontmatter.

### 3. Preview

```bash
just docs-serve
```

Live preview at http://localhost:1313 with hot reload.

### 4. Build for Embedding

```bash
just docs-build
```

Generates static HTML in `cmd/looms/docs/public/`.

### 5. Build Binary

```bash
just build-server-with-docs
```

Compiles `looms` with embedded docs via `//go:embed`.

### 6. Test

```bash
./bin/looms docs --no-open --port 8080
curl http://localhost:8080/
```

## Dependencies

### Build-time (Developer)

- **Hugo** v0.152.2+ - `brew install hugo`
  - Go-based static site generator
  - Required to build docs HTML

### Runtime (End User)

- **None** - Docs are embedded in binary
  - No external servers
  - No internet required
  - Just run `looms docs`

## Theme

Using **Hugo Book** theme:
- Lightweight, clean design
- Built-in search
- Mobile responsive
- GitHub: https://github.com/alex-shpak/hugo-book
- Installed as git submodule

## Migrated Content

The migration script (`scripts/migrate-docs-to-hugo.sh`) copies docs from `docs/` to `website/content/en/docs/` and adds Hugo frontmatter:

```yaml
---
title: "Architecture"
weight: 10
---
```

## Binary Size Impact

- **Before**: ~43MB
- **After**: 48MB
- **Embedded docs**: 4.4MB (compressed to ~5MB in binary)
- **Acceptable** for single-binary distribution

## Commands

| Command | Purpose |
|---------|---------|
| `just docs-build` | Build static HTML |
| `just docs-serve` | Dev server with live reload |
| `just docs-clean` | Remove built files |
| `just build-server-with-docs` | Build binary with embedded docs |
| `looms docs` | Serve embedded docs (end user) |

## Configuration

Edit `website/hugo.toml` to customize:
- Site title
- Theme settings
- Menu items
- Search behavior

## Adding Content

1. Add markdown to `docs/guides/MY_DOC.md`
2. Run `./scripts/migrate-docs-to-hugo.sh`
3. Preview with `just docs-serve`
4. Build with `just docs-build`

## Notes

- Hugo output is git-ignored (regenerated at build time)
- Theme is a git submodule (tracked in `.gitmodules`)
- Original `docs/` files remain primary source
- Migration script is idempotent (safe to re-run)
