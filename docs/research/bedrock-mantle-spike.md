# Amazon Bedrock Mantle: live spike for a Loom provider

**Date**: 2026-11-04
**Ticket**: TER-1402 (Loom: add AWS Bedrock Mantle LLM provider)
**Status**: 📋 Planned. Nothing in this document is implemented in Loom yet. This is the Phase 0 spike that resolves the unknowns before implementation.

## Summary

Amazon Bedrock exposes a second endpoint surface, `bedrock-mantle`, that speaks the OpenAI Chat Completions API, the OpenAI Responses API, and the Anthropic Messages API. Loom's existing `bedrock` provider uses InvokeModel, Converse, and the Anthropic SDK's Bedrock middleware, none of which Mantle serves. This spike ran live probes against four Mantle regions to settle how a Loom provider must authenticate, address models, pick paths, and what breaks in the current HTTP clients.

Five findings drive the implementation:

1. **Any `anthropic-beta` header value is rejected with 400.** Loom's anthropic client sends one on every request, so every request would fail. Prompt caching works without the header.
2. **Long-term Bedrock API keys were rejected; short-term keys and SigV4 work.** Short-term keys expire in 12 hours and are region-scoped, so SigV4 via the AWS credential chain is the right default for a long-running server.
3. **Both `Authorization: Bearer` and `x-api-key` are accepted.** The anthropic client's existing header needs no change.
4. **Model IDs have no `us.` or `global.` prefix, and the listing is not an entitlement check.** Unsubscribed models return 401 or 403 with a marketplace-subscription message.
5. **Path prefix is per vendor.** `/openai/v1` for Gemma 4, Grok, and GPT-5.x/6.x; `/anthropic/v1/messages` for Claude; `/v1` for everything else. The wrong prefix returns a stable 400.

Everything below is what was observed, with the method, so it can be re-run.

## Method

Account: Teradata Labs, SSO role `Bedrock-Access`. Regions probed: us-west-2 (primary), us-east-1, us-east-2, eu-west-1. Tools: curl 8.7.1, jq 1.8.1, a 60-line Go program that mints a short-term Bedrock API key with the aws-sdk-go-v2 v4 presigner, and two shell scripts (one for the feature probes, one for the route sweep). The AWS SDK dependency is already in Loom's go.mod.

Short-term key scheme, as implemented by the `aws-bedrock-token-generator` libraries and reproduced here:

```
presigned = SigV4-presign(POST https://bedrock.amazonaws.com/?Action=CallWithBearerToken&X-Amz-Expires=43200, service="bedrock", region=<region>)
token     = "bedrock-api-key-" + base64(presigned + "&Version=1")
```

Every probe sent a small prompt with `max_tokens` between 1 and 300. Total spend was negligible.

## Authentication

| Credential | Mantle result | Notes |
|---|---|---|
| Long-term key (`ABSK…`, IAM service-specific credential) | 401 `invalid_api_key` "Invalid bearer token" | Same key returned 200 on bedrock control plane and on bedrock-runtime Converse. Tested both regions and both header names. |
| Short-term key minted from the SSO role | 200 | Works with `Authorization: Bearer` and with `x-api-key`. |
| SigV4 (`curl --aws-sigv4 aws:amz:<region>:bedrock-mantle`) | 200 | GET `/v1/models` and POST `/anthropic/v1/messages`. Service name `bedrock` was also accepted on GET. |
| Short-term key from us-west-2 on the us-east-1 endpoint | 401 "Credential should be scoped to a valid region." | Keys are region-scoped. |
| No credential | 401 "Missing 'authorization' or 'x-api-key' header" | Confirms both header names are recognized at the gateway. |

The long-term key failure is most likely an IAM gap. The AWS IAM reference defines `bedrock-mantle:CallWithBearerToken` with a `bedrock-mantle:bearerTokenType` condition (`SHORT_TERM` or `LONG_TERM`), separate from the classic `bedrock:CallWithBearerToken`. The IAM user behind this key predates Mantle. This was not confirmed by reading the IAM user, because the role has no IAM read access.

Required and accepted headers on the Messages path:

- `anthropic-version: 2023-06-01` is required. Without it: 400 "anthropic_version: Field required".
- `anthropic-workspace-id: default` is accepted (200).
- `anthropic-beta` with any value is rejected. See next section.

## The anthropic-beta header

| Request | Result |
|---|---|
| `anthropic-beta: prompt-caching-2024-07-31` | 400 "Unexpected value(s) `prompt-caching-2024-07-31` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header." |
| `anthropic-beta: extended-cache-ttl-2025-04-11` | 400, same message shape |
| No `anthropic-beta`, system block with `cache_control: {type: ephemeral}`, first call | 200, `cache_creation_input_tokens: 7981`, `cache_read_input_tokens: 0` |
| Same request, second call two seconds later | 200, `cache_creation_input_tokens: 0`, `cache_read_input_tokens: 7981` |

Loom's anthropic client sets `anthropic-beta: prompt-caching-2024-07-31` unconditionally in both the blocking and streaming request builders (`pkg/llm/anthropic/client.go`, lines 582 and 826 at the time of writing). Against Mantle that is a 100% failure rate. Caching itself is GA on the Messages API and does not need the header, so the fix is to make the header configurable and send none for Mantle.

The usage object on Mantle carries the newer nested fields: `cache_creation.ephemeral_5m_input_tokens`, `cache_creation.ephemeral_1h_input_tokens`, and `service_tier`.

## Messages API feature parity

All on `anthropic.claude-haiku-4-5` in us-west-2.

| Feature | Result |
|---|---|
| Tool use | `tool_use` block returned with `{"city":"Paris"}`; `tool_result` round trip returned final text. |
| Streaming | Standard SSE: `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`. |
| Extended thinking (`budget_tokens: 1024`) | `thinking` and `text` blocks; `usage.output_tokens_details.thinking_tokens: 66`. |
| Response `model` field | Anthropic's native id (`claude-haiku-4-5-20251001`, `claude-opus-4-7`), not the Mantle id. |

The native id in the response is useful: pricing and context-window lookups can hit the existing `anthropic` catalog namespace without a Mantle-specific table for Claude.

## Model IDs, regions, and entitlement

IDs are plain `anthropic.claude-<name>`. A `us.` prefix returns 404 "The model 'us.anthropic.claude-haiku-4-5' does not exist". Cross-region inference profiles do not exist on Mantle, matching the AWS endpoints doc.

Claude models visible in `GET /v1/models` for this account:

| Region | Total models | Claude ids |
|---|---|---|
| us-west-2 | 49 | haiku-4-5 |
| us-east-2 | 50 | haiku-4-5 |
| us-east-1 | 61 | haiku-4-5, sonnet-5, opus-5, opus-5-5, opus-4-7, opus-4-8, fable-5 |
| eu-west-1 | 35 | haiku-4-5, sonnet-5, opus-5, opus-4-7, opus-4-8 |

The listing is not an entitlement check. Observed in us-east-1:

- `anthropic.claude-opus-4-7`: 200.
- `anthropic.claude-sonnet-5`: 403 AccessDeniedException, "not authorized to perform: aws-marketplace:Subscribe on resource: *". The role cannot auto-subscribe.
- Every `openai.gpt-5.*` and `openai.gpt-6*` id, in both regions: 401 "Your subscription to the model could not be established."

The listing object has no entitlement or route field. Its keys are `created`, `data_retention`, `id`, `object`, `owned_by`, `status`. Every model lists `data_retention` with `mode: default` and `allowed_modes: [default, aws_review, provider_data_share, none]`. That per-model retention knob was not explored.

Claude is not served on Chat Completions: `/v1/chat/completions` with a Claude id returns 400 "does not support the '/v1/chat/completions' API".

## Route prefix sweep

All 48 non-Claude ids in us-west-2, plus the 7 extra ids in us-east-1, were sent a one-token request on `/v1/chat/completions` and, on failure, on `/openai/v1/chat/completions`.

| Prefix | Models |
|---|---|
| `/openai/v1` | `google.gemma-4-26b-a4b`, `google.gemma-4-31b`, `google.gemma-4-e2b`, `xai.grok-4.3`, `xai.grok-4.6`, GPT-6 family (`openai.gpt-6-astra`, `openai.gpt-6.1-sol`) |
| `/v1` | deepseek (2), gemma-3 (3), minimax (3), mistral (8), moonshotai (2), nvidia (4), gpt-oss (4), qwen (7), writer (1), zai (4) |
| Unresolved (subscription) | `openai.gpt-5.4*`, `openai.gpt-5.5*`, `openai.gpt-5.6-*`, `openai.gpt-6-luna`, `openai.gpt-6-sol`, `openai.gpt-6.1-terra` returned 401 on both prefixes |

The wrong prefix returns a stable 400: "model `<id>` isn't supported on this route". The GPT-6 family additionally rejected `max_tokens` on `/openai/v1` ("Unsupported parameter: 'max_tokens' is not supported with this model") and wants `max_completion_tokens`.

A vendor-prefix table (`google.gemma-4*`, `xai.*`, `openai.gpt-5*`, `openai.gpt-6*` on `/openai/v1`) with the route error as a one-shot fallback trigger covers what was observed.

## OpenAI-compatible path behavior

Observed on `openai.gpt-oss-120b` and `mistral.devstral-2-123b`.

- **No `data: [DONE]` sentinel.** Both models' streams ended by closing the connection after the last chunk. Loom's openai `ChatStream` breaks on `[DONE]` and also ends on scanner EOF, so no change is needed.
- **`stream_options.include_usage` is honored.** The final chunk has `choices: []` and a `usage` object. Loom already reads `chunk.Usage` on chunks with no choices.
- **Chunks carry an `obfuscation` field.** Ignored by Loom's decoder.
- **gpt-oss is a reasoning model.** With `max_tokens: 8` the response had `finish_reason: length`, `content: null`, and the trace in a field named `reasoning`. With `max_tokens: 300` content arrived (`"OK"`, 29 completion tokens). Loom maps `reasoning_content`, not `reasoning`, so the trace is dropped silently. Harmless, but an alias would preserve it.
- **Tool calls work** on gpt-oss: `tool_calls[0].function.name = get_weather`, arguments as a JSON string.

## Concurrency and rate limiting

60 concurrent `anthropic.claude-haiku-4-5` requests with `max_tokens: 5` from one client in us-west-2:

| Metric | Value |
|---|---|
| Status codes | 60 × 200 |
| Latency min | 0.57 s |
| Latency p50 | 0.67 s |
| Latency p90 | 0.79 s |
| Latency max | 1.62 s |
| `retry-after` or rate-limit headers | none |

Successful responses carried only `x-amzn-requestid` and `x-request-id`. The gateway's `access-control-expose-headers` list names `anthropic-ratelimit-unified-*`, `x-amzn-gateway-limit-*`, `retry-after`, and `x-amzn-usage-reservation-*`, so those headers may appear under real pressure or with usage reservations. At this load there was nothing for Loom's rate limiter to read, which is consistent with AWS's description of Mantle as queue-based rather than fixed RPM/TPM.

## What this changes in the implementation plan

Relative to the plan in TER-1402:

1. **New Phase 1 blocker.** Make the `anthropic-beta` header configurable in `pkg/llm/anthropic` and send none for Mantle.
2. **Auth default changes to SigV4.** Long-term keys are unusable in this organization, and short-term keys expire in 12 hours and are bound to one region, so a static API-key string is not enough for `looms serve`. Sign each request with the aws-sdk-go-v2 `v4.Signer`, service `bedrock-mantle`, using the credential chain Loom already loads in `pkg/llm/bedrock/client_sdk.go`. Keep a static key as an override. This moves the former optional SigV4 phase into Phase 1, about one day.
3. **No auth-header change** in the anthropic client. `x-api-key` works.
4. **openai client.** Add `openai.gpt-5*` and `openai.gpt-6*` to the models that use `max_completion_tokens`. Optionally accept `reasoning` as an alias for `reasoning_content`.
5. **Routing.** Claude on `/anthropic/v1/messages`; `google.gemma-4*`, `xai.*`, `openai.gpt-5*`, `openai.gpt-6*` on `/openai/v1`; everything else on `/v1`. Treat the "isn't supported on this route" 400 as a one-shot fallback trigger.
6. **Catalog.** Claude ids resolve to the `anthropic` namespace through the response `model` field. A `bedrock-mantle` namespace is still needed for non-Claude models and to keep `ShowInDropdown=false`.
7. **Entitlement errors** (401 and 403 with "subscription could not be established" or `aws-marketplace:Subscribe`) must surface as configuration errors. `IsTransientStatus` already excludes 401 and 403, so they will not be retried.
8. **Config validation.** The endpoint region must equal the credential's region.

Revised estimate: 5 to 6 engineering days, up from 4 to 5, because SigV4 is now in scope.

## What was not tested

- Sonnet 5, Opus 5.x, Fable 5, and any GPT-5.x or GPT-6 model, because the account is not subscribed to them. Testing them needs someone with `aws-marketplace:Subscribe` on the account.
- The OpenAI Responses API. Loom's openai client speaks Chat Completions only.
- Server-side tools, background inference, and non-default Projects or Workspaces.
- Behavior under real rate pressure (hundreds of concurrent long requests) and whether the reservation or rate-limit headers appear.
- PrivateLink.
- The `data_retention` modes.

## Sources

- AWS, Responses API on Bedrock: https://docs.aws.amazon.com/bedrock/latest/userguide/inference-responses-api.html
- AWS, Bedrock endpoints: https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html
- AWS, model availability by endpoint: https://docs.aws.amazon.com/bedrock/latest/userguide/models-endpoint-availability.html
- AWS, Messages API on Bedrock: https://docs.aws.amazon.com/bedrock/latest/userguide/inference-messages-api.html
- AWS, API keys: https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html
- AWS, Projects: https://docs.aws.amazon.com/bedrock/latest/userguide/projects.html
- AWS blog, zero operator access design of Mantle: https://aws.amazon.com/blogs/machine-learning/exploring-the-zero-operator-access-design-of-mantle/
- LiteLLM provider page (third party): https://docs.litellm.ai/docs/providers/bedrock_mantle
