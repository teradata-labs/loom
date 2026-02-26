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
// Package models provides a model selection dialog for Loom.
// Model selection in Loom is handled via the server's looms.yaml configuration.
package models

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/config"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/exp/list"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

// ModelSelectorID is the dialog ID.
const ModelSelectorID = "models"

// ModelsDialogID is an alias for ModelSelectorID.
const ModelsDialogID = ModelSelectorID

// ModelChangedMsg is sent when the model is changed.
type ModelChangedMsg struct {
	Model config.Model
}

// ModelSelectedMsg is sent when a model is selected.
type ModelSelectedMsg struct {
	Model     config.Model
	ModelType config.SelectedModelType
}

// ProvidersLoadedMsg is sent when the provider list has been loaded.
type ProvidersLoadedMsg struct {
	Providers []*loomv1.ProviderEntry
	Active    string
}

// ModelOption represents a model selection option.
type ModelOption struct {
	Name        string
	Provider    string
	Description string
}

// Model is the model selector dialog.
type Model struct {
	width     int
	height    int
	providers []*loomv1.ProviderEntry
	active    string // currently active provider name
	cursor    int
}

// New creates a new model selector.
func New() *Model {
	return &Model{}
}

// NewModelDialogCmp creates a new model dialog component.
func NewModelDialogCmp() *Model {
	return New()
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return nil
}

// SetProviders sets the provider list and currently active provider.
func (m *Model) SetProviders(providers []*loomv1.ProviderEntry, active string) {
	m.providers = providers
	m.active = active
	m.cursor = 0
	// Position cursor on the active provider if present
	for i, p := range providers {
		if p.GetName() == active {
			m.cursor = i
			break
		}
	}
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil
	case ProvidersLoadedMsg:
		m.SetProviders(msg.Providers, msg.Active)
		return m, nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc", "q"))):
			return m, util.CmdHandler(dialogs.CloseDialogMsg{})
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if m.cursor > 0 {
				m.cursor--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if m.cursor < len(m.providers)-1 {
				m.cursor++
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if len(m.providers) > 0 {
				selected := m.providers[m.cursor]
				return m, tea.Batch(
					util.CmdHandler(dialogs.CloseDialogMsg{}),
					util.CmdHandler(ModelSelectedMsg{
						Model: config.Model{Name: selected.GetName()},
					}),
				)
			}
			// No providers loaded: just close
			return m, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
	}
	return m, nil
}

// View renders the model.
func (m *Model) View() string {
	t := styles.CurrentTheme()

	title := t.S().Base.Bold(true).Foreground(t.Primary).Render("Provider Pool")

	var body string
	if len(m.providers) == 0 {
		// Fallback: no providers configured - show static config hint
		body = t.S().Base.Foreground(t.FgBase).Render(
			"No named providers in pool.\n\n" +
				"Edit your looms.yaml to add providers:\n" +
				"  providers:\n" +
				"    - name: claude-opus\n" +
				"      provider: anthropic\n" +
				"      anthropic_model: claude-opus-4-5\n" +
				"    - name: llama-local\n" +
				"      provider: ollama\n" +
				"      ollama_model: llama3.2\n\n" +
				"Press ESC to close",
		)
	} else {
		lines := make([]string, 0, len(m.providers)+2)
		lines = append(lines, "Select a provider (enter to switch, esc to cancel):\n")
		for i, p := range m.providers {
			cfg := p.GetConfig()
			providerStr := ""
			modelStr := ""
			if cfg != nil {
				providerStr = cfg.GetProvider()
				modelStr = cfg.GetModel()
			}
			label := fmt.Sprintf("  %s  [%s / %s]", p.GetName(), providerStr, modelStr)
			if p.GetName() == m.active {
				label += " *"
			}
			if i == m.cursor {
				label = t.S().Base.Bold(true).Foreground(t.Primary).Render("> " + label[2:])
			} else {
				label = t.S().Base.Foreground(t.FgBase).Render(label)
			}
			lines = append(lines, label)
		}
		lines = append(lines, "\n[up/down] navigate  [enter] select  [esc] cancel")
		body = lipgloss.JoinVertical(lipgloss.Left, lines...)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body)

	return t.S().Base.
		Width(64).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus).
		Render(content)
}

// ID returns the dialog ID.
func (m *Model) ID() dialogs.DialogID {
	return ModelSelectorID
}

// Position returns the dialog position.
func (m *Model) Position() (int, int) {
	row := m.height/4 - 2 // just a bit above the center
	col := m.width / 2
	col -= 32 // half of dialog width (64/2)
	// Ensure non-negative positions
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	return row, col
}

// SetSize sets the dialog size.
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// ModelListComponent is a stub list component for model selection.
type ModelListComponent struct {
	width  int
	height int
}

// NewModelListComponent creates a new model list component.
func NewModelListComponent(keyMap list.KeyMap, placeholder string, showProvider bool) *ModelListComponent {
	return &ModelListComponent{}
}

// Init initializes the component.
func (m *ModelListComponent) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *ModelListComponent) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return m, nil
}

// View renders the component.
func (m *ModelListComponent) View() string {
	return ""
}

// SetSize sets the component size.
func (m *ModelListComponent) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	return nil
}

// GetSize returns the component size.
func (m *ModelListComponent) GetSize() (int, int) {
	return m.width, m.height
}

// Focus focuses the component.
func (m *ModelListComponent) Focus() tea.Cmd {
	return nil
}

// Blur blurs the component.
func (m *ModelListComponent) Blur() tea.Cmd {
	return nil
}

// IsFocused returns whether the component is focused.
func (m *ModelListComponent) IsFocused() bool {
	return false
}

// SelectedModel returns the currently selected model.
func (m *ModelListComponent) SelectedModel() *ModelOption {
	return nil
}

// APIKeyInput is a stub API key input component.
type APIKeyInput struct {
	value   string
	focused bool
	width   int
}

// NewAPIKeyInput creates a new API key input.
func NewAPIKeyInput() *APIKeyInput {
	return &APIKeyInput{}
}

// Init initializes the component.
func (a *APIKeyInput) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (a *APIKeyInput) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return a, nil
}

// View renders the component.
func (a *APIKeyInput) View() string {
	return ""
}

// Focus focuses the component.
func (a *APIKeyInput) Focus() tea.Cmd {
	a.focused = true
	return nil
}

// Blur blurs the component.
func (a *APIKeyInput) Blur() tea.Cmd {
	a.focused = false
	return nil
}

// IsFocused returns whether the component is focused.
func (a *APIKeyInput) IsFocused() bool {
	return a.focused
}

// Value returns the current input value.
func (a *APIKeyInput) Value() string {
	return a.value
}

// SetSize sets the component width.
func (a *APIKeyInput) SetSize(width, height int) tea.Cmd {
	a.width = width
	return nil
}

// GetSize returns the component size.
func (a *APIKeyInput) GetSize() (int, int) {
	return a.width, 1
}

// Cursor returns the cursor position.
func (a *APIKeyInput) Cursor() *tea.Cursor {
	return nil
}

// SetWidth sets the component width.
func (a *APIKeyInput) SetWidth(width int) {
	a.width = width
}

// APIKeyInputState represents API key input state.
type APIKeyInputState int

const (
	APIKeyInputStateEmpty APIKeyInputState = iota
	APIKeyInputStateVerifying
	APIKeyInputStateVerified
	APIKeyInputStateInvalid
	APIKeyInputStateError
)

// APIKeyStateChangeMsg is sent when API key state changes.
type APIKeyStateChangeMsg struct {
	State APIKeyInputState
}
