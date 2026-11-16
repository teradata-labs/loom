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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/tui/client"
)

var (
	compactKeepTurns int32
	compactFocus     string
	compactTimeout   time.Duration
)

var sessionsCompactCmd = &cobra.Command{
	Use:   "compact <session-id>",
	Short: "Shrink a long conversation's context between turns",
	Long: `Compact a session's context so a long conversation can keep going.

Older tool results become stubs (the agent can fetch them again) and older
turns are folded into the session's running summary, which takes one LLM
call. The system prompt, the summary and the newest --keep-turns turns stay
as they are. Run it between turns: it is refused while a turn is running or
while the session waits for a human decision.

Examples:
  loom sessions compact sess_abc123def456
  loom sessions compact sess_abc123def456 --focus "keep the final SQL and the open approvals"
  loom sessions compact sess_abc123def456 --keep-turns 4
`,
	Args: cobra.ExactArgs(1),
	Run:  runSessionsCompactCommand,
}

func init() {
	sessionsCmd.AddCommand(sessionsCompactCmd)
	sessionsCompactCmd.Flags().Int32Var(&compactKeepTurns, "keep-turns", 0, "Newest turns to keep verbatim (0 = server default, 2)")
	sessionsCompactCmd.Flags().StringVar(&compactFocus, "focus", "", "What the summary must keep, in priority order (at most 2000 bytes)")
	sessionsCompactCmd.Flags().DurationVar(&compactTimeout, "timeout", 3*time.Minute, "How long to wait; folding makes an LLM call")
}

func runSessionsCompactCommand(cmd *cobra.Command, args []string) {
	sessionID := args[0]
	c, err := client.NewClient(loomClientConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to Loom server at %s: %v\n", serverAddr, err)
		os.Exit(1)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
	defer cancel()
	resp, err := c.CompactSession(ctx, sessionID, compactKeepTurns, compactFocus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error compacting session: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(formatCompactResponse(sessionID, resp))
}

// formatCompactResponse renders a CompactSession result.
func formatCompactResponse(sessionID string, resp *loomv1.CompactSessionResponse) string {
	if !resp.GetCompacted() {
		return fmt.Sprintf("Nothing to compact: session %s has only its newest %d turns in context.\n",
			sessionID, resp.GetTurnsKept())
	}
	// A server built from this branch folds only and never sets
	// tool_results_stubbed. An older server can still set it, so the CLI
	// keeps the wording for it.
	var done string
	switch {
	case resp.GetToolResultsStubbed() && resp.GetTurnsFolded():
		done = "older tool results stubbed and older turns folded into the summary"
	case resp.GetTurnsFolded():
		done = "older turns folded into the summary"
	default:
		done = "older tool results stubbed"
	}
	return fmt.Sprintf("Compacted session %s: %s; newest %d turns kept.\nContext: about %d tokens before, %d after.\n",
		sessionID, done, resp.GetTurnsKept(), resp.GetTokensBefore(), resp.GetTokensAfter())
}
