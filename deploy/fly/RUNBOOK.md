# Loom × Dreambase Lab — Reproduction Runbook

How to stand up the capability-enabled Loom "lab" on Fly.io, wire it to Supabase
and OpenData, and connect it to Dreambase (or any MCP client). Reproduces the
`loom-dreambase-lab` deployment end to end.

> No secret **values** appear here — only secret **names** and how to derive them.

---

## 1. What you're building

```
                         Fly machine (one VM)
  ┌──────────────────────────────────────────────────────────┐
  Dreambase ──HTTPS/MCP──▶ loom-mcp (edge :8765)              │
  (MCP client)            • Streamable HTTP, MCP 2025-03-26   │
                          • validates Supabase JWT (bearer)   │
                          • LOOM_MCP_ALLOWED_TOOLS allow-list  │
                                  │ gRPC (loopback :60051)     │
                                  ▼                            │
                          looms (multi-agent server)          │
                          • agents from /data/agents/*.yaml    │
                          • stdio MCP shims:                   │
                              opendata-mcp  (→ OpenData REST)  │
                              supabase-write-mcp (→ Supabase)  │
  └──────────────────────────────────────────────────────────┘
        │                         │                       │
   Supabase Postgres        OpenData REST           Bedrock (Claude)
   (storage + auth +        (api.tryopendata.ai)    (LLM)
    dreambase schema)
```

- **looms** — the agent runtime (gRPC on loopback). Loads agents from `$LOOM_DATA_DIR/agents/`.
- **loom-mcp** — the internet-facing edge. Exposes a curated set of `loom_*` MCP tools over Streamable HTTP, authenticated with a Supabase JWT.
- **opendata-mcp** / **supabase-write-mcp** — local stdio MCP shims the agents use (public-data queries; writing results into the `dreambase` Supabase schema).
- **Supabase** — Postgres for Loom storage (sessions/messages/tasks/graph) *and* the `dreambase` schema that holds ETL output for dashboards, *and* the JWT issuer for auth.

Source of truth for the deploy: `deploy/fly/` (`Dockerfile`, `fly.lab.toml`, `looms.lab.yaml`, `entrypoint.sh`, `agents/`).

---

## 2. Prerequisites

- **Fly.io** account + `flyctl`.
- **Supabase** project (Postgres 15+; this lab is verified on 17.x). You need: the project ref, the connection DSN, and the JWT secret.
- **OpenData** API key (`od_live_…`) from tryopendata.ai.
- **LLM**: AWS Bedrock access to a Claude model (this lab uses `global.anthropic.claude-sonnet-4-5`). Anthropic API or Ollama also work via `LOOM_LLM_PROVIDER`.
- Local: Go 1.25+ and `just` only if you want to build/mint tokens locally.

---

## 3. Create the Fly app + volume

```bash
fly apps create loom-dreambase-lab
fly volumes create loom_lab_data --region dfw --size 1 -a loom-dreambase-lab
```

`fly.lab.toml` mounts that volume at `/data` (= `LOOM_DATA_DIR`), which persists
agents (`/data/agents/`), the workspace, and the scheduler db across restarts.

---

## 4. Secrets

Set as Fly secrets (injected as env; never in the repo):

| Secret | What it is |
|---|---|
| `LOOM_STORAGE_POSTGRES_DSN` | Supabase Postgres connection string (storage + the `dreambase` schema) |
| `LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF` | Supabase project ref (derives JWKS URL + issuer) |
| `LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET` | Supabase JWT secret (HS256 validation + minting long-lived tokens) |
| `OPENDATA_API_KEY` | OpenData `od_live_…` key (used by both stdio shims) |
| `AWS_BEARER_TOKEN_BEDROCK` (or AWS creds) | Bedrock auth for the LLM |

```bash
fly secrets set -a loom-dreambase-lab \
  LOOM_STORAGE_POSTGRES_DSN='postgres://...' \
  LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF='<ref>' \
  LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET='<jwt-secret>' \
  OPENDATA_API_KEY='od_live_...' \
  AWS_BEARER_TOKEN_BEDROCK='...'
```

Non-secret config lives in `fly.lab.toml` `[env]` (storage backend, LLM provider/model/region,
`LOOM_SERVER_AUTH_*` flags, and the edge allow-list `LOOM_MCP_ALLOWED_TOOLS`). **Never set `LOOM_YOLO`** — it would re-enable every tool, including `shell_execute`.

---

## 5. Deploy

```bash
fly deploy -c deploy/fly/fly.lab.toml -a loom-dreambase-lab
```

The image (`deploy/fly/Dockerfile`) builds `looms`, `loom-mcp`, `opendata-mcp`,
`supabase-write-mcp` and bakes `deploy/fly/agents/*.yaml` into `/etc/loom/agents/`.
`entrypoint.sh` then:
1. **Seeds** baked agents into `/data/agents/` (idempotent, non-clobbering).
2. Starts `looms` on loopback `:60051` (auto-applies DB migrations on boot — `LOOM_STORAGE_MIGRATION_AUTO_MIGRATE=true`).
3. Waits for readiness, then starts the `loom-mcp` edge on `:8765`.

Verify: `fly logs -a loom-dreambase-lab | grep -E "seeded agent|Ready to weave|MCP edge"`.

---

## 6. Supabase setup

- **Migrations**: applied automatically on boot. They create the storage tables, the analytics views (`cost_per_agent_day`, `tool_outcomes`, `task_throughput`), and the JWT-RLS bridge (`loom_current_user_id()`).
- **Expose the `dreambase` schema** so dashboards can read ETL output: Supabase dashboard → **Project Settings → Data API → Exposed schemas** → add `dreambase` (keep `public`, `graphql_public`). PostgREST reloads automatically.
- **RLS**: the `supabase-write-mcp` shim enables RLS + a `SELECT` policy on every table it writes, and grants `SELECT` to `anon`/`authenticated`. So exposed tables aren't flagged "RLS disabled," and dashboards can read them.

---

## 7. Agents

Seeded from `deploy/fly/agents/` (rich `agent:` YAML — each gets its **own** `agent_id`, hence its own graph-memory namespace):

- **`opendata-analyst`** — query OpenData → publish to the `dreambase` schema.
- **`etl-pipeline`** — full cross-dataset joins moved server-side via `dbwrite query_to_table`.

A **weaver** meta-agent also runs (creates/updates agents via `agent_management`). To add agents at runtime, weave a request to the weaver, or drop a YAML in `/data/agents/` (hot-reloaded).

**Agent config tips (learned the hard way):**
- Only list `tool_search` in `tools.builtin`. `graph_memory` / `query_tool_result` are auto-registered when their stores are wired; `get_error_details` / `task_board` are progressively disclosed. Listing them is redundant and trips validation.
- Use `tools.mcp: [{server: opendata, tools: ["*"]}, {server: dbwrite, tools: ["*"]}]` to statically attach the MCP tool sets (don't rely on dynamic discovery).
- Keep prompts task-oriented (no "You are a…").

---

## 8. MCP edge + authentication

The edge requires a **Supabase JWT bearer** (it does **not** implement OAuth — the "Connect" button in MCP clients won't work). Two token options:

**A. Short-lived (a logged-in user token):** fine for a quick session (~1h).

**B. Long-lived (recommended for a demo)** — sign an HS256 JWT with the project JWT secret. Run locally (it never leaves your machine):

```bash
python3 - <<'PY'
import jwt, time   # pip install pyjwt   (or use the stdlib HS256 snippet)
SECRET = "<LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET>"
REF    = "<LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF>"
print("Bearer " + jwt.encode({
    "sub":"dreambase-demo","role":"authenticated","aud":"authenticated",
    "iss":f"https://{REF}.supabase.co/auth/v1",
    "iat":int(time.time()),"exp":int(time.time())+60*60*24*30,
}, SECRET, algorithm="HS256"))
PY
```

The edge validates `alg=HS256` signed with that secret, `aud="authenticated"`, and `iss=https://<ref>.supabase.co/auth/v1`. All three must match.

**Edge allow-list** (`LOOM_MCP_ALLOWED_TOOLS` in `fly.lab.toml`): the edge advertises + permits ONLY the listed tools. Current minimal set:
`loom_weave, loom_list_agents, loom_get_health, loom_execute_workflow, loom_list_artifacts, loom_get_artifact_content`.
Everything else (all create/delete/start/stop, `register_tool`, schedules, session/trace reads, `loom_list_tools`) is hidden + rejected. Admin still happens through `loom_weave → the weaver`.

---

## 9. Connect Dreambase (or any MCP client)

In Dreambase → Add MCP Connection:

| Field | Value |
|---|---|
| Name | `Loom` |
| Transport Type | `Streamable HTTP` |
| Remote MCP Server URL | `https://loom-dreambase-lab.fly.dev/` (root path, no `/mcp`) |
| "Connect" (OAuth) | **skip** — the edge isn't an OAuth server |
| Add Custom Header | `Authorization` = `Bearer <token from §8>` |

Then **Test Connection** → green. To target a specific agent, pass `agent_id` (e.g. `opendata-analyst`) in the `loom_weave` arguments; `loom_list_agents` enumerates them.

**Dreambase caches both the schema list and the tool list.** After exposing a new schema, adding tables, or changing the allow-list, **remove + re-add the connection** to refresh.

---

## 10. Run the flows

- **Single agent:** `loom_weave(agent_id="opendata-analyst", query="…")`.
- **Server-side ETL (scales to thousands of rows):**
  `loom_weave(agent_id="etl-pipeline", query="join X and Y and load it into table Z")` → the agent calls `dbwrite query_to_table`, which runs the OpenData DuckDB query and writes the full result into the `dreambase` schema in-process (rows never pass through the model).
- **Multi-agent workflow:** `loom_execute_workflow(workflow_yaml=…)`. Use the **k8s typed** format (`apiVersion: loom/v1`, `kind: Workflow`, `spec.type: pipeline`, `spec.stages[].agent_id` referencing registered agents). The simpler `name/agents/tasks` form used in `workflow_examples/` is **rejected** ("missing apiVersion"). Quote any scalar value containing `": "` or YAML treats it as a nested mapping.

CLI equivalent (same edge): `loom chat --remote https://loom-dreambase-lab.fly.dev/ --agent <name> "…"`.

---

## 11. Gotchas & ops (things that bit us)

- **Dreambase shows nothing / stale tools** → re-add the connection (it caches schema + tool list).
- **`dreambase.*` tables 404 over the REST API** → the `dreambase` schema isn't exposed (§6), or the client is reading `public` (use the schema selector / `Accept-Profile: dreambase`).
- **Token 401** → wrong secret, wrong `iss`/ref, or `aud != authenticated` (§8).
- **Pipeline stage 2 "doesn't see" stage 1's output** → fixed: the plain pipeline now stashes large stage output in SharedMemory and passes a truncated summary + key; give pipeline stage agents `shared_memory_read`. Keep the upstream stage concise.
- **Workflow timeout on heavy stages** → raise `timeout_seconds`; a researcher+fact-checker doing many web searches can exceed a tight deadline.
- **Disabled-tool denials in analytics** are policy decisions, not failures — `tool_outcomes` splits `policy_denied_count` from `failure_count`.

---

## 12. Verifying a deploy

```bash
# health
curl -s https://loom-dreambase-lab.fly.dev/ -X POST \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"check","version":"1"}}}'
# → 200 + an Mcp-Session-Id header means auth + edge are healthy.

fly logs -a loom-dreambase-lab | grep -E "Ready to weave|allow-list|seeded agent"
```
