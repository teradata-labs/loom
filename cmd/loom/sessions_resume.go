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
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	sessionsResumeTimeout time.Duration
	resolveGapOutcome     string
	resolveGapResult      string
)

var sessionsResumeCmd = &cobra.Command{
	Use:   "resume <session-id>",
	Short: "Finish a turn that a server crash interrupted",
	Long: `Finish a conversation turn that was interrupted by a server crash.

Requires execution_log.enabled on the server while the turn ran. The server
reads its execution log: a tool call recorded as complete gets its saved
result instead of running again, a call that never started runs now, and the
turn's reply is produced. A turn that ran without the execution log is
refused, because the server cannot tell which of its calls already ran.

If a call started but its result was never recorded, its outcome is unknown
and nothing is retried. Check what the call did, record the outcome with
'loom sessions resolve-gap', then run resume again.

Examples:
  loom sessions resume sess_abc123def456
  loom sessions resume sess_abc123def456 --timeout 30m
`,
	Args: cobra.ExactArgs(1),
	Run:  runSessionsResumeCommand,
}

var sessionsResolveGapCmd = &cobra.Command{
	Use:   "resolve-gap <session-id>",
	Short: "Record the outcome of a tool call that resume could not determine",
	Long: `Record what happened to the tool call that 'loom sessions resume' reported
as an execution gap: it started before the crash, but its result was never
recorded. Check the tool's effect yourself first.

  --outcome succeeded --result '<json>'   the call ran; <json> is its result
  --outcome failed                        the call did not take effect

Then run 'loom sessions resume' again.

Examples:
  loom sessions resolve-gap sess_abc123def456 --outcome succeeded --result '{"rows": 3}'
  loom sessions resolve-gap sess_abc123def456 --outcome failed
`,
	Args: cobra.ExactArgs(1),
	Run:  runSessionsResolveGapCommand,
}

func init() {
	sessionsCmd.AddCommand(sessionsResumeCmd)
	sessionsCmd.AddCommand(sessionsResolveGapCmd)

	sessionsResumeCmd.Flags().DurationVar(&sessionsResumeTimeout, "timeout", 10*time.Minute,
		"How long to wait for the resumed turn, which runs LLM and tool calls")
	sessionsResolveGapCmd.Flags().StringVar(&resolveGapOutcome, "outcome", "", "succeeded or failed")
	sessionsResolveGapCmd.Flags().StringVar(&resolveGapResult, "result", "", "The call's result as JSON (required with --outcome succeeded)")
}

func runSessionsResumeCommand(cmd *cobra.Command, args []string) {
	sessionID := args[0]
	c, err := client.NewClient(loomClientConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s: %v\n", serverAddr, err)
		os.Exit(1)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), sessionsResumeTimeout)
	defer cancel()
	resp, err := c.ResumeSession(ctx, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resuming session: %v\n", err)
		os.Exit(1)
	}
	text, ok := formatResumeResponse(sessionID, resp)
	if !ok {
		fmt.Fprint(os.Stderr, text)
		os.Exit(1)
	}
	fmt.Print(text)
}

func runSessionsResolveGapCommand(cmd *cobra.Command, args []string) {
	sessionID := args[0]
	outcome, err := parseGapOutcome(resolveGapOutcome, resolveGapResult)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	c, err := client.NewClient(loomClientConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s: %v\n", serverAddr, err)
		os.Exit(1)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.ResolveExecutionGap(ctx, sessionID, outcome, resolveGapResult)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving the execution gap: %v\n", err)
		os.Exit(1)
	}
	gap := resp.GetGap()
	fmt.Printf("Recorded: tool %q (call %s, step %d, position %d) %s.\n",
		gap.GetToolName(), gap.GetToolUseId(), gap.GetStepIndex(), gap.GetBatchPosition(), resolveGapOutcome)
	fmt.Printf("Run 'loom sessions resume %s' to continue the turn.\n", sessionID)
}

// parseGapOutcome validates the resolve-gap flags.
func parseGapOutcome(outcome, resultJSON string) (loomv1.ExecutionGapOutcome, error) {
	switch outcome {
	case "succeeded":
		if resultJSON == "" {
			return 0, fmt.Errorf("--result is required with --outcome succeeded")
		}
		if !json.Valid([]byte(resultJSON)) {
			return 0, fmt.Errorf("--result must be valid JSON")
		}
		return loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_SUCCEEDED, nil
	case "failed":
		return loomv1.ExecutionGapOutcome_EXECUTION_GAP_OUTCOME_FAILED, nil
	}
	return 0, fmt.Errorf("--outcome must be succeeded or failed, got %q", outcome)
}

// formatResumeResponse renders a ResumeSession result. ok is false when the
// turn did not finish and the user has something to do.
func formatResumeResponse(sessionID string, resp *loomv1.ResumeSessionResponse) (text string, ok bool) {
	switch resp.GetOutcome() {
	case loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_RESUMED:
		return fmt.Sprintf("Resumed session %s.\n\n%s\n", sessionID, resp.GetContent()), true
	case loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_NOTHING_TO_RESUME:
		return fmt.Sprintf("Nothing to resume: the last turn of session %s is complete.\n", sessionID), true
	case loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_PARKED:
		return fmt.Sprintf("Session %s is waiting for a human decision again.\n", sessionID), true
	case loomv1.ResumeSessionOutcome_RESUME_SESSION_OUTCOME_EXECUTION_GAP:
		gap := resp.GetGap()
		return fmt.Sprintf("Execution gap: tool %q (call %s, step %d, position %d) started before the crash, "+
			"but its result was never recorded, so its outcome is unknown.\n"+
			"Check what the call did, then record it:\n"+
			"  loom sessions resolve-gap %s --outcome succeeded --result '<json>'\n"+
			"  loom sessions resolve-gap %s --outcome failed\n"+
			"and run 'loom sessions resume %s' again.\n",
			gap.GetToolName(), gap.GetToolUseId(), gap.GetStepIndex(), gap.GetBatchPosition(),
			sessionID, sessionID, sessionID), false
	}
	return fmt.Sprintf("Unexpected resume outcome %s for session %s.\n", resp.GetOutcome(), sessionID), false
}
