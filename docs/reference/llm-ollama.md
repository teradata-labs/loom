
# Ollama Local LLM Integration

Complete reference for connecting Loom to Ollama for local LLM inference.

**Version**: v1.0.0-beta.2


## Table of Contents

1. [Quick Reference](#quick-reference)
2. [Overview](#overview)
3. [Prerequisites](#prerequisites)
4. [Installation](#installation)
5. [Installing Models](#installing-models)
6. [Configuration](#configuration)
7. [Testing Your Setup](#testing-your-setup)
8. [Tool Calling Support](#tool-calling-support)
9. [Performance Considerations](#performance-considerations)
10. [Model Selection Guide](#model-selection-guide)
11. [Advanced Configuration](#advanced-configuration)
12. [Comparison: Ollama vs Cloud LLMs](#comparison-ollama-vs-cloud-llms)
13. [Error Codes](#error-codes)
14. [Best Practices](#best-practices)
15. [See Also](#see-also)


## Quick Reference

### Configuration Summary

```yaml
llm:
  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.1
  temperature: 0.8
  max_tokens: 4096
  timeout_seconds: 120
  ollama_tool_mode: auto  # auto, native, or prompt
```

### Popular Models

| Model | Size | Context | Best For | Pull Command |
|-------|------|---------|----------|--------------|
| `llama3.3:70b` | 70B | 128k | Latest Meta model, excellent quality | `ollama pull llama3.3:70b` |
| `llama3.1` | 8B | 128k | General purpose (recommended) | `ollama pull llama3.1` |
| `qwen2.5-coder` | 7B/32B | 32k | Code generation | `ollama pull qwen2.5-coder` |
| `deepseek-r1:7b` | 7B | 32k | Advanced reasoning | `ollama pull deepseek-r1:7b` |
| `mistral` | 7B | 8k | Instruction following | `ollama pull mistral` |
| `gemma2:9b` | 9B | 8k | Google's open model | `ollama pull gemma2:9b` |
| `phi4` | 14B | 16k | Microsoft's reasoning model | `ollama pull phi4` |
| `phi3` | 3.8B | 4k | Lightweight, fast | `ollama pull phi3` |

### Tool Calling Modes

| Mode | Behavior | When to Use | Ollama Version |
|------|----------|-------------|----------------|
| `auto` | Auto-detect native support | **Recommended** - Works with all models | Any |
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

# Alternative: Mistral 7B (excellent instruction following)
ollama pull mistral

# Code-focused: Qwen2.5 Coder 7B
ollama pull qwen2.5-coder

# Larger models (require more RAM/GPU):
ollama pull llama3.1:70b
ollama pull mixtral:8x7b
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
  ollama_model: llama3.1
  temperature: 0.8
  max_tokens: 4096
  timeout_seconds: 120
  ollama_tool_mode: auto  # Options: auto, native, prompt
```

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
**Required**: Yes

Model name to use. Must be pulled locally first.

**Examples**:
- `llama3.1` - Default tag (`:latest`)
- `llama3.1:70b` - Specific size
- `llama3.1:7b-q4_0` - Quantized version
- `qwen2.5-coder:7b` - Code-focused model

**See**: [Available Models](#available-models) for full list


#### temperature

**Type**: `float64`
**Default**: `0.8`
**Range**: `0.0` - `2.0`
**Required**: No

Sampling temperature for creativity control.

**Temperature guide**:
- **0.0-0.3**: Deterministic, focused responses
- **0.7-1.0**: Balanced creativity (recommended)
- **1.5-2.0**: Very creative, may be less coherent

**Example**:
```yaml
temperature: 0.7  # Balanced
```


#### max_tokens

**Type**: `int`
**Default**: `4096`
**Range**: `1` - model's context window
**Required**: No

Maximum response length in tokens.

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
**Default**: `120`
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


#### ollama_tool_mode

**Type**: `string` (enum)
**Default**: `auto`
**Allowed values**: `auto`, `native`, `prompt`
**Required**: No
**Available since**: v0.7.0

Tool calling mode for Ollama.

| Mode | Behavior | When to Use |
|------|----------|-------------|
| `auto` | Auto-detect if model supports native tools | **Recommended** - Works with all models |
| `native` | Force native tool calling API | When you know model supports it (Ollama v0.12.3+) |
| `prompt` | Prompt-based tool calling fallback | For older Ollama or unsupported models |

**Example**:
```yaml
ollama_tool_mode: auto  # Recommended
```

**See**: [Tool Calling Support](#tool-calling-support) for details


### Available Models

Popular models for Loom:

| Model | Size | Context | Best For | Pull Command |
|-------|------|---------|----------|--------------|
| **Llama 3.3** | 70B | 128k | Latest Meta model, excellent quality | `ollama pull llama3.3:70b` |
| **Llama 3.1** | 8B | 128k | General purpose, reliable (recommended) | `ollama pull llama3.1` |
| **DeepSeek-R1** | 7B/70B | 32k | Advanced reasoning, math & logic | `ollama pull deepseek-r1:7b` |
| **Qwen2.5-Coder** | 7B/32B | 32k | Code generation & understanding | `ollama pull qwen2.5-coder` |
| **DeepSeek-Coder-V2** | 16B | 64k | Advanced code tasks | `ollama pull deepseek-coder-v2` |
| **Mistral** | 7B | 8k | Instruction following | `ollama pull mistral` |
| **Mixtral** | 8x7B (47B total) | 32k | Mixture of experts | `ollama pull mixtral:8x7b` |
| **Gemma 2** | 9B/27B | 8k | Google's latest open model | `ollama pull gemma2:9b` |
| **Phi-4** | 14B | 16k | Microsoft's latest, strong reasoning | `ollama pull phi4` |
| **Phi-3** | 3.8B | 4k | Fast, lightweight | `ollama pull phi3` |

See [Ollama Model Library](https://ollama.com/library) for all available models.


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

If connection fails, see [ERR_CONNECTION_REFUSED](#err_connection_refused).


### Step 2: Start Loom Server

```bash
looms serve
```

**Expected output**:
```
INFO  Starting Loom server
INFO  gRPC server listening on :50051
INFO  HTTP gateway listening on :8080
INFO  LLM provider: ollama (endpoint: http://localhost:11434)
INFO  Model: llama3.1
```


### Step 3: Test with gRPC

```bash
grpcurl -plaintext -d '{"query": "What is 2+2?"}' \
  localhost:50051 loom.v1.LoomService/Weave
```

**Expected output**:
```json
{
  "text": "2 + 2 equals 4.",
  "sessionId": "sess_abc123",
  "cost": {
    "llmCost": {
      "provider": "ollama",
      "model": "llama3.1",
      "inputTokens": 10,
      "outputTokens": 8,
      "costUsd": 0
    }
  }
}
```

**Note**: `costUsd: 0` - Ollama is free!


## Tool Calling Support

### Native Tool Calling (Ollama v0.12.3+)

**Native tool calling is now supported!**

Loom supports Ollama's native tool calling API (requires Ollama v0.12.3 or later).

**Supported Models**:
- **Llama 3.3** - Full native tool calling
- **Llama 3.2** - Full native tool calling
- **Llama 3.1** - Full native tool calling
- **Qwen 2.5** (all variants) - Excellent tool calling
- **Mistral** / **Mixtral** - Native tool calling
- **DeepSeek-R1** - Tool calling with reasoning
- **Functionary** - Specialized for function calling

### Tool Mode Configuration

Configure how Loom handles tools with Ollama:

```yaml
llm:
  provider: ollama
  ollama_model: llama3.3
  ollama_tool_mode: auto  # Recommended: auto-detect support
```

**Tool Mode Options**:

| Mode | Behavior | When to Use |
|------|----------|-------------|
| `auto` | Automatically detect if model supports native tools | **Recommended** - Works with all models |
| `native` | Force native tool calling API | When you know your model supports it (Ollama v0.12.3+) |
| `prompt` | Use prompt-based tool calling | For older Ollama versions or unsupported models |

**How `auto` mode works**:
1. Loom checks Ollama version via API
2. If v0.12.3+, tests native tool calling
3. Falls back to prompt mode if unsupported


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
```yaml
llm:
  provider: ollama
  ollama_model: llama3.3
  ollama_tool_mode: auto  # Detects native support
```

**Force Native Mode** (Ollama v0.12.3+):
```yaml
llm:
  provider: ollama
  ollama_model: qwen2.5
  ollama_tool_mode: native  # Use native API
```

**Fallback to Prompt Mode** (older Ollama):
```yaml
llm:
  provider: ollama
  ollama_model: llama3.1
  ollama_tool_mode: prompt  # Prompt engineering workaround
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
| **Qwen 2.5** | Excellent | Very Good | Fast | **Recommended** for most use cases |
| **Llama 3.1 8B** | Good | Good | Very Fast | Good for development |
| **Functionary** | Specialized | Good | Fast | Optimized for tools |
| **DeepSeek-R1** | Good | Excellent | Slow | Best for reasoning tasks |


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
| Code Generation | `qwen2.5-coder` or `deepseek-coder-v2` | Optimized for code understanding |
| Math & Reasoning | `deepseek-r1` | Advanced reasoning capabilities |
| Production/Quality | `llama3.3:70b` | Latest Meta model, excellent quality |
| Low-resource devices | `phi3` (3.8B) | Smallest viable model |
| Balanced performance | `gemma2:9b` or `phi4` | Good quality, moderate size |


### By Hardware

| Hardware | Recommended Model |
|----------|------------------|
| Laptop (8GB RAM, no GPU) | `phi3` or `llama3.1:7b-q4_0` |
| Desktop (16GB RAM, no GPU) | `llama3.1`, `mistral`, or `gemma2:9b` |
| Desktop (NVIDIA RTX 3060) | `phi4`, `gemma2:9b`, or `deepseek-r1:7b` |
| Workstation (RTX 4090) | `llama3.3:70b`, `mixtral:8x7b`, or `deepseek-r1:70b` |
| Mac M1/M2 (16GB+) | `llama3.1`, `qwen2.5-coder`, or `phi4` |
| Mac M3/M4 Pro (32GB+) | `gemma2:27b` or `qwen2.5-coder:32b` |


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

Configure Loom to use specific instance:
```yaml
# Agent 1: Fast model
agents:
  - name: fast-agent
    llm:
      provider: ollama
      ollama_endpoint: http://localhost:11434
      ollama_model: llama3.1

# Agent 2: Large model
  - name: quality-agent
    llm:
      provider: ollama
      ollama_endpoint: http://localhost:11435
      ollama_model: llama3.3:70b
```


### Ollama Model Parameters

Advanced model parameters via Ollama API:

```bash
# View model configuration
ollama show llama3.1

# Adjust model parameters (requires Ollama API)
# Loom uses temperature and max_tokens from config
```

**Note**: Loom controls temperature and max_tokens. Advanced Ollama parameters (like `top_p`, `top_k`) not currently exposed.


## Comparison: Ollama vs Cloud LLMs

| Feature | Ollama | Anthropic/Bedrock |
|---------|--------|-------------------|
| **Cost** | Free (hardware only) | $3-$15 per 1M tokens |
| **Latency** | Low (local) | Medium (network) |
| **Privacy** | 100% private | Data sent to provider |
| **Quality** | Good (8B), Excellent (70B+) | Excellent |
| **Tool Calling** | Native (v0.12.3+) | Native, reliable |
| **Hardware Required** | Yes (GPU recommended) | No |
| **Internet Required** | No | Yes |
| **Rate Limits** | None (local) | Yes (varies by tier) |
| **Context Window** | 8k-128k (model-dependent) | 200k+ |

**Choose Ollama for**: Development, privacy, cost savings, offline work, local inference
**Choose Cloud for**: Production at scale, managed infrastructure, no hardware costs


## Error Codes

### ERR_CONNECTION_REFUSED

**Code**: `connection_refused`
**HTTP Status**: 503 Service Unavailable
**gRPC Code**: `UNAVAILABLE`

**Cause**: Ollama server not running.

**Example**:
```
Error: connection_refused: failed to connect to http://localhost:11434: connection refused
```

**Resolution**:
```bash
# Start Ollama server
ollama serve

# Or on macOS, restart the service
brew services restart ollama

# Verify server running
curl http://localhost:11434/api/tags
```

**Retry behavior**: Loom will not automatically retry. Start server and retry request.


### ERR_MODEL_NOT_FOUND

**Code**: `model_not_found`
**HTTP Status**: 404 Not Found
**gRPC Code**: `NOT_FOUND`

**Cause**: Model not downloaded locally.

**Example**:
```
Error: model_not_found: model 'llama3.1' not found. Run 'ollama pull llama3.1'
```

**Resolution**:
```bash
# List available models
ollama list

# Pull the model specified in config
ollama pull llama3.1

# Verify model pulled
ollama list | grep llama3.1
```

**Prevention**: Always run `ollama pull <model>` before configuring Loom.


### ERR_OUT_OF_MEMORY

**Code**: `out_of_memory`
**HTTP Status**: 507 Insufficient Storage
**gRPC Code**: `RESOURCE_EXHAUSTED`

**Cause**: Model too large for available RAM/VRAM.

**Example**:
```
Error: out_of_memory: failed to load model 'llama3.3:70b': insufficient memory (need 64GB, have 16GB)
```

**Resolution**:

**Option 1: Use smaller model**:
```yaml
ollama_model: phi3  # 3.8B parameter model
```

**Option 2: Use quantized version**:
```bash
ollama pull llama3.1:7b-q4_0  # 4-bit quantization
```

**Option 3: Close other applications** to free RAM:
```bash
# Check memory usage
free -h  # Linux
vm_stat  # macOS

# Kill memory-intensive processes
```

**Prevention**: Choose model size appropriate for your hardware (see [Hardware Requirements](#hardware-requirements)).


### ERR_TIMEOUT

**Code**: `timeout`
**HTTP Status**: 504 Gateway Timeout
**gRPC Code**: `DEADLINE_EXCEEDED`

**Cause**: Request took longer than configured timeout.

**Example**:
```
Error: timeout: request exceeded timeout of 120s (still generating after 125s)
```

**Resolution**:

**CPU inference (common cause)**:
```yaml
timeout_seconds: 300  # 5 minutes for slow CPU inference
```

**Reduce response length**:
```yaml
max_tokens: 1024  # Faster completion
```

**Use smaller model**:
```yaml
ollama_model: llama3.1  # 8B model faster than 70B
```

**Install GPU acceleration** (best solution):
```bash
# NVIDIA: Install CUDA
# AMD: Install ROCm
# Apple: Automatic via Metal
```

**Retry behavior**: Loom will not automatically retry. Increase timeout or optimize performance, then retry.


### ERR_TOOL_CALLING_FAILED

**Code**: `tool_calling_failed`
**HTTP Status**: 500 Internal Server Error
**gRPC Code**: `INTERNAL`

**Cause**: Tool mode incompatible with model or Ollama version.

**Example**:
```
Error: tool_calling_failed: native tool calling not supported by model 'mistral' (Ollama v0.11.0)
```

**Resolution**:

**Option 1: Use auto mode**:
```yaml
ollama_tool_mode: auto  # Automatically detect support
```

**Option 2: Upgrade Ollama** (for native tool calling):
```bash
# macOS
brew upgrade ollama

# Linux
curl -fsSL https://ollama.com/install.sh | sh

# Verify version
ollama --version  # Should be v0.12.3 or later
```

**Option 3: Use prompt mode** (fallback):
```yaml
ollama_tool_mode: prompt  # Works with all versions
```

**Prevention**: Use `ollama_tool_mode: auto` (recommended default).


### ERR_SLOW_INFERENCE

**Code**: `slow_inference`
**HTTP Status**: 200 OK (not an error, but warning)
**gRPC Code**: N/A

**Cause**: Running on CPU is much slower than GPU.

**Example**:
```
Warning: slow_inference: CPU inference detected, tokens/sec: 3.2 (expected: >20 on GPU)
```

**Resolution**:

**Option 1: Install CUDA/ROCm** for GPU acceleration:
```bash
# NVIDIA: https://developer.nvidia.com/cuda-downloads
# AMD: https://rocmdocs.amd.com/
```

**Option 2: Use smaller models** (3B-7B parameters):
```yaml
ollama_model: phi3  # 3.8B, fast on CPU
```

**Option 3: Reduce `max_tokens`**:
```yaml
max_tokens: 1024  # Faster completion
```

**Option 4: Increase `timeout_seconds`** to allow completion:
```yaml
timeout_seconds: 300  # 5 minutes
```

**Performance comparison**:
| Hardware | Tokens/sec | Time for 1000 tokens |
|----------|-----------|----------------------|
| CPU (8-core) | 5-10 | 100-200s |
| NVIDIA RTX 3060 | 20-50 | 20-50s |
| NVIDIA RTX 4090 | 50-100 | 10-20s |


## Best Practices

1. **Development/Testing**: Use Ollama to avoid API costs during development
2. **Tool Calling**: Use Ollama v0.12.3+ with supported models for native tool calling
3. **Hybrid**: Use Ollama for privacy-sensitive data, cloud for production scale
4. **Model Selection**: Start with `llama3.1:8b` for testing, `qwen2.5` or `llama3.3:70b` for production
5. **GPU**: Invest in GPU for serious local LLM work (especially for 70B+ models)
6. **Monitoring**: Track inference speed and tool calling accuracy to detect performance issues
7. **Tool Mode**: Use `auto` mode to automatically detect native tool support


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
