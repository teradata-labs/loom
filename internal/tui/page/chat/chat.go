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
package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/app"
	"github.com/teradata-labs/loom/internal/config"
	"github.com/teradata-labs/loom/internal/message"
	"github.com/teradata-labs/loom/internal/permission"
	"github.com/teradata-labs/loom/internal/pubsub"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/internal/tui/adapter"
	"github.com/teradata-labs/loom/internal/tui/components/anim"
	"github.com/teradata-labs/loom/internal/tui/components/chat"
	"github.com/teradata-labs/loom/internal/tui/components/chat/editor"
	"github.com/teradata-labs/loom/internal/tui/components/chat/header"
	"github.com/teradata-labs/loom/internal/tui/components/chat/messages"
	"github.com/teradata-labs/loom/internal/tui/components/chat/sidebar"
	"github.com/teradata-labs/loom/internal/tui/components/chat/splash"
	"github.com/teradata-labs/loom/internal/tui/components/completions"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/core/layout"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs/clarification"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs/claude"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs/commands"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs/filepicker"
	mcpdialog "github.com/teradata-labs/loom/internal/tui/components/dialogs/mcp"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs/pattern"
	"github.com/teradata-labs/loom/internal/tui/page"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/internal/version"
	"github.com/teradata-labs/loom/pkg/metaagent"
)

var ChatPageID page.PageID = "chat"

type (
	ChatFocusedMsg struct {
		Focused bool
	}
	CancelTimerExpiredMsg struct{}

	// AgentListRefreshTickMsg is sent periodically to refresh the agent list from server
	AgentListRefreshTickMsg struct{}

	// AddMCPServerSuccessMsg is sent when an MCP server is successfully added
	AddMCPServerSuccessMsg struct {
		ServerName string
	}
)

type PanelType string

const (
	PanelTypeChat    PanelType = "chat"
	PanelTypeEditor  PanelType = "editor"
	PanelTypeSplash  PanelType = "splash"
	PanelTypeSidebar PanelType = "sidebar"
)

// PillSection represents which pill section is focused when in pills panel.
type PillSection int

const (
	PillSectionTodos PillSection = iota
	PillSectionQueue
)

const (
	CompactModeWidthBreakpoint  = 120 // Width at which the chat page switches to compact mode
	CompactModeHeightBreakpoint = 30  // Height at which the chat page switches to compact mode
	EditorHeight                = 6   // Height of the editor input area including padding
	SideBarWidth                = 31  // Width of the sidebar
	SideBarDetailsPadding       = 1   // Padding for the sidebar details section
	HeaderHeight                = 1   // Height of the header

	// Layout constants for borders and padding
	BorderWidth        = 1 // Width of component borders
	LeftRightBorders   = 2 // Left + right border width (1 + 1)
	TopBottomBorders   = 2 // Top + bottom border width (1 + 1)
	DetailsPositioning = 2 // Positioning adjustment for details panel

	// Timing constants
	CancelTimerDuration      = 2 * time.Second // Duration before cancel timer expires
	AgentListRefreshInterval = 3 * time.Second // Interval for refreshing agent list from server
)

type ChatPage interface {
	util.Model
	layout.Help
	IsChatFocused() bool
}

// cancelTimerCmd creates a command that expires the cancel timer
func cancelTimerCmd() tea.Cmd {
	return tea.Tick(CancelTimerDuration, func(time.Time) tea.Msg {
		return CancelTimerExpiredMsg{}
	})
}

// agentListRefreshCmd creates a periodic command to refresh the agent list
func agentListRefreshCmd() tea.Cmd {
	return tea.Tick(AgentListRefreshInterval, func(time.Time) tea.Msg {
		return AgentListRefreshTickMsg{}
	})
}

type chatPage struct {
	width, height               int
	detailsWidth, detailsHeight int
	app                         *app.App
	keyboardEnhancements        tea.KeyboardEnhancementsMsg

	// Layout state
	compact      bool
	forceCompact bool
	focusedPane  PanelType

	// Session
	session        session.Session
	agentSessions  map[string]string // Map of agentID -> sessionID for session persistence per agent
	currentAgentID string            // Track current agent to know which session to use
	keyMap         KeyMap

	// Components
	header  header.Header
	sidebar sidebar.Sidebar
	chat    chat.MessageListCmp
	editor  editor.Editor
	splash  splash.Splash

	// Simple state flags
	showingDetails   bool
	isCanceling      bool
	splashFullScreen bool
	isOnboarding     bool
	isProjectInit    bool
	promptQueue      int

	// Pills state
	pillsExpanded      bool
	focusedPillSection PillSection

	// Todo spinner
	todoSpinner spinner.Model
}

func New(app *app.App) ChatPage {
	t := styles.CurrentTheme()
	return &chatPage{
		app:           app,
		keyMap:        DefaultKeyMap(),
		agentSessions: make(map[string]string), // Initialize agent session map
		header:        header.New(app.LSPClients),
		sidebar:       sidebar.New(app.History, app.LSPClients, false),
		chat:          chat.New(app),
		editor:        editor.New(app),
		splash:        splash.New(),
		focusedPane:   PanelTypeSplash,
		todoSpinner: spinner.New(
			spinner.WithSpinner(spinner.MiniDot),
			spinner.WithStyle(t.S().Base.Foreground(t.GreenDark)),
		),
	}
}

func (p *chatPage) Init() tea.Cmd {
	cfg := config.Get()
	compact := cfg.Options.TUI.CompactMode
	p.compact = compact
	p.forceCompact = compact
	p.sidebar.SetCompactMode(p.compact)

	// Initialize currentAgentID from coordinator if it was set before TUI creation
	// (e.g., via CLI agent selection)
	if p.currentAgentID == "" {
		if coord, ok := p.app.AgentCoordinator.(interface{ GetAgentID() string }); ok {
			p.currentAgentID = coord.GetAgentID()
			// Debug: log what we got from coordinator
			if f, err := os.OpenFile("/tmp/loom-chatpage-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				fmt.Fprintf(f, "[%s] chatPage.Init: Got currentAgentID from coordinator: '%s'\n", time.Now().Format("15:04:05"), p.currentAgentID)
				f.Close()
			}

			// Update splash screen with agent info
			p.splash.SetAgentInfo(p.currentAgentID, p.currentAgentID)
		}
	}

	// Set splash state based on config
	if !config.HasInitialDataConfig() {
		// First-time setup: show model selection
		p.splash.SetOnboarding(true)
		p.isOnboarding = true
		p.splashFullScreen = true
	} else if b, _ := config.ProjectNeedsInitialization(); b {
		// Project needs context initialization
		p.splash.SetProjectInit(true)
		p.isProjectInit = true
		p.splashFullScreen = true
	} else {
		// Ready to chat: focus editor, splash in background
		p.focusedPane = PanelTypeEditor
		p.splashFullScreen = false
	}

	return tea.Batch(
		p.header.Init(),
		p.sidebar.Init(),
		p.chat.Init(),
		p.editor.Init(),
		p.splash.Init(),
		p.fetchAgentsList(),   // Fetch agents list on init
		p.fetchMCPServers(),   // Fetch MCP servers on init
		agentListRefreshCmd(), // Start periodic agent list refresh
	)
}

func (p *chatPage) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if p.session.ID != "" && p.app.AgentCoordinator != nil {
		queueSize := p.app.AgentCoordinator.QueuedPrompts()
		if queueSize != p.promptQueue {
			p.promptQueue = queueSize
			cmds = append(cmds, p.SetSize(p.width, p.height))
		}
	}
	switch msg := msg.(type) {
	case tea.KeyboardEnhancementsMsg:
		p.keyboardEnhancements = msg
		return p, nil
	case tea.MouseWheelMsg:
		if p.compact {
			msg.Y -= 1
		}
		if p.isMouseOverChat(msg.X, msg.Y) {
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			return p, cmd
		} else if p.isMouseOverSidebar(msg.X, msg.Y) {
			u, cmd := p.sidebar.Update(msg)
			p.sidebar = u.(sidebar.Sidebar)
			return p, cmd
		}
		return p, nil
	case tea.MouseClickMsg:
		if p.isOnboarding || p.isProjectInit {
			return p, nil
		}
		if p.compact {
			msg.Y -= 1
		}
		if p.isMouseOverChat(msg.X, msg.Y) {
			p.focusedPane = PanelTypeChat
			p.chat.Focus()
			p.editor.Blur()
			p.sidebar.Blur()
		} else if p.isMouseOverSidebar(msg.X, msg.Y) {
			p.focusedPane = PanelTypeSidebar
			p.sidebar.Focus()
			p.chat.Blur()
			p.editor.Blur()
			// Forward click to sidebar for item selection
			u, cmd := p.sidebar.Update(msg)
			p.sidebar = u.(sidebar.Sidebar)
			return p, cmd
		} else {
			p.focusedPane = PanelTypeEditor
			p.editor.Focus()
			p.chat.Blur()
			p.sidebar.Blur()
		}
		u, cmd := p.chat.Update(msg)
		p.chat = u.(chat.MessageListCmp)
		return p, cmd
	case tea.MouseMotionMsg:
		if p.compact {
			msg.Y -= 1
		}
		if msg.Button == tea.MouseLeft {
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			return p, cmd
		}
		return p, nil
	case tea.MouseReleaseMsg:
		if p.isOnboarding || p.isProjectInit {
			return p, nil
		}
		if p.compact {
			msg.Y -= 1
		}
		if msg.Button == tea.MouseLeft {
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			return p, cmd
		}
		return p, nil
	case chat.SelectionCopyMsg:
		u, cmd := p.chat.Update(msg)
		p.chat = u.(chat.MessageListCmp)
		return p, cmd
	case tea.WindowSizeMsg:
		u, cmd := p.editor.Update(msg)
		p.editor = u.(editor.Editor)
		return p, tea.Batch(p.SetSize(msg.Width, msg.Height), cmd)
	case CancelTimerExpiredMsg:
		p.isCanceling = false
		return p, nil
	case AgentListRefreshTickMsg:
		// Periodically refresh agent list from server to pick up new agents created by weaver
		return p, tea.Batch(p.fetchAgentsList(), agentListRefreshCmd())
	case editor.OpenEditorMsg:
		u, cmd := p.editor.Update(msg)
		p.editor = u.(editor.Editor)
		return p, cmd
	case chat.SendMsg:
		return p, p.sendMessage(msg.Text, msg.Attachments)
	case editor.ClearCurrentSessionMsg:
		// User wants to clear the current agent's session and start fresh
		if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
			return p, util.ReportWarn("Agent is busy, please wait before clearing the session...")
		}
		return p, p.newSession()
	case chat.SessionSelectedMsg:
		return p, p.setSession(msg)
	case splash.SubmitAPIKeyMsg:
		u, cmd := p.splash.Update(msg)
		p.splash = u.(splash.Splash)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)
	case commands.ToggleCompactModeMsg:
		p.forceCompact = !p.forceCompact
		var cmd tea.Cmd
		if p.forceCompact {
			p.setCompactMode(true)
			cmd = p.updateCompactConfig(true)
		} else if p.width >= CompactModeWidthBreakpoint && p.height >= CompactModeHeightBreakpoint {
			p.setCompactMode(false)
			cmd = p.updateCompactConfig(false)
		}
		return p, tea.Batch(p.SetSize(p.width, p.height), cmd)
	case commands.OpenExternalEditorMsg:
		u, cmd := p.editor.Update(msg)
		p.editor = u.(editor.Editor)
		return p, cmd
	case pubsub.Event[session.Session]:
		if msg.Payload.ID == p.session.ID {
			prevHasIncompleteTodos := hasIncompleteTodos(p.session.Todos)
			prevHasInProgress := p.hasInProgressTodo()
			p.session = msg.Payload
			newHasIncompleteTodos := hasIncompleteTodos(p.session.Todos)
			newHasInProgress := p.hasInProgressTodo()
			if prevHasIncompleteTodos != newHasIncompleteTodos {
				cmds = append(cmds, p.SetSize(p.width, p.height))
			}
			if !prevHasInProgress && newHasInProgress {
				cmds = append(cmds, p.todoSpinner.Tick)
			}
		}
		u, cmd := p.header.Update(msg)
		p.header = u.(header.Header)
		cmds = append(cmds, cmd)
		u, cmd = p.sidebar.Update(msg)
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)
	case chat.SessionClearedMsg:
		u, cmd := p.header.Update(msg)
		p.header = u.(header.Header)
		cmds = append(cmds, cmd)
		u, cmd = p.sidebar.Update(msg)
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)
		u, cmd = p.chat.Update(msg)
		p.chat = u.(chat.MessageListCmp)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)
	case filepicker.FilePickedMsg,
		completions.CompletionsClosedMsg,
		completions.SelectCompletionMsg:
		u, cmd := p.editor.Update(msg)
		p.editor = u.(editor.Editor)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)

	case claude.ValidationCompletedMsg, claude.AuthenticationCompleteMsg:
		if p.focusedPane == PanelTypeSplash {
			u, cmd := p.splash.Update(msg)
			p.splash = u.(splash.Splash)
			cmds = append(cmds, cmd)
		}
		return p, tea.Batch(cmds...)
	case pubsub.Event[message.Message],
		anim.StepMsg,
		spinner.TickMsg:
		// Update todo spinner if agent is busy and we have in-progress todos
		agentBusy := p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID)
		if _, ok := msg.(spinner.TickMsg); ok && p.hasInProgressTodo() && agentBusy {
			var cmd tea.Cmd
			p.todoSpinner, cmd = p.todoSpinner.Update(msg)
			cmds = append(cmds, cmd)
		}
		// Start spinner when agent becomes busy and we have in-progress todos
		if _, ok := msg.(pubsub.Event[message.Message]); ok && p.hasInProgressTodo() && agentBusy {
			cmds = append(cmds, p.todoSpinner.Tick)
		}
		if p.focusedPane == PanelTypeSplash {
			u, cmd := p.splash.Update(msg)
			p.splash = u.(splash.Splash)
			cmds = append(cmds, cmd)
		} else {
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			cmds = append(cmds, cmd)
		}

		return p, tea.Batch(cmds...)
	case commands.ToggleYoloModeMsg:
		// update the editor style
		u, cmd := p.editor.Update(msg)
		p.editor = u.(editor.Editor)
		return p, cmd
	case sidebar.AgentsListMsg:
		u, cmd := p.sidebar.Update(msg)
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)

	case sidebar.MCPServersListMsg:
		u, cmd := p.sidebar.Update(msg)
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)

	case MCPServerToolsMsg:
		// Convert to sidebar message type and forward
		u, cmd := p.sidebar.Update(sidebar.UpdateMCPServerToolsMsg{
			ServerName: msg.ServerName,
			Tools:      msg.Tools,
		})
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)

	case AddMCPServerSuccessMsg:
		// MCP server successfully added - show success message and refresh the list
		return p, tea.Batch(
			util.ReportInfo(fmt.Sprintf("Successfully added MCP server '%s'", msg.ServerName)),
			p.fetchMCPServers(),
		)

	case metaagent.QuestionAskedMsg:
		// Agent asked a clarification question - show clarification dialog
		// Pass client, sessionID, agentID, and RPC timeout for RPC-based answering
		dialog := clarification.NewClarificationDialogCmp(
			msg.Question,
			p.app.Client(),
			p.session.ID,
			p.currentAgentID,
			0, // Use default RPC timeout (5s)
		)
		return p, util.CmdHandler(dialogs.OpenDialogMsg{Model: dialog})

	case sidebar.AgentSelectedMsg:
		// Debug: log agent selection
		if f, err := os.OpenFile("/tmp/chatpage-selection-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] AgentSelectedMsg received: msg.AgentID=%s, p.currentAgentID=%s\n",
				time.Now().Format("15:04:05"), msg.AgentID, p.currentAgentID)
			f.Close()
		}

		// Switch to the selected agent
		// Store current session for the previous agent
		if p.currentAgentID != "" && p.session.ID != "" {
			p.agentSessions[p.currentAgentID] = p.session.ID
		}

		// Set new agent ID
		p.currentAgentID = msg.AgentID
		p.app.SetAgentID(msg.AgentID)

		// Update splash screen with agent info
		p.splash.SetAgentInfo(msg.AgentID, msg.AgentID)

		// Debug: log after setting
		if f, err := os.OpenFile("/tmp/chatpage-selection-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] After setting: p.currentAgentID=%s\n",
				time.Now().Format("15:04:05"), p.currentAgentID)
			f.Close()
		}

		// Check if we have an existing session for this agent
		if existingSessionID, exists := p.agentSessions[msg.AgentID]; exists {
			// Load the existing session for this agent
			ctx := context.Background()
			sess, err := p.app.Sessions.Get(ctx, existingSessionID)
			if err == nil {
				// Successfully loaded existing session - use setSession to load everything
				// Make sure editor is focused
				p.focusedPane = PanelTypeEditor
				p.editor.Focus()
				p.chat.Blur()
				p.sidebar.Blur()
				// Fetch and send updated agents list to sidebar with new current agent
				return p, tea.Batch(p.setSession(sess), p.fetchAgentsList())
			}
			// If loading failed, fall through to create a new session
		}

		// No existing session for this agent - clear current session
		// A new one will be created on the next message
		p.session = session.Session{}

		// Notify chat and header components about session change
		u, cmd := p.chat.Update(chat.SessionClearedMsg{})
		p.chat = u.(chat.MessageListCmp)
		cmds = append(cmds, cmd)

		u, cmd = p.header.Update(chat.SessionClearedMsg{})
		p.header = u.(header.Header)
		cmds = append(cmds, cmd)

		u, cmd = p.sidebar.Update(chat.SessionClearedMsg{})
		p.sidebar = u.(sidebar.Sidebar)
		cmds = append(cmds, cmd)

		// Focus editor so user can start typing
		p.focusedPane = PanelTypeEditor
		p.editor.Focus()
		p.chat.Blur()
		p.sidebar.Blur()

		// Fetch and send updated agents list to sidebar with new current agent
		cmds = append(cmds, p.fetchAgentsList())

		return p, tea.Batch(cmds...)

	case sidebar.PatternCategorySelectedMsg:
		// Pattern categories are now expanded/collapsed in sidebar
		// No action needed here
		return p, nil

	case sidebar.PatternFileSelectedMsg:
		// Open pattern file in editor dialog
		return p, util.CmdHandler(dialogs.OpenDialogMsg{
			Model: pattern.NewPatternEditorDialog(msg.FilePath),
		})

	case sidebar.MCPServerSelectedMsg:
		// Handle MCP server selection - fetch tools for this server
		return p, p.fetchMCPServerTools(msg.ServerName)

	case sidebar.MCPToolSelectedMsg:
		// Handle MCP tool selection - show tool details dialog
		dialog := mcpdialog.NewToolDetailsDialog(msg.ServerName, msg.Tool)
		return p, util.CmdHandler(dialogs.OpenDialogMsg{Model: dialog})

	case sidebar.AddMCPServerActionMsg:
		// Open add MCP server dialog
		dialog := mcpdialog.NewAddMCPServerDialog(
			p.app.Client(),
			func(req *loomv1.AddMCPServerRequest) tea.Cmd {
				return p.handleAddMCPServer(req)
			},
		)
		return p, util.CmdHandler(dialogs.OpenDialogMsg{Model: dialog})

	case pubsub.Event[permission.PermissionNotification]:
		u, cmd := p.chat.Update(msg)
		p.chat = u.(chat.MessageListCmp)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)

	case commands.CommandRunCustomMsg:
		if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
			return p, util.ReportWarn("Agent is busy, please wait before executing a command...")
		}

		cmd := p.sendMessage(msg.Content, nil)
		if cmd != nil {
			return p, cmd
		}
	case splash.OnboardingCompleteMsg:
		p.splashFullScreen = false
		if b, _ := config.ProjectNeedsInitialization(); b {
			p.splash.SetProjectInit(true)
			p.splashFullScreen = true
			return p, p.SetSize(p.width, p.height)
		}
		err := p.app.InitCoderAgent()
		if err != nil {
			return p, util.ReportError(err)
		}
		p.isOnboarding = false
		p.isProjectInit = false
		p.focusedPane = PanelTypeEditor
		return p, p.SetSize(p.width, p.height)
	case commands.NewSessionsMsg:
		if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
			return p, util.ReportWarn("Agent is busy, please wait before starting a new session...")
		}
		return p, p.newSession()
	case tea.KeyPressMsg:
		// If conflict dialog is showing, route input to it

		switch {
		case key.Matches(msg, p.keyMap.NewSession):
			// if we have no agent do nothing
			if p.app.AgentCoordinator == nil {
				return p, nil
			}
			if p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
				return p, util.ReportWarn("Agent is busy, please wait before starting a new session...")
			}
			return p, p.newSession()
		case key.Matches(msg, p.keyMap.AddAttachment):
			// Skip attachment handling during onboarding/splash screen
			if p.focusedPane == PanelTypeSplash || p.isOnboarding {
				u, cmd := p.splash.Update(msg)
				p.splash = u.(splash.Splash)
				return p, cmd
			}
			_ = config.Get().Agents() // Ensure config is initialized
			model := config.Get().GetModelByType(config.SelectedModelTypeLarge)
			if model == nil {
				return p, util.ReportWarn("No model configured yet")
			}
			if model.SupportsImages {
				return p, util.CmdHandler(commands.OpenFilePickerMsg{})
			} else {
				return p, util.ReportWarn("File attachments are not supported by the current model: " + model.Name)
			}
		case key.Matches(msg, p.keyMap.Tab):
			// Tab always changes focus between panes (editor -> chat/splash -> sidebar -> editor)
			// Use arrow keys to navigate within the sidebar
			return p, p.changeFocus()
		case key.Matches(msg, p.keyMap.Cancel):
			if p.session.ID != "" && p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
				return p, p.cancel()
			}
		case key.Matches(msg, p.keyMap.Details):
			p.toggleDetails()
			return p, nil
		case key.Matches(msg, p.keyMap.TogglePills):
			if p.session.ID != "" {
				return p, p.togglePillsExpanded()
			}
		case key.Matches(msg, p.keyMap.PillLeft):
			if p.session.ID != "" && p.pillsExpanded {
				return p, p.switchPillSection(-1)
			}
		case key.Matches(msg, p.keyMap.PillRight):
			if p.session.ID != "" && p.pillsExpanded {
				return p, p.switchPillSection(1)
			}
		}

		switch p.focusedPane {
		case PanelTypeChat:
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			cmds = append(cmds, cmd)
		case PanelTypeEditor:
			u, cmd := p.editor.Update(msg)
			p.editor = u.(editor.Editor)
			cmds = append(cmds, cmd)
		case PanelTypeSidebar:
			u, cmd := p.sidebar.Update(msg)
			p.sidebar = u.(sidebar.Sidebar)
			cmds = append(cmds, cmd)
		case PanelTypeSplash:
			u, cmd := p.splash.Update(msg)
			p.splash = u.(splash.Splash)
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		switch p.focusedPane {
		case PanelTypeEditor:
			u, cmd := p.editor.Update(msg)
			p.editor = u.(editor.Editor)
			cmds = append(cmds, cmd)
			return p, tea.Batch(cmds...)
		case PanelTypeChat:
			u, cmd := p.chat.Update(msg)
			p.chat = u.(chat.MessageListCmp)
			cmds = append(cmds, cmd)
			return p, tea.Batch(cmds...)
		case PanelTypeSplash:
			u, cmd := p.splash.Update(msg)
			p.splash = u.(splash.Splash)
			cmds = append(cmds, cmd)
			return p, tea.Batch(cmds...)
		}
	}
	return p, tea.Batch(cmds...)
}

func (p *chatPage) Cursor() *tea.Cursor {
	if p.header.ShowingDetails() {
		return nil
	}
	switch p.focusedPane {
	case PanelTypeEditor:
		return p.editor.Cursor()
	case PanelTypeSplash:
		return p.splash.Cursor()
	default:
		return nil
	}
}

func (p *chatPage) View() string {
	var chatView string
	t := styles.CurrentTheme()

	if p.session.ID == "" {
		splashView := p.splash.View()
		editorView := p.editor.View()

		// Full screen during onboarding or project initialization (no agents configured yet)
		if p.splashFullScreen {
			chatView = splashView
		} else if p.compact {
			// Compact mode: splash + editor stacked vertically
			chatView = lipgloss.JoinVertical(
				lipgloss.Left,
				t.S().Base.Render(splashView),
				editorView,
			)
		} else {
			// Non-compact mode with sidebar: splash in chat area + sidebar
			sidebarView := p.sidebar.View()
			messagesColumn := lipgloss.JoinVertical(
				lipgloss.Left,
				splashView,
			)
			messages := lipgloss.JoinHorizontal(
				lipgloss.Left,
				messagesColumn,
				sidebarView,
			)
			chatView = lipgloss.JoinVertical(
				lipgloss.Left,
				messages,
				editorView,
			)
		}
	} else {
		messagesView := p.chat.View()
		editorView := p.editor.View()

		hasIncompleteTodos := hasIncompleteTodos(p.session.Todos)
		hasQueue := p.promptQueue > 0
		todosFocused := p.pillsExpanded && p.focusedPillSection == PillSectionTodos
		queueFocused := p.pillsExpanded && p.focusedPillSection == PillSectionQueue

		// Use spinner when agent is busy, otherwise show static icon
		agentBusy := p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID)
		inProgressIcon := t.S().Base.Foreground(t.GreenDark).Render(styles.CenterSpinnerIcon)
		if agentBusy {
			inProgressIcon = p.todoSpinner.View()
		}

		var pills []string
		if hasIncompleteTodos {
			pills = append(pills, todoPill(p.session.Todos, inProgressIcon, todosFocused, p.pillsExpanded, t))
		}
		if hasQueue {
			pills = append(pills, queuePill(p.promptQueue, queueFocused, p.pillsExpanded, t))
		}

		var expandedList string
		if p.pillsExpanded {
			if todosFocused && hasIncompleteTodos {
				expandedList = todoList(p.session.Todos, inProgressIcon, t, p.width-SideBarWidth)
			} else if queueFocused && hasQueue && p.app.AgentCoordinator != nil {
				queueItems := p.app.AgentCoordinator.QueuedPromptsList(p.session.ID)
				expandedList = queueList(queueItems, t)
			}
		}

		var pillsArea string
		if len(pills) > 0 {
			pillsRow := lipgloss.JoinHorizontal(lipgloss.Top, pills...)

			// Add help hint for expanding/collapsing pills based on state.
			var helpDesc string
			if p.pillsExpanded {
				helpDesc = "close"
			} else {
				helpDesc = "open"
			}
			// Style to match help section: keys in FgMuted, description in FgSubtle
			helpKey := t.S().Base.Foreground(t.FgMuted).Render("ctrl+space")
			helpText := t.S().Base.Foreground(t.FgSubtle).Render(helpDesc)
			helpHint := lipgloss.JoinHorizontal(lipgloss.Center, helpKey, " ", helpText)
			pillsRow = lipgloss.JoinHorizontal(lipgloss.Center, pillsRow, " ", helpHint)

			if expandedList != "" {
				pillsArea = lipgloss.JoinVertical(
					lipgloss.Left,
					pillsRow,
					expandedList,
				)
			} else {
				pillsArea = pillsRow
			}

			style := t.S().Base.MarginTop(1).PaddingLeft(3)
			pillsArea = style.Render(pillsArea)
		}

		if p.compact {
			headerView := p.header.View()
			views := []string{headerView, messagesView}
			if pillsArea != "" {
				views = append(views, pillsArea)
			}
			views = append(views, editorView)
			chatView = lipgloss.JoinVertical(lipgloss.Left, views...)
		} else {
			sidebarView := p.sidebar.View()
			var messagesColumn string
			if pillsArea != "" {
				messagesColumn = lipgloss.JoinVertical(
					lipgloss.Left,
					messagesView,
					pillsArea,
				)
			} else {
				messagesColumn = messagesView
			}
			messages := lipgloss.JoinHorizontal(
				lipgloss.Left,
				messagesColumn,
				sidebarView,
			)
			chatView = lipgloss.JoinVertical(
				lipgloss.Left,
				messages,
				p.editor.View(),
			)
		}
	}

	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(chatView).X(0).Y(0),
	}

	if p.showingDetails {
		style := t.S().Base.
			Width(p.detailsWidth).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.BorderFocus)
		version := t.S().Base.Foreground(t.Border).Width(p.detailsWidth - 4).AlignHorizontal(lipgloss.Right).Render(version.Version)
		details := style.Render(
			lipgloss.JoinVertical(
				lipgloss.Left,
				p.sidebar.View(),
				version,
			),
		)
		layers = append(layers, lipgloss.NewLayer(details).X(1).Y(1))
	}

	// Show conflict dialog overlay when conflicts detected

	canvas := lipgloss.NewCompositor(layers...)
	return canvas.Render()
}

func (p *chatPage) updateCompactConfig(compact bool) tea.Cmd {
	return func() tea.Msg {
		config.Get().SetCompactMode(compact)
		return nil
	}
}

func (p *chatPage) setCompactMode(compact bool) {
	if p.compact == compact {
		return
	}
	p.compact = compact
	if compact {
		p.sidebar.SetCompactMode(true)
	} else {
		p.setShowDetails(false)
	}
}

func (p *chatPage) handleCompactMode(newWidth int, newHeight int) {
	if p.forceCompact {
		return
	}
	if (newWidth < CompactModeWidthBreakpoint || newHeight < CompactModeHeightBreakpoint) && !p.compact {
		p.setCompactMode(true)
	}
	if (newWidth >= CompactModeWidthBreakpoint && newHeight >= CompactModeHeightBreakpoint) && p.compact {
		p.setCompactMode(false)
	}
}

func (p *chatPage) SetSize(width, height int) tea.Cmd {
	p.handleCompactMode(width, height)
	p.width = width
	p.height = height
	var cmds []tea.Cmd

	if p.session.ID == "" {
		// Full screen during onboarding (no agents configured)
		if p.splashFullScreen {
			cmds = append(cmds, p.splash.SetSize(width, height))
		} else if p.compact {
			// Compact mode: splash + editor stacked
			cmds = append(cmds, p.splash.SetSize(width, height-EditorHeight))
			cmds = append(cmds, p.editor.SetSize(width, EditorHeight))
			cmds = append(cmds, p.editor.SetPosition(0, height-EditorHeight))
		} else {
			// Non-compact: splash in chat area, sidebar on right
			chatHeight := height - EditorHeight
			cmds = append(cmds, p.splash.SetSize(width-SideBarWidth, chatHeight))
			cmds = append(cmds, p.sidebar.SetSize(SideBarWidth, chatHeight))
			cmds = append(cmds, p.editor.SetSize(width, EditorHeight))
			cmds = append(cmds, p.editor.SetPosition(0, height-EditorHeight))
		}
	} else {
		hasIncompleteTodos := hasIncompleteTodos(p.session.Todos)
		hasQueue := p.promptQueue > 0
		hasPills := hasIncompleteTodos || hasQueue

		pillsAreaHeight := 0
		if hasPills {
			pillsAreaHeight = pillHeightWithBorder + 1 // +1 for padding top
			if p.pillsExpanded {
				if p.focusedPillSection == PillSectionTodos && hasIncompleteTodos {
					pillsAreaHeight += len(p.session.Todos)
				} else if p.focusedPillSection == PillSectionQueue && hasQueue {
					pillsAreaHeight += p.promptQueue
				}
			}
		}

		if p.compact {
			cmds = append(cmds, p.chat.SetSize(width, height-EditorHeight-HeaderHeight-pillsAreaHeight))
			p.detailsWidth = width - DetailsPositioning
			cmds = append(cmds, p.sidebar.SetSize(p.detailsWidth-LeftRightBorders, p.detailsHeight-TopBottomBorders))
			cmds = append(cmds, p.editor.SetSize(width, EditorHeight))
			cmds = append(cmds, p.header.SetWidth(width-BorderWidth))
		} else {
			cmds = append(cmds, p.chat.SetSize(width-SideBarWidth, height-EditorHeight-pillsAreaHeight))
			cmds = append(cmds, p.editor.SetSize(width, EditorHeight))
			cmds = append(cmds, p.sidebar.SetSize(SideBarWidth, height-EditorHeight))
		}
		cmds = append(cmds, p.editor.SetPosition(0, height-EditorHeight))
	}
	return tea.Batch(cmds...)
}

func (p *chatPage) newSession() tea.Cmd {
	if p.session.ID == "" {
		return nil
	}

	// Clear the current agent's session from the map
	if p.currentAgentID != "" {
		delete(p.agentSessions, p.currentAgentID)
	}

	p.session = session.Session{}
	p.focusedPane = PanelTypeEditor
	p.editor.Focus()
	p.chat.Blur()
	p.isCanceling = false
	return tea.Batch(
		util.CmdHandler(chat.SessionClearedMsg{}),
		util.ReportInfo("Session cleared - starting fresh"),
		p.SetSize(p.width, p.height),
	)
}

func (p *chatPage) setSession(sess session.Session) tea.Cmd {
	if p.session.ID == sess.ID {
		return nil
	}

	var cmds []tea.Cmd
	p.session = sess

	if p.hasInProgressTodo() {
		cmds = append(cmds, p.todoSpinner.Tick)
	}

	cmds = append(cmds, p.SetSize(p.width, p.height))
	cmds = append(cmds, p.chat.SetSession(sess))
	cmds = append(cmds, p.sidebar.SetSession(sess))
	cmds = append(cmds, p.header.SetSession(sess))
	cmds = append(cmds, p.editor.SetSession(sess))

	return tea.Sequence(cmds...)
}

func (p *chatPage) changeFocus() tea.Cmd {
	agents, _ := p.app.AgentCoordinator.ListAgents(context.Background())

	// No session (splash showing): toggle between editor and sidebar only
	if p.session.ID == "" {
		// In compact mode or no agents, can't tab anywhere
		if p.compact || len(agents) == 0 {
			return nil
		}

		// Non-compact with agents: toggle editor <-> sidebar
		switch p.focusedPane {
		case PanelTypeEditor, PanelTypeChat, PanelTypeSplash:
			p.focusedPane = PanelTypeSidebar
			p.sidebar.Focus()
			p.editor.Blur()
		case PanelTypeSidebar:
			p.focusedPane = PanelTypeEditor
			p.editor.Focus()
			p.sidebar.Blur()
		}
		return nil
	}

	// In compact mode or when no agents, just toggle between editor and chat
	if p.compact || len(agents) == 0 {
		switch p.focusedPane {
		case PanelTypeEditor:
			p.focusedPane = PanelTypeChat
			p.chat.Focus()
			p.editor.Blur()
			p.sidebar.Blur()
		case PanelTypeChat, PanelTypeSidebar:
			p.focusedPane = PanelTypeEditor
			p.editor.Focus()
			p.chat.Blur()
			p.sidebar.Blur()
		}
		return nil
	}

	// In non-compact mode with agents and session, cycle through all three panels
	switch p.focusedPane {
	case PanelTypeEditor:
		p.focusedPane = PanelTypeChat
		p.chat.Focus()
		p.editor.Blur()
		p.sidebar.Blur()
	case PanelTypeChat:
		p.focusedPane = PanelTypeSidebar
		p.sidebar.Focus()
		p.chat.Blur()
		p.editor.Blur()
	case PanelTypeSidebar:
		p.focusedPane = PanelTypeEditor
		p.editor.Focus()
		p.chat.Blur()
		p.sidebar.Blur()
	}
	return nil
}

func (p *chatPage) togglePillsExpanded() tea.Cmd {
	hasPills := hasIncompleteTodos(p.session.Todos) || p.promptQueue > 0
	if !hasPills {
		return nil
	}
	p.pillsExpanded = !p.pillsExpanded
	if p.pillsExpanded {
		if hasIncompleteTodos(p.session.Todos) {
			p.focusedPillSection = PillSectionTodos
		} else {
			p.focusedPillSection = PillSectionQueue
		}
	}
	return p.SetSize(p.width, p.height)
}

func (p *chatPage) switchPillSection(dir int) tea.Cmd {
	if !p.pillsExpanded {
		return nil
	}
	hasIncompleteTodos := hasIncompleteTodos(p.session.Todos)
	hasQueue := p.promptQueue > 0

	if dir < 0 && p.focusedPillSection == PillSectionQueue && hasIncompleteTodos {
		p.focusedPillSection = PillSectionTodos
		return p.SetSize(p.width, p.height)
	}
	if dir > 0 && p.focusedPillSection == PillSectionTodos && hasQueue {
		p.focusedPillSection = PillSectionQueue
		return p.SetSize(p.width, p.height)
	}
	return nil
}

func (p *chatPage) cancel() tea.Cmd {
	if p.isCanceling {
		p.isCanceling = false
		if p.app.AgentCoordinator != nil {
			p.app.AgentCoordinator.Cancel()
		}
		return nil
	}

	if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.QueuedPrompts() > 0 {
		p.app.AgentCoordinator.ClearQueue(p.session.ID)
		return nil
	}
	p.isCanceling = true
	return cancelTimerCmd()
}

func (p *chatPage) setShowDetails(show bool) {
	p.showingDetails = show
	p.header.SetDetailsOpen(p.showingDetails)
	if !p.compact {
		p.sidebar.SetCompactMode(false)
	}
}

func (p *chatPage) toggleDetails() {
	if p.session.ID == "" || !p.compact {
		return
	}
	p.setShowDetails(!p.showingDetails)
}

func (p *chatPage) sendMessage(text string, attachments []message.Attachment) tea.Cmd {
	// Debug: log current state
	if f, err := os.OpenFile("/tmp/loom-chatpage-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "[%s] sendMessage: currentAgentID='%s', session.ID='%s'\n", time.Now().Format("15:04:05"), p.currentAgentID, p.session.ID)
		f.Close()
	}

	session := p.session
	var cmds []tea.Cmd
	if p.session.ID == "" {
		// Debug: log before creating session
		if f, err := os.OpenFile("/tmp/loom-chatpage-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			// Check what agentID the SessionAdapter will use
			if sessAdapter, ok := p.app.Sessions.(*adapter.SessionAdapter); ok {
				fmt.Fprintf(f, "[%s] sendMessage: Creating session, SessionAdapter will use agentID from coordinator\n", time.Now().Format("15:04:05"))
				_ = sessAdapter // just to avoid unused variable warning
			}
			f.Close()
		}

		newSession, err := p.app.Sessions.Create(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		session = newSession

		// Get current agent ID from coordinator if not already set
		if p.currentAgentID == "" {
			if coord, ok := p.app.AgentCoordinator.(interface{ GetAgentID() string }); ok {
				p.currentAgentID = coord.GetAgentID()
				// Debug: log retrieval
				if f, err := os.OpenFile("/tmp/loom-chatpage-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
					fmt.Fprintf(f, "[%s] sendMessage: Retrieved currentAgentID from coordinator: '%s'\n", time.Now().Format("15:04:05"), p.currentAgentID)
					f.Close()
				}
			}
		}

		// Store this session for the current agent
		if p.currentAgentID != "" {
			p.agentSessions[p.currentAgentID] = session.ID
		}

		// Set session synchronously BEFORE starting agent to avoid race condition
		// where streaming messages arrive before SessionSelectedMsg is processed.
		// This ensures p.session and p.chat's session are set before any progress
		// events can arrive at the chat component's handleMessageEvent.
		cmd := p.setSession(session)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if p.app.AgentCoordinator == nil {
		return util.ReportError(fmt.Errorf("coder agent is not initialized"))
	}

	// Create user message event to display in chat
	userMsg := message.NewMessage(
		fmt.Sprintf("user-%d", time.Now().UnixNano()),
		session.ID,
		message.User,
	)
	userMsg.AddPart(message.ContentText{Text: text})
	cmds = append(cmds, util.CmdHandler(pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: userMsg,
	}))

	cmds = append(cmds, p.chat.GoToBottom())
	cmds = append(cmds, func() tea.Msg {
		// Convert attachments to interface{}
		var attch []interface{}
		for _, a := range attachments {
			attch = append(attch, a)
		}
		_, err := p.app.AgentCoordinator.Run(context.Background(), session.ID, text, attch...)
		if err != nil {
			isCancelErr := errors.Is(err, context.Canceled)
			isPermissionErr := errors.Is(err, permission.ErrorPermissionDenied)
			if isCancelErr || isPermissionErr {
				return nil
			}
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  err.Error(),
			}
		}
		return nil
	})
	return tea.Batch(cmds...)
}

func (p *chatPage) Bindings() []key.Binding {
	bindings := []key.Binding{
		p.keyMap.NewSession,
		p.keyMap.AddAttachment,
	}
	if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
		cancelBinding := p.keyMap.Cancel
		if p.isCanceling {
			cancelBinding = key.NewBinding(
				key.WithKeys("esc", "alt+esc"),
				key.WithHelp("esc", "press again to cancel"),
			)
		}
		bindings = append([]key.Binding{cancelBinding}, bindings...)
	}

	switch p.focusedPane {
	case PanelTypeChat:
		bindings = append([]key.Binding{
			key.NewBinding(
				key.WithKeys("tab"),
				key.WithHelp("tab", "focus editor"),
			),
		}, bindings...)
		bindings = append(bindings, p.chat.Bindings()...)
	case PanelTypeEditor:
		bindings = append([]key.Binding{
			key.NewBinding(
				key.WithKeys("tab"),
				key.WithHelp("tab", "focus chat"),
			),
		}, bindings...)
		bindings = append(bindings, p.editor.Bindings()...)
	case PanelTypeSplash:
		bindings = append(bindings, p.splash.Bindings()...)
	}

	return bindings
}

func (p *chatPage) Help() help.KeyMap {
	var shortList []key.Binding
	var fullList [][]key.Binding
	switch {
	case p.isOnboarding && p.splash.IsShowingClaudeAuthMethodChooser():
		shortList = append(shortList,
			// Choose auth method
			key.NewBinding(
				key.WithKeys("left", "right", "tab"),
				key.WithHelp("←→/tab", "choose"),
			),
			// Accept selection
			key.NewBinding(
				key.WithKeys("enter"),
				key.WithHelp("enter", "accept"),
			),
			// Go back
			key.NewBinding(
				key.WithKeys("esc", "alt+esc"),
				key.WithHelp("esc", "back"),
			),
			// Quit
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
		)
		// keep them the same
		for _, v := range shortList {
			fullList = append(fullList, []key.Binding{v})
		}
	case p.isOnboarding && p.splash.IsShowingClaudeOAuth2():
		if p.splash.IsClaudeOAuthURLState() {
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("enter"),
					key.WithHelp("enter", "open"),
				),
				key.NewBinding(
					key.WithKeys("c"),
					key.WithHelp("c", "copy url"),
				),
			)
		} else if p.splash.IsClaudeOAuthComplete() {
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("enter"),
					key.WithHelp("enter", "continue"),
				),
			)
		} else {
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("enter"),
					key.WithHelp("enter", "submit"),
				),
			)
		}
		shortList = append(shortList,
			// Quit
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
		)
		// keep them the same
		for _, v := range shortList {
			fullList = append(fullList, []key.Binding{v})
		}
	case p.isOnboarding && !p.splash.IsShowingAPIKey():
		shortList = append(shortList,
			// Choose model
			key.NewBinding(
				key.WithKeys("up", "down"),
				key.WithHelp("↑/↓", "choose"),
			),
			// Accept selection
			key.NewBinding(
				key.WithKeys("enter", "ctrl+y"),
				key.WithHelp("enter", "accept"),
			),
			// Quit
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
		)
		// keep them the same
		for _, v := range shortList {
			fullList = append(fullList, []key.Binding{v})
		}
	case p.isOnboarding && p.splash.IsShowingAPIKey():
		if p.splash.IsAPIKeyValid() {
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("enter"),
					key.WithHelp("enter", "continue"),
				),
			)
		} else {
			shortList = append(shortList,
				// Go back
				key.NewBinding(
					key.WithKeys("esc", "alt+esc"),
					key.WithHelp("esc", "back"),
				),
			)
		}
		shortList = append(shortList,
			// Quit
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
		)
		// keep them the same
		for _, v := range shortList {
			fullList = append(fullList, []key.Binding{v})
		}
	case p.isProjectInit:
		shortList = append(shortList,
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
		)
		// keep them the same
		for _, v := range shortList {
			fullList = append(fullList, []key.Binding{v})
		}
	default:
		if p.editor.IsCompletionsOpen() {
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("tab", "enter"),
					key.WithHelp("tab/enter", "complete"),
				),
				key.NewBinding(
					key.WithKeys("esc", "alt+esc"),
					key.WithHelp("esc", "cancel"),
				),
				key.NewBinding(
					key.WithKeys("up", "down"),
					key.WithHelp("↑/↓", "choose"),
				),
			)
			for _, v := range shortList {
				fullList = append(fullList, []key.Binding{v})
			}
			return core.NewSimpleHelp(shortList, fullList)
		}
		if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.IsBusy(p.currentAgentID) {
			cancelBinding := key.NewBinding(
				key.WithKeys("esc", "alt+esc"),
				key.WithHelp("esc", "cancel"),
			)
			if p.isCanceling {
				cancelBinding = key.NewBinding(
					key.WithKeys("esc", "alt+esc"),
					key.WithHelp("esc", "press again to cancel"),
				)
			}
			if p.app.AgentCoordinator != nil && p.app.AgentCoordinator.QueuedPrompts() > 0 {
				cancelBinding = key.NewBinding(
					key.WithKeys("esc", "alt+esc"),
					key.WithHelp("esc", "clear queue"),
				)
			}
			shortList = append(shortList, cancelBinding)
			fullList = append(fullList,
				[]key.Binding{
					cancelBinding,
				},
			)
		}
		globalBindings := []key.Binding{}
		// we are in a session
		if p.session.ID != "" {
			var tabKey key.Binding
			switch p.focusedPane {
			case PanelTypeEditor:
				tabKey = key.NewBinding(
					key.WithKeys("tab"),
					key.WithHelp("tab", "focus chat"),
				)
			case PanelTypeChat:
				tabKey = key.NewBinding(
					key.WithKeys("tab"),
					key.WithHelp("tab", "focus editor"),
				)
			default:
				tabKey = key.NewBinding(
					key.WithKeys("tab"),
					key.WithHelp("tab", "focus chat"),
				)
			}
			shortList = append(shortList, tabKey)
			globalBindings = append(globalBindings, tabKey)

			// Show left/right to switch sections when expanded and both exist
			hasTodos := hasIncompleteTodos(p.session.Todos)
			hasQueue := p.promptQueue > 0
			if p.pillsExpanded && hasTodos && hasQueue {
				shortList = append(shortList, p.keyMap.PillLeft)
				globalBindings = append(globalBindings, p.keyMap.PillLeft)
			}
		}
		commandsBinding := key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "commands"),
		)
		if p.focusedPane == PanelTypeEditor && p.editor.IsEmpty() {
			commandsBinding.SetHelp("/ or ctrl+p", "commands")
		}
		helpBinding := key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "more"),
		)
		globalBindings = append(globalBindings, commandsBinding)
		globalBindings = append(globalBindings,
			key.NewBinding(
				key.WithKeys("ctrl+s"),
				key.WithHelp("ctrl+s", "sessions"),
			),
		)
		if p.session.ID != "" {
			globalBindings = append(globalBindings,
				key.NewBinding(
					key.WithKeys("ctrl+n"),
					key.WithHelp("ctrl+n", "new sessions"),
				))
		}
		shortList = append(shortList,
			// Commands
			commandsBinding,
		)
		fullList = append(fullList, globalBindings)

		switch p.focusedPane {
		case PanelTypeChat:
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("up", "down"),
					key.WithHelp("↑↓", "scroll"),
				),
				messages.CopyKey,
			)
			fullList = append(fullList,
				[]key.Binding{
					key.NewBinding(
						key.WithKeys("up", "down"),
						key.WithHelp("↑↓", "scroll"),
					),
					key.NewBinding(
						key.WithKeys("shift+up", "shift+down"),
						key.WithHelp("shift+↑↓", "next/prev item"),
					),
					key.NewBinding(
						key.WithKeys("pgup", "b"),
						key.WithHelp("b/pgup", "page up"),
					),
					key.NewBinding(
						key.WithKeys("pgdown", " ", "f"),
						key.WithHelp("f/pgdn", "page down"),
					),
				},
				[]key.Binding{
					key.NewBinding(
						key.WithKeys("u"),
						key.WithHelp("u", "half page up"),
					),
					key.NewBinding(
						key.WithKeys("d"),
						key.WithHelp("d", "half page down"),
					),
					key.NewBinding(
						key.WithKeys("g", "home"),
						key.WithHelp("g", "home"),
					),
					key.NewBinding(
						key.WithKeys("G", "end"),
						key.WithHelp("G", "end"),
					),
				},
				[]key.Binding{
					messages.CopyKey,
					messages.ClearSelectionKey,
				},
			)
		case PanelTypeEditor:
			newLineBinding := key.NewBinding(
				key.WithKeys("shift+enter", "ctrl+j"),
				// "ctrl+j" is a common keybinding for newline in many editors. If
				// the terminal supports "shift+enter", we substitute the help text
				// to reflect that.
				key.WithHelp("ctrl+j", "newline"),
			)
			if p.keyboardEnhancements.Flags > 0 {
				// Non-zero flags mean we have at least key disambiguation.
				newLineBinding.SetHelp("shift+enter", newLineBinding.Help().Desc)
			}
			shortList = append(shortList, newLineBinding)
			fullList = append(fullList,
				[]key.Binding{
					newLineBinding,
					key.NewBinding(
						key.WithKeys("ctrl+f"),
						key.WithHelp("ctrl+f", "add image"),
					),
					key.NewBinding(
						key.WithKeys("@"),
						key.WithHelp("@", "mention file"),
					),
					key.NewBinding(
						key.WithKeys("ctrl+o"),
						key.WithHelp("ctrl+o", "open editor"),
					),
				})

			if p.editor.HasAttachments() {
				fullList = append(fullList, []key.Binding{
					key.NewBinding(
						key.WithKeys("ctrl+r"),
						key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
					),
					key.NewBinding(
						key.WithKeys("ctrl+r", "r"),
						key.WithHelp("ctrl+r+r", "delete all attachments"),
					),
					key.NewBinding(
						key.WithKeys("esc", "alt+esc"),
						key.WithHelp("esc", "cancel delete mode"),
					),
				})
			}
		case PanelTypeSidebar:
			shortList = append(shortList,
				key.NewBinding(
					key.WithKeys("up", "down"),
					key.WithHelp("↑/↓", "navigate"),
				),
				key.NewBinding(
					key.WithKeys("enter"),
					key.WithHelp("enter", "select/expand"),
				),
			)
			fullList = append(fullList,
				[]key.Binding{
					key.NewBinding(
						key.WithKeys("up", "down"),
						key.WithHelp("↑/↓", "navigate"),
					),
					key.NewBinding(
						key.WithKeys("enter"),
						key.WithHelp("enter", "select/expand"),
					),
				},
				[]key.Binding{
					key.NewBinding(
						key.WithKeys("a"),
						key.WithHelp("a", "add MCP server"),
					),
				},
			)
		}
		shortList = append(shortList,
			// Quit
			key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("ctrl+c", "quit"),
			),
			// Help
			helpBinding,
		)
		fullList = append(fullList, []key.Binding{
			key.NewBinding(
				key.WithKeys("ctrl+g"),
				key.WithHelp("ctrl+g", "less"),
			),
		})
	}

	return core.NewSimpleHelp(shortList, fullList)
}

func (p *chatPage) IsChatFocused() bool {
	return p.focusedPane == PanelTypeChat
}

// fetchAgentsList fetches the list of available agents from the server
func (p *chatPage) fetchAgentsList() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		agentInfos, err := p.app.AgentCoordinator.ListAgents(ctx)
		if err != nil {
			// Return empty list on error
			return sidebar.AgentsListMsg{Agents: []sidebar.AgentInfo{}}
		}

		// Convert internal agent info to sidebar agent info
		agents := make([]sidebar.AgentInfo, len(agentInfos))
		for i, info := range agentInfos {
			agents[i] = sidebar.AgentInfo{
				ID:     info.ID,
				Name:   info.Name,
				Status: info.Status,
			}
		}

		return sidebar.AgentsListMsg{
			Agents:       agents,
			CurrentAgent: p.currentAgentID,
		}
	}
}

// fetchMCPServers fetches the list of MCP servers from the gRPC server
func (p *chatPage) fetchMCPServers() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := p.app.Client().ListMCPServers(ctx, &loomv1.ListMCPServersRequest{})
		if err != nil {
			// Return empty list on error
			return sidebar.MCPServersListMsg{Servers: []sidebar.MCPServerInfo{}}
		}

		// Convert proto MCP server info to sidebar MCP server info
		// Filter out test servers (those with names starting with "__test__")
		servers := make([]sidebar.MCPServerInfo, 0, len(resp.Servers))
		for _, s := range resp.Servers {
			// Skip test servers - they're temporary and used for connection testing
			if strings.HasPrefix(s.Name, "__test__") {
				continue
			}
			servers = append(servers, sidebar.MCPServerInfo{
				Name:      s.Name,
				Enabled:   s.Enabled,
				Connected: s.Connected,
				Transport: s.Transport,
				Status:    s.Status,
				ToolCount: s.ToolCount,
				Error:     s.Error,
			})
		}

		// Sort servers by name to prevent navigation glitches
		sort.Slice(servers, func(i, j int) bool {
			return servers[i].Name < servers[j].Name
		})

		return sidebar.MCPServersListMsg{Servers: servers}
	}
}

// handleAddMCPServer adds a new MCP server and refreshes the list
func (p *chatPage) handleAddMCPServer(req *loomv1.AddMCPServerRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := p.app.Client().AddMCPServer(ctx, req)
		if err != nil {
			return util.InfoMsg{
				Text: fmt.Sprintf("Failed to add MCP server: %v", err),
				Type: util.InfoTypeError,
			}
		}

		if !resp.Success {
			return util.InfoMsg{
				Text: fmt.Sprintf("Failed to add server: %s", resp.Message),
				Type: util.InfoTypeWarn,
			}
		}

		// Return success message - the Update handler will batch the info display and fetch
		return AddMCPServerSuccessMsg{
			ServerName: req.Name,
		}
	}
}

// MCPServerToolsMsg contains tools for a specific MCP server
type MCPServerToolsMsg struct {
	ServerName string
	Tools      []sidebar.MCPToolInfo
}

// fetchMCPServerTools fetches the tools for a specific MCP server
func (p *chatPage) fetchMCPServerTools(serverName string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Query MCP server directly through the manager (not agent's tool registry)
		tools, err := p.app.Client().ListMCPServerTools(ctx, serverName)
		if err != nil {
			// Return empty tools on error
			return MCPServerToolsMsg{ServerName: serverName, Tools: []sidebar.MCPToolInfo{}}
		}

		// Convert to sidebar tool info
		sidebarTools := make([]sidebar.MCPToolInfo, len(tools))
		for i, tool := range tools {
			sidebarTools[i] = sidebar.MCPToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchemaJson,
			}
		}

		// Sort tools alphabetically
		sort.Slice(sidebarTools, func(i, j int) bool {
			return sidebarTools[i].Name < sidebarTools[j].Name
		})

		return MCPServerToolsMsg{
			ServerName: serverName,
			Tools:      sidebarTools,
		}
	}
}

// isMouseOverChat checks if the given mouse coordinates are within the chat area bounds.
// Returns true if the mouse is over the chat area, false otherwise.
func (p *chatPage) isMouseOverChat(x, y int) bool {
	// No session means no chat area
	if p.session.ID == "" {
		return false
	}

	var chatX, chatY, chatWidth, chatHeight int

	if p.compact {
		// In compact mode: chat area starts after header and spans full width
		chatX = 0
		chatY = HeaderHeight
		chatWidth = p.width
		chatHeight = p.height - EditorHeight - HeaderHeight
	} else {
		// In non-compact mode: chat area spans from left edge to sidebar
		chatX = 0
		chatY = 0
		chatWidth = p.width - SideBarWidth
		chatHeight = p.height - EditorHeight
	}

	// Check if mouse coordinates are within chat bounds
	return x >= chatX && x < chatX+chatWidth && y >= chatY && y < chatY+chatHeight
}

func (p *chatPage) isMouseOverSidebar(x, y int) bool {
	// Sidebar only exists in non-compact mode
	if p.compact {
		return false
	}

	// In non-compact mode: sidebar is on the right side
	sidebarX := p.width - SideBarWidth
	sidebarY := 0
	sidebarWidth := SideBarWidth
	sidebarHeight := p.height - EditorHeight

	// Check if mouse coordinates are within sidebar bounds
	return x >= sidebarX && x < sidebarX+sidebarWidth && y >= sidebarY && y < sidebarY+sidebarHeight
}

func (p *chatPage) hasInProgressTodo() bool {
	for _, todo := range p.session.Todos {
		if todo.Status == session.TodoStatusInProgress {
			return true
		}
	}
	return false
}
