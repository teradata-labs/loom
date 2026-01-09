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
package core

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// Section renders a section header with a line extending to the width.
// Adapted from Crush's core component.
func Section(text string, width int) string {
	t := styles.CurrentTheme()
	char := "─"
	length := lipgloss.Width(text) + 1
	remainingWidth := width - length
	lineStyle := lipgloss.NewStyle().Foreground(t.Border)
	if remainingWidth > 0 {
		text = text + " " + lineStyle.Render(strings.Repeat(char, remainingWidth))
	}
	return text
}

// StatusOpts contains options for rendering status items.
// Adapted from Crush's core component.
type StatusOpts struct {
	Icon             string // if empty no icon will be shown
	Title            string
	TitleColor       color.Color
	Description      string
	DescriptionColor color.Color
	ExtraContent     string // additional content to append after the description
}

// Status renders a status line with icon, title, description, and extra content.
// Adapted from Crush's core component.
func Status(opts StatusOpts, width int) string {
	t := styles.CurrentTheme()
	icon := opts.Icon
	title := opts.Title
	titleColor := t.FgMuted
	if opts.TitleColor != nil {
		titleColor = opts.TitleColor
	}
	description := opts.Description
	descriptionColor := t.FgSubtle
	if opts.DescriptionColor != nil {
		descriptionColor = opts.DescriptionColor
	}
	title = lipgloss.NewStyle().Foreground(titleColor).Render(title)
	if description != "" {
		extraContentWidth := lipgloss.Width(opts.ExtraContent)
		if extraContentWidth > 0 {
			extraContentWidth += 1
		}
		description = ansi.Truncate(description, width-lipgloss.Width(icon)-lipgloss.Width(title)-2-extraContentWidth, "…")
		description = lipgloss.NewStyle().Foreground(descriptionColor).Render(description)
	}

	content := []string{}
	if icon != "" {
		content = append(content, icon)
	}
	content = append(content, title)
	if description != "" {
		content = append(content, description)
	}
	if opts.ExtraContent != "" {
		content = append(content, opts.ExtraContent)
	}

	return strings.Join(content, " ")
}
