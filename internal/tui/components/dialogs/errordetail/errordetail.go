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
package errordetail

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const ErrorDetailDialogID dialogs.DialogID = "error-detail"

// ErrorDetailDialog shows the full text of an error in a scrollable overlay.
type ErrorDetailDialog interface {
	dialogs.DialogModel
}

type keyMap struct {
	Close key.Binding
	Up    key.Binding
	Down  key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Close: key.NewBinding(
			key.WithKeys("esc", "alt+esc", "enter", "q"),
			key.WithHelp("esc/enter", "close"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "scroll up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "scroll down"),
		),
	}
}

type errorDetailDialog struct {
	wWidth, wHeight int
	width, height   int
	posRow, posCol  int

	errText  string
	viewport viewport.Model
	keyMap   keyMap
}

// New creates an error detail dialog displaying the full error text.
func New(errText string) ErrorDetailDialog {
	return &errorDetailDialog{
		errText:  errText,
		viewport: viewport.New(),
		keyMap:   defaultKeyMap(),
	}
}

func (d *errorDetailDialog) Init() tea.Cmd {
	return nil
}

func (d *errorDetailDialog) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
		d.recalcLayout()
		return d, nil

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Close):
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
	}

	var cmd tea.Cmd
	d.viewport, cmd = d.viewport.Update(msg)
	return d, cmd
}

func (d *errorDetailDialog) recalcLayout() {
	d.width = min(d.wWidth*9/10, 100)
	d.height = min(d.wHeight*7/10, 20)

	d.posRow = (d.wHeight - d.height) / 2
	d.posCol = (d.wWidth - d.width) / 2

	vpWidth := d.width - 4
	vpHeight := max(d.height-6, 2) // title + padding + help line; at least 2 rows
	d.viewport.SetWidth(vpWidth)
	d.viewport.SetHeight(vpHeight)
	d.viewport.SetContent(d.errText)
}

func (d *errorDetailDialog) View() string {
	t := styles.CurrentTheme()
	s := t.S()

	title := core.Title("Error Details", d.width-4)

	h := help.New()
	h.Styles.ShortKey = s.Subtle
	h.Styles.ShortDesc = s.Subtle
	helpLine := h.ShortHelpView([]key.Binding{
		d.keyMap.Close,
		d.keyMap.Up,
		d.keyMap.Down,
	})

	content := lipgloss.JoinVertical(lipgloss.Top,
		title,
		"",
		d.viewport.View(),
		"",
		helpLine,
	)

	dialog := s.Base.
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Red).
		Width(d.width).
		Render(content)

	return dialog
}

func (d *errorDetailDialog) Position() (int, int) {
	return d.posRow, d.posCol
}

func (d *errorDetailDialog) ID() dialogs.DialogID {
	return ErrorDetailDialogID
}
