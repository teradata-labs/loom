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
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"github.com/teradata-labs/loom/internal/app"
	"github.com/teradata-labs/loom/internal/tui"
	"github.com/teradata-labs/loom/internal/version"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	serverAddr string
	sessionID  string
	agentID    string // Internal: still called agentID in code, but flag is --thread for user-facing consistency

	// TLS flags
	tlsEnabled    bool
	tlsInsecure   bool
	tlsCAFile     string
	tlsServerName string
)

var rootCmd = &cobra.Command{
	Use:     "loom",
	Short:   "Loom TUI - Interactive chat interface for agent threads",
	Long:    `Loom Terminal UI - Chat with your Loom agent threads via gRPC. Provides interactive chat with session management, streaming, and real-time cost tracking.`,
	Version: version.Get(),
	Run:     runChat,
}

func init() {
	// Custom help template with Support at bottom
	rootCmd.SetHelpTemplate(`{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}

Quick Start:
  1. Start server: looms serve
  2. Connect to thread: loom --thread <thread-name>
  3. Or let loom auto-select if only one thread exists: loom

Support:
  GitHub: https://github.com/teradata-labs/loom/issues
  Documentation: https://github.com/teradata-labs/loom
`)

	rootCmd.PersistentFlags().StringVarP(&serverAddr, "server", "s", "localhost:60051", "Loom server address")
	rootCmd.PersistentFlags().StringVar(&sessionID, "session", "", "Resume existing session ID")
	rootCmd.PersistentFlags().StringVarP(&agentID, "thread", "t", "", "Thread ID to connect to (e.g., file-explorer-abc123, sql-optimizer-def456)")

	// TLS flags
	rootCmd.PersistentFlags().BoolVar(&tlsEnabled, "tls", false, "Enable TLS connection")
	rootCmd.PersistentFlags().BoolVar(&tlsInsecure, "tls-insecure", false, "Skip TLS certificate verification (for self-signed certs)")
	rootCmd.PersistentFlags().StringVar(&tlsCAFile, "tls-ca-file", "", "Path to CA certificate file")
	rootCmd.PersistentFlags().StringVar(&tlsServerName, "tls-server-name", "", "Override TLS server name verification")

	// Add subcommands
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(agentsCmd)
	rootCmd.AddCommand(sessionsCmd)
	rootCmd.AddCommand(artifactsCmd)
	rootCmd.AddCommand(mcpCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runChat(cmd *cobra.Command, args []string) {
	// Connect to server
	c, err := client.NewClient(client.Config{
		ServerAddr:    serverAddr,
		TLSEnabled:    tlsEnabled,
		TLSInsecure:   tlsInsecure,
		TLSCAFile:     tlsCAFile,
		TLSServerName: tlsServerName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s\n", serverAddr)
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "Make sure the server is running:\n")
		if tlsEnabled {
			fmt.Fprintf(os.Stderr, "  looms serve --config <config-with-tls>\n\n")
		} else {
			fmt.Fprintf(os.Stderr, "  looms serve\n\n")
		}
		os.Exit(1)
	}
	defer c.Close()

	// If no thread specified, start TUI with sidebar for selection
	// The sidebar always shows agents/workflows, so no need for CLI menu
	// agentID remains empty - user will select from sidebar

	// Debug: Log the agent ID being set
	if f, err := os.OpenFile("/tmp/loom-cli-debug.log", os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "Setting agentID on application: '%s'\n", agentID)
		f.Close()
	}

	// Create event channel for TUI
	events := make(chan tea.Msg, 100)

	// Create App from client
	application := app.NewFromClient(c, events)

	// Set the selected agent/thread ID
	application.SetAgentID(agentID)

	// Debug: Verify it was set
	if f, err := os.OpenFile("/tmp/loom-cli-debug.log", os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "Agent ID set on application\n")
		f.Close()
	}

	// Create new TUI model
	model := tui.New(application)

	// Run Bubbletea program
	// Note: In v2, AltScreen is controlled via View().AltScreen field
	// and mouse is enabled via tea.EnableMouseCellMotion() cmd
	p := tea.NewProgram(
		model,
		tea.WithEnvironment(os.Environ()),
		tea.WithFilter(tui.MouseEventFilter),
	)

	// Start event subscription in background
	go func() {
		application.Subscribe(p)
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}

	// Cleanup
	application.Shutdown()
}
