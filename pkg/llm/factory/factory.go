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
package factory

import (
	"fmt"
	"os"
	"time"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
)

// fallbackMaxOutputTokens is used when neither the caller configured MaxTokens
// nor the built-in catalog has an entry for the selected model. Kept deliberately
// conservative so unknown models don't trip provider-side token-count errors.
const fallbackMaxOutputTokens = 4096

// ProviderFactory creates LLM providers dynamically based on configuration.
type ProviderFactory struct {
	// Current configuration
	config FactoryConfig
}

// FactoryConfig holds configuration for creating LLM providers.
type FactoryConfig struct {
	// Default provider to use
	DefaultProvider string
	DefaultModel    string

	// Anthropic configuration
	AnthropicAPIKey string
	AnthropicModel  string

	// Bedrock configuration
	BedrockRegion          string
	BedrockAccessKeyID     string
	BedrockSecretAccessKey string
	BedrockSessionToken    string
	BedrockProfile         string
	BedrockBearerToken     string
	BedrockModelID         string

	// Ollama configuration
	OllamaEndpoint string
	OllamaModel    string

	// OpenAI configuration
	OpenAIAPIKey string
	OpenAIModel  string

	// Azure OpenAI configuration
	AzureOpenAIEndpoint     string
	AzureOpenAIDeploymentID string
	AzureOpenAIAPIKey       string
	AzureOpenAIEntraToken   string

	// Mistral configuration
	MistralAPIKey string
	MistralModel  string

	// Gemini configuration
	GeminiAPIKey string
	GeminiModel  string

	// HuggingFace configuration
	HuggingFaceToken string
	HuggingFaceModel string

	// Common settings
	MaxTokens   int
	Temperature float64
	Timeout     int // seconds
}

// NewProviderFactory creates a new provider factory.
//
// MaxTokens is deliberately left at zero when the caller did not configure it,
// so that per-provider creation can consult the built-in catalog for the
// model-specific MaxOutputTokens. A non-zero MaxTokens is treated as an
// explicit user override and honored as-is.
func NewProviderFactory(config FactoryConfig) *ProviderFactory {
	if config.Temperature == 0 {
		config.Temperature = 1.0
	}
	if config.Timeout == 0 {
		config.Timeout = 60
	}

	return &ProviderFactory{
		config: config,
	}
}

// resolveMaxOutput returns the per-request output token cap to use when
// constructing a provider client. Precedence:
//  1. Explicit FactoryConfig.MaxTokens (user override, wins over everything)
//  2. Built-in catalog MaxOutputTokens for the resolved provider+model
//  3. fallbackMaxOutputTokens (conservative default for unknown models)
func (f *ProviderFactory) resolveMaxOutput(provider, model string) int {
	if f.config.MaxTokens > 0 {
		return f.config.MaxTokens
	}
	if info := catalog.Lookup(provider, model); info != nil && info.MaxOutputTokens > 0 {
		return int(info.MaxOutputTokens)
	}
	return fallbackMaxOutputTokens
}

// CreateProvider creates an LLM provider for the specified provider type and model.
// Returns interface{} to avoid import cycles (caller should type assert to agent.LLMProvider).
func (f *ProviderFactory) CreateProvider(provider, model string) (interface{}, error) {
	// Use defaults if not specified
	if provider == "" {
		provider = f.config.DefaultProvider
	}
	if model == "" {
		model = f.config.DefaultModel
	}

	switch provider {
	case "anthropic":
		return f.createAnthropicProvider(model)
	case "bedrock":
		return f.createBedrockProvider(model)
	case "ollama":
		return f.createOllamaProvider(model)
	case "openai":
		return f.createOpenAIProvider(model)
	case "azure-openai", "azureopenai":
		return f.createAzureOpenAIProvider(model)
	case "mistral":
		return f.createMistralProvider(model)
	case "gemini":
		return f.createGeminiProvider(model)
	case "huggingface":
		return f.createHuggingFaceProvider(model)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func (f *ProviderFactory) createAnthropicProvider(model string) (interface{}, error) {
	apiKey := f.config.AnthropicAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic API key not configured (set llm.anthropic_api_key or ANTHROPIC_API_KEY)")
	}

	if model == "" {
		model = f.config.AnthropicModel
	}
	if model == "" {
		model = "claude-sonnet-4-5-20250929"
	}

	return anthropic.NewClient(anthropic.Config{
		APIKey:      apiKey,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("anthropic", model),
		Temperature: f.config.Temperature,
	}), nil
}

func (f *ProviderFactory) createBedrockProvider(model string) (interface{}, error) {
	if model == "" {
		model = f.config.BedrockModelID
	}
	if model == "" {
		model = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	}

	region := f.config.BedrockRegion
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}

	return bedrock.NewClient(bedrock.Config{
		Region:          region,
		AccessKeyID:     f.config.BedrockAccessKeyID,
		SecretAccessKey: f.config.BedrockSecretAccessKey,
		SessionToken:    f.config.BedrockSessionToken,
		Profile:         f.config.BedrockProfile,
		BearerToken:     f.config.BedrockBearerToken,
		ModelID:         model,
		MaxTokens:       f.resolveMaxOutput("bedrock", model),
		Temperature:     f.config.Temperature,
	})
}

func (f *ProviderFactory) createOllamaProvider(model string) (interface{}, error) {
	endpoint := f.config.OllamaEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("OLLAMA_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	if model == "" {
		model = f.config.OllamaModel
	}
	if model == "" {
		model = "llama3.2"
	}

	return ollama.NewClient(ollama.Config{
		Endpoint:    endpoint,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("ollama", model),
		Temperature: f.config.Temperature,
		Timeout:     time.Duration(f.config.Timeout) * time.Second,
	}), nil
}

func (f *ProviderFactory) createOpenAIProvider(model string) (interface{}, error) {
	apiKey := f.config.OpenAIAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("openai API key not configured (set llm.openai_api_key or OPENAI_API_KEY)")
	}

	if model == "" {
		model = f.config.OpenAIModel
	}
	if model == "" {
		model = "gpt-4o"
	}

	return openai.NewClient(openai.Config{
		APIKey:      apiKey,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("openai", model),
		Temperature: f.config.Temperature,
		Timeout:     time.Duration(f.config.Timeout) * time.Second,
	}), nil
}

func (f *ProviderFactory) createAzureOpenAIProvider(model string) (interface{}, error) {
	endpoint := f.config.AzureOpenAIEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("AZURE_OPENAI_ENDPOINT")
	}
	if endpoint == "" {
		return nil, fmt.Errorf("azure openai endpoint not configured (set llm.azure_openai_endpoint or AZURE_OPENAI_ENDPOINT)")
	}

	deploymentID := f.config.AzureOpenAIDeploymentID
	if deploymentID == "" {
		deploymentID = os.Getenv("AZURE_OPENAI_DEPLOYMENT_ID")
	}
	if deploymentID == "" {
		return nil, fmt.Errorf("azure openai deployment ID not configured (set llm.azure_openai_deployment_id or AZURE_OPENAI_DEPLOYMENT_ID)")
	}

	apiKey := f.config.AzureOpenAIAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_OPENAI_API_KEY")
	}

	entraToken := f.config.AzureOpenAIEntraToken
	if entraToken == "" {
		entraToken = os.Getenv("AZURE_OPENAI_ENTRA_TOKEN")
	}

	if apiKey == "" && entraToken == "" {
		return nil, fmt.Errorf("azure openai authentication not configured (set llm.azure_openai_api_key or llm.azure_openai_entra_token)")
	}

	return azureopenai.NewClient(azureopenai.Config{
		Endpoint:     endpoint,
		DeploymentID: deploymentID,
		APIKey:       apiKey,
		EntraToken:   entraToken,
		MaxTokens:    f.resolveMaxOutput("azure-openai", model),
		Temperature:  f.config.Temperature,
		Timeout:      time.Duration(f.config.Timeout) * time.Second,
	})
}

func (f *ProviderFactory) createMistralProvider(model string) (interface{}, error) {
	apiKey := f.config.MistralAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("MISTRAL_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("mistral API key not configured (set llm.mistral_api_key or MISTRAL_API_KEY)")
	}

	if model == "" {
		model = f.config.MistralModel
	}
	if model == "" {
		model = "mistral-large-latest"
	}

	return mistral.NewClient(mistral.Config{
		APIKey:      apiKey,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("mistral", model),
		Temperature: f.config.Temperature,
		Timeout:     time.Duration(f.config.Timeout) * time.Second,
	}), nil
}

func (f *ProviderFactory) createGeminiProvider(model string) (interface{}, error) {
	apiKey := f.config.GeminiAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key not configured (set llm.gemini_api_key or GEMINI_API_KEY)")
	}

	if model == "" {
		model = f.config.GeminiModel
	}
	if model == "" {
		model = "gemini-3-flash-preview"
	}

	return gemini.NewClient(gemini.Config{
		APIKey:      apiKey,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("gemini", model),
		Temperature: f.config.Temperature,
		Timeout:     time.Duration(f.config.Timeout) * time.Second,
	}), nil
}

func (f *ProviderFactory) createHuggingFaceProvider(model string) (interface{}, error) {
	token := f.config.HuggingFaceToken
	if token == "" {
		token = os.Getenv("HUGGINGFACE_API_KEY")
	}
	if token == "" {
		token = os.Getenv("HUGGINGFACE_TOKEN") // backward compat
	}
	if token == "" {
		return nil, fmt.Errorf("huggingface token not configured (set llm.huggingface_token or HUGGINGFACE_API_KEY)")
	}

	if model == "" {
		model = f.config.HuggingFaceModel
	}
	if model == "" {
		model = "meta-llama/Llama-3.1-70B-Instruct"
	}

	return huggingface.NewClient(huggingface.Config{
		Token:       token,
		Model:       model,
		MaxTokens:   f.resolveMaxOutput("huggingface", model),
		Temperature: f.config.Temperature,
		Timeout:     time.Duration(f.config.Timeout) * time.Second,
	}), nil
}

// IsProviderAvailable checks if a provider is available (credentials/config present).
func (f *ProviderFactory) IsProviderAvailable(provider string) bool {
	_, err := f.CreateProvider(provider, "")
	return err == nil
}
