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
// Package anim provides animation components (stub).
package anim

import (
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/tui/util"
)

// StepMsg is sent when an animation step occurs.
type StepMsg struct {
	Time time.Time
}

// Anim is an alias for Model (for compatibility).
type Anim = Model

// Settings configures an animation.
type Settings struct {
	Size        int
	GradColorA  color.Color
	GradColorB  color.Color
	CycleColors bool
	Label       string
	LabelColor  color.Color
}

// Model is an animation component.
type Model struct {
	frames   []string
	index    int
	speed    int
	running  bool
	style    lipgloss.Style
	settings Settings
}

// New creates a new animation with settings.
func New(s ...Settings) *Model {
	m := &Model{
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		speed:  100,
	}
	if len(s) > 0 {
		m.settings = s[0]
	}
	return m
}

// Spinner creates a spinner animation.
func Spinner() *Model {
	return New()
}

// Init initializes the animation.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	return m, nil
}

// View renders the animation.
func (m *Model) View() string {
	if !m.running || len(m.frames) == 0 {
		return ""
	}
	return m.style.Render(m.frames[m.index%len(m.frames)])
}

// Start starts the animation.
func (m *Model) Start() tea.Cmd {
	m.running = true
	return nil
}

// Stop stops the animation.
func (m *Model) Stop() {
	m.running = false
}

// Running returns whether the animation is running.
func (m *Model) Running() bool {
	return m.running
}

// SetStyle sets the style.
func (m *Model) SetStyle(s lipgloss.Style) {
	m.style = s
}

// Tick advances the animation.
func (m *Model) Tick() {
	if m.running && len(m.frames) > 0 {
		m.index = (m.index + 1) % len(m.frames)
	}
}

// SetLabel sets a label for the animation.
func (m *Model) SetLabel(label string) {
	// Label is not rendered in this stub
}
