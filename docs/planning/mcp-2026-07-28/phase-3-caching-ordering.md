# Phase 3 Brief: CacheableResult, Deterministic Ordering, Deploy Configs

Parent spec: §7.2, §7.3, §6.1 (ttl values)
Depends on: Phase 2 (result stamping infrastructure)
Size: Small

## Pinned decisions

- **[proposed] `ttlMs`/`cacheScope` are set on all list/read results in both modes.** The fields are additive; legacy clients ignore unknown fields. One code path, no mode branch.
- **[proposed] `ttlMs` is list-level, so the whole list takes the shortest constituent TTL:** 300000 when only static bridge tools are served; 15000 whenever TER-263 lazy skill loading could still change the list (per-tool TTLs don't exist in the spec).
- **[proposed] Sorting happens in the server-core list handlers** (tools, prompts, resources, templates), not in `buildToolDefinitions` — every provider benefits, authored order stays available internally.
- **`cacheScope` defaults to `"private"` at the `MCPServer` provider API; `"public"` is an explicit constructor option** (`WithPublicCacheScope()`), refused if the provider also registers identity-varying visibility. The bridge never sets it.

## Work items

1. `ttlMs int64` + `cacheScope string` on `ToolListResult`, `PromptListResult`, `ResourceListResult`, `ReadResourceResult`, templates list result (`pkg/mcp/protocol/types.go`), JSON tags exactly `ttlMs`/`cacheScope`.
2. Server option plumbing per the pinned decision; bridge wires 300000/15000 per TER-263 state.
3. `sort.Slice` by name at each list handler boundary.
4. Deploy configs (external to this repo where applicable): route on `Mcp-Method` at APISIX, remove sticky-session affinity for MCP upstreams. Deliverable here is a written config change list handed to whoever owns the gateway repo — mark done when that list exists, not when the gateway changes.

## Test scenarios

| # | Given | When | Then |
|---|---|---|---|
| 1 | Bridge with static tools only | `tools/list` ×2 | Byte-identical serializations; sorted by name; `ttlMs=300000`, `cacheScope="private"` |
| 2 | TER-263 lazy loading possible | `tools/list` | `ttlMs=15000` |
| 3 | Lazy skill load lands between calls | `tools/list` ×2 | Both sorted; second is a superset; each internally consistent |
| 4 | Legacy-mode client | `tools/list` | Fields present; legacy decode unaffected |
| 5 | Provider registers visibility + `WithPublicCacheScope()` | Server construction | Constructor error |
| 6 | Two identities with different visibility | `tools/list` each | Different lists, both `private`, both sorted |
| 7 | Prompts/resources/templates lists | each | Sorted, fields present |
| 8 (race) | Concurrent `tools/list` during a lazy skill load | `-race` | Clean; every response is a consistent snapshot |

## Acceptance criteria

Standing criteria, plus: golden file asserting the exact serialized `tools/list` result (this golden is the Bedrock prompt-cache byte-stability guarantee — a future diff on it is a deliberate cache-busting decision).
