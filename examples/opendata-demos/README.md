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

A second stdio shim ([`cmd/supabase-write-mcp`](../../cmd/supabase-write-mcp), `dbwrite:*`) lets an
agent **write results into your Supabase** (`dreambase` schema) for Dreambase — see #6 below.

## Agents (created on the lab via `loom_create_agent`, each with `tools.mcp: [{server: opendata, tools: ["*"]}]`)

| Agent | Role | Demo |
|-------|------|------|
| `od-analyst` | search → SQL → analyze; graph memory on | #1, #3, #4 |
| `data-scout` | catalog + dataset-graph discovery | #2 |
| `query-analyst` | DuckDB SQL specialist | #2 |
| `viz-builder` | results → chart-ready JSON | #2 |
| `critic` | reviews analysis for errors/cherry-picking | #2 |
| `anomaly-hunter` | z-score anomaly detection + explanation | #7 |
| `data-publisher` | OpenData join → write result to Supabase (`opendata:*` + `dbwrite:*`) | #6 |

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

## #6 (capstone) — publish a data product to Supabase for Dreambase

Instead of publishing *back to OpenData* (which needs Clerk OAuth — the `od_live_` key is
read-only, `401` on write), an agent joins public data in OpenData and **writes the result into
your Supabase**, where Dreambase reads it. No OAuth anywhere.

The `data-publisher` agent has both `opendata:*` and `dbwrite:*` tools. Example:
> "Using OpenData, join `owid/unemployment` and `owid/co2-emissions` on country/year for 2018,
> take the top 8 by CO2, then write the result to the Supabase table `unemployment_vs_co2`."

It runs the cross-dataset DuckDB join via `opendata:query`, then `dbwrite:write_table` creates
`dreambase.unemployment_vs_co2` on Supabase (typed columns, `GRANT SELECT` to the API roles).

The `dbwrite` shim ([`cmd/supabase-write-mcp`](../../cmd/supabase-write-mcp)) is deliberately narrow:
schema-locked (`dreambase`), strict table-name validation, parameterized inserts, no arbitrary
SQL/DROP. **Dreambase read note:** to read `dreambase.*` via PostgREST, add `dreambase` to the
project's *Exposed schemas* (Supabase → Settings → API); a direct Postgres connection needs no
change.

## Status
- ✅ #1, #2, #3, #7 verified live against OpenData.
- ✅ #4 scheduler enabled on the lab (`scheduler.enabled`); schedule created + triggered.
- ✅ #6 done via the Supabase-write loop (`data-publisher` + `dbwrite`): OpenData join →
  `dreambase.unemployment_vs_co2` on Supabase, verified.
