
# LLM Provider Support

Reference for all supported LLM providers and their configuration.

**Version**: v1.2.0
**Status**: ✅ 8 providers implemented (Anthropic, Bedrock, Ollama, OpenAI, Azure OpenAI, Mistral AI, Google Gemini, HuggingFace); 📋 Google Vertex AI planned


## Table of Contents

### Implemented Providers
- [Anthropic Claude](#1-anthropic-claude-recommended) - Direct API access (Production)
- [AWS Bedrock](#2-aws-bedrock) - Enterprise AWS integration (Production)
- [Ollama](#3-ollama-local) - Local inference (Production)
- [OpenAI](#4-openai) - GPT-5, GPT-4.1, and o-series reasoning models (Production)
- [Azure OpenAI](#5-azure-openai) - Microsoft Azure integration (Production)
- [Mistral AI](#6-mistral-ai) - Open and commercial models (Production)
- [Google Gemini](#7-google-gemini) - Google AI Studio (Production)
- [HuggingFace](#8-huggingface) - 1M+ open-source models (Production)

### Planned Providers
- [Google Vertex AI](#9-google-vertex-ai) - GCP integration (Planned)

### Reference
- [Provider Comparison](#provider-comparison) - Feature matrix and benchmarks
- [Configuration Reference](#configuration-reference) - Common and provider-specific parameters
- [Security Best Practices](#security-best-practices) - Credential management
- [Troubleshooting](#troubleshooting) - Common issues and solutions
- [Migration Between Providers](#migration-between-providers) - Switching providers
- [Performance Considerations](#performance-considerations) - Latency and throughput
- [Observability](#observability) - Cost tracking and monitoring
- [Implementation Guide](#implementation-guide) - For developers adding new providers


## Quick Reference

### Provider Status Summary

| Provider | Status | Tool Calling | Context | Pricing Range | Best For |
|----------|--------|--------------|---------|---------------|----------|
| **Anthropic Claude** | ✅ Production | Native | 200k-1M | $1-$75/1M tokens | General production, agent tasks |
| **AWS Bedrock** | ✅ Production | Native | 200k-1M | $1-$75/1M tokens | AWS infrastructure, enterprise |
| **Ollama** | ✅ Production | Auto-detect (native or prompt) | Varies | Free (local) | Development, privacy, offline |
| **OpenAI** | ✅ Production | Native | 200k-1M | $0.10-$40/1M tokens | GPT-5/4.1 tasks, reasoning models |
| **Azure OpenAI** | ✅ Production | Native | 200k-1M | $0.40-$40/1M tokens | Microsoft infrastructure |
| **Mistral AI** | ✅ Production | Native | 32k-256k | $0.10-$8/1M tokens | Reasoning, code models |
| **Google Gemini** | ✅ Production | Native | 1M | $0-$10/1M tokens | Long context, multimodal |
| **HuggingFace** | ✅ Production | Native | Varies | $0.20-$1.00/1M tokens | Open-source models |
| **Google Vertex AI** | 📋 Planned | TBD | TBD | TBD | GCP infrastructure |

### Authentication Methods

| Provider | Keyring Command | Environment Variable | Additional Methods |
|----------|----------------|----------------------|-------------------|
| **Anthropic** | `looms config set-key anthropic_api_key` | `LOOM_LLM_ANTHROPIC_API_KEY` | CLI flag `--anthropic-key` |
| **Bedrock** | `looms config set-key bedrock_access_key_id` | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | IAM role, AWS profile |
| **Ollama** | None (local) | N/A | N/A |
| **OpenAI** | `looms config set-key openai_api_key` | `LOOM_LLM_OPENAI_API_KEY` | CLI flag `--openai-key` |
| **Azure OpenAI** | `looms config set-key azure_openai_api_key` | `LOOM_LLM_AZURE_OPENAI_API_KEY` | Entra ID, Managed Identity |
| **Mistral** | `looms config set-key mistral_api_key` | `LOOM_LLM_MISTRAL_API_KEY` | CLI flag `--mistral-key` |
| **Gemini** | `looms config set-key gemini_api_key` | `LOOM_LLM_GEMINI_API_KEY` | CLI flag `--gemini-key` |
| **HuggingFace** | `looms config set-key huggingface_token` | `LOOM_LLM_HUGGINGFACE_TOKEN` | CLI flag `--huggingface-token` |

### Use Case Recommendations

| Use Case | Primary Recommendation | Alternative |
|----------|----------------------|-------------|
| **General Production** | Anthropic Claude | Bedrock |
| **AWS Infrastructure** | AWS Bedrock | Anthropic |
| **Azure Infrastructure** | Azure OpenAI | Anthropic |
| **GCP Infrastructure** | Vertex AI (planned) | Anthropic |
| **Development/Testing** | Ollama | Anthropic (small tasks) |
| **Complete Privacy** | Ollama | N/A |
| **Cost Optimization** | Ollama (local) or HuggingFace | Mistral |
| **Offline Work** | Ollama | N/A |
| **Maximum Quality** | Anthropic Claude | OpenAI GPT-5 |
| **Open Source Models** | HuggingFace or Ollama | N/A |


## Overview

Loom supports multiple LLM providers for different deployment scenarios. All providers implement the same `LLMProvider` interface (defined at `pkg/types/types.go:183`), enabling provider switching through configuration changes only - no code modifications required.

**Key Features**:
- Unified interface across all providers (`LLMProvider` + `StreamingLLMProvider`)
- Token streaming support on all 8 providers
- Automatic tool calling conversion (native or fallback)
- Cost tracking and observability
- Secure credential management (system keyring)
- Provider-agnostic agent code


## Implemented Providers

### 1. Anthropic Claude (Recommended)

**Status**: ✅ Implemented (Production)

The primary and recommended provider for Loom, offering the best balance of quality, tool calling support, and cost.

**Why Anthropic**:
- Native tool calling support (best agent performance)
- Latest Claude 4.6 and 4.5 models with excellent reasoning
- Direct API access (no intermediaries)
- Up to 1M context window (4.6 models)
- Competitive pricing ($1-$75 per 1M tokens)
- Strong documentation and API stability

**Configuration**:
```yaml
llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-6  # or claude-opus-4-6, claude-sonnet-4-5-20250929
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **System keyring** (recommended): `looms config set-key anthropic_api_key`
- **Environment variable**: `LOOM_LLM_ANTHROPIC_API_KEY`
- **CLI flag**: `--anthropic-key`

**Available Models** (6 models):
| Model | Context | Input Cost | Output Cost | Best For |
|-------|---------|------------|-------------|----------|
| `claude-opus-4-6` | 1M | $5/1M tokens | $25/1M tokens | Most capable reasoning |
| `claude-sonnet-4-6` | 1M | $3/1M tokens | $15/1M tokens | Balanced quality/cost |
| `claude-opus-4-5-20251101` | 200k | $5/1M tokens | $25/1M tokens | Complex reasoning |
| `claude-sonnet-4-5-20250929` | 200k | $3/1M tokens | $15/1M tokens | General tasks (recommended) |
| `claude-haiku-4-5-20251001` | 200k | $1/1M tokens | $5/1M tokens | Speed/cost optimization |
| `claude-opus-4-1-20250805` | 200k | $15/1M tokens | $75/1M tokens | Legacy complex reasoning |

**Tool Calling**:
- Native support via Anthropic's tool calling API
- Automatic conversion from Loom tool format
- Parallel tool execution supported

**When to Use**:
- Production agent deployments
- Tasks requiring native tool calling
- Direct API access preferred
- Cost-effective quality

**Limitations**:
- Requires internet connection
- Cloud-based (data sent to Anthropic servers)
- API rate limits (50-1000+ req/min depending on tier)

**Detailed Guide**: [llm-anthropic.md](./llm-anthropic/)

**Reference**: [Anthropic API Documentation](https://docs.anthropic.com/)


### 2. AWS Bedrock

**Status**: ✅ Implemented (Production)

Claude models through AWS infrastructure for enterprise deployments requiring AWS ecosystem integration.

**Why Bedrock**:
- Enterprise AWS integration (IAM, VPC, PrivateLink)
- Unified AWS billing
- Regional deployment options
- AWS compliance certifications (HIPAA, SOC 2, etc.)
- Same Claude models as Anthropic direct

**Configuration**:
```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: us.anthropic.claude-sonnet-4-6-v1:0  # or other Bedrock model IDs
  bedrock_profile: default  # Optional: AWS profile
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **IAM role** (recommended for EC2/ECS/Lambda)
- **AWS profile**: `~/.aws/credentials`
- **Environment variables**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- **System keyring**: `looms config set-key bedrock_access_key_id`

**Available Models** (6 models):
| Model ID | Model | Region Availability |
|----------|-------|---------------------|
| `us.anthropic.claude-opus-4-6-v1` | Claude Opus 4.6 | us-east-1, us-west-2 |
| `us.anthropic.claude-sonnet-4-6-v1:0` | Claude Sonnet 4.6 | us-east-1, us-west-2 |
| `us.anthropic.claude-opus-4-5-20251101-v1:0` | Claude Opus 4.5 | us-east-1, us-west-2 |
| `us.anthropic.claude-sonnet-4-5-20250929-v1:0` | Claude Sonnet 4.5 | us-east-1, us-west-2 |
| `us.anthropic.claude-haiku-4-5-20251001-v1:0` | Claude Haiku 4.5 | All AWS regions |
| `us.anthropic.claude-opus-4-1-20250805-v1:0` | Claude Opus 4.1 | us-east-1, us-west-2 |

**Tool Calling**:
- Native support (same as Anthropic direct)
- Automatic conversion from Loom tool format

**When to Use**:
- AWS-native infrastructure
- Enterprise compliance requirements
- VPC isolation needed
- Unified AWS billing preferred

**Limitations**:
- Requires AWS account and permissions
- Model access must be enabled per region
- Slightly higher latency than direct Anthropic

**Detailed Guide**: [llm-bedrock.md](./llm-bedrock/)

**Reference**: [AWS Bedrock Documentation](https://docs.aws.amazon.com/bedrock/)


### 3. Ollama (Local)

**Status**: ✅ Implemented (Production)

Local LLM inference for development, privacy, and cost savings.

**Why Ollama**:
- Zero API costs (free local inference)
- Complete data privacy (never leaves your machine)
- Works offline
- Fast iteration during development
- Support for many open-source models (Llama, Qwen, Mistral, etc.)

**Configuration**:
```yaml
llm:
  provider: ollama
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.2  # default; also supports qwen3, deepseek-r1, etc.
  temperature: 0.8
  max_tokens: 4096
  timeout_seconds: 120
```

**Authentication**: None required (local server)

**Default Models** (10 models in catalog; runtime discovery replaces with locally installed models):
| Model | Capabilities | Context | Best For |
|-------|-------------|---------|----------|
| `llama3.3` | text, tool-use | 128k | General tasks |
| `llama3.2` | text, tool-use | 128k | General tasks (factory default) |
| `llama3.2-vision` | text, vision | 128k | Multimodal |
| `llama3.1` | text, tool-use | 128k | General tasks |
| `qwen3` | text, tool-use, thinking | 128k | Reasoning |
| `qwen2.5` | text, tool-use | 128k | General tasks |
| `deepseek-r1` | text, thinking | 128k | Reasoning |
| `deepseek-v3` | text, tool-use | 128k | General tasks |
| `phi4` | text, tool-use | 16k | Small/fast |
| `gemma3` | text, vision | 128k | Vision tasks |

**Tool Calling**:
- **Auto-detection** (default `ToolModeAuto`) probes the model via `/api/show` to check for native tool support
- **Native mode** supported on many models (Llama 3.x, Qwen 2.5/3, Mistral, Phi 4, etc.) via Ollama v0.12.3+
- **Prompt-based fallback** for models without native tool support
- Can be explicitly set: `auto` (default), `native`, or `prompt`

**When to Use**:
- Development and testing
- Privacy-sensitive data
- Offline work required
- Zero API cost desired
- Prototyping agent workflows

**Limitations**:
- Native tool calling depends on model support (auto-detected; prompt fallback available)
- Requires sufficient local hardware (GPU recommended)
- Quality varies by model size (7B << 70B)
- Slower inference on CPU

**Installation**:
```bash
# Install Ollama
curl -fsSL https://ollama.com/install.sh | sh

# Pull a model
ollama pull qwen2.5:7b

# Start server (runs automatically on install)
ollama serve
```

**Detailed Guide**: [llm-ollama.md](./llm-ollama/)

**Reference**: [Ollama Documentation](https://github.com/ollama/ollama)


### 4. OpenAI

**Status**: ✅ Implemented (Production)

Direct access to OpenAI's GPT and o-series reasoning models.

**Why OpenAI**:
- Native tool calling support (function calling)
- Multiple model options (GPT-5, GPT-4.1 family, o-series)
- Advanced reasoning models (o3, o3-mini, o4-mini)
- Up to 1M context window (GPT-4.1 family)
- Competitive pricing ($0.10-$40 per 1M tokens)
- Mature API and ecosystem

**Configuration**:
```yaml
llm:
  provider: openai
  openai_model: gpt-4.1  # or gpt-5, o3, o4-mini
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **System keyring** (recommended): `looms config set-key openai_api_key`
- **Environment variable**: `LOOM_LLM_OPENAI_API_KEY`
- **CLI flag**: `--openai-key`

**Available Models** (8 models):
| Model | Context | Input Cost | Output Cost | Best For |
|-------|---------|------------|-------------|----------|
| `gpt-5` | 272k | $2.50/1M tokens | $10/1M tokens | Most capable |
| `gpt-5-mini` | 272k | $0.40/1M tokens | $1.60/1M tokens | Cost-effective GPT-5 |
| `gpt-4.1` | 1M | $2/1M tokens | $8/1M tokens | General tasks (recommended) |
| `gpt-4.1-mini` | 1M | $0.40/1M tokens | $1.60/1M tokens | Cost optimization |
| `gpt-4.1-nano` | 1M | $0.10/1M tokens | $0.40/1M tokens | Cheapest OpenAI |
| `o3` | 200k | $10/1M tokens | $40/1M tokens | Advanced reasoning |
| `o3-mini` | 200k | $1.10/1M tokens | $4.40/1M tokens | Fast reasoning |
| `o4-mini` | 200k | $1.10/1M tokens | $4.40/1M tokens | Latest reasoning |

**Tool Calling**:
- Native support via OpenAI's function calling API
- Automatic conversion from Loom tool format
- Parallel tool execution supported

**When to Use**:
- GPT-5 or GPT-4.1 model family preferred
- OpenAI ecosystem integration
- Advanced reasoning models needed (o3, o4-mini)
- Alternative to Claude

**Limitations**:
- Different models than Claude (different strengths)
- Reasoning models (o-series) don't support streaming
- Cloud-based (data sent to OpenAI servers)

**Detailed Guide**: [llm-openai.md](./llm-openai/)

**Reference**: [OpenAI API Documentation](https://platform.openai.com/docs/)


### 5. Azure OpenAI

**Status**: ✅ Implemented (Production)

OpenAI GPT models through Microsoft Azure infrastructure with enterprise security and multiple authentication options.

**Why Azure OpenAI**:
- Enterprise Microsoft integration (Azure Monitor, VNet)
- Deployment-based routing (models as named deployments)
- Multiple authentication methods (API key, Entra ID, Managed Identity)
- Regional data residency options
- Microsoft compliance certifications (HIPAA, SOC 2)
- Private endpoint support

**Configuration**:
```yaml
llm:
  provider: azure-openai
  azure_openai_endpoint: https://myresource.openai.azure.com
  azure_openai_deployment_id: gpt-4-1-deployment
  # azure_openai_api_key: set via keyring (looms config set-key azure_openai_api_key)
  # OR for Entra ID: azure_openai_entra_token set via keyring
  temperature: 1.0
  max_tokens: 4096
```

**Authentication Methods**:

1. **API Key** (via Azure Portal):
   - System keyring: `looms config set-key azure_openai_api_key`
   - Environment variable: `LOOM_LLM_AZURE_OPENAI_API_KEY`
   - Best for: Development, testing

2. **Microsoft Entra ID** (OAuth2 token):
   - System keyring: `looms config set-key azure_openai_entra_token`
   - Environment variable: `LOOM_LLM_AZURE_OPENAI_ENTRA_TOKEN`
   - Best for: Enterprise SSO, role-based access

3. **Managed Identity** (Azure resources):
   - Use Azure SDK to obtain token externally
   - Pass token as `EntraToken` to client
   - Best for: VM, App Service, AKS deployments
   - Automatic token refresh via Azure SDK

**Available Models** (5 models):
- GPT-5 ($2.50/$10 per 1M tokens)
- GPT-4.1 ($2/$8 per 1M tokens)
- GPT-4.1 Mini ($0.40/$1.60 per 1M tokens)
- o3 ($10/$40 per 1M tokens)
- o4-mini ($1.10/$4.40 per 1M tokens)

**Tool Calling**:
- Native support (same as OpenAI direct)
- Automatic conversion from Loom tool format

**When to Use**:
- Azure-native infrastructure
- Microsoft compliance requirements
- Entra ID authentication needed
- Regional data residency required

**Limitations**:
- Requires Azure account and OpenAI resource
- Deployment-based model selection (not direct model IDs)
- Slightly higher latency than direct OpenAI

**Detailed Guide**: [llm-azure-openai.md](./llm-azure-openai/)

**Reference**: [Azure OpenAI Documentation](https://learn.microsoft.com/azure/ai-services/openai/)


### 6. Mistral AI

**Status**: ✅ Implemented (Production)

High-performance models from Mistral AI, including both open-source and commercial options with OpenAI-compatible API.

**Why Mistral**:
- OpenAI-compatible API (easy migration)
- Competitive pricing ($0.10-$8 per 1M tokens)
- Reasoning models (Magistral) and code models (Codestral, Devstral)
- Native function calling support
- Strong multilingual capabilities

**Configuration**:
```yaml
llm:
  provider: mistral
  mistral_model: mistral-large-latest  # or mistral-small-latest, codestral-latest
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **System keyring** (recommended): `looms config set-key mistral_api_key`
- **Environment variable**: `LOOM_LLM_MISTRAL_API_KEY`
- **CLI flag**: `--mistral-key`
- API key from https://console.mistral.ai

**Available Models** (6 models):
| Model | Type | Context | Input Cost | Output Cost | Best For |
|-------|------|---------|------------|-------------|----------|
| `mistral-large-latest` | Commercial | 128k | $2/1M tokens | $6/1M tokens | Complex tasks |
| `mistral-small-latest` | Commercial | 32k | $0.10/1M tokens | $0.30/1M tokens | General tasks |
| `magistral-medium-latest` | Reasoning | 128k | $2/1M tokens | $8/1M tokens | Complex reasoning |
| `magistral-small-latest` | Reasoning | 128k | $0.50/1M tokens | $1.50/1M tokens | Fast reasoning |
| `codestral-latest` | Code | 256k | $0.30/1M tokens | $0.90/1M tokens | Code generation |
| `devstral-medium-latest` | Code | 128k | $0.50/1M tokens | $1.50/1M tokens | Development tasks |

**Tool Calling**:
- Native support via Mistral's function calling API
- OpenAI-compatible format
- Automatic conversion from Loom tool format

**When to Use**:
- OpenAI-compatible API desired
- Code-focused tasks (Codestral, Devstral)
- Reasoning tasks (Magistral)
- Cost optimization with quality
- Multilingual tasks

**Limitations**:
- Smaller context than Claude (32k-256k vs 200k-1M)
- Less established ecosystem than OpenAI/Anthropic

**Detailed Guide**: [llm-mistral.md](./llm-mistral/)

**Reference**: [Mistral AI Documentation](https://docs.mistral.ai/)


### 7. Google Gemini

**Status**: ✅ Implemented (Production)

Direct access to Google's latest Gemini models via Google AI Studio with native function calling support.

**Why Gemini**:
- Google AI models (Gemini 3.x preview, 2.5 Pro, 2.5 Flash, 2.5 Flash-Lite)
- Single API key authentication (one key, no OAuth or SDK setup)
- Native function calling support
- Competitive pricing ($0-$10 per 1M tokens; Gemini 3 Flash is free during preview)
- 1M token context windows
- Implicit caching for cost savings

**Configuration**:
```yaml
llm:
  provider: gemini
  gemini_model: gemini-3-flash-preview  # default; or gemini-3-pro-preview, gemini-2.5-pro, gemini-2.5-flash
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **System keyring** (recommended): `looms config set-key gemini_api_key`
- **Environment variable**: `LOOM_LLM_GEMINI_API_KEY`
- **CLI flag**: `--gemini-key`
- API key from https://makersuite.google.com/

**Available Models** (5 models, 3 GA + 2 preview):
| Model | Context | Input Cost | Output Cost | Best For |
|-------|---------|------------|-------------|----------|
| `gemini-3-pro-preview` | 1M | $2-4/1M tokens | $12-18/1M tokens | Most intelligent (preview) |
| `gemini-3-flash-preview` | 1M | Free (preview) | Free (preview) | Balanced (default) |
| `gemini-2.5-pro` | 1M | $1.25/1M tokens | $10/1M tokens | Complex reasoning (GA) |
| `gemini-2.5-flash` | 1M | $0.30/1M tokens | $2.50/1M tokens | Stable workhorse (GA) |
| `gemini-2.5-flash-lite` | 1M | $0.10/1M tokens | $0.40/1M tokens | Fastest/cheapest (GA) |

**Tool Calling**:
- Native support via Gemini's function calling API
- Different format from OpenAI (uses "model" role, not "assistant")
- Automatic conversion from Loom tool format

**When to Use**:
- Very long context required (1M tokens)
- Google ecosystem integration
- Cost-effective quality (2.5 Flash at $0.30/1M input)

**Limitations**:
- Different API format than OpenAI (not compatible)
- Less established ecosystem
- API key in query parameter (not header)

**Detailed Guide**: [llm-gemini.md](./llm-gemini/)

**Reference**: [Google AI Studio](https://makersuite.google.com/)


### 8. HuggingFace

**Status**: ✅ Implemented (Production)

Direct access to 1M+ open-source models via HuggingFace Inference API with OpenAI-compatible format.

**Why HuggingFace**:
- 1M+ open-source models available (Llama, Mixtral, Qwen, Gemma, etc.)
- OpenAI-compatible API (easy migration)
- Multiple backend providers (Together AI, Cohere, Groq)
- Free tier available for development
- Native function calling support
- Competitive pricing ($0.20-$1.00 per 1M tokens typical)
- Access to latest open-source models

**Configuration**:
```yaml
llm:
  provider: huggingface
  huggingface_model: meta-llama/Meta-Llama-3.1-70B-Instruct
  temperature: 1.0
  max_tokens: 4096
```

**Authentication**:
- **System keyring** (recommended): `looms config set-key huggingface_token`
- **Environment variable**: `LOOM_LLM_HUGGINGFACE_TOKEN`
- **CLI flag**: `--huggingface-token`
- Token from https://huggingface.co/settings/tokens

**Available Models** (popular):
| Model | Size | Provider | Typical Cost | Best For |
|-------|------|----------|--------------|----------|
| `meta-llama/Meta-Llama-3.1-70B-Instruct` | 70B | Together AI | $0.88/$0.88 per 1M tokens | General tasks (recommended) |
| `mistralai/Mixtral-8x7B-Instruct-v0.1` | 47B | Together AI | $0.60/$0.60 per 1M tokens | MoE architecture |
| `Qwen/Qwen2.5-72B-Instruct` | 72B | Together AI | $0.50/$0.50 per 1M tokens | Quality + cost |
| `google/gemma-2-27b-it` | 27B | Together AI | $0.30/$0.30 per 1M tokens | Cost optimization |

**Tool Calling**:
- Native support via OpenAI-compatible format
- Automatic conversion from Loom tool format
- Depends on backend provider support

**When to Use**:
- Open-source models preferred
- Cost optimization with quality
- Access to latest research models
- OpenAI-compatible API desired
- Multiple backend providers needed

**Limitations**:
- Pricing varies by backend provider
- Quality varies by model
- Backend provider selection automatic (not configurable)
- Some models don't support function calling

**Detailed Guide**: [llm-huggingface.md](./llm-huggingface/)

**Reference**: [HuggingFace Inference API](https://huggingface.co/docs/api-inference/index)


## Planned Providers

### 9. Google Vertex AI

**Status**: 📋 Planned

Claude and Gemini models through Google Cloud infrastructure.

**Why Vertex AI**:
- Enterprise Google Cloud integration
- Google compliance certifications
- Regional deployment options
- Unified GCP billing
- Integration with Google ecosystem

**Planned Configuration**:
```yaml
llm:
  provider: vertex-ai
  vertex_project_id: my-project
  vertex_location: us-central1
  vertex_model: claude-sonnet-4-6@latest
  temperature: 1.0
  max_tokens: 4096
```

**Planned Authentication**:
- Google Cloud service account
- Application Default Credentials (ADC)
- Workload Identity Federation
- System keyring: `looms config set-key gcp_credentials`

**Implementation Status**:
- Interface design: Planned
- Authentication: Planned
- Model support: Claude 4.x and Gemini 2.5 models planned
- Tool calling: Will support Vertex AI function calling

**Target Release**: TBD

**Reference**: [Vertex AI Documentation](https://cloud.google.com/vertex-ai)


## Provider Comparison

### Feature Matrix

| Feature | Anthropic | Bedrock | Ollama | OpenAI | Azure OpenAI | Mistral | Gemini | HuggingFace | Vertex AI |
|---------|-----------|---------|--------|--------|--------------|---------|--------|-------------|-----------|
| **Status** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | 📋 Planned |
| **Native Tool Calling** | ✅ | ✅ | ✅ (auto-detect) | ✅ | ✅ | ✅ | ✅ | ✅ | 📋 |
| **Cost** | $1-$75/1M | $1-$75/1M | Free | $0.10-$40/1M | $0.40-$40/1M | $0.10-$8/1M | $0-$10/1M | $0.20-$1/1M | TBD |
| **Context Window** | 200k-1M | 200k-1M | Varies | 200k-1M | 200k-1M | 32k-256k | 1M | Varies | TBD |
| **Latency** | Medium | Low-Medium | Very Low (GPU) | Medium | Medium | Medium | Medium | Medium | TBD |
| **Privacy** | Cloud | Cloud | 100% Local | Cloud | Cloud | Cloud | Cloud | Cloud | Cloud |
| **Internet Required** | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Hardware Required** | ❌ | ❌ | GPU rec'd | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Enterprise Integration** | Basic | AWS | None | Basic | Azure | Basic | Basic | Basic | GCP |
| **Compliance** | Anthropic | AWS | N/A | OpenAI | Microsoft | Mistral | Google | Varies | Google |


### Cost Comparison

Based on similar quality tiers (as of 2026):

| Provider | Model | Input (per 1M) | Output (per 1M) | Typical Task* |
|----------|-------|----------------|-----------------|---------------|
| **Anthropic** | Sonnet 4.6 | $3.00 | $15.00 | $0.0165 |
| **Bedrock** | Sonnet 4.6 | $3.00 | $15.00 | $0.0165 |
| **Ollama** | Qwen 3 | $0 | $0 | $0 |
| **OpenAI** | GPT-4.1 | $2.00 | $8.00 | $0.009 |
| **Azure OpenAI** | GPT-4.1 | $2.00 | $8.00 | $0.009 |
| **Mistral** | Large | $2.00 | $6.00 | $0.007 |
| **Gemini** | 2.5 Flash | $0.30 | $2.50 | $0.0027 |
| **HuggingFace** | Llama 3.1 70B | $0.88 | $0.88 | $0.0013 |

\* Typical task = 500 input tokens, 1000 output tokens

**Note**: Prices vary by region and are subject to change. Ollama is free but requires local hardware.


## Configuration Reference

### Common Parameters

These parameters work across all providers:

```yaml
llm:
  provider: <provider>        # Required: anthropic | bedrock | ollama | openai | azure-openai | mistral | gemini | huggingface
  temperature: 1.0            # Optional: Creativity (0.0-2.0, default 1.0)
  max_tokens: 4096            # Optional: Max response length (default 4096)
  timeout_seconds: 60         # Optional: Request timeout (default 60)
```

### Provider-Specific Parameters

**Anthropic**:
```yaml
anthropic_model: claude-sonnet-4-6
```

**Bedrock**:
```yaml
bedrock_region: us-west-2
bedrock_model_id: us.anthropic.claude-sonnet-4-6-v1:0
bedrock_profile: default  # Optional AWS profile
```

**Ollama**:
```yaml
ollama_endpoint: http://localhost:11434
ollama_model: llama3.2
```

**OpenAI**:
```yaml
openai_model: gpt-4.1
```

**Azure OpenAI**:
```yaml
azure_openai_endpoint: https://your-resource.openai.azure.com
azure_openai_deployment_id: gpt-4-1-deployment
```

**Mistral**:
```yaml
mistral_model: mistral-large-latest
```

**Gemini**:
```yaml
gemini_model: gemini-3-flash-preview
```

**HuggingFace**:
```yaml
huggingface_model: meta-llama/Meta-Llama-3.1-70B-Instruct
```

**Vertex AI** (planned):
```yaml
vertex_project_id: your-gcp-project
vertex_location: us-central1
vertex_model: claude-sonnet-4-6@latest
```


## Security Best Practices

### For All Providers

1. **Use System Keyring**: Store all API keys/credentials in system keyring
   ```bash
   looms config set-key <provider>_api_key
   ```

2. **Never Commit Secrets**: Add to `.gitignore`:
   ```
   # Never commit these!
   *.key
   *credentials*.yaml
   .env
   ```

3. **Least Privilege**: Grant minimum permissions needed
4. **Rotate Credentials**: Regularly rotate API keys and access credentials
5. **Monitor Usage**: Enable observability to detect anomalies
6. **Audit Logs**: Keep logs of all LLM API calls (Hawk integration)

### For Cloud Providers (Bedrock, Azure, Vertex AI)

7. **Use Managed Identities**: Prefer IAM roles over static credentials
8. **Enable VPC Endpoints**: Use private network connections where available
9. **Set Up Billing Alerts**: Monitor costs in real-time
10. **Enable Compliance Features**: Use encryption, audit logs, data residency

### For Local Providers (Ollama)

11. **Secure the Endpoint**: Don't expose Ollama port to public network
12. **Firewall Rules**: Only allow connections from localhost
13. **No Authentication**: Ollama has no built-in auth - use network isolation


## Troubleshooting

### Common Issues Across Providers

#### "Provider not found" or "Unknown provider"

**Cause**: Invalid provider name in config.

**Solution**:
```yaml
# Check spelling (case-sensitive)
provider: anthropic  # Correct
provider: Anthropic  # Wrong - case matters
```


#### "Authentication failed"

**Cause**: Missing or invalid credentials.

**Solution**:
1. Verify credentials are set:
   ```bash
   looms config get-key <provider>_api_key
   ```
2. Check environment variables:
   ```bash
   env | grep -i <provider>
   ```
3. Test with explicit credentials via CLI flag


#### "Model not found"

**Cause**: Invalid model ID or no access.

**Solution**:
- **Anthropic**: Check model ID matches documentation
- **Bedrock**: Request model access in AWS Console
- **Ollama**: Run `ollama pull <model>` first
- **Azure/OpenAI**: Check deployment name and region


#### "Timeout" or "Request took too long"

**Cause**: Request exceeded timeout limit.

**Solution**:
```yaml
timeout_seconds: 120  # Increase timeout
```

For Ollama (CPU inference can be very slow):
```yaml
timeout_seconds: 300  # 5 minutes
```


## Migration Between Providers

### Switching Providers

Configuration change only - no code changes needed:

**Before** (Anthropic):
```yaml
llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-6
```

**After** (Bedrock):
```yaml
llm:
  provider: bedrock
  bedrock_region: us-west-2
  bedrock_model_id: us.anthropic.claude-sonnet-4-6-v1:0
```

Loom automatically handles provider differences (authentication, API format, tool calling).

### Limitations

Not all providers are equal:

- **Ollama**: Native tool calling auto-detected per model (prompt fallback for unsupported models)
- **Azure/Vertex**: May have different rate limits
- **OpenAI/Gemini**: Different models than Claude

Test thoroughly after switching providers.


## Performance Considerations

### Latency

Typical latency by provider (500 input / 1000 output tokens):

| Provider | Typical Latency | P95 Latency |
|----------|----------------|-------------|
| **Anthropic** | 2-4s | 6-8s |
| **Bedrock** | 2-3s | 5-7s |
| **Ollama (GPU)** | 1-2s | 3-5s |
| **Ollama (CPU)** | 10-30s | 60s+ |
| **OpenAI** | 2-4s | 6-8s |
| **Azure OpenAI** | 2-4s | 6-8s |
| **Mistral** | 2-4s | 6-8s |
| **Gemini** | 2-4s | 6-8s |
| **HuggingFace** | 2-5s | 7-10s |

**Note**: Latency depends on network, region, model size, and load.

### Throughput

Requests per minute (approximate):

| Provider | Default Limit | Enterprise Limit |
|----------|---------------|------------------|
| **Anthropic** | 50 req/min | 1000+ req/min |
| **Bedrock** | 100 req/min | Request quota increase |
| **Ollama** | Unlimited | N/A (local) |
| **OpenAI** | 60 req/min | 5000+ req/min |
| **Azure OpenAI** | 60 req/min | Request quota increase |
| **Mistral** | 60 req/min | Request quota increase |
| **Gemini** | 60 req/min | Request quota increase |
| **HuggingFace** | Varies | Varies by backend |


## Observability

### Cost Tracking

All providers report cost through Loom's observability layer:

```go
// Automatic cost tracking in every LLM response (pkg/types/types.go Usage struct)
resp.Usage = types.Usage{
    InputTokens:  500,
    OutputTokens: 1000,
    TotalTokens:  1500,
    CostUSD:      0.0165,
    CacheReadInputTokens:     0, // Anthropic/Gemini prompt caching
    CacheCreationInputTokens: 0, // Anthropic cache write tokens
}
```

Enable Hawk integration to track costs:
```yaml
observability:
  enabled: true
  hawk_endpoint: http://localhost:9090
```

### Token Usage

Monitor token usage to optimize costs:
- Track input/output token ratios
- Identify high-cost requests
- Optimize prompts and patterns
- Set token limits per request


## Implementation Guide

### For Developers Adding New Providers

All LLM providers in Loom implement the `LLMProvider` interface, and all 8 current providers also implement `StreamingLLMProvider`:

```go
// pkg/types/types.go:183
type LLMProvider interface {
    Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error)
    Name() string
    Model() string
}

// pkg/types/types.go:201 - All 8 providers implement this
type StreamingLLMProvider interface {
    LLMProvider
    ChatStream(ctx context.Context, messages []Message, tools []shuttle.Tool,
        tokenCallback TokenCallback) (*LLMResponse, error)
}
```

**Implementation Steps**:

1. **Create provider package**: `pkg/llm/<provider>/`
2. **Implement LLMProvider interface**:
   ```go
   type <Provider>Client struct {
       config Config
       httpClient *http.Client
   }

   func (c *<Provider>Client) Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
       // Convert Loom format to provider API format
       // Make API call
       // Convert provider response to Loom format
       // Return LLMResponse
   }
   ```

3. **Handle authentication**: Support multiple auth methods (keyring, env, config)
4. **Implement tool calling**: Convert Loom tools to provider's function calling format
5. **Add tests**: Unit tests with mocked API, integration tests with real API
6. **Document**: Create `docs/reference/llm-<provider>.md` following existing structure

**Reference Implementations**:
- Direct API client: `pkg/llm/anthropic/client.go`
- AWS SDK integration: `pkg/llm/bedrock/client.go`
- HTTP-only client: `pkg/llm/ollama/client.go`

**Testing Requirements**:
- Unit tests with `-race` detector
- Integration tests with actual API calls
- Error handling tests (auth failures, rate limits, timeouts)
- Tool calling tests (native and fallback methods)


## Roadmap

### Completed (v1.2.0)
- ✅ Anthropic Claude (Opus 4.6, Sonnet 4.6, Opus 4.5, Sonnet 4.5, Haiku 4.5, Opus 4.1)
- ✅ AWS Bedrock (Claude 4.6, 4.5, 4.1 variants)
- ✅ Ollama (local inference, 10 default models, native tool auto-detect)
- ✅ OpenAI (GPT-5, GPT-4.1, o3/o4-mini reasoning)
- ✅ Azure OpenAI (GPT-5, GPT-4.1, o3/o4-mini, API key and Entra ID auth)
- ✅ Mistral AI (Mistral Large/Small, Magistral, Codestral, Devstral)
- ✅ Google Gemini (3 Pro/Flash preview, 2.5 Pro, 2.5 Flash, 2.5 Flash-Lite)
- ✅ HuggingFace Inference API

### Planned
- 📋 Google Vertex AI integration
- 📋 Provider fallback/failover
- 📋 Multi-provider load balancing
- 📋 Custom provider plugins
- 📋 Provider-specific optimizations

### Future Considerations
- Cohere (command models)
- Custom model endpoints
- Provider A/B testing
- Cost optimization engine


## See Also

- [Anthropic Integration](./llm-anthropic/) - Claude models direct
- [AWS Bedrock Integration](./llm-bedrock/) - Claude via AWS
- [Ollama Integration](./llm-ollama/) - Local inference
- [OpenAI Integration](./llm-openai/) - GPT models
- [Azure OpenAI Integration](./llm-azure-openai/) - GPT via Azure
- [Mistral AI Integration](./llm-mistral/) - Mistral models
- [Google Gemini Integration](./llm-gemini/) - Gemini models
- [HuggingFace Integration](./llm-huggingface/) - Open-source models
- [Agent Configuration](./agent-configuration/) - Agent YAML spec
- [Observability Guide](../guides/integration/observability/) - Hawk integration
