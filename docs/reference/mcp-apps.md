# MCP Apps Reference

Declarative spec format for building interactive UI apps rendered as MCP resources.

**Status**: ✅ Implemented (v1.1.0)

## Table of Contents

- [Overview](#overview)
- [Spec Format](#spec-format)
- [Layouts](#layouts)
- [Component Types](#component-types)
  - [Display Components](#display-components)
  - [Layout Components](#layout-components)
  - [Complex Components](#complex-components)
- [Colors](#colors)
- [Limits](#limits)
- [APIs](#apis)
  - [gRPC RPCs](#grpc-rpcs)
  - [MCP Tools](#mcp-tools)
  - [Agent Tools](#agent-tools)
- [Security Constraints](#security-constraints)

## Overview

MCP Apps are standalone HTML documents compiled from a declarative JSON spec. They can be created via:

- **gRPC**: `CreateUIApp` / `UpdateUIApp` / `DeleteUIApp` RPCs
- **MCP tools**: `loom_create_app` / `loom_update_app` / `loom_delete_app` (for Claude Code, Cursor, etc.)
- **Agent tools**: `create_ui_app` / `update_ui_app` / `delete_ui_app` (auto-registered to all server-side agents)

Apps are served as MCP resources at `ui://loom/<name>` and via HTTP at `/apps/<name>`.

There are 4 built-in (embedded) apps and up to 100 dynamic apps created at runtime.

## Spec Format

```json
{
  "version": "1.0",
  "title": "Dashboard Title",
  "description": "Optional subtitle",
  "badge": "Live",
  "layout": "stack",
  "components": [
    {
      "type": "header",
      "props": { "title": "My Dashboard" }
    },
    {
      "type": "chart",
      "props": { "chartType": "bar", "labels": ["A", "B"], "datasets": [...] }
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `version` | string | Yes | Must be `"1.0"` |
| `title` | string | No | HTML `<title>` and header fallback |
| `description` | string | No | Subtitle shown below the title |
| `badge` | string | No | Badge text in the header (default: `"MCP App"`) |
| `layout` | string | No | Top-level layout mode (default: `"stack"`) |
| `components` | array | Yes | Array of `UIComponent` objects (at least 1) |
| `data_type` | string | No | postMessage type for live data updates |

Each component has:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | Component type from the catalog below |
| `props` | object | No | Type-specific properties |
| `children` | array | No | Nested components (only for layout types: `section`, `tabs`) |
| `id` | string | No | ID for targeting with postMessage data updates |

## Layouts

The `layout` field controls how top-level components are arranged:

| Layout | Description |
|--------|-------------|
| `stack` | Vertical stack (default). Components fill the full width. |
| `grid-2` | Two-column grid. Responsive: collapses to single column on narrow screens. |
| `grid-3` | Three-column grid. Responsive: collapses to single column on narrow screens. |

## Component Types

14 built-in component types across 3 categories.

### Display Components

#### `stat-cards`

Row of KPI stat cards with label, value, optional color and sublabel.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `items` | array | Yes | Array of `{label, value, color?, sublabel?}` |

Each item:
- `label` (string, required): Metric name
- `value` (string, required): Display value
- `color` (string): Named color or hex
- `sublabel` (string): Delta or context text

```json
{
  "type": "stat-cards",
  "props": {
    "items": [
      {"label": "Total Revenue", "value": "$4.54M", "color": "success"},
      {"label": "Active Users", "value": "2,100", "color": "accent"}
    ]
  }
}
```

---

#### `chart`

Chart.js chart supporting 8 chart types.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `chartType` | string | Yes | `bar`, `line`, `pie`, `doughnut`, `radar`, `polarArea`, `scatter`, `bubble` |
| `title` | string | No | Chart title |
| `labels` | array | Yes | X-axis labels (array of strings) |
| `datasets` | array | Yes | Array of `{label, data, color?}` |
| `fill` | boolean | No | Fill area under line charts |
| `stacked` | boolean | No | Stack bar/line datasets |

Each dataset:
- `label` (string, required): Legend label
- `data` (array of numbers, required): Data values
- `color` (string): Named color or hex

```json
{
  "type": "chart",
  "props": {
    "chartType": "bar",
    "title": "Monthly Revenue",
    "labels": ["Jan", "Feb", "Mar"],
    "datasets": [
      {"label": "Revenue", "data": [320, 380, 410], "color": "accent"}
    ]
  }
}
```

---

#### `table`

Data table with columns, rows, optional sorting and max height.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | No | Table title |
| `columns` | array | Yes | Column headers (array of strings) |
| `rows` | array | Yes | 2D array of strings (each row is an array) |
| `sortable` | boolean | No | Enable click-to-sort on column headers |
| `maxHeight` | string | No | CSS max-height (e.g., `"400px"`) |

```json
{
  "type": "table",
  "props": {
    "title": "Top Customers",
    "columns": ["Customer", "Revenue"],
    "rows": [["Acme Corp", "$1.2M"], ["GlobalTech", "$890K"]],
    "sortable": true
  }
}
```

---

#### `key-value`

Key-value metadata pairs in grid or list layout.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | No | Section title |
| `items` | array | Yes | Array of `{key, value, color?}` |
| `layout` | string | No | `"grid"` (default) or `"list"` |

```json
{
  "type": "key-value",
  "props": {
    "title": "Database Info",
    "items": [
      {"key": "Host", "value": "prod-db-01"},
      {"key": "Status", "value": "Online", "color": "success"}
    ]
  }
}
```

---

#### `text`

Text block with optional styling.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `content` | string | Yes | Text content |
| `style` | string | No | `"default"`, `"note"`, `"warning"`, or `"error"` |

```json
{
  "type": "text",
  "props": {
    "content": "Analysis complete. 3 anomalies detected.",
    "style": "warning"
  }
}
```

---

#### `code-block`

Monospace code display with optional title and language hint.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `code` | string | Yes | Code content |
| `title` | string | No | Title above the block |
| `language` | string | No | Language hint (e.g., `"sql"`, `"json"`) |

```json
{
  "type": "code-block",
  "props": {
    "title": "Generated SQL",
    "language": "sql",
    "code": "SELECT customer_id, SUM(amount) FROM orders GROUP BY 1"
  }
}
```

---

#### `progress-bar`

Percentage progress bars with labels and colored fills.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | No | Section title |
| `items` | array | Yes | Array of `{label, value, color?}` |
| `thresholds` | object | No | Auto-color thresholds: `{warning: 60, error: 80}` |

Each item:
- `label` (string, required): Bar label
- `value` (number, required): 0-100 percentage
- `color` (string): Named color or hex

```json
{
  "type": "progress-bar",
  "props": {
    "title": "Storage Usage",
    "items": [
      {"label": "Disk", "value": 72, "color": "warning"},
      {"label": "Memory", "value": 45, "color": "success"}
    ]
  }
}
```

---

#### `badges`

Inline colored status badges/pills.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `items` | array | Yes | Array of `{text, color}` |

```json
{
  "type": "badges",
  "props": {
    "items": [
      {"text": "Production", "color": "success"},
      {"text": "v2.1.0", "color": "accent"},
      {"text": "3 Warnings", "color": "warning"}
    ]
  }
}
```

---

#### `heatmap`

Color-coded grid with row labels, column labels, and numeric values.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | No | Heatmap title |
| `rowLabels` | array | Yes | Row labels (array of strings) |
| `columnLabels` | array | Yes | Column labels (array of strings) |
| `values` | array | Yes | 2D array of numbers (rows x columns) |
| `colorScale` | string | No | `"blue"` (default), `"green"`, or `"red"` |

```json
{
  "type": "heatmap",
  "props": {
    "title": "Query Latency (ms)",
    "rowLabels": ["Mon", "Tue", "Wed"],
    "columnLabels": ["Morning", "Afternoon", "Evening"],
    "values": [[120, 150, 90], [200, 180, 110], [95, 130, 85]]
  }
}
```

---

### Layout Components

#### `header`

App header with title, optional description and badge.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | Yes | Header title |
| `description` | string | No | Subtitle text |
| `badge` | string | No | Badge text |

```json
{
  "type": "header",
  "props": {
    "title": "Revenue Analysis",
    "description": "Q1 2026 Summary",
    "badge": "Live"
  }
}
```

---

#### `section`

Collapsible section grouping child components. **Supports `children`.**

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | Yes | Section title |
| `subtitle` | string | No | Subtitle text |
| `collapsible` | boolean | No | Enable collapse/expand toggle |

```json
{
  "type": "section",
  "props": {"title": "Details", "collapsible": true},
  "children": [
    {"type": "text", "props": {"content": "Section content here"}},
    {"type": "key-value", "props": {"items": [{"key": "Status", "value": "OK"}]}}
  ]
}
```

---

#### `tabs`

Tab bar with child components per tab. **Supports `children`.**

Each child component maps 1:1 to a tab defined in `props.tabs`. The first child renders under the first tab, etc.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `tabs` | array | Yes | Array of `{label}` objects |

```json
{
  "type": "tabs",
  "props": {
    "tabs": [{"label": "Overview"}, {"label": "Details"}]
  },
  "children": [
    {"type": "text", "props": {"content": "Overview tab content"}},
    {"type": "table", "props": {"columns": ["Col"], "rows": [["Data"]]}}
  ]
}
```

---

### Complex Components

#### `dag`

Directed acyclic graph rendered as SVG with nodes and edges.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `title` | string | No | DAG title |
| `nodes` | array | Yes | Array of `{id, label, sublabel?, color?}` |
| `edges` | array | Yes | Array of `{from, to}` |

```json
{
  "type": "dag",
  "props": {
    "title": "Pipeline",
    "nodes": [
      {"id": "a", "label": "Extract"},
      {"id": "b", "label": "Transform"},
      {"id": "c", "label": "Load"}
    ],
    "edges": [
      {"from": "a", "to": "b"},
      {"from": "b", "to": "c"}
    ]
  }
}
```

---

#### `message-list`

Conversation message list with role-based styling.

**Props:**

| Prop | Type | Required | Description |
|------|------|----------|-------------|
| `messages` | array | Yes | Array of `{role, content, timestamp?}` |

Roles: `"user"`, `"assistant"`, `"system"`, `"tool"`

```json
{
  "type": "message-list",
  "props": {
    "messages": [
      {"role": "user", "content": "Show me revenue trends"},
      {"role": "assistant", "content": "Here's the analysis..."}
    ]
  }
}
```

---

## Colors

All components that accept a `color` prop use the Tokyonight Dark theme palette:

| Name | Use For | CSS Variable |
|------|---------|--------------|
| `accent` | Primary / informational | `--accent` (#7aa2f7) |
| `success` | Positive / healthy | `--success` (#9ece6a) |
| `warning` | Caution / degraded | `--warning` (#e0af68) |
| `error` | Failure / critical | `--error` (#f7768e) |
| `cyan` | Secondary highlight | `--cyan` (#7dcfff) |
| `magenta` | Tertiary highlight | `--magenta` (#bb9af7) |

You can also pass hex colors (e.g., `"#ff6b6b"`), but named colors are preferred for visual consistency.

## Limits

| Constraint | Value |
|------------|-------|
| Max components per spec | 50 |
| Max nesting depth | 10 |
| Max props size per component | 64 KB |
| Max total spec size | 512 KB |
| Max dynamic apps | 100 |
| Max total dynamic app storage | 50 MB |
| App name pattern | `^[a-z0-9][a-z0-9-]{0,62}$` |
| Reserved names | `component-types` |

## APIs

### gRPC RPCs

6 RPCs in `LoomService` (defined in `proto/loom/v1/loom.proto`):

| RPC | HTTP | Description |
|-----|------|-------------|
| `ListUIApps` | `GET /v1/apps` | List all registered apps |
| `GetUIApp` | `GET /v1/apps/{name}` | Get app metadata and compiled HTML |
| `CreateUIApp` | `POST /v1/apps` | Create a dynamic app from a spec |
| `UpdateUIApp` | `PUT /v1/apps/{name}` | Update an existing dynamic app |
| `DeleteUIApp` | `DELETE /v1/apps/{name}` | Delete a dynamic app |
| `ListComponentTypes` | `GET /v1/apps/component-types` | Discover available component types |

### MCP Tools

Exposed by `loom-mcp` bridge to MCP clients (Claude Code, Cursor, etc.):

| Tool | Description |
|------|-------------|
| `loom_create_app` | Create a dynamic app from a declarative spec |
| `loom_update_app` | Update an existing dynamic app |
| `loom_delete_app` | Delete a dynamic app |
| `loom_list_component_types` | Discover available component types and prop schemas |

### Agent Tools

Auto-registered to all server-side Loom agents (no configuration needed):

| Tool | Description |
|------|-------------|
| `create_ui_app` | Create a dynamic app from a declarative spec |
| `update_ui_app` | Update an existing dynamic app |
| `delete_ui_app` | Delete a dynamic app |
| `list_component_types` | Discover available component types and prop schemas |

Agents should call `list_component_types` first to discover the catalog, then use `create_ui_app` with a spec.

## Security Constraints

The compiler validates and sanitizes all specs before compilation:

- **Prototype pollution prevention**: Keys `__proto__`, `constructor`, `prototype` are rejected at any depth
- **Script injection prevention**: Values starting with `javascript:`, `vbscript:`, `data:text/html` are rejected
- **CSS injection prevention**: Values containing `url(`, `expression(`, `@import` are rejected
- **HTML injection prevention**: `<`, `>`, `&` in spec JSON are escaped to unicode equivalents
- **CSP**: Compiled HTML includes `Content-Security-Policy` with `default-src 'none'`, `connect-src 'none'`, `form-action 'none'`
- **DOM safety**: Runtime uses `textContent` exclusively (no `innerHTML`), SVG uses strict element/attribute allowlists
- **Chart.js**: Loaded with SRI hash from `cdn.jsdelivr.net` (pinned to v4.4.7)
