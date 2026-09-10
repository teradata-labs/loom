// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// failingSchedule returns a schedule whose pattern is rejected by the
// orchestrator before it runs anything.
//
// The verdict is a deterministic configuration error ("pipeline has no
// stages") with nothing to do with cancellation, which is what makes it useful:
// any run recorded as canceled that this schedule produced is a run whose real
// verdict was thrown away.
func failingSchedule(id string) *loomv1.ScheduledWorkflow {
	return &loomv1.ScheduledWorkflow{
		Id:           id,
		WorkflowName: id,
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}
}

// waitFor spins until cond holds, failing the test rather than hanging forever.
//
// Every wait in this file goes through here on purpose: a lock-ordering
// regression in the cancel path deadlocks, and a bare channel receive would
// turn that into a CI timeout with no indication of which invariant broke.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Microsecond)
	}
}

// seedRun registers an in-flight run directly, for the cases that are about
// CancelExecution's own bookkeeping rather than about a real execution.
func seedRun(s *Scheduler, scheduleID, execID string, cancel context.CancelFunc, settled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[execID] = &runState{scheduleID: scheduleID, cancel: cancel, settled: settled}
}

func TestCancelExecutionOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seed prepares scheduler state and returns the ID to cancel.
		seed        func(t *testing.T, s *Scheduler) string
		wantOutcome CancelOutcome
		wantErr     bool
		// wantSignaled asserts whether the run's context was actually canceled.
		wantSignaled bool
	}{
		{
			name: "in flight run is signaled",
			seed: func(t *testing.T, s *Scheduler) string {
				_, cancel := context.WithCancel(context.Background())
				seedRun(s, "sched-1", "exec-1", cancel, false)
				return "exec-1"
			},
			wantOutcome:  CancelOutcomeSignaled,
			wantSignaled: true,
		},
		{
			name: "settled run reports already finished",
			seed: func(t *testing.T, s *Scheduler) string {
				_, cancel := context.WithCancel(context.Background())
				seedRun(s, "sched-1", "exec-1", cancel, true)
				return "exec-1"
			},
			// The run has read its verdict; signaling now would corrupt it.
			wantOutcome:  CancelOutcomeAlreadyFinished,
			wantSignaled: false,
		},
		{
			name: "execution in history reports already finished",
			seed: func(t *testing.T, s *Scheduler) string {
				ctx := context.Background()
				sched := failingSchedule("sched-history")
				require.NoError(t, s.store.Create(ctx, sched))
				require.NoError(t, s.store.RecordExecution(ctx, &loomv1.ScheduleExecution{
					ExecutionId: "exec-done",
					Status:      "success",
				}, sched.Id))
				return "exec-done"
			},
			wantOutcome: CancelOutcomeAlreadyFinished,
		},
		{
			name: "unknown id reports not found rather than already finished",
			// The failure mode this guards: an execution ID from
			// ExecuteWorkflow/StreamWorkflow is a different namespace, and
			// telling an operator their live workflow "already finished" is a
			// lie they would act on.
			seed: func(t *testing.T, s *Scheduler) string {
				return "exec-from-another-namespace"
			},
			wantOutcome: CancelOutcomeNotFound,
		},
		{
			name: "empty id is rejected",
			seed: func(t *testing.T, s *Scheduler) string {
				return ""
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupTestScheduler(t)
			execID := tt.seed(t, s)

			// Track whether the run's context actually got canceled.
			signaled := false
			if run, ok := s.runs[execID]; ok {
				inner := run.cancel
				run.cancel = func() { signaled = true; inner() }
			}

			outcome, err := s.CancelExecution(context.Background(), execID, "operator stop")

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOutcome, outcome,
				"outcome should be %s, got %s", tt.wantOutcome, outcome)
			assert.Equal(t, tt.wantSignaled, signaled,
				"whether the run's context was canceled")
		})
	}
}

func TestCancelExecutionRecordsReasonBeforeSignaling(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)

	// Read the reason from inside the cancel func, which is what executeWorkflow
	// effectively does when its context unwinds. If the reason were written
	// after the signal, the classification could see a canceled context with no
	// explanation and record the stop as a crash.
	var (
		seenReason string
		seenOK     bool
	)
	seedRun(s, "sched-1", "exec-1", nil, false)
	s.mu.Lock()
	s.runs["exec-1"].cancel = func() {
		// RLock here also proves cancel() runs outside s.mu: were it called
		// under the write lock, this would deadlock rather than fail.
		s.mu.RLock()
		defer s.mu.RUnlock()
		run := s.runs["exec-1"]
		seenReason, seenOK = run.reason, run.canceled
	}
	s.mu.Unlock()

	outcome, err := s.CancelExecution(context.Background(), "exec-1", "operator stop")
	require.NoError(t, err)
	require.Equal(t, CancelOutcomeSignaled, outcome)

	assert.True(t, seenOK, "cancel flag must be set before the context is canceled")
	assert.Equal(t, "operator stop", seenReason,
		"reason must be readable before the context is canceled")
}

func TestCancelExecutionFirstReasonWins(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	_, cancel := context.WithCancel(context.Background())
	seedRun(s, "sched-1", "exec-1", cancel, false)

	ctx := context.Background()
	first, err := s.CancelExecution(ctx, "exec-1", "stopped by alice")
	require.NoError(t, err)
	require.Equal(t, CancelOutcomeSignaled, first)

	second, err := s.CancelExecution(ctx, "exec-1", "stopped by bob")
	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeSignaled, second,
		"a second cancel is idempotent for the caller")

	s.mu.RLock()
	defer s.mu.RUnlock()
	assert.Equal(t, "stopped by alice", s.runs["exec-1"].reason,
		"the history should name the operator whose signal stopped the run, not whoever asked last")
}

func TestRunningExecutions(t *testing.T) {
	t.Parallel()

	t.Run("excludes settled runs so every entry is still cancellable", func(t *testing.T) {
		t.Parallel()

		s := setupTestScheduler(t)
		seedRun(s, "sched-live", "exec-live", func() {}, false)
		seedRun(s, "sched-settled", "exec-settled", func() {}, true)

		got := s.RunningExecutions()

		assert.Equal(t, map[string]string{"sched-live": "exec-live"}, got,
			"a settled run would answer a stop button with \"already finished\"")

		// Everything advertised must actually be cancellable.
		for _, execID := range got {
			outcome, err := s.CancelExecution(context.Background(), execID, "check")
			require.NoError(t, err)
			assert.Equal(t, CancelOutcomeSignaled, outcome,
				"execution %s was advertised as running but is not cancellable", execID)
		}
	})

	t.Run("returns a copy", func(t *testing.T) {
		t.Parallel()

		s := setupTestScheduler(t)
		seedRun(s, "sched-1", "exec-1", func() {}, false)

		got := s.RunningExecutions()
		got["sched-1"] = "mutated"

		s.mu.RLock()
		defer s.mu.RUnlock()
		assert.Equal(t, "sched-1", s.runs["exec-1"].scheduleID,
			"RunningExecutions returned the live state, not a copy")
	})
}

// TestExecuteWorkflowRegistersBeforeAdvertising covers F3: there must be no
// window in which a run is advertised as running but answers a cancel with
// "not running".
func TestExecuteWorkflowRegistersBeforeAdvertising(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	ctx := context.Background()
	sched := failingSchedule("sched-advertise")
	require.NoError(t, s.store.Create(ctx, sched))

	// Hold the store lock so the run parks at UpdateCurrentExecution, i.e. in
	// the window between being marked running and reaching the orchestrator.
	s.store.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-advertise", nil)
	}()

	waitFor(t, "the run to be advertised", func() bool {
		return len(s.RunningExecutions()) == 1
	})

	// Advertised. It must therefore be cancellable, while still parked.
	outcome, err := s.CancelExecution(ctx, "exec-advertise", "operator stop")
	s.store.mu.Unlock()

	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeSignaled, outcome,
		"the scheduler advertised this execution, so a stop button offered for it must work")

	waitFor(t, "the run to finish", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

// TestCancelAfterVerdictReportsAlreadyFinished covers the "false signaled"
// half of F1: once a run has read its verdict, a cancel must not claim to have
// signaled it, and must not disturb what was recorded.
func TestCancelAfterVerdictReportsAlreadyFinished(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	ctx := context.Background()
	sched := failingSchedule("sched-late-cancel")
	require.NoError(t, s.store.Create(ctx, sched))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-late", nil)
	}()

	// settled is set in the same critical section that reads the verdict, so
	// observing it is proof the orchestrator has already returned.
	waitFor(t, "the run to read its verdict", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		run, ok := s.runs["exec-late"]
		return ok && run.settled
	})

	outcome, err := s.CancelExecution(ctx, "exec-late", "operator stop")
	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeAlreadyFinished, outcome,
		"the run had already reached its verdict; reporting it as signaled tells the operator a run was stopped when it was not")

	<-done

	// The run's own verdict must be intact.
	history, err := s.store.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "failed", history[0].Status,
		"a configuration failure was relabelled as an operator stop")
	assert.Contains(t, history[0].Error, "stages",
		"the real error was replaced by the cancel reason")

	got, err := s.store.Get(ctx, sched.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), got.Stats.FailedExecutions,
		"the failure was dropped from the counters the success rate is derived from")
	assert.Equal(t, "failed", got.Stats.LastStatus)
}

// answeringLLM answers at once and counts how often it was asked, so a test can
// tell whether the workflow actually completed.
type answeringLLM struct {
	calls atomic.Int32
}

func (l *answeringLLM) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.calls.Add(1)
	return &llmtypes.LLMResponse{Content: "done", StopReason: "stop"}, nil
}

func (l *answeringLLM) Name() string  { return "answering" }
func (l *answeringLLM) Model() string { return "answering" }

// TestCancelAfterSuccessKeepsTheSuccess covers the verdict-erasure half of F1
// with a run that genuinely succeeds.
//
// The failing-pattern sibling above proves a late cancel cannot relabel a
// failure. This one proves it cannot relabel a success, which is the worse
// outcome: a routine that just did its job would show up as stopped by an
// operator, with successful_executions never moving. The agent answers and the
// pipeline completes; only then does the cancel arrive.
func TestCancelAfterSuccessKeepsTheSuccess(t *testing.T) {
	t.Parallel()

	llm := &answeringLLM{}
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	sched := agentWorkflow("sched-late-success")
	require.NoError(t, s.store.Create(ctx, sched))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-late-success", nil)
	}()

	waitFor(t, "the run to read its verdict", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		run, ok := s.runs["exec-late-success"]
		return ok && run.settled
	})
	require.Positive(t, llm.calls.Load(), "the agent was never asked; the run did not actually complete")

	outcome, err := s.CancelExecution(ctx, "exec-late-success", "operator stop")
	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeAlreadyFinished, outcome)

	<-done

	history, err := s.store.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "success", history[0].Status,
		"a run that completed was relabelled as an operator stop")
	assert.Empty(t, history[0].Error)

	got, err := s.store.Get(ctx, sched.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), got.Stats.SuccessfulExecutions,
		"the success was dropped from the counters the success rate is derived from")
	assert.Equal(t, int32(1), got.Stats.TotalExecutions)
	assert.Equal(t, "success", got.Stats.LastStatus)
}

// TestExecuteWorkflowRecordsGenuineCancellation is the positive case: a cancel
// that really does stop the run is recorded as canceled and leaves the
// success-rate counters alone.
func TestExecuteWorkflowRecordsGenuineCancellation(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	ctx := context.Background()
	sched := failingSchedule("sched-genuine")
	require.NoError(t, s.store.Create(ctx, sched))

	// Park the run before the orchestrator runs, so the cancel provably lands
	// while it is still cancellable.
	s.store.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-genuine", nil)
	}()

	waitFor(t, "the run to register", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.runs["exec-genuine"]
		return ok
	})

	outcome, err := s.CancelExecution(ctx, "exec-genuine", "stopped by operator")
	s.store.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, CancelOutcomeSignaled, outcome)

	<-done

	history, err := s.store.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "canceled", history[0].Status)
	assert.Equal(t, "stopped by operator", history[0].Error)

	got, err := s.store.Get(ctx, sched.Id)
	require.NoError(t, err)
	assert.Equal(t, "canceled", got.Stats.LastStatus,
		"last_status must move off the previous run, or a stopped routine reports itself as having last succeeded")
	assert.Equal(t, int32(0), got.Stats.TotalExecutions,
		"a canceled run reached no verdict and must not count as an execution")
	assert.Equal(t, int32(0), got.Stats.FailedExecutions,
		"an operator's stop is not a failure; counting it would distort the success rate")
}

// TestCancelRacingCompletionKeepsRecordAndCountersConsistent fires a cancel at
// an unsynchronised point around completion, many times, and asserts that the
// history record and the counters agree on every interleaving.
//
// This is the shape of test the race detector cannot substitute for: the bug
// class is a logical interleaving, not a data race, so -race stays green while
// the two disagree. It checks consistency, not which verdict was right — the
// two deterministic tests above pin the verdict for a cancel that lands after
// completion.
func TestCancelRacingCompletionKeepsRecordAndCountersConsistent(t *testing.T) {
	t.Parallel()

	const iterations = 200

	s := setupTestScheduler(t)
	ctx := context.Background()

	var (
		canceledRuns int
		failedRuns   int
	)

	for i := 0; i < iterations; i++ {
		// A fresh schedule per iteration, so each one's counters and history
		// are judged on their own rather than needing a reset.
		sched := failingSchedule(fmt.Sprintf("sched-race-%d", i))
		require.NoError(t, s.store.Create(ctx, sched))

		execID := fmt.Sprintf("exec-race-%d", i)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			s.executeWorkflow(ctx, sched, execID, nil)
		}()

		go func() {
			defer wg.Done()
			// Unsynchronised on purpose: across iterations this lands before,
			// during and after the verdict. The run may register and tear down
			// before this goroutine ever observes it, so the spin is bounded —
			// a missed run is a legitimate interleaving, not a failure.
			deadline := time.Now().Add(2 * time.Second)
			for {
				s.mu.RLock()
				_, tracked := s.runs[execID]
				s.mu.RUnlock()
				if tracked || time.Now().After(deadline) {
					break
				}
			}
			_, _ = s.CancelExecution(ctx, execID, "operator stop")
		}()

		wg.Wait()

		history, err := s.store.GetExecutionHistory(ctx, sched.Id, 1)
		require.NoError(t, err)
		require.Len(t, history, 1, "iteration %d recorded no history", i)

		got, err := s.store.Get(ctx, sched.Id)
		require.NoError(t, err)

		switch history[0].Status {
		case "canceled":
			canceledRuns++
			// A canceled run reached no verdict: no counters, and the reason
			// rather than an orchestrator error.
			require.Equal(t, int32(0), got.Stats.FailedExecutions,
				"iteration %d: canceled run counted as a failure", i)
			require.Equal(t, int32(0), got.Stats.TotalExecutions,
				"iteration %d: canceled run counted as an execution", i)

		case "failed":
			failedRuns++
			// The cancel did not stop this run, so its real failure must be
			// recorded in full — this is the assertion that fails when the run
			// stays cancellable through its bookkeeping tail.
			require.Contains(t, history[0].Error, "stages",
				"iteration %d: real error replaced by the cancel reason", i)
			require.Equal(t, int32(1), got.Stats.FailedExecutions,
				"iteration %d: failure dropped from the counters", i)
			require.Equal(t, "failed", got.Stats.LastStatus,
				"iteration %d: last_status does not match the recorded outcome", i)

		default:
			t.Fatalf("iteration %d: unexpected status %q", i, history[0].Status)
		}
	}

	// Both interleavings must actually have occurred, or the test proved nothing.
	t.Logf("canceled=%d failed=%d of %d iterations", canceledRuns, failedRuns, iterations)
	assert.Positive(t, canceledRuns+failedRuns)
}

// TestStopCancelsInFlightRunsOnDeadline covers F6: Stop must not close the store
// out from under runs that are still writing to it.
func TestStopCancelsInFlightRunsOnDeadline(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	_, cancel := context.WithCancel(context.Background())

	signaled := make(chan struct{})
	seedRun(s, "sched-1", "exec-1", func() { close(signaled); cancel() }, false)
	// A settled run is past cancelling; draining must skip it rather than
	// signaling a context whose run has already recorded its verdict.
	seedRun(s, "sched-2", "exec-2", func() { t.Error("settled run was canceled during drain") }, true)

	// An already-expired context puts Stop straight onto its deadline branch.
	expired, expiredCancel := context.WithCancel(context.Background())
	expiredCancel()

	require.NoError(t, s.Stop(expired))

	select {
	case <-signaled:
	default:
		t.Error("Stop hit its deadline without cancelling the in-flight run")
	}
}
