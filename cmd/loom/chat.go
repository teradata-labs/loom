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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	chatMessage string
	chatStream  bool
	chatTimeout time.Duration
)

var chatCmd = &cobra.Command{
	Use:   "chat [message]",
	Short: "Send message to agent (CLI only, no TUI)",
	Long: `Send a message to an agent and get a response in the terminal.

Examples:
  loom chat --thread sql-agent "show me all tables"
  echo "analyze this query" | loom chat --thread sql-agent
  loom chat --thread sql-agent --stream "explain the schema"
`,
	Run: runChatCommand,
}

func init() {
	chatCmd.Flags().StringVarP(&chatMessage, "message", "m", "", "Message to send (if not provided, reads from stdin or args)")
	chatCmd.Flags().BoolVar(&chatStream, "stream", false, "Stream response in real-time")
	chatCmd.Flags().DurationVar(&chatTimeout, "timeout", 5*time.Minute, "Timeout for response")
}

func runChatCommand(cmd *cobra.Command, args []string) {
	// Validate thread is specified
	if agentID == "" {
		fmt.Fprintf(os.Stderr, "Error: --thread is required for chat command\n")
		fmt.Fprintf(os.Stderr, "\nUsage: loom chat --thread <thread-id> [message]\n")
		os.Exit(1)
	}

	// Determine message source: flag > args > stdin
	var message string
	if chatMessage != "" {
		message = chatMessage
	} else if len(args) > 0 {
		message = strings.Join(args, " ")
	} else {
		// Read from stdin
		scanner := bufio.NewScanner(os.Stdin)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		message = strings.Join(lines, "\n")
	}

	// Validate message is not empty
	message = strings.TrimSpace(message)
	if message == "" {
		fmt.Fprintf(os.Stderr, "Error: message cannot be empty\n")
		fmt.Fprintf(os.Stderr, "\nProvide a message via:\n")
		fmt.Fprintf(os.Stderr, "  - Arguments: loom chat --thread agent 'your message'\n")
		fmt.Fprintf(os.Stderr, "  - Flag: loom chat --thread agent --message 'your message'\n")
		fmt.Fprintf(os.Stderr, "  - Stdin: echo 'your message' | loom chat --thread agent\n")
		os.Exit(1)
	}

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

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()

	// Send message and handle response (always use StreamWeave)
	if err := streamChat(ctx, c, message); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func streamChat(ctx context.Context, c *client.Client, message string) error {
	var lastMessage string
	var totalTokens int32

	err := c.StreamWeave(ctx, message, sessionID, agentID, func(progress *loomv1.WeaveProgress) {
		// Capture running token count
		if progress.TokenCount > 0 {
			totalTokens = progress.TokenCount
		}

		// Only show progress if --stream flag is set
		if chatStream {
			switch progress.Stage {
			case loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION:
				fmt.Fprintf(os.Stderr, "[Pattern Selection: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_SCHEMA_DISCOVERY:
				fmt.Fprintf(os.Stderr, "[Schema Discovery: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION:
				// Show LLM generation progress
				if progress.Message != "" {
					fmt.Fprintf(os.Stderr, "[LLM: %s]\n", progress.Message)
				}

			case loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION:
				// Show tool execution
				if progress.ToolName != "" {
					fmt.Fprintf(os.Stderr, "[Executing Tool: %s]\n", progress.ToolName)
				}
				if progress.Message != "" {
					fmt.Fprintf(os.Stderr, "  %s\n", progress.Message)
				}

			case loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP:
				// Human input required
				fmt.Fprintf(os.Stderr, "[Waiting for human input: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_GUARDRAIL_CHECK:
				fmt.Fprintf(os.Stderr, "[Guardrail Check: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_SELF_CORRECTION:
				fmt.Fprintf(os.Stderr, "[Self-Correction: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_FAILED:
				// Error occurred
				fmt.Fprintf(os.Stderr, "[Failed: %s]\n", progress.Message)
			}

			// Show progress percentage if available
			if progress.Progress > 0 && progress.Progress < 100 {
				fmt.Fprintf(os.Stderr, "[Progress: %d%%]\n", progress.Progress)
			}
		}

		// Always capture the final message
		if progress.Stage == loomv1.ExecutionStage_EXECUTION_STAGE_COMPLETED {
			lastMessage = progress.Message
		}
	})

	if err != nil {
		return fmt.Errorf("stream error: %w", err)
	}

	// Print the final message/response
	if lastMessage != "" {
		fmt.Println(lastMessage)
	}

	// Print session info to stderr if user provided session ID
	if sessionID != "" {
		fmt.Fprintf(os.Stderr, "\n[Session: %s]\n", sessionID)
	}

	// Print token count if available
	if totalTokens > 0 {
		fmt.Fprintf(os.Stderr, "[Cost: $0.000000 | Tokens: %d]\n", totalTokens)
	}

	return nil
}
