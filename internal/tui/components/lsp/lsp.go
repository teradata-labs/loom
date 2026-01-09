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
// Package lsp provides LSP TUI component stubs.
package lsp

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/lsp"
	"github.com/teradata-labs/loom/internal/tui/styles"
)

// Model is the LSP component model.
type Model struct {
	client *lsp.Client
	theme  *styles.Theme
}

// New creates a new LSP component.
func New(client *lsp.Client, theme *styles.Theme) *Model {
	return &Model{
		client: client,
		theme:  theme,
	}
}

// Init initializes the component.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	return m, nil
}

// View renders the component.
func (m *Model) View() string {
	return ""
}

// SetSize sets the component size.
func (m *Model) SetSize(width, height int) {
}

// SetTheme sets the theme.
func (m *Model) SetTheme(theme *styles.Theme) {
	m.theme = theme
}

// Focused returns whether the component is focused.
func (m *Model) Focused() bool {
	return false
}

// SetFocused sets focus state.
func (m *Model) SetFocused(focused bool) {
}

// Width returns the component width.
func (m *Model) Width() int {
	return 0
}

// Height returns the component height.
func (m *Model) Height() int {
	return 0
}

// Render renders the LSP status.
func (m *Model) Render() string {
	if m.client == nil || !m.client.IsConnected() {
		return lipgloss.NewStyle().Foreground(m.theme.FgMuted).Render("LSP: disconnected")
	}
	return lipgloss.NewStyle().Foreground(m.theme.Success).Render("LSP: connected")
}

// IsConnected returns whether LSP is connected.
func (m *Model) IsConnected() bool {
	return m.client != nil && m.client.IsConnected()
}

// RenderOptions contains options for rendering LSP status.
type RenderOptions struct {
	Width int
	Max   int
}

// RenderLSPBlock renders LSP status as a block.
func RenderLSPBlock(clients map[string]*lsp.Client, opts RenderOptions) string {
	return ""
}
