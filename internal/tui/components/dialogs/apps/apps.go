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
package apps

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/internal/uiutil"
)

const (
	AppsDialogID dialogs.DialogID = "apps"

	defaultWidth int = 70
)

type listModel = list.FilterableList[list.CompletionItem[AppItem]]

// AppItem represents an app in the dialog.
type AppItem struct {
	Name        string
	DisplayName string
	Description string
	URL         string // Full HTTP URL to open in browser
}

// AppOpenedMsg is sent when an app is opened in the browser.
type AppOpenedMsg struct {
	Name string
	URL  string
}

// AppsDialog represents the app selection dialog.
type AppsDialog interface {
	dialogs.DialogModel
}

type appsDialogCmp struct {
	width   int
	wWidth  int // Width of the terminal window
	wHeight int // Height of the terminal window

	appList listModel
	keyMap  AppsDialogKeyMap
	help    help.Model
	apps    []AppItem
}

// NewAppsDialog creates a new apps dialog with the given list of apps.
func NewAppsDialog(apps []AppItem) AppsDialog {
	keyMap := DefaultAppsDialogKeyMap()
	listKeyMap := list.DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	t := styles.CurrentTheme()
	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	appList := list.NewFilterableList(
		[]list.CompletionItem[AppItem]{},
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
			list.WithResizeByList(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	return &appsDialogCmp{
		appList: appList,
		width:   defaultWidth,
		keyMap:  keyMap,
		help:    help,
		apps:    apps,
	}
}

func (d *appsDialogCmp) Init() tea.Cmd {
	appItems := []list.CompletionItem[AppItem]{}
	for _, app := range d.apps {
		appItems = append(appItems, list.NewCompletionItem(
			app.DisplayName,
			app,
			list.WithCompletionID(app.Name),
		))
	}
	return d.appList.SetItems(appItems)
}

func (d *appsDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
		return d, d.appList.SetSize(d.listWidth(), d.listHeight())
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Select):
			selectedItem := d.appList.SelectedItem()
			if selectedItem == nil {
				return d, nil // No item selected, do nothing
			}
			app := (*selectedItem).Value()
			// Open in browser
			_ = uiutil.OpenURL(app.URL)
			return d, tea.Sequence(
				util.CmdHandler(dialogs.CloseDialogMsg{}),
				util.CmdHandler(AppOpenedMsg{Name: app.Name, URL: app.URL}),
			)
		case key.Matches(msg, d.keyMap.Close):
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := d.appList.Update(msg)
			d.appList = u.(listModel)
			return d, cmd
		}
	}
	return d, nil
}

func (d *appsDialogCmp) View() string {
	t := styles.CurrentTheme()
	listView := d.appList

	header := t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Browse Apps", d.width-4))
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		listView.View(),
		"",
		t.S().Base.Width(d.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(d.help.View(d.keyMap)),
	)
	return d.style().Render(content)
}

func (d *appsDialogCmp) Cursor() *tea.Cursor {
	if cursor, ok := d.appList.(util.Cursor); ok {
		cursor := cursor.Cursor()
		if cursor != nil {
			cursor = d.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (d *appsDialogCmp) listWidth() int {
	return defaultWidth - 2
}

func (d *appsDialogCmp) listHeight() int {
	listHeight := len(d.appList.Items()) + 2 + 4 // height based on items + 2 for input + 4 for sections
	return min(listHeight, d.wHeight/2)
}

func (d *appsDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := d.Position()
	offset := row + 3
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

func (d *appsDialogCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(d.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (d *appsDialogCmp) Position() (int, int) {
	row := d.wHeight/4 - 2 // just a bit above the center
	col := d.wWidth / 2
	col -= d.width / 2
	return row, col
}

func (d *appsDialogCmp) ID() dialogs.DialogID {
	return AppsDialogID
}
