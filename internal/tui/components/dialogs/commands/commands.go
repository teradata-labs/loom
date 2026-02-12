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
package commands

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/csync"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/internal/uicmd"
)

const (
	CommandsDialogID dialogs.DialogID = "commands"

	defaultWidth int = 70
)

type commandType = uicmd.CommandType

const (
	SystemCommands = uicmd.SystemCommands
	UserCommands   = uicmd.UserCommands
	MCPPrompts     = uicmd.MCPPrompts
)

type listModel = list.FilterableList[list.CompletionItem[Command]]

// Command represents a command that can be executed
type (
	Command                         = uicmd.Command
	CommandRunCustomMsg             = uicmd.CommandRunCustomMsg
	ShowMCPPromptArgumentsDialogMsg = uicmd.ShowMCPPromptArgumentsDialogMsg
)

// CommandsDialog represents the commands dialog.
type CommandsDialog interface {
	dialogs.DialogModel
}

type commandDialogCmp struct {
	width   int
	wWidth  int // Width of the terminal window
	wHeight int // Height of the terminal window

	commandList  listModel
	keyMap       CommandsDialogKeyMap
	help         help.Model
	selected     commandType           // Selected SystemCommands, UserCommands, or MCPPrompts
	userCommands []Command             // User-defined commands
	mcpPrompts   *csync.Slice[Command] // MCP prompts
	sessionID    string                // Current session ID
}

type (
	SwitchSessionsMsg     struct{}
	NewSessionsMsg        struct{}
	SwitchModelMsg        struct{}
	QuitMsg               struct{}
	OpenFilePickerMsg     struct{}
	ToggleHelpMsg         struct{}
	ToggleCompactModeMsg  struct{}
	OpenExternalEditorMsg struct{}
	ToggleYoloModeMsg     struct{}
	OpenBrowseAppsMsg     struct{}
)

func NewCommandDialog(sessionID string) CommandsDialog {
	keyMap := DefaultCommandsDialogKeyMap()
	listKeyMap := list.DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	t := styles.CurrentTheme()
	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	commandList := list.NewFilterableList(
		[]list.CompletionItem[Command]{},
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
			list.WithResizeByList(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	return &commandDialogCmp{
		commandList: commandList,
		width:       defaultWidth,
		keyMap:      DefaultCommandsDialogKeyMap(),
		help:        help,
		selected:    SystemCommands,
		sessionID:   sessionID,
		mcpPrompts:  csync.NewSlice[Command](),
	}
}

func (c *commandDialogCmp) Init() tea.Cmd {
	commands, err := uicmd.LoadCustomCommands()
	if err != nil {
		return util.ReportError(err)
	}
	c.userCommands = commands
	c.mcpPrompts.SetSlice(uicmd.LoadMCPPrompts())
	return c.setCommandType(c.selected)
}

func (c *commandDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.wWidth = msg.Width
		c.wHeight = msg.Height
		return c, tea.Batch(
			c.setCommandType(c.selected),
			c.commandList.SetSize(c.listWidth(), c.listHeight()),
		)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, c.keyMap.Select):
			selectedItem := c.commandList.SelectedItem()
			if selectedItem == nil {
				return c, nil // No item selected, do nothing
			}
			command := (*selectedItem).Value()
			return c, tea.Sequence(
				util.CmdHandler(dialogs.CloseDialogMsg{}),
				command.Handler(command),
			)
		case key.Matches(msg, c.keyMap.Tab):
			if len(c.userCommands) == 0 && c.mcpPrompts.Len() == 0 {
				return c, nil
			}
			return c, c.setCommandType(c.next())
		case key.Matches(msg, c.keyMap.Close):
			return c, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := c.commandList.Update(msg)
			c.commandList = u.(listModel)
			return c, cmd
		}
	}
	return c, nil
}

func (c *commandDialogCmp) next() commandType {
	switch c.selected {
	case SystemCommands:
		if len(c.userCommands) > 0 {
			return UserCommands
		}
		if c.mcpPrompts.Len() > 0 {
			return MCPPrompts
		}
		fallthrough
	case UserCommands:
		if c.mcpPrompts.Len() > 0 {
			return MCPPrompts
		}
		fallthrough
	case MCPPrompts:
		return SystemCommands
	default:
		return SystemCommands
	}
}

func (c *commandDialogCmp) View() string {
	t := styles.CurrentTheme()
	listView := c.commandList
	radio := c.commandTypeRadio()

	header := t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Commands", c.width-lipgloss.Width(radio)-5) + " " + radio)
	if len(c.userCommands) == 0 && c.mcpPrompts.Len() == 0 {
		header = t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Commands", c.width-4))
	}
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		listView.View(),
		"",
		t.S().Base.Width(c.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(c.help.View(c.keyMap)),
	)
	return c.style().Render(content)
}

func (c *commandDialogCmp) Cursor() *tea.Cursor {
	if cursor, ok := c.commandList.(util.Cursor); ok {
		cursor := cursor.Cursor()
		if cursor != nil {
			cursor = c.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (c *commandDialogCmp) commandTypeRadio() string {
	t := styles.CurrentTheme()

	fn := func(i commandType) string {
		if i == c.selected {
			return "◉ " + i.String()
		}
		return "○ " + i.String()
	}

	parts := []string{
		fn(SystemCommands),
	}
	if len(c.userCommands) > 0 {
		parts = append(parts, fn(UserCommands))
	}
	if c.mcpPrompts.Len() > 0 {
		parts = append(parts, fn(MCPPrompts))
	}
	return t.S().Base.Foreground(t.FgHalfMuted).Render(strings.Join(parts, " "))
}

func (c *commandDialogCmp) listWidth() int {
	return defaultWidth - 2 // 4 for padding
}

func (c *commandDialogCmp) setCommandType(commandType commandType) tea.Cmd {
	c.selected = commandType

	var commands []Command
	switch c.selected {
	case SystemCommands:
		commands = c.defaultCommands()
	case UserCommands:
		commands = c.userCommands
	case MCPPrompts:
		commands = slices.Collect(c.mcpPrompts.Seq())
	}

	commandItems := []list.CompletionItem[Command]{}
	for _, cmd := range commands {
		opts := []list.CompletionItemOption{
			list.WithCompletionID(cmd.ID),
		}
		if cmd.Shortcut != "" {
			opts = append(
				opts,
				list.WithCompletionShortcut(cmd.Shortcut),
			)
		}
		commandItems = append(commandItems, list.NewCompletionItem(cmd.Title, cmd, opts...))
	}
	return c.commandList.SetItems(commandItems)
}

func (c *commandDialogCmp) listHeight() int {
	listHeigh := len(c.commandList.Items()) + 2 + 4 // height based on items + 2 for the input + 4 for the sections
	return min(listHeigh, c.wHeight/2)
}

func (c *commandDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := c.Position()
	offset := row + 3
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

func (c *commandDialogCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(c.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (c *commandDialogCmp) Position() (int, int) {
	row := c.wHeight/4 - 2 // just a bit above the center
	col := c.wWidth / 2
	col -= c.width / 2
	return row, col
}

func (c *commandDialogCmp) defaultCommands() []Command {
	commands := []Command{
		{
			ID:          "new_session",
			Title:       "New Session",
			Description: "start a new session",
			Shortcut:    "ctrl+n",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(NewSessionsMsg{})
			},
		},
		{
			ID:          "switch_session",
			Title:       "Switch Session",
			Description: "Switch to a different session",
			Shortcut:    "ctrl+o",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(SwitchSessionsMsg{})
			},
		},
		{
			ID:          "switch_model",
			Title:       "Switch Model",
			Description: "Switch to a different model",
			Shortcut:    "ctrl+l",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(SwitchModelMsg{})
			},
		},
	}

	// Only show toggle compact mode command if window width is larger than compact breakpoint (90)
	if c.wWidth > 120 && c.sessionID != "" {
		commands = append(commands, Command{
			ID:          "toggle_sidebar",
			Title:       "Toggle Sidebar",
			Description: "Toggle between compact and normal layout",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(ToggleCompactModeMsg{})
			},
		})
	}

	commands = append(commands, Command{
		ID:          "browse_apps",
		Title:       "Browse Apps",
		Description: "Open MCP apps in browser",
		Handler: func(cmd Command) tea.Cmd {
			return util.CmdHandler(OpenBrowseAppsMsg{})
		},
	})

	return append(commands, []Command{
		{
			ID:          "toggle_yolo",
			Title:       "Toggle Yolo Mode",
			Description: "Toggle yolo mode",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(ToggleYoloModeMsg{})
			},
		},
		{
			ID:          "toggle_help",
			Title:       "Toggle Help",
			Shortcut:    "ctrl+g",
			Description: "Toggle help",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(ToggleHelpMsg{})
			},
		},
		{
			ID:          "quit",
			Title:       "Quit",
			Description: "Quit",
			Shortcut:    "ctrl+c",
			Handler: func(cmd Command) tea.Cmd {
				return util.CmdHandler(QuitMsg{})
			},
		},
	}...)
}

func (c *commandDialogCmp) ID() dialogs.DialogID {
	return CommandsDialogID
}
