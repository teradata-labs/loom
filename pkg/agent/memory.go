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
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage"
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
	maxToolResults       int                        // Max tool results in kernel (0 = use default)

	// Real-time observers for cross-session updates
	// Map of agentID -> list of observers
	observers   map[string][]MemoryObserver
	observersMu sync.RWMutex
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
	m.reservedOutputTokens = reservedOutputTokens
}

// SetCompressionProfile sets the compression profile for new sessions.
// This controls compression behavior (thresholds, batch sizes) for memory management.
// If profile is nil, balanced profile defaults will be used.
func (m *Memory) SetCompressionProfile(profile *CompressionProfile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compressionProfile = profile
}

// SetMaxToolResults configures how many tool results to keep in the conversation kernel.
// 0 = use default (5).
func (m *Memory) SetMaxToolResults(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxToolResults = n
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
	maxToolRes := m.maxToolResults
	logger := m.logger
	m.mu.RUnlock()

	// Check write-lock path: existing session that needs metadata update
	m.mu.Lock()

	// Double-check: another goroutine may have created it while we waited for the write lock.
	// Sessions are always inserted fully built (SegmentedMem != nil), so we only need to
	// update metadata under the lock — never mutate SegmentedMem on a live session.
	if session, ok := m.sessions[sessionID]; ok {
		m.updateSessionMetadata(session, agentID, parentSessionID, store, ctx, logger)
		m.mu.Unlock()
		return session
	}

	// Try loading from persistent store (still under write lock to prevent
	// duplicate loads, but store.LoadSession is typically fast for cache misses)
	if store != nil {
		session, err := store.LoadSession(ctx, sessionID)
		if err == nil && session != nil {
			m.updateSessionMetadata(session, agentID, parentSessionID, store, ctx, logger)
			m.ensureSessionMemory(session, sessionID, sysFn, compProfile, maxCtx, reservedOut,
				sharedMem, store, tracer, llmProv, maxToolRes, ctx)
			if session.FailureTracker == nil {
				session.FailureTracker = newConsecutiveFailureTracker()
			}
			// Replay loaded messages into SegmentedMem
			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				for _, msg := range session.Messages {
					segMem.AddMessage(ctx, msg)
				}
			}
			m.sessions[sessionID] = session
			m.mu.Unlock()
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
	if maxToolRes > 0 {
		segMem.maxToolResults = maxToolRes
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
	tracer observability.Tracer, llmProv LLMProvider, maxToolRes int,
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
		if maxToolRes > 0 {
			segMem.maxToolResults = maxToolRes
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

// PersistMessage saves a message to persistent storage if configured.
func (m *Memory) PersistMessage(ctx context.Context, sessionID string, msg Message) error {
	if m.store == nil {
		return nil // No-op if no store configured
	}
	return m.store.SaveMessage(ctx, sessionID, msg)
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

	// Add message to session (this handles SegmentedMem if configured)
	session.AddMessage(ctx, msg)

	// Persist to store if configured
	if m.store != nil {
		if err := m.store.SaveMessage(ctx, sessionID, msg); err != nil {
			m.logger.Warn("Failed to persist message to storage",
				zap.String("session_id", sessionID),
				zap.String("role", msg.Role),
				zap.Error(err))
		}
	}

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
