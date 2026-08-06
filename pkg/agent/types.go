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
	"database/sql"
	"fmt"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/discovery"
	"github.com/teradata-labs/loom/pkg/skills/hygiene"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
)

// MCPClientRef holds a reference to an MCP client for cleanup
type MCPClientRef struct {
	Client     interface{ Close() error } // MCP client with Close method
	ServerName string
}

// lazyToolSet groups tools that are registered only when trigger(userMsg) returns true.
type lazyToolSet struct {
	tools   []shuttle.Tool
	trigger func(string) bool
}

// Agent is the core conversation agent that orchestrates LLM calls, tool execution,
// and backend interactions. It's designed to be backend-agnostic and work with
// any ExecutionBackend implementation (SQL databases, REST APIs, documents, etc.).
type Agent struct {
	// Unique agent identifier (UUID v4)
	// Registry-managed agents have stable GUIDs persisted to database
	// Standalone agents get ephemeral UUIDs from NewAgent
	id string

	// Mutex for thread-safe access to agent fields
	mu sync.RWMutex

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

	// LLM provider for generating responses
	llm LLMProvider

	// Role-specific LLM providers (nil = fallback to main llm)
	judgeLLM        LLMProvider // For evaluation operations
	orchestratorLLM LLMProvider // For merge/synthesis in fork-join orchestration
	classifierLLM   LLMProvider // For intent classification / pattern selection
	compressorLLM   LLMProvider // For memory compression / semantic search reranking

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

	// Skill orchestration. The skillOrchestrator owns the activation lifecycle;
	// skills enter the conversation only through manage_skills, never by prompt
	// injection. The discovery component (when present) composes binding
	// resolution, the hierarchical PageIndex router and the FTS5 fallback for
	// callers that drive it directly — the conversation loop does not.
	skillOrchestrator *skills.Orchestrator
	skillDiscovery    *discovery.Discovery
	// skillTaskEmitter materializes tasks for newly-activated skills onto
	// the agent's task board. nil means skill activations do not emit tasks
	// (legacy behavior).
	skillTaskEmitter *skilltasks.Emitter
	// skillsTurnState tracks which skills were activated in the current
	// turn so phase D can emit tasks only for the newly-activated set.
	skillsTurnState map[string]map[string]bool // sessionID -> skillName -> activated-this-turn

	// End-of-turn hygiene enforcement for skill-emitted tasks. Constructed
	// when both skillOrchestrator and taskManager are present; runs at the
	// conversation-loop return path to audit IN_PROGRESS / BLOCKED /
	// OPEN-unstarted tasks left dirty by an active skill.
	hygieneAuditor  *hygiene.Auditor
	hygieneEnforcer *hygiene.Enforcer

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

	// Configurable shared memory threshold for large tool results.
	// -1 = use storage.DefaultSharedMemoryThreshold; 0 = always reference; >0 = byte threshold
	sharedMemoryThreshold int64

	// Token counter for accurate token estimation
	tokenCounter *TokenCounter

	// In-turn SQLite databases backing query_tool_result's sql mode (HLD §7.1):
	// per (session, message), lazily built, dropped at turn end. Guarded by
	// inTurnSQLMu.
	inTurnSQL   map[inTurnSQLKey]*sql.DB
	inTurnSQLMu sync.Mutex

	// Workflow communication context (injected dynamically for workflow agents)
	workflowCommContext *WorkflowCommunicationContext

	// Provider pool support
	providerPool       map[string]LLMProvider // name → provider (nil = pool not configured)
	activeProviderName string                 // currently active provider name (empty = use llm field)
	allowedProviders   []string               // empty = all pool providers allowed

	// Lazy tool sets: registered only when trigger(userMessage) returns true.
	// Guarded by a.mu.
	lazyToolSets []lazyToolSet

	// suppressedBuiltinTools blocks agent-internal registrations (graph_memory,
	// task_board, manage_skills, load_pattern) and the retrieval tools
	// (query_tool_result, recall) from surfacing to the LLM. Subsystems
	// themselves (extractor, task manager) keep running.
	//
	// Populated by WithoutBuiltinTool options. The server (cmd/looms) is
	// the single source of truth for which tools should be surfaced —
	// agent code only consults this map.
	suppressedBuiltinTools map[string]bool

	// Per-session advertised-tool projection. The shared tools Registry is the
	// definition store (what CAN exist); advertisement is scoped per session so
	// one session's event never changes another's tool list.
	//
	// sessionToolLedger maps a session ID to the set of tool names THIS
	// session's own events registered (skill required_tools, progressive-
	// disclosure error/query tools). scopedToolNames is the union of every
	// session-scoped name across all sessions: a name in this set is advertised
	// to a session ONLY when that session's ledger holds it. baseToolNames is
	// the always-advertised set captured once, before any event-driven
	// registration, so a base tool a skill happens to require is not mistaken
	// for a session-scoped tool. Base tools never enter scopedToolNames, so the
	// projection can never drop one. Guarded by a.mu.
	sessionToolLedger map[string]map[string]bool
	scopedToolNames   map[string]bool
	baseToolNames     map[string]bool
	baseToolsOnce     sync.Once

	// Graph-backed episodic memory (optional).
	graphMemoryStore  memory.GraphMemoryStore
	graphMemoryConfig *loomv1.GraphMemoryConfig
	embedder          memory.Embedder // vector embeddings for semantic search (optional)

	// Task decomposition and kanban (optional).
	taskManager     *task.Manager
	taskDecomposer  *task.Decomposer
	taskBoardConfig *loomv1.TaskBoardConfig

	// Graph memory automatic extraction (mirrors finding extraction pattern).
	enableGraphMemoryExtraction        bool
	graphExtractionCadence             int
	graphToolExecutionsSinceExtraction int
	graphExtractionWG                  sync.WaitGroup // tracks in-flight async extractions

	// Conversation-turn-based graph extraction (fires on LLM responses, not tool use).
	graphConversationExtractionCadence int // 0 = disabled
	graphTurnsSinceExtraction          int

	// Debug context-dump sink. Lazily constructed on the first provider call
	// that has dumping enabled; stays nil on the production path. See
	// context_dump.go.
	contextDump     *contextDumper
	contextDumpOnce sync.Once

	// Per-mutation debug carrier injected into the memory and skill components.
	// Shares the context-dump switch; a no-op on the production path. See
	// context_debug.go.
	ctxDebug *contextDebug
}

// WorkflowCommunicationContext contains dynamic workflow communication info injected into prompts
type WorkflowCommunicationContext struct {
	// Subscribed topics for pub-sub communication
	SubscribedTopics []string

	// Available agents for point-to-point communication (workflow:agent format)
	AvailableAgents []string

	// Workflow name (for constructing agent IDs)
	WorkflowName string
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

	// MaxIterations caps tool calls executed per single LLM response (per-turn).
	// When the LLM emits more tool calls than this limit in one response, only
	// MaxIterations are executed; the rest receive "turn_limit_exceeded" errors.
	// 0 = use default (10).
	MaxIterations int

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

	// ProtectedRecentTurns is K (HLD §5.1): the top rung of relief's halving
	// ladder — the newest turns relief tries hardest to keep, though the ladder
	// walks toward T−1 when shedding at K is not enough. 0 = default (16).
	ProtectedRecentTurns int

	// PatternConfig controls pattern injection (nil = use defaults)
	PatternConfig *PatternConfig

	// SkillsConfig controls skill activation and injection (nil = use defaults)
	SkillsConfig *skills.SkillsConfig

	// Automatic finding extraction configuration
	EnableFindingExtraction bool // Whether to enable automatic finding extraction (default: true)
	ExtractionCadence       int  // Number of tool executions between extractions (default: 3)
	MaxFindings             int  // Maximum findings to keep in cache (default: 50)

	// OutputTokenCBThreshold is the number of consecutive turns where the LLM
	// hits the output token limit AND returns truncated tool calls before the
	// circuit breaker fires. 0 uses the default (8). -1 disables the CB entirely.
	OutputTokenCBThreshold int

	// EnableSelfHealing enables Tier 1 automatic recovery (context trimming,
	// tool disabling) before errors propagate to the caller. Default: true.
	EnableSelfHealing bool

	// RecoveryConfig holds tunables for the self-healing orchestrator.
	// Nil uses DefaultRecoveryConfig().
	RecoveryConfig *RecoveryConfig

	// Debug holds opt-in diagnostic switches, all off by default.
	Debug DebugConfig
}

// DebugConfig holds opt-in diagnostic switches. Every field defaults to its
// zero value (off) so the production path stays untouched unless a switch is
// explicitly set.
type DebugConfig struct {
	// ContextDump enables one un-redacted dump record per provider call,
	// capturing the compiled (messages, tools) about to be dispatched. Written
	// only to a local per-run sink, never to zap/Hawk. Off by default; also
	// enabled via the LOOM_DEBUG_CONTEXT_DUMP environment variable.
	ContextDump bool
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
		MaxIterations:     10,
		SystemPromptKey:   "agent.system.base",
		EnableTracing:     true,
		EnableSelfHealing: true,
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
type ContentBlock = types.ContentBlock
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
