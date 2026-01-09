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
package agents

import (
	"fmt"
	"image/color"
	"sort"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/teradata-labs/loom/pkg/tui/components/core"
	"github.com/teradata-labs/loom/pkg/tui/styles"
)

// AgentInfo represents an agent with status information.
// Adapted from Crush's SessionFile pattern.
type AgentInfo struct {
	ID         string
	Name       string
	Status     string // "active", "idle", "error"
	Color      color.Color
	LastActive int64 // Unix timestamp
}

// RenderOptions contains options for rendering agent lists.
// Adapted from Crush's RenderOptions pattern.
type RenderOptions struct {
	MaxWidth    int
	MaxItems    int
	ShowSection bool
	SectionName string
}

// RenderAgentList renders a list of agents with the given options.
// Adapted from Crush's RenderFileList pattern.
func RenderAgentList(agents []AgentInfo, opts RenderOptions) []string {
	t := styles.CurrentTheme()
	agentList := []string{}

	if opts.ShowSection {
		sectionName := opts.SectionName
		if sectionName == "" {
			sectionName = "Agents"
		}
		section := lipgloss.NewStyle().Foreground(t.FgSubtle).Render(sectionName)
		agentList = append(agentList, section)
	}

	if len(agents) == 0 {
		agentList = append(agentList, lipgloss.NewStyle().Foreground(t.Border).Render("None"))
		return agentList
	}

	// Sort agents by last active time (most recent first)
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].LastActive > agents[j].LastActive
	})

	// Determine how many items to show
	maxItems := len(agents)
	if opts.MaxItems > 0 {
		maxItems = min(opts.MaxItems, len(agents))
	}

	for i := 0; i < maxItems; i++ {
		agent := agents[i]

		// Status icon
		var icon string
		var iconColor color.Color
		switch agent.Status {
		case "active":
			icon = "▶"
			iconColor = t.Success
		case "idle":
			icon = "○"
			iconColor = t.FgSubtle
		case "error":
			icon = "✗"
			iconColor = t.Error
		default:
			icon = "○"
			iconColor = t.FgSubtle
		}

		styledIcon := lipgloss.NewStyle().Foreground(iconColor).Render(icon)

		// Agent name with color
		agentName := agent.Name
		if agentName == "" {
			agentName = agent.ID
		}
		agentName = ansi.Truncate(agentName, opts.MaxWidth-4, "…")

		agentList = append(agentList,
			core.Status(
				core.StatusOpts{
					Icon:       styledIcon,
					Title:      agentName,
					TitleColor: agent.Color,
				},
				opts.MaxWidth,
			),
		)
	}

	return agentList
}

// RenderAgentBlock renders a complete agent block with optional truncation indicator.
// Adapted from Crush's RenderFileBlock pattern.
func RenderAgentBlock(agents []AgentInfo, opts RenderOptions, showTruncationIndicator bool) string {
	t := styles.CurrentTheme()
	agentList := RenderAgentList(agents, opts)

	// Add truncation indicator if needed
	if showTruncationIndicator && opts.MaxItems > 0 && len(agents) > opts.MaxItems {
		remaining := len(agents) - opts.MaxItems
		if remaining == 1 {
			agentList = append(agentList, lipgloss.NewStyle().Foreground(t.FgMuted).Render("…"))
		} else {
			agentList = append(agentList,
				lipgloss.NewStyle().Foreground(t.FgSubtle).Render(fmt.Sprintf("…and %d more", remaining)),
			)
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, agentList...)
	if opts.MaxWidth > 0 {
		return lipgloss.NewStyle().Width(opts.MaxWidth).Render(content)
	}
	return content
}
