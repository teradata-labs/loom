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
	"math/rand"
	"strings"
	"time"

	"image/color"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Logo is an animated ASCII logo with star field
type Logo struct {
	width   int
	height  int
	stars   []star
	frame   int
	version string
}

type star struct {
	x          int
	y          int
	char       rune
	brightness int // 0-2 (dim to bright)
	speed      int // twinkle speed
}

type tickMsg time.Time

// NewLogo creates a new animated logo
func NewLogo(version string) Logo {
	l := Logo{
		width:   65,
		height:  11,
		version: version,
	}
	l.initStars()
	return l
}

func (l *Logo) initStars() {
	// Create star field around the logo
	numStars := 45
	l.stars = make([]star, numStars)

	// Note: Using math/rand for UI animations - crypto/rand not needed for visual effects
	for i := range l.stars {
		l.stars[i] = star{
			x:          rand.Intn(l.width),  // #nosec G404 -- UI animation, not security-sensitive
			y:          rand.Intn(l.height), // #nosec G404 -- UI animation, not security-sensitive
			char:       l.randomStarChar(),
			brightness: rand.Intn(3),     // #nosec G404 -- UI animation, not security-sensitive
			speed:      rand.Intn(3) + 1, // #nosec G404 -- UI animation, not security-sensitive
		}
	}
}

func (l *Logo) randomStarChar() rune {
	chars := []rune{'*', '·', '.'}
	// #nosec G404 -- UI animation, not security-sensitive
	return chars[rand.Intn(len(chars))]
}

// Init initializes the logo
func (l Logo) Init() tea.Cmd {
	return l.tick()
}

// Update handles messages
func (l Logo) Update(msg tea.Msg) (Logo, tea.Cmd) {
	switch msg.(type) {
	case tickMsg:
		l.frame++
		l.updateStars()
		return l, l.tick()
	}
	return l, nil
}

func (l *Logo) updateStars() {
	for i := range l.stars {
		// Update brightness (twinkling effect)
		if l.frame%l.stars[i].speed == 0 {
			l.stars[i].brightness = (l.stars[i].brightness + 1) % 3

			// Occasionally change star character
			// #nosec G404 -- UI animation, not security-sensitive
			if rand.Float32() < 0.1 {
				l.stars[i].char = l.randomStarChar()
			}
		}
	}
}

func (l Logo) tick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// View renders the logo
func (l Logo) View() string {
	logoLines := l.getLogoLines()
	startY := 3
	startX := 18

	var lines []string
	for y := 0; y < l.height; y++ {
		var parts []string

		for x := 0; x < l.width; x++ {
			// Check if this is a logo position
			logoLineIdx := y - startY
			logoCharIdx := x - startX

			if logoLineIdx >= 0 && logoLineIdx < len(logoLines) &&
				logoCharIdx >= 0 && logoCharIdx < len(logoLines[logoLineIdx]) {
				// This position has logo text - we'll handle it separately
				continue
			}

			// Check if there's a star here
			s := l.getStarAt(x, y)
			if s != nil {
				parts = append(parts, l.colorStar(string(s.char), s.brightness))
			} else {
				parts = append(parts, " ")
			}
		}

		// Build the line
		line := strings.Join(parts, "")

		// Insert logo text if this is a logo line
		logoLineIdx := y - startY
		if logoLineIdx >= 0 && logoLineIdx < len(logoLines) {
			// Build line with logo text inserted
			before := ""
			for x := 0; x < startX; x++ {
				s := l.getStarAt(x, y)
				if s != nil {
					before += l.colorStar(string(s.char), s.brightness)
				} else {
					before += " "
				}
			}

			logoText := l.colorLogo(logoLines[logoLineIdx], logoLineIdx)

			after := ""
			afterStart := startX + len(logoLines[logoLineIdx])
			for x := afterStart; x < l.width; x++ {
				s := l.getStarAt(x, y)
				if s != nil {
					after += l.colorStar(string(s.char), s.brightness)
				} else {
					after += " "
				}
			}

			line = before + logoText + after
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (l Logo) getStarAt(x, y int) *star {
	for i := range l.stars {
		if l.stars[i].x == x && l.stars[i].y == y {
			return &l.stars[i]
		}
	}
	return nil
}

func (l Logo) colorStar(s string, brightness int) string {
	colors := []color.Color{
		lipgloss.Color("240"), // Dim
		lipgloss.Color("245"), // Medium
		lipgloss.Color("255"), // Bright
	}
	return lipgloss.NewStyle().Foreground(colors[brightness]).Render(s)
}

func (l Logo) colorLogo(line string, lineNum int) string {
	cyan := lipgloss.Color("86")
	pink := lipgloss.Color("212")
	purple := lipgloss.Color("99")

	// Color the main logo (lines 0-2)
	if lineNum <= 2 {
		return lipgloss.NewStyle().Foreground(cyan).Bold(true).Render(line)
	}
	// Separator line
	if lineNum == 3 {
		return lipgloss.NewStyle().Foreground(purple).Render(line)
	}
	// Tagline
	if lineNum == 4 {
		return lipgloss.NewStyle().Foreground(pink).Render(line)
	}
	return line
}

func (l Logo) getLogoLines() []string {
	return []string{
		"╦  ╔═╗ ╔═╗ ╔╦╗",
		"║  ║ ║ ║ ║ ║║║",
		"╩═╝╚═╝ ╚═╝ ╩ ╩  " + l.version,
		"═════════════════════",
		"Pattern-Guided Agents",
	}
}

// Static returns a static (non-animated) version of the logo
func Static(version string) string {
	return `   *  .    ·    *    .   ·       *   .    ·    *    .   ·
      ·       *         ·    .         *       ·        .
  .      *        ·                        .       *
                  ╦  ╔═╗ ╔═╗ ╔╦╗                      ·
       *          ║  ║ ║ ║ ║ ║║║               ·      *
    ·             ╩═╝╚═╝ ╚═╝ ╩ ╩  ` + version + `            .
                  ═════════════════════         ·
       .          Pattern-Guided Agents      *          ·
                        *         ·
     ·      *       .        *         ·         *        .
   *  .    ·    *    .   ·       *   .    ·    *    .   ·   `
}

// StaticColored returns a colored static version (for terminal output)
func StaticColored(version string) string {
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	pink := lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	purple := lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	stars := dim.Render(`   *  .    ·    *    .   ·       *   .    ·    *    .   ·
      ·       *         ·    .         *       ·        .
  .      *        ·                        .       *        `)

	logo := cyan.Render(`                  ╦  ╔═╗ ╔═╗ ╔╦╗`) + dim.Render(`                      ·`) + "\n" +
		cyan.Render(`       *          ║  ║ ║ ║ ║ ║║║`) + dim.Render(`               ·      *`) + "\n" +
		dim.Render(`    ·             `) + cyan.Render(`╩═╝╚═╝ ╚═╝ ╩ ╩  `+version) + dim.Render(`            .`) + "\n" +
		dim.Render(`                  `) + purple.Render(`═════════════════════`) + dim.Render(`         ·`) + "\n" +
		dim.Render(`       .          `) + pink.Render(`Pattern-Guided Agents`) + dim.Render(`      *          ·`) + "\n" +
		dim.Render(`                        *         ·                          `)

	starsBottom := dim.Render(`     ·      *       .        *         ·         *        .
   *  .    ·    *    .   ·       *   .    ·    *    .   ·   `)

	return stars + "\n" + logo + "\n" + starsBottom
}
