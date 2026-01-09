// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/huggingface"
	"github.com/teradata-labs/loom/pkg/llm/mistral"
	"github.com/teradata-labs/loom/pkg/llm/ollama"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	toolregistry "github.com/teradata-labs/loom/pkg/tools/registry"
	"go.uber.org/zap"
)

// ReloadCallback is called when an agent config changes.
// It receives the agent name and new configuration.
type ReloadCallback func(name string, config *loomv1.AgentConfig) error

// Registry manages agent configurations and instances.
// It provides centralized agent lifecycle management, hot-reloading, and persistence.
type Registry struct {
	mu           sync.RWMutex
	configDir    string
	db           *sql.DB
	agents       map[string]*Agent
	configs      map[string]*loomv1.AgentConfig
	agentInfo    map[string]*AgentInstanceInfo
	logger       *zap.Logger
	watcher      *fsnotify.Watcher
	mcpMgr       *manager.Manager
	llmProvider  LLMProvider
	tracer       observability.Tracer
	sessionStore *SessionStore          // For persistent agent session traces
	toolRegistry *toolregistry.Registry // Tool search registry for dynamic tool discovery
	sharedMemory interface{}            // SharedMemoryStore for large tool result storage
	onReload     ReloadCallback         // Callback when config changes
}

// AgentInstanceInfo tracks runtime information about an agent instance
type AgentInstanceInfo struct {
	ID             string
	Name           string
	Status         string // "running", "stopped", "error", "initializing"
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ActiveSessions int
	TotalMessages  int64
	Error          string
}

// RegistryConfig configures the agent registry
type RegistryConfig struct {
	ConfigDir    string
	DBPath       string
	MCPManager   *manager.Manager
	LLMProvider  LLMProvider
	Logger       *zap.Logger
	Tracer       observability.Tracer
	SessionStore *SessionStore          // For persistent agent session traces
	ToolRegistry *toolregistry.Registry // Tool search registry for dynamic tool discovery

	// Database encryption (opt-in for enterprise deployments)
	EncryptDatabase bool   // Enable SQLCipher encryption
	EncryptionKey   string // Encryption key (or use LOOM_DB_KEY env var)
}

// NewRegistry creates a new agent registry
func NewRegistry(config RegistryConfig) (*Registry, error) {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	// Ensure config directory exists
	if err := ensureDir(filepath.Join(config.ConfigDir, "agents")); err != nil {
		return nil, fmt.Errorf("failed to create agents directory: %w", err)
	}

	// Open SQLite database with optional encryption
	db, err := OpenDB(DBConfig{
		Path:            config.DBPath,
		EncryptDatabase: config.EncryptDatabase,
		EncryptionKey:   config.EncryptionKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open registry database: %w", err)
	}

	// Initialize database schema
	if err := initRegistryDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize registry database: %w", err)
	}

	// Create file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	r := &Registry{
		configDir:    config.ConfigDir,
		db:           db,
		agents:       make(map[string]*Agent),
		configs:      make(map[string]*loomv1.AgentConfig),
		agentInfo:    make(map[string]*AgentInstanceInfo),
		logger:       config.Logger,
		watcher:      watcher,
		mcpMgr:       config.MCPManager,
		llmProvider:  config.LLMProvider,
		tracer:       config.Tracer,
		sessionStore: config.SessionStore,
		toolRegistry: config.ToolRegistry,
	}

	return r, nil
}

// LoadAgents loads all agent configurations from the agents directory and workflows
func (r *Registry) LoadAgents(ctx context.Context) error {
	// Load regular agents (recursively scan subdirectories)
	agentsDir := filepath.Join(r.configDir, "agents")

	var files []string
	err := filepath.WalkDir(agentsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip directories, only collect .yaml files
		if !d.IsDir() && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk agent configs: %w", err)
	}

	r.logger.Info("Loading agent configs", zap.String("dir", agentsDir), zap.Int("count", len(files)))

	for _, file := range files {
		config, err := LoadAgentConfig(file)
		if err != nil {
			r.logger.Error("Failed to load agent config",
				zap.String("file", file),
				zap.Error(err))
			continue
		}

		if err := ValidateAgentConfig(config); err != nil {
			r.logger.Error("Invalid agent config",
				zap.String("file", file),
				zap.String("agent", config.Name),
				zap.Error(err))
			continue
		}

		r.mu.Lock()
		r.configs[config.Name] = config
		r.mu.Unlock()

		r.logger.Info("Loaded agent config",
			zap.String("name", config.Name),
			zap.String("provider", config.Llm.Provider),
			zap.String("model", config.Llm.Model))
	}

	// Load workflows
	if err := r.LoadWorkflows(ctx); err != nil {
		r.logger.Warn("Failed to load workflows", zap.Error(err))
	}

	return nil
}

// LoadWorkflows loads workflow files and registers their coordinator agents
func (r *Registry) LoadWorkflows(ctx context.Context) error {
	workflowsDir := filepath.Join(r.configDir, "workflows")

	var files []string
	err := filepath.WalkDir(workflowsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip directories, only collect .yaml files
		if !d.IsDir() && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk workflow configs: %w", err)
	}

	if len(files) == 0 {
		r.logger.Debug("No workflow files found", zap.String("dir", workflowsDir))
		return nil
	}

	r.logger.Info("Loading workflow configs", zap.String("dir", workflowsDir), zap.Int("count", len(files)))

	for _, file := range files {
		configs, err := LoadWorkflowAgents(file, r.llmProvider)
		if err != nil {
			r.logger.Error("Failed to load workflow agents",
				zap.String("file", file),
				zap.Error(err))
			continue
		}

		// Register all agents from this workflow
		for _, config := range configs {
			if err := ValidateAgentConfig(config); err != nil {
				r.logger.Error("Invalid workflow agent config",
					zap.String("file", file),
					zap.String("agent", config.Name),
					zap.Error(err))
				continue
			}

			r.mu.Lock()
			r.configs[config.Name] = config
			r.mu.Unlock()

			r.logger.Info("Loaded workflow agent",
				zap.String("name", config.Name),
				zap.String("role", config.Metadata["role"]),
				zap.String("workflow", config.Metadata["workflow"]),
				zap.String("workflow_file", file))
		}
	}

	return nil
}

// RegisterConfig registers an agent configuration in the registry
// This is used by the meta-agent factory to add dynamically generated configs
func (r *Registry) RegisterConfig(config *loomv1.AgentConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs[config.Name] = config
}

// CreateAgent instantiates an agent from its configuration
func (r *Registry) CreateAgent(ctx context.Context, name string) (*Agent, error) {
	r.mu.RLock()
	config, exists := r.configs[name]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("agent configuration not found: %s", name)
	}

	// Check if agent already exists
	r.mu.RLock()
	_, running := r.agents[name]
	r.mu.RUnlock()

	if running {
		return nil, fmt.Errorf("agent %s is already running", name)
	}

	r.logger.Info("Creating agent", zap.String("name", name))

	agent, err := r.buildAgent(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to build agent: %w", err)
	}

	// Store agent and info
	r.mu.Lock()
	r.agents[name] = agent
	r.agentInfo[name] = &AgentInstanceInfo{
		ID:        name,
		Name:      name,
		Status:    "stopped",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	r.mu.Unlock()

	r.logger.Info("Agent created successfully", zap.String("name", name))

	return agent, nil
}

// buildAgent creates an agent instance from proto configuration
func (r *Registry) buildAgent(ctx context.Context, config *loomv1.AgentConfig) (*Agent, error) {
	// Create LLM provider from config
	llmProvider, err := r.createLLMProvider(config.Llm)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM provider: %w", err)
	}

	// Build options from config
	opts := []Option{
		WithName(config.Name),
	}

	// Set system prompt if provided
	if config.SystemPrompt != "" {
		opts = append(opts, WithSystemPrompt(config.SystemPrompt))
	}

	// Set description if provided
	if config.Description != "" {
		opts = append(opts, WithDescription(config.Description))
	}

	// Set behavior config (max_tool_executions, max_turns) if provided
	if config.Behavior != nil {
		agentConfig := &Config{
			Name:              config.Name, // Preserve name from config
			MaxToolExecutions: int(config.Behavior.MaxToolExecutions),
			MaxTurns:          int(config.Behavior.MaxTurns),
		}
		// Use defaults if not specified
		if agentConfig.MaxToolExecutions == 0 {
			agentConfig.MaxToolExecutions = 50
		}
		if agentConfig.MaxTurns == 0 {
			agentConfig.MaxTurns = 25 // Default from config_loader.go:258
		}
		opts = append(opts, WithConfig(agentConfig))
	}

	// Set tracer if provided
	if r.tracer != nil {
		opts = append(opts, WithTracer(r.tracer))
	}

	// Create memory with session storage for trace persistence
	var memory *Memory
	var sessionStore *SessionStore

	// Check if per-agent memory configuration is provided
	if config.Memory != nil && config.Memory.Type != "" {
		// Create per-agent session store based on config
		var err error
		sessionStore, err = r.createSessionStore(config.Memory)
		if err != nil {
			r.logger.Warn("Failed to create per-agent session store, using registry default",
				zap.String("agent", config.Name),
				zap.Error(err))
			sessionStore = r.sessionStore
		}
	} else {
		// Use registry's default session store
		sessionStore = r.sessionStore
	}

	if sessionStore != nil {
		memory = NewMemoryWithStore(sessionStore)

		// Configure memory compression if specified
		if config.Memory != nil && config.Memory.MemoryCompression != nil {
			profile, err := ResolveCompressionProfile(config.Memory.MemoryCompression)
			if err != nil {
				r.logger.Warn("Failed to resolve compression profile, using defaults",
					zap.String("agent", config.Name),
					zap.Error(err))
			} else {
				memory.SetCompressionProfile(&profile)
				r.logger.Info("Compression profile configured",
					zap.String("agent", config.Name),
					zap.String("profile", profile.Name),
					zap.Int("max_l1", profile.MaxL1Messages),
					zap.Int("warning_threshold", profile.WarningThresholdPercent))
			}
		}

		// Set context limits on memory if specified
		if config.Llm != nil {
			if config.Llm.MaxContextTokens > 0 || config.Llm.ReservedOutputTokens > 0 {
				memory.SetContextLimits(
					int(config.Llm.MaxContextTokens),
					int(config.Llm.ReservedOutputTokens))
			}
		}

		// Set tracer if provided
		if r.tracer != nil {
			memory.SetTracer(r.tracer)
		}

		opts = append(opts, WithMemory(memory))
	}

	// Set SharedMemory if available for large tool result storage
	if r.sharedMemory != nil {
		opts = append(opts, WithSharedMemory(r.sharedMemory))
	}

	// Create agent with configuration
	agent := NewAgent(
		nil, // Backend optional with MCP tools
		llmProvider,
		opts...,
	)

	// Register MCP tools if configured
	if config.Tools != nil && len(config.Tools.Mcp) > 0 && r.mcpMgr != nil {
		if err := r.registerMCPTools(ctx, agent, config.Tools.Mcp); err != nil {
			return nil, fmt.Errorf("failed to register MCP tools: %w", err)
		}
	}

	// Register builtin tools (file_write, http_request, grpc_call)
	// These are auto-injected so agents can save reports/results
	// Pass agent's PromptRegistry for externalized tool descriptions
	for _, tool := range builtin.All(agent.prompts) {
		agent.RegisterTool(tool)
	}

	// Register tool_search for dynamic tool discovery (if tool registry is available)
	if r.toolRegistry != nil {
		searchTool := toolregistry.NewSearchTool(r.toolRegistry)
		agent.RegisterTool(searchTool)
		r.logger.Debug("Registered tool_search for agent",
			zap.String("agent", config.Name))

		// Enable dynamic tool registration for discovered MCP tools
		// This allows agents to use tools found via tool_search without explicit config
		// Wrap the MCP manager to satisfy the shuttle.MCPManager interface
		var mcpMgrAdapter shuttle.MCPManager
		if r.mcpMgr != nil {
			mcpMgrAdapter = &mcpManagerAdapter{mgr: r.mcpMgr}
		}
		agent.SetToolRegistryForDynamicDiscovery(r.toolRegistry, mcpMgrAdapter)
		r.logger.Debug("Enabled dynamic tool registration for agent",
			zap.String("agent", config.Name))
	}

	// Register custom tools from config
	if config.Tools != nil && len(config.Tools.Custom) > 0 {
		if err := r.registerCustomTools(ctx, agent, config.Tools.Custom); err != nil {
			r.logger.Warn("Failed to register some custom tools",
				zap.String("agent", config.Name),
				zap.Error(err))
		}
	}

	return agent, nil
}

// createLLMProvider creates an LLM provider from configuration
func (r *Registry) createLLMProvider(config *loomv1.LLMConfig) (LLMProvider, error) {
	// If a default provider is configured and no per-agent provider specified, use it
	if r.llmProvider != nil && config.Provider == "" {
		r.logger.Debug("Using default LLM provider")
		return r.llmProvider, nil
	}

	// Create per-agent provider
	if config.Provider == "" {
		return nil, fmt.Errorf("no LLM provider specified in config")
	}

	r.logger.Info("Creating per-agent LLM provider",
		zap.String("provider", config.Provider),
		zap.String("model", config.Model))

	switch config.Provider {
	case "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
		}
		return anthropic.NewClient(anthropic.Config{
			APIKey:      apiKey,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	case "bedrock":
		region := os.Getenv("AWS_REGION")
		if region == "" {
			region = "us-west-2"
		}
		profile := os.Getenv("AWS_PROFILE")
		if profile == "" {
			profile = "default"
		}
		return bedrock.NewClient(bedrock.Config{
			Region:      region,
			Profile:     profile,
			ModelID:     config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true, // Always enable rate limiter for Bedrock to prevent throttling
				Logger:  r.logger,
				// Use defaults for other fields (RequestsPerSecond, TokensPerMinute, etc.)
			},
		})

	case "ollama":
		endpoint := os.Getenv("OLLAMA_ENDPOINT")
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		return ollama.NewClient(ollama.Config{
			Endpoint:    endpoint,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
		}
		return openai.NewClient(openai.Config{
			APIKey:      apiKey,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	case "azure-openai", "azureopenai":
		apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("AZURE_OPENAI_API_KEY environment variable not set")
		}
		endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")
		if endpoint == "" {
			return nil, fmt.Errorf("AZURE_OPENAI_ENDPOINT environment variable not set")
		}
		return azureopenai.NewClient(azureopenai.Config{
			APIKey:       apiKey,
			Endpoint:     endpoint,
			DeploymentID: config.Model,
			MaxTokens:    int(config.MaxTokens),
			Temperature:  float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		})

	case "mistral":
		apiKey := os.Getenv("MISTRAL_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("MISTRAL_API_KEY environment variable not set")
		}
		return mistral.NewClient(mistral.Config{
			APIKey:      apiKey,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	case "gemini":
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY environment variable not set")
		}
		return gemini.NewClient(gemini.Config{
			APIKey:      apiKey,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	case "huggingface":
		apiKey := os.Getenv("HUGGINGFACE_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("HUGGINGFACE_API_KEY environment variable not set")
		}
		return huggingface.NewClient(huggingface.Config{
			Token:       apiKey,
			Model:       config.Model,
			MaxTokens:   int(config.MaxTokens),
			Temperature: float64(config.Temperature),
			RateLimiterConfig: llm.RateLimiterConfig{
				Enabled: true,
				Logger:  r.logger,
			},
		}), nil

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", config.Provider)
	}
}

// registerMCPTools registers MCP tools for an agent
func (r *Registry) registerMCPTools(ctx context.Context, agent *Agent, mcpConfigs []*loomv1.MCPToolConfig) error {
	for _, mcpConfig := range mcpConfigs {
		// Check if we should register all tools (empty list or wildcard "*")
		shouldRegisterAll := len(mcpConfig.Tools) == 0 ||
			(len(mcpConfig.Tools) == 1 && mcpConfig.Tools[0] == "*")

		if shouldRegisterAll {
			// Register all tools from server
			if err := agent.RegisterMCPServer(ctx, r.mcpMgr, mcpConfig.Server); err != nil {
				r.logger.Error("Failed to register MCP server",
					zap.String("server", mcpConfig.Server),
					zap.Error(err))
				return err
			}
		} else {
			// Register specific tools
			for _, toolName := range mcpConfig.Tools {
				if err := agent.RegisterMCPTool(ctx, r.mcpMgr, mcpConfig.Server, toolName); err != nil {
					r.logger.Error("Failed to register MCP tool",
						zap.String("server", mcpConfig.Server),
						zap.String("tool", toolName),
						zap.Error(err))
					return err
				}
			}
		}
	}
	return nil
}

// createSessionStore creates a session store based on memory configuration.
// Supports per-agent memory isolation with different storage backends.
func (r *Registry) createSessionStore(memConfig *loomv1.MemoryConfig) (*SessionStore, error) {
	switch memConfig.Type {
	case "memory", "":
		// In-memory storage - return nil to use in-memory sessions
		return nil, nil

	case "sqlite":
		// SQLite file-based storage
		dbPath := memConfig.Path
		if dbPath == "" {
			return nil, fmt.Errorf("memory.path required for sqlite storage")
		}

		// Expand ~ to home directory
		if strings.HasPrefix(dbPath, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get home directory: %w", err)
			}
			dbPath = filepath.Join(home, dbPath[2:])
		}

		// Create parent directory if needed
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create memory directory: %w", err)
		}

		// Create session store with encryption config
		// TODO: Add per-agent encryption config in proto if needed
		// For now, per-agent stores inherit encryption from env var
		store, err := NewSessionStoreWithConfig(DBConfig{
			Path:            dbPath,
			EncryptDatabase: false, // Opt-in per agent via config
			EncryptionKey:   "",    // Use LOOM_DB_KEY env var if encryption enabled
		}, r.tracer)
		if err != nil {
			return nil, fmt.Errorf("failed to create session store: %w", err)
		}

		r.logger.Info("Created per-agent session store",
			zap.String("type", "sqlite"),
			zap.String("path", dbPath))

		return store, nil

	case "postgres":
		// PostgreSQL storage
		if memConfig.Dsn == "" {
			return nil, fmt.Errorf("memory.dsn required for postgres storage")
		}
		// Note: PostgreSQL session store not yet implemented
		return nil, fmt.Errorf("postgres session store not yet implemented - use sqlite or memory")

	default:
		return nil, fmt.Errorf("unsupported memory type: %s (use 'memory', 'sqlite', or 'postgres')", memConfig.Type)
	}
}

// registerCustomTools registers custom tools from configuration.
// Custom tools can be loaded from Go plugins or other dynamic sources.
func (r *Registry) registerCustomTools(ctx context.Context, agent *Agent, customConfigs []*loomv1.CustomToolConfig) error {
	for _, customConfig := range customConfigs {
		if customConfig.Name == "" {
			r.logger.Warn("Skipping custom tool with empty name")
			continue
		}

		// Log that custom tool loading is not yet fully implemented
		// In the future, this could:
		// 1. Load Go plugins from customConfig.Implementation path
		// 2. Parse tool definitions from JSON/YAML files
		// 3. Connect to external tool servers
		r.logger.Warn("Custom tool configuration detected but dynamic tool loading not yet implemented",
			zap.String("tool", customConfig.Name),
			zap.String("implementation", customConfig.Implementation))

		// For now, we skip custom tools - users should use MCP servers or builtin tools
		// Future implementation would load and register the tool here
	}

	return nil
}

// StartAgent starts a stopped agent
func (r *Registry) StartAgent(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agentInfo[name]
	if !exists {
		return fmt.Errorf("agent not found: %s", name)
	}

	if info.Status == "running" {
		return fmt.Errorf("agent %s is already running", name)
	}

	info.Status = "running"
	info.UpdatedAt = time.Now()

	r.logger.Info("Agent started", zap.String("name", name))

	return nil
}

// StopAgent stops a running agent
func (r *Registry) StopAgent(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agentInfo[name]
	if !exists {
		return fmt.Errorf("agent not found: %s", name)
	}

	if info.Status != "running" {
		return fmt.Errorf("agent %s is not running", name)
	}

	info.Status = "stopped"
	info.UpdatedAt = time.Now()

	r.logger.Info("Agent stopped", zap.String("name", name))

	return nil
}

// GetAgent returns a running agent instance
func (r *Registry) GetAgent(ctx context.Context, name string) (*Agent, error) {
	// Check if agent is already created and running
	r.mu.RLock()
	agent, exists := r.agents[name]
	r.mu.RUnlock()

	if exists {
		return agent, nil
	}

	// Agent not created yet - check if config exists and lazily create it
	r.mu.RLock()
	_, hasConfig := r.configs[name]
	r.mu.RUnlock()

	if !hasConfig {
		return nil, fmt.Errorf("agent not found or not running: %s", name)
	}

	// Lazily create agent from config
	return r.CreateAgent(ctx, name)
}

// CreateEphemeralAgent creates a temporary agent based on a role.
// This implements the collaboration.AgentFactory interface.
// The agent is NOT registered and caller must manage its lifecycle.
func (r *Registry) CreateEphemeralAgent(ctx context.Context, role string) (*Agent, error) {
	r.logger.Info("Creating ephemeral agent", zap.String("role", role))

	// For now, create a basic agent with the default LLM provider
	// TODO: Look up role-specific ephemeral policies from configs
	// TODO: Apply template configuration from policy
	// TODO: Track ephemeral agent spawns and costs

	if r.llmProvider == nil {
		return nil, fmt.Errorf("cannot create ephemeral agent: no LLM provider configured")
	}

	// Create basic agent
	agent := NewAgent(
		nil, // Backend optional
		r.llmProvider,
		WithName(fmt.Sprintf("ephemeral-%s", role)),
		WithDescription(fmt.Sprintf("Ephemeral agent for role: %s", role)),
	)

	r.logger.Info("Ephemeral agent created", zap.String("role", role), zap.String("name", agent.GetName()))

	return agent, nil
}

// GetAgentInfo returns information about an agent
func (r *Registry) GetAgentInfo(name string) (*AgentInstanceInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.agentInfo[name]
	if !exists {
		return nil, fmt.Errorf("agent not found: %s", name)
	}

	return info, nil
}

// ListAgents returns all registered agents
func (r *Registry) ListAgents() []*AgentInstanceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]*AgentInstanceInfo, 0, len(r.agentInfo))
	for _, info := range r.agentInfo {
		infos = append(infos, info)
	}

	return infos
}

// ListConfigs returns all loaded agent configurations (including non-instantiated agents)
func (r *Registry) ListConfigs() []*loomv1.AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	configs := make([]*loomv1.AgentConfig, 0, len(r.configs))
	for _, config := range r.configs {
		configs = append(configs, config)
	}

	return configs
}

// GetConfig returns the config for a specific agent by name
func (r *Registry) GetConfig(name string) *loomv1.AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.configs[name]
}

// DeleteAgent removes an agent
func (r *Registry) DeleteAgent(ctx context.Context, name string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agentInfo[name]
	if !exists {
		return fmt.Errorf("agent not found: %s", name)
	}

	// Check if agent is running
	if info.Status == "running" && !force {
		return fmt.Errorf("agent %s is running, use force=true to delete", name)
	}

	// Remove from runtime
	delete(r.agents, name)
	delete(r.agentInfo, name)
	delete(r.configs, name)

	// Delete config file
	configPath := filepath.Join(r.configDir, "agents", name+".yaml")
	if err := removeFile(configPath); err != nil {
		r.logger.Warn("Failed to delete config file",
			zap.String("path", configPath),
			zap.Error(err))
	}

	r.logger.Info("Agent deleted", zap.String("name", name))

	return nil
}

// ReloadAgent hot-reloads an agent's configuration
func (r *Registry) ReloadAgent(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agentInfo[name]
	if !exists {
		return fmt.Errorf("agent not found: %s", name)
	}

	// Reload config from file
	configPath := filepath.Join(r.configDir, "agents", name+".yaml")
	config, err := LoadAgentConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	if err := ValidateAgentConfig(config); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	r.configs[name] = config
	info.UpdatedAt = time.Now()

	// If agent is running, rebuild it
	if info.Status == "running" {
		agent, err := r.buildAgent(ctx, config)
		if err != nil {
			return fmt.Errorf("failed to rebuild agent: %w", err)
		}
		r.agents[name] = agent
	}

	r.logger.Info("Agent config reloaded", zap.String("name", name))

	return nil
}

// SetReloadCallback sets the callback function to be called when an agent config changes.
// The callback receives the agent name and new configuration, and should update the running agent.
func (r *Registry) SetReloadCallback(cb ReloadCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onReload = cb
}

// SetSharedMemory sets the SharedMemoryStore for agents created by this registry.
// This must be called after registry creation if agents need access to shared memory
// for large tool result storage.
func (r *Registry) SetSharedMemory(sharedMemory interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sharedMemory = sharedMemory
}

// ForceReload manually triggers a reload for the specified agent.
// This bypasses the file watcher and directly calls the reload callback.
// Useful for programmatic reloads (e.g., after metaagent creates an agent) or
// when file watchers are unreliable (e.g., macOS fsnotify issues).
func (r *Registry) ForceReload(ctx context.Context, name string) error {
	// Load config from file
	configPath := filepath.Join(r.configDir, "agents", name+".yaml")
	config, err := LoadAgentConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate config
	if err := ValidateAgentConfig(config); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Update registry's configs map
	r.mu.Lock()
	r.configs[name] = config
	r.mu.Unlock()

	// Call reload callback if set
	r.mu.RLock()
	callback := r.onReload
	r.mu.RUnlock()

	if callback != nil {
		if err := callback(name, config); err != nil {
			return fmt.Errorf("reload callback failed: %w", err)
		}
		r.logger.Info("Agent force-reloaded successfully", zap.String("agent", name))
	} else {
		// Fallback to internal reload if no callback set
		if err := r.ReloadAgent(ctx, name); err != nil {
			return fmt.Errorf("failed to reload agent: %w", err)
		}
	}

	return nil
}

// WatchConfigs watches for config file changes and auto-reloads agents.
//
// Note: fsnotify behavior varies by platform. On Darwin (macOS), the underlying
// FSEvents/kqueue implementation may not reliably detect file modifications in all cases.
// The callback mechanism is solid, but file change detection may require investigation
// or alternative approaches (e.g., polling, explicit reload triggers) on macOS.
func (r *Registry) WatchConfigs(ctx context.Context) error {
	agentsDir := filepath.Join(r.configDir, "agents")
	if err := r.watcher.Add(agentsDir); err != nil {
		return fmt.Errorf("failed to watch agents directory: %w", err)
	}

	// Also watch workflows directory
	workflowsDir := filepath.Join(r.configDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0750); err != nil {
		r.logger.Warn("Failed to create workflows directory", zap.Error(err))
	} else if err := r.watcher.Add(workflowsDir); err != nil {
		r.logger.Warn("Failed to watch workflows directory", zap.Error(err))
	} else {
		r.logger.Info("Started watching workflow configs", zap.String("dir", workflowsDir))
	}

	r.logger.Info("Started watching agent configs", zap.String("dir", agentsDir))

	for {
		select {
		case event, ok := <-r.watcher.Events:
			if !ok {
				return nil
			}

			// Handle both file creation and modification events
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				filename := filepath.Base(event.Name)

				// Check if this is the .reload trigger file
				if filename == ".reload" {
					r.logger.Info("Reload trigger detected, scanning for new agents")

					// Rescan directory for all config files
					if err := r.LoadAgents(ctx); err != nil {
						r.logger.Error("Failed to reload agents", zap.Error(err))
						continue
					}

					// Call reload callback for each newly discovered agent
					r.mu.RLock()
					callback := r.onReload
					configs := make(map[string]*loomv1.AgentConfig)
					for name, config := range r.configs {
						configs[name] = config
					}
					r.mu.RUnlock()

					if callback != nil {
						for name, config := range configs {
							if err := callback(name, config); err != nil {
								r.logger.Debug("Agent already loaded or callback failed",
									zap.String("agent", name),
									zap.Error(err))
							}
						}
					}
					continue
				}

				// Skip directories and non-YAML files
				ext := filepath.Ext(filename)
				if ext != ".yaml" && ext != ".yml" {
					continue
				}

				// Check if it's a directory (fsnotify can trigger for directories)
				info, err := os.Stat(event.Name)
				if err != nil || info.IsDir() {
					continue
				}

				// Config file changed
				name := filename[:len(filename)-len(ext)] // Remove .yaml/.yml extension

				r.logger.Info("Config file changed, reloading",
					zap.String("file", event.Name),
					zap.String("agent", name))

				// Determine if this is a workflow or agent config
				workflowsDir := filepath.Join(r.configDir, "workflows")
				isWorkflow := strings.Contains(event.Name, workflowsDir)

				if isWorkflow {
					// Load workflow agents
					configs, err := LoadWorkflowAgents(event.Name, r.llmProvider)
					if err != nil {
						r.logger.Error("Failed to load workflow file",
							zap.String("workflow", name),
							zap.Error(err))
						continue
					}

					// Register and reload all agents from this workflow
					r.mu.RLock()
					callback := r.onReload
					r.mu.RUnlock()

					for _, config := range configs {
						// Validate config
						if err := ValidateAgentConfig(config); err != nil {
							r.logger.Error("Invalid workflow agent config",
								zap.String("workflow", name),
								zap.String("agent", config.Name),
								zap.Error(err))
							continue
						}

						// Update registry's configs map
						r.mu.Lock()
						r.configs[config.Name] = config
						r.mu.Unlock()

						// Call reload callback if set
						if callback != nil {
							if err := callback(config.Name, config); err != nil {
								r.logger.Error("Workflow agent reload callback failed",
									zap.String("workflow", name),
									zap.String("agent", config.Name),
									zap.Error(err))
							} else {
								r.logger.Info("Workflow agent reloaded successfully",
									zap.String("workflow", name),
									zap.String("agent", config.Name))
							}
						} else {
							// Fallback to internal reload if no callback set
							if err := r.ReloadAgent(ctx, config.Name); err != nil {
								r.logger.Error("Failed to reload workflow agent",
									zap.String("workflow", name),
									zap.String("agent", config.Name),
									zap.Error(err))
							}
						}
					}
				} else {
					// Load single agent config
					config, err := LoadAgentConfig(event.Name)
					if err != nil {
						r.logger.Error("Failed to load config file",
							zap.String("agent", name),
							zap.Error(err))
						continue
					}

					// Validate config
					if err := ValidateAgentConfig(config); err != nil {
						r.logger.Error("Invalid agent config",
							zap.String("agent", name),
							zap.Error(err))
						continue
					}

					// Update registry's configs map
					r.mu.Lock()
					r.configs[name] = config
					r.mu.Unlock()

					// Call reload callback if set
					r.mu.RLock()
					callback := r.onReload
					r.mu.RUnlock()

					if callback != nil {
						if err := callback(name, config); err != nil {
							r.logger.Error("Reload callback failed",
								zap.String("agent", name),
								zap.Error(err))
						} else {
							r.logger.Info("Agent reloaded successfully",
								zap.String("agent", name))
						}
					} else {
						// Fallback to internal reload if no callback set
						if err := r.ReloadAgent(ctx, name); err != nil {
							r.logger.Error("Failed to reload agent",
								zap.String("agent", name),
								zap.Error(err))
						}
					}
				}
			}

		case err, ok := <-r.watcher.Errors:
			if !ok {
				return nil
			}
			r.logger.Error("Watcher error", zap.Error(err))

		case <-ctx.Done():
			r.logger.Info("Stopping config watcher")
			return nil
		}
	}
}

// Close closes the registry and cleans up resources
func (r *Registry) Close() error {
	r.watcher.Close()
	return r.db.Close()
}

// initRegistryDB initializes the SQLite registry database schema
func initRegistryDB(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		config_json TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		active_sessions INTEGER DEFAULT 0,
		total_messages INTEGER DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
	CREATE INDEX IF NOT EXISTS idx_agents_name ON agents(name);
	`

	_, err := db.Exec(schema)
	return err
}

// Helper functions

func ensureDir(path string) error {
	return os.MkdirAll(path, 0750)
}

func removeFile(path string) error {
	// Best effort file removal - ignore if doesn't exist
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
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
