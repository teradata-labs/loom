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
package sessions

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/event"
	"github.com/teradata-labs/loom/internal/session"
	"github.com/teradata-labs/loom/internal/tui/components/chat"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const SessionsDialogID dialogs.DialogID = "sessions"

// SessionDialog interface for the session switching dialog
type SessionDialog interface {
	dialogs.DialogModel
}

type SessionsList = list.FilterableList[list.CompletionItem[session.Session]]

type sessionDialogCmp struct {
	wWidth            int
	wHeight           int
	width             int
	selectedSessionID string
	keyMap            KeyMap
	sessionsList      SessionsList
	help              help.Model
}

// NewSessionDialogCmp creates a new session switching dialog
func NewSessionDialogCmp(sessions []session.Session, selectedID string) SessionDialog {
	t := styles.CurrentTheme()
	listKeyMap := list.DefaultKeyMap()
	keyMap := DefaultKeyMap()
	listKeyMap.Down.SetEnabled(false)
	listKeyMap.Up.SetEnabled(false)
	listKeyMap.DownOneItem = keyMap.Next
	listKeyMap.UpOneItem = keyMap.Previous

	items := make([]list.CompletionItem[session.Session], len(sessions))
	if len(sessions) > 0 {
		for i, session := range sessions {
			items[i] = list.NewCompletionItem(session.Title, session, list.WithCompletionID(session.ID))
		}
	}

	inputStyle := t.S().Base.PaddingLeft(1).PaddingBottom(1)
	sessionsList := list.NewFilterableList(
		items,
		list.WithFilterPlaceholder("Enter a session name"),
		list.WithFilterInputStyle(inputStyle),
		list.WithFilterListOptions(
			list.WithKeyMap(listKeyMap),
			list.WithWrapNavigation(),
		),
	)
	help := help.New()
	help.Styles = t.S().Help
	s := &sessionDialogCmp{
		selectedSessionID: selectedID,
		keyMap:            DefaultKeyMap(),
		sessionsList:      sessionsList,
		help:              help,
	}

	return s
}

func (s *sessionDialogCmp) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, s.sessionsList.Init())
	cmds = append(cmds, s.sessionsList.Focus())
	return tea.Sequence(cmds...)
}

func (s *sessionDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		var cmds []tea.Cmd
		s.wWidth = msg.Width
		s.wHeight = msg.Height
		s.width = min(120, s.wWidth-8)
		s.sessionsList.SetInputWidth(s.listWidth() - 2)
		cmds = append(cmds, s.sessionsList.SetSize(s.listWidth(), s.listHeight()))
		if s.selectedSessionID != "" {
			cmds = append(cmds, s.sessionsList.SetSelected(s.selectedSessionID))
		}
		return s, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, s.keyMap.Select):
			selectedItem := s.sessionsList.SelectedItem()
			if selectedItem != nil {
				selected := *selectedItem
				event.SessionSwitched()
				return s, tea.Sequence(
					util.CmdHandler(dialogs.CloseDialogMsg{}),
					util.CmdHandler(
						chat.SessionSelectedMsg(selected.Value()),
					),
				)
			}
		case key.Matches(msg, s.keyMap.Close):
			return s, util.CmdHandler(dialogs.CloseDialogMsg{})
		default:
			u, cmd := s.sessionsList.Update(msg)
			s.sessionsList = u.(SessionsList)
			return s, cmd
		}
	}
	return s, nil
}

func (s *sessionDialogCmp) View() string {
	t := styles.CurrentTheme()
	listView := s.sessionsList.View()
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		t.S().Base.Padding(0, 1, 1, 1).Render(core.Title("Switch Session", s.width-4)),
		listView,
		"",
		t.S().Base.Width(s.width-2).PaddingLeft(1).AlignHorizontal(lipgloss.Left).Render(s.help.View(s.keyMap)),
	)

	return s.style().Render(content)
}

func (s *sessionDialogCmp) Cursor() *tea.Cursor {
	if cursorProvider, ok := s.sessionsList.(util.Cursor); ok {
		cursor := cursorProvider.Cursor()
		if cursor != nil {
			cursor = s.moveCursor(cursor)
		}
		return cursor
	}
	return nil
}

func (s *sessionDialogCmp) style() lipgloss.Style {
	t := styles.CurrentTheme()
	return t.S().Base.
		Width(s.width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)
}

func (s *sessionDialogCmp) listHeight() int {
	return s.wHeight/2 - 6 // 5 for the border, title and help
}

func (s *sessionDialogCmp) listWidth() int {
	return s.width - 2 // 2 for the border
}

func (s *sessionDialogCmp) Position() (int, int) {
	row := s.wHeight/4 - 2 // just a bit above the center
	col := s.wWidth / 2
	col -= s.width / 2
	return row, col
}

func (s *sessionDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := s.Position()
	offset := row + 3 // Border + title
	cursor.Y += offset
	cursor.X = cursor.X + col + 2
	return cursor
}

// ID implements SessionDialog.
func (s *sessionDialogCmp) ID() dialogs.DialogID {
	return SessionsDialogID
}
