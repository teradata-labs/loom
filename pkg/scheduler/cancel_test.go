// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"fmt"
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

// hangGuard bounds every wait in the scheduler tests.
//
// It is a hang guard, not a performance bound. Its only job is to turn a
// deadlock — a lock-ordering regression in the cancel path, say — into a
// failure that names the invariant, instead of the bare CI timeout a channel
// receive would produce. It is therefore generous: on a CI runner under -race
// and atomic coverage, with the parallel tests in this package competing for
// CPU, a real agent run has been observed to take over four seconds to settle,
// and a guard sized to local timings failed there.
const hangGuard = 30 * time.Second

// waitFor spins until cond holds, failing the test rather than hanging forever.
// Every wait in this file goes through here on purpose; see hangGuard.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(hangGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		// Long enough not to contend with the run being waited on for the
		// scheduler lock; short enough that the wait adds nothing measurable.
		time.Sleep(2 * time.Millisecond)
	}
}

// seedRun registers an in-flight run directly, for the cases that are about
// CancelExecution's own bookkeeping rather than about a real execution.
func seedRun(s *Scheduler, scheduleID, execID string, cancel context.CancelFunc, settled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[execID] = &runState{scheduleID: scheduleID, cancel: cancel, settled: settled}
}

// launchParkedAfterVerdict starts executeWorkflow for sched/execID and returns
// once the run is parked at its first post-verdict store write, with the store
// lock held by the caller. The caller releases s.store.mu to let the run finish
// and then receives from done.
//
// This is deterministic rather than timing-based, which matters: a poll for
// "settled" can miss a fast run entirely — it settles and tears down between
// two polls, and an absent entry is indistinguishable from one not yet
// registered — so the poll spins until the hang guard fires. Every wait below
// is on a state the run cannot leave while it is parked.
//
// Sequence: the store lock is taken before launch, so the run registers and
// parks at UpdateCurrentExecution. The scheduler lock is then taken and the
// store lock released; the run performs that write, runs the orchestrator, and
// blocks at its linearization point. Once current_execution_id is visible in
// the store — proof the run is past that write and will not touch the store
// again before settling — the store lock is retaken and the scheduler lock
// released: the run settles and parks at UpdateLastWorkflowID, provably after
// the verdict, before any bookkeeping write, still in the run map.
func launchParkedAfterVerdict(t *testing.T, s *Scheduler, sched *loomv1.ScheduledWorkflow, execID string) <-chan struct{} {
	t.Helper()
	ctx := context.Background()
	done := make(chan struct{})

	s.store.mu.Lock() // park 1: UpdateCurrentExecution
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, execID, nil, true)
	}()
	waitFor(t, "the run to register", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.runs[execID]
		return ok
	})

	s.mu.Lock()         // the run will block here at its linearization point
	s.store.mu.Unlock() // release park 1
	waitFor(t, "current_execution_id to be persisted", func() bool {
		got, err := s.store.Get(ctx, sched.Id)
		return err == nil && got.CurrentExecutionId == execID
	})

	s.store.mu.Lock() // park 2: UpdateLastWorkflowID
	s.mu.Unlock()     // the run settles, then parks at park 2
	waitFor(t, "the run to read its verdict", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		run, ok := s.runs[execID]
		return ok && run.settled
	})
	return done
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
		s.executeWorkflow(ctx, sched, "exec-advertise", nil, true)
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

	// Parked after the verdict, before any bookkeeping write, still in the map.
	done := launchParkedAfterVerdict(t, s, sched, "exec-late")

	outcome, err := s.CancelExecution(ctx, "exec-late", "operator stop")
	s.store.mu.Unlock()
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

	// Parked after the verdict, before any bookkeeping write, still in the map.
	done := launchParkedAfterVerdict(t, s, sched, "exec-late-success")
	require.Positive(t, llm.calls.Load(), "the agent was never asked; the run did not actually complete")

	outcome, err := s.CancelExecution(ctx, "exec-late-success", "operator stop")
	s.store.mu.Unlock()
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
//
// The run has to be genuinely interrupted mid-flight, not merely signaled
// before an orchestrator call that would have failed on its own regardless —
// see TestCancelBeforeOrchestratorWithUnrelatedFailureIsNotMislabeledCanceled
// for that distinction. A blocking agent call is what makes the interruption
// real: the cancel unwinds context.WithTimeout's context inside Chat, which
// returns ctx.Err() and wraps up through the pipeline as context.Canceled.
func TestExecuteWorkflowRecordsGenuineCancellation(t *testing.T) {
	t.Parallel()

	llm := newDrainBlockingLLM()
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	sched := agentWorkflow("sched-genuine")
	require.NoError(t, s.store.Create(ctx, sched))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-genuine", nil, true)
	}()

	select {
	case <-llm.started:
	case <-time.After(hangGuard):
		t.Fatal("the workflow never reached the agent, so nothing was in flight to cancel")
	}

	outcome, err := s.CancelExecution(ctx, "exec-genuine", "stopped by operator")
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

// TestExecuteWorkflowRecordsGenuineCancellationForForkJoin covers N8: a
// genuine cancel of a fork-join schedule must still be classified as
// "canceled", not "failed". Fork-join and parallel patterns collect every
// branch's error into a local slice before this PR's fix and joined them with
// fmt.Errorf("...: %v", errors) — %v, not %w, so context.Canceled never
// reached errors.Is and the classification switch's canceled branch was
// unreachable for these two pattern types no matter how a run was stopped.
func TestExecuteWorkflowRecordsGenuineCancellationForForkJoin(t *testing.T) {
	t.Parallel()

	llm := newDrainBlockingLLM()
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	sched := forkJoinWorkflow("sched-genuine-forkjoin")
	require.NoError(t, s.store.Create(ctx, sched))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-genuine-forkjoin", nil, true)
	}()

	select {
	case <-llm.started:
	case <-time.After(hangGuard):
		t.Fatal("the workflow never reached the agent, so nothing was in flight to cancel")
	}

	outcome, err := s.CancelExecution(ctx, "exec-genuine-forkjoin", "stopped by operator")
	require.NoError(t, err)
	require.Equal(t, CancelOutcomeSignaled, outcome)

	<-done

	history, err := s.store.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "canceled", history[0].Status,
		"a genuine cancel of a fork-join run was recorded as failed because the joined branch errors lost context.Canceled through %v instead of %w")
	assert.Equal(t, "stopped by operator", history[0].Error)

	got, err := s.store.Get(ctx, sched.Id)
	require.NoError(t, err)
	assert.Equal(t, "canceled", got.Stats.LastStatus)
	assert.Equal(t, int32(0), got.Stats.TotalExecutions)
	assert.Equal(t, int32(0), got.Stats.FailedExecutions,
		"an operator's stop of a fork-join run was counted as a failure, corrupting the success rate")
}

// TestCancelBeforeOrchestratorWithUnrelatedFailureIsNotMislabeledCanceled
// covers N1: a cancel signal that arrives before the orchestrator ever runs
// does not, by itself, mean the orchestrator's error was caused by it. A
// misconfigured pipeline fails the same way whether or not anyone canceled it,
// and that real error must not be discarded and relabeled "canceled" just
// because a signal happened to land first.
func TestCancelBeforeOrchestratorWithUnrelatedFailureIsNotMislabeledCanceled(t *testing.T) {
	t.Parallel()

	s := setupTestScheduler(t)
	ctx := context.Background()
	sched := failingSchedule("sched-unrelated-failure")
	require.NoError(t, s.store.Create(ctx, sched))

	// Park the run before the orchestrator runs, so the cancel provably lands
	// first, then release it to run ExecutePattern against an already-canceled
	// context. The pattern has no stages, so it fails immediately for a reason
	// that has nothing to do with context cancellation.
	s.store.mu.Lock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.executeWorkflow(ctx, sched, "exec-unrelated", nil, true)
	}()

	waitFor(t, "the run to register", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok := s.runs["exec-unrelated"]
		return ok
	})

	outcome, err := s.CancelExecution(ctx, "exec-unrelated", "stopped by operator")
	s.store.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, CancelOutcomeSignaled, outcome)

	<-done

	history, err := s.store.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "failed", history[0].Status,
		"a configuration error unrelated to the cancel signal was relabeled as an operator stop")
	assert.Contains(t, history[0].Error, "stages",
		"the real error was discarded in favor of the cancel reason")

	got, err := s.store.Get(ctx, sched.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), got.Stats.FailedExecutions,
		"the failure was dropped from the counters the success rate is derived from")
	assert.Equal(t, "failed", got.Stats.LastStatus)
}

// controllableLLM parks inside Chat until the test releases it, and reports
// entry so the test never has to guess whether the call has started.
//
// A timing-based race between "cancel" and "let it complete" was tried here
// first and discarded: the run's settle time is dominated by real agent
// dispatch overhead (registry lookup, message assembly, SQLite writes) that
// varies by an order of magnitude between the first call and later ones, so
// no fixed jitter window reliably straddles it — the boundary this test
// exists to exercise cannot be hit by chance without becoming flaky under
// -race on a loaded runner. Parking on a channel makes both interleavings
// exact instead of probable.
type controllableLLM struct {
	entered chan struct{}
	release chan struct{}
}

func newControllableLLM() *controllableLLM {
	return &controllableLLM{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (l *controllableLLM) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.entered <- struct{}{}
	select {
	case <-l.release:
		return &llmtypes.LLMResponse{Content: "done", StopReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *controllableLLM) Name() string  { return "controllable" }
func (l *controllableLLM) Model() string { return "controllable" }

// TestCancelRacingCompletionKeepsRecordAndCountersConsistent pins a cancel
// against a completion at the same point — while the run is genuinely
// in-flight inside the agent call — many times, alternating which one the
// test lets win, and asserts the history record and the counters agree on
// every outcome.
//
// This is the shape of bug the race detector cannot substitute for: the bug
// class is a logical interleaving (does the recorded status match what
// actually happened at the boundary), not a data race, so -race stays green
// while the two disagree. The two deterministic tests above already pin the
// verdict for a cancel landing after the verdict; this one pins it for a
// cancel and a completion contending for the same still-in-flight run, many
// times, to catch a lock-ordering regression a single case could miss. The
// LLM must genuinely honor context cancellation (unlike failingSchedule's
// config-validation error, which never touches ctx and so can never
// legitimately race a cancel — see the N1 regression test above): a signal
// racing an error unrelated to it must never be relabeled canceled.
func TestCancelRacingCompletionKeepsRecordAndCountersConsistent(t *testing.T) {
	t.Parallel()

	// Enough to run the lock-ordering path many times over without starving
	// this parallel test's siblings on a slow runner.
	const iterations = 100

	llm := newControllableLLM()
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	var (
		canceledRuns int
		successRuns  int
	)

	for i := 0; i < iterations; i++ {
		// A fresh schedule per iteration, so each one's counters and history
		// are judged on their own rather than needing a reset.
		sched := agentWorkflow(fmt.Sprintf("sched-race-%d", i))
		require.NoError(t, s.store.Create(ctx, sched))

		execID := fmt.Sprintf("exec-race-%d", i)
		llm.entered = make(chan struct{}, 1)
		llm.release = make(chan struct{})
		wantCancel := i%2 == 0

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.executeWorkflow(ctx, sched, execID, nil, true)
		}()

		select {
		case <-llm.entered:
		case <-time.After(hangGuard):
			t.Fatalf("iteration %d: the run never reached the agent call", i)
		}

		if wantCancel {
			outcome, err := s.CancelExecution(ctx, execID, "operator stop")
			require.NoError(t, err)
			require.Equal(t, CancelOutcomeSignaled, outcome, "iteration %d", i)
		} else {
			close(llm.release)
		}

		<-done

		history, err := s.store.GetExecutionHistory(ctx, sched.Id, 1)
		require.NoError(t, err)
		require.Len(t, history, 1, "iteration %d recorded no history", i)

		got, err := s.store.Get(ctx, sched.Id)
		require.NoError(t, err)

		if wantCancel {
			canceledRuns++
			// A canceled run reached no verdict: no counters, and the reason
			// rather than an orchestrator error.
			require.Equal(t, "canceled", history[0].Status,
				"iteration %d: a run parked inside the agent call, then canceled, must be recorded as canceled", i)
			require.Equal(t, "operator stop", history[0].Error,
				"iteration %d: real error replaced the cancel reason, or vice versa", i)
			require.Equal(t, int32(0), got.Stats.SuccessfulExecutions,
				"iteration %d: canceled run counted as a success", i)
			require.Equal(t, int32(0), got.Stats.TotalExecutions,
				"iteration %d: canceled run counted as an execution", i)
		} else {
			successRuns++
			// The run was let through to completion, so its real success must
			// be recorded in full — this is the assertion that fails when the
			// run stays cancellable through its bookkeeping tail.
			require.Equal(t, "success", history[0].Status,
				"iteration %d: a run released to complete normally must be recorded as a success", i)
			require.Equal(t, int32(1), got.Stats.SuccessfulExecutions,
				"iteration %d: success dropped from the counters", i)
			require.Equal(t, "success", got.Stats.LastStatus,
				"iteration %d: last_status does not match the recorded outcome", i)
		}
	}

	t.Logf("canceled=%d success=%d of %d iterations", canceledRuns, successRuns, iterations)
	assert.Positive(t, canceledRuns)
	assert.Positive(t, successRuns)
}

// TestTriggerNowRegistersBeforeReturningTheID covers N2's transient half: an ID
// TriggerNow just handed back must already be tracked, with no window in which
// canceling it reports NOT_FOUND for an execution this scheduler, in fact,
// just minted.
//
// No synchronization with the spawned goroutine is used on purpose: before the
// fix, the goroutine registered the run itself, so a cancel arriving before it
// was even scheduled to run found nothing tracked. Registration now happens in
// TriggerNow itself, before the ID is returned, so there is nothing to race.
func TestTriggerNowRegistersBeforeReturningTheID(t *testing.T) {
	t.Parallel()

	llm := newDrainBlockingLLM()
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	sched := agentWorkflow("sched-trigger-race")
	require.NoError(t, s.AddSchedule(ctx, sched))

	executionID, err := s.TriggerNow(ctx, sched.Id, false, nil)
	require.NoError(t, err)

	outcome, err := s.CancelExecution(ctx, executionID, "operator stop")
	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeSignaled, outcome,
		"an ID TriggerNow just returned must already be tracked, not NOT_FOUND")
}

// TestTriggerNowOverridesScheduleSkipIfRunning covers N2's permanent half: the
// skip-if-running decision for a manual trigger is the request's own
// skipIfRunning, not the schedule's persisted config.
//
// Before the fix, executeWorkflow re-checked schedule.Schedule.SkipIfRunning
// regardless of what TriggerNow's caller asked for. An explicit "run it
// anyway" (skipIfRunning=false) against a SkipIfRunning:true schedule with a
// run already in flight minted a second ID, returned it, and then silently
// skipped without ever registering it — an ID that would answer NOT_FOUND
// forever, because nothing this scheduler ever recorded used it.
func TestTriggerNowOverridesScheduleSkipIfRunning(t *testing.T) {
	t.Parallel()

	llm := newDrainBlockingLLM()
	h := newAgentTestScheduler(t, llm)
	s := h.scheduler
	ctx := context.Background()

	sched := agentWorkflow("sched-trigger-override")
	sched.Schedule.SkipIfRunning = true
	require.NoError(t, s.AddSchedule(ctx, sched))

	first, err := s.TriggerNow(ctx, sched.Id, false, nil)
	require.NoError(t, err)

	select {
	case <-llm.started:
	case <-time.After(hangGuard):
		t.Fatal("the first triggered run never reached the agent")
	}

	second, err := s.TriggerNow(ctx, sched.Id, false, nil)
	require.NoError(t, err, `an explicit "run it anyway" trigger must not be rejected because the schedule's own skip_if_running is set`)
	require.NotEqual(t, first, second)

	outcome, err := s.CancelExecution(ctx, second, "operator stop")
	require.NoError(t, err)
	assert.Equal(t, CancelOutcomeSignaled, outcome,
		"the second trigger was silently skipped instead of running, so its ID was never registered")

	// Also stop the first run, so teardown does not have to sit out Stop's
	// grace period waiting for a blocked agent call nothing here still needs.
	_, err = s.CancelExecution(ctx, first, "test cleanup")
	require.NoError(t, err)
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
