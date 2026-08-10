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
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

var hitlCmd = &cobra.Command{
	Use:   "hitl",
	Short: "Human-in-the-Loop request management",
	Long: `Manage human-in-the-loop (HITL) requests from agents.

HITL enables agents to request human approval, input, decisions, and reviews
during execution. Use these commands to view pending requests and respond.

Examples:
  # List all pending requests
  looms hitl list

  # List pending requests for a session
  looms hitl list --session sess-123

  # Show details of a specific request
  looms hitl show req-abc123

  # Approve a request
  looms hitl respond req-abc123 --status approved --message "Yes, proceed"

  # Reject a request
  looms hitl respond req-abc123 --status rejected --message "No, do not proceed"`,
}

var hitlListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending HITL requests",
	Long: `List all pending human-in-the-loop requests.

Displays pending requests in a table format with ID, agent, priority, type,
age, and question. Use filters to narrow results by session or agent.

Examples:
  # List all pending requests
  looms hitl list

  # List requests for a specific session
  looms hitl list --session sess-123

  # List requests for a specific agent
  looms hitl list --agent agent-1`,
	Run: runHitlList,
}

var hitlShowCmd = &cobra.Command{
	Use:   "show [request-id]",
	Short: "Show details of a specific HITL request",
	Long: `Show full details of a human-in-the-loop request including context,
status, and response information.

Examples:
  # Show request details
  looms hitl show req-abc123`,
	Args: cobra.ExactArgs(1),
	Run:  runHitlShow,
}

var hitlExpireCmd = &cobra.Command{
	Use:   "expire [request-id]",
	Short: "Terminally close a stranded pending request as timed out",
	Long: `Close a pending request as "timeout" regardless of its expiry.

This is the operator's retirement route for a stranded hold — one whose
waiter died with its turn or process and that no response can ever reach.
A request that is already decided is left untouched (closing is not
resolving).

Examples:
  # Retire a stranded request
  looms hitl expire req-abc123`,
	Args: cobra.ExactArgs(1),
	Run:  runHitlExpire,
}

var hitlRespondCmd = &cobra.Command{
	Use:   "respond [request-id]",
	Short: "Respond to a HITL request",
	Long: `Respond to a human-in-the-loop request with approval, rejection, or input.

Status values:
  - approved: Approve the request (for approval/review types)
  - rejected: Reject the request (for approval/review types)
  - responded: Provide input/decision (for input/decision types)

Examples:
  # Approve a request
  looms hitl respond req-abc123 --status approved --message "Yes, proceed"

  # Reject a request
  looms hitl respond req-abc123 --status rejected --message "No, cancel"

  # Provide input
  looms hitl respond req-abc123 --status responded --message "Use PostgreSQL"

  # Provide structured data
  looms hitl respond req-abc123 --status approved --message "Approved" --data '{"confirmed":true}'`,
	Args: cobra.ExactArgs(1),
	Run:  runHitlRespond,
}

var (
	hitlSessionID   string
	hitlAgentID     string
	hitlStatus      string
	hitlMessage     string
	hitlData        string
	hitlRespondedBy string
	hitlCLIDBPath   string
	hitlUser        string
)

func init() {
	rootCmd.AddCommand(hitlCmd)
	hitlCmd.AddCommand(hitlListCmd)
	hitlCmd.AddCommand(hitlShowCmd)
	hitlCmd.AddCommand(hitlRespondCmd)
	hitlCmd.AddCommand(hitlExpireCmd)

	defaultHITLDB := filepath.Join(loomconfig.GetLoomDataDir(), "hitl.db")

	// Every subcommand opens the SAME store the server writes: the configured
	// storage backend on postgres, the node-local hitl.db on SQLite. --db is a
	// SQLite-only override; --user selects the tenant on postgres.
	for _, c := range []*cobra.Command{hitlListCmd, hitlShowCmd, hitlRespondCmd, hitlExpireCmd} {
		c.Flags().StringVar(&hitlCLIDBPath, "db", defaultHITLDB, "Path to HITL SQLite database (SQLite backend only)")
		c.Flags().StringVar(&hitlUser, "user", "", "Tenant user ID whose requests to operate on (required on the postgres backend)")
	}

	// List command flags
	hitlListCmd.Flags().StringVar(&hitlSessionID, "session", "", "Filter by session ID")
	hitlListCmd.Flags().StringVar(&hitlAgentID, "agent", "", "Filter by agent ID")

	// Respond command flags
	hitlRespondCmd.Flags().StringVar(&hitlStatus, "status", "approved", "Response status (approved, rejected, responded)")
	hitlRespondCmd.Flags().StringVar(&hitlMessage, "message", "", "Response message (required)")
	hitlRespondCmd.Flags().StringVar(&hitlData, "data", "", "Response data as JSON (optional)")
	hitlRespondCmd.Flags().StringVar(&hitlRespondedBy, "by", "", "Who is responding (default: current user)")

	_ = hitlRespondCmd.MarkFlagRequired("message")
}

// openHITLStore opens the store `looms serve` raises holds in, switching on
// the SAME config the server switches on: the configured storage backend when
// it is postgres, the node-local SQLite hitl.db otherwise. Postgres operations
// are tenant-scoped, so that branch requires --user and returns a context
// carrying it; the operator explicitly names whose holds they admit. The
// returned close func releases the store.
func openHITLStore(ctx context.Context, tracer observability.Tracer) (shuttle.HumanRequestStore, func(), context.Context, error) {
	if config != nil && config.Storage.Backend == "postgres" {
		if hitlUser == "" {
			return nil, nil, ctx, fmt.Errorf("the storage backend is postgres: --user is required (it selects the tenant whose requests you operate on)")
		}
		storageBackend, err := backend.NewStorageBackend(ctx, config.BuildProtoStorageConfig(), tracer)
		if err != nil {
			return nil, nil, ctx, fmt.Errorf("failed to open storage backend: %w", err)
		}
		return storageBackend.HumanRequestStore(),
			func() { _ = storageBackend.Close() },
			postgres.ContextWithUserID(ctx, hitlUser),
			nil
	}

	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{
		Path:   hitlCLIDBPath,
		Tracer: tracer,
	})
	if err != nil {
		return nil, nil, ctx, fmt.Errorf("failed to open database: %w", err)
	}
	return store, func() { _ = store.Close() }, ctx, nil
}

// createTracerFromConfig creates a tracer based on the global config.
func createTracerFromConfig() observability.Tracer {
	if config == nil || !config.Observability.Enabled {
		return observability.NewNoOpTracer()
	}

	// Create Hawk tracer if endpoint configured
	if config.Observability.HawkEndpoint != "" {
		tracer, err := observability.NewHawkTracer(observability.HawkConfig{
			Endpoint: config.Observability.HawkEndpoint,
			APIKey:   config.Observability.HawkAPIKey,
		})
		if err != nil {
			// Fall back to no-op tracer on error
			return observability.NewNoOpTracer()
		}
		return tracer
	}

	return observability.NewNoOpTracer()
}

func runHitlList(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// Create tracer
	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.hitl.list")
	defer tracer.EndSpan(span)

	if hitlSessionID != "" {
		span.SetAttribute("session_id", hitlSessionID)
	}
	if hitlAgentID != "" {
		span.SetAttribute("agent_id", hitlAgentID)
	}

	store, closeStore, ctx, err := openHITLStore(ctx, tracer)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeStore()

	var requests []*shuttle.HumanRequest
	if hitlSessionID != "" {
		requests, err = store.ListBySession(ctx, hitlSessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing requests: %v\n", err)
			os.Exit(1)
		}
		// Filter to pending only
		var pending []*shuttle.HumanRequest
		for _, req := range requests {
			if req.Status == "pending" {
				pending = append(pending, req)
			}
		}
		requests = pending
	} else {
		requests, err = store.ListPending(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing pending requests: %v\n", err)
			os.Exit(1)
		}
	}

	// Apply agent filter if specified
	if hitlAgentID != "" {
		var filtered []*shuttle.HumanRequest
		for _, req := range requests {
			if req.AgentID == hitlAgentID {
				filtered = append(filtered, req)
			}
		}
		requests = filtered
	}

	if len(requests) == 0 {
		fmt.Println("No pending requests")
		return
	}

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tAGENT\tPRIORITY\tTYPE\tAGE\tQUESTION")
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 80))

	for _, req := range requests {
		age := time.Since(req.CreatedAt).Round(time.Second)
		question := req.Question
		if len(question) > 50 {
			question = question[:47] + "..."
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n",
			req.ID,
			req.AgentID,
			req.Priority,
			req.RequestType,
			age,
			question,
		)
	}

	// #nosec G104 -- Flush to stdout, no error handling needed
	_ = w.Flush()
	fmt.Printf("\nTotal: %d pending request(s)\n", len(requests))
}

func runHitlShow(cmd *cobra.Command, args []string) {
	requestID := args[0]
	ctx := context.Background()

	// Create tracer
	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.hitl.show")
	defer tracer.EndSpan(span)

	span.SetAttribute("request_id", requestID)

	store, closeStore, ctx, err := openHITLStore(ctx, tracer)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeStore()

	req, err := store.Get(ctx, requestID)
	if err != nil || req == nil {
		span.SetAttribute("success", false)
		if err == nil {
			// The postgres store reports an absent row as (nil, nil).
			err = fmt.Errorf("request not found: %s", requestID)
		}
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error retrieving request: %v\n", err)
		os.Exit(1)
	}

	span.SetAttribute("status", req.Status)
	span.SetAttribute("request_type", req.RequestType)
	span.SetAttribute("priority", req.Priority)
	span.SetAttribute("success", true)

	// Print request details
	fmt.Printf("Request ID:     %s\n", req.ID)
	fmt.Printf("Agent ID:       %s\n", req.AgentID)
	fmt.Printf("Session ID:     %s\n", req.SessionID)
	fmt.Printf("Type:           %s\n", req.RequestType)
	fmt.Printf("Priority:       %s\n", req.Priority)
	fmt.Printf("Status:         %s\n", req.Status)
	fmt.Printf("\n")
	fmt.Printf("Question:       %s\n", req.Question)
	fmt.Printf("\n")

	// Print timing info
	fmt.Printf("Created:        %s (%v ago)\n", req.CreatedAt.Format(time.RFC3339), time.Since(req.CreatedAt).Round(time.Second))
	fmt.Printf("Expires:        %s (in %v)\n", req.ExpiresAt.Format(time.RFC3339), time.Until(req.ExpiresAt).Round(time.Second))
	fmt.Printf("Timeout:        %v\n", req.Timeout)
	fmt.Printf("\n")

	// Print context if present
	if len(req.Context) > 0 {
		fmt.Println("Context:")
		for k, v := range req.Context {
			fmt.Printf("  %s: %v\n", k, v)
		}
		fmt.Printf("\n")
	}

	// Print response info if responded
	if req.Status != "pending" {
		fmt.Printf("Response Status: %s\n", req.Status)
		fmt.Printf("Response:        %s\n", req.Response)
		if req.RespondedBy != "" {
			fmt.Printf("Responded By:    %s\n", req.RespondedBy)
		}
		if req.RespondedAt != nil {
			fmt.Printf("Responded At:    %s (%v ago)\n",
				req.RespondedAt.Format(time.RFC3339),
				time.Since(*req.RespondedAt).Round(time.Second))
		}

		if len(req.ResponseData) > 0 {
			fmt.Println("\nResponse Data:")
			for k, v := range req.ResponseData {
				fmt.Printf("  %s: %v\n", k, v)
			}
		}
	}
}

func runHitlExpire(cmd *cobra.Command, args []string) {
	requestID := args[0]
	ctx := context.Background()

	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.hitl.expire")
	defer tracer.EndSpan(span)
	span.SetAttribute("request_id", requestID)

	store, closeStore, ctx, err := openHITLStore(ctx, tracer)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeStore()

	operator := os.Getenv("USER")
	if operator == "" {
		operator = "operator"
	}
	if err := store.ExpireRequest(ctx, requestID, "operator:"+operator); err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error expiring request: %v\n", err)
		os.Exit(1)
	}

	// Success is judged by the row's STATE, deliberately unlike respond's
	// judged-by-whose-write: retirement is idempotent — a row already closed
	// by the sweep or a previous invocation is the desired end state, and
	// "Closed by" prints the row's true closing actor either way.
	after, err := store.Get(ctx, requestID)
	if err != nil || after == nil {
		span.SetAttribute("success", false)
		if err == nil {
			err = fmt.Errorf("request not found: %s", requestID)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if after.Status != "timeout" {
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "✗ Request NOT expired — it is %q", after.Status)
		if after.RespondedBy != "" {
			fmt.Fprintf(os.Stderr, " (responded by %s)", after.RespondedBy)
		}
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
	span.SetAttribute("success", true)
	fmt.Printf("✓ Request expired\n")
	fmt.Printf("  Request ID: %s\n", after.ID)
	fmt.Printf("  Closed by:  %s\n", after.RespondedBy)
}

func runHitlRespond(cmd *cobra.Command, args []string) {
	requestID := args[0]
	ctx := context.Background()

	// Create tracer
	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.hitl.respond")
	defer tracer.EndSpan(span)

	span.SetAttribute("request_id", requestID)
	span.SetAttribute("status", hitlStatus)

	// Validate --status before touching the store: the store writes the literal
	// verbatim and the resolver admits only the exact literal "approved", so a
	// mistyped status (e.g. "Approved") would be durably recorded yet DENY the
	// held call with the operator's approval message as the deny reason.
	switch hitlStatus {
	case "approved", "rejected", "responded":
	default:
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error: invalid --status %q (must be approved, rejected, or responded)\n", hitlStatus)
		os.Exit(1)
	}

	store, closeStore, ctx, err := openHITLStore(ctx, tracer)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeStore()

	// Parse response data if provided
	var responseData map[string]interface{}
	if hitlData != "" {
		if err := json.Unmarshal([]byte(hitlData), &responseData); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing --data JSON: %v\n", err)
			os.Exit(1)
		}
	}

	// Determine respondedBy
	respondedBy := hitlRespondedBy
	if respondedBy == "" {
		respondedBy = os.Getenv("USER")
		if respondedBy == "" {
			respondedBy = "unknown"
		}
	}

	span.SetAttribute("responded_by", respondedBy)

	// Respond to request. The store's conditional write is a deliberate no-op
	// for an already-decided or expired request, so success is judged by
	// reading the row back — never by the write returning nil.
	err = store.RespondToRequest(ctx, requestID, hitlStatus, hitlMessage, respondedBy, responseData)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Error responding to request: %v\n", err)
		os.Exit(1)
	}

	after, err := store.Get(ctx, requestID)
	if err != nil || after == nil {
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "Response submitted but the request could not be read back: %v\n", err)
		os.Exit(1)
	}
	// Success is judged by WHOSE write the row holds, not by status alone: a
	// lost race against a same-status write by another operator leaves the row
	// decided with the winner's message, and printing this invocation's flags
	// over it would certify a write that never landed.
	if after.Status != hitlStatus || after.RespondedBy != respondedBy || after.Response != hitlMessage {
		span.SetAttribute("success", false)
		fmt.Fprintf(os.Stderr, "✗ Response NOT recorded — request is %q", after.Status)
		if after.RespondedBy != "" {
			fmt.Fprintf(os.Stderr, " (responded by %s: %q)", after.RespondedBy, after.Response)
		}
		if after.Status == "pending" && !after.ExpiresAt.IsZero() && time.Now().After(after.ExpiresAt) {
			fmt.Fprintf(os.Stderr, " — expired at %s", after.ExpiresAt.Format(time.RFC3339))
		}
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}

	span.SetAttribute("success", true)

	// Print the ROW's values, never the flags': the row is what the agent reads.
	fmt.Printf("✓ Response recorded\n")
	fmt.Printf("  Request ID: %s\n", after.ID)
	fmt.Printf("  Status:     %s\n", after.Status)
	fmt.Printf("  Message:    %s\n", after.Response)
	fmt.Printf("  By:         %s\n", after.RespondedBy)
}
