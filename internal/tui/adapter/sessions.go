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
package adapter

import (
	"context"
	"fmt"
	"os"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// SessionAdapter wraps the gRPC client for session operations.
// Implements session.Service interface.
type SessionAdapter struct {
	client  *client.Client
	agentID string
}

// NewSessionAdapter creates a new session adapter.
func NewSessionAdapter(c *client.Client) *SessionAdapter {
	return &SessionAdapter{client: c}
}

// SetAgentID sets the agent ID for session operations.
func (s *SessionAdapter) SetAgentID(agentID string) {
	s.agentID = agentID
	// Debug: log agent ID being set
	if f, err := os.OpenFile("/tmp/loom-session-adapter-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] SessionAdapter.SetAgentID called with: '%s'\n", time.Now().Format("15:04:05"), agentID)
		f.Close()
	}
}

// Create creates a new session.
func (s *SessionAdapter) Create(ctx context.Context, title string) (session.Session, error) {
	// Debug: log session creation
	if f, err := os.OpenFile("/tmp/loom-session-adapter-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] SessionAdapter.Create: using agentID='%s' for session '%s'\n", time.Now().Format("15:04:05"), s.agentID, title)
		f.Close()
	}

	sess, err := s.client.CreateSession(ctx, title, s.agentID)
	if err != nil {
		return session.Session{}, err
	}
	return protoToSession(sess), nil
}

// Get retrieves a session by ID.
func (s *SessionAdapter) Get(ctx context.Context, id string) (session.Session, error) {
	sess, err := s.client.GetSession(ctx, id)
	if err != nil {
		return session.Session{}, err
	}
	return protoToSession(sess), nil
}

// List returns all sessions.
func (s *SessionAdapter) List(ctx context.Context) ([]session.Session, error) {
	sessions, err := s.client.ListSessions(ctx, 100, 0)
	if err != nil {
		return nil, err
	}

	result := make([]session.Session, len(sessions))
	for i, sess := range sessions {
		result[i] = protoToSession(sess)
	}
	return result, nil
}

// Delete removes a session.
func (s *SessionAdapter) Delete(ctx context.Context, id string) error {
	return s.client.DeleteSession(ctx, id)
}

// Subscribe subscribes to session events.
// Note: Loom doesn't have real-time session events via gRPC,
// so this returns an empty channel. Session updates are handled
// differently in the TUI.
func (s *SessionAdapter) Subscribe(ctx context.Context) <-chan pubsub.Event[session.Session] {
	ch := make(chan pubsub.Event[session.Session])
	// Close when context is done since we don't have real-time session events
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// protoToSession converts a proto Session to internal Session type.
func protoToSession(s *loomv1.Session) session.Session {
	createdAt := s.CreatedAt
	updatedAt := s.UpdatedAt
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	if updatedAt == 0 {
		updatedAt = createdAt
	}

	return session.Session{
		ID:        s.Id,
		Title:     s.Name,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

// ParseAgentToolSessionID is a stub for compatibility.
// Returns false since Loom handles agent sessions differently.
func (s *SessionAdapter) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	return "", "", false
}

// CreateAgentToolSessionID is a stub for compatibility.
func (s *SessionAdapter) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return ""
}
