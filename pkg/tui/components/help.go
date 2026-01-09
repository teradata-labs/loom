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
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// KeyBinding represents a keyboard shortcut.
type KeyBinding struct {
	Key  string
	Help string
}

// HelpView renders help text for key bindings.
type HelpView struct {
	styles   *styles.Styles
	bindings []KeyBinding
}

// NewHelpView creates a new help view.
func NewHelpView(s *styles.Styles, bindings []KeyBinding) *HelpView {
	return &HelpView{
		styles:   s,
		bindings: bindings,
	}
}

// Render renders the help view.
func (h *HelpView) Render(width int) string {
	if len(h.bindings) == 0 {
		return ""
	}

	var parts []string
	for _, b := range h.bindings {
		key := h.styles.HelpKey.Render(b.Key)
		help := h.styles.HelpValue.Render(b.Help)
		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Left, key, " ", help))
	}

	// Join with separators
	separator := h.styles.HelpValue.Render("•")
	joined := strings.Join(parts, " "+separator+" ")

	// Truncate if too long
	if lipgloss.Width(joined) > width {
		truncated := ""
		currentWidth := 0
		for _, part := range parts {
			partWidth := lipgloss.Width(part) + lipgloss.Width(separator) + 2
			if currentWidth+partWidth > width-3 {
				truncated += "..."
				break
			}
			if truncated != "" {
				truncated += " " + separator + " "
			}
			truncated += part
			currentWidth += partWidth
		}
		return truncated
	}

	return joined
}

// DefaultKeyBindings returns the default key bindings.
func DefaultKeyBindings() []KeyBinding {
	return []KeyBinding{
		{"ctrl+c", "quit"},
		{"ctrl+n", "new session"},
		{"ctrl+l", "clear"},
		{"ctrl+u", "page up"},
		{"ctrl+d", "page down"},
		{"pgup/pgdn", "scroll"},
	}
}
