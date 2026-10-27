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
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	loomagent "github.com/teradata-labs/loom/pkg/agent"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/backend"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Crash-resumable agent session management",
	Long: `Manage durable execution and crash-resume for agent conversation loops
(issue #229).

Resume works at tool-call granularity: a call whose result is already durably
recorded is backfilled instead of re-executed, and a call that never ran is
dispatched. The LLM calls of an interrupted turn are not replayed — a crash
before the assistant message persisted costs a fresh LLM call on resume.

Examples:
  # Resume a session after a process crash
  looms agent resume sess-abc123

  # Manually resolve an execution gap (a tool call whose outcome is unknown)
  looms agent resolve-gap sess-abc123 --outcome=succeeded --result '{"data":"..."}'
  looms agent resolve-gap sess-abc123 --outcome=failed`,
}

var agentResumeCmd = &cobra.Command{
	Use:   "resume [session-id]",
	Short: "Resume a session after a crash, completing any interrupted tool-call batch",
	Long: `Resume completes an interrupted conversation-loop turn without
re-executing a tool call whose outcome is already durably known: such a call
is backfilled from the recorded result instead of being invoked again. A tool
call that was never attempted is dispatched normally.

The turn's LLM calls are NOT replayed from the event log — a crash before the
assistant message persisted means resume makes a fresh (billed) LLM call.

If a tool call's outcome is genuinely unknown (a crash happened mid-call),
this reports an execution gap and does nothing further — resolve it first with
'looms agent resolve-gap', then run resume again.`,
	Args: cobra.ExactArgs(1),
	Run:  runAgentResume,
}

var agentResolveGapCmd = &cobra.Command{
	Use:   "resolve-gap [session-id]",
	Short: "Manually resolve an execution gap so resume can proceed",
	Args:  cobra.ExactArgs(1),
	Run:   runAgentResolveGap,
}

var (
	agentResolveGapOutcome string
	agentResolveGapResult  string
)

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentResumeCmd)
	agentCmd.AddCommand(agentResolveGapCmd)

	agentResolveGapCmd.Flags().StringVar(&agentResolveGapOutcome, "outcome", "", "Manually determined outcome: succeeded or failed")
	agentResolveGapCmd.Flags().StringVar(&agentResolveGapResult, "result", "", "Result data as JSON (required when --outcome=succeeded)")
	_ = agentResolveGapCmd.MarkFlagRequired("outcome")
}

// openAgentForSession loads the session's owning agent from the config
// registry (the same registry `looms serve` builds agents from), so
// ResumeSession has a fully-wired *Agent (LLM provider, tools, event log)
// rather than a bare storage handle. The session's own AgentID (set at
// creation, see types.Session.AgentID) picks which config to build — the
// operator does not need to know or pass it.
// openAgentStorage opens the storage backend the agent commands work against
// and makes sure its execution log is usable, so a data directory that has
// never been migrated yields a clear instruction instead of a raw
// "no such table: execution_events" SQL error.
//
// Migration follows storage.migration.auto_migrate exactly as `looms serve` and
// `looms workflow` do: an operator who can start the server can also run
// resume, and one who has turned auto-migration off is never surprised by a CLI
// command altering their schema. With it off and the table missing, the error
// says so rather than migrating behind their back.
func openAgentStorage(ctx context.Context, tracer observability.Tracer) (backend.StorageBackend, eventlog.EventLogStore, error) {
	storageBackend, err := backend.NewStorageBackend(ctx, config.BuildProtoStorageConfig(), tracer)
	if err != nil {
		return nil, nil, fmt.Errorf("opening storage backend: %w", err)
	}
	if config != nil && config.Storage.Migration.AutoMigrate {
		if err := storageBackend.Migrate(ctx); err != nil {
			_ = storageBackend.Close()
			return nil, nil, fmt.Errorf("running storage migrations: %w", err)
		}
	}
	provider, ok := any(storageBackend).(backend.EventLogProvider)
	if !ok || provider.EventLogStore() == nil {
		_ = storageBackend.Close()
		return nil, nil, fmt.Errorf("this storage backend does not provide an execution log")
	}
	eventLog := provider.EventLogStore()
	// A read of an empty session id touches the execution_events table without
	// needing any data: it succeeds on a migrated database and fails on one
	// that has never had the migration applied.
	if _, err := eventLog.NextStepIndex(ctx, ""); err != nil {
		_ = storageBackend.Close()
		return nil, nil, fmt.Errorf("the execution log is not readable (%v) -- if this database has never been migrated, run `looms serve` once or set storage.migration.auto_migrate to true", err)
	}
	return storageBackend, eventLog, nil
}

func openAgentForSession(ctx context.Context, sessionID string) (*loomagent.Agent, func(), error) {
	tracer := createTracerFromConfig()

	storageBackend, eventLog, err := openAgentStorage(ctx, tracer)
	if err != nil {
		return nil, nil, err
	}
	closeBackend := func() { _ = storageBackend.Close() }

	sess, err := storageBackend.SessionStorage().LoadSession(ctx, sessionID)
	if err != nil || sess == nil {
		closeBackend()
		if err == nil {
			err = fmt.Errorf("session not found: %s", sessionID)
		}
		return nil, nil, fmt.Errorf("loading session: %w", err)
	}

	// Registry.DBPath is the registry's own agent-config/instance-tracking
	// SQLite database — a separate concern from the session/event-log
	// storage backend opened above. Derive it the same way cmd_serve.go and
	// cmd_workflow.go do; NewRegistry (registry.go) unconditionally calls
	// OpenDB on this path, so it must never be left empty.
	dbPath := config.Database.Path
	if dbPath == "" {
		dbPath = filepath.Join(loomconfig.GetLoomDataDir(), "loom.db")
	}

	registry, err := loomagent.NewRegistry(loomagent.RegistryConfig{
		ConfigDir:    loomconfig.GetLoomDataDir(),
		DBPath:       dbPath,
		LLMProvider:  nil, // per-agent provider comes from each agent's own config; see Registry.LoadAgents
		Logger:       nil,
		Tracer:       tracer,
		SessionStore: storageBackend.SessionStorage(),
	})
	if err != nil {
		closeBackend()
		return nil, nil, fmt.Errorf("building agent registry: %w", err)
	}
	// Registry.Close() releases the registry's own SQLite handle and its
	// fsnotify watcher goroutine — a separate resource from storageBackend.
	// From this point on (registry successfully constructed), every exit
	// path — error or success — must close both, not just the backend.
	closeAll := func() {
		_ = registry.Close()
		closeBackend()
	}

	if err := registry.LoadAgents(ctx); err != nil {
		closeAll()
		return nil, nil, fmt.Errorf("loading agent configs: %w", err)
	}

	ag, err := registry.GetAgent(ctx, sess.AgentID)
	if err != nil {
		closeAll()
		return nil, nil, fmt.Errorf("finding agent %q for session %s: %w", sess.AgentID, sessionID, err)
	}

	// The registry-built agent was constructed without WithEventLog (the
	// registry does not know about this feature yet in phase 1 — wiring
	// WithEventLog into Registry.LoadAgents's own agent construction is a
	// natural follow-up once this CLI path proves the primitive out, but is
	// deliberately NOT done in this plan to keep this task's blast radius to
	// the CLI file alone). Reach into the agent to set it directly via a
	// package-internal setter.
	ag.SetEventLogForResume(eventLog)

	return ag, closeAll, nil
}

func runAgentResume(cmd *cobra.Command, args []string) {
	sessionID := args[0]
	ctx := context.Background()

	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.agent.resume")
	defer tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	ag, closeAgent, err := openAgentForSession(ctx, sessionID)
	if err != nil {
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer closeAgent()

	resp, err := ag.ResumeSession(ctx, sessionID)
	if err != nil {
		var gap *loomagent.ExecutionGap
		if errors.As(err, &gap) {
			fmt.Fprintf(os.Stderr,
				"⏸  Execution gap — outcome unknown for tool %q (call %s), step %d position %d.\n"+
					"Resolve it first:\n  looms agent resolve-gap %s --outcome=succeeded --result '<json>'\n"+
					"  looms agent resolve-gap %s --outcome=failed\nThen run resume again.\n",
				gap.ToolName, gap.ToolUseID, gap.StepIndex, gap.BatchPosition, sessionID, sessionID)
			os.Exit(1)
		}
		if errors.Is(err, loomagent.ErrNothingParked) {
			fmt.Println("✓ Nothing to resume — this session is already fully consistent.")
			return
		}
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error resuming session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Session %s resumed.\n", sessionID)
	if resp != nil {
		fmt.Printf("  Response: %s\n", resp.Content)
	}
}

func runAgentResolveGap(cmd *cobra.Command, args []string) {
	sessionID := args[0]
	ctx := context.Background()

	switch agentResolveGapOutcome {
	case "succeeded":
		if agentResolveGapResult == "" {
			fmt.Fprintln(os.Stderr, "Error: --result is required when --outcome=succeeded")
			os.Exit(1)
		}
	case "failed":
	default:
		fmt.Fprintf(os.Stderr, "Error: invalid --outcome %q (must be succeeded or failed)\n", agentResolveGapOutcome)
		os.Exit(1)
	}

	tracer := createTracerFromConfig()
	ctx, span := tracer.StartSpan(ctx, "cli.agent.resolve_gap")
	defer tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)
	span.SetAttribute("outcome", agentResolveGapOutcome)

	storageBackend, eventLog, err := openAgentStorage(ctx, tracer)
	if err != nil {
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = storageBackend.Close() }()

	events, err := eventLog.LoadEvents(ctx, sessionID)
	if err != nil {
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error loading events: %v\n", err)
		os.Exit(1)
	}

	// The gap to resolve is whichever one `looms agent resume` reports, and the
	// only way to guarantee that is to ask the same function. It needs the
	// session as well as the events: a call that already has a tool row is not
	// a gap even if its result event is missing, and scanning the event log
	// alone cannot tell the difference (it used to, and could attach the
	// operator's result to a different call than the one resume named).
	sess, err := storageBackend.SessionStorage().LoadSession(ctx, sessionID)
	if err != nil || sess == nil {
		if err == nil {
			err = fmt.Errorf("session not found: %s", sessionID)
		}
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
		os.Exit(1)
	}
	gap := loomagent.FindExecutionGap(sess, events)
	if gap == nil {
		fmt.Fprintln(os.Stderr, "No execution gap found for this session — nothing to resolve.")
		os.Exit(1)
	}
	gapStep, gapPos := gap.StepIndex, gap.BatchPosition

	var payload map[string]interface{}
	if agentResolveGapOutcome == "succeeded" {
		var resultData interface{}
		if err := json.Unmarshal([]byte(agentResolveGapResult), &resultData); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing --result JSON: %v\n", err)
			os.Exit(1)
		}
		payload = map[string]interface{}{
			"result": shuttle.Result{Success: true, Data: resultData},
			"error":  "",
		}
	} else {
		payload = map[string]interface{}{
			"result": shuttle.Result{Success: false, Error: &shuttle.Error{Code: "manually_resolved_failed", Message: "operator resolved this execution gap as failed"}},
			"error":  "operator resolved this execution gap as failed",
		}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building result payload: %v\n", err)
		os.Exit(1)
	}

	if err := eventLog.RecordResult(ctx, sessionID, gapStep, gapPos, eventlog.EventToolCallResult, "", payloadJSON); err != nil {
		span.RecordError(err)
		fmt.Fprintf(os.Stderr, "Error recording resolution: %v\n", err)
		os.Exit(1)
	}

	span.SetAttribute("success", true)
	fmt.Printf("✓ Gap resolved: tool %q (call %s), step %d, position %d, outcome %s.\n", gap.ToolName, gap.ToolUseID, gapStep, gapPos, agentResolveGapOutcome)
	fmt.Printf("  Run `looms agent resume %s` to continue.\n", sessionID)
}
