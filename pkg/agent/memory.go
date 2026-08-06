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
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
)

// SystemPromptFunc is a function that returns the system prompt for a new session.
// It can be used to dynamically load prompts from a PromptRegistry or other source.
// Accepts context.Context to enable proper context propagation (e.g., for RLS user_id in PostgreSQL).
type SystemPromptFunc func(ctx context.Context) string

// MemoryObserver is called when messages are added to sessions.
// This enables real-time updates across multiple sessions viewing the same agent's memory.
type MemoryObserver interface {
	// OnMessageAdded is called when a message is added to any session for this agent
	OnMessageAdded(agentID string, sessionID string, msg Message)
}

// MemoryObserverFunc is a function adapter for MemoryObserver.
type MemoryObserverFunc func(agentID string, sessionID string, msg Message)

// OnMessageAdded implements MemoryObserver.
func (f MemoryObserverFunc) OnMessageAdded(agentID string, sessionID string, msg Message) {
	f(agentID, sessionID, msg)
}

// Memory manages conversation sessions and history.
// Supports optional persistent storage via SessionStorage interface.
type Memory struct {
	mu                   sync.RWMutex
	sessions             map[string]*Session
	store                SessionStorage             // Optional persistent storage (SQLite, PostgreSQL, etc.)
	sharedMemory         *storage.SharedMemoryStore // Optional shared memory for large data
	systemPromptFunc     SystemPromptFunc           // Optional function to generate system prompts
	tracer               observability.Tracer       // Optional tracer for observability
	logger               *zap.Logger                // Structured logger for storage errors
	llmProvider          LLMProvider                // Optional LLM provider for semantic search reranking
	maxContextTokens     int                        // Context window size for new sessions (0 = use defaults)
	reservedOutputTokens int                        // Reserved tokens for output (0 = use defaults)
	compressionProfile   *CompressionProfile        // Optional compression profile for new sessions (nil = use defaults)
	compressor           MemoryCompressor           // Optional LLM compressor for L2 compaction (nil = heuristic fallback)
	thresholdBytes       int64                      // Offload / row / page bound in bytes (0 = default; HLD §5.1)

	// Real-time observers for cross-session updates
	// Map of agentID -> list of observers
	observers   map[string][]MemoryObserver
	observersMu sync.RWMutex

	// Restore re-fire hooks (set by the agent layer, which owns the skill
	// orchestrator and the per-session tool ledger). After a restart replay,
	// the restore walk calls these to reconstruct a session's runtime state
	// from its durable messages. nil hooks disable the corresponding re-fire.
	restoreActivateSkill func(sessionID, skillName string) // re-activate a loaded skill + wire its required tools

	// Per-mutation debug carrier, forwarded to each SegmentedMemory this manager
	// builds and read by the restore re-fire pass. nil or off is a no-op.
	ctxDebug *contextDebug

	// skillDeactivation is forwarded to each SegmentedMemory (fold's skill
	// deactivation path, HLD §4.5).
	skillDeactivation func(sessionID, skillName string)

	// protectedRecentTurns is K (HLD §5.1), forwarded to each SegmentedMemory.
	protectedRecentTurns int
}

// NewMemory creates a new in-memory session manager.
// Uses zap.L() (the global logger) by default, so storage errors are visible
// if a global logger has been configured (e.g., via zap.ReplaceGlobals).
// If no global logger is configured, zap.L() returns a no-op logger.
// Call SetLogger() to inject an explicit logger instance.
func NewMemory() *Memory {
	return &Memory{
		sessions:  make(map[string]*Session),
		store:     nil,
		logger:    zap.L(),
		observers: make(map[string][]MemoryObserver),
	}
}

// NewMemoryWithStore creates a memory manager with persistent storage.
// Uses zap.L() (the global logger) by default, so storage errors are visible
// if a global logger has been configured (e.g., via zap.ReplaceGlobals).
// If no global logger is configured, zap.L() returns a no-op logger.
// Call SetLogger() to inject an explicit logger instance.
func NewMemoryWithStore(store SessionStorage) *Memory {
	return &Memory{
		sessions:  make(map[string]*Session),
		store:     store,
		logger:    zap.L(),
		observers: make(map[string][]MemoryObserver),
	}
}

// SetLogger sets the structured logger for storage error reporting.
func (m *Memory) SetLogger(logger *zap.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = logger
}

// SetSystemPromptFunc sets a function to generate system prompts for new sessions.
// This allows dynamic prompt loading from PromptRegistry or other sources.
func (m *Memory) SetSystemPromptFunc(fn SystemPromptFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.systemPromptFunc = fn
}

// SetContextLimits sets the context window size and output reservation for new sessions.
// If maxContextTokens is 0, defaults will be used (200K for backwards compatibility).
// If reservedOutputTokens is 0, it will be calculated as 10% of maxContextTokens.
func (m *Memory) SetContextLimits(maxContextTokens, reservedOutputTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxContextTokens = maxContextTokens
	// 0 means "unset" — it must not erase a reservation an earlier caller
	// computed. Agent construction re-applies these limits from its config, and
	// a zero there would silently drop the reserve back to window/10, putting
	// the relief marks above the provider's refusal line (HLD §5.1 "usable").
	if reservedOutputTokens > 0 {
		m.reservedOutputTokens = reservedOutputTokens
	}
}

// SetCompressionProfile sets the compression profile for new sessions.
// This controls compression behavior (thresholds, batch sizes) for memory management.
// If profile is nil, balanced profile defaults will be used.
func (m *Memory) SetCompressionProfile(profile *CompressionProfile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compressionProfile = profile
}

// SetRestoreReFireHooks wires the callback the restore replay uses to rebuild
// a session's runtime state from its durable messages: activateSkill re-fires a
// load marker (no-evict activation + required-tool wiring). The agent layer
// supplies it because it touches the skill orchestrator and the per-session
// tool ledger. Disclosure re-fire is deleted (HLD §8): refs never survive a
// turn, so there is nothing to re-advertise on restore, and the error store is
// gone.
func (m *Memory) SetRestoreReFireHooks(activateSkill func(sessionID, skillName string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restoreActivateSkill = activateSkill
}

// SetContextDebug injects the per-mutation debug carrier. It is forwarded to
// every SegmentedMemory this manager builds and read by the restore re-fire
// pass, so compaction and re-fire logs share the agent's context-dump switch.
func (m *Memory) SetContextDebug(cd *contextDebug) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctxDebug = cd
}

// SetThresholdBytes sets the one threshold value (HLD §5.1: compile-time offload
// bound, persist-time row bound, retrieval page bound) for existing and future
// sessions.
func (m *Memory) SetThresholdBytes(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bytes <= 0 {
		return
	}
	if bytes < minThreshold {
		bytes = minThreshold // the §4.1 tail alone needs room; see minThreshold
	}
	m.thresholdBytes = bytes
	for _, session := range m.sessions {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			segMem.SetThreshold(bytes)
		}
	}
}

// thresholdOrDefault returns the configured threshold, else the default bound.
// Never below minThreshold: this value is the persist-time row bound, and a
// smaller one cannot hold stored = core + tail ≤ threshold (§4.1).
func (m *Memory) thresholdOrDefault() int {
	if m.thresholdBytes > 0 {
		if m.thresholdBytes < minThreshold {
			return minThreshold
		}
		return int(m.thresholdBytes)
	}
	return int(storage.DefaultSharedMemoryThreshold)
}

// sessionCurrentTurn returns T — the session's current turn number — derived as
// max(Turn) over the session's messages (HLD §5.1; no counter, no session state).
func sessionCurrentTurn(session *Session) int64 {
	if session == nil {
		return 0
	}
	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		return segMem.CurrentTurn()
	}
	var t int64
	for _, m := range session.GetMessages() {
		if m.Turn > t {
			t = m.Turn
		}
	}
	return t
}

// GetOrCreateSession gets an existing session or creates a new one.
// If persistent storage is configured, attempts to load from database first.
// ctx is threaded through to storage operations to enable RLS user isolation.
func (m *Memory) GetOrCreateSession(ctx context.Context, sessionID string) *Session {
	return m.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
}

// GetOrCreateSessionWithAgent gets an existing session or creates a new one with agent metadata.
// This is used for multi-agent workflows where sub-agents need to access parent sessions.
// ctx is threaded through to storage operations to enable RLS user isolation.
// Parameters:
//   - ctx: Context with user identity for RLS-scoped storage access
//   - sessionID: Unique session identifier
//   - agentID: Agent identity (e.g., "coordinator", "analyzer-sub-agent")
//   - parentSessionID: Parent session ID (for sub-agents to access coordinator session)
func (m *Memory) GetOrCreateSessionWithAgent(ctx context.Context, sessionID, agentID, parentSessionID string) *Session {
	// Fast path: read-lock check for existing session (most common case in
	// multi-turn conversations). No write lock needed if session already exists
	// and doesn't need metadata updates.
	m.mu.RLock()
	if session, ok := m.sessions[sessionID]; ok {
		needsUpdate := (agentID != "" && session.AgentID == "") ||
			(parentSessionID != "" && session.ParentSessionID == "") ||
			session.SegmentedMem == nil ||
			session.FailureTracker == nil
		if !needsUpdate {
			m.mu.RUnlock()
			return session
		}
	}
	m.mu.RUnlock()

	// Slow path: need to create or update. Read Memory config under read lock
	// first (to snapshot configuration), then build the session without any lock.
	m.mu.RLock()
	store := m.store
	sharedMem := m.sharedMemory
	sysFn := m.systemPromptFunc
	tracer := m.tracer
	llmProv := m.llmProvider
	maxCtx := m.maxContextTokens
	reservedOut := m.reservedOutputTokens
	compProfile := m.compressionProfile
	compressor := m.compressor
	thresholdBytes := m.thresholdBytes
	logger := m.logger
	ctxDebug := m.ctxDebug
	skillDeactivation := m.skillDeactivation
	protectedTurns := m.protectedRecentTurns
	m.mu.RUnlock()

	// Check write-lock path: existing session that needs metadata update
	m.mu.Lock()

	// Double-check: another goroutine may have created it while we waited for the write lock.
	// Defensive: also ensure SegmentedMem/FailureTracker are present — in the common
	// case they already are (ensureSessionMemory is a no-op), but this protects against
	// edge cases where a cached session has been stripped of these fields.
	if session, ok := m.sessions[sessionID]; ok {
		m.updateSessionMetadata(session, agentID, parentSessionID, store, ctx, logger)
		m.ensureSessionMemory(session, sessionID, sysFn, compProfile, maxCtx, reservedOut,
			sharedMem, store, tracer, llmProv, compressor, ctx)
		if session.FailureTracker == nil {
			session.FailureTracker = newConsecutiveFailureTracker()
		}
		m.mu.Unlock()
		return session
	}

	// Try loading from persistent store (still under write lock to prevent
	// duplicate loads, but store.LoadSession is typically fast for cache misses)
	if store != nil {
		session, err := store.LoadSession(ctx, sessionID)
		if err == nil && session != nil {
			m.updateSessionMetadata(session, agentID, parentSessionID, store, ctx, logger)
			// Sessions loaded from DB don't have SegmentedMem/FailureTracker
			// (they aren't persisted). Recreate them so compression and error
			// tracking continue to work across restarts.
			needsReplay := session.SegmentedMem == nil
			m.ensureSessionMemory(session, sessionID, sysFn, compProfile, maxCtx, reservedOut,
				sharedMem, store, tracer, llmProv, compressor, ctx)
			if session.FailureTracker == nil {
				session.FailureTracker = newConsecutiveFailureTracker()
			}
			// Reload is a read (HLD §8): the rows come back already filtered
			// folded=false and seq-ordered; ReplayMessages is a pure bulk
			// append; the newest 'fold' snapshot is the summary. No compaction,
			// no compressor calls on load — live and restored are the same read
			// of the same rows.
			var restoreSnapshot []Message
			var replayInto *SegmentedMemory
			var activateSkill func(sessionID, skillName string)
			if needsReplay {
				if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
					replayInto = segMem
				}
				restoreSnapshot = make([]Message, len(session.Messages))
				copy(restoreSnapshot, session.Messages)
				activateSkill = m.restoreActivateSkill
			}
			m.sessions[sessionID] = session
			m.mu.Unlock()

			if replayInto != nil {
				replayInto.ReplayMessages(ctx, restoreSnapshot)
				m.loadSummary(ctx, store, sessionID, replayInto)
			}

			// Restore re-fire runs after replay and outside the lock,
			// reconstructing skill activations from the durable snapshot.
			if activateSkill != nil {
				m.reFireOnRestore(sessionID, restoreSnapshot, activateSkill, ctxDebug)
			}
			return session
		}
	}

	// Not found in cache or store. Release the lock so other goroutines
	// working on different sessions aren't blocked while we build the
	// SegmentedMemory (which involves tiktoken calls).
	m.mu.Unlock()

	// Build SegmentedMemory OUTSIDE any lock — this is the expensive part.
	romContent := "Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return."
	if sysFn != nil {
		romContent = sysFn(ctx)
	}

	var segMem *SegmentedMemory
	if compProfile != nil {
		segMem = NewSegmentedMemoryWithCompression(romContent, maxCtx, reservedOut, *compProfile)
	} else {
		segMem = NewSegmentedMemory(romContent, maxCtx, reservedOut)
	}

	// Inject dependencies
	if sharedMem != nil {
		segMem.SetSharedMemory(sharedMem)
	}
	if store != nil {
		segMem.SetSessionStore(store, sessionID)
	}
	if tracer != nil {
		segMem.SetTracer(tracer)
	}
	if llmProv != nil {
		segMem.SetLLMProvider(llmProv)
	}
	if compressor != nil {
		segMem.SetCompressor(compressor)
	}
	if thresholdBytes > 0 {
		segMem.SetThreshold(thresholdBytes)
	}
	segMem.SetContextDebug(ctxDebug)
	segMem.SetSkillDeactivationHook(skillDeactivation)
	if protectedTurns > 0 {
		segMem.SetProtectedRecentTurns(protectedTurns)
	}

	session := &Session{
		ID:              sessionID,
		AgentID:         agentID,
		ParentSessionID: parentSessionID,
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		SegmentedMem:    segMem,
		FailureTracker:  newConsecutiveFailureTracker(),
	}

	// Insert fully-built session under write lock.
	// Double-check: another goroutine may have created the same session
	// while we were building SegmentedMemory.
	m.mu.Lock()
	if existing, ok := m.sessions[sessionID]; ok {
		// Another goroutine beat us — use theirs, discard ours.
		m.mu.Unlock()
		return existing
	}
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Persist to store if configured (outside lock)
	if store != nil {
		_ = store.SaveSession(ctx, session)
	}

	return session
}

// reFireOnRestore reconstructs a restored session's runtime state from its
// durable messages. It walks the durable snapshot (never the compacted L1/L2,
// where load markers are gone) and, via the injected hook, re-activates each
// loaded skill with its required tools. It performs activation and tool
// advertisement only — no conversation message, no task — so a replayed
// session advertises the same tools and reports the same active skills as a
// live one.
func (m *Memory) reFireOnRestore(sessionID string, messages []Message,
	activateSkill func(sessionID, skillName string),
	ctxDebug *contextDebug,
) {
	// The durable re-fire key (HLD §8, blueprint A8): an assistant row's
	// tool_calls entry with name=manage_skills, input.action=load, paired by
	// tool_use_id with a tool row whose content starts with "Skill loaded: " —
	// the confirmation manage_skills already emits. This key survives cloud
	// persistence, which never stores ToolResult metadata.
	loadCalls := make(map[string]string) // tool_use_id → skill name
	for i := range messages {
		for _, c := range messages[i].ToolCalls {
			if c.Name != "manage_skills" || c.ID == "" {
				continue
			}
			if action, _ := c.Input["action"].(string); action != "load" {
				continue
			}
			if name, _ := c.Input["name"].(string); name != "" {
				loadCalls[c.ID] = name
			}
		}
	}

	loadsReactivated := 0
	refired := make(map[string]bool)
	for i := range messages {
		msg := messages[i]
		if msg.Role != "tool" || msg.ToolUseID == "" || activateSkill == nil {
			continue
		}
		name := loadCalls[msg.ToolUseID]
		if name == "" || refired[name] {
			continue
		}
		if strings.HasPrefix(msg.Content, "Skill loaded: ") {
			refired[name] = true
			activateSkill(sessionID, name)
			loadsReactivated++
		}
	}

	// Mutation-debug: the runtime state this restore pass reconstructed.
	// No-op unless the context-dump switch is on.
	if ctxDebug.on() {
		zap.L().Debug("context mutation: restore re-fire",
			zap.String("session_id", sessionID),
			zap.Int("turn", ctxDebug.turn(sessionID)),
			zap.Int("loads_reactivated", loadsReactivated))
	}
}

// loadSummary installs the newest 'fold' summary version from the snapshot
// table (HLD §4.6, §8): rows are {"n","text"} JSON under snapshot_type='fold';
// the renderer reads max(n).
func (m *Memory) loadSummary(ctx context.Context, store SessionStorage, sessionID string, segMem *SegmentedMemory) {
	if store == nil || segMem == nil {
		return
	}
	snapshots, err := store.LoadMemorySnapshots(ctx, sessionID, "fold", 0)
	if err != nil {
		m.logger.Warn("Failed to load summary versions",
			zap.String("session_id", sessionID),
			zap.Error(err))
		return
	}
	bestN := 0
	bestText := ""
	for _, snap := range snapshots {
		var v struct {
			N    int    `json:"n"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(snap.Content), &v); err != nil {
			continue
		}
		if v.N > bestN {
			bestN = v.N
			bestText = v.Text
		}
	}
	if bestN > 0 {
		segMem.setSummary(bestN, bestText)
	}
}

// SetProtectedRecentTurns configures K — protected newest user turns (HLD
// §5.1; config ProtectedRecentTurns) — for existing and future sessions.
func (m *Memory) SetProtectedRecentTurns(k int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k <= 0 {
		return
	}
	m.protectedRecentTurns = k
	for _, session := range m.sessions {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			segMem.SetProtectedRecentTurns(k)
		}
	}
}

// SetSkillDeactivationHook wires the skills orchestrator's deactivation path
// into every session's memory (existing and future): fold deactivates skills
// whose manage_skills load pair lies inside the folded region (HLD §4.5).
func (m *Memory) SetSkillDeactivationHook(fn func(sessionID, skillName string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skillDeactivation = fn
	for _, session := range m.sessions {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
			segMem.SetSkillDeactivationHook(fn)
		}
	}
}

// updateSessionMetadata updates agent/parent metadata if not already set (must hold m.mu write lock or session-specific lock).
func (m *Memory) updateSessionMetadata(session *Session, agentID, parentSessionID string, store SessionStorage, ctx context.Context, logger *zap.Logger) {
	updated := false
	if agentID != "" && session.AgentID == "" {
		session.AgentID = agentID
		updated = true
	}
	if parentSessionID != "" && session.ParentSessionID == "" {
		session.ParentSessionID = parentSessionID
		updated = true
	}
	if updated && store != nil {
		if err := store.SaveSession(ctx, session); err != nil {
			logger.Warn("Failed to persist session metadata",
				zap.String("session_id", session.ID),
				zap.Error(err))
		}
	}
}

// ensureSessionMemory initializes SegmentedMemory if nil (must hold m.mu write lock or session-specific lock).
func (m *Memory) ensureSessionMemory(session *Session, sessionID string,
	sysFn SystemPromptFunc, compProfile *CompressionProfile,
	maxCtx, reservedOut int,
	sharedMem *storage.SharedMemoryStore, store SessionStorage,
	tracer observability.Tracer, llmProv LLMProvider, compressor MemoryCompressor,
	ctx context.Context,
) {
	if session.SegmentedMem != nil {
		return
	}

	romContent := "Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return."
	if sysFn != nil {
		romContent = sysFn(ctx)
	}

	if compProfile != nil {
		session.SegmentedMem = NewSegmentedMemoryWithCompression(romContent, maxCtx, reservedOut, *compProfile)
	} else {
		session.SegmentedMem = NewSegmentedMemory(romContent, maxCtx, reservedOut)
	}

	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
		if sharedMem != nil {
			segMem.SetSharedMemory(sharedMem)
		}
		if store != nil {
			segMem.SetSessionStore(store, sessionID)
		}
		if tracer != nil {
			segMem.SetTracer(tracer)
		}
		if llmProv != nil {
			segMem.SetLLMProvider(llmProv)
		}
		if compressor != nil {
			segMem.SetCompressor(compressor)
		}
		// m.mu is held by the caller, so m.thresholdBytes is read safely here.
		if m.thresholdBytes > 0 {
			segMem.SetThreshold(m.thresholdBytes)
		}
		segMem.SetContextDebug(m.ctxDebug)
		segMem.SetSkillDeactivationHook(m.skillDeactivation)
		if m.protectedRecentTurns > 0 {
			segMem.SetProtectedRecentTurns(m.protectedRecentTurns)
		}
	}
}

// GetSession retrieves a session by ID.
func (m *Memory) GetSession(sessionID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[sessionID]
	return session, ok
}

// DeleteSession removes a session.
func (m *Memory) DeleteSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, sessionID)
}

// ListSessions returns all active sessions.
func (m *Memory) ListSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}

	return sessions
}

// CountSessions returns the number of active sessions.
func (m *Memory) CountSessions() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.sessions)
}

// ClearAll removes all sessions from memory (does not affect persistent store).
func (m *Memory) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions = make(map[string]*Session)
}

// PersistSession saves a session to persistent storage if configured.
func (m *Memory) PersistSession(ctx context.Context, session *Session) error {
	if m.store == nil {
		return nil // No-op if no store configured
	}
	return m.store.SaveSession(ctx, session)
}

// PersistMessage saves a message's durable row, once, applying the fixed write
// rules of HLD §4 to the stored copy — the in-memory message stays whole. The
// store derives the row's seq and turn at insert and stamps both back onto msg;
// turnStart is passed only by the Chat()-entry persist site — the only
// turn-incrementing event.
//
// Write rules applied here (loom's single persist seam):
//   - §4.1 truncation: every tool result row is bounded at the threshold,
//     tail included (core cut backward to a rune boundary + normative tail).
//   - §4.3 retrieval pairs are not persisted: query_tool_result entries are
//     filtered from an assistant row's calls; tool rows whose tool_use_id
//     matches a filtered entry are not persisted; an assistant message left
//     with no text and no calls is not persisted. Both sides always — an
//     orphaned pair breaks replay at the API.
func (m *Memory) PersistMessage(ctx context.Context, sessionID string, msg *Message, turnStart bool) error {
	if m.store == nil {
		return nil // No-op if no store configured
	}

	threshold := m.thresholdOrDefault()

	switch msg.Role {
	case "assistant":
		filtered := make([]types.ToolCall, 0, len(msg.ToolCalls))
		for _, c := range msg.ToolCalls {
			if c.Name == "query_tool_result" {
				continue
			}
			filtered = append(filtered, c)
		}
		if msg.Content == "" && len(filtered) == 0 && len(msg.ContentBlocks) == 0 {
			// §4.3: an assistant message left with no text and no calls is not
			// persisted. In memory the message stays intact for the turn.
			return nil
		}
		stored := *msg
		stored.ToolCalls = filtered
		if err := m.store.SaveMessage(ctx, sessionID, &stored, turnStart); err != nil {
			return err
		}
		msg.ID = stored.ID
		msg.Turn = stored.Turn
		return nil

	case "tool":
		if m.isRetrievalPairResult(sessionID, msg.ToolUseID) {
			// §4.3: the result side of a filtered query_tool_result pair is not
			// persisted; in memory the pair stays intact for the producing turn.
			return nil
		}
		stored := *msg
		stored.Content = truncateToolRowContent(stored.Content, threshold)
		// The raw ToolResult record rides the row as tool_result_json; a payload
		// copy above the threshold would defeat the §4.1 row bound, so it is
		// dropped from the stored copy (content already carries the rendered
		// form; recovery is re-run).
		if stored.ToolResult != nil {
			if raw, err := json.Marshal(stored.ToolResult); err != nil || len(raw) > threshold {
				stored.ToolResult = nil
			}
		}
		if err := m.store.SaveMessage(ctx, sessionID, &stored, turnStart); err != nil {
			return err
		}
		msg.ID = stored.ID
		msg.Turn = stored.Turn
		return nil

	default:
		return m.store.SaveMessage(ctx, sessionID, msg, turnStart)
	}
}

// isRetrievalPairResult reports whether the tool row identified by toolUseID is
// the result side of a query_tool_result pair (§4.3). The pairing assistant
// message is already in the session's in-memory history — calls persist after
// their assistant row and within the producing turn.
func (m *Memory) isRetrievalPairResult(sessionID, toolUseID string) bool {
	if toolUseID == "" {
		return false
	}
	m.mu.RLock()
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil {
		return false
	}
	var msgs []Message
	if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok && segMem != nil {
		msgs = segMem.GetMessages()
	} else {
		msgs = session.GetMessages()
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		for _, c := range msgs[i].ToolCalls {
			if c.ID == toolUseID {
				return c.Name == "query_tool_result"
			}
		}
	}
	return false
}

// PersistToolExecution saves a tool execution to persistent storage if configured.
func (m *Memory) PersistToolExecution(ctx context.Context, sessionID string, exec ToolExecution) error {
	if m.store == nil {
		return nil // No-op if no store configured
	}
	return m.store.SaveToolExecution(ctx, sessionID, exec)
}

// SetSharedMemory configures shared memory for all sessions.
// This will inject the shared memory into all existing sessions
// and ensure future sessions also get it.
func (m *Memory) SetSharedMemory(sharedMemory *storage.SharedMemoryStore) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sharedMemory = sharedMemory

	// Inject into all existing sessions
	for _, session := range m.sessions {
		if session.SegmentedMem != nil {
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				segMem.SetSharedMemory(sharedMemory)
			}
		}
	}
}

// SetTracer sets the observability tracer for all sessions (existing and future).
// This enables error logging and metrics collection for memory operations.
func (m *Memory) SetTracer(tracer observability.Tracer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tracer = tracer

	// Inject into all existing sessions
	for _, session := range m.sessions {
		if session.SegmentedMem != nil {
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				segMem.SetTracer(tracer)
			}
		}
	}
}

// GetStore returns the SessionStorage if persistence is enabled, nil otherwise.
// Used for registering cleanup hooks and accessing persistence layer.
func (m *Memory) GetStore() SessionStorage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store
}

// SetCompressor sets the LLM-powered compressor for L2 compaction across all
// sessions (existing and future). With a compressor present, compaction routes
// through it; sessions without one fall back to the heuristic summariser.
func (m *Memory) SetCompressor(compressor MemoryCompressor) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.compressor = compressor

	// Inject into all existing sessions
	for _, session := range m.sessions {
		if session.SegmentedMem != nil {
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				segMem.SetCompressor(compressor)
			}
		}
	}
}

// SetLLMProvider sets the LLM provider for semantic search reranking (existing and future sessions).
// This enables LLM-based relevance scoring to improve search quality beyond BM25 keyword matching.
func (m *Memory) SetLLMProvider(llm LLMProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.llmProvider = llm

	// Inject into all existing sessions
	for _, session := range m.sessions {
		if session.SegmentedMem != nil {
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				segMem.SetLLMProvider(llm)
			}
		}
	}
}

// AddMessage adds a message to a session and notifies observers.
// This is the preferred way to add messages when real-time updates are needed.
// Falls back to session.AddMessage if session not found in Memory.
// ctx is threaded through to enable RLS-aware storage operations.
func (m *Memory) AddMessage(ctx context.Context, sessionID string, msg Message) {
	m.mu.RLock()
	session, found := m.sessions[sessionID]
	m.mu.RUnlock()

	if !found {
		// Session not in memory, nothing to do (will be handled by caller)
		return
	}

	// Stamp the turn (derived, never counted — HLD §4.5) and persist BEFORE the
	// in-memory append so the store-derived seq and turn land on the copy that
	// enters L1. PersistMessage applies the §4 write rules.
	msg.Turn = sessionCurrentTurn(session)
	if err := m.PersistMessage(ctx, sessionID, &msg, false); err != nil {
		m.logger.Warn("Failed to persist message to storage",
			zap.String("session_id", sessionID),
			zap.String("role", msg.Role),
			zap.Error(err))
	}

	// Add message to session (this handles SegmentedMem if configured)
	session.AddMessage(ctx, msg)

	// Notify observers if session has an agent_id
	if session.AgentID != "" {
		m.notifyObservers(session.AgentID, sessionID, msg)
	}
}

// RegisterObserver registers an observer for a specific agent's memory updates.
// The observer will be notified when messages are added to any session for this agent.
// This enables real-time cross-session updates.
func (m *Memory) RegisterObserver(agentID string, observer MemoryObserver) {
	m.observersMu.Lock()
	defer m.observersMu.Unlock()

	if m.observers == nil {
		m.observers = make(map[string][]MemoryObserver)
	}

	m.observers[agentID] = append(m.observers[agentID], observer)
}

// UnregisterObserver removes an observer for a specific agent.
// Note: This does a simple identity comparison, so the same observer instance must be passed.
func (m *Memory) UnregisterObserver(agentID string, observer MemoryObserver) {
	m.observersMu.Lock()
	defer m.observersMu.Unlock()

	observers := m.observers[agentID]
	for i, obs := range observers {
		if obs == observer {
			// Remove by swapping with last element and truncating
			m.observers[agentID] = append(observers[:i], observers[i+1:]...)
			break
		}
	}

	// Clean up empty observer lists
	if len(m.observers[agentID]) == 0 {
		delete(m.observers, agentID)
	}
}

// notifyObservers notifies all registered observers for an agent when a message is added.
// This is called internally after a message is saved to a session.
func (m *Memory) notifyObservers(agentID string, sessionID string, msg Message) {
	if agentID == "" {
		return // No agent ID, no observers to notify
	}

	m.observersMu.RLock()
	observers := m.observers[agentID]
	m.observersMu.RUnlock()

	// Notify observers asynchronously to avoid blocking message save
	for _, observer := range observers {
		go func(obs MemoryObserver) {
			obs.OnMessageAdded(agentID, sessionID, msg)
		}(observer)
	}
}
