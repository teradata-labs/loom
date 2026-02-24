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
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	sessionsLimit  int32
	sessionsOffset int32
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Manage conversation sessions",
	Long:  `List, view, and delete conversation sessions.`,
}

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List conversation sessions",
	Long: `List all conversation sessions with details.

Examples:
  loom sessions list
  loom sessions list --limit 50
`,
	Run: runSessionsListCommand,
}

var sessionsShowCmd = &cobra.Command{
	Use:   "show <session-id>",
	Short: "Show session details",
	Long: `Show detailed information about a specific session.

Examples:
  loom sessions show sess_abc123def456
`,
	Args: cobra.ExactArgs(1),
	Run:  runSessionsShowCommand,
}

var sessionsDeleteCmd = &cobra.Command{
	Use:   "delete <session-id>",
	Short: "Delete a session",
	Long: `Delete a session and all its conversation history.

Examples:
  loom sessions delete sess_abc123def456
`,
	Args: cobra.ExactArgs(1),
	Run:  runSessionsDeleteCommand,
}

func init() {
	// Add subcommands
	sessionsCmd.AddCommand(sessionsListCmd)
	sessionsCmd.AddCommand(sessionsShowCmd)
	sessionsCmd.AddCommand(sessionsDeleteCmd)

	// Flags for list command
	sessionsListCmd.Flags().Int32VarP(&sessionsLimit, "limit", "n", 20, "Maximum number of sessions to return")
	sessionsListCmd.Flags().Int32Var(&sessionsOffset, "offset", 0, "Number of sessions to skip")
}

func runSessionsListCommand(cmd *cobra.Command, args []string) {
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

	// List sessions
	sessions, err := c.ListSessions(ctx, sessionsLimit, sessionsOffset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing sessions: %v\n", err)
		os.Exit(1)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	// Print header
	fmt.Printf("%-25s %-15s %-15s %-12s %-15s\n", "SESSION ID", "STATE", "BACKEND", "MESSAGES", "CREATED")
	fmt.Println(strings.Repeat("-", 85))

	// Print sessions
	for _, session := range sessions {
		createdAt := "unknown"
		if session.CreatedAt > 0 {
			t := time.Unix(session.CreatedAt, 0)
			createdAt = formatTimeAgo(t)
		}

		state := session.State
		if state == "" {
			state = "unknown"
		}

		backend := session.Backend
		if backend == "" {
			backend = "n/a"
		}

		fmt.Printf("%-25s %-15s %-15s %-12d %-15s\n",
			session.Id,
			state,
			backend,
			session.ConversationCount,
			createdAt,
		)
	}

	// Print footer
	fmt.Printf("\nShowing %d session(s)", len(sessions))
	if sessionsOffset > 0 {
		fmt.Printf(" (offset: %d)", sessionsOffset)
	}
	fmt.Println()
}

func runSessionsShowCommand(cmd *cobra.Command, args []string) {
	sessionID := args[0]

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

	// Get session
	session, err := c.GetSession(ctx, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting session: %v\n", err)
		os.Exit(1)
	}

	// Print session details
	fmt.Printf("Session: %s\n", session.Id)
	if session.Name != "" {
		fmt.Printf("Name: %s\n", session.Name)
	}
	if session.Backend != "" {
		fmt.Printf("Backend: %s\n", session.Backend)
	}
	if session.State != "" {
		fmt.Printf("State: %s\n", session.State)
	}
	fmt.Printf("Messages: %d\n", session.ConversationCount)

	if session.TotalCostUsd > 0 {
		fmt.Printf("Total Cost: $%.6f\n", session.TotalCostUsd)
	}

	if session.CreatedAt > 0 {
		t := time.Unix(session.CreatedAt, 0)
		fmt.Printf("Created: %s (%s)\n", t.Format(time.RFC3339), formatTimeAgo(t))
	}

	if session.UpdatedAt > 0 {
		t := time.Unix(session.UpdatedAt, 0)
		fmt.Printf("Updated: %s (%s)\n", t.Format(time.RFC3339), formatTimeAgo(t))
	}

	// Print metadata if available
	if len(session.Metadata) > 0 {
		fmt.Println("\nMetadata:")
		for k, v := range session.Metadata {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
}

func runSessionsDeleteCommand(cmd *cobra.Command, args []string) {
	sessionID := args[0]

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

	// Delete session
	err = c.DeleteSession(ctx, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Deleted session: %s\n", sessionID)
}

// formatTimeAgo formats a time as "X ago" (e.g., "2 hours ago")
func formatTimeAgo(t time.Time) string {
	duration := time.Since(t)

	if duration < time.Minute {
		return "just now"
	} else if duration < time.Hour {
		minutes := int(duration.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	} else if duration < 7*24*time.Hour {
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	} else if duration < 30*24*time.Hour {
		weeks := int(duration.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	} else if duration < 365*24*time.Hour {
		months := int(duration.Hours() / 24 / 30)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	} else {
		years := int(duration.Hours() / 24 / 365)
		if years == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", years)
	}
}
