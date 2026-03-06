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
package pattern

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/teradata-labs/loom/internal/tui/components/chat/sidebar"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const (
	patternBrowserDialogID dialogs.DialogID = "pattern-browser"
	patternBrowserWidth    int              = 60
)

// PatternBrowserDialog shows all pattern categories and files for selection.
type PatternBrowserDialog interface {
	dialogs.DialogModel
}

// PatternBrowserKeyMap defines key bindings for the pattern browser dialog.
type PatternBrowserKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	View   key.Binding
	Close  key.Binding
}

// DefaultPatternBrowserKeyMap returns default key bindings.
func DefaultPatternBrowserKeyMap() PatternBrowserKeyMap {
	return PatternBrowserKeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "edit"),
		),
		View: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "view"),
		),
		Close: key.NewBinding(
			key.WithKeys("esc", "q"),
			key.WithHelp("esc/q", "close"),
		),
	}
}

// ShortHelp returns key bindings for the short help view.
func (k PatternBrowserKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Select, k.View, k.Close}
}

// FullHelp returns key bindings for the full help view.
func (k PatternBrowserKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down, k.Select, k.View, k.Close}}
}

// navigableItem represents a single navigable row in the browser.
type navigableItem struct {
	categoryIdx int
	fileIdx     int // -1 = category header row
}

type patternBrowserDialogCmp struct {
	wWidth, wHeight int

	categories         []sidebar.PatternCategory
	expandedCategories map[string]bool
	items              []navigableItem // flattened list of visible rows
	selectedIndex      int

	scrollOffset int // top-most visible item index

	keys PatternBrowserKeyMap
	help help.Model
}

// NewPatternBrowserDialog creates a new pattern browser dialog.
func NewPatternBrowserDialog() PatternBrowserDialog {
	t := styles.CurrentTheme()
	h := help.New()
	h.Styles = t.S().Help
	d := &patternBrowserDialogCmp{
		expandedCategories: make(map[string]bool),
		keys:               DefaultPatternBrowserKeyMap(),
		help:               h,
	}
	return d
}

func (d *patternBrowserDialogCmp) Init() tea.Cmd {
	d.categories = sidebar.ListPatternCategories()
	d.rebuildItems()
	return nil
}

// rebuildItems refreshes the flattened navigable item list.
func (d *patternBrowserDialogCmp) rebuildItems() {
	d.items = nil
	for ci, cat := range d.categories {
		d.items = append(d.items, navigableItem{categoryIdx: ci, fileIdx: -1})
		if d.expandedCategories[cat.Name] {
			for fi := range cat.Files {
				d.items = append(d.items, navigableItem{categoryIdx: ci, fileIdx: fi})
			}
		}
	}
}

func (d *patternBrowserDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.wWidth = msg.Width
		d.wHeight = msg.Height
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keys.Close):
			return d, util.CmdHandler(dialogs.CloseDialogMsg{})
		case key.Matches(msg, d.keys.Up):
			if d.selectedIndex > 0 {
				d.selectedIndex--
				d.clampScroll()
			}
		case key.Matches(msg, d.keys.Down):
			if d.selectedIndex < len(d.items)-1 {
				d.selectedIndex++
				d.clampScroll()
			}
		case key.Matches(msg, d.keys.Select):
			return d, d.activateSelected(false)
		case key.Matches(msg, d.keys.View):
			return d, d.activateSelected(true)
		}
	}
	return d, nil
}

// activateSelected handles Enter (edit) and v (view) on the selected item.
func (d *patternBrowserDialogCmp) activateSelected(viewOnly bool) tea.Cmd {
	if len(d.items) == 0 || d.selectedIndex >= len(d.items) {
		return nil
	}
	item := d.items[d.selectedIndex]
	cat := d.categories[item.categoryIdx]

	if item.fileIdx == -1 {
		// Category row: toggle expansion
		d.expandedCategories[cat.Name] = !d.expandedCategories[cat.Name]
		d.rebuildItems()
		return nil
	}

	// File row
	filePath := cat.Files[item.fileIdx]
	return tea.Sequence(
		util.CmdHandler(dialogs.CloseDialogMsg{}),
		func() tea.Msg {
			if viewOnly {
				return dialogs.OpenDialogMsg{Model: NewPatternViewerDialog(filePath)}
			}
			return sidebar.PatternFileSelectedMsg{FilePath: filePath}
		},
	)
}

// clampScroll ensures selectedIndex is within the visible window.
func (d *patternBrowserDialogCmp) clampScroll() {
	maxVisible := d.maxVisibleItems()
	if d.selectedIndex < d.scrollOffset {
		d.scrollOffset = d.selectedIndex
	}
	if d.selectedIndex >= d.scrollOffset+maxVisible {
		d.scrollOffset = d.selectedIndex - maxVisible + 1
	}
	if d.scrollOffset < 0 {
		d.scrollOffset = 0
	}
}

func (d *patternBrowserDialogCmp) maxVisibleItems() int {
	// Reserve lines: title(1) + padding(2) + help(1) + borders(2)
	maxH := d.wHeight/2 - 4
	if maxH < 5 {
		maxH = 5
	}
	return maxH
}

func (d *patternBrowserDialogCmp) View() string {
	t := styles.CurrentTheme()
	maxVisible := d.maxVisibleItems()

	title := core.Title("Pattern Library", patternBrowserWidth-4)
	header := t.S().Base.Padding(0, 1, 1, 1).Render(title)

	var rows []string
	end := d.scrollOffset + maxVisible
	if end > len(d.items) {
		end = len(d.items)
	}

	if len(d.items) == 0 {
		rows = append(rows, t.S().Subtle.PaddingLeft(2).Render("No patterns found"))
	}

	for i := d.scrollOffset; i < end; i++ {
		item := d.items[i]
		cat := d.categories[item.categoryIdx]
		isSelected := i == d.selectedIndex

		var line string
		if item.fileIdx == -1 {
			// Category row
			isExpanded := d.expandedCategories[cat.Name]
			icon := "▶"
			if isExpanded {
				icon = "▼"
			}
			iconColor := t.FgSubtle
			nameColor := t.FgBase
			if isSelected {
				iconColor = t.Primary
				nameColor = t.Primary
				icon = "⏺"
			}
			styledIcon := t.S().Base.Foreground(iconColor).Render(icon)
			styledName := t.S().Base.Foreground(nameColor).Render(cat.Name)
			count := t.S().Muted.Render(fmt.Sprintf("(%d)", cat.Count))
			if isSelected {
				line = fmt.Sprintf("> %s %s %s", styledIcon, styledName, count)
			} else {
				line = fmt.Sprintf("  %s %s %s", styledIcon, styledName, count)
			}
		} else {
			// File row (indented)
			filePath := cat.Files[item.fileIdx]
			fileName := filepath.Base(filePath)
			fileName = strings.TrimSuffix(fileName, filepath.Ext(fileName))

			fileColor := t.FgSubtle
			prefix := "    ⏺ "
			if isSelected {
				fileColor = t.Primary
				prefix = "  > ⏺ "
			}
			line = prefix + t.S().Base.Foreground(fileColor).Render(fileName)
		}
		rows = append(rows, line)
	}

	listContent := strings.Join(rows, "\n")

	helpView := t.S().Base.Width(patternBrowserWidth - 2).PaddingLeft(1).
		AlignHorizontal(lipgloss.Left).Render(d.help.View(d.keys))

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		t.S().Base.PaddingLeft(1).Render(listContent),
		"",
		helpView,
	)

	dialogStyle := t.S().Base.
		Width(patternBrowserWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus)

	return dialogStyle.Render(content)
}

func (d *patternBrowserDialogCmp) ID() dialogs.DialogID {
	return patternBrowserDialogID
}

func (d *patternBrowserDialogCmp) Position() (int, int) {
	row := d.wHeight/4 - 2
	col := d.wWidth/2 - patternBrowserWidth/2
	return row, col
}
