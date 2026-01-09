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

// CrushTheme implements a Crush-inspired color scheme.
// Based on Charm Bracelet's Crush TUI visual design.
type CrushTheme struct {
	// Primary interaction colors
	Primary   color.Color
	Secondary color.Color
	Tertiary  color.Color
	Accent    color.Color

	// Background palette
	BgBase        color.Color
	BgBaseLighter color.Color
	BgSubtle      color.Color
	BgOverlay     color.Color

	// Foreground palette
	FgBase      color.Color
	FgMuted     color.Color
	FgHalfMuted color.Color
	FgSubtle    color.Color
	FgSelected  color.Color

	// Status colors
	Success color.Color
	Error   color.Color
	Warning color.Color
	Info    color.Color

	// Semantic colors for tools and operations
	GreenDark  color.Color
	GreenLight color.Color
	RedDark    color.Color
	RedLight   color.Color
	Blue       color.Color
	Purple     color.Color
	Orange     color.Color

	// Special colors
	White  color.Color
	Cherry color.Color

	// Border colors
	Border      color.Color
	BorderFocus color.Color

	// Agent-specific colors (for multi-agent conversations)
	Agent1Color color.Color // Primary agent (purple)
	Agent2Color color.Color // Secondary agent (cyan)
	Agent3Color color.Color // Tertiary agent (green)
	Agent4Color color.Color // Quaternary agent (orange)
	Agent5Color color.Color // Quinary agent (pink)
	Agent6Color color.Color // Senary agent (yellow)
}

// GetAgentColor returns a distinct color for the given agent index (0-based).
// Uses predefined colors for first 6 agents, then generates distinct colors
// using HSL color space for unlimited agents.
func (t *CrushTheme) GetAgentColor(agentIndex int) color.Color {
	// Predefined colors for first 6 agents (carefully chosen for distinctiveness)
	predefined := []color.Color{
		t.Agent1Color, // Purple
		t.Agent2Color, // Cyan
		t.Agent3Color, // Green
		t.Agent4Color, // Orange
		t.Agent5Color, // Pink
		t.Agent6Color, // Yellow
	}

	if agentIndex < len(predefined) {
		return predefined[agentIndex]
	}

	// For agents beyond the first 6, generate distinct colors
	// using golden ratio distribution in HSL color space
	return GenerateDistinctColor(agentIndex)
}

// GenerateDistinctColor generates a visually distinct color for the given index.
// Uses golden ratio (φ ≈ 1.618) to distribute hues evenly in HSL color space.
// This ensures maximum visual distinction even with many agents.
func GenerateDistinctColor(index int) color.Color {
	// Golden ratio for hue distribution
	goldenRatio := 0.618033988749895

	// Calculate hue (0-360 degrees)
	// Start at offset to avoid predefined color ranges
	hue := (float64(index-6)*goldenRatio + 0.3) // Offset by 0.3 to avoid purple start
	hue = hue - float64(int(hue))               // Keep fractional part only
	hue = hue * 360                             // Convert to degrees

	// Fixed saturation and lightness for terminal visibility
	saturation := 70.0 // Vibrant but not overwhelming
	lightness := 60.0  // Bright enough for dark terminals

	// Convert HSL to 256-color palette approximation
	// This maps to the nearest terminal color
	return hslToTerminalColor(hue, saturation, lightness)
}

// hslToTerminalColor approximates HSL color to 256-color terminal palette.
func hslToTerminalColor(h, s, l float64) color.Color {
	// Map hue ranges to terminal color codes
	// This is a rough approximation for 256-color terminals
	hueSegment := int(h / 30) // 12 segments (360/30)

	// Terminal colors 16-231 are RGB colors in 6x6x6 cube
	// We'll use the outer colors (more saturated) for distinction
	colorMap := []int{
		196, // Red (0-30°)
		208, // Orange (30-60°)
		226, // Yellow (60-90°)
		118, // Yellow-green (90-120°)
		46,  // Green (120-150°)
		49,  // Cyan-green (150-180°)
		51,  // Cyan (180-210°)
		39,  // Sky blue (210-240°)
		27,  // Blue (240-270°)
		93,  // Purple (270-300°)
		201, // Magenta (300-330°)
		198, // Pink (330-360°)
	}

	return lipgloss.Color(fmt.Sprintf("%d", colorMap[hueSegment%len(colorMap)]))
}

// NewCrushTheme creates a Crush-inspired theme.
func NewCrushTheme() CrushTheme {
	return CrushTheme{
		// Primary colors - vibrant purples and blues
		Primary:   lipgloss.Color("141"), // Bright purple
		Secondary: lipgloss.Color("135"), // Medium purple
		Tertiary:  lipgloss.Color("99"),  // Violet
		Accent:    lipgloss.Color("213"), // Pink

		// Background palette - dark grays
		BgBase:        lipgloss.Color("235"), // Very dark gray
		BgBaseLighter: lipgloss.Color("236"), // Slightly lighter
		BgSubtle:      lipgloss.Color("237"), // Subtle elevation
		BgOverlay:     lipgloss.Color("238"), // Modal/dialog background

		// Foreground palette - progressive brightness
		FgBase:      lipgloss.Color("252"), // Bright foreground
		FgMuted:     lipgloss.Color("245"), // Muted text
		FgHalfMuted: lipgloss.Color("248"), // Between muted and normal
		FgSubtle:    lipgloss.Color("242"), // Very subtle text
		FgSelected:  lipgloss.Color("231"), // Bright white for selection

		// Status colors
		Success: lipgloss.Color("42"),  // Bright green
		Error:   lipgloss.Color("196"), // Bright red
		Warning: lipgloss.Color("214"), // Orange
		Info:    lipgloss.Color("39"),  // Blue

		// Semantic operation colors
		GreenDark:  lipgloss.Color("28"),  // Dark green for pending
		GreenLight: lipgloss.Color("42"),  // Bright green for success
		RedDark:    lipgloss.Color("88"),  // Dark red for errors
		RedLight:   lipgloss.Color("196"), // Bright red for failures
		Blue:       lipgloss.Color("39"),  // Info blue
		Purple:     lipgloss.Color("141"), // Primary purple
		Orange:     lipgloss.Color("214"), // Warning orange

		// Special colors
		White:  lipgloss.Color("255"), // Pure white
		Cherry: lipgloss.Color("161"), // Cherry red accent

		// Border colors
		Border:      lipgloss.Color("238"), // Subtle border
		BorderFocus: lipgloss.Color("141"), // Purple border when focused

		// Agent-specific colors for multi-agent support
		Agent1Color: lipgloss.Color("141"), // Purple (primary)
		Agent2Color: lipgloss.Color("51"),  // Cyan
		Agent3Color: lipgloss.Color("42"),  // Green
		Agent4Color: lipgloss.Color("214"), // Orange
		Agent5Color: lipgloss.Color("213"), // Pink
		Agent6Color: lipgloss.Color("226"), // Yellow
	}
}

// CrushStyles creates Crush-styled components.
type CrushStyles struct {
	Theme CrushTheme

	// Base styles
	Base     lipgloss.Style
	Title    lipgloss.Style
	Subtitle lipgloss.Style
	Muted    lipgloss.Style
	Subtle   lipgloss.Style

	// Message styles
	MessageHeader       lipgloss.Style
	MessageUser         lipgloss.Style
	MessageAssistant    lipgloss.Style
	MessageTool         lipgloss.Style
	MessageSystem       lipgloss.Style
	MessageSeparator    lipgloss.Style
	MessageBorder       lipgloss.Style
	MessageContentBlock lipgloss.Style

	// Input styles
	InputFocused lipgloss.Style
	InputBlurred lipgloss.Style
	InputPrompt  lipgloss.Style

	// Status styles
	StatusBar       lipgloss.Style
	ToolPending     lipgloss.Style
	ToolSuccess     lipgloss.Style
	ToolError       lipgloss.Style
	ToolCancelled   lipgloss.Style
	ProgressSpinner lipgloss.Style

	// Layout styles
	PageContainer lipgloss.Style
	DialogOverlay lipgloss.Style
	HelpText      lipgloss.Style
}

// NewCrushStyles creates a complete Crush-styled UI system.
func NewCrushStyles() *CrushStyles {
	theme := NewCrushTheme()

	return &CrushStyles{
		Theme: theme,

		// Base styles
		Base: lipgloss.NewStyle().
			Foreground(theme.FgBase).
			Background(theme.BgBase),

		Title: lipgloss.NewStyle().
			Foreground(theme.Accent).
			Bold(true),

		Subtitle: lipgloss.NewStyle().
			Foreground(theme.Primary),

		Muted: lipgloss.NewStyle().
			Foreground(theme.FgMuted),

		Subtle: lipgloss.NewStyle().
			Foreground(theme.FgSubtle),

		// Message styles with Crush-like appearance
		MessageHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Primary),

		MessageUser: lipgloss.NewStyle().
			Foreground(theme.Blue).
			Bold(true),

		MessageAssistant: lipgloss.NewStyle().
			Foreground(theme.FgBase),

		MessageTool: lipgloss.NewStyle().
			Foreground(theme.Orange),

		MessageSystem: lipgloss.NewStyle().
			Foreground(theme.FgMuted).
			Italic(true),

		MessageSeparator: lipgloss.NewStyle().
			Foreground(theme.FgSubtle).
			Background(theme.BgSubtle),

		MessageBorder: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(theme.FgSubtle).
			Padding(0, 1),

		MessageContentBlock: lipgloss.NewStyle().
			Background(theme.BgSubtle).
			Padding(1, 2).
			MarginTop(1).
			MarginBottom(1),

		// Input styles
		InputFocused: lipgloss.NewStyle().
			Foreground(theme.FgBase).
			Background(theme.BgBase).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(theme.Primary).
			Padding(0, 1),

		InputBlurred: lipgloss.NewStyle().
			Foreground(theme.FgMuted).
			Background(theme.BgBase).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(theme.FgSubtle).
			Padding(0, 1),

		InputPrompt: lipgloss.NewStyle().
			Foreground(theme.Primary).
			Bold(true),

		// Status styles
		StatusBar: lipgloss.NewStyle().
			Foreground(theme.FgMuted).
			Background(theme.BgBase).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(theme.FgSubtle).
			BorderTop(true).
			Padding(0, 1),

		ToolPending: lipgloss.NewStyle().
			Foreground(theme.GreenDark),

		ToolSuccess: lipgloss.NewStyle().
			Foreground(theme.GreenLight),

		ToolError: lipgloss.NewStyle().
			Foreground(theme.RedLight),

		ToolCancelled: lipgloss.NewStyle().
			Foreground(theme.FgMuted),

		ProgressSpinner: lipgloss.NewStyle().
			Foreground(theme.Primary),

		// Layout styles
		PageContainer: lipgloss.NewStyle().
			Padding(1, 1, 0, 1),

		DialogOverlay: lipgloss.NewStyle().
			Background(theme.BgOverlay).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Primary).
			Padding(1, 2),

		HelpText: lipgloss.NewStyle().
			Foreground(theme.FgMuted),
	}
}

// Global default theme instance
var defaultTheme *CrushTheme

// CurrentTheme returns the current global theme.
// This provides compatibility with Crush-style components that expect styles.CurrentTheme().
func CurrentTheme() *CrushTheme {
	if defaultTheme == nil {
		theme := NewCrushTheme()
		defaultTheme = &theme
	}
	return defaultTheme
}
