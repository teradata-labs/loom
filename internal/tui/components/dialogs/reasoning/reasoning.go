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
// Package reasoning provides a stub reasoning dialog for Loom.
package reasoning

import (
	tea "charm.land/bubbletea/v2"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/util"
)

// ReasoningDialogID is the dialog ID.
const ReasoningDialogID = "reasoning"

// ReasoningEffortChangedMsg is sent when the reasoning effort is changed.
type ReasoningEffortChangedMsg struct {
	Effort string
}

// ReasoningEffortSelectedMsg is sent when a reasoning effort level is selected.
type ReasoningEffortSelectedMsg struct {
	Effort string
}

// Model is a stub reasoning dialog.
type Model struct {
	width  int
	height int
}

// New creates a new reasoning dialog stub.
func New() *Model {
	return &Model{}
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return m, nil
}

// View renders the model.
func (m *Model) View() string {
	return "Reasoning configuration not available"
}

// ID returns the dialog ID.
func (m *Model) ID() dialogs.DialogID {
	return ReasoningDialogID
}

// Position returns the dialog position.
func (m *Model) Position() (int, int) {
	return 5, 5
}

// SetSize sets the dialog size.
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}
