
# Ollama Local LLM Integration

Technical reference for connecting Loom to Ollama for local LLM inference.

**Version**: v1.2.0


## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Overview](#overview)
3. [Prerequisites](#prerequisites)
4. [Installation](#installation)
5. [Installing Models](#installing-models)
6. [Configuration](#configuration)
7. [Testing Your Setup](#testing-your-setup)
8. [Tool Calling Support](#tool-calling-support)
9. [Streaming Support](#streaming-support)
10. [Vision Support](#vision-support)
11. [Performance Considerations](#performance-considerations)
12. [Model Selection Guide](#model-selection-guide)
13. [Dynamic Model Discovery](#dynamic-model-discovery)
14. [Advanced Configuration](#advanced-configuration)
15. [Comparison: Ollama vs Cloud LLMs](#comparison-ollama-vs-cloud-llms)
16. [Error Handling](#error-handling)
17. [Best Practices](#best-practices)
18. [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
llm:
  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.1:8b
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

**Note**: The tool mode (`auto`, `native`, or `prompt`) is configurable only via the Go API (`ollama.Config.ToolMode`), not through YAML config. It defaults to `auto`.

### Popular Models

| Model | Size | Context | Best For | Pull Command |
|-------|------|---------|----------|--------------|
| `llama3.3:70b` | 70B | 128k | Latest Meta model, excellent quality | `ollama pull llama3.3:70b` |
| `llama3.1:8b` | 8B | 128k | General purpose (server default) | `ollama pull llama3.1:8b` |
| `llama3.2` | 3B | 128k | Lightweight general purpose | `ollama pull llama3.2` |
| `qwen3:8b` | 8B | 128k | Strong reasoning, tool use | `ollama pull qwen3:8b` |
| `qwen3-coder:30b` | 30B | 128k | Code generation, large context | `ollama pull qwen3-coder:30b` |
| `qwen2.5-coder` | 7B/32B | 32k | Code generation | `ollama pull qwen2.5-coder` |
| `mistral-small3.1` | 24B | 128k | Instruction following, tool use | `ollama pull mistral-small3.1` |
| `deepseek-r1:7b` | 7B | 32k | Advanced reasoning | `ollama pull deepseek-r1:7b` |
| `phi4` | 14B | 16k | Microsoft's reasoning model | `ollama pull phi4` |

### Tool Calling Modes

| Mode | Behavior | When to Use | Ollama Version |
|------|----------|-------------|----------------|
| `auto` | Probe model via `/api/show`, fallback to known-models list | **Recommended** - Works with any model | Any |
| `native` | Force native tool calling API | When model supports it | v0.12.3+ |
| `prompt` | Prompt-based workaround | Older Ollama or unsupported models | Any |

### Hardware Requirements

| Model Size | RAM | GPU VRAM | Speed (tokens/sec) |
|------------|-----|----------|-------------------|
| 3B-7B | 8GB | 6GB+ | 20-50 (GPU), 5-10 (CPU) |
| 13B | 16GB | 10GB+ | 15-30 (GPU), 2-5 (CPU) |
| 70B | 64GB | 40GB+ | 10-20 (GPU), <1 (CPU) |

### Common Commands

```bash
# Installation
brew install ollama              # macOS
curl -fsSL https://ollama.com/install.sh | sh  # Linux

# Server management
ollama serve                     # Start server
brew services restart ollama     # macOS: restart service

# Model management
ollama list                      # List installed models
ollama pull llama3.1             # Download model
ollama run llama3.1 "test"       # Test model
ollama show llama3.1             # Model details
ollama rm llama3.1               # Remove model

# Testing
curl http://localhost:11434/api/tags  # Verify server running
ollama --version                 # Check version (need v0.12.3+ for native tools)
```


## Overview

Ollama provides local LLM inference on your machine:
- **Free**: No API costs
- **Private**: Data never leaves your machine
- **Fast**: Low-latency local inference
- **Offline**: Works without internet connection
- **Flexible**: Support for many open-source models
- **Streaming**: ✅ Implements `StreamingLLMProvider` for token-by-token output
- **Vision**: ✅ Supports multi-modal content (base64 images) for vision-capable models
- **Rate Limiting**: ✅ Client-side rate limiting via `RateLimiterConfig`

**Use Ollama when**:
- You want zero inference costs
- You need complete data privacy
- You're developing/testing offline
- You want to experiment with different models
- You have sufficient local compute (GPU recommended)


## Prerequisites

1. **Ollama Installed**: Download from [ollama.com](https://ollama.com)
2. **Sufficient Hardware**:
   - **Minimum**: 8GB RAM, CPU inference
   - **Recommended**: 16GB+ RAM, NVIDIA/AMD GPU
3. **Model Downloaded**: At least one model pulled locally


## Installation

### Installing Ollama

**macOS**:
```bash
brew install ollama
```

**Linux**:
```bash
curl -fsSL https://ollama.com/install.sh | sh
```

**Windows**:
Download installer from [ollama.com/download](https://ollama.com/download)

### Starting Ollama Server

```bash
# Start Ollama server (runs in background)
ollama serve

# Server will listen on http://localhost:11434
```

**Note**: On macOS, Ollama may auto-start as a service.

**Verify server is running**:
```bash
curl http://localhost:11434/api/tags
```

Expected output:
```json
{
  "models": [
    {"name": "llama3.1:latest", "size": 4661211648, ...}
  ]
}
```


## Installing Models

Pull models before using them:

```bash
# Recommended: Llama 3.1 8B (fast, good quality)
ollama pull llama3.1

# Strong reasoning and tool use
ollama pull qwen3:8b

# Code-focused models
ollama pull qwen3-coder:30b
ollama pull qwen2.5-coder

# Instruction following with tool support
ollama pull mistral-small3.1

# Larger models (require more RAM/GPU):
ollama pull llama3.3:70b
```

Verify models are installed:
```bash
ollama list
```

Expected output:
```
NAME                ID              SIZE      MODIFIED
llama3.1:latest     42182419e950    4.7 GB    2 hours ago
mistral:latest      f974a74358d6    4.1 GB    3 days ago
```


## Configuration

### Basic Setup

No authentication needed for local Ollama!

Edit `$LOOM_DATA_DIR/looms.yaml`:

```yaml
llm:
  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.1:8b
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60
```

**Note**: Tool mode is configurable only via the Go API (`ollama.Config.ToolMode`), not YAML. Defaults to `auto`.

### Configuration Parameters

#### provider

**Type**: `string`
**Required**: Yes
**Value**: `ollama`

Set this to `ollama` to use Ollama local inference.


#### ollama_endpoint

**Type**: `string` (URL)
**Default**: `http://localhost:11434`
**Required**: No

Ollama server endpoint.

**Local server**:
```yaml
ollama_endpoint: http://localhost:11434
```

**Remote server**:
```yaml
ollama_endpoint: http://192.168.1.100:11434
```


#### ollama_model

**Type**: `string`
**Default**: `llama3.1:8b` (server config default); `llama3.1` (Go API `ollama.Config` default)
**Required**: No (defaults to `llama3.1:8b` in server config)

Model name to use. Must be pulled locally first.

**Examples**:
- `llama3.1` - Default tag (`:latest`)
- `llama3.1:70b` - Specific size
- `llama3.1:7b-q4_0` - Quantized version
- `qwen2.5-coder:7b` - Code-focused model

**See**: [Available Models](#available-models) for full list


#### temperature

**Type**: `float64`
**Default**: `1.0` (server config default); `0.8` (Go API `ollama.Config` default)
**Range**: `0.0` - `1.0` (Loom agent-level validation enforces this range; Ollama's API accepts up to 2.0 but Loom rejects values above 1.0)
**Required**: No

Sampling temperature for creativity control.

**Temperature guide**:
- **0.0-0.3**: Deterministic, focused responses
- **0.7-1.0**: Balanced creativity (recommended)

**Example**:
```yaml
temperature: 0.7  # Balanced
```


#### max_tokens

**Type**: `int`
**Default**: Model-aware (see below)
**Range**: `1` - model's context window
**Required**: No

Maximum response length in tokens. If not set, Loom selects a default based on model name substrings:

- **70B+**: 8192 — names containing `70b`, `72b`, or `405b` (e.g., `llama3.3:70b`, `qwen2.5:72b`)
- **13B-34B**: 6144 — names containing `13b`, `14b`, `20b`, `30b`, `32b`, or `34b` (e.g., `qwen3-coder:30b`, `phi4:14b`)
- **Default**: 4096 — all other models (e.g., `llama3.1`, `qwen3:8b`, `mistral`, `phi4`, `mistral-small3.1`)

**Note**: Size detection is based on literal substrings in the model name. Models like `phi4` (14B actual) or `mistral-small3.1` (24B actual) get the default 4096 unless you specify the size tag explicitly (e.g., `phi4:14b`).

**Constraints**:

- Cannot exceed model's context window (typically 8k-128k)
- Larger values increase latency and memory usage
- Reduce for faster responses on CPU

**Example**:
```yaml
max_tokens: 2048  # Faster responses
```


#### timeout_seconds

**Type**: `int`
**Default**: `60` (server config default); `120` (Go API `ollama.Config` default)
**Range**: `1` - `3600`
**Required**: No

Request timeout in seconds.

**Recommendations**:
- **CPU inference**: 300s (5 minutes)
- **GPU inference**: 120s (2 minutes)
- **Small models (<7B)**: 60s (1 minute)

**Example**:
```yaml
timeout_seconds: 300  # 5 minutes for CPU
```


#### Tool Mode (Go API Only)

**Type**: `ollama.ToolMode` (enum)
**Default**: `auto`
**Allowed values**: `auto`, `native`, `prompt`
**Available since**: v0.7.0

Tool calling mode for Ollama. This is configurable only via the Go API (`ollama.Config.ToolMode`), not through YAML config.

| Mode | Behavior | When to Use |
|------|----------|-------------|
| `auto` | Probe model template via `/api/show`, fallback to known-models list | **Recommended** - Works with any model |
| `native` | Force native tool calling API | When you know model supports it (Ollama v0.12.3+) |
| `prompt` | Prompt-based tool calling fallback | For older Ollama or unsupported models |

**Example (Go API)**:
```go
client := ollama.NewClient(ollama.Config{
    Endpoint: "http://localhost:11434",
    Model:    "llama3.1",
    ToolMode: ollama.ToolModeAuto, // Recommended
})
```

**See**: [Tool Calling Support](#tool-calling-support) and [Dynamic Model Discovery](#dynamic-model-discovery) for details


### Available Models

Loom dynamically discovers installed Ollama models via `/api/tags`. Any model you pull will appear in the model registry automatically. Below are popular models verified to work with Loom:

| Model | Size | Context | Best For | Pull Command |
|-------|------|---------|----------|--------------|
| **Llama 3.3** | 70B | 128k | Latest Meta model, excellent quality | `ollama pull llama3.3:70b` |
| **Llama 3.1** | 8B | 128k | General purpose, reliable | `ollama pull llama3.1` |
| **Qwen 3** | 8B | 128k | Strong reasoning, tool use | `ollama pull qwen3:8b` |
| **Qwen 3 Coder** | 30B | 128k | Code generation, large context | `ollama pull qwen3-coder:30b` |
| **Qwen 2.5 Coder** | 7B/32B | 32k | Code generation & understanding | `ollama pull qwen2.5-coder` |
| **Mistral Small 3.1** | 24B | 128k | Instruction following, tool use | `ollama pull mistral-small3.1` |
| **DeepSeek-R1** | 7B/70B | 32k | Advanced reasoning, math & logic | `ollama pull deepseek-r1:7b` |
| **Command-R** | 35B | 128k | RAG, tool use | `ollama pull command-r` |
| **Mixtral** | 8x7B (47B total) | 32k | Mixture of experts | `ollama pull mixtral:8x7b` |
| **Phi-4** | 14B | 16k | Microsoft's reasoning model | `ollama pull phi4` |

See [Ollama Model Library](https://ollama.com/library) for all available models. Any model you pull is automatically detected by Loom — no configuration changes needed.


## Testing Your Setup

### Step 1: Verify Ollama Server

```bash
curl http://localhost:11434/api/tags
```

**Expected output**:
```json
{
  "models": [
    {"name": "llama3.1:latest", "size": 4661211648, ...}
  ]
}
```

If connection fails, see [Error Handling](#error-handling).


### Step 2: Start Loom Server

```bash
looms serve
```

**Expected output**:
```
INFO  Starting Loom server
INFO  gRPC server listening on :60051
INFO  HTTP gateway listening on :5006
INFO  LLM provider: ollama (endpoint: http://localhost:11434)
INFO  Model: llama3.1
```


### Step 3: Test with gRPC

```bash
grpcurl -plaintext -d '{"query": "What is 2+2?"}' \
  localhost:60051 loom.v1.LoomService/Weave
```

**Expected output** (fields shown in proto JSON camelCase):
```json
{
  "text": "2 + 2 equals 4.",
  "sessionId": "sess_abc123",
  "cost": {
    "llmCost": {
      "inputTokens": 10,
      "outputTokens": 8,
      "costUsd": 0,
      "model": "llama3.1",
      "provider": "ollama"
    }
  }
}
```

**Note**: `costUsd: 0` - Ollama is free!


## Tool Calling Support

### Native Tool Calling (Ollama v0.12.3+)

**Native tool calling is now supported!**

Loom supports Ollama's native tool calling API (requires Ollama v0.12.3 or later).

**Tool support is detected dynamically.** Loom probes Ollama's `/api/show` endpoint to check if a model's template includes tool-handling directives. Any Ollama model with tool support in its template will work automatically — no hardcoded model list required.

**Known-working model families** (used as fallback when probe fails; matched by prefix):

- **llama3.1**, **llama3.2**, **llama3.3** - Full native tool calling
- **qwen2.5**, **qwen2.5-coder**, **qwen3**, **qwen3-coder** - Excellent tool calling
- **mistral**, **mistral-small**, **mixtral** - Native tool calling (also matches `mistral-small3.1` etc. via prefix)
- **deepseek-r1** - Tool calling with reasoning
- **command-r** - Tool calling support
- **phi4** - Microsoft's reasoning model
- **functionary** - Specialized for function calling

### Tool Mode Configuration

Configure how Loom handles tools with Ollama. Tool mode is set via the Go API (`ollama.Config.ToolMode`), not YAML config:

```go
client := ollama.NewClient(ollama.Config{
    Model:    "llama3.3",
    ToolMode: ollama.ToolModeAuto, // Recommended: auto-detect support
})
```

**Tool Mode Options**:

| Mode | Behavior | When to Use |
|------|----------|-------------|
| `auto` | Probe model template, then fallback to known-models list | **Recommended** - Works with any model |
| `native` | Force native tool calling API | When you know your model supports it (Ollama v0.12.3+) |
| `prompt` | Use prompt-based tool calling | For older Ollama versions or unsupported models |

**How `auto` mode works**:

1. On first tool call, Loom queries Ollama's `/api/show` for the model's template
2. If the template contains tool-handling directives (e.g., `{{ .Tools }}`), native tools are enabled
3. If the probe fails (e.g., Ollama unreachable), falls back to a static known-models list
4. Result is cached for the client's lifetime — no repeated probes


### Checking Tool Support

To verify your Ollama version supports native tools:

```bash
# Check Ollama version (need v0.12.3+)
ollama --version
```

**Expected output**:
```
ollama version is 0.12.3
```

**Test tool calling with a model**:
```bash
ollama run llama3.3 "Use the calculator tool to compute 2+2"
```


### Tool Calling Examples

**Automatic Detection** (recommended):
```go
client := ollama.NewClient(ollama.Config{
    Model:    "llama3.3",
    ToolMode: ollama.ToolModeAuto, // Detects native support
})
```

**Force Native Mode** (Ollama v0.12.3+):
```go
client := ollama.NewClient(ollama.Config{
    Model:    "qwen2.5",
    ToolMode: ollama.ToolModeNative, // Use native API
})
```

**Fallback to Prompt Mode** (older Ollama):
```go
client := ollama.NewClient(ollama.Config{
    Model:    "llama3.1",
    ToolMode: ollama.ToolModePrompt, // Prompt engineering workaround
})
```


### Upgrading for Tool Support

If you have an older Ollama version:

```bash
# macOS
brew upgrade ollama

# Linux
curl -fsSL https://ollama.com/install.sh | sh

# Verify version
ollama --version  # Should be v0.12.3 or later

# Update your models
ollama pull llama3.3
ollama pull qwen2.5
```


### Model Recommendations for Tools

| Model | Tool Support | Quality | Speed | Recommendation |
|-------|--------------|---------|-------|----------------|
| **Llama 3.3 70B** | Excellent | Excellent | Medium | Best overall quality |
| **Qwen 3 8B** | Excellent | Very Good | Fast | **Recommended** for most use cases |
| **Qwen 3 Coder 30B** | Excellent | Excellent | Medium | Best for code tasks |
| **Mistral Small 3.1** | Excellent | Very Good | Medium | Strong instruction following |
| **Llama 3.1 8B** | Good | Good | Very Fast | Good for development |
| **DeepSeek-R1** | Good | Excellent | Slow | Best for reasoning tasks |


## Streaming Support

The Ollama client implements the `StreamingLLMProvider` interface, enabling token-by-token streaming via Ollama's `/api/chat` endpoint with `stream: true`.

**How it works**:
1. Loom sends the chat request with `stream: true`
2. Ollama returns newline-delimited JSON (NDJSON) — one JSON object per token
3. Each token is passed to the `tokenCallback` as it arrives
4. The final chunk (with `done: true`) contains usage metadata (`prompt_eval_count`, `eval_count`)

Streaming is used automatically when the client is accessed via `StreamWeave` gRPC RPC or SSE endpoints.


## Vision Support

The Ollama client supports multi-modal content blocks for vision-capable models (e.g., `llava`, `llama3.2-vision`).

**How it works**:
- User messages with `ContentBlocks` containing `type: "image"` are converted to Ollama's `images` array format
- Images must be base64-encoded in the `Image.Source.Data` field
- Text and image blocks are combined into a single Ollama message

**Note**: Vision support depends on the Ollama model — only models trained for vision tasks will process images.


## Performance Considerations

### Hardware Requirements

| Model Size | RAM | GPU VRAM | Speed (tokens/sec) |
|------------|-----|----------|-------------------|
| 3B-7B | 8GB | 6GB+ | 20-50 (GPU), 5-10 (CPU) |
| 13B | 16GB | 10GB+ | 15-30 (GPU), 2-5 (CPU) |
| 70B | 64GB | 40GB+ | 10-20 (GPU), <1 (CPU) |

**GPU strongly recommended** for models >7B parameters.

**Platform support**:
- **NVIDIA**: Install CUDA for GPU acceleration
- **AMD**: Install ROCm for GPU acceleration
- **Apple Silicon**: Automatic via Metal


### Optimization Tips

#### 1. Use GPU Acceleration

**NVIDIA**:
```bash
# Install CUDA
# See: https://developer.nvidia.com/cuda-downloads

# Verify GPU detected
nvidia-smi
```

**AMD**:
```bash
# Install ROCm
# See: https://rocmdocs.amd.com/en/latest/
```

**Apple Silicon**:
```bash
# No installation needed - Metal automatic
# Verify:
system_profiler SPDisplaysDataType
```


#### 2. Adjust Context Window

Reduce `max_tokens` for faster responses:

```yaml
max_tokens: 2048  # Half the default, 2x faster
```

**Trade-off**: Shorter responses, less context retention


#### 3. Use Quantized Models

Quantization reduces model size and speeds inference:

```bash
# 4-bit quantization (faster, less VRAM, lower quality)
ollama pull llama3.1:7b-q4_0

# 8-bit quantization (balanced quality/speed)
ollama pull llama3.1:7b-q8_0
```

**Quantization levels**:
- `q4_0`: 4-bit, ~50% speed boost, ~10% quality loss
- `q8_0`: 8-bit, ~20% speed boost, ~5% quality loss


#### 4. Keep Model in Memory

Pre-load model to avoid cold start latency:

```bash
# Pre-load model with 24-hour keepalive
ollama run llama3.1 "test" --keepalive 24h
```

**Memory usage**: Model stays in RAM/VRAM for 24 hours


### Cost Estimation

**Ollama is free!**

All inference runs locally - no API costs. Your only costs are:
- **Electricity**: Minimal on modern hardware (~50-200W for GPU)
- **Hardware**: One-time cost for GPU (optional but recommended)

**Cost comparison**:
| Provider | Model | Cost per 1M tokens |
|----------|-------|-------------------|
| Ollama | Llama 3.1 8B | $0 |
| Ollama | Qwen 2.5 7B | $0 |
| Anthropic | Claude Sonnet 4.5 | $3-$15 |
| OpenAI | GPT-4o | $2.50-$10 |


## Model Selection Guide

### By Use Case

| Use Case | Recommended Model | Why |
|----------|------------------|-----|
| Development/Testing | `llama3.1` (8B) | Fast, good quality, low resource usage |
| General purpose | `qwen3:8b` | Strong reasoning, tool use, fast |
| Code Generation | `qwen3-coder:30b` or `qwen2.5-coder` | Optimized for code understanding |
| Math & Reasoning | `deepseek-r1` | Advanced reasoning capabilities |
| Production/Quality | `llama3.3:70b` | Latest Meta model, excellent quality |
| Instruction following | `mistral-small3.1` | Strong tool support, balanced size |
| Balanced performance | `phi4` | Good quality, moderate size |


### By Hardware

| Hardware | Recommended Model |
|----------|------------------|
| Laptop (8GB RAM, no GPU) | `llama3.1:7b-q4_0` or `qwen3:8b` |
| Desktop (16GB RAM, no GPU) | `llama3.1`, `qwen3:8b`, or `mistral-small3.1` |
| Desktop (NVIDIA RTX 3060) | `phi4`, `qwen3:8b`, or `deepseek-r1:7b` |
| Workstation (RTX 4090) | `llama3.3:70b`, `qwen3-coder:30b`, or `deepseek-r1:70b` |
| Mac M1/M2 (16GB+) | `llama3.1`, `qwen3:8b`, or `phi4` |
| Mac M3/M4 Pro (32GB+) | `qwen3-coder:30b` or `mistral-small3.1` |


## Dynamic Model Discovery

Loom automatically discovers your installed Ollama models — no manual registry updates needed.

### How It Works

1. **Model Registry**: At startup, Loom queries Ollama's `/api/tags` endpoint to discover all installed models
2. **Tool Support Probe**: On first tool call, Loom queries `/api/show` for the model's template to detect tool support
3. **Fallback**: If Ollama is unreachable, Loom uses a static list of known-working models
4. **Max Tokens**: Automatically scaled based on model name substrings (default→4096, names containing 13b-34b→6144, 70b+→8192; see [max_tokens](#max_tokens) for details)

### What This Means

- **Pull any model** — it appears in Loom automatically
- **Tool support detected at runtime** — no hardcoded model allowlists
- **New model families work immediately** — no Loom update required
- **Graceful degradation** — static fallbacks if Ollama probe fails

### Verifying Discovered Models

```bash
# See what Ollama has installed (same data Loom discovers)
ollama list

# Check if a model's template supports tools
ollama show qwen3:8b --template
# Look for {{ .Tools }} or similar directives
```


## Advanced Configuration

### Custom Ollama Endpoint

Running Ollama on a different machine:

```yaml
ollama_endpoint: http://192.168.1.100:11434  # Remote Ollama server
```

**Firewall note**: Ensure port 11434 is accessible from Loom server.


### Ollama with GPU Containers

Using Docker with GPU:

```bash
# Run Ollama in Docker with GPU
docker run -d --gpus all -v ollama:/root/.ollama \
  -p 11434:11434 --name ollama ollama/ollama

# Pull model
docker exec -it ollama ollama pull llama3.1

# Verify
curl http://localhost:11434/api/tags
```

**Requirements**:
- Docker 19.03+
- NVIDIA Container Toolkit installed


### Multiple Ollama Instances

Run multiple Ollama servers for different models:

```bash
# Server 1: Fast small model on port 11434
OLLAMA_HOST=0.0.0.0:11434 ollama serve &

# Server 2: Large model on port 11435
OLLAMA_HOST=0.0.0.0:11435 ollama serve &
```

Configure per-agent Ollama endpoints using k8s-style agent YAML configs:

```yaml
# agents/fast-agent.yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: fast-agent
spec:
  llm:
    provider: ollama
    model: llama3.1
  # Uses default ollama_endpoint from looms.yaml (http://localhost:11434)
```

```yaml
# agents/quality-agent.yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: quality-agent
spec:
  llm:
    provider: ollama
    model: llama3.3:70b
  # To use a different Ollama instance, configure the server-level
  # ollama_endpoint in looms.yaml or use the Go API directly
```

**Note**: Per-agent Ollama endpoint overrides are available via the Go API (`ollama.Config.Endpoint`) but not via per-agent YAML config. Agent YAML configs inherit the server's `llm.ollama_endpoint`.


### Ollama Model Parameters

Advanced model parameters via Ollama API:

```bash
# View model configuration
ollama show llama3.1

# Adjust model parameters (requires Ollama API)
# Loom uses temperature and max_tokens from config
```

**Note**: Loom controls temperature and max_tokens. Advanced Ollama parameters (like `top_p`, `top_k`) not currently exposed via YAML config.


### Rate Limiting (Go API Only)

The Ollama client supports client-side rate limiting via the `RateLimiterConfig` field in `ollama.Config`. This is a global singleton shared across all Ollama client instances.

```go
client := ollama.NewClient(ollama.Config{
    Model: "llama3.1",
    RateLimiterConfig: llm.RateLimiterConfig{
        Enabled:           true,
        RequestsPerSecond: 2.0,
        // See llm.RateLimiterConfig for all fields
    },
})
```

Rate limiting is typically unnecessary for local Ollama (no API rate limits), but useful when running a shared Ollama server to prevent overload.


## Comparison: Ollama vs Cloud LLMs

| Feature | Ollama | Anthropic/Bedrock |
|---------|--------|-------------------|
| **Cost** | Free (hardware only) | $3-$15 per 1M tokens |
| **Latency** | Low (local) | Medium (network) |
| **Privacy** | 100% private | Data sent to provider |
| **Quality** | Good (8B), Excellent (70B+) | Excellent |
| **Tool Calling** | Native (v0.12.3+) | Native, reliable |
| **Streaming** | ✅ Token-by-token | ✅ Token-by-token |
| **Vision** | ✅ (model-dependent) | ✅ |
| **Hardware Required** | Yes (GPU recommended) | No |
| **Internet Required** | No | Yes |
| **Rate Limits** | None (local) | Yes (varies by tier) |
| **Context Window** | 8k-128k (model-dependent) | 200k+ |

**Choose Ollama for**: Development, privacy, cost savings, offline work, local inference
**Choose Cloud for**: Production at scale, managed infrastructure, no hardware costs


## Error Handling

The Ollama client does not define custom error codes. All errors are returned as generic `fmt.Errorf` messages wrapping the underlying cause. Common error patterns:

| Error Message Pattern | Likely Cause | Resolution |
|-----------------------|--------------|------------|
| `ollama API call failed: HTTP request failed: ... connection refused` | Ollama server not running | Run `ollama serve` or `brew services restart ollama` |
| `ollama API call failed: API error (status 404): ...` | Model not pulled locally | Run `ollama pull <model>` |
| `ollama API call failed: API error (status 500): ...` | Ollama internal error (e.g., out of memory) | Use a smaller model or quantized variant |
| `ollama API call failed: failed to marshal request: ...` | Invalid request parameters | Check configuration values |
| `ollama API call failed: failed to unmarshal response: ...` | Ollama returned invalid JSON | Check Ollama server health; may indicate version incompatibility |
| `ollama API call failed: failed to read response: ...` | HTTP response body could not be read | Check Ollama server health, network connectivity |
| `context deadline exceeded` | Request exceeded Go context timeout | Increase `timeout_seconds` in config |
| `error reading stream: ...` | Stream interrupted during streaming response | Check Ollama server health, retry |

**Debugging tips**:

```bash
# Verify Ollama server is running
curl http://localhost:11434/api/tags

# Start Ollama server
ollama serve

# On macOS, restart the service
brew services restart ollama

# Check installed models
ollama list

# Pull a missing model
ollama pull llama3.1
```

**Timeout issues** (common with CPU inference):
```yaml
timeout_seconds: 300  # 5 minutes for slow CPU inference
max_tokens: 1024      # Shorter responses complete faster
```

**Out of memory**: Use a smaller or quantized model:
```bash
ollama pull llama3.1:7b-q4_0  # 4-bit quantization, ~50% less VRAM
```


## Best Practices

1. **Development/Testing**: Use Ollama to avoid API costs during development
2. **Tool Mode**: Use `auto` mode (the default) — Loom probes the model dynamically and caches the result. Set via Go API (`ollama.Config.ToolMode`)
3. **Model Selection**: Start with `qwen3:8b` or `llama3.1` for testing, `llama3.3:70b` for quality
4. **Code Tasks**: Use `qwen3-coder:30b` or `qwen2.5-coder` for code generation and analysis
5. **Hybrid**: Use Ollama for privacy-sensitive data, cloud for production scale
6. **GPU**: Invest in GPU for serious local LLM work (especially for 13B+ models)
7. **Monitoring**: Track inference speed and tool calling accuracy to detect performance issues
8. **Any Model Works**: Loom discovers models dynamically — pull any Ollama model and it works


## See Also

### Loom Documentation
- [LLM Providers Overview](./llm-providers/) - All supported LLM providers
- [Agent Configuration](./agent-configuration/) - Agent YAML configuration
- [CLI Reference](./cli/) - Command-line interface

### Ollama Documentation
- [Ollama Model Library](https://ollama.com/library) - All available models
- [Ollama Documentation](https://github.com/ollama/ollama/blob/main/docs) - Official docs
- [Quantization Guide](https://github.com/ollama/ollama/blob/main/docs/faq.md#how-do-i-configure-ollama-server) - Model quantization
- [GPU Setup](https://github.com/ollama/ollama/blob/main/docs/gpu.md) - GPU acceleration

### Support
- [Ollama GitHub](https://github.com/ollama/ollama) - Source code and issues
- [Ollama Discord](https://discord.gg/ollama) - Community support
- [Loom Issues](https://github.com/teradata-labs/loom/issues) - Report Loom bugs
