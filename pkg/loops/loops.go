// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package loops is where runs live.
//
// A loop run is the unit of work: one walk through one loop, in one workspace's
// directory, spending one budget. The engine produces checkpoints and consumes
// them but deliberately keeps none — right for an engine, useless for a person
// who closes the terminal and comes back tomorrow. This package is the memory
// the engine does not have.
//
// A run belongs to the workspace, not to the conversation that started it. Work
// that takes hours cannot be owned by a session that lasts minutes: the session
// is recorded as provenance, and any later session in the same workspace can
// watch the run, answer what it is waiting on, or stop it.
package loops

import (
	"context"
	"errors"
	"time"
)

// Status is where a run stands, in the terms a person asks about it.
type Status string

const (
	// Running: a process holds its lease and is walking it.
	Running Status = "running"
	// Waiting: parked from inside — a state raised something only its
	// parent can settle. The run keeps its place and resumes into it.
	Waiting Status = "waiting"
	// TimerWait: stopped on time rather than on anybody. The pass is over and
	// the run owes itself the next one; nothing is being asked.
	//
	// A status of its own and not a flavour of Waiting, because the two are
	// opposite in what they want from a person: a waiting run wants an answer
	// and should be put in front of them, a timer-waiting run wants nothing and
	// must never reach them. Everything that decides whether a run needs its
	// owner reads the status, so the difference belongs there — and a clock can
	// then carry a run on without having to work out whether doing so would
	// answer somebody's question for them.
	TimerWait Status = "timer_wait"
	// Stopped: parked from outside — its parent said stop. The same shape
	// as Waiting with no question open: the run keeps its place and resumes
	// into it.
	Stopped Status = "stopped"
	// Done and Failed are the ends a walk reaches on its own. Discarding a
	// run removes its row; there is no status for it.
	Done   Status = "done"
	Failed Status = "failed"
)

// SuspendReason is why a run parked, carried from the engine's checkpoint.
type SuspendReason string

const (
	ReasonGate           SuspendReason = "gate"
	ReasonUnroutable     SuspendReason = "unroutable"
	ReasonCostCap        SuspendReason = "cost_cap"
	ReasonMaxTransitions SuspendReason = "max_transitions"
	ReasonNested         SuspendReason = "nested"
	ReasonBubbled        SuspendReason = "bubbled"
	// A state could not run — the model was unreachable, a tool failed. The run
	// waits at that state and is resumed into it.
	ReasonError            SuspendReason = "error"
	ReasonWorkspaceGone    SuspendReason = "workspace_gone"
	ReasonProcessAbandoned SuspendReason = "process_abandoned"
	// The run stopped in a state that waits. Nobody is asked anything; the
	// state says how long, and the run takes its next step when that is up.
	ReasonTimer SuspendReason = "timer"
)

// Kind separates the two ways work is given to an agent. Empty on a loop run,
// which is given a spec rather than handed to an agent.
type Kind string

const (
	// Connected: the conversation was handed to this run and comes back.
	Connected Kind = "connected"
	// Spawned: the run was given work; the conversation did not move.
	Spawned Kind = "spawned"
)

// Run is one piece of work: a walk through one loop, or one agent given
// something to do.
//
// The two share this record because they are the same question asked twice —
// what was given out, to whom, and where it stands. Which one a row is, is read
// off AgentID: set means an agent holds it and routing owns the row; empty means
// a spec does, and the loop engine owns it. The lease and checkpoint fields
// belong to the loop half and stay zero on the routing half, which never has
// more than one process walking it.
type Run struct {
	ID          string
	WorkspaceID string
	// SessionID is who spawned it: provenance, never lifecycle.
	SessionID string
	// ParentRunID and ParentState are the delegation tree — who to wake when
	// this run finishes, and which of its states routes on the outcome.
	ParentRunID string
	ParentState string

	// SpecName is the loop the run is of. The loop's definition is not kept
	// with the run: whoever takes the run up reads it then, by this name.
	SpecName string
	Brief    string
	// Title is what the person sees the run called: a few words on the task
	// it was given, set once the run has started; empty until it is.
	Title string
	// Dir is the run's own directory, under the workspace's units/.
	Dir string

	Status    Status
	StatePath []string
	// Vars is what the run has worked out so far: its cursors and its
	// children's outcomes. With StatePath it is what re-entering a state needs.
	Vars          map[string]string
	SuspendReason SuspendReason
	// ParkRecord is what the last park left behind, or empty.
	ParkRecord []byte
	// Word is a manager's word for this parked run, held until the person
	// says the run goes on with it; empty when none is waiting.
	Word string
	// Message is the words for the run's next turn: what the step it stands
	// at was handed, or the answer written to it while it stood stopped.
	Message string
	// StopWord is the word the step it stands at stopped on, a word its loop
	// does not route; empty while it walks.
	StopWord string
	// Terminal is the state it ended in: a loop's answer to its caller.
	Terminal string
	Report   string

	SpentUSD float64
	CapUSD   float64
	// Granted* are extensions given on resume past a ceiling. They live on the
	// run: what a run has been granted is the run's, not its loop's.
	GrantedCostUSD     float64
	GrantedTransitions int

	// LeaseOwner and LeaseHeartbeat tell a live run from one whose process
	// died. Empty owner means nobody holds it. The heartbeat is a liveness
	// clock kept to the millisecond, not a timestamp for display.
	LeaseOwner     string
	LeaseHeartbeat time.Time

	// AgentID names the agent this work was given to, and is what makes the row
	// routing's rather than the loop engine's.
	AgentID string
	Kind    Kind
	// OwnSession is this run's own conversation log. SessionID above is the
	// conversation that gave out the work; the two are different questions and
	// a run needs both.
	OwnSession string

	CreatedAt time.Time
	UpdatedAt time.Time
	EndedAt   time.Time
}

// Active reports whether the run is one a process should be walking.
func (r *Run) Active() bool { return r.Status == Running }

// Open reports whether a run is still something the conversation can continue:
// working, or waiting on a person. Finished work is not open, and neither is
// work that was stopped.
func (r *Run) Open() bool { return r.Status == Running || r.Status == Waiting }

// Step is one state a run entered: who ran it, what it concluded, what it cost.
// The walk is the sequence of these, and it is what makes a long autonomous run
// reviewable afterwards.
type Step struct {
	RunID      string
	Seq        int
	State      string
	AgentID    string // empty for tool states
	Tool       string // empty for agent states
	Signal     string
	Visit      int // 1 on first entry; higher means it came round again
	CostUSD    float64
	DurationMS int64
	At         time.Time
}

var (
	ErrNotFound = errors.New("loops: not found")
	// ErrLeaseHeld: another process holds the run. One executor per run is
	// what makes many watchers safe.
	ErrLeaseHeld = errors.New("loops: the run is held by another process")
	// ErrNotWaiting: a resume was attempted on a run that is not parked.
	ErrNotWaiting = errors.New("loops: the run is not waiting")
)

// Store is the durable side of a run. One implementation per backend: SQLite
// for the CLI, Postgres for the cloud, the same behaviour from both.
type Store interface {
	// Create records a spawned run. It is not yet held by anyone.
	Create(ctx context.Context, run *Run) error
	Get(ctx context.Context, id string) (*Run, error)
	// List returns a workspace's runs, newest first.
	List(ctx context.Context, workspaceID string) ([]*Run, error)
	// Children returns the runs a state of this run delegated.
	Children(ctx context.Context, parentRunID string) ([]*Run, error)

	// Progress records where a run is now: its state path and what it has
	// spent. Called on every transition, so it must be cheap.
	Progress(ctx context.Context, id string, statePath []string, spentUSD float64) error
	// Moved records a transition: the state being entered, the vars as they
	// stand, and what the completed state added to the spend. This is the
	// resume point, written as the walk moves rather than when it stops.
	Moved(ctx context.Context, id string, statePath []string, vars map[string]string, addUSD float64) error
	// Park stops a run for a person, with the reason it stopped. The lease is
	// kept: the process that parked it still owns it, and keeps proving so.
	Park(ctx context.Context, id string, reason SuspendReason) error
	// Stop parks a run from outside: status stopped, lease released, and
	// its place — state path, vars, park record — kept for a resume.
	Stop(ctx context.Context, id string) error
	// Discard removes a run, its steps, and every run delegated under it.
	// Nothing resumes a discarded run; its id is free again.
	Discard(ctx context.Context, id string) error
	// Finish closes a run: its terminal state, its report, and how it ended.
	Finish(ctx context.Context, id string, status Status, terminal, report string) error
	// Reopen is a finished or stopped run taken up again: working, at the
	// state it begins at, with the vars it begins with, and no longer saying
	// how it ended. Its steps, sessions and spend stand.
	Reopen(ctx context.Context, id string, statePath []string, vars map[string]string) error
	// HoldWord keeps a manager's word on a parked run until the person says
	// the run goes on with it. Acquiring the run spends it.
	HoldWord(ctx context.Context, id, word string) error
	// Name sets what the person sees the run called. It is not progress: the
	// run's updated_at stays as it was.
	Name(ctx context.Context, id, title string) error
	// Entered records the step a run is entering and the words it enters
	// with. A run that enters a step is no longer stopped.
	Entered(ctx context.Context, id string, statePath []string, message string) error
	// StoppedOn records that the step a run stands at stopped on a word its
	// loop does not route, with what the run had worked out.
	StoppedOn(ctx context.Context, id string, statePath []string, word string, vars map[string]string) error
	// Answer writes the words a stopped run is taken up with, and what it had
	// worked out, before it is taken up.
	Answer(ctx context.Context, id, message string, vars map[string]string) error
	// Grant raises a run's ceilings for a resume past them.
	Grant(ctx context.Context, id string, costUSD float64, transitions int) error

	// Acquire takes the run's lease for owner, refusing if another process
	// holds it and its heartbeat is still fresh. Taking the lease marks the
	// run running.
	Acquire(ctx context.Context, id, owner string, staleAfter time.Duration) error
	// Adopt takes the lease the way Acquire does but leaves the status as it
	// is: a parked run adopted by a new process is still parked, and still
	// owned — its heartbeat is what says the owner is alive.
	Adopt(ctx context.Context, id, owner string, staleAfter time.Duration) error
	// Heartbeat proves the holder is alive.
	Heartbeat(ctx context.Context, id, owner string) error
	// Release drops the lease without changing the run's status.
	Release(ctx context.Context, id, owner string) error
	// Abandoned returns runs marked running whose holder has stopped proving
	// it is alive — what reconcile-on-start works through.
	Abandoned(ctx context.Context, staleAfter time.Duration) ([]*Run, error)

	// AppendStep adds to the walk. Seq is assigned by the store.
	AppendStep(ctx context.Context, step *Step) error
	Walk(ctx context.Context, runID string) ([]*Step, error)

	// SavePark keeps what a park left behind — the engine's own record of the
	// question it stopped on — on the run's row, until the run is resumed. A
	// run's position is its row; this is only what a park adds to it.
	SavePark(ctx context.Context, runID string, record []byte) error
}
