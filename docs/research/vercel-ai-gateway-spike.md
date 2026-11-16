# Vercel AI Gateway live spike

**Date**: 2026-11-16. **Account**: the team gateway key (credits balance $505 at start; spend for the whole spike about $0.011).
**Models**: `anthropic/claude-haiku-4.5` (Messages surface), `openai/gpt-5-mini` (Chat Completions surface), `openai/text-embedding-3-small`.
**How**: `VERCEL_GATEWAY_LIVE=1 VERCEL_GATEWAY_LIVE_CAPTURE_DIR=… go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/vercelgateway/ -v` (exit 0, twice), plus curl probes for context overflow and embeddings. Captures were taken with credentials redacted; the shapes below are trimmed from them.

This is the Phase 0 output of [the plan](../plans/vercel-ai-gateway-plan.md). It records what the gateway did on this date, not what is contractual.

## Answers to the plan's §7 questions

| # | Question | Observed |
|---|---|---|
| 1 | Where is per-request cost in a raw response? | **Messages**: `provider_metadata.gateway.cost` (decimal string), top level of the body, and on the stream's `message_delta` event. **Chat**: `usage.cost` (JSON number), with `usage.market_cost` and `usage.gateway_cost`; also `choices[0].message.provider_metadata.gateway.cost` (blocking) and `choices[0].delta.provider_metadata.gateway.cost` (stream). The stream's final usage chunk carries `usage.cost`. Breakdown keys: `marketCost`, `surchargeCost`, `gatewayCost`, `inferenceCost`, `inputInferenceCost`, `outputInferenceCost`. |
| 2 | Where is routing metadata? | Same objects: `provider_metadata.gateway.routing` with `originalModelId`, `resolvedProvider`, `fallbacksAvailable[]`, `canonicalSlug`, `finalProvider`, `modelAttemptCount`, `modelAttempts[].providerAttempts[]` (`provider`, `credentialType`, `success`, `startTime`, `endTime`, `providerRequestId`, `statusCode`), `totalProviderAttemptCount`, `clientSessionId`, `clientSessionIdSource` (`fingerprint`). The key is snake_case `provider_metadata`, not the docs' `providerMetadata` (except on error bodies and embeddings, which use `providerMetadata`). |
| 3 | Is the completion `id` the generation id? | Yes, `gen_…` on both surfaces. Chat bodies and stream chunks also carry a top-level `generationId`. |
| 5 | Unit of `/v1/models` pricing | USD per token: Haiku 4.5 `0.000001`/`0.000005` = the $1/$5 card. Cache rates present (Haiku read $0.10, write $1.25 per 1M; GPT-5 mini read $0.025, write unpublished). |
| 9 | `anthropic-beta` | Not tested: loom sends none. `anthropic-beta` is in the CORS `Access-Control-Allow-Headers` list, which does not prove it is forwarded. |
| 10 | `anthropic-version` | Sent (`2023-06-01`) and accepted. Not tested without it. |
| 11 | Thinking signatures across turns | Haiku 4.5 with thinking on, 3-turn tool loop on `/v1/messages`, signed blocks replayed: accepted, same upstream every turn. Cross-upstream replay not tested. |
| 14 | `reasoning_details` replay | GPT-5 mini returns `reasoning_details` entries of type `reasoning.encrypted`. Replaying them in-turn (3-turn tool loop) is accepted. Whether replay is required is still open. |
| 15 | `stream_options.include_usage` | Honoured: a final chunk with `usage` (and `usage.cost`). |
| 19 | `providerOptions.gateway` in a raw body | Honoured on both surfaces: an unsatisfiable `only` returns 400 `No available providers match the 'only' filter: no-such-provider. Available providers are: anthropic, bedrock, claudeaws, vertexAnthropic`. `sort: ttft` and `caching: auto` are accepted. |
| 20 | Provider slugs | From that error: `anthropic`, `bedrock`, `claudeaws`, `vertexAnthropic` for Haiku 4.5; `openai`, `azure` for GPT-5 mini. Slugs can be camelCase. |
| 22 | Session affinity key | Responses report `clientSessionId` with source `fingerprint`; the key derivation is not visible. |
| 24 | Context-length overflow | Both surfaces: HTTP 400, `error.type` `AI_APICallError`, the upstream message passed through, no `code`. Messages: `prompt is too long: 300024 tokens > 200000 maximum`. Chat: `Your input exceeds the context window of this model. Please adjust your input and try again.` Error bodies also carry `providerMetadata.gateway.routing`. |
| 33 | `/v1/credits` on this account | Works: `{"balance":"504.98943946","token_pass":null,"total_used":"0.01056054"}` (strings). A bad key returns 401 `authentication_error`. |
| 34 | `/v1/embeddings` | OpenAI shape; `dimensions` honoured (256 returned). Body adds `providerMetadata.gateway` with `cost` and routing. |

Still open after the spike: 4 (generation lookup consistency), 6–8 (catalog stability, pinned ids, polling), 12–13 (cache usage on Bedrock/Vertex-served Messages, cross-family fallback), 16–18 (cache fields on Chat for Anthropic upstreams, `cache_control` placement, `reasoning_content`), 21, 23 (other `providerOptions` keys' shapes, including `zeroDataRetention`), 25–30 (mid-stream failure, 402 bodies, timeouts, enterprise concurrency, heartbeats), 31–32 (team header, OIDC outside Vercel).

## Other observed behaviour

- **Messages stream usage**: `message_start` reports `input_tokens: 0`. The real input count arrives in `message_delta.usage.input_tokens`, and cache counts only in `message_delta.provider_metadata.anthropic.usage` (`cache_read_input_tokens`, `cache_creation_input_tokens`).
- **Model echo**: the response `model` equals the requested gateway id (`anthropic/claude-haiku-4.5`, `openai/gpt-5-mini`), so a different `model` does indicate a fallback.
- **Unknown model**: Chat returns `Model 'acme/does-not-exist-model' not found` with type `model_not_found`.
- **Credential header**: `x-api-key` on `/v1/messages` and `Authorization: Bearer` on `/v1/chat/completions` and `/v1/credits` both work.

## Code changed because of the spike

Each change has a test built from the captured shape.

| Finding | Before | Change |
|---|---|---|
| Chat cost and routing live in `usage.cost` and `choices[0].message/delta.provider_metadata` | Chat calls were reported unpriced (cost 0) and without routing attributes | `vercelgateway` inspector reads those locations and the top-level `generationId` |
| Messages stream input and cache tokens arrive on `message_delta` | Streamed Messages calls reported 0 input tokens | anthropic stream parser takes non-zero `message_delta.usage` and `provider_metadata.anthropic.usage` counts |
| Context overflow comes as `AI_APICallError` with the upstream message | Both matchers missed it, so context relief would never fire for gateway traffic | `IsAnthropicContextTooLong` accepts `AI_APICallError`; `IsOpenAIContextTooLong` matches "exceeds the context window" |
| Slugs can be camelCase (`vertexAnthropic`) | Routing validation rejected them | slug pattern accepts upper-case letters |
| The live suite now asserts non-zero input tokens, a gateway-reported cost, a generation id and a final provider on every call | — | `assertGatewayFacts` in `live_test.go` |
