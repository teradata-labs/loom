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
	"image/color"

	"charm.land/lipgloss/v2"
)

// EnhancedTheme extends the base theme with additional visual options
// inspired by Charm Bracelet's Crush TUI.
type EnhancedTheme struct {
	Theme

	// Additional background shades
	BgBaseLighter color.Color
	BgSubtle      color.Color
	BgOverlay     color.Color

	// Additional foreground shades
	FgHalfMuted color.Color
	FgSelected  color.Color

	// Extended palette
	Blues   []color.Color
	Greens  []color.Color
	Reds    []color.Color
	Yellows []color.Color
	Grays   []color.Color
}

// EnhancedDefaultTheme returns an enhanced version of the default theme
// with additional color options.
func EnhancedDefaultTheme() EnhancedTheme {
	base := DefaultTheme

	return EnhancedTheme{
		Theme: base,

		// Background shades
		BgBaseLighter: lipgloss.Color("236"), // Slightly lighter than base
		BgSubtle:      lipgloss.Color("237"), // Subtle elevation
		BgOverlay:     lipgloss.Color("238"), // Overlay/modal backgrounds

		// Foreground shades
		FgHalfMuted: lipgloss.Color("250"), // Between normal and dim
		FgSelected:  lipgloss.Color("231"), // Bright white for selected items

		// Extended palette for gradients
		Blues: []color.Color{
			lipgloss.Color("24"), // Dark blue
			lipgloss.Color("39"), // Medium blue
			lipgloss.Color("45"), // Light blue
			lipgloss.Color("51"), // Bright blue
		},
		Greens: []color.Color{
			lipgloss.Color("28"), // Dark green
			lipgloss.Color("34"), // Medium green
			lipgloss.Color("42"), // Light green
			lipgloss.Color("46"), // Bright green
		},
		Reds: []color.Color{
			lipgloss.Color("88"),  // Dark red
			lipgloss.Color("160"), // Medium red
			lipgloss.Color("196"), // Light red
			lipgloss.Color("9"),   // Bright red
		},
		Yellows: []color.Color{
			lipgloss.Color("136"), // Dark yellow
			lipgloss.Color("178"), // Medium yellow
			lipgloss.Color("214"), // Light yellow/orange
			lipgloss.Color("226"), // Bright yellow
		},
		Grays: []color.Color{
			lipgloss.Color("235"), // Darkest gray
			lipgloss.Color("240"), // Dark gray
			lipgloss.Color("245"), // Medium gray
			lipgloss.Color("250"), // Light gray
			lipgloss.Color("255"), // White
		},
	}
}

// CompactStyles returns styles optimized for compact mode
// (reduced padding and spacing).
type CompactStyles struct {
	*Styles
	IsCompact bool
}

// NewCompactStyles creates styles for compact mode.
func NewCompactStyles(theme Theme) *CompactStyles {
	baseStyles := NewStyles(theme)

	// Reduce padding for compact mode
	baseStyles.Header = baseStyles.Header.Padding(0, 1)
	baseStyles.Footer = baseStyles.Footer.Padding(0, 1)
	baseStyles.StatusBar = baseStyles.StatusBar.Padding(0)
	baseStyles.InputBox = baseStyles.InputBox.Padding(0, 1)

	return &CompactStyles{
		Styles:    baseStyles,
		IsCompact: true,
	}
}
