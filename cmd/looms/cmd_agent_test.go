// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	loomagent "github.com/teradata-labs/loom/pkg/agent"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/backend"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// cmd.Execute() is not used here: cobra 1.10.2's Command.ExecuteC()
// unconditionally redirects to c.Root().ExecuteC() whenever the command has
// a parent (see command.go:1089-1092, "Regardless of what command execute is
// called on, run on Root only") — since agentResolveGapCmd is wired under
// agentCmd/rootCmd in this package's init(), cmd.SetArgs on the subcommand is
// silently discarded in favor of rootCmd's own (unset) args, which under `go
// test` fall back to os.Args[1:] full of `-test.*` flags. Verified by running
// the brief's literal Execute()-based test: it printed rootCmd's help and
// returned a nil error instead of the expected required-flag error. Calling
// ParseFlags + ValidateRequiredFlags directly exercises the exact cobra
// machinery (flag parsing, then required-flag validation) that runs just
// before Run in the real CLI, without going through the root-redirect.
func TestRunAgentResolveGap_RequiresOutcomeFlag(t *testing.T) {
	cmd := agentResolveGapCmd
	require.NoError(t, cmd.ParseFlags([]string{"some-session"}))

	err := cmd.ValidateRequiredFlags()
	require.Error(t, err,
		"cobra's MarkFlagRequired must reject a resolve-gap call with no --outcome before runAgentResolveGap ever executes")
	require.Contains(t, err.Error(), "outcome")
}

// TestRunAgentResolveGap_RealStore exercises runAgentResolveGap's actual
// gap-detection/RecordResult logic against a real SQLite-backed
// EventLogStore, mirroring cmd_hitl_test.go's real-store test convention
// (TestRunHitlExpire_RetiresStrandedRow) rather than stubbing the store out.
// It is driven through the Run function directly, not cmd.Execute(): cobra
// 1.10.2 unconditionally redirects Execute() to the root command when the
// target has a parent (see TestRunAgentResolveGap_RequiresOutcomeFlag's
// comment for the verified mechanics), which silently discards
// cmd.SetArgs on a subcommand like agentResolveGapCmd.
func TestRunAgentResolveGap_RealStore(t *testing.T) {
	tests := []struct {
		name        string
		outcome     string
		result      string
		wantSuccess bool
	}{
		{name: "succeeded", outcome: "succeeded", result: `{"data":"ok"}`, wantSuccess: true},
		{name: "failed", outcome: "failed", result: "", wantSuccess: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupIsolatedStorageConfig(t)
			ctx := context.Background()
			tracer := observability.NewNoOpTracer()

			sessionID := "sess-resolve-gap-" + tc.name

			// Seed via one connection, then close it before calling
			// runAgentResolveGap: that function opens its OWN, separate
			// connection to the same on-disk SQLite file, and holding two
			// live connections to one file open concurrently is flaky to
			// tear down on Windows (the pure-Go sqlite driver's file handles
			// do not always release in time for TempDir's own cleanup).
			// Sequential open/close/reopen, as cmd_hitl_test.go's
			// TestRunHitlExpire_RetiresStrandedRow already does, avoids it.
			func() {
				sb, err := backend.NewStorageBackend(ctx, config.BuildProtoStorageConfig(), tracer)
				require.NoError(t, err)
				defer func() { _ = sb.Close() }()
				// execution_events is a versioned migration (000011), not
				// part of the base schema NewSessionStoreWithConfig
				// bootstraps — mirror cmd_serve.go/cmd_workflow.go's
				// AutoMigrate-gated Migrate call, which
				// setupIsolatedStorageConfig enables.
				require.NoError(t, sb.Migrate(ctx))

				elp, ok := sb.(backend.EventLogProvider)
				require.True(t, ok, "SQLite backend must implement EventLogProvider")
				eventLog := elp.EventLogStore()
				require.NotNil(t, eventLog)

				// execution_events.session_id is a FOREIGN KEY REFERENCES
				// sessions(id) — a session row must exist before an event
				// can be recorded against it.
				// resolve-gap names the gap through loomagent.FindExecutionGap,
				// which reads the session's tail batch (a call that already has
				// a tool row is not a gap), so the session needs a real batch
				// with the call whose intent is seeded below.
				require.NoError(t, sb.SessionStorage().SaveSession(ctx, &loomagent.Session{
					ID:        sessionID,
					AgentID:   "test-agent",
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}))
				user := &loomagent.Message{Role: "user", Content: "run it", Timestamp: time.Now()}
				require.NoError(t, sb.SessionStorage().SaveMessage(ctx, sessionID, user, true))
				batch := &loomagent.Message{Role: "assistant", Timestamp: time.Now(), ToolCalls: []llmtypes.ToolCall{
					{ID: "call-1", Name: "some_tool", Input: map[string]interface{}{}},
				}}
				require.NoError(t, sb.SessionStorage().SaveMessage(ctx, sessionID, batch, false))

				// Record the LLM call that produced the batch, as the loop
				// does; resume and resolve-gap act only on a recorded batch.
				llmResult, err := json.Marshal(loomagent.LLMResponse{ToolCalls: batch.ToolCalls})
				require.NoError(t, err)
				require.NoError(t, eventLog.RecordResult(ctx, sessionID, 0, 0,
					eventlog.EventLLMCallResult, "", llmResult))

				// Seed an intent-only execution event: a tool_call_intent
				// with no matching tool_call_result — exactly the gap
				// runAgentResolveGap must find and close.
				require.NoError(t, eventLog.RecordIntent(ctx, sessionID, 0, 0,
					eventlog.EventToolCallIntent, "call-1", []byte(`{"tool_name":"some_tool"}`)))
			}()

			origOutcome, origResult := agentResolveGapOutcome, agentResolveGapResult
			t.Cleanup(func() { agentResolveGapOutcome, agentResolveGapResult = origOutcome, origResult })
			agentResolveGapOutcome = tc.outcome
			agentResolveGapResult = tc.result

			runAgentResolveGap(agentResolveGapCmd, []string{sessionID})

			// Reload via a fresh connection (runAgentResolveGap already
			// closed its own) to verify the write landed.
			sb2, err := backend.NewStorageBackend(ctx, config.BuildProtoStorageConfig(), tracer)
			require.NoError(t, err)
			defer func() { _ = sb2.Close() }()
			eventLog := sb2.(backend.EventLogProvider).EventLogStore()

			events, err := eventLog.LoadEvents(ctx, sessionID)
			require.NoError(t, err)

			var resultEvent *eventlog.ExecutionEvent
			for i := range events {
				if events[i].EventType == eventlog.EventToolCallResult &&
					events[i].StepIndex == 0 && events[i].BatchPosition == 0 {
					resultEvent = &events[i]
				}
			}
			require.NotNil(t, resultEvent,
				"expected a tool_call_result event at (step=0, position=0) resolving the seeded intent")

			var payload struct {
				Result struct {
					Success bool        `json:"success"`
					Data    interface{} `json:"data"`
				} `json:"result"`
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(resultEvent.Payload, &payload))
			require.Equal(t, tc.wantSuccess, payload.Result.Success)
			if tc.wantSuccess {
				require.Empty(t, payload.Error)
			} else {
				require.NotEmpty(t, payload.Error)
			}
		})
	}
}

// TestOpenAgentForSession_RegistryConstruction attempts the resume path's
// registry-construction code directly: a minimal valid agent YAML config
// under <LOOM_DATA_DIR>/agents, a real session row whose AgentID matches it,
// and a direct call to openAgentForSession. This is the only path that
// exercises the RegistryConfig.DBPath wiring fixed in task-8's first round
// (cmd_agent.go's dbPath fallback to <LOOM_DATA_DIR>/loom.db) — before this
// test, that fix was correct by inspection only.
//
// The agent config intentionally omits an LLM provider (the same "empty
// provider" shape pkg/agent/registry_test.go's createTestAgentConfig uses),
// so ValidateAgentConfig accepts it but createLLMProvider fails fast with "no
// LLM provider specified in config" once GetAgent lazily builds the agent —
// a deliberate, unrelated-to-plumbing failure the task brief explicitly
// allows ("even if the agent then fails later for an unrelated reason...").
// What this test actually proves is that none of the earlier stages —
// opening the storage backend, loading the session, constructing the
// registry, or loading agent configs — ever errors.
func TestOpenAgentForSession_RegistryConstruction(t *testing.T) {
	setupIsolatedStorageConfig(t)
	ctx := context.Background()

	agentName := "resume-plumbing-test-agent"
	agentsDir := filepath.Join(loomconfig.GetLoomDataDir(), "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	agentCfg := &loomv1.AgentConfig{
		Name:        agentName,
		Description: "Minimal agent for registry/DBPath plumbing test",
		Llm: &loomv1.LLMConfig{
			// Deliberately empty: see the doc comment above. Passes
			// ValidateAgentConfig (provider/model are optional together) but
			// fails LLM-provider construction, which is fine — see above.
			Provider:    "",
			Model:       "",
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "You are a test assistant",
		Memory: &loomv1.MemoryConfig{
			Type:       "memory",
			MaxHistory: 50,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  10,
			TimeoutSeconds: 300,
		},
	}
	require.NoError(t, loomagent.SaveAgentConfig(agentCfg, filepath.Join(agentsDir, agentName+".yaml")))

	tracer := observability.NewNoOpTracer()
	sessionID := "sess-registry-plumbing-test"

	// Seed via one connection, then close it before calling
	// openAgentForSession: that function opens its OWN separate connection
	// to the same on-disk SQLite file (and, for the registry, a second
	// SQLite file at the same path again — see cmd_agent.go's dbPath
	// fallback). Holding this seeding connection open concurrently with
	// those is flaky to tear down on Windows (the pure-Go sqlite driver's
	// file handles do not always release in time for TempDir's own
	// cleanup); sequential open/close/reopen avoids it, mirroring
	// cmd_hitl_test.go's TestRunHitlExpire_RetiresStrandedRow.
	func() {
		sb, err := backend.NewStorageBackend(ctx, config.BuildProtoStorageConfig(), tracer)
		require.NoError(t, err)
		defer func() { _ = sb.Close() }()
		// openAgentForSession opens its own storage backend connection to
		// the same on-disk SQLite file; migrating here persists the
		// execution_events table to that file so the second connection sees
		// it already applied, exactly as it would after a prior `looms
		// serve` run with auto_migrate enabled.
		require.NoError(t, sb.Migrate(ctx))

		require.NoError(t, sb.SessionStorage().SaveSession(ctx, &loomagent.Session{
			ID:        sessionID,
			AgentID:   agentName,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}))
	}()

	ag, closeAgent, err := openAgentForSession(ctx, sessionID)
	if closeAgent != nil {
		defer closeAgent()
	}

	// The registry/DBPath plumbing itself must never be the failure: none of
	// "opening storage backend", "loading session", "building agent
	// registry", or "loading agent configs" may appear in the error.
	if err != nil {
		for _, forbidden := range []string{
			"opening storage backend",
			"loading session",
			"building agent registry",
			"loading agent configs",
		} {
			require.False(t, strings.Contains(err.Error(), forbidden),
				"registry/DBPath plumbing must not fail (got %q, which contains %q)", err.Error(), forbidden)
		}
		// The only acceptable failure is the deliberate, unrelated LLM
		// provider gap described above, surfacing through GetAgent's
		// "finding agent" wrap.
		require.Contains(t, err.Error(), "finding agent",
			"expected the only possible failure past registry construction to be agent-building (no LLM provider), got: %v", err)
		require.Contains(t, err.Error(), "no LLM provider specified in config")
	} else {
		require.NotNil(t, ag)
	}
}

// TestOpenAgentStorage_NeverMigratedDataDir covers a data directory that has
// never had the execution_events migration applied. Both agent commands open
// storage through openAgentStorage, which follows storage.migration.auto_migrate
// the way `looms serve` does, and otherwise says what to do instead of
// surfacing "no such table: execution_events".
func TestOpenAgentStorage_NeverMigratedDataDir(t *testing.T) {
	ctx := context.Background()
	tracer := observability.NewNoOpTracer()

	t.Run("auto_migrate on migrates and the log is usable", func(t *testing.T) {
		setupIsolatedStorageConfig(t)
		config.Storage.Migration.AutoMigrate = true

		sb, eventLog, err := openAgentStorage(ctx, tracer)
		require.NoError(t, err)
		defer func() { _ = sb.Close() }()

		_, err = eventLog.LoadEvents(ctx, "no-such-session")
		require.NoError(t, err, "the execution log must be readable after auto-migration")
	})

	t.Run("auto_migrate off explains instead of leaking a SQL error", func(t *testing.T) {
		setupIsolatedStorageConfig(t)
		config.Storage.Migration.AutoMigrate = false

		sb, _, err := openAgentStorage(ctx, tracer)
		if sb != nil {
			_ = sb.Close()
		}
		require.Error(t, err)
		require.Contains(t, err.Error(), "auto_migrate",
			"the error must tell the operator how to get a migrated database")
		require.Contains(t, err.Error(), "looms serve")
		require.False(t, strings.HasPrefix(err.Error(), "no such table"),
			"a raw SQL error must not be the headline: %v", err)
	})
}
