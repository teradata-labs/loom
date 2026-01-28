
# Anthropic Claude Integration

This guide explains how to connect Loom to Anthropic's Claude API.

## Overview

Anthropic is the recommended LLM provider for Loom. It provides:
- Native tool calling support
- High-quality Claude 3.5 Sonnet model
- Excellent reasoning and coding capabilities
- Reliable API with good rate limits

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

| Model | ID | Best For |
|-------|----|----- |
| Claude Sonnet 4.5 | `claude-sonnet-4-5-20250929` | Latest model, best performance (recommended) |
| Claude Haiku 4.5 | `claude-haiku-4-5-20250128` | Fast, cost-effective tasks |
| Claude Opus 4.1 | `claude-opus-4-1-20250514` | Maximum intelligence, complex reasoning |
| Claude 3.5 Sonnet | `claude-3-5-sonnet-20241022` | Previous generation, reliable |
| Claude 3 Opus | `claude-3-opus-20240229` | Legacy maximum intelligence |
| Claude 3 Haiku | `claude-3-haiku-20240307` | Legacy fast model |

## Testing Your Setup

1. Start the server:
```bash
looms serve
```

2. Test with grpcurl:
```bash
grpcurl -plaintext -d '{"query": "What is 2+2?"}' \
  localhost:9090 loom.v1.LoomService/Weave
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
      "costUsd": 0.00018
    }
  }
}
```

## Cost Estimation

Claude Sonnet 4.5 pricing (as of 2025):
- **Input**: $3.00 per 1M tokens
- **Output**: $15.00 per 1M tokens

Example costs:
- Simple query (50 input, 100 output tokens): $0.0018
- Medium task (500 input, 1000 output tokens): $0.018
- Large task (5000 input, 10000 output tokens): $0.18

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
- **Tier 1**: 50 requests/minute, 40,000 tokens/minute
- **Tier 2**: 1000 requests/minute, 80,000 tokens/minute

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
2. **Model Selection**: Use Claude 3.5 Sonnet for most tasks (best balance of quality/cost)
3. **Temperature**: Use 1.0 for creative tasks, 0.7 for deterministic tasks
4. **Token Limits**: Set appropriate `max_tokens` based on your use case
5. **Monitoring**: Enable observability to track token usage and costs

## Advanced Configuration

### Custom Endpoint

For Anthropic proxy or testing:

```bash
# Set via environment
export LOOM_LLM_ANTHROPIC_ENDPOINT="https://your-proxy.example.com"
```

### Timeout Adjustment

For long-running tasks:

```yaml
llm:
  timeout_seconds: 120  # 2 minutes
```

## Next Steps

- See [Architecture](../architecture/) for how Loom uses the LLM
- Check [Examples](../examples/) for sample prompts and patterns
- Read [Tool Calling](L](./TOOL_CALLING/) for advanced agent capabilities

## Support

- Anthropic Documentation: https://docs.anthropic.com
- Anthropic Status: https://status.anthropic.com
- Loom Issues: https://github.com/teradata-labs/loom/issues
