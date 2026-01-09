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
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var patternCmd = &cobra.Command{
	Use:   "pattern",
	Short: "Manage patterns for agents",
	Long: `Manage pattern libraries for Loom agents.

Patterns are domain knowledge templates that guide agent behavior.
Use this command to create, list, and manage patterns at runtime.`,
}

var patternCreateCmd = &cobra.Command{
	Use:   "create [pattern-name]",
	Short: "Create a new pattern for an agent",
	Long: `Create a new pattern at runtime by uploading YAML content.

The pattern will be written to the agent's patterns directory and
automatically detected by the hot-reload watcher.

Examples:
  # Create pattern from file
  looms pattern create my-pattern --thread sql-thread --file pattern.yaml

  # Create pattern from stdin
  cat pattern.yaml | looms pattern create my-pattern --thread sql-thread --stdin

  # Interactive mode (opens editor)
  looms pattern create my-pattern --thread sql-thread --interactive`,
	Args: cobra.ExactArgs(1),
	Run:  runPatternCreate,
}

var patternWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch for real-time pattern updates",
	Long: `Watch for real-time pattern creation, modification, and deletion events.

This command streams pattern update events from the server and displays them
as they occur. Useful for monitoring pattern changes during development.

Examples:
  # Watch all pattern updates
  looms pattern watch

  # Watch updates for a specific thread
  looms pattern watch --thread sql-thread

  # Watch updates for a specific category
  looms pattern watch --category analytics

Press Ctrl+C to stop watching.`,
	Run: runPatternWatch,
}

var (
	patternAgentID     string
	patternCategory    string
	patternFile        string
	patternStdin       bool
	patternInteractive bool
	patternServer      string
	patternTimeout     int
)

func init() {
	rootCmd.AddCommand(patternCmd)
	patternCmd.AddCommand(patternCreateCmd)
	patternCmd.AddCommand(patternWatchCmd)

	// Create command flags
	patternCreateCmd.Flags().StringVar(&patternAgentID, "thread", "", "Thread ID to create pattern for (required)")
	patternCreateCmd.Flags().StringVar(&patternFile, "file", "", "Path to pattern YAML file")
	patternCreateCmd.Flags().BoolVar(&patternStdin, "stdin", false, "Read pattern YAML from stdin")
	patternCreateCmd.Flags().BoolVar(&patternInteractive, "interactive", false, "Open editor to create pattern interactively")
	patternCreateCmd.Flags().StringVar(&patternServer, "server", "localhost:9090", "Loom server address")
	patternCreateCmd.Flags().IntVar(&patternTimeout, "timeout", 30, "Request timeout in seconds")

	_ = patternCreateCmd.MarkFlagRequired("thread")

	// Watch command flags
	patternWatchCmd.Flags().StringVar(&patternAgentID, "thread", "", "Filter by thread ID (optional)")
	patternWatchCmd.Flags().StringVar(&patternCategory, "category", "", "Filter by pattern category (optional)")
	patternWatchCmd.Flags().StringVar(&patternServer, "server", "localhost:9090", "Loom server address")
}

func runPatternCreate(cmd *cobra.Command, args []string) {
	patternName := args[0]

	// Validate input source
	inputCount := 0
	if patternFile != "" {
		inputCount++
	}
	if patternStdin {
		inputCount++
	}
	if patternInteractive {
		inputCount++
	}

	if inputCount == 0 {
		fmt.Fprintf(os.Stderr, "Error: Must specify one of --file, --stdin, or --interactive\n")
		os.Exit(1)
	}
	if inputCount > 1 {
		fmt.Fprintf(os.Stderr, "Error: Can only specify one of --file, --stdin, or --interactive\n")
		os.Exit(1)
	}

	// Read pattern YAML content
	var yamlContent string
	var err error

	if patternFile != "" {
		// Read from file
		data, err := os.ReadFile(patternFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file %s: %v\n", patternFile, err)
			os.Exit(1)
		}
		yamlContent = string(data)
	} else if patternStdin {
		// Read from stdin
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		yamlContent = string(data)
	} else if patternInteractive {
		// TODO: Implement interactive editor mode
		fmt.Fprintf(os.Stderr, "Error: --interactive mode not yet implemented\n")
		fmt.Fprintf(os.Stderr, "Use --file or --stdin instead\n")
		os.Exit(1)
	}

	if yamlContent == "" {
		fmt.Fprintf(os.Stderr, "Error: Pattern YAML content is empty\n")
		os.Exit(1)
	}

	// Connect to server
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(patternTimeout)*time.Second)
	defer cancel()

	loomClient, err := client.NewClient(client.Config{
		ServerAddr: patternServer,
		Timeout:    time.Duration(patternTimeout) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server %s: %v\n", patternServer, err)
		os.Exit(1)
	}
	defer loomClient.Close()

	// Create pattern via RPC
	fmt.Printf("Creating pattern '%s' for agent '%s'...\n", patternName, patternAgentID)

	req := &loomv1.CreatePatternRequest{
		AgentId:     patternAgentID,
		Name:        patternName,
		YamlContent: yamlContent,
	}

	resp, err := loomClient.CreatePattern(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating pattern: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Failed to create pattern: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Printf("✅ Pattern created successfully!\n")
	fmt.Printf("   Name: %s\n", resp.PatternName)
	fmt.Printf("   File: %s\n", resp.FilePath)
	fmt.Printf("\nPattern is now available to the agent via hot-reload.\n")
}

func runPatternWatch(cmd *cobra.Command, args []string) {
	// Connect to server
	loomClient, err := client.NewClient(client.Config{
		ServerAddr: patternServer,
		Timeout:    60 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to server %s: %v\n", patternServer, err)
		os.Exit(1)
	}
	defer loomClient.Close()

	// Print header
	fmt.Printf("🔍 Watching for pattern updates on %s\n", patternServer)
	if patternAgentID != "" {
		fmt.Printf("   Filter: agent_id=%s\n", patternAgentID)
	}
	if patternCategory != "" {
		fmt.Printf("   Filter: category=%s\n", patternCategory)
	}
	fmt.Println("\nPress Ctrl+C to stop watching")

	// Create context that can be cancelled with Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		<-sigCh
		fmt.Println("\n\nStopping watch...")
		cancel()
	}()

	// Stream pattern updates
	err = loomClient.StreamPatternUpdates(ctx, patternAgentID, patternCategory, func(event *loomv1.PatternUpdateEvent) {
		timestamp := time.UnixMilli(event.Timestamp).Format("15:04:05")

		switch event.Type {
		case loomv1.PatternUpdateType_PATTERN_CREATED:
			fmt.Printf("[%s] ✨ CREATED   agent=%s pattern=%s file=%s\n",
				timestamp, event.AgentId, event.PatternName, event.FilePath)

		case loomv1.PatternUpdateType_PATTERN_MODIFIED:
			fmt.Printf("[%s] 📝 MODIFIED  agent=%s pattern=%s file=%s\n",
				timestamp, event.AgentId, event.PatternName, event.FilePath)

		case loomv1.PatternUpdateType_PATTERN_DELETED:
			fmt.Printf("[%s] 🗑️  DELETED   agent=%s pattern=%s\n",
				timestamp, event.AgentId, event.PatternName)

		case loomv1.PatternUpdateType_PATTERN_VALIDATION_FAILED:
			fmt.Printf("[%s] ❌ INVALID   agent=%s pattern=%s error=%s\n",
				timestamp, event.AgentId, event.PatternName, event.Error)

		default:
			fmt.Printf("[%s] ❓ UNKNOWN   agent=%s pattern=%s type=%v\n",
				timestamp, event.AgentId, event.PatternName, event.Type)
		}
	})

	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "\nError streaming pattern updates: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Watch stopped.")
}
