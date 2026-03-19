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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/charmtone"
	"github.com/teradata-labs/loom/internal/config"
	"github.com/teradata-labs/loom/internal/history"
	"github.com/teradata-labs/loom/internal/home"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/internal/tui/components/chat"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/core/layout"
	"github.com/teradata-labs/loom/internal/tui/components/logo"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/internal/version"
)

var debugLog *log.Logger

func init() {
	// Create debug log file
	f, err := os.OpenFile("/tmp/loom-sidebar-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		debugLog = log.New(f, "", log.LstdFlags)
	}
}

const LogoHeightBreakpoint = 30

// Default maximum number of items to show in each section
const (
	DefaultMaxLSPsShown = 8
	DefaultMaxMCPsShown = 8
	MinItemsPerSection  = 2 // Minimum items to show per section
)

// AgentInfo represents an agent in the multi-agent system
type AgentInfo struct {
	ID           string
	Name         string
	Status       string
	ModelInfo    string // Primary model (e.g., "anthropic/claude-sonnet-4")
	RoleLLMCount int    // Number of role-specific LLM overrides (0-4)
}

// AgentsListMsg contains the list of available agents
type AgentsListMsg struct {
	Agents         []AgentInfo
	CurrentAgent   string // ID of currently active agent
	ActiveProvider string // Name of the currently active provider (e.g., "gemini-flash")
}

// AgentSelectedMsg is sent when an agent is selected
type AgentSelectedMsg struct {
	AgentID string
}

// PatternCategorySelectedMsg is sent when a pattern category is selected (deprecated - now used for expansion)
type PatternCategorySelectedMsg struct {
	Category string
}

// PatternFileSelectedMsg is sent when a pattern file is selected to open in editor
type PatternFileSelectedMsg struct {
	FilePath string
}

// ShowPatternModalMsg is sent when user wants to see all patterns
type ShowPatternModalMsg struct{}

// MCPToolInfo represents a tool from an MCP server
type MCPToolInfo struct {
	Name        string
	Description string
	InputSchema string // JSON schema
}

// MCPServerInfo represents an MCP server in the sidebar
type MCPServerInfo struct {
	Name      string
	Enabled   bool
	Connected bool
	Transport string
	Status    string
	ToolCount int32
	Error     string        // Error message if status is error
	Tools     []MCPToolInfo // List of tools (populated when expanded)
}

// MCPServersListMsg contains the list of MCP servers
type MCPServersListMsg struct {
	Servers []MCPServerInfo
}

// UpdateMCPServerToolsMsg updates tools for a specific MCP server
type UpdateMCPServerToolsMsg struct {
	ServerName string
	Tools      []MCPToolInfo
}

// MCPServerSelectedMsg is sent when an MCP server is selected
type MCPServerSelectedMsg struct {
	ServerName string
}

// MCPToolSelectedMsg is sent when an MCP tool is selected
type MCPToolSelectedMsg struct {
	ServerName string
	ToolName   string
	Tool       MCPToolInfo
}

// AddMCPServerActionMsg is sent when user wants to add a new MCP server
type AddMCPServerActionMsg struct{}

// SidebarSection represents which section is currently selected
type SidebarSection int

const (
	SectionNone SidebarSection = iota
	SectionWeaver
	SectionMCP
)

// OpenPatternBrowserMsg is sent when user wants to open the pattern browser dialog
type OpenPatternBrowserMsg struct{}

type Sidebar interface {
	util.Model
	layout.Sizeable
	SetSession(session session.Session) tea.Cmd
	SetCompactMode(bool)
	Focus()
	Blur()
	IsFocused() bool
}

type sidebarCmp struct {
	width, height int
	session       session.Session
	logo          string
	cwd           string
	lspClients    any // LSP clients (nil in Loom)
	compactMode   bool
	history       history.Service
	agents        []AgentInfo // List of available agents
	currentAgent  string      // ID of currently active agent

	activeProvider string // Name of the currently active provider from ListProviders

	// Selection state
	selectedSection SidebarSection
	selectedIndex   int
	focused         bool

	// Cached items for navigation
	patternCategories []PatternCategory
	mcpServers        []MCPServerInfo // List of MCP servers

	// Pattern expansion state
	expandedCategories map[string]bool // Track which pattern categories are expanded

	// MCP server expansion state
	expandedMCPServers map[string]bool // Track which MCP servers are expanded

	// Scroll state
	scrollOffset int // Current scroll position (line offset)

	// Mouse support - track Y positions of clickable items
	weaverYStart  int
	contentYStart int // Where sidebar content begins (after logo/header)
}

func New(history history.Service, lspClients any, compact bool) Sidebar {
	return &sidebarCmp{
		lspClients:         lspClients,
		history:            history,
		compactMode:        compact,
		expandedCategories: make(map[string]bool),
		expandedMCPServers: make(map[string]bool),
	}
}

func (m *sidebarCmp) Init() tea.Cmd {
	return nil
}

func (m *sidebarCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case AgentsListMsg:
		if debugLog != nil {
			debugLog.Printf("[DEBUG] AgentsListMsg received with %d agents, currentAgent='%s', activeProvider='%s'\n", len(msg.Agents), msg.CurrentAgent, msg.ActiveProvider)
			for i, agent := range msg.Agents {
				debugLog.Printf("  [%d] name='%s', id='%s', status='%s'\n", i, agent.Name, agent.ID, agent.Status)
			}
		}
		m.agents = msg.Agents
		m.currentAgent = msg.CurrentAgent
		if msg.ActiveProvider != "" {
			m.activeProvider = msg.ActiveProvider
		}
		m.updateCachedItems()
		m.resetSelectionIfNeeded()
		return m, nil

	case MCPServersListMsg:
		if debugLog != nil {
			debugLog.Printf("[DEBUG] MCPServersListMsg received with %d servers\n", len(msg.Servers))
			for i, server := range msg.Servers {
				debugLog.Printf("  [%d] name='%s', enabled=%v, connected=%v, status='%s'\n",
					i, server.Name, server.Enabled, server.Connected, server.Status)
			}
		}
		m.mcpServers = msg.Servers
		return m, nil

	case UpdateMCPServerToolsMsg:
		// Update tools for a specific MCP server
		for i := range m.mcpServers {
			if m.mcpServers[i].Name == msg.ServerName {
				m.mcpServers[i].Tools = msg.Tools
				if debugLog != nil {
					debugLog.Printf("[DEBUG] Updated tools for server '%s': %d tools\n", msg.ServerName, len(msg.Tools))
				}
				break
			}
		}
		return m, nil

	case tea.MouseWheelMsg:
		// Handle scroll in sidebar
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollUp(1)
		case tea.MouseWheelDown:
			m.scrollDown(1)
		}
		return m, nil

	case tea.KeyPressMsg:
		if debugLog != nil {
			debugLog.Printf("[DEBUG] KeyPressMsg: key='%s', focused=%v\n", msg.String(), m.focused)
		}

		// IMPORTANT: Don't consume tab - let it bubble up to parent for focus changes
		if msg.String() == "tab" {
			return m, nil
		}

		if !m.focused {
			return m, nil
		}

		switch msg.String() {
		case "up":
			if debugLog != nil {
				debugLog.Printf("[DEBUG] Navigating up\n")
			}
			return m, m.navigateUp()

		case "down":
			if debugLog != nil {
				debugLog.Printf("[DEBUG] Navigating down\n")
			}
			return m, m.navigateDown()

		case "pgup":
			m.scrollUp(m.height / 2)
			return m, nil

		case "pgdown":
			m.scrollDown(m.height / 2)
			return m, nil

		case "enter":
			if debugLog != nil {
				debugLog.Printf("[DEBUG] Enter pressed - calling selectCurrentItem() with section=%d, index=%d\n", m.selectedSection, m.selectedIndex)
			}
			return m, m.selectCurrentItem()

		case "a":
			// Add new server when 'a' is pressed in MCP section
			if m.selectedSection == SectionMCP {
				if debugLog != nil {
					debugLog.Printf("[DEBUG] 'a' pressed in MCP section - opening add server dialog\n")
				}
				return m, util.CmdHandler(AddMCPServerActionMsg{})
			}

		case "ctrl+w":
			// Quick shortcut to select the weaver agent
			if debugLog != nil {
				debugLog.Printf("[DEBUG] ctrl+w pressed - selecting weaver\n")
			}
			return m, util.CmdHandler(AgentSelectedMsg{
				AgentID: "weaver",
			})
		}

	case tea.MouseClickMsg:
		// Handle mouse clicks for item selection
		if msg.Button == tea.MouseLeft {
			return m, m.handleMouseClick(msg.X, msg.Y)
		}

	case AgentSelectedMsg:
		// Update current agent when it changes
		m.currentAgent = msg.AgentID
		return m, nil

	case chat.SessionClearedMsg:
		m.session = session.Session{}
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent {
			if m.session.ID == msg.Payload.ID {
				// Merge to preserve fields like Title that the coordinator
				// does not include in cost/token update events.
				m.session = m.session.Merge(msg.Payload)
			}
		}
	}
	return m, nil
}

func (m *sidebarCmp) View() string {
	// Update cached items for navigation
	m.updateCachedItems()

	t := styles.CurrentTheme()
	parts := []string{}
	currentY := 0 // Track current Y position for mouse support

	style := t.S().Base.
		Padding(1)
	if m.compactMode {
		style = style.PaddingTop(0)
	}
	// Apply width after padding to account for horizontal frame size
	style = style.Width(m.width - style.GetHorizontalFrameSize())

	// Calculate available height for content (account for padding)
	paddingHeight := 2 // top + bottom padding
	if m.compactMode {
		paddingHeight = 1 // only bottom padding
	}
	maxContentHeight := m.height - paddingHeight

	// Ensure minimum content height for very small windows
	if maxContentHeight < 4 {
		maxContentHeight = 4
	}

	if !m.compactMode {
		if m.height > LogoHeightBreakpoint {
			parts = append(parts, m.logo)
			currentY += 7 // Approximate logo height
		} else {
			// Use a smaller logo for smaller screens
			parts = append(parts,
				logo.SmallRender(m.width-style.GetHorizontalFrameSize()),
				"")
			currentY += 2
		}
	}

	if !m.compactMode && m.session.ID != "" {
		parts = append(parts, t.S().Muted.Render(m.session.Title), "")
		currentY += 2
	} else if m.session.ID != "" {
		parts = append(parts, t.S().Text.Render(m.session.Title), "")
		currentY += 2
	}

	if !m.compactMode {
		parts = append(parts,
			m.cwd,
			"",
		)
		currentY += 2
	}

	modelBlock := m.currentModelBlock()
	parts = append(parts, modelBlock)
	// Count lines in model block
	currentY += strings.Count(modelBlock, "\n") + 1

	m.contentYStart = currentY

	// Vertical layout - render all content without height restrictions
	{
		// Show weaver first (special agent for creating other agents)
		weaverContent := m.weaverBlock()
		if weaverContent != "" {
			parts = append(parts, "", weaverContent)
			m.weaverYStart = currentY + 1 // +1 for empty line
		}

		// Show MCP servers (after weaver, before patterns)
		mcpContent := m.mcpServersBlock()
		if mcpContent != "" {
			parts = append(parts, "", mcpContent)
		}

		lspContent := m.lspBlock()
		if lspContent != "" {
			parts = append(parts, "", lspContent)
		}

		agentInfoContent := m.agentInfoBlock()
		if agentInfoContent != "" {
			parts = append(parts, "", agentInfoContent)
		}

		hintsContent := m.keyboardHintsBlock()
		if hintsContent != "" {
			parts = append(parts, "", hintsContent)
		}
	}

	// Join all content
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	contentLines := strings.Split(content, "\n")
	totalLines := len(contentLines)

	// Check if we need scrolling
	needsScroll := totalLines > maxContentHeight

	// If we need scroll indicator, reduce available content height by 2 (spacer + indicator)
	availableContentLines := maxContentHeight
	if needsScroll {
		availableContentLines = maxContentHeight - 2
	}

	// Ensure at least 1 line is always visible, even in very small windows
	if availableContentLines < 1 {
		availableContentLines = 1
		// Disable scroll indicator if we can't afford the space
		needsScroll = false
	}

	// Constrain scroll offset
	maxScroll := max(0, totalLines-availableContentLines)
	if m.scrollOffset > maxScroll {
		m.scrollOffset = maxScroll
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}

	// Extract visible portion based on scroll offset
	visibleStart := m.scrollOffset
	visibleEnd := min(visibleStart+availableContentLines, totalLines)
	visibleLines := contentLines[visibleStart:visibleEnd]

	visibleContent := strings.Join(visibleLines, "\n")

	// Add scroll indicator if there's overflow
	finalContent := visibleContent
	if needsScroll {
		scrollPct := float64(m.scrollOffset) / float64(maxScroll)
		scrollIndicator := m.renderScrollIndicator(scrollPct, m.scrollOffset > 0, visibleEnd < totalLines, t)
		// Add spacing above scroll indicator and center it
		spacer := ""
		// Use content width (after accounting for padding) for centering
		contentWidth := m.width - style.GetHorizontalFrameSize()
		centeredIndicator := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(scrollIndicator)
		finalContent = lipgloss.JoinVertical(lipgloss.Left, visibleContent, spacer, centeredIndicator)
	}

	return style.Height(m.height).Render(finalContent)
}

func (m *sidebarCmp) SetSize(width, height int) tea.Cmd {
	m.logo = m.logoBlock()
	m.cwd = cwd()
	m.width = width
	m.height = height
	return nil
}

func (m *sidebarCmp) GetSize() (int, int) {
	return m.width, m.height
}

func (m *sidebarCmp) logoBlock() string {
	return logo.Render(version.Version, true, logo.Opts{
		FieldColor:    charmtone.TeradataOrange,
		TitleColorA:   charmtone.TeradataCyan,
		TitleColorB:   charmtone.TeradataOrange,
		TeradataColor: charmtone.TeradataOrange,
		VersionColor:  charmtone.TeradataCyan,
		Width:         m.width - 2,
	})
}

func (m *sidebarCmp) getMaxWidth() int {
	return min(m.width-2, 58) // -2 for padding
}

func (m *sidebarCmp) lspBlock() string {
	// LSP not fully implemented in Loom yet
	return ""
}

//nolint:unused // Reserved for future MCP UI implementation
func (m *sidebarCmp) mcpBlock() string {
	// MCP not fully implemented in Loom yet
	return ""
}

// weaverBlock renders the Weaver section at the top of the sidebar.
// The weaver is a special agent for creating other agents via conversation.
func (m *sidebarCmp) weaverBlock() string {
	t := styles.CurrentTheme()

	// Find the weaver agent
	var weaverAgent *AgentInfo
	for i := range m.agents {
		if m.agents[i].Name == "weaver" {
			weaverAgent = &m.agents[i]
			break
		}
	}

	// Don't show section if weaver not available
	if weaverAgent == nil {
		return ""
	}

	var lines []string

	// Section header
	sectionHeader := "Weaver"
	if m.focused && m.selectedSection == SectionWeaver {
		sectionHeader = t.S().Base.Foreground(t.Primary).Render(sectionHeader)
	} else {
		sectionHeader = core.Section(sectionHeader, m.getMaxWidth())
	}
	lines = append(lines, sectionHeader)

	// Weaver entry
	isActive := weaverAgent.ID == m.currentAgent
	isSelected := m.focused && m.selectedSection == SectionWeaver

	// Icon: sparkles for weaver
	icon := "✨"
	iconColor := t.FgSubtle
	if isSelected || isActive {
		iconColor = t.Primary
	}

	styledIcon := t.S().Base.Foreground(iconColor).Render(icon)
	weaverName := weaverAgent.Name
	if weaverName == "" {
		weaverName = "weaver"
	}

	// Highlight selected/active
	titleColor := t.FgBase
	if isSelected {
		titleColor = t.Primary
		weaverName = "> " + weaverName
	} else if isActive {
		titleColor = t.Success
	}

	lines = append(lines,
		core.Status(
			core.StatusOpts{
				Icon:       styledIcon,
				Title:      weaverName,
				TitleColor: titleColor,
			},
			m.getMaxWidth(),
		),
	)

	// Add /agent-plan command hint when weaver is active
	if isActive {
		cmdHint := t.S().Base.Foreground(t.FgSubtle).PaddingLeft(2).Render("/agent-plan  guided planning")
		lines = append(lines, cmdHint)
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func formatTokensAndCost(tokens, contextWindow int64, cost float64) string {
	t := styles.CurrentTheme()
	// Format tokens in human-readable format (e.g., 110K, 1.2M)
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

	baseStyle := t.S().Base

	formattedCost := baseStyle.Foreground(t.FgMuted).Render(fmt.Sprintf("$%.2f", cost))

	formattedTokens = baseStyle.Foreground(t.FgSubtle).Render(fmt.Sprintf("(%s)", formattedTokens))
	formattedPercentage := baseStyle.Foreground(t.FgMuted).Render(fmt.Sprintf("%d%%", int(percentage)))
	formattedTokens = fmt.Sprintf("%s %s", formattedPercentage, formattedTokens)
	if percentage > 80 {
		// add the warning icon
		formattedTokens = fmt.Sprintf("%s %s", styles.WarningIcon, formattedTokens)
	}

	return fmt.Sprintf("%s %s", formattedTokens, formattedCost)
}

func (s *sidebarCmp) currentModelBlock() string {
	t := styles.CurrentTheme()

	// Determine model to display — use the most specific/current source available:
	// 1. session.Model (actual model from the last LLM cost report — most accurate)
	// 2. current agent's ModelInfo from the agents list (configured model)
	// 3. global config fallback
	var modelDisplayName string
	var showReasoning bool
	contextWindow := 200000 // Reasonable default for frontier models

	if s.session.Model != "" {
		// Best source: actual model reported in LLM cost info
		if s.session.Provider != "" {
			modelDisplayName = s.session.Provider + "/" + s.session.Model
		} else {
			modelDisplayName = s.session.Model
		}
	} else if s.activeProvider != "" {
		// Second: active provider name from ListProviders RPC (e.g., "gemini-flash")
		modelDisplayName = s.activeProvider
	} else if s.currentAgent != "" {
		// Third: agent's configured model from the agents list
		for _, ag := range s.agents {
			if ag.ID == s.currentAgent && ag.ModelInfo != "" {
				modelDisplayName = ag.ModelInfo
				break
			}
		}
	}

	if modelDisplayName == "" {
		// Fallback: global config (always available)
		model := config.Get().GetModel()
		modelDisplayName = model.Name
		showReasoning = model.CanReason()
		contextWindow = model.ContextWindow
	}

	modelIcon := t.S().Base.Foreground(t.FgSubtle).Render(styles.ModelIcon)
	modelName := t.S().Text.Render(modelDisplayName)
	parts := []string{fmt.Sprintf("%s %s", modelIcon, modelName)}

	if showReasoning {
		reasoningInfoStyle := t.S().Subtle.PaddingLeft(2)
		parts = append(parts, reasoningInfoStyle.Render("Thinking enabled"))
	}

	if s.session.ID != "" {
		// Use server-reported context window when available, otherwise fall back to default
		ctxWindow := int64(contextWindow)
		if s.session.ContextTokensMax > 0 {
			ctxWindow = s.session.ContextTokensMax
		}

		// Use server-reported context fill when available, otherwise sum tokens
		ctxUsed := int64(s.session.CompletionTokens + s.session.PromptTokens)
		if s.session.ContextTokensUsed > 0 {
			ctxUsed = s.session.ContextTokensUsed
		}

		parts = append(
			parts,
			"  "+formatTokensAndCost(
				ctxUsed,
				ctxWindow,
				s.session.Cost,
			),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// SetSession implements Sidebar.
func (m *sidebarCmp) SetSession(session session.Session) tea.Cmd {
	m.session = session
	return nil
}

// SetCompactMode sets the compact mode for the sidebar.
func (m *sidebarCmp) SetCompactMode(compact bool) {
	m.compactMode = compact
}

// Focus sets focus on the sidebar for agent selection.
func (m *sidebarCmp) Focus() {
	m.focused = true
	m.updateCachedItems()
	// Initialize selection to first available section (workflows -> agents -> patterns)
	if m.selectedSection == SectionNone {
		// Default to Weaver section
		m.selectedSection = SectionWeaver
		m.selectedIndex = 0
	}
}

// Blur removes focus from the sidebar.
func (m *sidebarCmp) Blur() {
	m.focused = false
}

// IsFocused returns whether the sidebar is focused.
func (m *sidebarCmp) IsFocused() bool {
	return m.focused
}

// updateCachedItems updates the cached lists for navigation
func (m *sidebarCmp) updateCachedItems() {
	// Cache pattern categories (used by pattern browser dialog)
	m.patternCategories = ListPatternCategories()
}

// resetSelectionIfNeeded resets selection if current selection is invalid
func (m *sidebarCmp) resetSelectionIfNeeded() {
	switch m.selectedSection {
	case SectionWeaver:
		// Weaver section only has one item (index 0)
		m.selectedIndex = 0
	case SectionMCP:
		maxMCPItems := m.getMCPNavigableItemCount()
		if m.selectedIndex >= maxMCPItems {
			m.selectedIndex = max(0, maxMCPItems-1)
		}
	}
}

// navigateUp moves selection up, crossing section boundaries
// Order: Weaver (top) -> MCP -> Patterns (bottom)
func (m *sidebarCmp) navigateUp() tea.Cmd {
	if m.selectedIndex > 0 {
		m.selectedIndex--
		return nil
	}

	// Move to previous section (going up: mcp -> weaver)
	switch m.selectedSection {
	case SectionMCP:
		m.selectedSection = SectionWeaver
		m.selectedIndex = 0
	case SectionWeaver:
		// Already at top
	}

	return nil
}

// navigateDown moves selection down, crossing section boundaries
// Order: Weaver (top) -> MCP (bottom)
func (m *sidebarCmp) navigateDown() tea.Cmd {
	maxIndex := 0
	switch m.selectedSection {
	case SectionWeaver:
		// Weaver section only has weaver (1 item)
		maxIndex = 0
	case SectionMCP:
		maxIndex = max(0, m.getMCPNavigableItemCount()-1)
	}

	if m.selectedIndex < maxIndex {
		m.selectedIndex++
		return nil
	}

	// Move to next section (going down: weaver -> mcp)
	switch m.selectedSection {
	case SectionWeaver:
		// Always allow navigation to MCP section (even if empty, user can press 'a' to add)
		m.selectedSection = SectionMCP
		m.selectedIndex = 0
	case SectionMCP:
		// Already at bottom
	}

	return nil
}

// getMCPNavigableItemCount returns the number of navigable items in MCP section
// (servers + visible tools from expanded servers)
func (m *sidebarCmp) getMCPNavigableItemCount() int {
	count := 0
	for _, server := range m.mcpServers {
		count++ // Server
		if m.expandedMCPServers[server.Name] {
			count += len(server.Tools) // Tools
		}
	}
	return count
}

// navigateToNextSection cycles to the next section
// Order: Weaver -> MCP -> Weaver (cycles)
//
//nolint:unused // Reserved for future keyboard navigation enhancement
func (m *sidebarCmp) navigateToNextSection() tea.Cmd {
	switch m.selectedSection {
	case SectionWeaver:
		// Always allow navigation to MCP section (even if empty, user can press 'a' to add)
		m.selectedSection = SectionMCP
		m.selectedIndex = 0
	case SectionMCP:
		// Wrap around to Weaver
		m.selectedSection = SectionWeaver
		m.selectedIndex = 0
	}
	return nil
}

// selectCurrentItem handles Enter key based on selected section
func (m *sidebarCmp) selectCurrentItem() tea.Cmd {
	switch m.selectedSection {
	case SectionWeaver:
		// Select weaver (only one item in this section)
		return util.CmdHandler(AgentSelectedMsg{
			AgentID: "weaver",
		})
	case SectionMCP:
		// Map selectedIndex to server or tool
		itemIndex := 0
		for _, server := range m.mcpServers {
			// Check if selecting the server line
			if itemIndex == m.selectedIndex {
				// Toggle expansion
				isExpanded := m.expandedMCPServers[server.Name]
				m.expandedMCPServers[server.Name] = !isExpanded

				// If expanding and tools not yet loaded, fetch them
				if !isExpanded && len(server.Tools) == 0 && server.Connected {
					// Send message to fetch tools for this server
					return util.CmdHandler(MCPServerSelectedMsg{
						ServerName: server.Name,
					})
				}
				return nil
			}
			itemIndex++

			// If expanded, check tools
			if m.expandedMCPServers[server.Name] {
				for _, tool := range server.Tools {
					if itemIndex == m.selectedIndex {
						// Selected a tool - show details
						return util.CmdHandler(MCPToolSelectedMsg{
							ServerName: server.Name,
							ToolName:   tool.Name,
							Tool:       tool,
						})
					}
					itemIndex++
				}
			}
		}
	}
	return nil
}

// handleMouseClick handles mouse click events and maps Y position to items
func (m *sidebarCmp) handleMouseClick(x, y int) tea.Cmd {
	// Check if click is within sidebar bounds
	if x < 0 || y < 0 || x >= m.width || y >= m.height {
		return nil
	}

	// Focus sidebar on click
	if !m.focused {
		m.focused = true
	}

	// Calculate which section and item was clicked
	// Account for padding (1 pixel) and scroll offset
	// The click Y is in viewport space, so we add scroll offset to get content space
	clickY := y - 1 + m.scrollOffset

	// Check weaver section (if visible)
	if m.weaverYStart > 0 && clickY >= m.weaverYStart {
		// Weaver section has header + items
		relativeY := clickY - m.weaverYStart
		// First line is section header
		if relativeY == 1 {
			// Clicked on weaver
			m.selectedSection = SectionWeaver
			m.selectedIndex = 0
			// Trigger selection (weaver)
			return util.CmdHandler(AgentSelectedMsg{AgentID: "weaver"})
		}
	}

	return nil
}

func cwd() string {
	cwd := config.Get().WorkingDir()
	t := styles.CurrentTheme()
	return t.S().Muted.Render(home.Short(cwd))
}

// PatternCategory represents a pattern category with count and file list.
type PatternCategory struct {
	Name  string
	Count int
	Files []string // List of pattern file paths in this category
}

// ListPatternCategories scans $LOOM_DATA_DIR/patterns and returns categories.
func ListPatternCategories() []PatternCategory {
	loomDir, err := home.Dir()
	if err != nil {
		return nil
	}

	patternsDir := filepath.Join(loomDir, "patterns")
	entries, err := os.ReadDir(patternsDir)
	if err != nil {
		return nil
	}

	var categories []PatternCategory
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		categoryPath := filepath.Join(patternsDir, name)
		// Get list of YAML files in subdirectory
		files := listYAMLFiles(categoryPath)
		if len(files) > 0 {
			categories = append(categories, PatternCategory{
				Name:  name,
				Count: len(files),
				Files: files,
			})
		}
	}
	return categories
}

// listYAMLFiles returns a list of .yaml and .yml file paths in a directory.
func listYAMLFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			files = append(files, path)
		}
		return nil
	})
	// Sort files for stable ordering
	sort.Strings(files)
	return files
}

// patternsBlock renders the patterns section with expansion support.
// mcpServersBlock renders the MCP servers section
func (m *sidebarCmp) mcpServersBlock() string {
	t := styles.CurrentTheme()

	// Section header with focus indicator
	sectionHeader := "MCP Servers"
	if m.focused && m.selectedSection == SectionMCP {
		sectionHeader = t.S().Base.Foreground(t.Primary).Render(sectionHeader)
	} else {
		sectionHeader = core.Section(sectionHeader, m.getMaxWidth())
	}

	// If no servers configured, show message
	if len(m.mcpServers) == 0 {
		noServers := t.S().Subtle.Render("  No servers configured")
		return lipgloss.JoinVertical(lipgloss.Left, sectionHeader, noServers)
	}

	parts := []string{sectionHeader}

	// Track item index for selection (servers + visible tools from expanded servers)
	itemIndex := 0

	// Render each server
	for _, server := range m.mcpServers {
		isExpanded := m.expandedMCPServers[server.Name]
		isSelected := m.focused && m.selectedSection == SectionMCP && itemIndex == m.selectedIndex

		// Single icon logic for servers (play button since they're expandable):
		// - If expanded: ▼ (down triangle)
		// - Else: ▶ (play button/right triangle)
		var icon string
		if isExpanded {
			icon = "▼"
		} else {
			icon = "▶"
		}

		// Icon color: primary if selected, otherwise status color
		iconColor := t.FgSubtle
		if isSelected {
			iconColor = t.Primary
		} else if server.Connected {
			iconColor = t.Success
		} else if server.Enabled {
			iconColor = t.Warning
		}

		styledIcon := t.S().Base.Foreground(iconColor).Render(icon)

		// Server name style
		nameStyle := t.S().Text
		nameText := server.Name
		if isSelected {
			nameStyle = t.S().Text.Foreground(t.Primary)
			nameText = "> " + nameText
		}
		name := nameStyle.Render(nameText)

		// Tool count if available
		toolInfo := ""
		if server.ToolCount > 0 {
			toolInfo = t.S().Muted.Render(fmt.Sprintf(" (%d)", server.ToolCount))
		}

		// Error indicator if there's an error
		errorIndicator := ""
		if server.Error != "" {
			errorIndicator = t.S().Base.Foreground(t.Error).Render(" !")
		}

		parts = append(parts, fmt.Sprintf("%s %s%s%s", styledIcon, name, toolInfo, errorIndicator))
		itemIndex++

		// If expanded, show tools or loading message
		if isExpanded {
			if len(server.Tools) == 0 {
				// Show loading indicator if tools haven't been fetched yet
				if server.Connected && server.ToolCount > 0 {
					loadingMsg := t.S().Subtle.Render(fmt.Sprintf("    Loading %d tools...", server.ToolCount))
					parts = append(parts, loadingMsg)
				} else if server.Connected {
					loadingMsg := t.S().Subtle.Render("    Loading tools...")
					parts = append(parts, loadingMsg)
				} else {
					noToolsMsg := t.S().Subtle.Render("    No tools available")
					parts = append(parts, noToolsMsg)
				}
			} else {
				// Show tools
				for _, tool := range server.Tools {
					toolIsSelected := m.focused && m.selectedSection == SectionMCP && itemIndex == m.selectedIndex

					// Tool icon: record button
					toolIcon := "⏺"
					toolIconColor := t.FgSubtle
					if toolIsSelected {
						toolIconColor = t.Primary
					}
					toolStyledIcon := t.S().Base.Foreground(toolIconColor).Render(toolIcon)

					// Tool name
					toolNameStyle := t.S().Subtle
					toolNameText := tool.Name
					if toolIsSelected {
						toolNameStyle = t.S().Text.Foreground(t.Primary)
						toolNameText = "> " + toolNameText
					}

					// Build tool line with indentation
					toolPrefix := t.S().Base.Foreground(t.FgSubtle).Render("  └─ ")
					toolLine := fmt.Sprintf("%s%s %s", toolPrefix, toolStyledIcon, toolNameStyle.Render(toolNameText))
					parts = append(parts, toolLine)
					itemIndex++
				}
			}
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// agentInfoBlock renders a brief info block about the current agent.
func (m *sidebarCmp) agentInfoBlock() string {
	if m.currentAgent == "" {
		return ""
	}

	// Find the current agent in the agent list
	var currentAgent *AgentInfo
	for i := range m.agents {
		if m.agents[i].ID == m.currentAgent {
			currentAgent = &m.agents[i]
			break
		}
	}
	if currentAgent == nil {
		return ""
	}

	t := styles.CurrentTheme()
	header := core.Section("Agent", m.getMaxWidth())
	parts := []string{header}

	// Status dot + agent name
	statusDot := "●"
	dotColor := t.FgSubtle
	switch currentAgent.Status {
	case "active", "ready":
		dotColor = t.Success
	case "busy", "working":
		dotColor = t.Warning
	case "error":
		dotColor = t.Error
	}
	dot := t.S().Base.Foreground(dotColor).Render(statusDot)
	name := t.S().Text.Render(currentAgent.Name)
	parts = append(parts, fmt.Sprintf("%s %s", dot, name))

	// Model info if non-empty
	if currentAgent.ModelInfo != "" {
		modelLine := t.S().Muted.PaddingLeft(2).Render(currentAgent.ModelInfo)
		parts = append(parts, modelLine)
	}

	// Role LLM count if > 0
	if currentAgent.RoleLLMCount > 0 {
		roleLine := t.S().Subtle.PaddingLeft(2).Render(fmt.Sprintf("%d role model(s)", currentAgent.RoleLLMCount))
		parts = append(parts, roleLine)
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// keyboardHintsBlock renders contextual keyboard shortcuts and slash command reference.
func (m *sidebarCmp) keyboardHintsBlock() string {
	t := styles.CurrentTheme()

	type hint struct {
		key  string
		desc string
	}

	// --- keyboard shortcuts ---
	keyHints := []hint{
		{"tab", "cycle focus"},
		{"↑/↓", "navigate"},
		{"enter", "select"},
		{"ctrl+n", "new session"},
		{"ctrl+e", "agents"},
		{"ctrl+w", "workflows"},
	}

	// --- slash commands ---
	slashHints := []hint{
		{"/clear", "clear chat"},
		{"/sessions", "sessions"},
		{"/model", "model"},
		{"/agents", "agents"},
		{"/workflows", "workflows"},
		{"/agent-plan", "plan agent"},
		{"/sidebar", "sidebar"},
		{"/apps", "apps"},
		{"/mcp", "add MCP"},
		{"/patterns", "patterns"},
		{"/quit", "exit"},
		{"/help", "help"},
	}

	renderHints := func(hints []hint) []string {
		var lines []string
		// Fixed column width for the key (command) side; use lipgloss Width so
		// ANSI codes don't break alignment.
		const colW = 12
		for _, h := range hints {
			keyRendered := t.S().Base.Foreground(t.FgMuted).Width(colW).Render(h.key)
			descRendered := t.S().Base.Foreground(t.FgSubtle).Render(h.desc)
			lines = append(lines, "  "+keyRendered+descRendered)
		}
		return lines
	}

	var lines []string
	lines = append(lines, core.Section("Keys", m.getMaxWidth()))
	lines = append(lines, renderHints(keyHints)...)
	lines = append(lines, "")
	lines = append(lines, core.Section("Slash", m.getMaxWidth()))
	lines = append(lines, renderHints(slashHints)...)

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// scrollUp scrolls the sidebar content up by the specified number of lines
func (m *sidebarCmp) scrollUp(lines int) {
	m.scrollOffset -= lines
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// scrollDown scrolls the sidebar content down by the specified number of lines
func (m *sidebarCmp) scrollDown(lines int) {
	m.scrollOffset += lines
}

// renderScrollIndicator renders a scroll position indicator
func (m *sidebarCmp) renderScrollIndicator(pct float64, hasAbove bool, hasBelow bool, t *styles.Theme) string {
	// For vertical scrolling in sidebar, show simple up/down indicators
	upArrow := "▲"
	downArrow := "▼"

	upColor := t.FgSubtle
	downColor := t.FgSubtle

	if hasAbove {
		upColor = t.Primary
	}
	if hasBelow {
		downColor = t.Primary
	}

	upStyled := t.S().Base.Foreground(upColor).Render(upArrow)
	downStyled := t.S().Base.Foreground(downColor).Render(downArrow)

	// Show scroll position as percentage
	scrollPctText := fmt.Sprintf(" %d%% ", int(pct*100))
	scrollPctStyled := t.S().Base.Foreground(t.FgMuted).Render(scrollPctText)

	return lipgloss.JoinHorizontal(lipgloss.Center, upStyled, scrollPctStyled, downStyled)
}
