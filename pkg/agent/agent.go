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
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/metaagent/learning"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/skills"
	skillbinding "github.com/teradata-labs/loom/pkg/skills/binding"
	"github.com/teradata-labs/loom/pkg/skills/discovery"
	"github.com/teradata-labs/loom/pkg/skills/hygiene"
	skilltasks "github.com/teradata-labs/loom/pkg/skills/tasks"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
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
	// Generate unique ID for this agent instance
	agentID := uuid.New().String()

	a := &Agent{
		id:           agentID,
		backend:      backend,
		llm:          llmProvider,
		tools:        shuttle.NewRegistry(),
		memory:       NewMemory(),
		config:       DefaultConfig(),
		tracer:       observability.NewNoOpTracer(),
		prompts:      nil, // Will be set via options
		tokenCounter: GetTokenCounter(),

		sessionToolLedger: make(map[string]map[string]bool),
		scopedToolNames:   make(map[string]bool),
	}

	// Mutation-debug carrier: its closures read this agent's switch and turn
	// source at log time, so it is safe to wire before options finalize config.
	a.ctxDebug = a.newContextDebug()

	// Default shared memory threshold: 16384 bytes (storage.DefaultSharedMemoryThreshold —
	// the one threshold value of HLD §5.1).
	a.sharedMemoryThreshold = int64(storage.DefaultSharedMemoryThreshold)

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

	// Initialize automatic graph memory extraction if graph memory is enabled.
	if a.graphMemoryStore != nil && a.graphMemoryConfig != nil &&
		a.graphMemoryConfig.Enabled && a.graphMemoryConfig.EnableExtraction {
		a.enableGraphMemoryExtraction = true
		a.graphExtractionCadence = int(a.graphMemoryConfig.ExtractionCadence)
		if a.graphExtractionCadence <= 0 {
			a.graphExtractionCadence = 5
		}
		a.graphToolExecutionsSinceExtraction = 0

		// Conversation-turn-based extraction (fires on LLM responses, not just tool use).
		a.graphConversationExtractionCadence = int(a.graphMemoryConfig.ConversationExtractionCadence)
		a.graphTurnsSinceExtraction = 0
	}

	// Initialize pattern orchestrator
	patternLibrary := patterns.NewLibrary(nil, a.config.PatternsDir)
	a.orchestrator = patterns.NewOrchestrator(patternLibrary)

	// Initialize LLM classifier if configured
	// Use classifier role LLM if available, otherwise fall back to main LLM
	if a.config.PatternConfig.UseLLMClassifier && llmProvider != nil {
		classifierProvider := llmProvider
		if a.classifierLLM != nil {
			classifierProvider = a.classifierLLM
		}
		llmClassifierConfig := patterns.DefaultLLMClassifierConfig(classifierProvider)
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
	// Context is threaded through for proper RLS user_id propagation in PostgreSQL.
	a.memory.SetSystemPromptFunc(func(ctx context.Context) string {
		return a.getSystemPrompt(ctx)
	})

	// Set context limits for memory (if configured)
	if a.config.MaxContextTokens > 0 || a.config.ReservedOutputTokens > 0 {
		a.memory.SetContextLimits(a.config.MaxContextTokens, a.config.ReservedOutputTokens)
	}

	// K — protected newest user turns (HLD §5.1, §9).
	if a.config.ProtectedRecentTurns > 0 {
		a.memory.SetProtectedRecentTurns(a.config.ProtectedRecentTurns)
	}

	// Fold deactivates every skill whose manage_skills load pair lies inside
	// the folded region (HLD §4.5) — wired through the skills orchestrator's
	// existing deactivation path plus the per-session tool ledger.
	a.memory.SetSkillDeactivationHook(a.deactivateSkillForFold)

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
		// Use dedicated compressor LLM if available, otherwise main LLM
		if a.compressorLLM != nil {
			a.memory.SetLLMProvider(a.compressorLLM)
		} else {
			a.memory.SetLLMProvider(a.llm)
		}

		// Default-on memory compression: wire an LLM-backed compressor so L2
		// compaction preserves decisions and approvals-with-scope via the
		// registry-sourced compaction prompt. Requires both an LLM (dedicated
		// compressor LLM preferred) and a prompt registry; without either, the
		// heuristic summariser remains the fallback.
		compressorLLM := a.llm
		if a.compressorLLM != nil {
			compressorLLM = a.compressorLLM
		}
		if compressorLLM != nil && a.prompts != nil {
			caller := newPromptRegistryCompressor(compressorLLM, a.prompts)
			a.memory.SetCompressor(NewLLMCompressor(caller))
		}
	}

	// Set shared memory on executor so large tool results are stored in the same store
	// that GetToolResultTool retrieves from (fixes tool reference loop bug)
	if a.executor != nil && a.sharedMemory != nil {
		threshold := int64(storage.DefaultSharedMemoryThreshold)
		if a.sharedMemoryThreshold >= 0 {
			threshold = a.sharedMemoryThreshold
		}
		a.executor.SetSharedMemory(a.sharedMemory, threshold)
	}

	// The findings channel is retired: neither the record_finding tool nor automatic
	// extraction exists. Durable notes belong in the conversation or in the stores.

	// shell_execute is no longer auto-registered in NewAgent
	// Agents that need shell_execute must explicitly list it in config.Tools.Builtin
	// Registration happens in AgentRegistry.CreateAgent() based on config

	// Note: tool_search is registered by AgentRegistry when a global tool registry is available
	// Individual agents don't have access to the global tool registry during construction

	// query_tool_result serves THIS TURN's memory by message_id (HLD §7.1) and
	// recall retrieves summary-cited conversation spans (HLD §6). Registered by
	// default — their doors are printed by the offload stub and the summary's
	// citations respectively — through RegisterTool, so the WithoutBuiltinTool
	// suppression set is honoured.
	queryTool := shuttle.Tool(NewQueryToolResultTool(a))
	recallTool := shuttle.Tool(NewRecallTool(a))
	if a.prompts != nil {
		queryTool = shuttle.NewPromptAwareTool(queryTool, a.prompts, "tools.query_tool_result")
		recallTool = shuttle.NewPromptAwareTool(recallTool, a.prompts, "tools.recall")
	}
	a.RegisterTool(queryTool)
	a.RegisterTool(recallTool)

	// Register graph_memory tool eagerly (not progressive disclosure).
	// Unlike conversation_memory which depends on runtime state (L2 swap events),
	// graph_memory only depends on graphMemoryStore + graphMemoryConfig — both known
	// at construction time. Registering here ensures the tool is in the LLM's tool
	// list from the very first conversation turn, matching the system prompt supplement
	// that references it. The post-loop calls in Chat/ChatWithProgress are harmless
	// no-ops since checkAndRegisterGraphMemoryTool early-returns if already registered.
	a.checkAndRegisterGraphMemoryTool()
	a.checkAndRegisterTaskBoardTool()
	a.checkAndRegisterManageSkillsTool()
	a.checkAndRegisterLoadPatternTool()

	// Auto-wire the skill task emitter when both the skill subsystem AND
	// the task subsystem are configured. The emitter is the bridge between
	// skill activations and task board entries (skills overhaul Phase 6).
	// Caller can preempt this by setting WithSkillTaskEmitter explicitly.
	if a.skillTaskEmitter == nil && a.skillOrchestrator != nil && a.taskManager != nil {
		a.skillTaskEmitter = skilltasks.NewEmitter(a.taskManager, a.taskDecomposer)
	}

	// Install the sticky-while-open-tasks checker on the orchestrator
	// when both the skill subsystem and the task subsystem are present.
	// Eviction will treat any active skill with non-DONE+non-CANCELLED
	// tasks for this (skill, session) pair as sticky, so in-flight work
	// is never abandoned mid-turn. (Skills overhaul deferred work C.)
	if a.skillOrchestrator != nil && a.taskManager != nil {
		taskMgr := a.taskManager
		a.skillOrchestrator.SetStickinessChecker(func(skillName, sessionID string) bool {
			open, err := taskMgr.HasOpenSkillTasks(context.Background(), skillName, sessionID)
			if err != nil {
				// Treat lookup failures as "not sticky" so eviction can
				// still proceed; logging is enough.
				zap.L().Debug("stickiness check failed",
					zap.String("skill", skillName),
					zap.String("session", sessionID),
					zap.Error(err))
				return false
			}
			return open
		})
	}

	// Construct the end-of-turn hygiene auditor + enforcer when both the
	// skill and task subsystems are wired. The auditor inspects tasks
	// emitted by currently-active skills via SkillIdempotencyKey; the
	// enforcer applies the resolved HygienePolicy from SkillsConfig.Hygiene
	// at end-of-turn (see runConversationLoop). Caller can preempt by
	// providing a non-nil pair via Option before NewAgent returns.
	if a.hygieneAuditor == nil && a.skillOrchestrator != nil && a.taskManager != nil {
		a.hygieneAuditor = hygiene.NewAuditor(
			a.taskManager,
			a.skillOrchestrator,
			hygiene.WithTracer(a.tracer),
			hygiene.WithLogger(zap.L()),
		)
	}
	if a.hygieneEnforcer == nil && a.taskManager != nil {
		a.hygieneEnforcer = hygiene.NewEnforcer(
			a.taskManager,
			hygiene.WithEnforcerTracer(a.tracer),
			hygiene.WithEnforcerLogger(zap.L()),
			hygiene.WithAgentID(a.id),
		)
	}

	// Install an eviction callback that boosts graph memory salience for
	// entities related to the evicted skill. When a skill is evicted, its
	// related knowledge becomes more relevant (the agent just used it),
	// so touching those memories strengthens future retrieval. Zero LLM
	// calls — pure FTS search + TouchMemories.
	if a.skillOrchestrator != nil && a.graphMemoryStore != nil {
		store := a.graphMemoryStore
		agentName := a.config.Name
		a.skillOrchestrator.SetOnSkillEviction(func(sessionID string, skill *skills.Skill, activeFor time.Duration) {
			ctx := context.Background()

			// Build search query from skill name + first 3 keywords.
			query := skill.Name
			keywords := skill.Trigger.Keywords
			if len(keywords) > 3 {
				keywords = keywords[:3]
			}
			for _, kw := range keywords {
				query += " " + kw
			}

			entities, err := store.SearchEntities(ctx, agentName, query, 5)
			if err != nil {
				zap.L().Debug("eviction salience boost: entity search failed",
					zap.String("skill", skill.Name),
					zap.String("session", sessionID),
					zap.Error(err))
				return
			}

			var memoryIDs []string
			for _, ent := range entities {
				memories, recallErr := store.Recall(ctx, memory.RecallOpts{
					AgentID:   agentName,
					EntityIDs: []string{ent.ID},
					Limit:     3,
				})
				if recallErr != nil {
					continue
				}
				for _, mem := range memories {
					memoryIDs = append(memoryIDs, mem.ID)
				}
			}

			if len(memoryIDs) == 0 {
				return
			}

			// Deduplicate.
			seen := make(map[string]struct{}, len(memoryIDs))
			deduped := memoryIDs[:0]
			for _, id := range memoryIDs {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					deduped = append(deduped, id)
				}
			}

			if touchErr := store.TouchMemories(ctx, deduped); touchErr != nil {
				zap.L().Debug("eviction salience boost: TouchMemories failed",
					zap.String("skill", skill.Name),
					zap.Error(touchErr))
				return
			}

			zap.L().Debug("eviction salience boost: touched memories",
				zap.String("skill", skill.Name),
				zap.String("session", sessionID),
				zap.Duration("active_for", activeFor),
				zap.Int("entity_count", len(entities)),
				zap.Int("memory_count", len(deduped)))
		})
	}

	// Wire restore re-fire: after a restart replay, the memory manager walks a
	// session's durable messages and calls back here to re-activate each loaded
	// skill (with its required tools) and re-register the disclosure tools
	// implied by durable error/large-result records, so a restored session
	// advertises the same tools and reports the same active skills as a live one.
	if a.memory != nil {
		a.memory.SetRestoreReFireHooks(a.reFireSkillActivation)
		a.memory.SetContextDebug(a.ctxDebug)
	}

	return a
}

// reFireSkillActivation re-activates a skill by name during restore replay:
// it pins the skill (no-evict) into the session and wires its required tools,
// mirroring a manage_skills load so a restored session's active skills and
// advertised tools match a live one. Called from the memory manager's restore
// walk for each durable load marker. A skill missing from the library is
// logged and skipped.
func (a *Agent) reFireSkillActivation(sessionID, skillName string) {
	// A restore can be the FIRST thing a fresh process does, before any turn has
	// run. Freeze the base set here too: re-firing wires the skill's required
	// tools, and with no base snapshot yet every one of them — including tools
	// that are base for all sessions — would be marked session-scoped and hidden
	// from every other session for the process's lifetime. captureBaseTools is
	// idempotent, so whichever of restore or the first turn arrives first wins,
	// and both run after the embedder's construction-time registration.
	a.captureBaseTools()
	if a.skillOrchestrator == nil {
		return
	}
	lib := a.skillOrchestrator.GetLibrary()
	if lib == nil {
		return
	}
	skill, err := lib.Load(skillName)
	if err != nil {
		zap.L().Warn("restore re-fire: skill not found; skipping",
			zap.String("skill", skillName),
			zap.String("session", sessionID),
			zap.Error(err))
		return
	}
	a.skillOrchestrator.ActivatePinned(sessionID, skill, "restore_replay", skillName, 1.0)
	a.enforceRequiredSkillTools(sessionID)
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

// WithoutBuiltinTool suppresses the named builtin tool from surfacing to the
// LLM. The corresponding subsystem (e.g. the graph memory extractor, task
// manager, error store) keeps running — only the tool definition is hidden
// from the agent's tool list.
//
// Used by the server to enforce tools.minimal / tools.none policy without
// disabling subsystem wiring. Pass one name per call; call repeatedly to
// suppress multiple tools.
//
// Currently honoured for: graph_memory, task_board, query_tool_result,
// recall, manage_skills, load_pattern. Other tools either
// are registered eagerly by the caller (cmd_serve) and can be omitted there,
// or are not subject to suppression.
func WithoutBuiltinTool(name string) Option {
	return func(a *Agent) {
		if name == "" {
			return
		}
		if a.suppressedBuiltinTools == nil {
			a.suppressedBuiltinTools = make(map[string]bool)
		}
		a.suppressedBuiltinTools[name] = true
	}
}

// isBuiltinToolSuppressed reports whether the named tool has been suppressed
// via WithoutBuiltinTool. Read-only; safe under the agent's normal mutex
// discipline because the suppression map is set at construction and not
// mutated afterwards.
func (a *Agent) isBuiltinToolSuppressed(name string) bool {
	if len(a.suppressedBuiltinTools) == 0 {
		return false
	}
	return a.suppressedBuiltinTools[name]
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

// WithMessageQueue enables async agent-to-agent messaging.
// When set, agents can send/receive messages via the queue, enabling
// fire-and-forget, request-response, and acknowledgment-based communication.
func WithMessageQueue(queue *communication.MessageQueue) Option {
	return func(a *Agent) {
		a.messageQueue = queue
	}
}

// WithJudgeLLM sets the LLM provider for evaluation operations.
func WithJudgeLLM(llm LLMProvider) Option {
	return func(a *Agent) {
		a.judgeLLM = llm
	}
}

// WithOrchestratorLLM sets the LLM provider for fork-join merge/synthesis.
func WithOrchestratorLLM(llm LLMProvider) Option {
	return func(a *Agent) {
		a.orchestratorLLM = llm
	}
}

// WithClassifierLLM sets the LLM provider for intent classification.
func WithClassifierLLM(llm LLMProvider) Option {
	return func(a *Agent) {
		a.classifierLLM = llm
	}
}

// WithCompressorLLM sets the LLM provider for memory compression.
func WithCompressorLLM(llm LLMProvider) Option {
	return func(a *Agent) {
		a.compressorLLM = llm
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

// WithSkillOrchestrator sets the skill orchestrator for skill activation and injection.
func WithSkillOrchestrator(orch *skills.Orchestrator) Option {
	return func(a *Agent) {
		a.skillOrchestrator = orch
	}
}

// WithSkillDiscovery wires the top-level Discovery. Skills are pulled by the
// model through manage_skills, so the conversation loop performs no discovery,
// matching or activation of its own; the wired Discovery is available to
// callers that drive it directly.
func WithSkillDiscovery(d *discovery.Discovery) Option {
	return func(a *Agent) {
		a.skillDiscovery = d
	}
}

// WithSkillTaskEmitter wires the skill task emitter. When set and the
// skill's EffectiveEmitTasks() returns true, freshly-activated skills
// materialize tasks onto the agent's task board. Requires that the
// agent's task manager is also configured.
func WithSkillTaskEmitter(e *skilltasks.Emitter) Option {
	return func(a *Agent) {
		a.skillTaskEmitter = e
	}
}

// captureBaseTools records the always-advertised tool set — every tool present
// before any event-driven (session-scoped) registration. Idempotent: the first
// call wins, so it must run before the first skill-required or progressive-
// disclosure registration of the agent's life (the top of runConversationLoop).
func (a *Agent) captureBaseTools() {
	a.baseToolsOnce.Do(func() {
		names := a.tools.List()
		a.mu.Lock()
		a.baseToolNames = make(map[string]bool, len(names))
		for _, n := range names {
			a.baseToolNames[n] = true
		}
		a.mu.Unlock()
	})
}

// registerSessionTool records name into sessionID's advertised set so the tool
// surfaces for THIS session on its next — and current, since the set is
// re-derived per provider call — turn. A base always-advertised name is not
// marked session-scoped, so a skill requiring a base tool never hides it from
// other sessions. This is the API D-A (manage_skills load) and D-restore
// (replay) use to advertise a tool into a single session.
func (a *Agent) registerSessionTool(sessionID string, name string) {
	if name == "" {
		return
	}
	if sessionID == "" {
		sessionID = a.id
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionToolLedger[sessionID] == nil {
		a.sessionToolLedger[sessionID] = make(map[string]bool)
	}
	a.sessionToolLedger[sessionID][name] = true
	if !a.baseToolNames[name] {
		a.scopedToolNames[name] = true
	}
}

// advertisedTools is the per-session tool projection offered on a provider call:
// every registered tool that is base (not session-scoped) or present in this
// session's ledger, with skill-excluded and permission-hidden tools removed and
// the result name-sorted for a deterministic order that is byte-stable across
// consecutive calls unless this session's own ledger changed. Re-derived per
// provider call so a mid-turn registration surfaces on the same turn.
func (a *Agent) advertisedTools(session *Session) []shuttle.Tool {
	all := a.tools.ListTools()

	sessionID := a.id
	if session != nil && session.ID != "" {
		sessionID = session.ID
	}

	a.mu.RLock()
	ledger := a.sessionToolLedger[sessionID]
	out := make([]shuttle.Tool, 0, len(all))
	for _, t := range all {
		name := t.Name()
		if !a.scopedToolNames[name] || ledger[name] {
			out = append(out, t)
		}
	}
	a.mu.RUnlock()

	out = a.applySkillExcludedTools(out, session)
	out = a.applyPermissionToolFilter(out)

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

// enforceRequiredSkillTools auto-registers tools listed in active skills'
// SkillToolConfig.RequiredTools when they are available in the builtin
// catalog and not already registered. Tools that aren't builtin-known are
// logged at Warn so operators can see what's missing without breaking the
// turn — the agent continues with whatever tools ARE registered.
//
// MCP servers (SkillToolConfig.MCPServers) are not yet activated by skill
// activation; the field is parsed but not enforced. A future change can
// add an MCPManager hook here.
func (a *Agent) enforceRequiredSkillTools(sessionID string) {
	if a.skillOrchestrator == nil {
		return
	}
	active := a.skillOrchestrator.GetActiveSkills(sessionID)
	if len(active) == 0 {
		return
	}
	for _, as := range active {
		if as == nil || as.Skill == nil {
			continue
		}
		for _, name := range as.Skill.Tools.RequiredTools {
			if !a.tools.IsRegistered(name) {
				tool := builtin.ByName(name)
				if tool == nil {
					zap.L().Warn("skill required tool not available; skipping",
						zap.String("skill", as.Skill.Name),
						zap.String("tool", name))
					continue
				}
				a.tools.Register(tool)
				zap.L().Debug("skill required tool auto-registered",
					zap.String("skill", as.Skill.Name),
					zap.String("tool", name))
			}
			// Advertise the required tool into THIS session even when another
			// session registered the definition first. Base tools are ignored
			// by registerSessionTool, so requiring one never hides it elsewhere.
			a.registerSessionTool(sessionID, name)
		}
		// Surface MCP-server requests so operators see when a skill has
		// declared servers that aren't yet honored. Logged once per turn
		// per skill rather than per server to avoid log spam.
		if len(as.Skill.Tools.MCPServers) > 0 {
			zap.L().Debug("skill declares mcp_servers; activation not yet supported",
				zap.String("skill", as.Skill.Name),
				zap.Int("count", len(as.Skill.Tools.MCPServers)))
		}
	}
}

// deactivateSkillForFold is fold's skill-deactivation path (HLD §4.5): when a
// skill's manage_skills load pair is folded, the skill is deactivated via the
// orchestrator's existing path and its required tools leave the session's
// advertised ledger — so they leave the provider tools parameter at the next
// compile. Re-loading the skill is the door back. A tool still required by a
// remaining active skill (or base) stays.
func (a *Agent) deactivateSkillForFold(sessionID, skillName string) {
	if a.skillOrchestrator == nil {
		return
	}
	var required []string
	for _, as := range a.skillOrchestrator.GetActiveSkills(sessionID) {
		if as != nil && as.Skill != nil && as.Skill.Name == skillName {
			required = as.Skill.Tools.RequiredTools
		}
	}

	a.skillOrchestrator.DeactivateSkill(sessionID, skillName)

	still := map[string]bool{}
	for _, as := range a.skillOrchestrator.GetActiveSkills(sessionID) {
		if as == nil || as.Skill == nil {
			continue
		}
		for _, n := range as.Skill.Tools.RequiredTools {
			still[n] = true
		}
	}
	a.mu.Lock()
	if ledger := a.sessionToolLedger[sessionID]; ledger != nil {
		for _, n := range required {
			if !still[n] {
				delete(ledger, n)
			}
		}
	}
	a.mu.Unlock()
}

// applySkillExcludedTools filters the input tool slice by removing any
// tool whose name appears in any active skill's
// SkillToolConfig.ExcludedTools. Multiple active skills' exclusions union;
// a tool excluded by ANY active skill is removed for this turn.
//
// The filter is per-turn (no permanent unregistration) so deactivating a
// skill restores its excluded tools on subsequent turns automatically.
func (a *Agent) applySkillExcludedTools(in []shuttle.Tool, session *Session) []shuttle.Tool {
	if a.skillOrchestrator == nil || session == nil {
		return in
	}
	sessionID := session.ID
	if sessionID == "" {
		sessionID = a.id
	}
	active := a.skillOrchestrator.GetActiveSkills(sessionID)
	if len(active) == 0 {
		return in
	}
	excluded := make(map[string]bool)
	for _, as := range active {
		if as == nil || as.Skill == nil {
			continue
		}
		for _, name := range as.Skill.Tools.ExcludedTools {
			excluded[name] = true
		}
	}
	if len(excluded) == 0 {
		return in
	}
	out := make([]shuttle.Tool, 0, len(in))
	for _, tool := range in {
		if excluded[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// applyPermissionToolFilter removes any tool the permission checker would never
// allow — hard-disabled tools, or approval-required tools when no approval
// mechanism is wired up — so the LLM is only offered tools it can actually
// execute. Without this the model "discovers" a disabled tool by calling it and
// eating a denial: a wasted turn, plus an intentional policy decision logged as
// a tool failure. The graph_memory/task_board subsystems keep functioning
// regardless of this filter; it only trims the tool names offered to the model
// (those names are still hidden if an explicit deny pattern matches them).
func (a *Agent) applyPermissionToolFilter(in []shuttle.Tool) []shuttle.Tool {
	if a.permissionChecker == nil {
		return in
	}
	out := make([]shuttle.Tool, 0, len(in))
	for _, tool := range in {
		if tool == nil || a.permissionChecker.Advertisable(tool.Name()) {
			out = append(out, tool)
		}
	}
	return out
}

// RegisterTool registers a tool with the agent. Honours the
// WithoutBuiltinTool suppression set: if the tool's name has been
// suppressed via that option, the registration is silently skipped so
// the LLM never sees the tool. Subsystems that drive the tool keep
// running — only the surface is hidden.
func (a *Agent) RegisterTool(tool shuttle.Tool) {
	if tool != nil && a.isBuiltinToolSuppressed(tool.Name()) {
		return
	}
	a.tools.Register(tool)
}

// RegisterTools registers multiple tools, honouring per-name suppression.
func (a *Agent) RegisterTools(tools ...shuttle.Tool) {
	for _, tool := range tools {
		a.RegisterTool(tool)
	}
}

// UnregisterTool unregisters a tool by name.
func (a *Agent) UnregisterTool(name string) {
	a.tools.Unregister(name)
}

// RegisterLazyTools registers tools that will only be added to the active tool set
// when trigger(userMessage) returns true. Safe to call concurrently.
func (a *Agent) RegisterLazyTools(tools []shuttle.Tool, trigger func(string) bool) {
	if len(tools) == 0 || trigger == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lazyToolSets = append(a.lazyToolSets, lazyToolSet{tools: tools, trigger: trigger})
}

// evaluateLazyTools promotes any lazy tool sets whose trigger matches msg
// into the active registry. Idempotent — already-registered tools are skipped.
// Promoted tools are base (not session-scoped), so they advertise to every
// session. Called from the conversation loop before the per-turn tool projection.
func (a *Agent) evaluateLazyTools(msg string) {
	a.mu.RLock()
	sets := make([]lazyToolSet, len(a.lazyToolSets))
	copy(sets, a.lazyToolSets)
	a.mu.RUnlock()

	for _, set := range sets {
		// Short-circuit if all tools in this set are already registered.
		allRegistered := true
		for _, t := range set.tools {
			if !a.tools.IsRegistered(t.Name()) {
				allRegistered = false
				break
			}
		}
		if allRegistered {
			continue
		}
		if set.trigger(msg) {
			for _, t := range set.tools {
				if !a.tools.IsRegistered(t.Name()) {
					a.tools.Register(t)
				}
			}
		}
	}
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

// GetID returns the agent's unique identifier.
// Every agent instance has a UUID assigned by NewAgent().
// Registry-managed agents have stable GUIDs persisted to database.
func (a *Agent) GetID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.id
}

// SetID sets the agent's ID (used by Registry for stable GUID assignment).
// This method allows external systems to set a stable GUID for the agent.
// IMPORTANT: This should only be called during agent initialization or hot-reload.
func (a *Agent) SetID(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.id = id
}

// SetToolRegistryForDynamicDiscovery configures the tool registry for dynamic tool discovery.
// When enabled, agents can use tools discovered via tool_search without explicit registration.
// MCP tools and builtin tools found in the registry will be dynamically registered when first used.
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
	// Always enable builtin tool provider for dynamic registration
	a.executor.SetBuiltinToolProvider(builtin.NewProvider())
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

// ContextState holds a snapshot of the agent's memory and context window state.
// Used to populate the proto ContextState message in WeaveResponse.
type ContextState struct {
	ActivePattern     string
	ContextTokensUsed int64
	ContextTokensMax  int64
	Rom               string
	ToolsLoaded       []string
}

// GetContextState returns a snapshot of the agent's memory and context window state
// for the given session. Returns nil if the session has no SegmentedMemory.
func (a *Agent) GetContextState(sessionID string) *ContextState {
	sess, ok := a.memory.GetSession(sessionID)
	if !ok || sess == nil {
		return nil
	}

	state := &ContextState{
		Rom:         a.config.Rom,
		ToolsLoaded: a.ListTools(),
	}

	if segMem, ok := sess.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		state.ActivePattern = segMem.GetActivePattern()
		state.ContextTokensUsed = int64(segMem.GetTokenCount())
		state.ContextTokensMax = int64(segMem.GetTokenBudgetMax())
	}

	return state
}

// ResetSessionContext clears the context window for a session, preserving ROM and
// registered tools. Returns true if the session was found and reset, false otherwise.
func (a *Agent) ResetSessionContext(sessionID string) bool {
	sess, ok := a.memory.GetSession(sessionID)
	if !ok || sess == nil {
		return false
	}

	if segMem, ok := sess.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		segMem.ResetContext()
		return true
	}

	return false
}

// getSystemPrompt loads the system prompt from config or PromptRegistry.
// Priority: ROM + Config.SystemPrompt (if explicitly set) > ROM + PromptRegistry > Default
// ROM (Read-Only Memory) provides domain-specific knowledge loaded based on config.Rom
func (a *Agent) getSystemPrompt(ctx context.Context) string {
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

	// Resolve the base prompt from config, prompt registry, or fallback
	var basePrompt string

	// If agent has a custom system prompt in config, use it (takes priority)
	if a.config != nil && a.config.SystemPrompt != "" {
		if romContent != "" {
			basePrompt = romContent + "\n\n---\n\n" + a.config.SystemPrompt
		} else {
			basePrompt = a.config.SystemPrompt
		}
	}

	// Try loading from PromptRegistry as fallback
	if basePrompt == "" && a.prompts != nil {
		patternCount := 0
		if a.orchestrator != nil && a.orchestrator.GetLibrary() != nil {
			patternCount = len(a.orchestrator.GetLibrary().ListAll())
		}

		backendType := "meta-agent"
		if a.backend != nil {
			backendType = a.backend.Name()
		}
		vars := map[string]interface{}{
			"backend_type":       backendType,
			"tool_count":         a.tools.Count(),
			"pattern_count":      patternCount,
			"pattern_categories": "none",
		}

		// Check if streaming is supported by the LLM provider
		a.mu.RLock()
		currentLLM := a.llm
		a.mu.RUnlock()
		streamingSupported := types.SupportsStreaming(currentLLM)

		// Try streaming-specific prompt if supported
		if streamingSupported {
			prompt, err := a.prompts.Get(ctx, "agent.system_with_streaming", vars)
			if err == nil && prompt != "" {
				basePrompt = prompt
			}
		}

		if basePrompt == "" {
			// Try loading system prompt with patterns if pattern library is available
			prompt, err := a.prompts.Get(ctx, "agent.system", vars)
			if err == nil && prompt != "" {
				basePrompt = prompt
			}
		}

		if basePrompt == "" {
			// Fall back to basic system prompt
			prompt, err := a.prompts.Get(ctx, "agent.system_basic", vars)
			if err == nil && prompt != "" {
				basePrompt = prompt
			}
		}
	}

	// Fall back to config (second check for non-nil config without SystemPrompt already set)
	if basePrompt == "" && a.config.SystemPrompt != "" {
		if romContent != "" {
			basePrompt = romContent + "\n\n---\n\n" + a.config.SystemPrompt
		} else {
			basePrompt = a.config.SystemPrompt
		}
	}

	// If we have ROM but no system prompt, just use ROM
	if basePrompt == "" && romContent != "" {
		basePrompt = romContent
	}

	// Final fallback - minimal instruction
	if basePrompt == "" {
		basePrompt = `Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return.`
	}

	// Inject task context (current tasks, ready front, board stats).
	// Rendered once into ROM at session creation — the ROM slot is
	// byte-stable for the session, so this is a snapshot, not a live view.
	basePrompt += a.buildTaskContext(ctx)

	// Append task board tool instructions after the context block,
	// so the agent sees "here's your state" before "here's how to manage it".
	basePrompt += a.taskBoardPromptSupplement()

	// Append the static menu of bound skills. Rendered once here into ROM at
	// session creation, so it stays byte-stable for the whole session.
	basePrompt += a.skillMenuPromptSupplement()

	// Append the team block for a woven workflow participant. Empty for every
	// other agent — nothing outside weave attaches a workflow context.
	basePrompt += a.workflowCommPromptSupplement()

	return basePrompt
}

// workflowCommPromptSupplement renders the team block for an agent that weave
// attached a workflow communication context to: the peers it can reach and the
// mechanics of reaching them. Empty for every other agent — a standalone agent,
// a cloud workflow step and a plain spawned sub-agent all render nothing,
// because none of their paths attaches a context. Session-stable: the peer list
// is fixed for the life of the workflow, so this does not disturb the ROM slot's
// byte-stability.
func (a *Agent) workflowCommPromptSupplement() string {
	return formatWorkflowCommunicationInstructions(a.workflowCommContext)
}

// skillMenuPromptSupplement renders the agent's bound skills as a
// `name — description` menu. It names every bound skill without loading any
// skill body: a skill's instructions enter the conversation only when the agent
// calls manage_skills with the load action for that skill. Returns an empty
// string when no skill library is wired or when skills are disabled.
func (a *Agent) skillMenuPromptSupplement() string {
	if a.skillOrchestrator == nil {
		return ""
	}
	lib := a.skillOrchestrator.GetLibrary()
	if lib == nil {
		return ""
	}

	skillsConfig := a.config.SkillsConfig
	if skillsConfig == nil {
		skillsConfig = skills.DefaultSkillsConfig()
	}
	if !skillsConfig.Enabled {
		return ""
	}

	resolved, err := skillbinding.NewResolver(lib).Resolve(skillsConfig)
	if err != nil || len(resolved) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n---\n\n# Available skills\n\n")
	b.WriteString("Bound to this agent. Load with manage_skills to bring instructions into the conversation; until then, only the name + description below are in context.\n\n")
	for _, rb := range resolved {
		if rb.Skill == nil {
			continue
		}
		if rb.Skill.Description != "" {
			b.WriteString(fmt.Sprintf("- %s — %s\n", rb.Skill.Name, rb.Skill.Description))
		} else {
			b.WriteString(fmt.Sprintf("- %s\n", rb.Skill.Name))
		}
	}
	return b.String()
}

// taskBoardPromptSupplement returns instructions for agents with task board enabled.
// Returns empty string when task board is not available.
func (a *Agent) taskBoardPromptSupplement() string {
	if a.taskManager == nil || a.taskBoardConfig == nil || !a.taskBoardConfig.Enabled {
		return ""
	}
	return `

---

TASK BOARD

You have a task_board tool for decomposing goals into dependency-tracked tasks and managing work via a kanban board.

Workflow:
1. decompose — Break a complex goal into a DAG of subtasks (specify strategy: backward, forward, or parallel)
2. ready — See what tasks have all dependencies satisfied and are available to work on
3. claim — Atomically claim a task to work on (prevents other agents from picking it up)
4. Work on the task using your other tools
5. update — Append progress notes, update approach as you learn more
6. close — Mark the task done with a completion reason
7. ready — Check what's unblocked next

Key actions: decompose, ready, claim, update, close, create, list, show, add_dep, board

Guidelines:
- For complex multi-step requests, use decompose before diving in
- Always claim a task before working on it
- Append structured notes at milestones: [STARTED], [PROGRESS], [KEY FINDING], [BLOCKED]
- Close tasks with a meaningful reason summarizing what was accomplished
- Use show to check dependencies and blocked/blocking relationships
- Tasks are domain-agnostic: research, analysis, writing, decisions — not just code`
}

// checkAndRegisterTaskBoardTool implements progressive disclosure for the task_board tool.
func (a *Agent) checkAndRegisterTaskBoardTool() {
	if a.isBuiltinToolSuppressed("task_board") {
		return
	}
	if a.tools.IsRegistered("task_board") {
		return
	}
	if a.taskManager == nil {
		return
	}
	if a.taskBoardConfig == nil || !a.taskBoardConfig.Enabled {
		return
	}

	tbTool := NewTaskBoardTool(a.taskManager, a.taskDecomposer, a.id, a.llm, a.taskBoardConfig)
	a.tools.Register(tbTool)
}

// checkAndRegisterManageSkillsTool registers the manage_skills builtin whenever
// the skill orchestrator is wired. It is a base advertised tool (present from the
// first turn), so it is registered at construction. Idempotent: re-entry is a
// no-op once the tool is registered.
func (a *Agent) checkAndRegisterManageSkillsTool() {
	if a.isBuiltinToolSuppressed("manage_skills") {
		return
	}
	if a.tools.IsRegistered("manage_skills") {
		return
	}
	// Guard the library too, mirroring checkAndRegisterLoadPatternTool: load/list
	// nil-deref GetLibrary() otherwise (an orchestrator can be wired without one).
	if a.skillOrchestrator == nil || a.skillOrchestrator.GetLibrary() == nil {
		return
	}

	tool := NewManageSkillsTool(
		a.skillOrchestrator,
		a.skillOrchestrator.GetLibrary(),
		a.permissionChecker,
		a.enforceRequiredSkillTools,
	)
	tool.ctxDebug = a.ctxDebug
	a.tools.Register(tool)
}

// checkAndRegisterLoadPatternTool registers the load_pattern builtin whenever a
// pattern library is configured. It is a base advertised tool (present from the
// first turn), so it is registered at construction. Idempotent: re-entry is a
// no-op once the tool is registered.
func (a *Agent) checkAndRegisterLoadPatternTool() {
	if a.isBuiltinToolSuppressed("load_pattern") {
		return
	}
	if a.tools.IsRegistered("load_pattern") {
		return
	}
	if a.orchestrator == nil || a.orchestrator.GetLibrary() == nil {
		return
	}

	a.tools.Register(NewLoadPatternTool(a.orchestrator))
}

// buildTaskContext queries the task store and builds a compact context block
// for injection into the system prompt. Rebuilt from DB each turn, so it
// survives context compaction — the agent never forgets what it's working on.
// Returns empty string if task board is not enabled, budget is 0, or no tasks exist.
func (a *Agent) buildTaskContext(ctx context.Context) string {
	if a.taskManager == nil || a.taskBoardConfig == nil || !a.taskBoardConfig.Enabled {
		return ""
	}

	// context_budget_tokens = 0 means disable context injection (per proto doc).
	// Negative values also disable. Unset (proto default 0) disables; users must
	// explicitly set a positive value or rely on the default below.
	budgetTokens := int(a.taskBoardConfig.ContextBudgetTokens)
	if budgetTokens < 0 {
		return ""
	}
	if budgetTokens == 0 {
		budgetTokens = 500 // default when not explicitly set
	}

	boardID := a.taskBoardConfig.DefaultBoardId

	// Query current claimed tasks for this agent.
	claimed, _, err := a.taskManager.ListTasks(ctx, task.ListTasksOpts{
		AssigneeAgentID: a.id,
		Status:          loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		BoardID:         boardID,
		Limit:           5,
	})
	if err != nil {
		zap.L().Warn("task context: failed to list claimed tasks", zap.Error(err))
	}

	// Query ready front.
	ready, err := a.taskManager.GetReadyFront(ctx, boardID, task.ReadyFrontOpts{
		MaxResults: 5,
	})
	if err != nil {
		zap.L().Warn("task context: failed to get ready front", zap.Error(err))
	}

	// Query board stats.
	allTasks, total, err := a.taskManager.ListTasks(ctx, task.ListTasksOpts{
		BoardID: boardID,
		Limit:   1000,
	})
	if err != nil {
		zap.L().Warn("task context: failed to list board tasks", zap.Error(err))
	}

	// Query recent completions (last 3 closed tasks for momentum/context).
	recentDone, _, _ := a.taskManager.ListTasks(ctx, task.ListTasksOpts{
		BoardID: boardID,
		Status:  loomv1.TaskStatus_TASK_STATUS_DONE,
		Limit:   3,
	})

	// If no tasks exist anywhere, skip the context block entirely.
	if total == 0 && len(claimed) == 0 && len(ready) == 0 {
		return ""
	}

	// Compute stats.
	stats := map[string]int{"total": total}
	for _, t := range allTasks {
		stats[task.StatusName(t.Status)]++
	}

	var b strings.Builder
	b.WriteString("\n\n--- TASK CONTEXT ---\n")

	// Current tasks with dependency info.
	if len(claimed) > 0 {
		for _, t := range claimed {
			fmt.Fprintf(&b, "CURRENT TASK: [%s] %q (%s, %s)\n",
				truncateID(t.ID), t.Title, task.StatusName(t.Status), task.PriorityName(t.Priority))
			if t.Objective != "" {
				fmt.Fprintf(&b, "  Objective: %s\n", t.Objective)
			}
			if t.Approach != "" {
				fmt.Fprintf(&b, "  Approach: %s\n", t.Approach)
			}
			if t.Notes != "" {
				notes := t.Notes
				if len(notes) > 200 {
					notes = "..." + notes[len(notes)-200:]
				}
				fmt.Fprintf(&b, "  Notes: %s\n", notes)
			}
			// Show dependency info for current task.
			a.writeTaskDeps(&b, ctx, t.ID)
		}
	} else {
		b.WriteString("NO CURRENT TASK — use task_board ready to find work\n")
	}

	// Ready front
	if len(ready) > 0 {
		fmt.Fprintf(&b, "\nREADY FRONT (%d tasks):\n", len(ready))
		for _, t := range ready {
			effort := ""
			if t.EstimatedEffort != "" {
				effort = ", ~" + t.EstimatedEffort
			}
			fmt.Fprintf(&b, "  [%s] %q (%s%s)\n",
				truncateID(t.ID), t.Title, task.PriorityName(t.Priority), effort)
		}
	}

	// Recent completions
	if len(recentDone) > 0 {
		fmt.Fprintf(&b, "\nRECENT COMPLETIONS (%d):\n", len(recentDone))
		for _, t := range recentDone {
			fmt.Fprintf(&b, "  [%s] %q — %s\n", truncateID(t.ID), t.Title, t.CloseReason)
		}
	}

	// Board stats
	fmt.Fprintf(&b, "\nBOARD: %d total", stats["total"])
	if v := stats["IN_PROGRESS"]; v > 0 {
		fmt.Fprintf(&b, " | %d in_progress", v)
	}
	if v := stats["BLOCKED"]; v > 0 {
		fmt.Fprintf(&b, " | %d blocked", v)
	}
	if v := stats["DONE"]; v > 0 {
		fmt.Fprintf(&b, " | %d done", v)
	}
	if v := stats["OPEN"]; v > 0 {
		fmt.Fprintf(&b, " | %d open", v)
	}
	b.WriteString("\n")

	result := b.String()

	// Rough token budget enforcement (~4 chars per token).
	maxChars := budgetTokens * 4
	if len(result) > maxChars {
		result = result[:maxChars] + "\n[task context truncated]\n"
	}

	return result
}

// writeTaskDeps appends blocked-by and blocking info for a task to the builder.
func (a *Agent) writeTaskDeps(b *strings.Builder, ctx context.Context, taskID string) {
	deps, _ := a.taskManager.Store().GetDependencies(ctx, taskID)
	dependents, _ := a.taskManager.Store().GetDependents(ctx, taskID)

	blockedBy := []string{}
	for _, d := range deps {
		if d.Type == loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS {
			blocker, err := a.taskManager.Store().GetTask(ctx, d.ToTaskID)
			if err == nil && !task.IsTerminal(blocker.Status) {
				blockedBy = append(blockedBy, fmt.Sprintf("[%s] %q", truncateID(blocker.ID), blocker.Title))
			}
		}
	}
	blocking := []string{}
	for _, d := range dependents {
		if d.Type == loomv1.TaskDependencyType_TASK_DEPENDENCY_TYPE_BLOCKS {
			blocked, err := a.taskManager.Store().GetTask(ctx, d.FromTaskID)
			if err == nil {
				blocking = append(blocking, fmt.Sprintf("[%s] %q", truncateID(blocked.ID), blocked.Title))
			}
		}
	}

	if len(blockedBy) > 0 {
		fmt.Fprintf(b, "  Blocked by: %s\n", strings.Join(blockedBy, ", "))
	} else {
		b.WriteString("  Blocked by: (none)\n")
	}
	if len(blocking) > 0 {
		fmt.Fprintf(b, "  Blocking: %s\n", strings.Join(blocking, ", "))
	}
}

// truncateID returns the first 8 chars of an ID for compact display.
func truncateID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// SetWorkflowCommunicationContext sets the workflow communication context for this agent.
// This context is used to inject dynamic communication instructions into the system prompt.
func (a *Agent) SetWorkflowCommunicationContext(ctx *WorkflowCommunicationContext) {
	a.workflowCommContext = ctx
}

// formatWorkflowCommunicationInstructions renders an agent's team block: the
// peers it can reach and the mechanics of reaching them. It is appended to the
// end of ROM at session creation (see workflowCommPromptSupplement) — weave
// attaches the context before the session exists, and the peer list is fixed
// for the life of the workflow, so the rendered block is byte-stable for the
// session.
//
// The reply rule is load-bearing: an agent's turn text is not delivered
// anywhere, so a received message travels back only when the agent calls
// send_message. Without that line a worker answers into nothing and the
// workflow stalls after its first delegation.
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
		instructions.WriteString("→ Incoming posts are marked \"[BROADCAST FROM agent]:\"\n")
		instructions.WriteString("→ Do NOT poll - you will be notified automatically\n\n")
	}

	// Point-to-point instructions (if available agents)
	if len(ctx.AvailableAgents) > 0 {
		instructions.WriteString("🔔 WORKFLOW COMMUNICATION (DIRECT MESSAGING)\n")
		instructions.WriteString(fmt.Sprintf("Available agents: %s\n", strings.Join(ctx.AvailableAgents, ", ")))
		instructions.WriteString("→ To send: send_message(to_agent=\"agent-id\", message=\"task description\")\n")
		instructions.WriteString("→ Incoming messages are marked \"[MESSAGE FROM agent]:\"\n")
		instructions.WriteString("→ When a message arrives, send your answer back to its sender with send_message — your reply text alone is not delivered\n")
		instructions.WriteString("→ Do NOT poll - you will be notified automatically\n")
	}

	if instructions.Len() == 0 {
		return ""
	}

	return "\n\n---\n\n" + strings.TrimRight(instructions.String(), "\n")
}

// getGuidanceMessage loads a guidance message from PromptRegistry or returns default.
// Used for user-facing messages like error recovery, max turns, etc.
func (a *Agent) getGuidanceMessage(ctx context.Context, key string, vars map[string]interface{}) string {
	// Try loading from PromptRegistry
	if a.prompts != nil {
		fullKey := "guidance." + key
		msg, err := a.prompts.Get(ctx, fullKey, vars)
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
func (a *Agent) getErrorMessage(ctx context.Context, category string, errorType string, vars map[string]interface{}) string {
	// Try loading from PromptRegistry
	if a.prompts != nil {
		fullKey := fmt.Sprintf("errors.%s.%s", category, errorType)
		msg, err := a.prompts.Get(ctx, fullKey, vars)
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

// maxPreviewLen is the maximum number of runes recorded in span preview attributes
// (message.preview / response.preview). Capping prevents unbounded trace payload sizes
// and avoids leaking full conversation content into observability backends.
const maxPreviewLen = 200

// truncatePreview returns up to maxPreviewLen runes of s, appending "…" when truncated.
func truncatePreview(s string) string {
	runes := []rune(s)
	if len(runes) <= maxPreviewLen {
		return s
	}
	return string(runes[:maxPreviewLen]) + "…"
}

// Chat processes a user message and returns a response.
// This is the main entry point for conversational interaction.
func (a *Agent) Chat(ctx context.Context, sessionID string, userMessage string) (*Response, error) {
	return a.chat(ctx, sessionID, userMessage, chatParams{
		spanName: "agent.chat",
	})
}

// ChatWithProgress is like Chat but supports streaming progress updates.
// The progressCallback will be called at key execution stages to report progress.
// This is used by StreamWeave to provide real-time feedback to clients.
func (a *Agent) ChatWithProgress(ctx context.Context, sessionID string, userMessage string, progressCallback ProgressCallback) (*Response, error) {
	return a.chat(ctx, sessionID, userMessage, chatParams{
		spanName:         "agent.chat_with_progress",
		progressCallback: progressCallback,
		reportProgress:   true,
	})
}

// ChatWithContentBlocks is like ChatWithProgress but the user turn carries
// multimodal content blocks (text and/or images) alongside the plain-text
// content. userMessage remains the canonical text (used for persistence,
// graph-memory extraction, and providers without multimodal support).
//
// When contentBlocks is non-empty, providers build the request from the blocks
// only — Content is not sent. To keep userMessage canonical, if contentBlocks
// contains no text block, userMessage is automatically prepended as one, so an
// image-only call still delivers the question text to the model. Callers that
// include their own text block are unaffected.
//
// progressCallback may be nil, in which case no progress events are emitted
// (equivalent to Chat).
func (a *Agent) ChatWithContentBlocks(ctx context.Context, sessionID string, userMessage string, contentBlocks []ContentBlock, progressCallback ProgressCallback) (*Response, error) {
	return a.chat(ctx, sessionID, userMessage, chatParams{
		spanName:            "agent.chat_with_content_blocks",
		contentBlocks:       contentBlocks,
		progressCallback:    progressCallback,
		reportProgress:      true,
		reportContentBlocks: true,
	})
}

// chatParams configures the shared conversation lifecycle behind Chat,
// ChatWithProgress, and ChatWithContentBlocks. Each public entry point sets only
// the fields it needs so its span and telemetry stay identical to the
// pre-refactor, hand-written form.
type chatParams struct {
	// spanName is the trace span name for this entry point.
	spanName string

	// contentBlocks, when present, is attached to the user turn so multimodal
	// content reaches providers that support it. nil for text-only entry points.
	contentBlocks []ContentBlock

	// progressCallback, when non-nil, is threaded into context so nested
	// operations (tools, backends, sub-agents) can report progress, and it
	// receives a StageFailed event if the conversation loop errors.
	progressCallback ProgressCallback

	// reportProgress adds the "has_progress" field to the conversation.started
	// event. Chat omits it; ChatWithProgress and ChatWithContentBlocks set it.
	reportProgress bool

	// reportContentBlocks adds the "message.content_blocks" span attribute and
	// the "content_blocks" started-event field. ChatWithContentBlocks only.
	reportContentBlocks bool
}

// hasTextBlock reports whether any of the blocks is a text block.
func hasTextBlock(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" {
			return true
		}
	}
	return false
}

// chat runs the full conversation lifecycle — span setup, user-message
// persistence, graph-memory kick-off, the conversation loop, and success/error
// telemetry — shared by the three public chat entry points. See chatParams for
// how each entry point tailors span name, multimodal content, and progress
// reporting.
func (a *Agent) chat(ctx context.Context, sessionID string, userMessage string, p chatParams) (*Response, error) {
	// Providers build the request exclusively from ContentBlocks when present,
	// dropping Content — so an image-only block set would silently send the
	// image without the question text. Keep userMessage truly canonical by
	// prepending it as a text block when the caller didn't include one. This is
	// a no-op for callers that already carry their prompt in a text block.
	if len(p.contentBlocks) > 0 && !hasTextBlock(p.contentBlocks) && userMessage != "" {
		p.contentBlocks = append([]ContentBlock{{Type: "text", Text: userMessage}}, p.contentBlocks...)
	}

	// Inject session ID into context for tool access
	ctx = session.WithSessionID(ctx, sessionID)

	// Start trace span — always created; NoOpTracer handles disabled case
	startTime := time.Now()
	ctx, span := a.tracer.StartSpan(ctx, p.spanName)
	defer a.tracer.EndSpan(span)

	// Set initial attributes
	span.SetAttribute(observability.AttrSessionID, sessionID)
	span.SetAttribute("message.length", len(userMessage))
	span.SetAttribute("message.preview", truncatePreview(userMessage))
	if p.reportContentBlocks {
		span.SetAttribute("message.content_blocks", len(p.contentBlocks))
	}
	a.mu.RLock()
	currentLLM := a.llm
	a.mu.RUnlock()
	span.SetAttribute("llm.provider", currentLLM.Name())
	span.SetAttribute("llm.model", currentLLM.Model())
	span.SetAttribute("config.max_turns", a.config.MaxTurns)
	span.SetAttribute("config.max_tool_executions", a.config.MaxToolExecutions)

	// Record conversation started event
	startedEvent := map[string]interface{}{
		"session_id":     sessionID,
		"message_length": len(userMessage),
	}
	if p.reportContentBlocks {
		startedEvent["content_blocks"] = len(p.contentBlocks)
	}
	if p.reportProgress {
		startedEvent["has_progress"] = p.progressCallback != nil
	}
	span.AddEvent("conversation.started", startedEvent)

	// Get or create session with agent metadata for proper ReferenceStore namespacing
	session := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")

	// TURN END for the previous turn (HLD §1, §7.3): a new turn is starting —
	// in-memory full payloads are replaced by their persisted-row form and the
	// in-turn SQLite is dropped. Rows and summary versions are all that remains.
	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		segMem.DropTurnPayloads()
	}
	a.dropInTurnSQLite(sessionID)

	// Add user message to history. ContentBlocks (when present) take precedence
	// over Content as providers build the request, so multimodal content reaches
	// the model; Content remains the canonical text for persistence and providers
	// without multimodal support.
	//
	// This is the Chat()-entry persist site — the only turn-incrementing event
	// (HLD §4.5) — hence turnStart=true.
	//
	// Time enters the session here, written into the turn at arrival: temporal
	// words ("today", "this month") resolve at utterance time, and a value
	// written once is durable content like any other row — the whole session
	// stays byte-stable. Nothing renders time dynamically anywhere.
	userMsg := a.appendMessage(ctx, session, Message{
		Role:          "user",
		Content:       time.Now().Format("[Mon 2006-01-02 15:04 MST] ") + userMessage,
		ContentBlocks: p.contentBlocks,
		AgentID:       a.id, // Track which agent received this message
		Timestamp:     time.Now(),
	}, true)
	_ = userMsg

	// Fire graph memory extraction on the incoming user message immediately,
	// in parallel with the LLM processing it. The user message is where the
	// information lives — extract entities/facts before the response comes back.
	if a.enableGraphMemoryExtraction {
		a.graphExtractionWG.Add(1)
		go func() {
			defer a.graphExtractionWG.Done()
			a.extractGraphMemoryAsync(ctx, sessionID)
		}()
	}

	// Store progressCallback in context so nested operations (tools, backends) can access it.
	// This enables sub-agent progress reporting (e.g., weaver's sub-agents).
	if p.progressCallback != nil {
		ctx = ContextWithProgressCallback(ctx, p.progressCallback)
	}

	// Create agent context (a nil progressCallback is fine — no events emitted)
	agentCtx := &agentContext{
		Context:          ctx,
		session:          session,
		tracer:           a.tracer,
		progressCallback: p.progressCallback,
	}

	// Run conversation loop
	response, err := a.runConversationLoop(agentCtx)

	a.checkAndRegisterGraphMemoryTool()
	a.checkAndRegisterTaskBoardTool()

	// Calculate total duration
	duration := time.Since(startTime)

	if err != nil {
		span.Status = observability.Status{
			Code:    observability.StatusError,
			Message: err.Error(),
		}
		span.SetAttribute(observability.AttrErrorMessage, err.Error())
		span.AddEvent("conversation.failed", map[string]interface{}{
			"error":       err.Error(),
			"duration_ms": duration.Milliseconds(),
		})

		a.tracer.RecordMetric("agent.conversations.failed", 1, map[string]string{
			observability.AttrSessionID: sessionID,
		})

		// Emit failure event to the progress callback when one is registered.
		if p.progressCallback != nil {
			p.progressCallback(ProgressEvent{
				Stage:     StageFailed,
				Progress:  0,
				Message:   fmt.Sprintf("Execution failed: %v", err),
				Timestamp: time.Now(),
			})
		}

		return nil, fmt.Errorf("conversation loop failed: %w", err)
	}

	// Add assistant response to history
	a.appendMessage(ctx, session, Message{
		Role:       "assistant",
		Content:    response.Content,
		AgentID:    a.id, // Track which agent generated this response
		Timestamp:  time.Now(),
		TokenCount: response.Usage.TotalTokens,
		CostUSD:    response.Usage.CostUSD,
	}, false)

	// Persist session
	if err := a.memory.PersistSession(ctx, session); err != nil {
		zap.L().Warn("Failed to persist session",
			zap.String("session_id", sessionID),
			zap.Error(err))
		span.RecordError(err)
	}

	// Record success metrics and span attributes
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
	span.SetAttribute("response.preview", truncatePreview(response.Content))

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

	// Emit metrics
	a.tracer.RecordMetric(observability.MetricAgentConversations, 1, map[string]string{
		observability.AttrSessionID: sessionID,
		"status":                    "success",
	})

	a.tracer.RecordMetric(observability.MetricAgentConversationDuration, float64(duration.Milliseconds()), map[string]string{
		observability.AttrSessionID: sessionID,
	})

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

	return response, nil
}

// appendMessage is the arrival seam (HLD §1): it stamps the message's turn,
// persists its durable row once (write rules §4; the store's RETURNING-derived
// seq and turn override the stamp), and appends the message to the session in
// full natural form. Nothing is examined, sized, flagged, or transformed at
// arrival. turnStart is true only at the Chat() entry — the only
// turn-incrementing event (HLD §4.5). Persist failures are logged, never fatal.
func (a *Agent) appendMessage(ctx context.Context, session *Session, msg Message, turnStart bool) Message {
	// In-memory derivation, identical arithmetic to the store's subquery — the
	// only derivation for storeless sessions and unpersisted rows.
	t := sessionCurrentTurn(session)
	if turnStart {
		t++
	}
	msg.Turn = t

	if err := a.memory.PersistMessage(ctx, session.ID, &msg, turnStart); err != nil {
		zap.L().Warn("Failed to persist message",
			zap.String("session_id", session.ID),
			zap.String("role", msg.Role),
			zap.Error(err))
	}

	session.AddMessage(ctx, msg)
	return msg
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

// emitToolStarted sends a tool-started progress event.
func emitToolStarted(ctx Context, progress int32, toolCall ToolCall) {
	if callback := ctx.ProgressCallback(); callback != nil {
		callback(ProgressEvent{
			Stage:         StageToolExecution,
			Progress:      progress,
			Message:       fmt.Sprintf("Executing tool: %s", toolCall.Name),
			ToolName:      toolCall.Name,
			Timestamp:     time.Now(),
			IsToolStarted: true,
			ToolInput:     toolCall.Input,
			ToolCallID:    toolCall.ID,
		})
	}
}

// emitToolCompleted sends a tool-completed progress event.
func emitToolCompleted(ctx Context, progress int32, toolCall ToolCall, result *shuttle.Result, execErr error) {
	if callback := ctx.ProgressCallback(); callback != nil {
		var toolErr string
		toolSuccess := execErr == nil && (result == nil || result.Success)
		if execErr != nil {
			toolErr = execErr.Error()
		} else if result != nil && !result.Success && result.Error != nil {
			toolErr = result.Error.Message
		}

		var durationMs int64
		var data interface{}
		if result != nil {
			durationMs = result.ExecutionTimeMs
			data = result.Data
		}

		callback(ProgressEvent{
			Stage:           StageToolExecution,
			Progress:        progress,
			Message:         fmt.Sprintf("Tool completed: %s", toolCall.Name),
			ToolName:        toolCall.Name,
			Timestamp:       time.Now(),
			IsToolCompleted: true,
			ToolResult:      data,
			ToolError:       toolErr,
			ToolSuccess:     toolSuccess,
			ToolDurationMs:  durationMs,
			ToolCallID:      toolCall.ID,
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
	// Start trace span — always created; NoOpTracer handles disabled case
	_, span := ctx.Tracer().StartSpan(ctx, "agent.conversation_loop")
	defer ctx.Tracer().EndSpan(span)

	session := ctx.Session()

	// Freeze the base always-advertised set before any skill-required or
	// progressive-disclosure registration can scope a tool to a session, so a
	// base tool a skill later requires is never mistaken for session-scoped.
	a.captureBaseTools()

	turnCount := 0
	toolExecutionCount := 0
	var allToolExecutions []ToolExecution
	emptyRetried := false                       // one-shot flag: retry empty LLM response at most once per conversation
	hygieneRetries := 0                         // capped count of REQUIRE_FIX retries the end-of-turn auditor has triggered
	var hygieneLast *hygiene.EnforcementOutcome // last outcome, surfaced into Response.Metadata

	// Self-healing orchestrator (Tier 1 recovery).
	var recovery *recoveryOrchestrator
	if a.config.EnableSelfHealing {
		recovery = newRecoveryOrchestrator(a.config.RecoveryConfig, span)
	}

	// Debug: Print config values
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		fmt.Printf("\n=== CONVERSATION LOOP DEBUG ===\n")
		fmt.Printf("MaxTurns: %d\n", a.config.MaxTurns)
		fmt.Printf("MaxToolExecutions: %d\n", a.config.MaxToolExecutions)
		fmt.Printf("=== END DEBUG ===\n\n")
	}

	// Lazy tool disclosure: promote tools whose trigger matches the current user message.
	{
		msgs := session.GetMessages()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				a.evaluateLazyTools(msgs[i].Content)
				break
			}
		}
	}

	// Inject graph memory context (if enabled and available).
	a.injectGraphMemoryContext(ctx, session)

	// Conversation loop
	for turnCount < a.config.MaxTurns && toolExecutionCount < a.config.MaxToolExecutions {
		turnCount++

		// Record turn start on conversation_loop span
		span.AddEvent("turn.started", map[string]interface{}{
			"turn":                turnCount,
			"tool_executions":     toolExecutionCount,
			"max_turns":           a.config.MaxTurns,
			"max_tool_executions": a.config.MaxToolExecutions,
		})

		// Build messages for LLM (will use segmented memory if configured).
		messages := session.GetMessages()

		// === FEATURE INTEGRATION: Soft Reminders ===
		// Compute reminders if approaching limits (non-intrusive, doesn't remove
		// tools). Thresholds: 75% of max (but minimum of 10 tools / 8 turns).
		// The reminder is applied transiently at the provider call — appended as
		// a trailing system message, never onto the ROM message (which would
		// invalidate the ROM cache prefix at exactly the high-pressure moment)
		// and never persisted, so it survives any relief recompile of messages.
		softReminder := ""
		if session.SegmentedMem != nil {
			// Check tool execution reminder
			toolReminder := buildSoftReminder(toolExecutionCount, a.config.MaxToolExecutions)
			// Check turn count reminder
			turnReminder := buildTurnReminder(turnCount, a.config.MaxTurns)

			// Combine reminders if both are active
			softReminder = toolReminder + turnReminder

			if softReminder != "" {
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

		// Emit LLM generation progress
		emitProgress(ctx, StageLLMGeneration, 20+clampInt32(turnCount*10), fmt.Sprintf("Generating response (turn %d)", turnCount), "")

		// Re-derive the advertised tools for this provider call so a mid-turn
		// registration (skill load, progressive disclosure) surfaces on the
		// same turn. Circuit-breaker-disabled tools stay filtered across turns.
		tools := a.advertisedTools(session)
		tools = recovery.activeTools(tools)

		// KERNEL accounting (HLD §2; blueprint A6): the serialized bytes of the
		// advertised tool schemas — the provider tools parameter as built — are
		// part of the compiled artifact and feed releasePressure's estimate.
		// Recomputed per provider call, which covers every registration change.
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			segMem.SetAdvertisedToolsBytes(advertisedToolsBytes(tools))
		}

		// Mutation-debug: the per-session tool projection about to be advertised
		// on this provider call. No-op unless the context-dump switch is on.
		if a.contextDebugEnabled() {
			advertised := make([]string, 0, len(tools))
			for _, t := range tools {
				advertised = append(advertised, t.Name())
			}
			zap.L().Debug("context mutation: tool assembly",
				zap.String("session_id", session.ID),
				zap.Int("turn", turnCount),
				zap.Int("count", len(advertised)),
				zap.Strings("advertised", advertised))
		}

		// Relief runs at compile, before the send: ReleasePressure self-gates on
		// loom's own estimate (§5.1) — a no-op under the start mark, otherwise it
		// sheds to the release mark. On a shed, recompile messages and the
		// advertised tool set so a fold-deactivated skill's tools do not linger.
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			if shed, estimate, target := segMem.ReleasePressure(ctx, 0); shed {
				zap.L().Info("relief: shed before send",
					zap.String("session_id", session.ID),
					zap.Int("estimate_tokens", estimate),
					zap.Int("target_tokens", target))
				messages = session.GetMessages()
				tools = a.advertisedTools(session)
				tools = recovery.activeTools(tools)
				segMem.SetAdvertisedToolsBytes(advertisedToolsBytes(tools))
			}
		}

		// withReminder appends the turn's soft reminder as a trailing system
		// message on a copy — transient, past every cache breakpoint, never
		// stored — so both the normal send and the recovery resend carry it.
		withReminder := func(msgs []Message) []Message {
			if softReminder == "" {
				return msgs
			}
			out := make([]Message, len(msgs), len(msgs)+1)
			copy(out, msgs)
			return append(out, Message{Role: "system", Content: strings.TrimSpace(softReminder)})
		}

		// Call LLM. Relief is proactive (above) — loom keeps the context under its
		// own window. The provider refusal is only a backstop: if a clean
		// context-too-long still comes back (loom's estimate under-counted), shed
		// and resend once; a second refusal ends the turn with the recoverable
		// context_exhausted error.
		llmResp, err := a.chatWithRetry(ctx, withReminder(messages), tools)
		if err != nil && errors.Is(err, llm.ErrContextTooLong) {
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
				_, estimate, target := segMem.ReleasePressure(ctx, pressureRecoveryPenalty)
				zap.L().Info("context too long: relief pass complete, resending once",
					zap.String("session_id", session.ID),
					zap.Int("estimate_tokens", estimate),
					zap.Int("target_tokens", target))
				// Relief can deactivate a skill whose load pair was folded
				// (HLD §4.5): its tools leave KERNEL. The resend is a recompile,
				// so recompute BOTH messages and the advertised tool set.
				messages = session.GetMessages()
				tools = a.advertisedTools(session)
				tools = recovery.activeTools(tools)
				if segMem2, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem2 != nil {
					segMem2.SetAdvertisedToolsBytes(advertisedToolsBytes(tools))
				}
				llmResp, err = a.chatWithRetry(ctx, withReminder(messages), tools)
				if err != nil && errors.Is(err, llm.ErrContextTooLong) {
					zap.L().Error("context too long after relief: turn ends",
						zap.String("session_id", session.ID),
						zap.Int("estimate_tokens", estimate),
						zap.Int("target_tokens", target),
						zap.Error(err))
					return nil, recovery.buildRecoverableError("context_exhausted", err, "",
						map[string]any{"estimate": estimate, "target": target})
				}
			}
		}
		if err != nil {
			span.AddEvent("turn.llm_failed", map[string]interface{}{
				"turn":  turnCount,
				"error": err.Error(),
			})
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// Record LLM response on conversation_loop span
		llmEvent := map[string]interface{}{
			"turn":          turnCount,
			"stop_reason":   llmResp.StopReason,
			"tool_calls":    len(llmResp.ToolCalls),
			"input_tokens":  llmResp.Usage.InputTokens,
			"output_tokens": llmResp.Usage.OutputTokens,
			"total_tokens":  llmResp.Usage.TotalTokens,
			"cost_usd":      llmResp.Usage.CostUSD,
			"has_content":   llmResp.Content != "",
		}
		if llmResp.Thinking != "" {
			llmEvent["has_thinking"] = true
			llmEvent["thinking_length"] = len(llmResp.Thinking)
			llmEvent["thinking"] = truncateString(llmResp.Thinking, 2000)
		}
		if llmResp.Content != "" {
			llmEvent["response_preview"] = truncateString(llmResp.Content, 500)
		}
		// Include tool call names for quick scan
		if len(llmResp.ToolCalls) > 0 {
			toolNames := make([]string, 0, len(llmResp.ToolCalls))
			for _, tc := range llmResp.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			llmEvent["tool_names"] = strings.Join(toolNames, ", ")
		}
		span.AddEvent("turn.llm_response", llmEvent)

		// === OUTPUT TOKEN CIRCUIT BREAKER ===
		// Protects against the agent getting stuck in an infinite tool-call loop where
		// each turn hits the output token limit with truncated (incomplete) tool calls,
		// making no forward progress.
		//
		// IMPORTANT: Only count as a failure when the LLM hits max_tokens AND returns
		// truncated tool calls. A verbose text response (no tool calls) that hits the
		// token limit is a legitimate, complete response and must NOT increment the counter.
		if failureTracker, ok := session.FailureTracker.(*consecutiveFailureTracker); ok && failureTracker != nil {
			threshold := a.config.OutputTokenCBThreshold
			if threshold == 0 {
				threshold = 8 // Default if not configured
			}

			if llmResp.StopReason == "max_tokens" {
				hasEmptyToolCall := detectEmptyToolCall(llmResp.ToolCalls)

				switch {
				case threshold < 0:
					// CB disabled entirely — do nothing

				case len(llmResp.ToolCalls) > 0 && hasEmptyToolCall:
					// TRUE FAILURE: agent is stuck in agentic loop with truncated tool calls.
					// The tool calls cannot be executed because they are incomplete.
					exhaustionCount := failureTracker.recordOutputTokenExhaustion(hasEmptyToolCall)

					span.AddEvent("output_token.exhaustion", map[string]interface{}{
						"count":              exhaustionCount,
						"has_empty_toolcall": hasEmptyToolCall,
						"stop_reason":        llmResp.StopReason,
						"output_tokens":      llmResp.Usage.OutputTokens,
						"threshold":          threshold,
					})

					if err := failureTracker.checkOutputTokenCircuitBreaker(threshold); err != nil {
						span.AddEvent("output_token.circuit_breaker_triggered", map[string]interface{}{
							"exhaustion_count":   exhaustionCount,
							"has_empty_toolcall": hasEmptyToolCall,
							"threshold":          threshold,
						})
						span.RecordError(err)

						// The output-token CB error propagates on the existing
						// recoverable channel WITHOUT touching context — a CB
						// trip is not a context decision (blueprint A5).
						if recovery != nil {
							return nil, recovery.buildRecoverableError("output_token_circuit_breaker", err, "rewind_and_retry", map[string]any{"threshold": threshold})
						}
						return nil, fmt.Errorf("output token circuit breaker: %w", err)
					}

				case len(llmResp.ToolCalls) == 0:
					// NOT A FAILURE: the LLM returned a complete text response that hit the
					// token limit. This is a valid terminal response. Reset the counter so
					// prior verbose turns do not accumulate toward the CB threshold.
					failureTracker.clearOutputTokenExhaustion()

					span.AddEvent("output_token.text_response_cleared", map[string]interface{}{
						"stop_reason":   llmResp.StopReason,
						"output_tokens": llmResp.Usage.OutputTokens,
					})

				default:
					// max_tokens with non-truncated tool calls: agent may still make progress
					// on the next turn. Don't count, don't clear — let it continue.
					span.AddEvent("output_token.non_truncated_toolcall", map[string]interface{}{
						"stop_reason":        llmResp.StopReason,
						"tool_call_count":    len(llmResp.ToolCalls),
						"has_empty_toolcall": hasEmptyToolCall,
					})
				}
			} else {
				// Normal completion (end_turn, stop_sequence, etc.) — clear the counter.
				failureTracker.clearOutputTokenExhaustion()

				span.AddEvent("output_token.exhaustion_cleared", map[string]interface{}{
					"stop_reason": llmResp.StopReason,
				})
			}
		}

		// If LLM returned text (no tool calls), we're done — unless the response
		// is empty, in which case we retry once with a nudge message.
		if len(llmResp.ToolCalls) == 0 {
			if strings.TrimSpace(llmResp.Content) == "" && !emptyRetried {
				// One-shot retry: nudge the LLM to produce a response.
				emptyRetried = true
				a.appendMessage(ctx, session, Message{
					Role:      "user",
					Content:   "Your previous response was empty. Please provide a response summarizing what you found or explaining what went wrong.",
					AgentID:   a.id,
					Timestamp: time.Now(),
				}, false)
				continue // re-enter conversation loop for one more LLM call
			}

			content := llmResp.Content
			if strings.TrimSpace(content) == "" {
				// Already retried and still empty — use fallback.
				content = fmt.Sprintf("Completed %d tool executions across %d turns.", toolExecutionCount, turnCount)
			}

			// Record conversation completion on span
			span.AddEvent("conversation.completed", map[string]interface{}{
				"turns":           turnCount,
				"tool_executions": toolExecutionCount,
				"stop_reason":     llmResp.StopReason,
				"response_length": len(content),
				"total_tokens":    llmResp.Usage.TotalTokens,
				"cost_usd":        llmResp.Usage.CostUSD,
			})
			span.SetAttribute("conversation.turns", turnCount)
			span.SetAttribute("conversation.tool_executions", toolExecutionCount)
			span.SetAttribute("conversation.stop_reason", llmResp.StopReason)
			span.SetAttribute("conversation.total_tokens", llmResp.Usage.TotalTokens)

			// End-of-turn hygiene check for skill-emitted tasks. Audits the
			// active skill's tasks and either injects a fixup message and
			// retries (REQUIRE_FIX), machine-repairs the board (AUTO_FIX), or
			// logs and continues (WARN_ONLY). See pkg/skills/hygiene.
			retry, outcome := a.runEndOfTurnHygiene(ctx, session, &hygieneRetries)
			if outcome != nil {
				hygieneLast = outcome
			}
			if retry {
				// runEndOfTurnHygiene already appended the synthetic user
				// message to the session and persisted it. Re-enter the
				// conversation loop so the LLM sees the fixup request.
				continue
			}

			meta := map[string]interface{}{
				"turns":           turnCount,
				"tool_executions": toolExecutionCount,
				"stop_reason":     llmResp.StopReason,
				"empty_retried":   emptyRetried,
			}
			if hygieneLast != nil {
				meta["hygiene_policy"] = hygieneLast.Policy.String()
				meta["hygiene_violations_found"] = hygieneLast.ViolationsFound
				meta["hygiene_by_kind"] = hygieneLast.ViolationsByKind
				meta["hygiene_resolved"] = hygieneLast.Resolved
				meta["hygiene_hitl_spawned"] = hygieneLast.HITLSpawned
				if hygieneLast.FallthroughReason != "" {
					meta["hygiene_fallthrough"] = hygieneLast.FallthroughReason
				}
			}
			return &Response{
				Content:        content,
				Usage:          llmResp.Usage,
				ToolExecutions: allToolExecutions,
				Thinking:       llmResp.Thinking,
				Metadata:       meta,
			}, nil
		}

		// Add assistant message with tool calls to history FIRST (required by Anthropic API)
		a.appendMessage(ctx, session, Message{
			Role:       "assistant",
			Content:    llmResp.Content,
			ToolCalls:  llmResp.ToolCalls,
			AgentID:    a.id, // Track which agent generated this response
			TokenCount: llmResp.Usage.TotalTokens,
			CostUSD:    llmResp.Usage.CostUSD,
			Timestamp:  time.Now(),
		}, false)

		// Execute tool calls with per-turn cap and deduplication.
		// MaxIterations limits how many tool calls are executed from a single
		// LLM response. Excess calls get "turn_limit_exceeded" error results.
		// Identical calls (same name + input) within a turn reuse the first result.
		maxPerTurn := a.config.MaxIterations
		if maxPerTurn <= 0 {
			maxPerTurn = 10 // default
		}
		turnToolCount := 0
		turnDedup := make(map[string]*shuttle.Result) // dedup key → result

		// pendingSidecars: text_body sidecar messages (e.g. skill body from
		// manage_skills(load)) buffered across the whole tool batch. Draining
		// them AFTER every tool_result in the batch has been appended keeps
		// each tool_use adjacent to its tool_result — required by Anthropic's
		// "tool_use ids must be followed by tool_result blocks" pairing rule
		// when the model fires multiple tools in parallel.
		var pendingSidecars []Message

		for i, toolCall := range llmResp.ToolCalls {
			if toolExecutionCount >= a.config.MaxToolExecutions {
				break
			}

			// Per-turn cap: skip remaining calls with an error result
			if turnToolCount >= maxPerTurn {
				a.appendMessage(ctx, session, Message{
					Role:      "tool",
					Content:   fmt.Sprintf("turn_limit_exceeded — per-turn tool call limit (%d) reached. Synthesize a response from the results you have.", maxPerTurn),
					ToolUseID: toolCall.ID,
					ToolResult: &shuttle.Result{
						Success: false,
						Error: &shuttle.Error{
							Code:    "turn_limit_exceeded",
							Message: fmt.Sprintf("per-turn tool call limit (%d) reached — call %d of %d skipped", maxPerTurn, i+1, len(llmResp.ToolCalls)),
						},
					},
					AgentID:   a.id,
					Timestamp: time.Now(),
				}, false)
				toolExecutionCount++
				continue
			}

			// Deduplication: compute canonical key from tool name + sorted JSON input
			dedupKey := toolCall.Name + "|" + canonicalJSON(toolCall.Input)
			if cachedResult, ok := turnDedup[dedupKey]; ok {
				a.appendMessage(ctx, session, Message{
					Role:       "tool",
					Content:    a.formatToolResult(ctx, session.ID, toolCall.Name, cachedResult, nil) + "\n(deduplicated — reused result from identical call in this turn)",
					ToolUseID:  toolCall.ID,
					ToolResult: cachedResult,
					AgentID:    a.id,
					Timestamp:  time.Now(),
				}, false)
				allToolExecutions = append(allToolExecutions, ToolExecution{
					ToolName: toolCall.Name,
					Input:    toolCall.Input,
					Result:   cachedResult,
				})
				toolExecutionCount++
				turnToolCount++
				continue
			}

			turnToolCount++
			toolExecutionCount++

			// Check if this is a HITL request (contact_human tool)
			if toolCall.Name == "contact_human" {
				// Extract HITL request details from tool input
				hitlInfo := extractHITLInfo(toolCall.Input)

				// Add instrumentation for HITL request
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

				// Emit HITL-specific progress event
				emitProgressWithHITL(ctx, StageHumanInTheLoop, 50, "Waiting for human response", toolCall.Name, hitlInfo)
			} else {
				// Emit tool-started progress event
				emitToolStarted(ctx, 50+clampInt32(toolExecutionCount*5), toolCall)
			}

			// Execute tool with tracing — always created
			_, toolSpan := ctx.Tracer().StartSpan(ctx, "agent.tool_execution")
			toolSpan.SetAttribute("tool_name", toolCall.Name)

			// Execute with self-correction (circuit breaker + SQL correction)
			result, err := a.executeToolWithSelfCorrection(ctx, toolCall.Name, toolCall.Input, session.ID)

			// Tier 1: if tool CB fired, disable tool and inject synthetic result.
			if err != nil && strings.Contains(err.Error(), "circuit breaker open") && recovery != nil {
				_, syntheticResult := recovery.recoverToolCB(ctx, toolCall.Name, &tools)
				result = syntheticResult
				err = nil
			}

			// Record tool execution on conversation_loop span
			{
				toolSuccess := err == nil && (result == nil || result.Success)
				toolEvent := map[string]interface{}{
					"turn":      turnCount,
					"tool_name": toolCall.Name,
					"success":   toolSuccess,
					"index":     i + 1,
					"total":     len(llmResp.ToolCalls),
				}
				if err != nil {
					toolEvent["error"] = err.Error()
				} else if result != nil && !result.Success && result.Error != nil {
					toolEvent["error"] = result.Error.Message
				}
				if result != nil {
					toolEvent["execution_time_ms"] = result.ExecutionTimeMs
				}
				span.AddEvent("turn.tool_execution", toolEvent)
			}

			// Add instrumentation for HITL completion
			if toolCall.Name == "contact_human" {
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

			// Record tool execution results on span
			{
				success := err == nil && (result == nil || result.Success)

				if err != nil {
					toolSpan.RecordError(err)
				} else if result != nil && !result.Success && result.Error != nil {
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

			// Cache result for dedup (only cache successful results or tool errors —
			// not Go-level errors which may be transient).
			if result != nil {
				turnDedup[dedupKey] = result
			}

			// Emit tool-completed progress event
			emitToolCompleted(ctx, 50+clampInt32(toolExecutionCount*5), toolCall, result, err)

			// Persist tool execution
			if persistErr := a.memory.PersistToolExecution(ctx, session.ID, execution); persistErr != nil {
				// Log but don't fail
				toolSpan.RecordError(persistErr)
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

					if escalationMsg != "" {
						span.AddEvent("failure.escalated", map[string]interface{}{
							"tool":          toolCall.Name,
							"failure_count": failureCount,
						})
					}
				} else {
					// Clear failures on success
					failureTracker.clear(toolCall.Name, toolCall.Input)

					span.AddEvent("failure.cleared", map[string]interface{}{
						"tool": toolCall.Name,
					})
				}
			}

			// Format tool result with escalation if needed
			formattedResult := a.formatToolResult(ctx, session.ID, toolCall.Name, result, err)
			if escalationMsg != "" {
				formattedResult = formatToolResultWithEscalation(formattedResult, err, escalationMsg)
			}

			// Add tool result to conversation
			a.appendMessage(ctx, session, Message{
				Role:       "tool",
				Content:    formattedResult,
				ToolUseID:  toolCall.ID, // Store ID for Bedrock/Anthropic format conversion
				ToolResult: result,
				AgentID:    a.id, // Track which agent executed this tool
				Timestamp:  time.Now(),
			}, false)

			// If the tool signaled a text_body sidecar (e.g. manage_skills(load)
			// — the skill body belongs under the user-instruction slot, not the
			// tool-result data slot), BUFFER it. Sidecars from an entire tool
			// batch are appended AFTER every tool_result in the batch, so
			// tool_use↔tool_result adjacency is preserved (Anthropic pairing).
			if result != nil && result.Metadata != nil {
				if textBody, ok := result.Metadata["text_body"].(string); ok && textBody != "" {
					pendingSidecars = append(pendingSidecars, Message{
						Role:      "user",
						Content:   textBody,
						AgentID:   a.id,
						Timestamp: time.Now(),
					})
				}
			}

			// === AUTOMATIC GRAPH MEMORY EXTRACTION ===
			// After each tool execution, check if we should extract graph memories.
			// Skip when the tool IS graph_memory — explicit use is higher quality.
			if a.enableGraphMemoryExtraction && toolCall.Name != "graph_memory" {
				a.graphToolExecutionsSinceExtraction++
				if a.graphToolExecutionsSinceExtraction >= a.graphExtractionCadence {
					a.graphExtractionWG.Add(1)
					go func() {
						defer a.graphExtractionWG.Done()
						a.extractGraphMemoryAsync(ctx, session.ID)
					}()
					a.graphToolExecutionsSinceExtraction = 0
				}
			}
		}

		// Drain buffered text_body sidecars from this batch AFTER every
		// tool_result is in place. Order within the batch preserved so a
		// multi-load turn (rare but possible) still stamps its bodies in
		// call order.
		for _, sidecar := range pendingSidecars {
			// Sidecars never advance the turn and hold no special status beyond
			// that (HLD §4.5).
			a.appendMessage(ctx, session, sidecar, false)
		}
	}

	// If we hit max turns/executions, make one final LLM call to synthesize results
	// This ensures the agent provides meaningful output instead of a generic error message
	emitProgress(ctx, StageSynthesis, 90, "Synthesizing tool execution results", "")

	// Add a synthesis request to the conversation
	// Include explicit format instructions since they may have been compressed in context
	synthesisPrompt := "You must provide your final answer NOW with whatever information you have gathered so far. Summarize your findings: what actions were taken, what results were produced, and any remaining steps the user would need to complete manually. Be concise and actionable. You MUST respond with text — do not return an empty response."
	a.appendMessage(ctx, session, Message{
		Role:      "user",
		Content:   synthesisPrompt,
		AgentID:   a.id, // Track which agent created this synthesis request
		Timestamp: time.Now(),
	}, false)

	// Make final LLM call WITHOUT tools to force synthesis
	finalResp, err := a.chatWithRetry(ctx, session.GetMessages(), nil)
	if err != nil {
		// Only fall back to guidance message if synthesis fails
		maxTurnsMessage := a.getGuidanceMessage(ctx, "max_turns_reached", nil)
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

	// Return synthesized response — fall back to a brief summary if LLM returned empty
	content := finalResp.Content
	if strings.TrimSpace(content) == "" {
		content = fmt.Sprintf("Completed %d tool executions across %d turns.", toolExecutionCount, turnCount)
	}

	return &Response{
		Content:        content,
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

// advertisedToolsBytes measures the serialized bytes of the advertised tool
// schemas as they ride the provider tools parameter (name + description +
// input schema JSON).
func advertisedToolsBytes(tools []shuttle.Tool) int {
	bytes := 0
	for _, t := range tools {
		bytes += len(t.Name()) + len(t.Description())
		if schema := t.InputSchema(); schema != nil {
			if b, err := json.Marshal(schema); err == nil {
				bytes += len(b)
			}
		}
	}
	return bytes
}

// canonicalJSON serializes a map to a deterministic JSON string for deduplication.
// Go's encoding/json.Marshal sorts map keys, making the output canonical.
func canonicalJSON(input map[string]interface{}) string {
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Sprintf("%v", input) // fallback
	}
	return string(b)
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
// checkAndRegisterGraphMemoryTool implements progressive disclosure for the graph_memory tool.
// Registers the tool immediately if a graph memory store is configured and enabled.
//
// The graph memory SUBSYSTEM (store + extractor) runs whenever
// graphMemoryStore is non-nil and graphMemoryConfig.Enabled is true — the
// server can suppress the TOOL surface here without disabling the subsystem.
func (a *Agent) checkAndRegisterGraphMemoryTool() {
	if a.isBuiltinToolSuppressed("graph_memory") {
		return
	}
	if a.tools.IsRegistered("graph_memory") {
		return
	}
	if a.graphMemoryStore == nil {
		return
	}
	if a.graphMemoryConfig != nil && !a.graphMemoryConfig.Enabled {
		return
	}

	tool := shuttle.Tool(NewGraphMemoryTool(a.graphMemoryStore, a.config.Name))
	a.tools.Register(tool)
}

// graphMemoryTokenBudget returns the token budget for graph memory context injection.
// Follows the same pattern as SegmentedMemory's L1 budget.
func (a *Agent) graphMemoryTokenBudget() int {
	if a.graphMemoryConfig == nil {
		return 0
	}
	if a.graphMemoryConfig.MaxContextTokens > 0 {
		return int(a.graphMemoryConfig.MaxContextTokens)
	}
	pct := a.graphMemoryConfig.ContextBudgetPercent
	if pct == 0 {
		pct = 10 // default 10%
	}
	if a.config.MaxContextTokens > 0 {
		return a.config.MaxContextTokens * int(pct) / 100
	}
	// Default: 200K context → 10% = 20K tokens
	return 200000 * int(pct) / 100
}

// FlushGraphMemoryExtraction blocks until all in-flight async graph memory
// extractions have completed. Call this before querying graph memory to
// ensure recently ingested content has been fully extracted.
func (a *Agent) FlushGraphMemoryExtraction() {
	a.graphExtractionWG.Wait()
}

// injectGraphMemoryContext queries graph memory for the current topic and injects
// relevant context into the conversation as a system message.
func (a *Agent) injectGraphMemoryContext(ctx context.Context, session *types.Session) {
	if a.graphMemoryStore == nil || a.graphMemoryConfig == nil || !a.graphMemoryConfig.Enabled {
		return
	}

	// Wait for any in-flight extractions to finish before querying.
	// This ensures recently ingested content is available for recall.
	a.graphExtractionWG.Wait()

	budget := a.graphMemoryTokenBudget()
	if budget <= 0 {
		return
	}

	// Get the last user message.
	var userMessage string
	msgs := session.GetMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content != "" {
			userMessage = msgs[i].Content
			break
		}
	}
	if userMessage == "" {
		return
	}

	// Use LLM to distill the user message into a search query for memory recall.
	// "What was the first issue I had with my new car after its first service?"
	// becomes something like "first issue with new car after first service".
	searchQuery := a.extractSearchQuery(ctx, userMessage)
	if searchQuery == "" {
		return
	}

	// Gather candidate memories from multiple sources.
	seen := make(map[string]bool)
	var candidates []*memory.Memory

	// Entity-scoped recall: search entities, get their neighborhoods.
	entities, err := a.graphMemoryStore.SearchEntities(ctx, a.config.Name, searchQuery, 5)
	if err == nil {
		for _, e := range entities {
			recall, err := a.graphMemoryStore.ContextFor(ctx, memory.ContextForOpts{
				AgentID:    a.config.Name,
				EntityName: e.Name,
				Topic:      searchQuery,
				MaxTokens:  budget,
			})
			if err == nil && recall != nil {
				for _, sm := range recall.Memories {
					if sm.Memory != nil && !seen[sm.Memory.ID] {
						seen[sm.Memory.ID] = true
						candidates = append(candidates, sm.Memory)
					}
				}
			}
		}
	}

	// Multi-hop recall via user entity: traverse 2 hops from the user node
	// to surface memories connected through shared relationships (e.g.,
	// user → ATTENDED → event_a, user → ATTENDED → event_b). This catches
	// facts that keyword search misses because they use different terms.
	candidates = append(candidates, a.multiHopRecall(ctx, seen, budget)...)

	// Unscoped recall: broad FTS5 search across all memories.
	memories, recallErr := a.graphMemoryStore.Recall(ctx, memory.RecallOpts{
		AgentID:   a.config.Name,
		Query:     searchQuery,
		Limit:     50,
		MaxTokens: budget,
	})
	if recallErr == nil {
		for _, m := range memories {
			if !seen[m.ID] {
				seen[m.ID] = true
				candidates = append(candidates, m)
			}
		}
	}

	if len(candidates) == 0 {
		return
	}

	// LLM re-rank: ask the LLM which candidates are actually relevant.
	relevant := a.rerankMemories(ctx, userMessage, candidates)
	if len(relevant) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("Relevant memories from past conversations:\n\n")
	for _, m := range relevant {
		sb.WriteString("- ")
		if m.EventDate != "" {
			// Surface the absolute date first so the answering LLM can order
			// facts lexicographically without re-resolving relative phrases
			// embedded in content.
			sb.WriteString("[")
			sb.WriteString(m.EventDate)
			sb.WriteString("] ")
		}
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}

	session.AddMessage(ctx, types.Message{
		Role:    "system",
		Content: "[Graph Memory Context]\n" + sb.String(),
	})
}

// multiHopRecall finds the user entity and traverses 2 hops outward to collect
// memories from connected entities. This surfaces facts that keyword search misses —
// e.g., "days between Holi and St. Mary's mass" requires both events, which are
// connected through the user entity but use different vocabulary.
func (a *Agent) multiHopRecall(ctx context.Context, seen map[string]bool, budget int) []*memory.Memory {
	if a.graphMemoryStore == nil {
		return nil
	}

	agentID := a.config.Name

	// Find the user entity (marked with is_user:true during extraction).
	userEntity := a.findUserEntity(ctx, agentID)
	if userEntity == nil {
		return nil
	}

	// Traverse 2 hops from the user entity in both directions.
	edges, err := a.graphMemoryStore.Neighbors(ctx, userEntity.ID, "", "both", 2)
	if err != nil || len(edges) == 0 {
		return nil
	}

	// Collect unique neighbor entity IDs (excluding user itself).
	neighborIDs := make(map[string]bool)
	for _, edge := range edges {
		if edge.SourceID != userEntity.ID {
			neighborIDs[edge.SourceID] = true
		}
		if edge.TargetID != userEntity.ID {
			neighborIDs[edge.TargetID] = true
		}
	}

	// Recall memories scoped to these neighbor entities.
	var entityIDs []string
	for id := range neighborIDs {
		entityIDs = append(entityIDs, id)
	}

	memories, err := a.graphMemoryStore.Recall(ctx, memory.RecallOpts{
		AgentID:   agentID,
		EntityIDs: entityIDs,
		Limit:     30,
		MaxTokens: budget,
	})
	if err != nil {
		return nil
	}

	var result []*memory.Memory
	for _, m := range memories {
		if !seen[m.ID] {
			seen[m.ID] = true
			result = append(result, m)
		}
	}

	return result
}

// findUserEntity locates the entity marked as the human user (is_user:true).
// Caches the result for the agent's lifetime to avoid repeated lookups.
func (a *Agent) findUserEntity(ctx context.Context, agentID string) *memory.Entity {
	// Check person entities for is_user property.
	entities, _, err := a.graphMemoryStore.ListEntities(ctx, agentID, "person", 20, 0)
	if err != nil {
		return nil
	}
	for _, e := range entities {
		if strings.Contains(e.PropertiesJSON, `"is_user":true`) ||
			strings.Contains(e.PropertiesJSON, `"is_user": true`) {
			return e
		}
	}
	return nil
}

// rerankMemories uses the LLM to select the most relevant memories for a user message
// from a pool of FTS5 candidates. Returns only the memories the LLM deems relevant.
func (a *Agent) rerankMemories(ctx context.Context, userMessage string, candidates []*memory.Memory) []*memory.Memory {
	if len(candidates) == 0 {
		return nil
	}

	rerankCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// Build numbered list of candidates.
	var sb strings.Builder
	sb.WriteString("User message:\n")
	sb.WriteString(userMessage)
	if len(userMessage) > 500 {
		sb.WriteString(userMessage[:500])
	} else {
		sb.WriteString(userMessage)
	}
	sb.WriteString("\n\nCandidate memories:\n")
	for i, m := range candidates {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, m.Content))
	}
	sb.WriteString("\nReturn ONLY the numbers of memories that are relevant to the user's message, ")
	sb.WriteString("comma-separated. Example: 1,3,7\n")
	sb.WriteString("If none are relevant, return: none")

	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}

	resp, err := llmProvider.Chat(rerankCtx, []types.Message{
		{Role: "user", Content: sb.String()},
	}, nil)
	if err != nil {
		// On failure, return all candidates (no worse than before).
		return candidates
	}

	// Parse the response — extract numbers.
	response := strings.TrimSpace(resp.Content)
	if strings.EqualFold(response, "none") {
		return nil
	}

	var result []*memory.Memory
	for _, part := range strings.Split(response, ",") {
		part = strings.TrimSpace(part)
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err == nil {
			if idx >= 1 && idx <= len(candidates) {
				result = append(result, candidates[idx-1])
			}
		}
	}

	if len(result) == 0 {
		// LLM returned something we couldn't parse — fall back to all candidates.
		return candidates
	}
	return result
}

// extractSearchQuery uses the LLM to distill a user message into a concise
// search query for memory recall. Returns a natural language query like
// "car GPS malfunction after March service" — not searchQuery, not the raw prompt.
func (a *Agent) extractSearchQuery(ctx context.Context, userMessage string) string {
	extractCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	prompt := fmt.Sprintf(
		"Distill this message into a concise search query for looking up relevant memories. "+
			"Return ONLY the search query, nothing else. Strip away instructions, "+
			"framing, and filler — just the core topic the user is asking about.\n\n"+
			"Message: %s\n\nSearch query:", userMessage)

	// Use compressor LLM if available (cheaper/faster), otherwise main LLM.
	llmProvider := a.llm
	if a.compressorLLM != nil {
		llmProvider = a.compressorLLM
	}

	resp, err := llmProvider.Chat(extractCtx, []types.Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return ""
	}

	query := strings.TrimSpace(resp.Content)
	if idx := strings.IndexByte(query, '\n'); idx > 0 {
		query = query[:idx]
	}
	return query
}

func (a *Agent) formatToolResult(ctx Context, sessionID string, toolName string, result *shuttle.Result, err error) string {
	// Arrival appends (HLD §1): the result enters the in-memory message WHOLE.
	// Nothing is examined, sized, flagged, or transformed at arrival — bounding
	// happens once, at persist (write rules §4), and large-result offload is a
	// pure render condition of ContextCompilation (§5.2), not an arrival event.
	_ = ctx
	_ = sessionID
	_ = toolName

	// An error result is a result that failed — same rule, no separate error
	// store (HLD §4.1).
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	if !result.Success {
		if result.Error != nil {
			return fmt.Sprintf("Tool error: %s - %s", result.Error.Code, result.Error.Message)
		}
		return "Tool execution failed"
	}

	if result.Data != nil {
		// Render a string result verbatim; marshal a composite result as JSON.
		// A Go map must never be rendered with %v.
		switch v := result.Data.(type) {
		case string:
			return v
		default:
			if b, marshalErr := json.Marshal(v); marshalErr == nil {
				return string(b)
			}
			return fmt.Sprintf("[unserializable result: %v]", result.Data)
		}
	}

	return "Success"
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
	// Drop the session's advertised-tool ledger too, or it grows unbounded on a
	// long-running multi-session server. scopedToolNames is process-global (a
	// name is scoped once any session scopes it) and is intentionally not pruned.
	a.mu.Lock()
	delete(a.sessionToolLedger, sessionID)
	a.mu.Unlock()
	// In-turn SQLite databases are normally dropped at the session's next turn
	// start; a deleted session has no next turn, so drop them here.
	a.dropInTurnSQLite(sessionID)
}

// ClearAllSessions removes all sessions from memory.
// Used by the benchmark server to free memory between scenarios.
func (a *Agent) ClearAllSessions() {
	a.memory.ClearAll()
	a.mu.Lock()
	a.sessionToolLedger = make(map[string]map[string]bool)
	a.mu.Unlock()
	a.dropAllInTurnSQLite()
}

// CreateSession creates a new session without sending a message to the LLM.
// Use this for session initialization; use Chat() for actual conversations.
// ctx carries user identity for RLS-scoped storage access.
// name is an optional human-readable session name.
func (a *Agent) CreateSession(ctx context.Context, sessionID, name string) *Session {
	// Use GetOrCreateSessionWithAgent to properly set agent_id in session metadata
	// This is critical for ReferenceStore namespacing and workflow sub-agent communication
	session := a.memory.GetOrCreateSessionWithAgent(ctx, sessionID, a.config.Name, "")
	if name != "" && session.Name == "" {
		session.Name = name
		// Persist updated name to store
		if a.memory.store != nil {
			_ = a.memory.store.SaveSession(ctx, session)
		}
	}
	return session
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

// SetPatternTracker wires a PatternEffectivenessTracker into this agent's
// orchestrator so that every pattern-guided turn records metrics to the
// pattern_effectiveness table. Safe to call with nil (no-op).
func (a *Agent) SetPatternTracker(tracker *learning.PatternEffectivenessTracker) {
	if a.orchestrator != nil && tracker != nil {
		a.orchestrator.WithTracker(tracker)
	}
}

// GetLLMProviderName returns the name of the LLM provider (e.g., "anthropic", "bedrock", "ollama").
func (a *Agent) GetLLMProviderName() string {
	a.mu.RLock()
	llm := a.llm
	a.mu.RUnlock()
	if llm == nil {
		return ""
	}
	return llm.Name()
}

// GetLLMModel returns the model identifier (e.g., "claude-3-5-sonnet-20241022").
func (a *Agent) GetLLMModel() string {
	a.mu.RLock()
	llm := a.llm
	a.mu.RUnlock()
	if llm == nil {
		return ""
	}
	return llm.Model()
}

// SetLLMProvider switches the main LLM provider for this agent.
// This allows mid-session model switching while preserving conversation context.
// The new provider will be used for all future LLM calls in all sessions.
// Only updates memory's LLM provider if no dedicated compressor LLM is set.
func (a *Agent) SetLLMProvider(llm LLMProvider) {
	a.mu.Lock()
	a.llm = llm
	compressorSet := a.compressorLLM != nil
	a.mu.Unlock()
	// Only update memory's LLM provider if no dedicated compressor exists
	if a.memory != nil && !compressorSet {
		a.memory.SetLLMProvider(llm)
	}
}

// GetLLMForRole returns the LLM provider for a specific role.
// Fallback chain: role-specific LLM -> main agent LLM.
func (a *Agent) GetLLMForRole(role loomv1.LLMRole) LLMProvider {
	a.mu.RLock()
	defer a.mu.RUnlock()

	switch role {
	case loomv1.LLMRole_LLM_ROLE_JUDGE:
		if a.judgeLLM != nil {
			return a.judgeLLM
		}
	case loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR:
		if a.orchestratorLLM != nil {
			return a.orchestratorLLM
		}
	case loomv1.LLMRole_LLM_ROLE_CLASSIFIER:
		if a.classifierLLM != nil {
			return a.classifierLLM
		}
	case loomv1.LLMRole_LLM_ROLE_COMPRESSOR:
		if a.compressorLLM != nil {
			return a.compressorLLM
		}
	case loomv1.LLMRole_LLM_ROLE_AGENT, loomv1.LLMRole_LLM_ROLE_UNSPECIFIED:
		// Fall through to return main LLM
	}
	return a.llm
}

// SetLLMProviderForRole sets the LLM provider for a specific role.
// For COMPRESSOR role, also updates memory's LLM provider.
// For AGENT/UNSPECIFIED role, delegates to SetLLMProvider.
func (a *Agent) SetLLMProviderForRole(role loomv1.LLMRole, llm LLMProvider) {
	a.mu.Lock()
	defer a.mu.Unlock()

	switch role {
	case loomv1.LLMRole_LLM_ROLE_JUDGE:
		a.judgeLLM = llm
	case loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR:
		a.orchestratorLLM = llm
	case loomv1.LLMRole_LLM_ROLE_CLASSIFIER:
		a.classifierLLM = llm
		// Update the orchestrator's intent classifier if pattern classification is enabled
		if a.orchestrator != nil && a.config.PatternConfig.UseLLMClassifier && llm != nil {
			llmClassifierConfig := patterns.DefaultLLMClassifierConfig(llm)
			llmClassifier := patterns.NewLLMIntentClassifier(llmClassifierConfig)
			a.orchestrator.SetIntentClassifier(llmClassifier)
		}
	case loomv1.LLMRole_LLM_ROLE_COMPRESSOR:
		a.compressorLLM = llm
		if a.memory != nil {
			a.memory.SetLLMProvider(llm)
		}
	case loomv1.LLMRole_LLM_ROLE_AGENT, loomv1.LLMRole_LLM_ROLE_UNSPECIFIED:
		a.llm = llm
		// Only update memory if no dedicated compressor
		if a.memory != nil && a.compressorLLM == nil {
			a.memory.SetLLMProvider(llm)
		}
	}
}

// GetLLMModelForRole returns the model identifier for a specific role's LLM.
func (a *Agent) GetLLMModelForRole(role loomv1.LLMRole) string {
	llm := a.GetLLMForRole(role)
	if llm == nil {
		return ""
	}
	return llm.Model()
}

// GetLLMProviderNameForRole returns the provider name for a specific role's LLM.
func (a *Agent) GetLLMProviderNameForRole(role loomv1.LLMRole) string {
	llm := a.GetLLMForRole(role)
	if llm == nil {
		return ""
	}
	return llm.Name()
}

// GetAllRoleLLMs returns all configured role-specific LLM providers (non-nil only).
// Always includes the main agent LLM. Used for health checks and diagnostics.
func (a *Agent) GetAllRoleLLMs() map[loomv1.LLMRole]LLMProvider {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make(map[loomv1.LLMRole]LLMProvider)
	if a.llm != nil {
		result[loomv1.LLMRole_LLM_ROLE_AGENT] = a.llm
	}
	if a.judgeLLM != nil {
		result[loomv1.LLMRole_LLM_ROLE_JUDGE] = a.judgeLLM
	}
	if a.orchestratorLLM != nil {
		result[loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR] = a.orchestratorLLM
	}
	if a.classifierLLM != nil {
		result[loomv1.LLMRole_LLM_ROLE_CLASSIFIER] = a.classifierLLM
	}
	if a.compressorLLM != nil {
		result[loomv1.LLMRole_LLM_ROLE_COMPRESSOR] = a.compressorLLM
	}
	return result
}

// SetSharedMemoryThreshold configures the byte threshold for storing large tool results in shared memory.
// -1 = use storage.DefaultSharedMemoryThreshold, 0 = always reference, >0 = reference only if result exceeds N bytes.
func (a *Agent) SetSharedMemoryThreshold(threshold int64) {
	a.sharedMemoryThreshold = threshold
	// Re-push to the executor: it captured the threshold at SetSharedMemory time,
	// so a setter call afterwards (the registry configures it post-construction)
	// would otherwise leave the two offload sites at different thresholds.
	if a.executor != nil && a.sharedMemory != nil {
		eff := int64(storage.DefaultSharedMemoryThreshold)
		if threshold >= 0 {
			eff = threshold
		}
		a.executor.SetSharedMemory(a.sharedMemory, eff)
	}
	// One value, three roles (HLD §5.1): the same bound is the compile-time
	// offload bound, the persist-time row bound, and the retrieval page bound.
	if a.memory != nil && threshold > 0 {
		a.memory.SetThresholdBytes(threshold)
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
		threshold := int64(storage.DefaultSharedMemoryThreshold)
		if a.sharedMemoryThreshold >= 0 {
			threshold = a.sharedMemoryThreshold
		}
		a.executor.SetSharedMemory(sharedMemory, threshold)
	}

	// Inject into memory manager (which handles all sessions)
	if a.memory != nil {
		a.memory.SetSharedMemory(sharedMemory)
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

// WithGraphMemoryStore sets the graph-backed episodic memory store.
func WithGraphMemoryStore(store memory.GraphMemoryStore, config *loomv1.GraphMemoryConfig) Option {
	return func(a *Agent) {
		a.graphMemoryStore = store
		a.graphMemoryConfig = config
	}
}

// WithTaskBoard sets the task manager, decomposer, and config for task decomposition and kanban.
func WithTaskBoard(manager *task.Manager, decomposer *task.Decomposer, config *loomv1.TaskBoardConfig) Option {
	return func(a *Agent) {
		a.taskManager = manager
		a.taskDecomposer = decomposer
		a.taskBoardConfig = config
	}
}

// WithEmbedder sets the vector embedding provider for semantic memory search.
func WithEmbedder(embedder memory.Embedder) Option {
	return func(a *Agent) {
		a.embedder = embedder
	}
}

// GetProviderPool returns the named provider pool (nil if not configured).
func (a *Agent) GetProviderPool() map[string]LLMProvider {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.providerPool
}

// GetActiveProviderName returns the currently active provider name.
func (a *Agent) GetActiveProviderName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeProviderName
}

// SetActiveProvider switches to a named provider from the pool.
// Returns an error if the name is not in the pool or not in the allowed list.
func (a *Agent) SetActiveProvider(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.providerPool == nil {
		return fmt.Errorf("provider pool not configured")
	}
	provider, ok := a.providerPool[name]
	if !ok {
		return fmt.Errorf("provider %q not found in pool", name)
	}
	// Check allowed list if restricted
	if len(a.allowedProviders) > 0 {
		allowed := false
		for _, ap := range a.allowedProviders {
			if ap == name {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("provider %q not in allowed providers list", name)
		}
	}
	a.llm = provider
	a.activeProviderName = name
	// Update memory's LLM provider if no dedicated compressor
	if a.memory != nil && a.compressorLLM == nil {
		a.memory.SetLLMProvider(provider)
	}
	return nil
}

// SetProviderPool configures the named provider pool and optional active provider.
func (a *Agent) SetProviderPool(pool map[string]LLMProvider, active string, allowed []string) error {
	a.mu.Lock()
	a.providerPool = pool
	a.allowedProviders = allowed
	a.mu.Unlock()

	if active != "" {
		return a.SetActiveProvider(active)
	}
	return nil
}
