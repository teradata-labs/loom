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
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teradata-labs/loom/embedded"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/communication"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/fabric"
	fabricfactory "github.com/teradata-labs/loom/pkg/fabric/factory"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/factory"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/scheduler"
	"github.com/teradata-labs/loom/pkg/server"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/tls"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"github.com/teradata-labs/loom/pkg/tui/components"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Loom gRPC server",
	Long: `Start the Loom Server with gRPC API.

The server will:
- Initialize the agent with configured LLM provider
- Set up session persistence with SQLite
- Enable observability (if configured)
- Listen for gRPC requests on the specified port

Press Ctrl+C to gracefully shutdown.`,
	Run: runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

// createPermissionChecker creates a PermissionChecker based on configuration.
// Returns nil if tool permissions are not configured.
func createPermissionChecker(config *Config, logger *zap.Logger) *shuttle.PermissionChecker {
	if config.Tools.Permissions.YOLO || config.Tools.Permissions.RequireApproval || len(config.Tools.Permissions.AllowedTools) > 0 || len(config.Tools.Permissions.DisabledTools) > 0 {
		logger.Info("Tool permissions configuration",
			zap.Bool("yolo", config.Tools.Permissions.YOLO),
			zap.Bool("require_approval", config.Tools.Permissions.RequireApproval),
			zap.Int("allowed_tools", len(config.Tools.Permissions.AllowedTools)),
			zap.Int("disabled_tools", len(config.Tools.Permissions.DisabledTools)),
			zap.String("default_action", config.Tools.Permissions.DefaultAction),
			zap.Int("timeout_seconds", config.Tools.Permissions.TimeoutSeconds))

		return shuttle.NewPermissionChecker(shuttle.PermissionConfig{
			RequireApproval: config.Tools.Permissions.RequireApproval,
			YOLO:            config.Tools.Permissions.YOLO,
			AllowedTools:    config.Tools.Permissions.AllowedTools,
			DisabledTools:   config.Tools.Permissions.DisabledTools,
			DefaultAction:   config.Tools.Permissions.DefaultAction,
			TimeoutSeconds:  config.Tools.Permissions.TimeoutSeconds,
		})
	}
	return nil
}

// createPromptRegistry creates a PromptRegistry based on configuration.
// Returns nil if prompts are not configured (agents will use hardcoded fallbacks).
func createPromptRegistry(config *Config, logger *zap.Logger) (prompts.PromptRegistry, error) {
	if config.Prompts.Source == "" {
		logger.Info("No prompts configuration found, agents will use hardcoded fallbacks")
		return nil, nil
	}

	logger.Info("Prompts configuration",
		zap.String("source", config.Prompts.Source),
		zap.String("file_dir", config.Prompts.FileDir),
		zap.Int("cache_size", config.Prompts.CacheSize),
		zap.Bool("enable_reload", config.Prompts.EnableReload))

	switch config.Prompts.Source {
	case "file":
		logger.Info("Using FileRegistry for prompts", zap.String("dir", config.Prompts.FileDir))
		registry := prompts.NewFileRegistry(config.Prompts.FileDir)
		if err := registry.Reload(context.Background()); err != nil {
			return nil, fmt.Errorf("failed to load prompts from %s: %w", config.Prompts.FileDir, err)
		}
		return registry, nil

	default:
		return nil, fmt.Errorf("unknown prompts.source: %s (must be 'file')", config.Prompts.Source)
	}
}

// initializeAgentsMap creates an empty agents map.
// All agents are now loaded from $LOOM_DATA_DIR/agents/ directory via the registry system.
// This function simply initializes the map that will be populated by the registry.
func initializeAgentsMap() map[string]*agent.Agent {
	return make(map[string]*agent.Agent)
}

// getDefaultModelForProvider returns the default model ID for the configured provider.
func getDefaultModelForProvider(cfg *Config) string {
	switch cfg.LLM.Provider {
	case "anthropic":
		if cfg.LLM.AnthropicModel != "" {
			return cfg.LLM.AnthropicModel
		}
		return "claude-sonnet-4-5-20250929"
	case "bedrock":
		if cfg.LLM.BedrockModelID != "" {
			return cfg.LLM.BedrockModelID
		}
		return "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	case "ollama":
		if cfg.LLM.OllamaModel != "" {
			return cfg.LLM.OllamaModel
		}
		return "llama3.2"
	case "openai":
		if cfg.LLM.OpenAIModel != "" {
			return cfg.LLM.OpenAIModel
		}
		return "gpt-4o"
	case "azure-openai", "azureopenai":
		// Azure uses deployment ID, not model ID
		return cfg.LLM.AzureOpenAIDeploymentID
	case "mistral":
		if cfg.LLM.MistralModel != "" {
			return cfg.LLM.MistralModel
		}
		return "mistral-large-latest"
	case "gemini":
		if cfg.LLM.GeminiModel != "" {
			return cfg.LLM.GeminiModel
		}
		return "gemini-2.0-flash-exp"
	case "huggingface":
		if cfg.LLM.HuggingFaceModel != "" {
			return cfg.LLM.HuggingFaceModel
		}
		return "meta-llama/Llama-3.1-70B-Instruct"
	default:
		return ""
	}
}

// createLLMProviderFromProtoConfig creates an LLM provider from proto LLMConfig.
// Uses server config for credentials and agent config for provider/model overrides.
func createLLMProviderFromProtoConfig(protoConfig *loomv1.LLMConfig, serverConfig *Config, logger *zap.Logger) (agent.LLMProvider, error) {
	if protoConfig == nil || protoConfig.Provider == "" {
		return nil, fmt.Errorf("proto LLM config is nil or missing provider")
	}

	// Get temperature (use proto config if set, otherwise server default)
	temperature := serverConfig.LLM.Temperature
	if protoConfig.Temperature > 0 {
		temperature = float64(protoConfig.Temperature)
	}

	// Get max tokens (use proto config if set, otherwise server default)
	maxTokens := serverConfig.LLM.MaxTokens
	if protoConfig.MaxTokens > 0 {
		maxTokens = int(protoConfig.MaxTokens)
	}

	timeout := time.Duration(serverConfig.LLM.Timeout) * time.Second

	switch protoConfig.Provider {
	case "anthropic":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.AnthropicModel
		}
		// Use server config API key, or fall back to environment variable
		apiKey := serverConfig.LLM.AnthropicAPIKey
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		return anthropic.NewClient(anthropic.Config{
			APIKey:      apiKey,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	case "bedrock":
		modelID := protoConfig.Model
		if modelID == "" {
			modelID = serverConfig.LLM.BedrockModelID
		}
		return bedrock.NewClient(bedrock.Config{
			Region:          serverConfig.LLM.BedrockRegion,
			AccessKeyID:     serverConfig.LLM.BedrockAccessKeyID,
			SecretAccessKey: serverConfig.LLM.BedrockSecretAccessKey,
			SessionToken:    serverConfig.LLM.BedrockSessionToken,
			Profile:         serverConfig.LLM.BedrockProfile,
			ModelID:         modelID,
			MaxTokens:       maxTokens,
			Temperature:     temperature,
		})

	case "ollama":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.OllamaModel
		}
		return ollama.NewClient(ollama.Config{
			Endpoint:    serverConfig.LLM.OllamaEndpoint,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	case "openai":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.OpenAIModel
		}
		// Use server config API key, or fall back to environment variable
		apiKey := serverConfig.LLM.OpenAIAPIKey
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
		return openai.NewClient(openai.Config{
			APIKey:      apiKey,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	case "azure-openai", "azureopenai":
		deploymentID := protoConfig.Model
		if deploymentID == "" {
			deploymentID = serverConfig.LLM.AzureOpenAIDeploymentID
		}
		return azureopenai.NewClient(azureopenai.Config{
			Endpoint:     serverConfig.LLM.AzureOpenAIEndpoint,
			DeploymentID: deploymentID,
			APIKey:       serverConfig.LLM.AzureOpenAIAPIKey,
			EntraToken:   serverConfig.LLM.AzureOpenAIEntraToken,
			MaxTokens:    maxTokens,
			Temperature:  temperature,
			Timeout:      timeout,
		})

	case "mistral":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.MistralModel
		}
		return mistral.NewClient(mistral.Config{
			APIKey:      serverConfig.LLM.MistralAPIKey,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	case "gemini":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.GeminiModel
		}
		return gemini.NewClient(gemini.Config{
			APIKey:      serverConfig.LLM.GeminiAPIKey,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	case "huggingface":
		model := protoConfig.Model
		if model == "" {
			model = serverConfig.LLM.HuggingFaceModel
		}
		return huggingface.NewClient(huggingface.Config{
			Token:       serverConfig.LLM.HuggingFaceToken,
			Model:       model,
			MaxTokens:   maxTokens,
			Temperature: temperature,
			Timeout:     timeout,
		}), nil

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s (supported: anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, huggingface)", protoConfig.Provider)
	}
}

// exportConfigToEnv exports certain config values as environment variables
// so that builtin tools can access them without requiring explicit parameters.
func exportConfigToEnv(cfg *Config) {
	// Export web search API keys if configured
	if cfg.Tools.WebSearch.BraveAPIKey != "" {
		os.Setenv("BRAVE_API_KEY", cfg.Tools.WebSearch.BraveAPIKey)
	}
	if cfg.Tools.WebSearch.TavilyAPIKey != "" {
		os.Setenv("TAVILY_API_KEY", cfg.Tools.WebSearch.TavilyAPIKey)
	}
	if cfg.Tools.WebSearch.SerpAPIKey != "" {
		os.Setenv("SERPAPI_KEY", cfg.Tools.WebSearch.SerpAPIKey)
	}
}

func runServe(cmd *cobra.Command, args []string) {
	// Validate configuration
	if err := config.Validate(); err != nil {
		log.Fatalf("Configuration validation failed: %v", err)
	}

	// Export config values to environment variables for tools
	exportConfigToEnv(config)

	// Create production logger (stack traces only for ERROR level)
	zapConfig := zap.NewProductionConfig()

	// Parse and set log level from config
	logLevel := zap.InfoLevel // default
	if config.Logging.Level != "" {
		if err := logLevel.UnmarshalText([]byte(config.Logging.Level)); err != nil {
			log.Printf("Invalid log level %q, using INFO: %v", config.Logging.Level, err)
		}
	}
	zapConfig.Level = zap.NewAtomicLevelAt(logLevel)

	// Configure log output file if specified
	if config.Logging.File != "" {
		zapConfig.OutputPaths = []string{config.Logging.File}
		zapConfig.ErrorOutputPaths = []string{config.Logging.File}
	}

	logger, err := zapConfig.Build(zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("Starting Loom Server", zap.String("version", rootCmd.Version))

	// Show actual config file used (not just the --config flag)
	configFileUsed := viper.ConfigFileUsed()
	if configFileUsed != "" {
		logger.Info("Config file loaded", zap.String("path", configFileUsed))
	} else {
		logger.Info("No config file found", zap.String("searched", "$LOOM_DATA_DIR/looms.yaml, ./looms.yaml, /etc/loom/looms.yaml"))
		logger.Info("Using defaults + environment variables")
	}

	// Create tracer based on mode
	var tracer observability.Tracer
	if config.Observability.Enabled {
		mode := config.Observability.Mode
		if mode == "" {
			// Default to service mode if endpoint is set, otherwise embedded
			if config.Observability.HawkEndpoint != "" {
				mode = "service"
			} else {
				mode = "embedded"
			}
		}

		switch mode {
		case "embedded":
			logger.Info("Observability enabled with embedded storage",
				zap.String("storage_type", config.Observability.StorageType),
				zap.String("sqlite_path", config.Observability.SQLitePath))

			storageType := config.Observability.StorageType
			if storageType == "" {
				storageType = "memory"
			}

			flushInterval := 30 * time.Second
			if config.Observability.FlushInterval != "" {
				if duration, err := time.ParseDuration(config.Observability.FlushInterval); err == nil {
					flushInterval = duration
				}
			}

			embeddedTracer, err := observability.NewEmbeddedTracer(&observability.EmbeddedConfig{
				StorageType:   storageType,
				SQLitePath:    config.Observability.SQLitePath,
				FlushInterval: flushInterval,
				Logger:        logger,
			})
			if err != nil {
				logger.Warn("Failed to create embedded tracer, using no-op tracer", zap.Error(err))
				tracer = observability.NewNoOpTracer()
			} else {
				tracer = embeddedTracer
			}

		case "service":
			logger.Info("Observability enabled with service export",
				zap.String("provider", config.Observability.Provider),
				zap.String("endpoint", config.Observability.HawkEndpoint))

			hawkTracer, err := observability.NewHawkTracer(observability.HawkConfig{
				Endpoint: config.Observability.HawkEndpoint,
				APIKey:   config.Observability.HawkAPIKey,
			})
			if err != nil {
				logger.Warn("Failed to create Hawk tracer, using no-op tracer", zap.Error(err))
				tracer = observability.NewNoOpTracer()
			} else {
				tracer = hawkTracer
			}

		case "none":
			logger.Info("Observability disabled by mode=none")
			tracer = observability.NewNoOpTracer()

		default:
			logger.Warn("Unknown observability mode, using no-op tracer", zap.String("mode", mode))
			tracer = observability.NewNoOpTracer()
		}
	} else {
		logger.Info("Observability disabled (use --observability=true to enable)")
		tracer = observability.NewNoOpTracer()
	}

	// Create session store
	logger.Info("Database configuration",
		zap.String("path", config.Database.Path),
		zap.String("driver", config.Database.Driver))
	store, err := agent.NewSessionStore(config.Database.Path, tracer)
	if err != nil {
		logger.Fatal("Failed to create session store", zap.Error(err))
	}
	defer store.Close()

	// Create error store (uses same database for error submission channel)
	errorStore, err := agent.NewSQLiteErrorStore(config.Database.Path, tracer)
	if err != nil {
		logger.Fatal("Failed to create error store", zap.Error(err))
	}
	logger.Info("Error store initialized (error submission channel enabled)")

	// Initialize artifacts directory
	loomDataDir := loomconfig.GetLoomDataDir()
	artifactsDir := filepath.Join(loomDataDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0750); err != nil {
		logger.Fatal("Failed to create artifacts directory", zap.Error(err))
	}
	logger.Info("Artifacts directory initialized", zap.String("path", artifactsDir))

	// Initialize scratchpad directory for agent notes and research
	scratchpadDir := filepath.Join(loomDataDir, "scratchpad")
	if err := os.MkdirAll(scratchpadDir, 0750); err != nil {
		logger.Fatal("Failed to create scratchpad directory", zap.Error(err))
	}
	logger.Info("Scratchpad directory initialized", zap.String("path", scratchpadDir))

	// Copy documentation from docs to loom data directory
	docsDestDir := filepath.Join(loomDataDir, "documentation")
	// Try to find the docs source directory (might be in current dir or parent dir)
	var docsSourceDir string
	possiblePaths := []string{
		"docs",
		filepath.Join("..", "docs"),
	}
	for _, path := range possiblePaths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			docsSourceDir = path
			break
		}
	}

	if docsSourceDir != "" {
		// Copy the docs directory
		if err := copyDir(docsSourceDir, docsDestDir); err != nil {
			logger.Warn("Failed to copy documentation", zap.Error(err))
		} else {
			logger.Info("Documentation copied",
				zap.String("source", docsSourceDir),
				zap.String("dest", docsDestDir))
		}
	} else {
		logger.Warn("Documentation source not found, skipping copy")
	}

	// Copy examples directory to loom data directory
	examplesDestDir := filepath.Join(loomDataDir, "examples")
	// Try to find the examples source directory
	var examplesSourceDir string
	possibleExamplesPaths := []string{
		"examples",
		filepath.Join(".", "examples"),
		filepath.Join(filepath.Dir(os.Args[0]), "..", "examples"),
	}
	for _, path := range possibleExamplesPaths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			examplesSourceDir = path
			break
		}
	}

	if examplesSourceDir != "" {
		// Copy the examples directory
		if err := copyDir(examplesSourceDir, examplesDestDir); err != nil {
			logger.Warn("Failed to copy examples", zap.Error(err))
		} else {
			logger.Info("Examples copied",
				zap.String("source", examplesSourceDir),
				zap.String("dest", examplesDestDir))
		}
	} else {
		logger.Warn("Examples source not found, skipping copy")
	}

	// Copy default weaver agent to loom data directory (if not exists)
	agentsDir := filepath.Join(loomDataDir, "agents")
	weaverDestPath := filepath.Join(agentsDir, "weaver.yaml")
	if _, err := os.Stat(weaverDestPath); os.IsNotExist(err) {
		// Ensure agents directory exists
		if err := os.MkdirAll(agentsDir, 0750); err != nil {
			logger.Warn("Failed to create agents directory", zap.Error(err))
		}

		// Get weaver from embedded files
		weaverData := embedded.GetWeaver()
		logger.Info("Using embedded weaver.yaml")

		// Write to destination
		if err := os.WriteFile(weaverDestPath, weaverData, 0600); err != nil {
			logger.Warn("Failed to copy weaver.yaml to agents directory", zap.Error(err))
		} else {
			logger.Info("Weaver agent installed",
				zap.String("source", "embedded"),
				zap.String("dest", weaverDestPath),
				zap.Int("size", len(weaverData)))
		}
	} else {
		logger.Debug("Weaver agent already exists", zap.String("path", weaverDestPath))
	}

	// Copy default guide agent to loom data directory (if not exists)
	guideDestPath := filepath.Join(agentsDir, "guide.yaml")
	if _, err := os.Stat(guideDestPath); os.IsNotExist(err) {
		// Ensure agents directory exists
		if err := os.MkdirAll(agentsDir, 0750); err != nil {
			logger.Warn("Failed to create agents directory", zap.Error(err))
		}

		// Get guide from embedded files
		guideData := embedded.GetGuide()
		logger.Info("Using embedded guide.yaml")

		// Write to destination
		if err := os.WriteFile(guideDestPath, guideData, 0600); err != nil {
			logger.Warn("Failed to copy guide.yaml to agents directory", zap.Error(err))
		} else {
			logger.Info("Guide agent installed",
				zap.String("source", "embedded"),
				zap.String("dest", guideDestPath),
				zap.Int("size", len(guideData)))
		}
	} else {
		logger.Debug("Guide agent already exists", zap.String("path", guideDestPath))
	}

	// Create agent guide in loom data directory (visible to agents)
	agentGuidePath := filepath.Join(loomDataDir, "START_HERE.md")
	if _, err := os.Stat(agentGuidePath); os.IsNotExist(err) {
		agentGuide := embedded.GetStartHere()
		if err := os.WriteFile(agentGuidePath, agentGuide, 0600); err != nil {
			logger.Warn("Failed to create agent guide", zap.Error(err))
		} else {
			logger.Info("Agent guide created", zap.String("path", agentGuidePath))
		}
	}

	// Create artifact store
	artifactStore, err := artifacts.NewSQLiteStore(config.Database.Path, tracer)
	if err != nil {
		logger.Fatal("Failed to create artifact store", zap.Error(err))
	}
	defer artifactStore.Close()
	logger.Info("Artifact store initialized")

	// Start artifact watcher for hot-reload
	var artifactWatcher *artifacts.Watcher
	if config.Artifacts.HotReload {
		artifactWatcher, err = artifacts.NewWatcher(artifactStore, artifacts.WatcherConfig{
			Enabled:    true,
			DebounceMs: 500,
			Logger:     logger,
			OnCreate: func(artifact *artifacts.Artifact, event string) {
				logger.Info("Artifact detected",
					zap.String("name", artifact.Name),
					zap.String("source", string(artifact.Source)),
					zap.String("content_type", artifact.ContentType))
			},
		})
		if err != nil {
			logger.Warn("Failed to create artifact watcher", zap.Error(err))
		} else {
			ctx := context.Background()
			go func() {
				if err := artifactWatcher.WithTracer(tracer).Start(ctx); err != nil {
					logger.Warn("Artifact watcher error", zap.Error(err))
				}
			}()
			logger.Info("Artifact hot-reload enabled", zap.String("directory", artifactsDir))
		}
	}

	// Note: We pass the store to agents, and each agent creates its own memory instance
	// to avoid sharing the system prompt function across agents.
	// (memory := agent.NewMemoryWithStore(store) would be shared)

	// Create LLM provider
	var llmProvider agent.LLMProvider
	switch config.LLM.Provider {
	case "anthropic":
		llmProvider = anthropic.NewClient(anthropic.Config{
			APIKey:      config.LLM.AnthropicAPIKey,
			Model:       config.LLM.AnthropicModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
		})
		logger.Info("LLM provider: Anthropic",
			zap.String("model", config.LLM.AnthropicModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "bedrock":
		bedrockClient, err := bedrock.NewClient(bedrock.Config{
			Region:          config.LLM.BedrockRegion,
			AccessKeyID:     config.LLM.BedrockAccessKeyID,
			SecretAccessKey: config.LLM.BedrockSecretAccessKey,
			SessionToken:    config.LLM.BedrockSessionToken,
			Profile:         config.LLM.BedrockProfile,
			ModelID:         config.LLM.BedrockModelID,
			MaxTokens:       config.LLM.MaxTokens,
			Temperature:     config.LLM.Temperature,
		})
		if err != nil {
			logger.Fatal("Failed to create Bedrock client", zap.Error(err))
		}
		llmProvider = bedrockClient

		authMethod := "default credentials chain"
		if config.LLM.BedrockAccessKeyID != "" {
			authMethod = "explicit credentials"
		} else if config.LLM.BedrockProfile != "" {
			authMethod = fmt.Sprintf("profile: %s", config.LLM.BedrockProfile)
		}
		logger.Info("LLM provider: AWS Bedrock",
			zap.String("region", config.LLM.BedrockRegion),
			zap.String("model", config.LLM.BedrockModelID),
			zap.String("auth", authMethod),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "ollama":
		llmProvider = ollama.NewClient(ollama.Config{
			Endpoint:    config.LLM.OllamaEndpoint,
			Model:       config.LLM.OllamaModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
			Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
		})
		logger.Info("LLM provider: Ollama",
			zap.String("endpoint", config.LLM.OllamaEndpoint),
			zap.String("model", config.LLM.OllamaModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "openai":
		llmProvider = openai.NewClient(openai.Config{
			APIKey:      config.LLM.OpenAIAPIKey,
			Model:       config.LLM.OpenAIModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
			Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
		})
		logger.Info("LLM provider: OpenAI",
			zap.String("model", config.LLM.OpenAIModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "azure-openai", "azureopenai":
		// Determine auth method
		authMethod := "API key"
		if config.LLM.AzureOpenAIEntraToken != "" {
			authMethod = "Microsoft Entra ID"
		}

		azureClient, err := azureopenai.NewClient(azureopenai.Config{
			Endpoint:     config.LLM.AzureOpenAIEndpoint,
			DeploymentID: config.LLM.AzureOpenAIDeploymentID,
			APIKey:       config.LLM.AzureOpenAIAPIKey,
			EntraToken:   config.LLM.AzureOpenAIEntraToken,
			MaxTokens:    config.LLM.MaxTokens,
			Temperature:  config.LLM.Temperature,
			Timeout:      time.Duration(config.LLM.Timeout) * time.Second,
		})
		if err != nil {
			logger.Fatal("Failed to create Azure OpenAI client", zap.Error(err))
		}
		llmProvider = azureClient
		logger.Info("LLM provider: Azure OpenAI",
			zap.String("endpoint", config.LLM.AzureOpenAIEndpoint),
			zap.String("deployment", config.LLM.AzureOpenAIDeploymentID),
			zap.String("auth", authMethod),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "mistral":
		llmProvider = mistral.NewClient(mistral.Config{
			APIKey:      config.LLM.MistralAPIKey,
			Model:       config.LLM.MistralModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
			Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
		})
		logger.Info("LLM provider: Mistral AI",
			zap.String("model", config.LLM.MistralModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "gemini":
		llmProvider = gemini.NewClient(gemini.Config{
			APIKey:      config.LLM.GeminiAPIKey,
			Model:       config.LLM.GeminiModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
			Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
		})
		logger.Info("LLM provider: Google Gemini",
			zap.String("model", config.LLM.GeminiModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	case "huggingface":
		llmProvider = huggingface.NewClient(huggingface.Config{
			Token:       config.LLM.HuggingFaceToken,
			Model:       config.LLM.HuggingFaceModel,
			MaxTokens:   config.LLM.MaxTokens,
			Temperature: config.LLM.Temperature,
			Timeout:     time.Duration(config.LLM.Timeout) * time.Second,
		})
		logger.Info("LLM provider: HuggingFace",
			zap.String("model", config.LLM.HuggingFaceModel),
			zap.Float64("temperature", config.LLM.Temperature),
			zap.Int("max_tokens", config.LLM.MaxTokens))

	default:
		logger.Fatal("Unsupported LLM provider",
			zap.String("provider", config.LLM.Provider),
			zap.String("supported", "anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, huggingface"))
	}

	// Initialize MCP manager (always, to allow dynamic server addition via TUI/gRPC)
	var mcpManager *mcpManager
	logger.Info("Initializing MCP manager", zap.Int("configured_servers", len(config.MCP.Servers)))
	mcpManager, err = initializeMCPManager(config, logger)
	if err != nil {
		logger.Warn("Failed to initialize MCP manager", zap.Error(err))
		logger.Warn("Agents will not have access to MCP tools")
		mcpManager = nil // Ensure it's nil on failure
	} else {
		logger.Info("MCP manager initialized successfully", zap.Int("servers_started", len(config.MCP.Servers)))
	}

	// Create tool registry for dynamic tool discovery
	var toolRegistry *toolregistry.Registry
	{
		toolDBPath := config.Database.Path + ".tools"
		if config.Database.Path == "" {
			toolDBPath = filepath.Join(loomconfig.GetLoomDataDir(), "tools.db")
		}

		// Create builtin indexer
		builtinIndexer := toolregistry.NewBuiltinIndexer(tracer)

		// Create MCP indexer if MCP manager is available
		var indexers []toolregistry.Indexer
		indexers = append(indexers, builtinIndexer)
		if mcpManager != nil {
			mcpIndexer := toolregistry.NewMCPIndexer(mcpManager.GetManager(), tracer)
			indexers = append(indexers, mcpIndexer)
		}

		var err error
		toolRegistry, err = toolregistry.New(toolregistry.Config{
			DBPath:   toolDBPath,
			LLM:      llmProvider,
			Tracer:   tracer,
			Indexers: indexers,
		})
		if err != nil {
			logger.Warn("Failed to create tool registry", zap.Error(err))
		} else {
			// Index all tools
			ctx := context.Background()
			resp, err := toolRegistry.IndexAll(ctx)
			if err != nil {
				logger.Warn("Failed to index tools", zap.Error(err))
			} else {
				logger.Info("Tool registry initialized",
					zap.Int32("builtin_tools", resp.BuiltinCount),
					zap.Int32("mcp_tools", resp.McpCount),
					zap.Int32("total_tools", resp.TotalCount),
					zap.Int64("duration_ms", resp.DurationMs))
			}
		}
	}

	// Create LLM provider factory for dynamic model switching
	providerFactory := factory.NewProviderFactory(factory.FactoryConfig{
		DefaultProvider: config.LLM.Provider,
		DefaultModel:    getDefaultModelForProvider(config),

		// Anthropic
		AnthropicAPIKey: config.LLM.AnthropicAPIKey,
		AnthropicModel:  config.LLM.AnthropicModel,

		// Bedrock
		BedrockRegion:          config.LLM.BedrockRegion,
		BedrockAccessKeyID:     config.LLM.BedrockAccessKeyID,
		BedrockSecretAccessKey: config.LLM.BedrockSecretAccessKey,
		BedrockSessionToken:    config.LLM.BedrockSessionToken,
		BedrockProfile:         config.LLM.BedrockProfile,
		BedrockModelID:         config.LLM.BedrockModelID,

		// Ollama
		OllamaEndpoint: config.LLM.OllamaEndpoint,
		OllamaModel:    config.LLM.OllamaModel,

		// OpenAI
		OpenAIAPIKey: config.LLM.OpenAIAPIKey,
		OpenAIModel:  config.LLM.OpenAIModel,

		// Azure OpenAI
		AzureOpenAIEndpoint:     config.LLM.AzureOpenAIEndpoint,
		AzureOpenAIDeploymentID: config.LLM.AzureOpenAIDeploymentID,
		AzureOpenAIAPIKey:       config.LLM.AzureOpenAIAPIKey,
		AzureOpenAIEntraToken:   config.LLM.AzureOpenAIEntraToken,

		// Mistral
		MistralAPIKey: config.LLM.MistralAPIKey,
		MistralModel:  config.LLM.MistralModel,

		// Gemini
		GeminiAPIKey: config.LLM.GeminiAPIKey,
		GeminiModel:  config.LLM.GeminiModel,

		// HuggingFace
		HuggingFaceToken: config.LLM.HuggingFaceToken,
		HuggingFaceModel: config.LLM.HuggingFaceModel,

		// Common settings
		MaxTokens:   config.LLM.MaxTokens,
		Temperature: config.LLM.Temperature,
		Timeout:     config.LLM.Timeout,
	})
	logger.Info("LLM provider factory created for dynamic model switching")

	// Create PromptRegistry (if configured)
	promptRegistry, err := createPromptRegistry(config, logger)
	if err != nil {
		logger.Fatal("Failed to create prompt registry", zap.Error(err))
	}
	if promptRegistry != nil {
		logger.Info("PromptRegistry created successfully")
	}

	// Create PermissionChecker (if configured)
	permissionChecker := createPermissionChecker(config, logger)

	// Initialize empty agents map - all agents loaded from $LOOM_DATA_DIR/agents/ via registry below
	agents := initializeAgentsMap()
	logger.Info("Agents will be loaded from $LOOM_DATA_DIR/agents/ directory via registry system")

	// Also load agents from $LOOM_DATA_DIR/agents/ directory (created by meta-agent)
	// Keep registry alive for hot-reload
	var registry *agent.Registry
	configDir := loomconfig.GetLoomDataDir()
	dbPath := config.Database.Path
	if dbPath == "" {
		dbPath = filepath.Join(configDir, "agents.db")
	}

	// Create registry to load meta-agent generated configs
	registry, err = agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:    configDir,
		DBPath:       dbPath,
		LLMProvider:  llmProvider,
		Logger:       logger,
		ToolRegistry: toolRegistry,
	})
	if err != nil {
		logger.Warn("Failed to create agent registry", zap.Error(err))
	} else {
		ctx := context.Background()
		if err := registry.LoadAgents(ctx); err != nil {
			logger.Warn("Failed to load agents from registry", zap.Error(err))
		} else {
			// Get configs from registry
			configs := registry.ListConfigs()
			logger.Info("Found agents from $LOOM_DATA_DIR/agents/", zap.Int("count", len(configs)))

			// Create agents from loaded configs
			for _, cfg := range configs {
				if _, exists := agents[cfg.Name]; exists {
					logger.Info("  Skipping agent (already loaded from looms.yaml)", zap.String("name", cfg.Name))
					continue
				}

				logger.Info("  Loading agent from registry",
					zap.String("name", cfg.Name),
					zap.Int("system_prompt_len", len(cfg.SystemPrompt)))

				// Load backend from backend_path if specified
				var backend fabric.ExecutionBackend
				if backendPath, ok := cfg.Metadata["backend_path"]; ok && backendPath != "" {
					logger.Info("    Backend path", zap.String("path", backendPath))
					backend, err = fabricfactory.LoadFromYAML(backendPath)
					if err != nil {
						logger.Warn("    Failed to load backend", zap.Error(err))
						backend = &mockBackend{} // Fallback to mock
					}
				} else {
					backend = &mockBackend{}
				}

				// Create memory instance for this agent
				// All agents share the global session store (sessions are isolated by agent_id)
				memory := agent.NewMemoryWithStore(store)

				// Configure memory compression if specified
				if cfg.Memory != nil && cfg.Memory.MemoryCompression != nil {
					profile, err := agent.ResolveCompressionProfile(cfg.Memory.MemoryCompression)
					if err != nil {
						logger.Warn("    Failed to resolve compression profile, using defaults",
							zap.Error(err))
					} else {
						memory.SetCompressionProfile(&profile)
						logger.Info("    Compression profile configured",
							zap.String("profile", profile.Name),
							zap.Int("max_l1", profile.MaxL1Messages),
							zap.Int("warning_threshold", profile.WarningThresholdPercent))
					}
				}

				// Set context limits on memory if specified
				if cfg.Llm != nil {
					if cfg.Llm.MaxContextTokens > 0 || cfg.Llm.ReservedOutputTokens > 0 {
						memory.SetContextLimits(
							int(cfg.Llm.MaxContextTokens),
							int(cfg.Llm.ReservedOutputTokens))
					}
				}

				// Inject tracer into memory for observability
				memory.SetTracer(tracer)

				// Create agent from config
				agentOpts := []agent.Option{
					agent.WithName(cfg.Name),
					agent.WithTracer(tracer),
					agent.WithMemory(memory),
					agent.WithErrorStore(errorStore),
					// Note: SharedMemory added via registry.SetSharedMemory() after it's created
				}

				if cfg.Description != "" {
					agentOpts = append(agentOpts, agent.WithDescription(cfg.Description))
				}

				if cfg.SystemPrompt != "" {
					agentOpts = append(agentOpts, agent.WithSystemPrompt(cfg.SystemPrompt))
				}

				// Create config from proto LLMConfig
				// Set max_turns and max_tool_executions from behavior config if specified, otherwise use defaults
				maxTurns := 25          // Default from pkg/agent/types.go:145
				maxToolExecutions := 50 // Default from pkg/agent/types.go:146
				if cfg.Behavior != nil {
					if cfg.Behavior.MaxTurns > 0 {
						maxTurns = int(cfg.Behavior.MaxTurns)
					}
					if cfg.Behavior.MaxToolExecutions > 0 {
						maxToolExecutions = int(cfg.Behavior.MaxToolExecutions)
					}
				}

				agentCfg := &agent.Config{
					Name:              cfg.Name,
					Description:       cfg.Description,
					SystemPrompt:      cfg.SystemPrompt,
					Rom:               cfg.Rom,      // ROM identifier for domain-specific knowledge
					Metadata:          cfg.Metadata, // Metadata includes backend_path for ROM auto-detection
					MaxTurns:          maxTurns,
					MaxToolExecutions: maxToolExecutions,
					EnableTracing:     config.Observability.Enabled,
				}

				// Set context limits if specified in LLM config
				if cfg.Llm != nil {
					if cfg.Llm.MaxContextTokens > 0 {
						agentCfg.MaxContextTokens = int(cfg.Llm.MaxContextTokens)
					}
					if cfg.Llm.ReservedOutputTokens > 0 {
						agentCfg.ReservedOutputTokens = int(cfg.Llm.ReservedOutputTokens)
					}
				}

				// Transfer pattern configuration from proto if present
				if cfg.Behavior != nil && cfg.Behavior.Patterns != nil {
					agentCfg.PatternConfig = &agent.PatternConfig{
						Enabled:            cfg.Behavior.Patterns.Enabled,
						MinConfidence:      float64(cfg.Behavior.Patterns.MinConfidence),
						MaxPatternsPerTurn: int(cfg.Behavior.Patterns.MaxPatternsPerTurn),
						EnableTracking:     cfg.Behavior.Patterns.EnableTracking,
						UseLLMClassifier:   cfg.Behavior.Patterns.UseLlmClassifier,
					}
				}

				agentOpts = append(agentOpts, agent.WithConfig(agentCfg))

				// Add PermissionChecker if configured
				if permissionChecker != nil {
					agentOpts = append(agentOpts, agent.WithPermissionChecker(permissionChecker))
				}

				// Determine LLM provider for this agent
				// If agent has specific LLM config, use it; otherwise use server default
				agentLLMProvider := llmProvider
				if cfg.Llm != nil && cfg.Llm.Provider != "" {
					logger.Info("    Agent has custom LLM configuration",
						zap.String("provider", cfg.Llm.Provider),
						zap.String("model", cfg.Llm.Model))
					customLLM, err := createLLMProviderFromProtoConfig(cfg.Llm, config, logger)
					if err != nil {
						logger.Warn("    Failed to create custom LLM provider, using server default",
							zap.Error(err))
						// Fall back to server default LLM
					} else {
						agentLLMProvider = customLLM
						logger.Info("    Using custom LLM",
							zap.String("provider", cfg.Llm.Provider),
							zap.String("model", cfg.Llm.Model))
					}
				} else {
					logger.Info("    Using server default LLM")
				}

				ag := agent.NewAgent(backend, agentLLMProvider, agentOpts...)

				// Always register shell_execute for all agents
				// For weaver, start in LOOM_DATA_DIR/examples/reference so relative paths work naturally
				var shellTool shuttle.Tool
				if cfg.Name == "weaver" {
					weaverBaseDir := filepath.Join(loomDataDir, "examples", "reference")
					shellTool = builtin.NewShellExecuteTool(weaverBaseDir)
					logger.Info("    Auto-registered shell_execute tool (baseDir: LOOM_DATA_DIR/examples/reference)")
				} else {
					shellTool = builtin.NewShellExecuteTool("")
					logger.Info("    Auto-registered shell_execute tool")
				}
				ag.RegisterTool(shellTool)

				// Always register workspace tool for session-scoped file management
				workspaceTool := builtin.NewWorkspaceTool(artifactStore)
				ag.RegisterTool(workspaceTool)
				logger.Info("    Auto-registered workspace tool")

				// Register builtin tools if specified
				if cfg.Tools != nil && len(cfg.Tools.Builtin) > 0 {
					logger.Info("    Registering builtin tools", zap.Int("count", len(cfg.Tools.Builtin)))
					for _, toolName := range cfg.Tools.Builtin {
						// Skip tools that are registered automatically or through other mechanisms
						skipTools := map[string]bool{
							"shell_execute":                   true, // Auto-registered for all agents
							"tool_search":                     true, // Auto-registered when tool registry available
							"recall_conversation":             true, // Registered with memory/swap layer
							"clear_recalled_context":          true, // Registered with memory/swap layer
							"search_conversation":             true, // Registered with memory/swap layer
							"get_tool_result":                 true, // Async tool result retrieval
							"get_error_details":               true, // Error details retrieval
							"delegate_to_agent":               true, // Coordination tool (registered elsewhere)
							"send_message":                    true, // Communication tool (requires MessageQueue)
							"receive_message":                 true, // Communication tool (requires MessageQueue)
							"shared_memory_write":             true, // Communication tool (requires SharedMemoryStore)
							"shared_memory_read":              true, // Communication tool (requires SharedMemoryStore)
							"top_n_query":                     true, // Presentation tool
							"group_by_query":                  true, // Presentation tool
							"generate_visualization":          true, // Visualization tool
							"generate_workflow_visualization": true, // Visualization tool
						}
						if skipTools[toolName] {
							continue
						}
						// spawn_agent removed

						tool := builtin.ByName(toolName)
						if tool != nil {
							ag.RegisterTool(tool)
							logger.Info("      Tool registered", zap.String("name", toolName))
						} else {
							logger.Warn("      Unknown builtin tool", zap.String("name", toolName))
						}
					}
				}

				// Register MCP tools if specified and MCP manager available
				if cfg.Tools != nil && len(cfg.Tools.Mcp) > 0 && mcpManager != nil {
					logger.Info("    Registering MCP tools", zap.Int("count", len(cfg.Tools.Mcp)))
					ctx := context.Background()
					for _, mcpConfig := range cfg.Tools.Mcp {
						// Check if specific tools requested or all ("*")
						if len(mcpConfig.Tools) == 1 && mcpConfig.Tools[0] == "*" {
							// Register all tools from this MCP server
							beforeCount := ag.ToolCount()
							if err := ag.RegisterMCPServer(ctx, mcpManager.GetManager(), mcpConfig.Server); err != nil {
								logger.Warn("      Failed to register MCP server",
									zap.String("server", mcpConfig.Server),
									zap.Error(err))
							} else {
								afterCount := ag.ToolCount()
								toolsAdded := afterCount - beforeCount
								logger.Info("      MCP server registered",
									zap.String("server", mcpConfig.Server),
									zap.String("tools", "all"),
									zap.Int("tools_added", toolsAdded),
									zap.Int("total_tools", afterCount))
							}
						} else {
							// Register specific tools
							for _, toolName := range mcpConfig.Tools {
								if err := ag.RegisterMCPTool(ctx, mcpManager.GetManager(), mcpConfig.Server, toolName); err != nil {
									logger.Warn("      Failed to register MCP tool",
										zap.String("server", mcpConfig.Server),
										zap.String("tool", toolName),
										zap.Error(err))
								} else {
									logger.Info("      MCP tool registered",
										zap.String("server", mcpConfig.Server),
										zap.String("tool", toolName))
								}
							}
						}
					}
				}

				// Register tool_search and enable dynamic tool registration if tool registry available
				if toolRegistry != nil {
					searchTool := toolregistry.NewSearchTool(toolRegistry)
					ag.RegisterTool(searchTool)
					logger.Info("    Registered tool_search for dynamic discovery")

					// Enable dynamic tool registration for discovered MCP tools
					var mcpMgrAdapter shuttle.MCPManager
					if mcpManager != nil {
						mcpMgrAdapter = &mcpManagerAdapter{mgr: mcpManager.GetManager()}
					}
					ag.SetToolRegistryForDynamicDiscovery(toolRegistry, mcpMgrAdapter)
					logger.Info("    Enabled dynamic tool registration")
				}

				// Get agent GUID from registry
				var agentGUID string
				if registry != nil {
					if info, err := registry.GetAgentInfo(cfg.Name); err == nil {
						agentGUID = info.ID
						// Set stable GUID on agent object to override ephemeral UUID
						ag.SetID(agentGUID)
						logger.Info("    Agent loaded successfully",
							zap.String("name", cfg.Name),
							zap.String("id", agentGUID),
							zap.Int("tool_count", ag.ToolCount()))
					} else {
						logger.Warn("    Failed to get agent GUID from registry",
							zap.String("name", cfg.Name),
							zap.Error(err))
						agentGUID = ag.GetID() // Use agent's internal GUID as fallback
					}
				} else {
					agentGUID = ag.GetID() // Use agent's internal GUID if no registry
				}

				// Store agent with GUID as key for stable references
				agents[agentGUID] = ag
			}
		}
		// DO NOT close registry - keep it alive for hot-reload
	}

	logger.Info("Total agents loaded", zap.Int("count", len(agents)))

	// Create TLS manager if enabled
	var tlsManager *tls.Manager
	if config.Server.TLS.Enabled {
		logger.Info("TLS enabled", zap.String("mode", config.Server.TLS.Mode))

		// Convert config to proto format
		tlsConfig := &loomv1.TLSConfig{
			Enabled: true,
			Mode:    config.Server.TLS.Mode,
		}

		// Add mode-specific config
		switch config.Server.TLS.Mode {
		case "manual":
			tlsConfig.Manual = &loomv1.ManualTLSConfig{
				CertFile: config.Server.TLS.Manual.CertFile,
				KeyFile:  config.Server.TLS.Manual.KeyFile,
				CaFile:   config.Server.TLS.Manual.CAFile,
			}
			logger.Info("  Manual TLS configuration",
				zap.String("cert", config.Server.TLS.Manual.CertFile),
				zap.String("key", config.Server.TLS.Manual.KeyFile))

		case "letsencrypt":
			tlsConfig.Letsencrypt = &loomv1.LetsEncryptConfig{
				Domains:           config.Server.TLS.LetsEncrypt.Domains,
				Email:             config.Server.TLS.LetsEncrypt.Email,
				AcmeDirectoryUrl:  config.Server.TLS.LetsEncrypt.ACMEDirectoryURL,
				HttpChallengePort: int32(config.Server.TLS.LetsEncrypt.HTTPChallengePort),
				CacheDir:          config.Server.TLS.LetsEncrypt.CacheDir,
				AutoRenew:         config.Server.TLS.LetsEncrypt.AutoRenew,
				RenewBeforeDays:   int32(config.Server.TLS.LetsEncrypt.RenewBeforeDays),
				AcceptTos:         config.Server.TLS.LetsEncrypt.AcceptTOS,
			}
			logger.Info("  Let's Encrypt configuration",
				zap.Strings("domains", config.Server.TLS.LetsEncrypt.Domains),
				zap.String("email", config.Server.TLS.LetsEncrypt.Email),
				zap.String("acme", config.Server.TLS.LetsEncrypt.ACMEDirectoryURL))

		case "self-signed":
			tlsConfig.SelfSigned = &loomv1.SelfSignedConfig{
				Hostnames:    config.Server.TLS.SelfSigned.Hostnames,
				IpAddresses:  config.Server.TLS.SelfSigned.IPAddresses,
				ValidityDays: int32(config.Server.TLS.SelfSigned.ValidityDays),
				Organization: config.Server.TLS.SelfSigned.Organization,
			}
			logger.Info("  Self-signed configuration", zap.Strings("hostnames", config.Server.TLS.SelfSigned.Hostnames))
			if len(config.Server.TLS.SelfSigned.IPAddresses) > 0 {
				logger.Info("  IP addresses", zap.Strings("ips", config.Server.TLS.SelfSigned.IPAddresses))
			}

		default:
			logger.Fatal("Unsupported TLS mode",
				zap.String("mode", config.Server.TLS.Mode),
				zap.String("supported", "manual, letsencrypt, or self-signed"))
		}

		var err error
		tlsManager, err = tls.NewManager(tlsConfig)
		if err != nil {
			logger.Fatal("Failed to create TLS manager", zap.Error(err))
		}

		// Start TLS manager (background renewal, etc.)
		ctx := context.Background()
		if err := tlsManager.Start(ctx); err != nil {
			logger.Fatal("Failed to start TLS manager", zap.Error(err))
		}

		// Get TLS status
		status, err := tlsManager.Status(ctx)
		if err != nil {
			logger.Warn("Failed to get TLS status", zap.Error(err))
		} else if status.Certificate != nil {
			logger.Info("  Certificate issuer", zap.String("issuer", status.Certificate.Issuer))
			if len(status.Certificate.Domains) > 0 {
				logger.Info("  Certificate domains", zap.Strings("domains", status.Certificate.Domains))
			}
			expiresAt := time.Unix(status.Certificate.ExpiresAt, 0)
			logger.Info("  Certificate expires",
				zap.String("date", expiresAt.Format("2006-01-02")),
				zap.Int32("days_until_expiry", status.Certificate.DaysUntilExpiry))
		}
	} else {
		logger.Info("TLS disabled")
	}

	// Create gRPC server with optional TLS
	var grpcServer *grpc.Server
	if tlsManager != nil {
		creds := credentials.NewTLS(tlsManager.TLSConfig())
		grpcServer = grpc.NewServer(grpc.Creds(creds))
		logger.Info("gRPC server TLS credentials applied")
	} else {
		grpcServer = grpc.NewServer()
	}
	loomService := server.NewMultiAgentServer(agents, store)
	loomv1.RegisterLoomServiceServer(grpcServer, loomService)

	// Set logger for server operations
	loomService.SetLogger(logger)

	// Set provider factory for dynamic model switching
	loomService.SetProviderFactory(providerFactory)
	logger.Info("Provider factory configured on server for model switching")

	// Set agent registry for workflow execution
	if registry != nil {
		loomService.SetAgentRegistry(registry)
		logger.Info("Agent registry configured on server for workflow execution")
	}

	// Set clarification timeouts from config
	if config.Server.Clarification.ChannelSendTimeoutMs > 0 {
		loomService.SetClarificationConfig(config.Server.Clarification.ChannelSendTimeoutMs)
		logger.Info("Clarification timeout configured",
			zap.Int("channel_send_timeout_ms", config.Server.Clarification.ChannelSendTimeoutMs))
	}

	// Set LLM concurrency limit to prevent rate limiting (especially for workflows with many subagents)
	loomService.SetLLMConcurrencyLimit(2)
	logger.Info("LLM concurrency limit configured to prevent rate limiting", zap.Int("limit", 2))

	// Initialize tri-modal communication system
	logger.Info("Initializing tri-modal communication system")

	// 1. MessageBus for broadcast/pub-sub
	messageBus := communication.NewMessageBus(nil, nil, nil, logger)

	// 2. MessageQueue for point-to-point with SQLite persistence
	messageQueue, err := communication.NewMessageQueue(":memory:", nil, logger)
	if err != nil {
		logger.Fatal("Failed to create message queue", zap.Error(err))
	}

	// 3. SharedMemoryStore for zero-copy data sharing
	sharedMemory, err := communication.NewSharedMemoryStore(nil, logger)
	if err != nil {
		logger.Fatal("Failed to create shared memory store", zap.Error(err))
	}

	// Get global SharedMemoryStore for tool results (different from communication SharedMemoryStore)
	// Configure with reasonable defaults: 500MB memory, compress >1MB objects, 1 hour TTL
	globalSharedMem := storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       500 * 1024 * 1024, // 500MB
		CompressionThreshold: 1 * 1024 * 1024,   // 1MB
		TTLSeconds:           3600,              // 1 hour
	})

	// Configure registry with SharedMemoryStore if it exists
	if registry != nil {
		registry.SetSharedMemory(globalSharedMem)
		logger.Info("SharedMemoryStore configured for agent registry")
	}

	// Inject SharedMemoryStore into already-loaded agents
	for name, ag := range agents {
		ag.SetSharedMemory(globalSharedMem)
		logger.Info("SharedMemoryStore injected into agent", zap.String("agent", name))
	}

	// 3.5. SQL Result Store for queryable large SQL results
	sqlResultStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     storage.GetDefaultLoomDBPath(),
		TTLSeconds: 3600, // 1 hour TTL
	})
	if err != nil {
		logger.Warn("Failed to initialize SQL result store, SQL results will fall back to shared memory", zap.Error(err))
	} else {
		// Inject SQL result store into all agents
		for name, ag := range agents {
			ag.SetSQLResultStore(sqlResultStore)
			logger.Info("SQLResultStore injected into agent", zap.String("agent", name))
		}
	}

	// 4. ReferenceStore for large payload handling
	refStore, err := communication.NewReferenceStoreFromConfig(communication.FactoryConfig{
		Store: communication.StoreConfig{Backend: "memory"},
		GC:    communication.GCConfig{Enabled: false},
	})
	if err != nil {
		logger.Fatal("Failed to create reference store", zap.Error(err))
	}

	// 5. PolicyManager for communication policies
	policyManager := communication.NewPolicyManager()

	// Configure the server with communication components
	err = loomService.ConfigureCommunication(messageBus, messageQueue, sharedMemory, refStore, policyManager, logger)
	if err != nil {
		logger.Fatal("Failed to configure communication system", zap.Error(err))
	}

	logger.Info("Tri-modal communication system initialized successfully")

	// Inject MCP manager into multi-agent server if available
	if mcpManager != nil {
		configPath := filepath.Join(loomconfig.GetLoomDataDir(), "looms.yaml")
		loomService.SetMCPManager(mcpManager.GetManager(), configPath, logger)
		logger.Info("MCP manager injected into multi-agent server")
	}

	// Inject tool registry into multi-agent server for dynamic tool discovery
	if toolRegistry != nil {
		loomService.SetToolRegistry(toolRegistry)
		logger.Info("Tool registry injected into multi-agent server - MCP tools will auto-index on add")
	}

	// Inject artifact store into multi-agent server
	loomService.SetArtifactStore(artifactStore)
	logger.Info("Artifact store injected into multi-agent server - gRPC endpoints enabled")

	// Initialize LearningAgent (self-improvement system)
	logger.Info("Initializing LearningAgent (self-improvement system)")
	var learningAgent *learning.LearningAgent
	var learningService *server.LearningService
	{
		// 1. Open database connection for learning agent
		learningDB, err := sql.Open("sqlite3", config.Database.Path)
		if err != nil {
			logger.Fatal("Failed to open database for learning agent", zap.Error(err))
		}
		defer learningDB.Close()

		// 2. Initialize self-improvement schema
		ctx := context.Background()
		if err := learning.InitSelfImprovementSchema(ctx, learningDB, tracer); err != nil {
			logger.Fatal("Failed to initialize self-improvement schema", zap.Error(err))
		}
		logger.Info("  Self-improvement schema initialized")

		// 3. Create metrics collector
		collector, err := learning.NewMetricsCollector(config.Database.Path, tracer)
		if err != nil {
			logger.Fatal("Failed to create metrics collector", zap.Error(err))
		}
		defer collector.Close()
		logger.Info("  Metrics collector created")

		// 4. Create learning engine
		engine := learning.NewLearningEngine(collector, tracer)
		logger.Info("  Learning engine created")

		// 5. Create pattern effectiveness tracker (with MessageBus)
		tracker := learning.NewPatternEffectivenessTracker(
			learningDB,
			tracer,
			messageBus,
			1*time.Hour,   // windowSize
			5*time.Minute, // flushInterval
		)
		logger.Info("  Pattern effectiveness tracker created")

		// 6. Create learning agent
		// Check for YAML config file first (declarative preferred)
		var learningConfig *loomv1.LearningAgentConfig
		if config.Learning.ConfigPath != "" {
			// Load from explicit config path
			loadedConfig, warnings, err := learning.LoadLearningAgentConfig(config.Learning.ConfigPath)
			if err != nil {
				logger.Fatal("Failed to load learning agent config", zap.String("path", config.Learning.ConfigPath), zap.Error(err))
			}
			learningConfig = loadedConfig
			for _, w := range warnings {
				logger.Warn("Learning agent config warning", zap.String("warning", w))
			}
			logger.Info("  Loaded LearningAgentConfig from YAML", zap.String("path", config.Learning.ConfigPath))
		} else if config.Learning.ConfigDir != "" {
			// Try to load from config directory
			configs, err := learning.LoadLearningAgentConfigs(config.Learning.ConfigDir)
			if err != nil {
				logger.Warn("Failed to load learning agent configs from directory", zap.String("dir", config.Learning.ConfigDir), zap.Error(err))
			} else if len(configs) > 0 {
				learningConfig = configs[0] // Use first config found
				logger.Info("  Loaded LearningAgentConfig from directory",
					zap.String("dir", config.Learning.ConfigDir),
					zap.String("name", learningConfig.Name),
					zap.Int("total_configs", len(configs)))
			}
		}

		// Create learning agent from config or inline settings
		if learningConfig != nil {
			// Use declarative YAML config
			learningAgent, err = learning.NewLearningAgentFromConfig(
				learningDB,
				tracer,
				engine,
				tracker,
				learningConfig,
			)
			if err != nil {
				logger.Fatal("Failed to create learning agent from config", zap.Error(err))
			}
			logger.Info("  LearningAgent created from declarative config",
				zap.String("name", learningConfig.Name),
				zap.String("autonomy", learningConfig.AutonomyLevel.String()),
				zap.String("analysis_interval", learningConfig.AnalysisInterval),
				zap.Strings("domains", learningConfig.Domains))
		} else {
			// Fall back to inline config from looms.yaml
			autonomyLevel := learning.AutonomyManual
			switch config.Learning.AutonomyLevel {
			case "human_approval":
				autonomyLevel = learning.AutonomyHumanApproval
			case "full":
				autonomyLevel = learning.AutonomyFull
			}

			analysisInterval := 1 * time.Hour
			if config.Learning.AnalysisInterval != "" {
				if parsed, err := time.ParseDuration(config.Learning.AnalysisInterval); err == nil {
					analysisInterval = parsed
				}
			}

			learningAgent, err = learning.NewLearningAgent(
				learningDB,
				tracer,
				engine,
				tracker,
				autonomyLevel,
				analysisInterval,
			)
			if err != nil {
				logger.Fatal("Failed to create learning agent", zap.Error(err))
			}
			logger.Info("  LearningAgent created from inline config",
				zap.String("autonomy", config.Learning.AutonomyLevel),
				zap.Duration("analysis_interval", analysisInterval))
		}

		// 7. Create gRPC service wrapper
		learningService = server.NewLearningService(learningAgent)
		logger.Info("  LearningService wrapper created")

		// 8. Register with gRPC server
		loomv1.RegisterLearningAgentServiceServer(grpcServer, learningService)
		logger.Info("  LearningAgentService registered with gRPC")

		// 9. Start autonomous analysis loop
		if err := learningAgent.Start(ctx); err != nil {
			logger.Fatal("Failed to start learning agent", zap.Error(err))
		}
		logger.Info("  LearningAgent analysis loop started")
	}
	logger.Info("LearningAgent initialization complete")

	// Initialize workflow scheduler
	var workflowScheduler *scheduler.Scheduler
	if config.Scheduler.Enabled {
		logger.Info("Initializing workflow scheduler")

		ctx := context.Background()

		workflowDir := config.Scheduler.WorkflowDir
		if workflowDir == "" {
			workflowDir = filepath.Join(loomconfig.GetLoomDataDir(), "workflows")
		}

		schedulerDBPath := config.Scheduler.DBPath
		if schedulerDBPath == "" {
			schedulerDBPath = filepath.Join(loomconfig.GetLoomDataDir(), "scheduler.db")
		}

		// Create orchestrator for workflow execution
		workflowOrchestrator := orchestration.NewOrchestrator(orchestration.Config{
			Registry:     registry,
			Tracer:       tracer,
			Logger:       logger,
			MessageBus:   messageBus,
			SharedMemory: sharedMemory,
		})

		// Create and start scheduler
		var err error
		workflowScheduler, err = scheduler.NewScheduler(ctx, scheduler.Config{
			WorkflowDir:  workflowDir,
			DBPath:       schedulerDBPath,
			Orchestrator: workflowOrchestrator,
			Registry:     registry,
			Tracer:       tracer,
			Logger:       logger,
			HotReload:    config.Scheduler.HotReload,
		})
		if err != nil {
			logger.Fatal("Failed to create workflow scheduler", zap.Error(err))
		}

		if err := workflowScheduler.Start(ctx); err != nil {
			logger.Fatal("Failed to start workflow scheduler", zap.Error(err))
		}

		logger.Info("Workflow scheduler started",
			zap.String("workflow_dir", workflowDir),
			zap.Bool("hot_reload", config.Scheduler.HotReload))

		// Register shutdown handler
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := workflowScheduler.Stop(shutdownCtx); err != nil {
				logger.Error("Scheduler shutdown error", zap.Error(err))
			}
		}()

		// Wire scheduler to multi-agent server for RPC access
		loomService.ConfigureScheduler(workflowScheduler)
		logger.Info("Scheduler configured on server for RPC access")
	}
	logger.Info("Workflow scheduler initialization complete")

	// Configure TLS if enabled
	if tlsManager != nil {
		// Build ServerConfig proto message with TLS config
		tlsProtoConfig := &loomv1.TLSConfig{
			Enabled: true,
			Mode:    config.Server.TLS.Mode,
		}

		// Add mode-specific TLS config
		switch config.Server.TLS.Mode {
		case "manual":
			tlsProtoConfig.Manual = &loomv1.ManualTLSConfig{
				CertFile: config.Server.TLS.Manual.CertFile,
				KeyFile:  config.Server.TLS.Manual.KeyFile,
				CaFile:   config.Server.TLS.Manual.CAFile,
			}
		case "letsencrypt":
			tlsProtoConfig.Letsencrypt = &loomv1.LetsEncryptConfig{
				Domains:           config.Server.TLS.LetsEncrypt.Domains,
				Email:             config.Server.TLS.LetsEncrypt.Email,
				AcmeDirectoryUrl:  config.Server.TLS.LetsEncrypt.ACMEDirectoryURL,
				HttpChallengePort: int32(config.Server.TLS.LetsEncrypt.HTTPChallengePort),
				CacheDir:          config.Server.TLS.LetsEncrypt.CacheDir,
				AutoRenew:         config.Server.TLS.LetsEncrypt.AutoRenew,
				RenewBeforeDays:   int32(config.Server.TLS.LetsEncrypt.RenewBeforeDays),
				AcceptTos:         config.Server.TLS.LetsEncrypt.AcceptTOS,
			}
		case "self-signed":
			tlsProtoConfig.SelfSigned = &loomv1.SelfSignedConfig{
				Hostnames:    config.Server.TLS.SelfSigned.Hostnames,
				IpAddresses:  config.Server.TLS.SelfSigned.IPAddresses,
				ValidityDays: int32(config.Server.TLS.SelfSigned.ValidityDays),
				Organization: config.Server.TLS.SelfSigned.Organization,
			}
		}

		serverConfig := &loomv1.ServerConfig{
			Name: "looms",
			Tls:  tlsProtoConfig,
		}

		loomService.ConfigureTLS(tlsManager, serverConfig)
	}

	// Configure shared memory if enabled
	if config.SharedMemory.Enabled {
		storageConfig := &storage.Config{
			MaxMemoryBytes:       config.SharedMemory.MaxMemoryBytes,
			CompressionThreshold: config.SharedMemory.CompressionThreshold,
			TTLSeconds:           config.SharedMemory.TTLSeconds,
		}

		// Configure disk overflow if enabled
		if config.SharedMemory.DiskOverflow {
			overflow, err := storage.NewDiskOverflowManager(&storage.DiskOverflowConfig{
				CachePath:   config.SharedMemory.DiskCachePath,
				MaxDiskSize: config.SharedMemory.MaxDiskBytes,
				TTLSeconds:  config.SharedMemory.TTLSeconds,
			})
			if err != nil {
				logger.Warn("Failed to create disk overflow manager", zap.Error(err))
			} else {
				storageConfig.OverflowHandler = overflow
			}
		}

		if err := loomService.ConfigureSharedMemory(storageConfig); err != nil {
			logger.Fatal("Failed to configure shared memory", zap.Error(err))
		}

		stats := loomService.SharedMemoryStore().Stats()
		logger.Info("Shared memory enabled",
			zap.Int64("max_mb", config.SharedMemory.MaxMemoryBytes/(1024*1024)),
			zap.Int64("threshold_kb", config.SharedMemory.ThresholdBytes/1024),
			zap.Int64("compression_mb", config.SharedMemory.CompressionThreshold/(1024*1024)))
		if config.SharedMemory.DiskOverflow {
			logger.Info("Disk overflow enabled",
				zap.String("path", config.SharedMemory.DiskCachePath),
				zap.Int64("max_gb", config.SharedMemory.MaxDiskBytes/(1024*1024*1024)))
		}
		_ = stats // For future metrics logging
	}

	// Configure tri-modal communication (inter-agent messaging)
	var (
		bus           *communication.MessageBus
		queue         *communication.MessageQueue
		sharedMemComm *communication.SharedMemoryStore
	)
	{
		commConfig := communication.FactoryConfig{
			Store: communication.StoreConfig{
				Backend: config.Communication.Store.Backend,
				Path:    config.Communication.Store.Path,
			},
			GC: communication.GCConfig{
				Enabled:  config.Communication.GC.Enabled,
				Strategy: config.Communication.GC.Strategy,
				Interval: config.Communication.GC.Interval,
			},
			AutoPromote: communication.AutoPromoteConfigParams{
				Enabled:   config.Communication.AutoPromote.Enabled,
				Threshold: config.Communication.AutoPromote.Threshold,
			},
			Policies: communication.PoliciesConfig{
				AlwaysReference: config.Communication.Policies.AlwaysReference,
				AlwaysValue:     config.Communication.Policies.AlwaysValue,
			},
		}

		// Create reference store
		refStore, err := communication.NewReferenceStoreFromConfig(commConfig)
		if err != nil {
			logger.Fatal("Failed to create reference store", zap.Error(err))
		}

		// Create policy manager
		policyManager := communication.NewPolicyManagerFromConfig(commConfig)

		// Create tri-modal communication components
		// 1. Broadcast Bus for pub/sub
		bus = communication.NewMessageBus(refStore, policyManager, tracer, logger)
		logger.Info("Broadcast bus initialized")

		// 2. Message Queue for point-to-point async messaging
		queuePath := config.Communication.Store.Path
		if queuePath == "" {
			queuePath = config.Database.Path // Fallback to main database
		}
		queue, err = communication.NewMessageQueue(queuePath, tracer, logger)
		if err != nil {
			logger.Fatal("Failed to create message queue", zap.Error(err))
		}
		logger.Info("Message queue initialized", zap.String("path", queuePath))

		// Set agent validator to prevent messages being sent to non-existent agents
		if registry != nil {
			queue.SetAgentValidator(func(agentID string) bool {
				return registry.GetConfig(agentID) != nil
			})
			logger.Info("Agent validator enabled for message queue")
		}

		// 3. Shared Memory for namespace-isolated data sharing
		sharedMemComm, err = communication.NewSharedMemoryStore(tracer, logger)
		if err != nil {
			logger.Fatal("Failed to create shared memory communication", zap.Error(err))
		}
		logger.Info("Shared memory communication initialized")

		// Configure server with tri-modal system
		if err := loomService.ConfigureCommunication(bus, queue, sharedMemComm, refStore, policyManager, logger); err != nil {
			logger.Fatal("Failed to configure tri-modal communication", zap.Error(err))
		}

		logger.Info("Tri-modal communication system enabled",
			zap.String("backend", config.Communication.Store.Backend),
			zap.String("store_path", config.Communication.Store.Path),
			zap.Bool("auto_promote", config.Communication.AutoPromote.Enabled),
			zap.Int64("threshold_kb", config.Communication.AutoPromote.Threshold/1024),
			zap.Int("gc_interval_sec", config.Communication.GC.Interval))

		// Register communication tools with all loaded agents
		// This ensures agents loaded from YAML configs get pub/sub tools
		logger.Info("Registering communication tools with loaded agents...")
		for agentID, ag := range agents {
			commTools := builtin.CommunicationTools(queue, bus, sharedMemComm, agentID)
			ag.RegisterTools(commTools...)
			logger.Info("  Communication tools registered",
				zap.String("agent", agentID),
				zap.Int("num_tools", len(commTools)))
		}
	}

	// Enable reflection if configured
	if config.Server.EnableReflection {
		reflection.Register(grpcServer)
		logger.Info("gRPC reflection enabled")
	}

	// Start hot-reload watchers for pattern libraries
	ctx := context.Background()
	if err := loomService.StartHotReload(ctx, nil); err != nil {
		logger.Warn("Failed to start hot-reload", zap.Error(err))
		// Continue anyway - hot-reload is not critical for server startup
	}

	// Start agent config hot-reload watcher
	if registry != nil {
		// Set reload callback to update running agents
		registry.SetReloadCallback(func(name string, guid string, agentConfig *loomv1.AgentConfig) error {
			logger.Info("Reloading agent in server",
				zap.String("agent", name),
				zap.String("guid", guid))

			// Load backend from backend_path if specified
			var backend fabric.ExecutionBackend
			if backendPath, ok := agentConfig.Metadata["backend_path"]; ok && backendPath != "" {
				logger.Info("  Loading backend", zap.String("path", backendPath))
				loadedBackend, err := fabricfactory.LoadFromYAML(backendPath)
				if err != nil {
					logger.Warn("  Failed to load backend, using mock", zap.Error(err))
					backend = &mockBackend{}
				} else {
					backend = loadedBackend
				}
			} else {
				backend = &mockBackend{}
			}

			// Create agent configuration
			// Set max_turns and max_tool_executions from behavior config if specified, otherwise use defaults
			maxTurns := 25          // Default from pkg/agent/types.go:145
			maxToolExecutions := 50 // Default from pkg/agent/types.go:146
			if agentConfig.Behavior != nil {
				if agentConfig.Behavior.MaxTurns > 0 {
					maxTurns = int(agentConfig.Behavior.MaxTurns)
				}
				if agentConfig.Behavior.MaxToolExecutions > 0 {
					maxToolExecutions = int(agentConfig.Behavior.MaxToolExecutions)
				}
			}

			cfg := &agent.Config{
				Name:              agentConfig.Name,
				Description:       agentConfig.Description,
				MaxTurns:          maxTurns,
				MaxToolExecutions: maxToolExecutions,
				SystemPrompt:      agentConfig.SystemPrompt,
				Rom:               agentConfig.Rom,      // ROM identifier for domain-specific knowledge
				Metadata:          agentConfig.Metadata, // Metadata includes backend_path for ROM auto-detection
				EnableTracing:     config.Observability.Enabled,
			}

			// Set context limits if specified in LLM config
			if agentConfig.Llm != nil {
				if agentConfig.Llm.MaxContextTokens > 0 {
					cfg.MaxContextTokens = int(agentConfig.Llm.MaxContextTokens)
				}
				if agentConfig.Llm.ReservedOutputTokens > 0 {
					cfg.ReservedOutputTokens = int(agentConfig.Llm.ReservedOutputTokens)
				}
			}

			// Create memory instance for this agent
			memory := agent.NewMemoryWithStore(store)

			// Configure memory compression if specified
			if agentConfig.Memory != nil && agentConfig.Memory.MemoryCompression != nil {
				profile, err := agent.ResolveCompressionProfile(agentConfig.Memory.MemoryCompression)
				if err != nil {
					logger.Warn("  Failed to resolve compression profile, using defaults",
						zap.Error(err))
				} else {
					memory.SetCompressionProfile(&profile)
					logger.Info("  Compression profile configured",
						zap.String("profile", profile.Name),
						zap.Int("max_l1", profile.MaxL1Messages),
						zap.Int("warning_threshold", profile.WarningThresholdPercent))
				}
			}

			// Set context limits on memory if specified
			if agentConfig.Llm != nil {
				if agentConfig.Llm.MaxContextTokens > 0 || agentConfig.Llm.ReservedOutputTokens > 0 {
					memory.SetContextLimits(
						int(agentConfig.Llm.MaxContextTokens),
						int(agentConfig.Llm.ReservedOutputTokens))
				}
			}

			// Inject tracer into memory for observability
			memory.SetTracer(tracer)

			// Create agent options
			agentOpts := []agent.Option{
				agent.WithTracer(tracer),
				agent.WithMemory(memory),
				agent.WithErrorStore(errorStore),
				agent.WithSharedMemory(globalSharedMem), // Use global storage SharedMemoryStore, not communication one
				agent.WithConfig(cfg),
			}

			// Add PermissionChecker if configured
			if permissionChecker != nil {
				agentOpts = append(agentOpts, agent.WithPermissionChecker(permissionChecker))
			}

			// Create new agent
			newAgent := agent.NewAgent(backend, llmProvider, agentOpts...)

			// Set stable GUID from registry if provided
			// This ensures the agent maintains the same ID across hot-reloads
			if guid != "" {
				newAgent.SetID(guid)
				logger.Debug("  Set stable GUID on agent", zap.String("guid", guid))
			}

			// Always register shell_execute for all agents
			// For weaver, start in LOOM_DATA_DIR/examples/reference so relative paths work naturally
			var shellTool shuttle.Tool
			if agentConfig.Name == "weaver" {
				weaverBaseDir := filepath.Join(loomDataDir, "examples", "reference")
				shellTool = builtin.NewShellExecuteTool(weaverBaseDir)
				logger.Info("  Auto-registered shell_execute tool (baseDir: LOOM_DATA_DIR/examples/reference)")
			} else {
				shellTool = builtin.NewShellExecuteTool("")
				logger.Info("  Auto-registered shell_execute tool")
			}
			newAgent.RegisterTool(shellTool)

			// Always register workspace tool for session-scoped file management
			workspaceTool := builtin.NewWorkspaceTool(artifactStore)
			newAgent.RegisterTool(workspaceTool)
			logger.Info("  Auto-registered workspace tool")

			// Register builtin tools if specified
			if agentConfig.Tools != nil && len(agentConfig.Tools.Builtin) > 0 {
				logger.Info("  Registering builtin tools", zap.Int("count", len(agentConfig.Tools.Builtin)))
				for _, toolName := range agentConfig.Tools.Builtin {
					// Skip shell_execute since it's already registered
					if toolName == "shell_execute" {
						continue
					}
					// spawn_agent removed

					tool := builtin.ByName(toolName)
					if tool != nil {
						newAgent.RegisterTool(tool)
						logger.Info("    Tool registered", zap.String("name", toolName))
					}
				}
			}

			// Register MCP tools if specified and MCP manager available
			if agentConfig.Tools != nil && len(agentConfig.Tools.Mcp) > 0 && mcpManager != nil {
				logger.Info("  Registering MCP tools", zap.Int("count", len(agentConfig.Tools.Mcp)))
				ctx := context.Background()
				for _, mcpConfig := range agentConfig.Tools.Mcp {
					// Check if specific tools requested or all ("*")
					if len(mcpConfig.Tools) == 1 && mcpConfig.Tools[0] == "*" {
						// Register all tools from this MCP server
						beforeCount := newAgent.ToolCount()
						if err := newAgent.RegisterMCPServer(ctx, mcpManager.GetManager(), mcpConfig.Server); err != nil {
							logger.Warn("    Failed to register MCP server",
								zap.String("server", mcpConfig.Server),
								zap.Error(err))
						} else {
							afterCount := newAgent.ToolCount()
							toolsAdded := afterCount - beforeCount
							logger.Info("    MCP server registered",
								zap.String("server", mcpConfig.Server),
								zap.String("tools", "all"),
								zap.Int("tools_added", toolsAdded),
								zap.Int("total_tools", afterCount))
						}
					} else {
						// Register specific tools
						for _, toolName := range mcpConfig.Tools {
							if err := newAgent.RegisterMCPTool(ctx, mcpManager.GetManager(), mcpConfig.Server, toolName); err != nil {
								logger.Warn("    Failed to register MCP tool",
									zap.String("server", mcpConfig.Server),
									zap.String("tool", toolName),
									zap.Error(err))
							} else {
								logger.Info("    MCP tool registered",
									zap.String("server", mcpConfig.Server),
									zap.String("tool", toolName))
							}
						}
					}
				}
			}

			// Register tool_search and enable dynamic tool registration if tool registry available
			if toolRegistry != nil {
				searchTool := toolregistry.NewSearchTool(toolRegistry)
				newAgent.RegisterTool(searchTool)
				logger.Info("  Registered tool_search for dynamic discovery")

				// Enable dynamic tool registration for discovered MCP tools
				var mcpMgrAdapter shuttle.MCPManager
				if mcpManager != nil {
					mcpMgrAdapter = &mcpManagerAdapter{mgr: mcpManager.GetManager()}
				}
				newAgent.SetToolRegistryForDynamicDiscovery(toolRegistry, mcpMgrAdapter)
				logger.Info("  Enabled dynamic tool registration")
			}

			// Check if agent already exists in server (by GUID)
			existingAgents := loomService.GetAgentIDs()
			agentExists := false
			// Use GUID for comparison (agents map is keyed by GUID now)
			agentGUIDToUse := guid
			if agentGUIDToUse == "" {
				// Fallback to agent's internal GUID if not provided
				agentGUIDToUse = newAgent.GetID()
			}
			for _, id := range existingAgents {
				if id == agentGUIDToUse {
					agentExists = true
					break
				}
			}

			// Add or update agent in server (using GUID)
			if agentExists {
				// Hot-reload: update existing agent
				if err := loomService.UpdateAgent(agentGUIDToUse, newAgent); err != nil {
					return fmt.Errorf("failed to update agent in server: %w", err)
				}
				logger.Info("  Agent reloaded in server successfully",
					zap.String("agent", name),
					zap.String("guid", agentGUIDToUse))
			} else {
				// New agent from metaagent: add to server
				loomService.AddAgent(agentGUIDToUse, newAgent)
				logger.Info("  Agent added to server successfully",
					zap.String("agent", name),
					zap.String("guid", agentGUIDToUse))
			}

			return nil
		})

		go func() {
			watchCtx, watchCancel := context.WithCancel(context.Background())
			defer watchCancel()

			if err := registry.WatchConfigs(watchCtx); err != nil {
				logger.Warn("Agent config watcher stopped", zap.Error(err))
			}
		}()
		logger.Info("Agent config hot-reload enabled (watching $LOOM_DATA_DIR/agents/ and $LOOM_DATA_DIR/workflows/)")
	}

	// Start server
	addr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Fatal("Failed to listen", zap.String("address", addr), zap.Error(err))
	}

	// Print animated logo
	fmt.Println()
	fmt.Println(components.StaticColored("v" + rootCmd.Version))
	fmt.Println()
	logger.Info("Server listening", zap.String("address", addr))

	// Start HTTP server with SSE support (if enabled)
	var httpSrv *server.HTTPServer
	if config.Server.HTTPPort > 0 {
		httpAddr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.HTTPPort)

		// Convert config CORS settings to server CORS config
		corsConfig := server.CORSConfig{
			Enabled:          config.Server.CORS.Enabled,
			AllowedOrigins:   config.Server.CORS.AllowedOrigins,
			AllowedMethods:   config.Server.CORS.AllowedMethods,
			AllowedHeaders:   config.Server.CORS.AllowedHeaders,
			ExposedHeaders:   config.Server.CORS.ExposedHeaders,
			AllowCredentials: config.Server.CORS.AllowCredentials,
			MaxAge:           config.Server.CORS.MaxAge,
		}

		// Validate CORS configuration
		if corsConfig.Enabled {
			hasWildcard := false
			for _, origin := range corsConfig.AllowedOrigins {
				if origin == "*" {
					hasWildcard = true
					break
				}
			}

			// Security validation: wildcard + credentials is invalid
			if hasWildcard && corsConfig.AllowCredentials {
				logger.Fatal("CORS configuration error: cannot use wildcard origins with allow_credentials=true",
					zap.Strings("allowed_origins", corsConfig.AllowedOrigins),
					zap.Bool("allow_credentials", corsConfig.AllowCredentials))
			}

			// Security warning for production
			if hasWildcard {
				logger.Warn("CORS configured with wildcard origins - this is INSECURE for production!",
					zap.String("recommendation", "Set server.cors.allowed_origins to specific domains in production"))
			} else {
				logger.Info("CORS enabled with restricted origins",
					zap.Strings("allowed_origins", corsConfig.AllowedOrigins))
			}
		}

		httpSrv = server.NewHTTPServerWithCORS(loomService, httpAddr, addr, logger, corsConfig)

		go func() {
			logger.Info("Starting HTTP/SSE server", zap.String("address", httpAddr))
			if err := httpSrv.Start(context.Background()); err != nil {
				logger.Error("HTTP server failed", zap.Error(err))
			}
		}()

		// Wait a moment for HTTP server to start
		time.Sleep(100 * time.Millisecond)
		logger.Info("HTTP/REST+SSE endpoints available",
			zap.String("sse_endpoint", fmt.Sprintf("http://%s/v1/weave:stream", httpAddr)),
			zap.String("health_endpoint", fmt.Sprintf("http://%s/health", httpAddr)))
	}

	logger.Info("Ready to weave!")

	// Start message queue monitor for event-driven workflow agent notifications
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	defer cancelMonitor()
	loomService.StartMessageQueueMonitor(monitorCtx)

	// Handle graceful shutdown
	go func() {
		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
		<-sigch
		logger.Info("Shutting down gracefully... (press Ctrl+C again to force)")

		// Start a goroutine to listen for second Ctrl+C (force shutdown)
		go func() {
			<-sigch
			logger.Warn("Force shutdown requested")
			os.Exit(1)
		}()

		// Stop message queue monitor first (prevents new work from starting)
		cancelMonitor()
		logger.Info("Message queue monitor cancelled")

		// Stop HTTP server
		if httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpSrv.Stop(ctx); err != nil {
				logger.Warn("Error stopping HTTP server", zap.Error(err))
			} else {
				logger.Info("HTTP server stopped")
			}
		}

		// Stop hot-reload watchers
		if err := loomService.StopHotReload(); err != nil {
			logger.Warn("Error stopping hot-reload", zap.Error(err))
		}

		// Close agent registry (stops config watcher)
		if registry != nil {
			if err := registry.Close(); err != nil {
				logger.Warn("Error closing registry", zap.Error(err))
			}
		}

		// Stop artifact watcher
		if artifactWatcher != nil {
			if err := artifactWatcher.Stop(); err != nil {
				logger.Warn("Error stopping artifact watcher", zap.Error(err))
			} else {
				logger.Info("Artifact watcher stopped")
			}
		}

		// Close tool registry
		if toolRegistry != nil {
			if err := toolRegistry.Close(); err != nil {
				logger.Warn("Error closing tool registry", zap.Error(err))
			} else {
				logger.Info("Tool registry closed")
			}
		}

		// Stop LearningAgent
		if learningAgent != nil {
			ctx := context.Background()
			if err := learningAgent.Stop(ctx); err != nil {
				logger.Warn("Error stopping learning agent", zap.Error(err))
			} else {
				logger.Info("LearningAgent stopped")
			}
		}

		// Stop tri-modal communication system
		if bus != nil {
			if err := bus.Close(); err != nil {
				logger.Warn("Error closing message bus", zap.Error(err))
			} else {
				logger.Info("Message bus closed")
			}
		}
		if queue != nil {
			if err := queue.Close(); err != nil {
				logger.Warn("Error closing message queue", zap.Error(err))
			} else {
				logger.Info("Message queue closed")
			}
		}
		if sharedMemComm != nil {
			if err := sharedMemComm.Close(); err != nil {
				logger.Warn("Error closing shared memory communication", zap.Error(err))
			} else {
				logger.Info("Shared memory communication closed")
			}
		}

		// Stop MCP manager (close MCP server connections)
		if mcpManager != nil {
			if err := mcpManager.GetManager().Stop(); err != nil {
				logger.Warn("Error stopping MCP manager", zap.Error(err))
			} else {
				logger.Info("MCP manager stopped")
			}
		}

		// Stop TLS manager
		if tlsManager != nil {
			logger.Info("Stopping TLS manager...")
			ctx := context.Background()
			if err := tlsManager.Stop(ctx); err != nil {
				logger.Warn("Error stopping TLS manager", zap.Error(err))
			} else {
				logger.Info("TLS manager stopped")
			}
		}

		// Graceful stop with timeout (10 seconds max)
		// After timeout, force stop to prevent hanging indefinitely
		logger.Info("Stopping gRPC server (waiting for active RPCs to complete)...")
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()

		select {
		case <-done:
			logger.Info("gRPC server stopped gracefully")
		case <-time.After(10 * time.Second):
			logger.Warn("gRPC server graceful stop timeout after 10s, forcing shutdown")
			grpcServer.Stop() // Force stop
		}

		logger.Info("Shutdown complete")
	}()

	if err := grpcServer.Serve(lis); err != nil {
		logger.Fatal("Failed to serve", zap.Error(err))
	}
}

// mockBackend is a temporary backend for testing.
type mockBackend struct{}

func (m *mockBackend) Name() string {
	return "mock"
}

func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{
		Type: "rows",
		Columns: []fabric.Column{
			{Name: "result", Type: "string"},
		},
		Rows: []map[string]interface{}{
			{"result": "Mock backend - not implemented yet"},
		},
		RowCount: 1,
	}, nil
}

func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{
		Name:   resource,
		Type:   "table",
		Fields: []fabric.Field{},
	}, nil
}

func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{}, nil
}

func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}

func (m *mockBackend) Close() error {
	return nil
}

func (m *mockBackend) Ping(ctx context.Context) error {
	return nil
}

func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("operation %s not supported by mock backend", op)
}

// mcpManager holds MCP manager instance for tool registration.
type mcpManager struct {
	manager *manager.Manager
}

// GetManager returns the underlying MCP manager instance.
func (m *mcpManager) GetManager() *manager.Manager {
	return m.manager
}

// initializeMCPManager initializes and starts all configured MCP servers.
func initializeMCPManager(config *Config, logger *zap.Logger) (*mcpManager, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Build MCP manager config
	mcpConfig := manager.Config{
		Servers: make(map[string]manager.ServerConfig),
	}

	for serverName, serverConfig := range config.MCP.Servers {
		logger.Debug("Processing MCP server from config",
			zap.String("name", serverName),
			zap.String("transport", serverConfig.Transport),
			zap.String("url", serverConfig.URL),
			zap.Bool("enabled_in_config", serverConfig.Enabled))

		// Default transport to stdio if not specified
		transport := serverConfig.Transport
		if transport == "" {
			transport = "stdio"
		}

		// Default to enabled if not explicitly set in config
		enabled := serverConfig.Enabled
		// In Go, bool defaults to false, so we treat unset as true
		// Only disable if explicitly set to false in config
		// Since we can't distinguish between unset and false in bool,
		// we just use the value as-is (true = enabled, false = disabled)
		// For backwards compatibility, servers without "enabled" field will be false
		// but the config loader should set defaults

		mcpConfig.Servers[serverName] = manager.ServerConfig{
			Command:          serverConfig.Command,
			Args:             serverConfig.Args,
			Env:              serverConfig.Env,
			Transport:        transport,
			URL:              serverConfig.URL,
			EnableSessions:   serverConfig.EnableSessions,
			EnableResumption: serverConfig.EnableResumption,
			Enabled:          enabled,
			ToolFilter: manager.ToolFilter{
				All: true, // Register all tools from this server
			},
		}
	}

	// Create and start MCP manager
	mcpMgr, err := manager.NewManager(mcpConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP manager: %w", err)
	}

	ctx := context.Background()
	if err := mcpMgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start MCP servers: %w", err)
	}

	return &mcpManager{
		manager: mcpMgr,
	}, nil
}

// mcpManagerAdapter adapts *manager.Manager to shuttle.MCPManager interface.
// This is needed because manager.Manager.GetClient returns (*client.Client, error)
// but the interface requires (interface{}, error) for generic handling.
type mcpManagerAdapter struct {
	mgr *manager.Manager
}

func (a *mcpManagerAdapter) GetClient(serverName string) (interface{}, error) {
	return a.mgr.GetClient(serverName)
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	// Get source directory info
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	// Create destination directory
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}

	// Read source directory contents
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	// Copy each entry
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Recursively copy subdirectory
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	// Read source file
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read source file: %w", err)
	}

	// Get source file info for permissions
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	// Write destination file
	if err := os.WriteFile(dst, data, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}

	return nil
}
