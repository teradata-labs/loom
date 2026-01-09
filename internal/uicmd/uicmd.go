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
// Package uicmd provides UI command utilities.
package uicmd

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Handler is a function that handles a command and returns a tea.Cmd.
type Handler func(cmd Command) tea.Cmd

// Command represents a UI command.
type Command struct {
	Name        string
	Description string
	Title       string
	Shortcut    string
	Aliases     []string
	Args        []Arg
	Run         func(args []string) error
	Handler     Handler
	ID          string
}

// Arg represents a command argument.
type Arg struct {
	Name        string
	Description string
	Required    bool
	Default     string
	Type        ArgType
	Choices     []string
}

// ArgType represents the type of an argument.
type ArgType int

const (
	ArgTypeString ArgType = iota
	ArgTypeInt
	ArgTypeBool
	ArgTypeFloat
	ArgTypeFile
	ArgTypeDir
	ArgTypeChoice
)

// Registry holds registered commands.
type Registry struct {
	commands map[string]*Command
}

// NewRegistry creates a new command registry.
func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]*Command),
	}
}

// Register registers a command.
func (r *Registry) Register(cmd *Command) {
	r.commands[cmd.Name] = cmd
	for _, alias := range cmd.Aliases {
		r.commands[alias] = cmd
	}
}

// Get gets a command by name.
func (r *Registry) Get(name string) *Command {
	return r.commands[name]
}

// List returns all commands.
func (r *Registry) List() []*Command {
	seen := make(map[string]bool)
	var cmds []*Command
	for _, cmd := range r.commands {
		if !seen[cmd.Name] {
			cmds = append(cmds, cmd)
			seen[cmd.Name] = true
		}
	}
	return cmds
}

// Parse parses a command string.
func Parse(input string) (name string, args []string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", nil
	}
	name = strings.TrimPrefix(parts[0], "/")
	if len(parts) > 1 {
		args = parts[1:]
	}
	return name, args
}

// FormatHelp formats command help.
func FormatHelp(cmd *Command) string {
	var sb strings.Builder
	sb.WriteString(cmd.Name)
	if len(cmd.Aliases) > 0 {
		sb.WriteString(" (aliases: ")
		sb.WriteString(strings.Join(cmd.Aliases, ", "))
		sb.WriteString(")")
	}
	sb.WriteString("\n")
	sb.WriteString(cmd.Description)
	if len(cmd.Args) > 0 {
		sb.WriteString("\n\nArguments:\n")
		for _, arg := range cmd.Args {
			sb.WriteString("  ")
			sb.WriteString(arg.Name)
			if arg.Required {
				sb.WriteString(" (required)")
			}
			sb.WriteString(": ")
			sb.WriteString(arg.Description)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// CommandType represents the type of command.
type CommandType int

const (
	SystemCommands CommandType = iota
	UserCommands
	MCPPrompts
)

// CommandRunCustomMsg is sent when a custom command is run.
type CommandRunCustomMsg struct {
	Command string
	Content string
}

// MCPPrompt represents an MCP prompt definition.
type MCPPrompt struct {
	Name        string
	Title       string
	Description string
	Arguments   []*Arg
}

// ShowMCPPromptArgumentsDialogMsg is sent to show MCP prompt arguments dialog.
type ShowMCPPromptArgumentsDialogMsg struct {
	PromptName string
	Prompt     *MCPPrompt
	OnSubmit   func(args map[string]string) tea.Cmd
}

// ShowArgumentsDialogMsg is sent to show arguments dialog.
type ShowArgumentsDialogMsg struct {
	Command     string
	CommandID   string
	Description string
	ArgNames    []string
	OnSubmit    func(args map[string]string) tea.Cmd
}

// CloseArgumentsDialogMsg is sent to close arguments dialog.
type CloseArgumentsDialogMsg struct{}

// LoadCustomCommands loads custom commands.
func LoadCustomCommands() ([]Command, error) {
	return nil, nil
}

// LoadMCPPrompts loads MCP prompts.
func LoadMCPPrompts() []Command {
	return nil
}

// String returns a string representation of CommandType.
func (c CommandType) String() string {
	switch c {
	case SystemCommands:
		return "System"
	case UserCommands:
		return "User"
	case MCPPrompts:
		return "MCP"
	default:
		return "Unknown"
	}
}
