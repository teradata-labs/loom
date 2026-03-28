# Google Gemini Provider

Technical reference for connecting Loom to Google Gemini via the Google AI Studio API.

**Version**: v1.2.0

**Status**: ✅ Implemented. Supports text, vision, tool calling, and streaming.


## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Overview](#overview)
3. [Authentication](#authentication)
4. [Configuration](#configuration)
5. [Available Models](#available-models)
6. [Features](#features)
7. [YAML Configuration Examples](#yaml-configuration-examples)
8. [Server Integration](#server-integration)
9. [Builder API Usage](#builder-api-usage)
10. [Cost Estimation](#cost-estimation)
11. [Rate Limiting](#rate-limiting)
12. [Error Codes](#error-codes)
13. [Limitations and Notes](#limitations-and-notes)
14. [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
llm:
  provider: gemini
  gemini_model: gemini-3-flash-preview
  temperature: 1.0
  max_tokens: 8192
  timeout_seconds: 60
```

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `GEMINI_API_KEY` | Google AI Studio API key | Yes (if not set via config or keyring) |
| `LOOM_LLM_GEMINI_API_KEY` | Alternative env var via Loom prefix | Yes (if not set via config or keyring) |
| `LOOM_LLM_GEMINI_MODEL` | Model override via Loom prefix | No |

### Authentication Priority

1. Config file (`llm.gemini_api_key` in `looms.yaml`)
2. System keyring (`looms config set-key gemini_api_key`)
3. Environment variable (`GEMINI_API_KEY` or `LOOM_LLM_GEMINI_API_KEY`)

**Note**: There is no `--gemini-key` CLI flag. Unlike the Anthropic provider which has `--anthropic-key`, Gemini authentication must use config, keyring, or environment variables.


## Overview

The Gemini provider connects Loom to Google's Gemini models through the Google AI Studio REST API (`generativelanguage.googleapis.com/v1beta`). It implements both the `LLMProvider` and `StreamingLLMProvider` interfaces, supporting:

- Text generation
- Vision (base64-encoded images via `inlineData`)
- Native function calling (tool use)
- Token-by-token streaming via Server-Sent Events (SSE)
- Implicit caching (automatic since May 2025; cached tokens tracked in usage metadata)
- Cost calculation per model
- Client-side rate limiting (shared singleton across all Gemini client instances)

**Source code**: `pkg/llm/gemini/client.go`, `pkg/llm/gemini/types.go`


## Authentication

An API key from [Google AI Studio](https://makersuite.google.com/) is required.

### Setting the API Key

**System keyring** (recommended):
```bash
looms config set-key gemini_api_key
```

**Environment variable**:
```bash
export GEMINI_API_KEY=your-api-key-here
```

**Config file** (not recommended for secrets):
```yaml
llm:
  gemini_api_key: your-api-key-here  # Prefer keyring instead
```

If no API key is found, the factory returns an error:
```
gemini API key not configured (set llm.gemini_api_key or GEMINI_API_KEY)
```


## Configuration

### Config Struct

The Go `Config` struct in `pkg/llm/gemini/client.go`:

```go
type Config struct {
    APIKey            string              // Required: API key from https://makersuite.google.com/
    Model             string              // Default: "gemini-3-flash-preview"
    MaxTokens         int                 // Default: 8192
    Temperature       float64             // Default: 1.0
    Timeout           time.Duration       // Default: 60s
    RateLimiterConfig llm.RateLimiterConfig // Optional rate limiter
}
```

### Configuration Parameters

#### provider

**Type**: `string`
**Required**: Yes
**Value**: `gemini`

Set this to `gemini` to use Google Gemini.


#### gemini_api_key

**Type**: `string`
**Required**: Yes (via config, keyring, or env var)

Google AI Studio API key. See [Authentication](#authentication) for all methods to provide this value.

**Security**: Do not store in config files checked into source control. Use keyring or environment variables.


#### gemini_model

**Type**: `string`
**Default**: `gemini-3-flash-preview`
**Required**: No

The Gemini model identifier. See [Available Models](#available-models) for all options.

**Examples**:
- `gemini-3-flash-preview` -- Default, balanced speed and quality (free during preview)
- `gemini-3-pro-preview` -- Most intelligent Gemini model (preview)
- `gemini-2.5-flash` -- Stable workhorse, $0.30/$2.50 per 1M tokens
- `gemini-2.5-pro` -- Complex reasoning with 1M context
- `gemini-2.5-flash-lite` -- Fastest and cheapest


#### temperature

**Type**: `float64`
**Default**: `1.0`
**Range**: `0.0` - `2.0`
**Required**: No

Sampling temperature for generation.

- **0.0-0.3**: Deterministic, focused responses
- **0.7-1.0**: Balanced
- **1.5-2.0**: Creative, may be less coherent


#### max_tokens

**Type**: `int`
**Default**: `8192`
**Range**: `1` - model's max output tokens (see model table)
**Required**: No

Maximum number of output tokens per response. Maps to the Gemini API's `maxOutputTokens` field.


#### timeout_seconds

**Type**: `int`
**Default**: `60`
**Range**: `1` - `3600`
**Required**: No

HTTP client timeout in seconds for API requests.


## Available Models

### Gemini 3 Preview Models

These models are in preview (as of 2026-03) and are used as defaults. They are supported in the client and cost calculator but are not yet in the GA model catalog dropdown.

| Model ID | Name | Context Window | Max Output | Input Cost (per 1M) | Output Cost (per 1M) | Capabilities |
|----------|------|----------------|------------|---------------------|----------------------|--------------|
| `gemini-3-pro-preview` | Gemini 3 Pro | 1,048,576 | 65,536 | $2.00-$4.00 (tiered) | $12.00-$18.00 (tiered) | text, vision, tool-use, thinking, image-gen |
| `gemini-3-flash-preview` | Gemini 3 Flash | 1,048,576 | 65,536 | Free during preview | Free during preview | text, vision, tool-use, thinking |

### GA Model Catalog (registered in model registry)

These GA models appear in Loom's model picker dropdown:

| Model ID | Name | Context Window | Max Output | Input Cost (per 1M) | Output Cost (per 1M) | Capabilities |
|----------|------|----------------|------------|---------------------|----------------------|--------------|
| `gemini-2.5-pro` | Gemini 2.5 Pro | 1,048,576 | 65,536 | $1.25 (low tier) | $10.00 (low tier) | text, vision, tool-use, thinking |
| `gemini-2.5-flash` | Gemini 2.5 Flash | 1,048,576 | 65,536 | $0.30 | $2.50 | text, vision, tool-use, thinking |
| `gemini-2.5-flash-lite` | Gemini 2.5 Flash-Lite | 1,048,576 | 32,768 | $0.10 | $0.40 | text, vision, tool-use |

**Note on tiered pricing**: Gemini 2.5 Pro and Gemini 3 Pro have tiered pricing based on prompt length (above/below 200k tokens). The `calculateCost()` function in `client.go` uses mid-range averages: $1.875/$12.50 for 2.5 Pro, $3.00/$15.00 for 3 Pro.

**Pricing source**: Comments in `pkg/llm/gemini/client.go`. Check [ai.google.dev/pricing](https://ai.google.dev/pricing) for current rates.


## Features

### Text Generation

Standard text input/output via the `Chat` method. Messages are converted to Gemini's `contents` format:
- `user` role maps to Gemini `user`
- `assistant` role maps to Gemini `model`
- `system` role is prepended as a `user` message with `"System instruction: "` prefix (Gemini does not have a native system role)
- `tool` role maps to Gemini `function`

### Vision (Multimodal)

Images are supported via base64-encoded inline data. When a message includes `ContentBlocks` of type `"image"` with a `base64` source, they are converted to Gemini's `inlineData` parts with the appropriate MIME type.

**Limitation**: URL-based image references are not supported by the Gemini REST API. Images must be base64-encoded.

### Tool Calling (Function Calling)

Tools registered with the agent are converted to Gemini `FunctionDeclaration` objects. The provider handles:
- Tool name sanitization (Gemini has stricter naming rules than other providers) and reverse mapping on response
- Schema conversion including nested properties, arrays with items, and enum values
- Empty type defaults: properties with empty `type` default to `"string"`; properties with sub-`properties` default to `"object"`; properties with `items` default to `"array"`

When the model returns `FunctionCall` parts, the response `StopReason` is set to `"tool_use"`.

### ThoughtSignature

Gemini 3+ models return an opaque `thoughtSignature` token at the `Part` level alongside function calls. This token must be echoed back verbatim in conversation history for function calling to work correctly. Loom handles this automatically:
1. The signature is captured from the response `Part` and stored on `ToolCall.ThoughtSignature`
2. When building the next request, the signature is placed back on the `Part` (not inside `FunctionCall`)
3. For parallel function calls, only the first call receives a signature per Gemini documentation

### Streaming

Token-by-token streaming is implemented via the `ChatStream` method using Gemini's `streamGenerateContent` endpoint with `alt=sse`. The stream:
- Uses Server-Sent Events (SSE) format
- Calls the `tokenCallback` for each text token received
- Extracts tool calls, finish reason, and usage metadata from streamed chunks
- Supports context cancellation during streaming
- Includes `"streaming": true` in response metadata

### Implicit Caching

Gemini's implicit caching (automatic since May 2025) is tracked via `UsageMetadata.CachedContentTokenCount`. This maps to `Usage.CacheReadInputTokens` in the Loom response for observability. Note: cached tokens still count against rate limits (cost savings only).


## YAML Configuration Examples

### Minimal Configuration

```yaml
llm:
  provider: gemini
  # API key set via keyring: looms config set-key gemini_api_key
```

Uses defaults: model `gemini-3-flash-preview`, temperature `1.0`, max_tokens `8192`, timeout `60s`.

### Full Configuration

```yaml
llm:
  provider: gemini
  gemini_model: gemini-3-flash-preview
  temperature: 0.7
  max_tokens: 4096
  timeout_seconds: 120

  # Rate limiting (optional)
  rate_limit:
    disabled: false
    requests_per_second: 2.0
    tokens_per_minute: 40000
    burst_capacity: 5
    min_delay_ms: 300
    max_retries: 5
    retry_backoff_ms: 1000
    queue_timeout_seconds: 300
```

### Per-Agent Configuration

```yaml
agents:
  agents:
    reasoning-agent:
      name: Reasoning Agent
      description: Uses Gemini 2.5 Pro for complex reasoning tasks
      system_prompt: "Analyze the following data and provide insights."
      max_turns: 25
      llm:
        provider: gemini
        gemini_model: gemini-2.5-pro
        temperature: 0.5
        max_tokens: 8192

    fast-agent:
      name: Fast Agent
      description: Uses Gemini 2.5 Flash-Lite for quick responses
      system_prompt: "Respond concisely."
      max_turns: 10
      llm:
        provider: gemini
        gemini_model: gemini-2.5-flash-lite
        temperature: 1.0
        max_tokens: 2048
```

### Named Provider Pool Entry

```yaml
providers:
  - name: gemini-flash
    provider: gemini
    gemini_model: gemini-3-flash-preview
    temperature: 1.0
    max_tokens: 4096

  - name: gemini-pro
    provider: gemini
    gemini_model: gemini-2.5-pro
    temperature: 0.7
    max_tokens: 8192

active_provider: gemini-flash
```


## Server Integration

### Starting with `looms serve`

```bash
# Using keyring for API key (recommended)
looms config set-key gemini_api_key
looms serve

# Using environment variable
export GEMINI_API_KEY=your-key
looms serve
```

**Note**: There is no `--gemini-key` CLI flag. Use keyring, environment variable, or config file to provide the API key.

### Verifying the Provider

After starting the server, verify with gRPC:

```bash
grpcurl -plaintext -d '{"query": "Hello"}' \
  localhost:60051 loom.v1.LoomService/Weave
```

The response `cost.llmCost.provider` field will show `"gemini"`.


## Builder API Usage

The `builder` package provides convenience methods for creating agents with Gemini.

### Default Model (gemini-3-flash-preview)

`WithGeminiLLM` defaults to `gemini-3-flash-preview` (free during preview).

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/teradata-labs/loom/pkg/builder"
)

func main() {
    apiKey := os.Getenv("GEMINI_API_KEY")
    if apiKey == "" {
        log.Fatal("GEMINI_API_KEY not set")
    }

    // WithBackend is required; WithGeminiLLM defaults to gemini-3-flash-preview
    ag, err := builder.NewAgentBuilder().
        WithBackend(myBackend).  // see backend.md for creating backends
        WithGeminiLLM(apiKey).
        Build()
    if err != nil {
        log.Fatalf("Failed to build agent: %v", err)
    }

    ctx := context.Background()
    resp, err := ag.Chat(ctx, "session-1", "What is 2 + 2?")
    if err != nil {
        log.Fatalf("Chat failed: %v", err)
    }
    fmt.Println(resp.Content)
}
```

### Custom Model

```go
ag, err := builder.NewAgentBuilder().
    WithBackend(myBackend).
    WithGeminiLLMCustomModel(apiKey, "gemini-2.5-pro").
    Build()
```

**Note**: The builder has no `WithSystemPrompt` method. To set a system prompt, use `agent.WithSystemPrompt()` as an agent option, or configure it via YAML (`system_prompt` field in agent config).

### Direct Client Construction

For full control over configuration, create the client directly:

```go
package main

import (
    "time"

    "github.com/teradata-labs/loom/pkg/llm"
    "github.com/teradata-labs/loom/pkg/llm/gemini"
)

func main() {
    client := gemini.NewClient(gemini.Config{
        APIKey:      "your-api-key",
        Model:       "gemini-3-flash-preview",
        MaxTokens:   4096,
        Temperature: 0.7,
        Timeout:     120 * time.Second,
        RateLimiterConfig: llm.RateLimiterConfig{
            Enabled:           true,
            RequestsPerSecond: 2.0,
            TokensPerMinute:   40000,
        },
    })

    // client implements both llmtypes.LLMProvider and llmtypes.StreamingLLMProvider
    _ = client.Name()  // "gemini"
    _ = client.Model() // "gemini-3-flash-preview"
}
```


## Cost Estimation

Cost is calculated per-request in `calculateCost()` using per-million-token rates. The pricing is hardcoded in `pkg/llm/gemini/client.go`. For models with tiered pricing (above/below 200k tokens), mid-range averages are used.

| Model | Input Cost (per 1M tokens) | Output Cost (per 1M tokens) | Cost for 1k input + 500 output tokens |
|-------|---------------------------|----------------------------|---------------------------------------|
| `gemini-3-pro-preview` | $3.00 (mid-range) | $15.00 (mid-range) | ~$0.01050 |
| `gemini-3-flash-preview` | $0.00 (free during preview) | $0.00 (free during preview) | $0.00000 |
| `gemini-2.5-pro` | $1.875 (mid-range) | $12.50 (mid-range) | ~$0.00813 |
| `gemini-2.5-flash` | $0.30 | $2.50 | ~$0.00155 |
| `gemini-2.5-flash-lite` | $0.10 | $0.40 | ~$0.00030 |

Unknown models default to `gemini-2.5-flash` pricing ($0.30/$2.50).

**Note**: The mid-range averages differ from the model catalog's low-tier prices. The catalog shows $1.25/$10.00 for 2.5 Pro (low tier, prompts up to 200k tokens). The cost calculator uses the mid-range average ($1.875/$12.50) to better reflect mixed workloads.


## Rate Limiting

The Gemini provider uses a **global singleton rate limiter** shared across all Gemini client instances in the process. This is initialized once via `sync.Once`.

Rate limiting is optional and controlled by the `RateLimiterConfig` passed in the `Config` struct. When enabled via YAML:

```yaml
llm:
  provider: gemini
  rate_limit:
    disabled: false
    requests_per_second: 2.0
    tokens_per_minute: 40000
    burst_capacity: 5
    min_delay_ms: 300
    max_retries: 5
    retry_backoff_ms: 1000
    queue_timeout_seconds: 300
```

Both `Chat` and `ChatStream` methods respect the rate limiter. Token usage is recorded for the rate limiter's metrics after streaming completes.


## Error Codes

### API Error (HTTP non-200)

**Cause**: Gemini API returned a non-200 HTTP status code.

**Example**:
```
API error (status 401): {"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}
```

**Resolution**: Verify API key is correct and has not been revoked. Check at [makersuite.google.com](https://makersuite.google.com/).


### Gemini API Error (in-body)

**Cause**: The HTTP response was 200 but the body contains an `error` object.

**Example**:
```
gemini API error: Invalid API key (code: 400)
```

**Resolution**: Same as above. Check API key validity.


### Request Marshaling Error

**Cause**: Failed to serialize request body to JSON.

**Example**:
```
failed to marshal request: json: unsupported type
```

**Resolution**: This indicates a bug in message/tool conversion. File an issue.


### Stream Read Error

**Cause**: Error reading SSE stream during `ChatStream`.

**Example**:
```
error reading stream: unexpected EOF
```

**Resolution**: May indicate network interruption or server-side timeout. Increase `timeout_seconds` or retry the request.


### Context Cancellation

**Cause**: The context was cancelled during streaming.

**Resolution**: This is expected behavior when the caller cancels the request. No action needed unless unintentional.


## Limitations and Notes

1. **No native system role**: Gemini does not have a `system` role. System messages are prepended as `user` messages with a `"System instruction: "` prefix.

2. **Role mapping**: Gemini uses `"model"` instead of `"assistant"` and `"function"` instead of `"tool"` for message roles. Loom handles this conversion automatically.

3. **No URL-based images**: The Gemini REST API does not support URL-based image references. Images must be provided as base64-encoded data via `inlineData`.

4. **Tool name sanitization**: Gemini has stricter tool name requirements than other providers. Loom sanitizes tool names before sending and reverses the mapping on response.

5. **Empty message handling**: Messages with empty content or no parts are skipped to avoid the Gemini "at least one parts field" API error.

6. **ThoughtSignature round-trip**: Gemini 3+ models return an opaque `thoughtSignature` that must be echoed back verbatim. Loom handles this automatically; however, if you are constructing message history manually, you must preserve `ToolCall.ThoughtSignature` values.

7. **No call IDs**: Gemini does not provide function call IDs in its response. The reversed tool name is used as the call ID.

8. **Global rate limiter**: The rate limiter is a process-wide singleton. All Gemini client instances share the same rate limiter, initialized from the first client that enables it.

9. **Cached tokens and rate limits**: Gemini's implicit caching provides cost savings, but cached tokens still count against rate limits.


## See Also

### Loom Documentation
- [LLM Providers Overview](./llm-providers.md) -- All supported LLM providers
- [CLI Reference](./cli.md) -- Command-line interface
- [Agent Configuration](./agent-configuration.md) -- Agent YAML configuration

### Google Documentation
- [Google AI Studio](https://makersuite.google.com/) -- API key management
- [Gemini API Pricing](https://ai.google.dev/pricing) -- Current pricing
- [Gemini API Reference](https://ai.google.dev/api/rest) -- REST API docs
- [Gemini Function Calling](https://ai.google.dev/docs/function_calling) -- Tool calling guide
