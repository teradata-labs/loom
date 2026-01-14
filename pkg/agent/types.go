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
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
)

// MCPClientRef holds a reference to an MCP client for cleanup
type MCPClientRef struct {
	Client     interface{ Close() error } // MCP client with Close method
	ServerName string
}

// Agent is the core conversation agent that orchestrates LLM calls, tool execution,
// and backend interactions. It's designed to be backend-agnostic and work with
// any ExecutionBackend implementation (SQL databases, REST APIs, documents, etc.).
type Agent struct {
	// Backend for executing domain-specific operations
	backend fabric.ExecutionBackend

	// Tool registry for available tools
	tools *shuttle.Registry

	// Tool executor
	executor *shuttle.Executor

	// Permission checker for tool execution
	permissionChecker *shuttle.PermissionChecker

	// Memory manager for conversation history
	memory *Memory

	// Error store for tool execution errors (supports error submission channel pattern)
	errorStore ErrorStore

	// LLM provider for generating responses
	llm LLMProvider

	// Tracer for observability
	tracer observability.Tracer

	// Prompt registry for loading prompts
	prompts prompts.PromptRegistry

	// Config holds agent configuration
	config *Config

	// Optional self-correction components (injected via options)
	guardrails      *fabric.GuardrailEngine
	circuitBreakers *fabric.CircuitBreakerManager

	// Pattern orchestration
	orchestrator *patterns.Orchestrator

	// Inter-agent communication (optional)
	refStore     communication.ReferenceStore // Reference storage for agent-to-agent communication
	commPolicy   *communication.PolicyManager // Communication policy manager
	messageQueue *communication.MessageQueue  // Message queue for async agent-to-agent communication

	// MCP client tracking for cleanup (lazy initialized)
	mcpClients map[string]MCPClientRef

	// Dynamic tool discovery for MCP servers (lazy tool loading)
	dynamicDiscovery *DynamicToolDiscovery

	// Shared memory store for large tool results (prevents context overflow)
	sharedMemory *storage.SharedMemoryStore

	// Reference tracker for automatic cleanup of shared memory references when sessions end
	refTracker *storage.SessionReferenceTracker

	// SQL result store for queryable large SQL results
	sqlResultStore *storage.SQLResultStore

	// Token counter for accurate token estimation
	tokenCounter *TokenCounter
}

// Config holds agent configuration.
type Config struct {
	// Name is the agent name (used for identification and logging)
	Name string

	// Description is a human-readable description of the agent's purpose
	Description string

	// MaxTurns is the maximum number of conversation turns before forcing completion
	MaxTurns int

	// MaxToolExecutions is the maximum number of tool executions per conversation
	MaxToolExecutions int

	// SystemPrompt is the direct system prompt text (takes precedence over SystemPromptKey)
	SystemPrompt string

	// SystemPromptKey is the key for loading the system prompt from promptio
	SystemPromptKey string

	// ROM identifier for domain-specific knowledge ("TD", "teradata", "auto", or "")
	Rom string

	// Metadata for agent configuration (includes backend_path for ROM auto-detection)
	Metadata map[string]string

	// EnableTracing enables observability tracing
	EnableTracing bool

	// PatternsDir is the directory containing pattern YAML files (optional)
	PatternsDir string

	// Backend configuration
	BackendConfig map[string]interface{}

	// Retry configuration for LLM calls
	Retry RetryConfig

	// MaxContextTokens is the model's context window size (0 = use defaults/auto-detect)
	MaxContextTokens int

	// ReservedOutputTokens is the number of tokens reserved for model output (0 = use defaults, typically 10%)
	ReservedOutputTokens int

	// PatternConfig controls pattern injection (nil = use defaults)
	PatternConfig *PatternConfig
}

// PatternConfig holds pattern injection configuration
type PatternConfig struct {
	// Enabled controls whether pattern injection is active
	Enabled bool

	// MinConfidence is the minimum confidence threshold (0.0-1.0)
	MinConfidence float64

	// MaxPatternsPerTurn limits patterns injected per conversation turn
	MaxPatternsPerTurn int

	// EnableTracking enables pattern effectiveness metrics
	EnableTracking bool

	// UseLLMClassifier enables LLM-based intent classification (default: false, uses keyword-based)
	UseLLMClassifier bool
}

// RetryConfig configures exponential backoff retry logic for LLM calls
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (0 = no retries)
	MaxRetries int

	// InitialDelay is the initial delay before the first retry
	InitialDelay time.Duration

	// MaxDelay is the maximum delay between retries
	MaxDelay time.Duration

	// Multiplier is the exponential backoff multiplier (e.g., 2.0 for doubling)
	Multiplier float64

	// Enabled enables retry logic
	Enabled bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		MaxTurns:          25,
		MaxToolExecutions: 50,
		SystemPromptKey:   "agent.system.base",
		EnableTracing:     true,
		BackendConfig:     make(map[string]interface{}),
		Retry: RetryConfig{
			Enabled:      true,
			MaxRetries:   3,
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     5 * time.Second,
			Multiplier:   2.0,
		},
	}
}

// DefaultPatternConfig returns defaults for pattern injection (enabled by default)
func DefaultPatternConfig() *PatternConfig {
	return &PatternConfig{
		Enabled:            true, // Enabled by default for v1.0.0
		MinConfidence:      0.50, // Balanced confidence threshold (was 0.75)
		MaxPatternsPerTurn: 1,    // Single pattern per turn
		EnableTracking:     true, // Track effectiveness
		UseLLMClassifier:   true, // Use LLM-based intent classifier (more accurate than keyword matching)
	}
}

// ValidatePatternConfig validates pattern configuration
func ValidatePatternConfig(cfg *PatternConfig) error {
	if cfg == nil {
		return nil
	}

	if cfg.MinConfidence < 0.0 || cfg.MinConfidence > 1.0 {
		return fmt.Errorf("min_confidence must be 0.0-1.0, got: %.2f", cfg.MinConfidence)
	}

	if cfg.MaxPatternsPerTurn < 0 || cfg.MaxPatternsPerTurn > 5 {
		return fmt.Errorf("max_patterns_per_turn must be 0-5, got: %d", cfg.MaxPatternsPerTurn)
	}

	return nil
}

// Type aliases for backward compatibility with code that imports pkg/agent.
// These types are now defined in pkg/types to break import cycles.
type Message = types.Message
type ToolCall = types.ToolCall
type Usage = types.Usage
type LLMResponse = types.LLMResponse
type LLMProvider = types.LLMProvider
type Session = types.Session
type Context = types.Context
type ProgressCallback = types.ProgressCallback
type ProgressEvent = types.ProgressEvent
type HITLRequestInfo = types.HITLRequestInfo
type ExecutionStage = types.ExecutionStage

// Re-export ExecutionStage constants for backward compatibility
const (
	StagePatternSelection = types.StagePatternSelection
	StageSchemaDiscovery  = types.StageSchemaDiscovery
	StageLLMGeneration    = types.StageLLMGeneration
	StageToolExecution    = types.StageToolExecution
	StageSynthesis        = types.StageSynthesis
	StageHumanInTheLoop   = types.StageHumanInTheLoop
	StageGuardrailCheck   = types.StageGuardrailCheck
	StageSelfCorrection   = types.StageSelfCorrection
	StageCompleted        = types.StageCompleted
	StageFailed           = types.StageFailed
)

// agentContext implements Context
type agentContext struct {
	context.Context
	session          *Session
	tracer           observability.Tracer
	progressCallback ProgressCallback
}

func (c *agentContext) Session() *Session {
	return c.session
}

func (c *agentContext) Tracer() observability.Tracer {
	return c.tracer
}

func (c *agentContext) ProgressCallback() ProgressCallback {
	return c.progressCallback
}
