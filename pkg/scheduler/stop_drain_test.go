// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// drainBlockingLLM is an LLM provider that never answers. It stands in for the
// hung external call this whole cancellation path exists for: the run is real,
// it holds its schedule's slot, and the only thing that will end it is its
// context being canceled.
type drainBlockingLLM struct {
	started   chan struct{}
	startOnce sync.Once
}

func newDrainBlockingLLM() *drainBlockingLLM {
	return &drainBlockingLLM{started: make(chan struct{})}
}

func (l *drainBlockingLLM) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.startOnce.Do(func() { close(l.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (l *drainBlockingLLM) Name() string  { return "blocking" }
func (l *drainBlockingLLM) Model() string { return "blocking" }

// drainTestScheduler is a scheduler wired to a real registry and orchestrator
// whose single agent blocks, plus the pieces a test needs to inspect it after
// shutdown.
type drainTestScheduler struct {
	scheduler *Scheduler
	llm       *drainBlockingLLM
	dbPath    string
	// stop is idempotent so a test can call it and still leave the cleanup in
	// place: Stop closes the store and closing it twice is not the test's
	// business.
	stop func(ctx context.Context) error
}

// newDrainTestScheduler builds a scheduler whose single agent never answers.
func newDrainTestScheduler(t *testing.T) *drainTestScheduler {
	t.Helper()

	llm := newDrainBlockingLLM()
	h := newAgentTestScheduler(t, llm)
	return &drainTestScheduler{scheduler: h.scheduler, llm: llm, dbPath: h.dbPath, stop: h.stop}
}

// agentTestScheduler is a scheduler wired to a real registry and orchestrator
// with one agent backed by the given provider.
type agentTestScheduler struct {
	scheduler *Scheduler
	dbPath    string
	stop      func(ctx context.Context) error
}

// newAgentTestScheduler builds a scheduler that can actually execute a
// workflow. The orchestrator is real on purpose: what is under test is the
// order of the scheduler's own bookkeeping against a run that is genuinely
// inside ExecutePattern, which a stubbed orchestrator cannot reproduce.
func newAgentTestScheduler(t *testing.T, llm agent.LLMProvider) *agentTestScheduler {
	t.Helper()

	ctx := context.Background()
	logger := zap.NewNop()

	agentDir := t.TempDir()
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		ConfigDir:   agentDir,
		DBPath:      filepath.Join(agentDir, "registry.db"),
		LLMProvider: llm,
		Logger:      logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registry.Close() })

	// Empty provider and model make the registry fall back to the provider
	// above, which is the blocking one.
	registry.RegisterConfig(&loomv1.AgentConfig{
		Name:        drainAgentName,
		Description: "Test agent that never answers",
		Llm: &loomv1.LLMConfig{
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "Respond to the prompt",
		Memory:       &loomv1.MemoryConfig{Type: "memory", MaxHistory: 50},
		Behavior:     &loomv1.BehaviorConfig{MaxIterations: 10, TimeoutSeconds: 300},
	})

	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:    registry,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      logger,
		LLMProvider: llm,
	})

	dbPath := filepath.Join(t.TempDir(), "scheduler.db")
	scheduler, err := NewScheduler(ctx, Config{
		DBPath:       dbPath,
		Orchestrator: orchestrator,
		Registry:     registry,
		Tracer:       observability.NewNoOpTracer(),
		Logger:       logger,
	})
	require.NoError(t, err)

	var (
		once    sync.Once
		stopErr error
	)
	stop := func(stopCtx context.Context) error {
		once.Do(func() { stopErr = scheduler.Stop(stopCtx) })
		return stopErr
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = stop(stopCtx)
	})

	return &agentTestScheduler{scheduler: scheduler, dbPath: dbPath, stop: stop}
}

const drainAgentName = "blocker"

// agentWorkflow returns a schedule whose single stage runs the test agent.
func agentWorkflow(id string) *loomv1.ScheduledWorkflow {
	return &loomv1.ScheduledWorkflow{
		Id:           id,
		WorkflowName: "blocking-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "start",
					Stages: []*loomv1.PipelineStage{
						{AgentId: drainAgentName, PromptTemplate: "go"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:     "0 0 * * *",
			Timezone: "UTC",
			Enabled:  true,
		},
	}
}

// awaitDrainCond fails the test instead of hanging. Every wait here goes
// through it: the failure this file guards against is a shutdown that never
// finishes, and a bare channel receive would report that as a CI timeout with
// nothing to read. The bound is hangGuard, for the reasons given there.
func awaitDrainCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, hangGuard, 5*time.Millisecond, what)
}

// TestStopDrainsInFlightRuns covers F6.
//
// Stop used to close the store as soon as it had signaled the in-flight runs.
// Those runs then spent their entire teardown — four SQLite writes, including
// the history entry an operator opened the view to read — failing against a
// closed database, on a goroutine with nobody to report to. The record of why
// the workflow stopped was the thing lost.
func TestStopDrainsInFlightRuns(t *testing.T) {
	tests := []struct {
		name string
		// launch a run and leave it hung inside the orchestrator.
		inFlight bool
		// grace is what Stop is given before it starts signaling.
		grace time.Duration
		// maxStop bounds how long the whole shutdown may take.
		maxStop    time.Duration
		wantStatus string
		wantError  string
	}{
		{
			name:     "hung run is signaled and its record still lands",
			inFlight: true,
			grace:    200 * time.Millisecond,
			// Far below max_execution_seconds (an hour by default), which is
			// what this proves Stop does not wait out; generous enough not to
			// be a performance assertion on a slow runner.
			maxStop:    hangGuard,
			wantStatus: "canceled",
			wantError:  shutdownCancelReason,
		},
		{
			name: "idle scheduler stops without waiting out its grace period",
			// Nothing is running, so Stop must return long before this
			// deadline rather than sitting on it.
			grace:   10 * time.Second,
			maxStop: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			h := newDrainTestScheduler(t)
			s := h.scheduler

			schedule := agentWorkflow("sched-drain")
			require.NoError(t, s.AddSchedule(ctx, schedule))

			var executionID string
			if tt.inFlight {
				var err error
				executionID, err = s.TriggerNow(ctx, schedule.Id, false, nil)
				require.NoError(t, err)

				select {
				case <-h.llm.started:
				case <-time.After(hangGuard):
					t.Fatal("the workflow never reached the agent, so nothing was in flight to drain")
				}
				awaitDrainCond(t, "the run should be advertised as cancellable", func() bool {
					return s.RunningExecutions()[schedule.Id] == executionID
				})
			}

			stopCtx, cancel := context.WithTimeout(ctx, tt.grace)
			defer cancel()

			start := time.Now()
			require.NoError(t, h.stop(stopCtx))
			elapsed := time.Since(start)
			assert.Less(t, elapsed, tt.maxStop,
				"Stop took %s; it should not outlast the runs it canceled", elapsed)

			// A trigger after shutdown must be refused, not started: the store
			// it would write to is closed.
			_, err := s.TriggerNow(ctx, schedule.Id, false, nil)
			require.Error(t, err, "TriggerNow started a run after the store was closed")
			assert.Contains(t, err.Error(), "stopped")

			if !tt.inFlight {
				return
			}

			// Read the outcome back through a fresh store: Stop closed the one
			// the scheduler held, so anything still visible here was written
			// before the database went away.
			reopened, err := NewStore(ctx, h.dbPath, zap.NewNop())
			require.NoError(t, err)
			defer func() { _ = reopened.Close() }()

			history, err := reopened.GetExecutionHistory(ctx, schedule.Id, 10)
			require.NoError(t, err)
			require.Len(t, history, 1,
				"the canceled run recorded no history; the store closed while it was still writing")
			assert.Equal(t, executionID, history[0].ExecutionId)
			assert.Equal(t, tt.wantStatus, history[0].Status)
			assert.Equal(t, tt.wantError, history[0].Error)

			got, err := reopened.Get(ctx, schedule.Id)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Stats.LastStatus)
			assert.Equal(t, tt.wantError, got.Stats.LastError)
			// A run the scheduler stopped on its way down reached no verdict,
			// so it must not move the counters a success rate is derived from.
			assert.Equal(t, int32(0), got.Stats.TotalExecutions)
			assert.Equal(t, int32(0), got.Stats.SuccessfulExecutions)
			assert.Equal(t, int32(0), got.Stats.FailedExecutions)
			assert.Empty(t, got.CurrentExecutionId,
				"current_execution_id still names a run that is over")
		})
	}
}

// TestStopIsIdempotent covers N5: a second Stop call must not panic. Stop
// closes s.stopCh unconditionally; without a guard on s.stopped, calling Stop
// twice — a plausible shutdown-path mistake, not just a test artifact — closes
// an already-closed channel and panics instead of returning cleanly.
func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	ctx := context.Background()

	require.NoError(t, s.Stop(ctx))
	assert.NotPanics(t, func() {
		require.NoError(t, s.Stop(ctx))
	})
}
