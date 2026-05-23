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
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const (
	SlashHelpDialogID dialogs.DialogID = "slash-help"
	slashHelpWidth    int              = 58
)

// SlashHelpDialog shows all available slash commands with descriptions and shortcuts.
type SlashHelpDialog interface {
	dialogs.DialogModel
}

type slashHelpEntry struct {
	cmd      string // slash command(s)
	desc     string // human description
	shortcut string // optional keyboard shortcut
}

var slashHelpEntries = []slashHelpEntry{
	{"/clear, /new, /reset", "clear current conversation", ""},
	{"/quit, /exit", "exit Loom", "ctrl+c"},
	{"/sessions", "switch session", "ctrl+o"},
	{"/model", "switch LLM model/provider", "ctrl+l"},
	{"/agents", "open agents dialog", "ctrl+e"},
	{"/workflows", "open workflows dialog", "ctrl+w"},
	{"/agent-plan", "guided agent planning (weaver)", ""},
	{"/sidebar", "toggle sidebar", ""},
	{"/apps", "browse UI apps", ""},
	{"/mcp", "add MCP server", ""},
	{"/patterns", "browse pattern library", ""},
	{"/help", "show this help", ""},
}

type slashHelpDialogCmp struct {
	wWidth, wHeight int
	closeKey        key.Binding
}

// NewSlashHelpDialog creates a dialog listing all slash commands.
func NewSlashHelpDialog() SlashHelpDialog {
	return &slashHelpDialogCmp{
		closeKey: key.NewBinding(
			key.WithKeys("esc", "q", "enter", "?"),
			key.WithHelp("esc/q/enter", "close"),
		),
	}
}

func (d *slashHelpDialogCmp) Init() tea.Cmd { return nil }

func (d *slashHelpDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
	case tea.KeyPressMsg:
		if key.Matches(msg, d.closeKey) {
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
	}
	return d, nil
}

func (d *slashHelpDialogCmp) View() string {
	t := styles.CurrentTheme()

	const cmdCol = 22  // width of left (command) column
	const shortCol = 9 // width of right (shortcut) column

	header := t.S().Base.Padding(0, 1, 1, 1).
		Render(core.Title("Slash Commands", slashHelpWidth-4))

	sep := t.S().Base.Foreground(t.FgSubtle).PaddingLeft(1).
		Render(strings.Repeat("─", slashHelpWidth-4))

	var rows []string
	for _, e := range slashHelpEntries {
		cmdStyled := t.S().Base.Foreground(t.Primary).Width(cmdCol).Render(e.cmd)
		descStyled := t.S().Base.Foreground(t.FgBase).Render(e.desc)

		var shortStyled string
		if e.shortcut != "" {
			shortStyled = t.S().Base.Foreground(t.FgMuted).
				Width(shortCol).Align(lipgloss.Right).Render(e.shortcut)
		} else {
			shortStyled = t.S().Base.Width(shortCol).Render("")
		}

		row := lipgloss.JoinHorizontal(lipgloss.Top, cmdStyled, descStyled, shortStyled)
		rows = append(rows, t.S().Base.PaddingLeft(1).Render(row))
	}

	closeHint := t.S().Base.Foreground(t.FgSubtle).PaddingLeft(1).
		Render("esc/q/enter  close")

	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		sep,
		"",
		lipgloss.JoinVertical(lipgloss.Left, rows...),
		"",
		sep,
		closeHint,
	)

	dialogStyle := t.S().Base.
		Width(slashHelpWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)

	return dialogStyle.Render(content)
}

func (d *slashHelpDialogCmp) ID() dialogs.DialogID { return SlashHelpDialogID }

func (d *slashHelpDialogCmp) Position() (int, int) {
	row := d.wHeight/2 - 10
	if row < 1 {
		row = 1
	}
	col := d.wWidth/2 - slashHelpWidth/2
	return row, col
}
