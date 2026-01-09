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
// Package charmtone provides a color palette (stub replacement for github.com/charmbracelet/x/exp/charmtone).
package charmtone

// Color is a color type compatible with lipgloss.
// It implements color.Color interface for lipgloss v2 compatibility.
type Color string

// Hex returns the hex value of a color.
func (c Color) Hex() string {
	return string(c)
}

// String returns the string value.
func (c Color) String() string {
	return string(c)
}

// RGBA implements color.Color interface.
func (c Color) RGBA() (r, g, b, a uint32) {
	hex := string(c)
	if len(hex) == 0 {
		return 0, 0, 0, 0xFFFF
	}
	if hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return 0, 0, 0, 0xFFFF
	}

	r = hexToDec(hex[0:2]) * 0x101
	g = hexToDec(hex[2:4]) * 0x101
	b = hexToDec(hex[4:6]) * 0x101
	a = 0xFFFF
	return
}

func hexToDec(hex string) uint32 {
	var result uint32
	for _, c := range hex {
		result *= 16
		switch {
		case c >= '0' && c <= '9':
			result += uint32(c - '0')
		case c >= 'a' && c <= 'f':
			result += uint32(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			result += uint32(c - 'A' + 10)
		}
	}
	return result
}

// Color definitions matching charmtone palette.
var (
	// Primary colors
	Charple Color = "#7B5FC7" // Purple
	Dolly   Color = "#F5D76E" // Yellow
	Bok     Color = "#7FD1AE" // Mint
	Zest    Color = "#FF9F43" // Orange

	// Background colors
	Pepper   Color = "#1A1B26" // Dark base
	BBQ      Color = "#24283B" // Slightly lighter
	Charcoal Color = "#414868" // Subtle
	Iron     Color = "#565F89" // Overlay
	Zinc     Color = "#6B7089" // Mid-gray

	// Foreground colors
	Ash    Color = "#A9B1D6" // Base text
	Squid  Color = "#565F89" // Muted
	Smoke  Color = "#787C99" // Half-muted
	Oyster Color = "#9AA5CE" // Subtle
	Salt   Color = "#C0CAF5" // Selected

	// Status colors
	Guac     Color = "#9ECE6A" // Success/Green
	Sriracha Color = "#F7768E" // Error/Red
	Mustard  Color = "#E0AF68" // Warning/Yellow
	Malibu   Color = "#7AA2F7" // Info/Blue

	// Accent colors
	Butter  Color = "#FFEAA7" // White/cream
	Sardine Color = "#B4F9F8" // Light blue
	Damson  Color = "#BB9AF7" // Blue-purple
	Citron  Color = "#E0AF68" // Citrus
	Julep   Color = "#73DACA" // Mint green
	Coral   Color = "#FF9E64" // Coral
	Salmon  Color = "#F7768E" // Pink/salmon
	Cherry  Color = "#DB4B4B" // Deep red
	Bengal  Color = "#FF7A93" // Bright pink
	Cheeky  Color = "#E0AF68" // Peach
	Pony    Color = "#BB9AF7" // Lavender
	Guppy   Color = "#7DCFFF" // Sky blue
	Mauve   Color = "#C0A8E4" // Mauve
	Hazy    Color = "#A9B1D6" // Hazy
	Cumin   Color = "#D8A657" // Cumin

	// Teradata brand colors
	TeradataOrange Color = "#F37440" // Teradata primary orange
	TeradataCyan   Color = "#00D4AA" // Vibrant cyan that pops with orange
)
