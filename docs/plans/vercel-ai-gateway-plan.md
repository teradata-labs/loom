# Plan: Vercel AI Gateway as an LLM backend

**Status**: 📋 Planned. Nothing in this document is implemented.
**Written**: 2026-11-13, against `main` @ `ab46f76f`.
**Scope**: a new `vercel-ai-gateway` LLM provider for agents and `looms serve`. The decision layer's existing use of the gateway for Jev (`pkg/decision/jev`) is unchanged.

## 1. What the gateway is

One host, `https://ai-gateway.vercel.sh`, that fronts many upstream providers under `creator/model` ids (`anthropic/claude-sonnet-4.5`, `openai/gpt-5`). It bills at provider list price with no token markup. Facts below come from Vercel's docs, read on 2026-11-13. Items marked **unverified** are not stated in the docs and are settled by the Phase 0 spike.

| Surface | Path | Auth |
|---|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` | Bearer |
| Anthropic Messages | `POST /v1/messages` | Bearer or `x-api-key` |
| OpenAI Responses | `POST /v1/responses` | Bearer |
| Embeddings | `POST /v1/embeddings` | Bearer |
| Model catalog | `GET /v1/models` | none |
| Credits | `GET /v1/credits` | Bearer |
| Generation lookup | `GET /v1/generation?id=gen_…` | Bearer |

- **Auth**: `AI_GATEWAY_API_KEY`, or a Vercel OIDC token (`VERCEL_OIDC_TOKEN`, valid 12 hours, no refresh path for raw HTTP clients). If both are sent the API key wins, even when it is invalid.
- **Routing**: a `providerOptions.gateway` object in the request body, accepted on all three inference surfaces. Keys: `order`, `only`, `sort` (`cost` | `ttft` | `tps`), `models` (fallback model ids), `byok`, `providerTimeouts`, `sessionAffinity`, `caching` (`auto`), `zeroDataRetention`.
- **Catalog entry**: `id`, `type` (`language`, `embedding`, `image`, …), `context_window`, `max_tokens`, `tags` (`reasoning`, `tool-use`, `vision`, `file-input`), `pricing.{input, output, input_cache_read, input_cache_write, input_tiers, output_tiers}` as decimal strings. The sample value `"0.000002"` implies USD per token; the docs do not state the unit (**unverified**).
- **Reasoning on Chat Completions**: request `reasoning: {enabled, effort, max_tokens, exclude}`; response `message.reasoning` plus `message.reasoning_details[]` (entries carry an Anthropic `signature`). This is not the LiteLLM shape (`thinking`, `thinking_blocks`, `reasoning_content`) that `pkg/llm/openai` speaks today.
- **Errors**: OpenAI-shaped `{"error": {message, type, param, code}}`. 429 with an optional `retry-after`. 402 for exhausted budget or BYOK without a positive credit balance. Paid tier is not rate limited by the gateway; free tier has an unpublished per-model limit.
- **Regional hosts**: `eu.` and `us.ai-gateway.vercel.sh`, 5% surcharge, 403 on free tier.

## 2. What loom already has

- `pkg/llm/litellm/client.go` (225 lines): a thin wrapper that points `openai.Client` at a gateway. The closest precedent for the OpenAI-shaped half.
- `pkg/llm/bedrockmantle`: one provider that routes Claude ids to `anthropic.Client` and everything else to `openai.Client`. The precedent for dual routing.
- `openai.Config` and `anthropic.Config` both expose `Endpoint`, `CatalogProvider`, `WrapTransport`; anthropic also has `NoBetaHeaders`; openai has `ExtraHeaders`.
- `catalog.Source` + `CachedSource` + `MultiSource`: dynamic model metadata and pricing. `factory.OllamaCatalogSource` is the precedent for a network-discovered source.
- `LLMConfig.provider` in `proto/loom/v1/agent_config.proto` is a string, so a new provider name alone needs no proto change.
- `AI_GATEWAY_API_KEY` is already read by `pkg/decision/jev/config.go`.

Gaps found while reading the code:

1. Neither client can add fields to the request body, so `providerOptions.gateway` cannot be sent.
2. The openai client reads gateway cost only from the `x-litellm-response-cost` header (`client.go:917`).
3. The openai client's thinking path is LiteLLM-shaped; `reasoning_details` is not parsed or replayed.
4. `catalog.Register` replaces the whole default source. `RegisterOllamaCatalogSource` installs `{ollama, static}`, so a second discovered source would be dropped or would drop Ollama's.
5. `ModelInfo` has no cache-read, cache-write or tiered rate fields, so the gateway's published cache and long-context prices cannot be stored.
6. A provider is wired in five separate switches (`factory.go:203`, `cmd_serve.go:842`, `:935`, `:1140`, `pkg/agent/registry.go:1382`) plus two validation lists (`cmd/looms/config.go:2011`, `pkg/agent/config_loader.go:1508`).

## 3. Design

### 3.1 Dual routing (recommended)

`pkg/llm/vercelgateway.Client` holds two inner clients and picks by model id:

- `anthropic/*` → `anthropic.Client` at `/v1/messages`. Thinking signatures, `cache_control` breakpoints, and cache-token usage then travel in the native shape loom already round-trips and tests.
- everything else → `openai.Client` at `/v1/chat/completions`.

The alternative is Chat Completions for every model. It is less code, but Claude thinking through it depends on gap 3, and a dropped signature breaks multi-turn tool loops with thinking on. Dual routing avoids shipping Claude on the weaker path first.

Cross-family fallback (`models: ["openai/gpt-5"]` on a Claude primary) stays legal: the gateway translates on whichever surface received the request.

### 3.2 Configuration

```yaml
llm:
  provider: vercel-ai-gateway                              # alias: vercel
  vercel_gateway_model: anthropic/claude-sonnet-4.5
  vercel_gateway_endpoint: https://ai-gateway.vercel.sh   # optional; regional hosts go here
  # vercel_gateway_api_key: keyring (looms config set-key vercel_gateway_api_key) or AI_GATEWAY_API_KEY
  vercel_gateway_routing:
    order: [bedrock, anthropic]
    only: []
    sort: ""                    # cost | ttft | tps
    fallback_models: [openai/gpt-5]
    zero_data_retention: false
    caching: ""                 # auto
```

Key resolution: config/keyring → `LOOM_LLM_VERCEL_GATEWAY_API_KEY` → `AI_GATEWAY_API_KEY`. The endpoint is a bare host; the client appends the surface path. There is no default model: an empty model is a config error, because a default would go stale with the gateway's catalog.

### 3.3 Proto (first, per "proto is law")

Add to `agent_config.proto`:

```proto
message LLMGatewayRouting {
  repeated string order = 1;
  repeated string only = 2;
  string sort = 3;
  repeated string fallback_models = 4;
  bool zero_data_retention = 5;
  string caching = 6;
}
// LLMConfig: LLMGatewayRouting gateway_routing = 12;
```

The message is named for gateways in general so a later OpenRouter-style backend can reuse it. `byok`, `providerTimeouts` and `sessionAffinity` are left out of the first cut: BYOK credentials are configured in the Vercel dashboard in the normal case, and per-request credentials in agent YAML are a secret-handling surface this plan does not want to open without a need.

If Phase 3 stores cache and tiered rates, `ModelInfo` gains `cost_per_1m_cache_read_usd` and `cost_per_1m_cache_write_usd` (additive, `buf breaking` clean).

### 3.4 Request-body extension

Add `ExtraBody map[string]any` to `openai.Config` and `anthropic.Config`, merged into the top level of the marshalled request. A key that collides with a field the client itself sets (`model`, `messages`, `tools`, `stream`, …) is rejected at `NewClient`, not at request time. `vercelgateway` builds `{"providerOptions": {"gateway": {...}}}` from the typed routing config; agent YAML never writes raw body JSON.

### 3.5 Catalog and cost

- `vercelgateway.CatalogSource(ctx, endpoint)` fetches `GET /v1/models`, keeps `type == "language"`, and maps `context_window`, `max_tokens`, pricing (string → per-1M float) and tags → capabilities into `ModelInfo` under provider key `vercel-ai-gateway`. Wrapped in `catalog.NewCachedSource`.
- Fix gap 4 first: add `catalog.RegisterAhead(src)` (or equivalent) that composes onto the registered chain, and move `RegisterOllamaCatalogSource` onto it with a regression test for Ollama + gateway together.
- Opt-in network I/O at `looms serve` startup when the provider is configured, matching the Ollama rule that importing the package never dials out. On fetch failure: log, keep running, and price Claude ids through `ClaudeFamilyPricing`. Other ids are reported as unpriced (cost 0 with a `cost_unpriced` span attribute), not at the gpt-4o fallback rate.
- Per-request cost: where the gateway reports cost in a raw REST response is **unverified** (the docs show it only via the AI SDK's `providerMetadata.gateway.cost`). Generalise the openai client's header-only `parseProviderCost` into a small extractor the wrapping provider supplies, and do the same on the anthropic client. `/v1/generation` is eventually consistent ("Usage event not found" right after completion), so it stays off the request path.
- Dotted ids: confirm `catalog.ClaudeFamilyPricing` and the adaptive-thinking matcher in `pkg/llm/adaptive_thinking.go` recognise `anthropic/claude-sonnet-4.5` as well as hyphenated ids. Not yet checked.

### 3.6 Errors, retries, rate limiting

- Limiter key includes credential scope, endpoint and model (already how both inner clients key it), so gateway traffic never shares a bucket with direct traffic.
- 429 and 5xx go through the existing retry path (`pkg/llm/transient.go`, `throttle.go`); honour `retry-after`.
- 402 is terminal: no retry, and the error text names credits/budget so the operator knows it is not an auth problem.
- A failed `only` filter is terminal.
- Context-overflow detection (`pkg/llm/errors.go`) must be checked against the gateway's error body for both surfaces; the shape through the gateway is **unverified**.

### 3.7 Health check and observability

- `HealthCheck` → `GET /v1/credits`. It validates the key without spending tokens. `/v1/models` is public and would pass with a bad key.
- Span attributes on every call: `gateway.endpoint_surface` (`messages` | `chat`), `gateway.final_provider`, `gateway.generation_id`, `gateway.fallback_used`, `gateway.attempt_count`, when the response carries them. No prompt content, no key material; credentials only as `llm.CredentialScope`.

### 3.8 Auth beyond API keys

OIDC is Phase 5. A token source re-reads `VERCEL_OIDC_TOKEN` (or a token file) per request through `WrapTransport`, because the token rotates and nothing refreshes it for a Go client. The client never sends both credentials, since an invalid API key would mask a valid token.

## 4. Phases

Each phase is one PR, stacked, each passing `just check`.

### Phase 0 — Live spike (no product code)

Output: `docs/research/vercel-ai-gateway-spike.md` with captured request/response pairs (keys redacted), and an answer or "still open" against each question in §7. Uses the enterprise gateway account.

- [ ] Raw chat response and stream: where cost, generation id and routing metadata appear; whether `stream_options.include_usage` is honoured; cached-token field names in `usage`
- [ ] `/v1/messages`: does it accept `anthropic-beta` (decides `NoBetaHeaders`); is `anthropic-version` required; usage and cache fields
- [ ] Claude thinking + tool loop over `/v1/messages`, 3+ turns, signatures replayed
- [ ] Same loop over `/v1/chat/completions` to record what `reasoning_details` replay requires
- [ ] `providerOptions.gateway` in the raw body on both surfaces: `order`, `only`, `models`, a deliberately unsatisfiable `only`
- [ ] Error bodies: bad key, unknown model, context overflow (both surfaces), 402, forced 429; mid-stream failure shape
- [ ] `/v1/models` live shape and pricing unit; `/v1/credits` shape
- [ ] Dotted-id behaviour of loom's Claude matchers

### Phase 1 — Proto + provider + wiring

- [ ] `LLMGatewayRouting` in proto; `buf lint`, `buf breaking`, `just proto`
- [ ] `pkg/llm/vercelgateway`: `Config`, `Client` (dual routing), `HealthCheck`, endpoint normalisation
- [ ] Wire into the five switches and two validation lists in §2 gap 6, viper defaults, keyring entry, `--llm-provider` help, sample YAML, `cmd/loom/decision.go` provider list
- [ ] Tests: table-driven routing by model id; `httptest` Chat/ChatStream/tools on both surfaces; 402/429/5xx classification; `-race`; fuzz for model-id parsing and endpoint normalisation
- [ ] Live test behind an env gate, like `bedrockmantle/live_test.go`
- [ ] Docs: `docs/reference/llm-vercel-ai-gateway.md`, `llm-providers.md`, `ENV_VARS.md`

### Phase 2 — Routing options

- [ ] `ExtraBody` on both inner clients with collision rejection and golden request-body tests
- [ ] Typed routing config → `providerOptions.gateway`; config validation (`sort` enum, `only`/`order` slugs non-empty)
- [ ] Routing metadata → span attributes
- [ ] Docs: routing section with the surcharge table from Vercel's pricing page

### Phase 3 — Catalog and cost

- [ ] `catalog` composition fix + Ollama regression test
- [ ] `vercelgateway.CatalogSource` + cached registration at serve startup; fuzz the `/v1/models` decoder
- [ ] Cost extractor hook on both inner clients; gateway-reported cost preferred, catalog estimate as fallback, unpriced reported as unpriced
- [ ] Optional `ModelInfo` cache-rate fields if the spike shows the estimate path is used in practice
- [ ] `max_tokens` and context window resolve from the gateway catalog (`resolveMaxOutput`)

### Phase 4 — Reasoning on the Chat surface

- [ ] `reasoning` request object and `reasoning_details` parse/replay in `pkg/llm/openai`, gated so LiteLLM requests stay byte-identical
- [ ] Thinking for non-Claude reasoning models (`reasoning.effort`) driven by the catalog's `reasoning` tag

### Phase 5 — OIDC, embeddings, regional

- [ ] OIDC token source
- [ ] `/v1/embeddings` through `pkg/embedding` (body shape **unverified**)
- [ ] Regional endpoint documented with its surcharge and tier restriction

### Dogfood gate (after Phases 1, 2 and 4)

Run an existing loom agent with tools and thinking on through the gateway for a multi-turn session, once on a Claude id and once on a non-Claude id, and compare token counts and cost against the same session run direct.

## 5. Risks

| Risk | Mitigation |
|---|---|
| Docs and live behaviour differ (the docs contradict themselves on the `/v1/models` shape and the completion `id`) | Phase 0 before any product code; findings recorded with captures |
| Gateway rejects `anthropic-beta` | `NoBetaHeaders` already exists; spike decides |
| Fallback to a different model family mid-conversation changes tool-call or thinking shape | Record `final_provider` per turn; test a forced fallback in the live suite |
| Catalog fetch adds a startup dependency on a public endpoint | Non-fatal, cached, opt-in |
| Free-tier per-model limits are unpublished | Default limiter stays on; reference doc states the limit is unknown |
| A sixth and seventh copy of provider wiring | Out of scope here; worth its own change to collapse the switches onto `factory` |

## 6. Decisions (settled 2026-11-13)

1. **Dual routing** (§3.1): Claude ids on the Messages surface, everything else on Chat Completions.
2. **Provider name** `vercel-ai-gateway`, with `vercel` accepted as an alias. The alias is normalised to the canonical name at config load, so limiter keys, catalog namespace and span attributes only ever see `vercel-ai-gateway`.
3. **`LLMGatewayRouting` is a general gateway message**, not Vercel-specific.
4. **Per-request BYOK credentials are out of scope.** BYOK keys are configured in the Vercel dashboard.
5. **Phase 0 key**: an enterprise gateway account is available for the spike and the live tests.

## 7. Open questions for Vercel

These are the points the public docs leave unstated or state two ways. Each one names the design choice in this plan that depends on the answer. The Phase 0 spike will probe them all against the live gateway; an authoritative answer is still worth having, because a probe shows today's behaviour and not what is contractual.

### Cost and metadata in raw REST responses

1. **Where is per-request cost in a raw (non-AI-SDK) response?** The docs show `providerMetadata.gateway.cost`, `marketCost` and `generationId` only through the AI SDK. For `POST /v1/chat/completions` and `POST /v1/messages` called over plain HTTP: is cost in the response body (which field), in a response header, or only available from `GET /v1/generation`? Same question for the final chunk of a stream. *(§3.5: decides whether cost is read inline or estimated.)*
2. **Where is routing metadata** (`routing.finalProvider`, `modelAttempts[]`, `fallbacksAvailable`) in a raw response, on both surfaces and in streams? *(§3.7 span attributes.)*
3. **Is the completion `id` the generation id?** The REST API page says the `gen_…` id is returned as the `id` field of every chat completion; the reasoning page's sample shows `"chatcmpl-123"`. Which is correct, and what carries the generation id on `/v1/messages`?
4. **How long after a response is `GET /v1/generation` consistent?** Is there a bound on the "Usage event not found" window? *(Decides whether a deferred cost reconciliation is practical.)*

### Catalog

5. **Unit of `pricing.*` in `GET /v1/models`.** The sample `"0.000002"` reads as USD per token. Please confirm, and confirm the unit for `input_cache_read`, `input_cache_write`, and the shape of `input_tiers` / `output_tiers` (threshold field name, whether a tier's rate applies to the whole prompt or only the tokens above the threshold).
6. **Which `/v1/models` shape is contractual?** The Chat Completions page shows entries with only `id`, `object`, `created`, `owned_by`; the REST API page documents `context_window`, `max_tokens`, `type`, `tags`, `pricing`, `modalities`, `supported_parameters`, `reasoning_options`. Are the richer fields stable to build on?
7. **Are model ids stable, and are there pinned ids?** The catalog uses dotted ids (`anthropic/claude-sonnet-4.5`). Are these floating aliases or fixed versions, are dated/pinned ids accepted, and how is a deprecation announced?
8. **Is there a recommended polling interval or an ETag/`Cache-Control` for `/v1/models`?** *(§3.5: cache TTL for the catalog source.)*

### Anthropic Messages surface

9. **Is the `anthropic-beta` header accepted, ignored, or rejected?** Another Anthropic-compatible gateway we support answers 400 to any beta value. If accepted, which beta values are forwarded, and does forwarding depend on which upstream (Anthropic, Bedrock, Vertex) serves the request?
10. **Is `anthropic-version` required?**
11. **Thinking signatures across turns.** With `thinking` on and a tool loop, must the client replay `thinking` blocks with their `signature` exactly as with Anthropic direct? If a later turn is served by a different upstream (session affinity miss or `order` fallback from Anthropic to Bedrock), does the signature from the earlier upstream still validate? Are `redacted_thinking` blocks passed through?
12. **Does `usage` on `/v1/messages` always carry `cache_creation_input_tokens` and `cache_read_input_tokens`**, including when Bedrock or Vertex serves the request?
13. **Non-Claude models on `/v1/messages` and Claude on `/v1/chat/completions`.** Both are legal per the docs. When `providerOptions.gateway.models` falls back from a Claude primary to a non-Claude model on the Messages surface, what happens to `thinking`, `cache_control` and tool-call ids in the translated request and response? *(§3.1: cross-family fallback.)*

### Chat Completions surface

14. **`reasoning_details` replay.** The docs say the gateway "preserves reasoning details across interactions". Does the client have to send `reasoning_details` back on the assistant message in the next request, or is preservation server-side (keyed on what)? *(Phase 4.)*
15. **`stream_options.include_usage`**: honoured, ignored, or is usage always sent on the final chunk?
16. **Cached-token fields in chat `usage`.** Is it `prompt_tokens_details.cached_tokens`, and is there a cache-write count? Does `prompt_tokens` include cached tokens (OpenAI convention) for Anthropic-served models too?
17. **`cache_control` placement.** The advanced page shows it at message level. Is it also accepted on content blocks and on tool definitions, and is it silently dropped or rejected for upstreams that do not support it? How does explicit `cache_control` interact with `providerOptions.gateway.caching: "auto"`?
18. **`reasoning_content`**: is it ever emitted (some OpenAI-compatible clients read it), or only `reasoning` / `reasoning_details`?

### Routing

19. **Is `providerOptions.gateway` in the raw JSON body the supported REST form** on all three surfaces, with exactly the camelCase keys the AI SDK uses? Is there a snake_case form? Is an unknown key ignored or a 400?
20. **Canonical list of provider slugs** for `order` / `only`, and is it available from an endpoint (the per-model `/endpoints` response?) so a client can validate config without hardcoding the list?
21. **`providerTimeouts` unit** (the sample `3000` reads as ms), and whether it applies to system credentials or only BYOK.
22. **Session affinity.** What is the affinity key (API key, conversation prefix hash, something else) and how long does it last? *(Affects prompt-cache hit rates and question 11.)*
23. **Exact shapes of `zeroDataRetention`, `disallowPromptTraining`, `inferenceRegion` and `has`** inside `providerOptions.gateway`.

### Errors, limits, timeouts

24. **Context-length overflow.** What status, `error.type` and `error.code` does the gateway return on each surface when the upstream rejects a prompt as too long? Is the upstream's message passed through or rewritten? Does the code differ by upstream? *(§3.6: loom compacts and retries on this error, so it has to be recognised.)*
25. **Mid-stream failure.** If the upstream fails after the first chunk, what does the client see: an SSE `error` event (shape?), a terminated connection, or a transparent retry on another provider? If the gateway retries mid-stream, can content be duplicated?
26. **Upstream failure → HTTP status mapping.** When every provider attempt fails, which status is returned, and is the upstream's status or error visible? Is an upstream 429 distinguishable from a gateway 429?
27. **402 body shapes** for: credits exhausted, budget exceeded (`quota_for_entity_exceeded`), BYOK with a non-positive balance.
28. **Maximum request duration and stream idle timeout** enforced by the gateway. *(Sets client timeouts; long thinking turns can be silent for over a minute.)*
29. **Enterprise rate limits.** The docs say paid-tier requests are not rate limited. Is there a connection or concurrency ceiling per key or team? We run fleets of several hundred concurrent agents on one credential.
30. **Heartbeats.** The streaming page mentions SSE comment lines (`: heartbeat`). At what interval, on which surfaces, and do they continue while an upstream is thinking?

### Auth

31. **Is there any header that selects a team or project** when a credential can see more than one, or is it always inferred from the credential?
32. **OIDC outside Vercel.** For a long-running Go service not deployed on Vercel, is there a supported way to obtain and refresh an OIDC token without the `vercel` CLI, or is an API key the intended credential there? The OIDC page gives two lifetimes (valid 12 hours; rotated in under an hour) — which should a client plan for?
33. **Does `GET /v1/credits` work with an enterprise/invoiced account**, and is it a suitable cheap credential check? *(§3.7 health check.)*

### Embeddings

34. **`POST /v1/embeddings` request and response shape**, batch size limit, and whether `dimensions` and `encoding_format` are passed through.

## Sources

- https://vercel.com/docs/ai-gateway
- https://vercel.com/docs/ai-gateway/sdks-and-apis/openai-chat-completions (and `/advanced`, `/streaming`, `/reasoning`)
- https://vercel.com/docs/ai-gateway/sdks-and-apis/anthropic-messages-api (and `/reasoning`)
- https://vercel.com/docs/ai-gateway/sdks-and-apis/openresponses
- https://vercel.com/docs/ai-gateway/sdks-and-apis/rest-api
- https://vercel.com/docs/ai-gateway/authentication-and-byok (and `/byok`, `/oidc`)
- https://vercel.com/docs/ai-gateway/models-and-providers/provider-filtering-and-ordering
- https://vercel.com/docs/ai-gateway/pricing
- https://vercel.com/docs/ai-gateway/rate-limits
