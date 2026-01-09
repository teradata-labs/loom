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
// Package app provides app types compatible with Crush's interface.
// This is a facade that wraps the adapter.LoomApp.
package app

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/teradata-labs/loom/internal/agent"
	"github.com/teradata-labs/loom/internal/history"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/permission"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/internal/tui/adapter"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// App represents the application.
// In Loom, this is a facade over the adapter.LoomApp.
type App struct {
	Sessions         session.Service
	Messages         message.Service
	History          history.Service
	Permissions      permission.Service
	AgentCoordinator agent.Coordinator
	LSPClients       *LSPClientMap  // Stub for LSP clients
	events           chan tea.Msg   // Event channel for TUI updates
	client           *client.Client // gRPC client for direct access
}

// NewFromClient creates a new App from a gRPC client.
// This is the main factory function for creating an App in Loom.
func NewFromClient(c *client.Client, events chan tea.Msg) *App {
	coordAdapter := adapter.NewCoordinatorAdapter(c, events)
	sessAdapter := adapter.NewSessionAdapter(c)
	msgAdapter := adapter.NewMessageAdapter(c)
	permAdapter := adapter.NewPermissionAdapter()

	return &App{
		Sessions:         sessAdapter,
		Messages:         msgAdapter,
		History:          &history.NoopService{},
		Permissions:      permAdapter,
		AgentCoordinator: coordAdapter,
		LSPClients:       &LSPClientMap{},
		events:           events,
		client:           c,
	}
}

// Client returns the underlying gRPC client.
func (a *App) Client() *client.Client {
	return a.client
}

// LSPClientMap is a stub for LSP clients map.
type LSPClientMap struct{}

// Seq returns an iterator over LSP clients (empty).
func (m *LSPClientMap) Seq(fn func(string, interface{}) bool) {}

// Values returns an iterator over values.
func (m *LSPClientMap) Values() func(yield func(interface{}) bool) {
	return func(yield func(interface{}) bool) {}
}

// Len returns the number of clients.
func (m *LSPClientMap) Len() int { return 0 }

// InitCoderAgent initializes the coder agent.
func (a *App) InitCoderAgent() error {
	// Stub - agent initialization is handled by the server
	return nil
}

// Config returns the application configuration.
func (a *App) Config() *Config {
	return &Config{workingDir: "."}
}

// Subscribe sends events to the TUI.
func (a *App) Subscribe(program *tea.Program) {
	if a.events == nil {
		return
	}
	for msg := range a.events {
		program.Send(msg)
	}
}

// Shutdown performs graceful shutdown.
func (a *App) Shutdown() {
	if a.events != nil {
		close(a.events)
	}
}

// UpdateAgentModel updates the agent model.
func (a *App) UpdateAgentModel(ctx context.Context) error {
	if a.AgentCoordinator == nil {
		return nil
	}
	return a.AgentCoordinator.UpdateModels(ctx)
}

// SetAgentID sets the current agent/thread ID on the coordinator and sessions.
func (a *App) SetAgentID(agentID string) {
	if coord, ok := a.AgentCoordinator.(interface{ SetAgentID(string) }); ok {
		coord.SetAgentID(agentID)
	}
	if sess, ok := a.Sessions.(interface{ SetAgentID(string) }); ok {
		sess.SetAgentID(agentID)
	}
}

// Config represents application configuration.
type Config struct {
	workingDir string
	Options    ConfigOptions
}

// ConfigOptions represents configuration options.
type ConfigOptions struct {
	Editor       string
	InitializeAs string
	TUI          TUIOptions
}

// TUIOptions represents TUI-specific options.
type TUIOptions struct {
	DiffMode    string
	Completions CompletionOptions
}

// CompletionOptions holds completion settings.
type CompletionOptions struct {
	Depth int
	Limit int
}

// Limits returns the depth and limit for completions.
func (c CompletionOptions) Limits() (int, int) {
	depth, limit := c.Depth, c.Limit
	if depth <= 0 {
		depth = 3 // default
	}
	if limit <= 0 {
		limit = 100 // default
	}
	return depth, limit
}

// WorkingDir returns the working directory.
func (c *Config) WorkingDir() string {
	return c.workingDir
}

// SetWorkingDir sets the working directory.
func (c *Config) SetWorkingDir(dir string) {
	c.workingDir = dir
}
