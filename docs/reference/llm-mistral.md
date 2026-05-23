
# Mistral AI Integration Reference

**Version**: v1.2.0

Technical reference for integrating Loom with Mistral AI's platform.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Features](#features)
- [Configuration](#configuration)
- [Model Support and Pricing](#model-support-and-pricing)
- [Model Details](#model-details)
- [Request and Response Format](#request-and-response-format)
- [Cost Tracking](#cost-tracking)
- [Tool Calling Support](#tool-calling-support)
- [Error Handling](#error-handling)
- [Error Codes](#error-codes)
- [Rate Limiting](#rate-limiting)
- [Testing](#testing)
- [Best Practices](#best-practices)
- [OpenAI Compatibility](#openai-compatibility)
- [Comparison with Other Providers](#comparison-with-other-providers)
- [Limitations](#limitations)
- [Migration from OpenAI](#migration-from-openai)
- [See Also](#see-also)


## Quick Reference

### Configuration Summary

```go
// Builder API (Default Model)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLM(apiKey).  // Uses mistral-large-latest
    Build()

// Builder API (Custom Model)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLMCustomModel(apiKey, "mistral-small-latest").
    Build()

// Direct Client
client := mistral.NewClient(mistral.Config{
    APIKey:      "your-api-key",              // Required
    Model:       "mistral-large-latest",      // Default: mistral-large-latest
    MaxTokens:   4096,                        // Default: 4096
    Temperature: 1.0,                         // Default: 1.0
    Timeout:     60 * time.Second,            // Default: 60s
})
```

### Available Models

#### General Purpose Models

| Model | ID | Input Cost | Output Cost | Context | Max Output | Best For |
|-------|----|-----------:|------------:|---------|------------|----------|
| **Mistral Large** | `mistral-large-latest` | $2.00/1M | $6.00/1M | 128K | 8K | Complex tasks, tool use |
| **Mistral Small** | `mistral-small-latest` | $0.10/1M | $0.30/1M | 32K | 8K | High volume, cost-effective |

#### Reasoning Models

| Model | ID | Input Cost | Output Cost | Context | Max Output | Best For |
|-------|----|-----------:|------------:|---------|------------|----------|
| **Magistral Medium** | `magistral-medium-latest` | $2.00/1M | $8.00/1M | 128K | 131K | Complex reasoning + tools |
| **Magistral Small** | `magistral-small-latest` | $0.50/1M | $1.50/1M | 128K | 131K | Cost-effective reasoning |

#### Code Models

| Model | ID | Input Cost | Output Cost | Context | Max Output | Best For |
|-------|----|-----------:|------------:|---------|------------|----------|
| **Codestral** | `codestral-latest` | $0.30/1M | $0.90/1M | 256K | 8K | Code generation, analysis |
| **Devstral** | `devstral-medium-latest` | $0.50/1M | $1.50/1M | 128K | 8K | Development workflows |

*Prices from Loom model catalog (`pkg/llm/factory/model_catalog.go`). Check [mistral.ai/technology/#pricing](https://mistral.ai/technology/#pricing) for current rates.*

### Common Commands

```bash
# Set API key environment variable
export MISTRAL_API_KEY="your-api-key"

# Get API key from console
open https://console.mistral.ai/api-keys/

# Test with unit tests
go test -tags fts5 -v ./pkg/llm/mistral/
```

### Configuration Parameters

| Parameter | Type | Required | Default | Constraints |
|-----------|------|----------|---------|-------------|
| `APIKey` | `string` | Yes | - | From console.mistral.ai |
| `Model` | `string` | No | `mistral-large-latest` | See available models |
| `MaxTokens` | `int` | No | `4096` | 1-32768 |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 |
| `Timeout` | `duration` | No | `60s` | 1s-10m |


## Overview

Mistral AI provides models for text, reasoning, and code. The integration offers:
- OpenAI-compatible API (easy migration)
- Native function calling support
- 6 models: general purpose, reasoning (Magistral), and code (Codestral/Devstral)
- Context windows from 32K to 256K
- Competitive pricing ($0.10-$8.00 per 1M tokens)
- Cost tracking for all models

**Implementation**: `pkg/llm/mistral/client.go`
**Test Coverage**: 72.0%
**API Endpoint**: `https://api.mistral.ai/v1/chat/completions`
**Interface**: Full `LLMProvider` compliance via OpenAI wrapper


## Prerequisites

1. **Mistral API Key**: Get your API key from [console.mistral.ai](https://console.mistral.ai/)
2. **API Access**: Free tier available for testing
3. **Credits**: Mistral uses a credit-based billing system

**Getting Your API Key**:

1. Visit [console.mistral.ai](https://console.mistral.ai/)
2. Sign up or log in
3. Navigate to "API Keys"
4. Create a new API key
5. Copy the key (shown only once)

**Verification**:

```bash
# Test API key
curl https://api.mistral.ai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "mistral-small-latest",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```


## Features

### Implemented ✅

- Full LLMProvider interface implementation (`pkg/llm/mistral/client.go`)
- OpenAI-compatible message format
- Function calling with JSON schema conversion
- Cost calculation for legacy models (open and commercial); see Known Issue below for newer models
- Custom model selection
- Temperature and max tokens configuration
- Tool calling (parallel tool calls supported)
- 72.0% test coverage (430 lines of tests)

### Implemented ✅ (Streaming)

- Streaming via `StreamingLLMProvider` interface (`ChatStream` method)
- Delegates to the underlying OpenAI-compatible client's `ChatStream`, then recalculates cost using Mistral pricing
- See `pkg/llm/mistral/client.go`

### Partial ⚠️

- Automatic retry with exponential backoff (available via `RateLimiterConfig`, not Mistral-specific)
- Circuit breaker integration (not implemented in the Mistral client)

### Known Issue ⚠️

**Cost calculation drift**: The `calculateCost()` function in `client.go` uses hardcoded prices from 2024-11 that differ from the model catalog (`pkg/llm/factory/model_catalog.go`). For example, `mistral-large-latest` is priced at $4.00/$12.00 in `calculateCost()` but $2.00/$6.00 in the catalog. Additionally, newer models (Magistral, Codestral, Devstral) are not in `calculateCost()` and fall back to the $4.00/$12.00 default. The model catalog prices shown in this document reflect the catalog, not the runtime `calculateCost()` output.


## Configuration

### Using Builder API (Programmatic)

The Mistral provider is available through both the Builder API (programmatic) and YAML configuration (server mode):

```go
import (
    "github.com/teradata-labs/loom/pkg/builder"
    "github.com/teradata-labs/loom/pkg/llm/mistral"
)

// Option 1: Using builder with default model (mistral-large-latest)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLM(apiKey).
    Build()

if err != nil {
    log.Fatalf("Failed to build agent: %v", err)
}

// Option 2: Using builder with custom model
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLMCustomModel(apiKey, "mistral-small-latest").
    Build()
```

**Expected Output**:

```go
// Success
agent := &agent.Agent{...}

// Error: Missing API key
Error: API key is required
```


### Direct Client Creation

For full configuration control, instantiate the client directly:

```go
import "github.com/teradata-labs/loom/pkg/llm/mistral"

// Create client with custom configuration
client := mistral.NewClient(mistral.Config{
    APIKey:      "your-api-key",
    Model:       "mistral-large-latest",
    MaxTokens:   8192,        // Increase for longer responses
    Temperature: 0.7,         // Lower for more deterministic
    Timeout:     120 * time.Second, // Longer timeout
})

// Use as LLMProvider
var provider llmtypes.LLMProvider = client

// Get provider info
fmt.Printf("Provider: %s\n", provider.Name())  // "mistral"
fmt.Printf("Model: %s\n", provider.Model())    // "mistral-large-latest"
```

**Expected Output**:

```
Provider: mistral
Model: mistral-large-latest
```


### Environment Variables

The Mistral client accepts API keys from environment variables:

```go
apiKey := os.Getenv("MISTRAL_API_KEY")
if apiKey == "" {
    log.Fatal("MISTRAL_API_KEY environment variable required")
}

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLM(apiKey).
    Build()
```

**Configuration Parameters Details**:

| Parameter | Type | Required | Default | Range | Description |
|-----------|------|----------|---------|-------|-------------|
| `APIKey` | `string` | Yes | - | - | Mistral API key from console |
| `Model` | `string` | No | `mistral-large-latest` | See models table | Model identifier |
| `MaxTokens` | `int` | No | `4096` | 1-32768 | Maximum tokens in response |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 | Sampling temperature |
| `Timeout` | `duration` | No | `60s` | 1s-10m | Request timeout |


### Using YAML Configuration (Server Mode)

Mistral is also available through YAML agent configuration files when using `looms serve`:

```yaml
llm:
  provider: mistral
  mistral_model: mistral-large-latest
  # mistral_api_key: set via keyring (looms config set-key mistral_api_key)
  temperature: 1.0
  max_tokens: 4096
```

**API key management** (do not put API keys in YAML files):

```bash
# Store API key in keyring (recommended)
looms config set-key mistral_api_key

# Or set via environment variable
export LOOM_LLM_MISTRAL_API_KEY="your-api-key"
```

The factory (`pkg/llm/factory/factory.go`) resolves the API key in this order:
1. `llm.mistral_api_key` config field
2. `MISTRAL_API_KEY` environment variable


## Model Support and Pricing

Pricing per million tokens (source: `pkg/llm/factory/model_catalog.go`):

### General Purpose Models

| Model | ID | Input Cost | Output Cost | Context | Max Output | Capabilities |
|-------|----|-----------:|------------:|---------|------------|--------------|
| **Mistral Large** | `mistral-large-latest` | $2.00 | $6.00 | 128K | 8K | text, tool-use |
| **Mistral Small** | `mistral-small-latest` | $0.10 | $0.30 | 32K | 8K | text, tool-use |


### Reasoning Models (Magistral)

| Model | ID | Input Cost | Output Cost | Context | Max Output | Capabilities |
|-------|----|-----------:|------------:|---------|------------|--------------|
| **Magistral Medium** | `magistral-medium-latest` | $2.00 | $8.00 | 128K | 131K | text, tool-use, thinking |
| **Magistral Small** | `magistral-small-latest` | $0.50 | $1.50 | 128K | 131K | text, tool-use, thinking |

Magistral models support extended thinking (reasoning traces) and have a 131K max output window, making them suitable for complex multi-step reasoning tasks.


### Code Models

| Model | ID | Input Cost | Output Cost | Context | Max Output | Capabilities |
|-------|----|-----------:|------------:|---------|------------|--------------|
| **Codestral** | `codestral-latest` | $0.30 | $0.90 | 256K | 8K | text, tool-use |
| **Devstral** | `devstral-medium-latest` | $0.50 | $1.50 | 128K | 8K | text, tool-use |

Codestral has the largest context window (256K) of any Mistral model, optimized for code generation and analysis. Devstral is tuned for development workflows.

**Note**: Prices are approximate and may vary. Check [mistral.ai/technology/#pricing](https://mistral.ai/technology/#pricing) for current rates.


## Model Details

### mistral-large-latest (Mistral Large)

- **Context**: 128K tokens
- **Max Output**: 8K tokens
- **Capabilities**: text, tool-use
- **Cost**: $2.00/1M input, $6.00/1M output
- **Best For**: Complex tasks requiring strong general reasoning and tool use

**Example Use Cases**:
- Multi-step tool orchestration
- Long-context document analysis (up to 128K)
- High-quality text generation


### mistral-small-latest (Mistral Small)

- **Context**: 32K tokens
- **Max Output**: 8K tokens
- **Capabilities**: text, tool-use
- **Cost**: $0.10/1M input, $0.30/1M output
- **Best For**: High-volume, cost-sensitive workloads

**Example Use Cases**:
- Simple classification and extraction
- High-volume production workloads
- Development and testing (lowest cost)


### magistral-medium-latest (Magistral Medium)

- **Context**: 128K tokens
- **Max Output**: 131K tokens
- **Capabilities**: text, tool-use, thinking (reasoning)
- **Cost**: $2.00/1M input, $8.00/1M output
- **IsReasoning**: true

**Example Use Cases**:
- Complex multi-step reasoning
- Long-form generation (131K output window)
- Tasks requiring chain-of-thought traces


### magistral-small-latest (Magistral Small)

- **Context**: 128K tokens
- **Max Output**: 131K tokens
- **Capabilities**: text, tool-use, thinking (reasoning)
- **Cost**: $0.50/1M input, $1.50/1M output
- **IsReasoning**: true

**Example Use Cases**:
- Cost-effective reasoning tasks
- Moderate-complexity analysis with reasoning traces
- Development and testing of reasoning workflows


### codestral-latest (Codestral)

- **Context**: 256K tokens
- **Max Output**: 8K tokens
- **Capabilities**: text, tool-use
- **Cost**: $0.30/1M input, $0.90/1M output
- **Best For**: Code generation and analysis

**Example Use Cases**:
- Code generation and completion
- Large codebase analysis (256K context)
- Code review and refactoring suggestions


### devstral-medium-latest (Devstral)

- **Context**: 128K tokens
- **Max Output**: 8K tokens
- **Capabilities**: text, tool-use
- **Cost**: $0.50/1M input, $1.50/1M output
- **Best For**: Development workflows and agent-driven coding

**Example Use Cases**:
- Agentic coding workflows with tool use
- Development assistant tasks
- Code-aware document generation


## Request and Response Format

### Chat Request

```go
import (
    "context"
    "github.com/teradata-labs/loom/pkg/llm/types"
)

ctx := context.Background()
messages := []types.Message{
    {
        Role:    "system",
        Content: "Answer programming questions with clear, concise Go code examples.",
    },
    {
        Role:    "user",
        Content: "Explain Go interfaces.",
    },
}

response, err := client.Chat(ctx, messages, nil)
if err != nil {
    log.Fatalf("Chat failed: %v", err)
}

fmt.Printf("Response: %s\n", response.Content)
fmt.Printf("Cost: $%.6f\n", response.Usage.CostUSD)
```

**HTTP Equivalent** (internal):

```http
POST /v1/chat/completions HTTP/1.1
Host: api.mistral.ai
Authorization: Bearer YOUR_API_KEY
Content-Type: application/json

{
  "model": "mistral-large-latest",
  "messages": [
    {"role": "system", "content": "Answer programming questions with clear, concise Go code examples."},
    {"role": "user", "content": "Explain Go interfaces."}
  ],
  "max_tokens": 4096,
  "temperature": 1.0
}
```


### Chat Response

```go
type LLMResponse struct {
    Content    string                 // Assistant response
    ToolCalls  []ToolCall             // Tool calls (if any)
    StopReason string                 // Why the LLM stopped (e.g., "end_turn", "tool_use")
    Usage      Usage                  // Token counts and cost
    Metadata   map[string]interface{} // Provider metadata
    Thinking   string                 // Internal reasoning (for models with extended thinking)
}

type Usage struct {
    InputTokens              int
    OutputTokens             int
    TotalTokens              int
    CostUSD                  float64
    CacheReadInputTokens     int // Tokens served from prompt cache
    CacheCreationInputTokens int // Tokens written to prompt cache
}
```

**Example**:

```go
response := &types.LLMResponse{
    Content: "In Go, an interface is a type that specifies a set of method signatures...",
    Usage: types.Usage{
        InputTokens:  35,
        OutputTokens: 245,
        TotalTokens:  280,
        CostUSD:      0.001540,  // $2/1M input + $6/1M output (mistral-large)
    },
    Metadata: map[string]interface{}{
        "provider": "mistral",
    },
}
```

**Expected Output**:

```
Response: In Go, an interface is a type that specifies a set of method signatures...
Tokens: 35 input, 245 output (280 total)
Cost: $0.001540
```


## Cost Tracking

The Mistral provider automatically calculates costs based on token usage:

```go
response, err := agent.Chat(ctx, "Analyze this code...")
if err != nil {
    return err
}

// Access usage information
fmt.Printf("Input tokens: %d\n", response.Usage.InputTokens)
fmt.Printf("Output tokens: %d\n", response.Usage.OutputTokens)
fmt.Printf("Total tokens: %d\n", response.Usage.TotalTokens)
fmt.Printf("Cost: $%.6f\n", response.Usage.CostUSD)
```

**Expected Output**:

```
Input tokens: 1250
Output tokens: 3420
Total tokens: 4670
Cost: $0.046080
```


### Cost Calculation Formula

```
Cost = (InputTokens / 1,000,000 * InputPrice) + (OutputTokens / 1,000,000 * OutputPrice)
```


### Example Costs by Model

**mistral-large-latest** ($2.00/$6.00 per 1M tokens):
- Simple query (100 input, 200 output): $0.001400
- Medium task (1000 input, 2000 output): $0.014000
- Large task (10000 input, 20000 output): $0.140000

**mistral-small-latest** ($0.10/$0.30 per 1M tokens):
- Simple query (100 input, 200 output): $0.000070
- Medium task (1000 input, 2000 output): $0.000700
- Large task (10000 input, 20000 output): $0.007000

**magistral-medium-latest** ($2.00/$8.00 per 1M tokens):
- Simple query (100 input, 200 output): $0.001800
- Medium task (1000 input, 2000 output): $0.018000
- Large task (10000 input, 20000 output): $0.180000

**codestral-latest** ($0.30/$0.90 per 1M tokens):
- Simple query (100 input, 200 output): $0.000210
- Medium task (1000 input, 2000 output): $0.002100
- Large task (10000 input, 20000 output): $0.021000


## Tool Calling Support

The Mistral provider supports function calling via the OpenAI-compatible API wrapper:

```go
import "github.com/teradata-labs/loom/pkg/shuttle"

// Define custom tool
type WeatherTool struct{}

func (t *WeatherTool) Name() string { return "get_weather" }
func (t *WeatherTool) Description() string {
    return "Get current weather for a location"
}
func (t *WeatherTool) InputSchema() *shuttle.JSONSchema {
    return &shuttle.JSONSchema{
        Type: "object",
        Properties: map[string]*shuttle.JSONSchema{
            "location": {
                Type:        "string",
                Description: "City name",
            },
        },
        Required: []string{"location"},
    }
}
func (t *WeatherTool) Execute(ctx context.Context, input map[string]interface{}) (*shuttle.Result, error) {
    return &shuttle.Result{
        Success: true,
        Data:    map[string]interface{}{"temp": 72, "condition": "sunny"},
    }, nil
}
func (t *WeatherTool) Backend() string { return "" }

// Use tool with agent
tools := []shuttle.Tool{&WeatherTool{}}
response, err := client.Chat(ctx, messages, tools)
if err != nil {
    return err
}

// Handle tool calls
for _, toolCall := range response.ToolCalls {
    fmt.Printf("Tool: %s\n", toolCall.Name)
    fmt.Printf("Arguments: %v\n", toolCall.Input)
}
```

**Expected Output**:

```
Tool: get_weather
Arguments: map[location:San Francisco]
```


### Advanced Tool Features

Mistral supports advanced tool calling features:

1. **Parallel Tool Calls**: Multiple tools executed simultaneously
   ```go
   // Mistral can call get_weather AND get_time in parallel
   response.ToolCalls // Contains multiple tool calls
   ```

2. **Tool Choice Mode**:
   - `auto`: Let model decide when to use tools (default, hardcoded in Loom's OpenAI wrapper)
   - Mistral's API also supports `none`, `any`, and `required`, but Loom does not currently expose tool choice configuration

3. **Tool Result Handling**: Tool results feed back into conversation
   ```go
   // Execute tools
   for _, tc := range response.ToolCalls {
       result := executeTool(tc)
       // Add tool result to conversation
       messages = append(messages, toolResultMessage(tc, result))
   }

   // Continue conversation with tool results
   response, err = client.Chat(ctx, messages, tools)
   ```


## Error Handling

Common errors and solutions:

```go
response, err := client.Chat(ctx, messages, tools)
if err != nil {
    // Check error type
    switch {
    case strings.Contains(err.Error(), "401"):
        log.Error("Invalid API key")
    case strings.Contains(err.Error(), "429"):
        log.Error("Rate limit exceeded - implement backoff")
    case strings.Contains(err.Error(), "500"), strings.Contains(err.Error(), "503"):
        log.Error("Mistral service error - retry with backoff")
    default:
        log.Errorf("Unknown error: %v", err)
    }
    return err
}
```

**Expected Error Output**:

```
// Invalid API key
Error: Mistral API error (401): Invalid API key

// Rate limit
Error: Mistral API error (429): Rate limit exceeded. Please retry after 60 seconds.

// Model not found
Error: Mistral API error (404): The model 'invalid-model' does not exist

// Service unavailable
Error: Mistral API error (503): Service temporarily unavailable
```


## Error Codes

### ERR_INVALID_API_KEY

**Code**: `invalid_api_key`
**HTTP Status**: 401 Unauthorized
**gRPC Code**: `UNAUTHENTICATED`

**Cause**: API key is missing, invalid, or expired.

**Example**:
```
Error: Mistral API error (401): Invalid API key
```

**Resolution**:
1. Verify API key from [console.mistral.ai](https://console.mistral.ai/api-keys/)
2. Check for typos or extra whitespace
3. Ensure key hasn't been deleted or rotated
4. Create new API key if needed

**Retry behavior**: Not retryable (fix API key first)


### ERR_INSUFFICIENT_CREDITS

**Code**: `insufficient_credits`
**HTTP Status**: 402 Payment Required
**gRPC Code**: `FAILED_PRECONDITION`

**Cause**: Account has insufficient credits to complete request.

**Example**:
```
Error: Mistral API error (402): Insufficient credits. Please add credits to your account.
```

**Resolution**:
1. Check balance at [console.mistral.ai](https://console.mistral.ai/billing/)
2. Add credits to your account
3. Upgrade to paid plan if on free tier
4. Monitor credit usage to avoid future issues

**Retry behavior**: Not retryable until credits added


### ERR_MODEL_NOT_FOUND

**Code**: `model_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Specified model doesn't exist or you don't have access.

**Example**:
```
Error: Mistral API error (404): The model 'invalid-model' does not exist
```

**Resolution**:
1. Verify model ID from available models list
2. Check for typos in model name
3. Ensure you have access to commercial models (if using paid model)
4. Use `mistral-large-latest` as default

**Retry behavior**: Not retryable (fix model name)

**Valid Model IDs**:
- General: `mistral-large-latest`, `mistral-small-latest`
- Reasoning: `magistral-medium-latest`, `magistral-small-latest`
- Code: `codestral-latest`, `devstral-medium-latest`


### ERR_RATE_LIMIT

**Code**: `rate_limit_exceeded`
**HTTP Status**: 429 Too Many Requests
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Exceeded rate limits for your account tier.

**Example**:
```
Error: Mistral API error (429): Rate limit exceeded. Please retry after 60 seconds.
```

**Resolution**:
1. **Immediate**: Implement exponential backoff (retry after delay)
2. **Short-term**: Reduce request rate
3. **Long-term**: Upgrade account tier for higher limits

**Retry behavior**: Retryable with exponential backoff (wait time specified in error)

**Example Retry Logic**:

```go
import "time"

func chatWithRetry(client *mistral.Client, ctx context.Context, messages []types.Message) (*types.LLMResponse, error) {
    maxRetries := 5
    baseDelay := 2 * time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        resp, err := client.Chat(ctx, messages, nil)
        if err == nil {
            return resp, nil
        }

        // Check if rate limit error
        if !strings.Contains(err.Error(), "429") {
            return nil, err // Non-retryable error
        }

        // Exponential backoff
        delay := baseDelay * time.Duration(1<<uint(attempt))
        log.Printf("Rate limit hit, retrying in %v (attempt %d/%d)", delay, attempt+1, maxRetries)
        time.Sleep(delay)
    }

    return nil, fmt.Errorf("max retries exceeded")
}
```

**Expected Output**:

```
Rate limit hit, retrying in 2s (attempt 1/5)
Rate limit hit, retrying in 4s (attempt 2/5)
Success: received response after 2 retries
```


### ERR_INVALID_REQUEST

**Code**: `invalid_request`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Request parameters are invalid (e.g., temperature out of range, invalid tool schema).

**Example**:
```
Error: Mistral API error (400): 'temperature' must be between 0 and 2
```

**Resolution**:
1. Validate temperature: 0.0-2.0
2. Validate max_tokens: 1-32768
3. Verify tool schemas match OpenAI format
4. Check message format (required fields: role, content)

**Retry behavior**: Not retryable (fix request parameters)


### ERR_SERVICE_UNAVAILABLE

**Code**: `service_unavailable`
**HTTP Status**: 500 Internal Server Error / 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Mistral AI service is experiencing issues.

**Example**:
```
Error: Mistral API error (503): Service temporarily unavailable. Please try again later.
```

**Resolution**:
1. **Immediate**: Retry with exponential backoff
2. **Check status**: [status.mistral.ai](https://status.mistral.ai/) (if available)
3. **Fallback**: Switch to alternate model or provider
4. **Monitor**: Set up alerting for service health

**Retry behavior**: Retryable with exponential backoff (transient error)


### ERR_TIMEOUT

**Code**: `timeout`
**HTTP Status**: 408 Request Timeout / Client-side timeout
**gRPC Code**: `DEADLINE_EXCEEDED`

**Cause**: Request exceeded configured timeout.

**Example**:
```
Error: HTTP request failed: context deadline exceeded
```

**Resolution**:
1. **Increase timeout**: `Timeout: 120 * time.Second`
2. **Reduce complexity**: Smaller prompts, fewer tools, lower max_tokens
3. **Check network**: Ensure stable connectivity
4. **Use smaller model**: Switch to faster model (e.g., mistral-small)

**Retry behavior**: Retryable with same request


### ERR_CONTEXT_LENGTH_EXCEEDED

**Code**: `context_length_exceeded`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Total tokens (prompt + completion) exceeds model's context window.

**Example**:
```
Error: Mistral API error (400): This model's maximum context length is 32768 tokens. However, you requested 35000 tokens.
```

**Resolution**:
1. Reduce prompt length (truncate conversation history)
2. Reduce max_tokens parameter
3. Switch to model with larger context (`codestral-latest` has 256K)
4. Implement sliding window for long conversations

**Retry behavior**: Not retryable until prompt reduced

**Context Limits**:
- `mistral-small-latest`: 32K tokens
- `mistral-large-latest`: 128K tokens
- `magistral-medium-latest`: 128K tokens
- `magistral-small-latest`: 128K tokens
- `codestral-latest`: 256K tokens
- `devstral-medium-latest`: 128K tokens


## Rate Limiting

Mistral AI applies rate limits based on account tier:

### Rate Limit Tiers

| Tier | Requests/Min | Tokens/Min | Use Case |
|------|--------------|------------|----------|
| **Free** | 5 | 10K | Testing, development |
| **Starter** | 60 | 500K | Small production |
| **Pro** | 120 | 2M | Production |
| **Enterprise** | Custom | Custom | High-volume production |

**Note**: Limits vary by model and account. Check your specific limits at [console.mistral.ai](https://console.mistral.ai/).


### Handling Rate Limits

**Option 1: Exponential Backoff** (shown in ERR_RATE_LIMIT section)

**Option 2: Client-Side Rate Limiting**

```go
import "github.com/teradata-labs/loom/pkg/llm"

// Configure client with rate limiter
client := mistral.NewClient(mistral.Config{
    APIKey:  apiKey,
    Model:   "mistral-large-latest",
    RateLimiterConfig: llm.RateLimiterConfig{
        Enabled:           true,
        RequestsPerSecond: 2.0,
        TokensPerMinute:   450000,  // 450K TPM (below 500K limit)
        BurstCapacity:     5,
        MinDelay:          200 * time.Millisecond,
        MaxRetries:        5,
        RetryBackoff:      time.Second,
        QueueTimeout:      5 * time.Minute,
    },
})
```

**Behavior**:
- Requests are automatically queued when approaching limit
- Prevents 429 errors before they occur
- Smooths request rate across time


## Testing

The Mistral provider has 72.0% test coverage:

```bash
# Run tests (fts5 tag required for all Loom packages)
cd /path/to/loom
go test -tags fts5 ./pkg/llm/mistral/

# With coverage (72.0%)
go test -tags fts5 -cover ./pkg/llm/mistral/

# With race detection (required per project policy)
go test -tags fts5 -race ./pkg/llm/mistral/

# Verbose output
go test -tags fts5 -v ./pkg/llm/mistral/
```

**Expected Output**:

```
=== RUN   TestNewClient
=== RUN   TestNewClient/default_config
=== RUN   TestNewClient/custom_config
--- PASS: TestNewClient (0.00s)
=== RUN   TestCalculateCost
--- PASS: TestCalculateCost (0.00s)
...
PASS
coverage: 72.0% of statements
ok  	github.com/teradata-labs/loom/pkg/llm/mistral	0.124s
```


### Integration Testing

Test against real Mistral API:

```go
func TestMistral_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    apiKey := os.Getenv("MISTRAL_API_KEY")
    if apiKey == "" {
        t.Skip("MISTRAL_API_KEY not set")
    }

    client := mistral.NewClient(mistral.Config{
        APIKey: apiKey,
        Model:  "mistral-small-latest",  // Use cheaper model for tests
    })

    ctx := context.Background()
    messages := []types.Message{
        {Role: "user", Content: "Say hello"},
    }

    resp, err := client.Chat(ctx, messages, nil)
    require.NoError(t, err)
    assert.NotEmpty(t, resp.Content)
    assert.Greater(t, resp.Usage.TotalTokens, 0)
    assert.Greater(t, resp.Usage.CostUSD, 0.0)
}
```

**Run Integration Tests**:

```bash
export MISTRAL_API_KEY="your-api-key"
go test -tags fts5 -v ./pkg/llm/mistral -run Integration
```


## Best Practices

### 1. Model Selection Strategy

```go
// High volume, cost-effective: Use small model
agent, err := builder.NewAgentBuilder().
    WithMistralLLMCustomModel(apiKey, "mistral-small-latest").
    Build()

// Complex tasks with tool use: Use large model
agent, err := builder.NewAgentBuilder().
    WithMistralLLM(apiKey).  // Defaults to mistral-large-latest
    Build()

// Reasoning tasks: Use Magistral
agent, err := builder.NewAgentBuilder().
    WithMistralLLMCustomModel(apiKey, "magistral-medium-latest").
    Build()

// Code generation: Use Codestral
agent, err := builder.NewAgentBuilder().
    WithMistralLLMCustomModel(apiKey, "codestral-latest").
    Build()
```

**Decision Tree**:
1. **Development/Testing**: → `mistral-small-latest` (cheapest at $0.10/$0.30)
2. **High-Volume Simple Tasks**: → `mistral-small-latest` (cost-effective)
3. **Complex Tasks + Tool Use**: → `mistral-large-latest` (best general quality)
4. **Complex Reasoning**: → `magistral-medium-latest` (reasoning traces, 131K output)
5. **Cost-Effective Reasoning**: → `magistral-small-latest` ($0.50/$1.50)
6. **Code Generation**: → `codestral-latest` (256K context, code-optimized)
7. **Agentic Coding**: → `devstral-medium-latest` (development workflows)


### 2. Cost Management

```go
import "go.uber.org/zap"

// Track costs per session
totalCost := 0.0

for _, turn := range conversation {
    response, err := agent.Chat(ctx, turn.Message)
    if err != nil {
        return err
    }

    totalCost += response.Usage.CostUSD

    logger.Info("Turn completed",
        zap.Float64("turn_cost", response.Usage.CostUSD),
        zap.Float64("total_cost", totalCost),
        zap.Int("input_tokens", response.Usage.InputTokens),
        zap.Int("output_tokens", response.Usage.OutputTokens),
    )

    // Warn if cost exceeds threshold
    if totalCost > 0.10 {
        logger.Warn("Session cost exceeds threshold",
            zap.Float64("total_cost", totalCost),
        )
    }
}
```


### 3. Error Handling with Retry

```go
import "time"

func chatWithRetryBackoff(client *mistral.Client, ctx context.Context, messages []types.Message) (*types.LLMResponse, error) {
    maxRetries := 5
    baseDelay := 1 * time.Second
    maxDelay := 32 * time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        resp, err := client.Chat(ctx, messages, nil)
        if err == nil {
            return resp, nil
        }

        // Determine if retryable
        isRetryable := strings.Contains(err.Error(), "429") || // Rate limit
                       strings.Contains(err.Error(), "500") || // Server error
                       strings.Contains(err.Error(), "503")    // Service unavailable

        if !isRetryable {
            return nil, err // Non-retryable error
        }

        // Calculate delay with exponential backoff and jitter
        delay := baseDelay * time.Duration(1<<uint(attempt))
        if delay > maxDelay {
            delay = maxDelay
        }
        jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
        totalDelay := delay + jitter

        log.Printf("Request failed (attempt %d/%d), retrying in %v: %v",
                   attempt+1, maxRetries, totalDelay, err)
        time.Sleep(totalDelay)
    }

    return nil, fmt.Errorf("max retries exceeded")
}
```


### 4. Secure API Key Management

```go
// ❌ Bad - Hardcoded
apiKey := "abc123..."

// ✅ Good - Environment variable
apiKey := os.Getenv("MISTRAL_API_KEY")
if apiKey == "" {
    return fmt.Errorf("MISTRAL_API_KEY not set")
}

// ✅ Better - Secure vault (e.g., HashiCorp Vault)
apiKey, err := vault.GetSecret("mistral-api-key")
if err != nil {
    return fmt.Errorf("failed to get API key: %w", err)
}

// ✅ Best - Loom keyring integration
// looms config set-key mistral_api_key
```


### 5. Token Optimization

```go
// Set appropriate max_tokens for your use case
config := mistral.Config{
    APIKey:    apiKey,
    MaxTokens: 512,  // For short responses (e.g., classification)
}

// For longer responses
config.MaxTokens = 2048

// For very long responses (e.g., code generation)
config.MaxTokens = 8192

// Never exceed model's context window
// mistral-large: 128K total (prompt + completion)
```


### 6. Monitor Usage

```go
import "go.uber.org/zap"

logger, _ := zap.NewProduction()

// Log before request
logger.Info("Mistral request",
    zap.String("model", "mistral-large-latest"),
    zap.Int("input_message_count", len(messages)),
)

startTime := time.Now()
response, err := client.Chat(ctx, messages, tools)

// Log after request
logger.Info("Mistral response",
    zap.Duration("latency", time.Since(startTime)),
    zap.Int("input_tokens", response.Usage.InputTokens),
    zap.Int("output_tokens", response.Usage.OutputTokens),
    zap.Float64("cost_usd", response.Usage.CostUSD),
    zap.Int("tool_calls", len(response.ToolCalls)),
)
```

**Expected Log Output**:

```json
{
  "level": "info",
  "msg": "Mistral request",
  "model": "mistral-large-latest",
  "input_message_count": 3
}
{
  "level": "info",
  "msg": "Mistral response",
  "latency": "1.234s",
  "input_tokens": 145,
  "output_tokens": 423,
  "cost_usd": 0.005660,
  "tool_calls": 2
}
```


## OpenAI Compatibility

Mistral AI uses an OpenAI-compatible API, so migration requires minimal code changes:

### API Compatibility

| Feature | OpenAI | Mistral | Compatible? |
|---------|--------|---------|-------------|
| **Message Format** | `[{role, content}]` | `[{role, content}]` | ✅ Yes |
| **Tool Calling** | Function calling | Function calling | ✅ Yes |
| **Streaming** | SSE | SSE | ✅ Yes (via OpenAI-compatible delegation) |
| **Temperature** | 0-2 | 0-2 | ✅ Yes |
| **Max Tokens** | Model-specific | Model-specific | ✅ Yes |
| **System Messages** | Supported | Supported | ✅ Yes |


### Migration Example

```go
// Before: OpenAI
agent, err := builder.NewAgentBuilder().
    WithOpenAILLM(openaiKey).
    Build()

// After: Mistral (drop-in replacement)
agent, err := builder.NewAgentBuilder().
    WithMistralLLM(mistralKey).
    Build()
```

The message format, tool calling, and response structure are identical.


## Comparison with Other Providers

| Feature | Mistral | OpenAI | Anthropic | Ollama |
|---------|---------|--------|-----------|--------|
| **API Compatibility** | OpenAI-like | Native | Native | OpenAI-like |
| **Tool Calling** | Via OpenAI wrapper | Native | Native | Limited |
| **Cost** | $0.10-$8.00/M tokens | $0.10-$40/M tokens | $1-$75/M tokens | Free (local) |
| **Reasoning Models** | Yes (Magistral) | Yes (o3/o4) | Yes (thinking) | Yes (DeepSeek R1) |
| **Context Window** | 32K-256K | 200K-1M | 200K-1M | Model-dependent |
| **Privacy** | API call | API call | API call | Full (local) |
| **European Provider** | Yes (France) | No (US) | No (US) | N/A |


## Limitations

1. **Cost Calculation Drift** ⚠️: The `calculateCost()` function in `client.go` has hardcoded prices from 2024-11 that do not match current model catalog prices. Newer models (Magistral, Codestral, Devstral) are missing from `calculateCost()` and fall back to default ($4.00/$12.00) pricing. Cost values in `response.Usage.CostUSD` may be inaccurate at runtime.

2. **Rate Limit Handling**: No built-in automatic retry in the Mistral client itself
   - **Workaround**: Use `RateLimiterConfig` (shared across all providers) or implement exponential backoff manually (see Best Practices)

3. **Tool Choice Not Configurable**: Loom hardcodes `tool_choice: "auto"` when tools are present. Mistral's API supports `none`, `any`, and `required` modes, but these are not exposed through Loom's configuration.

4. **Model Availability**: Some models require additional access approval
   - **Resolution**: Request access at [console.mistral.ai](https://console.mistral.ai/)


## Migration from OpenAI

Migrating from OpenAI to Mistral requires minimal changes due to API compatibility:

### Before (OpenAI)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(openaiKey).
    Build()
```


### After (Mistral)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLM(mistralKey).  // Defaults to mistral-large-latest
    Build()

// Or with custom model
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithMistralLLMCustomModel(mistralKey, "mistral-small-latest").
    Build()
```


### Key Differences

| Aspect | OpenAI | Mistral |
|--------|--------|---------|
| **Endpoint** | `api.openai.com` | `api.mistral.ai` |
| **API Key Source** | platform.openai.com | console.mistral.ai |
| **Default Model** | `gpt-4.1` | `mistral-large-latest` |
| **Reasoning Models** | o3, o4-mini | Magistral Medium/Small |
| **Pricing** | $0.10-$40/1M | $0.10-$8.00/1M |
| **Context Window** | 200K-1M | 32K-256K |
| **Provider Location** | US | France (EU) |


### Migration Checklist

- [ ] Get Mistral API key from [console.mistral.ai](https://console.mistral.ai/)
- [ ] Update code to use `WithMistralLLM()` instead of `WithOpenAILLM()`
- [ ] Choose appropriate model (see Model Selection Strategy)
- [ ] Update cost tracking/budgets (different pricing)
- [ ] Test tool calling (API-compatible but verify behavior)
- [ ] Update error handling (similar but Mistral-specific messages)
- [ ] Monitor performance (Mistral may have different latencies)
- [ ] Update documentation/configs to reflect new provider

The message format, tool calling, and response structure remain identical.


## See Also

### LLM Provider Documentation
- [LLM Provider Overview](./llm-providers.md) - All supported LLM providers
- [Anthropic Integration](./llm-anthropic.md) - Claude models (direct API)
- [OpenAI Integration](./llm-openai.md) - Similar OpenAI-compatible API structure
- [Gemini Integration](./llm-gemini.md) - Google Gemini models
- [Azure OpenAI Integration](./llm-azure-openai.md) - Azure-hosted OpenAI models
- [Bedrock Integration](./llm-bedrock.md) - AWS Bedrock (Claude models)
- [Ollama Integration](./llm-ollama.md) - Local/self-hosted models
- [HuggingFace Integration](./llm-huggingface.md) - HuggingFace Inference API

### Integration Guides
- [Agent Configuration](./agent-configuration.md) - Complete agent setup
- [Quickstart Guide](../guides/quickstart.md) - Getting started with Loom

### External Resources
- [Mistral AI Documentation](https://docs.mistral.ai/)
- [Mistral AI Pricing](https://mistral.ai/technology/#pricing)
- [Mistral API Console](https://console.mistral.ai/)
- [Mistral Open Source Models](https://mistral.ai/technology/#models)
