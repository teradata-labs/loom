// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	// HookCheckpoint is the reserved edge hook: it forces a checkpoint write
	// before the walk moves, rather than being handed to the ActionHandler.
	HookCheckpoint = "checkpoint"

	// DismissAll is the dismiss entry that ends every live session.
	DismissAll = "*"

	// defaultMaxTransitions bounds a walk whose graph does not converge. It is
	// deliberately generous: the bound exists to turn a runaway into a
	// suspension a human can look at, not to cap normal work.
	defaultMaxTransitions = 100
)

// StateMachineExecutor walks a StateMachinePattern.
//
// The other executors run an arrangement of agents once. This one walks a
// graph: the pattern is the graph, a run is the path, and the same state may
// be visited any number of times. Routing is a map lookup on a signal the
// state already produced, so a healthy transition makes no LLM call and costs
// nothing — models appear only inside agent-bodied states, and at the
// exceptions the route table cannot resolve.
type StateMachineExecutor struct {
	orchestrator *Orchestrator
	// spec is the machine as its author wrote it; pattern is the machine as
	// it runs — the spec with its manager seated, from the moment the machine
	// is taken up.
	spec       *loomv1.StateMachinePattern
	pattern    *loomv1.StateMachinePattern
	workflowID string

	// The directory this run acts on. The world a process operates on is a
	// directory: tool states run in it, and a child run gets one beneath it.
	dir string
}

// NewStateMachineExecutor creates a new state machine executor.
func NewStateMachineExecutor(orchestrator *Orchestrator, pattern *loomv1.StateMachinePattern, workflowID string) *StateMachineExecutor {
	return &StateMachineExecutor{
		orchestrator: orchestrator,
		spec:         pattern,
		pattern:      pattern,
		workflowID:   workflowID,
		dir:          pattern.GetDir(),
	}
}

// machineState is the walk state of a run. It is built fresh by Execute and
// rehydrated from a WorkflowCheckpoint by Resume; suspension only happens
// between states, so this fully describes a run.
type machineState struct {
	// State to enter next.
	current string

	// last is the state the walk last left: when the walk ends, the one that
	// ended it.
	last string

	// Transitions taken so far.
	transitions int32

	// What the loop keeps across states: the items it is working through, what
	// its children finished as, where its cursor is.
	vars map[string]string

	// Re-entries per loopback state. Stalling is a property of a cycle, not of
	// a long run: counting every move would park a healthy epic and miss a
	// review that has bounced six times.
	visits map[string]int32

	// Append-only execution history for cost accounting.
	allResults []*loomv1.AgentResult

	// entered is where the current state's results begin in allResults: what
	// it has spent is everything from here. It moves on once a transition out
	// of the state is recorded, so what one visit spent is recorded once.
	entered int
	// carry is how many results at the end of allResults belong to the state
	// about to be entered: the visit it is re-entered for had spent them before
	// it stopped part-way, or before it was asked to restate its signal.
	carry int

	// One snapshot per state visited, in visit order.
	snapshots []*loomv1.CheckpointStageSnapshot

	// agent_id -> model name.
	modelsUsed map[string]string

	// REVISE round-trips consumed per gated state (state name -> count).
	// Keyed by state, not agent: one role commonly owns several gated states,
	// and an agent-keyed budget would let a revision in one exhaust another's.
	gateRevisions map[string]int32

	// The prose the last state handed forward. A state's output does two jobs:
	// its final line routes, and everything above it is what the next state
	// needs to know. Conflating them means either a router parsing prose or a
	// handoff squeezed into one word.
	message string

	// Feedback to thread into the next state's prompt (consumed once).
	revisionFeedback string

	// A person's answer to the park this walk was resumed from, when the park
	// was a child's: the decision the kept child is taken up with. It is for
	// the state the walk is resumed into, and is gone once that state has run.
	answer *loomv1.GateDecision

	// An answer the loop cannot use goes back to the agent that gave it, once
	// per answer. sendBack is what the agent is told, consumed by its next
	// turn; sentBack says the answer now awaited is the one sent back for.
	sendBack string
	sentBack bool

	// Ceilings in force. Taken from the pattern on a fresh run and from the
	// checkpoint's grants on a resumed one.
	costCeiling       float64
	transitionCeiling int32
}

// Execute walks the machine from its entry state.
func (e *StateMachineExecutor) Execute(ctx context.Context) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.state_machine")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)
	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.type", "state_machine")
		workflowSpan.SetAttribute("workflow.id", e.workflowID)
		workflowSpan.SetAttribute("machine.entry_state", e.pattern.GetEntryState())
		workflowSpan.SetAttribute("machine.state_count", fmt.Sprintf("%d", len(e.pattern.GetStates())))
	}

	if err := e.validate(ctx); err != nil {
		return nil, &NotWalkable{err}
	}

	state := &machineState{
		current:           entryOf(ctx, e.pattern),
		modelsUsed:        make(map[string]string),
		gateRevisions:     make(map[string]int32),
		visits:            map[string]int32{},
		vars:              seedRunVars(ctx, map[string]string{}),
		message:           InitialMessageFromContext(ctx),
		costCeiling:       e.pattern.GetMaxCostUsd(),
		transitionCeiling: e.maxTransitions(),
	}

	// Where this walk was told to start is for this walk only; the loops it
	// delegates to start where their own definitions say.
	ctx = withEnter(ctx, "")

	e.orchestrator.logger.Info("Starting state machine execution",
		zap.String("workflow_id", e.workflowID),
		zap.String("entry_state", state.current),
		zap.Int("states", len(e.pattern.GetStates())))

	return e.walk(ctx, startTime, state)
}

// Resume continues a suspended machine from a WorkflowCheckpoint.
//
// APPROVE means "carry on from here": at a gate it routes on the recorded
// signal, and at a ceiling it grants one further ceiling's worth. REVISE
// re-enters the state the run stopped in, with the decision's feedback in the
// prompt — including for an unroutable signal, where steering the worker until
// it emits something the table accepts is the whole recovery.
//
// A run that stopped inside a child is resumed by entering the state that
// delegated: it keeps the child as it stopped and takes it up with the
// decision. A machine resumed as someone's child opens with what its parent
// sent it back with, the way a machine started as a child does.
//
// The machine resumed is the definition it is given, as that definition
// stands now: a checkpoint is a position and what the run has worked out, and
// holds no copy of the machine. A run goes on under a definition changed
// since it stopped; the one thing a changed definition can refuse is the
// position — a run standing in a state the definition no longer has stays
// where it is, and the refusal names the state.
func (e *StateMachineExecutor) Resume(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint, decision *loomv1.GateDecision) (*loomv1.WorkflowResult, error) {
	startTime := time.Now()

	if err := e.validate(ctx); err != nil {
		return nil, &NotWalkable{err}
	}

	if len(ckpt.StatePath) == 0 {
		return nil, fmt.Errorf("resume: checkpoint has no state path")
	}
	current := ckpt.StatePath[0]
	st, ok := e.pattern.GetStates()[current]
	if !ok {
		return nil, fmt.Errorf("resume: the run stands at state %q, which this definition does not have", current)
	}

	ctx, workflowSpan := e.orchestrator.tracer.StartSpan(ctx, "workflow.state_machine.resume")
	defer e.orchestrator.tracer.EndSpan(workflowSpan)
	if workflowSpan != nil {
		workflowSpan.SetAttribute("workflow.id", ckpt.WorkflowId)
		workflowSpan.SetAttribute("resume.state", current)
		workflowSpan.SetAttribute("resume.reason", ckpt.SuspendReason.String())
		workflowSpan.SetAttribute("resume.decision", decision.Action.String())
	}

	state := &machineState{
		current:     current,
		transitions: ckpt.GrantedTransitions, // transitions already taken are folded into the grant
		allResults:  ckpt.AllResults,
		// What the checkpoint holds was spent and reported before the stop.
		entered:           len(ckpt.AllResults),
		snapshots:         ckpt.StageSnapshots,
		modelsUsed:        ckpt.ModelsUsed,
		gateRevisions:     ckpt.GateRevisionCounts,
		vars:              ckpt.Vars,
		message:           InitialMessageFromContext(ctx),
		costCeiling:       e.pattern.GetMaxCostUsd(),
		transitionCeiling: e.maxTransitions(),
	}
	if state.modelsUsed == nil {
		state.modelsUsed = make(map[string]string)
	}
	if state.gateRevisions == nil {
		state.gateRevisions = make(map[string]int32)
	}
	state.visits = map[string]int32{}
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	// The checkpoint's vars are what the run has worked out; the seed is only
	// what it started with, so it fills gaps and never overwrites.
	state.vars = seedRunVars(ctx, state.vars)
	// A child stands where it stopped on its own record. Copies of a stopped
	// child kept in a parent's variables — as an earlier engine kept them —
	// are not where it stands, and are let go; a person's decision on a child
	// that parked the tree is held again below, for this entry.
	for key := range state.vars {
		for _, held := range []string{"__kept:", "__waiting:", "__unheard:", "__kept_words:", "__entered:"} {
			if strings.HasPrefix(key, held) {
				delete(state.vars, key)
			}
		}
	}
	if state.message == "" {
		// Taken up with no words handed to it: it reads the words it stood with.
		state.message = state.vars[messageKey]
	}
	delete(state.vars, messageKey)
	if ckpt.GrantedCostUsd > 0 {
		state.costCeiling = ckpt.GrantedCostUsd
	}
	if ckpt.GrantedTransitions > 0 {
		state.transitionCeiling = ckpt.GrantedTransitions
	}
	state.transitions = int32(len(ckpt.StageSnapshots)) // #nosec G115 -- visit counts are tiny

	restoreWorkflowMemory(ctx, e.orchestrator, ckpt)

	switch decision.Action {
	case loomv1.GateAction_GATE_ACTION_APPROVE:
		switch ckpt.SuspendReason {
		case loomv1.SuspendReason_SUSPEND_REASON_GATE:
			// The body already ran, and the gate stopped the walk before anything
			// was done with what it said. So everything the walk does with a
			// state's output is done here: what it worked out is kept, its signal
			// is read the way every signal is, and what it said is handed on.
			gated := ckpt.PendingGate.GetStageOutput()
			applyVars(state, gated)
			next, err := e.resolveTransition(ctx, state, st, current, signalOf(gated), gated)
			if err != nil {
				if errors.Is(err, errUnroutable) {
					// What the gated state said is not a word its table routes —
					// it escalated, say. Approving its output does not make the
					// word routable: the run stops there as it would have
					// without a gate, for whoever settles it.
					return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, nil)
				}
				return nil, err
			}
			next = e.leaving(state, current, next, signalOf(gated))
			e.takeList(state, next, gated)
			state.message = handoff(gated, e.listField(next))
			state.current = next

		case loomv1.SuspendReason_SUSPEND_REASON_COST_CAP:
			state.costCeiling = e.spend(state) + e.pattern.GetMaxCostUsd()
			e.orchestrator.logger.Info("Resuming past cost cap",
				zap.Float64("granted_usd", state.costCeiling))

		case loomv1.SuspendReason_SUSPEND_REASON_MAX_TRANSITIONS:
			state.transitionCeiling = state.transitions + e.maxTransitions()

		case loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE:
			// Re-enter unchanged: the worker gets another turn to emit a
			// signal the table accepts.

		case loomv1.SuspendReason_SUSPEND_REASON_ERROR:
			// Re-enter unchanged: whatever stopped the state from running — a
			// model that could not be reached, a credential that had expired —
			// is what the person resuming has dealt with.

		case loomv1.SuspendReason_SUSPEND_REASON_UNSPECIFIED:
			// An edge checkpoint: the walk never parked, it died — killed,
			// crashed, the machine asleep — after crossing an edge marked to be
			// kept. It records the state being entered; enter it.

		case loomv1.SuspendReason_SUSPEND_REASON_NESTED:
			if err := e.answerNested(state, st, current, decision, ckpt.NestedCheckpoint); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("resume: checkpoint has no suspend reason")
		}

	case loomv1.GateAction_GATE_ACTION_REVISE:
		if strings.TrimSpace(decision.Feedback) == "" {
			return nil, fmt.Errorf("resume: REVISE decision requires feedback")
		}
		if ckpt.SuspendReason == loomv1.SuspendReason_SUSPEND_REASON_NESTED {
			if err := e.answerNested(state, st, current, decision, ckpt.NestedCheckpoint); err != nil {
				return nil, err
			}
			break
		}
		if st.GetDelegate() != nil && ckpt.SuspendReason != loomv1.SuspendReason_SUSPEND_REASON_GATE {
			// A state that runs a child, stopped because that child stopped:
			// the person's words are the answer to what the child asked.
			state.message = decision.Feedback
			if state.vars[askedKey(current)] != "" {
				state.vars[answeringKey(current)] = "1"
			}
			break
		}
		if ckpt.SuspendReason == loomv1.SuspendReason_SUSPEND_REASON_GATE {
			gate := st.GetHitlGate()
			if gate == nil {
				return nil, fmt.Errorf("resume: checkpointed gate state %q has no hitl_gate", current)
			}
			if target := gate.GetReviseTargetStageId(); target != "" {
				if _, ok := e.pattern.GetStates()[target]; !ok {
					return nil, fmt.Errorf("resume: revise target %q is not a state in this machine", target)
				}
				state.current = target
			}
			state.gateRevisions[current]++
			if state.gateRevisions[current] > maxGateRevisions(gate) {
				return nil, fmt.Errorf("resume: gate on state %s exceeded max_revisions (%d)", current, maxGateRevisions(gate))
			}
		}
		state.revisionFeedback = decision.Feedback

	default:
		return nil, fmt.Errorf("resume: unsupported gate decision %s", decision.Action)
	}

	e.orchestrator.logger.Info("Resuming state machine execution",
		zap.String("workflow_id", ckpt.WorkflowId),
		zap.String("state", state.current),
		zap.String("decision", decision.Action.String()))

	state.carry = unfinished(state.allResults)
	return e.walk(ctx, startTime, state)
}

// walk is the machine itself: run the state, route on what it emitted, repeat.
func (e *StateMachineExecutor) walk(ctx context.Context, startTime time.Time, state *machineState) (*loomv1.WorkflowResult, error) {
	for {
		st, ok := e.pattern.GetStates()[state.current]
		if !ok {
			return nil, fmt.Errorf("state machine entered unknown state %q", state.current)
		}

		state.entered = len(state.allResults) - state.carry
		state.carry = 0
		// The run's record says where it stands and the words its turn reads,
		// before the turn is taken: a run taken up from its record reads them.
		e.orchestrator.record().Entering(ctx, e.workflowID, state.current, state.message)
		signal, output, err := e.runState(ctx, state, st, state.current)
		failed := false
		// A person's answer is for the park the walk was resumed from: the
		// state entered first, and no state after it.
		state.answer = nil
		if err == nil {
			// What a state concluded belongs with what it did. Duration, cost
			// and who ran it were already recorded; without the signal the
			// record cannot say why the run went where it went next, which is
			// the only question anyone asks of a finished walk.
			stamp(state, signal)
		}
		if err != nil {
			var nested *nestedSuspension
			if errors.As(err, &nested) {
				return nil, e.suspendNested(ctx, state, nested.inner)
			}
			// A person's answer that could not be applied refuses the resume:
			// the run is left parked as it was, not parked anew on an error.
			var refused *refusedAnswer
			if errors.As(err, &refused) {
				return nil, refused.err
			}
			// A step that could not run is a step that cannot go on: it
			// escalates like any other, with what went wrong — to its loop's
			// manager, or, in a loop with none, up to the nearest manager
			// above. Only the manager's own step, or a run with no manager
			// at all, stops the run for a person.
			word := e.escalationOf(ctx, state.current, st)
			if word == "" {
				return nil, e.suspendOnError(ctx, state, err)
			}
			signal, output = e.escalateFailure(state, word, err)
			failed = true
		}

		if state.costCeiling > 0 && e.spend(state) > state.costCeiling {
			return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_COST_CAP, nil)
		}

		// An answer the loop cannot use — a last line it cannot read, a word
		// that is not the step's, a word without what it must carry — goes back
		// to the agent that gave it, once, with the line and what is wrong
		// with it. Still unusable, it is the step's escalation, and goes where
		// the step's own escalation goes. It comes before anything is done
		// with the answer: a person at the step's gate is asked about an answer
		// the loop can use, or about the escalation.
		if st.GetAgent() != nil && !failed {
			if reason := e.unusable(st, signal, output); reason != "" {
				if !state.sentBack {
					state.sentBack = true
					state.sendBack = invalidResponse(signalText(output), reason)
					e.orchestrator.logger.Info("Answer sent back",
						zap.String("state", state.current), zap.String("reason", reason))
					state.carry = len(state.allResults) - state.entered
					continue
				}
				signal, output = e.escalateUnusable(state, output, reason)
			}
			state.sentBack = false
		}

		if gate := st.GetHitlGate(); gate != nil && !failed {
			req := buildGateRequestFor(gateRequestSubject{
				WorkflowID: e.workflowID,
				Gate:       gate,
				AgentID:    stateAgentID(st, state.current),
				StatePath:  state.current,
				Ordinal:    int(state.transitions),
				Descriptor: fmt.Sprintf("state %s", state.current),
				Output:     output,
			})
			e.orchestrator.emitProgress(WorkflowProgressEvent{
				PatternType:    "state_machine",
				WorkflowID:     e.workflowID,
				Message:        fmt.Sprintf("State %s awaiting human review", state.current),
				CurrentAgentID: stateAgentID(st, state.current),
				PartialResults: state.allResults,
				HITLRequest:    req,
			})

			decision, err := evaluateHITLGate(ctx, e.orchestrator, gate, req)
			if err != nil {
				return nil, err
			}
			if decision == nil {
				return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_GATE, req)
			}
			switch decision.Action {
			case loomv1.GateAction_GATE_ACTION_REJECT:
				return nil, &GateRejected{StageAgentID: state.current, Feedback: decision.Feedback}
			case loomv1.GateAction_GATE_ACTION_REVISE:
				e.reportStop(ctx, state)
				state.gateRevisions[state.current]++
				if state.gateRevisions[state.current] > maxGateRevisions(gate) {
					return nil, fmt.Errorf("gate on state %s exceeded max_revisions (%d)", state.current, maxGateRevisions(gate))
				}
				state.revisionFeedback = decision.Feedback
				if target := gate.GetReviseTargetStageId(); target != "" {
					if _, ok := e.pattern.GetStates()[target]; !ok {
						return nil, fmt.Errorf("gate on state %s names unknown revise target %q", state.current, target)
					}
					state.current = target
				}
				continue
			}
		}

		if st.GetTerminal() {
			return e.result(state, output, startTime), nil
		}

		// A state may set run state on its way out: the list of repos a round
		// will fan over, the task a cursor has reached. It rides the same JSON
		// line the signal does, because a state that has just worked something
		// out is the only thing that knows it.
		applyVars(state, output)

		if _, routed := st.GetRoute()[normalizeSignal(signal)]; !routed &&
			isNested(ctx) && contains(e.pattern.GetBubbleSignals(), normalizeSignal(signal)) {
			// The scope that started this run owns this decision.
			return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_BUBBLED, nil)
		}

		next, err := e.resolveTransition(ctx, state, st, state.current, signal, output)
		if err != nil && !errors.Is(err, errUnroutable) && !failed {
			// What runs on the way out — a commit after the step — failing is
			// the step failing: it escalates the same way.
			if word := e.escalationOf(ctx, state.current, st); word != "" {
				signal, output = e.escalateFailure(state, word, err)
				failed = true
				next, err = e.resolveTransition(ctx, state, st, state.current, signal, output)
			}
		}
		if err != nil {
			if errors.Is(err, errUnroutable) {
				// A stop on a word nothing routes is heard by the loop above:
				// what this step can end with goes with it, and the run's own
				// record says where it stopped and on what.
				noteEnds(state, st)
				e.orchestrator.record().StoppedOn(ctx, e.workflowID, state.current, normalizeSignal(signal), state.vars)
				return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, nil)
			}
			return nil, e.suspendOnError(ctx, state, err)
		}
		next = e.leaving(state, state.current, next, signal)
		state.last = state.current

		e.takeList(state, next, output)
		switch {
		case failed:
			state.message = e.raisedHere(state.current, st) + "\n\n" + handoff(output, "")
		case st.GetDelegate() != nil:
			// A list state's output is not a seat's reply with a signal line at
			// the end: it is the record of how its children finished, and that
			// whole record is what the next state opens with.
			state.message = output
		case st.GetAgent() != nil && normalizeSignal(signal) == EscalateSignal && e.managersWordFor(state.current) != "":
			// A step's escalation to its machine's manager says where it came
			// from before what was said, as one raised from below does.
			state.message = e.raisedHere(state.current, st)
			if said := handoff(output, e.listField(next)); said != "" {
				state.message += "\n\nIt said:\n" + said
			}
		default:
			state.message = handoff(output, e.listField(next))
		}
		var spent float64
		for _, r := range state.allResults[state.entered:] {
			if r.Cost != nil {
				spent += r.Cost.CostUsd
			}
		}
		for _, obs := range e.orchestrator.observers() {
			obs.Transitioned(ctx, e.workflowID, state.current, normalizeSignal(signal), next, state.vars, spent)
		}
		// The visit is recorded: a stop before the next state runs has spent
		// nothing of its own.
		state.entered = len(state.allResults)
		state.current = next
		state.transitions++
		if e.isLoopback(next) {
			state.visits[next]++ // 1 after the first re-entry, which is what ⟲ means
			if state.visits[next] >= state.transitionCeiling {
				e.orchestrator.logger.Info("State machine stalled",
					zap.String("state", next), zap.Int32("entries", state.visits[next]))
				return nil, e.suspend(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_MAX_TRANSITIONS, nil)
			}
		}
	}
}

// endsKey is where a step that stopped on a word nothing routes leaves what
// it can end with, on the record of its last turn.
const endsKey = "ends"

// noteEnds leaves, on the last result of the visit under way, the words the
// step can end with and where each leads.
func noteEnds(state *machineState, st *loomv1.WorkflowState) {
	if n := len(state.allResults); n > state.entered {
		last := state.allResults[n-1]
		if last.Metadata == nil {
			last.Metadata = map[string]string{}
		}
		last.Metadata[endsKey] = endsOf(st)
	}
}

// endsOf is the words a step's routes take, each with the state it leads to.
func endsOf(st *loomv1.WorkflowState) string {
	words := routeKeys(st)
	sort.Strings(words)
	ends := make([]string, 0, len(words))
	for _, word := range words {
		if next := st.GetRoute()[word].GetNextState(); next != "" {
			ends = append(ends, word+" → "+next)
		} else {
			ends = append(ends, word)
		}
	}
	return strings.Join(ends, " · ")
}

// canEndWith is the line that tells whoever hears an escalation what the step
// that raised it can end with: its own words, and the escalation that reaches
// the hearer.
func canEndWith(ends string) string {
	if ends == "" {
		return "This step can end with: " + EscalateSignal + " → you"
	}
	return "This step can end with: " + ends + " · " + EscalateSignal + " → you"
}

// raisedHere is where an escalation of one of the machine's own steps came
// from, as its manager is told: the step, what it can end with, and the
// manager's word that sends the walk back to it.
func (e *StateMachineExecutor) raisedHere(name string, st *loomv1.WorkflowState) string {
	routed := proto.Clone(st).(*loomv1.WorkflowState)
	delete(routed.Route, EscalateSignal)
	return strings.Join([]string{
		"From: " + name,
		canEndWith(endsOf(routed)),
		fmt.Sprintf("Your word `%s` sends the loop back to this step.", e.managersWordFor(name)),
	}, "\n")
}

// stamp marks the results of the visit under way with what the state
// concluded, the visit it was and the state it is.
func stamp(state *machineState, signal string) {
	for _, r := range state.allResults[state.entered:] {
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		r.Metadata["signal"] = signal
		// visits is incremented on the way into a state, so it already is
		// the visit number — except for the entry state, which nothing
		// moved into.
		visit := state.visits[state.current]
		if visit == 0 {
			visit = 1
		}
		r.Metadata["visit"] = fmt.Sprintf("%d", visit)
		r.Metadata["state_path"] = state.current
	}
}

// unusable is why the loop cannot use an agent step's answer, or "" when it
// can: the last line cannot be read, its word is not one of the step's, or the
// word came without what the loop's spec says it carries. A word of the step
// that nothing routes is usable — that is the step's own judgement, and stops
// the run where it stands.
func (e *StateMachineExecutor) unusable(st *loomv1.WorkflowState, signal, output string) string {
	word := normalizeSignal(signal)
	if _, routed := st.GetRoute()[word]; !routed {
		if allowed := stateVocab(st); len(allowed) > 0 && !contains(allowed, word) {
			return whyUnread(output)
		}
	}
	if missing := e.missingFields(signal, output); len(missing) > 0 {
		return fmt.Sprintf("%q came without %s", word, strings.Join(missing, ", "))
	}
	return ""
}

// whyUnread is what is wrong with a last line that gave no word of the step.
func whyUnread(output string) string {
	text := signalText(output)
	if text == "" {
		return "the answer is empty"
	}
	if strings.HasPrefix(text, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			return "it is not valid JSON: " + err.Error()
		}
		word, ok := payload["signal"].(string)
		if !ok {
			return `it has no "signal"`
		}
		return fmt.Sprintf("%q is not a word this step can end with", word)
	}
	return "it is not a word this step can end with"
}

// signalText is the end of an answer as the loop read it for its signal: the
// last line, or the whole JSON object that ends there.
func signalText(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	end := signalLineIndex(lines)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[payloadStart(lines, end):end+1], "\n"))
}

// invalidResponse is what an agent is told when its answer is sent back.
func invalidResponse(line, reason string) string {
	return fmt.Sprintf("Your response format or state is invalid.\nThe line: %s\nWhat is wrong with it: %s\n"+
		"Your answer stands. Send it again in the expected format.", line, reason)
}

// escalateUnusable makes an answer that is still unusable after it was sent
// back the step's escalation. The agent's answer stays in the record as it was
// given; the escalation is the engine's, recorded after it, and says what
// whoever hears it needs: the step, the line, what is wrong with it, and what
// the step said.
func (e *StateMachineExecutor) escalateUnusable(state *machineState, output, reason string) (string, string) {
	name := state.current
	said := fmt.Sprintf("%s ended twice with a response the loop could not read.\nThe line: %s\nWhat is wrong with it: %s",
		name, signalText(output), reason)
	// What the step said is above its last line; an agent that sent only the
	// line again said it in the answer that was sent back.
	words := handoff(output, "")
	for i := len(state.allResults) - 2; words == "" && i >= state.entered; i-- {
		words = handoff(state.allResults[i].GetOutput(), "")
	}
	if words != "" {
		said += "\nWhat it said: " + words
	}
	escalation := said + "\n" + `{"signal":"` + EscalateSignal + `"}`
	e.record(state, name, name, &loomv1.AgentResult{
		AgentId:         name,
		Output:          escalation,
		Metadata:        map[string]string{"state": name},
		ConfidenceScore: 1.0,
		Cost:            &loomv1.AgentExecutionCost{},
	})
	stamp(state, EscalateSignal)
	e.orchestrator.logger.Info("Unusable answer escalated",
		zap.String("state", name), zap.String("reason", reason))
	return EscalateSignal, escalation
}

// escalationOf is the word a step that could not run escalates with, or ""
// when it stops the run instead: the run was stopped, the step is the
// manager's own, or there is no manager to hear it — none in this machine and
// none above it. The word goes where the step's escalation goes: its machine's
// manager, or, unrouted, up to the nearest manager above.
func (e *StateMachineExecutor) escalationOf(ctx context.Context, name string, st *loomv1.WorkflowState) string {
	if ctx.Err() != nil || name == e.pattern.GetLogicSeat() {
		return ""
	}
	if e.managersWordFor(name) == "" && !managedAbove(ctx) {
		return ""
	}
	if st.GetDelegate() != nil {
		return EscalatedSignal
	}
	return EscalateSignal
}

// escalateFailure makes a step that could not run the step's escalation. The
// escalation is the engine's, recorded as the step's result, and says what the
// manager needs: the step, and what stopped it.
func (e *StateMachineExecutor) escalateFailure(state *machineState, word string, cause error) (string, string) {
	name := state.current
	escalation := fmt.Sprintf("%s could not run: %v", name, cause) + "\n" + `{"signal":"` + word + `"}`
	e.record(state, name, name, &loomv1.AgentResult{
		AgentId:         name,
		Output:          escalation,
		Metadata:        map[string]string{"state": name},
		ConfidenceScore: 1.0,
		Cost:            &loomv1.AgentExecutionCost{},
	})
	stamp(state, word)
	e.orchestrator.logger.Info("Step that could not run escalated",
		zap.String("state", name), zap.Error(cause))
	return word, escalation
}

// errUnroutable marks a signal the state's route does not contain. It is not a
// failure: an unroutable signal is exactly the case a machine cannot decide and
// a human or supervising agent must.
var errUnroutable = errors.New("signal is not in the state's route")

// resolveTransition walks the edge — hook, dismissals, then the move — and
// returns the state to enter next.
func (e *StateMachineExecutor) resolveTransition(ctx context.Context, state *machineState, st *loomv1.WorkflowState, from, signal, output string) (string, error) {
	tr, ok := st.GetRoute()[normalizeSignal(signal)]
	if !ok {
		e.orchestrator.logger.Info("Unroutable signal",
			zap.String("state", from),
			zap.String("signal", signal),
			zap.Strings("legal", routeKeys(st)))
		return "", fmt.Errorf("%w: state %q emitted %q, legal signals are %v", errUnroutable, from, signal, routeKeys(st))
	}

	// Order matters: the checkpoint is written before any dismissal on the same
	// edge, so a resumed run finds the sessions the checkpoint described.
	if tr.GetCheckpoint() {
		if _, err := e.buildCheckpoint(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_UNSPECIFIED, nil); err != nil {
			return "", fmt.Errorf("transition checkpoint failed: %w", err)
		}
	}

	for _, name := range tr.GetDismiss() {
		// A dismiss names a conversation, and a conversation keyed by a cursor is
		// named with it: `tester-{suite}` is one surface's tester. Read literally
		// the name matches nothing, and dismissing an unknown role is a no-op by
		// design — so the seam meant to bound a per-surface conversation never
		// ended one.
		name = interpolateRun(name, map[string]string{ParamDir: e.dir}, state.vars)
		if err := e.dismiss(ctx, name); err != nil {
			return "", err
		}
	}

	if hook := tr.GetHook(); hook != nil {
		if _, _, err := e.runTool(ctx, hook, from, normalizeSignal(signal), output, state.vars); err != nil {
			return "", err
		}
	}

	next := tr.GetNextState()
	// Where the child of the state this route enters takes up, when the
	// route says. Kept for that state's entry, which spends it.
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	delete(state.vars, enterChildKey(next))
	if at := tr.GetEnter(); at != "" {
		state.vars[enterChildKey(next)] = at
	}
	if computed := tr.GetNextTool(); computed != nil {
		resolved, _, err := e.runTool(ctx, computed, from, normalizeSignal(signal), output, state.vars)
		if err != nil {
			return "", err
		}
		next = resolved
	}

	// Host code is deterministic, but it is still not trusted to name a state
	// that exists — a dangling target should say so, not walk into a nil body.
	if _, ok := e.pattern.GetStates()[next]; !ok {
		return "", fmt.Errorf("state %q signal %q routes to unknown state %q", from, signal, next)
	}
	return next, nil
}

// dismiss says a role is not needed for now: its session leaves memory and its
// history stays in storage. The next entry into that role finds the history and
// resumes it, so a dismissal never costs a seat what it has already worked out.
func (e *StateMachineExecutor) dismiss(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if name == DismissAll {
		for _, ag := range e.orchestrator.registeredAgents() {
			for _, s := range ag.ListSessions() {
				if !strings.HasPrefix(s.ID, e.workflowID+"-") {
					continue
				}
				ag.DeleteSession(s.ID)
			}
		}
		return nil
	}
	ag, err := e.orchestrator.GetAgent(ctx, name)
	if err != nil {
		// Dismissing a role that was never entered is a no-op, not an error:
		// terminal edges routinely dismiss roles a given path never used.
		return nil
	}
	for _, s := range ag.ListSessions() {
		if strings.HasPrefix(s.ID, e.workflowID+"-"+name) {
			ag.DeleteSession(s.ID)
		}
	}
	return nil
}

// runState executes a state's body and returns its routing signal and output.
func (e *StateMachineExecutor) runState(ctx context.Context, state *machineState, st *loomv1.WorkflowState, name string) (string, string, error) {
	ctx, span := e.orchestrator.tracer.StartSpan(ctx, fmt.Sprintf("state_machine.state.%s", name))
	defer e.orchestrator.tracer.EndSpan(span)
	if span != nil {
		span.SetAttribute("state.name", name)
		span.SetAttribute("state.transition", fmt.Sprintf("%d", state.transitions))
	}

	e.orchestrator.emitProgress(WorkflowProgressEvent{
		PatternType:    "state_machine",
		WorkflowID:     e.workflowID,
		Message:        fmt.Sprintf("Entering state %s", name),
		StatePath:      []string{name},
		CurrentAgentID: stateAgentID(st, name),
		PartialResults: state.allResults,
	})

	switch body := st.GetBody().(type) {
	case *loomv1.WorkflowState_Agent:
		return e.runAgentState(ctx, state, st, body.Agent, name, span)

	case *loomv1.WorkflowState_Tool:
		return e.runToolState(ctx, state, body.Tool, name)

	case *loomv1.WorkflowState_Delegate:
		return e.runDelegateState(ctx, state, body.Delegate, name)

	default:
		if st.GetTerminal() {
			// A terminal state may simply end the run.
			return "", e.lastOutput(state), nil
		}
		return "", "", fmt.Errorf("state %q has no body and is not terminal", name)
	}
}

// stepMarker is the first line of a state's instructions when they are sent. It
// is how a later entry tells, from the conversation itself, whether this seat
// has been given this state's instructions: it survives a restart with the
// stored history, goes with a dismissed conversation, and is absent from a
// fork's copy of another seat's states. The newline ends the name, so one
// state's marker never matches another state whose name it begins.
func stepMarker(state string) string {
	return "Step " + state + "\n\n"
}

// agentDir is the directory this machine's agent states work in, as its spec
// declares it: written with the run's variables, and beneath the run's own
// directory when relative. Empty when the spec declares none. A variable the
// run does not have is an error — a directory half-named is no directory.
func (e *StateMachineExecutor) agentDir(vars map[string]string) (string, error) {
	declared := strings.TrimSpace(e.pattern.GetAgentDir())
	if declared == "" {
		return "", nil
	}
	dir := interpolateRun(declared, map[string]string{ParamDir: e.dir, ParamRun: e.workflowID}, vars)
	if strings.ContainsAny(dir, "{}") {
		return "", fmt.Errorf("agent_dir %q names a variable this run does not have", declared)
	}
	if !filepath.IsAbs(dir) && e.dir != "" {
		dir = filepath.Join(e.dir, dir)
	}
	return filepath.Clean(dir), nil
}

// seatBusyRetry is how long a step waits before trying again a seat whose
// conversation is taking a turn elsewhere.
var seatBusyRetry = 2 * time.Second

// runAgentState takes one agent turn. This is the only body that costs tokens.
func (e *StateMachineExecutor) runAgentState(ctx context.Context, state *machineState, st *loomv1.WorkflowState, b *loomv1.AgentState, name string, span *observability.Span) (string, string, error) {
	startTime := time.Now()

	ag, err := e.orchestrator.GetAgent(ctx, b.GetAgentId())
	if err != nil {
		return "", "", fmt.Errorf("state %q: %w", name, err)
	}

	sessionID := e.sessionID(b, state.vars)

	// Which prompt depends on whether this conversation has happened before,
	// in memory or in storage. Reading memory alone would make every cold
	// resume look like a first entry and silently re-mint.
	seen, err := ag.SessionExists(ctx, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("state %q: could not check session %q: %w", name, sessionID, err)
	}
	if !seen {
		if err := e.fork(ctx, ag, b, sessionID, state.vars); err != nil {
			return "", "", fmt.Errorf("state %q: %w", name, err)
		}
	}
	// A state's instructions are sent once per conversation: the first time
	// this seat enters this state. A return carries only what came back — the
	// conversation already holds the instructions, and sending them again reads
	// as an order to start the job over. "First" is per state, not per
	// conversation: one seat serves several states in one conversation, and
	// each state's instructions arrive when the seat first reaches it. A forked
	// seat's copied history names its parent's states, not this one, so its
	// first turn gets the full instructions too.
	// Every turn carries the step's instructions, with its item and the run's
	// variables named, and then the words the turn is for: a step never relies
	// on its conversation to know which job, and which item, this turn is.
	marker := stepMarker(name)
	prompt := marker + b.GetInstructions()
	// The machine's manager is not a step of the walk and is not told it
	// is one: its escalation is the park, and what that means is the
	// host's to say.
	if name != e.pattern.GetLogicSeat() {
		prompt = marker + withTheWayOut(st, b.GetInstructions())
	}
	// A prompt is told where it is working and what the run has worked out, the
	// same way a tool's arguments are. Without it an agent cannot be pointed at
	// its own directory, and every child of a set would be sent to the same
	// place — which is not a prompting mistake, it is the engine failing to say
	// where the work is.
	prompt = interpolateRun(prompt, map[string]string{
		ParamDir:     e.dir,
		ParamState:   name,
		ParamMessage: state.message,
		ParamRun:     e.workflowID,
	}, state.vars)
	if state.message != "" {
		prompt = prompt + "\n\nmessage in flight:\n" + state.message
	}
	if state.revisionFeedback != "" {
		prompt = injectRevisionFeedback(prompt, state.revisionFeedback, "human reviewer")
		state.revisionFeedback = ""
	}
	// Where this machine's agents work is its spec's to say. The turn's tools
	// start there, and the step is told the same directory — one declaration,
	// enforced and stated, so the two cannot differ.
	agentDir, err := e.agentDir(state.vars)
	if err != nil {
		return "", "", fmt.Errorf("state %q: %w", name, err)
	}
	if agentDir != "" {
		ctx = shuttle.ContextWithWorkDir(ctx, agentDir)
		prompt += "\n\nWorking folder for this step: " + agentDir
	}
	prompt += e.signalContract(st)
	// An answer sent back is the whole turn: the step has its instructions and
	// what came to it, and is told only what was wrong with what it sent.
	if state.sendBack != "" {
		prompt, state.sendBack = state.sendBack, ""
	}

	if span != nil {
		span.SetAttribute("state.body", "agent")
		span.SetAttribute("agent.id", b.GetAgentId())
		span.SetAttribute("agent.session_id", sessionID)
		span.SetAttribute("agent.prompt_kind", map[bool]string{true: "return", false: "instructions"}[seen])
	}

	// A tool the step calls can read the run's record by its id.
	ctx = WithRunID(ctx, e.workflowID)
	var response *agent.Response
	for {
		if progressCb := agent.ProgressCallbackFromContext(ctx); progressCb != nil {
			response, err = ag.ChatWithProgress(ctx, sessionID, prompt, stampedWith(progressCb, e.workflowID, name))
		} else {
			response, err = ag.Chat(ctx, sessionID, prompt)
		}
		if !errors.Is(err, agent.ErrSessionBusy) {
			break
		}
		// The seat's conversation is taking a turn elsewhere — the person
		// talking with it. Nothing is wrong, so the step waits its turn rather
		// than stopping the run.
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(seatBusyRetry):
			continue
		}
		break
	}
	// Stamp the workflow id onto the session so cost/token roll-ups can find
	// every phase-agent session of this run with one WHERE clause — a turn that
	// failed spent money too. Idempotent on subsequent visits; a stamp error is
	// logged but not fatal.
	if e.workflowID != "" {
		if err := ag.StampSessionRunID(ctx, sessionID, e.workflowID); err != nil {
			e.orchestrator.logger.Warn("stamp run_id failed; roll-ups will miss this session",
				zap.String("session_id", sessionID),
				zap.String("run_id", e.workflowID),
				zap.Error(err))
		}
	}
	if err != nil {
		// What the turn spent before it failed is this visit's too.
		var failed *agent.TurnFailed
		if errors.As(err, &failed) {
			e.record(state, name, b.GetAgentId(), &loomv1.AgentResult{
				AgentId:    b.GetAgentId(),
				Metadata:   map[string]string{"state": name, "agent_name": ag.GetName(), "failed": "true"},
				DurationMs: time.Since(startTime).Milliseconds(),
				Cost: &loomv1.AgentExecutionCost{
					TotalTokens:  types.SafeInt32(failed.Spent.TotalTokens),
					InputTokens:  types.SafeInt32(failed.Spent.InputTokens),
					OutputTokens: types.SafeInt32(failed.Spent.OutputTokens),
					CostUsd:      failed.Spent.CostUSD,
				},
			})
		}
		return "", "", fmt.Errorf("state %q agent chat failed: %w", name, err)
	}

	model := ag.GetLLMModel()
	if model != "" {
		state.modelsUsed[b.GetAgentId()] = model
	}

	meta := map[string]string{"state": name, "agent_name": ag.GetName()}
	if response.Thinking != "" {
		meta["thinking"] = response.Thinking
	}
	spent := response.TurnUsage
	if spent.TotalTokens == 0 && spent.CostUSD == 0 {
		spent = response.Usage
	}
	result := &loomv1.AgentResult{
		AgentId:         b.GetAgentId(),
		Output:          response.Content,
		Metadata:        meta,
		ConfidenceScore: 1.0,
		DurationMs:      time.Since(startTime).Milliseconds(),
		// The turn's cost is every call the seat made — the reads, the tool
		// rounds, the answer — not the last one. A response built outside the
		// conversation loop carries only its single call.
		Cost: &loomv1.AgentExecutionCost{
			TotalTokens:  types.SafeInt32(spent.TotalTokens),
			InputTokens:  types.SafeInt32(spent.InputTokens),
			OutputTokens: types.SafeInt32(spent.OutputTokens),
			CostUsd:      spent.CostUSD,
		},
	}
	e.record(state, name, b.GetAgentId(), result)
	return signalOf(response.Content), response.Content, nil
}

// forkParent is the conversation a seat continues: the one a run var names
// for its agent, else the one the spec says it thinks on from, by that seat's
// id in this run.
func (e *StateMachineExecutor) forkParent(b *loomv1.AgentState, vars map[string]string) string {
	if parent := strings.TrimSpace(vars[ForkVarPrefix+b.GetAgentId()]); parent != "" {
		return parent
	}
	if from := strings.TrimSpace(b.GetForkOf()); from != "" {
		return e.workflowID + "-" + from
	}
	return ""
}

// fork gives a seat that has never spoken in this run the memory of a
// conversation the run names for it. A parent that is named and does not exist
// is a cold start, not a failure: a lead run on a plan nobody groomed here has
// no architect to remember, and its brief is written to stand alone.
func (e *StateMachineExecutor) fork(ctx context.Context, ag *agent.Agent, b *loomv1.AgentState, sessionID string, vars map[string]string) error {
	parent := e.forkParent(b, vars)
	if parent == "" || parent == sessionID {
		return nil
	}
	agentID := b.GetAgentId()
	exists, err := ag.SessionExists(ctx, parent)
	if err != nil {
		return fmt.Errorf("could not check fork parent %q: %w", parent, err)
	}
	if !exists {
		e.orchestrator.logger.Info("fork parent absent; minting cold",
			zap.String("agent", agentID), zap.String("parent", parent))
		return nil
	}
	if err := ag.ForkSession(ctx, parent, sessionID); err != nil {
		return fmt.Errorf("fork %q from %q: %w", sessionID, parent, err)
	}
	e.orchestrator.logger.Info("forked",
		zap.String("agent", agentID), zap.String("session_id", sessionID), zap.String("parent", parent))
	return nil
}

// runToolState performs work that needs no judgement. It costs nothing and is
// reproducible, which is why a real process spends most of its states here.
func (e *StateMachineExecutor) runToolState(ctx context.Context, state *machineState, t *loomv1.ToolState, name string) (string, string, error) {
	startTime := time.Now()

	signal, output, err := e.runTool(ctx, t, name, "", state.message, state.vars)
	if err != nil {
		return "", "", err
	}

	e.record(state, name, name, &loomv1.AgentResult{
		AgentId:         name,
		Output:          output,
		Metadata:        map[string]string{"state": name, "tool": t.GetTool()},
		ConfidenceScore: 1.0,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Cost:            &loomv1.AgentExecutionCost{},
	})
	return signal, output, nil
}

// nestedSuspension carries an inner pattern's checkpoint outward so the state
// it suspended in can be recorded around it.
type nestedSuspension struct {
	state string
	inner *loomv1.WorkflowCheckpoint
}

func (n *nestedSuspension) Error() string {
	return fmt.Sprintf("nested workflow suspended in state %s", n.state)
}

// answerNested is a person's decision for a run that parked inside a child.
// The state that delegated keeps the child as it parked and is entered again:
// it takes the child up with the decision, and goes on as it does on any
// entry — the children that finished stay finished, the rest of a set runs,
// and a child that stops again is handled as a child that stops is.
func (e *StateMachineExecutor) answerNested(state *machineState, st *loomv1.WorkflowState, name string, decision *loomv1.GateDecision, inner *loomv1.WorkflowCheckpoint) error {
	if st.GetDelegate() == nil {
		return fmt.Errorf("resume: state %q does not delegate", name)
	}
	if inner == nil {
		return fmt.Errorf("resume: nested suspension in state %q has no nested checkpoint", name)
	}
	if err := e.keep(state, name, inner); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	state.answer = decision
	return nil
}

// suspendNested records the outer position around a sub-workflow's checkpoint.
func (e *StateMachineExecutor) suspendNested(ctx context.Context, state *machineState, inner *loomv1.WorkflowCheckpoint) error {
	ckpt, err := e.buildCheckpoint(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_NESTED, nil)
	if err != nil {
		return err
	}
	ckpt.NestedCheckpoint = inner
	e.orchestrator.logger.Info("State machine suspended inside a nested workflow",
		zap.String("workflow_id", e.workflowID),
		zap.String("state", state.current))
	return &WorkflowSuspended{Checkpoint: ckpt}
}

// suspendOnError stops the walk at a state that could not run, keeping
// everything before it. No run ends because a step broke: a broken step is one
// more place a person or a supervising agent takes over, and what they resume is
// this state, not the run from its beginning. A live chain reached its design
// gate fifty minutes in, hit an engine defect there, and had to be paid for again
// from the first state because the error left nothing to resume.
//
// A walk that was stopped on purpose is not an error to recover from; its
// context is done, and that is reported as it is.
func (e *StateMachineExecutor) suspendOnError(ctx context.Context, state *machineState, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	e.reportStop(ctx, state)
	ckpt, err := e.buildCheckpoint(ctx, state, loomv1.SuspendReason_SUSPEND_REASON_ERROR, nil)
	if err != nil {
		return fmt.Errorf("%w (and the state could not be checkpointed: %v)", cause, err)
	}
	ckpt.Error = cause.Error()
	e.orchestrator.logger.Warn("State machine stopped at a state that could not run",
		zap.String("workflow_id", e.workflowID),
		zap.String("state", state.current),
		zap.Error(cause))
	return &WorkflowSuspended{Checkpoint: ckpt}
}

// stampedWith is a seat's progress callback that says which loop and step
// each event came from. A step inside a child loop stamps first; the loops
// above it leave its stamp alone.
func stampedWith(cb agent.ProgressCallback, runID, state string) agent.ProgressCallback {
	return func(ev agent.ProgressEvent) {
		if ev.RunID == "" {
			ev.RunID, ev.State = runID, state
		}
		cb(ev)
	}
}

// reportStop tells the observer about a visit that ended without a
// transition: the state finished — it said what it concluded — and the walk
// stopped there, at a gate, an escalation, a ceiling, or a signal it could not
// route, or a person sent it back. A state stopped part-way — its model could
// not be reached, a child it handed work to is waiting — has not finished its
// visit: nothing is reported, and what it spent is carried into the visit's
// one record when it ends.
func (e *StateMachineExecutor) reportStop(ctx context.Context, state *machineState) {
	obs, ok := e.orchestrator.observer.(StopObserver)
	if !ok || state.entered >= len(state.allResults) || unfinished(state.allResults) > 0 {
		return
	}
	var spent float64
	for _, r := range state.allResults[state.entered:] {
		if r.Cost != nil {
			spent += r.Cost.CostUsd
		}
	}
	obs.Stopped(ctx, e.workflowID, state.current, state.allResults[len(state.allResults)-1].GetMetadata()["signal"], spent)
	state.entered = len(state.allResults)
}

// unfinished is how many results at the end of a walk's record belong to a
// visit that has not finished: a state's results are stamped with it when the
// state finishes, so the unstamped tail is what a visit stopped part-way had
// spent.
func unfinished(results []*loomv1.AgentResult) int {
	n := 0
	for i := len(results) - 1; i >= 0 && results[i].GetMetadata()["state_path"] == ""; i-- {
		n++
	}
	return n
}

// suspend builds the checkpoint and returns it as the WorkflowSuspended error
// callers of ExecutePattern already handle.
func (e *StateMachineExecutor) suspend(ctx context.Context, state *machineState, reason loomv1.SuspendReason, gate *loomv1.HITLGateRequest) error {
	e.reportStop(ctx, state)
	ckpt, err := e.buildCheckpoint(ctx, state, reason, gate)
	if err != nil {
		return err
	}
	e.orchestrator.logger.Info("State machine suspended",
		zap.String("workflow_id", e.workflowID),
		zap.String("state", state.current),
		zap.String("reason", reason.String()))
	return &WorkflowSuspended{Checkpoint: ckpt}
}

func (e *StateMachineExecutor) buildCheckpoint(ctx context.Context, state *machineState, reason loomv1.SuspendReason, gate *loomv1.HITLGateRequest) (*loomv1.WorkflowCheckpoint, error) {
	// A park keeps the words the state it stopped at was entered with: taken
	// up from the park, the state reads them again.
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	if state.message != "" {
		state.vars[messageKey] = state.message
	} else {
		delete(state.vars, messageKey)
	}
	return &loomv1.WorkflowCheckpoint{
		CheckpointVersion:  CheckpointVersion,
		WorkflowId:         e.workflowID,
		PatternType:        "state_machine",
		StatePath:          []string{state.current},
		SuspendReason:      reason,
		StageSnapshots:     state.snapshots,
		AllResults:         state.allResults,
		ModelsUsed:         state.modelsUsed,
		GateRevisionCounts: state.gateRevisions,
		PendingGate:        gate,
		GrantedCostUsd:     state.costCeiling,
		GrantedTransitions: state.transitionCeiling,
		Vars:               state.vars,
		SharedMemory:       snapshotWorkflowMemory(ctx, e.orchestrator),
		CreatedAtMs:        time.Now().UnixMilli(),
	}, nil
}

func (e *StateMachineExecutor) result(state *machineState, output string, startTime time.Time) *loomv1.WorkflowResult {
	return &loomv1.WorkflowResult{
		PatternType:  "state_machine",
		AgentResults: state.allResults,
		MergedOutput: output,
		Metadata: map[string]string{
			"terminal_state": state.current,
			"transitions":    fmt.Sprintf("%d", state.transitions),
			"last_state":     state.last,
			"vars":           varsJSON(state.vars),
		},
		DurationMs: time.Since(startTime).Milliseconds(),
		Cost:       machineCost(state.allResults),
	}
}

// NotWalkable is a machine the engine will not walk as it is defined: a
// manager that cannot be seated, a seat nobody fills, a route to a state that
// is not there. Nothing of it has run.
type NotWalkable struct{ Err error }

func (n *NotWalkable) Error() string { return n.Err.Error() }
func (n *NotWalkable) Unwrap() error { return n.Err }

// CanWalk says why a workflow cannot be walked as it is defined, without
// walking it; nil when it can. A host asks before it takes a run up, so a
// definition the engine would refuse is refused before the run is touched.
func (o *Orchestrator) CanWalk(ctx context.Context, pattern *loomv1.WorkflowPattern) error {
	sm := pattern.GetStateMachine()
	if sm == nil {
		return nil
	}
	return NewStateMachineExecutor(o, sm, pattern.GetWorkflowId()).validate(ctx)
}

// validate rejects a machine that cannot be walked, before anything runs.
func (e *StateMachineExecutor) validate(ctx context.Context) error {
	// The manager is seated here, when the machine is taken up — started or
	// resumed — and so for every machine at every depth: a child is taken up
	// the same way. The children it names are not read for it: how a named
	// child can end is read when the state that runs it is entered.
	seated, err := Seated(e.spec, nil)
	if err != nil {
		return err
	}
	e.pattern = seated
	if len(e.pattern.GetStates()) == 0 {
		return fmt.Errorf("state machine has no states")
	}
	entry := e.pattern.GetEntryState()
	if entry == "" {
		return fmt.Errorf("state machine has no entry_state")
	}
	if _, ok := e.pattern.GetStates()[entry]; !ok {
		return fmt.Errorf("entry_state %q is not a state in this machine", entry)
	}
	if err := validateToolCoverage(e.pattern, e.orchestrator.toolExecutor); err != nil {
		return err
	}

	terminals := 0
	for name, st := range e.pattern.GetStates() {
		if st.GetTerminal() {
			terminals++
		} else if st.GetBody() == nil {
			return fmt.Errorf("state %q has no body and is not terminal", name)
		}
		if b := st.GetAgent(); b != nil {
			if b.GetAgentId() == "" {
				return fmt.Errorf("state %q has an agent body with no agent_id", name)
			}
			if _, err := e.orchestrator.GetAgent(ctx, b.GetAgentId()); err != nil {
				return fmt.Errorf("state %q: %w", name, err)
			}
		}
		for signal, tr := range st.GetRoute() {
			if next := tr.GetNextState(); next != "" {
				if _, ok := e.pattern.GetStates()[next]; !ok {
					return fmt.Errorf("state %q signal %q routes to unknown state %q", name, signal, next)
				}
				if tr.GetEnter() != "" && e.pattern.GetStates()[next].GetDelegate() == nil {
					return fmt.Errorf("state %q signal %q says where a child takes up, and %q runs no child", name, signal, next)
				}
			} else if tr.GetNextTool() == nil {
				return fmt.Errorf("state %q signal %q has neither a destination nor a tool to compute one", name, signal)
			}
		}
		if gate := st.GetHitlGate(); gate != nil {
			if target := gate.GetReviseTargetStageId(); target != "" {
				if _, ok := e.pattern.GetStates()[target]; !ok {
					return fmt.Errorf("state %q gate names unknown revise target %q", name, target)
				}
			}
		}
	}
	if terminals == 0 {
		return fmt.Errorf("state machine has no terminal state — a walk could never end")
	}
	return nil
}

// isLoopback reports whether re-entering this state counts toward the stall
// bound. A machine that declares none is counted everywhere, which is the safe
// reading when nobody has said which states form its cycles.
func (e *StateMachineExecutor) isLoopback(name string) bool {
	declared := e.pattern.GetLoopbackStates()
	if len(declared) == 0 {
		return true
	}
	for _, s := range declared {
		if s == name {
			return true
		}
	}
	return false
}

func (e *StateMachineExecutor) maxTransitions() int32 {
	if n := e.pattern.GetMaxTransitions(); n > 0 {
		return n
	}
	return defaultMaxTransitions
}

// sessionID keys a session by role, and by suite when the state declares one.
// There is no positional component: a role visited from several states is one
// conversation, which is what lets a lead accumulate context across a cycle.
//
// The suffix is interpolated against the run, because its whole purpose is to
// name a cursor: `{suite}` is what makes one tester several conversations, one
// per surface. Read literally it is one conversation keyed "{suite}", which is
// the collapse it exists to prevent — and it read literally, in a live run, until
// this line.
//
// A seat the run was given a conversation for (SessionVarPrefix) takes its turns
// there instead: the conversation is named by whoever started the run, not
// derived from it.
func (e *StateMachineExecutor) sessionID(b *loomv1.AgentState, vars map[string]string) string {
	if own := strings.TrimSpace(vars[SessionVarPrefix+b.GetAgentId()]); own != "" {
		return own
	}
	id := e.workflowID + "-" + b.GetAgentId()
	suffix := interpolateRun(b.GetSessionKeySuffix(), map[string]string{ParamDir: e.dir}, vars)
	if suffix = strings.TrimSpace(suffix); suffix != "" {
		id += "-" + suffix
	}
	return id
}

func (e *StateMachineExecutor) record(state *machineState, stateName, agentID string, result *loomv1.AgentResult) {
	state.allResults = append(state.allResults, result)
	state.snapshots = append(state.snapshots, &loomv1.CheckpointStageSnapshot{
		AgentId:    agentID,
		FullOutput: result.Output,
	})
}

func (e *StateMachineExecutor) spend(state *machineState) float64 {
	var total float64
	for _, r := range state.allResults {
		if r.Cost != nil {
			total += r.Cost.CostUsd
		}
	}
	return total
}

func (e *StateMachineExecutor) lastOutput(state *machineState) string {
	if len(state.snapshots) == 0 {
		return ""
	}
	return state.snapshots[len(state.snapshots)-1].FullOutput
}

// nestedKey marks a context as running inside another machine's state. Only a
// nested run bubbles: at the top there is no owning scope to bubble to.
type nestedKey struct{}

func markNested(ctx context.Context) context.Context {
	return context.WithValue(ctx, nestedKey{}, true)
}

func isNested(ctx context.Context) bool {
	v, _ := ctx.Value(nestedKey{}).(bool)
	return v
}

// managedKey marks a context as running beneath a machine with a manager, at
// any depth: a step there that cannot go on has someone above to hear it.
type managedKey struct{}

// handDown is the context a child of this machine runs in: nested, and
// beneath a manager when this machine has one or runs beneath one itself.
func (e *StateMachineExecutor) handDown(ctx context.Context) context.Context {
	ctx = markNested(ctx)
	if e.pattern.GetLogicSeat() != "" {
		ctx = context.WithValue(ctx, managedKey{}, true)
	}
	return ctx
}

func managedAbove(ctx context.Context) bool {
	v, _ := ctx.Value(managedKey{}).(bool)
	return v
}

// applyVars keeps what a state worked out on its way out: the `vars` of the
// object it ended on.
func applyVars(state *machineState, output string) {
	payload := parseSignalPayload(output)
	raw, ok := payload["vars"].(map[string]any)
	if !ok {
		return
	}
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	for k, v := range raw {
		state.vars[k] = fmt.Sprintf("%v", v)
	}
}

// signalContract is the engine telling a seat, at the end of every turn, which
// signals this state may end on. The legal set is the spec's knowledge; left to
// a prompt's prose it is forgotten on a resume, misremembered after a long
// turn, or never written, and each of those costs a retry or a park. It rides
// last because that is where a long prompt is read most reliably.
func (e *StateMachineExecutor) signalContract(st *loomv1.WorkflowState) string {
	allowed := stateVocab(st)
	if len(allowed) == 0 {
		return ""
	}
	sort.Strings(allowed)
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n== SIGNAL CONTRACT ==\nYour reply MUST end with exactly one line: a JSON "+
		"object whose \"signal\" is one of %s. Nothing after that line. "+
		"A reply without it does not count as done.", quoted(allowed))
	for _, sig := range allowed {
		if req, ok := e.pattern.GetSignalFields()[sig]; ok && len(req.GetFields()) > 0 {
			fmt.Fprintf(&b, "\nWith \"%s\" the same object also carries: %s.", sig, quoted(req.GetFields()))
		}
	}
	return b.String()
}

func quoted(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strconv.Quote(w)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// stateVocab is the set of signals a state may legitimately emit. A state that
// declares none is taken at its route: every signal it can produce is one it
// can act on.
//
// An agent state may always escalate. A spec says the outcomes a state can
// reach; nobody can write the ones it cannot, so the way out is not the
// author's to remember — a seat with only successes to choose from answers
// with the nearest one, and a loop whose seats can only succeed goes round
// until a ceiling stops it. This is the engine's rule, applied when the state
// runs, so it holds for every machine at every depth.
func stateVocab(st *loomv1.WorkflowState) []string {
	var out []string
	if v := st.GetVocab(); len(v) > 0 {
		out = make([]string, 0, len(v)+1)
		for _, s := range v {
			out = append(out, normalizeSignal(s))
		}
	} else {
		out = routeKeys(st)
	}
	if st.GetAgent() != nil && len(out) > 0 && !contains(out, EscalateSignal) {
		out = append(out, EscalateSignal)
	}
	return out
}

// EscalateSignal is what an agent state says when none of the words it was
// given is true. A state that routes it keeps its own handling; unrouted, it
// stops the run where it stands, for whoever asked for the work.
const EscalateSignal = "escalate"

// theWayOut tells a step its own name and how to say it cannot do the work.
const theWayOut = "\n\nThis step is {loom_state}. If you cannot do the work, say so, name this step, and end with {\"signal\":\"escalate\"}."

// withTheWayOut adds the way out to a step's instructions, once.
func withTheWayOut(st *loomv1.WorkflowState, instructions string) string {
	switch {
	case !contains(stateVocab(st), EscalateSignal),
		strings.Contains(instructions, "This step is {loom_state}."):
		return instructions
	}
	return instructions + theWayOut
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// missingFields reports which of a signal's contracted payload fields the
// state's output failed to carry. A state emits its signal as a bare word or as
// a JSON object; only the JSON form can carry fields, so a bare word is missing
// all of them.
func (e *StateMachineExecutor) missingFields(signal, output string) []string {
	req, ok := e.pattern.GetSignalFields()[normalizeSignal(signal)]
	if !ok || len(req.GetFields()) == 0 {
		return nil
	}
	payload := parseSignalPayload(output)
	var missing []string
	for _, f := range req.GetFields() {
		if v, ok := payload[f]; !ok || strings.TrimSpace(fmt.Sprintf("%v", v)) == "" {
			missing = append(missing, f)
		}
	}
	return missing
}

// parseSignalPayload reads the JSON object a state may end with. Anything else
// carries no fields.
// SignalPayload is the object a state ended on, for callers outside the engine
// that need the fields it carries — a bounce's `problem`, a park's reason.
func SignalPayload(output string) map[string]any { return parseSignalPayload(output) }

func parseSignalPayload(output string) map[string]any {
	if payload := parseJSONObject(signalLine(output)); payload != nil {
		return payload
	}
	// A payload with a list or a nested object in it does not fit on one line,
	// and a model asked for one JSON object writes it out pretty-printed — so the
	// last line is "}" or "]}" and reading only that line finds no signal at all.
	// The state then parks as unroutable, which reads as a worker that answered
	// wrongly rather than an engine that read one line of an answer.
	return parseTrailingJSONObject(output)
}

// parseTrailingJSONObject reads a JSON object that ENDS at the output's last
// meaningful line, however many lines it spans. It grows the window upwards from
// that line and takes the first complete object, which is the innermost one that
// closes there — the signal's own object rather than some document that happens
// to enclose it.
func parseTrailingJSONObject(output string) map[string]any {
	lines := strings.Split(output, "\n")
	end := signalLineIndex(lines)
	if end < 0 || !strings.HasSuffix(strings.TrimSpace(lines[end]), "}") {
		return nil
	}
	// A signal object is small. Bounding the window keeps a long document with a
	// brace at the end from being parsed as one, and keeps the cost fixed.
	const maxSpan = 200
	for start := end; start >= 0 && end-start < maxSpan; start-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[start]), "{") {
			continue
		}
		if payload := parseJSONObject(strings.Join(lines[start:end+1], "\n")); payload != nil {
			return payload
		}
	}
	return nil
}

// payloadStart is the first line of the JSON object that ends at `end`, or `end`
// itself when the signal is a bare word or a one-line object.
func payloadStart(lines []string, end int) int {
	if end < 0 || !strings.HasSuffix(strings.TrimSpace(lines[end]), "}") {
		return end
	}
	const maxSpan = 200
	for start := end; start >= 0 && end-start < maxSpan; start-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[start]), "{") {
			continue
		}
		if parseJSONObject(strings.Join(lines[start:end+1], "\n")) != nil {
			return start
		}
	}
	return end
}

func parseJSONObject(s string) map[string]any {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		return nil
	}
	return payload
}

// signalLine is the line carrying the routing signal: the last line that is
// neither blank nor a code fence.
//
// The fence is the whole reason this is not simply the last line. A state's
// contract is to end with its signal, and a model asked for one JSON object
// routinely wraps it in ```…``` — which made the closing fence the signal, so
// the payload never parsed and the machine routed on "```". Nothing in a loop's
// vocabulary matches that, so the state parked as unroutable and the run looked
// like a worker that had answered wrongly rather than an engine that had read
// the wrong line.
func signalLine(output string) string {
	lines := strings.Split(output, "\n")
	if i := signalLineIndex(lines); i >= 0 {
		return strings.TrimSpace(lines[i])
	}
	return ""
}

// signalLineIndex is signalLine as a position, so the handoff can be everything
// above it without the two disagreeing about where the signal starts.
func signalLineIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || isCodeFence(t) {
			continue
		}
		return i
	}
	return -1
}

// isCodeFence reports a line that is only a fence — ``` or ~~~, with or without
// a language tag. A line that merely contains one is prose and is left alone.
func isCodeFence(t string) bool {
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return false
	}
	return !strings.ContainsAny(strings.TrimLeft(t, "`~"), " \t")
}

// handoff is everything above the signal line: the prose a state hands to
// whichever state runs next.
func handoff(output, listField string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	i := signalLineIndex(lines)
	if i < 0 {
		return ""
	}
	// A pretty-printed payload spans several lines, and the signal is the whole
	// object rather than the "}" it ends on. Handing the object's own first lines
	// forward would give the next state half a JSON document as its brief.
	i = payloadStart(lines, i)
	// Everything above the signal, less the fence that was opened to hold it —
	// left in, the handoff ends on a dangling ``` that the next state reads as
	// content.
	above := lines[:i]
	for len(above) > 0 {
		t := strings.TrimSpace(above[len(above)-1])
		if t == "" || isCodeFence(t) {
			above = above[:len(above)-1]
			continue
		}
		break
	}
	message := strings.TrimSpace(strings.Join(above, "\n"))
	// A signal object carries more than its routing word. A reviewer asked to end
	// on {"signal":"deltas","deltas":[…]} puts its findings exactly there, and a
	// handoff that stops at the prose tells the next seat "four fixes are still
	// outstanding" and none of the four. What routes the walk (signal) and what
	// the run remembers (vars) are the engine's; everything else in the object
	// was written for whoever acts next.
	if carried := carriedFields(output, listField); carried != "" {
		if message != "" {
			message += "\n\n"
		}
		message += carried
	}
	return message
}

// carriedFields renders the fields of a state's signal object that are content
// rather than control.
func carriedFields(output, listField string) string {
	payload := parseSignalPayload(output)
	delete(payload, "signal")
	delete(payload, "vars")
	if listField != "" {
		// The list a seat hands the engine is the engine's; the next seat reads
		// how the walk over it ended, not the list itself.
		delete(payload, listField)
	}
	if len(payload) == 0 {
		return ""
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(body)
}

// lastNonEmptyLine is the routing signal convention: a state ends its output
// with a single routing word. Prose above it is the handoff content and is
// never parsed.
func signalOf(output string) string {
	if payload := parseSignalPayload(output); payload != nil {
		if sig, ok := payload["signal"].(string); ok {
			return sig
		}
	}
	return signalLine(output)
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// normalizeSignal folds the trivial differences between what a model writes
// and what a route table declares.
func normalizeSignal(s string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), ".!"))
}

func routeKeys(st *loomv1.WorkflowState) []string {
	keys := make([]string, 0, len(st.GetRoute()))
	for k := range st.GetRoute() {
		keys = append(keys, k)
	}
	return keys
}

func stateAgentID(st *loomv1.WorkflowState, fallback string) string {
	if b := st.GetAgent(); b != nil {
		return b.GetAgentId()
	}
	return fallback
}

func machineCost(results []*loomv1.AgentResult) *loomv1.WorkflowCost {
	cost := &loomv1.WorkflowCost{AgentCostsUsd: make(map[string]float64)}
	for _, r := range results {
		if r.Cost == nil {
			continue
		}
		cost.TotalCostUsd += r.Cost.CostUsd
		cost.TotalTokens += r.Cost.TotalTokens
		if r.Cost.TotalTokens > 0 {
			cost.LlmCalls++
		}
		cost.AgentCostsUsd[r.AgentId] += r.Cost.CostUsd
	}
	return cost
}

// nestedSignal is what a finished sub-workflow reports to the state that ran
// it. A nested machine reports the terminal state it reached — "delivered",
// "blocked" — which is the outcome the outer graph routes on. Routing on the
// inner worker's last line instead would leak the sub-workflow's private
// vocabulary into its caller's route table, so every change inside the nested
// machine would silently become a change to the machine that calls it.
//
// Other pattern types have no terminal state, so their merged output is all
// there is.
func nestedSignal(sub *loomv1.WorkflowResult) string {
	if terminal := sub.GetMetadata()["terminal_state"]; terminal != "" {
		return terminal
	}
	return lastNonEmptyLine(sub.MergedOutput)
}

// sinceResume is what a child spent after it was resumed: its whole spend
// less what it had spent when it stopped. Nil when that is nothing.
func sinceResume(whole, before *loomv1.AgentExecutionCost) *loomv1.AgentExecutionCost {
	if whole == nil {
		return nil
	}
	c := &loomv1.AgentExecutionCost{CostUsd: whole.CostUsd, TotalTokens: whole.TotalTokens}
	if before != nil {
		c.CostUsd -= before.CostUsd
		c.TotalTokens -= before.TotalTokens
	}
	if c.CostUsd <= 0 && c.TotalTokens <= 0 {
		return nil
	}
	return c
}

func nestedCost(sub *loomv1.WorkflowResult) *loomv1.AgentExecutionCost {
	if sub.Cost == nil {
		return &loomv1.AgentExecutionCost{}
	}
	return &loomv1.AgentExecutionCost{
		TotalTokens: sub.Cost.TotalTokens,
		CostUsd:     sub.Cost.TotalCostUsd,
	}
}

// leaving is the move out of a state, as the run's record must show it, and
// the state it goes to. A state that runs a child and reported that child's
// stop is entered with the answer when the walk comes back to it straight
// from the state its report went to. A state a manager sends the walk to,
// while another state waits on its answer, returns the walk to that state
// when it ends done.
func (e *StateMachineExecutor) leaving(state *machineState, from, next, signal string) string {
	if state.vars == nil {
		state.vars = map[string]string{}
	}
	if back := state.vars[returnKey(from)]; back != "" {
		delete(state.vars, returnKey(from))
		if normalizeSignal(signal) == "done" && next != back {
			if _, ok := e.pattern.GetStates()[back]; ok {
				next = back
			}
		}
	}
	states := e.pattern.GetStates()
	if states[next].GetDelegate() != nil && state.vars[askedKey(next)] != "" {
		if judge := states[next].GetRoute()[EscalatedSignal].GetNextState(); judge != "" && judge == from {
			state.vars[answeringKey(next)] = "1"
		}
	}
	if seat := e.pattern.GetLogicSeat(); seat != "" && from == seat && states[next].GetDelegate() != nil {
		for name := range states {
			if name != next && state.vars[askedKey(name)] != "" {
				state.vars[returnKey(next)] = name
				break
			}
		}
	}
	return next
}
