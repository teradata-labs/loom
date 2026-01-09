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
// Package mcp provides MCP TUI component stubs.
package mcp

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/tui/styles"
)

// Model is the MCP component model.
type Model struct {
	theme   *styles.Theme
	servers []ServerInfo
}

// ServerInfo contains MCP server information.
type ServerInfo struct {
	Name      string
	Status    string
	ToolCount int
}

// New creates a new MCP component.
func New(theme *styles.Theme) *Model {
	return &Model{
		theme:   theme,
		servers: []ServerInfo{},
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

// Render renders the MCP status.
func (m *Model) Render() string {
	if len(m.servers) == 0 {
		return lipgloss.NewStyle().Foreground(m.theme.FgMuted).Render("MCP: no servers")
	}
	return lipgloss.NewStyle().Foreground(m.theme.Success).Render("MCP: connected")
}

// ServerCount returns the number of MCP servers.
func (m *Model) ServerCount() int {
	return len(m.servers)
}

// ToolCount returns total tool count across all servers.
func (m *Model) ToolCount() int {
	total := 0
	for _, s := range m.servers {
		total += s.ToolCount
	}
	return total
}

// RenderOptions contains options for rendering MCP status.
type RenderOptions struct {
	Width int
	Max   int
}

// RenderMCPBlock renders MCP status as a block.
func RenderMCPBlock(servers []ServerInfo, opts RenderOptions) string {
	return ""
}
