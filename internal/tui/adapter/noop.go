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

	"github.com/teradata-labs/loom/internal/agent"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/internal/session"
)

// NoopCoordinator is a no-op coordinator for when server is unavailable.
type NoopCoordinator struct {
	agentID string
}

func (n *NoopCoordinator) Run(ctx context.Context, sessionID, prompt string, attachments ...any) (any, error) {
	return nil, fmt.Errorf("server not available")
}

func (n *NoopCoordinator) IsBusy(agentID string) bool {
	return false
}

func (n *NoopCoordinator) IsSessionBusy(sessionID string) bool {
	return false
}

func (n *NoopCoordinator) GetAgentID() string {
	return n.agentID
}

func (n *NoopCoordinator) SetAgentID(agentID string) {
	n.agentID = agentID
}

func (n *NoopCoordinator) Cancel() {}

func (n *NoopCoordinator) CancelAll() {}

func (n *NoopCoordinator) ClearQueue(sessionID string) {}

func (n *NoopCoordinator) UpdateModels(ctx context.Context) error {
	return fmt.Errorf("server not available")
}

func (n *NoopCoordinator) Summarize(ctx context.Context, sessionID string) error {
	return fmt.Errorf("server not available")
}

func (n *NoopCoordinator) QueuedPrompts() int {
	return 0
}

func (n *NoopCoordinator) QueuedPromptsList(sessionID string) []string {
	return nil
}

func (n *NoopCoordinator) ListAgents(ctx context.Context) ([]agent.AgentInfo, error) {
	return nil, fmt.Errorf("server not available")
}

// NoopSessionAdapter is a no-op session adapter for when server is unavailable.
type NoopSessionAdapter struct {
	agentID string
}

func (n *NoopSessionAdapter) Create(ctx context.Context, title string) (session.Session, error) {
	return session.Session{}, fmt.Errorf("server not available")
}

func (n *NoopSessionAdapter) Get(ctx context.Context, sessionID string) (session.Session, error) {
	return session.Session{}, fmt.Errorf("server not available")
}

func (n *NoopSessionAdapter) Update(ctx context.Context, sess session.Session) error {
	return fmt.Errorf("server not available")
}

func (n *NoopSessionAdapter) Delete(ctx context.Context, sessionID string) error {
	return fmt.Errorf("server not available")
}

func (n *NoopSessionAdapter) List(ctx context.Context) ([]session.Session, error) {
	return nil, fmt.Errorf("server not available")
}

func (n *NoopSessionAdapter) Subscribe(ctx context.Context) <-chan pubsub.Event[session.Session] {
	ch := make(chan pubsub.Event[session.Session])
	close(ch)
	return ch
}

func (n *NoopSessionAdapter) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	return "", "", false
}

func (n *NoopSessionAdapter) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return ""
}

func (n *NoopSessionAdapter) SetAgentID(agentID string) {
	n.agentID = agentID
}

// NoopMessageAdapter is a no-op message adapter for when server is unavailable.
type NoopMessageAdapter struct{}

func (n *NoopMessageAdapter) Create(ctx context.Context, msg message.Message) error {
	return fmt.Errorf("server not available")
}

func (n *NoopMessageAdapter) Get(ctx context.Context, messageID string) (message.Message, error) {
	return message.Message{}, fmt.Errorf("server not available")
}

func (n *NoopMessageAdapter) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	return nil, fmt.Errorf("server not available")
}

func (n *NoopMessageAdapter) Update(ctx context.Context, msg message.Message) error {
	return fmt.Errorf("server not available")
}

func (n *NoopMessageAdapter) Delete(ctx context.Context, messageID string) error {
	return fmt.Errorf("server not available")
}

func (n *NoopMessageAdapter) Subscribe(ctx context.Context) <-chan pubsub.Event[message.Message] {
	ch := make(chan pubsub.Event[message.Message])
	close(ch)
	return ch
}
