# Fly.io deployment (hardened, public MCP edge)

Runs `looms` (loopback, trusted) + `loom-mcp` (the authenticated Streamable-HTTP
edge Dreambase hits) in one Fly machine. Storage and the agent's data source are
Supabase Postgres.

## Files
- `Dockerfile` — builds the real `looms` + `loom-mcp` with `-tags fts5` (distinct from `deploy/Dockerfile`, which is the benchmark image).
- `entrypoint.sh` — supervises both processes; exits (→ Fly restart) if either dies.
- `fly.toml` — exposes only `:8765` behind Fly TLS; single always-on machine.
- `looms.yaml` — **mandatory** security config (deny-by-default tools). Loom's defaults are `yolo:true`/`require_approval:false`, i.e. wide open.
- `backends/supabase.yaml` — optional read-only Supabase data tool for the agent.

## Do we need a Supabase-specific tool?

Two different things, don't conflate them:
- **Storage backend** (`storage.backend=postgres`): where Loom persists its *own*
  state (sessions, tasks, graph_memory). Configured via env. Not a tool.
- **Execution backend** (`backends/supabase.yaml`, referenced by an agent's
  `backend_path`): lets the agent *query the project's data* as a tool. This is
  the "Supabase tool."

You need the execution backend **only if the agent must read the live database**
(investigate an anomaly, inspect schema to propose indexes). If Dreambase passes
the data in the prompt (e.g. the Report Card findings JSON), you can skip it.
If you add it: use a **read-only, least-privilege** Postgres role + RLS — the
agent runs LLM-generated SQL through it.

## Egress (outbound) — what the machine must reach

| Destination | Port | Why | Required? |
|---|---|---|---|
| `api.anthropic.com` | 443 | LLM calls (`loom_weave`) | yes |
| `<pooler-host>` (e.g. `aws-0-<region>.pooler.supabase.com`) | 5432 | storage (session mode) | yes |
| same pooler host | 6543 | execution backend (transaction mode) | only if using the Supabase data tool |
| `api.tavily.com` | 443 | `web_search` (Tavily) | yes (you asked for Tavily) |
| `<ref>.supabase.co` | 443 | JWKS fetch for JWT validation | only for asymmetric-key Supabase projects |
| Hawk endpoint | 443 | observability export | only if `observability` is enabled (off by default) |

> **Fly does not firewall egress by default** — any process in the machine can
> reach anything. That's why the disabled tools (`shell_execute`, `http_request`,
> `grpc_call`) matter: they're the arbitrary-egress/exfiltration paths. The list
> above is the *intended* egress; everything else is closed at the tool layer,
> not the network layer.

## Ingress (inbound)

| Source | Port | To | Notes |
|---|---|---|---|
| Internet (Dreambase) | 443 (Fly TLS) → 8765 | `loom-mcp` HTTP edge | the ONLY public ingress; auth required |
| (in-machine) `loom-mcp` | 127.0.0.1:60051 | `looms` gRPC | loopback only, never exposed |

Not exposed: `looms` gRPC `:60051`, REST/SSE gateway `:5006` (disabled via `LOOM_SERVER_HTTP_PORT=0`).

## Secrets (`fly secrets set`)

```bash
fly secrets set \
  LOOM_STORAGE_POSTGRES_DSN="postgresql://postgres.<ref>:<pw>@<pooler-host>:5432/postgres?sslmode=require" \
  AWS_BEARER_TOKEN_BEDROCK="ABSK..." \          # Bedrock long-term API key (fly.toml sets provider/model/region); or use LOOM_LLM_ANTHROPIC_API_KEY with LOOM_LLM_PROVIDER=anthropic
  LOOM_SERVER_AUTH_SUPABASE_PROJECT_REF="<ref>" \
  LOOM_SERVER_AUTH_SUPABASE_JWT_SECRET="<jwt-secret>" \   # omit for asymmetric-key projects (JWKS auto-derived)
  LOOM_ADMIN_TOKEN="$(openssl rand -hex 24)" \
  TAVILY_API_KEY="tvly-..." \
  SUPABASE_PROJECT_REF="<ref>" \                          # for backends/supabase.yaml (if used)
  SUPABASE_ANON_KEY="<anon-key>" \
  SUPABASE_DB_PASSWORD="<read-only-role-password>" \
  SUPABASE_REGION="<region>"
```

## Deploy

```bash
fly launch --no-deploy --copy-config   # uses this fly.toml
fly secrets set ...                     # (above)
fly deploy
fly logs                                # expect "14 migrations" + "HTTP-MCP server ready"
```

## Verify the lockdown
Ask the agent (via `loom_weave`) to run a shell command — expect:
`tool 'shell_execute' is disabled by configuration`. Any non-allowlisted tool
returns the "requires user approval … not yet implemented" denial.

## Hard requirements (recap)
- `min_machines_running = 1`, never scale >1 (in-memory session/agent state isn't shared).
- `looms.yaml` present and `yolo:false` / `require_approval:true` (defaults are insecure).
- `LOOM_YOLO` must NOT be in the env.
- Use a read-only DB role for `backends/supabase.yaml` if you enable the data tool.
- Lock down Fly org membership: `fly ssh console` / `fly secrets` = full secret access.
