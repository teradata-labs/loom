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
// Package transport implements session management for streamable-http transport.
package transport

import (
	"fmt"
	"sync"
)

// SessionManager manages MCP session IDs for streamable-http transport.
// Per the MCP spec, session IDs are globally unique, cryptographically secure,
// and consist only of visible ASCII characters (0x21 to 0x7E).
type SessionManager struct {
	sessionID string
	mu        sync.RWMutex
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{}
}

// SetSessionID sets the session ID from the server's Mcp-Session-Id header.
func (s *SessionManager) SetSessionID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate session ID contains only visible ASCII (0x21 to 0x7E)
	if id != "" {
		for _, c := range id {
			if c < 0x21 || c > 0x7E {
				return fmt.Errorf("invalid session ID: contains non-ASCII or invisible characters")
			}
		}
	}

	s.sessionID = id
	return nil
}

// GetSessionID returns the current session ID.
func (s *SessionManager) GetSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

// HasSession returns true if a session ID is set.
func (s *SessionManager) HasSession() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID != ""
}

// ClearSession clears the session ID (used during re-initialization).
func (s *SessionManager) ClearSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = ""
}
