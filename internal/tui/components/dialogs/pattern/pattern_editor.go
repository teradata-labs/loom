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
	"os"
	"path/filepath"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gopkg.in/yaml.v3"

	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
)

const (
	patternEditorDialogID dialogs.DialogID = "pattern-editor"
)

// PatternEditorDialog allows editing pattern files with YAML validation
type PatternEditorDialog interface {
	dialogs.DialogModel
}

type patternEditorDialogCmp struct {
	wWidth, wHeight int
	width, height   int

	filePath        string
	fileName        string
	originalContent string
	isDirty         bool
	saveError       string
	validationError string

	textarea textarea.Model
	keys     PatternEditorKeyMap
	help     help.Model

	positionRow int
	positionCol int

	confirmingClose bool
}

// PatternEditorKeyMap defines key bindings for pattern editor dialog
type PatternEditorKeyMap struct {
	Save  key.Binding
	Close key.Binding
}

// DefaultPatternEditorKeyMap returns default key bindings
func DefaultPatternEditorKeyMap() PatternEditorKeyMap {
	return PatternEditorKeyMap{
		Save: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "save"),
		),
		Close: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close"),
		),
	}
}

// ShortHelp returns key bindings for the short help view
func (k PatternEditorKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Save, k.Close}
}

// FullHelp returns key bindings for the full help view
func (k PatternEditorKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Save, k.Close},
	}
}

// SaveFileMsg is sent after successfully saving a file
type SaveFileMsg struct {
	FilePath string
}

// NewPatternEditorDialog creates a new pattern editor dialog
func NewPatternEditorDialog(filePath string) PatternEditorDialog {
	t := styles.CurrentTheme()
	h := help.New()
	h.Styles = t.S().Help

	// Read file content
	// #nosec G304 -- filePath comes from user selecting a pattern file in the sidebar
	content, err := os.ReadFile(filePath)
	if err != nil {
		content = []byte("# Error reading file: " + err.Error())
	}

	ta := textarea.New()
	ta.SetValue(string(content))
	ta.ShowLineNumbers = true
	ta.Prompt = ""

	return &patternEditorDialogCmp{
		filePath:        filePath,
		fileName:        filepath.Base(filePath),
		originalContent: string(content),
		textarea:        ta,
		keys:            DefaultPatternEditorKeyMap(),
		help:            h,
	}
}

func (m *patternEditorDialogCmp) ID() dialogs.DialogID {
	return patternEditorDialogID
}

func (m *patternEditorDialogCmp) Position() (int, int) {
	return m.positionRow, m.positionCol
}

func (m *patternEditorDialogCmp) Init() tea.Cmd {
	return textarea.Blink
}

func (m *patternEditorDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.wWidth, m.wHeight = msg.Width, msg.Height
		return m, m.resize()

	case tea.KeyPressMsg:
		// Handle confirmation dialog
		if m.confirmingClose {
			switch msg.String() {
			case "y", "Y":
				return m, util.CmdHandler(dialogs.CloseDialogMsg{})
			case "n", "N", "esc":
				m.confirmingClose = false
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Save):
			return m, m.saveFile()

		case key.Matches(msg, m.keys.Close):
			if m.isDirty {
				m.confirmingClose = true
				return m, nil
			}
			return m, util.CmdHandler(dialogs.CloseDialogMsg{})
		}
	}

	// Update dirty state
	oldValue := m.textarea.Value()
	m.textarea, cmd = m.textarea.Update(msg)
	newValue := m.textarea.Value()

	if oldValue != newValue {
		m.isDirty = newValue != m.originalContent
		m.validationError = "" // Clear validation error on edit
		m.saveError = ""       // Clear save error on edit
	}

	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m *patternEditorDialogCmp) resize() tea.Cmd {
	// Dialog should be 70% of screen width, 80% of height
	m.width = int(float64(m.wWidth) * 0.7)
	m.height = int(float64(m.wHeight) * 0.8)

	// Ensure minimum size
	if m.width < 60 {
		m.width = 60
	}
	if m.height < 20 {
		m.height = 20
	}

	// Account for border, padding, title, help, and status line
	contentWidth := m.width - 4   // 2 for border, 2 for padding
	contentHeight := m.height - 8 // 2 for border, 2 for padding, 2 for title/help, 2 for status

	// Set textarea size
	m.textarea.SetWidth(contentWidth)
	m.textarea.SetHeight(contentHeight)

	// Set position (centered)
	m.positionRow = m.wHeight/2 - m.height/2
	m.positionCol = m.wWidth/2 - m.width/2

	return nil
}

func (m *patternEditorDialogCmp) saveFile() tea.Cmd {
	content := m.textarea.Value()

	// Validate YAML syntax
	var yamlData interface{}
	if err := yaml.Unmarshal([]byte(content), &yamlData); err != nil {
		m.validationError = fmt.Sprintf("YAML validation failed: %s", err.Error())
		return nil
	}

	// Format YAML on save
	formattedYAML, err := yaml.Marshal(yamlData)
	if err != nil {
		m.saveError = fmt.Sprintf("Failed to format YAML: %s", err.Error())
		return nil
	}

	// Write to file
	if err := os.WriteFile(m.filePath, formattedYAML, 0600); err != nil {
		m.saveError = fmt.Sprintf("Failed to save file: %s", err.Error())
		return nil
	}

	// Update state
	m.originalContent = string(formattedYAML)
	m.textarea.SetValue(string(formattedYAML))
	m.isDirty = false
	m.saveError = ""
	m.validationError = ""

	// Return success message
	return util.CmdHandler(SaveFileMsg{FilePath: m.filePath})
}

func (m *patternEditorDialogCmp) View() string {
	t := styles.CurrentTheme()

	// Title with dirty indicator
	titleText := "Pattern Editor: " + m.fileName
	if m.isDirty {
		titleText += " •"
	}
	title := t.S().Base.
		Bold(true).
		Foreground(t.Primary).
		Render(titleText)

	// File path
	pathLabel := t.S().Base.Foreground(t.FgMuted).Render("File: ")
	pathValue := t.S().Base.Foreground(t.FgSubtle).Render(m.filePath)
	filePath := pathLabel + pathValue

	// Status line
	var statusLine string
	if m.confirmingClose {
		statusLine = t.S().Base.
			Foreground(t.Warning).
			Render("⚠ Unsaved changes. Close anyway? (y/n)")
	} else if m.saveError != "" {
		statusLine = t.S().Base.Foreground(t.Error).Render("✗ " + m.saveError)
	} else if m.validationError != "" {
		statusLine = t.S().Base.Foreground(t.Error).Render("✗ " + m.validationError)
	} else if m.isDirty {
		statusLine = t.S().Base.Foreground(t.Warning).Render("● Modified")
	} else {
		statusLine = t.S().Base.Foreground(t.Success).Render("✓ Saved")
	}

	// Content (textarea)
	content := m.textarea.View()

	// Help
	helpView := m.help.View(m.keys)

	// Assemble dialog
	inner := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		filePath,
		"",
		content,
		"",
		statusLine,
		helpView,
	)

	// Border
	borderColor := t.BorderFocus
	if m.validationError != "" || m.saveError != "" {
		borderColor = t.Error
	} else if m.isDirty {
		borderColor = t.Warning
	}

	style := t.S().Base.
		Width(m.width).
		Height(m.height).
		Padding(1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor)

	return style.Render(inner)
}

func (m *patternEditorDialogCmp) Cursor() *tea.Cursor {
	// Get cursor from textarea
	if m.confirmingClose {
		return nil // Hide cursor during confirmation
	}

	// Calculate cursor position relative to dialog
	row, col := m.Position()

	// Account for: border (1), padding (1), title (1), file path (1), empty line (1)
	// So content starts at row + 5
	contentStartRow := row + 5

	// Get textarea cursor position
	textareaCursor := m.textarea.Cursor()
	if textareaCursor != nil {
		x := col + 2 + textareaCursor.X // border (1) + padding (1) + textarea position
		y := contentStartRow + textareaCursor.Y
		return tea.NewCursor(x, y)
	}

	return nil
}
