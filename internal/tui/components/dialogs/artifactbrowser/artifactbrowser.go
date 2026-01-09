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
package artifactbrowser

import (
	"fmt"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const ArtifactBrowserID dialogs.DialogID = "artifactbrowser"

// ArtifactBrowser interface for the artifact browser dialog
type ArtifactBrowser interface {
	dialogs.DialogModel
}

// ArtifactSelectedMsg is sent when an artifact is selected
type ArtifactSelectedMsg struct {
	Artifact *loomv1.Artifact
}

type ArtifactsList = list.FilterableList[list.CompletionItem[*loomv1.Artifact]]

type artifactBrowserCmp struct {
	wWidth        int
	wHeight       int
	width         int
	keyMap        KeyMap
	artifactsList ArtifactsList
	help          help.Model
}

// NewArtifactBrowserCmp creates a new artifact browser dialog
func NewArtifactBrowserCmp(artifacts []*loomv1.Artifact) ArtifactBrowser {
	t := styles.CurrentTheme()
	listKeyMap := list.DefaultKeyMap()
	keyMap := DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	items := make([]list.CompletionItem[*loomv1.Artifact], len(artifacts))
	if len(artifacts) > 0 {
		for i, artifact := range artifacts {
			// Create display string with name and content type
			displayStr := fmt.Sprintf("%s (%s)", artifact.Name, artifact.ContentType)
			items[i] = list.NewCompletionItem(displayStr, artifact, list.WithCompletionID(artifact.Id))
		}
	}

	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	artifactsList := list.NewFilterableList(
		items,
		list.WithFilterPlaceholder("Enter artifact name or type"),
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	a := &artifactBrowserCmp{
		keyMap:        DefaultKeyMap(),
		artifactsList: artifactsList,
		help:          help,
	}

	return a
}

func (a *artifactBrowserCmp) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, a.artifactsList.Init())
	cmds = append(cmds, a.artifactsList.Focus())
	return tea.Sequence(cmds...)
}

func (a *artifactBrowserCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		var cmds []tea.Cmd
		a.wWidth = msg.Width
		a.wHeight = msg.Height
		a.width = min(120, a.wWidth-8)
		a.artifactsList.SetInputWidth(a.listWidth() - 2)
		cmds = append(cmds, a.artifactsList.SetSize(a.listWidth(), a.listHeight()))
		return a, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, a.keyMap.Select):
			selectedItem := a.artifactsList.SelectedItem()
			if selectedItem != nil {
				selected := *selectedItem
				return a, tea.Sequence(
					util.CmdHandler(dialogs.CloseDialogMsg{}),
					util.CmdHandler(
						ArtifactSelectedMsg{Artifact: selected.Value()},
					),
				)
			}
		case key.Matches(msg, a.keyMap.Close):
			return a, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := a.artifactsList.Update(msg)
			a.artifactsList = u.(ArtifactsList)
			return a, cmd
		}
	}
	return a, nil
}

func (a *artifactBrowserCmp) View() string {
	t := styles.CurrentTheme()
	listView := a.artifactsList.View()
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Browse Artifacts", a.width-4)),
		listView,
		"",
		t.S().Base.Width(a.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(a.help.View(a.keyMap)),
	)

	return a.style().Render(content)
}

func (a *artifactBrowserCmp) Cursor() *tea.Cursor {
	if cursorProvider, ok := a.artifactsList.(util.Cursor); ok {
		cursor := cursorProvider.Cursor()
		if cursor != nil {
			cursor = a.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (a *artifactBrowserCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(a.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (a *artifactBrowserCmp) listHeight() int {
	return a.wHeight/2 - 6 // 5 for the border, title and help
}

func (a *artifactBrowserCmp) listWidth() int {
	return a.width - 2 // 2 for the border
}

func (a *artifactBrowserCmp) Position() (int, int) {
	row := a.wHeight/4 - 2 // just a bit above the center
	col := a.wWidth / 2
	col -= a.width / 2
	return row, col
}

func (a *artifactBrowserCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := a.Position()
	offset := row + 3 // Border + title
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

// ID implements ArtifactBrowser.
func (a *artifactBrowserCmp) ID() dialogs.DialogID {
	return ArtifactBrowserID
}
