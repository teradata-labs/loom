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
package agents

import (
	"fmt"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/tui/components/chat/sidebar"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const (
	AgentsDialogID dialogs.DialogID = "agents"

	defaultWidth int = 70
)

type listModel = list.FilterableList[list.CompletionItem[AgentInfo]]

// AgentInfo represents an agent in the dialog.
type AgentInfo struct {
	ID           string
	Name         string
	Status       string
	ModelInfo    string // Primary model (e.g., "anthropic/claude-sonnet-4")
	RoleLLMCount int    // Number of role-specific LLM overrides (0-4)
}

// AgentsDialog represents the agent selection dialog.
type AgentsDialog interface {
	dialogs.DialogModel
}

type agentsDialogCmp struct {
	width   int
	wWidth  int // Width of the terminal window
	wHeight int // Height of the terminal window

	agentList listModel
	keyMap    AgentsDialogKeyMap
	help      help.Model
	agents    []AgentInfo
}

func NewAgentsDialog(agents []AgentInfo) AgentsDialog {
	keyMap := DefaultAgentsDialogKeyMap()
	listKeyMap := list.DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	t := styles.CurrentTheme()
	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	agentList := list.NewFilterableList(
		[]list.CompletionItem[AgentInfo]{},
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
			list.WithResizeByList(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	return &agentsDialogCmp{
		agentList: agentList,
		width:     defaultWidth,
		keyMap:    keyMap,
		help:      help,
		agents:    agents,
	}
}

func (d *agentsDialogCmp) Init() tea.Cmd {
	agentItems := []list.CompletionItem[AgentInfo]{}
	for _, ag := range d.agents {
		// Build display label with model info
		label := ag.Name
		if ag.ModelInfo != "" {
			label += "  " + ag.ModelInfo
			if ag.RoleLLMCount > 0 {
				label += fmt.Sprintf(" (+%d roles)", ag.RoleLLMCount)
			}
		}
		agentItems = append(agentItems, list.NewCompletionItem(
			label,
			ag,
			list.WithCompletionID(ag.ID),
		))
	}
	return d.agentList.SetItems(agentItems)
}

func (d *agentsDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
		return d, d.agentList.SetSize(d.listWidth(), d.listHeight())
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Select):
			selectedItem := d.agentList.SelectedItem()
			if selectedItem == nil {
				return d, nil // No item selected, do nothing
			}
			agent := (*selectedItem).Value()
			return d, tea.Sequence(
				util.CmdHandler(dialogs.CloseDialogMsg{}),
				util.CmdHandler(sidebar.AgentSelectedMsg{
					AgentID: agent.ID,
				}),
			)
		case key.Matches(msg, d.keyMap.Close):
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := d.agentList.Update(msg)
			d.agentList = u.(listModel)
			return d, cmd
		}
	}
	return d, nil
}

func (d *agentsDialogCmp) View() string {
	t := styles.CurrentTheme()
	listView := d.agentList

	header := t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Select Agent", d.width-4))
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		listView.View(),
		"",
		t.S().Base.Width(d.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(d.help.View(d.keyMap)),
	)
	return d.style().Render(content)
}

func (d *agentsDialogCmp) Cursor() *tea.Cursor {
	if cursor, ok := d.agentList.(util.Cursor); ok {
		cursor := cursor.Cursor()
		if cursor != nil {
			cursor = d.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (d *agentsDialogCmp) listWidth() int {
	return defaultWidth - 2
}

func (d *agentsDialogCmp) listHeight() int {
	listHeight := len(d.agentList.Items()) + 2 + 4 // height based on items + 2 for input + 4 for sections
	return min(listHeight, d.wHeight/2)
}

func (d *agentsDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := d.Position()
	offset := row + 3
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

func (d *agentsDialogCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(d.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (d *agentsDialogCmp) Position() (int, int) {
	row := d.wHeight/4 - 2 // just a bit above the center
	col := d.wWidth / 2
	col -= d.width / 2
	return row, col
}

func (d *agentsDialogCmp) ID() dialogs.DialogID {
	return AgentsDialogID
}
