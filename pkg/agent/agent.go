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
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
)

// progressCallbackKey is the context key for storing progress callbacks
type progressCallbackKey struct{}

// ContextWithProgressCallback stores a progress callback in the context
// so that nested operations (like tool executions) can emit progress events.
func ContextWithProgressCallback(ctx context.Context, callback ProgressCallback) context.Context {
	return context.WithValue(ctx, progressCallbackKey{}, callback)
}

// ProgressCallbackFromContext retrieves the progress callback from context.
// Returns nil if no callback is stored in the context.
func ProgressCallbackFromContext(ctx context.Context) ProgressCallback {
	if cb, ok := ctx.Value(progressCallbackKey{}).(ProgressCallback); ok {
		return cb
	}
	return nil
}

// NewAgent creates a new Agent instance.
//
// For comprehensive observability, pass instrumented LLM and executor:
//
//	llmProvider = llm.NewInstrumentedProvider(baseProvider, tracer)
//	// Then create agent with WithTracer(tracer)
//
// The agent will automatically use instrumented versions if provided,
// enabling end-to-end tracing of conversations, LLM calls, and tool executions.
func NewAgent(backend fabric.ExecutionBackend, llmProvider LLMProvider, opts ...Option) *Agent {
	a := &Agent{
		backend:      backend,
		llm:          llmProvider,
		tools:        shuttle.NewRegistry(),
		memory:       NewMemory(),
		config:       DefaultConfig(),
		tracer:       observability.NewNoOpTracer(),
		prompts:      nil, // Will be set via options
		tokenCounter: GetTokenCounter(),
	}

	// Enable self-correction by default (guardrails + circuit breakers)
	// Users can opt-out via WithoutSelfCorrection() or provide custom implementations
	a.guardrails = fabric.NewGuardrailEngine()
	a.circuitBreakers = fabric.NewCircuitBreakerManager(fabric.DefaultCircuitBreakerConfig())

	// Apply options (may override defaults above)
	for _, opt := range opts {
		opt(a)
	}

	// Initialize pattern config with defaults if not set
	if a.config.PatternConfig == nil {
		a.config.PatternConfig = DefaultPatternConfig()
	}

	// Initialize automatic finding extraction with defaults if not set
	if a.config.ExtractionCadence == 0 {
		a.config.ExtractionCadence = 3 // Default: extract every 3 tool calls
	}
	if a.config.MaxFindings == 0 {
		a.config.MaxFindings = 50 // Default: keep 50 findings
	}
	// Enable by default (can be explicitly disabled by setting enable_finding_extraction: false in config)
	a.enableFindingExtraction = true
	a.extractionCadence = a.config.ExtractionCadence
	a.toolExecutionsSinceExtraction = 0

	// Initialize pattern orchestrator
	patternLibrary := patterns.NewLibrary(nil, a.config.PatternsDir)
	a.orchestrator = patterns.NewOrchestrator(patternLibrary)

	// Initialize LLM classifier if configured
	if a.config.PatternConfig.UseLLMClassifier && llmProvider != nil {
		llmClassifierConfig := patterns.DefaultLLMClassifierConfig(llmProvider)
		llmClassifier := patterns.NewLLMIntentClassifier(llmClassifierConfig)
		a.orchestrator.SetIntentClassifier(llmClassifier)
	}

	// Create executor with tool registry
	// Note: Pass instrumented executor via SetExecutor() if you want tool tracing
	a.executor = shuttle.NewExecutor(a.tools)

	// Set permission checker on executor if provided
	if a.permissionChecker != nil {
		a.executor.SetPermissionChecker(a.permissionChecker)
	}

	// Set up system prompt function for memory
	// This allows dynamic prompt loading from PromptRegistry
	a.memory.SetSystemPromptFunc(func() string {
		return a.getSystemPrompt()
	})

	// Set context limits for memory (if configured)
	if a.config.MaxContextTokens > 0 || a.config.ReservedOutputTokens > 0 {
		a.memory.SetContextLimits(a.config.MaxContextTokens, a.config.ReservedOutputTokens)
	}

	// Initialize shared memory store for large tool results (#2: Persistent Global Storage)
	// Use global singleton so references work across agent instances and survive restarts.
	// The global store provides:
	// - Shared references across all agents (no per-instance isolation issues)
	// - Disk-backed persistence (survives restarts)
	// - LRU eviction to disk (survives memory pressure)
	a.sharedMemory = storage.GetGlobalSharedMemory(&storage.Config{
		MaxMemoryBytes:       500 * 1024 * 1024, // 500MB in-memory hot cache (increased for data-intensive workloads)
		CompressionThreshold: 1024 * 1024,       // Compress results >1MB
		TTLSeconds:           3600,              // 1 hour TTL (both memory and disk)
	})

	// Set shared memory on agent memory for coordination
	if a.memory != nil {
		a.memory.SetSharedMemory(a.sharedMemory)
		// Pass tracer to memory for error logging in swap operations
		a.memory.SetTracer(a.tracer)
		// Pass LLM provider to memory for semantic search reranking
		a.memory.SetLLMProvider(a.llm)
	}

	// Set shared memory on executor so large tool results are stored in the same store
	// that GetToolResultTool retrieves from (fixes tool reference loop bug)
	if a.executor != nil && a.sharedMemory != nil {
		a.executor.SetSharedMemory(a.sharedMemory, storage.DefaultSharedMemoryThreshold)
	}

	// PROGRESSIVE DISCLOSURE: get_error_details tool is registered dynamically after first error
	// See formatToolResult() for automatic registration when error store is used

	// REMOVED: Manual record_finding tool (replaced by automatic extraction)
	// Findings are now automatically extracted from tool results using LLM-based semantic analysis
	// See finding_extractor.go for implementation

	// Initialize SQL result store for queryable large SQL results
	// This allows filtering/aggregating SQL results without context blowout
	sqlResultStore, err := storage.NewSQLResultStore(&storage.SQLResultStoreConfig{
		DBPath:     storage.GetDefaultLoomDBPath(),
		TTLSeconds: 3600, // 1 hour TTL
	})
	if err == nil {
		// Store reference for later use (e.g., when SetSharedMemory() is called)
		a.sqlResultStore = sqlResultStore

		// Set on executor so SQL results go to queryable tables
		if a.executor != nil {
			a.executor.SetSQLResultStore(sqlResultStore)
		}

		// PROGRESSIVE DISCLOSURE: query_tool_result tool is registered dynamically after first large result
		// See formatToolResult() for automatic registration when large results are stored
	}

	// Auto-register shell_execute tool (standard toolset)
	// Uses LOOM_DATA_DIR as baseDir for consistent artifact/data management
	a.tools.Register(builtin.NewShellExecuteTool(config.GetLoomDataDir()))

	// Note: tool_search is registered by AgentRegistry when a global tool registry is available
	// Individual agents don't have access to the global tool registry during construction

	// Register built-in get_tool_result tool for retrieving metadata
	// EXPERIMENT: get_tool_result removed - inline metadata makes it unnecessary
	// Inline metadata now includes preview, schema, size, and retrieval hints directly in tool responses.
	// Agents should use query_tool_result for advanced querying (pagination, SQL filters).
	//
	// v1.0.1: Now returns only metadata, accepts both memory and SQL stores
	// if a.sharedMemory != nil || sqlResultStore != nil {
	// 	a.tools.Register(NewGetToolResultTool(a.sharedMemory, sqlResultStore))
	// }
	// If SQL store fails to initialize, just log and continue without it
	// (SQL results will fall back to shared memory)

	// Initialize reference tracker for automatic cleanup (#1: Session-Scoped Reference Pinning)
	// When sessions end, this ensures all SharedMemory references are released (RefCount decremented).
	// Without this, references accumulate indefinitely causing memory leaks.
	if a.sharedMemory != nil {
		a.refTracker = storage.NewSessionReferenceTracker(a.sharedMemory)

		// Register cleanup hook with SessionStore if persistence is enabled
		// This decouples reference cleanup from session deletion logic
		if a.memory != nil {
			if store := a.memory.GetStore(); store != nil {
				store.RegisterCleanupHook(a.cleanupSessionReferences)
			}
		}
	}

	// EXPERIMENT: Long-conversation tools disabled to test scratchpad + inline metadata approach
	// These tools (recall_conversation, search_conversation, clear_recalled_context) previously
	// enabled agents to access long-term conversation history from swap layer.
	// Testing hypothesis: inline metadata + scratchpad may be more effective for multi-turn tasks.
	//
	// recallTool := shuttle.Tool(NewRecallConversationTool(a.memory))
	// clearTool := shuttle.Tool(NewClearRecalledContextTool(a.memory))
	// searchTool := shuttle.Tool(NewSearchConversationTool(a.memory))
	//
	// if a.prompts != nil {
	// 	recallTool = shuttle.NewPromptAwareTool(recallTool, a.prompts, "tools.memory.recall_conversation_description")
	// 	clearTool = shuttle.NewPromptAwareTool(clearTool, a.prompts, "tools.memory.clear_recalled_context_description")
	// 	searchTool = shuttle.NewPromptAwareTool(searchTool, a.prompts, "tools.memory.search_conversation_description")
	// }
	//
	// a.tools.Register(recallTool)
	// a.tools.Register(clearTool)
	// a.tools.Register(searchTool)

	return a
}

// Option is a functional option for configuring an Agent.
type Option func(*Agent)

// WithTracer sets the observability tracer.
func WithTracer(tracer observability.Tracer) Option {
	return func(a *Agent) {
		a.tracer = tracer
	}
}

// WithPrompts sets the prompt registry.
func WithPrompts(registry prompts.PromptRegistry) Option {
	return func(a *Agent) {
		a.prompts = registry
	}
}

// WithConfig sets the agent configuration.
func WithConfig(config *Config) Option {
	return func(a *Agent) {
		a.config = config
	}
}

// WithMemory sets a custom memory manager.
func WithMemory(memory *Memory) Option {
	return func(a *Agent) {
		a.memory = memory
	}
}

// WithSharedMemory sets the SharedMemoryStore for large tool result storage.
// This enables agents to store and reference large tool outputs efficiently.
func WithSharedMemory(sharedMemory interface{}) Option {
	return func(a *Agent) {
		if sm, ok := sharedMemory.(*storage.SharedMemoryStore); ok {
			a.sharedMemory = sm
		}
	}
}

// WithCompressionProfile sets the compression profile for memory management.
// This controls compression thresholds and batch sizes for conversation history.
func WithCompressionProfile(profile *CompressionProfile) Option {
	return func(a *Agent) {
		a.memory.SetCompressionProfile(profile)
	}
}

// WithPermissionChecker sets the permission checker for tool execution.
func WithPermissionChecker(checker *shuttle.PermissionChecker) Option {
	return func(a *Agent) {
		a.permissionChecker = checker
	}
}

// WithGuardrails enables pre-flight validation and error tracking.
func WithGuardrails(guardrails *fabric.GuardrailEngine) Option {
	return func(a *Agent) {
		a.guardrails = guardrails
	}
}

// WithCircuitBreakers enables failure isolation for tools.
func WithCircuitBreakers(breakers *fabric.CircuitBreakerManager) Option {
	return func(a *Agent) {
		a.circuitBreakers = breakers
	}
}

// WithoutSelfCorrection explicitly disables self-correction (guardrails + circuit breakers).
// By default, agents have self-correction enabled. Use this option to disable it.
// Note: This creates a marker guardrails/breakers that prevents default initialization.
func WithoutSelfCorrection() Option {
	return func(a *Agent) {
		// Set to a zero-capacity manager to signal "explicitly disabled"
		// This prevents the default initialization in NewAgent
		a.guardrails = nil
		a.circuitBreakers = nil
	}
}

// WithName sets the agent name in the configuration.
func WithName(name string) Option {
	return func(a *Agent) {
		a.config.Name = name
	}
}

// WithSystemPrompt sets the direct system prompt text.
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) {
		a.config.SystemPrompt = prompt
	}
}

// WithDescription sets the agent description.
func WithDescription(description string) Option {
	return func(a *Agent) {
		a.config.Description = description
	}
}

// WithErrorStore enables error submission channel for storing full error details.
// When set, tool execution errors are stored in SQLite with only summaries sent to LLM.
// The get_error_details built-in tool is automatically registered.
func WithErrorStore(store ErrorStore) Option {
	return func(a *Agent) {
		a.errorStore = store
	}
}

// WithMessageQueue enables async agent-to-agent messaging.
// When set, agents can send/receive messages via the queue, enabling
// fire-and-forget, request-response, and acknowledgment-based communication.
func WithMessageQueue(queue *communication.MessageQueue) Option {
	return func(a *Agent) {
		a.messageQueue = queue
	}
}

// WithPatternConfig sets pattern configuration.
func WithPatternConfig(cfg *PatternConfig) Option {
	return func(a *Agent) {
		a.config.PatternConfig = cfg
	}
}

// WithPatternInjection enables/disables pattern injection.
func WithPatternInjection(enabled bool) Option {
	return func(a *Agent) {
		if a.config.PatternConfig == nil {
			a.config.PatternConfig = DefaultPatternConfig()
		}
		a.config.PatternConfig.Enabled = enabled
	}
}

// RegisterTool registers a tool with the agent.
func (a *Agent) RegisterTool(tool shuttle.Tool) {
	a.tools.Register(tool)
}

// RegisterTools registers multiple tools.
func (a *Agent) RegisterTools(tools ...shuttle.Tool) {
	for _, tool := range tools {
		a.RegisterTool(tool)
	}
}

// UnregisterTool unregisters a tool by name.
func (a *Agent) UnregisterTool(name string) {
	a.tools.Unregister(name)
}

// ToolCount returns the number of registered tools.
func (a *Agent) ToolCount() int {
	return a.tools.Count()
}

// ListTools returns a list of all registered tool names.
func (a *Agent) ListTools() []string {
	return a.tools.List()
}

// GetName returns the agent name from configuration.
func (a *Agent) GetName() string {
	return a.config.Name
}

// SetToolRegistryForDynamicDiscovery configures the tool registry for dynamic tool discovery.
// When enabled, agents can use tools discovered via tool_search without explicit registration.
// MCP tools found in the registry will be dynamically registered when first used.
func (a *Agent) SetToolRegistryForDynamicDiscovery(toolRegistry shuttle.ToolRegistry, mcpManager shuttle.MCPManager) {
	if a.executor == nil {
		return
	}
	if toolRegistry != nil {
		a.executor.SetToolRegistry(toolRegistry)
	}
	if mcpManager != nil {
		a.executor.SetMCPManager(mcpManager)
	}
}

// GetDescription returns the agent description from configuration.
func (a *Agent) GetDescription() string {
	return a.config.Description
}

// GetConfig returns a copy of the agent configuration.
func (a *Agent) GetConfig() *Config {
	// Return a copy to prevent external modification
	configCopy := *a.config
	return &configCopy
}

// getSystemPrompt loads the system prompt from config or PromptRegistry.
// Priority: ROM + Config.SystemPrompt (if explicitly set) > ROM + PromptRegistry > Default
// ROM (Read-Only Memory) provides domain-specific knowledge loaded based on config.Rom
// formatSystemPromptWithDatetime prepends current date/time information to system prompts.
// This helps agents maintain temporal awareness and prevents confusion about the current date.
func formatSystemPromptWithDatetime(prompt string, workflowCtx *WorkflowCommunicationContext) string {
	now := time.Now()

	// Format: "Monday, January 2, 2006 at 3:04 PM MST"
	dateStr := now.Format("Monday, January 2, 2006")
	timeStr := now.Format("3:04 PM MST")
	timezone := now.Location().String()

	// Get UTC offset for clarity
	_, offset := now.Zone()
	offsetHours := offset / 3600
	offsetSign := "+"
	if offsetHours < 0 {
		offsetSign = "-"
		offsetHours = -offsetHours
	}

	header := fmt.Sprintf("CURRENT DATE AND TIME\n"+
		"Date: %s\n"+
		"Time: %s (UTC%s%d)\n"+
		"Timezone: %s\n\n"+
		"---\n\n",
		dateStr, timeStr, offsetSign, offsetHours, timezone)

	// Add workflow communication instructions if available
	workflowInstructions := formatWorkflowCommunicationInstructions(workflowCtx)

	return header + workflowInstructions + prompt
}

func (a *Agent) getSystemPrompt() string {
	// Load ROM content first (if configured)
	var romContent string
	if a.config != nil {
		// Get backend_path from metadata for auto-detection
		backendPath := ""
		if a.config.Metadata != nil {
			backendPath = a.config.Metadata["backend_path"]
		}

		// Load ROM based on config.Rom and backend_path
		romContent = LoadROMContent(a.config.Rom, backendPath)
	}

	// If agent has a custom system prompt in config, use it (takes priority)
	if a.config != nil && a.config.SystemPrompt != "" {
		// Combine ROM + System Prompt
		if romContent != "" {
			return formatSystemPromptWithDatetime(romContent+"\n\n---\n\n"+a.config.SystemPrompt, a.workflowCommContext)
		}
		return formatSystemPromptWithDatetime(a.config.SystemPrompt, a.workflowCommContext)
	}

	// Try loading from PromptRegistry as fallback
	if a.prompts != nil {
		patternCount := 0
		if a.orchestrator != nil && a.orchestrator.GetLibrary() != nil {
			patternCount = len(a.orchestrator.GetLibrary().ListAll())
		}

		vars := map[string]interface{}{
			"backend_type":       a.backend.Name(),
			"tool_count":         a.tools.Count(),
			"pattern_count":      patternCount,
			"pattern_categories": "none",
		}

		// Check if streaming is supported by the LLM provider
		streamingSupported := types.SupportsStreaming(a.llm)

		// Try streaming-specific prompt if supported
		if streamingSupported {
			prompt, err := a.prompts.Get(context.Background(), "agent.system_with_streaming", vars)
			if err == nil && prompt != "" {
				return formatSystemPromptWithDatetime(prompt, a.workflowCommContext)
			}
			// Fall through to standard prompt if streaming prompt not found
		}

		// Try loading system prompt with patterns if pattern library is available
		prompt, err := a.prompts.Get(context.Background(), "agent.system", vars)
		if err == nil && prompt != "" {
			return formatSystemPromptWithDatetime(prompt, a.workflowCommContext)
		}

		// Fall back to basic system prompt
		prompt, err = a.prompts.Get(context.Background(), "agent.system_basic", vars)
		if err == nil && prompt != "" {
			return formatSystemPromptWithDatetime(prompt, a.workflowCommContext)
		}
	}

	// Fall back to config
	if a.config.SystemPrompt != "" {
		// Combine ROM + System Prompt
		if romContent != "" {
			return formatSystemPromptWithDatetime(romContent+"\n\n---\n\n"+a.config.SystemPrompt, a.workflowCommContext)
		}
		return formatSystemPromptWithDatetime(a.config.SystemPrompt, a.workflowCommContext)
	}

	// If we have ROM but no system prompt, just use ROM
	if romContent != "" {
		return formatSystemPromptWithDatetime(romContent, a.workflowCommContext)
	}

	// Final fallback - minimal instruction
	return formatSystemPromptWithDatetime(`Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return.`, a.workflowCommContext)
}

// SetWorkflowCommunicationContext sets the workflow communication context for this agent.
// This context is used to inject dynamic communication instructions into the system prompt.
func (a *Agent) SetWorkflowCommunicationContext(ctx *WorkflowCommunicationContext) {
	a.workflowCommContext = ctx
}

// formatWorkflowCommunicationInstructions generates concise, directive communication instructions
// based on the agent's workflow context. These instructions are injected at the top of the system
// prompt (after timestamp) to ensure LLMs see and follow them.
func formatWorkflowCommunicationInstructions(ctx *WorkflowCommunicationContext) string {
	if ctx == nil {
		return ""
	}

	var instructions strings.Builder

	// Pub-sub instructions (if subscribed to topics)
	if len(ctx.SubscribedTopics) > 0 {
		instructions.WriteString("🔔 WORKFLOW COMMUNICATION (PUB-SUB)\n")
		instructions.WriteString(fmt.Sprintf("Subscribed topics: %s\n", strings.Join(ctx.SubscribedTopics, ", ")))
		instructions.WriteString("→ To post: publish(topic=\"topic-name\", message=\"your message\")\n")
		instructions.WriteString("→ Responses auto-inject as \"[BROADCAST FROM agent]: ...\"\n")
		instructions.WriteString("→ Do NOT poll - you will be notified automatically\n\n")
	}

	// Point-to-point instructions (if available agents)
	if len(ctx.AvailableAgents) > 0 {
		instructions.WriteString("🔔 WORKFLOW COMMUNICATION (DIRECT MESSAGING)\n")
		instructions.WriteString(fmt.Sprintf("Available agents: %s\n", strings.Join(ctx.AvailableAgents, ", ")))
		instructions.WriteString("→ To send: send_message(to_agent=\"agent-id\", message=\"task description\")\n")
		instructions.WriteString("→ Responses auto-inject as \"[MESSAGE FROM agent]: ...\"\n")
		instructions.WriteString("→ Do NOT poll - you will be notified automatically\n\n")
	}

	if instructions.Len() > 0 {
		instructions.WriteString("---\n\n")
	}

	return instructions.String()
}

// getGuidanceMessage loads a guidance message from PromptRegistry or returns default.
// Used for user-facing messages like error recovery, max turns, etc.
func (a *Agent) getGuidanceMessage(key string, vars map[string]interface{}) string {
	// Try loading from PromptRegistry
	if a.prompts != nil {
		fullKey := "guidance." + key
		msg, err := a.prompts.Get(context.Background(), fullKey, vars)
		if err == nil && msg != "" {
			return msg
		}
	}

	// Fallbacks for common messages
	switch key {
	case "max_turns_reached":
		return "I apologize, but I've reached my processing limit. Please try rephrasing your request or breaking it into smaller steps."
	case "llm_call_failed":
		return "I encountered an error while processing your request. Please try again or rephrase your question."
	case "tool_execution_failed":
		if vars != nil {
			if errMsg, ok := vars["error"].(string); ok {
				return fmt.Sprintf("The tool execution failed: %s. Let me try a different approach.", errMsg)
			}
		}
		return "The tool execution failed. Let me try a different approach."
	default:
		return "An unexpected situation occurred. Please try again."
	}
}

// getErrorMessage loads an error message from PromptRegistry or returns default.
// Used for structured error messages with context variables.
// Format: errors.{category}.{type} (e.g., "errors.llm.timeout", "errors.tool_execution.invalid_input")
//
//nolint:unused // Infrastructure for future error message integration
func (a *Agent) getErrorMessage(category string, errorType string, vars map[string]interface{}) string {
	// Try loading from PromptRegistry
	if a.prompts != nil {
		fullKey := fmt.Sprintf("errors.%s.%s", category, errorType)
		msg, err := a.prompts.Get(context.Background(), fullKey, vars)
		if err == nil && msg != "" {
			return msg
		}
	}

	// Fallback to generic error message if PromptRegistry not available
	// These are minimal fallbacks - prefer PromptRegistry for detailed messages
	if vars != nil {
		if errMsg, ok := vars["error_message"].(string); ok {
			return errMsg
		}
		if errDetails, ok := vars["error_details"].(string); ok {
			return errDetails
		}
	}
	return fmt.Sprintf("Error in %s: %s", category, errorType)
}

// Chat processes a user message and returns a response.
// This is the main entry point for conversational interaction.
func (a *Agent) Chat(ctx context.Context, sessionID string, userMessage string) (*Response, error) {
	// Inject session ID into context for tool access
	ctx = session.WithSessionID(ctx, sessionID)

	// Start trace span with detailed attributes
	var span *observability.Span
	startTime := time.Now()

	if a.config.EnableTracing {
		ctx, span = a.tracer.StartSpan(ctx, observability.SpanAgentConversation)
		defer a.tracer.EndSpan(span)

		// Set initial attributes
		span.SetAttribute(observability.AttrSessionID, sessionID)
		span.SetAttribute("message.length", len(userMessage))
		span.SetAttribute("message.preview", truncateString(userMessage, 100))
		span.SetAttribute("llm.provider", a.llm.Name())
		span.SetAttribute("llm.model", a.llm.Model())
		span.SetAttribute("config.max_turns", a.config.MaxTurns)
		span.SetAttribute("config.max_tool_executions", a.config.MaxToolExecutions)

		// Record conversation started event
		span.AddEvent("conversation.started", map[string]interface{}{
			"session_id":     sessionID,
			"message_length": len(userMessage),
		})
	}

	// Get or create session with agent metadata for proper ReferenceStore namespacing
	session := a.memory.GetOrCreateSessionWithAgent(sessionID, a.config.Name, "")

	// Add user message to history
	userMsg := Message{
		Role:      "user",
		Content:   userMessage,
		Timestamp: time.Now(),
	}
	session.AddMessage(userMsg)

	// Persist message if storage configured
	if err := a.memory.PersistMessage(ctx, sessionID, userMsg); err != nil {
		// Log error but don't fail the request
		if span != nil {
			span.RecordError(err)
		}
	}

	// Create agent context
	agentCtx := &agentContext{
		Context:          ctx,
		session:          session,
		tracer:           a.tracer,
		progressCallback: nil, // No progress callback for regular Chat
	}

	// Run conversation loop
	response, err := a.runConversationLoop(agentCtx)

	// Calculate total duration
	duration := time.Since(startTime)

	if err != nil {
		if span != nil {
			span.Status = observability.Status{
				Code:    observability.StatusError,
				Message: err.Error(),
			}
			span.SetAttribute(observability.AttrErrorMessage, err.Error())
			span.AddEvent("conversation.failed", map[string]interface{}{
				"error":       err.Error(),
				"duration_ms": duration.Milliseconds(),
			})
		}

		// Emit error metric
		if a.config.EnableTracing {
			a.tracer.RecordMetric("agent.conversations.failed", 1, map[string]string{
				observability.AttrSessionID: sessionID,
			})
		}

		return nil, fmt.Errorf("conversation loop failed: %w", err)
	}

	// Add assistant response to history
	assistantMsg := Message{
		Role:       "assistant",
		Content:    response.Content,
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}
	session.AddMessage(assistantMsg)

	// Persist final message and session
	if err := a.memory.PersistMessage(ctx, sessionID, assistantMsg); err != nil {
		if span != nil {
			span.RecordError(err)
		}
	}
	if err := a.memory.PersistSession(ctx, session); err != nil {
		if span != nil {
			span.RecordError(err)
		}
	}

	// Record success metrics and span attributes
	if span != nil {
		span.Status = observability.Status{
			Code: observability.StatusOK,
		}

		// Capture conversation metrics
		turns := response.Metadata["turns"].(int)
		toolExecs := response.Metadata["tool_executions"].(int)

		span.SetAttribute("conversation.turns", turns)
		span.SetAttribute("conversation.tool_executions", toolExecs)
		span.SetAttribute("conversation.duration_ms", duration.Milliseconds())
		span.SetAttribute("conversation.tokens.total", response.Usage.TotalTokens)
		span.SetAttribute("conversation.tokens.input", response.Usage.InputTokens)
		span.SetAttribute("conversation.tokens.output", response.Usage.OutputTokens)
		span.SetAttribute("conversation.cost.usd", response.Usage.CostUSD)
		span.SetAttribute("conversation.stop_reason", response.Metadata["stop_reason"])
		span.SetAttribute("response.length", len(response.Content))
		span.SetAttribute("response.preview", truncateString(response.Content, 100))

		// Check if we hit limits
		if maxTurnsHit, ok := response.Metadata["max_turns_hit"].(bool); ok && maxTurnsHit {
			span.SetAttribute("conversation.max_turns_hit", true)
		}
		if maxExecHit, ok := response.Metadata["max_exec_hit"].(bool); ok && maxExecHit {
			span.SetAttribute("conversation.max_executions_hit", true)
		}

		// Record completion event
		span.AddEvent("conversation.completed", map[string]interface{}{
			"duration_ms":     duration.Milliseconds(),
			"turns":           turns,
			"tool_executions": toolExecs,
			"cost_usd":        response.Usage.CostUSD,
			"tokens":          response.Usage.TotalTokens,
		})
	}

	// Emit metrics
	if a.config.EnableTracing {
		a.tracer.RecordMetric(observability.MetricAgentConversations, 1, map[string]string{
			observability.AttrSessionID: sessionID,
			"status":                    "success",
		})

		a.tracer.RecordMetric(observability.MetricAgentConversationDuration, float64(duration.Milliseconds()), map[string]string{
			observability.AttrSessionID: sessionID,
		})

		turns := response.Metadata["turns"].(int)
		toolExecs := response.Metadata["tool_executions"].(int)

		a.tracer.RecordMetric("agent.turns.total", float64(turns), map[string]string{
			observability.AttrSessionID: sessionID,
		})

		a.tracer.RecordMetric("agent.tool_executions.total", float64(toolExecs), map[string]string{
			observability.AttrSessionID: sessionID,
		})

		a.tracer.RecordMetric("agent.cost.usd", response.Usage.CostUSD, map[string]string{
			observability.AttrSessionID: sessionID,
		})

		a.tracer.RecordMetric("agent.tokens.total", float64(response.Usage.TotalTokens), map[string]string{
			observability.AttrSessionID: sessionID,
		})
	}

	return response, nil
}

// ChatWithProgress is like Chat but supports streaming progress updates.
// The progressCallback will be called at key execution stages to report progress.
// This is used by StreamWeave to provide real-time feedback to clients.
func (a *Agent) ChatWithProgress(ctx context.Context, sessionID string, userMessage string, progressCallback ProgressCallback) (*Response, error) {
	// Inject session ID into context for tool access
	ctx = session.WithSessionID(ctx, sessionID)

	// Start trace span if enabled
	var span *observability.Span
	if a.config.EnableTracing {
		ctx, span = a.tracer.StartSpan(ctx, "agent.chat_with_progress")
		defer a.tracer.EndSpan(span)
		span.SetAttribute("session_id", sessionID)
		span.SetAttribute("message_length", fmt.Sprintf("%d", len(userMessage)))
	}

	// Get or create session with agent metadata for proper ReferenceStore namespacing
	session := a.memory.GetOrCreateSessionWithAgent(sessionID, a.config.Name, "")

	// Add user message to history
	userMsg := Message{
		Role:      "user",
		Content:   userMessage,
		Timestamp: time.Now(),
	}
	session.AddMessage(userMsg)

	// Persist message if storage configured
	if err := a.memory.PersistMessage(ctx, sessionID, userMsg); err != nil {
		// Log error but don't fail the request
		if span != nil {
			span.RecordError(err)
		}
	}

	// Store progressCallback in context so nested operations (tools, backends) can access it
	// This enables sub-agent progress reporting (e.g., weaver's sub-agents)
	if progressCallback != nil {
		ctx = ContextWithProgressCallback(ctx, progressCallback)
	}

	// Create agent context with progress callback
	agentCtx := &agentContext{
		Context:          ctx, // Now contains progressCallback
		session:          session,
		tracer:           a.tracer,
		progressCallback: progressCallback,
	}

	// Run conversation loop (will emit progress events)
	response, err := a.runConversationLoop(agentCtx)
	if err != nil {
		// Emit failure event
		if progressCallback != nil {
			progressCallback(ProgressEvent{
				Stage:     StageFailed,
				Progress:  0,
				Message:   fmt.Sprintf("Execution failed: %v", err),
				Timestamp: time.Now(),
			})
		}
		return nil, fmt.Errorf("conversation loop failed: %w", err)
	}

	// Add assistant response to history
	assistantMsg := Message{
		Role:       "assistant",
		Content:    response.Content,
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}
	session.AddMessage(assistantMsg)

	// Persist final message and session
	if err := a.memory.PersistMessage(ctx, sessionID, assistantMsg); err != nil {
		if span != nil {
			span.RecordError(err)
		}
	}
	if err := a.memory.PersistSession(ctx, session); err != nil {
		if span != nil {
			span.RecordError(err)
		}
	}

	// Note: We don't emit StageCompleted here - the caller (StreamWeave) will
	// emit it with the full result included in the progress event

	return response, nil
}

// Response represents the agent's response to a user message.
type Response struct {
	// Content is the text response
	Content string

	// Usage tracks token usage and cost
	Usage Usage

	// ToolExecutions contains tools that were executed
	ToolExecutions []ToolExecution

	// Metadata contains additional response information
	Metadata map[string]interface{}

	// Thinking contains the agent's internal reasoning process
	// (for models that support extended thinking)
	Thinking string
}

// ToolExecution records a tool execution.
type ToolExecution struct {
	ToolName string
	Input    map[string]interface{}
	Result   *shuttle.Result
	Error    error
}

// emitProgress sends a progress event if a callback is configured.
// This is a helper to avoid nil checks everywhere.
func emitProgress(ctx Context, stage ExecutionStage, progress int32, message string, toolName string) {
	if callback := ctx.ProgressCallback(); callback != nil {
		callback(ProgressEvent{
			Stage:     stage,
			Progress:  progress,
			Message:   message,
			ToolName:  toolName,
			Timestamp: time.Now(),
		})
	}
}

// emitProgressWithHITL sends a progress event with HITL request information.
func emitProgressWithHITL(ctx Context, stage ExecutionStage, progress int32, message string, toolName string, hitlInfo *HITLRequestInfo) {
	if callback := ctx.ProgressCallback(); callback != nil {
		callback(ProgressEvent{
			Stage:       stage,
			Progress:    progress,
			Message:     message,
			ToolName:    toolName,
			Timestamp:   time.Now(),
			HITLRequest: hitlInfo,
		})
	}
}

// extractHITLInfo extracts HITL request details from contact_human tool input.
// Returns partial info even if some fields are missing (graceful degradation).
func extractHITLInfo(input map[string]interface{}) *HITLRequestInfo {
	info := &HITLRequestInfo{
		Context: make(map[string]interface{}),
	}

	// Extract required fields with type assertions
	if question, ok := input["question"].(string); ok {
		info.Question = question
	}

	// Extract optional fields
	if requestType, ok := input["request_type"].(string); ok {
		info.RequestType = requestType
	} else {
		info.RequestType = "input" // default
	}

	if priority, ok := input["priority"].(string); ok {
		info.Priority = priority
	} else {
		info.Priority = "normal" // default
	}

	// Extract timeout (may be float64 from JSON)
	if timeoutSec, ok := input["timeout_seconds"].(float64); ok {
		info.Timeout = time.Duration(timeoutSec) * time.Second
	} else {
		info.Timeout = 5 * time.Minute // default
	}

	// Extract context map if present
	if contextMap, ok := input["context"].(map[string]interface{}); ok {
		info.Context = contextMap
	}

	// Note: RequestID is not available at this point (generated by contact_human tool)
	// It will be filled in by the tool execution result

	return info
}

// runConversationLoop executes the LLM-driven conversation loop.
// This implements the core agent behavior: LLM generates tool calls,
// we execute them, feed results back to LLM, repeat until completion.
func (a *Agent) runConversationLoop(ctx Context) (*Response, error) {
	// Start trace span
	var span *observability.Span
	if a.config.EnableTracing {
		_, span = ctx.Tracer().StartSpan(ctx, "agent.conversation_loop")
		defer ctx.Tracer().EndSpan(span)
	}

	session := ctx.Session()
	turnCount := 0
	toolExecutionCount := 0
	var allToolExecutions []ToolExecution

	// Debug: Print config values
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		fmt.Printf("\n=== CONVERSATION LOOP DEBUG ===\n")
		fmt.Printf("MaxTurns: %d\n", a.config.MaxTurns)
		fmt.Printf("MaxToolExecutions: %d\n", a.config.MaxToolExecutions)
		fmt.Printf("=== END DEBUG ===\n\n")
	}

	// Get available tools
	tools := a.tools.ListTools()

	// Emit pattern selection progress
	emitProgress(ctx, StagePatternSelection, 10, "Analyzing query and selecting patterns", "")

	// === PATTERN SELECTION INTEGRATION ===
	var selectedPattern *patterns.Pattern
	var patternConfidence float64

	// Get pattern config (use defaults if not set)
	patternConfig := a.config.PatternConfig
	if patternConfig == nil {
		patternConfig = DefaultPatternConfig()
	}

	// Only select patterns if enabled and prerequisites are met
	if patternConfig.Enabled && a.orchestrator != nil && session != nil && a.backend != nil {
		// Get most recent user message
		messages := session.GetMessages()
		var lastUserMessage string
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				lastUserMessage = messages[i].Content
				break
			}
		}

		if lastUserMessage != "" {
			// Start pattern selection span
			var patternSpan *observability.Span
			if a.config.EnableTracing && a.tracer != nil {
				_, patternSpan = a.tracer.StartSpan(ctx, "agent.pattern_selection")
				defer a.tracer.EndSpan(patternSpan)
			}

			// Build context data
			contextData := map[string]interface{}{
				"backend_type": a.backend.Name(),
				"session_id":   session.ID,
			}

			// Step 1: Classify intent
			intent, intentConf := a.orchestrator.ClassifyIntent(lastUserMessage, contextData)

			if patternSpan != nil {
				patternSpan.SetAttribute("intent.category", string(intent))
				patternSpan.SetAttribute("intent.confidence", fmt.Sprintf("%.2f", intentConf))
			}

			// Step 2: Recommend pattern (if intent confidence sufficient)
			if intent != patterns.IntentUnknown && intentConf > 0.3 {
				patternName, patternConf := a.orchestrator.RecommendPattern(lastUserMessage, intent)
				patternConfidence = patternConf

				if patternSpan != nil {
					patternSpan.SetAttribute("pattern.name", patternName)
					patternSpan.SetAttribute("pattern.confidence", fmt.Sprintf("%.2f", patternConf))
				}

				// Step 3: Load pattern if confidence threshold met
				if patternName != "" && patternConf >= patternConfig.MinConfidence {
					pattern, err := a.orchestrator.GetLibrary().Load(patternName)
					if err == nil {
						selectedPattern = pattern

						// Format and inject pattern
						formattedPattern := pattern.FormatForLLM()

						// Inject into segmented memory
						if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
							segMem.InjectPattern(formattedPattern, pattern.Name)

							if patternSpan != nil {
								tokenCount := a.tokenCounter.CountTokens(formattedPattern)
								patternSpan.SetAttribute("pattern.tokens", tokenCount)
								patternSpan.SetAttribute("pattern.injected", "true")
							}
						}

						// Record metrics
						if a.config.EnableTracing && a.tracer != nil {
							a.tracer.RecordMetric("patterns.recommended", 1.0, map[string]string{
								"pattern":    patternName,
								"intent":     string(intent),
								"confidence": fmt.Sprintf("%.0f", patternConf*100),
							})
						}
					} else if patternSpan != nil {
						patternSpan.RecordError(fmt.Errorf("pattern load failed: %w", err))
					}
				}
			}

			// Update progress with pattern info
			if selectedPattern != nil {
				emitProgress(ctx, StagePatternSelection, 15,
					fmt.Sprintf("Selected pattern: %s (%.0f%% confidence)",
						selectedPattern.Title, patternConfidence*100), "")
			}
		}
	}
	// === END PATTERN SELECTION ===

	// Conversation loop
	for turnCount < a.config.MaxTurns && toolExecutionCount < a.config.MaxToolExecutions {
		turnCount++
		turnStartTime := time.Now()

		// === FEATURE INTEGRATION: Token Budget Management ===
		// Check token budget and enforce compression if needed (segmented memory only)
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			budgetInfo := checkTokenBudget(segMem)

			// Log budget status if tracing enabled
			if a.config.EnableTracing && span != nil {
				span.SetAttribute("token_budget.current", budgetInfo.currentTokens)
				span.SetAttribute("token_budget.available", budgetInfo.availableTokens)
				span.SetAttribute("token_budget.usage_pct", budgetInfo.budgetPct)
				span.SetAttribute("token_budget.max_output", budgetInfo.maxOutputTokens)

				if budgetInfo.budgetPct > 70 {
					span.AddEvent("token_budget.warning", map[string]interface{}{
						"usage_pct": budgetInfo.budgetPct,
					})
				}
			}

			// Force compression at 85% threshold
			compressed, err := enforceTokenBudget(ctx, segMem, budgetInfo)
			if err != nil {
				return nil, fmt.Errorf("token budget enforcement failed: %w", err)
			}
			if compressed && a.config.EnableTracing && span != nil {
				span.AddEvent("memory.compressed", map[string]interface{}{
					"trigger": "budget_critical",
				})
			}
		}

		// Build messages for LLM (will use segmented memory if configured)
		messages := session.GetMessages()

		// === FEATURE INTEGRATION: Soft Reminders ===
		// Add reminders if approaching limits (non-intrusive, doesn't remove tools)
		// Thresholds: 75% of max (but minimum of 10 tools / 8 turns)
		if session.SegmentedMem != nil {
			// Check tool execution reminder
			toolReminder := buildSoftReminder(toolExecutionCount, a.config.MaxToolExecutions)
			// Check turn count reminder
			turnReminder := buildTurnReminder(turnCount, a.config.MaxTurns)

			// Combine reminders if both are active
			combinedReminder := toolReminder + turnReminder

			if combinedReminder != "" {
				// Append reminder to system message (if exists) or first user message
				if len(messages) > 0 && messages[0].Role == "system" {
					messages[0].Content += combinedReminder
				} else if len(messages) > 0 && messages[0].Role == "user" {
					messages[0].Content += combinedReminder
				}

				if a.config.EnableTracing && span != nil {
					span.AddEvent("soft_reminder.added", map[string]interface{}{
						"tool_count":        toolExecutionCount,
						"turn_count":        turnCount,
						"max_tools":         a.config.MaxToolExecutions,
						"max_turns":         a.config.MaxTurns,
						"tool_threshold":    int(float64(a.config.MaxToolExecutions) * 0.75),
						"turn_threshold":    int(float64(a.config.MaxTurns) * 0.75),
						"has_tool_reminder": toolReminder != "",
						"has_turn_reminder": turnReminder != "",
					})
				}
			}
		}

		// Emit LLM generation progress
		emitProgress(ctx, StageLLMGeneration, 20+int32(turnCount*10), fmt.Sprintf("Generating response (turn %d)", turnCount), "")

		// Call LLM
		llmResp, err := a.chatWithRetry(ctx, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// === OUTPUT TOKEN CIRCUIT BREAKER ===
		// Check if output token limit was hit and circuit breaker should trigger
		if failureTracker, ok := session.FailureTracker.(*consecutiveFailureTracker); ok && failureTracker != nil {
			if llmResp.StopReason == "max_tokens" {
				// Detect if tool calls are truncated (empty Input indicates mid-generation cutoff)
				hasEmptyToolCall := detectEmptyToolCall(llmResp.ToolCalls)

				// Record output token exhaustion
				exhaustionCount := failureTracker.recordOutputTokenExhaustion(hasEmptyToolCall)

				// Add tracing event
				if a.config.EnableTracing && span != nil {
					span.AddEvent("output_token.exhaustion", map[string]interface{}{
						"count":              exhaustionCount,
						"has_empty_toolcall": hasEmptyToolCall,
						"stop_reason":        llmResp.StopReason,
						"output_tokens":      llmResp.Usage.OutputTokens,
					})
				}

				// Check if circuit breaker threshold exceeded (default: 3 consecutive failures)
				if err := failureTracker.checkOutputTokenCircuitBreaker(3); err != nil {
					// Circuit breaker triggered - fail with actionable error message
					if a.config.EnableTracing && span != nil {
						span.AddEvent("output_token.circuit_breaker_triggered", map[string]interface{}{
							"exhaustion_count":   exhaustionCount,
							"has_empty_toolcall": hasEmptyToolCall,
						})
						span.RecordError(err)
					}
					return nil, fmt.Errorf("output token circuit breaker: %w", err)
				}
			} else {
				// Clear output token exhaustion counter on successful response
				failureTracker.clearOutputTokenExhaustion()

				if a.config.EnableTracing && span != nil {
					span.AddEvent("output_token.exhaustion_cleared", map[string]interface{}{
						"stop_reason": llmResp.StopReason,
					})
				}
			}
		}

		// If LLM returned text (no tool calls), we're done
		if len(llmResp.ToolCalls) == 0 {
			return &Response{
				Content:        llmResp.Content,
				Usage:          llmResp.Usage,
				ToolExecutions: allToolExecutions,
				Thinking:       llmResp.Thinking,
				Metadata: map[string]interface{}{
					"turns":           turnCount,
					"tool_executions": toolExecutionCount,
					"stop_reason":     llmResp.StopReason,
				},
			}, nil
		}

		// Add assistant message with tool calls to history FIRST (required by Anthropic API)
		assistantMsg := Message{
			Role:       "assistant",
			Content:    llmResp.Content,
			ToolCalls:  llmResp.ToolCalls,
			TokenCount: llmResp.Usage.TotalTokens,
			CostUSD:    llmResp.Usage.CostUSD,
			Timestamp:  time.Now(),
		}
		session.AddMessage(assistantMsg)

		// Persist assistant message with tool calls (critical for observability)
		if err := a.memory.PersistMessage(ctx, session.ID, assistantMsg); err != nil {
			// Log error but don't fail the request
			if span != nil {
				span.RecordError(err)
			}
		}

		// Execute tool calls
		for _, toolCall := range llmResp.ToolCalls {
			if toolExecutionCount >= a.config.MaxToolExecutions {
				break
			}
			toolExecutionCount++

			// Check if this is a HITL request (contact_human tool)
			if toolCall.Name == "contact_human" {
				// Extract HITL request details from tool input
				hitlInfo := extractHITLInfo(toolCall.Input)

				// Add instrumentation for HITL request
				if a.config.EnableTracing && span != nil {
					span.AddEvent("hitl.request_detected", map[string]interface{}{
						"question":     hitlInfo.Question,
						"request_type": hitlInfo.RequestType,
						"priority":     hitlInfo.Priority,
						"timeout":      hitlInfo.Timeout.String(),
					})
					span.SetAttribute("hitl.active", true)
					span.SetAttribute("hitl.question", hitlInfo.Question)
					span.SetAttribute("hitl.request_type", hitlInfo.RequestType)
					span.SetAttribute("hitl.priority", hitlInfo.Priority)
				}

				// Emit HITL-specific progress event
				emitProgressWithHITL(ctx, StageHumanInTheLoop, 50, "Waiting for human response", toolCall.Name, hitlInfo)
			} else {
				// Emit standard tool execution progress
				emitProgress(ctx, StageToolExecution, 50+int32(toolExecutionCount*5), fmt.Sprintf("Executing tool: %s", toolCall.Name), toolCall.Name)
			}

			// Execute tool with tracing
			var toolSpan *observability.Span
			if a.config.EnableTracing {
				_, toolSpan = ctx.Tracer().StartSpan(ctx, "agent.tool_execution")
				toolSpan.SetAttribute("tool_name", toolCall.Name)
			}

			// Execute with self-correction (circuit breaker + SQL correction)
			result, err := a.executeToolWithSelfCorrection(ctx, toolCall.Name, toolCall.Input, session.ID)

			// Add instrumentation for HITL completion
			if toolCall.Name == "contact_human" && a.config.EnableTracing && span != nil {
				if err != nil {
					span.AddEvent("hitl.request_failed", map[string]interface{}{
						"error": err.Error(),
					})
				} else if result != nil {
					// Extract response status from result
					status := "unknown"
					if result.Data != nil {
						if dataMap, ok := result.Data.(map[string]interface{}); ok {
							if s, ok := dataMap["status"].(string); ok {
								status = s
							}
						}
					}
					span.AddEvent("hitl.request_completed", map[string]interface{}{
						"status":            status,
						"execution_time_ms": result.ExecutionTimeMs,
					})
					span.SetAttribute("hitl.status", status)
				}
			}

			if a.config.EnableTracing && toolSpan != nil {
				// Determine success: both err must be nil AND result.Success must be true
				success := err == nil && (result == nil || result.Success)

				// Record errors from both Go error and result.Error
				if err != nil {
					toolSpan.RecordError(err)
				} else if result != nil && !result.Success && result.Error != nil {
					// MCP tools can fail without Go error - record result.Error
					toolSpan.RecordError(fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message))
					toolSpan.SetAttribute("error.code", result.Error.Code)
					toolSpan.SetAttribute("error.message", result.Error.Message)
				}

				toolSpan.SetAttribute("success", fmt.Sprintf("%t", success))
				if result != nil {
					toolSpan.SetAttribute("execution_time_ms", fmt.Sprintf("%d", result.ExecutionTimeMs))
				}
				ctx.Tracer().EndSpan(toolSpan)
			}

			// Record execution
			execution := ToolExecution{
				ToolName: toolCall.Name,
				Input:    toolCall.Input,
				Result:   result,
				Error:    err,
			}
			allToolExecutions = append(allToolExecutions, execution)

			// Persist tool execution
			if persistErr := a.memory.PersistToolExecution(ctx, session.ID, execution); persistErr != nil {
				// Log but don't fail
				if toolSpan != nil {
					toolSpan.RecordError(persistErr)
				}
			}

			// === FEATURE INTEGRATION: Consecutive Failure Tracking ===
			var escalationMsg string
			if failureTracker, ok := session.FailureTracker.(*consecutiveFailureTracker); ok && failureTracker != nil && session.SegmentedMem != nil {
				if err != nil {
					// Track failure
					errorType := extractErrorType(result)
					if errorType == "" && result != nil && result.Error != nil && result.Error.Message != "" {
						errorType = "execution_error"
					} else if errorType == "" {
						errorType = "unknown_error"
					}

					failureCount := failureTracker.record(toolCall.Name, toolCall.Input, errorType)
					escalationMsg = failureTracker.getEscalationMessage(failureCount, 2)

					if escalationMsg != "" && a.config.EnableTracing && span != nil {
						span.AddEvent("failure.escalated", map[string]interface{}{
							"tool":          toolCall.Name,
							"failure_count": failureCount,
						})
					}
				} else {
					// Clear failures on success
					failureTracker.clear(toolCall.Name, toolCall.Input)

					if a.config.EnableTracing && span != nil {
						span.AddEvent("failure.cleared", map[string]interface{}{
							"tool": toolCall.Name,
						})
					}
				}
			}

			// Format tool result with escalation if needed
			formattedResult := a.formatToolResult(ctx, session.ID, toolCall.Name, result, err)
			if escalationMsg != "" {
				formattedResult = formatToolResultWithEscalation(formattedResult, err, escalationMsg)
			}

			// Add tool result to conversation
			toolMsg := Message{
				Role:       "tool",
				Content:    formattedResult,
				ToolUseID:  toolCall.ID, // Store ID for Bedrock/Anthropic format conversion
				ToolResult: result,
				Timestamp:  time.Now(),
			}
			session.AddMessage(toolMsg)

			// Persist message
			if persistErr := a.memory.PersistMessage(ctx, session.ID, toolMsg); persistErr != nil {
				// Log but don't fail
				if toolSpan != nil {
					toolSpan.RecordError(persistErr)
				}
			}

			// === AUTOMATIC FINDING EXTRACTION ===
			// After each tool execution, check if we should extract findings
			if a.enableFindingExtraction {
				a.toolExecutionsSinceExtraction++
				if a.toolExecutionsSinceExtraction >= a.extractionCadence {
					// Run extraction in background (non-blocking)
					go a.extractFindingsAsync(ctx, session.ID)
					a.toolExecutionsSinceExtraction = 0
				}
			}
		}

		// === PATTERN EFFECTIVENESS TRACKING ===
		// Track pattern usage after tool execution completes
		if selectedPattern != nil && patternConfig.EnableTracking && len(allToolExecutions) > 0 {
			// Get the most recent tool execution for this turn
			lastExecution := allToolExecutions[len(allToolExecutions)-1]

			// Determine success based on execution result
			success := lastExecution.Error == nil && (lastExecution.Result == nil || lastExecution.Result.Success)

			// Extract error type if failed
			errorType := ""
			if !success {
				if lastExecution.Error != nil {
					errorType = "execution_error"
				} else if lastExecution.Result != nil && lastExecution.Result.Error != nil {
					errorType = lastExecution.Result.Error.Code
				}
			}

			// Calculate cost (rough estimate based on LLM usage)
			costUSD := 0.0
			if llmResp != nil && llmResp.Usage.InputTokens > 0 {
				// Anthropic pricing (approximate): $3/million input, $15/million output
				costUSD = float64(llmResp.Usage.InputTokens)*0.000003 +
					float64(llmResp.Usage.OutputTokens)*0.000015
			}

			// Calculate latency from turn start
			latency := time.Since(turnStartTime)

			// Extract LLM provider and model info
			llmProvider := "anthropic" // Default
			llmModel := "claude-sonnet-4-5"
			// TODO: Extract actual provider/model from LLM provider interface when available

			// Record pattern usage for effectiveness tracking
			a.orchestrator.RecordPatternUsage(
				ctx,
				selectedPattern.Name,
				a.config.Name,
				success,
				costUSD,
				latency,
				errorType,
				llmProvider,
				llmModel,
			)
		}
		// === END PATTERN EFFECTIVENESS TRACKING ===
	}

	// If we hit max turns/executions, make one final LLM call to synthesize results
	// This ensures the agent provides meaningful output instead of a generic error message
	emitProgress(ctx, StageSynthesis, 90, "Synthesizing tool execution results", "")

	// Add a synthesis request to the conversation
	// Include explicit format instructions since they may have been compressed in context
	synthesisPrompt := "Based on all the tool executions and data you've gathered above, provide your complete response now. Follow the exact output format specified in your system instructions."
	synthesisMsg := Message{
		Role:      "user",
		Content:   synthesisPrompt,
		Timestamp: time.Now(),
	}
	session.AddMessage(synthesisMsg)

	// Persist synthesis message for observability
	if err := a.memory.PersistMessage(ctx, session.ID, synthesisMsg); err != nil {
		// Log error but don't fail the request
		if span != nil {
			span.RecordError(err)
		}
	}

	// Make final LLM call WITHOUT tools to force synthesis
	finalResp, err := a.chatWithRetry(ctx, session.GetMessages(), nil)
	if err != nil {
		// Only fall back to guidance message if synthesis fails
		maxTurnsMessage := a.getGuidanceMessage("max_turns_reached", nil)
		return &Response{
			Content:        maxTurnsMessage,
			Usage:          Usage{},
			ToolExecutions: allToolExecutions,
			Metadata: map[string]interface{}{
				"turns":           turnCount,
				"tool_executions": toolExecutionCount,
				"max_turns_hit":   turnCount >= a.config.MaxTurns,
				"max_exec_hit":    toolExecutionCount >= a.config.MaxToolExecutions,
				"synthesis_error": err.Error(),
			},
		}, nil
	}

	// Return synthesized response
	return &Response{
		Content:        finalResp.Content,
		Usage:          finalResp.Usage,
		ToolExecutions: allToolExecutions,
		Thinking:       finalResp.Thinking,
		Metadata: map[string]interface{}{
			"turns":           turnCount + 1, // Include synthesis turn
			"tool_executions": toolExecutionCount,
			"max_turns_hit":   turnCount >= a.config.MaxTurns,
			"max_exec_hit":    toolExecutionCount >= a.config.MaxToolExecutions,
			"synthesized":     true,
		},
	}, nil
}

// contextWithValue wraps a Context to add a key-value pair while preserving the Context interface.
type contextWithValue struct {
	Context
	key interface{}
	val interface{}
}

// Value returns the value associated with this context for key, or delegates to parent.
func (c *contextWithValue) Value(key interface{}) interface{} {
	if key == c.key {
		return c.val
	}
	return c.Context.Value(key)
}

// executeToolWithSelfCorrection wraps tool execution with optional circuit breaker.
// If circuit breaker is enabled, provides failure isolation for tools.
// If guardrails are enabled, tracks errors for error analysis.
func (a *Agent) executeToolWithSelfCorrection(ctx Context, toolName string, input map[string]interface{}, sessionID string) (*shuttle.Result, error) {
	var result *shuttle.Result
	var err error

	// CRITICAL FIX: Add session_id and agent_id to context for tools that need it
	// Tools like recall_conversation, search_conversation, clear_recalled_context, and agent_management
	// expect session_id and agent_id to be available in context
	// Wrap the context to add both while preserving the Context interface
	ctxWithSession := &contextWithValue{
		Context: ctx,
		key:     "session_id",
		val:     sessionID,
	}
	ctxWithAgent := &contextWithValue{
		Context: ctxWithSession,
		key:     "agent_id",
		val:     a.config.Name,
	}

	// Execute with circuit breaker if enabled
	if a.circuitBreakers != nil {
		breaker := a.circuitBreakers.GetBreaker(toolName)
		cbErr := breaker.Execute(func() error {
			result, err = a.executor.Execute(ctxWithAgent, toolName, input)
			return err
		})

		// If circuit breaker itself failed (breaker open), return that error
		if cbErr != nil && err == nil {
			return nil, fmt.Errorf("circuit breaker open for tool %s: %w", toolName, cbErr)
		}
	} else {
		// No circuit breaker - execute directly
		result, err = a.executor.Execute(ctxWithAgent, toolName, input)
	}

	// If execution succeeded and guardrails enabled, clear error record
	if err == nil && result != nil && result.Success && a.guardrails != nil {
		a.guardrails.ClearErrorRecord(sessionID)
	}

	// If execution failed and guardrails enabled, track error
	if (err != nil || (result != nil && !result.Success)) && a.guardrails != nil {
		errorAnalysis := a.analyzeError(result, err)
		query := ""
		if sql, ok := input["sql"].(string); ok {
			query = sql
		}
		_ = a.guardrails.HandleErrorWithAnalysis(ctx, sessionID, query, errorAnalysis)
	}

	return result, err
}

// analyzeError converts execution error into ErrorAnalysisInfo for self-correction.
func (a *Agent) analyzeError(result *shuttle.Result, err error) *fabric.ErrorAnalysisInfo {
	if err != nil {
		return &fabric.ErrorAnalysisInfo{
			ErrorType:   "execution_error",
			Summary:     err.Error(),
			Suggestions: []string{"Check tool input parameters", "Verify tool is properly configured"},
		}
	}

	if result == nil {
		return &fabric.ErrorAnalysisInfo{
			ErrorType:   "unknown_error",
			Summary:     "Tool execution returned nil result",
			Suggestions: []string{},
		}
	}

	if result.Error == nil {
		return &fabric.ErrorAnalysisInfo{
			ErrorType:   "unknown_error",
			Summary:     "Tool execution failed without error details",
			Suggestions: []string{},
		}
	}

	// Use result.Error for detailed analysis
	errorMsg := result.Error.Message
	errorCode := result.Error.Code

	// Backend should classify errors, but we provide a fallback
	errorType := fabric.InferErrorType(errorCode, errorMsg)

	return &fabric.ErrorAnalysisInfo{
		ErrorType:   errorType,
		Summary:     fmt.Sprintf("[%s] %s", errorCode, errorMsg),
		Suggestions: []string{}, // Backend-specific suggestions would go here
	}
}

// formatToolResult formats a tool execution result for inclusion in conversation.
// Uses the error submission channel pattern: stores full errors in SQLite and provides
// error references to the LLM, allowing the agent to fetch full details on demand.
func (a *Agent) formatToolResult(ctx Context, sessionID string, toolName string, result *shuttle.Result, err error) string {
	// Handle execution errors (tool didn't run)
	if err != nil {
		errMsg := fmt.Sprintf("%v", err)
		summary := extractFirstLine(errMsg, 100)

		// Store full error if error store is available
		if a.errorStore != nil {
			errorID, storeErr := a.errorStore.Store(ctx, &StoredError{
				SessionID:    sessionID,
				ToolName:     toolName,
				RawError:     json.RawMessage(fmt.Sprintf(`{"message": %q}`, errMsg)),
				ShortSummary: summary,
			})

			if storeErr == nil {
				// Progressive disclosure: Register get_error_details tool after first error
				if !a.tools.IsRegistered("get_error_details") {
					a.tools.Register(NewGetErrorDetailsTool(a.errorStore))
				}

				// Successfully stored - return reference
				return fmt.Sprintf(`Tool '%s' failed: %s
[Error ID: %s]
📋 Use get_error_details("%s") for complete error information`,
					toolName,
					summary,
					errorID,
					errorID)
			}
			// If storage failed, fall through to truncation
		}

		// Fallback: truncate if error store unavailable or storage failed
		return fmt.Sprintf("Error: %s", truncateErrorMessage(errMsg, 500))
	}

	// Handle tool execution errors (tool ran but failed)
	if !result.Success {
		if result.Error != nil {
			// Marshal raw error preserving original structure
			rawError, _ := json.Marshal(result.Error)
			summary := extractErrorSummary(result.Error)

			// Store full error if error store is available
			if a.errorStore != nil {
				errorID, storeErr := a.errorStore.Store(ctx, &StoredError{
					SessionID:    sessionID,
					ToolName:     toolName,
					RawError:     rawError,
					ShortSummary: summary,
				})

				if storeErr == nil {
					// Progressive disclosure: Register get_error_details tool after first error
					if !a.tools.IsRegistered("get_error_details") {
						a.tools.Register(NewGetErrorDetailsTool(a.errorStore))
					}

					// Successfully stored - return reference
					return fmt.Sprintf(`Tool '%s' failed: %s
[Error ID: %s]
📋 Use get_error_details tool with error_id="%s" for complete error information`,
						toolName,
						summary,
						errorID,
						errorID)
				}
				// If storage failed, fall through to truncation
			}

			// Fallback: truncate if error store unavailable or storage failed
			truncatedMsg := truncateErrorMessage(result.Error.Message, 500)
			return fmt.Sprintf("Tool error: %s - %s", result.Error.Code, truncatedMsg)
		}
		return "Tool execution failed"
	}

	// CRITICAL FIX: Pin DataReferences returned by tools (like tool_search)
	// Tools may create their own references in SharedMemoryStore, and we need to pin them
	// to prevent LRU eviction while the session is active
	if result.DataReference != nil && a.refTracker != nil {
		a.refTracker.PinForSession(sessionID, result.DataReference.Id)
	}

	// Format successful result with smart truncation
	if result.Data != nil {
		dataStr := fmt.Sprintf("%v", result.Data)

		// Check if result is large enough to warrant reference storage
		tokenCount := a.tokenCounter.CountTokens(dataStr)
		const maxInlineTokens = 1000 // Keep small results inline, store large ones

		// Skip reference creation if MCP tool already truncated (#1: Stop Double-Truncation)
		// MCP tools handle truncation intelligently at 4096 bytes, creating another
		// reference would fail since the reference system expects full data
		if result.Metadata != nil {
			if truncated, ok := result.Metadata["truncated"].(bool); ok && truncated {
				// MCP tool already handled truncation - return as-is
				return dataStr
			}
		}

		// CRITICAL: Don't wrap progressive disclosure tool outputs - they already retrieve data from shared memory
		// Wrapping them again creates infinite recursion: query_tool_result → DataRef A → query_tool_result(A) → DataRef B → ...
		// Excluded tools: get_tool_result (metadata), query_tool_result (actual data retrieval)
		if tokenCount > maxInlineTokens && toolName != "get_tool_result" && toolName != "query_tool_result" {
			// Large result - store reference and provide summary

			// Try shared memory first (fastest, in-process)
			if a.sharedMemory != nil {
				// Generate unique ID for this result
				refID := fmt.Sprintf("ref_%s_%d", toolName, time.Now().UnixNano())

				// Determine content type
				contentType := "text/plain"
				if result.Metadata != nil {
					if ct, ok := result.Metadata["content_type"].(string); ok {
						contentType = ct
					}
				}

				// Convert result metadata to string map for storage
				storageMeta := make(map[string]string)
				if result.Metadata != nil {
					for k, v := range result.Metadata {
						storageMeta[k] = fmt.Sprintf("%v", v)
					}
				}
				storageMeta["tool_name"] = toolName
				storageMeta["session_id"] = sessionID

				// Store in shared memory
				dataRef, storeErr := a.sharedMemory.Store(refID, []byte(dataStr), contentType, storageMeta)
				if storeErr == nil {
					// Pin reference for session (auto-cleanup on session end)
					// This prevents LRU eviction while session is active and ensures cleanup when session ends
					if a.refTracker != nil {
						a.refTracker.PinForSession(sessionID, dataRef.Id)
					}

					// Progressive disclosure: Register query_tool_result after first large result
					if !a.tools.IsRegistered("query_tool_result") && a.sqlResultStore != nil {
						a.tools.Register(NewQueryToolResultTool(a.sqlResultStore, a.sharedMemory))
					}

					// Get metadata to create rich inline summary (eliminates need for get_tool_result call)
					meta, metaErr := a.sharedMemory.GetMetadata(dataRef)
					if metaErr == nil && meta != nil {
						// Format rich metadata inline (same as executor.go does)
						richSummary := formatAgentSharedMemoryResult(meta, dataRef.Id, toolName)
						return richSummary
					}

					// Fallback to basic summary if metadata unavailable
					summary := extractDataSummary(result.Data, result.Metadata)
					return fmt.Sprintf(`✓ %s

%s

📎 Full result stored in memory (ID: %s)
💡 Use get_tool_result(reference_id="%s") to retrieve complete data

Token efficiency: %d tokens → ~50 tokens (%.1f%% reduction)`,
						toolName,
						summary,
						dataRef.Id,
						dataRef.Id,
						tokenCount,
						(float64(tokenCount-50)/float64(tokenCount))*100)
				}
			}

			// Fallback: Smart truncation with structure preservation
			return truncateWithStructure(dataStr, 800, result.Metadata)
		}

		// Small result - return inline
		return dataStr
	}

	return "Success"
}

// formatAgentSharedMemoryResult creates a rich inline summary with metadata for agent context.
// Similar to executor.formatSharedMemoryResultSummary but includes tool name context.
// This eliminates the need for a separate get_tool_result call - agents get all context immediately.
func formatAgentSharedMemoryResult(meta *storage.DataMetadata, id string, toolName string) string {
	var summary strings.Builder

	// Header with tool name, data type and size
	summary.WriteString(fmt.Sprintf("✓ %s completed: Large %s stored in memory (%d bytes, ~%d tokens)\n\n",
		toolName, meta.DataType, meta.SizeBytes, meta.EstimatedTokens))

	// Preview section
	if meta.Preview != nil && (len(meta.Preview.First5) > 0 || len(meta.Preview.Last5) > 0) {
		summary.WriteString("📋 Preview:\n")
		if len(meta.Preview.First5) > 0 {
			previewJSON, _ := json.MarshalIndent(meta.Preview.First5, "", "  ")
			summary.WriteString(fmt.Sprintf("First 5 items:\n%s\n", string(previewJSON)))
		}
		if len(meta.Preview.Last5) > 0 && meta.DataType == "json_array" {
			previewJSON, _ := json.MarshalIndent(meta.Preview.Last5, "", "  ")
			summary.WriteString(fmt.Sprintf("\nLast 5 items:\n%s\n", string(previewJSON)))
		}
		summary.WriteString("\n")
	}

	// Schema section (if available)
	if meta.Schema != nil {
		switch meta.DataType {
		case "json_object":
			if len(meta.Schema.Fields) > 0 {
				fieldNames := make([]string, 0, len(meta.Schema.Fields))
				for _, field := range meta.Schema.Fields {
					fieldNames = append(fieldNames, fmt.Sprintf("%s (%s)", field.Name, field.Type))
				}
				summary.WriteString(fmt.Sprintf("📊 Schema: %d fields\n%s\n\n",
					len(meta.Schema.Fields), strings.Join(fieldNames, ", ")))
			}
		case "json_array":
			summary.WriteString(fmt.Sprintf("📊 Array: %d items\n", meta.Schema.ItemCount))
			if len(meta.Schema.Fields) > 0 {
				fieldNames := make([]string, 0, len(meta.Schema.Fields))
				for _, field := range meta.Schema.Fields {
					fieldNames = append(fieldNames, fmt.Sprintf("%s (%s)", field.Name, field.Type))
				}
				summary.WriteString(fmt.Sprintf("Item schema: %s\n\n", strings.Join(fieldNames, ", ")))
			}
		case "text":
			summary.WriteString(fmt.Sprintf("📊 Text: %d lines\n\n", meta.Schema.ItemCount))
		}
	}

	// Retrieval hints - how to access this data
	summary.WriteString("💡 How to retrieve:\n")
	switch meta.DataType {
	case "json_object":
		summary.WriteString(fmt.Sprintf("⚠️ This json_object is too large (%d bytes) for direct retrieval\n", meta.SizeBytes))
		summary.WriteString("Use the preview and schema above to understand the structure\n")
		if meta.Schema != nil && len(meta.Schema.Fields) > 0 {
			summary.WriteString("Consider which specific fields you need from the object\n")
		}

	case "json_array":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', offset=0, limit=100)\n", id))
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...')\n", id))
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			summary.WriteString("⚠️ Large dataset - use filtering to avoid context overload\n")
		}

	case "text":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', offset=0, limit=100)\n", id))
		if meta.Schema != nil && meta.Schema.ItemCount > 1000 {
			summary.WriteString(fmt.Sprintf("⚠️ Large file (%d lines) - paginate to avoid loading all at once\n", meta.Schema.ItemCount))
		}

	case "csv":
		summary.WriteString(fmt.Sprintf("query_tool_result(reference_id='%s', sql='SELECT * FROM results WHERE ...')\n", id))
		summary.WriteString("💡 CSV auto-converts to queryable SQLite table\n")
	}

	return summary.String()
}

func extractDataSummary(data interface{}, metadata map[string]interface{}) string {
	var parts []string

	// Check metadata for structured info (common in database tools)
	if metadata != nil {
		if rows, ok := metadata["rows"].(int); ok {
			parts = append(parts, fmt.Sprintf("📊 %d rows returned", rows))
		} else if rows, ok := metadata["row_count"].(int); ok {
			parts = append(parts, fmt.Sprintf("📊 %d rows returned", rows))
		}

		if cols, ok := metadata["columns"].([]interface{}); ok {
			parts = append(parts, fmt.Sprintf("📋 %d columns", len(cols)))
		} else if cols, ok := metadata["column_count"].(int); ok {
			parts = append(parts, fmt.Sprintf("📋 %d columns", cols))
		}

		if execTime, ok := metadata["execution_time_ms"].(int64); ok {
			parts = append(parts, fmt.Sprintf("⏱️  %dms", execTime))
		}
	}

	// Try to extract structural info from data itself
	switch v := data.(type) {
	case map[string]interface{}:
		if rows, ok := v["rows"].([]interface{}); ok {
			parts = append(parts, fmt.Sprintf("📊 %d rows", len(rows)))
		}
		if cols, ok := v["columns"].([]interface{}); ok {
			parts = append(parts, fmt.Sprintf("📋 Columns: %v", cols))
		}
	case []interface{}:
		parts = append(parts, fmt.Sprintf("📊 %d items returned", len(v)))
	case string:
		if len(v) > 100 {
			parts = append(parts, fmt.Sprintf("📄 Text result (%d chars)", len(v)))
		}
	}

	if len(parts) == 0 {
		return "Result available (details stored)"
	}

	return strings.Join(parts, " • ")
}

// truncateWithStructure intelligently truncates data while preserving structure.
// For JSON/tabular data, shows sample rows + summary instead of raw truncation.
func truncateWithStructure(dataStr string, maxChars int, metadata map[string]interface{}) string {
	if len(dataStr) <= maxChars {
		return dataStr
	}

	// Try to parse as JSON for smart truncation
	var jsonData interface{}
	if err := json.Unmarshal([]byte(dataStr), &jsonData); err == nil {
		// Successfully parsed as JSON
		switch v := jsonData.(type) {
		case map[string]interface{}:
			// Likely table data with rows/columns
			if rows, ok := v["rows"].([]interface{}); ok {
				sampleSize := 3
				if len(rows) > sampleSize {
					v["rows"] = rows[:sampleSize]
					sampleJSON, _ := json.MarshalIndent(v, "", "  ")
					return fmt.Sprintf("%s\n\n... [showing %d of %d rows. Result truncated due to size.]",
						string(sampleJSON), sampleSize, len(rows))
				}
			}
		case []interface{}:
			// Array of items
			sampleSize := 5
			if len(v) > sampleSize {
				sample, _ := json.MarshalIndent(v[:sampleSize], "", "  ")
				return fmt.Sprintf("%s\n\n... [showing %d of %d items]", string(sample), sampleSize, len(v))
			}
		}
	}

	// Fallback: Simple truncation at clean break point
	truncateAt := maxChars
	if idx := strings.LastIndex(dataStr[:maxChars], "\n"); idx > 0 {
		truncateAt = idx
	}

	return dataStr[:truncateAt] + fmt.Sprintf("\n\n... [truncated %d → %d chars, %.1f%% reduction]",
		len(dataStr), truncateAt, (float64(len(dataStr)-truncateAt)/float64(len(dataStr)))*100)
}

// truncateErrorMessage truncates error messages to a maximum length while preserving readability.
// This prevents massive Go stack traces from MCP tools from breaking LLM API calls.
func truncateErrorMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}

	// Try to find a clean break point (newline) near the limit
	// This helps preserve the first line of multi-line errors
	truncateAt := maxLen
	for i := 0; i < maxLen && i < len(msg); i++ {
		if msg[i] == '\n' {
			// Found a newline within limit - use it as break point
			truncateAt = i
			break
		}
	}

	return msg[:truncateAt] + fmt.Sprintf("... [truncated %d chars]", len(msg)-truncateAt)
}

// GetSession retrieves a session by ID.
func (a *Agent) GetSession(sessionID string) (*Session, bool) {
	return a.memory.GetSession(sessionID)
}

// ListSessions returns all active sessions.
func (a *Agent) ListSessions() []*Session {
	return a.memory.ListSessions()
}

// DeleteSession removes a session.
func (a *Agent) DeleteSession(sessionID string) {
	a.memory.DeleteSession(sessionID)
}

// cleanupSessionReferences releases all shared memory references for a session.
// Called automatically when sessions are deleted (via cleanup hook).
// This prevents reference leaks by decrementing RefCount on all pinned references.
func (a *Agent) cleanupSessionReferences(ctx context.Context, sessionID string) {
	if a.refTracker == nil {
		return
	}

	// Start Hawk span for observability
	_, span := a.tracer.StartSpan(ctx, "agent.cleanup_session_references")
	defer a.tracer.EndSpan(span)

	span.SetAttribute("session_id", sessionID)

	// Release all references for this session
	releasedCount := a.refTracker.UnpinSession(sessionID)

	// Record metrics
	span.SetAttribute("references_released", fmt.Sprintf("%d", releasedCount))

	// Log if references were released
	if releasedCount > 0 {
		span.SetAttribute("status", "cleaned")
	} else {
		span.SetAttribute("status", "no_refs")
	}
}

// CreateSession creates a new session without sending a message to the LLM.
// Use this for session initialization; use Chat() for actual conversations.
func (a *Agent) CreateSession(sessionID string) *Session {
	// Use GetOrCreateSessionWithAgent to properly set agent_id in session metadata
	// This is critical for ReferenceStore namespacing and workflow sub-agent communication
	return a.memory.GetOrCreateSessionWithAgent(sessionID, a.config.Name, "")
}

// RegisteredTools returns all registered tools.
func (a *Agent) RegisteredTools() []shuttle.Tool {
	return a.tools.ListTools()
}

// RegisteredToolsByBackend returns all tools registered for a specific backend.
// Pass empty string to get backend-agnostic tools.
func (a *Agent) RegisteredToolsByBackend(backend string) []shuttle.Tool {
	return a.tools.ListByBackend(backend)
}

// GetGuardrails returns the guardrail engine for pre-flight validation (may be nil if not enabled).
func (a *Agent) GetGuardrails() *fabric.GuardrailEngine {
	return a.guardrails
}

// GetCircuitBreakers returns the circuit breaker manager for failure isolation (may be nil if not enabled).
func (a *Agent) GetCircuitBreakers() *fabric.CircuitBreakerManager {
	return a.circuitBreakers
}

// GetOrchestrator returns the pattern orchestrator for intent classification.
func (a *Agent) GetOrchestrator() *patterns.Orchestrator {
	return a.orchestrator
}

// GetLLMProviderName returns the name of the LLM provider (e.g., "anthropic", "bedrock", "ollama").
func (a *Agent) GetLLMProviderName() string {
	if a.llm == nil {
		return ""
	}
	return a.llm.Name()
}

// GetLLMModel returns the model identifier (e.g., "claude-3-5-sonnet-20241022").
func (a *Agent) GetLLMModel() string {
	if a.llm == nil {
		return ""
	}
	return a.llm.Model()
}

// SetLLMProvider switches the LLM provider for this agent.
// This allows mid-session model switching while preserving conversation context.
// The new provider will be used for all future LLM calls in all sessions.
func (a *Agent) SetLLMProvider(llm LLMProvider) {
	a.llm = llm
	// Also update memory's LLM provider for compression operations
	if a.memory != nil {
		a.memory.SetLLMProvider(llm)
	}
}

// SetSharedMemory configures shared memory for this agent.
// This injects the shared memory store into:
// - The agent itself (for formatToolResult to store large results)
// - All existing sessions' segmented memory
// - The tool executor for automatic large result handling
// - Future sessions created by this agent
// - Re-registers GetToolResultTool with the new store
func (a *Agent) SetSharedMemory(sharedMemory *storage.SharedMemoryStore) {
	// Set on agent itself (CRITICAL: used by formatToolResult and GetToolResultTool registration)
	a.sharedMemory = sharedMemory

	// Inject into tool executor
	if a.executor != nil {
		a.executor.SetSharedMemory(sharedMemory, storage.DefaultSharedMemoryThreshold)
	}

	// Inject into memory manager (which handles all sessions)
	if a.memory != nil {
		a.memory.SetSharedMemory(sharedMemory)
	}

	// EXPERIMENT: get_tool_result removed - inline metadata makes it unnecessary
	// Re-register GetToolResultTool with the new store
	// This ensures the tool uses the correct store instance for retrievals
	// v1.0.1: Pass both memory and SQL stores (SQL store passed as nil here, configured separately)
	// if sharedMemory != nil && a.tools != nil {
	// 	a.tools.Register(NewGetToolResultTool(sharedMemory, a.sqlResultStore))
	// }

	// Re-register QueryToolResultTool with the new shared memory instance if it was already registered
	// This fixes the bug where query_tool_result was using an old singleton store while
	// tool_search was storing data in the new multi-agent server's shared memory
	// Note: With progressive disclosure, this tool is only registered after first large result
	if sharedMemory != nil && a.sqlResultStore != nil && a.tools != nil {
		if a.tools.IsRegistered("query_tool_result") {
			// Re-register with new shared memory instance
			a.tools.Register(NewQueryToolResultTool(a.sqlResultStore, sharedMemory))
		}
	}

	// Update reference tracker with new store
	if sharedMemory != nil {
		a.refTracker = storage.NewSessionReferenceTracker(sharedMemory)
	}
}

// SetSQLResultStore configures SQL result store for this agent.
// This enables queryable storage for large SQL results, preventing context blowout.
func (a *Agent) SetSQLResultStore(sqlStore *storage.SQLResultStore) {
	// Store reference for later use
	a.sqlResultStore = sqlStore

	// Inject into tool executor so SQL results go to queryable tables
	if a.executor != nil {
		a.executor.SetSQLResultStore(sqlStore)
	}

	// Register query_tool_result tool if not already registered
	// v1.0.1: Pass both SQL and memory stores
	if sqlStore != nil && a.tools != nil {
		a.tools.Register(NewQueryToolResultTool(sqlStore, a.sharedMemory))
	}
}

// SetReferenceStore configures the reference store for inter-agent communication.
// This enables Send/Receive methods for agent-to-agent messaging.
func (a *Agent) SetReferenceStore(store communication.ReferenceStore) {
	a.refStore = store
}

// SetCommunicationPolicy configures the communication policy manager.
// This determines when to use references vs values in inter-agent communication.
func (a *Agent) SetCommunicationPolicy(policy *communication.PolicyManager) {
	a.commPolicy = policy
}

// truncateString truncates a string to maxLen characters with ellipsis.
// Used for span attributes to avoid huge trace payloads.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// WithReferenceStore enables inter-agent communication via reference store.
// When set, agents can send/receive messages using value or reference semantics.
func WithReferenceStore(store communication.ReferenceStore) Option {
	return func(a *Agent) {
		a.refStore = store
	}
}

// WithCommunicationPolicy sets the policy for determining reference vs value communication.
func WithCommunicationPolicy(policy *communication.PolicyManager) Option {
	return func(a *Agent) {
		a.commPolicy = policy
	}
}
