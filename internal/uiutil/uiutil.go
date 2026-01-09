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
// Package uiutil provides UI utility functions.
package uiutil

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
)

// OpenInEditor opens a file in the default editor.
func OpenInEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	// #nosec G204 -- Intentional: CLI tool uses $EDITOR env var (standard Unix practice)
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// OpenURL opens a URL in the default browser.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("open", url)
	}
	return cmd.Run()
}

// ExecShell executes a command in the shell.
func ExecShell(ctx context.Context, cmdStr string, callback tea.ExecCallback) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		return callback(err)
	}
}

// Cursor represents a cursor state.
type Cursor struct {
	visible bool
}

// Show shows the cursor.
func (c *Cursor) Show() {
	c.visible = true
}

// Hide hides the cursor.
func (c *Cursor) Hide() {
	c.visible = false
}

// Visible returns whether cursor is visible.
func (c *Cursor) Visible() bool {
	return c.visible
}

// InfoType represents the type of info message.
type InfoType int

const (
	InfoTypeInfo InfoType = iota
	InfoTypeSuccess
	InfoTypeWarn
	InfoTypeError
	InfoTypeUpdate
)

// InfoMsg is an info message.
type InfoMsg struct {
	Text     string
	Msg      string // Alias for Text
	Type     InfoType
	Duration time.Duration
	TTL      time.Duration // Alias for Duration
}

// ClearStatusMsg clears the status message.
type ClearStatusMsg struct{}

// SendInfo sends an info message.
func SendInfo(text string, infoType InfoType) tea.Cmd {
	return func() tea.Msg {
		return InfoMsg{
			Text:     text,
			Msg:      text,
			Type:     infoType,
			Duration: 3 * time.Second,
			TTL:      3 * time.Second,
		}
	}
}

// SendInfoWithDuration sends an info message with duration.
func SendInfoWithDuration(text string, infoType InfoType, duration time.Duration) tea.Cmd {
	return func() tea.Msg {
		return InfoMsg{
			Text:     text,
			Type:     infoType,
			Duration: duration,
		}
	}
}

// ClearStatus clears the status message.
func ClearStatus() tea.Cmd {
	return func() tea.Msg {
		return ClearStatusMsg{}
	}
}

// CmdHandler wraps a message in a command that returns that message.
func CmdHandler(msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return msg
	}
}

// ReportError reports an error message.
func ReportError(msg string) tea.Cmd {
	return SendInfo(msg, InfoTypeError)
}

// ReportInfo reports an info message.
func ReportInfo(msg string) tea.Cmd {
	return SendInfo(msg, InfoTypeInfo)
}

// ReportWarn reports a warning message.
func ReportWarn(msg string) tea.Cmd {
	return SendInfo(msg, InfoTypeWarn)
}
