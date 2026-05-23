
# HuggingFace Provider

Reference for connecting Loom to the HuggingFace Inference API.

**Version**: v1.2.0


## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Overview](#overview)
3. [Authentication](#authentication)
4. [Configuration](#configuration)
5. [Models](#models)
6. [Cost Estimates](#cost-estimates)
7. [Tool Calling](#tool-calling)
8. [Error Codes](#error-codes)
9. [Limitations](#limitations)
10. [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
llm:
  provider: huggingface
  huggingface_model: meta-llama/Meta-Llama-3.1-70B-Instruct
  # huggingface_token: set via keyring (looms config set-key huggingface_token)
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

### Environment Variables

```bash
# Direct environment variable (checked by server provider creation)
export HUGGINGFACE_API_KEY="hf_xxxxxxxxxxxxxxxxxxxxxxxxxx"

# Viper-mapped environment variable (checked by config validation)
export LOOM_LLM_HUGGINGFACE_TOKEN="hf_xxxxxxxxxxxxxxxxxxxxxxxxxx"
```

### Supported Models (with cost estimates)

| Model | Input $/1M tokens | Output $/1M tokens |
|-------|-------------------|--------------------|
| `meta-llama/Meta-Llama-3.1-70B-Instruct` (default) | $0.80 | $0.80 |
| `meta-llama/Meta-Llama-3.1-8B-Instruct` | $0.20 | $0.20 |
| `mistralai/Mixtral-8x7B-Instruct-v0.1` | $0.60 | $0.60 |
| `Qwen/Qwen2.5-72B-Instruct` | $0.80 | $0.80 |
| `google/gemma-2-9b-it` | $0.30 | $0.30 |
| Other / unknown | $1.00 | $1.00 |


## Overview

The HuggingFace provider connects Loom to the [HuggingFace Inference API](https://huggingface.co/inference-api). Internally it wraps the OpenAI client because HuggingFace exposes an OpenAI-compatible chat completions endpoint at `https://router.huggingface.co/v1/chat/completions`.

### Feature Status

| Feature | Status | Notes |
|---------|--------|-------|
| Chat completions | ✅ Implemented | Via OpenAI-compatible endpoint |
| Tool calling | ✅ Implemented | Same format as OpenAI function calling |
| Cost estimation | ✅ Implemented | Client-side estimates, not exact |
| Client-side rate limiting | ✅ Implemented | Configurable via `rate_limit` section |
| Keyring token storage | ✅ Implemented | Via `looms config set-key huggingface_token` |
| Streaming | ⚠️ Not implemented | `StreamingLLMProvider` interface not implemented |
| Vision/image input | ⚠️ Not supported | Text-based chat only |
| Custom endpoint | ⚠️ Not supported | Hardcoded to HuggingFace router URL |

This means:

- Any model hosted on HuggingFace Inference API that supports the OpenAI chat completions format works.
- Tool calling is supported (same format as OpenAI function calling).
- The provider name reported in responses is `huggingface`.

**Implementation**: `pkg/llm/huggingface/client.go` -- wraps `pkg/llm/openai` with the HuggingFace endpoint and HuggingFace-specific cost calculation.


## Authentication

A HuggingFace **token** (not "API key") is required. Obtain one from [huggingface.co/settings/tokens](https://huggingface.co/settings/tokens).

The token is resolved in the following order (first non-empty wins):

1. **Keyring** (recommended): `looms config set-key huggingface_token`
2. **Config field**: `llm.huggingface_token` in `looms.yaml` (not recommended for secrets)
3. **Viper-mapped environment variable**: `LOOM_LLM_HUGGINGFACE_TOKEN`
4. **Direct environment variable**: `HUGGINGFACE_API_KEY` (fallback checked at provider creation)

**Note**: Unlike some other providers (e.g., Anthropic with `--anthropic-key`), there is no dedicated CLI flag for the HuggingFace token. Use the keyring or environment variables instead.

If no token is found, the server fails to start with:

```
huggingface token is required (set via --huggingface-token, LOOM_LLM_HUGGINGFACE_TOKEN, or save to keyring with 'looms config set-key huggingface_token')
```

### Keyring Setup

```bash
# Store token securely in OS keyring
looms config set-key huggingface_token
# You will be prompted to enter the token
```


## Configuration

### Provider-Specific Fields

#### huggingface_token

**Type**: `string`
**Required**: Yes
**Environment variables**: `LOOM_LLM_HUGGINGFACE_TOKEN` (Viper-mapped), `HUGGINGFACE_API_KEY` (direct fallback)
**Keyring key**: `huggingface_token`
**CLI flag**: None (use keyring or environment variable)

HuggingFace access token. See [Authentication](#authentication) for setup methods.


#### huggingface_model

**Type**: `string`
**Default**: `meta-llama/Meta-Llama-3.1-70B-Instruct`
**Required**: No

The HuggingFace model identifier. Must be the full `org/model-name` format as shown on the HuggingFace model page (e.g., `meta-llama/Meta-Llama-3.1-8B-Instruct`).

Browse available models at [huggingface.co/models](https://huggingface.co/models). The model must support the chat completions API format.


### Common Fields (shared across all providers)

#### temperature

**Type**: `float64`
**Default**: `1.0`
**Range**: `0.0` - `1.0` (Loom agent config validation enforces this range)
**Required**: No

Sampling temperature for generation. Lower values produce more deterministic output. Note: while the OpenAI-compatible API accepts 0.0-2.0, Loom's agent config validator rejects values above 1.0.


#### max_tokens

**Type**: `int`
**Default**: `4096`
**Required**: No

Maximum number of tokens in the response.


#### timeout_seconds

**Type**: `int`
**Default**: `60`
**Required**: No

Request timeout in seconds. Applies to each individual API call.


### Complete YAML Example

```yaml
llm:
  provider: huggingface
  huggingface_model: meta-llama/Meta-Llama-3.1-70B-Instruct
  # huggingface_token: set via keyring (looms config set-key huggingface_token)
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60

  # Optional: client-side rate limiting
  rate_limit:
    requests_per_second: 2.0
    tokens_per_minute: 30000
    min_delay_ms: 400
```

### Minimal YAML Example

```yaml
llm:
  provider: huggingface
  # Uses default model: meta-llama/Meta-Llama-3.1-70B-Instruct
  # Token from keyring or HUGGINGFACE_API_KEY env var
```

### Agent-Level YAML Example

When configuring HuggingFace in an agent YAML config (not the server `looms.yaml`), use the generic `provider` and `model` fields:

```yaml
agent:
  name: my-agent
  llm:
    provider: huggingface
    model: meta-llama/Meta-Llama-3.1-70B-Instruct
    temperature: 0.7
    max_tokens: 4096
```


## Models

The following models have explicit cost entries in the provider. Any model available on the HuggingFace Inference API can be used; models not listed below fall back to the default cost estimate of $1.00 / $1.00 per million tokens.

### Llama Models

| Model ID | Notes |
|----------|-------|
| `meta-llama/Meta-Llama-3.1-70B-Instruct` | Default model, recommended |
| `meta-llama/Llama-3.1-70B-Instruct` | Alias |
| `meta-llama/Meta-Llama-3.1-8B-Instruct` | Faster, lower cost |
| `meta-llama/Llama-3.1-8B-Instruct` | Alias |

### Mixtral Models

| Model ID | Notes |
|----------|-------|
| `mistralai/Mixtral-8x7B-Instruct-v0.1` | Mixture of experts |
| `mistralai/Mixtral-8x22B-Instruct-v0.1` | Larger MoE variant |

### Qwen Models

| Model ID | Notes |
|----------|-------|
| `Qwen/Qwen2.5-72B-Instruct` | Large Qwen model |
| `Qwen/Qwen2.5-Coder-32B-Instruct` | Code-focused |

### Gemma Models

| Model ID | Notes |
|----------|-------|
| `google/gemma-2-9b-it` | Google's 9B model |
| `google/gemma-2-27b-it` | Google's 27B model |

Additional models listed on the [HuggingFace model hub](https://huggingface.co/models) that support OpenAI-compatible chat completions can be used by setting `huggingface_model` to the model ID.


## Cost Estimates

Cost calculation is performed client-side using hardcoded estimates. HuggingFace pricing varies significantly depending on the backend inference provider (Together AI, Cohere, Groq, self-hosted, etc.).

**The cost values in Loom are estimates, not exact.** For accurate pricing, consult:

- [huggingface.co/pricing](https://huggingface.co/pricing)
- The pricing page of the specific backend provider

| Model | Input ($/1M tokens) | Output ($/1M tokens) | Source estimate |
|-------|---------------------|----------------------|-----------------|
| Llama 3.1 70B | $0.80 | $0.80 | Together AI |
| Llama 3.1 8B | $0.20 | $0.20 | Together AI |
| Mixtral 8x7B / 8x22B | $0.60 | $0.60 | Together AI |
| Qwen 2.5 72B / Coder 32B | $0.80 | $0.80 | Estimated |
| Gemma 2 9B / 27B | $0.30 | $0.30 | Estimated |
| Unknown / other models | $1.00 | $1.00 | Conservative default |


## Tool Calling

Tool calling is supported. Because the HuggingFace provider delegates to the OpenAI client, tools are sent as OpenAI-format function definitions and tool call responses are parsed the same way.

Whether tool calling actually works depends on the model. Models like Llama 3.1, Mixtral, and Qwen 2.5 support function calling through the HuggingFace Inference API. Smaller or older models may not.

There is no tool mode configuration (unlike the Ollama provider). If the model supports tools in the OpenAI format, they work. If it does not, tool calls will fail or be ignored by the model.


## Error Codes

### Invalid Token

**Cause**: Missing or invalid HuggingFace token.

**Examples**:
```
huggingface token is required (set via --huggingface-token, LOOM_LLM_HUGGINGFACE_TOKEN, or save to keyring with 'looms config set-key huggingface_token')
```
```
huggingface token not configured (set llm.huggingface_token or HUGGINGFACE_API_KEY)
```

**Resolution**: Set the token via keyring, environment variable, or config. See [Authentication](#authentication).


### API Error from HuggingFace

**Cause**: HuggingFace returns an error (invalid token, model not found, rate limit, etc.).

**Example**:
```
Invalid HuggingFace token
```

**Resolution**:
1. Verify your token at [huggingface.co/settings/tokens](https://huggingface.co/settings/tokens)
2. Verify the model ID is correct and accessible
3. Check your HuggingFace account has Inference API access


### Timeout

**Cause**: Request exceeded `timeout_seconds`.

**Resolution**: Increase `timeout_seconds` in the YAML config, or switch to a smaller/faster model.


## Limitations

- **OpenAI-compatible endpoint only**: The provider uses `https://router.huggingface.co/v1/chat/completions`. Models that do not support this endpoint format will not work.
- **Cost estimates are approximate**: Pricing varies by backend provider. The hardcoded estimates may not match your actual billing.
- **No streaming**: The current implementation does not support streaming responses.
- **No vision/image input**: Only text-based chat is supported.
- **Endpoint is not configurable**: The HuggingFace router URL is hardcoded. Self-hosted HuggingFace endpoints are not supported through this provider (use the OpenAI provider with a custom endpoint instead).
- **Rate limiting depends on your HuggingFace plan**: Free tier has lower rate limits than Pro or Enterprise. Client-side rate limiting can be configured via the `rate_limit` section.
- **No dedicated CLI flag**: Unlike Anthropic (`--anthropic-key`) or OpenAI (`--openai-key`), there is no `--huggingface-token` CLI flag. The error message references one, but it does not exist. Use the keyring or environment variables instead.


## See Also

### Loom Documentation
- [LLM Providers Overview](./llm-providers.md) - All supported LLM providers
- [OpenAI Provider](./llm-openai.md) - OpenAI provider (same underlying client)
- [Agent Configuration](./agent-configuration.md) - Agent YAML configuration
- [CLI Reference](./cli.md) - Command-line interface

### HuggingFace Documentation
- [HuggingFace Inference API](https://huggingface.co/inference-api) - Inference API overview
- [HuggingFace Models](https://huggingface.co/models) - Browse available models
- [HuggingFace Tokens](https://huggingface.co/settings/tokens) - Manage access tokens
- [HuggingFace Pricing](https://huggingface.co/pricing) - Pricing details
