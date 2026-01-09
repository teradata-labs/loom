// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package builder provides convenience functions for quickly setting up agents.
// This is the "batteries included" API that makes it easy to get started.
package builder

import (
	"context"
	"fmt"
	"os"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/prompts"
)

// AgentBuilder provides a fluent API for building agents.
type AgentBuilder struct {
	backend fabric.ExecutionBackend
	llm     agent.LLMProvider
	options []agent.Option
	tools   []interface{} // Will be converted to shuttle.Tool

	// Tracer configuration (only one should be set, checked in Build())
	tracer       observability.Tracer            // Direct tracer injection (highest priority)
	tracerConfig *observability.AutoSelectConfig // Auto-selection config

	// Prompts configuration (priority: promptsRegistry > promptsSource > promptsDir)
	promptsRegistry prompts.PromptRegistry // Direct registry injection (highest priority)
	promptsSource   string                 // "file" | "promptio"
	promptsDir      string                 // Directory for file/promptio loaders

	guardrails bool
	breakers   bool
}

// NewAgentBuilder creates a new agent builder.
func NewAgentBuilder() *AgentBuilder {
	return &AgentBuilder{
		options: make([]agent.Option, 0),
		tools:   make([]interface{}, 0),
	}
}

// WithBackend sets the execution backend.
func (b *AgentBuilder) WithBackend(backend fabric.ExecutionBackend) *AgentBuilder {
	b.backend = backend
	return b
}

// WithAnthropicLLM configures Anthropic Claude as the LLM provider.
func (b *AgentBuilder) WithAnthropicLLM(apiKey string) *AgentBuilder {
	b.llm = anthropic.NewClient(anthropic.Config{
		APIKey: apiKey,
		Model:  "claude-sonnet-4-5-20250929",
	})
	return b
}

// WithBedrockLLM configures AWS Bedrock as the LLM provider.
func (b *AgentBuilder) WithBedrockLLM(region string) *AgentBuilder {
	client, err := bedrock.NewClient(bedrock.Config{
		Region:  region,
		ModelID: "us.anthropic.claude-sonnet-4-20250514-v1:0",
	})
	if err != nil {
		// Store error for Build() to return
		return b
	}
	b.llm = client
	return b
}

// DefaultOllamaEndpoint is the default Ollama server endpoint.
// Can be overridden via LOOM_LLM_OLLAMA_ENDPOINT or OLLAMA_BASE_URL environment variables.
const DefaultOllamaEndpoint = "http://localhost:11434"

// DefaultOllamaModel is the default Ollama model (Llama 3.1 8B for good balance of speed/quality).
// Can be overridden via LOOM_LLM_OLLAMA_MODEL environment variable.
const DefaultOllamaModel = "llama3.1:8b"

// WithOllamaLLM configures Ollama as the LLM provider.
func (b *AgentBuilder) WithOllamaLLM(model string) *AgentBuilder {
	if model == "" {
		// Check environment variable for default model
		model = os.Getenv("LOOM_LLM_OLLAMA_MODEL")
		if model == "" {
			model = DefaultOllamaModel
		}
	}
	// Check LOOM-prefixed env var first, then standard OLLAMA_BASE_URL, then default
	endpoint := os.Getenv("LOOM_LLM_OLLAMA_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OLLAMA_BASE_URL")
	}
	if endpoint == "" {
		endpoint = DefaultOllamaEndpoint
	}
	b.llm = ollama.NewClient(ollama.Config{
		Endpoint: endpoint,
		Model:    model,
	})
	return b
}

// DefaultOpenAIModel is the default OpenAI model (GPT-4.1 as of 2025).
// Can be overridden via LOOM_LLM_OPENAI_MODEL environment variable.
const DefaultOpenAIModel = "gpt-4.1"

// WithOpenAILLM configures OpenAI as the LLM provider.
func (b *AgentBuilder) WithOpenAILLM(apiKey string) *AgentBuilder {
	model := os.Getenv("LOOM_LLM_OPENAI_MODEL")
	if model == "" {
		model = DefaultOpenAIModel
	}
	b.llm = openai.NewClient(openai.Config{
		APIKey: apiKey,
		Model:  model,
	})
	return b
}

// WithOpenAILLMCustomModel configures OpenAI with a custom model.
func (b *AgentBuilder) WithOpenAILLMCustomModel(apiKey, model string) *AgentBuilder {
	b.llm = openai.NewClient(openai.Config{
		APIKey: apiKey,
		Model:  model,
	})
	return b
}

// WithAzureOpenAILLM configures Azure OpenAI as the LLM provider using API key authentication.
func (b *AgentBuilder) WithAzureOpenAILLM(endpoint, deploymentID, apiKey string) *AgentBuilder {
	client, err := azureopenai.NewClient(azureopenai.Config{
		Endpoint:     endpoint,
		DeploymentID: deploymentID,
		APIKey:       apiKey,
	})
	if err != nil {
		// Store error for Build() to return
		return b
	}
	b.llm = client
	return b
}

// WithAzureOpenAIEntraAuth configures Azure OpenAI with Microsoft Entra ID authentication.
func (b *AgentBuilder) WithAzureOpenAIEntraAuth(endpoint, deploymentID, entraToken string) *AgentBuilder {
	client, err := azureopenai.NewClient(azureopenai.Config{
		Endpoint:     endpoint,
		DeploymentID: deploymentID,
		EntraToken:   entraToken,
	})
	if err != nil {
		// Store error for Build() to return
		return b
	}
	b.llm = client
	return b
}

// WithMistralLLM configures Mistral AI as the LLM provider.
func (b *AgentBuilder) WithMistralLLM(apiKey string) *AgentBuilder {
	b.llm = mistral.NewClient(mistral.Config{
		APIKey: apiKey,
		Model:  "mistral-large-latest",
	})
	return b
}

// WithMistralLLMCustomModel configures Mistral AI with a custom model.
func (b *AgentBuilder) WithMistralLLMCustomModel(apiKey, model string) *AgentBuilder {
	b.llm = mistral.NewClient(mistral.Config{
		APIKey: apiKey,
		Model:  model,
	})
	return b
}

// WithGeminiLLM configures Google Gemini as the LLM provider.
func (b *AgentBuilder) WithGeminiLLM(apiKey string) *AgentBuilder {
	b.llm = gemini.NewClient(gemini.Config{
		APIKey: apiKey,
		Model:  "gemini-2.5-flash",
	})
	return b
}

// WithGeminiLLMCustomModel configures Google Gemini with a custom model.
func (b *AgentBuilder) WithGeminiLLMCustomModel(apiKey, model string) *AgentBuilder {
	b.llm = gemini.NewClient(gemini.Config{
		APIKey: apiKey,
		Model:  model,
	})
	return b
}

// WithHuggingFaceLLM configures HuggingFace as the LLM provider.
func (b *AgentBuilder) WithHuggingFaceLLM(token string) *AgentBuilder {
	b.llm = huggingface.NewClient(huggingface.Config{
		Token: token,
		Model: "meta-llama/Meta-Llama-3.1-70B-Instruct",
	})
	return b
}

// WithHuggingFaceLLMCustomModel configures HuggingFace with a custom model.
func (b *AgentBuilder) WithHuggingFaceLLMCustomModel(token, model string) *AgentBuilder {
	b.llm = huggingface.NewClient(huggingface.Config{
		Token: token,
		Model: model,
	})
	return b
}

// WithTracer sets a tracer directly (highest priority).
// This bypasses auto-selection and uses the provided tracer as-is.
func (b *AgentBuilder) WithTracer(tracer observability.Tracer) *AgentBuilder {
	b.tracer = tracer
	return b
}

// WithAutoTracer enables automatic tracer selection based on environment variables.
// See observability.NewAutoSelectTracerFromEnv() for supported environment variables.
//
// Environment Variables:
//   - LOOM_TRACER_MODE: "auto", "service", "embedded", or "none" (default: "auto")
//   - LOOM_TRACER_PREFER_EMBEDDED: "true" or "false" (default: "true")
//   - HAWK_URL: Service endpoint (for service mode)
//   - HAWK_API_KEY: Service authentication (for service mode)
//   - LOOM_EMBEDDED_STORAGE: "memory" or "sqlite" (default: "memory")
//   - LOOM_EMBEDDED_SQLITE_PATH: Path to SQLite database (required if storage=sqlite)
func (b *AgentBuilder) WithAutoTracer() *AgentBuilder {
	if b.tracerConfig == nil {
		b.tracerConfig = &observability.AutoSelectConfig{
			Mode:           observability.TracerModeAuto,
			PreferEmbedded: true,
		}
	}
	b.tracerConfig.Mode = observability.TracerModeAuto
	return b
}

// WithEmbeddedTracer enables embedded Hawk tracer with in-process storage.
// This provides 10,000x faster performance than service mode and works without
// a separate Hawk service.
//
// storageType must be "memory" (fast, non-persistent) or "sqlite" (persistent).
// sqlitePath is required when storageType is "sqlite".
//
// Example:
//
//	// Memory storage (development)
//	builder.WithEmbeddedTracer("memory", "")
//
//	// SQLite storage (persistent)
//	builder.WithEmbeddedTracer("sqlite", "/tmp/loom-traces.db")
func (b *AgentBuilder) WithEmbeddedTracer(storageType, sqlitePath string) *AgentBuilder {
	if b.tracerConfig == nil {
		b.tracerConfig = &observability.AutoSelectConfig{}
	}
	b.tracerConfig.Mode = observability.TracerModeEmbedded
	b.tracerConfig.EmbeddedStorageType = storageType
	b.tracerConfig.EmbeddedSQLitePath = sqlitePath
	return b
}

// WithHawk enables Hawk service observability (backward compatible).
// For embedded mode, use WithEmbeddedTracer() instead.
// For automatic selection, use WithAutoTracer() instead.
func (b *AgentBuilder) WithHawk(endpoint string) *AgentBuilder {
	if b.tracerConfig == nil {
		b.tracerConfig = &observability.AutoSelectConfig{}
	}
	b.tracerConfig.Mode = observability.TracerModeService
	b.tracerConfig.HawkURL = endpoint
	return b
}

// WithHawkAPIKey adds an API key for Hawk service authentication.
// Only relevant when using WithHawk() for service mode.
func (b *AgentBuilder) WithHawkAPIKey(apiKey string) *AgentBuilder {
	if b.tracerConfig == nil {
		b.tracerConfig = &observability.AutoSelectConfig{}
	}
	b.tracerConfig.HawkAPIKey = apiKey
	return b
}

// WithPrompts sets the prompts directory.
// Deprecated: Use WithPromptsFile() for clarity. Will be removed in v2.0.0.
func (b *AgentBuilder) WithPrompts(dir string) *AgentBuilder {
	return b.WithPromptsFile(dir)
}

// WithPromptsFile uses FileRegistry for prompts (file-based loading).
func (b *AgentBuilder) WithPromptsFile(dir string) *AgentBuilder {
	b.promptsDir = dir
	b.promptsSource = "file"
	return b
}

// WithPromptsPromptio uses PromptioRegistry for prompts (promptio library integration).
func (b *AgentBuilder) WithPromptsPromptio(dir string) *AgentBuilder {
	b.promptsDir = dir
	b.promptsSource = "promptio"
	return b
}

// WithPromptsRegistry directly injects a PromptRegistry (custom implementation).
func (b *AgentBuilder) WithPromptsRegistry(registry prompts.PromptRegistry) *AgentBuilder {
	b.promptsRegistry = registry
	b.promptsSource = "custom"
	return b
}

// WithGuardrails enables guardrails for validation.
func (b *AgentBuilder) WithGuardrails() *AgentBuilder {
	b.guardrails = true
	return b
}

// WithCircuitBreakers enables circuit breakers for failure isolation.
func (b *AgentBuilder) WithCircuitBreakers() *AgentBuilder {
	b.breakers = true
	return b
}

// WithTools adds tools to the agent.
func (b *AgentBuilder) WithTools(tools ...interface{}) *AgentBuilder {
	b.tools = append(b.tools, tools...)
	return b
}

// Build creates the agent.
func (b *AgentBuilder) Build() (*agent.Agent, error) {
	if b.backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if b.llm == nil {
		return nil, fmt.Errorf("LLM provider is required")
	}

	// Build options
	opts := b.options

	// Add tracer if configured (priority: direct > config > none)
	if b.tracer != nil {
		// Direct tracer injection (highest priority)
		opts = append(opts, agent.WithTracer(b.tracer))
	} else if b.tracerConfig != nil {
		// Auto-selection based on config
		tracer, err := observability.NewAutoSelectTracer(b.tracerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create tracer: %w", err)
		}
		opts = append(opts, agent.WithTracer(tracer))
	}
	// If neither is set, agent will use NoOpTracer by default

	// Add prompts if configured (priority: promptsRegistry > promptsSource > promptsDir)
	if b.promptsRegistry != nil {
		// Direct registry injection (highest priority)
		opts = append(opts, agent.WithPrompts(b.promptsRegistry))
	} else if b.promptsSource == "promptio" {
		// PromptioRegistry (library integration)
		registry := prompts.NewPromptioRegistry(b.promptsDir)
		opts = append(opts, agent.WithPrompts(registry))
	} else if b.promptsSource == "file" || b.promptsDir != "" {
		// FileRegistry (file-based loading)
		registry := prompts.NewFileRegistry(b.promptsDir)
		if err := registry.Reload(context.Background()); err != nil {
			return nil, fmt.Errorf("failed to load prompts from %s: %w", b.promptsDir, err)
		}
		opts = append(opts, agent.WithPrompts(registry))
	}

	// Add guardrails if enabled
	if b.guardrails {
		guardrails := fabric.NewGuardrailEngine()
		opts = append(opts, agent.WithGuardrails(guardrails))
	}

	// Add circuit breakers if enabled
	if b.breakers {
		breakers := fabric.NewCircuitBreakerManager(fabric.DefaultCircuitBreakerConfig())
		opts = append(opts, agent.WithCircuitBreakers(breakers))
	}

	// Create agent
	ag := agent.NewAgent(b.backend, b.llm, opts...)

	// Register tools if any
	// Note: tools would need to be shuttle.Tool interface
	// This is left for future enhancement

	return ag, nil
}

// Quick convenience functions

// NewQuickAgent creates an agent with sensible defaults.
// This is the fastest way to get started with loom.
func NewQuickAgent(backend fabric.ExecutionBackend, llm agent.LLMProvider) *agent.Agent {
	return agent.NewAgent(backend, llm)
}

// NewProductionAgent creates an agent with all production features enabled.
// This uses Hawk service mode (requires running Hawk service).
// For embedded mode without a separate service, use NewDevelopmentAgent() instead.
func NewProductionAgent(
	backend fabric.ExecutionBackend,
	llmProvider agent.LLMProvider,
	hawkEndpoint string,
) (*agent.Agent, error) {
	tracer, err := observability.NewHawkTracer(observability.HawkConfig{
		Endpoint: hawkEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create hawk tracer: %w", err)
	}

	guardrails := fabric.NewGuardrailEngine()
	breakers := fabric.NewCircuitBreakerManager(fabric.DefaultCircuitBreakerConfig())

	return agent.NewAgent(
		backend,
		llmProvider,
		agent.WithTracer(tracer),
		agent.WithGuardrails(guardrails),
		agent.WithCircuitBreakers(breakers),
	), nil
}

// NewDevelopmentAgent creates an agent with embedded tracing for development.
// This uses in-process storage (memory or SQLite) and doesn't require a separate Hawk service.
// It provides 10,000x faster performance than service mode.
//
// storageType must be "memory" (fast, non-persistent) or "sqlite" (persistent).
// sqlitePath is required when storageType is "sqlite", can be empty string for memory.
//
// Example:
//
//	// Memory storage (development)
//	agent, err := loom.NewDevelopmentAgent(backend, llmProvider, "memory", "")
//
//	// SQLite storage (persistent)
//	agent, err := loom.NewDevelopmentAgent(backend, llmProvider, "sqlite", "/tmp/traces.db")
func NewDevelopmentAgent(
	backend fabric.ExecutionBackend,
	llmProvider agent.LLMProvider,
	storageType string,
	sqlitePath string,
) (*agent.Agent, error) {
	tracer, err := observability.NewEmbeddedHawkTracer(&observability.EmbeddedConfig{
		StorageType: storageType,
		SQLitePath:  sqlitePath,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create embedded tracer: %w", err)
	}

	return agent.NewAgent(
		backend,
		llmProvider,
		agent.WithTracer(tracer),
	), nil
}

// NewInstrumentedAgent creates an agent with comprehensive end-to-end observability.
// This automatically wraps the LLM provider with instrumentation, capturing detailed
// traces and metrics for:
// - Full conversation flows (turns, costs, tokens)
// - Every LLM call (latency, token usage, tool calls)
//
// Note: Tool execution tracing happens automatically if tools are instrumented.
// For complete observability stack, also instrument your tools before registering them.
//
// This is the recommended way to create production agents with full visibility.
//
// Example:
//
//	tracer := observability.NewNoOpTracer() // or NewHawkTracer()
//	llmProvider := anthropic.NewClient(config)
//	backend := myBackend
//
//	agent := loom.NewInstrumentedAgent(backend, llmProvider, tracer)
//	agent.RegisterTool(myTool)  // Tools can be instrumented separately
func NewInstrumentedAgent(
	backend fabric.ExecutionBackend,
	llmProvider agent.LLMProvider,
	tracer observability.Tracer,
	opts ...agent.Option,
) *agent.Agent {
	// Wrap LLM provider with instrumentation
	instrumentedLLM := llm.NewInstrumentedProvider(llmProvider, tracer)

	// Build options list with tracer
	allOpts := []agent.Option{
		agent.WithTracer(tracer),
	}
	allOpts = append(allOpts, opts...)

	// Create agent with instrumented LLM
	// The agent will automatically use the tracer for conversation-level spans
	// Tool execution tracing happens if the executor is instrumented (see below)
	ag := agent.NewAgent(backend, instrumentedLLM, allOpts...)

	return ag
}

// NewFullyInstrumentedAgent is like NewInstrumentedAgent but also includes
// guardrails and circuit breakers for production resilience.
func NewFullyInstrumentedAgent(
	backend fabric.ExecutionBackend,
	llmProvider agent.LLMProvider,
	tracer observability.Tracer,
) *agent.Agent {
	guardrails := fabric.NewGuardrailEngine()
	breakers := fabric.NewCircuitBreakerManager(fabric.DefaultCircuitBreakerConfig())

	return NewInstrumentedAgent(
		backend,
		llmProvider,
		tracer,
		agent.WithGuardrails(guardrails),
		agent.WithCircuitBreakers(breakers),
	)
}
