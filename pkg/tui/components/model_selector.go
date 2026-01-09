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
package components

import (
	"fmt"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// ModelItem represents a selectable LLM model.
type ModelItem struct {
	info *loomv1.ModelInfo
}

// FilterValue implements list.Item.
func (m ModelItem) FilterValue() string {
	if m.info == nil {
		return ""
	}
	return m.info.Name
}

// ModelSelectorModel is a UI component for selecting LLM models.
type ModelSelectorModel struct {
	list     list.Model
	styles   *styles.Styles
	icons    *styles.Icons
	selected *loomv1.ModelInfo
	quitting bool
	width    int
	height   int
}

// NewModelSelector creates a new model selector.
func NewModelSelector(models []*loomv1.ModelInfo, currentModel *loomv1.ModelInfo, s *styles.Styles) *ModelSelectorModel {
	icons := styles.DefaultIcons()

	items := make([]list.Item, len(models))
	for i, model := range models {
		items[i] = ModelItem{info: model}
	}

	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(s.Theme.Primary).
		Bold(true)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(s.Theme.TextDim)

	l := list.New(items, delegate, 0, 0)
	l.Title = fmt.Sprintf("%s  Switch Model", icons.ModelIcon)
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.Styles.Title = l.Styles.Title.
		Foreground(s.Theme.Primary).
		Bold(true).
		MarginLeft(2)

	return &ModelSelectorModel{
		list:     l,
		styles:   s,
		icons:    icons,
		selected: currentModel,
	}
}

// Init initializes the model selector.
func (m *ModelSelectorModel) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *ModelSelectorModel) Update(msg tea.Msg) (*ModelSelectorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-4, msg.Height-4)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "enter":
			if item, ok := m.list.SelectedItem().(ModelItem); ok {
				m.selected = item.info
				m.quitting = true
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View renders the model selector.
func (m *ModelSelectorModel) View() string {
	if m.quitting {
		return ""
	}

	// Render list in a styled box
	content := m.list.View()

	// Add help text at bottom
	helpStyle := lipgloss.NewStyle().
		Foreground(m.styles.Theme.TextDim).
		MarginTop(1).
		MarginLeft(2)

	help := helpStyle.Render("enter: select • esc: cancel • /: filter")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.styles.Theme.Primary).
		Padding(1, 2).
		Width(m.width - 4).
		Height(m.height - 4)

	return box.Render(lipgloss.JoinVertical(
		lipgloss.Left,
		content,
		help,
	))
}

// Selected returns the selected model, if any.
func (m *ModelSelectorModel) Selected() *loomv1.ModelInfo {
	return m.selected
}

// IsDone returns true if the selector has finished (user selected or cancelled).
func (m *ModelSelectorModel) IsDone() bool {
	return m.quitting
}

// Title returns the title for a model item.
func (m ModelItem) Title() string {
	if m.info == nil {
		return ""
	}
	return fmt.Sprintf("%s (%s)", m.info.Name, m.info.Provider)
}

// Description returns the description for a model item.
func (m ModelItem) Description() string {
	if m.info == nil {
		return ""
	}
	desc := fmt.Sprintf("Context: %dk", m.info.ContextWindow/1000)

	if m.info.CostPer_1MInputUsd > 0 {
		desc += fmt.Sprintf(" • In: $%.2f/1M", m.info.CostPer_1MInputUsd)
	}
	if m.info.CostPer_1MOutputUsd > 0 {
		desc += fmt.Sprintf(" • Out: $%.2f/1M", m.info.CostPer_1MOutputUsd)
	}

	if !m.info.Available {
		desc += " • UNAVAILABLE"
	}

	return desc
}
