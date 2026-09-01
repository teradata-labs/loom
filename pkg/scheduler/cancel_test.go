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
package scheduler

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// newCancelTestScheduler builds only the state CancelExecution touches. The
// full scheduler needs an orchestrator, a registry, and a database; none of
// that is involved in signalling a cancel.
func newCancelTestScheduler() *Scheduler {
	return &Scheduler{
		runningWorkflows: make(map[string]string),
		runningCancels:   make(map[string]context.CancelFunc),
		cancelReasons:    make(map[string]string),
		logger:           zap.NewNop(),
	}
}

// Cancelling a live execution must actually cancel its context and record the
// reason, so executeWorkflow can classify the run as cancelled rather than
// failed.
func TestCancelExecutionSignalsTheRun(t *testing.T) {
	s := newCancelTestScheduler()

	execCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.runningWorkflows["sched-1"] = "exec-1"
	s.runningCancels["exec-1"] = cancel
	s.mu.Unlock()

	cancelled, err := s.CancelExecution(context.Background(), "exec-1", "stopped by test")
	if err != nil {
		t.Fatalf("CancelExecution: %v", err)
	}
	if !cancelled {
		t.Fatal("cancelled = false for a running execution")
	}

	select {
	case <-execCtx.Done():
	default:
		t.Error("the execution context was not cancelled")
	}

	s.mu.RLock()
	reason := s.cancelReasons["exec-1"]
	s.mu.RUnlock()
	if reason != "stopped by test" {
		t.Errorf("recorded reason = %q, want %q", reason, "stopped by test")
	}
}

// An execution that already finished is not an error: the caller wanted it
// stopped and it is stopped. Reporting NotFound would make the UI apologise for
// winning a race.
func TestCancelExecutionUnknownIDIsNotAnError(t *testing.T) {
	s := newCancelTestScheduler()

	cancelled, err := s.CancelExecution(context.Background(), "exec-gone", "")
	if err != nil {
		t.Fatalf("unknown execution returned an error: %v", err)
	}
	if cancelled {
		t.Error("cancelled = true for an execution that was never running")
	}

	// Nothing should be left behind for a run that does not exist, or the
	// reason would be misapplied if that ID were later reused.
	s.mu.RLock()
	_, leaked := s.cancelReasons["exec-gone"]
	s.mu.RUnlock()
	if leaked {
		t.Error("a reason was recorded for a non-running execution")
	}
}

func TestCancelExecutionRequiresID(t *testing.T) {
	s := newCancelTestScheduler()

	if _, err := s.CancelExecution(context.Background(), "", "why"); err == nil {
		t.Error("empty execution_id returned nil error")
	}
}

// The reason must be recorded before the context is cancelled. If it were the
// other way round, executeWorkflow could observe a cancelled context, classify
// the run as failed, and miss the reason entirely.
func TestCancelRecordsReasonBeforeCancelling(t *testing.T) {
	s := newCancelTestScheduler()

	var (
		mu             sync.Mutex
		reasonAtCancel string
		sawReason      bool
	)

	s.mu.Lock()
	s.runningCancels["exec-1"] = func() {
		s.mu.RLock()
		r, ok := s.cancelReasons["exec-1"]
		s.mu.RUnlock()
		mu.Lock()
		reasonAtCancel, sawReason = r, ok
		mu.Unlock()
	}
	s.mu.Unlock()

	if _, err := s.CancelExecution(context.Background(), "exec-1", "operator stop"); err != nil {
		t.Fatalf("CancelExecution: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawReason {
		t.Fatal("the reason was not visible when cancel fired")
	}
	if reasonAtCancel != "operator stop" {
		t.Errorf("reason at cancel = %q, want %q", reasonAtCancel, "operator stop")
	}
}

func TestRunningExecutionsSnapshot(t *testing.T) {
	s := newCancelTestScheduler()

	s.mu.Lock()
	s.runningWorkflows["sched-1"] = "exec-1"
	s.runningWorkflows["sched-2"] = "exec-2"
	s.mu.Unlock()

	got := s.RunningExecutions()
	if len(got) != 2 || got["sched-1"] != "exec-1" || got["sched-2"] != "exec-2" {
		t.Fatalf("RunningExecutions() = %v", got)
	}

	// The result must be a copy — handing out the live map would let a caller
	// mutate scheduler state without the lock.
	got["sched-1"] = "tampered"
	s.mu.RLock()
	live := s.runningWorkflows["sched-1"]
	s.mu.RUnlock()
	if live != "exec-1" {
		t.Error("RunningExecutions returned the live map, not a copy")
	}
}
