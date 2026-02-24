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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var agentsCmd = &cobra.Command{
	Use:     "agents",
	Aliases: []string{"list", "ls", "threads"},
	Short:   "List available agents/threads",
	Long:    `List all available agents and threads configured on the server.`,
	Run:     runAgentsCommand,
}

func runAgentsCommand(cmd *cobra.Command, args []string) {
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
	defer func() { _ = c.Close() }()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// List agents
	agents, err := c.ListAgents(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing agents: %v\n", err)
		os.Exit(1)
	}

	if len(agents) == 0 {
		fmt.Println("No agents configured.")
		fmt.Println("\nTo create an agent, see: https://github.com/teradata-labs/loom")
		return
	}

	// Print agents
	fmt.Printf("Available agents (%d):\n\n", len(agents))
	for _, agent := range agents {
		// Print agent ID and name
		fmt.Printf("  %s", agent.Id)
		if agent.Name != "" && agent.Name != agent.Id {
			fmt.Printf(" (%s)", agent.Name)
		}
		fmt.Println()

		// Print status and stats
		if agent.Status != "" {
			fmt.Printf("    Status: %s", agent.Status)
			if agent.ActiveSessions > 0 {
				fmt.Printf(" | Active sessions: %d", agent.ActiveSessions)
			}
			if agent.TotalMessages > 0 {
				fmt.Printf(" | Total messages: %d", agent.TotalMessages)
			}
			fmt.Println()
		}

		// Print uptime if available
		if agent.UptimeSeconds > 0 {
			hours := agent.UptimeSeconds / 3600
			minutes := (agent.UptimeSeconds % 3600) / 60
			fmt.Printf("    Uptime: %dh %dm\n", hours, minutes)
		}

		// Print metadata if available (e.g., model, provider)
		if len(agent.Metadata) > 0 {
			// Look for common metadata fields
			if model, ok := agent.Metadata["model"]; ok {
				fmt.Printf("    Model: %s", model)
				if provider, ok := agent.Metadata["provider"]; ok {
					fmt.Printf(" (%s)", provider)
				}
				fmt.Println()
			}
			if desc, ok := agent.Metadata["description"]; ok && desc != "" {
				// Truncate long descriptions
				if len(desc) > 60 {
					desc = desc[:57] + "..."
				}
				fmt.Printf("    %s\n", desc)
			}
		}

		fmt.Println()
	}

	fmt.Println("To connect to an agent:")
	fmt.Println("  loom --thread <agent-id>                # Open TUI")
	fmt.Println("  loom chat --thread <agent-id> 'message' # CLI chat")
}
