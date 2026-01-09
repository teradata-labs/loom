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
// Package hyper provides Hyper auth dialog stubs.
package hyper

import (
	tea "charm.land/bubbletea/v2"

	"github.com/teradata-labs/loom/internal/tui/styles"
)

// Model is the Hyper auth dialog model.
type Model struct {
	theme *styles.Theme
}

// New creates a new Hyper auth dialog.
func New(theme *styles.Theme) *Model {
	return &Model{
		theme: theme,
	}
}

// Init initializes the dialog.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	return m, nil
}

// View renders the dialog.
func (m *Model) View() string {
	return ""
}

// SetSize sets the dialog size.
func (m *Model) SetSize(width, height int) {
}

// SetTheme sets the theme.
func (m *Model) SetTheme(theme *styles.Theme) {
	m.theme = theme
}

// Visible returns whether the dialog is visible.
func (m *Model) Visible() bool {
	return false
}

// Show shows the dialog.
func (m *Model) Show() {
}

// Hide hides the dialog.
func (m *Model) Hide() {
}

// AuthRequired returns whether auth is required.
func AuthRequired() bool {
	return false
}

// IsAuthenticated returns whether user is authenticated.
func IsAuthenticated() bool {
	return true
}
