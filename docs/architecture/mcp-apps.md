# MCP Apps Architecture

How the MCP Apps system compiles declarative JSON specs into secure, standalone HTML documents.

**Status**: ✅ Implemented (v1.1.0)

## Table of Contents

- [Overview](#overview)
- [System Components](#system-components)
- [Compilation Pipeline](#compilation-pipeline)
- [Registry and Storage](#registry-and-storage)
- [Security Model](#security-model)
- [API Surface](#api-surface)
- [Design Decisions](#design-decisions)

## Overview

MCP Apps are interactive HTML dashboards built from a declarative JSON spec. The system converts a component tree into standalone HTML pages using a Go template pipeline with embedded CSS and JavaScript. Apps are stored in an in-memory registry and served as MCP resources to clients like Claude Desktop.

```
 Spec (JSON)                                                     HTML
 ┌─────────────────┐    ┌───────────┐    ┌──────────────┐    ┌──────────────┐
 │ {version, title, │───>│ Validate  │───>│   Compile    │───>│  Standalone  │
 │  components: [   │    │ (security │    │ (template +  │    │  HTML page   │
 │   {type, props}  │    │  + limits)│    │  runtime.js) │    │  (~30-50KB)  │
 │  ]}              │    └───────────┘    └──────────────┘    └──────────────┘
 └─────────────────┘                                               │
                                                                   v
                                                          ┌────────────────┐
                                                          │   Registry     │
                                                          │ (in-memory)    │
                                                          │ ui://loom/name │
                                                          └────────────────┘
```

## System Components

### Component Catalog (`pkg/mcp/apps/catalog.go`)

Defines the 14 built-in component types across 3 categories:

| Category | Components |
|----------|-----------|
| Display | stat-cards, chart, table, key-value, text, code-block, progress-bar, badges, heatmap |
| Layout | header, section, tabs |
| Complex | dag, message-list |

Each catalog entry includes:
- Type name and category
- Description
- Props JSON schema (for LLM tool discovery)
- Whether the type supports `children` (only `section` and `tabs`)
- Example JSON

The catalog is used by:
1. **Compiler validation**: Reject unknown component types
2. **ListComponentTypes RPC/tool**: Let LLMs discover the available components
3. **Children validation**: Only `section` and `tabs` accept child components

### Compiler (`pkg/mcp/apps/compiler.go`)

The `Compiler` struct holds:
- A `ComponentCatalog` (created once at startup)
- A parsed `html/template.Template` from the embedded app template
- The embedded `runtime.js` as a string

Key methods:
- `Validate(spec)` — checks structural limits and security constraints
- `Compile(spec)` — validates, marshals spec to JSON, renders template
- `ListComponentTypes()` — returns proto `ComponentType` messages for discovery

### Registry (`pkg/mcp/apps/registry.go`)

The `UIResourceRegistry` stores all apps in a `map[string]*UIResource` protected by `sync.RWMutex`. It manages both:
- **Embedded apps** (4 built-in, registered at startup, cannot be deleted)
- **Dynamic apps** (agent-created, up to 100, 50 MB total)

The registry provides:
- MCP resource interface: `List()`, `Read(uri)`
- gRPC server interface: `ListAppInfo()`, `GetAppHTML()`, `CreateApp()`, `UpdateApp()`, `DeleteApp()`
- Change notifications: `SetOnChange(fn)` for cache invalidation

### App Template (`pkg/mcp/apps/html/app-template.html`)

An embedded HTML template (~1000 lines of CSS) that provides:
- Tokyonight Dark theme with 6 named colors
- CSS for all 14 component types
- Responsive grid layouts
- CSP meta tag restricting script sources

### Runtime (`pkg/mcp/apps/html/runtime.js`)

An embedded JavaScript file (~600 lines) that:
- Parses the spec from `<script type="application/json" id="app-spec">`
- Renders components using `document.createElement` + `textContent` (no `innerHTML`)
- Creates Chart.js charts (loaded via CDN with SRI hash)
- Renders DAGs as SVG using a custom topological layout engine
- Handles tab switching, collapsible sections, and sortable tables

## Compilation Pipeline

### Step 1: Validation

```
Validate(spec)
├── Check version == "1.0"
├── Check total spec JSON size ≤ 512 KB
├── Check layout ∈ {stack, grid-2, grid-3, ""}
├── Check components ≥ 1
└── validateComponents(components, depth=0, count=0)
    ├── Check depth ≤ 10
    ├── For each component:
    │   ├── count++ → check ≤ 50
    │   ├── Check type in catalog
    │   ├── Check props JSON size ≤ 64 KB
    │   ├── sanitizeStruct(props):
    │   │   ├── Reject keys: __proto__, constructor, prototype
    │   │   ├── Reject value prefixes: javascript:, vbscript:, data:text/html
    │   │   └── Reject CSS patterns: url(, expression(, @import
    │   └── If children: check catalog.HasChildren(type), recurse
    └── Return first error or nil
```

### Step 2: Compilation

```
Compile(spec)
├── Validate(spec)                         // full validation
├── protojson.Marshal(spec)                // spec → JSON string
├── Escape <, >, & → \u003c, \u003e, \u0026  // prevent </script> injection
├── template.Execute({Title, SpecJSON, Runtime})
│   ├── Title: spec.Title or "Loom App"
│   ├── SpecJSON: sanitized JSON (as template.JS)
│   └── Runtime: embedded runtime.js (as template.JS)
└── Return HTML bytes
```

The output is a single HTML document containing:
1. CSP meta tag
2. All CSS inlined in `<style>`
3. Spec JSON in `<script type="application/json">`
4. Runtime JS inlined in `<script>`

No external dependencies except Chart.js (loaded via CDN with SRI).

## Registry and Storage

### Embedded Apps

Registered at server startup by `RegisterEmbeddedApps()`:

| App | URI |
|-----|-----|
| Conversation Viewer | `ui://loom/conversation-viewer` |
| Data Chart | `ui://loom/data-chart` |
| EXPLAIN Plan Visualizer | `ui://loom/explain-plan-visualizer` |
| Data Quality Dashboard | `ui://loom/data-quality-dashboard` |

These are compiled from hand-written HTML files embedded via `//go:embed`. They cannot be updated or deleted.

### Dynamic Apps

Created at runtime through any API surface. Stored in the same registry map with `Dynamic: true`.

**Capacity limits** (enforced atomically under write lock):
- Max 100 dynamic apps
- Max 50 MB total HTML bytes across all dynamic apps

**Atomic operations**: `CreateApp` and `UpdateApp` use a single write lock for the check-and-mutate sequence, preventing TOCTOU race conditions. The `upsertLocked()` helper performs the actual map mutation while the caller holds the lock.

**Change notifications**: After each mutation, the registry calls `onChange()` outside the lock. This is used by the MCP server to notify clients of resource list changes via MCP notifications.

### URI Scheme

All apps use the `ui://loom/<name>` URI scheme. The short name is extracted from the URI by taking everything after the last `/`.

## Security Model

### Input Validation

1. **Prototype pollution prevention**: Keys `__proto__`, `constructor`, `prototype` rejected at any nesting depth
2. **Script injection prevention**: String values starting with `javascript:`, `vbscript:`, `data:text/html` rejected
3. **CSS injection prevention**: String values containing `url(`, `expression(`, `@import` rejected
4. **Size limits**: 512 KB total spec, 64 KB per component props, 50 components max, 10 levels nesting max

### Output Sanitization

1. **HTML entity escaping**: `<`, `>`, `&` in spec JSON replaced with `\u003c`, `\u003e`, `\u0026` before template embedding
2. **Content-Security-Policy**: `default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; form-action 'none'`
3. **Chart.js SRI**: Loaded from `cdn.jsdelivr.net` with a pinned Subresource Integrity hash (v4.4.7)

### Runtime Safety

1. **DOM safety**: All component rendering uses `document.createElement` + `textContent`. No `innerHTML` anywhere.
2. **SVG safety**: DAG rendering uses strict element allowlist (12 elements: `svg`, `g`, `rect`, `circle`, `line`, `path`, `text`, `tspan`, `defs`, `marker`, `polygon`, `polyline`) and attribute allowlist (30 attributes)
3. **No network access**: `connect-src 'none'` prevents the app from making any network requests
4. **No form submission**: `form-action 'none'` prevents form-based attacks

### App Name Validation

- Must match `^[a-z0-9][a-z0-9-]{0,62}$`
- Reserved names (e.g., `component-types`) are rejected to prevent HTTP route collisions

## API Surface

The system exposes three parallel API surfaces, all backed by the same compiler and registry:

### 1. gRPC/HTTP RPCs (`pkg/server/apps_rpc.go`)

6 RPCs in `LoomService`:

| RPC | HTTP | Description |
|-----|------|-------------|
| `ListUIApps` | `GET /v1/apps` | List all apps |
| `GetUIApp` | `GET /v1/apps/{name}` | Get app metadata + HTML |
| `CreateUIApp` | `POST /v1/apps` | Create from spec |
| `UpdateUIApp` | `PUT /v1/apps/{name}` | Update existing |
| `DeleteUIApp` | `DELETE /v1/apps/{name}` | Delete dynamic app |
| `ListComponentTypes` | `GET /v1/apps/component-types` | Component catalog |

### 2. MCP Bridge Tools (`pkg/mcp/server/bridge_tools.go`, `pkg/mcp/server/bridge_handlers.go`)

4 tools exposed via the MCP protocol (for Claude Code, Cursor, etc.):

| Tool | Maps to RPC |
|------|------------|
| `loom_create_app` | `CreateUIApp` |
| `loom_update_app` | `UpdateUIApp` |
| `loom_delete_app` | `DeleteUIApp` |
| `loom_list_component_types` | `ListComponentTypes` |

### 3. Agent Tools (`pkg/server/app_tools.go`)

4 `shuttle.Tool` implementations auto-registered to all server-side agents:

| Tool | Description |
|------|-------------|
| `create_ui_app` | Create app from spec (includes spec format in tool description) |
| `update_ui_app` | Update existing app |
| `delete_ui_app` | Delete dynamic app |
| `list_component_types` | Discover component catalog |

These tools convert `map[string]interface{}` params from LLM output to `*loomv1.UIAppSpec` via JSON round-trip with `protojson.UnmarshalOptions{DiscardUnknown: true}`, which tolerates extra fields LLMs sometimes produce.

## Design Decisions

### Why declarative specs, not raw HTML?

1. **Security**: Raw HTML allows arbitrary script injection. The spec is validated and sanitized before compilation.
2. **Consistency**: All apps share the same Tokyonight Dark theme and design tokens.
3. **LLM ergonomics**: JSON specs are easier for LLMs to generate correctly than full HTML documents.
4. **Validation**: Structural limits and type checking catch errors before rendering.

### Why inline everything in one HTML file?

MCP resources are served as single documents. The compiled HTML must be self-contained (no asset loading from the server). The only exception is Chart.js, loaded from a CDN with SRI integrity verification.

### Why `textContent` instead of `innerHTML`?

Defense-in-depth. Even though the spec is validated, using `textContent` eliminates any possibility of HTML injection from spec values reaching the DOM.

### Why auto-register agent tools?

Any agent might need to create visualizations from query results or analysis output. Requiring configuration per-agent would be an unnecessary barrier. The tools are lightweight (no state, no side effects beyond the registry) and safe to register universally.

### Why in-memory registry, not SQLite?

Dynamic apps are ephemeral by design — they're created during agent conversations and viewed in the same session. Persistence across server restarts is not a requirement. The in-memory registry keeps the implementation simple with no database dependencies.
