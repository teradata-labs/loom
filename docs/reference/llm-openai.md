
# OpenAI Integration Reference

**Version**: v1.2.0

Technical reference for integrating Loom with OpenAI's API.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Features](#features)
- [Configuration](#configuration)
- [Model Support and Pricing](#model-support-and-pricing)
- [Reasoning Models](#reasoning-models)
- [Request and Response Format](#request-and-response-format)
- [Cost Tracking](#cost-tracking)
- [Tool Calling Support](#tool-calling-support)
- [Vision Support](#vision-support)
- [Error Handling](#error-handling)
- [Error Codes](#error-codes)
- [Rate Limiting](#rate-limiting)
- [Testing](#testing)
- [Best Practices](#best-practices)
- [Comparison with Other Providers](#comparison-with-other-providers)
- [Limitations](#limitations)
- [Migration to Azure OpenAI](#migration-to-azure-openai)
- [See Also](#see-also)


## Quick Reference

### Configuration Summary

```go
// Builder API (Default Model: gpt-4.1)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(apiKey).
    Build()

// Builder API (Custom Model)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLMCustomModel(apiKey, "gpt-5").
    Build()

// Direct Client
client := openai.NewClient(openai.Config{
    APIKey:      "sk-proj-...",              // Required
    Model:       "gpt-4.1",                  // Default: gpt-4.1
    Endpoint:    "https://api.openai.com/v1/chat/completions", // Default
    MaxTokens:   4096,                       // Default: 4096
    Temperature: 1.0,                        // Default: 1.0
    Timeout:     60 * time.Second,           // Default: 60s
})
```

### Available Models

#### Standard Models

| Model | ID | Input Cost | Output Cost | Context | Output | Features |
|-------|----|-----------:|------------:|---------|--------|----------|
| **GPT-5** | `gpt-5` | $2.50/1M | $10.00/1M | 272K | 128K | Reasoning, vision, tools |
| **GPT-5 Mini** | `gpt-5-mini` | $0.40/1M | $1.60/1M | 272K | 128K | Reasoning, vision, tools |
| **GPT-4.1** | `gpt-4.1` | $2.00/1M | $8.00/1M | 1M | 32K | Vision, tools (default) |
| **GPT-4.1 Mini** | `gpt-4.1-mini` | $0.40/1M | $1.60/1M | 1M | 32K | Vision, tools, cost-effective |
| **GPT-4.1 Nano** | `gpt-4.1-nano` | $0.10/1M | $0.40/1M | 1M | 32K | Vision, tools, budget |

#### Reasoning Models

| Model | ID | Input Cost | Output Cost | Context | Output | Best For |
|-------|----|-----------:|------------:|---------|--------|----------|
| **o3** | `o3` | $10.00/1M | $40.00/1M | 200K | 100K | Complex reasoning, vision, tools |
| **o3-mini** | `o3-mini` | $1.10/1M | $4.40/1M | 200K | 100K | Reasoning, tools, budget (no vision) |
| **o4-mini** | `o4-mini` | $1.10/1M | $4.40/1M | 200K | 100K | Reasoning, vision, tools, cost-effective |

*Prices as of March 2026. Check [openai.com/pricing](https://openai.com/pricing) for current rates.*

### Common Commands

```bash
# Set API key environment variable
export OPENAI_API_KEY="sk-proj-..."

# Get API key from platform
open https://platform.openai.com/api-keys

# Test connection
curl https://api.openai.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -d '{"model": "gpt-4.1-nano", "messages": [{"role": "user", "content": "Hello"}]}'
```

### Configuration Parameters

| Parameter | Type | Required | Default | Constraints |
|-----------|------|----------|---------|-------------|
| `APIKey` | `string` | Yes | - | Format: `sk-proj-...` or `sk-...` |
| `Model` | `string` | No | `gpt-4.1` | See available models |
| `Endpoint` | `string` | No | `https://api.openai.com/v1/chat/completions` | Valid HTTPS URL |
| `MaxTokens` | `int` | No | `4096` | 1-128000 (model dependent) |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 |
| `Timeout` | `duration` | No | `60s` | 1s-10m |


## Overview

OpenAI provides access to GPT-5, GPT-4.1, and reasoning models through their API. The integration offers:
- Native tool calling support (function calling)
- Multiple model options (GPT-5, GPT-5 Mini, GPT-4.1, GPT-4.1 Mini, GPT-4.1 Nano)
- Reasoning models (o3, o3-mini, o4-mini)
- Vision support (image input)
- Cost tracking for all models

**Implementation**: `pkg/llm/openai/client.go`
**Types**: `pkg/llm/openai/types.go`
**Test Coverage**: 48.2% (801 lines of tests)
**API Endpoint**: `https://api.openai.com/v1/chat/completions`
**Interfaces**: `LLMProvider` and `StreamingLLMProvider` compliance


## Prerequisites

1. **OpenAI API Key**: Get your API key from [platform.openai.com](https://platform.openai.com/api-keys)
2. **API Access**: Ensure your account has API access enabled
3. **Credits**: OpenAI uses a prepaid credit system

**Getting Your API Key**:

1. Visit [platform.openai.com](https://platform.openai.com/)
2. Sign up or log in
3. Navigate to "API Keys" in settings
4. Click "Create new secret key"
5. Copy the key (shown only once)
6. Add credits to your account

**Verification**:

```bash
# Test API key
curl https://api.openai.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4.1-nano",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**Expected Output**:

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1677652288,
  "model": "gpt-4.1-nano",
  "choices": [{
    "message": {"role": "assistant", "content": "Hello! How can I assist you today?"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 9, "completion_tokens": 9, "total_tokens": 18}
}
```


## Features

### Implemented ✅

- Full `LLMProvider` and `StreamingLLMProvider` interface implementation (`pkg/llm/openai/client.go`)
- Message conversion (system, user, assistant, tool roles)
- Tool calling with JSON schema conversion (function calling)
- Cost calculation for legacy models (gpt-4o, gpt-4o-mini, gpt-4-turbo, gpt-4, gpt-3.5-turbo, o1-preview, o1-mini)
- Custom model selection
- Temperature and max tokens configuration
- `max_completion_tokens` for modern models, `max_tokens` for legacy models
- Vision support (image input via ContentBlocks)
- Multi-modal content (text + images)
- 48.2% test coverage (801 lines of tests)
- Rate limiting (configurable via `llm.RateLimiterConfig`)
- Streaming support (SSE-based token streaming via `ChatStream`)
- Tool name sanitization (MCP namespace colon replacement)

### In Progress ⚠️

- Cost calculation pricing data for newer models (gpt-4.1, gpt-5, o3, o4-mini) -- currently falls back to gpt-4o pricing

### Planned 📋

- Automatic retry with exponential backoff
- Circuit breaker integration


## Configuration

### Using Builder API (Programmatic)

The OpenAI provider is currently available through the Builder API for programmatic agent creation:

```go
import "github.com/teradata-labs/loom/pkg/builder"

// Option 1: Using builder with default model (gpt-4.1)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(apiKey).
    Build()

if err != nil {
    log.Fatalf("Failed to build agent: %v", err)
}

// Option 2: Using builder with custom model
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLMCustomModel(apiKey, "gpt-5").
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
import (
    "github.com/teradata-labs/loom/pkg/llm/openai"
    llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// Create client with custom configuration
client := openai.NewClient(openai.Config{
    APIKey:      "sk-proj-...",
    Model:       "gpt-4.1",
    Endpoint:    "https://api.openai.com/v1/chat/completions", // Optional
    MaxTokens:   8192,        // Increase for longer responses
    Temperature: 0.7,         // Lower for more deterministic
    Timeout:     120 * time.Second, // Longer timeout for complex tasks
})

// Use as LLMProvider
var provider llmtypes.LLMProvider = client

// Get provider info
fmt.Printf("Provider: %s\n", provider.Name())  // "openai"
fmt.Printf("Model: %s\n", provider.Model())    // "gpt-4.1"
```

**Expected Output**:

```
Provider: openai
Model: gpt-4.1
```


### Environment Variables

The OpenAI client reads API keys from environment variables. The model and endpoint can also be overridden via environment variables:

| Variable | Purpose | Checked By |
|----------|---------|------------|
| `OPENAI_API_KEY` | API key (must be passed explicitly) | User code |
| `OPENAI_DEFAULT_MODEL` | Override default model | `NewClient()` (when `Config.Model` is empty) |
| `LOOM_LLM_OPENAI_MODEL` | Override default model (Loom-prefixed) | `NewClient()` and `WithOpenAILLM()` builder |
| `OPENAI_API_ENDPOINT` | Override API endpoint | `NewClient()` (when `Config.Endpoint` is empty) |
| `LOOM_LLM_OPENAI_ENDPOINT` | Override API endpoint (Loom-prefixed) | `NewClient()` (when `Config.Endpoint` is empty) |

**Priority**: Loom-prefixed variables (`LOOM_LLM_OPENAI_*`) are checked after standard OpenAI variables. First non-empty value wins.

```go
apiKey := os.Getenv("OPENAI_API_KEY")
if apiKey == "" {
    log.Fatal("OPENAI_API_KEY environment variable required")
}

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(apiKey).
    Build()
```

**Configuration Parameters Details**:

| Parameter | Type | Required | Default | Range | Description |
|-----------|------|----------|---------|-------|-------------|
| `APIKey` | `string` | Yes | - | - | OpenAI API key (sk-proj- or sk-) |
| `Model` | `string` | No | `gpt-4.1` | See models table | Model identifier |
| `Endpoint` | `string` | No | `https://api.openai.com/v1/chat/completions` | Valid HTTPS URL | API endpoint |
| `MaxTokens` | `int` | No | `4096` | 1-128000 | Maximum tokens in response |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 | Sampling temperature |
| `Timeout` | `duration` | No | `60s` | 1s-10m | Request timeout |


## Model Support and Pricing

Pricing as of March 2026 (per million tokens):

### Standard Models

| Model | ID | Input Cost | Output Cost | Context | Output | Tool Calling | Vision | Best For |
|-------|----|-----------:|------------:|---------|--------|--------------|--------|----------|
| **GPT-5** | `gpt-5` | $2.50 | $10.00 | 272K | 128K | ✅ | ✅ | Reasoning + general tasks |
| **GPT-5 Mini** | `gpt-5-mini` | $0.40 | $1.60 | 272K | 128K | ✅ | ✅ | Cost-effective reasoning |
| **GPT-4.1** | `gpt-4.1` | $2.00 | $8.00 | 1M | 32K | ✅ | ✅ | General tasks, large context (default) |
| **GPT-4.1 Mini** | `gpt-4.1-mini` | $0.40 | $1.60 | 1M | 32K | ✅ | ✅ | Cost-effective, large context |
| **GPT-4.1 Nano** | `gpt-4.1-nano` | $0.10 | $0.40 | 1M | 32K | ✅ | ✅ | Budget, large context |

### Model Details

#### GPT-5

- **Context**: 272K tokens
- **Max Output**: 128K tokens
- **Features**: Reasoning, text, vision, tool calling
- **Best For**: Tasks requiring both reasoning and general capability

**Use Cases**:
- Complex multi-step reasoning with tool use
- Code generation and debugging
- Research and technical writing
- Data analysis with tool calling


#### GPT-5 Mini

- **Context**: 272K tokens
- **Max Output**: 128K tokens
- **Features**: Reasoning, text, vision, tool calling
- **Best For**: Cost-effective reasoning tasks

**Use Cases**:
- Classification and categorization with reasoning
- Code generation with reasoning
- Content creation
- High-throughput applications needing reasoning


#### GPT-4.1

- **Context**: 1M tokens
- **Max Output**: 32K tokens
- **Features**: Text, vision, tool calling
- **Best For**: Large context tasks, balanced cost/quality (default model)

**Use Cases**:
- Long document analysis (up to 1M context)
- Code review of large codebases
- Customer support chatbots
- General-purpose tasks


#### GPT-4.1 Mini

- **Context**: 1M tokens
- **Max Output**: 32K tokens
- **Features**: Text, vision, tool calling
- **Best For**: Cost-effective large context tasks

**Use Cases**:
- High-volume document processing
- Simple question answering over large contexts
- Text extraction and summarization
- Budget-conscious applications


#### GPT-4.1 Nano

- **Context**: 1M tokens
- **Max Output**: 32K tokens
- **Features**: Text, vision, tool calling
- **Best For**: Budget tasks with large context

**Use Cases**:
- Classification and categorization
- Simple text extraction
- High-throughput, low-cost processing
- Rapid prototyping


## Reasoning Models

OpenAI's o-series models are optimized for complex reasoning tasks.

### Reasoning Models Table

| Model | ID | Input Cost | Output Cost | Context | Output | Tool Calling | Vision | Best For |
|-------|----|-----------:|------------:|---------|--------|--------------|--------|----------|
| **o3** | `o3` | $10.00 | $40.00 | 200K | 100K | ✅ | ✅ | Complex reasoning, STEM, coding |
| **o3-mini** | `o3-mini` | $1.10 | $4.40 | 200K | 100K | ✅ | ❌ | Budget reasoning, tools |
| **o4-mini** | `o4-mini` | $1.10 | $4.40 | 200K | 100K | ✅ | ✅ | Reasoning with vision, cost-effective |

### o3

- **Context**: 200K tokens
- **Max Output**: 100K tokens
- **Cost**: $10.00/1M input, $40.00/1M output
- **Features**: Reasoning, vision, tool calling
- **Best For**: Complex reasoning, STEM, coding

**Use Cases**:
- Mathematical proofs and problem-solving
- Complex code debugging
- Scientific research analysis
- Multi-step logical reasoning with tool use


### o3-mini

- **Context**: 200K tokens
- **Max Output**: 100K tokens
- **Cost**: $1.10/1M input, $4.40/1M output
- **Features**: Reasoning, tool calling (no vision)
- **Best For**: Budget reasoning tasks

**Use Cases**:
- STEM education and tutoring
- Code generation with reasoning
- Logical puzzle solving
- Cost-effective reasoning workflows

**Note**: o3-mini does not support vision input.


### o4-mini

- **Context**: 200K tokens
- **Max Output**: 100K tokens
- **Cost**: $1.10/1M input, $4.40/1M output
- **Features**: Reasoning, vision, tool calling
- **Best For**: Cost-effective reasoning with vision

**Use Cases**:
- Image analysis with reasoning
- Multi-modal reasoning tasks
- Code generation with visual context
- Cost-effective alternative to o3


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
        Content: "You are a helpful coding assistant.",
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
Host: api.openai.com
Authorization: Bearer sk-proj-...
Content-Type: application/json

{
  "model": "gpt-4.1",
  "messages": [
    {"role": "system", "content": "You are a helpful coding assistant."},
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
    StopReason string                 // Why the LLM stopped ("end_turn", "max_tokens", "tool_use")
    Usage      Usage                  // Token counts and cost
    Metadata   map[string]interface{} // Provider metadata
    Thinking   string                 // Internal reasoning (for thinking-enabled models)
}

type Usage struct {
    InputTokens              int
    OutputTokens             int
    TotalTokens              int
    CostUSD                  float64
    CacheReadInputTokens     int     // Tokens served from prompt cache
    CacheCreationInputTokens int     // Tokens written to prompt cache
}
```

**Example**:

```go
response := &types.LLMResponse{
    Content: "In Go, an interface is a type that specifies a method set...",
    StopReason: "end_turn",
    Usage: types.Usage{
        InputTokens:  42,
        OutputTokens: 287,
        TotalTokens:  329,
        CostUSD:      0.002954,  // $2.00/1M input + $8.00/1M output (gpt-4.1)
    },
}
```

**Expected Output**:

```
Response: In Go, an interface is a type that specifies a method set...
Tokens: 42 input, 287 output (329 total)
Cost: $0.002954
```


## Cost Tracking

The OpenAI provider calculates costs based on token usage.

**Note**: The `calculateCost()` function currently has pricing data for: `gpt-4o`, `gpt-4o-mini`, `gpt-4-turbo`, `gpt-4`, `gpt-3.5-turbo`, `o1-preview`, `o1-mini`. Models not in this list (including `gpt-4.1`, `gpt-5`, `o3`, `o4-mini`) fall back to `gpt-4o` pricing ($2.50/$10.00 per 1M tokens). ⚠️ Pricing data for newer models needs to be added to `pkg/llm/openai/client.go`.

Usage example:

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
Input tokens: 1520
Output tokens: 3840
Total tokens: 5360
Cost: $0.042200
```


### Cost Calculation Formula

```
Cost = (InputTokens / 1,000,000 * InputPrice) + (OutputTokens / 1,000,000 * OutputPrice)
```


### Example Costs by Model

**GPT-5** ($2.50/$10.00 per 1M tokens):
- Simple query (100 input, 200 output): $0.002250
- Medium task (1000 input, 2000 output): $0.022500
- Large task (10000 input, 20000 output): $0.225000

**GPT-4.1** ($2.00/$8.00 per 1M tokens):
- Simple query (100 input, 200 output): $0.001800
- Medium task (1000 input, 2000 output): $0.018000
- Large task (10000 input, 20000 output): $0.180000

**GPT-4.1 Nano** ($0.10/$0.40 per 1M tokens):
- Simple query (100 input, 200 output): $0.000090
- Medium task (1000 input, 2000 output): $0.000900
- Large task (10000 input, 20000 output): $0.009000

**o3** ($10.00/$40.00 per 1M tokens):
- Simple query (100 input, 200 output): $0.009000
- Medium task (1000 input, 2000 output): $0.090000
- Large task (10000 input, 20000 output): $0.900000


## Tool Calling Support

The OpenAI provider supports tool calling (function calling):

```go
import "github.com/teradata-labs/loom/pkg/shuttle"

// Define custom tool implementing shuttle.Tool interface
type CalculatorTool struct{}

func (t *CalculatorTool) Name() string { return "calculator" }
func (t *CalculatorTool) Description() string {
    return "Perform mathematical calculations"
}
func (t *CalculatorTool) InputSchema() *shuttle.JSONSchema {
    return shuttle.NewObjectSchema(
        "Calculator parameters",
        map[string]*shuttle.JSONSchema{
            "expression": shuttle.NewStringSchema("Mathematical expression to evaluate"),
        },
        []string{"expression"},
    )
}
func (t *CalculatorTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
    // Implementation
    return &shuttle.Result{
        Success: true,
        Data:    map[string]interface{}{"result": 42},
    }, nil
}
func (t *CalculatorTool) Backend() string { return "" } // backend-agnostic

// Use tool with agent
tools := []shuttle.Tool{&CalculatorTool{}}
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
Tool: calculator
Arguments: map[expression:2+2]
```


### Tool Calling Features

1. **Parallel Tool Calls**: GPT-5 and GPT-4.1 can call multiple tools simultaneously
   ```go
   // OpenAI may call get_weather AND get_time in one response
   response.ToolCalls // Contains multiple tool calls
   ```

2. **Tool Choice Control**:
   - `auto`: Let model decide (default)
   - `none`: Never use tools
   - `required`: Must use tools

3. **Structured Outputs**: JSON mode for structured data (via internal request type)
   ```go
   // Force JSON output format (internal ChatCompletionRequest field)
   // ResponseFormat is map[string]interface{}, not a plain string
   req.ResponseFormat = map[string]interface{}{"type": "json_object"}
   ```


## Vision Support

GPT-5, GPT-5 Mini, GPT-4.1 family, o3, and o4-mini support image inputs:

```go
import "github.com/teradata-labs/loom/pkg/types"

// Create message with image (ContentBlock, ImageContent, ImageSource are in pkg/types)
messages := []types.Message{
    {
        Role: "user",
        ContentBlocks: []types.ContentBlock{
            {
                Type: "text",
                Text: "What's in this image?",
            },
            {
                Type: "image",
                Image: &types.ImageContent{
                    Type: "image",
                    Source: types.ImageSource{
                        Type:      "url",
                        MediaType: "image/jpeg",
                        URL:       "https://example.com/image.jpg",
                    },
                },
            },
        },
    },
}

response, err := client.Chat(ctx, messages, nil)
```

**Expected Output**:

```
Response: The image shows a cat sitting on a windowsill...
```


### Image Formats

**Supported Formats**:
- PNG
- JPEG
- WEBP
- Non-animated GIF

**Image Size Limits**:
- Maximum file size: 20MB
- Maximum dimensions: 2048x2048 pixels
- Images automatically resized if larger

**Image Input Methods**:
1. **URL**: Public HTTPS URL to image
2. **Base64**: Data URI with base64-encoded image

```go
// Base64 example
imageURL := "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEA..."
```

**Cost**: Images consume tokens based on size and detail level:
- Low detail: 85 tokens per image
- High detail: 85 + (170 * tiles) tokens


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
        log.Error("OpenAI service error - retry with backoff")
    default:
        log.Errorf("Unknown error: %v", err)
    }
    return err
}
```

**Expected Error Output**:

```
// Invalid API key
Error: OpenAI API error (401): Invalid API key provided

// Rate limit
Error: OpenAI API error (429): Rate limit reached for requests

// Model access
Error: OpenAI API error (404): The model 'invalid-model' does not exist or you do not have access to it

// Service unavailable
Error: OpenAI API error (503): The engine is currently overloaded
```


## Error Codes

### ERR_INVALID_API_KEY

**Code**: `invalid_api_key`
**HTTP Status**: 401 Unauthorized
**gRPC Code**: `UNAUTHENTICATED`

**Cause**: API key is missing, invalid, malformed, or revoked.

**Example**:
```
Error: OpenAI API error (401): Invalid API key provided: sk-proj-****
```

**Resolution**:
1. Verify API key from [platform.openai.com/api-keys](https://platform.openai.com/api-keys)
2. Check key format: `sk-proj-...` (new) or `sk-...` (legacy)
3. Ensure key hasn't been revoked
4. Create new API key if needed

**Retry behavior**: Not retryable (fix API key first)


### ERR_INSUFFICIENT_QUOTA

**Code**: `insufficient_quota`
**HTTP Status**: 429 Too Many Requests
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Account has insufficient credits or exceeded usage limits.

**Example**:
```
Error: OpenAI API error (429): You exceeded your current quota, please check your plan and billing details
```

**Resolution**:
1. Check balance at [platform.openai.com/account/billing](https://platform.openai.com/account/billing)
2. Add credits to prepaid balance
3. Upgrade plan if on free tier
4. Wait for quota reset (if on tier-based limits)

**Retry behavior**: Not retryable until credits added or quota reset


### ERR_MODEL_NOT_FOUND

**Code**: `model_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Specified model doesn't exist or account doesn't have access.

**Example**:
```
Error: OpenAI API error (404): The model 'invalid-model' does not exist or you do not have access to it
```

**Resolution**:
1. Verify model ID from available models list
2. Check for typos in model name
3. Ensure account tier has access
4. Use `gpt-4.1-nano` or `gpt-4.1-mini` as budget alternatives

**Retry behavior**: Not retryable (fix model name or upgrade account)

**Valid Model IDs**:
- Standard: `gpt-5`, `gpt-5-mini`, `gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`
- Reasoning: `o3`, `o3-mini`, `o4-mini`


### ERR_RATE_LIMIT

**Code**: `rate_limit_exceeded`
**HTTP Status**: 429 Too Many Requests
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Exceeded rate limits for your account tier.

**Example**:
```
Error: OpenAI API error (429): Rate limit reached for requests in organization org-xxx on requests per min (RPM): Limit 3, Used 3, Requested 1
```

**Resolution**:
1. **Immediate**: Implement exponential backoff (retry after delay)
2. **Short-term**: Reduce request rate or batch requests
3. **Long-term**: Upgrade account tier for higher limits

**Retry behavior**: Retryable with exponential backoff

**Rate Limits by Tier** (as of March 2026):

| Tier | RPM | TPM | Description |
|------|-----|-----|-------------|
| **Free** | 3 | 40K | Trial/testing |
| **Tier 1** | 500 | 200K | $5+ spent |
| **Tier 2** | 5K | 2M | $50+ spent |
| **Tier 3** | 5K | 10M | $100+ spent |
| **Tier 4** | 10K | 30M | $250+ spent |
| **Tier 5** | 10K | 100M | $1K+ spent |

**Example Retry Logic**:

```go
import "time"

func chatWithRetry(client *openai.Client, ctx context.Context, messages []types.Message) (*types.LLMResponse, error) {
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

**Code**: `invalid_request_error`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Request parameters are invalid (e.g., temperature out of range, invalid message format).

**Example**:
```
Error: OpenAI API error (400): 'temperature' must be between 0 and 2
```

**Resolution**:
1. Validate temperature: 0.0-2.0
2. Validate max_tokens: 1 to model's max (128K for gpt-5, 32K for gpt-4.1, 100K for o3)
4. Verify message format (required fields: role, content)
5. Check tool schemas match OpenAI format

**Retry behavior**: Not retryable (fix request parameters)


### ERR_SERVICE_UNAVAILABLE

**Code**: `service_unavailable`
**HTTP Status**: 500 Internal Server Error / 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: OpenAI service is experiencing issues or overloaded.

**Example**:
```
Error: OpenAI API error (503): The engine is currently overloaded, please try again later
```

**Resolution**:
1. **Immediate**: Retry with exponential backoff
2. **Check status**: [status.openai.com](https://status.openai.com/)
3. **Fallback**: Switch to alternate model (e.g., `gpt-4.1-nano`)
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
4. **Use faster model**: Switch to `gpt-4.1-nano` or `gpt-4.1-mini`

**Retry behavior**: Retryable with same request


### ERR_CONTEXT_LENGTH_EXCEEDED

**Code**: `context_length_exceeded`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Total tokens (prompt + completion) exceeds model's context window.

**Example**:
```
Error: OpenAI API error (400): This model's maximum context length is 8192 tokens. However, your messages resulted in 10000 tokens.
```

**Resolution**:
1. Reduce prompt length (truncate conversation history)
2. Reduce max_tokens parameter
3. Switch to model with larger context:
   - o3/o3-mini/o4-mini: 200K
   - GPT-5/GPT-5 Mini: 272K
   - GPT-4.1/GPT-4.1 Mini/GPT-4.1 Nano: 1M
4. Implement sliding window for long conversations

**Retry behavior**: Not retryable until prompt reduced


### ERR_CONTENT_FILTER

**Code**: `content_filter`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Request or response triggered OpenAI's content policy filters.

**Example**:
```
Error: OpenAI API error (400): Your request was rejected as a result of our safety system
```

**Resolution**:
1. Review content policy: [openai.com/policies](https://openai.com/policies)
2. Rephrase prompt to avoid policy violations
3. Implement content moderation API for user inputs
4. Contact support if false positive

**Retry behavior**: Not retryable (modify content)


## Rate Limiting

OpenAI applies rate limits based on account tier and usage:

### Rate Limit Types

1. **Requests Per Minute (RPM)**: Max requests in a minute
2. **Tokens Per Minute (TPM)**: Max tokens processed in a minute
3. **Tokens Per Day (TPD)**: Daily token limit (tier-based)

### Rate Limits by Tier

| Tier | Qualification | RPM | TPM (GPT-4.1) | TPD |
|------|---------------|-----|--------------|-----|
| **Free** | $0 spent | 3 | 40K | 200K |
| **Tier 1** | $5+ spent | 500 | 200K | 2M |
| **Tier 2** | $50+ spent | 5K | 2M | 10M |
| **Tier 3** | $100+ spent | 5K | 10M | 20M |
| **Tier 4** | $250+ spent | 10K | 30M | 100M |
| **Tier 5** | $1K+ spent | 10K | 100M | 300M |

**Note**: Limits vary by model. Check [platform.openai.com/account/limits](https://platform.openai.com/account/limits) for your specific limits.


### Handling Rate Limits

**Option 1: Exponential Backoff** (shown in ERR_RATE_LIMIT section)

**Option 2: Client-Side Rate Limiting**

```go
import "github.com/teradata-labs/loom/pkg/llm"

// Configure client with rate limiter
client := openai.NewClient(openai.Config{
    APIKey:  apiKey,
    Model:   "gpt-4.1",
    RateLimiterConfig: llm.RateLimiterConfig{
        Enabled:         true,
        TokensPerMinute: 180000,  // 180K TPM (below 200K limit for Tier 1)
    },
})
```

**Behavior**:
- Requests are automatically queued when approaching limit
- Prevents 429 errors before they occur
- Smooths request rate across time


## Testing

The OpenAI provider has 48.2% test coverage:

```bash
# Run tests (fts5 tag required for loom project)
cd /path/to/loom
go test -tags fts5 ./pkg/llm/openai/

# With coverage (48.2%)
go test -tags fts5 -cover ./pkg/llm/openai/

# With race detection
go test -tags fts5 -race ./pkg/llm/openai/

# Verbose output
go test -tags fts5 -v ./pkg/llm/openai/
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
coverage: 48.2% of statements
ok  	github.com/teradata-labs/loom/pkg/llm/openai	0.198s
```


### Integration Testing

Test against real OpenAI API:

```go
func TestOpenAI_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
        t.Skip("OPENAI_API_KEY not set")
    }

    client := openai.NewClient(openai.Config{
        APIKey: apiKey,
        Model:  "gpt-4.1-nano",  // Use cheapest model for tests
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
export OPENAI_API_KEY="sk-proj-..."
go test -tags fts5 -v ./pkg/llm/openai -run Integration
```


## Best Practices

### 1. Model Selection Strategy

```go
// Budget tasks: Use gpt-4.1-nano
agent, err := builder.NewAgentBuilder().
    WithOpenAILLMCustomModel(apiKey, "gpt-4.1-nano").
    Build()

// General tasks: Use gpt-4.1 (default, best balance)
agent, err := builder.NewAgentBuilder().
    WithOpenAILLM(apiKey).
    Build()

// Reasoning + general: Use gpt-5
agent, err := builder.NewAgentBuilder().
    WithOpenAILLMCustomModel(apiKey, "gpt-5").
    Build()

// Advanced reasoning: Use o3 (with tools and vision)
agent, err := builder.NewAgentBuilder().
    WithOpenAILLMCustomModel(apiKey, "o3").
    Build()
```

**Decision Tree**:
1. **Budget**: → `gpt-4.1-nano` ($0.10/$0.40)
2. **Cost-Effective**: → `gpt-4.1-mini` or `gpt-5-mini` ($0.40/$1.60)
3. **General Purpose**: → `gpt-4.1` ($2.00/$8.00) (default)
4. **Reasoning + General**: → `gpt-5` ($2.50/$10.00)
5. **Advanced Reasoning**: → `o3` ($10.00/$40.00)
6. **Cost-Effective Reasoning**: → `o3-mini` or `o4-mini` ($1.10/$4.40)


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
    if totalCost > 0.50 {
        logger.Warn("Session cost exceeds threshold",
            zap.Float64("total_cost", totalCost),
        )
    }
}
```


### 3. Error Handling with Retry

```go
import "time"

func chatWithRetryAndBackoff(client *openai.Client, ctx context.Context, messages []types.Message) (*types.LLMResponse, error) {
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
apiKey := "sk-proj-abc123..."

// ✅ Good - Environment variable
apiKey := os.Getenv("OPENAI_API_KEY")
if apiKey == "" {
    return fmt.Errorf("OPENAI_API_KEY not set")
}

// ✅ Better - Secure vault (e.g., HashiCorp Vault)
apiKey, err := vault.GetSecret("openai-api-key")
if err != nil {
    return fmt.Errorf("failed to get API key: %w", err)
}

// ✅ Best - Loom keyring integration
// looms config set-key openai_api_key
```


### 5. Token Optimization

```go
// Set appropriate max_tokens for your use case
config := openai.Config{
    APIKey:    apiKey,
    MaxTokens: 512,  // For short responses (e.g., classification)
}

// For longer responses
config.MaxTokens = 2048

// For very long responses (e.g., code generation)
config.MaxTokens = 8192

// Never exceed model's context window
// gpt-4.1: 1M context, 32K max output
// gpt-5: 272K context, 128K max output
// o3: 200K context, 100K max output
```


### 6. Monitor Usage

```go
import "go.uber.org/zap"

logger, _ := zap.NewProduction()

// Log before request
logger.Info("OpenAI request",
    zap.String("model", "gpt-4.1"),
    zap.Int("input_message_count", len(messages)),
)

startTime := time.Now()
response, err := client.Chat(ctx, messages, tools)

// Log after request
logger.Info("OpenAI response",
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
  "msg": "OpenAI request",
  "model": "gpt-4.1",
  "input_message_count": 3
}
{
  "level": "info",
  "msg": "OpenAI response",
  "latency": "1.456s",
  "input_tokens": 234,
  "output_tokens": 567,
  "cost_usd": 0.006265,
  "tool_calls": 1
}
```


## Comparison with Other Providers

| Feature | OpenAI | Anthropic | Azure OpenAI | Bedrock | Gemini | HuggingFace | Mistral | Ollama |
|---------|--------|-----------|--------------|---------|--------|-------------|---------|---------|
| **API Format** | Native | Native | OpenAI-compatible | AWS SDK | Native | Native | OpenAI-compatible | OpenAI-compatible |
| **Tool Calling** | Native | Native | Native | Native | Native | Limited | Native | Limited |
| **Models** | GPT-5, GPT-4.1, o3 | Claude Sonnet 4.5 | GPT-5, GPT-4.1 (Azure) | Claude (via AWS) | Gemini 3 | Open models | Mistral, Mixtral | Open models |
| **Context** | 200K-1M | 200K | 200K-1M | 200K | Model-dependent | Model-dependent | 32K-128K | Model-dependent |
| **Vision** | ✅ Most models | ✅ Claude | ✅ Most models | ✅ Claude | ✅ | ⚠️ Limited | ⚠️ Limited | ⚠️ Limited |
| **Privacy** | API call | API call | VPC/Private | VPC/Private | API call | API call | API call | Full (local) |
| **Deployment** | Cloud | Cloud | Azure regions | AWS regions | Cloud | Cloud | Cloud | Self-hosted |


## Limitations

1. **Vision**: Image input supported but requires ContentBlocks
   - **Workaround**: Use ContentBlocks API (see Vision Support section)
   - **Status**: Implemented ✅

2. **Rate Limit Handling**: No built-in automatic retry
   - **Workaround**: Implement exponential backoff manually (see Best Practices)


## Migration to Azure OpenAI

Migrating from OpenAI to Azure OpenAI for enterprise compliance:

### Before (OpenAI Direct)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(apiKey).
    Build()
```


### After (Azure OpenAI)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAILLM(
        "https://myresource.openai.azure.com",  // Azure endpoint
        "gpt-4.1-deployment",                   // Deployment name
        azureKey,                              // Azure API key
    ).
    Build()
```


### Key Differences

| Aspect | OpenAI Direct | Azure OpenAI |
|--------|---------------|--------------|
| **Endpoint** | `api.openai.com` | `{resource}.openai.azure.com` |
| **Authentication** | API key only | API key or Entra ID |
| **Model Selector** | Model name (`gpt-4.1`) | Deployment name (`gpt-4.1-prod`) |
| **Rate Limits** | Per-org (tier-based) | Per-deployment (TPM quotas) |
| **Regions** | Global (single endpoint) | Regional (multiple endpoints) |
| **Compliance** | SOC 2 | Azure certifications (ISO, HIPAA) |
| **VNet** | No | Yes (Private Endpoint) |
| **Cost** | Pay-per-use | Pay-per-use or Provisioned |

The message format, tool calling, and response structure remain identical.


### Migration Checklist

- [ ] Create Azure OpenAI resource in Azure Portal
- [ ] Deploy models with deployment names
- [ ] Update endpoint URL in configuration
- [ ] Update authentication (API key or Entra ID)
- [ ] Change model name to deployment name
- [ ] Test connectivity and authentication
- [ ] Monitor TPM quotas (different from OpenAI RPM/TPM limits)
- [ ] Update retry logic for Azure-specific errors
- [ ] Configure regional failover (optional)
- [ ] Update logging/observability for Azure-specific fields


## See Also

### LLM Provider Documentation
- [LLM Provider Overview](./llm-providers.md) - All supported LLM providers
- [Anthropic Integration](./llm-anthropic.md) - Anthropic Claude provider
- [Azure OpenAI Integration](./llm-azure-openai.md) - Enterprise deployment
- [Bedrock Integration](./llm-bedrock.md) - AWS Bedrock provider
- [Gemini Integration](./llm-gemini.md) - Google Gemini provider
- [HuggingFace Integration](./llm-huggingface.md) - HuggingFace Inference API
- [Mistral Integration](./llm-mistral.md) - Mistral AI provider
- [Ollama Integration](./llm-ollama.md) - Local/on-premise models

### Integration Guides
- [Agent Configuration](./agent-configuration.md) - Complete agent setup

### External Resources
- [OpenAI Documentation](https://platform.openai.com/docs)
- [OpenAI Pricing](https://openai.com/pricing)
- [OpenAI API Reference](https://platform.openai.com/docs/api-reference)
- [OpenAI Status](https://status.openai.com/)
