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
// Package logo renders a Loom wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/charmbracelet/x/ansi"
	"github.com/teradata-labs/loom/internal/charmtone"
	"github.com/teradata-labs/loom/internal/slice"
	"github.com/teradata-labs/loom/internal/tui/styles"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

const diag = `╱`

// Opts are the options for rendering the Loom title art.
type Opts struct {
	FieldColor    color.Color // diagonal lines
	TitleColorA   color.Color // left gradient ramp point
	TitleColorB   color.Color // right gradient ramp point
	TeradataColor color.Color // Teradata™ text color
	VersionColor  color.Color // Version text color
	Width         int         // width of the rendered logo, used for truncation
}

// Render renders the Loom logo. Set the argument to true to render the narrow
// version, intended for use in a sidebar.
//
// The compact argument determines whether it renders compact for the sidebar
// or wider for the main pane.
func Render(version string, compact bool, o Opts) string {
	const teradata = " Teradata™"

	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// Title.
	const spacing = 1
	letterforms := []letterform{
		letterL,
		letterO,
		letterO,
		letterM,
	}
	stretchIndex := -1 // -1 means no stretching.
	if !compact {
		stretchIndex = 0 // Always stretch the L in LOOM for the wide banner
	}

	loom := renderWord(spacing, stretchIndex, letterforms...)
	loomWidth := lipgloss.Width(loom)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(loom, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(r, o.TitleColorA, o.TitleColorB))
	}
	loom = b.String()

	// Teradata and version.
	metaRowGap := 1
	maxVersionWidth := loomWidth - lipgloss.Width(teradata) - metaRowGap
	version = ansi.Truncate(version, maxVersionWidth, "…") // truncate version if too long.
	gap := max(0, loomWidth-lipgloss.Width(teradata)-lipgloss.Width(version))
	metaRow := fg(o.TeradataColor, teradata) + strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	// Join the meta row and big Loom title.
	loom = strings.TrimSpace(metaRow + "\n" + loom)

	// Narrow version.
	if compact {
		field := fg(o.FieldColor, strings.Repeat(diag, loomWidth))
		return strings.Join([]string{field, field, loom, field, ""}, "\n")
	}

	fieldHeight := lipgloss.Height(loom)

	// Left field.
	const leftWidth = 6
	leftFieldRow := fg(o.FieldColor, strings.Repeat(diag, leftWidth))
	leftField := new(strings.Builder)
	for range fieldHeight {
		fmt.Fprintln(leftField, leftFieldRow)
	}

	// Right field.
	rightWidth := max(15, o.Width-loomWidth-leftWidth-2) // 2 for the gap.
	const stepDownAt = 0
	rightField := new(strings.Builder)
	for i := range fieldHeight {
		width := rightWidth
		if i >= stepDownAt {
			width = rightWidth - (i - stepDownAt)
		}
		fmt.Fprint(rightField, fg(o.FieldColor, strings.Repeat(diag, width)), "\n")
	}

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftField.String(), hGap, loom, hGap, rightField.String())
	if o.Width > 0 {
		// Truncate the logo to the specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// SmallRender renders a smaller version of the Loom logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(width int) string {
	t := styles.CurrentTheme()
	title := t.S().Base.Foreground(charmtone.TeradataOrange).Render("Teradata™")
	title = fmt.Sprintf("%s %s", title, styles.ApplyBoldForegroundGrad("Loom", charmtone.TeradataCyan, charmtone.TeradataOrange))
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after "Loom"
	if remainingWidth > 0 {
		lines := strings.Repeat("╱", remainingWidth)
		title = fmt.Sprintf("%s %s", title, t.S().Base.Foreground(charmtone.TeradataOrange).Render(lines))
	}
	return title
}

// renderWord renders letterforms to fork a word. stretchIndex is the index of
// the letter to stretch, or -1 if no letter should be stretched.
func renderWord(spacing int, stretchIndex int, letterforms ...letterform) string {
	if spacing < 0 {
		spacing = 0
	}

	renderedLetterforms := make([]string, len(letterforms))

	// pick one letter randomly to stretch
	for i, letter := range letterforms {
		renderedLetterforms[i] = letter(i == stretchIndex)
	}

	if spacing > 0 {
		// Add spaces between the letters and render.
		renderedLetterforms = slice.Intersperse(renderedLetterforms, strings.Repeat(" ", spacing))
	}
	return strings.TrimSpace(
		lipgloss.JoinHorizontal(lipgloss.Top, renderedLetterforms...),
	)
}

// letterL renders the letter L in a stylized way. It takes a boolean that
// determines whether to stretch the letter horizontally.
func letterL(stretch bool) string {
	// Here's what we're making:
	//
	// █
	// █
	// ▀▀▀▀▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	bottom := heredoc.Doc(`


		▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(bottom, letterformProps{
			stretch:    stretch,
			width:      4,
			minStretch: 8,
			maxStretch: 15,
		}),
	)
}

// letterO renders the letter O in a stylized way. It takes a boolean that
// determines whether to stretch the letter horizontally.
func letterO(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▄
	// █   █
	//  ▀▀▀

	side := heredoc.Doc(`
		▄
		█

	`)
	middle := heredoc.Doc(`
		▀

		▀
	`)
	rightSide := heredoc.Doc(`
		▄
		█

	`)
	return joinLetterform(
		side,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 6,
			maxStretch: 10,
		}),
		rightSide,
	)
}

// letterM renders the letter M in a stylized way. It takes a boolean that
// determines whether to stretch the letter horizontally.
func letterM(stretch bool) string {
	// Here's what we're making:
	//
	// █▄ ▄█
	// █ ▀ █
	// ▀   ▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	leftInner := heredoc.Doc(`
		▄

	`)
	middle := heredoc.Doc(`

		▀
	`)
	rightInner := heredoc.Doc(`
		▄

	`)
	right := heredoc.Doc(`
		█
		█
		▀
	`)
	return joinLetterform(
		left,
		leftInner,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      1,
			minStretch: 3,
			maxStretch: 6,
		}),
		rightInner,
		right,
	)
}

func joinLetterform(letters ...string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, letters...)
}

// letterformProps defines letterform stretching properties.
// for readability.
type letterformProps struct {
	width      int
	minStretch int
	maxStretch int
	stretch    bool
}

// stretchLetterformPart is a helper function for letter stretching. If randomize
// is false the minimum number will be used.
func stretchLetterformPart(s string, p letterformProps) string {
	if p.maxStretch < p.minStretch {
		p.minStretch, p.maxStretch = p.maxStretch, p.minStretch
	}
	n := p.width
	if p.stretch {
		n = cachedRandN(p.maxStretch-p.minStretch) + p.minStretch //nolint:gosec
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}
