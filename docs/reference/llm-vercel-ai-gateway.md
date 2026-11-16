# Vercel AI Gateway

**Version**: v1.4.0
**Status**: ⚠️ Partial. The provider (`pkg/llm/vercelgateway`) and its wiring are implemented, unit-tested against a fake gateway, and **run against the live gateway on 2026-11-16** (`claude-haiku-4.5` on the Messages surface, `gpt-5-mini` on Chat Completions, `text-embedding-3-small`; see [the spike record](../research/vercel-ai-gateway-spike.md)). Behaviours marked *unverified* below were not exercised live.

The [Vercel AI Gateway](https://vercel.com/docs/ai-gateway) is one host, `https://ai-gateway.vercel.sh`, that fronts many upstream providers under creator-scoped model ids (`anthropic/claude-sonnet-4.5`, `openai/gpt-5`, `google/gemini-3.1-pro-preview`). Loom's `vercel-ai-gateway` provider (alias `vercel`) sends Claude ids to the gateway's Anthropic Messages surface and every other id to its OpenAI Chat Completions surface.

## Table of Contents

- [What is implemented](#what-is-implemented)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [How requests are routed](#how-requests-are-routed)
- [Routing options](#routing-options)
- [Thinking and reasoning](#thinking-and-reasoning)
- [Model catalog and cost](#model-catalog-and-cost)
- [Errors, retries and rate limits](#errors-retries-and-rate-limits)
- [Observability](#observability)
- [Embeddings](#embeddings)
- [Environment variables](#environment-variables)
- [Testing](#testing)
- [Limitations and unverified behaviour](#limitations-and-unverified-behaviour)

## What is implemented

✅ Implemented (unit-tested; live-tested 2026-11-16 unless marked):

- Dual routing: `anthropic/*` → `POST /v1/messages`, everything else → `POST /v1/chat/completions`
- Blocking and streaming calls, tool calls, on both surfaces
- Typed routing options (`order`, `only`, `sort`, `fallback_models`, `zero_data_retention`, `caching`) sent as `providerOptions.gateway`, from `looms.yaml` or agent YAML (`gateway_routing`, proto `LLMGatewayRouting`)
- API key auth (keyring, `LOOM_LLM_VERCEL_GATEWAY_API_KEY`, `AI_GATEWAY_API_KEY`) and Vercel OIDC token auth
- Health check with `GET /v1/credits` (validates the key, spends no tokens)
- Model catalog from `GET /v1/models`: context window, max output, input/output/cache rates, reasoning tag; refreshed hourly in the background under `looms serve`
- Gateway-reported cost and routing metadata read from responses when present; otherwise a catalog estimate; ids with no known rate reported as unpriced (cost 0, `llm.cost_unpriced=true`)
- 402 (credits/budget) as a distinct, non-retried error
- Chat-surface reasoning: the gateway's `reasoning` request object and `reasoning_details` parse/replay
- Memory embeddings: `embedding.provider: vercel-ai-gateway` points loom's OpenAI-shaped embedder at the gateway's `POST /v1/embeddings` (model default `openai/text-embedding-3-small`; API key only, no OIDC)
- Wiring: provider factory, `looms serve` (main LLM, provider pool, agent YAML LLMs), `looms workflow`, `looms config init` (option 5), agent registry, `loom-standalone`, `loom decision replay`, `pkg/builder` (`WithVercelGatewayLLM`)

📋 Not implemented:

- The OpenAI Responses surface (`/v1/responses`)
- Per-request BYOK credentials, `providerTimeouts`, `sessionAffinity` (out of scope by decision; BYOK keys are configured in the Vercel dashboard)
- Tiered (long-context) gateway prices (`input_tiers`/`output_tiers`): only the base rate is stored
- The TUI model picker (`internal/catwalk`) lists no gateway models; its list is static and the gateway catalog is not

## Configuration

### `looms.yaml`

```yaml
llm:
  provider: vercel-ai-gateway                          # alias: vercel
  vercel_gateway_model: anthropic/claude-sonnet-4.5    # required, no default
  vercel_gateway_endpoint: https://ai-gateway.vercel.sh  # optional; eu./us. regional hosts go here
  # vercel_gateway_api_key: looms config set-key vercel_gateway_api_key   (or AI_GATEWAY_API_KEY)
  # vercel_gateway_oidc: false
  # vercel_gateway_oidc_token_file: /var/run/vercel/oidc-token
  vercel_gateway_routing:
    order: [bedrock, anthropic]
    only: []
    sort: ""                 # cost | ttft | tps
    fallback_models: [openai/gpt-5]
    zero_data_retention: false
    caching: ""              # auto
```

There is no default model: an empty `vercel_gateway_model` fails `looms` config validation, because a built-in default would go stale with the gateway's catalog. The endpoint is a base URL; a pasted surface URL (`…/v1/chat/completions`) is reduced to the base. It must be `https`, except for a loopback host.

A gateway client that cannot be built is never replaced by another LLM: a routing block can restrict upstreams or require zero data retention, and a silent fallback would bypass that. Concretely:

- `looms serve` does not start such an agent (cold start), and refuses the reload of an edited agent YAML, keeping the running agent (hot reload). This holds for the agent's `llm` and for each role LLM (`judge_llm`, `orchestrator_llm`, `classifier_llm`, `compressor_llm`).
- The agent registry (runtime create, rebuild) fails the agent build in the same case.
- `looms workflow` and `looms eval` exit with the build error instead of falling back to `ANTHROPIC_API_KEY` or the Bedrock profile.
- The same rule applies to `bedrock-mantle`.

`gateway_routing` on an LLM whose `provider` is not `vercel-ai-gateway` (or that names no provider and so runs on the server default) is a validation error, not silently ignored.

### Agent YAML

```yaml
agent:
  llm:
    provider: vercel-ai-gateway
    model: openai/gpt-5
    gateway_routing:          # replaces the server's vercel_gateway_routing for this agent
      only: [azure, openai]
      sort: ttft
```

Under `looms serve`, credentials and the endpoint come from the server's `llm.vercel_gateway_*` settings; the agent picks the model and may replace the routing block. This holds for agents loaded at startup, reloaded, and created or rebuilt at runtime through the registry (`RegistryConfig.LLMProviderBuilder`). An agent's `gateway_routing` replaces the server's block as a whole; it is not merged field by field. Through the agent registry without `looms serve`, credentials come from the environment (see below).

### Go

```go
client, err := vercelgateway.NewClient(vercelgateway.Config{
    APIKey:  vercelgateway.ResolveAPIKey(""), // LOOM_LLM_VERCEL_GATEWAY_API_KEY, then AI_GATEWAY_API_KEY
    Model:   "anthropic/claude-sonnet-4.5",
    Routing: &vercelgateway.Routing{Order: []string{"bedrock", "anthropic"}},
    RateLimiterConfig: llm.RateLimiterConfig{Enabled: true},
})
```

```go
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithVercelGatewayLLM("openai/gpt-5").
    Build()
```

```go
factory.FactoryConfig{
    VercelGateway: vercelgateway.Config{Model: "openai/gpt-5"}, // key from the env chain when empty
}
```

## Authentication

| Method | How | Notes |
|---|---|---|
| API key | `looms config set-key vercel_gateway_api_key`, `LOOM_LLM_VERCEL_GATEWAY_API_KEY`, or `AI_GATEWAY_API_KEY` (checked in that order) | Sent as `x-api-key` on the Messages surface and `Authorization: Bearer` on Chat Completions and `/v1/credits` |
| Vercel OIDC | `vercel_gateway_oidc: true`; token from `vercel_gateway_oidc_token_file`, else `VERCEL_OIDC_TOKEN` | Read fresh on every request, because Vercel rotates it and nothing refreshes it for a Go client. A token whose JWT `exp` has passed fails locally with "OIDC token expired" before any request is sent. |

An API key always wins over OIDC, and the client never sends both: per Vercel's docs the gateway prefers the API key when both arrive, even an invalid one.

Every request (both surfaces and the health check) must go to the configured gateway scheme and host. A redirect to another host, or to plain http, is refused before it is sent, so neither `x-api-key` (which Go copies across hosts) nor the OIDC bearer token can leave for an unconfigured host. Pool entries in `looms.yaml` inherit the main LLM's API key, endpoint and, when they set neither a key nor OIDC, its OIDC settings.

## How requests are routed

| Model id | Surface | Inner client |
|---|---|---|
| `anthropic/…` | `POST /v1/messages` | `pkg/llm/anthropic` (no `anthropic-beta` header) |
| anything else | `POST /v1/chat/completions` | `pkg/llm/openai` (gateway reasoning shape, unpriced reporting on) |

Claude ids go to the Messages surface so thinking signatures, `cache_control` breakpoints and cache-token usage keep the native Anthropic shape that loom already round-trips. A cross-family fallback (`fallback_models: [openai/gpt-5]` on a Claude primary) is still legal; the gateway translates on the surface that received the request (*unverified*).

The only beta header loom sends to Anthropic is the obsolete prompt-caching one; it is suppressed for the gateway, since `cache_control` works without it.

## Routing options

`vercel_gateway_routing` (looms.yaml), `gateway_routing` (agent YAML) and proto `LLMGatewayRouting` map to the request body as:

| Loom field | Body key under `providerOptions.gateway` | Values |
|---|---|---|
| `order` | `order` | provider slugs, tried first in order |
| `only` | `only` | provider slugs; no match is a terminal error |
| `sort` | `sort` | `cost`, `ttft`, `tps` |
| `fallback_models` | `models` | creator/model ids |
| `zero_data_retention` | `zeroDataRetention` | `true` (accepted live; policy effect *unverified*) |
| `caching` | `caching` | `auto` |

Slugs are checked for shape only (letters, digits, `.`, `_`, `-`; no duplicates). Live slugs include camelCase ones (`vertexAnthropic`, `claudeaws`). The gateway publishes no canonical slug list a client can validate against. Unset fields are omitted; with no routing at all the request body carries no `providerOptions`.

Regional hosts (`eu.ai-gateway.vercel.sh`, `us.ai-gateway.vercel.sh`) carry a 5% surcharge and return 403 on the free tier, per Vercel's pricing page. Set them with `vercel_gateway_endpoint`.

## Thinking and reasoning

- **Claude ids** (Messages surface): `thinking_level` maps to Anthropic thinking exactly as on the `anthropic` provider (adaptive on 4.6+/5, budget tiers on older models). Dotted gateway ids (`claude-sonnet-4.6`) are recognised by the adaptive-thinking and pricing matchers.
- **Other ids** (Chat surface): when the catalog tags the model `reasoning` (or the id is Claude-family), loom sends `"reasoning": {"enabled": true, "effort": "low"|"medium"|"high"}` (`auto` sets no effort; `xhigh`/`max` map to `high`). Models the catalog does not know get no reasoning field. The response's `reasoning_details` entries are kept as thinking blocks (type `reasoning.text`, `reasoning.summary` or `reasoning.encrypted`, with signature and format) and replayed on the assistant message within the same turn. Whether the gateway requires that replay is *unverified*.

## Model catalog and cost

In `looms` (serve, workflow, eval), the first gateway client built for an endpoint (main LLM, pool entry, an agent's own `llm` block, a reload) starts the catalog for that endpoint in the background (the build does not wait): `GET /v1/models` (no credential needed) once, then every hour. Each endpoint gets its own catalog entry, so a regional host does not replace the global one. The registry without `looms`, `pkg/builder`, `loom-standalone` and `loom decision` do not fetch it; call `vercelgateway.RegisterCatalog` to get it there. Language models are registered under the catalog namespace `vercel-ai-gateway` with context window, max output tokens, capabilities (the entry's `tags`), `is_reasoning`, and per-1M input/output/cache-read/cache-write rates. A failed fetch is logged; serving continues, and the background refresh fills the catalog in once the gateway answers. A failed refresh keeps the previous catalog.

Prices in `/v1/models` are decimal strings in USD per token (confirmed live: Haiku 4.5 lists `0.000001`/`0.000005`, its $1/$5 card). Loom multiplies by 10⁶.

Cost per call, in order:

1. The cost the gateway reports in the response. Observed live on every call: on the Messages surface `provider_metadata.gateway.cost` (body and the stream's `message_delta`); on Chat Completions `usage.cost` (and `choices[0].message/delta.provider_metadata.gateway.cost`).
2. A catalog estimate: the gateway catalog's rates (published cache rates win over vendor multipliers), else the Claude family card for Claude ids.
3. Otherwise, on the Chat surface, an **estimate**: the median input and median output rate of the same creator's priced models in the gateway catalog (`openai/*` for `openai/new-model`), or of every priced catalog model when the creator has none (`catalog.CreatorMedianPricing`). The call is flagged `llm.cost_unpriced = true` and `llm.cost_estimated = true`, `Usage.CostEstimated` is set, `LLMCost.cost_estimated` is true in `Weave`/`StreamWeave` responses, and the TUI sidebar and `loom chat` show the cost with an asterisk (`$0.42*`). Cost ceilings (eval `max_cost_usd`, session totals) count the estimate. The asterisk is live only: stored messages and session totals do not keep it. With no priced catalog model at all (catalog not loaded), the cost is 0 with `llm.cost_unpriced = true` only. On the Messages surface, a Claude id the family table does not recognise is priced at the Sonnet 4 card ($3/$15 per 1M) without the unpriced flag, as on the `anthropic` provider.

Output cap: under `looms serve` the request carries `llm.max_tokens` (looms default 4096) or the agent's `max_tokens`. Only the provider factory path reads the catalog's `max_tokens` when none is configured (fallback 4096). Context window: loom's memory budgets against `llm.max_context_tokens` when set, else its built-in default (200K); the catalog's `context_window` is stored but no serve path reads it today. Set `max_context_tokens` for models with a smaller window.

## Errors, retries and rate limits

| Condition | Behaviour |
|---|---|
| 429, 500/502/503/504/529 | Retried by the client-side limiter with backoff, honouring `Retry-After` |
| 402 | `vercelgateway.ErrCreditsExhausted` ("payment required … credits exhausted, budget exceeded, or BYOK without a positive credit balance"); not retried |
| 401/403/404, unsatisfiable `only` | Terminal (deterministic client error) |
| Error payload after a stream started | Returned as an error (status from the error type) instead of a truncated success |
| Context too long | 400 with `error.type` `AI_APICallError` and the upstream message passed through (live). Loom's matchers recognise both surfaces' wording, so context relief fires. |

The limiter is on by default. The gateway does not rate limit paid-tier requests and publishes no free-tier limit, so unset limiter fields take loom's generic defaults (`llm.DefaultRateLimiterConfig`: 2 req/s, burst 5) on **both** surfaces; without this, Claude ids would inherit the anthropic client's Tier 1 limits (0.7 req/s). Raise `llm.rate_limit.*` for a large fleet on a paid team. The limiter key includes the credential scope, endpoint and model, so gateway traffic never shares a bucket with direct traffic.

## Observability

Every call's response metadata, and the LLM span, carry (when the response provides them):

| Attribute | Value |
|---|---|
| `gateway.endpoint_surface` | `messages` or `chat` (always set) |
| `gateway.generation_id` | `gen_…` id (Chat completion `id`, or `providerMetadata.gateway.generationId`) |
| `gateway.final_model` | model the response reports |
| `gateway.fallback_used` | `true` when the served model differs from the requested one, or more than one model was attempted |
| `gateway.final_provider` | upstream that served the call |
| `gateway.attempt_count` | provider attempts |
| `llm.cost_unpriced` | `true` when neither the gateway nor a published rate priced the call |
| `llm.cost_estimated` | `true` when the cost is the creator-median estimate (shown with an asterisk) |

No prompt content or key material is recorded.

## Embeddings

```yaml
embedding:
  enabled: true
  provider: vercel-ai-gateway       # alias: vercel
  model: openai/text-embedding-3-small   # creator/model id; this is the default
  dimensions: 1536
  # api_key: optional; defaults to the llm.vercel_gateway_* key chain
```

The endpoint is `llm.vercel_gateway_endpoint` (default global host) plus `/v1/embeddings`. The request is the OpenAI embeddings shape (`model`, `input`, `dimensions`); whether the gateway passes `dimensions` through for every upstream is *unverified*. OIDC is not supported for embeddings.

## Environment variables

The full entries are in [`ENV_VARS.md`](../../ENV_VARS.md#vercel-ai-gateway).

| Variable | Used by | Meaning |
|---|---|---|
| `LOOM_LLM_VERCEL_GATEWAY_API_KEY` | all paths | API key (checked first) |
| `AI_GATEWAY_API_KEY` | all paths | API key (Vercel's own name; also read by the Jev decision provider) |
| `VERCEL_OIDC_TOKEN` | OIDC | token, read per request when no token file is set |
| `VERCEL_AI_GATEWAY_ENDPOINT` | agent registry, `pkg/builder`, `loom-standalone`, `loom decision` | endpoint override for paths without `looms.yaml` |
| `LOOM_LLM_VERCEL_GATEWAY_MODEL`, `LOOM_LLM_VERCEL_GATEWAY_ENDPOINT` | `looms` | viper overrides of the `llm.vercel_gateway_*` keys |

## Testing

Unit tests (fake gateway over `httptest`, race detector):

```bash
go test -tags fts5 -race ./pkg/llm/vercelgateway/ ./cmd/looms/ -run 'Gateway|VercelGateway'
```

Fuzz tests: `FuzzValidateModelID`, `FuzzNormalizeEndpoint`, `FuzzInspectPayload`, `FuzzDecodeModels`.

Live tests (real gateway; spend a few cents):

```bash
export AI_GATEWAY_API_KEY=...            # or LOOM_LLM_VERCEL_GATEWAY_API_KEY
VERCEL_GATEWAY_LIVE=1 \
VERCEL_GATEWAY_LIVE_CAPTURE_DIR=/tmp/vgw-captures \
  go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/vercelgateway/ -v
```

`VERCEL_GATEWAY_LIVE_CLAUDE` / `VERCEL_GATEWAY_LIVE_OTHER` override the models (defaults `anthropic/claude-haiku-4.5`, `openai/gpt-5-mini`). The capture directory receives every request/response pair with credentials redacted, for the spike write-up.

## Limitations and unverified behaviour

Verified live on 2026-11-16 (details in [the spike record](../research/vercel-ai-gateway-spike.md)): cost and routing metadata locations, the generation id, the pricing unit, `providerOptions.gateway` in the raw body (an unsatisfiable `only` is rejected), `gateway.fallback_used` (the gateway echoes the requested id), Claude signed-thinking replay over a 3-turn tool loop, GPT-5 mini `reasoning_details` replay, context-overflow bodies, `/v1/credits`, `/v1/embeddings`.

Still unverified (the plan's §7 lists the questions behind them):

- Whether `anthropic-beta` is accepted or forwarded (question 9). Loom sends none.
- Thinking-signature replay when a later turn is served by a different upstream, and whether `reasoning_details` replay is required rather than merely accepted (questions 11, 14).
- Cache usage fields when Bedrock or Vertex serves a Messages call, and cross-family fallback behaviour (questions 12–13).
- The `zeroDataRetention` value shape and other `providerOptions` keys (question 23). Loom sends `true`; the gateway accepted the request, but no live check shows the policy was applied.
- 402, forced 429 and mid-stream failure bodies (questions 25–27).
- The gateway's own request and stream-idle timeouts, and enterprise concurrency limits (questions 28–29). Loom's defaults: 300 s per blocking call, first-byte wait equal to that on streams.
- OIDC: implemented and unit-tested, not run live (question 32).

## See Also

- [LLM providers](./llm-providers.md)
- [Plan](../plans/vercel-ai-gateway-plan.md)
- [Bedrock Mantle](./llm-bedrock-mantle.md) (the same dual-routing design on AWS)
