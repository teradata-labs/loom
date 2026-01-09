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
package mcp

import (
	"encoding/json"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/tui/components/chat/sidebar"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const (
	toolDetailsDialogID dialogs.DialogID = "mcp-tool-details"
)

// ToolDetailsDialog shows MCP tool details
type ToolDetailsDialog interface {
	dialogs.DialogModel
}

type toolDetailsDialogCmp struct {
	wWidth, wHeight int
	width, height   int

	serverName string
	toolName   string
	tool       sidebar.MCPToolInfo

	viewport viewport.Model
	keys     ToolDetailsKeyMap
	help     help.Model

	positionRow int
	positionCol int
}

// ToolDetailsKeyMap defines key bindings for tool details dialog
type ToolDetailsKeyMap struct {
	Close key.Binding
}

// DefaultToolDetailsKeyMap returns default key bindings
func DefaultToolDetailsKeyMap() ToolDetailsKeyMap {
	return ToolDetailsKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc", "q"),
			key.WithHelp("esc/q", "close"),
		),
	}
}

// ShortHelp returns key bindings for the short help view
func (k ToolDetailsKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Close}
}

// FullHelp returns key bindings for the full help view
func (k ToolDetailsKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Close},
	}
}

func NewToolDetailsDialog(serverName string, tool sidebar.MCPToolInfo) ToolDetailsDialog {
	t := styles.CurrentTheme()
	h := help.New()
	h.Styles = t.S().Help

	return &toolDetailsDialogCmp{
		serverName: serverName,
		toolName:   tool.Name,
		tool:       tool,
		viewport:   viewport.New(),
		keys:       DefaultToolDetailsKeyMap(),
		help:       h,
	}
}

func (m *toolDetailsDialogCmp) ID() dialogs.DialogID {
	return toolDetailsDialogID
}

func (m *toolDetailsDialogCmp) Position() (int, int) {
	return m.positionRow, m.positionCol
}

func (m *toolDetailsDialogCmp) Init() tea.Cmd {
	return m.viewport.Init()
}

func (m *toolDetailsDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.wWidth, m.wHeight = msg.Width, msg.Height
		return m, m.resize()

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Close):
			return m, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
	}

	// Forward all other messages to viewport for scrolling
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *toolDetailsDialogCmp) resize() tea.Cmd {
	t := styles.CurrentTheme()

	// Dialog should be 70% of screen width, 80% of height
	m.width = int(float64(m.wWidth) * 0.7)
	m.height = int(float64(m.wHeight) * 0.8)

	// Ensure minimum size
	if m.width < 60 {
		m.width = 60
	}
	if m.height < 20 {
		m.height = 20
	}

	// Account for border and padding
	contentWidth := m.width - 4   // 2 for border, 2 for padding
	contentHeight := m.height - 6 // 2 for border, 2 for padding, 2 for title/help

	// Set viewport size
	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(contentHeight)

	// Build content
	content := m.buildContent(contentWidth, t)
	m.viewport.SetContent(content)

	// Set position (centered)
	m.positionRow = m.wHeight/2 - m.height/2
	m.positionCol = m.wWidth/2 - m.width/2

	return nil
}

func (m *toolDetailsDialogCmp) buildContent(width int, t *styles.Theme) string {
	var parts []string

	// Server name
	serverLabel := t.S().Base.Foreground(t.FgMuted).Render("Server:")
	serverValue := t.S().Base.Foreground(t.Primary).Render(m.serverName)
	parts = append(parts, serverLabel+" "+serverValue)
	parts = append(parts, "")

	// Tool name
	toolLabel := t.S().Base.Foreground(t.FgMuted).Render("Tool:")
	toolValue := t.S().Base.Bold(true).Render(m.toolName)
	parts = append(parts, toolLabel+" "+toolValue)
	parts = append(parts, "")

	// Description
	if m.tool.Description != "" {
		descLabel := t.S().Base.Foreground(t.FgMuted).Render("Description:")
		parts = append(parts, descLabel)

		// Wrap description text
		wrapped := wrapText(m.tool.Description, width-2)
		for _, line := range strings.Split(wrapped, "\n") {
			parts = append(parts, "  "+line)
		}
		parts = append(parts, "")
	}

	// Input Schema
	if m.tool.InputSchema != "" {
		schemaLabel := t.S().Base.Foreground(t.FgMuted).Render("Input Schema:")
		parts = append(parts, schemaLabel)

		// Try to pretty-print JSON
		var schemaObj interface{}
		if err := json.Unmarshal([]byte(m.tool.InputSchema), &schemaObj); err == nil {
			prettyJSON, err := json.MarshalIndent(schemaObj, "", "  ")
			if err == nil {
				schemaLines := strings.Split(string(prettyJSON), "\n")
				for _, line := range schemaLines {
					parts = append(parts, "  "+t.S().Base.Foreground(t.FgSubtle).Render(line))
				}
			}
		} else {
			// Fallback: show raw schema
			schemaLines := strings.Split(m.tool.InputSchema, "\n")
			for _, line := range schemaLines {
				parts = append(parts, "  "+t.S().Base.Foreground(t.FgSubtle).Render(line))
			}
		}
	}

	return strings.Join(parts, "\n")
}

func (m *toolDetailsDialogCmp) View() string {
	t := styles.CurrentTheme()

	// Title
	title := t.S().Base.
		Bold(true).
		Foreground(t.Primary).
		Render("MCP Tool Details")

	// Content (viewport)
	content := m.viewport.View()

	// Help
	helpView := m.help.View(m.keys)

	// Assemble dialog
	inner := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		content,
		"",
		helpView,
	)

	// Border
	style := t.S().Base.
		Width(m.width).
		Height(m.height).
		Padding(1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)

	return style.Render(inner)
}

func (m *toolDetailsDialogCmp) Cursor() *tea.Cursor {
	return nil
}

// wrapText wraps text to fit within a given width
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	var lines []string
	var currentLine string

	for _, word := range words {
		if currentLine == "" {
			currentLine = word
		} else if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}

	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return strings.Join(lines, "\n")
}
