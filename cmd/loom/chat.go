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
	defer func() { _ = c.Close() }()

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
	// Track streamed content length so we only print new tokens incrementally
	var streamedLen int
	var lastStreamedContent string // last PartialContent we streamed, for dedup at COMPLETED
	var pendingPlan *loomv1.ExecutionPlan // Store plan if created

	err := c.StreamWeave(ctx, message, sessionID, agentID, func(progress *loomv1.WeaveProgress) {
		// Capture running token count
		if progress.TokenCount > 0 {
			totalTokens = progress.TokenCount
		}

		// Capture plan if created (display after stream completes)
		if progress.IsPlanCreated && progress.Plan != nil {
			pendingPlan = progress.Plan
		}

		// Only show progress if --stream flag is set
		if chatStream {
			switch progress.Stage {
			case loomv1.ExecutionStage_EXECUTION_STAGE_PATTERN_SELECTION:
				fmt.Fprintf(os.Stderr, "[Pattern Selection: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_SCHEMA_DISCOVERY:
				fmt.Fprintf(os.Stderr, "[Schema Discovery: %s]\n", progress.Message)

			case loomv1.ExecutionStage_EXECUTION_STAGE_LLM_GENERATION:
				// Stream actual LLM tokens to stdout incrementally
				if progress.PartialContent != "" && progress.IsTokenStream {
					content := progress.PartialContent
					if len(content) > streamedLen {
						fmt.Print(content[streamedLen:])
						streamedLen = len(content)
						lastStreamedContent = content
					}
				} else if progress.Message != "" {
					fmt.Fprintf(os.Stderr, "[LLM: %s]\n", progress.Message)
				}

			case loomv1.ExecutionStage_EXECUTION_STAGE_TOOL_EXECUTION:
				// Reset streamed length — new LLM generation starts after tool use
				streamedLen = 0
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
			// Get actual agent response — check multiple sources in priority order
			if progress.PartialResult != nil && progress.PartialResult.DataJson != "" {
				lastMessage = progress.PartialResult.DataJson
			} else if progress.PartialContent != "" {
				lastMessage = progress.PartialContent
			} else if progress.Message != "" && progress.Message != "Query completed successfully" {
				lastMessage = progress.Message
			}
			// If no content found anywhere, lastMessage stays empty —
			// the streamed tokens (if any) are already on stdout
		}
	})

	if err != nil {
		return fmt.Errorf("stream error: %w", err)
	}

	// Handle plan approval if a plan was created
	if pendingPlan != nil {
		fmt.Fprintf(os.Stderr, "\n=== Execution Plan Created ===\n")
		fmt.Fprintf(os.Stderr, "Plan ID: %s\n", pendingPlan.PlanId)
		fmt.Fprintf(os.Stderr, "Reasoning: %s\n\n", pendingPlan.Reasoning)
		fmt.Fprintf(os.Stderr, "Steps:\n")
		for _, tool := range pendingPlan.Tools {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", tool.Step, tool.ToolName)
			fmt.Fprintf(os.Stderr, "     Rationale: %s\n", tool.Rationale)
			if tool.ParamsJson != "" {
				fmt.Fprintf(os.Stderr, "     Params: %s\n", tool.ParamsJson)
			}
		}
		fmt.Fprintf(os.Stderr, "\nApprove this plan? (yes/no): ")

		// Read approval from stdin (safe here - outside the stream callback)
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			response := strings.ToLower(strings.TrimSpace(scanner.Text()))
			approved := response == "yes" || response == "y"

			// Call ApprovePlan RPC
			approveReq := &loomv1.ApprovePlanRequest{
				PlanId:   pendingPlan.PlanId,
				Approved: approved,
			}
			if _, err := c.GetLoomClient().ApprovePlan(ctx, approveReq); err != nil {
				return fmt.Errorf("failed to approve plan: %w", err)
			}

			if approved {
				fmt.Fprintf(os.Stderr, "[Plan approved, executing...]\n\n")

				// Execute the plan and wait for results
				execReq := &loomv1.ExecutePlanRequest{
					PlanId: pendingPlan.PlanId,
				}
				execResp, err := c.GetLoomClient().ExecutePlan(ctx, execReq)
				if err != nil {
					return fmt.Errorf("failed to execute plan: %w", err)
				}

				// Print execution result
				// Print execution result
				if execResp.Summary != "" {
					fmt.Println(execResp.Summary)
				}

				// Print session/token info
				if sessionID != "" {
					fmt.Fprintf(os.Stderr, "\n[Session: %s]\n", sessionID)
				}
				if totalTokens > 0 {
					fmt.Fprintf(os.Stderr, "[Cost: $0.000000 | Tokens: %d]\n", totalTokens)
				}

				return nil
			} else {
				fmt.Fprintf(os.Stderr, "[Plan rejected]\n")
				return nil
			}
		}
	}

	// Print the final message/response.
	// If we already streamed tokens, only print if the final message differs
	// (e.g., the agent produced a final summary after tool calls).
	if lastMessage != "" {
		if chatStream && lastStreamedContent != "" {
			// We already streamed content. Only print the final message
			// if it differs from what was streamed (e.g., a post-tool summary).
			if lastMessage != lastStreamedContent {
				fmt.Println()
				fmt.Println(lastMessage)
			} else {
				// Already streamed this content — just ensure trailing newline
				fmt.Println()
			}
		} else {
			fmt.Println(lastMessage)
		}
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
