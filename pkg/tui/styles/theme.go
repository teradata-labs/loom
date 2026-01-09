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
package styles

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme defines the color scheme for the TUI.
type Theme struct {
	// Main colors
	Primary   color.Color
	Secondary color.Color
	Accent    color.Color

	// Status colors
	Success color.Color
	Warning color.Color
	Error   color.Color
	Info    color.Color

	// UI colors
	Background color.Color
	Foreground color.Color
	Border     color.Color
	BorderDim  color.Color

	// Text colors
	TextNormal color.Color
	TextDim    color.Color
	TextBright color.Color

	// Role colors (for chat)
	UserColor      color.Color
	AssistantColor color.Color
	ToolColor      color.Color
	SystemColor    color.Color
}

// DefaultTheme is the default Loom color scheme.
var DefaultTheme = Theme{
	// Main colors
	Primary:   lipgloss.Color("86"),  // Cyan
	Secondary: lipgloss.Color("212"), // Pink
	Accent:    lipgloss.Color("99"),  // Purple

	// Status colors
	Success: lipgloss.Color("42"),  // Green
	Warning: lipgloss.Color("214"), // Orange
	Error:   lipgloss.Color("196"), // Red
	Info:    lipgloss.Color("39"),  // Blue

	// UI colors
	Background: lipgloss.Color("235"), // Dark gray
	Foreground: lipgloss.Color("255"), // White
	Border:     lipgloss.Color("86"),  // Cyan
	BorderDim:  lipgloss.Color("240"), // Dim gray

	// Text colors
	TextNormal: lipgloss.Color("255"), // White
	TextDim:    lipgloss.Color("245"), // Gray
	TextBright: lipgloss.Color("231"), // Bright white

	// Role colors
	UserColor:      lipgloss.Color("39"),  // Blue
	AssistantColor: lipgloss.Color("212"), // Pink
	ToolColor:      lipgloss.Color("214"), // Orange
	SystemColor:    lipgloss.Color("245"), // Gray
}

// Styles contains all styled components.
type Styles struct {
	Theme Theme

	// App styles
	App       lipgloss.Style
	Header    lipgloss.Style
	Footer    lipgloss.Style
	StatusBar lipgloss.Style

	// Message styles
	MessageUser      lipgloss.Style
	MessageAssistant lipgloss.Style
	MessageTool      lipgloss.Style
	MessageSystem    lipgloss.Style

	// Input styles
	InputBox    lipgloss.Style
	InputPrompt lipgloss.Style

	// List styles
	ListItem         lipgloss.Style
	ListItemSelected lipgloss.Style

	// Info styles
	SessionInfo lipgloss.Style
	CostInfo    lipgloss.Style
	ErrorInfo   lipgloss.Style

	// Help styles
	HelpKey   lipgloss.Style
	HelpValue lipgloss.Style

	// Text styles
	TextDim lipgloss.Style
}

// NewStyles creates a new Styles with the given theme.
func NewStyles(theme Theme) *Styles {
	s := &Styles{
		Theme: theme,
	}

	// App styles
	s.App = lipgloss.NewStyle().
		Background(theme.Background).
		Foreground(theme.Foreground)

	s.Header = lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(theme.Border).
		BorderBottom(true).
		Padding(0, 1)

	s.Footer = lipgloss.NewStyle().
		Foreground(theme.TextDim).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(theme.BorderDim).
		BorderTop(true).
		Padding(0, 1)

	s.StatusBar = lipgloss.NewStyle().
		Foreground(theme.TextDim).
		Padding(0, 1)

	// Message styles
	s.MessageUser = lipgloss.NewStyle().
		Foreground(theme.UserColor).
		Bold(true)

	s.MessageAssistant = lipgloss.NewStyle().
		Foreground(theme.AssistantColor)

	s.MessageTool = lipgloss.NewStyle().
		Foreground(theme.ToolColor).
		Italic(true)

	s.MessageSystem = lipgloss.NewStyle().
		Foreground(theme.SystemColor).
		Italic(true)

	// Input styles
	s.InputBox = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(0, 1)

	s.InputPrompt = lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	// List styles
	s.ListItem = lipgloss.NewStyle().
		Padding(0, 2)

	s.ListItemSelected = lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Padding(0, 2)

	// Info styles
	s.SessionInfo = lipgloss.NewStyle().
		Foreground(theme.Info).
		Padding(0, 1)

	s.CostInfo = lipgloss.NewStyle().
		Foreground(theme.Warning).
		Padding(0, 1)

	s.ErrorInfo = lipgloss.NewStyle().
		Foreground(theme.Error).
		Bold(true).
		Padding(0, 1)

	// Help styles
	s.HelpKey = lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	s.HelpValue = lipgloss.NewStyle().
		Foreground(theme.TextDim)

	// Text styles
	s.TextDim = lipgloss.NewStyle().
		Foreground(theme.TextDim)

	return s
}

// DefaultStyles returns the default styles.
func DefaultStyles() *Styles {
	return NewStyles(DefaultTheme)
}

// FormatSessionID formats a session ID for display.
func (s *Styles) FormatSessionID(sessionID string) string {
	if len(sessionID) > 12 {
		return sessionID[:12] + "..."
	}
	return sessionID
}

// FormatCost formats cost information for display.
func (s *Styles) FormatCost(costUSD float64) string {
	if costUSD == 0 {
		return s.CostInfo.Render("Free")
	}
	if costUSD < 0.01 {
		return s.CostInfo.Render(lipgloss.JoinHorizontal(lipgloss.Left, "$", lipgloss.NewStyle().Render(fmt.Sprintf("%.6f", costUSD))))
	}
	return s.CostInfo.Render(lipgloss.JoinHorizontal(lipgloss.Left, "$", lipgloss.NewStyle().Render(fmt.Sprintf("%.4f", costUSD))))
}

// FormatError formats an error message for display.
func (s *Styles) FormatError(err error) string {
	return s.ErrorInfo.Render("✗ " + err.Error())
}

// FormatSuccess formats a success message for display.
func (s *Styles) FormatSuccess(msg string) string {
	return lipgloss.NewStyle().
		Foreground(s.Theme.Success).
		Render("✓ " + msg)
}

// Width returns the width needed for the given text with style.
func Width(s lipgloss.Style, text string) int {
	return lipgloss.Width(s.Render(text))
}
