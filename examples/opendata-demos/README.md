# Loom × OpenData × Dreambase demos

Public data (OpenData) → agent reasoning + orchestration (Loom) → dashboards (Dreambase).

## How Loom reaches OpenData

OpenData's hosted MCP server (`mcp.tryopendata.ai`) requires an **interactive Clerk
OAuth** flow (authorization_code; no machine grant), which a headless `looms` server
can't complete. Its **REST API** accepts a static `od_live_` key. So Loom talks to
OpenData through a small local **stdio MCP shim** — [`cmd/opendata-mcp`](../../cmd/opendata-mcp) —
that wraps the REST API with the key. The lab spawns it over stdio (no OAuth), and its
tools surface to agents as `opendata:*`.

Config: `deploy/fly/looms.lab.yaml` (`mcp.servers.opendata` → stdio `/usr/local/bin/opendata-mcp`;
`tools.permissions.allowed_tools` includes `opendata:*`). The shim reads `OPENDATA_API_KEY`
(a Fly secret) from the inherited env.

### Shim tools (`opendata:*`)
`search`, `list_providers`, `get_dataset`, `list_columns`, `query` (cross-dataset DuckDB SQL),
`query_dataset`, `related_datasets` (dataset graph).

## Agents (created on the lab via `loom_create_agent`, each with `tools.mcp: [{server: opendata, tools: ["*"]}]`)

| Agent | Role | Demo |
|-------|------|------|
| `od-analyst` | search → SQL → analyze; graph memory on | #1, #3, #4 |
| `data-scout` | catalog + dataset-graph discovery | #2 |
| `query-analyst` | DuckDB SQL specialist | #2 |
| `viz-builder` | results → chart-ready JSON | #2 |
| `critic` | reviews analysis for errors/cherry-picking | #2 |
| `anomaly-hunter` | z-score anomaly detection + explanation | #7 |

## Demos & how to run

**#1 — Ask the economy** (single agent):
```
loom chat --remote https://loom-dreambase-lab.fly.dev/ \
  "Find an OpenData dataset about unemployment, then return the 3 most recent rows. Cite the dataset path."
```
(or target `od-analyst` by id via `loom_weave`). The run's tokens/cost/tools land in Supabase →
the `cost_per_agent_day` / `tool_outcomes` Dreambase dashboards.

**#2 — Research desk** (multi-agent pipeline): [`research-desk.yaml`](research-desk.yaml) —
`data-scout → query-analyst → viz-builder → critic`. Run with the `loom_execute_workflow`
MCP tool (`workflow_yaml` = the file's contents).

**#3 — Research memory**: ask `od-analyst` about a dataset, then in a later turn ask it to
recall which dataset it used — it remembers via Loom graph memory (and uses OpenData's
`related_datasets` graph to discover joinable data).

**#4 — Living dashboard** (scheduled): a cron workflow (`scheduler.enabled: true`) that re-runs an
OpenData snapshot; each run refreshes the Dreambase telemetry. Create via `loom_schedule_workflow`
with a single-stage pipeline pattern (see `living-dashboard.pattern.json`).

**#7 — Anomaly hunter**: ask `anomaly-hunter` to find + explain anomalies in an OpenData series
(it computes z-scores via DuckDB and cross-references related datasets).

## Status / limitations
- ✅ #1, #2, #3, #7 verified live against OpenData.
- ✅ #4 scheduler enabled on the lab (`scheduler.enabled`).
- ⚠️ **#6 (publish a data product to OpenData) is not supported**: OpenData's write endpoints
  (`user-views`) require Clerk OAuth — the `od_live_` key is read-only (`401` on write). The
  read/query/graph demos work; "publishing back" to OpenData would need the OAuth flow. A
  Loom-side alternative is to publish the result to Supabase for Dreambase instead.
