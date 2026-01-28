// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/artifacts"
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
// It receives the agent name, agent GUID, and new configuration.
// The GUID is the stable identifier that should be used for agent registration.
type ReloadCallback func(name string, guid string, config *loomv1.AgentConfig) error

// Registry manages agent configurations and instances.
// It provides centralized agent lifecycle management, hot-reloading, and persistence.
type Registry struct {
	mu           sync.RWMutex
	configDir    string
	db           *sql.DB
	agents       map[string]*Agent // Map of agent name -> Agent instance
	configs      map[string]*loomv1.AgentConfig
	agentInfo    map[string]*AgentInstanceInfo // Map of agent GUID -> AgentInstanceInfo
	agentsByName map[string]string             // Map of agent name -> GUID for lookup
	logger       *zap.Logger
	watcher      *fsnotify.Watcher
	mcpMgr       *manager.Manager
	llmProvider  LLMProvider
	tracer       observability.Tracer
	sessionStore *SessionStore          // For persistent agent session traces
	toolRegistry *toolregistry.Registry // Tool search registry for dynamic tool discovery
	sharedMemory interface{}            // SharedMemoryStore for large tool result storage
	onReload     ReloadCallback         // Callback when config changes

	// Agent dependencies (injected by server)
	errorStore        ErrorStore                 // For error tracking and retrieval
	permissionChecker *shuttle.PermissionChecker // For permission validation
	artifactStore     interface{}                // artifacts.Store for workspace tool
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

	// Agent dependencies (injected by server)
	ErrorStore        ErrorStore                 // For error tracking and retrieval
	PermissionChecker *shuttle.PermissionChecker // For permission validation
	ArtifactStore     interface{}                // artifacts.Store for workspace tool

	// Database encryption (opt-in for enterprise deployments)
	EncryptDatabase bool   // Enable SQLCipher encryption
	EncryptionKey   string // Encryption key (or use LOOM_DB_KEY env var)
}

// NewRegistry creates a new agent registry
func NewRegistry(config RegistryConfig) (*Registry, error) {
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	// Ensure config directories exist
	if err := ensureDir(filepath.Join(config.ConfigDir, "agents")); err != nil {
		return nil, fmt.Errorf("failed to create agents directory: %w", err)
	}
	if err := ensureDir(filepath.Join(config.ConfigDir, "workflows")); err != nil {
		return nil, fmt.Errorf("failed to create workflows directory: %w", err)
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
		configDir:         config.ConfigDir,
		db:                db,
		agents:            make(map[string]*Agent),
		configs:           make(map[string]*loomv1.AgentConfig),
		agentInfo:         make(map[string]*AgentInstanceInfo),
		agentsByName:      make(map[string]string),
		logger:            config.Logger,
		watcher:           watcher,
		mcpMgr:            config.MCPManager,
		llmProvider:       config.LLMProvider,
		tracer:            config.Tracer,
		sessionStore:      config.SessionStore,
		toolRegistry:      config.ToolRegistry,
		errorStore:        config.ErrorStore,
		permissionChecker: config.PermissionChecker,
		artifactStore:     config.ArtifactStore,
	}

	// Load existing agents from database to restore GUIDs
	if err := r.loadAgentsFromDB(); err != nil {
		config.Logger.Warn("Failed to load agents from database", zap.Error(err))
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

		// Ensure agent has a GUID in the database
		// Check if agent already exists (loaded from DB)
		existingGUID, exists := r.agentsByName[config.Name]
		if exists {
			// Agent exists, update config in database
			info := r.agentInfo[existingGUID]
			info.UpdatedAt = time.Now()
			r.mu.Unlock()
			if err := r.persistAgentInfo(info, config); err != nil {
				r.logger.Warn("Failed to update agent config in database",
					zap.String("name", config.Name),
					zap.String("id", existingGUID),
					zap.Error(err))
			} else {
				r.logger.Info("Updated agent config",
					zap.String("name", config.Name),
					zap.String("id", existingGUID),
					zap.String("provider", config.Llm.Provider),
					zap.String("model", config.Llm.Model))
			}
		} else {
			// Agent doesn't exist, create new entry with GUID
			agentID := uuid.New().String()
			info := &AgentInstanceInfo{
				ID:        agentID,
				Name:      config.Name,
				Status:    "loaded", // Config loaded but agent not yet instantiated
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			r.agentInfo[agentID] = info
			r.agentsByName[config.Name] = agentID
			r.mu.Unlock()

			if err := r.persistAgentInfo(info, config); err != nil {
				r.logger.Warn("Failed to persist agent info",
					zap.String("name", config.Name),
					zap.String("id", agentID),
					zap.Error(err))
			} else {
				r.logger.Info("Loaded agent config",
					zap.String("name", config.Name),
					zap.String("id", agentID),
					zap.String("provider", config.Llm.Provider),
					zap.String("model", config.Llm.Model))
			}
		}
		// Note: Lock released above in both branches
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

			// Check if agent already loaded from database (via loadAgentsFromDB)
			r.mu.RLock()
			agentID, exists := r.agentsByName[config.Name]
			r.mu.RUnlock()

			var info *AgentInstanceInfo
			if exists {
				// Agent already exists in memory (loaded from DB), use its GUID
				r.mu.RLock()
				info = r.agentInfo[agentID]
				r.mu.RUnlock()
				info.UpdatedAt = time.Now()

				r.mu.Lock()
				r.configs[config.Name] = config
				r.mu.Unlock()
			} else {
				// New workflow agent, generate GUID
				agentID = uuid.New().String()
				info = &AgentInstanceInfo{
					ID:        agentID,
					Name:      config.Name,
					Status:    "initializing",
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}

				r.mu.Lock()
				r.configs[config.Name] = config
				r.agentInfo[agentID] = info
				r.agentsByName[config.Name] = agentID
				r.mu.Unlock()
			}

			// Persist to database (upsert: create new or update existing)
			if err := r.persistAgentInfo(info, config); err != nil {
				r.logger.Warn("Failed to persist workflow agent info",
					zap.String("name", config.Name),
					zap.String("id", agentID),
					zap.Error(err))
			}

			r.logger.Info("Loaded workflow agent",
				zap.String("name", config.Name),
				zap.String("id", agentID),
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

	// Build agent - it gets an ephemeral UUID from NewAgent
	agent, err := r.buildAgent(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to build agent: %w", err)
	}

	// Check if agent exists in DB (has stable GUID)
	r.mu.RLock()
	existingGUID, hasStableGUID := r.agentsByName[name]
	r.mu.RUnlock()

	var agentID string
	if hasStableGUID {
		// Use stable GUID from database - override ephemeral UUID
		agentID = existingGUID
		agent.SetID(agentID)
		r.logger.Info("Using existing stable GUID",
			zap.String("name", name),
			zap.String("id", agentID))
	} else {
		// New agent - use UUID from NewAgent as stable GUID
		agentID = agent.GetID()
		r.logger.Info("Assigned new stable GUID",
			zap.String("name", name),
			zap.String("id", agentID))
	}

	// Create agent info with stable GUID
	info := &AgentInstanceInfo{
		ID:        agentID,
		Name:      name,
		Status:    "stopped",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Persist to database
	if err := r.persistAgentInfo(info, config); err != nil {
		return nil, fmt.Errorf("failed to persist agent info: %w", err)
	}

	// Store agent and info in memory
	r.mu.Lock()
	r.agents[name] = agent
	r.agentInfo[agentID] = info
	r.agentsByName[name] = agentID
	r.mu.Unlock()

	r.logger.Info("Agent created successfully",
		zap.String("name", name),
		zap.String("id", agentID))

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

	// Inject server dependencies if available
	if r.errorStore != nil {
		opts = append(opts, WithErrorStore(r.errorStore))
	}

	if r.permissionChecker != nil {
		opts = append(opts, WithPermissionChecker(r.permissionChecker))
	}

	// Create agent with configuration
	agent := NewAgent(
		nil, // Backend optional with MCP tools
		llmProvider,
		opts...,
	)

	// Register workspace tool if artifactStore is available
	if r.artifactStore != nil {
		// Type assert to artifacts.ArtifactStore
		if artifactStore, ok := r.artifactStore.(artifacts.ArtifactStore); ok {
			workspaceTool := shuttle.Tool(builtin.NewWorkspaceTool(artifactStore))
			// Wrap with PromptAwareTool for externalized descriptions
			if agent.prompts != nil {
				workspaceTool = shuttle.NewPromptAwareTool(workspaceTool, agent.prompts, "tools.workspace")
			}
			agent.RegisterTool(workspaceTool)
			r.logger.Debug("Registered workspace tool",
				zap.String("agent", config.Name))
		} else {
			r.logger.Warn("artifactStore does not implement artifacts.ArtifactStore interface",
				zap.String("agent", config.Name))
		}
	}

	// Always register shell_execute tool (standard toolset)
	// For weaver, use LOOM_DATA_DIR; for others, use current dir
	shellTool := shuttle.Tool(builtin.NewShellExecuteTool(""))
	if config.Name == "weaver" {
		shellTool = builtin.NewShellExecuteTool(r.configDir)
		r.logger.Debug("Registered shell_execute tool with LOOM_DATA_DIR",
			zap.String("agent", config.Name),
			zap.String("baseDir", r.configDir))
	} else {
		r.logger.Debug("Registered shell_execute tool",
			zap.String("agent", config.Name))
	}
	// Wrap with PromptAwareTool for externalized descriptions
	if agent.prompts != nil {
		shellTool = shuttle.NewPromptAwareTool(shellTool, agent.prompts, "tools.shell_execute")
	}
	agent.RegisterTool(shellTool)

	// Register MCP tools if configured
	if config.Tools != nil && len(config.Tools.Mcp) > 0 && r.mcpMgr != nil {
		if err := r.registerMCPTools(ctx, agent, config.Tools.Mcp); err != nil {
			return nil, fmt.Errorf("failed to register MCP tools: %w", err)
		}
	}

	// Register builtin tools based on config (file_write, http_request, grpc_call, etc.)
	// Only register tools explicitly listed in config.Tools.Builtin
	// Pass agent's PromptRegistry for externalized tool descriptions
	if config.Tools != nil && len(config.Tools.Builtin) > 0 {
		// Filter builtin tools based on config
		for _, toolName := range config.Tools.Builtin {
			tool := builtin.ByName(toolName)
			if tool != nil {
				// Wrap with PromptAwareTool if prompts registry available
				if agent.prompts != nil {
					key := fmt.Sprintf("tools.%s", toolName)
					tool = shuttle.NewPromptAwareTool(tool, agent.prompts, key)
				}
				agent.RegisterTool(tool)
			} else {
				// Tool not found in builtin, might be a special tool (get_tool_result, etc.)
				// or tool_search - these are registered separately
				r.logger.Debug("Skipping non-builtin tool (may be registered separately)",
					zap.String("tool", toolName),
					zap.String("agent", config.Name))
			}
		}
	} else {
		// Backward compatibility: If no Tools.Builtin specified, register all builtin tools
		for _, tool := range builtin.All(agent.prompts) {
			agent.RegisterTool(tool)
		}
	}

	// Register tool_search for dynamic tool discovery (if tool registry is available AND requested in config)
	if r.toolRegistry != nil {
		// Check if tool_search is explicitly requested in config
		shouldRegisterToolSearch := false
		if config.Tools != nil && len(config.Tools.Builtin) > 0 {
			for _, toolName := range config.Tools.Builtin {
				if toolName == "tool_search" {
					shouldRegisterToolSearch = true
					break
				}
			}
		} else {
			// Backward compatibility: If no Tools.Builtin specified, auto-register tool_search
			shouldRegisterToolSearch = true
		}

		if shouldRegisterToolSearch {
			searchTool := shuttle.Tool(toolregistry.NewSearchTool(r.toolRegistry))
			// Wrap with PromptAwareTool for externalized descriptions
			if agent.prompts != nil {
				searchTool = shuttle.NewPromptAwareTool(searchTool, agent.prompts, "tools.tool_search")
			}
			agent.RegisterTool(searchTool)
			r.logger.Debug("Registered tool_search for agent",
				zap.String("agent", config.Name))
		}

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

	// Filter registered tools to only those specified in config
	// This handles special tools (get_tool_result, recall_conversation, etc.)
	// that are auto-registered by NewAgent() but should respect the config filter
	if config.Tools != nil && len(config.Tools.Builtin) > 0 {
		allowedTools := make(map[string]bool)
		for _, toolName := range config.Tools.Builtin {
			allowedTools[toolName] = true
			// Handle tool name variations (get_error_detail vs get_error_details)
			if toolName == "get_error_detail" {
				allowedTools["get_error_details"] = true
			}
		}

		// Get list of currently registered tools
		registeredTools := agent.ListTools()
		for _, toolName := range registeredTools {
			// Skip MCP and custom tools (they're filtered by their own registration logic)
			// Only filter builtin/framework tools
			if !allowedTools[toolName] && !r.isMCPTool(toolName) && !r.isCustomTool(toolName, config) {
				agent.UnregisterTool(toolName)
				r.logger.Debug("Unregistered tool not in config.Tools.Builtin",
					zap.String("tool", toolName),
					zap.String("agent", config.Name))
			}
		}
	}

	return agent, nil
}

// isMCPTool checks if a tool name belongs to an MCP tool.
// MCP tools are prefixed with the server name.
func (r *Registry) isMCPTool(toolName string) bool {
	// MCP tools use format: "server_name.tool_name" or just match against known MCP servers
	// For now, just check if it contains a period (simple heuristic)
	// TODO: Improve this by checking against registered MCP tools
	return false // Placeholder - MCP tools filtered via their own logic
}

// isCustomTool checks if a tool name belongs to a custom tool defined in config.
func (r *Registry) isCustomTool(toolName string, config *loomv1.AgentConfig) bool {
	if config.Tools == nil || len(config.Tools.Custom) == 0 {
		return false
	}
	for _, customTool := range config.Tools.Custom {
		if customTool.Name == toolName {
			return true
		}
	}
	return false
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

// StartAgent starts a stopped agent by name or GUID
func (r *Registry) StartAgent(ctx context.Context, nameOrID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Try GUID lookup first
	info, exists := r.agentInfo[nameOrID]
	if !exists {
		// Fall back to name lookup
		agentID, nameExists := r.agentsByName[nameOrID]
		if !nameExists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
		info, exists = r.agentInfo[agentID]
		if !exists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
	}

	if info.Status == "running" {
		return fmt.Errorf("agent %s is already running", info.Name)
	}

	info.Status = "running"
	info.UpdatedAt = time.Now()

	r.logger.Info("Agent started",
		zap.String("name", info.Name),
		zap.String("id", info.ID))

	return nil
}

// StopAgent stops a running agent by name or GUID
func (r *Registry) StopAgent(ctx context.Context, nameOrID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Try GUID lookup first
	info, exists := r.agentInfo[nameOrID]
	if !exists {
		// Fall back to name lookup
		agentID, nameExists := r.agentsByName[nameOrID]
		if !nameExists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
		info, exists = r.agentInfo[agentID]
		if !exists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
	}

	if info.Status != "running" {
		return fmt.Errorf("agent %s is not running", info.Name)
	}

	info.Status = "stopped"
	info.UpdatedAt = time.Now()

	r.logger.Info("Agent stopped",
		zap.String("name", info.Name),
		zap.String("id", info.ID))

	return nil
}

// GetAgent returns a running agent instance by name or GUID
func (r *Registry) GetAgent(ctx context.Context, nameOrID string) (*Agent, error) {
	// First try direct name lookup
	r.mu.RLock()
	agent, exists := r.agents[nameOrID]
	_, hasConfig := r.configs[nameOrID]
	r.mu.RUnlock()

	if exists {
		return agent, nil
	}

	if hasConfig {
		// Lazily create agent from config
		return r.CreateAgent(ctx, nameOrID)
	}

	// Not found by name - try GUID lookup
	r.mu.RLock()
	info, guidExists := r.agentInfo[nameOrID]
	r.mu.RUnlock()

	if !guidExists {
		return nil, fmt.Errorf("agent not found: %s", nameOrID)
	}

	// Found by GUID - try to get by name
	name := info.Name
	r.mu.RLock()
	agent, exists = r.agents[name]
	_, hasConfig = r.configs[name]
	r.mu.RUnlock()

	if exists {
		return agent, nil
	}

	if hasConfig {
		// Lazily create agent from config
		return r.CreateAgent(ctx, name)
	}

	return nil, fmt.Errorf("agent not found or not running: %s", nameOrID)
}

// CreateEphemeralAgent creates a temporary agent based on a role.
// This implements the collaboration.AgentFactory interface.
// The agent is NOT registered and caller must manage its lifecycle.
//
// Ephemeral agents receive stable GUIDs for tracking and observability,
// but are NOT persisted to the database since they're temporary.
func (r *Registry) CreateEphemeralAgent(ctx context.Context, role string) (*Agent, error) {
	agentName := fmt.Sprintf("ephemeral-%s", role)

	// For now, create a basic agent with the default LLM provider
	// TODO: Look up role-specific ephemeral policies from configs
	// TODO: Apply template configuration from policy
	// TODO: Track ephemeral agent spawns and costs

	if r.llmProvider == nil {
		return nil, fmt.Errorf("cannot create ephemeral agent: no LLM provider configured")
	}

	// Create basic agent - it gets an ephemeral UUID from NewAgent
	agent := NewAgent(
		nil, // Backend optional
		r.llmProvider,
		WithName(agentName),
		WithDescription(fmt.Sprintf("Ephemeral agent for role: %s", role)),
	)

	// Ephemeral agents keep their NewAgent UUID (not persisted to DB)
	agentID := agent.GetID()

	r.logger.Info("Creating ephemeral agent",
		zap.String("role", role),
		zap.String("id", agentID),
		zap.String("name", agentName))

	// Track ephemeral agent in memory (not persisted to DB)
	// This allows GetAgentInfo to work with ephemeral agents
	info := &AgentInstanceInfo{
		ID:        agentID,
		Name:      agentName,
		Status:    "running",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	r.mu.Lock()
	r.agentInfo[agentID] = info
	r.agentsByName[agentName] = agentID
	// Note: NOT adding to r.agents map since caller manages lifecycle
	r.mu.Unlock()

	r.logger.Info("Ephemeral agent created",
		zap.String("role", role),
		zap.String("id", agentID),
		zap.String("name", agentName))

	return agent, nil
}

// GetAgentInfo returns information about an agent by name or GUID.
// Supports both stable GUID lookups and legacy name-based lookups.
func (r *Registry) GetAgentInfo(nameOrID string) (*AgentInstanceInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try GUID lookup first
	info, exists := r.agentInfo[nameOrID]
	if exists {
		return info, nil
	}

	// Fall back to name lookup
	agentID, exists := r.agentsByName[nameOrID]
	if exists {
		info, exists = r.agentInfo[agentID]
		if exists {
			return info, nil
		}
	}

	return nil, fmt.Errorf("agent not found: %s", nameOrID)
}

// GetAgentByID returns information about an agent by GUID only.
// Use this when you specifically need GUID-based lookup without name fallback.
func (r *Registry) GetAgentByID(id string) (*AgentInstanceInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.agentInfo[id]
	if !exists {
		return nil, fmt.Errorf("agent not found with ID: %s", id)
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

// DeleteAgent removes an agent by name or GUID
func (r *Registry) DeleteAgent(ctx context.Context, nameOrID string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Try GUID lookup first
	info, exists := r.agentInfo[nameOrID]
	if !exists {
		// Fall back to name lookup
		agentID, nameExists := r.agentsByName[nameOrID]
		if !nameExists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
		info, exists = r.agentInfo[agentID]
		if !exists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
	}

	// Check if agent is running
	if info.Status == "running" && !force {
		return fmt.Errorf("agent %s is running, use force=true to delete", info.Name)
	}

	// Remove from runtime
	delete(r.agents, info.Name)
	delete(r.agentInfo, info.ID)
	delete(r.agentsByName, info.Name)
	delete(r.configs, info.Name)

	// Delete from database
	_, err := r.db.Exec("DELETE FROM agents WHERE id = ?", info.ID)
	if err != nil {
		r.logger.Warn("Failed to delete agent from database",
			zap.String("id", info.ID),
			zap.Error(err))
	}

	// Delete config file
	configPath := filepath.Join(r.configDir, "agents", info.Name+".yaml")
	if err := removeFile(configPath); err != nil {
		r.logger.Warn("Failed to delete config file",
			zap.String("path", configPath),
			zap.Error(err))
	}

	r.logger.Info("Agent deleted",
		zap.String("name", info.Name),
		zap.String("id", info.ID))

	return nil
}

// ReloadAgent hot-reloads an agent's configuration by name or GUID
func (r *Registry) ReloadAgent(ctx context.Context, nameOrID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Try GUID lookup first
	info, exists := r.agentInfo[nameOrID]
	if !exists {
		// Fall back to name lookup
		agentID, nameExists := r.agentsByName[nameOrID]
		if !nameExists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
		info, exists = r.agentInfo[agentID]
		if !exists {
			return fmt.Errorf("agent not found: %s", nameOrID)
		}
	}

	// Reload config from file
	configPath := filepath.Join(r.configDir, "agents", info.Name+".yaml")
	config, err := LoadAgentConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	if err := ValidateAgentConfig(config); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	r.configs[info.Name] = config
	info.UpdatedAt = time.Now()

	// If agent is running, rebuild it
	if info.Status == "running" {
		agent, err := r.buildAgent(ctx, config)
		if err != nil {
			return fmt.Errorf("failed to rebuild agent: %w", err)
		}
		r.agents[info.Name] = agent
	}

	r.logger.Info("Agent config reloaded",
		zap.String("name", info.Name),
		zap.String("id", info.ID))

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
		// Get agent GUID for callback
		agentGUID := ""
		if info, err := r.GetAgentInfo(name); err == nil {
			agentGUID = info.ID
		} else {
			r.logger.Warn("Could not get GUID for force-reload callback",
				zap.String("agent", name),
				zap.Error(err))
		}

		if err := callback(name, agentGUID, config); err != nil {
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
							// Get agent GUID for callback
							agentGUID := ""
							if info, err := r.GetAgentInfo(name); err == nil {
								agentGUID = info.ID
							}

							if err := callback(name, agentGUID, config); err != nil {
								r.logger.Debug("Agent already loaded or callback failed",
									zap.String("agent", name),
									zap.String("guid", agentGUID),
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
							// Get agent GUID for callback
							agentGUID := ""
							if info, err := r.GetAgentInfo(config.Name); err == nil {
								agentGUID = info.ID
							}

							if err := callback(config.Name, agentGUID, config); err != nil {
								r.logger.Error("Workflow agent reload callback failed",
									zap.String("workflow", name),
									zap.String("agent", config.Name),
									zap.String("guid", agentGUID),
									zap.Error(err))
							} else {
								r.logger.Info("Workflow agent reloaded successfully",
									zap.String("workflow", name),
									zap.String("agent", config.Name),
									zap.String("guid", agentGUID))
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
						// Get agent GUID for callback
						agentGUID := ""
						info, err := r.GetAgentInfo(name)
						if err == nil {
							// Agent exists in database, use its stable GUID
							agentGUID = info.ID
						} else {
							// Agent is NEW (just created), generate and persist stable GUID
							r.logger.Info("New agent detected, generating stable GUID",
								zap.String("agent", name))

							// Generate new stable GUID
							agentGUID = uuid.New().String()

							// Create agent info
							now := time.Now()
							newInfo := &AgentInstanceInfo{
								ID:             agentGUID,
								Name:           name,
								Status:         "initializing",
								CreatedAt:      now,
								UpdatedAt:      now,
								ActiveSessions: 0,
								TotalMessages:  0,
							}

							// Persist to database BEFORE callback
							// This ensures GetAgentInfo will work correctly for subsequent operations
							if err := r.persistAgentInfo(newInfo, config); err != nil {
								r.logger.Error("Failed to persist new agent info",
									zap.String("agent", name),
									zap.String("guid", agentGUID),
									zap.Error(err))
								// Continue anyway with the GUID we generated
							} else {
								r.logger.Info("New agent persisted to registry",
									zap.String("agent", name),
									zap.String("guid", agentGUID))

								// Update in-memory maps so GetAgentInfo can find it
								r.mu.Lock()
								r.agentInfo[agentGUID] = newInfo
								r.agentsByName[name] = agentGUID
								r.mu.Unlock()
							}
						}

						if err := callback(name, agentGUID, config); err != nil {
							r.logger.Error("Reload callback failed",
								zap.String("agent", name),
								zap.String("guid", agentGUID),
								zap.Error(err))
						} else {
							r.logger.Info("Agent reloaded successfully",
								zap.String("agent", name),
								zap.String("guid", agentGUID))
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

// persistAgentInfo persists agent info to the database.
// This ensures agents have stable GUIDs that survive restarts.
func (r *Registry) persistAgentInfo(info *AgentInstanceInfo, config *loomv1.AgentConfig) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal agent config: %w", err)
	}

	// Use INSERT ... ON CONFLICT(name) to handle name-based upserts
	// This preserves GUID stability: if agent name exists, keep its GUID
	query := `
		INSERT INTO agents (id, name, config_json, status, created_at, updated_at, active_sessions, total_messages)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			config_json = excluded.config_json,
			status = excluded.status,
			updated_at = excluded.updated_at
	`

	_, err = r.db.Exec(query,
		info.ID,
		info.Name,
		string(configJSON),
		info.Status,
		info.CreatedAt.Unix(),
		info.UpdatedAt.Unix(),
		info.ActiveSessions,
		info.TotalMessages,
	)

	return err
}

// loadAgentsFromDB loads previously created agents from the database.
// This is called during registry initialization to restore agent GUIDs.
func (r *Registry) loadAgentsFromDB() error {
	rows, err := r.db.Query(`
		SELECT id, name, config_json, status, created_at, updated_at, active_sessions, total_messages
		FROM agents
	`)
	if err != nil {
		return fmt.Errorf("failed to query agents: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var info AgentInstanceInfo
		var configJSON string
		var createdAt, updatedAt int64

		err := rows.Scan(
			&info.ID,
			&info.Name,
			&configJSON,
			&info.Status,
			&createdAt,
			&updatedAt,
			&info.ActiveSessions,
			&info.TotalMessages,
		)
		if err != nil {
			r.logger.Warn("Failed to scan agent row", zap.Error(err))
			continue
		}

		info.CreatedAt = time.Unix(createdAt, 0)
		info.UpdatedAt = time.Unix(updatedAt, 0)

		// Load config from JSON
		var config loomv1.AgentConfig
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			r.logger.Warn("Failed to unmarshal agent config",
				zap.String("agent", info.Name),
				zap.Error(err))
			continue
		}

		// Store in registry maps
		r.agentInfo[info.ID] = &info
		r.agentsByName[info.Name] = info.ID
		r.configs[info.Name] = &config

		r.logger.Debug("Loaded agent from database",
			zap.String("name", info.Name),
			zap.String("id", info.ID))
	}

	return rows.Err()
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
		name TEXT NOT NULL UNIQUE,
		config_json TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		active_sessions INTEGER DEFAULT 0,
		total_messages INTEGER DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
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
