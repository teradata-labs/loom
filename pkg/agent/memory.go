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
)

// SystemPromptFunc is a function that returns the system prompt for a new session.
// It can be used to dynamically load prompts from a PromptRegistry or other source.
type SystemPromptFunc func() string

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
// Supports optional persistent storage via SessionStore.
type Memory struct {
	mu                   sync.RWMutex
	sessions             map[string]*Session
	store                *SessionStore              // Optional persistent storage
	sharedMemory         *storage.SharedMemoryStore // Optional shared memory for large data
	systemPromptFunc     SystemPromptFunc           // Optional function to generate system prompts
	tracer               observability.Tracer       // Optional tracer for observability
	llmProvider          LLMProvider                // Optional LLM provider for semantic search reranking
	maxContextTokens     int                        // Context window size for new sessions (0 = use defaults)
	reservedOutputTokens int                        // Reserved tokens for output (0 = use defaults)
	compressionProfile   *CompressionProfile        // Optional compression profile for new sessions (nil = use defaults)

	// Real-time observers for cross-session updates
	// Map of agentID -> list of observers
	observers   map[string][]MemoryObserver
	observersMu sync.RWMutex
}

// NewMemory creates a new in-memory session manager.
func NewMemory() *Memory {
	return &Memory{
		sessions:  make(map[string]*Session),
		store:     nil,
		observers: make(map[string][]MemoryObserver),
	}
}

// NewMemoryWithStore creates a memory manager with persistent storage.
func NewMemoryWithStore(store *SessionStore) *Memory {
	return &Memory{
		sessions:  make(map[string]*Session),
		store:     store,
		observers: make(map[string][]MemoryObserver),
	}
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

// GetOrCreateSession gets an existing session or creates a new one.
// If persistent storage is configured, attempts to load from database first.
func (m *Memory) GetOrCreateSession(sessionID string) *Session {
	return m.GetOrCreateSessionWithAgent(sessionID, "", "")
}

// GetOrCreateSessionWithAgent gets an existing session or creates a new one with agent metadata.
// This is used for multi-agent workflows where sub-agents need to access parent sessions.
// Parameters:
//   - sessionID: Unique session identifier
//   - agentID: Agent identity (e.g., "coordinator", "analyzer-sub-agent")
//   - parentSessionID: Parent session ID (for sub-agents to access coordinator session)
func (m *Memory) GetOrCreateSessionWithAgent(sessionID, agentID, parentSessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check in-memory cache first
	if session, ok := m.sessions[sessionID]; ok {
		// Update agent metadata if provided (allows setting after creation)
		updated := false
		if agentID != "" && session.AgentID == "" {
			session.AgentID = agentID
			updated = true
		}
		if parentSessionID != "" && session.ParentSessionID == "" {
			session.ParentSessionID = parentSessionID
			updated = true
		}

		// Persist updated metadata to store
		if updated && m.store != nil {
			if err := m.store.SaveSession(context.Background(), session); err != nil {
				// Log error but don't fail (session is updated in memory)
				// TODO: Consider adding error callback
				_ = err
			}
		}

		// DEFENSIVE FIX: Ensure SegmentedMemory exists even for cached sessions
		// Protects against edge cases where session might have lost SegmentedMem
		if session.SegmentedMem == nil {
			romContent := "Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return."
			if m.systemPromptFunc != nil {
				romContent = m.systemPromptFunc()
			}

			if m.compressionProfile != nil {
				session.SegmentedMem = NewSegmentedMemoryWithCompression(romContent, m.maxContextTokens, m.reservedOutputTokens, *m.compressionProfile)
			} else {
				session.SegmentedMem = NewSegmentedMemory(romContent, m.maxContextTokens, m.reservedOutputTokens)
			}

			if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
				if m.sharedMemory != nil {
					segMem.SetSharedMemory(m.sharedMemory)
				}
				if m.store != nil {
					segMem.SetSessionStore(m.store, sessionID)
				}
				if m.tracer != nil {
					segMem.SetTracer(m.tracer)
				}
				if m.llmProvider != nil {
					segMem.SetLLMProvider(m.llmProvider)
				}
			}
		}

		if session.FailureTracker == nil {
			session.FailureTracker = newConsecutiveFailureTracker()
		}

		return session
	}

	// Try loading from persistent store
	if m.store != nil {
		session, err := m.store.LoadSession(context.Background(), sessionID)
		if err == nil {
			// Update agent metadata if provided
			updated := false
			if agentID != "" && session.AgentID == "" {
				session.AgentID = agentID
				updated = true
			}
			if parentSessionID != "" && session.ParentSessionID == "" {
				session.ParentSessionID = parentSessionID
				updated = true
			}

			// Persist updated metadata
			if updated {
				if err := m.store.SaveSession(context.Background(), session); err != nil {
					// Log error but don't fail
					_ = err
				}
			}

			// CRITICAL FIX: Re-initialize SegmentedMemory for loaded sessions
			// Sessions loaded from DB don't have SegmentedMem/FailureTracker (not persisted)
			// We MUST recreate these for compression and error tracking to work
			if session.SegmentedMem == nil {
				// Initialize ROM content
				romContent := "Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return."
				if m.systemPromptFunc != nil {
					romContent = m.systemPromptFunc()
				}

				// Create SegmentedMemory with compression profile
				if m.compressionProfile != nil {
					session.SegmentedMem = NewSegmentedMemoryWithCompression(romContent, m.maxContextTokens, m.reservedOutputTokens, *m.compressionProfile)
				} else {
					session.SegmentedMem = NewSegmentedMemory(romContent, m.maxContextTokens, m.reservedOutputTokens)
				}

				// Inject dependencies
				if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
					if m.sharedMemory != nil {
						segMem.SetSharedMemory(m.sharedMemory)
					}
					if m.store != nil {
						segMem.SetSessionStore(m.store, sessionID)
					}
					if m.tracer != nil {
						segMem.SetTracer(m.tracer)
					}
					if m.llmProvider != nil {
						segMem.SetLLMProvider(m.llmProvider)
					}
				}
			}

			// Re-initialize FailureTracker if missing
			if session.FailureTracker == nil {
				session.FailureTracker = newConsecutiveFailureTracker()
			}

			m.sessions[sessionID] = session
			return session
		}
		// If not found in store, create new below
	}

	// Create new session with agent metadata
	session := &Session{
		ID:              sessionID,
		AgentID:         agentID,
		ParentSessionID: parentSessionID,
		Messages:        []Message{},
		Context:         make(map[string]interface{}),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Initialize segmented memory (tiered memory management for 100+ turn conversations)
	// ROM content can be customized per agent
	romContent := "Use available tools to help the user accomplish their goals. Never fabricate data - only report what tools actually return."
	if m.systemPromptFunc != nil {
		romContent = m.systemPromptFunc()
	}

	// Use compression profile if configured, otherwise use balanced defaults
	if m.compressionProfile != nil {
		session.SegmentedMem = NewSegmentedMemoryWithCompression(romContent, m.maxContextTokens, m.reservedOutputTokens, *m.compressionProfile)
	} else {
		session.SegmentedMem = NewSegmentedMemory(romContent, m.maxContextTokens, m.reservedOutputTokens)
	}

	// Inject shared memory if configured
	if m.sharedMemory != nil {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
			segMem.SetSharedMemory(m.sharedMemory)
		}
	}

	// Enable swap layer if SessionStore is configured (opt-out design)
	// This enables "forever conversations" by automatically evicting L2 to database
	if m.store != nil {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
			segMem.SetSessionStore(m.store, sessionID)
		}
	}

	// Inject tracer if configured
	if m.tracer != nil {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
			segMem.SetTracer(m.tracer)
		}
	}

	// Inject LLM provider if configured (for semantic search reranking)
	if m.llmProvider != nil {
		if segMem, ok := session.SegmentedMem.(*SegmentedMemory); ok {
			segMem.SetLLMProvider(m.llmProvider)
		}
	}

	// Initialize failure tracker for consecutive error detection
	session.FailureTracker = newConsecutiveFailureTracker()

	m.sessions[sessionID] = session

	// Persist to store if configured
	if m.store != nil {
		_ = m.store.SaveSession(context.Background(), session)
	}

	return session
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

// GetStore returns the SessionStore if persistence is enabled, nil otherwise.
// Used for registering cleanup hooks and accessing persistence layer.
func (m *Memory) GetStore() *SessionStore {
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
func (m *Memory) AddMessage(sessionID string, msg Message) {
	m.mu.RLock()
	session, found := m.sessions[sessionID]
	m.mu.RUnlock()

	if !found {
		// Session not in memory, nothing to do (will be handled by caller)
		return
	}

	// Add message to session (this handles SegmentedMem if configured)
	session.AddMessage(msg)

	// Persist to store if configured
	if m.store != nil {
		ctx := context.Background()
		if err := m.store.SaveMessage(ctx, sessionID, msg); err != nil {
			// Log error but don't fail (message is in session memory)
			// TODO: Consider adding error callback
			_ = err
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
