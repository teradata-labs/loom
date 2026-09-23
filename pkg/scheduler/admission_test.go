// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestShutdownSignalReachesRunAdmittedBeforeItStarts covers the gap between
// reserving a drain slot and becoming cancellable.
//
// Reserving and registering used to be separate steps: the cron path counted a
// run in inFlight and only registered it several instructions later, inside
// executeWorkflow. Stop's grace expiry signals exactly once, so a run sitting
// in that gap was never signaled — and the final drain, which has no deadline
// on purpose, then waited for it anyway. Shutdown stopped being bounded by the
// operator's 30s shutdown context and became bounded by max_execution_seconds,
// an hour by default, with a hung agent call at the other end.
//
// The handoff is parked deliberately rather than raced: the run is admitted,
// then held outside executeWorkflow for the whole of Stop's signal, which is
// the interleaving the bug needed and which no amount of -race can surface on
// its own.
func TestShutdownSignalReachesRunAdmittedBeforeItStarts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newDrainTestScheduler(t)
	s := h.scheduler

	sched := agentWorkflow("sched-admitted-early")
	require.NoError(t, s.AddSchedule(ctx, sched))

	const execID = "exec-admitted-early"
	admission, _ := s.admitRun(sched.Id, execID, false)
	require.Equal(t, runAdmitted, admission)

	// Admission alone makes the run cancellable. This is the assertion that
	// fails outright if reservation and registration are ever split again.
	require.Equal(t, execID, s.RunningExecutions()[sched.Id],
		"a run holding its schedule's slot must be cancellable before the goroutine that executes it exists")

	// Park the handoff: the run stays between admission and executeWorkflow for
	// the whole of Stop's grace expiry and signal.
	release := make(chan struct{})
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		defer s.inFlight.Done()
		<-release
		s.executeWorkflow(ctx, sched, execID, nil)
	}()

	// A deadline already in the past, so Stop takes its expired-grace branch
	// without this test depending on how fast anything runs.
	stopCtx, cancelStop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelStop()

	stopped := make(chan error, 1)
	go func() { stopped <- h.stop(stopCtx) }()

	awaitDrainCond(t, "the shutdown signal should reach a run that has only just been admitted", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		run, ok := s.runs[execID]
		return ok && run.canceled
	})

	// Only now does the run reach executeWorkflow, which must deliver the
	// signal that landed while it was parked instead of blocking in the agent
	// until its execution timeout.
	close(release)

	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(hangGuard):
		t.Fatal("Stop never returned: the shutdown signal missed a run that was admitted but had not yet started, so the deadline-free drain waited out its execution timeout")
	}
	<-runDone

	// Read the outcome back through a fresh store: Stop closed the one the
	// scheduler held, so anything visible here was written before it went away.
	reopened, err := NewStore(ctx, h.dbPath, zap.NewNop())
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	history, err := reopened.GetExecutionHistory(ctx, sched.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1,
		"the run recorded no history; it was never signaled, or the store closed while it was still writing")
	assert.Equal(t, execID, history[0].ExecutionId)
	assert.Equal(t, "canceled", history[0].Status)
	assert.Equal(t, shutdownCancelReason, history[0].Error)
}

// TestConcurrentTriggersHonorSkipIfRunning covers two-phase admission.
//
// The skip_if_running check and the registration that answers it used to be
// separate critical sections, so triggers arriving together could all observe
// an idle schedule and all start. That is not a cosmetic race: skip_if_running
// is the guarantee RESUME-mode schedules rely on to keep two runs out of the
// same agent sessions, and the manual-trigger path has no second line of
// defence — executeWorkflow deliberately does not recheck the schedule's own
// setting for a manual run.
//
// All the triggers are released from one barrier so they hit the admission
// window together. The verdict does not depend on who wins: whoever is
// admitted first holds the slot for the rest of the test, because the agent
// blocks, so exactly one admission is the only correct outcome under every
// interleaving.
func TestConcurrentTriggersHonorSkipIfRunning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := newDrainTestScheduler(t)
	s := h.scheduler

	sched := agentWorkflow("sched-concurrent-trigger")
	require.NoError(t, s.AddSchedule(ctx, sched))

	const triggers = 8

	var (
		release  = make(chan struct{})
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted []string
		refused  []string
	)

	for range triggers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			execID, err := s.TriggerNow(ctx, sched.Id, true, nil)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				refused = append(refused, err.Error())
				return
			}
			admitted = append(admitted, execID)
		}()
	}
	close(release)
	wg.Wait()

	require.Len(t, admitted, 1,
		"skip_if_running admitted %d concurrent runs of one schedule; it promises at most one", len(admitted))
	require.Len(t, refused, triggers-1)
	for _, msg := range refused {
		assert.Contains(t, msg, "previous execution still running")
		assert.Contains(t, msg, admitted[0],
			"a refusal must name the run that actually holds the slot, since that is the one an operator waits on or cancels")
	}
	assert.Equal(t, admitted[0], s.RunningExecutions()[sched.Id],
		"the admitted run is the one occupying the schedule")

	// Stop with no grace left, so the blocked run is signaled at once rather
	// than leaving the cleanup to sit out its whole grace period.
	expired, cancelStop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelStop()
	require.NoError(t, h.stop(expired))
}

// TestSkipIfRunningIsDecidedUnderOneLockHold is the deterministic pin for the
// same bug, and the reason it is a separate test: the concurrent-trigger case
// above asserts the promise an operator relies on, but it cannot force the
// interleaving that broke it — two-phase admission still admits one run most
// of the time, so that test passes against the bug often enough to be useless
// as a regression pin.
//
// This one asserts the structural property instead, with no race to lose. A
// reader holds s.mu for the whole call: under single-critical-section
// admission, admitRun cannot reach any verdict until that reader lets go,
// because deciding and recording the answer are the same lock hold. A verdict
// that arrives while the read lock is still held can only have been computed
// from a snapshot somebody else was free to invalidate — which is exactly how
// two concurrent triggers both passed skip_if_running.
func TestSkipIfRunningIsDecidedUnderOneLockHold(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := setupTestScheduler(t)

	sched := failingSchedule("sched-admission-lock")
	sched.Schedule.SkipIfRunning = true
	require.NoError(t, s.AddSchedule(ctx, sched))

	// The schedule is already occupied, so admitRun has a verdict available to
	// it — refuse — without anything having to execute.
	seedRun(s, sched.Id, "exec-holding-the-slot", nil, false)

	s.mu.RLock()

	verdict := make(chan runAdmission, 1)
	go func() {
		admission, _ := s.admitRun(sched.Id, "exec-racing", true)
		verdict <- admission
	}()

	// Generous on purpose: with admission in one critical section the call is
	// blocked on a lock this test holds, so no runner is slow enough to make
	// this wait flaky. It is only sensitivity to the bug that it buys.
	select {
	case got := <-verdict:
		s.mu.RUnlock()
		t.Fatalf("admitRun reached the verdict %v while another reader held s.mu: "+
			"skip_if_running is being decided outside the critical section that registers the answer, "+
			"so two triggers can both be admitted", got)
	case <-time.After(200 * time.Millisecond):
	}

	s.mu.RUnlock()

	select {
	case got := <-verdict:
		require.Equal(t, runRefusedRunning, got)
	case <-time.After(hangGuard):
		t.Fatal("admitRun never returned after the read lock was released")
	}
}
