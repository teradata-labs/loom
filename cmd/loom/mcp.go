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
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers",
	Long:  `List, test, and manage Model Context Protocol (MCP) servers.`,
}

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List MCP servers",
	Long: `List all configured MCP servers and their status.

Examples:
  loom mcp list
`,
	Run: runMCPListCommand,
}

var mcpTestCmd = &cobra.Command{
	Use:   "test <server-name>",
	Short: "Test MCP server connection",
	Long: `Test connection to an MCP server and verify it's working properly.

Examples:
  loom mcp test vantage
  loom mcp test github
`,
	Args: cobra.ExactArgs(1),
	Run:  runMCPTestCommand,
}

var mcpToolsCmd = &cobra.Command{
	Use:   "tools <server-name>",
	Short: "List tools from MCP server",
	Long: `List all available tools from a specific MCP server.

Examples:
  loom mcp tools vantage
  loom mcp tools github
`,
	Args: cobra.ExactArgs(1),
	Run:  runMCPToolsCommand,
}

func init() {
	// Add subcommands
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpTestCmd)
	mcpCmd.AddCommand(mcpToolsCmd)
}

func runMCPListCommand(cmd *cobra.Command, args []string) {
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

	// List MCP servers
	resp, err := c.ListMCPServers(ctx, &loomv1.ListMCPServersRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing MCP servers: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Servers) == 0 {
		fmt.Println("No MCP servers configured.")
		fmt.Println("\nTo configure MCP servers, see:")
		fmt.Println("  https://github.com/teradata-labs/loom/docs/mcp")
		return
	}

	// Print header
	fmt.Printf("%-20s %-15s %-10s %-s\n", "NAME", "STATUS", "TOOLS", "COMMAND")
	fmt.Println(strings.Repeat("-", 80))

	// Print servers
	for _, server := range resp.Servers {
		status := server.Status
		if status == "" {
			status = "unknown"
		}

		command := server.Command
		if len(command) > 40 {
			command = command[:37] + "..."
		}

		fmt.Printf("%-20s %-15s %-10d %-s\n",
			server.Name,
			status,
			server.ToolCount,
			command,
		)
	}

	fmt.Printf("\nTotal: %d MCP server(s)\n", len(resp.Servers))
}

func runMCPTestCommand(cmd *cobra.Command, args []string) {
	serverName := args[0]

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
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = c.Close() }()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Printf("Checking MCP server: %s\n", serverName)
	fmt.Println()

	// Get MCP server info
	serverInfo, err := c.GetMCPServer(ctx, &loomv1.GetMCPServerRequest{
		ServerName: serverName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting MCP server: %v\n", err)
		os.Exit(1)
	}

	// Print server status
	fmt.Println("Server Info:")
	fmt.Printf("  Name: %s\n", serverInfo.Name)
	fmt.Printf("  Transport: %s\n", serverInfo.Transport)
	fmt.Printf("  Command: %s\n", serverInfo.Command)
	if len(serverInfo.Args) > 0 {
		fmt.Printf("  Args: %v\n", serverInfo.Args)
	}
	fmt.Println()

	// Print connection status
	fmt.Println("Status:")
	if serverInfo.Connected {
		fmt.Println("  ✅ Connected")
	} else {
		fmt.Println("  ❌ Not connected")
	}

	fmt.Printf("  State: %s\n", serverInfo.Status)

	if serverInfo.Enabled {
		fmt.Println("  ✅ Enabled")
	} else {
		fmt.Println("  ⚠️ Disabled")
	}

	if serverInfo.Error != "" {
		fmt.Printf("  Error: %s\n", serverInfo.Error)
	}

	if serverInfo.UptimeSeconds > 0 {
		hours := serverInfo.UptimeSeconds / 3600
		minutes := (serverInfo.UptimeSeconds % 3600) / 60
		fmt.Printf("  Uptime: %dh %dm\n", hours, minutes)
	}

	// List tools
	fmt.Println()
	fmt.Println("Tools:")
	tools, err := c.ListMCPServerTools(ctx, serverName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error listing tools: %v\n", err)
	} else if len(tools) == 0 {
		fmt.Println("  No tools available")
	} else {
		fmt.Printf("  %d tools available\n", len(tools))
		// Show first 10 tools
		limit := len(tools)
		if limit > 10 {
			limit = 10
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("    - %s\n", tools[i].Name)
		}
		if len(tools) > 10 {
			fmt.Printf("    ... and %d more\n", len(tools)-10)
		}
	}
}

func runMCPToolsCommand(cmd *cobra.Command, args []string) {
	serverName := args[0]

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
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = c.Close() }()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// List MCP server tools
	tools, err := c.ListMCPServerTools(ctx, serverName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing MCP server tools: %v\n", err)
		os.Exit(1)
	}

	if len(tools) == 0 {
		fmt.Printf("No tools available from MCP server: %s\n", serverName)
		return
	}

	// Print header
	fmt.Printf("Tools from MCP server: %s\n", serverName)
	fmt.Println()

	// Print tools
	for _, tool := range tools {
		fmt.Printf("%-30s", tool.Name)
		if tool.Description != "" {
			desc := tool.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			fmt.Printf(" - %s", desc)
		}
		fmt.Println()
	}

	fmt.Printf("\nTotal: %d tool(s)\n", len(tools))
}
