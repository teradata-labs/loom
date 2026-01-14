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
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/zalando/go-keyring"
)

const (
	// ServiceName for keyring storage
	ServiceName = "loom"
	// DefaultConfigFileName is the name of the config file
	DefaultConfigFileName = "looms"
)

// Config holds all configuration for the Loom server.
// Priority: CLI flags > config file > env vars > defaults
type Config struct {
	// DataDir is the Loom data directory (computed from LOOM_DATA_DIR env var or ~/.loom)
	// This field is set during config initialization and is read-only.
	// It is not loaded from config file - use LOOM_DATA_DIR environment variable to override.
	DataDir string `mapstructure:"-"`

	// Server configuration
	Server ServerConfig `mapstructure:"server"`

	// LLM provider configuration
	LLM LLMConfig `mapstructure:"llm"`

	// Database configuration
	Database DatabaseConfig `mapstructure:"database"`

	// Shared memory configuration
	SharedMemory SharedMemoryConfig `mapstructure:"shared_memory"`

	// Communication configuration (tiered reference-based messaging)
	Communication CommunicationConfig `mapstructure:"communication"`

	// Observability configuration
	Observability ObservabilityConfig `mapstructure:"observability"`

	// Logging configuration
	Logging LoggingConfig `mapstructure:"logging"`

	// Agent configurations (for multi-agent deployment)
	Agents AgentsConfig `mapstructure:"agents"`

	// MCP configuration (for Python tools and other MCP servers)
	MCP MCPConfig `mapstructure:"mcp"`

	// Prompts configuration (for PromptRegistry integration)
	Prompts PromptsConfig `mapstructure:"prompts"`

	// Tools configuration (for builtin tool API keys)
	Tools ToolsConfig `mapstructure:"tools"`

	// Learning agent configuration (for self-improvement)
	Learning LearningConfig `mapstructure:"learning"`

	// Docker configuration (for container-based tool execution)
	Docker DockerConfig `mapstructure:"docker"`

	// TUI configuration (for terminal UI client)
	TUI TUIConfig `mapstructure:"tui"`

	// Artifacts configuration (for file storage and management)
	Artifacts ArtifactsConfig `mapstructure:"artifacts"`

	// Scheduler configuration (for cron-based workflow execution)
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
}

// ArtifactsConfig holds artifacts storage configuration.
type ArtifactsConfig struct {
	// MaxFileSize is the maximum file size in bytes (default: 100MB)
	MaxFileSize int64 `mapstructure:"max_file_size"`

	// MaxTotalSize is the maximum total storage size in bytes (default: 10GB)
	MaxTotalSize int64 `mapstructure:"max_total_size"`

	// DefaultTTL is the default time-to-live in seconds (0 = no expiration)
	DefaultTTL int64 `mapstructure:"default_ttl"`

	// AutoCleanup enables automatic cleanup of expired artifacts
	AutoCleanup bool `mapstructure:"auto_cleanup"`

	// SoftDeleteDays is the number of days to keep soft-deleted artifacts (default: 30)
	SoftDeleteDays int `mapstructure:"soft_delete_days"`

	// HotReload enables hot-reload for artifact directory (default: true)
	HotReload bool `mapstructure:"hot_reload"`
}

// DockerConfig holds Docker daemon configuration.
type DockerConfig struct {
	// Host is the Docker daemon host (default: auto-detect)
	// Examples: "unix:///var/run/docker.sock", "tcp://localhost:2375"
	Host string `mapstructure:"host"`

	// SocketPaths is a list of Docker socket paths to try in order (default: platform-specific)
	// On macOS: [~/.orbstack/run/docker.sock, ~/.docker/run/docker.sock, /var/run/docker.sock]
	// On Linux: [/var/run/docker.sock]
	SocketPaths []string `mapstructure:"socket_paths"`

	// CleanupIntervalSeconds is the interval for container cleanup (default: 300)
	CleanupIntervalSeconds int `mapstructure:"cleanup_interval_seconds"`

	// DefaultRotationHours is the default container rotation interval (default: 4)
	DefaultRotationHours int `mapstructure:"default_rotation_hours"`

	// DefaultMaxExecutions is the default max executions before container rotation (default: 1000)
	DefaultMaxExecutions int `mapstructure:"default_max_executions"`
}

// TUIConfig holds terminal UI client configuration.
type TUIConfig struct {
	// ServerAddr is the default server address to connect to (default: localhost:60051)
	ServerAddr string `mapstructure:"server_addr"`

	// HTTPAddr is the HTTP server address for SSE streaming (default: localhost:8080)
	HTTPAddr string `mapstructure:"http_addr"`

	// Theme is the TUI color theme (default: auto)
	Theme string `mapstructure:"theme"`
}

// ServerConfig holds server-specific configuration.
type ServerConfig struct {
	Port             int                 `mapstructure:"port"`
	Host             string              `mapstructure:"host"`
	HTTPPort         int                 `mapstructure:"http_port"` // HTTP/REST+SSE port (default: 5006, 0=disabled)
	EnableReflection bool                `mapstructure:"enable_reflection"`
	TLS              TLSConfig           `mapstructure:"tls"`
	Clarification    ClarificationConfig `mapstructure:"clarification"` // Clarification question timeouts
	CORS             CORSServerConfig    `mapstructure:"cors"`          // CORS configuration for HTTP endpoints
}

// CORSServerConfig holds CORS configuration for HTTP endpoints.
//
// SECURITY WARNING: The default configuration uses wildcard origins (["*"]) which is
// ONLY appropriate for development environments or purely public APIs. For production:
//   - Set allowed_origins to specific domains: ["https://yourdomain.com"]
//   - NEVER use ["*"] with allow_credentials: true (browser will reject it)
//   - Consider using environment-based configuration
type CORSServerConfig struct {
	Enabled          bool     `mapstructure:"enabled"`           // Enable CORS (default: true)
	AllowedOrigins   []string `mapstructure:"allowed_origins"`   // Allowed origins (default: ["*"] - INSECURE for production!)
	AllowedMethods   []string `mapstructure:"allowed_methods"`   // Allowed HTTP methods (default: ["GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"])
	AllowedHeaders   []string `mapstructure:"allowed_headers"`   // Allowed headers (default: ["*"])
	ExposedHeaders   []string `mapstructure:"exposed_headers"`   // Exposed headers (default: ["Content-Length", "Content-Type"])
	AllowCredentials bool     `mapstructure:"allow_credentials"` // Allow credentials (default: false - cannot be true with wildcard origins)
	MaxAge           int      `mapstructure:"max_age"`           // Max age in seconds for preflight cache (default: 86400)
}

// ClarificationConfig holds configuration for clarification question timeouts
type ClarificationConfig struct {
	// RPCTimeoutSeconds is the timeout for RPC calls from TUI to server (default: 5)
	RPCTimeoutSeconds int `mapstructure:"rpc_timeout_seconds"`

	// ChannelSendTimeoutMs is the timeout for sending to answer channel (default: 100)
	ChannelSendTimeoutMs int `mapstructure:"channel_send_timeout_ms"`
}

// TLSConfig holds TLS/HTTPS configuration for the server.
type TLSConfig struct {
	Enabled     bool                `mapstructure:"enabled"`     // Enable TLS (default: false)
	Mode        string              `mapstructure:"mode"`        // "manual", "letsencrypt", "self-signed"
	Manual      ManualTLSConfig     `mapstructure:"manual"`      // Manual certificate configuration
	LetsEncrypt LetsEncryptConfig   `mapstructure:"letsencrypt"` // Let's Encrypt configuration
	SelfSigned  SelfSignedTLSConfig `mapstructure:"self_signed"` // Self-signed certificate configuration
}

// ManualTLSConfig holds manual certificate configuration.
type ManualTLSConfig struct {
	CertFile          string `mapstructure:"cert_file"`           // Path to certificate file
	KeyFile           string `mapstructure:"key_file"`            // Path to private key file
	CAFile            string `mapstructure:"ca_file"`             // Path to CA certificate (optional, for mTLS)
	RequireClientCert bool   `mapstructure:"require_client_cert"` // Require client certificates (mTLS)
}

// LetsEncryptConfig holds Let's Encrypt configuration.
type LetsEncryptConfig struct {
	Domains           []string `mapstructure:"domains"`             // Domain names for certificate
	Email             string   `mapstructure:"email"`               // Email for Let's Encrypt notifications
	ACMEDirectoryURL  string   `mapstructure:"acme_directory_url"`  // ACME directory URL (default: production)
	HTTPChallengePort int32    `mapstructure:"http_challenge_port"` // HTTP-01 challenge port (default: 80)
	CacheDir          string   `mapstructure:"cache_dir"`           // Certificate cache directory
	AutoRenew         bool     `mapstructure:"auto_renew"`          // Enable automatic renewal
	RenewBeforeDays   int32    `mapstructure:"renew_before_days"`   // Renew certificates N days before expiry
	AcceptTOS         bool     `mapstructure:"accept_tos"`          // Accept Let's Encrypt Terms of Service
}

// SelfSignedTLSConfig holds self-signed certificate configuration.
type SelfSignedTLSConfig struct {
	Hostnames    []string `mapstructure:"hostnames"`     // Hostnames to include in certificate
	IPAddresses  []string `mapstructure:"ip_addresses"`  // IP addresses to include in certificate
	ValidityDays int32    `mapstructure:"validity_days"` // Certificate validity in days
	Organization string   `mapstructure:"organization"`  // Organization name
}

// LLMConfig holds LLM provider configuration.
type LLMConfig struct {
	Provider string `mapstructure:"provider"` // anthropic, bedrock, ollama
	Model    string `mapstructure:"model"`    // Deprecated: use provider-specific model fields

	// Anthropic-specific
	AnthropicAPIKey string `mapstructure:"anthropic_api_key"` // From CLI/env/keyring only
	AnthropicModel  string `mapstructure:"anthropic_model"`

	// Bedrock-specific
	BedrockRegion          string `mapstructure:"bedrock_region"`
	BedrockAccessKeyID     string `mapstructure:"bedrock_access_key_id"`     // From CLI/env/keyring only
	BedrockSecretAccessKey string `mapstructure:"bedrock_secret_access_key"` // From CLI/env/keyring only
	BedrockSessionToken    string `mapstructure:"bedrock_session_token"`     // From CLI/env/keyring only
	BedrockProfile         string `mapstructure:"bedrock_profile"`
	BedrockModelID         string `mapstructure:"bedrock_model_id"`

	// Ollama-specific
	OllamaEndpoint string `mapstructure:"ollama_endpoint"`
	OllamaModel    string `mapstructure:"ollama_model"`

	// OpenAI-specific
	OpenAIAPIKey string `mapstructure:"openai_api_key"` // From CLI/env/keyring only
	OpenAIModel  string `mapstructure:"openai_model"`

	// Azure OpenAI-specific
	AzureOpenAIEndpoint     string `mapstructure:"azure_openai_endpoint"`
	AzureOpenAIDeploymentID string `mapstructure:"azure_openai_deployment_id"`
	AzureOpenAIAPIKey       string `mapstructure:"azure_openai_api_key"`     // From CLI/env/keyring only
	AzureOpenAIEntraToken   string `mapstructure:"azure_openai_entra_token"` // From CLI/env/keyring only

	// Mistral-specific
	MistralAPIKey string `mapstructure:"mistral_api_key"` // From CLI/env/keyring only
	MistralModel  string `mapstructure:"mistral_model"`

	// Gemini-specific
	GeminiAPIKey string `mapstructure:"gemini_api_key"` // From CLI/env/keyring only
	GeminiModel  string `mapstructure:"gemini_model"`

	// HuggingFace-specific
	HuggingFaceToken string `mapstructure:"huggingface_token"` // From CLI/env/keyring only
	HuggingFaceModel string `mapstructure:"huggingface_model"`

	// Common generation parameters
	Temperature float64 `mapstructure:"temperature"`
	MaxTokens   int     `mapstructure:"max_tokens"`
	Timeout     int     `mapstructure:"timeout_seconds"`
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Path   string `mapstructure:"path"`
	Driver string `mapstructure:"driver"` // sqlite, postgres (future)
}

// CommunicationConfig holds tiered communication configuration for inter-agent messaging.
type CommunicationConfig struct {
	Store       CommunicationStoreConfig       `mapstructure:"store"`
	GC          CommunicationGCConfig          `mapstructure:"gc"`
	AutoPromote CommunicationAutoPromoteConfig `mapstructure:"auto_promote"`
	Policies    CommunicationPoliciesConfig    `mapstructure:"policies"`
}

// CommunicationStoreConfig holds reference store configuration.
type CommunicationStoreConfig struct {
	Backend  string `mapstructure:"backend"`   // memory | sqlite | redis (default: sqlite)
	Path     string `mapstructure:"path"`      // For sqlite (default: same as database.path)
	RedisURL string `mapstructure:"redis_url"` // For redis (e.g., "redis://localhost:6379/0")
}

// CommunicationGCConfig holds garbage collection configuration for references.
type CommunicationGCConfig struct {
	Enabled  bool   `mapstructure:"enabled"`  // Enable GC (default: true)
	Strategy string `mapstructure:"strategy"` // ref_counting | ttl | manual (default: ref_counting)
	Interval int    `mapstructure:"interval"` // GC interval in seconds (default: 300)
}

// CommunicationAutoPromoteConfig holds auto-promotion configuration (Tier 2).
type CommunicationAutoPromoteConfig struct {
	Enabled   bool  `mapstructure:"enabled"`   // Enable auto-promotion (default: true)
	Threshold int64 `mapstructure:"threshold"` // Size threshold in bytes (default: 10240 = 10KB)
}

// CommunicationPoliciesConfig holds policy overrides for specific message types.
type CommunicationPoliciesConfig struct {
	// Force always-reference for these message types (Tier 1)
	AlwaysReference []string `mapstructure:"always_reference"`

	// Force always-value for these message types (Tier 3)
	AlwaysValue []string `mapstructure:"always_value"`
}

// SharedMemoryConfig holds shared memory configuration.
type SharedMemoryConfig struct {
	Enabled              bool   `mapstructure:"enabled"`               // Enable shared memory (default: true)
	MaxMemoryBytes       int64  `mapstructure:"max_memory_bytes"`      // Max memory size (default: 1GB)
	ThresholdBytes       int64  `mapstructure:"threshold_bytes"`       // Threshold for using shared memory (default: 100KB)
	CompressionThreshold int64  `mapstructure:"compression_threshold"` // Compression threshold (default: 1MB)
	TTLSeconds           int64  `mapstructure:"ttl_seconds"`           // Time-to-live in seconds (default: 3600)
	DiskOverflow         bool   `mapstructure:"disk_overflow_enabled"` // Enable disk overflow (default: true, CRITICAL for preventing data loss)
	DiskCachePath        string `mapstructure:"disk_cache_path"`       // Disk cache path (default: /tmp/loom/cache)
	MaxDiskBytes         int64  `mapstructure:"max_disk_bytes"`        // Max disk cache size (default: 10GB)
}

// ObservabilityConfig holds observability configuration.
type ObservabilityConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Provider string `mapstructure:"provider"` // hawk, otlp

	// Hawk-specific
	HawkEndpoint string `mapstructure:"hawk_endpoint"`
	HawkAPIKey   string `mapstructure:"hawk_api_key"` // From CLI/env only
}

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // text, json
	File   string `mapstructure:"file"`   // File path for log output (optional, defaults to stdout/stderr)
}

// AgentsConfig holds configuration for multiple agents (for no-code deployment).
type AgentsConfig struct {
	// Agents is a map of agent ID to agent configuration
	Agents map[string]AgentConfig `mapstructure:"agents"`
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	// Name is the agent name
	Name string `mapstructure:"name"`

	// Description is the agent description
	Description string `mapstructure:"description"`

	// BackendPath is the path to the backend YAML configuration file
	BackendPath string `mapstructure:"backend_path"`

	// SystemPrompt is the direct system prompt text (takes precedence over SystemPromptKey)
	SystemPrompt string `mapstructure:"system_prompt"`

	// SystemPromptKey is the key for loading the system prompt from promptio
	SystemPromptKey string `mapstructure:"system_prompt_key"`

	// MaxTurns is the maximum number of conversation turns
	MaxTurns int `mapstructure:"max_turns"`

	// MaxToolExecutions is the maximum number of tool executions per conversation
	MaxToolExecutions int `mapstructure:"max_tool_executions"`

	// EnableTracing enables observability tracing for this agent
	EnableTracing bool `mapstructure:"enable_tracing"`

	// PatternsDir is the directory containing pattern YAML files (optional)
	PatternsDir string `mapstructure:"patterns_dir"`

	// LLM is the LLM configuration specific to this agent (optional, defaults to server LLM)
	LLM *LLMConfig `mapstructure:"llm"`
}

// MCPConfig holds configuration for MCP servers (for Python tools and other MCP servers).
type MCPConfig struct {
	// Servers is a map of server name to MCP server configuration
	Servers map[string]MCPServerConfig `mapstructure:"servers"`
}

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	// Command is the executable to run (e.g., "python3", "node")
	Command string `mapstructure:"command"`

	// Args are the command-line arguments (e.g., ["-m", "mcp_server.main"])
	Args []string `mapstructure:"args"`

	// Env are environment variables to set for the MCP server
	Env map[string]string `mapstructure:"env"`

	// Transport is the communication transport (default: "stdio")
	Transport string `mapstructure:"transport"`

	// WorkingDir is the working directory for the MCP server process (optional)
	WorkingDir string `mapstructure:"working_dir"`
}

// PromptsConfig holds configuration for PromptRegistry integration.
type PromptsConfig struct {
	// Source is the prompt source type: "file" (default)
	Source string `mapstructure:"source"`

	// FileDir is the directory for file-based prompts (FileRegistry)
	FileDir string `mapstructure:"file_dir"`

	// CacheSize is the maximum number of prompts to cache
	CacheSize int `mapstructure:"cache_size"`

	// EnableReload enables hot-reload for prompts
	EnableReload bool `mapstructure:"enable_reload"`
}

// ToolsConfig holds configuration for builtin tools.
type ToolsConfig struct {
	// WebSearch holds web search tool configuration
	WebSearch WebSearchConfig `mapstructure:"web_search"`

	// Permissions holds tool permission configuration
	Permissions ToolPermissionsConfig `mapstructure:"permissions"`

	// Executor holds tool executor configuration
	Executor ToolExecutorConfig `mapstructure:"executor"`
}

// ToolExecutorConfig holds tool executor settings.
type ToolExecutorConfig struct {
	// Timeout is the default tool execution timeout (default: 30s)
	TimeoutSeconds int `mapstructure:"timeout_seconds"`

	// MaxRetries is the maximum number of retry attempts (default: 3)
	MaxRetries int `mapstructure:"max_retries"`

	// RetryDelayMs is the delay between retries in milliseconds (default: 100)
	RetryDelayMs int `mapstructure:"retry_delay_ms"`

	// ConcurrentLimit is the maximum concurrent tool executions (default: 10)
	ConcurrentLimit int `mapstructure:"concurrent_limit"`
}

// ToolPermissionsConfig holds tool permission settings.
type ToolPermissionsConfig struct {
	// RequireApproval requires user approval before executing tools (default: false)
	RequireApproval bool `mapstructure:"require_approval"`

	// YOLO mode bypasses all permission prompts (default: false)
	// Can be enabled via --yolo flag or LOOM_YOLO=true env var
	YOLO bool `mapstructure:"yolo"`

	// AllowedTools is a list of tool names that are always allowed without prompts
	// Empty list means all tools require approval (if RequireApproval is true)
	AllowedTools []string `mapstructure:"allowed_tools"`

	// DisabledTools is a list of tool names that are never allowed
	DisabledTools []string `mapstructure:"disabled_tools"`

	// DefaultAction is the default action when user doesn't respond (allow, deny)
	// Default: "deny"
	DefaultAction string `mapstructure:"default_action"`

	// TimeoutSeconds is how long to wait for user response (default: 300)
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
}

// WebSearchConfig holds web search API keys.
type WebSearchConfig struct {
	// BraveAPIKey for Brave Search API (from CLI/env/keyring only)
	BraveAPIKey string `mapstructure:"brave_api_key"`

	// TavilyAPIKey for Tavily AI Search API (from CLI/env/keyring only)
	TavilyAPIKey string `mapstructure:"tavily_api_key"`

	// SerpAPIKey for SerpAPI (from CLI/env/keyring only)
	SerpAPIKey string `mapstructure:"serpapi_key"`

	// DefaultProvider is the default web search provider to use
	DefaultProvider string `mapstructure:"default_provider"`

	// Endpoints holds API endpoint URLs (configurable for proxies or self-hosted)
	Endpoints WebSearchEndpointsConfig `mapstructure:"endpoints"`

	// TimeoutSeconds is the HTTP client timeout for search requests (default: 30)
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
}

// WebSearchEndpointsConfig holds configurable API endpoints for web search.
type WebSearchEndpointsConfig struct {
	// Brave is the Brave Search API endpoint (default: https://api.search.brave.com/res/v1/web/search)
	Brave string `mapstructure:"brave"`

	// Tavily is the Tavily API endpoint (default: https://api.tavily.com/search)
	Tavily string `mapstructure:"tavily"`

	// SerpAPI is the SerpAPI endpoint (default: https://serpapi.com/search)
	SerpAPI string `mapstructure:"serpapi"`

	// DuckDuckGo is the DuckDuckGo API endpoint (default: https://api.duckduckgo.com/)
	DuckDuckGo string `mapstructure:"duckduckgo"`
}

// LearningConfig holds configuration for the learning agent (self-improvement system).
// This can be configured via:
// 1. Config file (looms.yaml) - basic settings
// 2. YAML files in ~/.loom/learning/ - full declarative config (preferred)
// 3. CLI flags - for quick overrides
type LearningConfig struct {
	// Enabled enables the learning agent (default: true)
	Enabled bool `mapstructure:"enabled"`

	// ConfigPath is the path to a LearningAgentConfig YAML file
	// If set, this takes precedence over inline config
	ConfigPath string `mapstructure:"config_path"`

	// ConfigDir is a directory containing LearningAgentConfig YAML files
	// All *.yaml files in this directory will be loaded
	// Default: ~/.loom/learning/
	ConfigDir string `mapstructure:"config_dir"`

	// AutonomyLevel controls how improvements are applied
	// Values: "manual" (default), "human_approval", "full"
	AutonomyLevel string `mapstructure:"autonomy_level"`

	// AnalysisInterval is how often to analyze judge results
	// Format: Go duration string (e.g., "1h", "30m")
	// Default: "1h"
	AnalysisInterval string `mapstructure:"analysis_interval"`

	// Domains to analyze (empty = all domains)
	Domains []string `mapstructure:"domains"`
}

// SchedulerConfig holds workflow scheduler configuration.
type SchedulerConfig struct {
	// Enabled enables the workflow scheduler (default: false)
	Enabled bool `mapstructure:"enabled"`

	// WorkflowDir is the directory containing workflow YAML files with schedule sections
	// Default: ~/.loom/workflows/
	WorkflowDir string `mapstructure:"workflow_dir"`

	// DBPath is the path to the scheduler SQLite database
	// Default: ~/.loom/scheduler.db
	DBPath string `mapstructure:"db_path"`

	// HotReload enables automatic reloading when workflow files change (default: true)
	HotReload bool `mapstructure:"hot_reload"`
}

// LoadConfig loads configuration from multiple sources with proper priority:
// 1. Command line flags (highest priority)
// 2. Config file
// 3. Environment variables
// 4. Defaults (lowest priority)
func LoadConfig(cfgFile string) (*Config, error) {
	// Set defaults
	setDefaults()

	// Setup config file
	if cfgFile != "" {
		// Use config file from flag
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config in standard locations
		// Config search paths (in order of priority)
		viper.AddConfigPath(loomconfig.GetLoomDataDir()) // Loom data directory (respects LOOM_DATA_DIR)
		viper.AddConfigPath(".")                         // Current directory
		viper.AddConfigPath("/etc/loom/")                // System-wide
		viper.SetConfigName(DefaultConfigFileName)       // looms.yaml
		viper.SetConfigType("yaml")
	}

	// Read config file (if it exists)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Config file was found but another error occurred
			return nil, fmt.Errorf("error reading config file %s: %w", viper.ConfigFileUsed(), err)
		}
		// Config file not found; using defaults + env vars + flags
	}

	// Bind environment variables
	viper.SetEnvPrefix("LOOM")
	viper.AutomaticEnv()

	// Unmarshal config
	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set DataDir from environment or default
	// This must be done after unmarshal since it's not loaded from config file
	config.DataDir = loomconfig.GetLoomDataDir()

	// Load secrets from keyring if not provided via CLI/env
	// Non-fatal: keyring might not be available - user can provide secrets via CLI/env
	_ = loadSecretsFromKeyring(&config)

	return &config, nil
}

// setDefaults sets default configuration values.
func setDefaults() {
	// Server defaults
	viper.SetDefault("server.port", 60051)
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.enable_reflection", true)

	// Clarification defaults
	viper.SetDefault("server.clarification.rpc_timeout_seconds", 5)
	viper.SetDefault("server.clarification.channel_send_timeout_ms", 100)

	// CORS defaults (permissive for development, MUST be configured for production)
	// SECURITY WARNING: Defaults to wildcard origins for best DX - change in production!
	// Set LOOM_CORS_ORIGINS env var or server.cors.allowed_origins in config for production.
	viper.SetDefault("server.cors.enabled", true)
	viper.SetDefault("server.cors.allowed_origins", []string{"*"}) // INSECURE for production!
	viper.SetDefault("server.cors.allowed_methods", []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"})
	viper.SetDefault("server.cors.allowed_headers", []string{"*"})
	viper.SetDefault("server.cors.exposed_headers", []string{"Content-Length", "Content-Type"})
	viper.SetDefault("server.cors.allow_credentials", false) // MUST be false with wildcard origins
	viper.SetDefault("server.cors.max_age", 86400)           // 24 hours

	// LLM defaults
	viper.SetDefault("llm.provider", "anthropic")
	viper.SetDefault("llm.anthropic_model", "claude-sonnet-4-5-20250514")
	viper.SetDefault("llm.bedrock_region", "us-west-2")
	viper.SetDefault("llm.bedrock_model_id", "us.anthropic.claude-sonnet-4-5-20250929-v1:0") // Cross-region inference profile
	viper.SetDefault("llm.ollama_endpoint", "http://localhost:11434")
	viper.SetDefault("llm.ollama_model", "llama3.1:8b")
	viper.SetDefault("llm.openai_model", "gpt-4.1")
	viper.SetDefault("llm.azure_openai_endpoint", "")
	viper.SetDefault("llm.azure_openai_deployment_id", "")
	viper.SetDefault("llm.mistral_model", "mistral-large-latest")
	viper.SetDefault("llm.gemini_model", "gemini-2.5-flash")
	viper.SetDefault("llm.huggingface_model", "meta-llama/Meta-Llama-3.1-70B-Instruct")
	viper.SetDefault("llm.temperature", 1.0)
	viper.SetDefault("llm.max_tokens", 4096)
	viper.SetDefault("llm.timeout_seconds", 60)

	// Database defaults (use loom data directory)
	defaultDBPath := filepath.Join(loomconfig.GetLoomDataDir(), "loom.db")
	viper.SetDefault("database.path", defaultDBPath)
	viper.SetDefault("database.driver", "sqlite")

	// Communication defaults (SQLite-backed, auto-promote enabled)
	viper.SetDefault("communication.store.backend", "sqlite")
	viper.SetDefault("communication.store.path", defaultDBPath) // Reuse same database as sessions
	viper.SetDefault("communication.gc.enabled", true)
	viper.SetDefault("communication.gc.strategy", "ref_counting")
	viper.SetDefault("communication.gc.interval", 300) // 5 minutes
	viper.SetDefault("communication.auto_promote.enabled", true)
	viper.SetDefault("communication.auto_promote.threshold", 10240) // 10KB
	viper.SetDefault("communication.policies.always_reference", []string{"session_state", "workflow_context", "collaboration_state"})
	viper.SetDefault("communication.policies.always_value", []string{"control", "pattern_ref"})

	// Observability defaults (enabled by default)
	viper.SetDefault("observability.enabled", true)
	viper.SetDefault("observability.provider", "hawk")

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "text")

	// Tool permissions defaults
	viper.SetDefault("tools.permissions.require_approval", false)
	viper.SetDefault("tools.permissions.yolo", true) // Default to YOLO mode for autonomous operation
	viper.SetDefault("tools.permissions.default_action", "deny")
	viper.SetDefault("tools.permissions.timeout_seconds", 300)
	// Check LOOM_YOLO environment variable
	if os.Getenv("LOOM_YOLO") == "true" || os.Getenv("LOOM_YOLO") == "1" {
		viper.Set("tools.permissions.yolo", true)
	}

	// Prompts defaults
	// Use file-based registry as default
	viper.SetDefault("prompts.source", "file")
	viper.SetDefault("prompts.file_dir", "./prompts")
	viper.SetDefault("prompts.cache_size", 1000)
	viper.SetDefault("prompts.enable_reload", true)

	// Tools defaults
	viper.SetDefault("tools.web_search.default_provider", "tavily")
	viper.SetDefault("tools.web_search.timeout_seconds", 30)
	viper.SetDefault("tools.web_search.endpoints.brave", "https://api.search.brave.com/res/v1/web/search")
	viper.SetDefault("tools.web_search.endpoints.tavily", "https://api.tavily.com/search")
	viper.SetDefault("tools.web_search.endpoints.serpapi", "https://serpapi.com/search")
	viper.SetDefault("tools.web_search.endpoints.duckduckgo", "https://api.duckduckgo.com/")

	// Tool executor defaults
	viper.SetDefault("tools.executor.timeout_seconds", 30)
	viper.SetDefault("tools.executor.max_retries", 3)
	viper.SetDefault("tools.executor.retry_delay_ms", 100)
	viper.SetDefault("tools.executor.concurrent_limit", 10)

	// Learning agent defaults
	viper.SetDefault("learning.enabled", true)
	viper.SetDefault("learning.autonomy_level", "manual")
	viper.SetDefault("learning.analysis_interval", "1h")
	defaultLearningDir := filepath.Join(loomconfig.GetLoomDataDir(), "learning")
	viper.SetDefault("learning.config_dir", defaultLearningDir)

	// Docker defaults (platform-aware socket paths)
	viper.SetDefault("docker.cleanup_interval_seconds", 300)
	viper.SetDefault("docker.default_rotation_hours", 4)
	viper.SetDefault("docker.default_max_executions", 1000)
	// Socket paths are detected at runtime if not configured

	// TUI defaults
	viper.SetDefault("tui.server_addr", "localhost:60051")
	viper.SetDefault("tui.http_addr", "localhost:5006")
	viper.SetDefault("tui.theme", "auto")

	// Shared memory defaults (use temp dir, not hardcoded /tmp)
	defaultCachePath := filepath.Join(os.TempDir(), "loom", "cache")
	viper.SetDefault("shared_memory.enabled", true)
	viper.SetDefault("shared_memory.max_memory_bytes", 1024*1024*1024) // 1GB
	viper.SetDefault("shared_memory.threshold_bytes", 2560)            // 2.5KB
	viper.SetDefault("shared_memory.compression_threshold", 1024*1024) // 1MB
	viper.SetDefault("shared_memory.ttl_seconds", 3600)                // 1 hour
	viper.SetDefault("shared_memory.disk_overflow_enabled", true)      // CRITICAL: Prevent data loss on memory pressure
	viper.SetDefault("shared_memory.disk_cache_path", defaultCachePath)
	viper.SetDefault("shared_memory.max_disk_bytes", 10*1024*1024*1024) // 10GB

	// Artifacts defaults
	viper.SetDefault("artifacts.max_file_size", 100*1024*1024)      // 100MB
	viper.SetDefault("artifacts.max_total_size", 10*1024*1024*1024) // 10GB
	viper.SetDefault("artifacts.default_ttl", 0)                    // No expiration
	viper.SetDefault("artifacts.auto_cleanup", false)               // Manual cleanup
	viper.SetDefault("artifacts.soft_delete_days", 30)              // 30-day recovery window
	viper.SetDefault("artifacts.hot_reload", true)                  // Enable hot-reload
}

// SecretMapping defines how to load a secret from keyring into the config.
// The key is the keyring key name, and the setter is a function that applies the value to the config.
type SecretMapping struct {
	KeyringKey string
	Setter     func(*Config, string)
	IsSet      func(*Config) bool // Returns true if the value is already set (skip keyring lookup)
}

// GetSecretMappings returns all secret mappings for the application.
// Developers can extend this by adding new SecretMapping entries.
func GetSecretMappings() []SecretMapping {
	return []SecretMapping{
		{
			KeyringKey: "anthropic_api_key",
			Setter:     func(c *Config, val string) { c.LLM.AnthropicAPIKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.AnthropicAPIKey != "" },
		},
		{
			KeyringKey: "bedrock_access_key_id",
			Setter:     func(c *Config, val string) { c.LLM.BedrockAccessKeyID = val },
			IsSet:      func(c *Config) bool { return c.LLM.BedrockAccessKeyID != "" },
		},
		{
			KeyringKey: "bedrock_secret_access_key",
			Setter:     func(c *Config, val string) { c.LLM.BedrockSecretAccessKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.BedrockSecretAccessKey != "" },
		},
		{
			KeyringKey: "bedrock_session_token",
			Setter:     func(c *Config, val string) { c.LLM.BedrockSessionToken = val },
			IsSet:      func(c *Config) bool { return c.LLM.BedrockSessionToken != "" },
		},
		{
			KeyringKey: "hawk_api_key",
			Setter:     func(c *Config, val string) { c.Observability.HawkAPIKey = val },
			IsSet:      func(c *Config) bool { return c.Observability.HawkAPIKey != "" },
		},
		{
			KeyringKey: "openai_api_key",
			Setter:     func(c *Config, val string) { c.LLM.OpenAIAPIKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.OpenAIAPIKey != "" },
		},
		{
			KeyringKey: "azure_openai_api_key",
			Setter:     func(c *Config, val string) { c.LLM.AzureOpenAIAPIKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.AzureOpenAIAPIKey != "" },
		},
		{
			KeyringKey: "azure_openai_entra_token",
			Setter:     func(c *Config, val string) { c.LLM.AzureOpenAIEntraToken = val },
			IsSet:      func(c *Config) bool { return c.LLM.AzureOpenAIEntraToken != "" },
		},
		{
			KeyringKey: "mistral_api_key",
			Setter:     func(c *Config, val string) { c.LLM.MistralAPIKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.MistralAPIKey != "" },
		},
		{
			KeyringKey: "gemini_api_key",
			Setter:     func(c *Config, val string) { c.LLM.GeminiAPIKey = val },
			IsSet:      func(c *Config) bool { return c.LLM.GeminiAPIKey != "" },
		},
		{
			KeyringKey: "huggingface_token",
			Setter:     func(c *Config, val string) { c.LLM.HuggingFaceToken = val },
			IsSet:      func(c *Config) bool { return c.LLM.HuggingFaceToken != "" },
		},
		// Web search tool API keys
		{
			KeyringKey: "brave_search_api_key",
			Setter:     func(c *Config, val string) { c.Tools.WebSearch.BraveAPIKey = val },
			IsSet:      func(c *Config) bool { return c.Tools.WebSearch.BraveAPIKey != "" },
		},
		{
			KeyringKey: "tavily_api_key",
			Setter:     func(c *Config, val string) { c.Tools.WebSearch.TavilyAPIKey = val },
			IsSet:      func(c *Config) bool { return c.Tools.WebSearch.TavilyAPIKey != "" },
		},
		{
			KeyringKey: "serpapi_key",
			Setter:     func(c *Config, val string) { c.Tools.WebSearch.SerpAPIKey = val },
			IsSet:      func(c *Config) bool { return c.Tools.WebSearch.SerpAPIKey != "" },
		},
		// MCP-specific secrets (Teradata)
		{
			KeyringKey: "td_password",
			Setter: func(c *Config, val string) {
				injectMCPEnvSecret(c, "TD_PASSWORD", val)
			},
			IsSet: func(c *Config) bool {
				return checkMCPEnvSecret(c, "TD_PASSWORD")
			},
		},
		// MCP-specific secrets (GitHub)
		{
			KeyringKey: "github_token",
			Setter: func(c *Config, val string) {
				injectMCPEnvSecret(c, "GITHUB_TOKEN", val)
			},
			IsSet: func(c *Config) bool {
				return checkMCPEnvSecret(c, "GITHUB_TOKEN")
			},
		},
		// MCP-specific secrets (PostgreSQL)
		{
			KeyringKey: "postgres_password",
			Setter: func(c *Config, val string) {
				injectMCPEnvSecret(c, "POSTGRES_PASSWORD", val)
			},
			IsSet: func(c *Config) bool {
				return checkMCPEnvSecret(c, "POSTGRES_PASSWORD")
			},
		},
		// MCP-specific secrets (DATABASE_URL for full connection strings)
		{
			KeyringKey: "database_url",
			Setter: func(c *Config, val string) {
				injectMCPEnvSecret(c, "DATABASE_URL", val)
			},
			IsSet: func(c *Config) bool {
				return checkMCPEnvSecret(c, "DATABASE_URL")
			},
		},
		// Add more MCP secrets as needed
	}
}

// loadSecretsFromKeyring loads API keys from system keyring using the secret mappings.
// This is extensible - just add more entries to GetSecretMappings().
func loadSecretsFromKeyring(config *Config) error {
	for _, mapping := range GetSecretMappings() {
		// Skip if value is already set (from CLI/env/config file)
		if mapping.IsSet(config) {
			continue
		}

		// Try to load from keyring
		value, err := GetSecretFromKeyring(mapping.KeyringKey)
		if err == nil && value != "" {
			mapping.Setter(config, value)
		}
		// Non-fatal: if keyring doesn't have the key, just continue
	}

	return nil
}

// GetSecretFromKeyring retrieves a secret from the system keyring.
func GetSecretFromKeyring(key string) (string, error) {
	return keyring.Get(ServiceName, key)
}

// SaveSecretToKeyring saves a secret to the system keyring.
func SaveSecretToKeyring(key, value string) error {
	return keyring.Set(ServiceName, key, value)
}

// DeleteSecretFromKeyring removes a secret from the system keyring.
func DeleteSecretFromKeyring(key string) error {
	return keyring.Delete(ServiceName, key)
}

// ListAvailableSecretKeys returns all known secret keys that can be stored in the keyring.
// Useful for CLI commands that need to show available options.
func ListAvailableSecretKeys() []string {
	mappings := GetSecretMappings()
	keys := make([]string, len(mappings))
	for i, mapping := range mappings {
		keys[i] = mapping.KeyringKey
	}
	return keys
}

// injectMCPEnvSecret injects a secret into all MCP servers' environment variables.
// This allows secrets from the keyring to be passed to MCP servers automatically.
func injectMCPEnvSecret(config *Config, envKey, value string) {
	if config.MCP.Servers == nil {
		return
	}

	for name, serverConfig := range config.MCP.Servers {
		if serverConfig.Env == nil {
			serverConfig.Env = make(map[string]string)
		}
		// Only inject if not already set in config
		if _, exists := serverConfig.Env[envKey]; !exists {
			serverConfig.Env[envKey] = value
		}
		// Update the map (necessary because Go maps with struct values require this)
		config.MCP.Servers[name] = serverConfig
	}
}

// checkMCPEnvSecret checks if an environment variable is set in any MCP server.
// Returns true if at least one MCP server has this env var set.
func checkMCPEnvSecret(config *Config, envKey string) bool {
	if config.MCP.Servers == nil {
		return false
	}

	for _, serverConfig := range config.MCP.Servers {
		if serverConfig.Env != nil {
			if _, exists := serverConfig.Env[envKey]; exists {
				return true
			}
		}
	}
	return false
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	// Validate server config
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", c.Server.Port)
	}

	// Validate LLM config
	if c.LLM.Provider == "" {
		return fmt.Errorf("llm.provider is required")
	}

	switch c.LLM.Provider {
	case "anthropic":
		if c.LLM.AnthropicAPIKey == "" {
			return fmt.Errorf("anthropic API key is required (set via --anthropic-key, LOOM_LLM_ANTHROPIC_API_KEY, or save to keyring with 'looms config set-key anthropic_api_key')")
		}

	case "bedrock":
		if c.LLM.BedrockRegion == "" {
			return fmt.Errorf("bedrock region is required (set llm.bedrock_region in config or LOOM_LLM_BEDROCK_REGION env var)")
		}
		// Note: We don't require explicit credentials here because:
		// - User might be using AWS profile (BedrockProfile)
		// - User might be using IAM role (default credentials chain)
		// - User might be using environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
		// The Bedrock client will handle auth validation at runtime

	case "ollama":
		if c.LLM.OllamaEndpoint == "" {
			return fmt.Errorf("ollama endpoint is required (set llm.ollama_endpoint in config)")
		}
		if c.LLM.OllamaModel == "" {
			return fmt.Errorf("ollama model is required (set llm.ollama_model in config)")
		}

	case "openai":
		if c.LLM.OpenAIAPIKey == "" {
			return fmt.Errorf("openai API key is required (set via --openai-key, LOOM_LLM_OPENAI_API_KEY, or save to keyring with 'looms config set-key openai_api_key')")
		}

	case "azure-openai", "azureopenai":
		if c.LLM.AzureOpenAIEndpoint == "" {
			return fmt.Errorf("azure openai endpoint is required (set llm.azure_openai_endpoint in config)")
		}
		if c.LLM.AzureOpenAIDeploymentID == "" {
			return fmt.Errorf("azure openai deployment ID is required (set llm.azure_openai_deployment_id in config)")
		}
		if c.LLM.AzureOpenAIAPIKey == "" && c.LLM.AzureOpenAIEntraToken == "" {
			return fmt.Errorf("azure openai API key or Entra token is required (set via keyring with 'looms config set-key azure_openai_api_key' or 'looms config set-key azure_openai_entra_token')")
		}

	case "mistral":
		if c.LLM.MistralAPIKey == "" {
			return fmt.Errorf("mistral API key is required (set via --mistral-key, LOOM_LLM_MISTRAL_API_KEY, or save to keyring with 'looms config set-key mistral_api_key')")
		}

	case "gemini":
		if c.LLM.GeminiAPIKey == "" {
			return fmt.Errorf("gemini API key is required (set via --gemini-key, LOOM_LLM_GEMINI_API_KEY, or save to keyring with 'looms config set-key gemini_api_key')")
		}

	case "huggingface":
		if c.LLM.HuggingFaceToken == "" {
			return fmt.Errorf("huggingface token is required (set via --huggingface-token, LOOM_LLM_HUGGINGFACE_TOKEN, or save to keyring with 'looms config set-key huggingface_token')")
		}

	default:
		return fmt.Errorf("unsupported LLM provider: %s (must be anthropic, bedrock, ollama, openai, azure-openai, mistral, gemini, or huggingface)", c.LLM.Provider)
	}

	// Validate database config
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}

	// Validate observability config
	if c.Observability.Enabled {
		if c.Observability.HawkEndpoint == "" {
			return fmt.Errorf("observability.hawk_endpoint is required when observability is enabled")
		}
		// Note: HawkAPIKey is optional - not required for local Hawk installations
	}

	return nil
}

// GenerateExampleConfig generates an example configuration file.
func GenerateExampleConfig() string {
	return `# Loom Server Configuration
# Priority: CLI flags > config file > environment variables > defaults

server:
  port: 60051
  host: 0.0.0.0
  enable_reflection: true

llm:
  # Provider options: anthropic, bedrock, ollama, openai, azure-openai, mistral
  provider: anthropic

  # Anthropic configuration
  anthropic_model: claude-sonnet-4-5-20250929
  # anthropic_api_key: set via keyring (looms config set-key anthropic_api_key)

  # AWS Bedrock configuration
  bedrock_region: us-west-2
  bedrock_model_id: anthropic.claude-sonnet-4-5-20250929-v1:0
  # bedrock_profile: default  # Use AWS profile instead of explicit credentials
  # bedrock_access_key_id: set via keyring or env (LOOM_LLM_BEDROCK_ACCESS_KEY_ID)
  # bedrock_secret_access_key: set via keyring or env (LOOM_LLM_BEDROCK_SECRET_ACCESS_KEY)
  # bedrock_session_token: set via keyring or env (LOOM_LLM_BEDROCK_SESSION_TOKEN)

  # Ollama configuration (local inference)
  ollama_endpoint: http://localhost:11434
  ollama_model: llama3.1

  # OpenAI configuration
  openai_model: gpt-4o
  # openai_api_key: set via keyring (looms config set-key openai_api_key)

  # Azure OpenAI configuration
  # azure_openai_endpoint: https://your-resource.openai.azure.com
  # azure_openai_deployment_id: gpt-4o-deployment
  # azure_openai_api_key: set via keyring (looms config set-key azure_openai_api_key)
  # azure_openai_entra_token: set via keyring (looms config set-key azure_openai_entra_token)

  # Mistral AI configuration
  mistral_model: mistral-large-latest
  # mistral_api_key: set via keyring (looms config set-key mistral_api_key)

  # Google Gemini configuration
  gemini_model: gemini-2.5-flash
  # gemini_api_key: set via keyring (looms config set-key gemini_api_key)

  # HuggingFace configuration
  huggingface_model: meta-llama/Meta-Llama-3.1-70B-Instruct
  # huggingface_token: set via keyring (looms config set-key huggingface_token)

  # Common generation parameters (apply to all providers)
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60

database:
  path: ./loom.db
  driver: sqlite

observability:
  enabled: false
  provider: hawk
  hawk_endpoint: ""
  # hawk_api_key should be set via keyring - NOT in config file

tools:
  # Web search tool configuration
  web_search:
    default_provider: tavily  # tavily, brave, serpapi, duckduckgo
    # tavily_api_key: set via keyring (looms config set-key tavily_api_key)
    # brave_api_key: set via keyring (looms config set-key brave_search_api_key)
    # serpapi_key: set via keyring (looms config set-key serpapi_key)
    # Note: Tavily provides AI-optimized results (1000 searches/month FREE)
    # Brave Search also excellent (2000 searches/month FREE)
    # DuckDuckGo works without API key (factual queries only, limited results)

logging:
  level: info  # debug, info, warn, error
  format: text # text, json

# Multi-agent configuration (optional - for no-code agent deployment)
agents:
  agents:
    # Example: SQL agent for database queries
    sql-agent:
      name: SQL Query Agent
      description: Executes SQL queries against configured databases
      backend_path: ./backends/postgres.yaml
      system_prompt_key: agent.system.sql
      max_turns: 25
      max_tool_executions: 50
      enable_tracing: true
      patterns_dir: ./patterns/sql

    # Example: File system agent
    file-agent:
      name: File System Agent
      description: Reads and analyzes files
      backend_path: ./backends/file.yaml
      system_prompt: "You are a helpful assistant that can read and analyze files."
      max_turns: 15
      max_tool_executions: 30
      enable_tracing: true

# MCP server configuration (optional - for Python tools and other MCP servers)
mcp:
  servers:
    # Example: Python-based data analysis tools
    python-tools:
      command: python3
      args:
        - -m
        - mcp_server.main
      env:
        PYTHONPATH: ./mcp_servers/python
      transport: stdio
      working_dir: ./mcp_servers/python

    # Example: Node.js-based web scraping tools
    node-tools:
      command: node
      args:
        - ./mcp_servers/node/index.js
      env:
        NODE_ENV: production
      transport: stdio
      working_dir: ./mcp_servers/node

# Note: Secrets should NEVER be committed to config files.
# Use the keyring for secure storage:
#   looms config set-key anthropic_api_key
#   looms config set-key bedrock_access_key_id
#   looms config set-key bedrock_secret_access_key
#   looms config set-key hawk_api_key
#   looms config set-key brave_search_api_key
#   looms config set-key tavily_api_key
#   looms config set-key serpapi_key
`
}
