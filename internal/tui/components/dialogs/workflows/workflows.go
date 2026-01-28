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
package workflows

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
	WorkflowsDialogID dialogs.DialogID = "workflows"

	defaultWidth int = 70
)

type listModel = list.FilterableList[list.CompletionItem[WorkflowInfo]]

// WorkflowInfo represents a workflow in the dialog.
type WorkflowInfo struct {
	CoordinatorID   string
	CoordinatorName string
	SubAgentCount   int
}

// WorkflowsDialog represents the workflow selection dialog.
type WorkflowsDialog interface {
	dialogs.DialogModel
}

type workflowsDialogCmp struct {
	width   int
	wWidth  int // Width of the terminal window
	wHeight int // Height of the terminal window

	workflowList listModel
	keyMap       WorkflowsDialogKeyMap
	help         help.Model
	workflows    []WorkflowInfo
}

func NewWorkflowsDialog(workflows []WorkflowInfo) WorkflowsDialog {
	keyMap := DefaultWorkflowsDialogKeyMap()
	listKeyMap := list.DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	t := styles.CurrentTheme()
	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	workflowList := list.NewFilterableList(
		[]list.CompletionItem[WorkflowInfo]{},
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
			list.WithResizeByList(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	return &workflowsDialogCmp{
		workflowList: workflowList,
		width:        defaultWidth,
		keyMap:       keyMap,
		help:         help,
		workflows:    workflows,
	}
}

func (d *workflowsDialogCmp) Init() tea.Cmd {
	workflowItems := []list.CompletionItem[WorkflowInfo]{}
	for _, workflow := range d.workflows {
		displayName := fmt.Sprintf("%s (%d agents)", workflow.CoordinatorName, workflow.SubAgentCount)
		workflowItems = append(workflowItems, list.NewCompletionItem(
			displayName,
			workflow,
			list.WithCompletionID(workflow.CoordinatorID),
		))
	}
	return d.workflowList.SetItems(workflowItems)
}

func (d *workflowsDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
		return d, d.workflowList.SetSize(d.listWidth(), d.listHeight())
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Select):
			selectedItem := d.workflowList.SelectedItem()
			if selectedItem == nil {
				return d, nil // No item selected, do nothing
			}
			workflow := (*selectedItem).Value()
			return d, tea.Sequence(
				util.CmdHandler(dialogs.CloseDialogMsg{}),
				util.CmdHandler(sidebar.AgentSelectedMsg{
					AgentID: workflow.CoordinatorID,
				}),
			)
		case key.Matches(msg, d.keyMap.Close):
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := d.workflowList.Update(msg)
			d.workflowList = u.(listModel)
			return d, cmd
		}
	}
	return d, nil
}

func (d *workflowsDialogCmp) View() string {
	t := styles.CurrentTheme()
	listView := d.workflowList

	header := t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Select Workflow", d.width-4))
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		listView.View(),
		"",
		t.S().Base.Width(d.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(d.help.View(d.keyMap)),
	)
	return d.style().Render(content)
}

func (d *workflowsDialogCmp) Cursor() *tea.Cursor {
	if cursor, ok := d.workflowList.(util.Cursor); ok {
		cursor := cursor.Cursor()
		if cursor != nil {
			cursor = d.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (d *workflowsDialogCmp) listWidth() int {
	return defaultWidth - 2
}

func (d *workflowsDialogCmp) listHeight() int {
	listHeight := len(d.workflowList.Items()) + 2 + 4 // height based on items + 2 for input + 4 for sections
	return min(listHeight, d.wHeight/2)
}

func (d *workflowsDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := d.Position()
	offset := row + 3
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

func (d *workflowsDialogCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(d.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (d *workflowsDialogCmp) Position() (int, int) {
	row := d.wHeight/4 - 2 // just a bit above the center
	col := d.wWidth / 2
	col -= d.width / 2
	return row, col
}

func (d *workflowsDialogCmp) ID() dialogs.DialogID {
	return WorkflowsDialogID
}
