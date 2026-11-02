// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// FannedSignal is what a state reports when it delegated to more than one
// child. With several children there is no single outcome to route on, so the
// terminals are written to run state and the step after this one decides what
// they add up to — a judgement that belongs to the process, not the engine.
const FannedSignal = "fanned"

// SteppedSignal is what a stepping list state reports after each entry: the
// entry's outcome is the message, and the state after this one judges it
// before the walk re-enters for the next.
const SteppedSignal = "stepped"

// recordChildOutcome remembers one child's terminal, so re-entering the set
// runs only what still has to run.
func (e *StateMachineExecutor) recordChildOutcome(state *machineState, d *loomv1.DelegateState, name, item, terminal string) {
	done := e.outcomes(state, name)
	done[item] = terminal
	e.setOutcomes(state, name, done)
}

// runDelegateState hands the work to another workflow, once or once per item.
//
// One child and a hundred are the same step. Writing the single case on its own
// is what hides the questions a set asks: which items are in play, where each
// runs, what each may spend, how many run at once, and what they all finished
// as. Every one of those is the parent's own state.
func (e *StateMachineExecutor) runDelegateState(ctx context.Context, state *machineState, d *loomv1.DelegateState, name string) (string, string, error) {
	// The child is the one written into the state, or the one of the name the
	// state gives — found now, so what runs is the child as it is defined when
	// the state is entered. A child that cannot be found stops the run here.
	template, ctx, err := e.childOf(ctx, d, name)
	if err != nil {
		if state.answer != nil {
			// A person's answer is waiting on this state's child: it cannot be
			// applied, and is not spent. The child it was kept for goes back
			// to where the answer found it.
			delete(state.vars, keptKey(name))
			return "", "", &refusedAnswer{err}
		}
		return "", "", err
	}
	startTime := time.Now()
	entries, fanned := e.delegationItems(state, d, name)
	if len(entries) == 0 {
		// Nothing to do is an outcome, not an error: a round where no repo moved
		// and a task set that turned out empty both arrive here.
		return FannedSignal, "", nil
	}
	if !fanned {
		// A managed machine hears every end of a child it runs alone that is
		// not done. How the child can end is read off the child as it is
		// defined now, when the state runs it.
		e.routeEndsToManager(name, template)
	}
	done := e.outcomes(state, name)
	pending := make([]listEntry, 0, len(entries))
	for _, en := range entries {
		if _, finished := done[en.Item]; !finished {
			pending = append(pending, en)
		}
	}
	// One entry at a time, each judged by the state after this one before the
	// next begins: the walk ends after each entry and re-entry runs the next.
	stepping := d.GetStep() && len(pending) > 0
	if stepping {
		pending = pending[:1]
	}
	budget, err := e.settledBudget(state, d, name, len(pending))
	if err != nil {
		return "", "", fmt.Errorf("state %q: %w", name, err)
	}
	e.orchestrator.logger.Info("Delegating",
		zap.String("state", name),
		zap.Int("items", len(entries)),
		zap.Int("to_run", len(pending)),
		zap.Float64("budget_each_usd", budget))

	single := !fanned && len(entries) == 1
	if single && !stepping {
		// A child that ended last time other than done and is entered again
		// takes up at the step that ended it. What it ended with is spent by
		// this entry.
		if at := state.vars[stoppedAtKey(name)]; at != "" {
			ctx = withReentry(ctx, at, decodeVars(state.vars[stoppedVarsKey(name)]))
		}
		delete(state.vars, stoppedAtKey(name))
		delete(state.vars, stoppedVarsKey(name))
	}
	// A person's decision on a child that parked the whole tree is taken up
	// with that child.
	kept, err := e.takeKept(state, name)
	if err != nil {
		return "", "", err
	}
	// The words of this entry. Entered with the answer to what a child of
	// this state asked, they are that child's alone: written on its record,
	// and it is taken up with them. Any other entry takes a stopped child up
	// with its own words, and the first child started opens with the words
	// the state was entered with.
	note := state.vars[askedKey(name)]
	answering := note != "" && state.vars[answeringKey(name)] != ""
	delete(state.vars, askedKey(name))
	delete(state.vars, answeringKey(name))
	opening := state.message
	if answering {
		opening = ""
		if who := asked(note); e.runsFirst(d, pending, who) {
			e.answer(ctx, childID(e.workflowID, name, who), state.message)
		}
	}
	if note != "" {
		e.orchestrator.record().Noted(ctx, e.workflowID, name, state.vars)
	}
	enterAt := state.vars[enterChildKey(name)]
	if enterAt != "" {
		// The route that entered this state names a step of the child's loop
		// to take a delivered child up at: the loop must have it, and it must
		// be a step that runs children.
		if st := template.GetStateMachine().GetStates()[enterAt]; st == nil || st.GetDelegate() == nil {
			return "", "", fmt.Errorf("state %q: the route that entered it sends a child back in at %q, which is not a step of its loop that runs children", name, enterAt)
		}
	}
	results, stopped, ended, failed, blocked := e.runChildren(ctx, state, d, template, name, pending, budget, kept, opening, enterAt)
	if ctx.Err() != nil {
		// The walk is being stopped: nothing of this entry is an outcome, and
		// every child stands where it stood on its own record.
		return "", "", ctx.Err()
	}
	if blocked == nil {
		delete(state.vars, enterChildKey(name))
	}
	for item, terminal := range results {
		done[item] = terminal
	}
	e.setOutcomes(state, name, done)
	if blocked != nil {
		// A child could not be run at all. Nothing of the others is lost: the
		// ones that stopped stand where they stopped, on their own records.
		switch {
		case !blocked.kept:
		case kept.answer:
			// A person's answer could not be applied. It is not spent, and the
			// run's variables are as they were before it was tried.
			return "", "", &refusedAnswer{blocked.err}
		default:
			_ = e.keep(state, name, kept.ckpt)
		}
		return "", "", blocked.err
	}
	if len(stopped) > 0 {
		// Children stopped, each on its own record. The first in the order
		// the items were given is heard; the rest are taken up, with their own
		// words, the next time this state runs them.
		var item string
		var suspended *loomv1.WorkflowCheckpoint
		for _, en := range entries {
			if ckpt, ok := stopped[en.Item]; ok {
				item, suspended = en.Item, ckpt
				break
			}
		}
		if suspended != nil {
			// A child that stopped on a word its loop does not route cannot go
			// on: this state says so in its own loop, whose table says who
			// judges it. Whatever else a child stops on — a gate, a ceiling, a
			// step that could not run, a wait — parks the tree for a person.
			if suspended.GetSuspendReason() == loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE {
				return e.escalated(state, name, fanned, len(entries), item, suspended, startTime)
			}
			return "", "", &nestedSuspension{state: name, inner: suspended}
		}
	}

	// How the children finished is this state's output, and so the message the
	// next state opens with: one "<item>=<terminal>" per line.
	signal, output := FannedSignal, summarise(done)
	switch {
	case stepping:
		// A set walked one entry at a time is, each time it is entered, a state
		// that ran one child, and it answers as one. An entry that ran to done
		// is stepped: the state after judges what it delivered before the next
		// begins. An entry that ended any other way did not deliver, and the
		// state answers with that end — the word the spec routes a stopped
		// child by — rather than with the word for one that did.
		item := pending[0].Item
		signal, output = SteppedSignal, item+"="+done[item]
		if !strings.EqualFold(done[item], "DONE") {
			signal, output = e.endedOtherwise(name, done[item], output, ended[item])
		}
	case single:
		// A single child answers its parent directly: its terminal is the
		// signal, which is how a phase reports what it concluded. A child that
		// ended any way but done also hands up the whole of what the step that
		// ended it said, and remembers where it stopped, so whoever the parent
		// sends it to can judge it and send it back to that step.
		terminal := done[entries[0].Item]
		signal, output = terminal, terminal
		if r := ended[entries[0].Item]; r != nil && !strings.EqualFold(terminal, "DONE") {
			signal, output = e.endedOtherwise(name, terminal, terminal, r)
			if at := r.GetMetadata()["last_state"]; at != "" {
				state.vars[stoppedAtKey(name)] = at
				state.vars[stoppedVarsKey(name)] = r.GetMetadata()["vars"]
			}
		}
	case uniform(done, entries) && e.routes(name, done[entries[0].Item]):
		// A set whose children all ended the same way answers with that word
		// when the spec routes it: suites all green route as green. A mixed
		// set, or a spec that routes only "fanned", is fanned, left to the
		// state after to read.
		signal = normalizeSignal(done[entries[0].Item])
	}
	// A child that could not run at all has FAILED as its outcome, and why
	// goes with it: whoever reads this state's output — the state after it, or
	// the person it stops for — is told what stopped the child.
	if why := whyFailed(failed); why != "" {
		output += "\n\n" + why
	}

	// The state that delegated is a state the run entered, and it has to appear
	// in its own walk. Without a record of it, the parent's history skips
	// straight from the step before to the step after, and the hours its
	// children spent are unaccounted for in the one place someone looks.
	e.record(state, name, name, &loomv1.AgentResult{
		AgentId: name,
		Output:  output,
		Metadata: map[string]string{
			"state":    name,
			"children": fmt.Sprintf("%d", len(entries)),
		},
		ConfidenceScore: 1.0,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Cost:            &loomv1.AgentExecutionCost{},
	})
	return signal, output, nil
}

// runsFirst says the child of this item is the one this entry runs first —
// for a set run together, one of those it runs. An answer is written on its
// record only then: written on a child this entry does not take up, it would
// be read later as an answer to a question asked again.
func (e *StateMachineExecutor) runsFirst(d *loomv1.DelegateState, pending []listEntry, item string) bool {
	if len(pending) == 0 {
		return false
	}
	if d.GetStep() || d.GetMaxParallel() == 1 {
		return pending[0].Item == item
	}
	for _, en := range pending {
		if en.Item == item {
			return true
		}
	}
	return false
}

// answer writes the words a stopped child is taken up with on its record. A
// child that is itself a state that reported its own child's stop passes them
// on: it is taken up answering.
func (e *StateMachineExecutor) answer(ctx context.Context, id, words string) {
	rec := e.orchestrator.record()
	place, ok := rec.PlaceOf(ctx, id)
	if !ok || place.Ended || len(place.StatePath) == 0 {
		return
	}
	vars := place.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	if at := place.At(); vars[askedKey(at)] != "" {
		vars[answeringKey(at)] = "1"
	}
	if err := rec.Answer(ctx, id, words, vars); err != nil {
		e.orchestrator.logger.Warn("An answer could not be written on the record of the child it is for",
			zap.String("child", id), zap.Error(err))
	}
}

// listEntry is one thing a list state delegates over: its item, and where the
// child works when the list said so.
type listEntry struct {
	Item string
	Dir  string
	// Vars is what else the list said about this entry — its repo, its
	// surface — handed to the child as constants the spec declared.
	Vars map[string]string
}

// listKeyFor is where the engine keeps the list a state was handed.
func listKeyFor(name string) string { return "__list:" + name }

// takeList notices a list arriving with a transition into a list state and
// keeps it as that state's walk. A list is the one structured thing a seat
// hands the engine besides its word: it rides on the signal line under the
// field the state names. A new list is a fresh walk — what an earlier list
// finished as is forgotten, and the children's share of the budget is settled
// again — while a re-entry without one continues the walk in progress.
func (e *StateMachineExecutor) takeList(state *machineState, next, output string) {
	if d := e.pattern.GetStates()[next].GetDelegate(); d != nil && d.GetItemsKey() == "" && state.vars[keptKey(next)] == "" && state.vars[askedKey(next)] == "" {
		// A set of one entered by a transition is a fresh walk every time: a
		// verification asked for again is run again. A resume, which re-enters
		// without a transition, continues a walk in progress — and so does an
		// entry whose child stopped and was reported.
		delete(state.vars, outcomesKeyFor(next))
		delete(state.vars, "__budget:"+next)
	}
	payload := parseSignalPayload(output)
	if len(payload) == 0 {
		return
	}
	// A list is for every list state that walks that name, not only the next
	// one: the suites a plan names are written by one state and proved by
	// another, and the plan says them once.
	for name, st := range e.pattern.GetStates() {
		d := st.GetDelegate()
		if d == nil || d.GetItemsKey() == "" {
			continue
		}
		raw, ok := payload[d.GetItemsKey()]
		if !ok || raw == nil {
			continue
		}
		rendered := ""
		switch v := raw.(type) {
		case string:
			rendered = v
		default:
			if body, err := json.Marshal(v); err == nil {
				rendered = string(body)
			}
		}
		if strings.TrimSpace(rendered) == "" {
			continue
		}
		if state.vars == nil {
			state.vars = map[string]string{}
		}
		state.vars[listKeyFor(name)] = rendered
		delete(state.vars, outcomesKeyFor(name))
		delete(state.vars, "__budget:"+name)
	}
}

// listField is the field a list rides on into the state named, if any, so a
// handover can leave it out of the words the next seat reads.
func (e *StateMachineExecutor) listField(next string) string {
	return e.pattern.GetStates()[next].GetDelegate().GetItemsKey()
}

// delegationItems resolves what to delegate over. No items key means one child,
// which is the set of one.
func (e *StateMachineExecutor) delegationItems(state *machineState, d *loomv1.DelegateState, name string) ([]listEntry, bool) {
	if d.GetItemsKey() == "" {
		return []listEntry{{}}, false
	}
	return parseList(state.vars[listKeyFor(name)]), true
}

// parseList reads a list as a seat wrote it: a JSON array of entries, or
// items separated by commas — a signal rides one line, so a seat naming its
// items there cannot separate them with newlines, and both read the same.
func parseList(raw string) []listEntry {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var objects []map[string]any
		if err := json.Unmarshal([]byte(raw), &objects); err == nil {
			var out []listEntry
			for _, obj := range objects {
				en := listEntry{Vars: map[string]string{}}
				for k, v := range obj {
					value := strings.TrimSpace(fmt.Sprint(v))
					switch k {
					case "item":
						en.Item = value
					case "dir":
						en.Dir = value
					default:
						en.Vars[k] = value
					}
				}
				if en.Item != "" {
					out = append(out, en)
				}
			}
			return out
		}
	}
	var items []listEntry
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == ','
	}) {
		if v := strings.TrimSpace(item); v != "" {
			items = append(items, listEntry{Item: v})
		}
	}
	return items
}

// childBudget divides what the parent has left between the children still to
// run. A floor exists because a child squeezed below what its work costs does
// not fail cleanly — it stops halfway, having spent the money.
func (e *StateMachineExecutor) childBudget(state *machineState, d *loomv1.DelegateState, pending int) (float64, error) {
	if pending == 0 || state.costCeiling <= 0 {
		return 0, nil
	}
	remaining := state.costCeiling - e.spend(state)
	if remaining <= 0 {
		return 0, fmt.Errorf("no budget left to delegate with")
	}
	each := remaining / float64(pending)
	if floor := d.GetMinBudgetUsd(); floor > 0 && each < floor {
		return 0, fmt.Errorf("$%.2f left over %d children is $%.2f each, below the $%.2f a child needs — raise the cap or cut the set",
			remaining, pending, each, floor)
	}
	return each, nil
}

// settledBudget is what a child gets, decided once for the set and remembered.
// Recomputing it on re-entry would divide what is left now among children
// that were promised a share of what was left then — and refuse a child taken
// up again when its promised share no longer fits.
func (e *StateMachineExecutor) settledBudget(state *machineState, d *loomv1.DelegateState, name string, pending int) (float64, error) {
	key := "__budget:" + name
	if v, ok := state.vars[key]; ok {
		var settled float64
		if _, err := fmt.Sscanf(v, "%g", &settled); err == nil {
			return settled, nil
		}
	}
	budget, err := e.childBudget(state, d, pending)
	if err != nil {
		return 0, err
	}
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	state.vars[key] = fmt.Sprintf("%g", budget)
	return budget, nil
}

// runChildren runs the pending children, up to max_parallel at a time, and
// returns what each finished as. The first suspension is carried back: a parked
// child is the parent's business, and the ones that finished are not re-run.
//
// kept is the child this state holds where it stopped, if it holds one: it is
// taken up there rather than started. Every child that stops is returned as
// it stopped. A child that cannot be run at all — a kept one that cannot be
// taken up, one whose machine cannot be walked — is returned as what blocks
// the state, and is nobody's outcome.
func (e *StateMachineExecutor) runChildren(ctx context.Context, state *machineState, d *loomv1.DelegateState, template *loomv1.WorkflowPattern, name string, pending []listEntry, budget float64, kept *keptChild, opening, enterAt string) (map[string]string, map[string]*loomv1.WorkflowCheckpoint, map[string]*loomv1.WorkflowResult, map[string]string, *blockedChild) {
	var (
		mu      sync.Mutex
		results = map[string]string{}
		ended   = map[string]*loomv1.WorkflowResult{}
		failed  = map[string]string{}
		stopped = map[string]*loomv1.WorkflowCheckpoint{}
		blocked *blockedChild
		wg      sync.WaitGroup
	)

	limit := int(d.GetMaxParallel())
	if limit <= 0 || limit > len(pending) {
		limit = len(pending)
	}
	slots := make(chan struct{}, limit)
	rec := e.orchestrator.record()

	// Where the child works is the parent's to say. The run's placing variables
	// come down the tree from the top, and a parent that has re-placed itself —
	// a hole that made itself a unit for the task loop it delegates to — hands
	// its own values down, not the top's.
	ctx = handDown(ctx, inheritedRunVars(ctx, state.vars))
	// What a child started opens with is the turn before it: the words this
	// state was entered with for the first, and, one after another, what the
	// child before it ended with. A child taken up where it stopped opens with
	// its own words, from its own record.
	words := opening

	for i, en := range pending {
		// Dispatch in the order the items were given: the next child starts only
		// when a slot frees, so a set walked one at a time is walked in order —
		// which is what a list ordered by dependency relies on.
		slots <- struct{}{}
		// A child that stopped has stopped the set: the children after it wait.
		// Nothing that has not started starts; whatever was already running
		// finishes. Otherwise a set walked one at a time would run every child
		// up to its own stop before anyone was asked anything.
		mu.Lock()
		halted := len(stopped) > 0 || blocked != nil
		opens := words
		mu.Unlock()
		if halted || ctx.Err() != nil {
			<-slots
			break
		}
		wg.Add(1)
		go func(i int, en listEntry, opens string) {
			item := en.Item
			defer wg.Done()
			defer func() { <-slots }()

			// The parent's variables, as they stand: a child that finishes
			// writes its outcome into them under mu, while this one reads.
			mu.Lock()
			parentVars := make(map[string]string, len(state.vars))
			for k, v := range state.vars {
				parentVars[k] = v
			}
			mu.Unlock()
			id := childID(e.workflowID, name, item)
			var mine *keptChild
			var takeUp *Place
			childCtx := handMessage(ctx, opens)
			if kept != nil && kept.item == item {
				mine = kept
				own := ""
				if place, ok := rec.PlaceOf(ctx, id); ok {
					own = place.Message
				}
				childCtx = handMessage(ctx, own)
			} else if place, ok := rec.PlaceOf(ctx, id); ok {
				switch {
				case !place.Ended && len(place.StatePath) > 0:
					// It stopped, or its walk died: it is taken up where its
					// own record says it stands.
					takeUp = &place
				case i == 0 && enterAt != "" && place.Ended && strings.EqualFold(place.Terminal, "DONE"):
					// Sent back in after it ran to done: taken up at the step
					// the route names, which runs its last child again.
					if again := sentBackIn(place.Vars, enterAt); again != nil {
						childCtx = withSentIn(withReentry(childCtx, enterAt, again), true)
					}
				}
			}
			terminal, cost, ckpt, result, err := e.runChild(childCtx, d, template, name, en, budget, parentVars, mine, takeUp)
			mu.Lock()
			defer mu.Unlock()
			if result != nil {
				ended[item] = result
				if limit == 1 {
					words = handoff(result.GetMergedOutput(), "")
				}
			}
			if cost != nil {
				// What a child spent is what the parent spent. A budget that
				// cannot see its children's cost is not a budget: the whole
				// point of dividing it is that the total is bounded.
				state.allResults = append(state.allResults, &loomv1.AgentResult{
					AgentId:  name + ":" + item,
					Metadata: map[string]string{"state": name, "delegated": item},
					Cost:     cost,
				})
			}
			var cannot *cannotRun
			switch {
			case ctx.Err() != nil:
				// The walk is being stopped: the child did not fail and did
				// not stop; it stands where its own record says.
			case ckpt != nil:
				stopped[item] = ckpt
			case errors.As(err, &cannot):
				if blocked == nil {
					blocked = &blockedChild{err: err, kept: mine != nil}
				}
			case err != nil:
				// A child that failed outright still has an outcome its parent
				// can route on; hiding it would strand the whole set.
				results[item] = "FAILED"
				failed[item] = err.Error()
				e.orchestrator.logger.Warn("Delegated child failed",
					zap.String("state", name), zap.String("item", item), zap.Error(err))
			default:
				results[item] = terminal
			}
			// A child that reached a terminal is recorded the moment it does,
			// not when the whole set returns: the set may never return in this
			// process, and whoever picks the run up must know which children
			// are done. mu serialises this with the other children.
			if t, ok := results[item]; ok {
				e.recordChildOutcome(state, d, name, item, t)
				for _, obs := range e.orchestrator.observers() {
					obs.ChildEnded(ctx, e.workflowID, name, id, t, state.vars)
				}
			}
		}(i, en, opens)
	}
	wg.Wait()
	return results, stopped, ended, failed, blocked
}

// blockedChild is a child of a state that could not be run at all, and so
// stops the state rather than being one of its outcomes; kept says it was the
// child the state held where it stopped.
type blockedChild struct {
	err  error
	kept bool
}

// cannotRun is a child that could not be run at all.
type cannotRun struct{ err error }

func (c *cannotRun) Error() string { return c.err.Error() }
func (c *cannotRun) Unwrap() error { return c.err }

// whyFailed says why the children that could not run could not: the reason
// alone for a single child, one "<item>: <reason>" per line for a set.
func whyFailed(failed map[string]string) string {
	items := make([]string, 0, len(failed))
	for item := range failed {
		items = append(items, item)
	}
	sort.Strings(items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			lines = append(lines, failed[item])
			continue
		}
		lines = append(lines, item+": "+failed[item])
	}
	return strings.Join(lines, "\n")
}

// runChild runs one child in its own directory, with its own budget and its own
// workflow id — so its sessions, its checkpoint and its spend are its own.
//
// A kept child is the same child — the same directory, budget and id, told the
// same things — taken up from where it stopped instead of started.
func (e *StateMachineExecutor) runChild(ctx context.Context, d *loomv1.DelegateState, template *loomv1.WorkflowPattern, name string, en listEntry, budget float64, parentVars map[string]string, kept *keptChild, takeUp *Place) (string, *loomv1.AgentExecutionCost, *loomv1.WorkflowCheckpoint, *loomv1.WorkflowResult, error) {
	item := en.Item
	child, err := e.childPattern(d, template, name, en, budget)
	if err != nil {
		return "", nil, nil, nil, err
	}

	// Give the child its place before asking it to work there. Delegating means
	// handing over a piece of the world, and a directory that does not exist is
	// not a piece of the world — every tool the child runs would refuse.
	if dir := child.GetStateMachine().GetDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", nil, nil, nil, fmt.Errorf("could not make %s for %q: %w", dir, item, err)
		}
	}

	// A child is told what it is before it works: the constants this delegation
	// carries, and its own item — the repo, the seat, the cluster it was fanned
	// out for. They ride beside what it inherits from the parent.
	given := map[string]string{}
	for k, v := range RunVarsFromContext(ctx) {
		given[k] = v
	}
	// A constant is spelled from what the parent holds — its placing, its own
	// item, what its list said — so a delegation can say `task: "{item}"` and
	// name the parent's item for a child that walks that task's suites.
	spelling := map[string]string{}
	for k, v := range parentVars {
		spelling[k] = v
	}
	for k, v := range given {
		spelling[k] = v
	}
	for k, v := range d.GetVars() {
		given[k] = interpolateRun(v, map[string]string{ParamRun: e.workflowID}, spelling)
	}
	// What the list said about this entry fills the constants the spec
	// declared for it; a field the spec did not declare is not the child's.
	for k, v := range en.Vars {
		if _, declared := d.GetVars()[k]; declared {
			given[k] = v
		}
	}
	if item != "" {
		given["item"] = item
	}
	// Sent back into a child that stopped, the child takes up where it
	// stopped: at the step that ended it, holding what it had worked out, with
	// what it is sent back with in front of that step.
	if at, worked := reentryOf(ctx); at != "" {
		for k, v := range worked {
			if _, placed := given[k]; !placed {
				given[k] = v
			}
		}
		if sentInOf(ctx) {
			// A route named the step: the child's loop must have it, and it
			// must be a step that runs children.
			if st, ok := child.GetStateMachine().GetStates()[at]; !ok || st.GetDelegate() == nil {
				return "", nil, nil, nil, &cannotRun{fmt.Errorf("state %q: its route sends %s back in at %q, which is not a step of its loop that runs children", name, item, at)}
			}
		}
		ctx = withEnter(ctx, at)
		// The re-entry is this child's alone: the loops it runs begin where
		// they begin.
		ctx = withReentry(ctx, "", nil)
		ctx = withSentIn(ctx, false)
	}
	ctx = WithRunVars(ctx, given)

	if kept != nil {
		// What it spent before it stopped reached the parent then; what it
		// spends from here is added now.
		result, err := e.orchestrator.ResumeWorkflow(e.handDown(ctx), child, kept.ckpt, kept.decision)
		if err != nil {
			var s *WorkflowSuspended
			if errors.As(err, &s) {
				return "", sinceResume(spentBy(s.Checkpoint), spentBy(kept.ckpt)), s.Checkpoint, nil, nil
			}
			return "", nil, nil, nil, &cannotRun{fmt.Errorf("resume: delegated workflow in state %q failed: %w", name, err)}
		}
		return nestedSignal(result), sinceResume(nestedCost(result), spentBy(kept.ckpt)), nil, result, nil
	}

	if takeUp != nil {
		// Taken up where its own record says it stands, with its own words.
		ckpt := &loomv1.WorkflowCheckpoint{
			CheckpointVersion: CheckpointVersion,
			WorkflowId:        child.GetWorkflowId(),
			PatternType:       "state_machine",
			StatePath:         []string{takeUp.At()},
			Vars:              takeUp.Vars,
			CreatedAtMs:       time.Now().UnixMilli(),
		}
		result, err := e.orchestrator.ResumeWorkflow(e.handDown(handMessage(ctx, takeUp.Message)), child, ckpt,
			&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
		if err != nil {
			var s *WorkflowSuspended
			if errors.As(err, &s) {
				return "", spentBy(s.Checkpoint), s.Checkpoint, nil, nil
			}
			var unwalkable *NotWalkable
			if (errors.As(err, &unwalkable) || strings.HasPrefix(err.Error(), "resume:")) && ctx.Err() == nil {
				return "", nil, nil, nil, &cannotRun{fmt.Errorf("state %q: its child %s cannot be taken up where it stands: %w", name, item, err)}
			}
			return "", nil, nil, nil, err
		}
		return nestedSignal(result), nestedCost(result), nil, result, nil
	}

	for _, obs := range e.orchestrator.observers() {
		obs.ChildStarted(ctx, e.workflowID, name, child)
	}
	result, err := e.orchestrator.ExecutePattern(e.handDown(ctx), child)
	if err != nil {
		var s *WorkflowSuspended
		if errors.As(err, &s) {
			// A parked child has already spent what it spent; that must reach
			// the parent even though the child has not finished.
			return "", spentBy(s.Checkpoint), s.Checkpoint, nil, nil
		}
		// A child whose machine cannot be walked as it is defined has not
		// failed at its work: it never began. It stops this state, saying why,
		// and is run when the state is entered again.
		var unwalkable *NotWalkable
		if errors.As(err, &unwalkable) && ctx.Err() == nil {
			return "", nil, nil, nil, &cannotRun{fmt.Errorf("state %q: its child %s cannot be walked: %w", name, item, err)}
		}
		return "", nil, nil, nil, err
	}
	return nestedSignal(result), nestedCost(result), nil, result, nil
}

// routes reports whether a state's routing table has a word.
func (e *StateMachineExecutor) routes(name, word string) bool {
	_, ok := e.pattern.GetStates()[name].GetRoute()[normalizeSignal(word)]
	return ok
}

// uniform reports whether every entry ended the same way.
func uniform(done map[string]string, entries []listEntry) bool {
	if len(entries) == 0 {
		return false
	}
	first := done[entries[0].Item]
	for _, en := range entries[1:] {
		if done[en.Item] != first {
			return false
		}
	}
	return first != ""
}

// spentBy totals what a suspended run had spent when it parked.
func spentBy(ckpt *loomv1.WorkflowCheckpoint) *loomv1.AgentExecutionCost {
	total := &loomv1.AgentExecutionCost{}
	for _, r := range ckpt.GetAllResults() {
		if r.Cost != nil {
			total.CostUsd += r.Cost.CostUsd
			total.TotalTokens += r.Cost.TotalTokens
		}
	}
	return total
}

// childPattern builds the workflow a child actually runs: its own directory,
// its own budget, its own id. A child taken up again is built the same way,
// on the budget that was settled for it.
func (e *StateMachineExecutor) childPattern(d *loomv1.DelegateState, template *loomv1.WorkflowPattern, name string, en listEntry, budget float64) (*loomv1.WorkflowPattern, error) {
	child, ok := proto.Clone(template).(*loomv1.WorkflowPattern)
	if !ok {
		return nil, fmt.Errorf("could not copy the child workflow")
	}
	child.WorkflowId = childID(e.workflowID, name, en.Item)
	if sm := child.GetStateMachine(); sm != nil {
		dir, err := e.childDir(d, en)
		if err != nil {
			return nil, err
		}
		sm.Dir = dir
		// A child's ceiling is its own or its share of the parent's, whichever is
		// less. Its own is what the work should cost; the share is what is left to
		// spend. Taking the share alone hands the first child of a long run the
		// whole remaining balance.
		if budget > 0 && (sm.GetMaxCostUsd() <= 0 || budget < sm.GetMaxCostUsd()) {
			sm.MaxCostUsd = budget
		}
	}
	return child, nil
}

func childID(workflowID, state, item string) string {
	id := workflowID + "-" + state
	if item != "" {
		id += "-" + item
	}
	return id
}

// itemOf recovers which child a checkpoint belongs to. The id says so: a child
// is named for the parent, the state that delegated, and the item.
func itemOf(workflowID, state, childWorkflowID string) string {
	base := childID(workflowID, state, "")
	if childWorkflowID == base {
		return "" // the set of one: there is no item to name
	}
	return strings.TrimPrefix(childWorkflowID, base+"-")
}

// childDir is where a child works: where the list said, else where the
// state's template says, else where the parent works. The place is always
// under the parent's folder: a list is a model's words, and a place that
// climbs out of the parent's folder or names an absolute one is refused
// rather than created.
func (e *StateMachineExecutor) childDir(d *loomv1.DelegateState, en listEntry) (string, error) {
	sub := en.Dir
	if sub == "" {
		if d.GetDirTemplate() == "" {
			return e.dir, nil
		}
		if strings.ContainsAny(en.Item, `/\`) || en.Item == ".." {
			return "", fmt.Errorf("child %q is not one name, so it cannot be spelled into a folder", en.Item)
		}
		sub = strings.ReplaceAll(d.GetDirTemplate(), "{item}", en.Item)
	}
	if filepath.IsAbs(sub) || !filepath.IsLocal(sub) {
		return "", fmt.Errorf("child %q is placed at %q, which is not under its parent's folder", en.Item, sub)
	}
	if e.dir == "" {
		return sub, nil
	}
	return filepath.Join(e.dir, sub), nil
}

// outcomes reads what the children have finished as so far, so a re-entered
// delegation runs only what still has to run.
func outcomesKeyFor(name string) string { return "__outcomes:" + name }

func (e *StateMachineExecutor) outcomes(state *machineState, name string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(state.vars[outcomesKeyFor(name)], "\n") {
		if item, terminal, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[item] = terminal
		}
	}
	return out
}

// setOutcomes remembers what the children finished as. It is kept under an
// internal key whether or not the process asked for one, because that record is
// what stops a re-entered set paying for its finished children a second time;
// the declared key is the copy the next state reads.
func (e *StateMachineExecutor) setOutcomes(state *machineState, name string, done map[string]string) {
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	state.vars[outcomesKeyFor(name)] = summarise(done)
}

// summarise renders the children's outcomes as run state the next step reads.
func summarise(done map[string]string) string {
	items := make([]string, 0, len(done))
	for item := range done {
		items = append(items, item)
	}
	sort.Strings(items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item+"="+done[item])
	}
	return strings.Join(lines, "\n")
}

// inheritedRunVars is the run's placing variables as the parent now holds
// them: what came down from above, overridden by anything the parent's own
// states have set.
func inheritedRunVars(ctx context.Context, parentVars map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range RunVarsFromContext(ctx) {
		// What the engine keeps for a machine — its lists, its children's
		// outcomes, a child it holds — is that machine's alone. A machine taken
		// up again is given all its variables back, and none of those is its
		// children's to inherit: a child with a state of the same name would
		// read its parent's walk as its own.
		if strings.HasPrefix(k, ownPrefix) {
			continue
		}
		out[k] = v
	}
	// Where the child works is the parent's to say, and that is all a parent
	// hands down: what a step gives the next is its handover, the message in
	// flight, never a value substituted into the child's text.
	for _, k := range []string{"ws", "unit_dir", "tera_home", "feature"} {
		if v := parentVars[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

// stoppedAtKey and stoppedVarsKey are where a parent keeps where its single
// child stopped and what the child had worked out, until it is entered again.
func stoppedAtKey(state string) string   { return "__stopped:" + state }
func stoppedVarsKey(state string) string { return "__stopped_vars:" + state }

type enterKey struct{}
type reentryKey struct{}

type reentry struct {
	at     string
	worked map[string]string
}

// withEnter tells the next walk started with this context to begin at a state
// of its own rather than its entry state.
func withEnter(ctx context.Context, state string) context.Context {
	return context.WithValue(ctx, enterKey{}, state)
}

// EntryOf is where a walk started with this context begins: the state it was
// told to begin at, when the machine has it, and its entry state otherwise.
// A child taken up again begins at the step that ended it.
func EntryOf(ctx context.Context, p *loomv1.StateMachinePattern) string {
	return entryOf(ctx, p)
}

// entryOf is where a walk begins: the state it was told to begin at, when the
// machine has it, and its entry state otherwise.
func entryOf(ctx context.Context, p *loomv1.StateMachinePattern) string {
	if at, _ := ctx.Value(enterKey{}).(string); at != "" {
		if _, ok := p.GetStates()[at]; ok {
			return at
		}
	}
	return p.GetEntryState()
}

func withReentry(ctx context.Context, at string, worked map[string]string) context.Context {
	return context.WithValue(ctx, reentryKey{}, reentry{at: at, worked: worked})
}

func reentryOf(ctx context.Context) (string, map[string]string) {
	r, _ := ctx.Value(reentryKey{}).(reentry)
	return r.at, r.worked
}

// varsJSON and decodeVars carry a walk's variables across the end of a child.
func varsJSON(vars map[string]string) string {
	if len(vars) == 0 {
		return ""
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeVars(s string) map[string]string {
	out := map[string]string{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

// ownPrefix opens the name of every variable the engine keeps for a machine's
// own walk.
const ownPrefix = "__"

// A kept child.
//
// A child that stops is normally the whole tree's stop: the parent records
// its position around the child's and parks. A state that takes up its
// child's stop itself — an escalation it routes, a park a person has answered
// — keeps the child instead: the child's checkpoint, whole, among the
// machine's own variables, so it is there after a restart and goes wherever
// the machine's position goes. The next entry into the state takes the child
// up from it.

// A child that parked the whole tree for a person — a gate, a ceiling, a step
// that could not run — is held by the state that ran it until the person's
// decision comes back, and is taken up with that decision. Every other stop is
// on the child's own record.

// keptKey is where a state holds a child that parked the tree for a person.
func keptKey(state string) string { return "__kept:" + state }

// keptChild is a child a state holds where it stopped: the item it was
// delegated for, its checkpoint, and the decision it is taken up with.
type keptChild struct {
	item     string
	ckpt     *loomv1.WorkflowCheckpoint
	decision *loomv1.GateDecision
	// answer says the decision is a person's answer to the park this walk
	// was resumed from. A child that cannot be taken up then refuses the
	// resume, and the run stays parked as it was.
	answer bool
}

// keep holds a stopped child for the state that delegated to it.
func (e *StateMachineExecutor) keep(state *machineState, name string, ckpt *loomv1.WorkflowCheckpoint) error {
	body, err := proto.Marshal(ckpt)
	if err != nil {
		return fmt.Errorf("state %q: the stopped child could not be kept: %w", name, err)
	}
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	state.vars[keptKey(name)] = base64.StdEncoding.EncodeToString(body)
	return nil
}

// keptAt is the child a state holds where it stopped, or nil. A value that
// cannot be read is an error: the child is not let go because of it.
func (e *StateMachineExecutor) keptAt(state *machineState, name string) (*loomv1.WorkflowCheckpoint, error) {
	raw := state.vars[keptKey(name)]
	if raw == "" {
		return nil, nil
	}
	ckpt, err := decodeCheckpoint(raw)
	if err != nil {
		return nil, fmt.Errorf("state %q: the child kept for it cannot be read: %w", name, err)
	}
	return ckpt, nil
}

// takeKept takes the child a state holds, to be run with the children still
// to run, with the person's decision on the park the walk was resumed from. A
// held child the walk has moved past — a new list without its item — is
// among none of the children to run, and is let go.
func (e *StateMachineExecutor) takeKept(state *machineState, name string) (*keptChild, error) {
	ckpt, err := e.keptAt(state, name)
	if ckpt == nil || err != nil {
		return nil, err
	}
	delete(state.vars, keptKey(name))
	kept := &keptChild{item: itemOf(e.workflowID, name, ckpt.GetWorkflowId()), ckpt: ckpt, decision: state.answer, answer: state.answer != nil}
	if kept.decision == nil {
		kept.decision = &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE}
	}
	return kept, nil
}

func decodeCheckpoint(raw string) (*loomv1.WorkflowCheckpoint, error) {
	body, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	ckpt := &loomv1.WorkflowCheckpoint{}
	if err := proto.Unmarshal(body, ckpt); err != nil {
		return nil, err
	}
	return ckpt, nil
}

// escalated is a child's stop as this state's own word: the work below cannot
// go on. The child stands where it stopped, on its own record; this state
// notes which child it reported, so that the answer, when the walk comes back
// with it, is written on that child's record.
func (e *StateMachineExecutor) escalated(state *machineState, name string, fanned bool, children int, item string, ckpt *loomv1.WorkflowCheckpoint, startTime time.Time) (string, string, error) {
	output := EscalatedSignal
	if fanned {
		output = item + "=" + EscalatedSignal
	}
	// Whoever hears it is told where it came from before what was said: the
	// step that stopped, down through every loop between, what that step can
	// end with, and the word that reaches it.
	output += "\n\n" + e.raisedFrom(name, item, ckpt)
	state.vars[askedKey(name)] = "item:" + item
	e.record(state, name, name, &loomv1.AgentResult{
		AgentId: name,
		Output:  output,
		Metadata: map[string]string{
			"state":    name,
			"children": fmt.Sprintf("%d", children),
		},
		ConfidenceScore: 1.0,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Cost:            &loomv1.AgentExecutionCost{},
	})
	return EscalatedSignal, output, nil
}

// endedOtherwise is how a state answers for a child that ended any way but
// done: with that end, and with the whole of what the step that ended it said.
// An end the state does not route is a word nobody expected of it, and so it
// is the state's escalation — heard by the machine's manager, or by the
// nearest one above.
func (e *StateMachineExecutor) endedOtherwise(name, terminal, output string, ended *loomv1.WorkflowResult) (string, string) {
	if said := strings.TrimSpace(ended.GetMergedOutput()); said != "" {
		output += "\n\n" + said
	}
	if !e.routes(name, terminal) {
		return EscalatedSignal, output
	}
	return terminal, output
}

// raisedFrom is where an escalation a state raises for a child came from, as
// whoever hears it is told: the states down to the step that stopped, each
// with the item it was running; what that step can end with; and, in a machine
// with a manager, the word of the manager's that reaches it — which reaches
// that step and no other.
func (e *StateMachineExecutor) raisedFrom(name, item string, ckpt *loomv1.WorkflowCheckpoint) string {
	here := name
	if item != "" {
		here += " (" + item + ")"
	}
	word, said, ends := "", "", ""
	if results := ckpt.GetAllResults(); len(results) > 0 {
		last := results[len(results)-1]
		word = normalizeSignal(last.GetMetadata()["signal"])
		said = strings.TrimSpace(last.GetOutput())
		ends = last.GetMetadata()[endsKey]
	}
	manager := ""
	if w := e.managersWordFor(name); w != "" {
		manager = fmt.Sprintf("Your word `%s` goes back to this step only. No other step runs until this one ends with its word.", w)
	}
	// A child that is itself a step reporting its own child's stop already
	// says where the stop came from: this state's place goes in front of it.
	if word == EscalatedSignal {
		if at := strings.Index(said, "From: "); at >= 0 {
			below := said[at+len("From: "):]
			head, rest, _ := strings.Cut(below, "\n\nIt said:")
			lines := strings.Split(head, "\n")
			out := []string{"From: " + here + " › " + lines[0]}
			for _, l := range lines[1:] {
				if !strings.HasPrefix(l, "Your word `") {
					out = append(out, l)
				}
			}
			if manager != "" {
				out = append(out, manager)
			}
			text := strings.Join(out, "\n")
			if rest != "" {
				text += "\n\nIt said:" + rest
			}
			return text
		}
	}
	lines := []string{"From: " + here + " › " + firstOf(ckpt.GetStatePath())}
	if ends != "" || word != "" {
		lines = append(lines, canEndWith(ends))
	}
	if manager != "" {
		lines = append(lines, manager)
	}
	text := strings.Join(lines, "\n")
	if said != "" {
		text += "\n\nIt said:\n" + said
	}
	return text
}

// firstOf is the last state of a path: where a run stands.
func firstOf(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return path[len(path)-1]
}

// managersWordFor is the word the machine's manager sends the walk to a state
// with, when the state's escalation is the manager's to hear; "" otherwise.
func (e *StateMachineExecutor) managersWordFor(name string) string {
	seat := e.pattern.GetLogicSeat()
	st := e.pattern.GetStates()[name]
	if seat == "" || st == nil {
		return ""
	}
	word := EscalateSignal
	if st.GetDelegate() != nil {
		word = EscalatedSignal
	}
	if st.GetRoute()[word].GetNextState() != seat {
		return ""
	}
	return routedWord(e.pattern.GetStates()[seat], name)
}

// routeEndsToManager gives a managed machine's state that runs one child a
// route to the manager for each end of that child that is not done and that
// the state does not route itself — as the child is defined when the state
// runs it.
func (e *StateMachineExecutor) routeEndsToManager(name string, child *loomv1.WorkflowPattern) {
	seat := e.pattern.GetLogicSeat()
	st := e.pattern.GetStates()[name]
	if seat == "" || name == seat || st == nil || e.pattern == e.spec {
		return
	}
	for _, end := range childEnds(child) {
		if st.Route[end] == nil {
			if st.Route == nil {
				st.Route = map[string]*loomv1.Transition{}
			}
			st.Route[end] = &loomv1.Transition{Target: &loomv1.Transition_NextState{NextState: seat}}
		}
	}
}

// refusedAnswer is a person's answer to a park that could not be applied:
// the child it was for could not be taken up. The resume is refused, and the
// run is left as it was parked.
type refusedAnswer struct{ err error }

func (r *refusedAnswer) Error() string { return r.err.Error() }
func (r *refusedAnswer) Unwrap() error { return r.err }

// childOf is the workflow a delegating state runs, and the context its
// children run under.
//
// A child written into the state is that. A child given by name is found now,
// by whoever hosts the engine, so the one that runs is the one defined when
// the state is entered — each time it is entered. Names are followed down the
// tree: a workflow that reaches itself again through the workflows it
// delegates to would never end, and is refused where it closes, with the
// chain named.
func (e *StateMachineExecutor) childOf(ctx context.Context, d *loomv1.DelegateState, state string) (*loomv1.WorkflowPattern, context.Context, error) {
	if inline := d.GetWorkflow(); inline != nil {
		return inline, ctx, nil
	}
	name := d.GetWorkflowName()
	if name == "" {
		return nil, ctx, fmt.Errorf("state %q delegates but names no workflow", state)
	}
	chain, _ := ctx.Value(delegatedKey{}).([]string)
	for _, above := range chain {
		if above == name {
			return nil, ctx, fmt.Errorf("state %q delegates to %q, which delegates to itself through %s",
				state, name, strings.Join(append(append([]string(nil), chain...), name), " -> "))
		}
	}
	found, err := e.orchestrator.workflowNamed(ctx, name)
	if err != nil {
		return nil, ctx, fmt.Errorf("state %q delegates to %q: %w", state, name, err)
	}
	return found, context.WithValue(ctx, delegatedKey{}, append(append([]string(nil), chain...), name)), nil
}

// delegatedKey carries the names of the workflows delegated to by name, from
// the top of the tree down to the machine walking.
type delegatedKey struct{}

// WithWorkflowName says the workflow about to be run or resumed is the one of
// this name, so a workflow that delegates back to it by name is refused where
// the chain closes rather than one level inside itself.
func WithWorkflowName(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, delegatedKey{}, []string{name})
}
