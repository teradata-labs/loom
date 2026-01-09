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
// Package adapter provides adapters to bridge Crush's TUI interfaces with Loom's gRPC client.
// This allows the Crush TUI components to work with Loom's server without modification.
package adapter

import (
	"context"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

// LoomApp adapts Loom's gRPC client to match Crush's app.App interface.
// This allows Crush TUI components to work seamlessly with Loom's server.
type LoomApp struct {
	client *client.Client

	// Services matching Crush's App interface
	Sessions    *SessionAdapter
	Messages    *MessageAdapter
	Permissions *PermissionAdapter

	// Agent coordinator wrapping gRPC streaming
	AgentCoordinator *CoordinatorAdapter

	// Configuration
	config *LoomConfig

	// Event system
	events    chan tea.Msg
	eventsCtx context.Context
	cancel    context.CancelFunc
	eventsWG  sync.WaitGroup

	// Lifecycle
	closed atomic.Bool
}

// LoomConfig holds TUI configuration.
type LoomConfig struct {
	ServerAddr string
	WorkingDir string
	AgentID    string // Selected agent/thread
	Options    ConfigOptions
}

// ConfigOptions holds TUI options.
type ConfigOptions struct {
	Editor       string
	InitializeAs string
	TUI          TUIOptions
}

// TUIOptions holds TUI-specific options.
type TUIOptions struct {
	DiffMode string
}

// NewLoomApp creates a new LoomApp wrapping the gRPC client.
func NewLoomApp(c *client.Client) *LoomApp {
	ctx, cancel := context.WithCancel(context.Background())

	app := &LoomApp{
		client:    c,
		events:    make(chan tea.Msg, 100),
		eventsCtx: ctx,
		cancel:    cancel,
		config: &LoomConfig{
			ServerAddr: c.ServerAddr(),
			WorkingDir: ".", // Default to current directory
		},
	}

	// Initialize adapters
	app.Sessions = NewSessionAdapter(c)
	app.Messages = NewMessageAdapter(c)
	app.Permissions = NewPermissionAdapter()
	app.AgentCoordinator = NewCoordinatorAdapter(c, app.events)

	return app
}

// Client returns the underlying gRPC client.
func (a *LoomApp) Client() *client.Client {
	return a.client
}

// Config returns the application configuration.
func (a *LoomApp) Config() *LoomConfig {
	return a.config
}

// SetAgentID sets the current agent/thread ID.
func (a *LoomApp) SetAgentID(agentID string) {
	a.config.AgentID = agentID
	// Propagate to adapters
	if a.Sessions != nil {
		a.Sessions.SetAgentID(agentID)
	}
	if a.AgentCoordinator != nil {
		a.AgentCoordinator.SetAgentID(agentID)
	}
}

// Subscribe sends events to the TUI as tea.Msgs.
// Matches Crush's app.Subscribe interface.
func (a *LoomApp) Subscribe(program *tea.Program) {
	a.eventsWG.Add(1)
	defer a.eventsWG.Done()

	for {
		select {
		case <-a.eventsCtx.Done():
			return
		case msg, ok := <-a.events:
			if !ok {
				return
			}
			program.Send(msg)
		}
	}
}

// Shutdown performs a graceful shutdown of the application.
func (a *LoomApp) Shutdown() {
	if !a.closed.CompareAndSwap(false, true) {
		return // Already closed
	}

	// Cancel event subscriptions
	a.cancel()
	a.eventsWG.Wait()

	// Cancel any active coordination
	if a.AgentCoordinator != nil {
		a.AgentCoordinator.CancelAll()
	}

	// Close gRPC client
	if a.client != nil {
		_ = a.client.Close()
	}
}

// ListAgents returns available agents from the server.
func (a *LoomApp) ListAgents(ctx context.Context) ([]*loomv1.AgentInfo, error) {
	return a.client.ListAgents(ctx)
}

// ListAvailableModels returns available models from the server.
func (a *LoomApp) ListAvailableModels(ctx context.Context) ([]*loomv1.ModelInfo, error) {
	return a.client.ListAvailableModels(ctx)
}

// SwitchModel switches the model for a session.
func (a *LoomApp) SwitchModel(ctx context.Context, sessionID, provider, model string) (*loomv1.SwitchModelResponse, error) {
	return a.client.SwitchModel(ctx, sessionID, provider, model)
}

// IsBusy returns true if the current default agent is busy.
func (a *LoomApp) IsBusy() bool {
	if a.AgentCoordinator == nil {
		return false
	}
	// Pass empty string to check the default agent
	return a.AgentCoordinator.IsBusy("")
}
