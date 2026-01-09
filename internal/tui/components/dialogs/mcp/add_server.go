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
package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/internal/tui/components/dialogs"
	"github.com/teradata-labs/loom/internal/tui/styles"
	"github.com/teradata-labs/loom/internal/tui/util"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

const (
	addServerDialogID dialogs.DialogID = "add-mcp-server"
)

// Field indices
const (
	fieldName = iota
	fieldCommand
	fieldArgs
	fieldEnvVars
	fieldTransport
	fieldWorkingDir
	fieldCount
)

// AddMCPServerDialog represents the add MCP server dialog.
type AddMCPServerDialog interface {
	dialogs.DialogModel
}

type addMCPServerDialogCmp struct {
	wWidth, wHeight int
	width, height   int

	inputs  []textinput.Model
	focused int
	keys    AddServerDialogKeyMap
	help    help.Model

	client   *client.Client
	onSubmit func(req *loomv1.AddMCPServerRequest) tea.Cmd

	// Test state
	testStatus    string // "", "testing", "success", "error"
	testMessage   string
	testError     string
	testToolCount int32
	testLatency   int64
	testPassed    bool
}

// TestMCPServerResultMsg is sent when test completes
type TestMCPServerResultMsg struct {
	Success   bool
	Message   string
	Error     string
	ToolCount int32
	Latency   int64
}

func NewAddMCPServerDialog(
	c *client.Client,
	onSubmit func(req *loomv1.AddMCPServerRequest) tea.Cmd,
) AddMCPServerDialog {
	t := styles.CurrentTheme()
	inputs := make([]textinput.Model, fieldCount)

	// Name
	inputs[fieldName] = textinput.New()
	inputs[fieldName].Placeholder = "server-name"
	inputs[fieldName].SetWidth(50)
	inputs[fieldName].SetVirtualCursor(false)
	inputs[fieldName].Prompt = ""
	inputs[fieldName].SetStyles(t.S().TextInput)
	inputs[fieldName].Focus()

	// Command
	inputs[fieldCommand] = textinput.New()
	inputs[fieldCommand].Placeholder = "/path/to/mcp-server"
	inputs[fieldCommand].SetWidth(50)
	inputs[fieldCommand].SetVirtualCursor(false)
	inputs[fieldCommand].Prompt = ""
	inputs[fieldCommand].SetStyles(t.S().TextInput)

	// Args (comma-separated)
	inputs[fieldArgs] = textinput.New()
	inputs[fieldArgs].Placeholder = "serve,--mode=stdio (comma-separated)"
	inputs[fieldArgs].SetWidth(50)
	inputs[fieldArgs].SetVirtualCursor(false)
	inputs[fieldArgs].Prompt = ""
	inputs[fieldArgs].SetStyles(t.S().TextInput)

	// Env vars (key=value,key2=value2)
	inputs[fieldEnvVars] = textinput.New()
	inputs[fieldEnvVars].Placeholder = "KEY=value,KEY2=value2 (comma-separated)"
	inputs[fieldEnvVars].SetWidth(50)
	inputs[fieldEnvVars].SetVirtualCursor(false)
	inputs[fieldEnvVars].Prompt = ""
	inputs[fieldEnvVars].SetStyles(t.S().TextInput)

	// Transport
	inputs[fieldTransport] = textinput.New()
	inputs[fieldTransport].Placeholder = "stdio"
	inputs[fieldTransport].SetValue("stdio")
	inputs[fieldTransport].SetWidth(50)
	inputs[fieldTransport].SetVirtualCursor(false)
	inputs[fieldTransport].Prompt = ""
	inputs[fieldTransport].SetStyles(t.S().TextInput)

	// Working directory
	inputs[fieldWorkingDir] = textinput.New()
	inputs[fieldWorkingDir].Placeholder = "/path/to/working/dir (optional)"
	inputs[fieldWorkingDir].SetWidth(50)
	inputs[fieldWorkingDir].SetVirtualCursor(false)
	inputs[fieldWorkingDir].Prompt = ""
	inputs[fieldWorkingDir].SetStyles(t.S().TextInput)

	return &addMCPServerDialogCmp{
		inputs:   inputs,
		keys:     DefaultAddServerDialogKeyMap(),
		help:     help.New(),
		width:    70,
		client:   c,
		onSubmit: onSubmit,
	}
}

// Init implements AddMCPServerDialog.
func (c *addMCPServerDialogCmp) Init() tea.Cmd {
	return nil
}

// Update implements AddMCPServerDialog.
func (c *addMCPServerDialogCmp) Update(msg tea.Msg) (util.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.wWidth = msg.Width
		c.wHeight = msg.Height
		c.width = min(90, c.wWidth)
		c.height = min(25, c.wHeight)
		for i := range c.inputs {
			c.inputs[i].SetWidth(c.width - 10)
		}

	case TestMCPServerResultMsg:
		c.testStatus = "complete"
		c.testPassed = msg.Success
		c.testMessage = msg.Message
		c.testError = msg.Error
		c.testToolCount = msg.ToolCount
		c.testLatency = msg.Latency
		return c, nil

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, c.keys.Close):
			return c, util.CmdHandler(dialogs.CloseDialogMsg{})

		case key.Matches(msg, c.keys.Test):
			// Start test
			if c.testStatus == "testing" {
				return c, nil // Already testing
			}
			c.testStatus = "testing"
			c.testPassed = false
			c.testMessage = ""
			c.testError = ""
			return c, c.testConnection()

		case key.Matches(msg, c.keys.Confirm):
			// Only allow save if test passed
			if !c.testPassed {
				return c, nil
			}

			req := c.buildRequest()
			req.AutoStart = true
			req.Enabled = true

			return c, tea.Sequence(
				util.CmdHandler(dialogs.CloseDialogMsg{}),
				c.onSubmit(req),
			)

		case key.Matches(msg, c.keys.Next):
			// Move to the next input
			c.inputs[c.focused].Blur()
			c.focused = (c.focused + 1) % len(c.inputs)
			c.inputs[c.focused].Focus()

		case key.Matches(msg, c.keys.Previous):
			// Move to the previous input
			c.inputs[c.focused].Blur()
			c.focused = (c.focused - 1 + len(c.inputs)) % len(c.inputs)
			c.inputs[c.focused].Focus()

		default:
			var cmd tea.Cmd
			c.inputs[c.focused], cmd = c.inputs[c.focused].Update(msg)
			return c, cmd
		}

	case tea.PasteMsg:
		var cmd tea.Cmd
		c.inputs[c.focused], cmd = c.inputs[c.focused].Update(msg)
		return c, cmd
	}
	return c, nil
}

// testConnection runs the test connection RPC
func (c *addMCPServerDialogCmp) testConnection() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()

		req := &loomv1.TestMCPServerConnectionRequest{
			Command:        c.inputs[fieldCommand].Value(),
			Args:           c.parseArgs(c.inputs[fieldArgs].Value()),
			Env:            c.parseEnvVars(c.inputs[fieldEnvVars].Value()),
			Transport:      c.inputs[fieldTransport].Value(),
			WorkingDir:     c.inputs[fieldWorkingDir].Value(),
			TimeoutSeconds: 30,
		}

		resp, err := c.client.TestMCPServerConnection(ctx, req)
		if err != nil {
			return TestMCPServerResultMsg{
				Success: false,
				Error:   fmt.Sprintf("RPC error: %v", err),
			}
		}

		return TestMCPServerResultMsg{
			Success:   resp.Success,
			Message:   resp.Message,
			Error:     resp.Error,
			ToolCount: resp.ToolCount,
			Latency:   resp.LatencyMs,
		}
	}
}

// buildRequest constructs the AddMCPServerRequest from inputs
func (c *addMCPServerDialogCmp) buildRequest() *loomv1.AddMCPServerRequest {
	return &loomv1.AddMCPServerRequest{
		Name:       c.inputs[fieldName].Value(),
		Command:    c.inputs[fieldCommand].Value(),
		Args:       c.parseArgs(c.inputs[fieldArgs].Value()),
		Env:        c.parseEnvVars(c.inputs[fieldEnvVars].Value()),
		Transport:  c.inputs[fieldTransport].Value(),
		WorkingDir: c.inputs[fieldWorkingDir].Value(),
	}
}

// parseArgs splits comma-separated args
func (c *addMCPServerDialogCmp) parseArgs(argsStr string) []string {
	if argsStr == "" {
		return nil
	}
	parts := strings.Split(argsStr, ",")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			args = append(args, trimmed)
		}
	}
	return args
}

// parseEnvVars parses "KEY=value,KEY2=value2" format
func (c *addMCPServerDialogCmp) parseEnvVars(envStr string) map[string]string {
	if envStr == "" {
		return nil
	}
	env := make(map[string]string)
	pairs := strings.Split(envStr, ",")
	for _, pair := range pairs {
		trimmed := strings.TrimSpace(pair)
		if trimmed == "" {
			continue
		}
		kv := strings.SplitN(trimmed, "=", 2)
		if len(kv) == 2 {
			env[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return env
}

// View implements AddMCPServerDialog.
func (c *addMCPServerDialogCmp) View() string {
	t := styles.CurrentTheme()
	baseStyle := t.S().Base

	title := lipgloss.NewStyle().
		Foreground(t.Primary).
		Bold(true).
		Padding(0, 1).
		Render("Add MCP Server")

	// Input fields
	fieldLabels := []string{
		"Name*",
		"Command*",
		"Arguments",
		"Environment Variables",
		"Transport*",
		"Working Directory",
	}

	inputFields := make([]string, len(c.inputs))
	for i, input := range c.inputs {
		labelStyle := baseStyle.Padding(1, 1, 0, 1)

		if i == c.focused {
			labelStyle = labelStyle.Foreground(t.FgBase).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(t.FgMuted)
		}

		label := labelStyle.Render(fieldLabels[i] + ":")

		field := t.S().Text.
			Padding(0, 1).
			Render(input.View())

		inputFields[i] = lipgloss.JoinVertical(lipgloss.Left, label, field)
	}

	// Test status section
	testSection := c.renderTestStatus()

	elements := []string{title}
	elements = append(elements, inputFields...)
	if testSection != "" {
		elements = append(elements, "", testSection)
	}

	c.help.ShowAll = false
	helpText := baseStyle.Padding(0, 1).Render(c.help.View(c.keys))
	elements = append(elements, "", helpText)

	content := lipgloss.JoinVertical(lipgloss.Left, elements...)

	return baseStyle.Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.BorderFocus).
		Width(c.width).
		Render(content)
}

// renderTestStatus renders the test status section
func (c *addMCPServerDialogCmp) renderTestStatus() string {
	if c.testStatus == "" {
		return ""
	}

	t := styles.CurrentTheme()

	if c.testStatus == "testing" {
		return t.S().Base.
			Foreground(t.Warning).
			Padding(0, 1).
			Render("🔄 Testing connection...")
	}

	if c.testPassed {
		statusLine := fmt.Sprintf("✅ Test passed! Discovered %d tools (%dms)", c.testToolCount, c.testLatency)
		return t.S().Base.
			Foreground(t.Success).
			Padding(0, 1).
			Render(statusLine)
	}

	// Test failed
	errorMsg := c.testError
	if errorMsg == "" {
		errorMsg = "Unknown error"
	}
	statusLine := fmt.Sprintf("❌ Test failed: %s (%dms)", errorMsg, c.testLatency)
	return t.S().Base.
		Foreground(t.Error).
		Padding(0, 1).
		Render(statusLine)
}

func (c *addMCPServerDialogCmp) Cursor() *tea.Cursor {
	if len(c.inputs) == 0 {
		return nil
	}
	cursor := c.inputs[c.focused].Cursor()
	if cursor != nil {
		cursor = c.moveCursor(cursor)
	}
	return cursor
}

const (
	headerHeight      = 3
	itemHeight        = 3
	paddingHorizontal = 3
)

func (c *addMCPServerDialogCmp) moveCursor(cursor *tea.Cursor) *tea.Cursor {
	row, col := c.Position()
	offset := row + headerHeight + (1+c.focused)*itemHeight
	cursor.Y += offset
	cursor.X = cursor.X + col + paddingHorizontal
	return cursor
}

func (c *addMCPServerDialogCmp) Position() (int, int) {
	row := (c.wHeight / 2) - (c.height / 2)
	col := (c.wWidth / 2) - (c.width / 2)
	return row, col
}

// ID implements AddMCPServerDialog.
func (c *addMCPServerDialogCmp) ID() dialogs.DialogID {
	return addServerDialogID
}
