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
package clarification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/teradata-labs/loom/internal/tui/components/core"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/pkg/metaagent"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

const (
	ClarificationDialogID dialogs.DialogID = "clarification"
)

// ClarificationDialogCmp is the interface for the clarification dialog
type ClarificationDialogCmp interface {
	dialogs.DialogModel
}

// clarificationDialogCmp is the implementation
type clarificationDialogCmp struct {
	wWidth    int
	wHeight   int
	width     int
	height    int
	question  *metaagent.Question
	textInput textinput.Model
	viewport  viewport.Model

	positionRow int
	positionCol int

	keyMap KeyMap

	// RPC-based answering (for remote TUI clients)
	client            *client.Client
	sessionID         string
	agentID           string
	rpcTimeoutSeconds int // RPC timeout in seconds (default: 5)

	// Error handling
	errorMsg string
	sending  bool
}

// NewClarificationDialogCmp creates a new clarification dialog
// For remote TUI clients, provide client/sessionID/agentID for RPC-based answering
// For local/console mode, pass nil client and use question.AnswerChan
// rpcTimeoutSeconds: RPC timeout in seconds (0 = use default of 5s)
func NewClarificationDialogCmp(question *metaagent.Question, c *client.Client, sessionID, agentID string, rpcTimeoutSeconds int) ClarificationDialogCmp {
	ti := textinput.New()
	ti.Placeholder = "Type your answer here..."
	ti.Focus()
	ti.CharLimit = 1000

	vp := viewport.New()

	// Use default timeout if not specified
	if rpcTimeoutSeconds <= 0 {
		rpcTimeoutSeconds = 5
	}

	return &clarificationDialogCmp{
		question:          question,
		textInput:         ti,
		viewport:          vp,
		keyMap:            DefaultKeyMap(),
		client:            c,
		sessionID:         sessionID,
		agentID:           agentID,
		rpcTimeoutSeconds: rpcTimeoutSeconds,
	}
}

func (c *clarificationDialogCmp) Init() tea.Cmd {
	return textinput.Blink
}

func (c *clarificationDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case answerResultMsg:
		// Handle RPC result
		c.sending = false
		if msg.success {
			// Success - close dialog
			return c, func() tea.Msg {
				return dialogs.CloseDialogMsg{}
			}
		}
		// Error - show message and keep dialog open
		c.errorMsg = fmt.Sprintf("Failed to send answer: %v", msg.err)
		return c, nil

	case tea.WindowSizeMsg:
		c.wWidth = msg.Width
		c.wHeight = msg.Height

		// Dialog should be 80% of screen width, max 80 chars
		c.width = min(c.wWidth*8/10, 80)
		c.height = min(c.wHeight*7/10, 25)

		// Center the dialog
		c.positionRow = (c.wHeight - c.height) / 2
		c.positionCol = (c.wWidth - c.width) / 2

		// Update viewport dimensions
		c.viewport.SetWidth(c.width - 4)
		c.viewport.SetHeight(c.height - 10)

		return c, nil

	case tea.KeyMsg:
		// Don't accept input while sending
		if c.sending {
			return c, nil
		}

		switch {
		case key.Matches(msg, c.keyMap.Submit):
			answer := strings.TrimSpace(c.textInput.Value())
			if answer != "" {
				// Clear any previous error
				c.errorMsg = ""

				// Try RPC-based answering first (for remote TUI clients)
				if c.client != nil && c.question.ID != "" {
					c.sending = true
					return c, c.sendAnswerViaRPC(answer)
				}

				// Fallback to channel-based answering (for local/console mode)
				if c.question.AnswerChan != nil {
					// Non-blocking send (channel should have buffer)
					select {
					case c.question.AnswerChan <- answer:
						// Success - close dialog
						return c, func() tea.Msg {
							return dialogs.CloseDialogMsg{}
						}
					default:
						// Channel full or closed - show error
						c.errorMsg = "Failed to send answer: channel unavailable"
						return c, nil
					}
				}
				// No way to send answer
				c.errorMsg = "Failed to send answer: no communication channel available"
				return c, nil
			}
			return c, nil

		case key.Matches(msg, c.keyMap.Cancel):
			// User cancelled - close without sending answer (will timeout)
			return c, func() tea.Msg {
				return dialogs.CloseDialogMsg{}
			}
		}
	}

	// Don't update text input while sending
	if !c.sending {
		// Update text input
		var cmd tea.Cmd
		c.textInput, cmd = c.textInput.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Update viewport
	var cmd tea.Cmd
	c.viewport, cmd = c.viewport.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return c, tea.Batch(cmds...)
}

func (c *clarificationDialogCmp) View() string {
	t := styles.CurrentTheme()
	s := t.S()
	baseStyle := s.Base

	title := core.Title("Clarification Needed", c.width-4)

	// Build content for viewport
	var content strings.Builder

	// Question prompt
	content.WriteString(s.Subtle.Render("Question:"))
	content.WriteString("\n")
	content.WriteString(c.question.Prompt)
	content.WriteString("\n\n")

	// Options if provided
	if len(c.question.Options) > 0 {
		content.WriteString(s.Subtle.Render("Suggested options:"))
		content.WriteString("\n")
		for i, opt := range c.question.Options {
			content.WriteString(fmt.Sprintf("  %d. %s\n", i+1, opt))
		}
		content.WriteString("\n")
	}

	// Context if provided
	if c.question.Context != "" {
		content.WriteString(s.Subtle.Render("Context:"))
		content.WriteString("\n")
		content.WriteString(c.question.Context)
		content.WriteString("\n\n")
	}

	// Timeout if set
	if c.question.Timeout > 0 {
		content.WriteString(s.Subtle.Render(fmt.Sprintf("(Timeout: %v)", c.question.Timeout)))
		content.WriteString("\n")
	}

	c.viewport.SetContent(content.String())

	// Build dialog layout
	strs := []string{
		title,
		"",
		c.viewport.View(),
		"",
		s.Subtle.Render("Your answer:"),
		c.textInput.View(),
		"",
		c.renderStatus(s),
		c.renderError(s),
		c.renderHelp(),
	}

	dialogContent := lipgloss.JoinVertical(lipgloss.Top, strs...)

	dialog := baseStyle.
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus).
		Width(c.width).
		Render(dialogContent)

	return dialog
}

func (c *clarificationDialogCmp) renderStatus(s *styles.Styles) string {
	if !c.sending {
		return ""
	}

	// Show "Sending..." indicator when RPC is in progress
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Italic(true)
	return statusStyle.Render("→ Sending answer...")
}

func (c *clarificationDialogCmp) renderError(s *styles.Styles) string {
	if c.errorMsg == "" {
		return ""
	}

	// Red error message with X symbol
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	return errorStyle.Render("✗ " + c.errorMsg)
}

func (c *clarificationDialogCmp) renderHelp() string {
	t := styles.CurrentTheme()
	s := t.S()
	h := help.New()
	h.Styles.ShortKey = s.Subtle
	h.Styles.ShortDesc = s.Subtle

	return h.ShortHelpView([]key.Binding{
		c.keyMap.Submit,
		c.keyMap.Cancel,
	})
}

func (c *clarificationDialogCmp) Position() (int, int) {
	return c.positionRow, c.positionCol
}

func (c *clarificationDialogCmp) ID() dialogs.DialogID {
	return ClarificationDialogID
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// answerResultMsg carries the result of the RPC call
type answerResultMsg struct {
	success bool
	err     error
}

// sendAnswerViaRPC sends the answer to the server via RPC
func (c *clarificationDialogCmp) sendAnswerViaRPC(answer string) tea.Cmd {
	return func() tea.Msg {
		// Create context with configurable timeout
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.rpcTimeoutSeconds)*time.Second)
		defer cancel()

		// Send answer via RPC
		err := c.client.AnswerClarificationQuestion(ctx, c.sessionID, c.question.ID, answer, c.agentID)
		if err != nil {
			return answerResultMsg{success: false, err: err}
		}

		// Success - close dialog
		return answerResultMsg{success: true, err: nil}
	}
}
