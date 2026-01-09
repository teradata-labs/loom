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
package sidebar

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
	"github.com/teradata-labs/loom/pkg/tui/components/agents"
	"github.com/teradata-labs/loom/pkg/tui/components/core"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

const LogoHeightBreakpoint = 30

// Default maximum number of items to show in each section
const (
	DefaultMaxAgentsShown = 10
	MinItemsPerSection    = 2 // Minimum items to show per section
)

// AgentsListMsg contains the list of active agents.
// Follows Crush's SessionFilesMsg pattern.
type AgentsListMsg struct {
	Agents []agents.AgentInfo
}

// Sidebar interface for the sidebar component.
// Adapted from Crush's Sidebar interface.
type Sidebar interface {
	Init() tea.Cmd
	Update(tea.Msg) (Sidebar, tea.Cmd)
	View() string
	SetSize(width, height int) tea.Cmd
	GetSize() (int, int)
	SetSession(sessionID, agentID string, currentModel *loomv1.ModelInfo) tea.Cmd
	SetCompactMode(bool)
	SetTokensAndCost(tokens int64, cost float64)
}

type sidebarCmp struct {
	width, height int
	sessionID     string
	agentID       string
	compactMode   bool
	client        *client.Client

	// Current session info
	currentModel *loomv1.ModelInfo
	tokens       int64
	cost         float64

	// Agents list
	agentsList []agents.AgentInfo
}

// New creates a new sidebar component.
// Follows Crush's sidebar constructor pattern.
func New(c *client.Client, compact bool) Sidebar {
	return &sidebarCmp{
		client:      c,
		compactMode: compact,
		agentsList:  []agents.AgentInfo{},
	}
}

func (m *sidebarCmp) Init() tea.Cmd {
	return nil
}

func (m *sidebarCmp) Update(msg tea.Msg) (Sidebar, tea.Cmd) {
	switch msg := msg.(type) {
	case AgentsListMsg:
		m.agentsList = msg.Agents
		return m, nil
	}
	return m, nil
}

// View renders the sidebar.
// Adapted from Crush's sidebar View method.
func (m *sidebarCmp) View() string {
	t := styles.CurrentTheme()
	parts := []string{}

	style := lipgloss.NewStyle().
		Foreground(t.FgBase).
		Background(t.BgBase).
		Width(m.width).
		Height(m.height).
		Padding(1)
	if m.compactMode {
		style = style.PaddingTop(0)
	}

	// Logo (if not compact and tall enough)
	if !m.compactMode {
		if m.height > LogoHeightBreakpoint {
			parts = append(parts, m.logoBlock())
		} else {
			// Small logo
			smallLogo := lipgloss.NewStyle().
				Foreground(t.Primary).
				Bold(true).
				Render("🧵 Loom")
			parts = append(parts, smallLogo, "")
		}
	}

	// Session title (if present)
	if !m.compactMode && m.sessionID != "" {
		sessionDisplay := fmt.Sprintf("Session %s", m.formatSessionID(m.sessionID))
		parts = append(parts, lipgloss.NewStyle().Foreground(t.FgMuted).Render(sessionDisplay), "")
	} else if m.sessionID != "" {
		sessionDisplay := fmt.Sprintf("Session %s", m.formatSessionID(m.sessionID))
		parts = append(parts, lipgloss.NewStyle().Foreground(t.FgBase).Render(sessionDisplay), "")
	}

	// Model block
	parts = append(parts, m.currentModelBlock())

	// Agents section
	if m.sessionID != "" {
		parts = append(parts, "", m.agentsBlock())
	}

	return style.Render(
		lipgloss.JoinVertical(lipgloss.Left, parts...),
	)
}

func (m *sidebarCmp) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	return nil
}

func (m *sidebarCmp) GetSize() (int, int) {
	return m.width, m.height
}

func (m *sidebarCmp) logoBlock() string {
	t := styles.CurrentTheme()
	// Simple logo for now
	logo := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		Render("🧵 Loom")
	return logo
}

func (m *sidebarCmp) formatSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m *sidebarCmp) getMaxWidth() int {
	return min(m.width-2, 58) // -2 for padding
}

// calculateAvailableHeight estimates how much height is available for dynamic content
// Adapted from Crush's calculation method.
func (m *sidebarCmp) calculateAvailableHeight() int {
	usedHeight := 0

	if !m.compactMode {
		if m.height > LogoHeightBreakpoint {
			usedHeight += 2 // Logo height
		} else {
			usedHeight += 2 // Smaller logo height
		}
		usedHeight += 1 // Empty line after logo
	}

	if m.sessionID != "" {
		usedHeight += 1 // Session line
		usedHeight += 1 // Empty line after session
	}

	usedHeight += 3 // Model info (can be multi-line)
	usedHeight += 2 // Section header + empty line
	usedHeight += 2 // Top and bottom padding

	return max(0, m.height-usedHeight)
}

// getDynamicLimits calculates how many agents to show based on available height
// Adapted from Crush's getDynamicLimits method.
func (m *sidebarCmp) getDynamicLimits() int {
	availableHeight := m.calculateAvailableHeight()

	// If we have very little space, use minimum values
	if availableHeight < 10 {
		return MinItemsPerSection
	}

	// Calculate limit for agents
	maxAgents := max(MinItemsPerSection, min(DefaultMaxAgentsShown, availableHeight))

	return maxAgents
}

func (m *sidebarCmp) agentsBlock() string {
	// Limit the number of agents shown
	maxAgents := m.getDynamicLimits()

	return agents.RenderAgentBlock(m.agentsList, agents.RenderOptions{
		MaxWidth:    m.getMaxWidth(),
		MaxItems:    maxAgents,
		ShowSection: true,
		SectionName: core.Section("Agents", m.getMaxWidth()),
	}, true)
}

func (m *sidebarCmp) formatTokensAndCost(tokens, contextWindow int64, cost float64) string {
	t := styles.CurrentTheme()
	// Format tokens in human-readable format (e.g., 110K, 1.2M)
	// Adapted from Crush's formatting.
	var formattedTokens string
	switch {
	case tokens >= 1_000_000:
		formattedTokens = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		formattedTokens = fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		formattedTokens = fmt.Sprintf("%d", tokens)
	}

	// Remove .0 suffix if present
	if strings.HasSuffix(formattedTokens, ".0K") {
		formattedTokens = strings.Replace(formattedTokens, ".0K", "K", 1)
	}
	if strings.HasSuffix(formattedTokens, ".0M") {
		formattedTokens = strings.Replace(formattedTokens, ".0M", "M", 1)
	}

	percentage := (float64(tokens) / float64(contextWindow)) * 100

	baseStyle := lipgloss.NewStyle()

	formattedCost := baseStyle.Foreground(t.FgMuted).Render(fmt.Sprintf("$%.2f", cost))
	formattedTokens = baseStyle.Foreground(t.FgSubtle).Render(fmt.Sprintf("(%s)", formattedTokens))
	formattedPercentage := baseStyle.Foreground(t.FgMuted).Render(fmt.Sprintf("%d%%", int(percentage)))
	formattedTokens = fmt.Sprintf("%s %s", formattedPercentage, formattedTokens)

	if percentage > 80 {
		// add the warning icon
		formattedTokens = fmt.Sprintf("%s %s", "⚠", formattedTokens)
	}

	return fmt.Sprintf("%s %s", formattedTokens, formattedCost)
}

func (m *sidebarCmp) currentModelBlock() string {
	t := styles.CurrentTheme()

	if m.currentModel == nil {
		return ""
	}

	modelIcon := lipgloss.NewStyle().Foreground(t.FgSubtle).Render("⬡")
	modelName := lipgloss.NewStyle().Foreground(t.FgBase).Render(m.currentModel.Name)
	modelInfo := fmt.Sprintf("%s %s", modelIcon, modelName)
	parts := []string{modelInfo}

	// Add token and cost info if available
	if m.tokens > 0 && m.currentModel.ContextWindow > 0 {
		parts = append(
			parts,
			"  "+m.formatTokensAndCost(
				m.tokens,
				int64(m.currentModel.ContextWindow),
				m.cost,
			),
		)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		parts...,
	)
}

// FetchAgentsMsg triggers fetching the agents list from the server
type FetchAgentsMsg struct{}

// fetchAgents fetches the list of agents from the server
func (m *sidebarCmp) fetchAgents() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	agentInfos, err := m.client.ListAgents(ctx)
	if err != nil {
		return AgentsListMsg{Agents: []agents.AgentInfo{}}
	}

	t := styles.CurrentTheme()
	agentsList := make([]agents.AgentInfo, 0, len(agentInfos))
	for _, info := range agentInfos {
		status := info.Status
		if status == "" {
			status = "idle"
		}

		// Highlight current agent
		color := t.FgBase
		if info.Id == m.agentID {
			color = t.Primary
			status = "active"
		}

		agentsList = append(agentsList, agents.AgentInfo{
			ID:         info.Id,
			Name:       info.Name,
			Status:     status,
			Color:      color,
			LastActive: time.Now().Unix(),
		})
	}

	return AgentsListMsg{Agents: agentsList}
}

// SetSession implements Sidebar.
func (m *sidebarCmp) SetSession(sessionID, agentID string, currentModel *loomv1.ModelInfo) tea.Cmd {
	m.sessionID = sessionID
	m.agentID = agentID
	m.currentModel = currentModel

	// Fetch all agents from server for multi-agent sessions
	if sessionID != "" {
		return m.fetchAgents
	}

	return nil
}

// SetCompactMode sets the compact mode for the sidebar.
func (m *sidebarCmp) SetCompactMode(compact bool) {
	m.compactMode = compact
}

// SetTokensAndCost updates token usage and cost.
func (m *sidebarCmp) SetTokensAndCost(tokens int64, cost float64) {
	m.tokens = tokens
	m.cost = cost
}
