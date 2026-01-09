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
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// SpinnerModel is an enhanced spinner with gradient colors and animations
// inspired by Crush TUI.
type SpinnerModel struct {
	frames   []string
	frameIdx int
	style    lipgloss.Style
	isActive bool
	message  string
	theme    *styles.Theme
}

// spinnerTickMsg is sent to advance the spinner animation.
type spinnerTickMsg time.Time

// NewSpinner creates a new enhanced spinner.
func NewSpinner(theme *styles.Theme) *SpinnerModel {
	// Smooth spinner frames
	frames := []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
	}

	return &SpinnerModel{
		frames:   frames,
		frameIdx: 0,
		style:    lipgloss.NewStyle().Foreground(theme.Primary),
		isActive: false,
		theme:    theme,
	}
}

// Start activates the spinner.
func (s *SpinnerModel) Start() {
	s.isActive = true
}

// Stop deactivates the spinner.
func (s *SpinnerModel) Stop() {
	s.isActive = false
}

// SetMessage sets the spinner message.
func (s *SpinnerModel) SetMessage(msg string) {
	s.message = msg
}

// IsActive returns whether the spinner is animating.
func (s *SpinnerModel) IsActive() bool {
	return s.isActive
}

// Update handles spinner animation.
func (s *SpinnerModel) Update(msg tea.Msg) (*SpinnerModel, tea.Cmd) {
	switch msg.(type) {
	case spinnerTickMsg:
		if s.isActive {
			s.frameIdx = (s.frameIdx + 1) % len(s.frames)
			return s, spinnerTick()
		}
	}
	return s, nil
}

// View renders the spinner.
func (s *SpinnerModel) View() string {
	if !s.isActive {
		return ""
	}

	frame := s.frames[s.frameIdx]
	styledFrame := s.style.Render(frame)

	if s.message != "" {
		dimStyle := lipgloss.NewStyle().Foreground(s.theme.TextDim)
		return lipgloss.JoinHorizontal(lipgloss.Left, styledFrame, " ", dimStyle.Render(s.message))
	}

	return styledFrame
}

// spinnerTick returns a command that triggers the next animation frame.
func spinnerTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// StartSpinner returns a command to start the spinner animation.
func StartSpinner() tea.Cmd {
	return spinnerTick()
}
