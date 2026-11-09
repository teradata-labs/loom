# AWS Bedrock Mantle Integration

Technical reference for connecting Loom to the Amazon Bedrock `bedrock-mantle` endpoint.

**Version**: v1.4.0
**Status**: ⚠️ Partial. Implemented: provider (`pkg/llm/bedrockmantle`), provider factory, agent registry, `looms` server config (`llm.bedrock_mantle_*`), `looms` CLI, `loom-standalone`, `quickstart.sh` and `pkg/builder`. The catalog prices every model AWS documents on Mantle except GPT-5.x/6.x (53 models). Not done: rates for GPT-5.x/6.x and the OpenAI Responses API (📋). See [Cost accounting](#cost-accounting) and [What is not done yet](#what-is-not-done-yet).

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

### Through `looms.yaml` (server and CLI)

```yaml
llm:
  provider: bedrock-mantle
  bedrock_mantle_model: anthropic.claude-haiku-4-5
  # bedrock_mantle_region: us-west-2   # defaults to bedrock_region (which defaults to us-west-2)
  # bedrock_mantle_endpoint: https://bedrock-mantle.us-west-2.api.aws  # PrivateLink override only
  # bedrock_profile: bedrock            # SigV4 via a named AWS profile; otherwise keys or role from the AWS credential chain
```

| Key | Environment | Keyring | Notes |
|---|---|---|---|
| `llm.bedrock_mantle_region` | `LOOM_LLM_BEDROCK_MANTLE_REGION` | | Falls back to `llm.bedrock_region` |
| `llm.bedrock_mantle_endpoint` | `LOOM_LLM_BEDROCK_MANTLE_ENDPOINT` | | Base URL override. A trailing `/` or `/v1` is stripped |
| `llm.bedrock_mantle_api_key` | `BEDROCK_MANTLE_API_KEY` | `looms config set-key bedrock_mantle_api_key` | Bedrock API key. Empty selects SigV4 |
| `llm.bedrock_mantle_model` | `LOOM_LLM_BEDROCK_MANTLE_MODEL` | | Default `anthropic.claude-haiku-4-5` |

SigV4 credentials are the Bedrock ones: `llm.bedrock_profile`, or `bedrock_access_key_id` / `bedrock_secret_access_key` / `bedrock_session_token` from the keyring, or the AWS default chain (env keys, SSO, IAM role). When a profile is set, the main server LLM, per-agent `llm:` blocks, top-level `providers:` pool entries and `looms workflow` ignore explicit keys. A mid-session model switch builds the client through the provider factory, which uses explicit keys when both keys and a profile are set; the `bedrock` provider does the same.

The provider is selectable everywhere `looms` builds an LLM: the main server LLM, top-level `providers:` pool entries (they inherit the Mantle region, endpoint and API key from the main LLM when omitted; a region set on the pool entry wins over an inherited one: an entry that sets `bedrock_mantle_region` uses it, an entry that sets only `bedrock_region` uses that region and does not inherit the main LLM's `bedrock_mantle_region`, and an entry that sets neither inherits the main LLM's `bedrock_mantle_region`, else its `bedrock_region`), per-agent `llm:` blocks in agent YAML (model from the agent, credentials from the server), `looms workflow`, and `looms config init` (option 4). `loom-standalone --provider bedrock-mantle` reads `BEDROCK_MANTLE_REGION` (then `AWS_REGION`, then `us-west-2`) and `BEDROCK_MANTLE_API_KEY` from the environment.

`quickstart.sh` offers Mantle as option 4 of its provider menu: it asks for the region, for an AWS profile (SigV4, with an optional `aws sso login`) or a Bedrock API key (keyring), and for the model, then writes `llm.provider`, `llm.bedrock_mantle_region`, `llm.bedrock_mantle_model` and either `llm.bedrock_profile` or the `bedrock_mantle_api_key` secret.

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

Under `looms serve`, the agent block selects the provider and model; region, endpoint and credentials come from the server's `llm.bedrock_mantle_*` and `llm.bedrock_*` settings above. When the agent registry is used directly from Go (without `looms`), it reads only environment variables: `BEDROCK_MANTLE_REGION` (then `AWS_REGION`), `BEDROCK_MANTLE_ENDPOINT`, `BEDROCK_MANTLE_API_KEY`, and the default AWS credential chain (`AWS_PROFILE`, env keys, SSO, IAM role) for SigV4.

The same applies to agent YAML that `looms serve` loads from `$LOOM_DATA_DIR/agents/` at startup, for `llm`, `compressor_llm` and `classifier_llm`: the agent block selects the provider and model, and region, endpoint and credentials come from the server settings above. The region is `llm.bedrock_mantle_region` when it is set, else `BEDROCK_MANTLE_REGION`, else `AWS_REGION`, else `llm.bedrock_region` (default `us-west-2`). An endpoint or API key that the server settings leave empty is read from `BEDROCK_MANTLE_ENDPOINT` and `BEDROCK_MANTLE_API_KEY`. If the client cannot be built (for example, no region and no endpoint is set), `looms serve` logs an error and does not start that agent. It does not run the agent on the server's default LLM or on the primary LLM, as it does for other providers, because that would send the traffic outside the AWS account.

### Through `pkg/builder` (Go)

```go
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithBedrockMantleLLM("us-west-2"). // "" falls back to BEDROCK_MANTLE_REGION, then AWS_REGION
    Build()
```

`WithBedrockMantleLLM` signs with SigV4 from the AWS credential chain unless `BEDROCK_MANTLE_API_KEY` is set, and takes the model from `LOOM_LLM_BEDROCK_MANTLE_MODEL` (default `anthropic.claude-haiku-4-5`). A construction error (for example no region anywhere) is returned by `Build`, not swallowed; `WithBedrockLLM` now reports its errors the same way.

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

AWS documents these Claude models on Mantle (bedrock-runtime serves the rest of the family through inference profiles): Fable 5.1, Fable 5, Mythos 5 (Mantle-only, verification required), Opus 5.5, Opus 5, Opus 4.8, Opus 4.7, Sonnet 5.5, Sonnet 5, Haiku 5.5 and Haiku 4.5. Opus 4.1/4.5/4.6, Sonnet 4/4.5/4.6 and Claude 3 Haiku are not on Mantle. Anthropic's [Claude in Amazon Bedrock](https://platform.claude.com/docs/en/build-with-claude/claude-in-amazon-bedrock) page also lists `anthropic.claude-mythos-5-1`; AWS's endpoint-availability page does not, so the catalog leaves it out.

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

The catalog's `bedrock-mantle` namespace (`pkg/llm/catalog/catalog.go`) prices every model AWS documents on the endpoint except GPT-5.x/6.x, so the inner clients attach the Mantle rate to each response instead of a fallback. The rates were verified on 2026-11-05 against two sources:

- **Claude**: Anthropic's [first-party card](https://platform.claude.com/docs/en/about-claude/pricing) × 1.10. AWS bills the same per-token rate for a model on both endpoints ([endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html), "Pricing"), and a bare Mantle id is a regional, single-Region endpoint, which for Claude 4.5 and later carries a 10 percent premium over the `global.*` profiles that `bedrock-runtime` offers ([Claude in Amazon Bedrock](https://platform.claude.com/docs/en/build-with-claude/claude-in-amazon-bedrock), "Regions"). On the AWS bill, the same model costs 10% more through this provider than through `bedrock` with a `global.` profile, and the same as through `bedrock` with a `us.`/`eu.` profile. Loom's cost estimates match that. The `bedrock` client multiplies the catalog rate by `catalog.BedrockEndpointMultiplier` on every token bucket: 1.10 for Claude 4.5 and later on a geographic profile (`us.`, `eu.`, `apac.`, …) or an in-region `anthropic.` id, and 1.00 on a `global.` profile, on Claude models before 4.5 and on non-Claude ids. The Mantle rows already include the premium, and the anthropic client that this provider prices through applies no multiplier, so the premium is counted once. For Claude Opus 4.7, Loom reports $5.50/$27.50 per 1M input/output tokens on Mantle (`anthropic.claude-opus-4-7`) and through `bedrock` with `us.anthropic.claude-opus-4-7-v1:0` or `eu.anthropic.claude-opus-4-7-v1:0`, and $5.00/$25.00 through `bedrock` with `global.anthropic.claude-opus-4-7-v1:0`. Haiku 5.5 is priced at $0.11/$0.55 for prompts up to 100K tokens. Above 100K, `catalog.RequestPricing` replaces the row with Anthropic's long-context card ($0.50/$2.50) scaled by the row's ratio to the first-party card, so the premium carries over: $0.55/$2.75 on this provider, the same as `bedrock` with a `us.`/`eu.` profile, and $0.50/$2.50 through `anthropic` or `bedrock` with a `global.` profile.
- **Everything else**: the [AWS Price List API](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrock/current/us-west-2/index.json) for AmazonBedrock, offer version 20261102165015, `<id>-mantle-…-tokens-standard` SKUs (us-west-2 and us-east-1 are identical). Only the standard tier is modelled; Mantle also sells flex (−50%), priority (+75%) and batch tiers, and Loom does not send `service_tier`. Grok 4.6 also has a cheaper `-global` SKU ($2.00/$6.00) that a bare Mantle id does not reach.

Context window and max output are the AWS model cards (`K` written as K×1000). Where a card states no output cap (Gemma 4, Voxtral, Grok) the row carries the cap AWS states for a sibling model, or Grok 4.3's documented `max_completion_tokens` default, and says so in a comment. Capabilities and `IsReasoning` follow the card's modalities and Reasoning line; "tool-use" is client-side tool calling.

### Claude

| Model id | Model | Input $/1M | Output $/1M | Context | Max output | Capabilities |
|---|---|---|---|---|---|---|
| `anthropic.claude-fable-5-1` | Claude Fable 5.1 | 11.00 | 55.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-fable-5` | Claude Fable 5 | 11.00 | 55.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-mythos-5` | Claude Mythos 5 | 11.00 | 55.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-opus-5-5` | Claude Opus 5.5 | 4.40 | 22.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-opus-5` | Claude Opus 5 | 5.50 | 27.50 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-opus-4-8` | Claude Opus 4.8 | 5.50 | 27.50 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-opus-4-7` | Claude Opus 4.7 | 5.50 | 27.50 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-sonnet-5-5` | Claude Sonnet 5.5 | 2.20 | 11.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-sonnet-5` | Claude Sonnet 5 | 2.20 | 11.00 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-haiku-5-5` | Claude Haiku 5.5 | 0.11 | 0.55 | 1M | 128k | text, vision, tool-use, thinking |
| `anthropic.claude-haiku-4-5` | Claude Haiku 4.5 | 1.10 | 5.50 | 200k | 64k | text, vision, tool-use, thinking |

### Open-weight and third-party models

| Model id | Model | Input $/1M | Output $/1M | Context | Max output | Capabilities |
|---|---|---|---|---|---|---|
| `deepseek.v3.1` | DeepSeek-V3.1 | 0.58 | 1.68 | 128k | 8k | text, tool-use |
| `deepseek.v3.2` | DeepSeek V3.2 | 0.62 | 1.85 | 164k | 8k | text, tool-use |
| `google.gemma-3-4b-it` | Gemma 3 4B IT | 0.04 | 0.08 | 128k | 8k | text, vision, tool-use |
| `google.gemma-3-12b-it` | Gemma 3 12B IT | 0.09 | 0.29 | 128k | 8k | text, vision, tool-use |
| `google.gemma-3-27b-it` | Gemma 3 27B | 0.23 | 0.38 | 128k | 8k | text, vision, tool-use |
| `google.gemma-4-e2b` | Gemma 4 E2B | 0.04 | 0.08 | 128k | 8k | text, vision, tool-use, thinking |
| `google.gemma-4-26b-a4b` | Gemma 4 26B-A4B | 0.13 | 0.40 | 256k | 8k | text, vision, tool-use, thinking |
| `google.gemma-4-31b` | Gemma 4 31B | 0.14 | 0.40 | 256k | 8k | text, vision, tool-use, thinking |
| `minimax.minimax-m2` | MiniMax M2 | 0.30 | 1.20 | 1M | 8k | text, tool-use |
| `minimax.minimax-m2.1` | MiniMax M2.1 | 0.30 | 1.20 | 196k | 8k | text, tool-use |
| `minimax.minimax-m2.5` | MiniMax M2.5 | 0.30 | 1.20 | 196k | 8k | text, tool-use |
| `mistral.devstral-2-123b` | Devstral 2 123B | 0.40 | 2.00 | 256k | 32k | text, tool-use |
| `mistral.magistral-small-2509` | Magistral Small 2509 | 0.50 | 1.50 | 128k | 40k | text, vision, tool-use, thinking |
| `mistral.ministral-3-14b-instruct` | Ministral 3 14B | 0.20 | 0.20 | 128k | 8k | text, vision, tool-use |
| `mistral.ministral-3-8b-instruct` | Ministral 3 8B | 0.15 | 0.15 | 128k | 8k | text, vision, tool-use |
| `mistral.ministral-3-3b-instruct` | Ministral 3 3B | 0.10 | 0.10 | 128k | 8k | text, vision, tool-use |
| `mistral.mistral-large-3-675b-instruct` | Mistral Large 3 | 0.50 | 1.50 | 256k | 32k | text, vision, tool-use |
| `mistral.voxtral-mini-3b-2507` | Voxtral Mini 3B 2507 | 0.04 | 0.04 | 32k | 8k | text, tool-use |
| `mistral.voxtral-small-24b-2507` | Voxtral Small 24B 2507 | 0.10 | 0.30 | 32k | 8k | text, tool-use |
| `moonshotai.kimi-k2-thinking` | Kimi K2 Thinking | 0.60 | 2.50 | 256k | 16k | text, tool-use, thinking |
| `moonshotai.kimi-k2.5` | Kimi K2.5 | 0.60 | 3.00 | 256k | 16k | text, vision, tool-use |
| `nvidia.nemotron-nano-9b-v2` | Nemotron Nano 9B v2 | 0.06 | 0.23 | 128k | 8k | text, tool-use |
| `nvidia.nemotron-nano-12b-v2` | Nemotron Nano 12B v2 VL | 0.20 | 0.60 | 128k | 8k | text, vision, tool-use |
| `nvidia.nemotron-nano-3-30b` | Nemotron Nano 3 30B | 0.06 | 0.24 | 256k | 8k | text, tool-use, thinking |
| `nvidia.nemotron-super-3-120b` | Nemotron 3 Super 120B | 0.15 | 0.65 | 256k | 32k | text, tool-use, thinking |
| `openai.gpt-oss-20b` | gpt-oss-20b | 0.07 | 0.30 | 128k | 16k | text, tool-use, thinking |
| `openai.gpt-oss-120b` | gpt-oss-120b | 0.15 | 0.60 | 128k | 16k | text, tool-use, thinking |
| `openai.gpt-oss-safeguard-20b` | gpt-oss-safeguard-20b | 0.07 | 0.20 | 128k | 16k | text, tool-use, thinking |
| `openai.gpt-oss-safeguard-120b` | gpt-oss-safeguard-120b | 0.15 | 0.60 | 128k | 16k | text, tool-use, thinking |
| `qwen.qwen3-32b` | Qwen3 32B | 0.15 | 0.60 | 32k | 8k | text, tool-use, thinking |
| `qwen.qwen3-235b-a22b-2507` | Qwen3 235B A22B 2507 | 0.22 | 0.88 | 256k | 8k | text, tool-use, thinking |
| `qwen.qwen3-coder-30b-a3b-instruct` | Qwen3 Coder 30B A3B Instruct | 0.15 | 0.60 | 256k | 16k | text, tool-use |
| `qwen.qwen3-coder-480b-a35b-instruct` | Qwen3 Coder 480B A35B Instruct | 0.45 | 1.80 | 256k | 16k | text, tool-use |
| `qwen.qwen3-coder-next` | Qwen3 Coder Next | 0.50 | 1.20 | 256k | 16k | text, tool-use |
| `qwen.qwen3-next-80b-a3b-instruct` | Qwen3 Next 80B A3B | 0.14 | 1.20 | 256k | 8k | text, tool-use, thinking |
| `qwen.qwen3-vl-235b-a22b-instruct` | Qwen3 VL 235B A22B | 0.53 | 2.66 | 256k | 8k | text, vision, tool-use |
| `writer.palmyra-vision-7b` | Palmyra Vision 7B | 0.15 | 0.60 | 4k | 4k | text, vision, tool-use |
| `xai.grok-4.3` | Grok 4.3 | 1.25 | 2.50 | 1M | 131,072 | text, vision, tool-use, thinking |
| `xai.grok-4.6` | Grok 4.6 | 2.20 | 6.60 | 500k | 131,072 | text, vision, tool-use, thinking |
| `zai.glm-4.7` | GLM 4.7 | 0.60 | 2.20 | 203k | 128k | text, tool-use |
| `zai.glm-4.7-flash` | GLM 4.7 Flash | 0.07 | 0.40 | 203k | 128k | text, tool-use |
| `zai.glm-5` | GLM 5 | 1.00 | 3.20 | 200k | 128k | text, tool-use |

Not priced: `openai.gpt-5.*` and `openai.gpt-6*` (served on Mantle, no price-list SKU yet; the openai client's fallback rate applies), `zai.glm-4.6` (listed live, not on AWS's Mantle availability page or price list), and the cache-read/cache-write tiers of the open models (Grok and Kimi K3 publish them; Loom's openai client does not price cache hits).

## Testing

Unit tests run with the rest of the repository:

```bash
go test -tags fts5 -race ./pkg/llm/bedrockmantle/ ./pkg/llm/factory/ ./pkg/llm/anthropic/ ./pkg/llm/openai/ ./cmd/looms/
```

The live suite needs a region and a credential that is allowed to call `bedrock-mantle:CreateInference`:

```bash
BEDROCK_MANTLE_LIVE_REGION=us-west-2 AWS_PROFILE=bedrock \
  go test -tags "fts5 integration" -race -run TestLive ./pkg/llm/bedrockmantle/ -v
```

It covers the health check, Claude chat, streaming, tool calls and prompt caching, gpt-oss chat, streaming and tool calls, the Gemma 4 `/openai/v1` route, and the route fallback (forced by starting Grok on the wrong prefix). Override the models with `BEDROCK_MANTLE_LIVE_CLAUDE` and `BEDROCK_MANTLE_LIVE_OPENAI` if your account subscribes to different ones.

## What is not done yet

- 📋 Rates for `openai.gpt-5.*` / `openai.gpt-6*` on Mantle (no AWS price-list SKU as of offer 20261102165015), and for `zai.glm-4.6`; service tiers other than standard.
- 📋 `anthropic.claude-mythos-5-1`: Anthropic lists it for the Mantle endpoint, AWS does not. Add it once the two agree.
- 📋 OpenAI Responses API, server-side tools, background inference, Projects and Workspaces. Loom's openai client speaks Chat Completions, which every non-Claude Mantle model serves, so these add capabilities rather than reach.

## See Also

- [AWS Bedrock Integration](llm-bedrock.md) for the `bedrock-runtime` provider
- [LLM Provider Support](llm-providers.md)
- [Bedrock Mantle spike notes](../research/bedrock-mantle-spike.md)
- AWS: [Bedrock endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html), [Messages API on Bedrock](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-messages-api.html), [API keys](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html)
