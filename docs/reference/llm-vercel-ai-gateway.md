# Vercel AI Gateway

**Version**: v1.4.0
**Status**: ⚠️ Partial. Every feature listed under [What is implemented](#what-is-implemented) is built, unit-tested against a fake gateway, and run against the live gateway (2026-11-16 on Haiku 4.5 and GPT-5 mini; 2026-11-19 on Sonnet 5.5, GPT-5.5, Haiku 5.5 and Qwen 3.7 Flash, plus 372 agent runs in the [evaluation](../research/vercel-gateway-eval-results.md)). What keeps the status at Partial is listed under [Limitations](#limitations-and-unverified-behaviour): OIDC has not been run live, and no BYOK call has yet been served by a customer key (the request-scoped key is sent and was tried upstream; the test key had no Anthropic credit).

The [Vercel AI Gateway](https://vercel.com/docs/ai-gateway) is one host, `https://ai-gateway.vercel.sh`, that fronts many upstream providers under creator-scoped model ids (`anthropic/claude-sonnet-4.5`, `openai/gpt-5`, `google/gemini-3.1-pro-preview`). Loom's `vercel-ai-gateway` provider (alias `vercel`) by default sends Claude ids to the gateway's Anthropic Messages API and every other id to its OpenAI Chat Completions API; `gateway_api` selects the Messages, Chat Completions or OpenAI Responses API for every id instead.

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

✅ Implemented (unit-tested and live-tested):

- API selection (`gateway_api`): `auto` (default: `anthropic/*` → `POST /v1/messages`, everything else → `POST /v1/chat/completions`), `messages`, `chat`, or `responses` (`POST /v1/responses`, through `pkg/llm/openairesponses`, any model id)
- Streaming and tool calls on every API. `Chat` is served over a stream too, so a stalled upstream is caught by the first-byte and idle timeouts (see [Errors](#errors-retries-and-rate-limits))
- Routing options sent as `providerOptions.gateway`: `order`, `only`, `sort`, `fallback_models`, `zero_data_retention`, `caching`, per-request BYOK credentials (`byok`), `provider_timeouts_ms`, `session_affinity`, `disallow_prompt_training`, `inference_region`, `headquarters`, `user`, `tags`. From `looms.yaml` (`vercel_gateway_routing`) or agent YAML (`gateway_routing`, proto `LLMGatewayRouting`)
- Session affinity: every model request carries `x-session-id`, a SHA-256 digest of the loom session id (verified: the gateway reports `clientSessionIdSource: explicit`)
- API key auth (keyring, `LOOM_LLM_VERCEL_GATEWAY_API_KEY`, `AI_GATEWAY_API_KEY`) and Vercel OIDC token auth (OIDC unit-tested only)
- Health check with `GET /v1/credits` (validates the key, spends no tokens)
- Model catalog from `GET /v1/models`: context window, max output, input/output/cache rates, prompt-size price tiers, reasoning tag; refreshed hourly in the background under `looms serve`; listed by `ListAvailableModels` and the TUI model picker
- Gateway-reported cost and routing metadata read from responses on all three APIs; otherwise a catalog estimate (tier-aware); ids with no known rate reported as unpriced
- 402 (credits/budget) as a distinct, non-retried error
- Reasoning: Anthropic thinking on Messages; the gateway's `reasoning` object and `reasoning_details` replay on Chat; `reasoning` items with `encrypted_content` replay on Responses
- Memory embeddings: `embedding.provider: vercel-ai-gateway` points loom's OpenAI-shaped embedder at the gateway's `POST /v1/embeddings` (model default `openai/text-embedding-3-small`; API key only, no OIDC)
- Wiring: provider factory, `looms serve` (main LLM, provider pool, agent YAML LLMs), `looms workflow`, `looms config init` (option 5), agent registry, `loom-standalone`, `loom decision replay`, `pkg/builder` (`WithVercelGatewayLLM`, `WithVercelGatewayConfig`)

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
  vercel_gateway_api: ""     # "" / auto | messages | chat | responses
  vercel_gateway_routing:
    order: [bedrock, anthropic]
    only: []
    sort: ""                 # cost | ttft | tps
    fallback_models: [openai/gpt-5]
    zero_data_retention: false
    caching: ""              # auto
    byok:                    # secrets come from environment variables, never from this file
      - provider: anthropic
        env: {apiKey: LOOM_LLM_ANTHROPIC_API_KEY}
    provider_timeouts_ms: {anthropic: 15000}
    session_affinity: true   # unset = gateway default (on)
    disallow_prompt_training: false
    inference_region: {scope: zone, geo_region: us}
    headquarters: [US]       # or [!CN] to exclude
    user: ""
    tags: [feature:chat]
```

There is no default model: an empty `vercel_gateway_model` fails `looms` config validation, because a built-in default would go stale with the gateway's catalog. The endpoint is a base URL; a pasted surface URL (`…/v1/chat/completions`) is reduced to the base. It must be `https`, except for a loopback host.

A gateway client that cannot be built is never replaced by another LLM: a routing block can restrict upstreams or require zero data retention, and a silent fallback would bypass that. Concretely:

- `looms serve` does not start such an agent (cold start), and refuses the reload of an edited agent YAML, keeping the running agent (hot reload). This holds for the agent's `llm` and for each role LLM (`judge_llm`, `orchestrator_llm`, `classifier_llm`, `compressor_llm`).
- The agent registry (runtime create, rebuild) fails the agent build in the same case.
- `looms workflow` and `looms eval` exit with the build error instead of falling back to `ANTHROPIC_API_KEY` or the Bedrock profile.
- The same rule applies to `bedrock-mantle`.

`gateway_routing` or `gateway_api` on an LLM whose `provider` is not `vercel-ai-gateway` (or that names no provider and so runs on the server default) is a validation error, not silently ignored.

### Agent YAML

```yaml
agent:
  llm:
    provider: vercel-ai-gateway
    model: openai/gpt-5
    gateway_api: responses    # replaces the server's vercel_gateway_api for this agent
    gateway_routing:          # replaces the server's vercel_gateway_routing for this agent
      only: [azure, openai]
      sort: ttft
```

Under `looms serve`, credentials and the endpoint come from the server's `llm.vercel_gateway_*` settings; the agent picks the model and may replace the routing block and the API. This holds for agents loaded at startup, reloaded, and created or rebuilt at runtime through the registry (`RegistryConfig.LLMProviderBuilder`). An agent's `gateway_routing` replaces the server's block as a whole; it is not merged field by field. Through the agent registry without `looms serve`, credentials come from the environment (see below).

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
// Full configuration: API, routing (including BYOK), timeouts, thinking.
// Empty Endpoint and APIKey are filled from the environment.
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithVercelGatewayConfig(vercelgateway.Config{
        Model:   "openai/gpt-5.5",
        API:     vercelgateway.APIResponses,
        Routing: &vercelgateway.Routing{Only: []string{"openai"}},
        RateLimiterConfig: llm.RateLimiterConfig{Enabled: true},
    }).
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

| `gateway_api` | Model id | API | Inner client |
|---|---|---|---|
| `""` / `auto` | `anthropic/…` | `POST /v1/messages` | `pkg/llm/anthropic` (no `anthropic-beta` header) |
| `""` / `auto` | anything else | `POST /v1/chat/completions` | `pkg/llm/openai` (gateway reasoning shape, unpriced reporting on) |
| `messages` | any | `POST /v1/messages` | `pkg/llm/anthropic` |
| `chat` | any | `POST /v1/chat/completions` | `pkg/llm/openai` |
| `responses` | any | `POST /v1/responses` | `pkg/llm/openairesponses` |

By default Claude ids go to the Messages API so thinking signatures, `cache_control` breakpoints and cache-token usage keep the native Anthropic shape that loom already round-trips. The gateway translates between model families on every API (verified live 2026-11-19: Sonnet 5.5 on Responses, GPT-5.5 on Responses).

On the Responses API, `store` is always `false` and `previous_response_id` is never sent: the gateway's response ids are `gen_…` generation ids, not provider response ids, so the whole conversation is sent every turn and reasoning is replayed from `encrypted_content` (requested with `include: ["reasoning.encrypted_content"]`).

The only beta header loom sends to Anthropic is the obsolete prompt-caching one; it is suppressed for the gateway, since `cache_control` works without it. The gateway accepts an `anthropic-beta` header, an unknown value included (live, 2026-11-19).

## Routing options

`vercel_gateway_routing` (looms.yaml), `gateway_routing` (agent YAML) and proto `LLMGatewayRouting` map to the request body as:

| Loom field | Body key under `providerOptions.gateway` | Values |
|---|---|---|
| `order` | `order` | provider slugs, tried first in order |
| `only` | `only` | provider slugs; no match is a terminal error |
| `sort` | `sort` | `cost`, `ttft`, `tps` |
| `fallback_models` | `models` | creator/model ids |
| `zero_data_retention` | `zeroDataRetention` | `true` |
| `caching` | `caching` | `auto` (the gateway places cache breakpoints; verified live on anthropic, bedrock and vertex) |
| `byok` | `byok` | list of credentials; see below |
| `provider_timeouts_ms` | `providerTimeouts.byok` | provider slug → milliseconds, 1000–789000; BYOK credentials only |
| `session_affinity` | `sessionAffinity` | `true` / `false`; unset = gateway default (on) |
| `disallow_prompt_training` | `disallowPromptTraining` | `true`; not applied to BYOK requests |
| `inference_region` | `inferenceRegion` | `{scope: global}` or `{scope: zone, geo_region: us\|eu\|au\|jp}` |
| `headquarters` | `headquarters` | ISO 3166-1 alpha-2 codes (`US`), or all prefixed `!` to exclude; mixing is rejected |
| `user` | `user` | end-user id for usage reporting, ≤ 256 characters |
| `tags` | `tags` | ≤ 10 tags, each 1–64 characters |

Every key above was accepted by the live gateway on the Messages, Chat Completions and Responses APIs (2026-11-19).

### BYOK credentials

```yaml
byok:
  - provider: anthropic                 # anthropic | openai | azure | bedrock | vertex
    env: {apiKey: LOOM_LLM_ANTHROPIC_API_KEY}
  - provider: azure
    env: {apiKey: AZURE_OPENAI_API_KEY}
    values: {resourceName: my-resource}
    model_mappings: {openai/gpt-5.5: my-gpt55-deployment}
  - provider: bedrock
    env: {accessKeyId: AWS_ACCESS_KEY_ID, secretAccessKey: AWS_SECRET_ACCESS_KEY}
    values: {region: us-east-1}
  - provider: vertex
    env: {googleCredentials.privateKey: GCP_SA_PRIVATE_KEY}
    values: {project: my-project, location: us-east5, googleCredentials.clientEmail: sa@my-project.iam.gserviceaccount.com}
```

| Provider | Secret fields (`env` only) | Other fields (`env` or `values`) |
|---|---|---|
| `anthropic`, `openai` | `apiKey` | |
| `azure` | `apiKey` | `resourceName` (required); `model_mappings` |
| `bedrock` | `accessKeyId`, `secretAccessKey` | `region` |
| `vertex` | `googleCredentials.privateKey` | `project`, `location`, `googleCredentials.clientEmail` (all required) |

- **Secrets never sit in YAML.** `env` maps a credential field to the environment variable that holds it; the variable is read when the client is built. A secret field under `values` is a validation error. A variable that is unset or empty fails the build with an error naming the variable, never its value.
- Field names are matched without regard to case (`looms.yaml` goes through viper, which lowercases map keys).
- Several credentials for one provider are tried in order. When any request-scoped credential is present, the gateway ignores the dashboard's BYOK keys for that request. If every BYOK credential fails, the gateway falls back to its own provider keys unless the team disabled that in the dashboard (verified: a rejected key was followed by a `system` attempt that served the call). `gateway.attempts` shows every attempt.

Slugs are checked for shape only (letters, digits, `.`, `_`, `-`; no duplicates). Live slugs include camelCase ones (`vertexAnthropic`, `claudeaws`). The gateway publishes no canonical slug list a client can validate against. Unset fields are omitted; with no routing at all the request body carries no `providerOptions`.

Regional hosts (`eu.ai-gateway.vercel.sh`, `us.ai-gateway.vercel.sh`) carry a 5% surcharge and return 403 on the free tier, per Vercel's pricing page. Set them with `vercel_gateway_endpoint`.

## Thinking and reasoning

- **Messages API** (Claude ids by default): `thinking_level` maps to Anthropic thinking exactly as on the `anthropic` provider (adaptive on 4.6+/5, budget tiers on older models). Dotted gateway ids (`claude-sonnet-4.6`) are recognised by the adaptive-thinking and pricing matchers. Signed thinking produced on `anthropic` replays to `bedrock` and to `vertex` (verified live 2026-11-19 with `only` pinned per turn). Adaptive thinking decides per request whether to think; a trivial request may get none.
- **Chat Completions API**: when the catalog tags the model `reasoning` (or the id is Claude-family), loom sends `"reasoning": {"enabled": true, "effort": "low"|"medium"|"high"}` (`auto` sets no effort; `xhigh`/`max` map to `high`). Models the catalog does not know get no reasoning field. The response's `reasoning_details` entries are kept as thinking blocks (type `reasoning.text`, `reasoning.summary` or `reasoning.encrypted`) and replayed within the same turn. Whether the gateway requires that replay is *unverified*.
- **Responses API**: `thinking_level` maps to `reasoning: {effort, summary: "auto"}` (`auto` → `medium`) for every model, with `include: ["reasoning.encrypted_content"]`. Returned `reasoning` items are kept as thinking blocks (type `responses.reasoning`) and replayed before the turn's function calls (verified live on GPT-5.5 and Sonnet 5.5). GPT-5.5 returned no reasoning items on a trivial tool task even at `medium` effort; on a task that needs thought it does.

## Model catalog and cost

In `looms` (serve, workflow, eval), the first gateway client built for an endpoint (main LLM, pool entry, an agent's own `llm` block, a reload) starts the catalog for that endpoint in the background (the build does not wait): `GET /v1/models` (no credential needed) once, then every hour. Each endpoint gets its own catalog entry, so a regional host does not replace the global one. The registry without `looms`, `pkg/builder`, `loom-standalone` and `loom decision` do not fetch it; call `vercelgateway.RegisterCatalog` to get it there. Language models are registered under the catalog namespace `vercel-ai-gateway` with context window, max output tokens, capabilities (the entry's `tags`), `is_reasoning`, and per-1M input/output/cache-read/cache-write rates. A failed fetch is logged; serving continues, and the background refresh fills the catalog in once the gateway answers. A failed refresh keeps the previous catalog.

Prices in `/v1/models` are decimal strings in USD per token (confirmed live: Haiku 4.5 lists `0.000001`/`0.000005`, its $1/$5 card). Loom multiplies by 10⁶.

**Price tiers.** 95 catalog models (2026-11-18) publish `input_tiers`, `output_tiers` and cache tiers keyed on prompt size, for example `openai/gpt-5.5` at $5/$30 per 1M below 272,000 prompt tokens and $10/$45 above. Loom stores them (proto `ModelInfo.price_tiers`) and prices a request with the tier that contains its prompt (input tokens, cache included), applied to the whole request. Verified live 2026-11-19: `alibaba/qwen3.7-flash` at 45,014 prompt tokens and `anthropic/claude-haiku-5.5` at 190,016 were billed by the gateway at exactly the whole-prompt tier rate ($0.004534 and $0.095020), not the flat rate ($0.001361, $0.019004) or a marginal split.

Cost per call, in order:

1. The cost the gateway reports in the response. Observed live on every call: on the Messages API `provider_metadata.gateway.cost` (body and the stream's `message_delta`); on Chat Completions `usage.cost` (and `choices[0].message/delta.provider_metadata.gateway.cost`); on Responses `provider_metadata.gateway.cost` (body, and `response.provider_metadata` in the stream's `response.completed`).
2. A catalog estimate: the gateway catalog's rates for the prompt's tier (published cache rates win over vendor multipliers), else the Claude family card for Claude ids.
3. Otherwise, on the Chat surface, an **estimate**: the median input and median output rate of the same creator's priced models in the gateway catalog (`openai/*` for `openai/new-model`), or of every priced catalog model when the creator has none (`catalog.CreatorMedianPricing`). The call is flagged `llm.cost_unpriced = true` and `llm.cost_estimated = true`, `Usage.CostEstimated` is set, `LLMCost.cost_estimated` is true in `Weave`/`StreamWeave` responses, and the TUI sidebar and `loom chat` show the cost with an asterisk (`$0.42*`). Cost ceilings (eval `max_cost_usd`, session totals) count the estimate. The asterisk is live only: stored messages and session totals do not keep it. With no priced catalog model at all (catalog not loaded), the cost is 0 with `llm.cost_unpriced = true` only. On the Messages surface, a Claude id the family table does not recognise is priced at the Sonnet 4 card ($3/$15 per 1M) without the unpriced flag, as on the `anthropic` provider.

Output cap: under `looms serve` the request carries `llm.max_tokens` (looms default 4096) or the agent's `max_tokens`. Only the provider factory path reads the catalog's `max_tokens` when none is configured (fallback 4096). Context window: loom's memory budgets against `llm.max_context_tokens` when set, else its built-in default (200K); the catalog's `context_window` is stored but no serve path reads it today. Set `max_context_tokens` for models with a smaller window.

## Errors, retries and rate limits

| Condition | Behaviour |
|---|---|
| 429, 500/502/503/504/529 | Retried by the client-side limiter with backoff, honouring `Retry-After` |
| 402 | `vercelgateway.ErrCreditsExhausted` ("payment required … credits exhausted, budget exceeded, or BYOK without a positive credit balance"); not retried |
| 401/403/404, unsatisfiable `only` | Terminal (deterministic client error) |
| No first byte within `StreamFirstByteTimeout` (default 60 s), or silence longer than `StreamIdleTimeout` (default 90 s) | Error wrapping `llm.ErrStreamTimeout`. Retried once when no token had reached the caller; after a token was delivered a retry would repeat text, so the error is returned |
| Stream ends without `message_stop` (Messages), without `finish_reason` or `[DONE]` (Chat), or without `response.completed`/`incomplete`/`failed` (Responses) | Error (`truncated response`), not a partial answer |
| Error payload after a stream started | Returned as an error (status from the error type) instead of a truncated success |
| Context too long | 400 with `error.type` `AI_APICallError` and the upstream message passed through (live). Loom's matchers recognise every API's wording, so context relief fires. |

Timeouts: every model call is streamed, `Chat` included, so a long answer that keeps producing bytes is never cut off by an overall deadline (live: a 48,000-token Sonnet 5.5 stream ran 6 min 38 s to completion). The defaults come from live measurements on 2026-11-19: response headers arrived within 6 s, and the longest silence inside a stream was 13.4 s (GPT-5.5, `xhigh` reasoning, Chat Completions). Before this change a stalled call held an agent for the full 300 s request timeout (once in 186 evaluation runs). `Timeout` (default 300 s) now bounds only the health check.

The limiter is on by default. The gateway does not rate limit paid-tier requests and publishes no free-tier limit, so unset limiter fields take loom's generic defaults (`llm.DefaultRateLimiterConfig`: 2 req/s, burst 5) on **both** surfaces; without this, Claude ids would inherit the anthropic client's Tier 1 limits (0.7 req/s). Raise `llm.rate_limit.*` for a large fleet on a paid team. The limiter key includes the credential scope, endpoint and model, so gateway traffic never shares a bucket with direct traffic.

## Observability

Every call's response metadata, and the LLM span, carry (when the response provides them):

| Attribute | Value |
|---|---|
| `gateway.endpoint_surface` | `messages`, `chat` or `responses` (always set) |
| `gateway.generation_id` | `gen_…` id |
| `gateway.final_model` | model the response reports |
| `gateway.fallback_used` | `true` when the served model differs from the requested one, or more than one model was attempted. A served id without the creator prefix, with dashes for dots (`claude-sonnet-5-5`) or with a date suffix (`gpt-5.5-2026-04-23`) is the same model |
| `gateway.final_provider` | upstream that served the call |
| `gateway.attempt_count` | provider attempts |
| `gateway.attempts` | every attempt in order as `provider/credential/status` (`anthropic/byok/400,anthropic/system/200`) |
| `gateway.credential_type` | credential of the attempt that served: `byok` or `system` |
| `gateway.session_source` | where the gateway took the affinity key: `explicit` (loom's `x-session-id`), `fingerprint`, … |
| `llm.cost_unpriced` | `true` when neither the gateway nor a published rate priced the call |
| `llm.cost_estimated` | `true` when the cost is the creator-median estimate (shown with an asterisk) |

No prompt content or key material is recorded. The session id sent to the gateway is a digest, not loom's session id.

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

`-run TestLiveComplete` covers the Responses API, BYOK (needs `LOOM_LLM_ANTHROPIC_API_KEY`), session affinity, the remaining routing keys, `anthropic-beta`, thinking replay across upstreams, cache usage per upstream and price tiers (about $0.25 in all). `VERCEL_GATEWAY_LIVE_LONG=1` adds a 48,000-token stream (about $0.50).

## Limitations and unverified behaviour

Verified live on 2026-11-16 (details in [the spike record](../research/vercel-ai-gateway-spike.md)): cost and routing metadata locations, the generation id, the pricing unit, `providerOptions.gateway` in the raw body (an unsatisfiable `only` is rejected), Claude signed-thinking replay over a 3-turn tool loop, GPT-5 mini `reasoning_details` replay, context-overflow bodies, `/v1/credits`, `/v1/embeddings`.

Verified live on 2026-11-19 (`TestLiveComplete_*`, results in [the evaluation](../research/vercel-gateway-eval-results.md) §16): the Responses API on GPT-5.5 and Sonnet 5.5 (text, streaming, tool loop, reasoning replay, cost); every routing key in the table above; BYOK credentials reach the upstream; session affinity; `anthropic-beta` accepted; thinking replay from anthropic to bedrock and vertex; cache read/write fields from anthropic, bedrock and vertex; whole-prompt tier pricing; a 6 min 38 s stream.

Not verified, and why:

- **OIDC** (question 32): implemented and unit-tested; needs a Vercel project token (`vercel env pull`), which this account has not run.
- **A BYOK call served by the customer's key**: the key was sent and tried, but the test Anthropic key had no credit (upstream 400) and the gateway fell back to its own key.
- **402, forced 429 and mid-stream failure bodies** (questions 25–27): none can be provoked on demand on a paid team without draining credit; the parsers are unit-tested against the documented shapes.
- **Whether `reasoning_details` replay on Chat Completions is required** rather than merely accepted (question 14), and whether `zeroDataRetention: true` changes routing (question 23): accepted live; the response does not show the policy.
- **Enterprise concurrency limits** (question 29).

📋 Not implemented: none of the gateway's documented request options is missing. The `internal/catwalk` package is an unused stub; the TUI model picker reads `ListAvailableModels`, which lists the gateway catalog.

## See Also

- [LLM providers](./llm-providers.md)
- [Plan](../plans/vercel-ai-gateway-plan.md)
- [Bedrock Mantle](./llm-bedrock-mantle.md) (the same dual-routing design on AWS)
