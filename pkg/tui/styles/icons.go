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

// Icons contains visual symbols for terminal UI elements.
// Inspired by Charm Bracelet's Crush TUI.
type Icons struct {
	// Status & Feedback
	Check   string
	Error   string
	Warning string
	Info    string
	Hint    string

	// Loading & Animation
	Spinner       string
	CenterSpinner string
	Loading       string

	// Navigation & Content
	ArrowRight string
	Document   string
	Model      string

	// Tool Operations
	Pending string
	Success string
	Failed  string

	// Borders & Containers
	BorderThin  string
	BorderThick string

	// Task Management
	TodoCompleted string
	TodoPending   string

	// Model switching
	ModelIcon  string
	SwitchIcon string

	// Permission
	PermissionIcon string
	ApproveIcon    string
	DenyIcon       string

	// Sidebar
	WarningIcon string
	PlayIcon    string
}

// DefaultIcons returns the default icon set.
func DefaultIcons() *Icons {
	return &Icons{
		// Status & Feedback
		Check:   "✓",
		Error:   "×",
		Warning: "⚠",
		Info:    "ⓘ",
		Hint:    "∵",

		// Loading & Animation
		Spinner:       "...",
		CenterSpinner: "⋯",
		Loading:       "⟳",

		// Navigation & Content
		ArrowRight: "→",
		Document:   "⎘",
		Model:      "◇",

		// Tool Operations
		Pending: "●",
		Success: "✓",
		Failed:  "×",

		// Borders & Containers
		BorderThin:  "│",
		BorderThick: "▌",

		// Task Management
		TodoCompleted: "✓",
		TodoPending:   "•",

		// Model switching
		ModelIcon:  "⬡",
		SwitchIcon: "⇄",

		// Permission
		PermissionIcon: "⚿",
		ApproveIcon:    "✓",
		DenyIcon:       "×",

		// Sidebar
		WarningIcon: "⚠",
		PlayIcon:    "▶",
	}
}
