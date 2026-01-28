
# Azure OpenAI Integration Reference

**Version**: v1.0.0-beta.1

Complete technical reference for integrating Loom with Azure OpenAI Service.


## Table of Contents

- [Quick Reference](#quick-reference)
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Features](#features)
- [Authentication Methods](#authentication-methods)
- [Configuration](#configuration)
- [Deployment Setup](#deployment-setup)
- [Supported Models](#supported-models)
- [API Differences from OpenAI](#api-differences-from-openai)
- [Request and Response Format](#request-and-response-format)
- [Cost Tracking](#cost-tracking)
- [Error Handling](#error-handling)
- [Error Codes](#error-codes)
- [Rate Limiting](#rate-limiting)
- [Testing](#testing)
- [Production Best Practices](#production-best-practices)
- [Comparison with Other Providers](#comparison-with-other-providers)
- [Troubleshooting](#troubleshooting)
- [Migration from OpenAI](#migration-from-openai)
- [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
# Builder API (Programmatic)
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAILLM(
        "https://myresource.openai.azure.com",  # Azure endpoint
        "gpt-4o-deployment",                    # Deployment name
        "your-api-key",                        # API key
    ).
    Build()
```

```go
// Direct Instantiation
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     "https://myresource.openai.azure.com", // Required
    DeploymentID: "gpt-4o-deployment",                   // Required
    APIKey:       "your-api-key",                        // Required (or EntraToken)
    APIVersion:   "2024-10-21",                          // Default: "2024-10-21"
    ModelName:    "gpt-4o",                              // Optional (auto-inferred)
    MaxTokens:    4096,                                  // Default: 4096
    Temperature:  0.7,                                   // Default: 1.0
    Timeout:      60 * time.Second,                      // Default: 60s
})
```

### Available Models

| Model | Deployment ID | Input Cost | Output Cost | Context | Features |
|-------|---------------|------------|-------------|---------|----------|
| GPT-4o | `gpt-4o` | $2.50/1M | $10.00/1M | 128K | Tools, vision, streaming |
| GPT-4o mini | `gpt-4o-mini` | $0.15/1M | $0.60/1M | 128K | Tools, vision, streaming |
| GPT-4 Turbo | `gpt-4-turbo` | $10.00/1M | $30.00/1M | 128K | Tools, vision, streaming |
| GPT-4 | `gpt-4` | $30.00/1M | $60.00/1M | 8K | Tools, streaming |
| GPT-3.5 Turbo | `gpt-35-turbo` | $0.50/1M | $1.50/1M | 16K | Tools, streaming |

*Prices are Pay-As-You-Go rates. Provisioned throughput pricing differs.*

### Authentication Methods

| Method | Header | Use Case | Security |
|--------|--------|----------|----------|
| **API Key** | `api-key: <key>` | Simple auth, dev/test | Moderate (rotate regularly) |
| **Entra ID** | `Authorization: Bearer <token>` | Enterprise, production | High (Azure AD managed) |

### Common Commands

```bash
# Get Azure endpoint
az cognitiveservices account show \
  --name myresource \
  --resource-group mygroup \
  --query properties.endpoint

# Get API key
az cognitiveservices account keys list \
  --name myresource \
  --resource-group mygroup

# Get Entra ID token
az account get-access-token \
  --resource https://cognitiveservices.azure.com \
  --query accessToken
```

### Configuration Parameters

| Parameter | Type | Required | Default | Constraints |
|-----------|------|----------|---------|-------------|
| `Endpoint` | `string` | Yes | - | Must be valid HTTPS URL |
| `DeploymentID` | `string` | Yes | - | Must match Azure deployment |
| `APIKey` | `string` | One of* | - | - |
| `EntraToken` | `string` | One of* | - | Format: `Bearer <token>` |
| `APIVersion` | `string` | No | `"2024-10-21"` | Azure API version |
| `ModelName` | `string` | No | Auto-inferred | For cost calculation |
| `MaxTokens` | `int` | No | `4096` | 1-128000 (model dependent) |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 |
| `Timeout` | `duration` | No | `60s` | 1s-10m |

*Either `APIKey` or `EntraToken` is required (not both).


## Overview

Azure OpenAI provides enterprise access to OpenAI models through Microsoft's secure cloud infrastructure. The integration offers:
- Deployment-based routing (models hosted as named deployments)
- Dual authentication (API key or Microsoft Entra ID)
- Regional deployment options for compliance
- Same API format as OpenAI (tool calling, messages)
- Automatic model inference for cost calculation

**Implementation**: `pkg/llm/azureopenai/client.go`
**Test Coverage**: 76% (515 lines of tests)
**Interface**: Full `LLMProvider` compliance


## Prerequisites

1. **Azure Subscription**: Active Azure subscription with OpenAI access
2. **Azure OpenAI Resource**: Created in Azure Portal ([learn.microsoft.com/azure/ai-services/openai](https://learn.microsoft.com/azure/ai-services/openai))
3. **Model Deployment**: Deploy a model (e.g., gpt-4o) with a deployment name
4. **Authentication**: Either API key or Entra ID token

**Verification**:

```bash
# Check if resource exists
az cognitiveservices account show \
  --name myresource \
  --resource-group mygroup

# List deployments
az cognitiveservices account deployment list \
  --name myresource \
  --resource-group mygroup
```


## Features

### Implemented ✅

- Full LLMProvider interface implementation (`pkg/llm/azureopenai/client.go`)
- Message conversion (system, user, assistant, tool roles)
- Tool calling with JSON schema conversion (OpenAI-compatible)
- Cost calculation for all major models
- Deployment-based routing (Azure-specific)
- Dual authentication support (API key and Entra ID)
- Model name inference from deployment ID
- Temperature and max tokens configuration
- Streaming support (`StreamChat`)
- Rate limiting (configurable)
- 76% test coverage (515 lines of tests)

### Partial ⚠️

- Server integration (available via Builder API only, not in `looms serve` CLI yet)
- Keyring storage (not integrated with `looms config` commands)

### Planned 📋

- Full CLI integration with `looms config` (v1.1.0)
- Automatic retry with exponential backoff (v1.1.0)
- Circuit breaker integration (v1.2.0)


## Authentication Methods

### 1. API Key Authentication (Simpler)

Get your API key from Azure Portal:
1. Navigate to your Azure OpenAI resource
2. Go to "Keys and Endpoint"
3. Copy either KEY1 or KEY2

**Configuration**:

```go
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     "https://myresource.openai.azure.com",
    DeploymentID: "gpt-4o-deployment",
    APIKey:       "your-api-key",
})
if err != nil {
    log.Fatalf("Failed to create client: %v", err)
}
```

**HTTP Header**: `api-key: <key>`

**Security Notes**:
- Rotate keys regularly (Azure supports KEY1 and KEY2 for zero-downtime rotation)
- Store in Azure Key Vault, not environment variables
- Use Key Vault references in Azure App Service

**Expected Output**:

```go
// Success
client := &azureopenai.Client{...}

// Error: Missing API key
Error: either APIKey or EntraToken must be provided
```


### 2. Microsoft Entra ID Authentication (More Secure)

For enterprise scenarios using Azure Active Directory:

**Configuration**:

```go
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     "https://myresource.openai.azure.com",
    DeploymentID: "gpt-4o-deployment",
    EntraToken:   bearerToken,  // Obtained from Azure AD
})
if err != nil {
    log.Fatalf("Failed to create client: %v", err)
}
```

**HTTP Header**: `Authorization: Bearer <token>`

**Obtaining Token Programmatically**:

```bash
# Using Azure CLI
az account get-access-token \
  --resource https://cognitiveservices.azure.com \
  --query accessToken \
  --output tsv
```

**Expected Output**:

```
eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsIng1dCI6Ik1yNS1BVW...
```

**Token Refresh**:

Entra ID tokens expire after 1 hour. Implement refresh logic:

```go
import "github.com/Azure/azure-sdk-for-go/sdk/azidentity"

// Get token credential
cred, err := azidentity.NewDefaultAzureCredential(nil)
if err != nil {
    return err
}

// Get token
token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
    Scopes: []string{"https://cognitiveservices.azure.com/.default"},
})
if err != nil {
    return err
}

// Create client with token
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     endpoint,
    DeploymentID: deploymentID,
    EntraToken:   token.Token,
})
```


## Configuration

### Using Builder API (Programmatic)

The Azure OpenAI provider is currently available through the Builder API for programmatic agent creation:

```go
import (
    "github.com/teradata-labs/loom/pkg/builder"
    "github.com/teradata-labs/loom/pkg/llm/azureopenai"
)

// Option 1: API Key Authentication
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAILLM(
        "https://myresource.openai.azure.com",  // Your Azure endpoint
        "gpt-4o-deployment",                    // Your deployment name
        "your-api-key",                        // API key from Azure Portal
    ).
    Build()

if err != nil {
    log.Fatalf("Failed to build agent: %v", err)
}

// Option 2: Microsoft Entra ID Authentication
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAIEntraAuth(
        "https://myresource.openai.azure.com",
        "gpt-4o-deployment",
        entraToken,  // Bearer token from Azure AD
    ).
    Build()
```

**Expected Output**:

```go
// Success
agent := &agent.Agent{...}

// Error: Invalid endpoint
Error: endpoint is required

// Error: Invalid deployment
Error: deployment ID is required
```


### Direct Instantiation

For more control, instantiate the client directly:

```go
import "github.com/teradata-labs/loom/pkg/llm/azureopenai"

// Full configuration with all options
client, err := azureopenai.NewClient(azureopenai.Config{
    // Required
    Endpoint:     "https://myresource.openai.azure.com",
    DeploymentID: "gpt-4o-deployment",

    // Authentication (choose one)
    APIKey:       "your-api-key",        // Option 1: API key
    EntraToken:   "Bearer ...",          // Option 2: Entra ID token

    // Optional
    APIVersion:   "2024-10-21",          // Default: "2024-10-21"
    ModelName:    "gpt-4o",              // For cost tracking (auto-inferred if not set)
    MaxTokens:    4096,                  // Default: 4096, Range: 1-128000
    Temperature:  0.7,                   // Default: 1.0, Range: 0.0-2.0
    Timeout:      60 * time.Second,      // Default: 60s, Range: 1s-10m
})
```

**Parameter Specifications**:

| Parameter | Type | Required | Default | Range | Description |
|-----------|------|----------|---------|-------|-------------|
| `Endpoint` | `string` | Yes | - | Valid HTTPS URL | Azure OpenAI resource endpoint |
| `DeploymentID` | `string` | Yes | - | Azure deployment name | Your model deployment name |
| `APIKey` | `string` | One of* | - | - | Azure API key (KEY1 or KEY2) |
| `EntraToken` | `string` | One of* | - | Valid JWT token | Microsoft Entra ID bearer token |
| `APIVersion` | `string` | No | `"2024-10-21"` | Azure API version | Azure OpenAI API version |
| `ModelName` | `string` | No | Auto-inferred | OpenAI model ID | Model for cost calculation |
| `MaxTokens` | `int` | No | `4096` | 1-128000 | Maximum tokens in response |
| `Temperature` | `float64` | No | `1.0` | 0.0-2.0 | Sampling temperature |
| `Timeout` | `duration` | No | `60s` | 1s-10m | Request timeout |

*Either `APIKey` or `EntraToken` is required (not both).


## Deployment Setup

### Creating a Deployment

1. In Azure Portal, navigate to your Azure OpenAI resource
2. Go to "Model deployments"
3. Click "Create new deployment"
4. Select a model (e.g., gpt-4o, gpt-4, gpt-35-turbo)
5. Choose a deployment name (e.g., "gpt-4o-production")
6. Set capacity (Tokens Per Minute quota)

**Expected Output**:

```
Deployment created successfully
Name: gpt-4o-production
Model: gpt-4o
Capacity: 120K TPM
Status: Succeeded
```


### Deployment Naming Conventions

The Azure OpenAI client can automatically infer the model from your deployment name:

| Deployment Name Examples | Inferred Model | Used for Cost Calculation |
|-------------------------|----------------|---------------------------|
| `gpt-4o-deployment` | `gpt-4o` | Yes ✅ |
| `gpt-4o-prod` | `gpt-4o` | Yes ✅ |
| `my-gpt4-turbo` | `gpt-4-turbo` | Yes ✅ |
| `prod-gpt-35-turbo` | `gpt-35-turbo` | Yes ✅ |
| `custom-name` | Unknown | No ⚠️ (specify `ModelName`) |

**Model Inference Logic**:
- Searches deployment name for model identifiers (`gpt-4o`, `gpt-4-turbo`, `gpt-35-turbo`, etc.)
- Case-insensitive matching
- Handles common prefixes/suffixes (`prod-`, `-deployment`, etc.)
- Falls back to "unknown" if no match found

**Recommendation**: Include model name in deployment ID for automatic cost tracking.


## Supported Models

Azure OpenAI supports these OpenAI models (availability varies by region):

| Model | Deployment Model ID | Input Cost | Output Cost | Context | Tool Calling | Vision | Streaming |
|-------|-------------------|------------|-------------|---------|--------------|--------|-----------|
| **GPT-4o** | `gpt-4o` | $2.50/1M | $10.00/1M | 128K | ✅ | ✅ | ✅ |
| **GPT-4o mini** | `gpt-4o-mini` | $0.15/1M | $0.60/1M | 128K | ✅ | ✅ | ✅ |
| **GPT-4 Turbo** | `gpt-4-turbo` | $10.00/1M | $30.00/1M | 128K | ✅ | ✅ | ✅ |
| **GPT-4** | `gpt-4` | $30.00/1M | $60.00/1M | 8K | ✅ | ❌ | ✅ |
| **GPT-3.5 Turbo** | `gpt-35-turbo` | $0.50/1M | $1.50/1M | 16K | ✅ | ❌ | ✅ |

*Prices shown are Pay-As-You-Go rates (as of January 2025). Provisioned throughput pricing differs.*

**Regional Availability**:

Check current model availability by region:

```bash
az cognitiveservices account list-models \
  --resource-group mygroup \
  --name myresource \
  --query "[].{Model:model.name, Version:model.version}" \
  --output table
```


## API Differences from OpenAI

### URL Structure

Azure OpenAI uses deployment-based routing:

```
# OpenAI (Direct)
POST https://api.openai.com/v1/chat/completions

# Azure OpenAI
POST https://{resource}.openai.azure.com/openai/deployments/{deployment-id}/chat/completions?api-version={version}
```

**Example URLs**:

```
# OpenAI
https://api.openai.com/v1/chat/completions

# Azure OpenAI (East US)
https://myresource.openai.azure.com/openai/deployments/gpt-4o-prod/chat/completions?api-version=2024-10-21
```


### Request Format

The request body is identical to OpenAI:

```go
// Same message format as OpenAI
messages := []types.Message{
    {Role: "system", Content: "You are a helpful assistant"},
    {Role: "user", Content: "Hello!"},
}

// Same tool format as OpenAI
tools := []shuttle.Tool{
    &MyCustomTool{},
}

// Chat method handles Azure-specific routing internally
response, err := client.Chat(ctx, messages, tools)
if err != nil {
    log.Fatalf("Chat failed: %v", err)
}

fmt.Printf("Response: %s\n", response.Message)
fmt.Printf("Tokens: %d input, %d output\n",
    response.TokensUsed.Input,
    response.TokensUsed.Output)
fmt.Printf("Cost: $%.4f\n", response.CostUSD)
```

**Expected Output**:

```
Response: Hello! How can I assist you today?
Tokens: 24 input, 9 output
Cost: $0.0001
```


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
        Content: "You are a SQL expert.",
    },
    {
        Role:    "user",
        Content: "Explain SELECT statements.",
    },
}

response, err := client.Chat(ctx, messages, nil)
```

**HTTP Equivalent** (internal):

```http
POST /openai/deployments/gpt-4o-prod/chat/completions?api-version=2024-10-21
Host: myresource.openai.azure.com
api-key: your-api-key
Content-Type: application/json

{
  "messages": [
    {"role": "system", "content": "You are a SQL expert."},
    {"role": "user", "content": "Explain SELECT statements."}
  ],
  "max_tokens": 4096,
  "temperature": 1.0
}
```


### Chat Response

```go
type Response struct {
    Message    string        // Assistant response
    TokensUsed TokenUsage    // Token counts
    CostUSD    float64       // Estimated cost
    ToolCalls  []ToolCall    // Tool calls (if any)
}

type TokenUsage struct {
    Input  int
    Output int
    Total  int
}
```

**Example**:

```go
response := &types.Response{
    Message: "A SELECT statement retrieves data from database tables...",
    TokensUsed: types.TokenUsage{
        Input:  28,
        Output: 156,
        Total:  184,
    },
    CostUSD: 0.0016,  // $2.50/1M input + $10/1M output
}
```


## Cost Tracking

The Azure OpenAI client automatically calculates costs based on token usage:

```go
response, err := client.Chat(ctx, messages, tools)
if err != nil {
    return err
}

fmt.Printf("Tokens used: %d input, %d output\n",
    response.TokensUsed.Input,
    response.TokensUsed.Output)
fmt.Printf("Estimated cost: $%.4f\n", response.CostUSD)
```

**Expected Output**:

```
Tokens used: 1245 input, 387 output
Estimated cost: $0.0070
```

**Cost Calculation Formula**:

```
Cost = (InputTokens / 1,000,000 * InputPrice) + (OutputTokens / 1,000,000 * OutputPrice)
```

**Example (GPT-4o)**:
```
Input:  1245 tokens * $2.50/1M = $0.0031
Output: 387 tokens * $10.00/1M = $0.0039
Total:  $0.0070
```


### Model Name for Cost Calculation

Cost calculation requires the model name. The client attempts to infer this from your deployment ID, but you can specify it explicitly:

```go
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     "https://myresource.openai.azure.com",
    DeploymentID: "my-custom-deployment",
    APIKey:       apiKey,
    ModelName:    "gpt-4o",  // Explicitly specify for cost tracking
})
```

**When to Specify `ModelName`**:
- ✅ Deployment name doesn't include model identifier
- ✅ Want to override inferred model
- ✅ Using custom/fine-tuned models

**When Auto-Inference Works**:
- ✅ Deployment name contains `gpt-4o`, `gpt-4-turbo`, `gpt-35-turbo`, etc.
- ✅ Standard Azure naming conventions


## Error Handling

Azure OpenAI returns errors in the same format as OpenAI:

```go
response, err := client.Chat(ctx, messages, tools)
if err != nil {
    // Check error type
    switch {
    case strings.Contains(err.Error(), "401"):
        log.Error("Authentication failed - check API key")
    case strings.Contains(err.Error(), "404"):
        log.Error("Deployment not found - verify deployment name")
    case strings.Contains(err.Error(), "429"):
        log.Error("Rate limit exceeded - implement backoff")
    case strings.Contains(err.Error(), "500"):
        log.Error("Azure service error - retry with backoff")
    default:
        log.Errorf("Unknown error: %v", err)
    }
    return err
}
```

**Expected Error Output**:

```
// 401 Unauthorized
Error: API error (status 401): {"error": {"message": "Access denied due to invalid subscription key."}}

// 404 Not Found
Error: Azure OpenAI API error: Deployment 'invalid-deployment' not found (type: invalid_request_error)

// 429 Rate Limit
Error: API error (status 429): {"error": {"message": "Requests to the ChatCompletions_Create Operation under Azure OpenAI API version 2024-10-21 have exceeded token rate limit of your current OpenAI S0 pricing tier."}}

// 500 Internal Error
Error: API error (status 500): {"error": {"message": "The service is currently unavailable."}}
```


## Error Codes

### ERR_INVALID_ENDPOINT

**Code**: `invalid_endpoint`
**HTTP Status**: N/A (Client-side validation)
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Endpoint URL is empty or malformed.

**Example**:
```
Error: endpoint is required
```

**Resolution**:
1. Verify endpoint format: `https://{resource-name}.openai.azure.com`
2. Check resource name matches Azure Portal
3. Ensure HTTPS protocol (not HTTP)

**Retry behavior**: Not retryable (configuration error)


### ERR_INVALID_DEPLOYMENT

**Code**: `invalid_deployment`
**HTTP Status**: N/A (Client-side validation)
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Deployment ID is empty.

**Example**:
```
Error: deployment ID is required
```

**Resolution**:
1. Verify deployment exists in Azure Portal
2. Check deployment name matches exactly (case-sensitive)
3. Ensure deployment is in "Succeeded" state

**Retry behavior**: Not retryable (configuration error)


### ERR_MISSING_AUTH

**Code**: `missing_auth`
**HTTP Status**: N/A (Client-side validation)
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Neither API key nor Entra token provided.

**Example**:
```
Error: either APIKey or EntraToken must be provided
```

**Resolution**:
1. Provide API key: `APIKey: "your-api-key"`
2. OR provide Entra token: `EntraToken: "Bearer ..."`
3. Do not provide both (API key takes precedence)

**Retry behavior**: Not retryable (configuration error)


### ERR_UNAUTHORIZED

**Code**: `unauthorized`
**HTTP Status**: 401 Unauthorized
**gRPC Code**: `UNAUTHENTICATED`

**Cause**: Invalid or expired authentication credentials.

**Example**:
```
Error: API error (status 401): {"error": {"message": "Access denied due to invalid subscription key."}}
```

**Resolution**:
1. **API Key**: Regenerate key in Azure Portal (Keys and Endpoint)
2. **Entra Token**: Refresh token (tokens expire after 1 hour)
3. Verify resource permissions (RBAC roles: "Cognitive Services OpenAI User")
4. Check subscription status (not suspended/cancelled)

**Retry behavior**: Not automatically retried (authentication must be fixed first)


### ERR_DEPLOYMENT_NOT_FOUND

**Code**: `DeploymentNotFound`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Deployment name doesn't exist or is in wrong region.

**Example**:
```
Error: Azure OpenAI API error: Deployment 'invalid-deployment' not found (type: invalid_request_error)
```

**Resolution**:
1. List deployments: `az cognitiveservices account deployment list`
2. Verify deployment name matches exactly (case-sensitive)
3. Check deployment status is "Succeeded"
4. Ensure using correct region/endpoint

**Retry behavior**: Not automatically retried (deployment must be created/fixed)


### ERR_RATE_LIMIT

**Code**: `rate_limit_exceeded`
**HTTP Status**: 429 Too Many Requests
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Exceeded Tokens Per Minute (TPM) quota for deployment.

**Example**:
```
Error: API error (status 429): {"error": {"message": "Requests to the ChatCompletions_Create Operation under Azure OpenAI API version 2024-10-21 have exceeded token rate limit of your current OpenAI S0 pricing tier."}}
```

**Resolution**:
1. **Immediate**: Implement exponential backoff (retry after 1s, 2s, 4s, 8s)
2. **Short-term**: Reduce request rate or token usage
3. **Long-term**: Increase TPM quota in Azure Portal (Model deployments → Edit → Tokens Per Minute)
4. **Alternative**: Create additional deployments for load balancing

**Retry behavior**: Should be retried with exponential backoff (currently manual, automatic retry coming in v1.1.0)

**Example Retry Logic**:

```go
import "time"

func chatWithRetry(client *azureopenai.Client, ctx context.Context, messages []types.Message) (*types.Response, error) {
    maxRetries := 3
    baseDelay := time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        resp, err := client.Chat(ctx, messages, nil)
        if err == nil {
            return resp, nil
        }

        // Check if rate limit error
        if !strings.Contains(err.Error(), "429") {
            return nil, err // Non-retryable error
        }

        // Exponential backoff with jitter
        delay := baseDelay * time.Duration(1<<uint(attempt))
        jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
        time.Sleep(delay + jitter)
    }

    return nil, fmt.Errorf("max retries exceeded")
}
```


### ERR_SERVICE_UNAVAILABLE

**Code**: `service_unavailable`
**HTTP Status**: 500 Internal Server Error / 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Azure OpenAI service is experiencing issues.

**Example**:
```
Error: API error (status 500): {"error": {"message": "The service is currently unavailable."}}
```

**Resolution**:
1. **Immediate**: Retry with exponential backoff (Azure outages usually resolve in minutes)
2. **Check status**: [Azure Status](https://status.azure.com/)
3. **Failover**: Switch to alternate region deployment
4. **Alert**: Configure Azure Monitor alerts for service health

**Retry behavior**: Should be retried with exponential backoff (transient error)


### ERR_TIMEOUT

**Code**: `timeout`
**HTTP Status**: 408 Request Timeout / Client-side timeout
**gRPC Code**: `DEADLINE_EXCEEDED`

**Cause**: Request exceeded configured timeout or Azure processing time.

**Example**:
```
Error: HTTP request failed: context deadline exceeded
```

**Resolution**:
1. **Increase timeout**: `Timeout: 120 * time.Second`
2. **Reduce request complexity**: Smaller prompts, fewer tools, lower max_tokens
3. **Check network**: Ensure stable connectivity to Azure region
4. **Consider streaming**: Use `StreamChat` for long responses

**Retry behavior**: Retryable with same request (Azure state is not persisted on timeout)


### ERR_INVALID_REQUEST

**Code**: `invalid_request_error`
**HTTP Status**: 400 Bad Request
**gRPC Code**: `INVALID_ARGUMENT`

**Cause**: Request parameters are invalid (e.g., temperature out of range, invalid tool schema).

**Example**:
```
Error: Azure OpenAI API error: 'temperature' must be between 0 and 2 (type: invalid_request_error)
```

**Resolution**:
1. Validate temperature: 0.0-2.0
2. Validate max_tokens: 1 to model's context window
3. Verify tool schemas match OpenAI format
4. Check message format (required fields: role, content)

**Retry behavior**: Not retryable (fix request parameters)


## Rate Limiting

Azure OpenAI uses Tokens Per Minute (TPM) quotas set at the deployment level.

### TPM Quotas by Tier

| Tier | TPM Quota | Requests/Min | Use Case |
|------|-----------|--------------|----------|
| **Free (F0)** | 20K | 3 | Development, testing |
| **Standard (S0)** | 120K | 720 | Production (default) |
| **Provisioned** | Custom | Custom | High-volume production |

**Check Your Quota**:

```bash
az cognitiveservices account deployment show \
  --name myresource \
  --resource-group mygroup \
  --deployment-name gpt-4o-prod \
  --query properties.rateLimits
```


### Handling Rate Limits

#### Option 1: Retry with Exponential Backoff

```go
import (
    "math/rand"
    "time"
)

func chatWithBackoff(client *azureopenai.Client, ctx context.Context, messages []types.Message) (*types.Response, error) {
    maxRetries := 5
    baseDelay := 1 * time.Second
    maxDelay := 32 * time.Second

    for attempt := 0; attempt < maxRetries; attempt++ {
        resp, err := client.Chat(ctx, messages, nil)
        if err == nil {
            return resp, nil
        }

        // Check if rate limit error (429)
        if !strings.Contains(err.Error(), "429") {
            return nil, err // Non-retryable error
        }

        // Calculate delay with exponential backoff
        delay := baseDelay * time.Duration(1<<uint(attempt))
        if delay > maxDelay {
            delay = maxDelay
        }

        // Add jitter (0-1000ms)
        jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
        totalDelay := delay + jitter

        log.Printf("Rate limit hit, retrying in %v (attempt %d/%d)", totalDelay, attempt+1, maxRetries)
        time.Sleep(totalDelay)
    }

    return nil, fmt.Errorf("max retries exceeded: rate limit persists")
}
```

**Expected Output**:

```
Rate limit hit, retrying in 1.234s (attempt 1/5)
Rate limit hit, retrying in 2.567s (attempt 2/5)
Success: received response after 2 retries
```


#### Option 2: Token Rate Limiting (Client-Side)

```go
import "github.com/teradata-labs/loom/pkg/llm"

// Configure client with rate limiter
client, err := azureopenai.NewClient(azureopenai.Config{
    Endpoint:     endpoint,
    DeploymentID: deploymentID,
    APIKey:       apiKey,
    RateLimiterConfig: llm.RateLimiterConfig{
        TokensPerMinute: 100000,  // 100K TPM (below your 120K quota)
        RefillInterval:  time.Minute,
    },
})
```

**Behavior**:
- Requests are automatically queued when rate limit approached
- Prevents 429 errors before they occur
- Smooths request rate across time


#### Option 3: Load Balancing Across Deployments

```go
type LoadBalancedClient struct {
    clients []*azureopenai.Client
    current int
    mu      sync.Mutex
}

func (lb *LoadBalancedClient) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.Response, error) {
    lb.mu.Lock()
    client := lb.clients[lb.current]
    lb.current = (lb.current + 1) % len(lb.clients)
    lb.mu.Unlock()

    return client.Chat(ctx, messages, tools)
}

// Create load balancer with multiple deployments
lb := &LoadBalancedClient{
    clients: []*azureopenai.Client{
        client1, // Deployment 1 (120K TPM)
        client2, // Deployment 2 (120K TPM)
        client3, // Deployment 3 (120K TPM)
    },
}

// Effective capacity: 360K TPM
response, err := lb.Chat(ctx, messages, nil)
```


### Monitoring Token Usage

```go
import "sync/atomic"

type TokenTracker struct {
    tokensPerMinute int64
    resetTime       time.Time
    mu              sync.Mutex
}

func (t *TokenTracker) TrackUsage(tokens int) {
    t.mu.Lock()
    defer t.mu.Unlock()

    // Reset counter every minute
    if time.Now().After(t.resetTime) {
        t.tokensPerMinute = 0
        t.resetTime = time.Now().Add(time.Minute)
    }

    t.tokensPerMinute += int64(tokens)

    // Warn if approaching quota (80% of 120K = 96K)
    if t.tokensPerMinute > 96000 {
        log.Warnf("Approaching TPM quota: %d/120000", t.tokensPerMinute)
    }
}
```


## Testing

The Azure OpenAI client includes comprehensive tests:

```bash
# Run tests
cd /path/to/loom
go test ./pkg/llm/azureopenai

# With coverage (76.0%)
go test -cover ./pkg/llm/azureopenai

# With race detection
go test -race ./pkg/llm/azureopenai

# Verbose output
go test -v ./pkg/llm/azureopenai
```

**Expected Output**:

```
=== RUN   TestNewClient
=== RUN   TestNewClient/valid_config_with_API_key
=== RUN   TestNewClient/valid_config_with_Entra_token
=== RUN   TestNewClient/missing_endpoint
=== RUN   TestNewClient/missing_deployment
=== RUN   TestNewClient/missing_authentication
--- PASS: TestNewClient (0.00s)
...
PASS
coverage: 76.0% of statements
ok  	github.com/teradata-labs/loom/pkg/llm/azureopenai	0.156s
```


### Integration Testing

Test against real Azure OpenAI:

```go
func TestAzureOpenAI_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")
    deployment := os.Getenv("AZURE_OPENAI_DEPLOYMENT")
    apiKey := os.Getenv("AZURE_OPENAI_API_KEY")

    if endpoint == "" || deployment == "" || apiKey == "" {
        t.Skip("Azure OpenAI credentials not configured")
    }

    client, err := azureopenai.NewClient(azureopenai.Config{
        Endpoint:     endpoint,
        DeploymentID: deployment,
        APIKey:       apiKey,
    })
    require.NoError(t, err)

    ctx := context.Background()
    messages := []types.Message{
        {Role: "user", Content: "Say hello"},
    }

    resp, err := client.Chat(ctx, messages, nil)
    require.NoError(t, err)
    assert.NotEmpty(t, resp.Message)
    assert.Greater(t, resp.TokensUsed.Total, 0)
    assert.Greater(t, resp.CostUSD, 0.0)
}
```

**Run Integration Tests**:

```bash
export AZURE_OPENAI_ENDPOINT="https://myresource.openai.azure.com"
export AZURE_OPENAI_DEPLOYMENT="gpt-4o-prod"
export AZURE_OPENAI_API_KEY="your-api-key"

go test -v ./pkg/llm/azureopenai -run Integration
```


## Production Best Practices

### 1. Use Deployment Names Strategically

```go
// Good: Environment-specific deployments
deploymentID := fmt.Sprintf("gpt-4o-%s", environment)  // gpt-4o-prod, gpt-4o-dev

// Good: Model version in name for cost tracking
deploymentID := "gpt-4o-2024-05-13-prod"

// Bad: Generic names (can't infer model for cost tracking)
deploymentID := "my-deployment-1"
```


### 2. Implement Circuit Breakers

```go
// Use with Loom's builder API
agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAILLM(endpoint, deploymentID, apiKey).
    WithCircuitBreakers().  // Handles Azure outages gracefully
    Build()
```

**Circuit Breaker Behavior** (coming in v1.2.0):
- Opens after 5 consecutive failures
- Half-opens after 30 seconds to test recovery
- Prevents cascading failures


### 3. Monitor Quotas

Azure OpenAI has strict TPM quotas. Monitor usage:

```go
import "go.uber.org/zap"

// Track token usage
totalTokens += response.TokensUsed.Total
tokensPerMinute := calculateTPM(totalTokens, startTime)

if tokensPerMinute > tpmQuota * 0.8 {
    logger.Warn("Approaching TPM quota limit",
        zap.Int64("tokens_per_minute", tokensPerMinute),
        zap.Int64("quota", tpmQuota),
        zap.Float64("usage_pct", float64(tokensPerMinute)/float64(tpmQuota)*100),
    )
}
```

**Expected Log Output**:

```json
{
  "level": "warn",
  "msg": "Approaching TPM quota limit",
  "tokens_per_minute": 98456,
  "quota": 120000,
  "usage_pct": 82.05
}
```


### 4. Use Regional Deployments

Deploy models in multiple regions for resilience:

```go
type RegionalClient struct {
    regions []RegionConfig
}

type RegionConfig struct {
    Name     string
    Endpoint string
    Client   *azureopenai.Client
}

func (r *RegionalClient) ChatWithFailover(ctx context.Context, messages []types.Message) (*types.Response, error) {
    for _, region := range r.regions {
        resp, err := region.Client.Chat(ctx, messages, nil)
        if err == nil {
            return resp, nil
        }

        log.Warnf("Region %s failed: %v, trying next", region.Name, err)
    }

    return nil, fmt.Errorf("all regions failed")
}

// Create regional clients
regional := &RegionalClient{
    regions: []RegionConfig{
        {
            Name:     "East US",
            Endpoint: "https://eastus-resource.openai.azure.com",
            Client:   client1,
        },
        {
            Name:     "West Europe",
            Endpoint: "https://westeurope-resource.openai.azure.com",
            Client:   client2,
        },
        {
            Name:     "Japan East",
            Endpoint: "https://japaneast-resource.openai.azure.com",
            Client:   client3,
        },
    },
}
```

**Available Regions** (as of January 2025):
- East US
- East US 2
- West Europe
- France Central
- UK South
- Sweden Central
- Australia East
- Japan East
- Canada East


### 5. Secure API Keys

Never hardcode API keys:

```go
// ❌ Bad - Hardcoded
apiKey := "abc123..."

// ✅ Good - Environment variable
apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
if apiKey == "" {
    return fmt.Errorf("AZURE_OPENAI_API_KEY not set")
}

// ✅ Better - Azure Key Vault
import "github.com/Azure/azure-sdk-for-go/sdk/keyvault/azsecrets"

func getAPIKeyFromVault(ctx context.Context) (string, error) {
    client, err := azsecrets.NewClient("https://myvault.vault.azure.net/", cred, nil)
    if err != nil {
        return "", err
    }

    secret, err := client.GetSecret(ctx, "openai-api-key", "", nil)
    if err != nil {
        return "", err
    }

    return *secret.Value, nil
}

// ✅ Best - Managed Identity (no secrets at all)
agent, err := builder.NewAgentBuilder().
    WithAzureOpenAIEntraAuth(endpoint, deploymentID, entraToken).
    Build()
```


### 6. Log for Observability

```go
import (
    "go.uber.org/zap"
    "github.com/teradata-labs/loom/pkg/observability"
)

logger, _ := zap.NewProduction()
tracer := observability.NewHawkTracer(hawkConfig)

// Log requests
logger.Info("Azure OpenAI request",
    zap.String("deployment", deploymentID),
    zap.Int("input_tokens", len(messages)),
    zap.String("session_id", sessionID),
)

// Trace execution
ctx, span := tracer.StartSpan(ctx, "azure_openai_chat")
defer tracer.EndSpan(span)

response, err := client.Chat(ctx, messages, tools)

// Log response
logger.Info("Azure OpenAI response",
    zap.Int("output_tokens", response.TokensUsed.Output),
    zap.Float64("cost_usd", response.CostUSD),
    zap.Duration("latency", time.Since(startTime)),
)
```


### 7. Handle Streaming Properly

```go
stream, err := client.StreamChat(ctx, messages, tools)
if err != nil {
    return err
}

for chunk := range stream {
    if chunk.Error != nil {
        log.Errorf("Stream error: %v", chunk.Error)
        break
    }

    fmt.Print(chunk.Delta)
}
```


## Comparison with Other Providers

| Feature | Azure OpenAI | OpenAI Direct | AWS Bedrock |
|---------|--------------|---------------|-------------|
| **Deployment** | Azure regions | Global | AWS regions |
| **Authentication** | API key / Entra ID | API key only | IAM |
| **Model Access** | Deployment-based | Model name | Model ID |
| **Compliance** | Azure certifications (ISO 27001, SOC 2, HIPAA) | SOC 2 | AWS certifications |
| **VNet Integration** | Yes (Private Endpoint) | No | Yes (VPC Endpoint) |
| **Cost** | Pay-as-you-go / Provisioned | Credits / Pay-as-you-go | Pay-as-you-go |
| **Rate Limits** | TPM quotas per deployment | Per-org rate limits | Per-model rate limits |
| **Tool Calling** | ✅ | ✅ | ⚠️ (Claude only) |
| **Streaming** | ✅ | ✅ | ✅ |
| **Vision** | ✅ (GPT-4o, GPT-4 Turbo) | ✅ | ⚠️ (Claude only) |
| **Region Options** | 9+ regions | N/A | 10+ regions |
| **SLA** | 99.9% (with SLA) | 99.9% | 99.99% (with SLA) |


## Troubleshooting

### Common Issues

#### 1. "404 Deployment Not Found"

**Symptoms**:
```
Error: Azure OpenAI API error: Deployment 'gpt-4o-deployment' not found (type: invalid_request_error)
```

**Causes**:
- Deployment name is incorrect or doesn't exist
- Using wrong Azure resource/endpoint
- Deployment is in different region

**Resolution**:
```bash
# List all deployments
az cognitiveservices account deployment list \
  --name myresource \
  --resource-group mygroup \
  --query "[].{Name:name, Model:properties.model.name, Status:properties.provisioningState}"

# Verify endpoint
az cognitiveservices account show \
  --name myresource \
  --resource-group mygroup \
  --query properties.endpoint
```


#### 2. "401 Unauthorized"

**Symptoms**:
```
Error: API error (status 401): {"error": {"message": "Access denied due to invalid subscription key."}}
```

**Causes**:
- API key is invalid or regenerated
- Entra token is expired (1 hour TTL)
- Wrong Azure subscription or resource

**Resolution**:
```bash
# Regenerate API key
az cognitiveservices account keys regenerate \
  --name myresource \
  --resource-group mygroup \
  --key-name key1

# Get new Entra token
az account get-access-token \
  --resource https://cognitiveservices.azure.com \
  --query accessToken
```


#### 3. "429 Rate Limit Exceeded"

**Symptoms**:
```
Error: API error (status 429): {"error": {"message": "Requests to the ChatCompletions_Create Operation... have exceeded token rate limit..."}}
```

**Causes**:
- Exceeded TPM quota (e.g., 120K TPM for Standard)
- Burst traffic beyond sustained capacity
- Multiple clients using same deployment

**Resolution**:
```bash
# Increase TPM quota (Azure Portal)
# Model deployments → Select deployment → Edit → Tokens Per Minute

# Or create additional deployment for load balancing
az cognitiveservices account deployment create \
  --name myresource \
  --resource-group mygroup \
  --deployment-name gpt-4o-prod-2 \
  --model-name gpt-4o \
  --model-version "2024-05-13" \
  --sku-capacity 120 \
  --sku-name "Standard"
```


#### 4. "500 Internal Server Error"

**Symptoms**:
```
Error: API error (status 500): {"error": {"message": "The service is currently unavailable."}}
```

**Causes**:
- Azure OpenAI service outage
- Regional issues
- Capacity issues (rare)

**Resolution**:
1. Check Azure Status: https://status.azure.com/
2. Implement retry with exponential backoff
3. Failover to alternate region deployment
4. Monitor Azure Service Health in Portal


### Debug Logging

Enable debug logging to see API requests:

```go
import (
    "net/http"
    "net/http/httputil"
)

// Custom HTTP client with logging
type loggingTransport struct {
    Transport http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    // Log request
    reqDump, _ := httputil.DumpRequestOut(req, true)
    log.Printf("Request:\n%s\n", reqDump)

    // Execute request
    resp, err := t.Transport.RoundTrip(req)

    // Log response
    if resp != nil {
        respDump, _ := httputil.DumpResponse(resp, true)
        log.Printf("Response:\n%s\n", respDump)
    }

    return resp, err
}

// Create client with logging transport
httpClient := &http.Client{
    Transport: &loggingTransport{
        Transport: http.DefaultTransport,
    },
}
```

**Expected Debug Output**:

```
Request:
POST /openai/deployments/gpt-4o-prod/chat/completions?api-version=2024-10-21 HTTP/1.1
Host: myresource.openai.azure.com
api-key: sk-...
Content-Type: application/json

{"messages":[{"role":"user","content":"Hello"}],"max_tokens":4096,"temperature":1.0}

Response:
HTTP/2.0 200 OK
Content-Type: application/json
...

{"id":"chatcmpl-123","object":"chat.completion","created":1234567890,...}
```


## Migration from OpenAI

Migrating from OpenAI to Azure OpenAI is straightforward:

### Before (OpenAI Direct)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithOpenAILLM(
        openaiKey,       // OpenAI API key
        "gpt-4o",       // Model name
    ).
    Build()
```


### After (Azure OpenAI)

```go
import "github.com/teradata-labs/loom/pkg/builder"

agent, err := builder.NewAgentBuilder().
    WithBackend(backend).
    WithAzureOpenAILLM(
        "https://myresource.openai.azure.com",  // Azure endpoint
        "gpt-4o-deployment",                    // Deployment name
        azureKey,                              // Azure API key
    ).
    Build()
```


### Key Differences

| Aspect | OpenAI Direct | Azure OpenAI |
|--------|---------------|--------------|
| **Endpoint** | `api.openai.com` | `{resource}.openai.azure.com` |
| **Model Selector** | Model name (`gpt-4o`) | Deployment name (`gpt-4o-prod`) |
| **Authentication** | API key only | API key or Entra ID |
| **URL Format** | `/v1/chat/completions` | `/openai/deployments/{id}/chat/completions?api-version=...` |
| **Rate Limits** | Per-organization | Per-deployment (TPM quotas) |
| **Regions** | Global (single endpoint) | Regional (multiple endpoints) |
| **Compliance** | SOC 2 | Azure certifications (ISO, HIPAA, etc.) |
| **Cost Tracking** | Automatic (model name in request) | Requires model name inference from deployment |


### Migration Checklist

- [ ] Create Azure OpenAI resource in Azure Portal
- [ ] Deploy models with deployment names
- [ ] Update endpoint URL in configuration
- [ ] Update authentication (API key or Entra ID)
- [ ] Change model name to deployment name
- [ ] Test connectivity and authentication
- [ ] Monitor TPM quotas (different from OpenAI rate limits)
- [ ] Update retry logic for Azure-specific errors
- [ ] Configure regional failover (optional)
- [ ] Update logging/observability for Azure-specific fields

The message format, tool calling, and response structure remain identical.


## See Also

### LLM Provider Documentation
- [LLM Provider Overview](./llm-providers.md) - All supported LLM providers
- [OpenAI Integration](./llm-openai.md) - Direct OpenAI API integration
- [AWS Bedrock Integration](./llm-bedrock.md) - Alternative enterprise option
- [Ollama Integration](./llm-ollama.md) - Local/on-premise models

### Integration Guides
- [Agent Configuration](./agent-configuration.md) - Complete agent setup
- [Builder API Reference](../guides/builder-api.md) - Programmatic agent creation
- [Cost Tracking Guide](../guides/cost-tracking.md) - Monitor LLM costs

### External Resources
- [Azure OpenAI Documentation](https://learn.microsoft.com/azure/ai-services/openai/)
- [Azure OpenAI Pricing](https://azure.microsoft.com/pricing/details/cognitive-services/openai-service/)
- [Azure OpenAI Quotas](https://learn.microsoft.com/azure/ai-services/openai/quotas-limits)
- [Azure CLI Reference](https://learn.microsoft.com/cli/azure/cognitiveservices)
