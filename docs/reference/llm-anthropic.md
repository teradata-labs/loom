
# Anthropic Claude Integration

This guide explains how to connect Loom to Anthropic's Claude API.

## Overview

**Version**: v1.3.0

Anthropic is the recommended LLM provider for Loom. The integration provides:
- ✅ Native tool calling support
- ✅ Streaming via `ChatStream` method (`StreamingLLMProvider` interface)
- ✅ Vision/image support (multimodal content blocks)
- ✅ Prompt caching (automatic, cache-aware cost calculation)
- ✅ Built-in rate limiter with exponential backoff and 429 retry
- ✅ Configurable custom API endpoint (for proxies or testing)

## Prerequisites

1. **Anthropic API Key**: Get your API key from [console.anthropic.com](https://console.anthropic.com/)
2. **API Access**: Ensure your account has API access enabled

## Configuration

### Option 1: System Keyring (Recommended)

Store your API key securely in the system keyring:

```bash
looms config set-key anthropic_api_key
# Enter your API key when prompted (input is hidden)
```

The key will be stored in:
- **macOS**: Keychain
- **Windows**: Credential Manager
- **Linux**: Secret Service (libsecret)

### Option 2: Environment Variable

```bash
export LOOM_LLM_ANTHROPIC_API_KEY="sk-ant-api03-..."
```

### Option 3: CLI Flag

```bash
looms serve --anthropic-key "sk-ant-api03-..."
```

**Warning**: Never commit API keys to config files!

## Configuration File

Edit `$LOOM_DATA_DIR/looms.yaml`:

```yaml
llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

### Available Models

Source of truth: `pkg/llm/catalog/catalog.go` (`BuildCatalog()`).

| Model | ID | Context | Max Output | Input $/1M | Output $/1M |
|-------|----|---------|------------|------------|-------------|
| Claude Opus 4.7 | `claude-opus-4-7` | 1M | 128K | $5.00 | $25.00 |
| Claude Opus 4.6 | `claude-opus-4-6` | 1M | 128K | $5.00 | $25.00 |
| Claude Sonnet 4.6 | `claude-sonnet-4-6` | 1M | 64K | $3.00 | $15.00 |
| Claude Opus 4.5 | `claude-opus-4-5-20251101` | 200K | 64K | $5.00 | $25.00 |
| Claude Sonnet 4.5 | `claude-sonnet-4-5-20250929` | 200K | 64K | $3.00 | $15.00 |
| Claude Haiku 4.5 | `claude-haiku-4-5-20251001` | 200K | 64K | $1.00 | $5.00 |
| Claude Opus 4.1 | `claude-opus-4-1-20250805` | 200K | 32K | $15.00 | $75.00 |

All models support: text, vision, tool-use, thinking (per catalog capabilities). The default model when none is configured is `claude-sonnet-4-5-20250929` (`pkg/llm/factory/factory.go`).

## Testing Your Setup

1. Start the server:
```bash
looms serve
```

2. Test with grpcurl:
```bash
grpcurl -plaintext -d '{"query": "What is 2+2?"}' \
  localhost:60051 loom.v1.LoomService/Weave
```

Expected output:
```json
{
  "text": "2 + 2 equals 4.",
  "sessionId": "...",
  "cost": {
    "llmCost": {
      "provider": "anthropic",
      "model": "claude-sonnet-4-5-20250929",
      "inputTokens": 10,
      "outputTokens": 8,
      "costUsd": 0.00015
    }
  }
}
```

## Cost Estimation

`calculateCost` in `pkg/llm/anthropic/client.go` resolves per-million-token rates in this order:
the model catalog (`catalog.LookupPricing`), then `catalog.ClaudeFamilyPricing` (substring match on
the model family, shared with the Bedrock and OpenAI-compatible clients), then the Sonnet 4 rate
($3/$15) for an id that is not recognisably Claude.

| Model family | Input $/1M | Output $/1M | Cache read |
|--------------|------------|-------------|------------|
| Fable 5.1 / Mythos 5.1 | $10.00 | $50.00 | 0.025x input ($0.25) |
| Fable 5 / Mythos 5 | $10.00 | $50.00 | 0.10x input ($1.00) |
| Opus 5.5 | $4.00 | $20.00 | 0.05x input ($0.20) |
| Opus 5 | $5.00 | $25.00 | 0.10x input |
| Sonnet 5.5 | $2.00 | $10.00 | 0.05x input ($0.10) |
| Sonnet 5 | $2.00 | $10.00 | 0.10x input |
| Haiku 5.5 (prompt ≤ 100K tokens) | $0.10 | $0.50 | 0.10x input |
| Haiku 5.5 (prompt > 100K tokens) | $0.50 | $2.50 | 0.10x input |
| Opus 4.5 – 4.8 | $5.00 | $25.00 | 0.10x input |
| Opus 4.1 | $15.00 | $75.00 | 0.10x input |
| Haiku 4.5 | $1.00 | $5.00 | 0.10x input |
| Sonnet 4.x | $3.00 | $15.00 | 0.10x input |

A cache write (5-minute TTL) bills at 1.25x the input rate on every model. Cache-read multipliers
come from `catalog.ClaudeCacheReadMultiplier`. Haiku 5.5's prompt length counts every input token
of the request, cache reads and writes included (`catalog.ClaudeLongContextPricing`). Rates are
Anthropic's published first-party card; 1-hour cache writes (2x) and `inference_geo` pricing are
not modelled.

Example costs for Sonnet-tier models ($3.00/$15.00):
- Small query (50 input, 100 output tokens): $0.00165
- Medium task (500 input, 1000 output tokens): $0.0165
- Large task (5000 input, 10000 output tokens): $0.165

## Troubleshooting

### Error: "Invalid API key"

- Verify your API key is correct
- Check that the key starts with `sk-ant-api03-`
- Ensure the key is properly set in keyring/env/CLI

```bash
# Verify keyring storage
looms config get-key anthropic_api_key

# Test with explicit key
looms serve --anthropic-key "sk-ant-api03-..."
```

### Error: "Rate limit exceeded"

Anthropic enforces rate limits:
- **Free / Tier 1**: 50 requests/minute, 30,000-100,000 input tokens/minute
- **Tier 2**: 1,000 requests/minute, 2,000,000 input tokens/minute
- **Tier 3+**: 5,000+ requests/minute

Solutions:
- Reduce `max_tokens` in config
- Add delays between requests
- Upgrade your Anthropic tier

### Error: "Model not found"

- Verify the model ID is correct
- Check your Anthropic account has access to the model
- Some models require explicit access grants

## Best Practices

1. **Use Keyring**: Always store API keys in the system keyring, never in config files
2. **Model Selection**: Use Claude Sonnet 4.5 for most tasks (best balance of quality/cost)
3. **Temperature**: Use 1.0 for creative tasks, 0.7 for deterministic tasks
4. **Token Limits**: Set appropriate `max_tokens` based on your use case
5. **Monitoring**: Enable observability to track token usage and costs

## Advanced Configuration

### Custom API Endpoint

For proxies or testing, override the default Anthropic API endpoint
(`https://api.anthropic.com/v1/messages`) using an environment variable:

```bash
export ANTHROPIC_API_ENDPOINT=https://custom-proxy.example.com
```

When unset, the client falls back to `DefaultAnthropicEndpoint` defined in
`pkg/llm/anthropic/client.go`.

### Timeout Adjustment

For long-running tasks:

```yaml
llm:
  timeout_seconds: 120  # 2 minutes
```

## Streaming Support

✅ Streaming is implemented via the `ChatStream` method in `pkg/llm/anthropic/client.go`, which
uses Anthropic's Messages API with `stream=true`. The Anthropic client satisfies the
`StreamingLLMProvider` interface (verified by compile-time assertion:
`var _ llmtypes.StreamingLLMProvider = (*Client)(nil)`), delivering tokens to a
`llmtypes.TokenCallback` as they are generated. Streaming is used automatically by gRPC streaming
RPCs (e.g., `StreamWeave`) and the TUI chat interface.

## Prompt Caching

✅ Prompt caching is enabled by default. The client sends the `anthropic-beta: prompt-caching-2024-07-31`
header on every request (see `client.go` lines 537, 760). Cached tokens do not count against
Anthropic's input-tokens-per-minute (ITPM) rate limit. Cost calculation is cache-aware
(see `calculateCost` in `pkg/llm/anthropic/client.go`):

- **Cache creation (write)**: $3.75 per 1M tokens (1.25x input price)
- **Cache read**: $0.30 per 1M tokens (0.10x input price) on Sonnet 4.x; the multiplier is per model (see [Cost Estimation](#cost-estimation))

Token usage is reported in the `LLMCost` proto message via `cache_read_input_tokens` (field 7)
and `cache_creation_input_tokens` (field 8).

No configuration is required; caching is handled automatically by the Anthropic API when
repeated system prompts or long conversations are detected.

## Rate Limiter

✅ The Anthropic client includes a built-in rate limiter (`pkg/llm.RateLimiter`) that is shared
by every Anthropic client on the same model with the same rate-limit values (clients that set
different values each get their own limiter, with a logged warning that the combined rate can
exceed the quota). The rate limiter:

- Automatically retries HTTP 429 (rate limit) responses with exponential backoff
- Respects Anthropic's per-tier ITPM (input-tokens-per-minute) limits
- Does not count cached tokens against ITPM limits

Default tier targets (Tier 1):
- 50 requests per minute
- 30K-100K input tokens per minute

The rate limiter is configured via `RateLimiterConfig` when constructing the client. See
`DefaultAnthropicRateLimiterConfig()` in `pkg/llm/anthropic/client.go` for default values.

## Vision / Image Support

✅ The Anthropic client supports multimodal content blocks (text + images). Messages with
`ContentBlocks` containing `"image"` type entries are converted to Anthropic's native image
content block format with base64-encoded `source` data. Supported media types include
`image/jpeg`, `image/png`, etc.

See `convertMessages` in `pkg/llm/anthropic/client.go` and `ImageSource` in
`pkg/llm/anthropic/types.go` for implementation details.

## Next Steps

- See [Architecture](../architecture/loom-system-architecture.md) for how Loom uses the LLM
- See [LLM Providers](./llm-providers.md) for multi-provider overview and comparison
- Read [Tool Registry](./tool-registry.md) for tool calling and registration

## Support

- Anthropic Documentation: https://docs.anthropic.com
- Anthropic Status: https://status.anthropic.com
- Loom Issues: https://github.com/teradata-labs/loom/issues
