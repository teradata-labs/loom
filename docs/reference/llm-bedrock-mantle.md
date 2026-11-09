# AWS Bedrock Mantle Integration

Technical reference for connecting Loom to the Amazon Bedrock `bedrock-mantle` endpoint.

**Version**: v1.4.0
**Status**: ⚠️ Partial. The provider is implemented and tested in the Go library (`pkg/llm/bedrockmantle`), the provider factory, and the agent registry. The `looms` server YAML (`llm.bedrock_mantle_*`) and CLI wiring are not done yet; see [What is not done yet](#what-is-not-done-yet).

## Table of Contents

1. [Overview](#overview)
2. [When to use Mantle instead of Bedrock](#when-to-use-mantle-instead-of-bedrock)
3. [Configuration](#configuration)
4. [Authentication](#authentication)
5. [Models, regions and entitlement](#models-regions-and-entitlement)
6. [How requests are routed](#how-requests-are-routed)
7. [Feature support](#feature-support)
8. [Cost accounting](#cost-accounting)
9. [Testing](#testing)
10. [What is not done yet](#what-is-not-done-yet)
11. [See Also](#see-also)

## Overview

Amazon Bedrock serves models on two endpoint surfaces. `bedrock-runtime` is the classic one that Loom's `bedrock` provider uses (Converse, InvokeModel). `bedrock-mantle` (`https://bedrock-mantle.{region}.api.aws`) speaks the Anthropic Messages API, the OpenAI Chat Completions API, and the OpenAI Responses API instead. It does not serve Converse or InvokeModel, so the `bedrock` provider cannot reach it.

The `bedrock-mantle` provider does not add a third wire protocol. It builds Loom's existing anthropic client for Claude models and the existing openai client for every other vendor, pointed at the right Mantle path, and adds what Mantle needs on top: bearer or SigV4 authentication, no `anthropic-beta` header, the per-vendor path prefix, and a health check.

Everything this document states about the endpoint's behavior was observed live on 2026-11-04 against us-west-2, us-east-1, us-east-2 and eu-west-1. The spike notes are in [docs/research/bedrock-mantle-spike.md](../research/bedrock-mantle-spike.md).

## When to use Mantle instead of Bedrock

AWS's own guidance is to use `bedrock-runtime` for new applications and Mantle only when a model or capability you need is missing there. Reasons to pick `bedrock-mantle`:

- A model that is only served on Mantle (Gemma 4, GPT-5.x and GPT-6.x in the probed account; AWS also lists Claude Mythos 5 as Mantle-only).
- An existing OpenAI- or Anthropic-SDK-shaped integration you want to keep.
- Mantle-only platform features such as Projects and Workspaces (not used by Loom yet).

What you give up on Mantle: Guardrails, cross-region inference profiles (`us.` and `global.` model ids), intelligent prompt routing, and the Converse API.

## Configuration

### Through the provider factory (Go)

```go
f := factory.NewProviderFactory(factory.FactoryConfig{
    BedrockMantleRegion: "us-west-2",
    BedrockMantleAPIKey: "",            // empty = SigV4 from the AWS credential chain
    BedrockMantleModel:  "anthropic.claude-haiku-4-5",
    // SigV4 credential sources are shared with the bedrock provider:
    // BedrockProfile, BedrockAccessKeyID, BedrockSecretAccessKey, BedrockSessionToken
})
p, err := f.CreateProvider("bedrock-mantle", "anthropic.claude-haiku-4-5")
```

| FactoryConfig field | Environment fallback | Notes |
|---|---|---|
| `BedrockMantleRegion` | `BEDROCK_MANTLE_REGION`, then `AWS_REGION` | Required unless an endpoint is set |
| `BedrockMantleEndpoint` | `BEDROCK_MANTLE_ENDPOINT` | Base URL override (PrivateLink DNS). A trailing `/` or `/v1` is stripped |
| `BedrockMantleAPIKey` | `BEDROCK_MANTLE_API_KEY` | Bedrock API key. Empty selects SigV4 |
| `BedrockMantleModel` | none | Default `anthropic.claude-haiku-4-5` |

All four fields accept `${VAR}` placeholders, like the other factory fields.

### Through agent YAML (agent registry)

```yaml
llm:
  provider: bedrock-mantle
  model: anthropic.claude-haiku-4-5
  max_tokens: 4096
```

The registry path reads only environment variables: `BEDROCK_MANTLE_REGION` (then `AWS_REGION`), `BEDROCK_MANTLE_ENDPOINT`, `BEDROCK_MANTLE_API_KEY`, and the default AWS credential chain (`AWS_PROFILE`, env keys, SSO, IAM role) for SigV4.

The same applies to agent YAML that `looms serve` loads from `$LOOM_DATA_DIR/agents/` at startup, for `llm`, `compressor_llm` and `classifier_llm`. The server reads the same environment variables. If the client cannot be built (for example, no region and no endpoint is set), `looms serve` logs an error and does not start that agent. It does not run the agent on the server's default LLM or on the primary LLM, as it does for other providers, because that would send the traffic outside the AWS account.

### Directly (Go)

```go
c, err := bedrockmantle.NewClientForModel(bedrockmantle.Config{
    Region: "us-west-2",
    Model:  "openai.gpt-oss-120b",
})
```

## Authentication

Mantle accepts two credentials. Loom picks one at construction:

| Credential | Loom setting | Observed behavior |
|---|---|---|
| **SigV4** (default) | API key empty | Every request is signed for service `bedrock-mantle` with credentials from static keys, a named profile, or the default chain. Works for IAM roles, SSO sessions and access keys. No expiry handling needed. |
| **Bedrock API key** | `BedrockMantleAPIKey` / `BEDROCK_MANTLE_API_KEY` | Sent as `x-api-key` on the Messages path and `Authorization: Bearer` on Chat Completions. Both header names are accepted. |

Two facts about API keys matter in practice:

- **Short-term keys** (`bedrock-api-key-…`, minted from AWS credentials, 12-hour lifetime) are bound to one region. Loom decodes the region from the key and refuses to construct a client whose region differs, instead of failing on the first request with "Credential should be scoped to a valid region".
- **Long-term keys** (`ABSK…`) were rejected by Mantle with 401 "Invalid bearer token" in the organization probed, while the same key worked on `bedrock-runtime`. AWS IAM has a separate `bedrock-mantle:CallWithBearerToken` action; a key's IAM user created before Mantle existed will not have it. For that reason Loom does **not** pick up an ambient `AWS_BEARER_TOKEN_BEDROCK` for Mantle. Bearer auth is opt-in through the Mantle API key setting; SigV4 is the default.

Mantle requires the `anthropic-version` header (Loom sends `2023-06-01`) and rejects any `anthropic-beta` value with 400. The provider disables the beta header on the anthropic client; prompt caching still works without it.

## Models, regions and entitlement

Model ids are Mantle's own, with no inference-profile prefix: `anthropic.claude-haiku-4-5`, `anthropic.claude-opus-4-7`, `openai.gpt-oss-120b`, `google.gemma-4-e2b`. A `us.` or `global.` prefix returns 404.

What `GET /v1/models` lists depends on the region, and the listing is **not** an entitlement check. Observed in the Teradata Labs account:

| Region | Claude ids listed |
|---|---|
| us-west-2, us-east-2 | `anthropic.claude-haiku-4-5` |
| us-east-1 | haiku-4-5, sonnet-5, opus-5, opus-5-5, opus-4-7, opus-4-8, fable-5 |
| eu-west-1 | haiku-4-5, sonnet-5, opus-5, opus-4-7, opus-4-8 |

A listed model the account is not subscribed to fails at inference time with 401 or 403 and a message containing "subscription" or `aws-marketplace:Subscribe`. Loom surfaces these as errors without retrying; fix them by subscribing in the AWS Marketplace for that account.

Mantle regions as documented by AWS: us-east-1, us-east-2, us-west-2, eu-central-1, eu-north-1, eu-south-1, eu-west-1, eu-west-2, ap-northeast-1, ap-south-1, ap-southeast-2, ap-southeast-3, sa-east-1, us-gov-east-1, us-gov-west-1.

## How requests are routed

| Model id | Path | Inner client |
|---|---|---|
| `anthropic.*` | `/anthropic/v1/messages` | anthropic |
| `google.gemma-4*`, `xai.*`, `openai.gpt-5*`, `openai.gpt-6*` | `/openai/v1/chat/completions` | openai |
| everything else (gpt-oss, Gemma 3, Mistral, Qwen, DeepSeek, MiniMax, Kimi, Nemotron, GLM, Palmyra) | `/v1/chat/completions` | openai |

Claude is not served on Chat Completions (400 "does not support the '/v1/chat/completions' API"), so Claude never falls over. For every other vendor the client starts on the table's prefix and, if Mantle answers 400 "model `<id>` isn't supported on this route", rebuilds once on the other prefix and stays there for the life of the client. A second route error is returned to the caller.

## Feature support

| Feature | Claude on Mantle | Other vendors on Mantle |
|---|---|---|
| Chat | ✅ | ✅ |
| Streaming | ✅ Messages SSE | ✅ Chat Completions SSE (Mantle sends no `data: [DONE]`; the client ends on EOF) |
| Tool calling | ✅ | ✅ (verified on gpt-oss-120b) |
| Prompt caching | ✅ `cache_control` honored; cache write then read verified | ⚠️ not verified |
| Extended thinking | ✅ `thinking` blocks returned | ⚠️ reasoning models return their trace in `reasoning`; Loom maps it to `Thinking` |
| Health check | ✅ `GET /v1/models` | ✅ |
| Guardrails, cross-region profiles, prompt routing | ❌ not offered on Mantle | ❌ |
| OpenAI Responses API | ❌ not implemented (Loom's openai client speaks Chat Completions) | ❌ |

Under load, Mantle queues rather than rejecting: 60 concurrent Haiku requests from one client all returned 200 with a p50 of 0.67 seconds and no `retry-after` or rate-limit headers. Loom's client-side rate limiter still applies when configured, but has nothing to read from Mantle's responses at that load. When a rate limiter is configured (agent YAML loaded by the agent registry or by `looms serve`), a Mantle 500, 502, 503, 504 or 529 that arrives before any content streams is retried under the limiter's budget and backoff, the same as a 429. The provider factory passes no limiter, so on that path the error surfaces on the first attempt, as it does for every other provider.

## Cost accounting

Mantle bills the same per-token rates as `bedrock-runtime`. The catalog's `bedrock-mantle` namespace lists the Mantle ids whose rates are already verified in this repository:

| Model id | Input $/1M | Output $/1M | Context | Max output |
|---|---|---|---|---|
| `anthropic.claude-haiku-4-5` | 1.00 | 5.00 | 200k | 64k |
| `anthropic.claude-opus-4-7` | 5.00 | 25.00 | 1M | 128k |

Other Mantle models are priced by the inner client's fallback table, which is **wrong** for them (non-Claude ids fall to the openai client's default rates). Add entries to the `bedrock-mantle` namespace in `pkg/llm/catalog/catalog.go` as rates are verified.

## Testing

Unit tests run with the rest of the repository:

```bash
go test -tags fts5 -race ./pkg/llm/bedrockmantle/ ./pkg/llm/factory/ ./pkg/llm/anthropic/ ./pkg/llm/openai/
```

The live suite needs a region and a credential that is allowed to call `bedrock-mantle:CreateInference`:

```bash
BEDROCK_MANTLE_LIVE_REGION=us-west-2 AWS_PROFILE=bedrock \
  go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/bedrockmantle/ -v
```

It covers the health check, Claude chat, streaming, tool calls and prompt caching, gpt-oss chat, streaming and tool calls, the Gemma 4 `/openai/v1` route, and the route fallback (forced by starting Grok on the wrong prefix). Override the models with `BEDROCK_MANTLE_LIVE_CLAUDE` and `BEDROCK_MANTLE_LIVE_OPENAI` if your account subscribes to different ones.

## What is not done yet

- 📋 `looms` server config: `llm.bedrock_mantle_region`, `llm.bedrock_mantle_api_key`, `llm.bedrock_mantle_endpoint`, `llm.bedrock_mantle_model` in `looms.yaml`, `looms config set-key`, and the server-level provider switch in `cmd/looms` (`createProviderWithRateLimit`). Until then the `looms serve` server-level LLM cannot be this provider. Agent YAML with `llm.provider: bedrock-mantle` works under `looms serve` (see [Through agent YAML](#through-agent-yaml-agent-registry)).
- 📋 Provider-name plumbing elsewhere in the tree (`internal/config` provider list, `pkg/agent/model_context_limits.go` defaults, metaagent validator, OTel `gen_ai.system` mapping, `cmd/loom-standalone`).
- 📋 Catalog entries for the other Claude models Mantle lists (Sonnet 5, Opus 5.x, Opus 4.8, Fable 5) and for non-Claude models, once their rates are verified.
- 📋 OpenAI Responses API, server-side tools, background inference, Projects and Workspaces.

## See Also

- [AWS Bedrock Integration](llm-bedrock.md) for the `bedrock-runtime` provider
- [LLM Provider Support](llm-providers.md)
- [Bedrock Mantle spike notes](../research/bedrock-mantle-spike.md)
- AWS: [Bedrock endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html), [Messages API on Bedrock](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-messages-api.html), [API keys](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html)
