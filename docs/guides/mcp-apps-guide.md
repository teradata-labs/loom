# MCP Apps Guide

Create interactive dashboards and visualizations from agent conversations or MCP tool calls.

**Status**: ✅ Implemented (v1.1.0)

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Create a Dashboard](#create-a-dashboard)
  - [Add Charts to an App](#add-charts-to-an-app)
  - [Use Grid Layouts](#use-grid-layouts)
  - [Add Tabs and Sections](#add-tabs-and-sections)
  - [Update an Existing App](#update-an-existing-app)
  - [Delete an App](#delete-an-app)
- [Examples](#examples)
  - [Example 1: Sales KPI Dashboard](#example-1-sales-kpi-dashboard)
  - [Example 2: System Health Monitor](#example-2-system-health-monitor)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

MCP Apps let you create standalone HTML dashboards from a declarative JSON spec. You describe what you want (stat cards, charts, tables, etc.), and Loom compiles it into a self-contained HTML page with the Tokyonight Dark theme.

Apps can be created from three surfaces:
- **Claude Code / Cursor**: via MCP tools (`loom_create_app`)
- **Loom agents**: via auto-registered tools (`create_ui_app`)
- **gRPC/HTTP API**: via `CreateUIApp` RPC

All apps are served at `http://localhost:5006/apps/<name>` and as MCP resources at `ui://loom/<name>`.

## Prerequisites

- Loom server running (`looms serve`)
- For MCP tool access: `loom-mcp` bridge configured in your MCP client
- For agent access: any Loom agent (tools are auto-registered)

## Quick Start

The fastest way to create an app is through Claude Code (or any MCP client with Loom connected):

**Step 1**: Discover available components:

Call `loom_list_component_types` — this returns all 14 component types with their prop schemas.

**Step 2**: Create your app:

Call `loom_create_app` with a name and spec:

```json
{
  "name": "my-first-app",
  "spec": {
    "version": "1.0",
    "title": "My First App",
    "layout": "stack",
    "components": [
      {
        "type": "header",
        "props": {"title": "Hello Loom", "badge": "Demo"}
      },
      {
        "type": "stat-cards",
        "props": {
          "items": [
            {"label": "Status", "value": "Online", "color": "success"},
            {"label": "Uptime", "value": "99.9%", "color": "accent"}
          ]
        }
      }
    ]
  }
}
```

**Step 3**: View your app at `http://localhost:5006/apps/my-first-app`.

## Common Tasks

### Create a Dashboard

Every app needs a `version`, at least one component, and a unique name:

```json
{
  "name": "revenue-dashboard",
  "display_name": "Revenue Dashboard",
  "description": "Q1 2026 revenue analysis",
  "spec": {
    "version": "1.0",
    "title": "Revenue Dashboard",
    "badge": "Q1 2026",
    "layout": "stack",
    "components": [
      {"type": "header", "props": {"title": "Revenue Dashboard", "badge": "Q1 2026"}}
    ]
  }
}
```

**Name rules**: lowercase letters, numbers, and hyphens only. Must start with a letter or number. Max 63 characters. Pattern: `^[a-z0-9][a-z0-9-]{0,62}$`.

**Reserved names**: `component-types` (collides with the discovery endpoint).

### Add Charts to an App

Use the `chart` component with Chart.js. Supported chart types: `bar`, `line`, `pie`, `doughnut`, `radar`, `polarArea`, `scatter`, `bubble`.

```json
{
  "type": "chart",
  "props": {
    "chartType": "line",
    "title": "Monthly Revenue",
    "labels": ["Jan", "Feb", "Mar", "Apr", "May", "Jun"],
    "datasets": [
      {"label": "2025", "data": [320, 380, 410, 390, 450, 480], "color": "accent"},
      {"label": "2026", "data": [350, 420, 460, 440, 510, 540], "color": "success"}
    ],
    "fill": true
  }
}
```

For stacked bar charts, add `"stacked": true`.

### Use Grid Layouts

Three layout options control how top-level components arrange:

| Layout | Behavior |
|--------|----------|
| `stack` | Vertical stack (default). Full width. |
| `grid-2` | Two-column grid. Collapses to single column below 768px. |
| `grid-3` | Three-column grid. Collapses to single column below 768px. |

```json
{
  "version": "1.0",
  "title": "Grid Example",
  "layout": "grid-2",
  "components": [
    {"type": "chart", "props": {"chartType": "bar", "labels": ["A"], "datasets": [{"label": "X", "data": [1]}]}},
    {"type": "chart", "props": {"chartType": "pie", "labels": ["A"], "datasets": [{"label": "Y", "data": [1]}]}}
  ]
}
```

### Add Tabs and Sections

Use `tabs` to create a tab bar. Each child component maps 1:1 to a tab:

```json
{
  "type": "tabs",
  "props": {
    "tabs": [{"label": "Overview"}, {"label": "Details"}, {"label": "SQL"}]
  },
  "children": [
    {"type": "stat-cards", "props": {"items": [{"label": "Total", "value": "42"}]}},
    {"type": "table", "props": {"columns": ["Name", "Value"], "rows": [["Foo", "Bar"]]}},
    {"type": "code-block", "props": {"language": "sql", "code": "SELECT * FROM orders"}}
  ]
}
```

Use `section` with `collapsible: true` for expand/collapse sections:

```json
{
  "type": "section",
  "props": {"title": "Advanced Details", "collapsible": true},
  "children": [
    {"type": "key-value", "props": {"items": [{"key": "Host", "value": "prod-db-01"}]}}
  ]
}
```

### Update an Existing App

Call `loom_update_app` (MCP) or `update_ui_app` (agent) with the app name and new spec:

```json
{
  "name": "revenue-dashboard",
  "spec": {
    "version": "1.0",
    "title": "Revenue Dashboard (Updated)",
    "layout": "stack",
    "components": [
      {"type": "header", "props": {"title": "Revenue Dashboard", "badge": "Updated"}}
    ]
  }
}
```

Empty `display_name` and `description` keep the existing values.

### Delete an App

Call `loom_delete_app` (MCP) or `delete_ui_app` (agent) with the app name:

```json
{"name": "revenue-dashboard"}
```

Only dynamic (agent-created) apps can be deleted. The 4 built-in apps cannot be modified or deleted.

## Examples

### Example 1: Sales KPI Dashboard

A dashboard with stat cards, a bar chart, and a data table.

**MCP tool call** (`loom_create_app`):

```json
{
  "name": "sales-kpis",
  "display_name": "Sales KPI Dashboard",
  "spec": {
    "version": "1.0",
    "title": "Sales KPI Dashboard",
    "badge": "Live",
    "layout": "stack",
    "components": [
      {
        "type": "header",
        "props": {
          "title": "Sales KPI Dashboard",
          "description": "Real-time sales metrics",
          "badge": "Live"
        }
      },
      {
        "type": "stat-cards",
        "props": {
          "items": [
            {"label": "Total Revenue", "value": "$4.54M", "color": "success", "sublabel": "+12% vs Q4"},
            {"label": "Active Customers", "value": "2,100", "color": "accent"},
            {"label": "Avg Order Value", "value": "$892", "color": "cyan"},
            {"label": "Churn Rate", "value": "3.2%", "color": "warning", "sublabel": "+0.5%"}
          ]
        }
      },
      {
        "type": "chart",
        "props": {
          "chartType": "bar",
          "title": "Revenue by Region",
          "labels": ["West", "East", "South", "Central", "International"],
          "datasets": [
            {"label": "Q1 2026", "data": [1200, 980, 750, 610, 1000], "color": "accent"},
            {"label": "Q4 2025", "data": [1050, 920, 680, 590, 880], "color": "magenta"}
          ]
        }
      },
      {
        "type": "table",
        "props": {
          "title": "Top 5 Customers",
          "columns": ["Customer", "Region", "Revenue", "Orders"],
          "rows": [
            ["Acme Corp", "West", "$1.2M", "342"],
            ["GlobalTech", "East", "$890K", "215"],
            ["DataFlow Inc", "South", "$670K", "189"],
            ["CloudBase", "Central", "$540K", "156"],
            ["NetPrime", "International", "$480K", "134"]
          ],
          "sortable": true
        }
      }
    ]
  }
}
```

**Result**: App created at `http://localhost:5006/apps/sales-kpis` and `ui://loom/sales-kpis`.

### Example 2: System Health Monitor

A grid-layout dashboard with progress bars, heatmap, and badges.

```json
{
  "name": "system-health",
  "display_name": "System Health Monitor",
  "spec": {
    "version": "1.0",
    "title": "System Health",
    "badge": "Monitor",
    "layout": "grid-2",
    "components": [
      {
        "type": "progress-bar",
        "props": {
          "title": "Resource Usage",
          "items": [
            {"label": "CPU", "value": 67, "color": "accent"},
            {"label": "Memory", "value": 82, "color": "warning"},
            {"label": "Disk", "value": 45, "color": "success"},
            {"label": "Network", "value": 23, "color": "cyan"}
          ]
        }
      },
      {
        "type": "badges",
        "props": {
          "items": [
            {"text": "Production", "color": "success"},
            {"text": "v2.1.0", "color": "accent"},
            {"text": "3 Warnings", "color": "warning"},
            {"text": "0 Errors", "color": "success"}
          ]
        }
      },
      {
        "type": "heatmap",
        "props": {
          "title": "Query Latency (ms)",
          "rowLabels": ["Mon", "Tue", "Wed", "Thu", "Fri"],
          "columnLabels": ["Morning", "Afternoon", "Evening", "Night"],
          "values": [
            [120, 150, 90, 60],
            [200, 180, 110, 70],
            [95, 130, 85, 55],
            [160, 210, 140, 80],
            [110, 125, 75, 50]
          ],
          "colorScale": "blue"
        }
      },
      {
        "type": "key-value",
        "props": {
          "title": "Server Info",
          "items": [
            {"key": "Host", "value": "prod-db-01"},
            {"key": "Status", "value": "Online", "color": "success"},
            {"key": "Uptime", "value": "45d 12h 33m"},
            {"key": "Last Deploy", "value": "2026-02-10 14:30 UTC"}
          ]
        }
      }
    ]
  }
}
```

**Result**: A two-column dashboard with resource usage, status badges, latency heatmap, and server info.

## Troubleshooting

### "app name is reserved"

The name `component-types` is reserved because it collides with the component discovery HTTP endpoint. Choose a different name.

### "app already exists"

A dynamic app with that name exists. Either:
- Use a different name
- Set `"overwrite": true` in the `loom_create_app` call to replace it

### "cannot overwrite embedded app"

The 4 built-in apps (conversation-viewer, data-chart, explain-plan-visualizer, data-quality-dashboard) cannot be modified. Choose a different name.

### "compile failed: unknown type"

You used a component type that doesn't exist. Call `loom_list_component_types` to see the 14 valid types.

### "spec exceeds maximum size"

The total spec JSON must be under 512 KB. Individual component props must be under 64 KB. Reduce data size or split into multiple apps.

### "dynamic app limit reached"

Maximum 100 dynamic apps. Delete unused apps with `loom_delete_app` to free slots.

### App not rendering in Claude Desktop

MCP apps are served as `text/html` MCP resources. Claude Desktop renders them in an iframe. If the app doesn't render:
1. Check the server is running (`looms serve`)
2. Verify the app exists: `GET http://localhost:5006/v1/apps`
3. Try viewing directly at `http://localhost:5006/apps/<name>`

## Next Steps

- [MCP Apps Reference](../reference/mcp-apps.md) — full component catalog with all props and examples
- [MCP Apps Architecture](../architecture/mcp-apps.md) — compiler pipeline and security model
- [MCP Integration](../../README.md) — connecting MCP servers to Loom
