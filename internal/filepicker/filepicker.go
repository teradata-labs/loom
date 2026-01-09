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
// Package filepicker provides a file picker component (stub replacement for charm.land/bubbles/v2/filepicker).
package filepicker

import (
	"os"
	"path/filepath"
	"sort"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// KeyMap defines keybindings.
type KeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Back   key.Binding
	Select key.Binding
}

// Model is a file picker component.
type Model struct {
	// Exported fields for compatibility
	CurrentDirectory string
	AllowedTypes     []string
	ShowPermissions  bool
	ShowSize         bool
	AutoHeight       bool
	Cursor           string
	Styles           Styles
	KeyMap           KeyMap

	// Internal fields
	files       []os.DirEntry
	cursorIdx   int
	selected    string
	showHidden  bool
	dirOnly     bool
	fileAllowed bool
	height      int
	width       int
}

// Styles configures file picker appearance.
type Styles struct {
	Cursor           lipgloss.Style
	Symlink          lipgloss.Style
	Directory        lipgloss.Style
	File             lipgloss.Style
	Permission       lipgloss.Style
	Selected         lipgloss.Style
	DisabledCursor   lipgloss.Style
	DisabledFile     lipgloss.Style
	DisabledSelected lipgloss.Style
	EmptyDirectory   lipgloss.Style
	FileSize         lipgloss.Style
}

// DefaultStyles returns default styles.
func DefaultStyles() Styles {
	return Styles{
		Cursor:           lipgloss.NewStyle().Foreground(lipgloss.Color("#7B5FC7")),
		Directory:        lipgloss.NewStyle().Foreground(lipgloss.Color("#7AA2F7")),
		File:             lipgloss.NewStyle(),
		Symlink:          lipgloss.NewStyle().Foreground(lipgloss.Color("#BB9AF7")),
		Permission:       lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		Selected:         lipgloss.NewStyle().Bold(true),
		DisabledCursor:   lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		DisabledFile:     lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		DisabledSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		EmptyDirectory:   lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
		FileSize:         lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
	}
}

// New creates a new file picker.
func New() Model {
	cwd, _ := os.Getwd()
	return Model{
		CurrentDirectory: cwd,
		height:           20,
		width:            40,
		showHidden:       false,
		fileAllowed:      true,
		Styles:           DefaultStyles(),
		Cursor:           ">",
		KeyMap: KeyMap{
			Up:     key.NewBinding(key.WithKeys("up", "k")),
			Down:   key.NewBinding(key.WithKeys("down", "j")),
			Back:   key.NewBinding(key.WithKeys("backspace", "h")),
			Select: key.NewBinding(key.WithKeys("enter", "l")),
		},
	}
}

// Option configures the file picker.
type Option func(*Model)

// WithCurrentDir sets the current directory.
func WithCurrentDir(dir string) Option {
	return func(m *Model) {
		m.CurrentDirectory = dir
	}
}

// WithShowHidden shows hidden files.
func WithShowHidden(show bool) Option {
	return func(m *Model) {
		m.showHidden = show
	}
}

// WithDirOnly shows only directories.
func WithDirOnly(dirOnly bool) Option {
	return func(m *Model) {
		m.dirOnly = dirOnly
	}
}

// WithFileAllowed allows file selection.
func WithFileAllowed(allowed bool) Option {
	return func(m *Model) {
		m.fileAllowed = allowed
	}
}

// WithAllowedTypes sets allowed file types.
func WithAllowedTypes(types []string) Option {
	return func(m *Model) {
		m.AllowedTypes = types
	}
}

// WithHeight sets the picker height.
func WithHeight(h int) Option {
	return func(m *Model) {
		m.height = h
	}
}

// WithStyles sets the styles.
func WithStyles(s Styles) Option {
	return func(m *Model) {
		m.Styles = s
	}
}

// Init initializes the model.
func (m Model) Init() tea.Cmd {
	return m.readDir
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursorIdx > 0 {
				m.cursorIdx--
			}
		case "down", "j":
			if m.cursorIdx < len(m.files)-1 {
				m.cursorIdx++
			}
		case "enter":
			if m.cursorIdx < len(m.files) {
				f := m.files[m.cursorIdx]
				if f.IsDir() {
					m.CurrentDirectory = filepath.Join(m.CurrentDirectory, f.Name())
					m.cursorIdx = 0
					return m, m.readDir
				}
				if m.fileAllowed {
					m.selected = filepath.Join(m.CurrentDirectory, f.Name())
				}
			}
		case "backspace":
			parent := filepath.Dir(m.CurrentDirectory)
			if parent != m.CurrentDirectory {
				m.CurrentDirectory = parent
				m.cursorIdx = 0
				return m, m.readDir
			}
		}
	case readDirMsg:
		m.files = msg.entries
	}
	return m, nil
}

// View renders the file picker.
func (m Model) View() string {
	if len(m.files) == 0 {
		return m.Styles.EmptyDirectory.Render("(empty)")
	}

	var s string
	for i, f := range m.files {
		if i >= m.height {
			break
		}

		name := f.Name()
		if f.IsDir() {
			name += "/"
		}

		if i == m.cursorIdx {
			s += m.Styles.Cursor.Render(m.Cursor + " ")
			if f.IsDir() {
				s += m.Styles.Directory.Render(name)
			} else {
				s += m.Styles.File.Render(name)
			}
		} else {
			s += "  "
			if f.IsDir() {
				s += m.Styles.Directory.Render(name)
			} else {
				s += m.Styles.File.Render(name)
			}
		}
		s += "\n"
	}

	return s
}

// Selected returns the selected file path.
func (m Model) Selected() string {
	return m.selected
}

// CurrentDir returns the current directory.
func (m Model) CurrentDir() string {
	return m.CurrentDirectory
}

// SetSize sets the picker size.
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// SetStyles sets the styles.
func (m *Model) SetStyles(s Styles) {
	m.Styles = s
}

// SetHeight sets the picker height.
func (m *Model) SetHeight(h int) {
	m.height = h
}

// DidSelectFile returns true if a file was selected in this update.
func (m Model) DidSelectFile(msg tea.Msg) (bool, string) {
	if m.selected != "" {
		selected := m.selected
		// Note: clearing m.selected here would be ineffective since m is a value receiver
		// The caller should handle resetting the model state if needed
		return true, selected
	}
	return false, ""
}

// HighlightedPath returns the path of the currently highlighted item.
func (m Model) HighlightedPath() string {
	if m.cursorIdx >= 0 && m.cursorIdx < len(m.files) {
		return filepath.Join(m.CurrentDirectory, m.files[m.cursorIdx].Name())
	}
	return ""
}

type readDirMsg struct {
	entries []os.DirEntry
}

func (m Model) readDir() tea.Msg {
	entries, err := os.ReadDir(m.CurrentDirectory)
	if err != nil {
		return readDirMsg{entries: nil}
	}

	// Filter hidden files if needed
	if !m.showHidden {
		var filtered []os.DirEntry
		for _, e := range entries {
			if e.Name()[0] != '.' {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// Filter files if dirOnly
	if m.dirOnly {
		var filtered []os.DirEntry
		for _, e := range entries {
			if e.IsDir() {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// Sort: directories first, then files
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() && !entries[j].IsDir() {
			return true
		}
		if !entries[i].IsDir() && entries[j].IsDir() {
			return false
		}
		return entries[i].Name() < entries[j].Name()
	})

	return readDirMsg{entries: entries}
}
