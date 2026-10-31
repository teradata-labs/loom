// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// A machine's manager.
//
// A machine that names a manager (logic_seat) handles its own trouble. Every
// agent step's escalation routes to the manager, a step that runs one child
// machine routes that child's every end but done to it, and the manager may
// send the machine back to any step by naming it: one route per step, keyed
// by the step's name as the engine reads a signal. The manager's own
// escalation stays unrouted — that is the park, and the only way anything
// leaves the machine. A step that routes a word itself keeps its own handling.
//
// Trouble below the machine is its trouble too. A step deep in the machines
// this one runs that escalates, with nothing between routing it, stops its
// machine; the step here that delegated routes that escalation to the manager
// like any other, keeping the child as it stopped. Sent back to that step,
// the machine takes the child up at the step that escalated, with the
// manager's words in front of it. The nearest manager above a step is the one
// that hears it.
//
// This is how the machine runs, so it is worked out here, when the engine
// takes a machine up — never written into a spec beforehand. Every machine
// the engine runs is taken up the same way, a child like its parent, so it
// holds at every depth; and a spec stays what its author wrote.

// Seated is a machine as the engine runs it: the spec, with its manager's
// routes, words and instructions in place. A machine that names no manager is
// returned as it is. The spec itself is not touched.
//
// find is how a child the machine delegates to by name is found, to learn how
// that child can end. It may be nil, and a child it cannot find is passed
// over: the step that runs the child is what says so, when it runs.
func Seated(spec *loomv1.StateMachinePattern, find func(name string) (*loomv1.WorkflowPattern, error)) (*loomv1.StateMachinePattern, error) {
	seat := strings.TrimSpace(spec.GetLogicSeat())
	if seat == "" {
		return spec, nil
	}
	sm, _ := proto.Clone(spec).(*loomv1.StateMachinePattern)
	manager, ok := sm.GetStates()[seat]
	if !ok {
		return nil, fmt.Errorf("logic_seat %q is not a state of this loop", seat)
	}
	if manager.GetAgent() == nil {
		return nil, fmt.Errorf("logic_seat %q is not an agent state", seat)
	}
	if manager.Route == nil {
		manager.Route = map[string]*loomv1.Transition{}
	}
	// A manager whose words were its routes' — it declared none — keeps them
	// as words when the steps' words are added.
	if len(manager.Vocab) == 0 {
		manager.Vocab = routeKeys(manager)
		slices.Sort(manager.Vocab)
	}
	names := make([]string, 0, len(sm.GetStates()))
	for name := range sm.GetStates() {
		names = append(names, name)
	}
	slices.Sort(names)
	sendsTo := func(name string) {
		word := strings.ToLower(name)
		if manager.Route[word] == nil {
			manager.Route[word] = &loomv1.Transition{Target: &loomv1.Transition_NextState{NextState: name}}
		}
		if !slices.Contains(manager.Vocab, word) {
			manager.Vocab = append(manager.Vocab, word)
		}
	}
	for _, name := range names {
		st := sm.GetStates()[name]
		if name == seat {
			continue
		}
		d := st.GetDelegate()
		if d == nil && st.GetAgent() == nil && st.GetTool() == nil {
			continue
		}
		if st.Route == nil {
			st.Route = map[string]*loomv1.Transition{}
		}
		// A step that runs one child machine ends with how that machine ended.
		// Every end but done is a step that could not go on, and it goes to the
		// manager, who may send the machine back into that step by its name.
		if d != nil && d.GetItemsKey() == "" {
			child := d.GetWorkflow()
			if child == nil && d.GetWorkflowName() != "" && find != nil {
				child, _ = find(d.GetWorkflowName())
			}
			for _, end := range childEnds(child) {
				if st.Route[end] == nil {
					st.Route[end] = &loomv1.Transition{Target: &loomv1.Transition_NextState{NextState: seat}}
				}
			}
		}
		// An agent step's escalation is its own, and a command step's is that
		// it could not run; a delegating step's is a stop of the work below it
		// that nothing below routed.
		word := EscalateSignal
		if d != nil {
			word = EscalatedSignal
		}
		if st.Route[word] == nil {
			st.Route[word] = &loomv1.Transition{Target: &loomv1.Transition_NextState{NextState: seat}}
		}
		sendsTo(name)
	}
	// The manager is given the machine's shape: every step in walk order, the
	// word that sends the machine there, and what the step is for. Without it
	// a manager can name any step and knows none of them. How a manager works —
	// what it does with what reaches it, and what its own escalation means —
	// is the host's to tell it: the engine routes, and says nothing of that.
	manager.GetAgent().Instructions = withLoopMap(manager.GetAgent().GetInstructions(), sm, seat)
	return sm, nil
}

// childEnds are the words a child machine can end with other than done: its
// terminal states, as the engine hands them to the parent.
func childEnds(child *loomv1.WorkflowPattern) []string {
	var out []string
	for name, st := range child.GetStateMachine().GetStates() {
		if st.GetTerminal() && !strings.EqualFold(name, "DONE") {
			out = append(out, strings.ToLower(name))
		}
	}
	slices.Sort(out)
	return out
}

const loopMapHeader = "\n\nThe loop's steps, in walk order. To send the loop to one, end with its word; what you say above the word is what that step reads:\n"

// withLoopMap appends the machine's steps to the manager's prompt, once.
func withLoopMap(prompt string, sm *loomv1.StateMachinePattern, seat string) string {
	if strings.Contains(prompt, loopMapHeader) {
		return prompt
	}
	var b strings.Builder
	b.WriteString(loopMapHeader)
	for _, name := range walkOrder(sm) {
		if name == seat {
			continue
		}
		st := sm.GetStates()[name]
		switch {
		case st.GetAgent() != nil:
			fmt.Fprintf(&b, "- `%s` → %s: %s\n", strings.ToLower(name), name, firstLineOf(st.GetAgent().GetInstructions()))
		case st.GetTerminal():
			fmt.Fprintf(&b, "- %s: the loop ends here (not a step you can name)\n", name)
		case st.GetDelegate() != nil && routedWord(sm.GetStates()[seat], name) != "" && onlyBackTo(st, seat):
			fmt.Fprintf(&b, "- `%s` → %s: runs a child loop for you, and comes back to you\n",
				routedWord(sm.GetStates()[seat], name), name)
		case st.GetDelegate() != nil && routedWord(sm.GetStates()[seat], name) != "":
			fmt.Fprintf(&b, "- `%s` → %s: a phase; sent back after it stopped, it takes up at the step that stopped\n",
				routedWord(sm.GetStates()[seat], name), name)
		case st.GetDelegate() != nil:
			fmt.Fprintf(&b, "- %s: runs a child loop (not a step you can name)\n", name)
		case st.GetTool() != nil && routedWord(sm.GetStates()[seat], name) != "":
			fmt.Fprintf(&b, "- `%s` → %s: a fixed step the loop runs itself; sent back, it runs again\n",
				routedWord(sm.GetStates()[seat], name), name)
		default:
			fmt.Fprintf(&b, "- %s: a fixed step the loop runs itself (not a step you can name)\n", name)
		}
	}
	return prompt + strings.TrimRight(b.String(), "\n")
}

// onlyBackTo says a step's every route leads back to the manager: a child
// machine the manager calls and hears back from, rather than a stage of the
// walk.
func onlyBackTo(st *loomv1.WorkflowState, seat string) bool {
	for _, tr := range st.GetRoute() {
		if tr.GetNextState() != seat {
			return false
		}
	}
	return true
}

// routedWord is the word the manager sends the machine to a state with, when
// its own routes name it: how a manager calls a utility machine it may call.
func routedWord(seat *loomv1.WorkflowState, state string) string {
	words := make([]string, 0, len(seat.GetRoute()))
	for w := range seat.GetRoute() {
		words = append(words, w)
	}
	slices.Sort(words)
	for _, w := range words {
		if seat.GetRoute()[w].GetNextState() == state {
			return w
		}
	}
	return ""
}

// walkOrder is the states from the entry, following routes breadth-first,
// with any the routes never reach after, by name.
func walkOrder(sm *loomv1.StateMachinePattern) []string {
	states := sm.GetStates()
	seen := map[string]bool{}
	var out []string
	queue := []string{sm.GetEntryState()}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] || states[name] == nil {
			continue
		}
		seen[name] = true
		out = append(out, name)
		keys := make([]string, 0, len(states[name].GetRoute()))
		for k := range states[name].GetRoute() {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if next := states[name].GetRoute()[k].GetNextState(); next != "" && !seen[next] {
				queue = append(queue, next)
			}
		}
	}
	rest := make([]string, 0, len(states))
	for name := range states {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// firstLineOf is a prompt's first line of words, short enough for a map.
func firstLineOf(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 140 {
			line = line[:140] + "…"
		}
		return line
	}
	return "(no instruction)"
}
