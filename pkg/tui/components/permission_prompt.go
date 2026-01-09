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
package components

import (
	"fmt"
	"strings"

	"image/color"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// PermissionResult represents the user's permission decision.
type PermissionResult struct {
	Granted  bool
	Remember bool
	Message  string
}

// PermissionPromptModel is a UI component for requesting tool execution permission.
type PermissionPromptModel struct {
	request        *loomv1.ToolPermissionRequest
	styles         *styles.Styles
	icons          *styles.Icons
	result         *PermissionResult
	rememberToggle bool
	quitting       bool
	width          int
	height         int
}

// NewPermissionPrompt creates a new permission prompt.
func NewPermissionPrompt(req *loomv1.ToolPermissionRequest, s *styles.Styles) *PermissionPromptModel {
	return &PermissionPromptModel{
		request:        req,
		styles:         s,
		icons:          styles.DefaultIcons(),
		rememberToggle: false,
	}
}

// Init initializes the permission prompt.
func (m *PermissionPromptModel) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *PermissionPromptModel) Update(msg tea.Msg) (*PermissionPromptModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			// Deny and quit
			m.result = &PermissionResult{
				Granted:  false,
				Remember: false,
				Message:  "Cancelled by user",
			}
			m.quitting = true
			return m, tea.Quit

		case "y", "enter":
			// Approve
			m.result = &PermissionResult{
				Granted:  true,
				Remember: m.rememberToggle,
				Message:  "",
			}
			m.quitting = true
			return m, tea.Quit

		case "n":
			// Deny
			m.result = &PermissionResult{
				Granted:  false,
				Remember: m.rememberToggle,
				Message:  "Permission denied by user",
			}
			m.quitting = true
			return m, tea.Quit

		case "r":
			// Toggle remember
			m.rememberToggle = !m.rememberToggle
			return m, nil
		}
	}

	return m, nil
}

// View renders the permission prompt.
func (m *PermissionPromptModel) View() string {
	if m.quitting {
		return ""
	}

	// Title with icon based on risk level
	var icon string
	var titleStyle lipgloss.Style
	switch m.request.RiskLevel {
	case "high":
		icon = m.icons.Warning
		titleStyle = lipgloss.NewStyle().
			Foreground(m.styles.Theme.Warning).
			Bold(true)
	case "critical":
		icon = m.icons.Error
		titleStyle = lipgloss.NewStyle().
			Foreground(m.styles.Theme.Error).
			Bold(true)
	default:
		icon = m.icons.PermissionIcon
		titleStyle = lipgloss.NewStyle().
			Foreground(m.styles.Theme.Info).
			Bold(true)
	}

	title := titleStyle.Render(fmt.Sprintf("%s  Permission Required", icon))

	// Tool info
	toolStyle := lipgloss.NewStyle().
		Foreground(m.styles.Theme.Primary).
		Bold(true)
	toolInfo := fmt.Sprintf("Tool: %s", toolStyle.Render(m.request.ToolName))

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(m.styles.Theme.TextNormal).
		Width(min(60, m.width-8)).
		MarginTop(1).
		MarginBottom(1)
	desc := descStyle.Render(m.request.Description)

	// Arguments (if provided)
	var argsView string
	if m.request.ArgsJson != "" && m.request.ArgsJson != "{}" {
		argsStyle := lipgloss.NewStyle().
			Foreground(m.styles.Theme.TextDim).
			Width(min(60, m.width-8)).
			MarginTop(1)
		argsView = argsStyle.Render(fmt.Sprintf("Arguments: %s", m.request.ArgsJson))
	}

	// Risk level indicator
	riskStyle := lipgloss.NewStyle().
		Foreground(m.getRiskColor()).
		MarginTop(1)
	riskInfo := riskStyle.Render(fmt.Sprintf("Risk Level: %s", strings.ToUpper(m.request.RiskLevel)))

	// Remember toggle
	rememberIcon := "[ ]"
	if m.rememberToggle {
		rememberIcon = "[✓]"
	}
	rememberStyle := lipgloss.NewStyle().
		Foreground(m.styles.Theme.TextDim).
		MarginTop(1)
	rememberView := rememberStyle.Render(fmt.Sprintf("%s Remember this decision", rememberIcon))

	// Actions
	actionsStyle := lipgloss.NewStyle().
		Foreground(m.styles.Theme.Primary).
		Bold(true).
		MarginTop(2)

	actions := actionsStyle.Render("y: approve • n: deny • r: toggle remember • esc: cancel")

	// Combine all elements
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		toolInfo,
		desc,
		argsView,
		riskInfo,
		rememberView,
		"",
		actions,
	)

	// Box with border
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.styles.Theme.Primary).
		Padding(1, 2).
		Width(min(70, m.width-4))

	return box.Render(content)
}

// Result returns the permission result after user decision.
func (m *PermissionPromptModel) Result() *PermissionResult {
	return m.result
}

// getRiskColor returns the color for the risk level.
func (m *PermissionPromptModel) getRiskColor() color.Color {
	switch m.request.RiskLevel {
	case "high":
		return m.styles.Theme.Warning
	case "critical":
		return m.styles.Theme.Error
	case "medium":
		return m.styles.Theme.Info
	default:
		return m.styles.Theme.Success
	}
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
