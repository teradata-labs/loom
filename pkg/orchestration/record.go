// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"maps"
	"strings"
	"sync"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// A run's own record.
//
// Every run — the top one and every child a state delegates to — has one
// record of where it stands: the step, what it has worked out, the words its
// next turn reads, and the word it stopped on. A loop that cannot go on stops
// where it stands, on its own record. The loop above it holds no copy of that
// place: it reports the stop in its own loop, and when the answer comes back
// it writes the answer on the stopped run's record and takes the run up from
// it. A run taken up any other way — after a death, after the walk went
// elsewhere first — is taken up from its record with its own words.
//
// The host keeps the records when it can (a RunObserver that is also a
// PlaceReader and a RecordObserver); otherwise the engine keeps them for the
// life of the orchestrator.

// Place is a run's record as the engine reads it.
type Place struct {
	StatePath []string
	Vars      map[string]string
	// Message is the words the run's next turn reads.
	Message string
	// StopWord is the word the step it stands at stopped on; empty while it walks.
	StopWord string
	// Ended says the run reached a terminal; Terminal is which.
	Ended    bool
	Terminal string
}

// At is the step a place stands at.
func (p Place) At() string {
	if len(p.StatePath) == 0 {
		return ""
	}
	return p.StatePath[len(p.StatePath)-1]
}

// PlaceReader is a host that keeps every run's record.
type PlaceReader interface {
	// PlaceOf is a run's record; false when the run has none.
	PlaceOf(ctx context.Context, runID string) (Place, bool)
	// Answer writes the words a stopped run is taken up with, and what it
	// has worked out, on its record.
	Answer(ctx context.Context, runID, message string, vars map[string]string) error
}

// RecordObserver is told what the engine writes on a run's record besides
// its transitions.
type RecordObserver interface {
	// Entering: the run is entering a step, with these words.
	Entering(ctx context.Context, workflowID, state, message string)
	// StoppedOn: the step the run stands at stopped on a word its loop does
	// not route; vars is what the run had worked out.
	StoppedOn(ctx context.Context, workflowID, state, word string, vars map[string]string)
	// Noted: what the run has worked out changed inside a step.
	Noted(ctx context.Context, workflowID, state string, vars map[string]string)
}

// runRecord is where the engine keeps and reads runs' records.
type runRecord interface {
	PlaceReader
	RecordObserver
}

// record is the host's record keeper when it keeps records, else the engine's.
func (o *Orchestrator) record() runRecord {
	if r, ok := o.observer.(runRecord); ok {
		return r
	}
	return o.places
}

// observers is everything told what a walk does: the host's observer, and
// the engine's own records when the host keeps none.
func (o *Orchestrator) observers() []RunObserver {
	var out []RunObserver
	if o.observer != nil {
		out = append(out, o.observer)
	}
	if _, ok := o.observer.(runRecord); !ok {
		out = append(out, o.places)
	}
	return out
}

// memRecords keeps runs' records in memory, for an orchestrator whose host
// keeps none.
type memRecords struct {
	mu   sync.Mutex
	runs map[string]*Place
}

func newMemRecords() *memRecords { return &memRecords{runs: map[string]*Place{}} }

func (m *memRecords) at(id string) *Place {
	p := m.runs[id]
	if p == nil {
		p = &Place{}
		m.runs[id] = p
	}
	return p
}

func (m *memRecords) PlaceOf(_ context.Context, id string) (Place, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.runs[id]
	if !ok {
		return Place{}, false
	}
	out := *p
	out.StatePath = append([]string(nil), p.StatePath...)
	out.Vars = maps.Clone(p.Vars)
	return out, true
}

func (m *memRecords) Answer(_ context.Context, id, message string, vars map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(id)
	p.Message = message
	p.Vars = maps.Clone(vars)
	return nil
}

func (m *memRecords) Entering(_ context.Context, id, state, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(id)
	p.StatePath, p.Message, p.StopWord = []string{state}, message, ""
	p.Ended, p.Terminal = false, ""
}

func (m *memRecords) StoppedOn(_ context.Context, id, state, word string, vars map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(id)
	p.StatePath, p.StopWord, p.Vars = []string{state}, word, maps.Clone(vars)
}

func (m *memRecords) Noted(_ context.Context, id, state string, vars map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(id)
	p.StatePath, p.Vars = []string{state}, maps.Clone(vars)
}

func (m *memRecords) ChildStarted(ctx context.Context, _, _ string, child *loomv1.WorkflowPattern) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(child.GetWorkflowId())
	*p = Place{StatePath: []string{EntryOf(ctx, child.GetStateMachine())}, Vars: maps.Clone(RunVarsFromContext(ctx))}
}

func (m *memRecords) Transitioned(_ context.Context, id, _, _, entering string, vars map[string]string, _ float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.at(id)
	p.StatePath, p.Vars, p.StopWord = []string{entering}, maps.Clone(vars), ""
}

func (m *memRecords) ChildEnded(_ context.Context, parentID, parentState, childID, terminal string, parentVars map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.at(childID)
	c.Ended, c.Terminal, c.StatePath, c.StopWord = true, terminal, nil, ""
	p := m.at(parentID)
	p.StatePath, p.Vars = []string{parentState}, maps.Clone(parentVars)
}

// The engine's notes on a run's record about its children.

// askedKey marks the state whose child stopped and was reported in this
// loop: the value names the child's item.
func askedKey(state string) string { return "__asked:" + state }

// answeringKey marks that the state is entered with the answer to what its
// child asked.
func answeringKey(state string) string { return "__answering:" + state }

// enterChildKey is where a route's enter is kept for the state it enters.
func enterChildKey(state string) string { return "__enter:" + state }

// returnKey is where a step a manager sent the walk to remembers the step
// that asked, to return to it when it ends done.
func returnKey(state string) string { return "__return:" + state }

// messageKey is where a park keeps the words its state was entered with.
const messageKey = "__message"

// asked is the item an asked note names.
func asked(note string) string { return strings.TrimPrefix(note, "item:") }

// EscalatedSignal is what a state that runs a child loop ends with when that
// child stopped on a word its loop does not route, or ended in a way the
// state does not route: the work below cannot go on. The state's own route
// table says who judges it.
const EscalatedSignal = "escalated"

// heldPrefixes are the engine's notes on a run's record that are about a
// walk in hand: none of them is carried when a run that ended is sent back in.
var heldPrefixes = []string{"__kept:", "__waiting:", "__stopped:", "__stopped_vars:", "__unheard:",
	"__kept_words:", "__entered:", "__enter:", "__budget:", "__asked:", "__answering:", "__return:", messageKey}

// sentBackIn is what a run that ran to done starts with when a route sends it
// back in at a step that runs a set: what it had worked out, with that step's
// last child no longer finished, so the step runs it again. Nil when the step
// has no list, or its last child had not finished.
func sentBackIn(worked map[string]string, at string) map[string]string {
	if worked == nil {
		return nil
	}
	list := parseList(worked[listKeyFor(at)])
	if len(list) == 0 {
		return nil
	}
	last := list[len(list)-1].Item
	out := make(map[string]string, len(worked))
next:
	for k, v := range worked {
		for _, p := range heldPrefixes {
			if strings.HasPrefix(k, p) {
				continue next
			}
		}
		out[k] = v
	}
	done := map[string]string{}
	for _, line := range strings.Split(worked[outcomesKeyFor(at)], "\n") {
		if item, terminal, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			done[item] = terminal
		}
	}
	if _, ok := done[last]; !ok {
		return nil
	}
	delete(done, last)
	out[outcomesKeyFor(at)] = summarise(done)
	return out
}

// sentInKey marks a child's context as sent back in at a step a route named.
type sentInKey struct{}

func withSentIn(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, sentInKey{}, on)
}

func sentInOf(ctx context.Context) bool {
	on, _ := ctx.Value(sentInKey{}).(bool)
	return on
}

// runIDKey carries the run whose step an agent turn belongs to.
type runIDKey struct{}

// WithRunID says the turn in hand is a step of this run.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey{}, runID)
}

// RunIDFromContext is the run whose step the turn in hand is, if any: what a
// tool a step calls needs to read that run's record.
func RunIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(runIDKey{}).(string)
	return id
}
