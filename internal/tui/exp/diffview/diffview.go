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
// Package diffview provides diff viewing components (stub).
package diffview

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
)

// DiffView is an alias for Model (for compatibility with usage patterns).
type DiffView = Model

// Model is a diff viewer component.
type Model struct {
	old         string
	new         string
	width       int
	height      int
	styles      Styles
	chromaStyle *chroma.Style
	tabWidth    int
}

// LineStyle configures a diff line style.
type LineStyle struct {
	Line       lipgloss.Style
	Number     lipgloss.Style
	LineNumber lipgloss.Style
	Gutter     lipgloss.Style
	GutterSet  string
	Code       lipgloss.Style
	Symbol     lipgloss.Style
}

// Styles configures diff viewer appearance.
type Styles struct {
	Added       lipgloss.Style
	Removed     lipgloss.Style
	Changed     lipgloss.Style
	Unchanged   lipgloss.Style
	LineNumber  lipgloss.Style
	Header      lipgloss.Style
	DividerLine LineStyle
	MissingLine LineStyle
	EqualLine   LineStyle
	InsertLine  LineStyle
	DeleteLine  LineStyle
}

// Style is an alias for Styles for compatibility.
type Style = Styles

// DefaultStyles returns default styles.
func DefaultStyles() Styles {
	return Styles{
		Added:      lipgloss.NewStyle().Foreground(lipgloss.Color("#9ECE6A")),
		Removed:    lipgloss.NewStyle().Foreground(lipgloss.Color("#F7768E")),
		Changed:    lipgloss.NewStyle().Foreground(lipgloss.Color("#E0AF68")),
		Unchanged:  lipgloss.NewStyle(),
		LineNumber: lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		Header:     lipgloss.NewStyle().Bold(true),
	}
}

// New creates a new diff viewer.
func New() *Model {
	return &Model{
		styles:   DefaultStyles(),
		tabWidth: 4,
	}
}

// SetContent sets the diff content.
func (m *Model) SetContent(old, new string) {
	m.old = old
	m.new = new
}

// SetSize sets the viewer size.
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// SetStyles sets the styles.
func (m *Model) SetStyles(s Styles) {
	m.styles = s
}

// Init initializes the viewer.
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	return m, nil
}

// View renders the diff viewer.
func (m *Model) View() string {
	if m.old == "" && m.new == "" {
		return ""
	}
	return m.styles.Header.Render("Diff View (stub)")
}

// GetWidth returns the viewer width.
func (m *Model) GetWidth() int {
	return m.width
}

// GetHeight returns the viewer height.
func (m *Model) GetHeight() int {
	return m.height
}

// ChromaStyle sets the chroma style and returns self for chaining.
func (m *Model) ChromaStyle(style *chroma.Style) *Model {
	m.chromaStyle = style
	return m
}

// Style sets the diff styles and returns self for chaining.
func (m *Model) Style(s Styles) *Model {
	m.styles = s
	return m
}

// TabWidth sets the tab width and returns self for chaining.
func (m *Model) TabWidth(width int) *Model {
	m.tabWidth = width
	return m
}

// Before sets the old content and returns self for chaining.
func (m *Model) Before(path string, content ...string) *Model {
	if len(content) > 0 {
		m.old = content[0]
	} else {
		m.old = path
	}
	return m
}

// After sets the new content and returns self for chaining.
func (m *Model) After(path string, content ...string) *Model {
	if len(content) > 0 {
		m.new = content[0]
	} else {
		m.new = path
	}
	return m
}

// Height sets the height and returns self for chaining.
func (m *Model) Height(h int) *Model {
	m.height = h
	return m
}

// Width sets the width and returns self for chaining.
func (m *Model) Width(w int) *Model {
	m.width = w
	return m
}

// XOffset sets the X offset and returns self for chaining.
func (m *Model) XOffset(x int) *Model {
	return m
}

// YOffset sets the Y offset and returns self for chaining.
func (m *Model) YOffset(y int) *Model {
	return m
}

// Split sets the diff to split view mode.
func (m *Model) Split() *Model {
	return m
}

// Unified sets the diff to unified view mode.
func (m *Model) Unified() *Model {
	return m
}

// String returns the rendered diff as a string.
func (m *Model) String() string {
	return m.View()
}
